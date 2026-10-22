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
	"time"

	"infer-dp-scheduler/pkg/common/client"
	"infer-dp-scheduler/pkg/common/config"
	"infer-dp-scheduler/pkg/common/constant"
	"infer-dp-scheduler/pkg/common/errs"
	"infer-dp-scheduler/pkg/common/logger"
	"infer-dp-scheduler/pkg/common/utils"
)

const (
	Timeout       = "request timeout failure"
	ConnectErr    = "request connection error"
	KVTransferErr = "request kv transfer error"
)

type FailureCheckerModule struct {
	cfg                  *config.RequestFailuresConfig
	statusCodes          map[int]struct{}
	subCheckerEventsChan chan []*utils.RequestEventMsg
	failureInstanceChan  chan *LeaderSubCheckModuleEvent

	mu            sync.RWMutex
	instancesInfo map[string]*InstanceFailureInfo

	fakeRequestMu          sync.RWMutex
	fakeRequestSent        map[string]struct{}
	leaderHealthChecker    *LeaderHealthChecker
	closeChan              chan struct{}
	isLinkDownModuleEnable bool
}

type InstanceFailureInfo struct {
	InstanceName    string
	LastFailureTime time.Time
	FailureEvent    []*FailureTimestamps
}

type FailureTimestamps struct {
	FailureTimestamp time.Time
	ErrorCode        int
	IsDownDirectly   bool
}

func NewFailureChecker(checker *LeaderHealthChecker) *FailureCheckerModule {
	requestFailuresCfg := checker.cfg.AvailableCheckersCfg.RequestFailuresCfg
	statusCodes := make(map[int]struct{})
	if len(requestFailuresCfg.FailureConditionsConfig.StatusCodes) != 0 {
		for _, code := range requestFailuresCfg.FailureConditionsConfig.StatusCodes {
			statusCodes[code] = struct{}{}
		}
	}
	return &FailureCheckerModule{
		cfg:                    &requestFailuresCfg,
		isLinkDownModuleEnable: checker.cfg.AvailableCheckersCfg.LinkDownCfg.Enable || checker.cfg.AvailableCheckersCfg.LinkBlacklistConfig.Enable,
		statusCodes:            statusCodes,
		subCheckerEventsChan:   make(chan []*utils.RequestEventMsg, 1000),
		failureInstanceChan:    make(chan *LeaderSubCheckModuleEvent, 1000),
		instancesInfo:          make(map[string]*InstanceFailureInfo),
		fakeRequestSent:        make(map[string]struct{}),
		leaderHealthChecker:    checker,
		closeChan:              make(chan struct{}),
	}
}

// OnAllInstancesRenewed called by instances_managers, should copy allInstances if needed
// This includes both healthy and unhealthy instances
func (fcm *FailureCheckerModule) OnAllInstancesRenewed(available, unavailable map[string]*client.Instance) {
	fcm.mu.Lock()
	defer fcm.mu.Unlock()
	instanceInfo := make(map[string]*InstanceFailureInfo)
	for _, info := range fcm.instancesInfo {
		_, existedInAvailable := available[info.InstanceName]
		_, existedInUnavailable := unavailable[info.InstanceName]
		if existedInAvailable || existedInUnavailable {
			instanceInfo[info.InstanceName] = info
		}
	}
	fcm.instancesInfo = instanceInfo
}

func (fcm *FailureCheckerModule) OnRequestEvents(workerId string, workerBootId string, requestEvents []*utils.RequestEventMsg) {
	if !fcm.cfg.Enable {
		return
	}
	fcm.subCheckerEventsChan <- requestEvents
}

func (fcm *FailureCheckerModule) OnInstanceTokenEvent(workerId string, instanceEvents []*utils.InstanceEventMsg) {
}

// Determine failure issues
func (fcm *FailureCheckerModule) runRequestEventLoop() {
	if !fcm.cfg.Enable {
		return
	}
	for {
		if !fcm.leaderHealthChecker.isMeLeaderFunc() {
			time.Sleep(LeaderCheckInternal)
			continue
		}
		select {
		case <-fcm.closeChan:
			logger.Log().Infof("failure checker stopped")
			return
		case event, ok := <-fcm.subCheckerEventsChan:
			if !ok {
				return
			}
			for _, msg := range event {
				if utils.InSlice(msg.EventType, constant.ReqSuccessEvents) {
					if fcm.cfg.ConsecutiveFailure {
						fcm.handleSuccess(msg)
					}
				}
				if utils.InSlice(msg.EventType, constant.ReqFailedEvents) {
					fcm.handleFailure(msg)
				}
			}
		}
	}
}

func (fcm *FailureCheckerModule) runSendFakeRequestLoop() {
	for {
		select {
		case <-fcm.leaderHealthChecker.closeChan:
			return
		case event, ok := <-fcm.failureInstanceChan:
			if !ok {
				return
			}
			if event.EventType != RequestFailure {
				return
			}
			instanceID := event.InstanceId
			instance, isHealthy := fcm.leaderHealthChecker.findInstanceWithStatus(instanceID)
			if instance == nil || !isHealthy {
				return
			}
			if !fcm.cfg.FakeRequest.Enable {
				// If virtual request functionality is disabled, directly return instance as unavailable
				logger.Log().Infof("send fake request funcation is not enable, instance %s availabe to leader module", instance.Client.GetEndpoint())
				fcm.leaderHealthChecker.OnSubCheckModuleEvent(singleEventToEventSlice(event))
				continue
			}
			fcm.checkInstanceWithFakeReq(event, instance)
		}
	}
}

func (fcm *FailureCheckerModule) handleSuccess(msg *utils.RequestEventMsg) {
	fcm.mu.Lock()
	defer fcm.mu.Unlock()
	info, exists := fcm.instancesInfo[msg.TargetInstance]
	if !exists {
		return
	}
	// If this exists (indicating the instance has failure records), reset when a successful record is received
	info.FailureEvent = make([]*FailureTimestamps, 0)
}

// If a P/D instance continuously experiences n failed request events within this time period, the instance may be considered abnormal;
func (fcm *FailureCheckerModule) handleFailure(msg *utils.RequestEventMsg) {
	if fcm.isLinkDownModuleEnable && msg.FailureDetails.IsKVTransferErr {
		// If it's a linkdown error and the linkdown module is enabled, the failure module ignores this error
		return
	}
	timeout := fcm.cfg.FailureConditionsConfig.RequestTimeout
	if !timeout && msg.FailureDetails.IsTimeout {
		// Configuration treats timeout requests as non-failure requests
		return
	}
	if !msg.FailureDetails.IsConnectionErr && !msg.FailureDetails.IsTimeout {
		// No timeout + connection failure must have a status code
		if _, exists := fcm.statusCodes[msg.FailureDetails.StatusCode]; !exists {
			// Only return codes present in status codes are considered failed requests
			return
		}
	}

	instanceId := msg.TargetInstance
	instance, isHealthy := fcm.leaderHealthChecker.findInstanceWithStatus(instanceId)
	if instance == nil || !isHealthy {
		return
	}

	isDownDirectEvent := msg.FailureDetails.IsTimeout || msg.FailureDetails.IsConnectionErr || msg.FailureDetails.IsKVTransferErr
	fcm.mu.Lock()
	defer fcm.mu.Unlock()
	recordFailureEvent(fcm.instancesInfo, fcm.cfg, instanceId, msg.FailureDetails.StatusCode, isDownDirectEvent)
	// Analyze the instance's error information, first determine if the number of error requests within the time window meets expectations
	// If expectations are met and there are direct down errors, send events directly to the main module
	info := fcm.instancesInfo[instanceId]
	isHealthy, isDownDirectly := fcm.analyzeInstanceFailure(info)
	logger.Log().Warnf("Instance %s detects %d failures in last %v ms, status: isHealthy %v, isDownDirectly: %v",
		instance.Client.GetEndpoint(), len(info.FailureEvent), fcm.cfg.TimeWindow, isHealthy, isDownDirectly)
	if isHealthy {
		return
	}
	instanceEvents := &LeaderSubCheckModuleEvent{
		EventType:  RequestFailure,
		EventMsg:   getFailureEventMsg(msg.FailureDetails),
		InstanceId: instanceId,
	}
	if isDownDirectly {
		logger.Log().Errorf("Instance %s encountered a fatal failure, send event to leader", instance.Client.GetEndpoint())
		fcm.leaderHealthChecker.OnSubCheckModuleEvent(singleEventToEventSlice(instanceEvents))
		return
	}
	fcm.failureInstanceChan <- instanceEvents
}

func recordFailureEvent(instancesInfo map[string]*InstanceFailureInfo, cfg *config.RequestFailuresConfig, instanceId string, errorCode int, isDownDirectly bool) {
	info, exists := instancesInfo[instanceId]
	if !exists {
		// Create error information
		info = &InstanceFailureInfo{
			InstanceName: instanceId,
			FailureEvent: make([]*FailureTimestamps, 0),
		}
		instancesInfo[instanceId] = info
	}
	now := time.Now()
	failureTimeStamp := &FailureTimestamps{
		FailureTimestamp: now,
		ErrorCode:        errorCode,
		IsDownDirectly:   isDownDirectly,
	}
	info.FailureEvent = append(info.FailureEvent, failureTimeStamp)
	cutoffTime := now.Add(-time.Duration(cfg.TimeWindow) * time.Millisecond)
	i := 0
	for i < len(info.FailureEvent) && info.FailureEvent[i].FailureTimestamp.Before(cutoffTime) {
		i++
	}
	info.FailureEvent = info.FailureEvent[i:]
}

func getFailureEventMsg(details *utils.InstanceFailureDetails) string {
	if details.IsTimeout {
		return Timeout
	}
	if details.IsConnectionErr {
		return ConnectErr
	}
	if details.IsKVTransferErr {
		return KVTransferErr
	}
	return RequestFailure
}

func (fcm *FailureCheckerModule) analyzeInstanceFailure(instanceFailureInfo *InstanceFailureInfo) (bool, bool) {
	if len(instanceFailureInfo.FailureEvent) < fcm.cfg.MaxFailuresTime {
		return true, false
	}
	for _, timestamps := range instanceFailureInfo.FailureEvent {
		if timestamps.IsDownDirectly {
			return false, true
		}
	}
	return false, false
}

func (fcm *FailureCheckerModule) checkInstanceWithFakeReq(failureEvent *LeaderSubCheckModuleEvent, instance *client.Instance) {
	go func(ins *client.Instance) {
		// Prevent duplicate sending, if already sent before, end directly
		if fcm.addInsFakeRequest(ins.ID) {
			isAlive, failMessage := fcm.sendFakeRequestTestIsAlive(ins)
			if !isAlive {
				failureEvent.EventMsg = failMessage
				fcm.leaderHealthChecker.OnSubCheckModuleEvent(singleEventToEventSlice(failureEvent))
			}
			// If it's alive, need to notify
			fcm.rmInsFakeRequest(ins.ID)
		}
	}(instance)
}

func (fcm *FailureCheckerModule) addInsFakeRequest(instanceId string) bool {
	fcm.fakeRequestMu.Lock()
	defer fcm.fakeRequestMu.Unlock()
	_, exist := fcm.fakeRequestSent[instanceId]
	if exist {
		return false
	}
	fcm.fakeRequestSent[instanceId] = struct{}{}
	return true
}

func (fcm *FailureCheckerModule) rmInsFakeRequest(instanceId string) {
	fcm.fakeRequestMu.Lock()
	defer fcm.fakeRequestMu.Unlock()
	delete(fcm.fakeRequestSent, instanceId)
}

func (fcm *FailureCheckerModule) isInternalError(errDetails *errs.RSError) bool {
	// Determine if instance returns 500 error
	if errDetails == nil {
		return false
	}

	isInternalError := false
	if errDetails.EngineErr != nil {
		isInternalError = errDetails.EngineErr.EngineStatus == http.StatusInternalServerError
	} else if errDetails.EngineStreamErr != nil {
		isInternalError = errDetails.EngineStreamErr.EngineStatus == http.StatusInternalServerError
	}
	return isInternalError
}

func (fcm *FailureCheckerModule) isInstanceInternalError(instance *client.Instance, errDetails *errs.RSError) bool {
	if errDetails == nil {
		return false
	}
	// Return true if the detected instance returns 500 error
	if roleErrStage, exists := constant.FailureRoleStageMap[instance.Stats.GetRole()]; exists {
		if errDetails.ErrorStage == roleErrStage {
			if fcm.isInternalError(errDetails) {
				return true
			}
		}
	} else {
		logger.Log().Warnf("Unknown role %s", instance.Stats.GetRole())
	}
	return false
}

func (fcm *FailureCheckerModule) sendFakeRequestTestIsAlive(instance *client.Instance) (bool, string) {
	request := fcm.cfg.FakeRequest
	interval := request.SendInterval
	maxSendTime := request.MaxSendTime
	endpoint := instance.Client.GetEndpoint()
	logger.Log().Infof("leader health module is going to send fake request to %s", endpoint)

	// Track consecutive failure count
	consecutiveFailures := 0
	// Create ticker
	ticker := time.NewTicker(time.Duration(interval) * time.Millisecond)
	defer ticker.Stop()

	// Track current attempt count
	attempts := 0
	// Loop execution until maximum attempts are reached
	for attempts < maxSendTime {
		attempts++
		// Execute check
		logger.Log().Infof("lead health module send fake request %d/%d to instance %s", attempts, maxSendTime, endpoint)

		successToCheck, testSuccess, errDetails := fcm.leaderHealthChecker.probeInferRequestModel.sendProbeInferRequestWithTimeout(
			instance, "failure")
		if successToCheck && testSuccess {
			return true, fmt.Sprintf("fake request success")
		}
		if fcm.isInstanceInternalError(instance, errDetails) {
			// If the detected instance returns 500 error, increment consecutiveFailures
			consecutiveFailures++
		}
		if consecutiveFailures == request.FailureThreshold {
			break
		}

		// If not the last attempt, wait for the next check
		if attempts < maxSendTime {
			<-ticker.C
		}
	}

	// Virtual request conclusion log
	logger.Log().Warnf("instance %s fake request failed consecutive %d times on instnace", endpoint, consecutiveFailures)
	if consecutiveFailures >= request.FailureThreshold {
		return false, fmt.Sprintf("%d consecutive fake request failed in %d ms", consecutiveFailures, interval*attempts)
	}
	return true, fmt.Sprintf("fake request failed %d times", consecutiveFailures)
}

func (fcm *FailureCheckerModule) Stop() {
	close(fcm.closeChan)
}

func singleEventToEventSlice(singleEvent *LeaderSubCheckModuleEvent) []*LeaderSubCheckModuleEvent {
	events := make([]*LeaderSubCheckModuleEvent, 0)
	events = append(events, singleEvent)
	return events
}
