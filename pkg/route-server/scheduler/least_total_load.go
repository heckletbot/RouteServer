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
	"sort"
	"strconv"
	"strings"
	"sync"

	"infer-dp-scheduler/pkg/common/constant"
	"infer-dp-scheduler/pkg/common/instance-metrics"
	"infer-dp-scheduler/pkg/common/logger"
	"infer-dp-scheduler/pkg/common/scheduler"
	"infer-dp-scheduler/pkg/common/utils"
)

const (
	LoadTypeNum = 2
	Verbose     = "verbose"
)

type LeastTotalLoad struct {
	mu                   sync.RWMutex
	instLoads            map[string][]int64      // The first element of []int64 stores total request count load, the second stores total request length load
	unavailableInstLoads map[string][]int64      // Temporarily stores load info of unhealthy instances
	reqDispatchInfo      map[string]*requestInfo // Single request info including dispatched instance id and request length load
	instBatchSize        map[string]int64
	defaultBatchSize     int64 // Default batch size if instance's StaticInfo doesn't have "max_num_seqs"

	verbose bool
}

type instCompareLoad struct {
	instId string
	load   float64
}

type requestInfo struct {
	instId string
	load   int64
}

func (l *LeastTotalLoad) Init(config map[string]string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.instLoads = make(map[string][]int64)
	l.unavailableInstLoads = make(map[string][]int64)
	l.reqDispatchInfo = make(map[string]*requestInfo)
	l.instBatchSize = make(map[string]int64)
	if maxNumSeqs, exists := config[constant.MaxNumSeqs]; exists {
		num, err := strconv.ParseInt(maxNumSeqs, 10, 64)
		if err == nil {
			l.defaultBatchSize = num
		}
	}
	if verboseStr, exist := config[Verbose]; exist {
		if verbose, err := strconv.ParseBool(verboseStr); err == nil {
			l.verbose = verbose
		}
	}
	if l.defaultBatchSize <= 0 {
		l.defaultBatchSize = 16
	}
	return nil
}

func (l *LeastTotalLoad) ClearLoad() {
	l.mu.Lock()
	defer l.mu.Unlock()
	logger.Log().Debugf("Begin clear load")
	// Clear request info
	l.reqDispatchInfo = make(map[string]*requestInfo)
	// Clear instance load but keep the instance
	instLoadsCopy := make(map[string][]int64, len(l.instLoads))
	for instId := range l.instLoads {
		instLoadsCopy[instId] = make([]int64, LoadTypeNum)
	}
	l.instLoads = instLoadsCopy
	// Clear unavailable instances info
	l.unavailableInstLoads = make(map[string][]int64)
	logger.Log().Debugf("Clear load successfully")
}

func (l *LeastTotalLoad) getRequestLoads() map[string]map[string]*scheduler.RequestLoadInfo {
	res := make(map[string]map[string]*scheduler.RequestLoadInfo)
	for reqId, reqInfo := range l.reqDispatchInfo {
		// Get request load and dispatched instance id from reqInfo
		reqLoad := reqInfo.load
		instId := reqInfo.instId
		if _, exists := res[instId]; !exists {
			res[instId] = make(map[string]*scheduler.RequestLoadInfo)
		}
		res[instId][reqId] = &scheduler.RequestLoadInfo{
			LoadVal: float64(reqLoad),
			Desc:    "Request load is prompt length",
		}
	}
	return res
}

func (l *LeastTotalLoad) getInstBatchSize(instanceId string) int64 {
	batchSize := l.defaultBatchSize
	if instBatchSize, exists := l.instBatchSize[instanceId]; exists {
		batchSize = instBatchSize
	}
	return batchSize
}

func (l *LeastTotalLoad) getInstLoadInfo(includeRequests bool, requestLoads map[string]map[string]*scheduler.RequestLoadInfo, available bool) []*scheduler.InstanceLoadInfo {
	res := make([]*scheduler.InstanceLoadInfo, 0)
	var instLoadsCopy map[string][]int64
	if available {
		instLoadsCopy = l.instLoads
	} else {
		instLoadsCopy = l.unavailableInstLoads
	}
	for instId, load := range instLoadsCopy {
		var desc string
		if available {
			desc = fmt.Sprintf("Total request num: %d, total request length: %d, max_num_seqs: %d",
				load[0], load[1], l.getInstBatchSize(instId))
		} else {
			desc = fmt.Sprintf("Total request num: %d, total request length: %d", load[0], load[1])
		}
		loadInfo := &scheduler.InstanceLoadInfo{
			Id:      instId,
			LoadVal: l.computeInstLoad(instId, load),
			Desc:    desc,
		}
		if includeRequests {
			if instReqLoad, exists := requestLoads[instId]; exists {
				loadInfo.Requests = instReqLoad
			} else {
				loadInfo.Requests = nil
			}
		}
		res = append(res, loadInfo)
	}
	return res
}

func (l *LeastTotalLoad) ReportLoads(includeRequests bool, includeUnavailable bool) *scheduler.AllInstanceLoads {
	l.mu.RLock()
	defer l.mu.RUnlock()
	reqLoads := make(map[string]map[string]*scheduler.RequestLoadInfo)
	if includeRequests {
		reqLoads = l.getRequestLoads()
	}

	availableInstLoads := l.getInstLoadInfo(includeRequests, reqLoads, true)
	var unavailableInstLoads []*scheduler.InstanceLoadInfo
	if includeUnavailable {
		unavailableInstLoads = l.getInstLoadInfo(includeRequests, reqLoads, false)
	}
	res := &scheduler.AllInstanceLoads{
		Available:   availableInstLoads,
		Unavailable: unavailableInstLoads,
	}
	return res
}

func (l *LeastTotalLoad) GetName() string {
	return LeastTotalLoadAlgoName
}

func (l *LeastTotalLoad) NeedStatsList() []string {
	return []string{}
}

func (l *LeastTotalLoad) clusterChange(newInstances map[string]instance_metrics.WorkerStats) bool {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if len(newInstances) != len(l.instLoads) {
		return true
	}
	for key := range newInstances {
		if _, exists := l.instLoads[key]; !exists {
			return true
		}
	}
	return false
}

func (l *LeastTotalLoad) UpdateCluster(available map[string]instance_metrics.WorkerStats, unavailable map[string]instance_metrics.WorkerStats) error {
	if !l.clusterChange(available) {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	instLoadsCopy := make(map[string][]int64, len(available))
	unavailableInstLoadsCopy := make(map[string][]int64, len(unavailable))
	instBatchSizeCopy := make(map[string]int64, len(available))
	reqDispatchInfoCopy := make(map[string]*requestInfo)
	// Get new instLoads and instBatchSize
	for id, stat := range available {
		if loadInfo, exists := l.instLoads[id]; exists {
			instLoadsCopy[id] = loadInfo
		} else if unavailableInstLoadInfo, exists := l.unavailableInstLoads[id]; exists {
			// If instance was in previously unhealthy instance map, add it back
			instLoadsCopy[id] = unavailableInstLoadInfo
		} else {
			instLoadsCopy[id] = make([]int64, LoadTypeNum)
		}

		// Get batch size
		zcs, ok := stat.(*instance_metrics.ZeroCopyStats)
		if ok {
			if maxNumSeqs, exists := zcs.StaticInfo[constant.MaxNumSeqs]; exists {
				batchSize, err := strconv.ParseInt(maxNumSeqs, 10, 64)
				if err == nil && batchSize > 0 {
					instBatchSizeCopy[id] = batchSize
				}
			}
		}
		// Use default if batch size not available
		if _, exists := instBatchSizeCopy[id]; !exists {
			logger.Log().Warnf("Instance %s StaticInfo does not contain the max_num_seqs value or value is error, "+
				"use the default value %d", id, l.defaultBatchSize)
			instBatchSizeCopy[id] = l.defaultBatchSize
		}
	}

	// Update unavailableInstLoads
	for id, _ := range unavailable {
		if loadInfo, exists := l.instLoads[id]; exists {
			unavailableInstLoadsCopy[id] = loadInfo
		} else if unavailableInstLoadInfo, exists := l.unavailableInstLoads[id]; exists {
			unavailableInstLoadsCopy[id] = unavailableInstLoadInfo
		} else {
			unavailableInstLoadsCopy[id] = make([]int64, LoadTypeNum)
		}
	}

	// Update reqDispatchInfo
	for reqId, reqInfo := range l.reqDispatchInfo {
		instId := reqInfo.instId
		if _, exists := available[instId]; exists {
			reqDispatchInfoCopy[reqId] = reqInfo
		}
		if _, exists := unavailableInstLoadsCopy[instId]; exists {
			reqDispatchInfoCopy[reqId] = reqInfo
		}
	}

	l.instLoads = instLoadsCopy
	l.unavailableInstLoads = unavailableInstLoadsCopy
	l.reqDispatchInfo = reqDispatchInfoCopy
	l.instBatchSize = instBatchSizeCopy
	logger.Log().Debugf("Update instance loads info successfully, workerNum: %d", len(l.instLoads))
	return nil
}

func (l *LeastTotalLoad) addLoad(instanceId string, request utils.GenerateRequest) {
	// Lock already held by DispatchRequest caller, no need to lock again
	reqId := request.GetRequestId()
	reqLen := request.GetPromptTokenCount()
	if loadInfo, exists := l.instLoads[instanceId]; exists {
		// Increment request count and add request length to instance load
		loadInfo[0] += 1
		loadInfo[1] += int64(reqLen)
	} else {
		logger.Log().Warnf("instanceId %s not in instLoads", instanceId)
		l.instLoads[instanceId] = []int64{1, int64(reqLen)}
	}
	// Update request's dispatched instance and length load
	l.reqDispatchInfo[reqId] = &requestInfo{
		instId: instanceId,
		load:   int64(reqLen),
	}
}

func (l *LeastTotalLoad) reduceLoad(request utils.GenerateRequest) {
	// Lock already held by OnRequestEvent caller, no need to lock again
	reqId := request.GetRequestId()
	var reqLen int64

	if reqInfo, exists := l.reqDispatchInfo[reqId]; exists {
		instId := reqInfo.instId
		reqLen = reqInfo.load
		if loadInfo, exists := l.instLoads[instId]; exists {
			// Decrement request count and subtract request length from instance load
			loadInfo[0] -= 1
			loadInfo[1] -= int64(reqLen)
		} else if unavailableInstloadInfo, exists := l.unavailableInstLoads[instId]; exists {
			// Also decrement if instance exists in unavailableInstLoads but not in instLoads
			unavailableInstloadInfo[0] -= 1
			unavailableInstloadInfo[1] -= int64(reqLen)
		}
		delete(l.reqDispatchInfo, reqId)
	}
}

func (l *LeastTotalLoad) UpdateConfig(config map[string]string) error {
	return nil
}

func (l *LeastTotalLoad) GetUpdateInterval() int64 {
	// no need update unless nodes changed
	return -1
}

func (l *LeastTotalLoad) computeInstLoad(instanceId string, load []int64) float64 {
	if len(load) != LoadTypeNum {
		return 0
	}
	reqNum := load[0]
	reqLen := load[1]
	batchSize := l.getInstBatchSize(instanceId)
	return float64(reqLen) * (math.Ceil(float64(reqNum)/float64(batchSize)) + 1) / 2
}

func (l *LeastTotalLoad) compareAndSortLoads() []instCompareLoad {
	// Calculate load for each instance according to load formula and sort
	var instCompareLoads []instCompareLoad
	for instId, load := range l.instLoads {
		instCompareLoads = append(instCompareLoads, instCompareLoad{
			instId,
			l.computeInstLoad(instId, load),
		})
	}
	sort.Slice(instCompareLoads, func(i, j int) bool {
		return instCompareLoads[i].load < instCompareLoads[j].load
	})
	return instCompareLoads
}

func (l *LeastTotalLoad) DispatchRequest(request utils.GenerateRequest, params map[string]string, insBlackMap map[string]interface{}) (string, bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	nodesCount := int64(len(l.instLoads))
	if nodesCount == 0 {
		return "", false, nil
	}
	if insBlackMap != nil {
		if nodesCount == int64(len(insBlackMap)) {
			return "", true, nil
		}
	}

	// Calculate load for each instance according to load formula and sort
	instCompareLoads := l.compareAndSortLoads()
	if l.verbose {
		logger.Log().Infof("%s: computedLoads: %v", request.GetRequestId(), printInsLoad(instCompareLoads, l.instLoads))
	}
	skipped := false
	var idx int64
	for idx = 0; idx < nodesCount; idx++ {
		if insBlackMap != nil {
			_, exist := insBlackMap[instCompareLoads[idx].instId]
			if exist {
				skipped = true
				continue
			}
		}
		l.addLoad(instCompareLoads[idx].instId, request)
		return instCompareLoads[idx].instId, skipped, nil
	}
	return "", skipped, nil
}

func printInsLoad(computed []instCompareLoad, srcLoads map[string][]int64) string {
	builder := strings.Builder{}
	for _, load := range computed {
		reqNum := (int64)(-1)
		totalLen := (int64)(-1)
		if insLoad, exists := srcLoads[load.instId]; exists {
			reqNum = insLoad[0]
			totalLen = insLoad[1]
		}
		builder.WriteString(fmt.Sprintf("<%s, req_num=%v, total_len=%v, load=%v>, ", load.instId, reqNum, totalLen, load.load))
	}
	return builder.String()
}

func (l *LeastTotalLoad) OnRequestEvent(event *utils.RequestEventMsg) {
	if event.EventType != constant.RequestNew {
		l.mu.Lock()
		defer l.mu.Unlock()
		// Update load (decrease load)
		l.reduceLoad(event.Request)
	}
}
