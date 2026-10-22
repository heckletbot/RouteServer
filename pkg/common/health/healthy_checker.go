/*
 * Copyright 2026 The vLLM-Cloud Authors.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package health

import (
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"infer-dp-scheduler/pkg/common/constant"
	"infer-dp-scheduler/pkg/common/utils"

	"infer-dp-scheduler/pkg/common/client"
	"infer-dp-scheduler/pkg/common/config"
	instance_metrics "infer-dp-scheduler/pkg/common/instance-metrics"
	"infer-dp-scheduler/pkg/common/logger"
)

type HealthyCheckerModule struct {
	availableCfg            *config.RestHealthConfig
	unavailableCfg          *config.RestHealthConfig
	doHealthCheck           sync.Mutex
	initInstancesEventsChan chan []*client.Instance
	leaderHealthChecker     *LeaderHealthChecker
	MaxConcurrency          int
	BatchSize               int
	Interval                time.Duration
	closeChan               chan interface{}
	probeInferRequestMu     sync.RWMutex
	probeInferRequest       map[string]struct{}
}

func NewHealthyChecker(checker *LeaderHealthChecker) *HealthyCheckerModule {
	return &HealthyCheckerModule{
		availableCfg:            &checker.cfg.AvailableCheckersCfg.RestHealthCfg,
		unavailableCfg:          &checker.cfg.UnavailableCheckersCfg.RestHealthCfg,
		initInstancesEventsChan: make(chan []*client.Instance, 1000),
		leaderHealthChecker:     checker,
		MaxConcurrency:          checker.cfg.RestHealthCommonConfig.MaxConcurrency,
		BatchSize:               checker.cfg.RestHealthCommonConfig.BatchSize,
		Interval:                time.Duration(checker.cfg.RestHealthCommonConfig.Interval) * time.Millisecond,
		closeChan:               make(chan interface{}),
		probeInferRequest:       make(map[string]struct{}),
	}
}

func (hcm *HealthyCheckerModule) healthCheckLoop() {
	healthCheckInterval := hcm.Interval
	ticker := time.NewTicker(healthCheckInterval) // Check interval
	defer ticker.Stop()

	for {
		if !hcm.leaderHealthChecker.isMeLeaderFunc() {
			time.Sleep(LeaderCheckInternal)
			continue
		}
		select {
		case <-hcm.closeChan:
			logger.Log().Infof("healthy checker stopped")
			return
		case <-ticker.C:
			if !hcm.availableCfg.Enable {
				return
			}
			logger.Log().Trace("instance manager checking health check")
			// This instance theoretically needs to get the latest information
			hcm.doActiveHealthCheck(hcm.leaderHealthChecker.GetAllInstance())
		case event, ok := <-hcm.initInstancesEventsChan:
			if !ok {
				return
			}
			hcm.doActiveHealthCheck(event)
		}
	}
}

func (hcm *HealthyCheckerModule) probeInferReqBeforeToAvailable(toAvailable []*client.Instance) {
	if len(toAvailable) == 0 {
		return
	}
	for _, instance := range toAvailable {
		go func(instance *client.Instance) {
			// Conditions for directly sending success events without sending probe requests:
			// 1. Instance status is not fault
			// 2. Instance role is not in the list that needs to handle requests
			if instance.Stats.GetStatus() != instance_metrics.Faulty || !utils.InMap(instance.Stats.GetRole(), constant.NeedHandleReqRoleMap) {
				logger.Log().Debugf("[recover] instance %s status is not faulty or this role need not to probe, skip probe process, role: %s status: %v",
					instance.ID, instance.Stats.GetRole(), instance.Stats.GetStatus())
				instanceEvents := &LeaderSubCheckModuleEvent{
					EventType:  GetHealthSuccess,
					EventMsg:   GetHealthSuccess,
					InstanceId: instance.ID,
				}
				hcm.leaderHealthChecker.OnSubCheckModuleEvent(singleEventToEventSlice(instanceEvents))
				return
			}

			if !hcm.addInstProbeInferReq(instance.ID) {
				// If the instance is already being probed, return directly
				logger.Log().Warnf("[recover] instance %s already in probe process", instance.ID)
				return
			}
			// Send probe request
			logger.Log().Infof("[recover] begin send probe infer request to instance %s", instance.ID)
			successToCheck, testSuccess, _ := hcm.leaderHealthChecker.probeInferRequestModel.sendProbeInferRequestWithTimeout(
				instance, "recover")
			if successToCheck && testSuccess {
				instanceEvents := &LeaderSubCheckModuleEvent{
					EventType:  GetHealthSuccess,
					EventMsg:   GetHealthSuccess,
					InstanceId: instance.ID,
				}
				hcm.leaderHealthChecker.OnSubCheckModuleEvent(singleEventToEventSlice(instanceEvents))
			} else {
				// Reset success count on probe failure
				logger.Log().Warnf("[recover] send probe infer request to instance %s error, "+
					"reset ConsecutiveHealthSuccess to 0", instance.ID)
				atomic.StoreInt32(&instance.ConsecutiveHealthSuccess, 0)
			}
			// Remove instance from probeInferRequest after probe completion
			hcm.rmInstProbeInferReq(instance.ID)
		}(instance)
	}
}

func (hcm *HealthyCheckerModule) addInstProbeInferReq(instanceId string) bool {
	hcm.probeInferRequestMu.Lock()
	defer hcm.probeInferRequestMu.Unlock()
	_, exist := hcm.probeInferRequest[instanceId]
	if exist {
		return false
	}
	hcm.probeInferRequest[instanceId] = struct{}{}
	return true
}

func (hcm *HealthyCheckerModule) rmInstProbeInferReq(instanceId string) {
	hcm.probeInferRequestMu.Lock()
	defer hcm.probeInferRequestMu.Unlock()
	delete(hcm.probeInferRequest, instanceId)
}

// doHealthCheck performs a health check on all instances managed by the InstanceManager.
// It updates the availability status of instances
func (hcm *HealthyCheckerModule) doActiveHealthCheck(allInstance []*client.Instance) {
	// First need to acquire lock
	hcm.doHealthCheck.Lock()
	defer hcm.doHealthCheck.Unlock()
	var toAvailable []*client.Instance
	var toUnavailable []*client.Instance

	toAvailable, toUnavailable = hcm.runBatch(allInstance)

	// Theoretically, all recorded here are
	hasChanges := len(toUnavailable) > 0 || len(toAvailable) > 0
	toUnavailableEvents := make([]*LeaderSubCheckModuleEvent, 0)
	toAvailableEvents := make([]*LeaderSubCheckModuleEvent, 0)
	if hasChanges {
		// Need to call onInstanceHealthChangesFunc method here
		for _, instance := range toUnavailable {
			instanceEvents := &LeaderSubCheckModuleEvent{
				EventType:  GetHealthFailure,
				EventMsg:   instance.FaultMessage,
				InstanceId: instance.ID,
			}
			toUnavailableEvents = append(toUnavailableEvents, instanceEvents)
		}
		hcm.leaderHealthChecker.OnSubCheckModuleEvent(toUnavailableEvents)
		// If instance recovery probe request switch is enabled, send probe request first
		if hcm.unavailableCfg.EnableProbeInferRequest {
			hcm.probeInferReqBeforeToAvailable(toAvailable)
		} else {
			for _, instance := range toAvailable {
				instanceEvents := &LeaderSubCheckModuleEvent{
					EventType:  GetHealthSuccess,
					EventMsg:   GetHealthSuccess,
					InstanceId: instance.ID,
				}
				toAvailableEvents = append(toAvailableEvents, instanceEvents)
			}
			hcm.leaderHealthChecker.OnSubCheckModuleEvent(toAvailableEvents)
		}
	}
}

// runBatch perform batch health check, recording unhealthy instances in available into toUnavailable,
// and healthy instances in unavailable into toAvailable.
func (hcm *HealthyCheckerModule) runBatch(allInstance []*client.Instance) ([]*client.Instance, []*client.Instance) {
	batches := hcm.createBatches(allInstance)
	logger.Log().Tracef("start health check with %v batches", len(batches))
	var (
		toAvailable   []*client.Instance
		toUnavailable []*client.Instance
		resMutex      sync.Mutex
	)
	checkConcurrency := hcm.MaxConcurrency
	sem := make(chan struct{}, checkConcurrency)
	var wg sync.WaitGroup

	for _, batch := range batches {
		wg.Add(1)
		sem <- struct{}{}

		go func(batch []*client.Instance) {
			defer wg.Done()
			defer func() { <-sem }()
			toAvailableBatch, toUnavailableBatch := hcm.checkBatch(batch)
			resMutex.Lock()
			toAvailable = append(toAvailable, toAvailableBatch...)
			toUnavailable = append(toUnavailable, toUnavailableBatch...)
			resMutex.Unlock()
		}(batch)
	}
	// wait for all batch returned.
	wg.Wait()
	return toAvailable, toUnavailable
}

func (hcm *HealthyCheckerModule) createBatches(nodes []*client.Instance) [][]*client.Instance {
	checkBatchSize := hcm.BatchSize
	batches := make([][]*client.Instance, 0, (len(nodes)+checkBatchSize-1)/checkBatchSize)

	for i := 0; i < len(nodes); i += checkBatchSize {
		end := i + checkBatchSize
		if end > len(nodes) {
			end = len(nodes)
		}
		batches = append(batches, nodes[i:end])
	}
	return batches
}

func (hcm *HealthyCheckerModule) checkBatch(batch []*client.Instance) (toAvailable, toUnavailable []*client.Instance) {
	for _, instance := range batch {
		if instance.Client == nil {
			logger.Log().Warnf("instance %s client is nil", instance.ID)
			continue
		}
		healthy, statusCode, err := instance.Client.HealthCheck()
		status := instance.Stats.GetStatus()
		if instance.Stats == nil {
			logger.Log().Warnf("instance %s Stats is nil", instance.ID)
			continue
		}
		if healthy {
			hcm.tryToGetModelName(instance)
			toAvailable = hcm.instanceHealthSuccess(instance, status, toAvailable)
		} else {
			toUnavailable = hcm.instanceHealthFailed(instance, statusCode, status, err, toUnavailable)
		}
		if status == instance_metrics.Faulty {
			newCount := atomic.AddInt32(&instance.HealthCheckCountAfterUnhealthy, 1)
			logger.Log().Infof("instance %s is unhealthy, health check count increased by %d",
				instance.Client.GetEndpoint(), newCount)
		}
	}
	return toAvailable, toUnavailable
}

func (hcm *HealthyCheckerModule) tryToGetModelName(instance *client.Instance) {
	if hcm.leaderHealthChecker.probeInferRequestModel.getModelName() != "" {
		return
	}
	name, err := instance.Client.ModelName()
	if err != nil {
		logger.Log().Errorf("instance %s get model name failed, err = %v", instance.Client.GetEndpoint(), err)
		return
	}
	hcm.leaderHealthChecker.probeInferRequestModel.setModelName(name)
}

func (hcm *HealthyCheckerModule) instanceHealthSuccess(instance *client.Instance, status instance_metrics.InstanceStatus,
	toAvailable []*client.Instance) []*client.Instance {
	instance.ConsecutiveHealthFailed = 0
	instance.ConsecutiveHealthUnreachable = 0
	if status != instance_metrics.Available {
		atomic.AddInt32(&instance.ConsecutiveHealthSuccess, 1)
		if hcm.shouldMoveToHealthy(instance) {
			toAvailable = append(toAvailable, instance)
		}
	}
	return toAvailable
}

// FatalStatusCodes status code where should fail directly for health check
var FatalStatusCodes = map[int]interface{}{
	http.StatusInternalServerError: nil,
	http.StatusServiceUnavailable:  nil,
}

func (hcm *HealthyCheckerModule) instanceHealthFailed(instance *client.Instance, statusCode int, status instance_metrics.InstanceStatus, err error, toUnavailable []*client.Instance) []*client.Instance {
	if !hcm.availableCfg.Enable {
		logger.Log().Warnf("Ignoring instance [%s] active health check failure "+
			"due to health-check-enable-active-failure is set to false: err = %v", instance.Client.GetEndpoint(), err)
		return toUnavailable
	}
	atomic.StoreInt32(&instance.ConsecutiveHealthSuccess, 0)
	instance.ConsecutiveHealthFailed++
	if statusCode == 0 {
		instance.ConsecutiveHealthUnreachable++
	} else {
		instance.ConsecutiveHealthUnreachable = 0
	}
	if hcm.availableCfg.FailOn500 {
		_, isFatalStatusCode := FatalStatusCodes[statusCode]
		if isFatalStatusCode && instance.Stats.GetStatus() != instance_metrics.Faulty {
			if err != nil {
				instance.FaultMessage = err.Error()
			}
			logger.Log().Warnf("instance %s /health check failed with 500 status code, instance down directly, last err: %v",
				instance.Client.GetEndpoint(), instance.LastError)
			status = instance_metrics.Faulty
			toUnavailable = append(toUnavailable, instance)
			instance.LastError = err
			return toUnavailable
		}
	}
	if hcm.shouldMoveToUnhealthy(instance) {
		instance.FaultMessage = fmt.Sprintf("%d times health request failed, last error: %v", instance.ConsecutiveHealthFailed, instance.LastError)
		toUnavailable = append(toUnavailable, instance)
	}
	if instance.Stats.GetStatus() == instance_metrics.Init {
		if err != nil {
			instance.FaultMessage = err.Error()
		}
	}
	instance.LastError = err

	return toUnavailable
}

func (hcm *HealthyCheckerModule) shouldMoveToHealthy(instance *client.Instance) bool {
	if instance.Stats.GetStatus() == instance_metrics.Init {
		return true
	}
	if instance.Stats.GetStatus() == instance_metrics.Faulty {
		if !hcm.unavailableCfg.Enable {
			return false
		}
		if instance.ConsecutiveHealthSuccess < int32(hcm.unavailableCfg.ConsecutiveTime) {
			logger.Log().Infof(
				"faulty instance [%s] consecutive health check success [%v] time, "+
					"will try to move to available after [%v] consecutive success",
				instance.Client.GetEndpoint(), instance.ConsecutiveHealthSuccess, hcm.unavailableCfg.ConsecutiveTime)
			return false
		} else {
			logger.Log().Infof(
				"faulty instance [%s] consecutive health check success [%v] time, "+
					"reach [%v] consecutive success, try to move to available (need to send prob request if configured)",
				instance.Client.GetEndpoint(), instance.ConsecutiveHealthSuccess, hcm.unavailableCfg.ConsecutiveTime)
			return true
		}
	}
	return false
}

func (hcm *HealthyCheckerModule) shouldMoveToUnhealthy(instance *client.Instance) bool {
	if instance.Stats.GetStatus() == instance_metrics.Init {
		logger.Log().Tracef("instance [%s] /health check failed [%v] times, "+
			"status is %s, last err = %v,",
			instance.Client.GetEndpoint(), instance.ConsecutiveHealthFailed, instance.Stats.GetStatus(), instance.LastError)
		return false
	}
	if instance.Stats.GetStatus() == instance_metrics.Available {
		if instance.ConsecutiveHealthFailed < int32(hcm.availableCfg.ConsecutiveTime) {
			logger.Log().Infof("instance [%s] /health check failed [%v] times, "+
				"will be move to unavailable after [%v] consecutive failures, last err = %v",
				instance.Client.GetEndpoint(), instance.ConsecutiveHealthFailed, hcm.availableCfg.ConsecutiveTime, instance.LastError)
			return false
		} else {
			logger.Log().Warnf("instance [%s] /health check failed [%v] times, "+
				"reach [%v] consecutive failures, move to unavailable status",
				instance.Client.GetEndpoint(), instance.ConsecutiveHealthFailed, hcm.availableCfg.ConsecutiveTime)
			return true
		}
	}
	return false
}

func (hcm *HealthyCheckerModule) OnAllInstanceChanged(initInstance []*client.Instance) {
	if len(initInstance) == 0 {
		return
	}
	logger.Log().Infof("healthy checker send healthy request to init instances")
	hcm.initInstancesEventsChan <- initInstance
}

func (hcm *HealthyCheckerModule) Stop() {
	close(hcm.closeChan)
	close(hcm.initInstancesEventsChan)
}
