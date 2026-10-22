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

	"infer-dp-scheduler/pkg/common/client"
	"infer-dp-scheduler/pkg/common/config"
	"infer-dp-scheduler/pkg/common/logger"
	"infer-dp-scheduler/pkg/common/utils"
)

type LinkdownCheckerModule struct {
	cfg                  *config.RequestFailuresConfig
	subCheckerEventsChan chan []*utils.RequestEventMsg
	failureInstanceChan  chan *LeaderSubCheckModuleEvent
	mu                   sync.RWMutex
	instancesInfo        map[string]*InstanceFailureInfo
	leaderHealthChecker  *LeaderHealthChecker
	closeChan            chan interface{}
}

func NewLinkDownChecker(checker *LeaderHealthChecker) *LinkdownCheckerModule {
	linkDownCfg := &checker.cfg.AvailableCheckersCfg.LinkDownCfg
	linkDownChecker := &LinkdownCheckerModule{
		leaderHealthChecker:  checker,
		cfg:                  linkDownCfg,
		subCheckerEventsChan: make(chan []*utils.RequestEventMsg, 1000),
		closeChan:            make(chan interface{}),
		instancesInfo:        make(map[string]*InstanceFailureInfo),
	}
	return linkDownChecker
}

func (lkm *LinkdownCheckerModule) runRequestEventLoop() {
	if !lkm.cfg.Enable {
		return
	}
	for {
		if !lkm.leaderHealthChecker.isMeLeaderFunc() {
			time.Sleep(LeaderCheckInternal)
			continue
		}
		select {
		case <-lkm.closeChan:
			logger.Log().Infof("linkdown checker stopped")
			return
		case event, ok := <-lkm.subCheckerEventsChan:
			if !ok {
				return
			}
			for _, msg := range event {
				if msg.FailureDetails != nil && msg.FailureDetails.IsKVTransferErr {
					lkm.handleLinkDownEvent(msg)
				}
			}
		}
	}
}

func (lkm *LinkdownCheckerModule) handleLinkDownEvent(msg *utils.RequestEventMsg) {
	linkDownInstanceId := msg.PreInstance
	if linkDownInstanceId == "" {
		// Link down only occurs on decode errors, pre instance should not be empty
		return
	}
	linkDownInstance, isInstanceHealthy := lkm.leaderHealthChecker.findInstanceWithStatus(linkDownInstanceId)
	if linkDownInstance == nil || !isInstanceHealthy {
		return
	}
	instanceId := msg.TargetInstance
	instance, isHealthy := lkm.leaderHealthChecker.findInstanceWithStatus(instanceId)
	if instance == nil || !isHealthy {
		return
	}
	lkm.mu.Lock()
	defer lkm.mu.Unlock()
	recordFailureEvent(lkm.instancesInfo, lkm.cfg, linkDownInstanceId, msg.FailureDetails.StatusCode, true)
	info := lkm.instancesInfo[linkDownInstanceId]
	logger.Log().Warnf("Instance %s (role: %v)  detects a link down failure event, previous instance %s(role: %v) detects %d link down failures in last %v ms",
		instance.Client.GetEndpoint(), instance.Stats.GetRole(), linkDownInstance.Client.GetEndpoint(), linkDownInstance.Stats.GetRole(), len(info.FailureEvent), lkm.cfg.TimeWindow)
	if len(info.FailureEvent) < lkm.cfg.MaxFailuresTime {
		return
	}
	instanceEvents := &LeaderSubCheckModuleEvent{
		EventType:  RequestFailure,
		EventMsg:   getFailureEventMsg(msg.FailureDetails),
		InstanceId: linkDownInstanceId,
	}
	logger.Log().Errorf("Instance %s(role: %v) has experienced %d linkdown events in last %d ms and it will be moved to the unhealthy state.",
		linkDownInstance.Client.GetEndpoint(), linkDownInstance.Stats.GetRole(), len(info.FailureEvent), lkm.cfg.TimeWindow)
	lkm.leaderHealthChecker.OnSubCheckModuleEvent(singleEventToEventSlice(instanceEvents))
}

func (lkm *LinkdownCheckerModule) OnRequestEvents(workerId string, workerBootId string, requestEvents []*utils.RequestEventMsg) {
	if !lkm.cfg.Enable {
		return
	}
	lkm.subCheckerEventsChan <- requestEvents
}

func (lkm *LinkdownCheckerModule) OnInstanceTokenEvent(workerId string, instanceEvents []*utils.InstanceEventMsg) {
}

func (lkm *LinkdownCheckerModule) OnAllInstancesRenewed(available, unavailable map[string]*client.Instance) {
	lkm.mu.Lock()
	defer lkm.mu.Unlock()
	instanceInfo := make(map[string]*InstanceFailureInfo)
	for _, info := range lkm.instancesInfo {
		_, existedInAvailable := available[info.InstanceName]
		_, existedInUnavailable := unavailable[info.InstanceName]
		if existedInAvailable || existedInUnavailable {
			instanceInfo[info.InstanceName] = info
		}
	}
	lkm.instancesInfo = instanceInfo
}

func (lkm *LinkdownCheckerModule) Stop() {
	close(lkm.closeChan)
}
