/*
 * Copyright 2026 The RouteServer Authors.
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

	"route-server/pkg/common/config"
	"route-server/pkg/common/logger"
	"route-server/pkg/common/utils"
)

type blackInfo struct {
	blockTime time.Time
	role      string
}
type FallbackHealthChecker struct {
	mu                sync.RWMutex
	fallbackCfg       *config.FallbackHealthCheckConfig
	fcm               *FailureCheckerModule
	instancesInfo     map[string]*InstanceFailureInfo
	fallbackBlacklist map[string]blackInfo
	stopChan          chan struct{}
}

func NewFallbackHealthChecker(cfg *config.HealthCheckConfig) *FallbackHealthChecker {
	fhc := &FallbackHealthChecker{
		fallbackCfg:       &cfg.FallbackHealthCheckConfig,
		fcm:               NewFallbackFailureChecker(cfg),
		instancesInfo:     make(map[string]*InstanceFailureInfo),
		fallbackBlacklist: make(map[string]blackInfo),
		stopChan:          make(chan struct{}),
	}
	go fhc.startRemoveBlackListLoop()
	return fhc
}

func NewFallbackFailureChecker(cfg *config.HealthCheckConfig) *FailureCheckerModule {
	requestFailuresCfg := cfg.AvailableCheckersCfg.RequestFailuresCfg
	statusCodes := make(map[int]struct{})
	if len(requestFailuresCfg.FailureConditionsConfig.StatusCodes) != 0 {
		for _, code := range requestFailuresCfg.FailureConditionsConfig.StatusCodes {
			statusCodes[code] = struct{}{}
		}
	}
	return &FailureCheckerModule{
		cfg:                    &requestFailuresCfg,
		isLinkDownModuleEnable: cfg.AvailableCheckersCfg.LinkDownCfg.Enable || cfg.AvailableCheckersCfg.LinkBlacklistConfig.Enable,
		statusCodes:            statusCodes,
	}
}

func (f *FallbackHealthChecker) startRemoveBlackListLoop() {
	ticker := time.NewTicker(time.Duration(f.fallbackCfg.ResumeCheckIntervalMs) * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-f.stopChan:
			return
		case <-ticker.C:
			f.mu.Lock()
			keyToRemove := make([]string, 0, len(f.fallbackBlacklist))
			for instanceId, blackInfo := range f.fallbackBlacklist {
				if time.Since(blackInfo.blockTime) > time.Duration(f.fallbackCfg.ResumeTimeMs)*time.Millisecond {
					keyToRemove = append(keyToRemove, instanceId)
				}
			}

			for _, key := range keyToRemove {
				delete(f.fallbackBlacklist, key)
				delete(f.instancesInfo, key)
				logger.Log().Infof("[fallback health check] Instance %s is resumed from the blacklist after %v ms", key, f.fallbackCfg.ResumeTimeMs)
			}
			f.mu.Unlock()
		}

	}
}

// HandleFallbackFailure If a P/D instance consecutively experiences n fail_req_event within this time period, the instance is considered potentially abnormal;
func (f *FallbackHealthChecker) HandleFallbackFailure(msg *utils.RequestEventMsg) {
	if f.fcm.isLinkDownModuleEnable && msg.FailureDetails.IsKVTransferErr {
		// If it's a linkdown error and the linkdown module is enabled, the failure module ignores this type of error
		return
	}
	timeout := f.fcm.cfg.FailureConditionsConfig.RequestTimeout
	if !timeout && msg.FailureDetails.IsTimeout {
		// Configuration considers timeout requests as non-failure requests
		return
	}
	if !msg.FailureDetails.IsConnectionErr && !msg.FailureDetails.IsTimeout {
		// If there's no timeout + connection failure, there must be a status code
		if _, exists := f.fcm.statusCodes[msg.FailureDetails.StatusCode]; !exists {
			// Only requests with return codes present in the status code list are considered failed requests
			return
		}
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	instanceId := msg.TargetInstance
	isDownDirectEvent := msg.FailureDetails.IsTimeout || msg.FailureDetails.IsConnectionErr || msg.FailureDetails.IsKVTransferErr
	recordFailureEvent(f.instancesInfo, f.fcm.cfg, instanceId, msg.FailureDetails.StatusCode, isDownDirectEvent)
	// Analyze the instance's error information, first determine if the number of error requests within the time window meets the expected threshold
	// If the threshold is reached and there are direct down errors, send events directly to the main module
	info := f.instancesInfo[instanceId]
	isHealthy, _ := f.fcm.analyzeInstanceFailure(info)
	if isHealthy {
		return
	}

	b := blackInfo{
		blockTime: time.Now(),
		role:      msg.Request.GetType(),
	}
	f.fallbackBlacklist[instanceId] = b
	logger.Log().Errorf("[fallback health check] Instance %s detects %d failures in last %v ms, added to the blacklist",
		instanceId, len(info.FailureEvent), f.fcm.cfg.TimeWindow)
}

func (f *FallbackHealthChecker) MergeBlacklist(role string, insBlackMap map[string]interface{}) map[string]interface{} {
	f.mu.RLock()
	defer f.mu.RUnlock()

	if insBlackMap == nil {
		insBlackMap = make(map[string]interface{}, len(f.fallbackBlacklist))
	}

	for k, v := range f.fallbackBlacklist {
		if v.role == role {
			insBlackMap[k] = nil
		}
	}

	return insBlackMap
}

func (f *FallbackHealthChecker) Close() {
	close(f.stopChan)
}
