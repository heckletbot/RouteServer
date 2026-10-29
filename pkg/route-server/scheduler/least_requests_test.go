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
	"reflect"
	"strconv"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"

	"infer-dp-scheduler/pkg/common/constant"
	stats2 "infer-dp-scheduler/pkg/common/instance-metrics"
	"infer-dp-scheduler/pkg/common/utils"
)

func lrInitNodesStat() map[string]stats2.WorkerStats {
	return map[string]stats2.WorkerStats{
		"node1": &stats2.ZeroCopyStats{
			Id:     "node1",
			Role:   "P",
			Status: stats2.Available,
			StaticInfo: map[string]string{
				"max_num_seqs": "4",
			},
		},
		"node2": &stats2.ZeroCopyStats{
			Id:     "node2",
			Role:   "P",
			Status: stats2.Available,
			StaticInfo: map[string]string{
				"max_num_seqs": "4",
			},
		},
		"node3": &stats2.ZeroCopyStats{
			Id:     "node3",
			Role:   "P",
			Status: stats2.Available,
			StaticInfo: map[string]string{
				"max_num_seqs": "4",
			},
		},
	}
}

func lrInitLoads(lr *LeastRequests) {
	lr.instRequestCounts = map[string]int64{
		"node1": 3,
		"node2": 1,
		"node3": 1,
	}
	lr.instLastAssignedTime = map[string]int64{
		"node1": 3000,
		"node2": 1000,
		"node3": 2000,
	}
	lr.reqDispatchInfo = map[string]*lrRequestInfo{
		"req_1": {instId: "node1", load: 100},
		"req_2": {instId: "node1", load: 200},
		"req_3": {instId: "node1", load: 300},
		"req_4": {instId: "node2", load: 400},
		"req_5": {instId: "node3", load: 500},
	}
}

func TestLeastRequests_SingleNode(t *testing.T) {
	lr := &LeastRequests{}
	err := lr.Init(make(map[string]string))
	assert.NoError(t, err)

	stats := map[string]stats2.WorkerStats{
		"node1": &stats2.ZeroCopyStats{
			Id:     "node1",
			Role:   "P",
			Status: stats2.Available,
			StaticInfo: map[string]string{
				"max_num_seqs": "2",
			},
		},
	}
	err = lr.UpdateCluster(stats, nil)
	assert.NoError(t, err)
	assert.Equal(t, int64(-1), lr.GetUpdateInterval())
	assert.Equal(t, int64(2), lr.instBatchSize["node1"])

	req := &TestLeastTotalLoadReq{reqId: "req_1", promptLen: 256}
	res, skipped, err := lr.DispatchRequest(req, nil, nil)
	assert.NoError(t, err)
	assert.False(t, skipped)
	assert.Equal(t, "node1", res)
	assert.Equal(t, int64(1), lr.instRequestCounts["node1"])
}

func TestLeastRequests_LeastRequestCount(t *testing.T) {
	lr := &LeastRequests{}
	err := lr.Init(make(map[string]string))
	assert.NoError(t, err)

	stats := lrInitNodesStat()
	err = lr.UpdateCluster(stats, nil)
	assert.NoError(t, err)
	lrInitLoads(lr)

	req := &TestLeastTotalLoadReq{reqId: "req_6", promptLen: 128}
	res, skipped, err := lr.DispatchRequest(req, nil, nil)
	assert.NoError(t, err)
	assert.False(t, skipped)
	assert.Equal(t, "node2", res)
	assert.Equal(t, int64(2), lr.instRequestCounts["node2"])
}

func TestLeastRequests_OldestFirstTieBreak(t *testing.T) {
	lr := &LeastRequests{}
	err := lr.Init(make(map[string]string))
	assert.NoError(t, err)

	stats := lrInitNodesStat()
	err = lr.UpdateCluster(stats, nil)
	assert.NoError(t, err)

	lr.instRequestCounts = map[string]int64{
		"node1": 1,
		"node2": 1,
		"node3": 2,
	}
	lr.instLastAssignedTime = map[string]int64{
		"node1": 1000,
		"node2": 2000,
		"node3": 3000,
	}
	lr.reqDispatchInfo = make(map[string]*lrRequestInfo)

	req := &TestLeastTotalLoadReq{reqId: "req_1", promptLen: 128}
	res, skipped, err := lr.DispatchRequest(req, nil, nil)
	assert.NoError(t, err)
	assert.False(t, skipped)
	assert.Equal(t, "node1", res)
	assert.Equal(t, int64(2), lr.instRequestCounts["node1"])
}

func TestLeastRequests_NewInstancePriority(t *testing.T) {
	lr := &LeastRequests{}
	err := lr.Init(make(map[string]string))
	assert.NoError(t, err)

	stats := lrInitNodesStat()
	err = lr.UpdateCluster(stats, nil)
	assert.NoError(t, err)

	lr.instRequestCounts = map[string]int64{
		"node1": 0,
		"node2": 0,
		"node3": 1,
	}
	lr.instLastAssignedTime = map[string]int64{
		"node1": 5000,
		"node2": 0,
		"node3": 3000,
	}
	lr.reqDispatchInfo = make(map[string]*lrRequestInfo)

	req := &TestLeastTotalLoadReq{reqId: "req_1", promptLen: 128}
	res, _, err := lr.DispatchRequest(req, nil, nil)
	assert.NoError(t, err)
	assert.Equal(t, "node2", res)
}

func TestLeastRequests_Blacklist(t *testing.T) {
	lr := &LeastRequests{}
	err := lr.Init(make(map[string]string))
	assert.NoError(t, err)

	stats := lrInitNodesStat()
	err = lr.UpdateCluster(stats, nil)
	assert.NoError(t, err)
	lrInitLoads(lr)

	blacklist := map[string]interface{}{"node2": struct{}{}}
	req := &TestLeastTotalLoadReq{reqId: "req_6", promptLen: 128}
	res, skipped, err := lr.DispatchRequest(req, nil, blacklist)
	assert.NoError(t, err)
	assert.True(t, skipped)
	assert.Equal(t, "node3", res)
	assert.Equal(t, int64(2), lr.instRequestCounts["node3"])
}

func TestLeastRequests_AllBlacklisted(t *testing.T) {
	lr := &LeastRequests{}
	err := lr.Init(make(map[string]string))
	assert.NoError(t, err)

	stats := lrInitNodesStat()
	err = lr.UpdateCluster(stats, nil)
	assert.NoError(t, err)

	blacklist := map[string]interface{}{
		"node1": struct{}{},
		"node2": struct{}{},
		"node3": struct{}{},
	}
	req := &TestLeastTotalLoadReq{reqId: "req_1", promptLen: 128}
	res, skipped, err := lr.DispatchRequest(req, nil, blacklist)
	assert.NoError(t, err)
	assert.True(t, skipped)
	assert.Empty(t, res)
}

func TestLeastRequests_NoAvailableInstances(t *testing.T) {
	lr := &LeastRequests{}
	err := lr.Init(make(map[string]string))
	assert.NoError(t, err)

	req := &TestLeastTotalLoadReq{reqId: "req_1", promptLen: 128}
	res, skipped, err := lr.DispatchRequest(req, nil, nil)
	assert.NoError(t, err)
	assert.False(t, skipped)
	assert.Empty(t, res)
}

func TestLeastRequests_OnRequestEvent(t *testing.T) {
	lr := &LeastRequests{}
	err := lr.Init(make(map[string]string))
	assert.NoError(t, err)

	stats := lrInitNodesStat()
	err = lr.UpdateCluster(stats, nil)
	assert.NoError(t, err)
	lrInitLoads(lr)

	req := &TestLeastTotalLoadReq{reqId: "req_4", promptLen: 400}
	event := &utils.RequestEventMsg{
		Request:        utils.WrapRequest(req, false),
		TargetInstance: "",
		EventType:      constant.RequestPrefillSuccess,
	}
	lr.OnRequestEvent(event)

	assert.Equal(t, int64(0), lr.instRequestCounts["node2"])
	assert.Equal(t, int64(3), lr.instRequestCounts["node1"])
	assert.Equal(t, int64(1), lr.instRequestCounts["node3"])
	_, exists := lr.reqDispatchInfo["req_4"]
	assert.False(t, exists)
}

func TestLeastRequests_OnRequestEventIgnoreNew(t *testing.T) {
	lr := &LeastRequests{}
	err := lr.Init(make(map[string]string))
	assert.NoError(t, err)

	stats := lrInitNodesStat()
	err = lr.UpdateCluster(stats, nil)
	assert.NoError(t, err)
	lrInitLoads(lr)

	req := &TestLeastTotalLoadReq{reqId: "req_4", promptLen: 400}
	event := &utils.RequestEventMsg{
		Request:        utils.WrapRequest(req, false),
		TargetInstance: "",
		EventType:      constant.RequestNew,
	}
	lr.OnRequestEvent(event)

	assert.Equal(t, int64(1), lr.instRequestCounts["node2"])
	_, exists := lr.reqDispatchInfo["req_4"]
	assert.True(t, exists)
}

func TestLeastRequests_OnRequestEventWithUnavailable(t *testing.T) {
	lr := &LeastRequests{}
	err := lr.Init(make(map[string]string))
	assert.NoError(t, err)

	stats := lrInitNodesStat()
	err = lr.UpdateCluster(stats, nil)
	assert.NoError(t, err)
	lrInitLoads(lr)

	unavailable := map[string]stats2.WorkerStats{"node2": &stats2.ZeroCopyStats{}}
	availableStats := lrInitNodesStat()
	delete(availableStats, "node2")
	_ = lr.UpdateCluster(availableStats, unavailable)

	assert.Equal(t, int64(1), lr.unavailableReqCounts["node2"])

	req := &TestLeastTotalLoadReq{reqId: "req_4", promptLen: 400}
	event := &utils.RequestEventMsg{
		Request:        utils.WrapRequest(req, false),
		TargetInstance: "",
		EventType:      constant.RequestPrefillSuccess,
	}
	lr.OnRequestEvent(event)

	assert.Equal(t, int64(0), lr.unavailableReqCounts["node2"])
	_, exists := lr.reqDispatchInfo["req_4"]
	assert.False(t, exists)
}

func TestLeastRequests_AddNode(t *testing.T) {
	lr := &LeastRequests{}
	err := lr.Init(make(map[string]string))
	assert.NoError(t, err)

	stats := lrInitNodesStat()
	err = lr.UpdateCluster(stats, nil)
	assert.NoError(t, err)
	lrInitLoads(lr)

	stats["node4"] = &stats2.ZeroCopyStats{
		Id:     "node4",
		Role:   "P",
		Status: stats2.Available,
		StaticInfo: map[string]string{
			"max_num_seqs": "4",
		},
	}
	err = lr.UpdateCluster(stats, nil)
	assert.NoError(t, err)

	assert.Equal(t, int64(0), lr.instRequestCounts["node4"])
	_, hasLastTime := lr.instLastAssignedTime["node4"]
	assert.False(t, hasLastTime)

	req := &TestLeastTotalLoadReq{reqId: "req_6", promptLen: 128}
	res, _, err := lr.DispatchRequest(req, nil, nil)
	assert.NoError(t, err)
	assert.Equal(t, "node4", res)
}

func TestLeastRequests_RemoveNode(t *testing.T) {
	lr := &LeastRequests{}
	err := lr.Init(make(map[string]string))
	assert.NoError(t, err)

	stats := lrInitNodesStat()
	err = lr.UpdateCluster(stats, nil)
	assert.NoError(t, err)
	lrInitLoads(lr)

	delete(stats, "node1")
	err = lr.UpdateCluster(stats, nil)
	assert.NoError(t, err)

	_, exists := lr.instRequestCounts["node1"]
	assert.False(t, exists)

	req := &TestLeastTotalLoadReq{reqId: "req_6", promptLen: 128}
	res, _, err := lr.DispatchRequest(req, nil, nil)
	assert.NoError(t, err)
	assert.Equal(t, "node2", res)
}

func TestLeastRequests_UpdateClusterWithUnavailable(t *testing.T) {
	lr := &LeastRequests{}
	err := lr.Init(make(map[string]string))
	assert.NoError(t, err)

	stats := lrInitNodesStat()
	err = lr.UpdateCluster(stats, nil)
	assert.NoError(t, err)
	lrInitLoads(lr)

	assert.Equal(t, 3, len(lr.instRequestCounts))
	assert.Equal(t, 0, len(lr.unavailableReqCounts))

	unavailable := map[string]stats2.WorkerStats{"node3": &stats2.ZeroCopyStats{}}
	availableStats := lrInitNodesStat()
	delete(availableStats, "node3")
	_ = lr.UpdateCluster(availableStats, unavailable)

	assert.Equal(t, 2, len(lr.instRequestCounts))
	assert.Equal(t, 1, len(lr.unavailableReqCounts))
	assert.Equal(t, int64(3), lr.instRequestCounts["node1"])
	assert.Equal(t, int64(1), lr.instRequestCounts["node2"])
	assert.Equal(t, int64(1), lr.unavailableReqCounts["node3"])

	availableStats = lrInitNodesStat()
	_ = lr.UpdateCluster(availableStats, nil)
	assert.Equal(t, 3, len(lr.instRequestCounts))
	assert.Equal(t, 0, len(lr.unavailableReqCounts))
	assert.Equal(t, int64(3), lr.instRequestCounts["node1"])
	assert.Equal(t, int64(1), lr.instRequestCounts["node2"])
	assert.Equal(t, int64(1), lr.instRequestCounts["node3"])
}

func TestLeastRequests_ClearLoad(t *testing.T) {
	lr := &LeastRequests{}
	err := lr.Init(make(map[string]string))
	assert.NoError(t, err)

	stats := lrInitNodesStat()
	err = lr.UpdateCluster(stats, nil)
	assert.NoError(t, err)
	lrInitLoads(lr)

	lr.ClearLoad()
	assert.Equal(t, 3, len(lr.instRequestCounts))
	assert.Equal(t, 0, len(lr.reqDispatchInfo))
	assert.Equal(t, 3, len(lr.instBatchSize))
	for _, count := range lr.instRequestCounts {
		assert.Equal(t, int64(0), count)
	}
	assert.Equal(t, 0, len(lr.instLastAssignedTime))
	assert.Equal(t, 0, len(lr.unavailableReqCounts))
}

func TestLeastRequests_ClearLoadAndDispatch(t *testing.T) {
	lr := &LeastRequests{}
	err := lr.Init(make(map[string]string))
	assert.NoError(t, err)

	stats := lrInitNodesStat()
	err = lr.UpdateCluster(stats, nil)
	assert.NoError(t, err)
	lrInitLoads(lr)

	lr.ClearLoad()
	req := &TestLeastTotalLoadReq{reqId: "req_1", promptLen: 1024}
	res, _, err := lr.DispatchRequest(req, nil, nil)
	assert.NoError(t, err)
	assert.NotEmpty(t, res)

	reqInfo, exists := lr.reqDispatchInfo["req_1"]
	assert.True(t, exists)
	assert.Equal(t, res, reqInfo.instId)
}

func TestLeastRequests_BatchSize(t *testing.T) {
	lr := &LeastRequests{}
	config := map[string]string{"max_num_seqs": "4"}
	err := lr.Init(config)
	assert.NoError(t, err)
	assert.Equal(t, int64(4), lr.defaultBatchSize)

	stats := lrInitNodesStat()
	err = lr.UpdateCluster(stats, nil)
	assert.NoError(t, err)
	assert.Equal(t, int64(4), lr.instBatchSize["node1"])

	lr2 := &LeastRequests{}
	lr2.Init(config)
	stats["node1"] = &stats2.ZeroCopyStats{
		Id:         "node1",
		Role:       "P",
		Status:     stats2.Available,
		StaticInfo: make(map[string]string),
	}
	err = lr2.UpdateCluster(stats, nil)
	assert.NoError(t, err)
	assert.Equal(t, int64(4), lr2.instBatchSize["node1"])

	lr3 := &LeastRequests{}
	lr3.Init(make(map[string]string))
	stats["node1"] = &stats2.ZeroCopyStats{
		Id:         "node1",
		Role:       "P",
		Status:     stats2.Available,
		StaticInfo: make(map[string]string),
	}
	err = lr3.UpdateCluster(stats, nil)
	assert.NoError(t, err)
	assert.Equal(t, int64(16), lr3.instBatchSize["node1"])
}

func TestLeastRequests_ReportLoads(t *testing.T) {
	lr := &LeastRequests{}
	err := lr.Init(make(map[string]string))
	assert.NoError(t, err)

	stats := lrInitNodesStat()
	err = lr.UpdateCluster(stats, nil)
	assert.NoError(t, err)
	lrInitLoads(lr)

	res := lr.ReportLoads(false, false)
	assert.Equal(t, 3, len(res.Available))
	assert.Nil(t, res.Unavailable)

	for _, instLoadInfo := range res.Available {
		assert.Nil(t, instLoadInfo.Requests)
		if instLoadInfo.Id == "node1" {
			assert.Equal(t, float64(3)/float64(4), instLoadInfo.LoadVal)
		} else if instLoadInfo.Id == "node2" {
			assert.Equal(t, float64(1)/float64(4), instLoadInfo.LoadVal)
		} else if instLoadInfo.Id == "node3" {
			assert.Equal(t, float64(1)/float64(4), instLoadInfo.LoadVal)
		}
	}

	resWithReqs := lr.ReportLoads(true, true)
	assert.Equal(t, 3, len(resWithReqs.Available))
	for _, instLoadInfo := range resWithReqs.Available {
		assert.NotNil(t, instLoadInfo.Requests)
	}
}

func TestLeastRequests_ConcurrentReq(t *testing.T) {
	lr := &LeastRequests{}
	err := lr.Init(make(map[string]string))
	assert.NoError(t, err)

	stats := lrInitNodesStat()
	err = lr.UpdateCluster(stats, nil)
	assert.NoError(t, err)

	var wg sync.WaitGroup
	for i := 1; i <= 1000; i++ {
		wg.Add(1)
		req := &TestLeastTotalLoadReq{
			reqId:     "req_" + strconv.Itoa(i),
			promptLen: 1024,
		}
		go func() {
			defer wg.Done()
			lr.DispatchRequest(req, nil, nil)
			event := &utils.RequestEventMsg{
				Request:        utils.WrapRequest(req, false),
				TargetInstance: "",
				EventType:      constant.RequestPrefillSuccess,
			}
			lr.OnRequestEvent(event)
		}()
	}
	wg.Wait()

	for _, count := range lr.instRequestCounts {
		assert.Equal(t, int64(0), count)
	}
}

func TestLeastRequests_GetName(t *testing.T) {
	lr := &LeastRequests{}
	assert.Equal(t, "least-requests", lr.GetName())
}

func TestLeastRequests_NeedStatsList(t *testing.T) {
	lr := &LeastRequests{}
	assert.Empty(t, lr.NeedStatsList())
}

func TestLeastRequests_UpdateConfig(t *testing.T) {
	lr := &LeastRequests{}
	err := lr.UpdateConfig(map[string]string{"key": "value"})
	assert.NoError(t, err)
}

func TestLeastRequests_ClusterNoChange(t *testing.T) {
	lr := &LeastRequests{}
	err := lr.Init(make(map[string]string))
	assert.NoError(t, err)

	stats := lrInitNodesStat()
	err = lr.UpdateCluster(stats, nil)
	assert.NoError(t, err)
	lrInitLoads(lr)

	beforeLoads := make(map[string]int64)
	for k, v := range lr.instRequestCounts {
		beforeLoads[k] = v
	}

	err = lr.UpdateCluster(stats, nil)
	assert.NoError(t, err)

	assert.True(t, reflect.DeepEqual(beforeLoads, lr.instRequestCounts))
}

func TestLeastRequests_Registration(t *testing.T) {
	algo, err := GetNewSchedulerAlgo(LeastRequestsAlgoName)
	assert.NoError(t, err)
	assert.Equal(t, "least-requests", algo.GetName())
}

func TestLeastRequests_DispatchUpdatesLastAssignedTime(t *testing.T) {
	lr := &LeastRequests{}
	err := lr.Init(make(map[string]string))
	assert.NoError(t, err)

	stats := lrInitNodesStat()
	err = lr.UpdateCluster(stats, nil)
	assert.NoError(t, err)

	req1 := &TestLeastTotalLoadReq{reqId: "req_1", promptLen: 100}
	res1, _, err := lr.DispatchRequest(req1, nil, nil)
	assert.NoError(t, err)
	assert.NotEmpty(t, res1)
	firstTime := lr.instLastAssignedTime[res1]
	assert.Greater(t, firstTime, int64(0))

	req2 := &TestLeastTotalLoadReq{reqId: "req_2", promptLen: 100}
	res2, _, err := lr.DispatchRequest(req2, nil, nil)
	assert.NoError(t, err)
	assert.NotEmpty(t, res2)
	if res2 == res1 {
		assert.Greater(t, lr.instLastAssignedTime[res2], firstTime)
	}
}
