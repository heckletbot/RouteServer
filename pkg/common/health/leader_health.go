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
	"sync"
	"time"

	"infer-dp-scheduler/pkg/common/constant"
	"infer-dp-scheduler/pkg/common/utils"

	"github.com/gin-gonic/gin"
	"infer-dp-scheduler/pkg/common/client"
	"infer-dp-scheduler/pkg/common/config"
	"infer-dp-scheduler/pkg/common/errs"
	instance_metrics "infer-dp-scheduler/pkg/common/instance-metrics"
	"infer-dp-scheduler/pkg/common/logger"
)

type LeaderEventListener interface {
	OnRequestEvents(workerId string, requestEvents []*utils.RequestEventMsg)

	OnInstanceTokenEvent(workerId string, instanceEvents []*utils.InstanceEventMsg)
	OnAllInstancesRenewed(available, unavailable map[string]*client.Instance)
}

type LeaderHealthChecker struct {
	// receive event from sub check modules
	subCheckerEventsChan    chan []*LeaderSubCheckModuleEvent
	initInstancesEventsChan chan []*client.Instance
	cfg                     *config.HealthCheckConfig
	// mu is the lock for controlling allInstance access
	mu           sync.RWMutex
	allInstances []*client.Instance
	// checkerMu is the lock for controlling updates to healthInstance and unhealthyInstance
	checkerMu                   sync.RWMutex
	healthInstance              map[string]*client.Instance
	healthyPrefillNum           int
	healthyDecodeNum            int
	unhealthyInstance           map[string]*client.Instance
	onInstanceHealthChangesFunc func([]*client.Instance, []*client.Instance) error
	healthyCheckerModule        *HealthyCheckerModule
	failureCheckerModule        *FailureCheckerModule
	linkDownCheckerModule       *LinkdownCheckerModule
	hangCheckerModule           *LeaderHangDecider
	linkBlacklistCheckerModule  *LinkBlacklistCheckerModule
	probeInferRequestModel      *ProbeInferRequest
	modelName                   string
	modelNameMu                 sync.RWMutex
	closeChan                   chan struct{}

	isCandidate    bool
	isMeLeaderFunc func() bool
}

type LeaderSubCheckModuleEvent struct {
	EventType  string // 0: GET /health success, 1 GET /health failure, 2 request failure, 3 hang
	EventMsg   string
	InstanceId string
}

type EventType string

const (
	GetHealthSuccess = "health_check_success"
	GetHealthFailure = "health_check_failure"
	RequestFailure   = "request_failure"
	InstanceHang     = "instance_hang"
	LinkBlacklist    = "link_blacklist"
)

func NewLeaderHealthChecker(cfg *config.HealthCheckConfig, onInstanceHealthChangesFunc func(moveToAvailable []*client.Instance, moveToUnavailable []*client.Instance) error,
	testInstance func(ctx *gin.Context, path string, isChat bool, instance *client.Instance) (successToCheck bool, testSuccess bool, errDetails *errs.RSError),
	isCandidate bool, pdLinkBlacklistManager *PDLinkBlacklistManager,
	isMeLeaderFunc func() bool) *LeaderHealthChecker {
	lhc := &LeaderHealthChecker{}
	lhc.isCandidate = isCandidate
	lhc.isMeLeaderFunc = isMeLeaderFunc
	lhc.cfg = cfg
	lhc.onInstanceHealthChangesFunc = onInstanceHealthChangesFunc
	lhc.allInstances = make([]*client.Instance, 0)
	lhc.subCheckerEventsChan = make(chan []*LeaderSubCheckModuleEvent, 1000)
	lhc.initInstancesEventsChan = make(chan []*client.Instance, 1000)
	lhc.healthInstance = make(map[string]*client.Instance)
	lhc.unhealthyInstance = make(map[string]*client.Instance)
	lhc.closeChan = make(chan struct{})
	lhc.mu = sync.RWMutex{}
	lhc.checkerMu = sync.RWMutex{}
	healthyCheckerModule := NewHealthyChecker(lhc)
	failureChecker := NewFailureChecker(lhc)
	hangChecker := NewLeaderHangDecider(lhc)
	linkDownChecker := NewLinkDownChecker(lhc)
	linkBlacklistCheckerModule := NewLinkBlacklistChecker(lhc, pdLinkBlacklistManager)
	probeInferRequestModel := NewProbeInferRequest(cfg, testInstance)
	lhc.probeInferRequestModel = probeInferRequestModel
	// Inject the probe module into the Hang checker
	hangChecker.SetProbeInferRequest(probeInferRequestModel)
	lhc.addHealthyModule(healthyCheckerModule)
	lhc.addFailureModule(failureChecker)
	lhc.addHangModule(hangChecker)
	lhc.addLinkDownModule(linkDownChecker)
	lhc.addLinkBlackListModule(linkBlacklistCheckerModule)
	return lhc
}

func (lhc *LeaderHealthChecker) Init() {
	if !lhc.isCandidate {
		logger.Log().Infof("Local node is not a candidate, do not init health check module")
		return
	}

	go lhc.healthyCheckerModule.healthCheckLoop()
	go lhc.failureCheckerModule.runRequestEventLoop()
	go lhc.failureCheckerModule.runSendFakeRequestLoop()
	go lhc.hangCheckerModule.runHangCheckerLoop()
	go lhc.linkDownCheckerModule.runRequestEventLoop()
	go lhc.linkBlacklistCheckerModule.runLinkBlacklistLoop()
	go lhc.runLeaderHealthDecider()
	logger.Log().Infof("Leader health checker module started")
}

func (lhc *LeaderHealthChecker) addHealthyModule(healthyCheckerModule *HealthyCheckerModule) {
	lhc.healthyCheckerModule = healthyCheckerModule
}

func (lhc *LeaderHealthChecker) addFailureModule(failureCheckerModule *FailureCheckerModule) {
	lhc.failureCheckerModule = failureCheckerModule
}

func (lhc *LeaderHealthChecker) addLinkDownModule(linkDownChecker *LinkdownCheckerModule) {
	lhc.linkDownCheckerModule = linkDownChecker
}

func (lhc *LeaderHealthChecker) addLinkBlackListModule(linkBlacklistCheckerModule *LinkBlacklistCheckerModule) {
	lhc.linkBlacklistCheckerModule = linkBlacklistCheckerModule
}

func (lhc *LeaderHealthChecker) addHangModule(hangCheckerModule *LeaderHangDecider) {
	lhc.hangCheckerModule = hangCheckerModule
}

// called by instances_managers, should copy allInstances if needed
func (lhc *LeaderHealthChecker) OnAllInstancesRenewed(available, unavailable map[string]*client.Instance) {
	lhc.mu.Lock()
	lhc.checkerMu.Lock()

	lhc.healthInstance = make(map[string]*client.Instance)
	lhc.healthyPrefillNum = 0
	lhc.healthyDecodeNum = 0
	lhc.unhealthyInstance = make(map[string]*client.Instance)
	lhc.allInstances = make([]*client.Instance, 0, len(available)+len(unavailable))
	for _, instance := range available {
		if instance.Stats.GetStatus().String() != constant.RunningStatus {
			logger.Log().Errorf("receiving renewed available instance [%s] that should be running, but it's state is [%s]",
				instance.ID, instance.Stats.GetStatus().String())
			instance.Stats.SetStatus(instance_metrics.Available)
		}
		lhc.addHealthyInstance(instance)
		lhc.allInstances = append(lhc.allInstances, instance)
	}
	for _, instance := range unavailable {
		if instance.Stats.GetStatus().String() == constant.RunningStatus {
			logger.Log().Errorf(
				"receiving renewed unavailable instance [%s] that should not be in running, but it's state is [%s]",
				instance.ID, instance.Stats.GetStatus().String())
			instance.Stats.SetStatus(instance_metrics.Faulty)
			instance.FaultMessage = "renewed as unavailable"
		}
		lhc.unhealthyInstance[instance.ID] = instance
		lhc.allInstances = append(lhc.allInstances, instance)
	}
	lhc.mu.Unlock()
	lhc.checkerMu.Unlock()

	lhc.initInstancesEventsChan <- lhc.allInstances
	lhc.hangCheckerModule.OnAllInstancesRenewed(available, unavailable)
	lhc.failureCheckerModule.OnAllInstancesRenewed(available, unavailable)
	lhc.linkDownCheckerModule.OnAllInstancesRenewed(available, unavailable)
	lhc.linkBlacklistCheckerModule.OnAllInstancesRenewed(available, unavailable)
}

func (lhc *LeaderHealthChecker) OnRequestEvents(workerId string, requestEvents []*utils.RequestEventMsg) {
	nonTestingEvents := make([]*utils.RequestEventMsg, 0, len(requestEvents))
	for _, event := range requestEvents {
		if !event.InternalTestingReq {
			nonTestingEvents = append(nonTestingEvents, event)
		}
	}
	lhc.failureCheckerModule.OnRequestEvents(workerId, nonTestingEvents)
	lhc.linkDownCheckerModule.OnRequestEvents(workerId, nonTestingEvents)
	lhc.hangCheckerModule.OnRequestEvents(workerId, nonTestingEvents)
	lhc.linkBlacklistCheckerModule.OnRequestEvents(workerId, nonTestingEvents)
}

func (lhc *LeaderHealthChecker) OnInstanceTokenEvent(workerId string, instanceEvents []*utils.InstanceEventMsg) {
	lhc.hangCheckerModule.OnInstanceTokenEvent(workerId, instanceEvents)
}

func (lhc *LeaderHealthChecker) OnSubCheckModuleEvent(instanceEvents []*LeaderSubCheckModuleEvent) {
	lhc.subCheckerEventsChan <- instanceEvents
}

func (lhc *LeaderHealthChecker) GetAllInstance() []*client.Instance {
	lhc.mu.RLock()
	defer lhc.mu.RUnlock()
	return lhc.allInstances
}

// GetInstanceById retrieves instance object by instance ID in a thread-safe manner
func (lhc *LeaderHealthChecker) GetInstanceById(instanceId string) *client.Instance {
	lhc.checkerMu.RLock()
	defer lhc.checkerMu.RUnlock()
	if instance, ok := lhc.healthInstance[instanceId]; ok {
		return instance
	}
	if instance, ok := lhc.unhealthyInstance[instanceId]; ok {
		return instance
	}
	return nil
}

func (lhc *LeaderHealthChecker) isInstanceHealthy(instanceId string) bool {
	lhc.checkerMu.RLock()
	defer lhc.checkerMu.RUnlock()
	// Check if instance is in the healthy instance list
	if _, ok := lhc.healthInstance[instanceId]; ok {
		return true
	}
	return false
}

func (lhc *LeaderHealthChecker) GetHealthyInstanceNum() (int, int) {
	lhc.checkerMu.RLock()
	defer lhc.checkerMu.RUnlock()
	return lhc.healthyPrefillNum, lhc.healthyDecodeNum
}

// Theoretically need to return all instance information here, including available and unavailable
func (lhc *LeaderHealthChecker) runLeaderHealthDecider() {
	for {
		if !lhc.isMeLeaderFunc() {
			time.Sleep(LeaderCheckInternal)
			continue
		}
		select {
		case <-lhc.closeChan:
			logger.Log().Infof("leader health checker stopped")
			return
		case event, ok := <-lhc.subCheckerEventsChan:
			if !ok {
				return
			}
			lhc.handleSubModuleEvent(event)
		case event, ok := <-lhc.initInstancesEventsChan:
			if !ok {
				return
			}
			lhc.handleInitInstanceEvent(event)
		}
	}
}

func (lhc *LeaderHealthChecker) moveToAvailableInstance(instanceId string, msg string) *client.Instance {
	lhc.checkerMu.Lock()
	defer lhc.checkerMu.Unlock()
	// Already in healthy instances, no change needed. Return directly.
	if _, exist := lhc.healthInstance[instanceId]; exist {
		return nil
	}
	// If instance was originally in unHealthMap, need to remove it
	instance, exist := lhc.unhealthyInstance[instanceId]
	if !exist {
		return nil
	}
	instance.Stats.SetStatus(instance_metrics.Available)
	instance.FaultMessage = msg
	lhc.addHealthyInstance(instance)
	logger.Log().Infof("move instance %s to healthy because %s", instance.Client.GetEndpoint(), msg)
	delete(lhc.unhealthyInstance, instanceId)
	return instance
}

func (lhc *LeaderHealthChecker) moveToUnAvailableInstance(instanceId string, msg string, eventType string) *client.Instance {
	lhc.checkerMu.Lock()
	defer lhc.checkerMu.Unlock()
	var instance *client.Instance
	// Already in unhealthy instances, no change needed. Return directly.
	if unhealthyInstance, exist := lhc.unhealthyInstance[instanceId]; exist {
		if unhealthyInstance.Stats.GetStatus() != instance_metrics.Init {
			return nil
		}
		// Instance with init status encountering 500 error needs status change, sync to instance manager
		instance = unhealthyInstance
	}
	if instance == nil {
		unhealthyInstance, exist := lhc.healthInstance[instanceId]
		if !exist {
			return nil
		}
		instance = unhealthyInstance
	}

	instance.Stats.SetStatus(instance_metrics.Faulty)
	instance.FaultMessage = msg
	instance.FaultType = eventType
	lhc.unhealthyInstance[instanceId] = instance
	logger.Log().Warnf("move instance %s to unhealthy because %s", instance.Client.GetEndpoint(), msg)
	lhc.removeHealthyInstance(instance)
	return instance

}

func (lhc *LeaderHealthChecker) addHealthyInstance(instance *client.Instance) {
	lhc.healthInstance[instance.ID] = instance
	if instance.Stats.GetRole() == constant.Prefill {
		lhc.healthyPrefillNum++
	} else if instance.Stats.GetRole() == constant.Decode {
		lhc.healthyDecodeNum++
	}
}

func (lhc *LeaderHealthChecker) removeHealthyInstance(instance *client.Instance) {
	delete(lhc.healthInstance, instance.ID)
	if instance.Stats.GetRole() == constant.Prefill {
		lhc.healthyPrefillNum--
	} else if instance.Stats.GetRole() == constant.Decode {
		lhc.healthyDecodeNum--
	}
}

func (lhc *LeaderHealthChecker) handleSubModuleEvent(events []*LeaderSubCheckModuleEvent) {
	hasChanges := false
	if len(events) == 0 {
		return
	}
	for _, event := range events {
		if event.EventType == GetHealthSuccess {
			instance := lhc.moveToAvailableInstance(event.InstanceId, event.EventMsg)
			if instance != nil {
				hasChanges = true
			}
		} else {
			instance := lhc.moveToUnAvailableInstance(event.InstanceId, event.EventMsg, event.EventType)
			if instance != nil {
				hasChanges = true
			}
		}
	}
	if !hasChanges {
		return
	}
	lhc.checkerMu.RLock()
	healthyInstances := mapValuesToSlice(lhc.healthInstance)
	unhealthyInstances := mapValuesToSlice(lhc.unhealthyInstance)
	lhc.checkerMu.RUnlock()
	err := lhc.onInstanceHealthChangesFunc(healthyInstances, unhealthyInstances)
	if err != nil {
		logger.Log().Errorf("fail to change instance manager func, error: %v", err.Error())
		return
	}
}

func (lhc *LeaderHealthChecker) handleLeaderModelEvent() {}

func (lhc *LeaderHealthChecker) handleInitInstanceEvent(allInstance []*client.Instance) {
	// Recently initialized instances also need to be added to the map
	// Prevent missing init status instances when calling onInstanceHealthChangesFunc method
	lhc.checkerMu.Lock()
	defer lhc.checkerMu.Unlock()
	initInstance := make([]*client.Instance, 0)
	for _, instance := range allInstance {
		if instance.Stats == nil {
			continue
		}
		if instance.Stats.GetStatus() == instance_metrics.Init {
			lhc.unhealthyInstance[instance.ID] = instance
			logger.Log().Warnf("move instance %s to unhealthy because it's status is %s", instance.Client.GetEndpoint(), instance_metrics.Init)
			initInstance = append(initInstance, instance)
		}
	}
	// Need to actively trigger healthy_checker module to initiate health check
	// Prevent impact on instance join timing due to long health check intervals in scaling scenarios
	lhc.healthyCheckerModule.OnAllInstanceChanged(initInstance)
}

func (lhc *LeaderHealthChecker) findInstanceWithStatus(instanceId string) (*client.Instance, bool) {
	lhc.checkerMu.RLock()
	defer lhc.checkerMu.RUnlock()
	instance, exist := lhc.healthInstance[instanceId]
	if exist {
		return instance, true
	}
	instance, exist = lhc.unhealthyInstance[instanceId]
	if exist {
		return instance, false
	}
	return nil, false
}

func mapValuesToSlice[K comparable, V any](m map[K]V) []V {
	values := make([]V, 0, len(m))

	for _, value := range m {
		values = append(values, value)
	}

	return values
}

func (lhc *LeaderHealthChecker) Stop() {
	if lhc.closeChan == nil {
		return
	}
	close(lhc.closeChan)
	lhc.healthyCheckerModule.Stop()
	lhc.failureCheckerModule.Stop()
	lhc.hangCheckerModule.Stop()
	lhc.linkDownCheckerModule.Stop()
	lhc.linkBlacklistCheckerModule.Stop()
}

type InstanceHealthFailureEvent struct {
	InstanceId string
	Type       string
}

type InstanceHealthSuccessEvent struct {
}
