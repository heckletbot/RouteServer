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

package scheduler

import (
	"fmt"
	"math"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"infer-dp-scheduler/pkg/common/constant"
	instance_metrics "infer-dp-scheduler/pkg/common/instance-metrics"
	"infer-dp-scheduler/pkg/common/logger"
	"infer-dp-scheduler/pkg/common/scheduler"
	"infer-dp-scheduler/pkg/common/utils"
	"infer-dp-scheduler/pkg/route-server/apc"
	"infer-dp-scheduler/pkg/route-server/metrics"
)

var (
	_ scheduler.ScheduleAlgorithm = &APCScheduler{}
)

type apcRequestInfo struct {
	instanceId     string
	promptTokenCnt int64   // Actual prompt token count
	matchedRatio   float64 // Match ratio for load reduction calculation
	reqLen         uint64  // Request length for load calculation
}

// APCScheduler APC Scheduler Implementation
type APCScheduler struct {
	name    string
	config  map[string]string
	running atomic.Bool

	// Instance state management
	instancesMutex           sync.RWMutex
	availableInstanceLoads   map[string]apc.InstanceLoad
	unavailableInstanceLoads map[string]apc.InstanceLoad
	reqDispatchInfo          map[string]*apcRequestInfo // Single request info for load management
	instBatchSize            map[string]int64

	defaultBatchSize int64
	prefixLoadBase   int64
}

const (
	defaultBatchSize      = 16
	defaultPrefixLoadBase = 1
)

// NewAPCScheduler Creates APC Scheduler Instance
func NewAPCScheduler() (scheduler.ScheduleAlgorithm, error) {
	// At this stage only create structure, do not initialize APCManager
	return &APCScheduler{
		name:                     APCSchedulerName,
		availableInstanceLoads:   make(map[string]apc.InstanceLoad),
		unavailableInstanceLoads: make(map[string]apc.InstanceLoad),
		reqDispatchInfo:          make(map[string]*apcRequestInfo),
		instBatchSize:            make(map[string]int64),
		running:                  atomic.Bool{},
		defaultBatchSize:         defaultBatchSize,
		prefixLoadBase:           defaultPrefixLoadBase,
	}, nil
}

func (a *APCScheduler) updateConfig(config map[string]string) {
	// 1. Parse default-batch-size
	if defaultBatchSizeStr, exists := config["default-batch-size"]; exists {
		if configDefaultBatchSize, err := strconv.ParseInt(defaultBatchSizeStr, 10, 64); err == nil && configDefaultBatchSize >= 0 {
			a.defaultBatchSize = configDefaultBatchSize
		} else {
			logger.Log().Warnf("Failed to parse default-batch-size '%s', using default %d", defaultBatchSizeStr, defaultBatchSize)
			a.defaultBatchSize = defaultBatchSize
		}
	} else {
		a.defaultBatchSize = defaultBatchSize
	}

	// 2. Parse prefix-load-base
	if prefixLoadBaseStr, exists := config["prefix-load-base"]; exists {
		if configPrefixLoadBase, err := strconv.ParseInt(prefixLoadBaseStr, 10, 64); err == nil && configPrefixLoadBase >= 0 {
			a.prefixLoadBase = configPrefixLoadBase
		} else {
			logger.Log().Warnf("Failed to parse prefix-load-base '%s', using default %d", prefixLoadBaseStr, defaultPrefixLoadBase)
			a.prefixLoadBase = defaultPrefixLoadBase
		}
	} else {
		a.prefixLoadBase = defaultPrefixLoadBase
	}
	a.config = config
}

// Init Initializes the scheduler
func (a *APCScheduler) Init(config map[string]string) error {
	if config == nil {
		config = make(map[string]string)
	}

	a.updateConfig(config)
	logger.Log().Infof("Initializing APC scheduler with config: default-batch-size=%d, prefix-load-base=%d", a.defaultBatchSize, a.prefixLoadBase)

	// Check global APC manager status
	apcManager := apc.GetGlobalManager()
	if apcManager == nil {
		return fmt.Errorf("APC manager not initialized, cannot create APC scheduler")
	} else {
		// Check if APC is enabled
		apcConfig := apcManager.GetConfig()
		if !apcConfig.Enable {
			return fmt.Errorf("APC manager disable, cannot create APC scheduler")
		} else {
			logger.Log().Infof("APC scheduler initialized with APC manager enabled")
		}
	}

	a.running.Store(true)

	logger.Log().Infof("APC scheduler %s initialized successfully", a.name)
	return nil
}

// GetName Gets scheduler name
func (a *APCScheduler) GetName() string {
	return a.name
}

// NeedStatsList Gets the list of required statistics
func (a *APCScheduler) NeedStatsList() []string {
	return []string{
		"ActiveRequests",
		"TotalTokens",
		"QueueLength",
		"CacheSize",
	}
}

func (a *APCScheduler) getInstancesToRemove(newAvailable map[string]instance_metrics.WorkerStats, newUnavailable map[string]instance_metrics.WorkerStats) map[string]struct{} {
	var instancesToRemove = make(map[string]struct{})
	for _, instances := range []map[string]apc.InstanceLoad{a.availableInstanceLoads, a.unavailableInstanceLoads} {
		for instanceId, _ := range instances {
			_, available := newAvailable[instanceId]
			_, unavailable := newUnavailable[instanceId]
			if !available && !unavailable {
				instancesToRemove[instanceId] = struct{}{}
			}
		}
	}
	return instancesToRemove
}

// UpdateInstances Updates instance state, caller is responsible for locking
func (a *APCScheduler) updateInstances(newAvailable map[string]instance_metrics.WorkerStats, newUnavailable map[string]instance_metrics.WorkerStats) {
	availables := make(map[string]apc.InstanceLoad)
	instBatchSizeCopy := make(map[string]int64)

	for instanceID, stats := range newAvailable {
		// Only process prefill instances
		if stats.GetRole() == constant.Prefill {
			old, exist := a.availableInstanceLoads[instanceID]
			if !exist {
				old = apc.InstanceLoad{
					RequestsTotalCnt: 0,
					PromptTotalLen:   0,
				}
			}
			availables[instanceID] = old

			// Get batch size
			zcs, ok := stats.(*instance_metrics.ZeroCopyStats)
			if ok {
				if maxNumSeqs, exists := zcs.StaticInfo[constant.MaxNumSeqs]; exists {
					batchSize, err := strconv.ParseInt(maxNumSeqs, 10, 64)
					if err == nil && batchSize > 0 {
						instBatchSizeCopy[instanceID] = batchSize
					}
				}
			}
			// Use default if batch size not available
			if _, exists := instBatchSizeCopy[instanceID]; !exists {
				logger.Log().Warnf("Instance %s StaticInfo does not contain the max_num_seqs value or value is error, "+
					"use the default value %d", instanceID, a.defaultBatchSize)
				instBatchSizeCopy[instanceID] = a.defaultBatchSize
			}
		}
	}
	a.availableInstanceLoads = availables
	a.instBatchSize = instBatchSizeCopy

	// Update unavailable instances
	unavailables := make(map[string]apc.InstanceLoad)
	for instanceID, stats := range newUnavailable {
		// Only process prefill instances
		if stats.GetRole() == constant.Prefill {
			unavailables[instanceID] = a.unavailableInstanceLoads[instanceID]
		}
	}
	a.unavailableInstanceLoads = unavailables
}

func (a *APCScheduler) removeInstanceLoads(instances map[string]struct{}) {
	for reqId, reqInfo := range a.reqDispatchInfo {
		if _, exist := instances[reqInfo.instanceId]; exist {
			delete(a.reqDispatchInfo, reqId)
		}
	}
}

// UpdateCluster Updates cluster state
func (a *APCScheduler) UpdateCluster(newAvailable map[string]instance_metrics.WorkerStats, newUnavailable map[string]instance_metrics.WorkerStats) error {
	if !a.running.Load() {
		return fmt.Errorf("APC scheduler is not running")
	}

	start := time.Now()
	defer func() {
		logger.Log().Tracef(" APC scheduler cluster update completed in %v", time.Since(start))
	}()

	a.instancesMutex.Lock()
	defer a.instancesMutex.Unlock()

	// Get instances to remove
	instancesToRemove := a.getInstancesToRemove(newAvailable, newUnavailable)

	// Update available instances
	a.updateInstances(newAvailable, newUnavailable)
	a.removeInstanceLoads(instancesToRemove)

	// Removed instances need cache cleanup
	apcManager := apc.GetGlobalManager()
	if apcManager != nil {
		if err := apcManager.RemoveInstanceIdFromCache(instancesToRemove); err != nil {
			logger.Log().Errorf("Failed to sync APC manager with cluster: %v", err)
		}
	}

	logger.Log().Tracef("APC scheduler cluster updated: newAvailable=%d, newUnavailable=%d",
		len(a.availableInstanceLoads), len(a.unavailableInstanceLoads))

	return nil
}

// UpdateConfig Updates configuration
func (a *APCScheduler) UpdateConfig(config map[string]string) error {
	if config == nil {
		return nil
	}

	a.instancesMutex.Lock()
	defer a.instancesMutex.Unlock()

	// Update local config
	oldConfig := a.config
	a.updateConfig(config)

	logger.Log().Infof("APC scheduler config updated, old %v, new %v", oldConfig, config)
	return nil
}

// GetUpdateInterval Gets update interval
func (a *APCScheduler) GetUpdateInterval() int64 {
	// APC algorithm doesn't need update, if > 0 it would try to get prometheus metrics, not yet connected
	return -1
}

func (a *APCScheduler) addLoadUnsafe(instanceId string, request utils.GenerateRequest, matchedRatio float64) {
	promptTokenCount := request.GetPromptTokenCount()
	load := a.availableInstanceLoads[instanceId]
	unmatchedBytes := float64(promptTokenCount) * (1 - matchedRatio)
	reqLen := uint64(unmatchedBytes) + uint64(a.prefixLoadBase)
	load.PromptTotalLen += reqLen
	load.RequestsTotalCnt += 1
	a.availableInstanceLoads[instanceId] = load
	a.reqDispatchInfo[request.GetRequestId()] = &apcRequestInfo{
		instanceId:     instanceId,
		promptTokenCnt: int64(promptTokenCount),
		matchedRatio:   matchedRatio,
		reqLen:         reqLen,
	}
}

func (a *APCScheduler) reduceLoad(requestId string) {
	a.instancesMutex.Lock()
	defer a.instancesMutex.Unlock()

	// Get request info from reqDispatchInfo
	reqInfo, exists := a.reqDispatchInfo[requestId]
	if !exists {
		logger.Log().Warnf("Request %s not found in dispatch info, cannot reduce load", requestId)
		return
	}

	instanceId := reqInfo.instanceId
	// Reduce load
	if availableLoad, exist := a.availableInstanceLoads[instanceId]; exist {
		availableLoad.PromptTotalLen -= reqInfo.reqLen
		availableLoad.RequestsTotalCnt -= 1
		a.availableInstanceLoads[instanceId] = availableLoad
	} else if unavailableLoad, exist := a.unavailableInstanceLoads[instanceId]; exist {
		unavailableLoad.PromptTotalLen -= reqInfo.reqLen
		unavailableLoad.RequestsTotalCnt -= 1
		a.unavailableInstanceLoads[instanceId] = unavailableLoad
	}

	// Delete request info
	delete(a.reqDispatchInfo, requestId)
}

// DispatchRequest Dispatches request
func (a *APCScheduler) DispatchRequest(request utils.GenerateRequest, params map[string]string, insBlackMap map[string]interface{}) (string, bool, error) {
	if !a.running.Load() {
		return "", false, fmt.Errorf("APC scheduler is not running")
	}

	// Extract request info
	requestID := request.GetRequestId()
	model := request.GetModel()
	enableAPC := a.isAPCEnabled(request)
	strategy := request.GetApcStrategyType()

	logger.Log().Tracef("APC scheduler dispatching request %s: model=%s, enableAPC=%v, strategy=%s",
		requestID, model, enableAPC, strategy)

	apcManager := apc.GetGlobalManager()
	if !enableAPC || apcManager == nil || !apcManager.IsRunning() {
		return "", false, fmt.Errorf("APC disabled or not running")
	}

	// Get current available instances (protected by read lock) and perform matching and selection
	a.instancesMutex.Lock()
	defer a.instancesMutex.Unlock()

	currentAvailableInstanceLoads := make(map[string]apc.InstanceLoad)
	for key, value := range a.availableInstanceLoads {
		currentAvailableInstanceLoads[key] = value
	}

	if len(currentAvailableInstanceLoads) == 0 {
		logger.Log().Debugf("APC dispatch failed for request %s: no available instances", request.GetRequestId())
		return "", false, nil
	}

	// Get matched instance candidates (Strategy is responsible for cache lookup)
	matchedInstances, err := apcManager.GetMatchedInstances(request, currentAvailableInstanceLoads, insBlackMap)
	if err != nil {
		logger.Log().Debugf("No matched instances for request %s: %v, will select from all available instances", requestID, err)
		// Continue even if no matched instances, let all available instances participate to avoid starvation
		matchedInstances = make(map[string]float64)
	}

	// Calculate load and select under lock protection to ensure thread safety in predictLoadAfterAddRequest
	bestInstance, bestMatchedRatio := a.selectBestInstanceByLoad(request, matchedInstances, currentAvailableInstanceLoads)

	// Record match ratio
	metrics.RecordAPCQueryMatchRatio(bestMatchedRatio)

	if bestInstance == "" {
		return "", false, fmt.Errorf("APC dispatch failed for request %s: no best instance selected", requestID)
	}

	a.addLoadUnsafe(bestInstance, request, bestMatchedRatio)
	logger.Log().Tracef("APC successfully matched request %s to instance %s", requestID, bestInstance)
	return bestInstance, false, nil
}

// IsRequestEventNeedProcess Checks if request event needs processing
func (a *APCScheduler) IsRequestEventNeedProcess(event *utils.RequestEventMsg) bool {
	if event.TargetInstance == "" {
		logger.Log().Errorf("apc prefill request target instance id is null")
		return false
	}

	return !event.InternalTestingReq && event.EventType == constant.RequestPrefillSuccess
}

// OnRequestEvent Handles request events
func (a *APCScheduler) OnRequestEvent(event *utils.RequestEventMsg) {
	// Record processing start time, use defer to record total elapsed time
	startTime := time.Now()
	defer func() {
		requestID := ""
		if event != nil && event.Request != nil {
			requestID = event.Request.GetRequestId()
		}
		logger.Log().Debugf("APC scheduler OnRequestEvent for request %s took %v", requestID, time.Since(startTime))
	}()

	if event == nil {
		logger.Log().Errorf("APC scheduler received nil request event")
		return
	}

	requestID := event.Request.GetRequestId()
	logger.Log().Tracef("APC scheduler received request event, type=%v, requestId=%s, ", event.EventType, requestID)
	if requestID == "" {
		return
	}

	// Record reduceLoad processing time
	if event.EventType != constant.RequestNew {
		a.reduceLoad(requestID)
	}
	if !a.IsRequestEventNeedProcess(event) {
		return
	}

	// === APC CACHE UPDATE ===
	// Only RequestPrefillSuccess events update cache, ensuring KV Cache actually exists
	apcManager := apc.GetGlobalManager()
	if apcManager == nil || !apcManager.IsMeLeader() {
		logger.Log().Debugf("current apc manager is on non-Leader, requestId=%s", requestID)
		return
	}

	// Directly call Manager's UpdateCache method, algorithm handles mapping internally
	if err := apcManager.UpdateCache(event); err != nil {
		// Fallback to least-total-load scheduling on update failure
		logger.Log().Debugf("Failed to update APC cache via requestId: %s, error: %v", requestID, err)
	} else {
		logger.Log().Debugf("APC cache updated via requestId: %s -> %s", requestID, event.TargetInstance)
	}
	// === END APC CACHE UPDATE ===
}

// ReportLoads Reports load information
func (a *APCScheduler) ReportLoads(includeRequests bool, includeUnavailable bool) *scheduler.AllInstanceLoads {
	a.instancesMutex.RLock()
	defer a.instancesMutex.RUnlock()

	reqLoads := make(map[string]map[string]*scheduler.RequestLoadInfo)
	if includeRequests {
		reqLoads = a.getRequestLoads()
	}

	availableInstLoads := a.getInstLoadInfo(includeRequests, reqLoads, true)
	var unavailableInstLoads []*scheduler.InstanceLoadInfo
	if includeUnavailable {
		unavailableInstLoads = a.getInstLoadInfo(includeRequests, reqLoads, false)
	}

	res := &scheduler.AllInstanceLoads{
		Available:   availableInstLoads,
		Unavailable: unavailableInstLoads,
	}
	return res
}

// ClearLoad Clears load information
func (a *APCScheduler) ClearLoad() {
	a.instancesMutex.Lock()
	defer a.instancesMutex.Unlock()

	logger.Log().Debugf("Begin clear load")
	// Clear request info
	a.reqDispatchInfo = make(map[string]*apcRequestInfo)
	// Clear instance load but keep the instance
	availableLoadsCopy := make(map[string]apc.InstanceLoad, len(a.availableInstanceLoads))
	for instanceID := range a.availableInstanceLoads {
		availableLoadsCopy[instanceID] = apc.InstanceLoad{}
	}
	a.availableInstanceLoads = availableLoadsCopy
	// Clear unavailable instances info
	a.unavailableInstanceLoads = make(map[string]apc.InstanceLoad)
	logger.Log().Debugf("Clear load successfully")
}

// getRequestLoads Gets request load information
func (a *APCScheduler) getRequestLoads() map[string]map[string]*scheduler.RequestLoadInfo {
	reqLoads := make(map[string]map[string]*scheduler.RequestLoadInfo)
	for reqId, reqInfo := range a.reqDispatchInfo {
		if _, exists := reqLoads[reqInfo.instanceId]; !exists {
			reqLoads[reqInfo.instanceId] = make(map[string]*scheduler.RequestLoadInfo)
		}
		// Use match ratio directly
		reqLoads[reqInfo.instanceId][reqId] = &scheduler.RequestLoadInfo{
			LoadVal: reqInfo.matchedRatio,
			Desc: fmt.Sprintf("prompt_bytes: %d, match_rate: %.2f%%",
				reqInfo.promptTokenCnt, reqInfo.matchedRatio*100),
		}
	}
	return reqLoads
}

// getInstLoadInfo Gets instance load information
func (a *APCScheduler) getInstLoadInfo(includeRequests bool, reqLoads map[string]map[string]*scheduler.RequestLoadInfo, available bool) []*scheduler.InstanceLoadInfo {
	var instLoads map[string]apc.InstanceLoad
	if available {
		instLoads = a.availableInstanceLoads
	} else {
		instLoads = a.unavailableInstanceLoads
	}

	var res []*scheduler.InstanceLoadInfo
	for instanceID, load := range instLoads {
		// Calculate instance load, refer to LeastTotalLoad's computeInstLoad method
		loadVal := a.computeInstLoad(instanceID, load)

		var desc string
		if available {
			batchSize := a.getInstBatchSize(instanceID)
			desc = fmt.Sprintf("Total request num: %d, total prompt length: %d, batch_size: %d",
				load.RequestsTotalCnt, load.PromptTotalLen, batchSize)
		} else {
			desc = fmt.Sprintf("Total request num: %d, total prompt length: %d",
				load.RequestsTotalCnt, load.PromptTotalLen)
		}

		instLoadInfo := &scheduler.InstanceLoadInfo{
			Id:      instanceID,
			LoadVal: loadVal,
			Desc:    desc,
		}

		if includeRequests && reqLoads != nil {
			if instReqLoad, exists := reqLoads[instanceID]; exists {
				instLoadInfo.Requests = instReqLoad
			}
		}
		res = append(res, instLoadInfo)
	}
	return res
}

// computeInstLoad Calculates instance load, refer to LeastTotalLoad implementation
func (a *APCScheduler) computeInstLoad(instanceId string, load apc.InstanceLoad) float64 {
	reqNum := load.RequestsTotalCnt
	reqLen := load.PromptTotalLen
	batchSize := a.getInstBatchSize(instanceId)
	if batchSize <= 0 {
		batchSize = a.defaultBatchSize
	}
	return float64(reqLen) * (math.Ceil(float64(reqNum)/float64(batchSize)) + 1) / 2
}

// getInstBatchSize Gets instance batch size
func (a *APCScheduler) getInstBatchSize(instanceId string) int64 {
	batchSize := a.defaultBatchSize
	if instBatchSize, exists := a.instBatchSize[instanceId]; exists {
		batchSize = instBatchSize
	}
	return batchSize
}

// selectBestInstanceByLoad Selects best instance based on load
func (a *APCScheduler) selectBestInstanceByLoad(request utils.GenerateRequest, matchedInstances map[string]float64, availableLoads map[string]apc.InstanceLoad) (string, float64) {
	type InstancePredictedLoad struct {
		InstanceID    string
		PredictedLoad float64
		MatchedRatio  float64
	}

	// Pre-calculate and collect predicted loads for all available instances to avoid starving unmatched instances
	var predictedResults []*InstancePredictedLoad
	for instanceID, currentLoad := range availableLoads {
		// Get matched bytes, returns 0 if instance not in matchedInstances
		matchedRatio := matchedInstances[instanceID]

		predictedLoad := a.predictLoadAfterAddRequest(request, currentLoad, instanceID, matchedRatio)

		predictedResults = append(predictedResults, &InstancePredictedLoad{
			InstanceID:    instanceID,
			PredictedLoad: predictedLoad,
			MatchedRatio:  matchedRatio,
		})
	}

	if len(predictedResults) == 0 {
		return "", 0
	}

	logger.Log().Tracef("Current available loads: %v", availableLoads)

	// One pass to find best instance: prefer lowest predicted load, if loads equal prefer longest match
	best := predictedResults[0]
	for i := 1; i < len(predictedResults); i++ {
		current := predictedResults[i]
		if current.PredictedLoad < best.PredictedLoad ||
			(current.PredictedLoad == best.PredictedLoad && current.MatchedRatio > best.MatchedRatio) {
			best = current
		}
	}

	logger.Log().Tracef("Selected best instance %s with predicted load %f and matched ratio %.4f for request %v",
		best.InstanceID, best.PredictedLoad, best.MatchedRatio, request.GetRequestId())
	return best.InstanceID, best.MatchedRatio
}

// predictLoadAfterAddRequest Pre-calculates load after adding request
func (a *APCScheduler) predictLoadAfterAddRequest(request utils.GenerateRequest, currentLoad apc.InstanceLoad, instanceID string, matchedRatio float64) float64 {
	promptTokenCount := request.GetPromptTokenCount()

	// Calculate total length after adding request
	// Note: Perform floating point calculation for unmatched bytes first, convert to uint64 at the end
	unmatchedBytes := float64(promptTokenCount) * (1 - matchedRatio)
	reqLen := currentLoad.PromptTotalLen + uint64(unmatchedBytes) + uint64(a.prefixLoadBase)
	reqNum := currentLoad.RequestsTotalCnt + 1

	// Prefer instance's batchSize, use default if not available
	batchSize := a.defaultBatchSize
	if instBatchSize, exists := a.instBatchSize[instanceID]; exists && instBatchSize > 0 {
		batchSize = instBatchSize
	}

	logger.Log().Tracef("predict load for request %v instance %v reqLen %v reqNum %v batchSize %v, promptCount %v, matchedRatio %v, prefixLoadBase %v",
		request.GetRequestId(), instanceID, reqLen, reqNum, batchSize, promptTokenCount, matchedRatio, a.prefixLoadBase)
	// Return predicted load
	return float64(reqLen) * (math.Ceil(float64(reqNum)/float64(batchSize)) + 1) / 2
}

// isAPCEnabled Checks if APC is enabled for request
func (a *APCScheduler) isAPCEnabled(request utils.GenerateRequest) bool {
	if request == nil {
		return false
	}

	// Determine if APC is enabled through SLOTag (simplified here, can be adjusted based on specific needs)
	// Example: requests with specific SLOTag enable APC
	// default: all prefill requests enable APC (if global config is enabled)
	if request.GetType() == constant.Prefill {
		apcManager := apc.GetGlobalManager()
		return apcManager != nil && apcManager.IsRunning() && apcManager.GetConfig().Enable
	}

	return false
}
