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
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"infer-dp-scheduler/pkg/common/constant"
	"infer-dp-scheduler/pkg/common/instance-metrics"
	"infer-dp-scheduler/pkg/common/logger"
	"infer-dp-scheduler/pkg/common/scheduler"
	"infer-dp-scheduler/pkg/common/utils"
)

type LeastRequests struct {
	mu sync.RWMutex

	instRequestCounts    map[string]int64
	unavailableReqCounts map[string]int64
	instLastAssignedTime map[string]int64
	reqDispatchInfo      map[string]*lrRequestInfo
	instBatchSize        map[string]int64
	defaultBatchSize     int64

	verbose bool
}

type lrRequestInfo struct {
	instId string
	load   int64
}

type lrInstCompare struct {
	instId           string
	requestCount     int64
	lastAssignedTime int64
}

func (lr *LeastRequests) Init(config map[string]string) error {
	lr.mu.Lock()
	defer lr.mu.Unlock()
	lr.instRequestCounts = make(map[string]int64)
	lr.unavailableReqCounts = make(map[string]int64)
	lr.instLastAssignedTime = make(map[string]int64)
	lr.reqDispatchInfo = make(map[string]*lrRequestInfo)
	lr.instBatchSize = make(map[string]int64)
	if maxNumSeqs, exists := config[constant.MaxNumSeqs]; exists {
		num, err := strconv.ParseInt(maxNumSeqs, 10, 64)
		if err == nil {
			lr.defaultBatchSize = num
		}
	}
	if verboseStr, exist := config[Verbose]; exist {
		if verbose, err := strconv.ParseBool(verboseStr); err == nil {
			lr.verbose = verbose
		}
	}
	if lr.defaultBatchSize <= 0 {
		lr.defaultBatchSize = 16
	}
	return nil
}

func (lr *LeastRequests) GetName() string {
	return LeastRequestsAlgoName
}

func (lr *LeastRequests) NeedStatsList() []string {
	return []string{}
}

func (lr *LeastRequests) GetUpdateInterval() int64 {
	return -1
}

func (lr *LeastRequests) UpdateConfig(config map[string]string) error {
	return nil
}

func (lr *LeastRequests) ClearLoad() {
	lr.mu.Lock()
	defer lr.mu.Unlock()
	logger.Log().Debugf("Begin clear load")
	lr.reqDispatchInfo = make(map[string]*lrRequestInfo)
	instCountsCopy := make(map[string]int64, len(lr.instRequestCounts))
	for instId := range lr.instRequestCounts {
		instCountsCopy[instId] = 0
	}
	lr.instRequestCounts = instCountsCopy
	lr.instLastAssignedTime = make(map[string]int64)
	lr.unavailableReqCounts = make(map[string]int64)
	logger.Log().Debugf("Clear load successfully")
}

func (lr *LeastRequests) getInstBatchSize(instanceId string) int64 {
	batchSize := lr.defaultBatchSize
	if instBatchSize, exists := lr.instBatchSize[instanceId]; exists {
		batchSize = instBatchSize
	}
	return batchSize
}

func (lr *LeastRequests) clusterChange(newInstances map[string]instance_metrics.WorkerStats) bool {
	lr.mu.RLock()
	defer lr.mu.RUnlock()
	if len(newInstances) != len(lr.instRequestCounts) {
		return true
	}
	for key := range newInstances {
		if _, exists := lr.instRequestCounts[key]; !exists {
			return true
		}
	}
	return false
}

func (lr *LeastRequests) UpdateCluster(available map[string]instance_metrics.WorkerStats, unavailable map[string]instance_metrics.WorkerStats) error {
	if !lr.clusterChange(available) {
		return nil
	}
	lr.mu.Lock()
	defer lr.mu.Unlock()
	instCountsCopy := make(map[string]int64, len(available))
	unavailableCountsCopy := make(map[string]int64, len(unavailable))
	instBatchSizeCopy := make(map[string]int64, len(available))
	instLastAssignedTimeCopy := make(map[string]int64, len(available))
	reqDispatchInfoCopy := make(map[string]*lrRequestInfo)

	for id, stat := range available {
		if count, exists := lr.instRequestCounts[id]; exists {
			instCountsCopy[id] = count
		} else if unavailCount, exists := lr.unavailableReqCounts[id]; exists {
			instCountsCopy[id] = unavailCount
		} else {
			instCountsCopy[id] = 0
		}
		if lastTime, exists := lr.instLastAssignedTime[id]; exists {
			instLastAssignedTimeCopy[id] = lastTime
		}

		zcs, ok := stat.(*instance_metrics.ZeroCopyStats)
		if ok {
			if maxNumSeqs, exists := zcs.StaticInfo[constant.MaxNumSeqs]; exists {
				batchSize, err := strconv.ParseInt(maxNumSeqs, 10, 64)
				if err == nil && batchSize > 0 {
					instBatchSizeCopy[id] = batchSize
				}
			}
		}
		if _, exists := instBatchSizeCopy[id]; !exists {
			logger.Log().Warnf("Instance %s StaticInfo does not contain the max_num_seqs value or value is error, "+
				"use the default value %d", id, lr.defaultBatchSize)
			instBatchSizeCopy[id] = lr.defaultBatchSize
		}
	}

	for id := range unavailable {
		if count, exists := lr.instRequestCounts[id]; exists {
			unavailableCountsCopy[id] = count
		} else if unavailCount, exists := lr.unavailableReqCounts[id]; exists {
			unavailableCountsCopy[id] = unavailCount
		} else {
			unavailableCountsCopy[id] = 0
		}
	}

	for reqId, reqInfo := range lr.reqDispatchInfo {
		instId := reqInfo.instId
		if _, exists := available[instId]; exists {
			reqDispatchInfoCopy[reqId] = reqInfo
		}
		if _, exists := unavailableCountsCopy[instId]; exists {
			reqDispatchInfoCopy[reqId] = reqInfo
		}
	}

	lr.instRequestCounts = instCountsCopy
	lr.unavailableReqCounts = unavailableCountsCopy
	lr.instLastAssignedTime = instLastAssignedTimeCopy
	lr.reqDispatchInfo = reqDispatchInfoCopy
	lr.instBatchSize = instBatchSizeCopy
	logger.Log().Debugf("Update instance loads info successfully, workerNum: %d", len(lr.instRequestCounts))
	return nil
}

func (lr *LeastRequests) sortInstances() []lrInstCompare {
	var instCompares []lrInstCompare
	for instId, count := range lr.instRequestCounts {
		lastTime := lr.instLastAssignedTime[instId]
		instCompares = append(instCompares, lrInstCompare{
			instId:           instId,
			requestCount:     count,
			lastAssignedTime: lastTime,
		})
	}
	sort.Slice(instCompares, func(i, j int) bool {
		if instCompares[i].requestCount != instCompares[j].requestCount {
			return instCompares[i].requestCount < instCompares[j].requestCount
		}
		return instCompares[i].lastAssignedTime < instCompares[j].lastAssignedTime
	})
	return instCompares
}

func (lr *LeastRequests) DispatchRequest(request utils.GenerateRequest, params map[string]string, insBlackMap map[string]interface{}) (string, bool, error) {
	lr.mu.Lock()
	defer lr.mu.Unlock()
	nodesCount := int64(len(lr.instRequestCounts))
	if nodesCount == 0 {
		return "", false, nil
	}

	sortedInsts := lr.sortInstances()
	if lr.verbose {
		logger.Log().Infof("%s: sortedInstances: %v", request.GetRequestId(), lrPrintInstLoad(sortedInsts))
	}

	skipped := false
	selectedInst := ""
	for i := 0; i < len(sortedInsts); i++ {
		if insBlackMap != nil {
			if _, exist := insBlackMap[sortedInsts[i].instId]; exist {
				skipped = true
				continue
			}
		}
		selectedInst = sortedInsts[i].instId
		break
	}

	if selectedInst == "" {
		return "", skipped, nil
	}

	if lr.verbose {
		minCount := sortedInsts[0].requestCount
		tieCount := 0
		for _, inst := range sortedInsts {
			if inst.requestCount == minCount {
				tieCount++
			}
		}
		if tieCount == 1 {
			logger.Log().Infof("%s: dispatched to %s, reason: only instance with minimum request count %d",
				request.GetRequestId(), selectedInst, minCount)
		} else {
			logger.Log().Infof("%s: dispatched to %s, reason: %d instances tied at minimum request count %d, "+
				"selected the one with earliest last assigned time",
				request.GetRequestId(), selectedInst, tieCount, minCount)
		}
	}

	lr.instRequestCounts[selectedInst]++
	lr.instLastAssignedTime[selectedInst] = time.Now().UnixNano()
	reqId := request.GetRequestId()
	reqLen := request.GetPromptTokenCount()
	lr.reqDispatchInfo[reqId] = &lrRequestInfo{
		instId: selectedInst,
		load:   int64(reqLen),
	}
	return selectedInst, skipped, nil
}

func (lr *LeastRequests) OnRequestEvent(event *utils.RequestEventMsg) {
	if event.EventType != constant.RequestNew {
		lr.mu.Lock()
		defer lr.mu.Unlock()
		lr.reduceLoad(event.Request)
	}
}

func (lr *LeastRequests) reduceLoad(request utils.GenerateRequest) {
	reqId := request.GetRequestId()
	if reqInfo, exists := lr.reqDispatchInfo[reqId]; exists {
		instId := reqInfo.instId
		if _, exists := lr.instRequestCounts[instId]; exists {
			lr.instRequestCounts[instId]--
		} else if _, exists := lr.unavailableReqCounts[instId]; exists {
			lr.unavailableReqCounts[instId]--
		}
		delete(lr.reqDispatchInfo, reqId)
	}
}

func (lr *LeastRequests) getRequestLoads() map[string]map[string]*scheduler.RequestLoadInfo {
	res := make(map[string]map[string]*scheduler.RequestLoadInfo)
	for reqId, reqInfo := range lr.reqDispatchInfo {
		instId := reqInfo.instId
		if _, exists := res[instId]; !exists {
			res[instId] = make(map[string]*scheduler.RequestLoadInfo)
		}
		res[instId][reqId] = &scheduler.RequestLoadInfo{
			LoadVal: float64(reqInfo.load),
			Desc:    "Request load is prompt length",
		}
	}
	return res
}

func (lr *LeastRequests) getInstLoadInfo(includeRequests bool, requestLoads map[string]map[string]*scheduler.RequestLoadInfo, available bool) []*scheduler.InstanceLoadInfo {
	res := make([]*scheduler.InstanceLoadInfo, 0)
	var counts map[string]int64
	if available {
		counts = lr.instRequestCounts
	} else {
		counts = lr.unavailableReqCounts
	}
	for instId, count := range counts {
		var desc string
		if available {
			desc = fmt.Sprintf("Request count: %d, max_num_seqs: %d", count, lr.getInstBatchSize(instId))
		} else {
			desc = fmt.Sprintf("Request count: %d", count)
		}
		loadVal := float64(0)
		if available {
			batchSize := lr.getInstBatchSize(instId)
			if batchSize > 0 {
				loadVal = float64(count) / float64(batchSize)
			}
		}
		loadInfo := &scheduler.InstanceLoadInfo{
			Id:      instId,
			LoadVal: loadVal,
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

func (lr *LeastRequests) ReportLoads(includeRequests bool, includeUnavailable bool) *scheduler.AllInstanceLoads {
	lr.mu.RLock()
	defer lr.mu.RUnlock()
	reqLoads := make(map[string]map[string]*scheduler.RequestLoadInfo)
	if includeRequests {
		reqLoads = lr.getRequestLoads()
	}
	availableInstLoads := lr.getInstLoadInfo(includeRequests, reqLoads, true)
	var unavailableInstLoads []*scheduler.InstanceLoadInfo
	if includeUnavailable {
		unavailableInstLoads = lr.getInstLoadInfo(includeRequests, reqLoads, false)
	}
	return &scheduler.AllInstanceLoads{
		Available:   availableInstLoads,
		Unavailable: unavailableInstLoads,
	}
}

func lrPrintInstLoad(sorted []lrInstCompare) string {
	builder := strings.Builder{}
	for _, inst := range sorted {
		builder.WriteString(fmt.Sprintf("<%s, req_count=%d, last_assigned=%d>, ", inst.instId, inst.requestCount, inst.lastAssignedTime))
	}
	return builder.String()
}
