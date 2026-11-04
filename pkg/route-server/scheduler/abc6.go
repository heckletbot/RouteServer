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

package scheduler

import (
	"fmt"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	monitor_client "route-server/pkg/common/client/monitor-client"
	"route-server/pkg/common/constant"
	"route-server/pkg/common/instance-metrics"
	"route-server/pkg/common/logger"
	"route-server/pkg/common/scheduler"
	"route-server/pkg/common/utils"
)

const (
	TokenLoads               = 0
	WaitingQueue             = 1
	RunningQueue             = 2
	OverloadPunishment       = 1280000
	DefaultOverdueIntervalMs = 1000 // 1s
	TimeToForceUpdateSecond  = 5
	ABCLoadTypeNum           = 3
)

// ABC6 NOTE:
// 1. manager.DispatchRequest() invokes UpdateCluster to updates workers stats(including loads information)
// 2. the update intervals is quite long, so during the interval, we should maintain the worker loads information manually
// 3. OnRequestEvent() is invoked when manager finishes processing the request
type ABC6 struct {
	// Only assigned during Init, read-only afterwards
	defaultBatchSize         int64
	updateMetricsIntervalMs  int64
	overdueMetricsIntervalMs time.Duration

	// NOTE: The following are critical variables, thread-safe
	workerNum atomic.Int32

	mu               sync.RWMutex
	DPLoads          map[string][]int64   // {id: [token, waiting, running]}, DPLoads stores worker load information
	unavailable      map[string][]int64   // {id: [token, waiting, running]}, stashes unavailable instance loads
	workerFlushedMap map[string]bool      // {id: flushed}, tracks if worker loads have been flushed
	instBatchSize    map[string]int64     // {id: batchSize}, stores batch size for each instance
	workerLastUpdate map[string]time.Time // {id: lastUpdate}

	reqDispatchInfo map[string]*abc6RequestInfo // {reqId: instance id information}
}

type compareLoad struct {
	instId string
	load   int64
}

type abc6RequestInfo struct {
	instId       string
	load         int64
	sortLoadType int
}

func (a *ABC6) Init(config map[string]string) error {
	if maxNumSeqs, exists := config[constant.MaxNumSeqs]; exists {
		num, err := strconv.ParseInt(maxNumSeqs, 10, 64)
		if err == nil {
			a.defaultBatchSize = num
		}
	}
	if a.defaultBatchSize <= 0 {
		a.defaultBatchSize = 16
	}

	if intervalMs, exists := config[constant.UpdateIntervalMs]; exists {
		interval, err := strconv.ParseInt(intervalMs, 10, 64)
		if err == nil {
			a.updateMetricsIntervalMs = interval
		}
	}
	if a.updateMetricsIntervalMs <= 0 {
		a.updateMetricsIntervalMs = -1
	}

	if overdueMetricsIntervalMs, exists := config[constant.MetricsOverdueIntervalMs]; exists {
		val, err := strconv.ParseInt(overdueMetricsIntervalMs, 10, 64)
		if err == nil {
			a.overdueMetricsIntervalMs = time.Duration(val) * time.Millisecond
		}
	}
	if a.overdueMetricsIntervalMs <= 0 {
		a.overdueMetricsIntervalMs = time.Duration(DefaultOverdueIntervalMs) * time.Millisecond
	}

	a.DPLoads = make(map[string][]int64)
	a.workerFlushedMap = make(map[string]bool)
	a.instBatchSize = make(map[string]int64)
	a.workerLastUpdate = make(map[string]time.Time)
	a.reqDispatchInfo = make(map[string]*abc6RequestInfo)
	a.unavailable = make(map[string][]int64)

	return nil
}

func (a *ABC6) getInstBatchSize(instanceId string) int64 {
	batchSize := a.defaultBatchSize
	if instBatchSize, exists := a.instBatchSize[instanceId]; exists {
		batchSize = instBatchSize
	}
	return batchSize
}

func (a *ABC6) getInstLoadInfo(includeRequests bool, requestLoads map[string]map[string]*scheduler.RequestLoadInfo, available bool) []*scheduler.InstanceLoadInfo {
	res := make([]*scheduler.InstanceLoadInfo, 0)
	var instLoadsCopy map[string][]int64
	if available {
		instLoadsCopy = a.DPLoads
	} else {
		instLoadsCopy = a.unavailable
	}
	for instId, load := range instLoadsCopy {
		var desc string
		if available {
			desc = fmt.Sprintf("PromptTokens+GenerationTokens: %d, Running Requests Queue: %d, "+
				"Waiting Requests Queue: %d, max_num_seqs: %d", load[TokenLoads], load[RunningQueue], load[WaitingQueue], a.getInstBatchSize(instId))
		} else {
			desc = fmt.Sprintf("PromptTokens+GenerationTokens: %d, Running Requests Queue: %d, "+
				"Waiting Requests Queue: %d", load[TokenLoads], load[RunningQueue], load[WaitingQueue])
		}
		loadInfo := &scheduler.InstanceLoadInfo{
			Id:      instId,
			LoadVal: a.computeSingleLoads(instId),
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

func (a *ABC6) ReportLoads(includeRequests bool, includeUnavailable bool) *scheduler.AllInstanceLoads {
	a.mu.RLock()
	defer a.mu.RUnlock()
	reqLoads := make(map[string]map[string]*scheduler.RequestLoadInfo, len(a.DPLoads))
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

func (a *ABC6) getRequestLoads() map[string]map[string]*scheduler.RequestLoadInfo {
	res := make(map[string]map[string]*scheduler.RequestLoadInfo)
	for reqId, reqInfo := range a.reqDispatchInfo {
		// Get req load and dispatched instance id from reqInfo
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

func (a *ABC6) GetName() string {
	return ABC6AlgoName
}

func (a *ABC6) NeedStatsList() []string {
	return []string{
		monitor_client.NumRequestsWaiting,
		monitor_client.NumRequestsRunning,
		monitor_client.PromptTokensTotal,
		monitor_client.GenerationTokensTotal,
	}
}

func (a *ABC6) ClearLoad() {
	a.mu.Lock()
	defer a.mu.Unlock()

	logger.Log().Debugf("Begin clear load")
	a.reqDispatchInfo = make(map[string]*abc6RequestInfo)

	instLoadsCopy := make(map[string][]int64, len(a.DPLoads))
	for instId := range a.DPLoads {
		instLoadsCopy[instId] = []int64{0, 0, 0}
		a.workerLastUpdate[instId] = time.Time{}
		// set flushed true to stop from reduceLoad()
		a.workerFlushedMap[instId] = true
	}
	a.DPLoads = instLoadsCopy

	unavailableCopy := make(map[string][]int64, len(a.unavailable))
	for instId := range a.unavailable {
		unavailableCopy[instId] = []int64{0, 0, 0}
	}
	a.unavailable = unavailableCopy
}

func (a *ABC6) UpdateCluster(available map[string]instance_metrics.WorkerStats, unavailable map[string]instance_metrics.WorkerStats) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	newWorkerNum := len(available)
	instBatchSizeCopy := make(map[string]int64, newWorkerNum)
	DPLoadsCopy := make(map[string][]int64, newWorkerNum)
	workerFlushedMapCopy := make(map[string]bool, newWorkerNum)
	workerLastUpdateCopy := make(map[string]time.Time, newWorkerNum)
	reqDispatchInfoCopy := make(map[string]*abc6RequestInfo, len(a.reqDispatchInfo))

	a.workerNum.Store(int32(newWorkerNum))

	// 1. Write latest statistics
	for id, stat := range available {
		batchSize := a.defaultBatchSize
		if zcs, ok := stat.(*instance_metrics.ZeroCopyStats); ok {
			if maxNumSeqs, exists := zcs.StaticInfo[constant.MaxNumSeqs]; exists {
				if num, err := strconv.ParseInt(maxNumSeqs, 10, 64); err == nil && num > 0 {
					batchSize = num
				}
			}
		}
		instBatchSizeCopy[id] = batchSize

		// if id doesn't exist in current instances map, it may be a new instance or a recovered instance from unavailable state.
		if _, exists := a.DPLoads[id]; !exists {
			// restore from stashed loads
			if _, exists := a.unavailable[id]; exists {
				load := make([]int64, ABCLoadTypeNum)
				copy(load, a.unavailable[id])
				DPLoadsCopy[id] = load
				delete(a.unavailable, id)
			} else {
				DPLoadsCopy[id] = []int64{0, 0, 0}
			}
			workerLastUpdateCopy[id] = time.Time{}
		} else {
			load := make([]int64, ABCLoadTypeNum)
			copy(load, a.DPLoads[id])
			DPLoadsCopy[id] = load
			workerLastUpdateCopy[id] = a.workerLastUpdate[id]
		}

		if a.collectLoadsFromStats(DPLoadsCopy, workerLastUpdateCopy, id, stat) {
			workerFlushedMapCopy[id] = true
		} else {
			workerFlushedMapCopy[id] = false
		}
	}

	// stash loads of unavailable instances
	for id := range unavailable {
		if _, exists := a.DPLoads[id]; exists {
			load := make([]int64, ABCLoadTypeNum)
			copy(load, a.DPLoads[id])
			a.unavailable[id] = load
		} else if _, exists := a.unavailable[id]; !exists {
			// Not in a.DPLoads and not in a.unavailable, initialize
			a.unavailable[id] = make([]int64, ABCLoadTypeNum)
		}
	}

	for reqId, reqInfo := range a.reqDispatchInfo {
		if _, exists := available[reqInfo.instId]; exists {
			reqDispatchInfoCopy[reqId] = reqInfo
		}
		if _, exists := a.unavailable[reqInfo.instId]; exists {
			reqDispatchInfoCopy[reqId] = reqInfo
		}
	}

	a.instBatchSize = instBatchSizeCopy
	a.DPLoads = DPLoadsCopy
	a.workerFlushedMap = workerFlushedMapCopy
	a.workerLastUpdate = workerLastUpdateCopy
	a.reqDispatchInfo = reqDispatchInfoCopy

	logger.Log().Debugf("Update instance loads info successfully, workerNum: %d", a.workerNum.Load())
	return nil
}

// compute the load of each worker from the metrics
func (a *ABC6) collectLoadsFromStats(DPLoads map[string][]int64, workerLastUpdate map[string]time.Time, id string, stat instance_metrics.WorkerStats) bool {
	if stat.GetNumericStat(monitor_client.PromptTokensTotal) == nil ||
		stat.GetNumericStat(monitor_client.GenerationTokensTotal) == nil ||
		stat.GetNumericStat(monitor_client.NumRequestsWaiting) == nil ||
		stat.GetNumericStat(monitor_client.NumRequestsRunning) == nil {
		logger.Log().Warnf("[%v] algorithm: metrics are inadequate for instance %s", a.GetName(), id)
		return false
	}

	curTime := time.Now()
	interval := curTime.Sub(stat.GetTimeStamp())
	if interval > a.overdueMetricsIntervalMs {
		logger.Log().Warnf("[%v] algorithm got outdated metric for instance [%s], metric lagged time [%v] , drop it", a.GetName(), id, interval)
		return false
	}

	newTokenLoads := int64(stat.GetNumericStat(monitor_client.PromptTokensTotal)[0].Value + stat.GetNumericStat(monitor_client.GenerationTokensTotal)[0].Value)
	newWaiting := int64(stat.GetNumericStat(monitor_client.NumRequestsWaiting)[0].Value)
	newRunning := int64(stat.GetNumericStat(monitor_client.NumRequestsRunning)[0].Value)

	loads := DPLoads[id]
	lastUpdate := workerLastUpdate[id]

	// Whether force update is needed
	needForce := loads[RunningQueue] > 0 && newRunning == 0 && curTime.Sub(lastUpdate) > TimeToForceUpdateSecond*time.Second

	if loads[WaitingQueue] != newWaiting ||
		loads[RunningQueue] != newRunning ||
		(loads[TokenLoads] == newTokenLoads && newWaiting == 0 && newRunning == 1) ||
		needForce {
		DPLoads[id] = []int64{newTokenLoads, newWaiting, newRunning}
	}

	workerLastUpdate[id] = curTime
	return true
}

// External needs read lock
func (a *ABC6) isInstanceOverloaded(id string) bool {
	loads, exists := a.DPLoads[id]
	if !exists {
		return false
	}
	batch, exists := a.instBatchSize[id]
	if !exists || batch <= 0 {
		batch = a.defaultBatchSize
	}
	isOverloaded := loads[WaitingQueue] >= 2 || loads[RunningQueue] >= batch-1
	return isOverloaded
}

// selectWorkerByLoad selects the workers with the top-k least load, and compensates the load for top-1 worker temporarily,
// but after OnRequestEvent is called, we need to check the real final chosen worker(details in OnRequestEvent),
// that means if the final chosen worker is not the one we compensated for, we need to minus the load for top-1 worker we once compensated,
// and compensates the load for real final chosen worker.
func (a *ABC6) getSortedWorkersByLoad() ([]compareLoad, int) {
	var compareLoads []compareLoad

	a.mu.RLock()
	defer a.mu.RUnlock()
	// 1. Choose which load type to sort by based on whether all are overloaded
	sortLoadType := a.selectSortLoadType()

	// 2. Sort by corresponding load
	compareLoads = a.compareAndSortLoads(sortLoadType)

	return compareLoads, sortLoadType
}

// External needs read lock
// Determines if all are overloaded, if yes use WaitingQueue sort, otherwise use TokenLoads sort
func (a *ABC6) selectSortLoadType() int {
	allOverloaded := true
	for id := range a.DPLoads {
		if !a.isInstanceOverloaded(id) {
			allOverloaded = false
			break
		}
	}

	if allOverloaded {
		return WaitingQueue
	} else {
		return TokenLoads
	}
}

// External needs read lock
// sort the workers by type of load
func (a *ABC6) compareAndSortLoads(sortLoadType int) []compareLoad {
	var list []compareLoad
	for id, loads := range a.DPLoads {
		l := loads[sortLoadType]
		// If using TokenLoads as sorting criteria and current node is overloaded, apply punishment value
		if sortLoadType == TokenLoads && a.isInstanceOverloaded(id) {
			l += OverloadPunishment
		}
		list = append(list, compareLoad{
			instId: id,
			load:   l,
		})
	}
	sort.Slice(list, func(i, j int) bool {
		return list[i].load < list[j].load
	})
	return list
}

// External needs read lock
func (a *ABC6) computeSingleLoads(instId string) float64 {
	sortLoadType := a.selectSortLoadType()

	if _, exists := a.DPLoads[instId]; exists {
		l := a.DPLoads[instId][sortLoadType]
		if sortLoadType == TokenLoads && a.isInstanceOverloaded(instId) {
			l += OverloadPunishment
		}
		return float64(l)
	} else if _, exists := a.unavailable[instId]; exists {
		l := a.unavailable[instId][TokenLoads]
		return float64(l)
	} else {
		return 0
	}
}

// NOTE: we just compensate for the first one of sortedWorkers, the final chosen worker will be returned
// in OnRequestEvent, maybe it's different from the first one, but it doesn't matter, we just need to minus
// the predicted loads from the first worker.
func (a *ABC6) addLoads(instanceId string, request utils.GenerateRequest, sortLoadType int) {
	a.mu.Lock()
	defer a.mu.Unlock()

	reqId := request.GetRequestId()
	reqLen := request.GetPromptTokenCount()

	loads, exists := a.DPLoads[instanceId]
	if !exists {
		stashedLoads, exists := a.unavailable[instanceId]
		// if instance id not exist in DPLoads and unavailable, init its loads
		if !exists {
			if sortLoadType == TokenLoads {
				a.DPLoads[instanceId] = []int64{int64(reqLen), 0, 1}
			} else {
				a.DPLoads[instanceId] = []int64{0, 1, 0}
			}
		} else {
			addLoads(stashedLoads, int64(reqLen), sortLoadType)
		}
	} else {
		addLoads(loads, int64(reqLen), sortLoadType)
	}

	a.workerFlushedMap[instanceId] = false
	// Update request's dispatched instance and length load
	a.reqDispatchInfo[reqId] = &abc6RequestInfo{
		instId:       instanceId,
		load:         int64(reqLen),
		sortLoadType: sortLoadType,
	}
}

func (a *ABC6) reduceLoads(request utils.GenerateRequest) {
	a.mu.Lock()
	defer a.mu.Unlock()

	reqId := request.GetRequestId()
	var reqLen int64

	var instId string
	var sortLoadType int
	if info, exists := a.reqDispatchInfo[reqId]; !exists {
		return
	} else {
		reqLen = info.load
		instId = info.instId
		sortLoadType = info.sortLoadType
	}
	delete(a.reqDispatchInfo, reqId)

	// Skip if already flushed by synced metrics
	if flushed, ok := a.workerFlushedMap[instId]; ok && flushed {
		return
	}

	loads, exists := a.DPLoads[instId]
	if !exists {
		stashedLoads, exists := a.unavailable[instId]
		if exists {
			reduceLoads(stashedLoads, reqLen, sortLoadType)
		}
		return
	}

	reduceLoads(loads, reqLen, sortLoadType)
}

func addLoads(loads []int64, reqLen int64, sortLoadType int) {
	if sortLoadType == TokenLoads {
		loads[TokenLoads] += reqLen
		loads[RunningQueue] += 1
	} else {
		loads[WaitingQueue] += 1
	}
}

func reduceLoads(loads []int64, reqLen int64, sortLoadType int) {
	if sortLoadType == TokenLoads {
		loads[TokenLoads] -= reqLen
		loads[RunningQueue] -= 1
		if loads[TokenLoads] < 0 {
			loads[TokenLoads] = 0
		}
		if loads[RunningQueue] < 0 {
			loads[RunningQueue] = 0
		}
	} else {
		loads[WaitingQueue] -= 1
		if loads[WaitingQueue] < 0 {
			loads[WaitingQueue] = 0
		}
	}
}

// DispatchRequest Dispatches request
func (a *ABC6) DispatchRequest(request utils.GenerateRequest, params map[string]string, insBlackMap map[string]interface{}) (string, bool, error) {
	if a.workerNum.Load() == 0 {
		return "", false, nil
	}

	sortedWorkers, loadType := a.getSortedWorkersByLoad()
	skipped := false
	for idx := 0; idx < len(sortedWorkers); idx++ {
		if insBlackMap != nil {
			_, exist := insBlackMap[sortedWorkers[idx].instId]
			if exist {
				skipped = true
				continue
			}
		}
		a.addLoads(sortedWorkers[idx].instId, request, loadType)
		return sortedWorkers[idx].instId, skipped, nil
	}
	return "", skipped, nil
}

func (a *ABC6) OnRequestEvent(event *utils.RequestEventMsg) {
	// as we add predicted loads manually, we need to reduce loads when manager finishes processing the request
	// see comment notes in addLoads
	if event.EventType != constant.RequestNew {
		a.reduceLoads(event.Request)
	}
}

func (a *ABC6) UpdateConfig(config map[string]string) error {
	return nil
}

func (a *ABC6) GetUpdateInterval() int64 {
	return a.updateMetricsIntervalMs
}
