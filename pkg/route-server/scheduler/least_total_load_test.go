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
	"net/http"
	"reflect"
	"strconv"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"

	"infer-dp-scheduler/pkg/common/constant"
	stats2 "infer-dp-scheduler/pkg/common/instance-metrics"
	"infer-dp-scheduler/pkg/common/utils"
)

func TestLeastTotalLoad_SingleNode(t *testing.T) {
	ltl := &LeastTotalLoad{}
	err := ltl.Init(make(map[string]string))
	if err != nil {
		t.Errorf("init error %v", err)
	}
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
	err = ltl.UpdateCluster(stats, nil)
	if err != nil {
		t.Errorf("update error %v", err)
	}
	if ltl.GetUpdateInterval() > 0 {
		t.Errorf("least-total-load return positive interval %v", ltl.GetUpdateInterval())
	}
	// Verify instBatchSize
	if ltl.instBatchSize["node1"] != 2 {
		t.Errorf("node %s batch size error, value = %d, expectValue = 2", "node1", ltl.instBatchSize["node1"])
	}

	req := &TestLeastTotalLoadReq{
		reqId:     "req_1",
		promptLen: 256,
	}
	expectTargetNode := "node1"
	expectTargetNodeLoad := []int64{1, 256}
	DispatchAndCheck(ltl, req, expectTargetNode, expectTargetNodeLoad, t)
}

func TestLeastTotalLoad_MultipleNode(t *testing.T) {
	ltl := &LeastTotalLoad{}
	err := ltl.Init(make(map[string]string))
	if err != nil {
		t.Errorf("init error %v", err)
	}
	stats := InitNodesStat()
	err = ltl.UpdateCluster(stats, nil)
	if err != nil {
		t.Errorf("update error %v", err)
	}
	if ltl.GetUpdateInterval() > 0 {
		t.Errorf("least-total-load return positive interval %v", ltl.GetUpdateInterval())
	}
	// Initialize load
	InitLoads(ltl)

	// Request 4 times
	promptLens := []int32{256, 128, 512, 64}
	expectTargetNodes := []string{"node1", "node2", "node3", "node2"}
	expectTargetNodesLoad := [][]int64{{2, 257}, {2, 130}, {2, 515}, {3, 194}}
	for i := 0; i < 4; i++ {
		reqId := "req_" + strconv.Itoa(i+4)
		req := &TestLeastTotalLoadReq{
			reqId:     reqId,
			promptLen: promptLens[i],
		}
		DispatchAndCheck(ltl, req, expectTargetNodes[i], expectTargetNodesLoad[i], t)
	}
}

func TestLeastTotalLoad_AddNode(t *testing.T) {
	ltl := &LeastTotalLoad{}
	err := ltl.Init(make(map[string]string))
	if err != nil {
		t.Errorf("init error %v", err)
	}
	stats := InitNodesStat()
	err = ltl.UpdateCluster(stats, nil)
	if err != nil {
		t.Errorf("update error %v", err)
	}
	if ltl.GetUpdateInterval() > 0 {
		t.Errorf("least-total-load return positive interval %v", ltl.GetUpdateInterval())
	}
	// Initialize load
	InitLoads(ltl)

	// Add a new node
	stats["node4"] = &stats2.ZeroCopyStats{
		Id:     "node4",
		Role:   "P",
		Status: stats2.Available,
		StaticInfo: map[string]string{
			"max_num_seqs": "2",
		},
	}
	err = ltl.UpdateCluster(stats, nil)
	if err != nil {
		t.Errorf("update cluster error %v", err)
	}

	// New node has minimum load, one request expected to dispatch to this node
	reqId := "req_4"
	req := &TestLeastTotalLoadReq{
		reqId:     reqId,
		promptLen: 1024,
	}
	expectTargetNode := "node4"
	expectTargetNodeLoad := []int64{1, 1024}
	DispatchAndCheck(ltl, req, expectTargetNode, expectTargetNodeLoad, t)
}

func TestLeastTotalLoad_ReduceNode(t *testing.T) {
	ltl := &LeastTotalLoad{}
	err := ltl.Init(make(map[string]string))
	if err != nil {
		t.Errorf("init error %v", err)
	}
	stats := InitNodesStat()
	err = ltl.UpdateCluster(stats, nil)
	if err != nil {
		t.Errorf("update error %v", err)
	}
	if ltl.GetUpdateInterval() > 0 {
		t.Errorf("least-total-load return positive interval %v", ltl.GetUpdateInterval())
	}
	// Initialize load
	InitLoads(ltl)

	// Remove node1 which has minimum load
	delete(stats, "node1")
	err = ltl.UpdateCluster(stats, nil)
	if err != nil {
		t.Errorf("update cluster error %v", err)
	}
	// Now node2 has minimum load, one request expected to dispatch to node2
	reqId := "req_4"
	req := &TestLeastTotalLoadReq{
		reqId:     reqId,
		promptLen: 1024,
	}
	expectTargetNode := "node2"
	expectTargetNodeLoad := []int64{2, 1026}
	DispatchAndCheck(ltl, req, expectTargetNode, expectTargetNodeLoad, t)
}

func TestLeastTotalLoad_OnRequestEvent(t *testing.T) {
	ltl := &LeastTotalLoad{}
	err := ltl.Init(make(map[string]string))
	if err != nil {
		t.Errorf("init error %v", err)
	}
	stats := InitNodesStat()
	err = ltl.UpdateCluster(stats, nil)
	if err != nil {
		t.Errorf("update error %v", err)
	}
	if ltl.GetUpdateInterval() > 0 {
		t.Errorf("least-total-load return positive interval %v", ltl.GetUpdateInterval())
	}
	// Initialize load
	InitLoads(ltl)
	err = ltl.UpdateCluster(stats, nil)
	if err != nil {
		t.Errorf("update cluster error %v", err)
	}
	// One request expected to dispatch to node1
	reqId := "req_4"
	req := &TestLeastTotalLoadReq{
		reqId:     reqId,
		promptLen: 1024,
	}
	expectTargetNode := "node1"
	expectTargetNodeLoad := []int64{2, 1025}
	DispatchAndCheck(ltl, req, expectTargetNode, expectTargetNodeLoad, t)

	// Verify node1 load after OnRequestEvent
	event := &utils.RequestEventMsg{
		Request:        utils.WrapRequest(req, false),
		TargetInstance: "",
		EventType:      constant.RequestPrefillSuccess,
	}
	ltl.OnRequestEvent(event)
	// Verify the hit node's load
	expectTargetNodeLoad = []int64{1, 1}
	if !reflect.DeepEqual(ltl.instLoads[expectTargetNode], expectTargetNodeLoad) {
		t.Errorf("target node load info is incorrect, value = %v, expectValue = %v",
			ltl.instLoads[expectTargetNode], expectTargetNodeLoad)
	}
	// Verify the mapping between request and dispatched instance
	if _, exists := ltl.reqDispatchInfo[reqId]; exists {
		t.Errorf("request id %s still in reqDispatchInfo after event done, reqDispatchInfo = %v",
			reqId, ltl.reqDispatchInfo)
	}
}

func TestLeastTotalLoad_OnRequestEventWithUnavailable(t *testing.T) {
	ltl := &LeastTotalLoad{}
	err := ltl.Init(make(map[string]string))
	if err != nil {
		t.Errorf("init error %v", err)
	}
	stats := InitNodesStat()
	err = ltl.UpdateCluster(stats, nil)
	if err != nil {
		t.Errorf("update error %v", err)
	}
	if ltl.GetUpdateInterval() > 0 {
		t.Errorf("least-total-load return positive interval %v", ltl.GetUpdateInterval())
	}
	// Initialize load
	InitLoads(ltl)
	_ = ltl.UpdateCluster(stats, nil)

	// One request expected to dispatch to node1
	reqId := "req_4"
	req := &TestLeastTotalLoadReq{
		reqId:     reqId,
		promptLen: 1024,
	}
	expectTargetNode := "node1"
	expectTargetNodeLoad := []int64{2, 1025}
	DispatchAndCheck(ltl, req, expectTargetNode, expectTargetNodeLoad, t)

	// Set node1 unavailable
	unavailable := map[string]stats2.WorkerStats{"node1": &stats2.ZeroCopyStats{}}
	availableStats := InitNodesStat()
	delete(availableStats, "node1")
	_ = ltl.UpdateCluster(availableStats, unavailable)

	// Verify node1 load after OnRequestEvent
	event := &utils.RequestEventMsg{
		Request:        utils.WrapRequest(req, false),
		TargetInstance: "",
		EventType:      constant.RequestPrefillSuccess,
	}
	ltl.OnRequestEvent(event)
	// Verify the hit node's load
	assert.EqualValues(t, 2, len(ltl.instLoads))
	assert.EqualValues(t, 1, len(ltl.unavailableInstLoads))
	assert.EqualValues(t, 3, len(ltl.reqDispatchInfo))
	assert.EqualValues(t, 2, len(ltl.instBatchSize))
	assert.EqualValues(t, []int64{1, 1}, ltl.unavailableInstLoads["node1"])
	assert.EqualValues(t, []int64{1, 2}, ltl.instLoads["node2"])
	assert.EqualValues(t, []int64{1, 3}, ltl.instLoads["node3"])

	// Verify the mapping between request and dispatched instance
	if _, exists := ltl.reqDispatchInfo[reqId]; exists {
		t.Errorf("request id %s still in reqDispatchInfo after event done, reqDispatchInfo = %v",
			reqId, ltl.reqDispatchInfo)
	}
}

func TestLeastTotalLoad_BatchSize(t *testing.T) {
	// When max_num_seqs exists in scheduler config and instance StaticInfo, batchSize is the instance value
	ltl := &LeastTotalLoad{}
	config := map[string]string{
		"max_num_seqs": "4",
	}
	err := ltl.Init(config)
	if err != nil {
		t.Errorf("init error %v", err)
	}
	if ltl.defaultBatchSize != 4 {
		t.Errorf("default batch size is incorrect, value = %d, expectValue = 4", ltl.defaultBatchSize)
	}

	stats := InitNodesStat()
	err = ltl.UpdateCluster(stats, nil)
	if err != nil {
		t.Errorf("update error %v", err)
	}
	// Verify node1 batchSize, node StaticInfo max_num_seqs value is 2, expected 2
	if batchSize, exists := ltl.instBatchSize["node1"]; exists {
		if batchSize != 2 {
			t.Errorf("node 'node1' batch size is incorrect, value = %d, expectValue = 2", batchSize)
		}
	} else {
		t.Errorf("instBatchSize does not contain node 'node1' info")
	}

	// When max_num_seqs exists in scheduler config but not in instance StaticInfo, batchSize is the scheduler config value
	ltl = &LeastTotalLoad{}
	ltl.Init(config)
	stats["node1"] = &stats2.ZeroCopyStats{
		Id:         "node1",
		Role:       "P",
		Status:     stats2.Available,
		StaticInfo: make(map[string]string),
	}
	err = ltl.UpdateCluster(stats, nil)
	if err != nil {
		t.Errorf("update error %v", err)
	}
	// Verify node1 batchSize
	if batchSize, exists := ltl.instBatchSize["node1"]; exists {
		if batchSize != 4 {
			t.Errorf("node 'node1' batch size is incorrect, value = %d, expectValue = 4", batchSize)
		}
	} else {
		t.Errorf("instBatchSize does not contain node 'node1' info")
	}

	// When max_num_seqs does not exist in scheduler config nor instance StaticInfo, batchSize uses default value 16
	ltl = &LeastTotalLoad{}
	ltl.Init(make(map[string]string))
	stats["node1"] = &stats2.ZeroCopyStats{
		Id:         "node1",
		Role:       "P",
		Status:     stats2.Available,
		StaticInfo: make(map[string]string),
	}
	err = ltl.UpdateCluster(stats, nil)
	if err != nil {
		t.Errorf("update error %v", err)
	}
	// Verify node1 batchSize
	if batchSize, exists := ltl.instBatchSize["node1"]; exists {
		if batchSize != 16 {
			t.Errorf("node 'node1' batch size is incorrect, value = %d, expectValue = 16", batchSize)
		}
	} else {
		t.Errorf("instBatchSize does not contain node 'node1' info")
	}
}

func TestLeastTotalLoad_ClearLoad(t *testing.T) {
	ltl := &LeastTotalLoad{}
	err := ltl.Init(make(map[string]string))
	if err != nil {
		t.Errorf("init error %v", err)
	}
	stats := InitNodesStat()
	err = ltl.UpdateCluster(stats, nil)
	if err != nil {
		t.Errorf("update error %v", err)
	}
	if ltl.GetUpdateInterval() > 0 {
		t.Errorf("least-total-load return positive interval %v", ltl.GetUpdateInterval())
	}
	// Initialize load
	InitLoads(ltl)
	// Call clear load, verify instLoads instances load are all 0, reqDispatchInfo empty, instBatchSize unchanged
	ltl.ClearLoad()
	if len(ltl.instLoads) != 3 {
		t.Errorf("the length of instLoads is incorrect, value = %d, expectValue = 3", len(ltl.instLoads))
	}
	if len(ltl.reqDispatchInfo) != 0 {
		t.Errorf("the length of reqDispatchInfo is incorrect, value = %d, expectValue = 0", len(ltl.reqDispatchInfo))
	}
	if len(ltl.instBatchSize) != 3 {
		t.Errorf("the length of instBatchSize is incorrect, value = %d, expectValue = 0", len(ltl.instBatchSize))
	}
	// Verify load is cleared
	nodes := []string{"node1", "node2", "node3"}
	expectTargetNodeLoad := []int64{0, 0}
	for _, node := range nodes {
		if !reflect.DeepEqual(ltl.instLoads[node], expectTargetNodeLoad) {
			t.Errorf("target node load info is incorrect, value = %v, expectValue = %v",
				ltl.instLoads[node], expectTargetNodeLoad)
		}
	}
}

func TestLeastTotalLoad_ClearLoadWithUnavailable(t *testing.T) {
	ltl := &LeastTotalLoad{}
	err := ltl.Init(make(map[string]string))
	if err != nil {
		t.Errorf("init error %v", err)
	}
	stats := InitNodesStat()
	err = ltl.UpdateCluster(stats, nil)
	if err != nil {
		t.Errorf("update error %v", err)
	}
	if ltl.GetUpdateInterval() > 0 {
		t.Errorf("least-total-load return positive interval %v", ltl.GetUpdateInterval())
	}
	// Initialize load
	InitLoads(ltl)
	// Set node1 unavailable
	unavailable := map[string]stats2.WorkerStats{"node1": &stats2.ZeroCopyStats{}}
	availableStats := InitNodesStat()
	delete(availableStats, "node1")
	_ = ltl.UpdateCluster(availableStats, unavailable)

	// Call clear load, verify instLoads instances load are all 0, reqDispatchInfo empty, instBatchSize unchanged, unavailableInstLoads empty
	ltl.ClearLoad()
	assert.EqualValues(t, 2, len(ltl.instLoads))
	assert.EqualValues(t, 0, len(ltl.unavailableInstLoads))
	assert.EqualValues(t, 0, len(ltl.reqDispatchInfo))
	assert.EqualValues(t, 2, len(ltl.instBatchSize))
	assert.EqualValues(t, []int64{0, 0}, ltl.instLoads["node2"])
	assert.EqualValues(t, []int64{0, 0}, ltl.instLoads["node3"])
}

func TestLeastTotalLoad_ClearLoadAndDispatch(t *testing.T) {
	ltl := &LeastTotalLoad{}
	err := ltl.Init(make(map[string]string))
	if err != nil {
		t.Errorf("init error %v", err)
	}
	stats := InitNodesStat()
	err = ltl.UpdateCluster(stats, nil)
	if err != nil {
		t.Errorf("update error %v", err)
	}
	if ltl.GetUpdateInterval() > 0 {
		t.Errorf("least-total-load return positive interval %v", ltl.GetUpdateInterval())
	}
	// Initialize load
	InitLoads(ltl)
	// Call clear load then send request, verify return result
	ltl.ClearLoad()
	reqId := "req_1"
	req := &TestLeastTotalLoadReq{
		reqId:     reqId,
		promptLen: 1024,
	}
	res, _, err := ltl.DispatchRequest(req, nil, nil)
	if err != nil {
		t.Errorf("dispatch failed %v", err)
	}
	if len(res) == 0 {
		t.Errorf("dispatch return empty node")
	}
	// Verify reqDispatchInfo has value
	reqDispatchInfo, exists := ltl.reqDispatchInfo[reqId]
	if !exists {
		t.Errorf("request id %s not in reqDispatchInfo, reqDispatchInfo = %v", reqId, ltl.reqDispatchInfo)
	}
	expectRes := reqDispatchInfo.instId
	if !reflect.DeepEqual(res, expectRes) {
		t.Errorf("dispatch return unexpected res, res = %v, expectRes = %v", res, expectRes)
	}
}

func TestLeastTotalLoad_ReportLoads_NotIncludeRequests(t *testing.T) {
	ltl := &LeastTotalLoad{}
	err := ltl.Init(make(map[string]string))
	if err != nil {
		t.Errorf("init error %v", err)
	}
	stats := InitNodesStat()
	err = ltl.UpdateCluster(stats, nil)
	if err != nil {
		t.Errorf("update error %v", err)
	}
	if ltl.GetUpdateInterval() > 0 {
		t.Errorf("least-total-load return positive interval %v", ltl.GetUpdateInterval())
	}
	// Initialize load
	InitLoads(ltl)
	// Query instance load info (excluding req load)
	res := ltl.ReportLoads(false, false)
	availableInstLoads := res.Available
	if len(availableInstLoads) != 3 {
		t.Errorf("reportLoads res length is incorrect, length = %v, expectLength = %v", len(availableInstLoads), 3)
	}
	for _, instLoadInfo := range availableInstLoads {
		instId := instLoadInfo.Id
		if instId == "node1" {
			if instLoadInfo.LoadVal != 1 {
				t.Errorf("instance 'node1' load value is incorrect, value = %v, expectValue = %v", instLoadInfo.LoadVal, 1)
			}
		} else if instId == "node2" {
			if instLoadInfo.LoadVal != 2 {
				t.Errorf("instance 'node2' load value is incorrect, value = %v, expectValue = %v", instLoadInfo.LoadVal, 2)
			}
		} else if instId == "node3" {
			if instLoadInfo.LoadVal != 3 {
				t.Errorf("instance 'node3' load value is incorrect, value = %v, expectValue = %v", instLoadInfo.LoadVal, 3)
			}
		} else {
			t.Errorf("instance %s is not expected in reportLoads res", instId)
		}
		// Should not return req load info
		if instLoadInfo.Requests != nil {
			t.Errorf("should not return instance %s requests loads info", instId)
		}
	}
}

func TestLeastTotalLoad_ReportLoads_IncludeRequests(t *testing.T) {
	ltl := &LeastTotalLoad{}
	err := ltl.Init(make(map[string]string))
	if err != nil {
		t.Errorf("init error %v", err)
	}
	stats := InitNodesStat()
	err = ltl.UpdateCluster(stats, nil)
	if err != nil {
		t.Errorf("update error %v", err)
	}
	if ltl.GetUpdateInterval() > 0 {
		t.Errorf("least-total-load return positive interval %v", ltl.GetUpdateInterval())
	}
	// Initialize load
	InitLoads(ltl)
	// Query instance load info (including req load)
	res := ltl.ReportLoads(true, false)
	availableInstLoads := res.Available
	if len(availableInstLoads) != 3 {
		t.Errorf("reportLoads res length is incorrect, length = %v, expectLength = %v", len(availableInstLoads), 3)
	}
	for _, instLoadInfo := range availableInstLoads {
		instId := instLoadInfo.Id
		// Request load should not be empty
		if instLoadInfo.Requests == nil {
			t.Errorf("instance '%s' request load should not be nil", instId)
		}
		reqLoadMap := instLoadInfo.Requests
		if instId == "node1" {
			if instLoadInfo.LoadVal != 1 {
				t.Errorf("instance '%s' load value is incorrect, value = %v, expectValue = %v", instId, instLoadInfo.LoadVal, 1)
			}
			if reqLoadInfo, exists := reqLoadMap["req_1"]; !exists {
				t.Errorf("instance '%s' reqLoadInfo is incorrect, value = %v, expect contains request '%s'", instId, reqLoadInfo, "req_1")
			} else {
				if reqLoadInfo.LoadVal != 1 {
					t.Errorf("instance '%s' request '%s' load value is incorrect, value = %v, expectValue = %v", instId, "req_1", reqLoadInfo.LoadVal, 1)
				}
			}
		} else if instId == "node2" {
			if instLoadInfo.LoadVal != 2 {
				t.Errorf("instance '%s' load value is incorrect, value = %v, expectValue = %v", instId, instLoadInfo.LoadVal, 2)
			}
			if reqLoadInfo, exists := reqLoadMap["req_2"]; !exists {
				t.Errorf("instance '%s' reqLoadInfo is incorrect, value = %v, expect contains request '%s'", instId, reqLoadInfo, "req_2")
			} else {
				if reqLoadInfo.LoadVal != 2 {
					t.Errorf("instance '%s' request '%s' load value is incorrect, value = %v, expectValue = %v", instId, "req_2", reqLoadInfo.LoadVal, 2)
				}
			}
		} else if instId == "node3" {
			if instLoadInfo.LoadVal != 3 {
				t.Errorf("instance '%s' load value is incorrect, value = %v, expectValue = %v", instId, instLoadInfo.LoadVal, 3)
			}
			if reqLoadInfo, exists := reqLoadMap["req_3"]; !exists {
				t.Errorf("instance '%s' reqLoadInfo is incorrect, value = %v, expect contains request '%s'", instId, reqLoadInfo, "req_3")
			} else {
				if reqLoadInfo.LoadVal != 3 {
					t.Errorf("instance '%s' request '%s' load value is incorrect, value = %v, expectValue = %v", instId, "req_3", reqLoadInfo.LoadVal, 3)
				}
			}
		} else {
			t.Errorf("instance %s is not expected in reportLoads res", instId)
		}
	}
}

func TestLeastTotalLoad_UpdateClusterWithUnavailable(t *testing.T) {
	ltl := &LeastTotalLoad{}
	err := ltl.Init(make(map[string]string))
	if err != nil {
		t.Errorf("init error %v", err)
	}
	stats := InitNodesStat()
	err = ltl.UpdateCluster(stats, nil)
	if err != nil {
		t.Errorf("update error %v", err)
	}
	if ltl.GetUpdateInterval() > 0 {
		t.Errorf("least-total-load return positive interval %v", ltl.GetUpdateInterval())
	}
	// Initialize load
	InitLoads(ltl)
	assert.EqualValues(t, 3, len(ltl.instLoads))
	assert.EqualValues(t, 0, len(ltl.unavailableInstLoads))
	assert.EqualValues(t, 3, len(ltl.reqDispatchInfo))
	assert.EqualValues(t, 3, len(ltl.instBatchSize))

	// Set node3 unavailable
	unavailable := map[string]stats2.WorkerStats{"node3": &stats2.ZeroCopyStats{}}
	availableStats := InitNodesStat()
	delete(availableStats, "node3")
	_ = ltl.UpdateCluster(availableStats, unavailable)
	assert.EqualValues(t, 2, len(ltl.instLoads))
	assert.EqualValues(t, 1, len(ltl.unavailableInstLoads))
	assert.EqualValues(t, 3, len(ltl.reqDispatchInfo))
	assert.EqualValues(t, 2, len(ltl.instBatchSize))
	assert.EqualValues(t, []int64{1, 1}, ltl.instLoads["node1"])
	assert.EqualValues(t, []int64{1, 2}, ltl.instLoads["node2"])
	assert.EqualValues(t, []int64{1, 3}, ltl.unavailableInstLoads["node3"])
	// Restore node3
	availableStats = InitNodesStat()
	_ = ltl.UpdateCluster(availableStats, nil)
	assert.EqualValues(t, 3, len(ltl.instLoads))
	assert.EqualValues(t, 0, len(ltl.unavailableInstLoads))
	assert.EqualValues(t, 3, len(ltl.reqDispatchInfo))
	assert.EqualValues(t, 3, len(ltl.instBatchSize))
	assert.EqualValues(t, []int64{1, 1}, ltl.instLoads["node1"])
	assert.EqualValues(t, []int64{1, 2}, ltl.instLoads["node2"])
	assert.EqualValues(t, []int64{1, 3}, ltl.instLoads["node3"])

	// Set node1, node2 unavailable
	unavailable = map[string]stats2.WorkerStats{"node2": &stats2.ZeroCopyStats{}, "node1": &stats2.ZeroCopyStats{}}
	availableStats = InitNodesStat()
	delete(availableStats, "node1")
	delete(availableStats, "node2")
	_ = ltl.UpdateCluster(availableStats, unavailable)
	assert.EqualValues(t, 1, len(ltl.instLoads))
	assert.EqualValues(t, 2, len(ltl.unavailableInstLoads))
	assert.EqualValues(t, 3, len(ltl.reqDispatchInfo))
	assert.EqualValues(t, 1, len(ltl.instBatchSize))
	assert.EqualValues(t, []int64{1, 1}, ltl.unavailableInstLoads["node1"])
	assert.EqualValues(t, []int64{1, 2}, ltl.unavailableInstLoads["node2"])
	assert.EqualValues(t, []int64{1, 3}, ltl.instLoads["node3"])
	// Restore node1
	unavailable = map[string]stats2.WorkerStats{"node2": &stats2.ZeroCopyStats{}}
	availableStats = InitNodesStat()
	delete(availableStats, "node2")
	_ = ltl.UpdateCluster(availableStats, unavailable)
	assert.EqualValues(t, 2, len(ltl.instLoads))
	assert.EqualValues(t, 1, len(ltl.unavailableInstLoads))
	assert.EqualValues(t, 3, len(ltl.reqDispatchInfo))
	assert.EqualValues(t, 2, len(ltl.instBatchSize))
	assert.EqualValues(t, []int64{1, 1}, ltl.instLoads["node1"])
	assert.EqualValues(t, []int64{1, 2}, ltl.unavailableInstLoads["node2"])
	assert.EqualValues(t, []int64{1, 3}, ltl.instLoads["node3"])
	// Restore node2 as well
	availableStats = InitNodesStat()
	_ = ltl.UpdateCluster(availableStats, nil)
	assert.EqualValues(t, 3, len(ltl.instLoads))
	assert.EqualValues(t, 0, len(ltl.unavailableInstLoads))
	assert.EqualValues(t, 3, len(ltl.reqDispatchInfo))
	assert.EqualValues(t, 3, len(ltl.instBatchSize))
	assert.EqualValues(t, []int64{1, 1}, ltl.instLoads["node1"])
	assert.EqualValues(t, []int64{1, 2}, ltl.instLoads["node2"])
	assert.EqualValues(t, []int64{1, 3}, ltl.instLoads["node3"])
}

func TestLeastTotalLoad_ConcurrentReq(t *testing.T) {
	ltl := &LeastTotalLoad{}
	err := ltl.Init(make(map[string]string))
	if err != nil {
		t.Errorf("init error %v", err)
	}
	stats := InitNodesStat()
	err = ltl.UpdateCluster(stats, nil)
	if err != nil {
		t.Errorf("update error %v", err)
	}

	// Start 1000 goroutines to test concurrent requests
	var wg sync.WaitGroup
	for i := 1; i <= 1000; i++ {
		wg.Add(1)
		req := &TestLeastTotalLoadReq{
			reqId:     "req_" + strconv.Itoa(i),
			promptLen: 1024,
		}
		go ConcurrentReqWorker(&wg, ltl, req)
	}
	// Wait for all goroutines to complete
	wg.Wait()

	// After completion, all loads should be 0, verify node1
	expectTargetNode := "node1"
	expectTargetNodeLoad := []int64{0, 0}
	if !reflect.DeepEqual(ltl.instLoads[expectTargetNode], expectTargetNodeLoad) {
		t.Errorf("target node %s load info is incorrect, value = %v, expectValue = %v",
			expectTargetNode, ltl.instLoads[expectTargetNode], expectTargetNodeLoad)
	}
}

func ConcurrentReqWorker(wg *sync.WaitGroup, ltl *LeastTotalLoad, req *TestLeastTotalLoadReq) {
	defer wg.Done() // Mark current goroutine as done
	ltl.DispatchRequest(req, nil, nil)
	event := &utils.RequestEventMsg{
		Request:        utils.WrapRequest(req, false),
		TargetInstance: "",
		EventType:      constant.RequestPrefillSuccess,
	}
	ltl.OnRequestEvent(event)
}

func InitLoads(ltl *LeastTotalLoad) {
	// Initialize load
	ltl.instLoads = map[string][]int64{
		"node1": {1, 1},
		"node2": {1, 2},
		"node3": {1, 3},
	}
	ltl.reqDispatchInfo = map[string]*requestInfo{
		"req_1": {
			instId: "node1",
			load:   1,
		},
		"req_2": {
			instId: "node2",
			load:   2,
		},
		"req_3": {
			instId: "node3",
			load:   3,
		},
	}
}

func InitNodesStat() map[string]stats2.WorkerStats {
	stats := map[string]stats2.WorkerStats{
		"node1": &stats2.ZeroCopyStats{
			Id:     "node1",
			Role:   "P",
			Status: stats2.Available,
			StaticInfo: map[string]string{
				"max_num_seqs": "2",
			},
		},
		"node2": &stats2.ZeroCopyStats{
			Id:     "node2",
			Role:   "P",
			Status: stats2.Available,
			StaticInfo: map[string]string{
				"max_num_seqs": "2",
			},
		},
		"node3": &stats2.ZeroCopyStats{
			Id:     "node3",
			Role:   "P",
			Status: stats2.Available,
			StaticInfo: map[string]string{
				"max_num_seqs": "2",
			},
		},
	}
	return stats
}

func DispatchAndCheck(ltl *LeastTotalLoad, req *TestLeastTotalLoadReq, expectTargetNode string,
	expectTargetNodeLoad []int64, t *testing.T) {
	reqId := req.reqId
	expectRes :=
		expectTargetNode

	res, _, err := ltl.DispatchRequest(req, nil, nil)
	if err != nil {
		t.Errorf("dispatch failed %v", err)
	}
	if !reflect.DeepEqual(res, expectRes) {
		t.Errorf("dispatch return unexpected res, res = %v, expectRes = %v", res, expectRes)
	}
	// Verify the hit node's load
	if !reflect.DeepEqual(ltl.instLoads[expectTargetNode], expectTargetNodeLoad) {
		t.Errorf("target node load info is incorrect, value = %v, expectValue = %v",
			ltl.instLoads[expectTargetNode], expectTargetNodeLoad)
	}
	// Verify the mapping between request and dispatched instance
	if _, exists := ltl.reqDispatchInfo[reqId]; !exists {
		t.Errorf("request id %s not in reqDispatchInfo, reqDispatchInfo = %v", reqId, ltl.reqDispatchInfo)
	}

	if ltl.reqDispatchInfo[reqId].instId != expectTargetNode {
		t.Errorf("unexpected reqId, instId in reqDispatchInfo, reqId = %s, instId = %s, expectInstId = %s",
			reqId, ltl.reqDispatchInfo[reqId].instId, expectTargetNode)
	}
	if ltl.reqDispatchInfo[reqId].load != int64(req.promptLen) {
		t.Errorf("unexpected reqId, load in reqDispatchInfo, reqId = %s, load = %d, expectLoad = %d",
			reqId, ltl.reqDispatchInfo[reqId].load, int64(req.promptLen))
	}
}

type TestLeastTotalLoadReq struct {
	reqId     string
	promptLen int32
}

func (l *TestLeastTotalLoadReq) GetApcStrategyType() string {
	return ""
}

func (l *TestLeastTotalLoadReq) GetPromptCacheKey() string {
	return ""
}

func (l *TestLeastTotalLoadReq) GetModel() string {
	return "test-model"
}

func (l *TestLeastTotalLoadReq) IsStream() bool {
	return true
}

func (l *TestLeastTotalLoadReq) GetChat() []utils.ChatContent {
	return make([]utils.ChatContent, 0)
}

func (l *TestLeastTotalLoadReq) GetPromptTokenCount() int32 {
	return l.promptLen
}

func (l *TestLeastTotalLoadReq) GetSLOTag() string {
	return constant.SLOTagDefault
}

func (l *TestLeastTotalLoadReq) GetSLOInterval() *utils.SLOInterval {
	return nil
}

func (l *TestLeastTotalLoadReq) GetPrompt() []utils.ChatContent {
	return make([]utils.ChatContent, 0)
}

func (l *TestLeastTotalLoadReq) GetMaxToken() int32 {
	return 5000
}

func (l *TestLeastTotalLoadReq) GetRequestId() string {
	return l.reqId
}

func (l *TestLeastTotalLoadReq) GetHttpRequest() *http.Request {
	return nil
}

func (l *TestLeastTotalLoadReq) GetType() string {
	return constant.Prefill
}

func (l *TestLeastTotalLoadReq) GetTokenCount() int32 {
	return 10
}
