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
	"math/rand"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	monitor_client "route-server/pkg/common/client/monitor-client"
	"route-server/pkg/common/constant"
	"route-server/pkg/common/instance-metrics"
	"route-server/pkg/common/logger"
	"route-server/pkg/common/scheduler"
	"route-server/pkg/common/utils"
)

// mockGenerateRequest is a mock implementation of GenerateRequest for testing.
type mockGenerateRequest struct {
	reqId        string
	promptTokens int32
}

func (m *mockGenerateRequest) IsStream() bool {
	return true
}

func (m *mockGenerateRequest) GetRequestId() string {
	return m.reqId
}

func (m *mockGenerateRequest) GetPromptTokenCount() int32 {
	return m.promptTokens
}

func (m *mockGenerateRequest) GetChat() []utils.ChatContent {
	return make([]utils.ChatContent, 0)
}

func (m *mockGenerateRequest) GetSLOTag() string { return constant.SLOTagDefault }

func (m *mockGenerateRequest) GetSLOInterval() *utils.SLOInterval { return nil }

func (m *mockGenerateRequest) GetPrompt() []utils.ChatContent {
	return make([]utils.ChatContent, 0)
}

func (m *mockGenerateRequest) GetMaxToken() int32 {
	return 5000
}

func (m *mockGenerateRequest) GetHttpRequest() *http.Request {
	return nil
}

func (m *mockGenerateRequest) GetType() string {
	return constant.Decode
}

func (m *mockGenerateRequest) GetTokenCount() int32 {
	return 10
}

func (m *mockGenerateRequest) GetApcStrategyType() string {
	return ""
}

func (m *mockGenerateRequest) GetModel() string {
	return "test-model"
}

func (m *mockGenerateRequest) GetPromptCacheKey() string {
	return ""
}

// contains checks if a slice contains a specific element.
func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}

// createWorkerStats creates a mock WorkerStats for testing.
func createWorkerStats(id, role, maxNumSeqs string, waiting, running, promptTokens, genTokens float64) instance_metrics.WorkerStats {
	return &instance_metrics.ZeroCopyStats{
		Id:   id,
		Role: role,
		NumericStats: &map[string][]*instance_metrics.MetricInfo{
			monitor_client.NumRequestsWaiting:    {{MetricName: monitor_client.NumRequestsWaiting, Value: waiting}},
			monitor_client.NumRequestsRunning:    {{MetricName: monitor_client.NumRequestsRunning, Value: running}},
			monitor_client.PromptTokensTotal:     {{MetricName: monitor_client.PromptTokensTotal, Value: promptTokens}},
			monitor_client.GenerationTokensTotal: {{MetricName: monitor_client.GenerationTokensTotal, Value: genTokens}},
		},
		Status: instance_metrics.Available,
		StaticInfo: map[string]string{
			constant.MaxNumSeqs: maxNumSeqs,
		},
		TimeStamp: time.Now(),
	}
}

// createIncompleteWorkerStats creates a mock WorkerStats for testing.
func createIncompleteWorkerStats(id, role, maxNumSeqs string) instance_metrics.WorkerStats {
	return &instance_metrics.ZeroCopyStats{
		Id:           id,
		Role:         role,
		NumericStats: &map[string][]*instance_metrics.MetricInfo{},
		Status:       instance_metrics.Available,
		StaticInfo: map[string]string{
			constant.MaxNumSeqs: maxNumSeqs,
		},
		TimeStamp: time.Now(),
	}
}

// setupABC6 initializes an ABC6 instance for testing.
func setupABC6(maxNumSeqs int, workers map[string]instance_metrics.WorkerStats, unavailable []string) (*ABC6, error) {
	a := &ABC6{
		DPLoads:          make(map[string][]int64),
		reqDispatchInfo:  make(map[string]*abc6RequestInfo),
		workerFlushedMap: make(map[string]bool),
		instBatchSize:    make(map[string]int64),
		workerLastUpdate: make(map[string]time.Time),
	}
	config := map[string]string{
		constant.MaxNumSeqs: strconv.Itoa(maxNumSeqs),
	}
	if err := a.Init(config); err != nil {
		return nil, err
	}
	// Create unavailable workers map
	unavailableMap := make(map[string]instance_metrics.WorkerStats)
	for _, id := range unavailable {
		if worker, exists := workers[id]; exists {
			unavailableMap[id] = worker
		}
	}
	if err := a.UpdateCluster(workers, unavailableMap); err != nil {
		return nil, err
	}
	return a, nil
}

// TestDispatchRequest tests the DispatchRequest method.
func TestDispatchRequest(t *testing.T) {
	tests := []struct {
		name                     string
		workers                  map[string]instance_metrics.WorkerStats
		maxNumSeqs               int
		overdueMetricsIntervalMs int
		promptTokens             int32
		candidateNum             uint32
		expectedWorker           []string
	}{
		{
			name: "Single worker",
			workers: map[string]instance_metrics.WorkerStats{
				"worker1": createWorkerStats("worker1", "decode", "10", 0, 1, 100, 50),
			},
			maxNumSeqs: 10,

			promptTokens:   100,
			candidateNum:   1,
			expectedWorker: []string{"worker1"},
		},
		{
			name: "All workers overloaded",
			workers: map[string]instance_metrics.WorkerStats{
				"worker1": createWorkerStats("worker1", "decode", "10", 2, 9, 200, 100),
				"worker2": createWorkerStats("worker2", "decode", "10", 3, 9, 300, 150),
				"worker3": createWorkerStats("worker3", "decode", "10", 2, 9, 250, 120),
			},
			maxNumSeqs:     10,
			promptTokens:   100,
			candidateNum:   1,
			expectedWorker: []string{"worker1", "worker3"}, // WQNumLoads = 2
		},
		{
			name: "Some workers not overloaded",
			workers: map[string]instance_metrics.WorkerStats{
				"worker1": createWorkerStats("worker1", "decode", "10", 1, 1, 100, 50),
				"worker2": createWorkerStats("worker2", "decode", "10", 2, 9, 300, 150),
				"worker3": createWorkerStats("worker3", "decode", "10", 0, 1, 50, 25),
			},
			maxNumSeqs:     10,
			promptTokens:   100,
			candidateNum:   1,
			expectedWorker: []string{"worker3"}, // TokenLoadss = 75
		},
		{
			name: "Multiple workers with equal loads",
			workers: map[string]instance_metrics.WorkerStats{
				"worker1": createWorkerStats("worker1", "decode", "10", 1, 1, 100, 50),
				"worker2": createWorkerStats("worker2", "decode", "10", 1, 1, 100, 50),
				"worker3": createWorkerStats("worker3", "decode", "10", 2, 9, 300, 150),
			},
			maxNumSeqs:     10,
			promptTokens:   100,
			candidateNum:   1,
			expectedWorker: []string{"worker1", "worker2"}, // TokenLoadss = 150
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := &mockGenerateRequest{
				reqId:        "123",
				promptTokens: tt.promptTokens,
			}

			a, err := setupABC6(tt.maxNumSeqs, tt.workers, nil)
			if err != nil {
				t.Errorf("setupABC6() error = %v", err)
			}

			chosenID, skipped, _ := a.DispatchRequest(request, nil, nil)

			if skipped == true {
				t.Errorf("Dispathch Alg didn't skip any instance, bug get skipped true")
			}

			if !contains(tt.expectedWorker, chosenID) {
				t.Errorf("DispatchRequest() chosenID %s not in expected %v", chosenID, tt.expectedWorker)
			}

			expectChosenID := tt.expectedWorker[0]

			// Check if all workers are overloaded
			isAllOverloaded := true
			for key, loads := range a.DPLoads {
				batchSize, _ := a.instBatchSize[key]
				if loads[WaitingQueue] < 2 && loads[RunningQueue] < batchSize-1 {
					isAllOverloaded = false
				}
			}

			if isAllOverloaded {
				// Verify WaitingQueue increase
				loads, _ := a.DPLoads[chosenID]
				expectedWQ := int64(tt.workers[expectChosenID].GetNumericStat(monitor_client.NumRequestsWaiting)[0].Value) + 1
				if loads[WaitingQueue] != expectedWQ {
					t.Errorf("DPLoads[%s][WaitingQueue] = %d, want %d", expectChosenID, loads[WaitingQueue], expectedWQ)
				}
			} else {
				// Verify TokenLoadss and RunningQueues
				loads, _ := a.DPLoads[chosenID]
				expectedDP := int64(
					tt.workers[expectChosenID].GetNumericStat(monitor_client.PromptTokensTotal)[0].Value+
						tt.workers[expectChosenID].GetNumericStat(monitor_client.GenerationTokensTotal)[0].Value) +
					int64(tt.promptTokens)
				if loads[TokenLoads] != expectedDP {
					t.Errorf("DPLoads[%s][TokenLoads] = %d, want %d", expectChosenID, loads[TokenLoads], expectedDP)
				}
				expectedRQ := int64(tt.workers[expectChosenID].GetNumericStat(monitor_client.NumRequestsRunning)[0].Value) + 1
				if loads[RunningQueue] != expectedRQ {
					t.Errorf("DPLoads[%s][RunningQueue] = %d, want %d", expectChosenID, loads[RunningQueue], expectedRQ)
				}
			}
		})
	}
}

// TestUpdateCluster tests the UpdateCluster method.
func TestUpdateCluster(t *testing.T) {
	tests := []struct {
		name              string
		initialWorkers    map[string]instance_metrics.WorkerStats
		newWorkers        map[string]instance_metrics.WorkerStats
		maxNumSeqs        int
		expectedWorkerNum int
		expectedError     string
	}{
		{
			name:              "Empty cluster",
			initialWorkers:    map[string]instance_metrics.WorkerStats{},
			newWorkers:        map[string]instance_metrics.WorkerStats{},
			maxNumSeqs:        128,
			expectedWorkerNum: 0,
			expectedError:     "",
		},
		{
			name:           "Single worker",
			initialWorkers: map[string]instance_metrics.WorkerStats{},
			newWorkers: map[string]instance_metrics.WorkerStats{
				"worker1": createWorkerStats("worker1", "decode", "10", 0, 1, 100, 50),
			},
			maxNumSeqs:        128,
			expectedWorkerNum: 1,
			expectedError:     "",
		},
		{
			name: "Add workers",
			initialWorkers: map[string]instance_metrics.WorkerStats{
				"worker1": createWorkerStats("worker1", "decode", "10", 0, 1, 100, 50),
			},
			newWorkers: map[string]instance_metrics.WorkerStats{
				"worker1": createWorkerStats("worker1", "decode", "10", 1, 2, 200, 50),
				"worker2": createWorkerStats("worker2", "decode", "10", 0, 1, 50, 25),
			},
			maxNumSeqs:        128,
			expectedWorkerNum: 2,
			expectedError:     "",
		},
		{
			name: "Remove workers",
			initialWorkers: map[string]instance_metrics.WorkerStats{
				"worker1": createWorkerStats("worker1", "decode", "10", 0, 1, 100, 50),
				"worker2": createWorkerStats("worker2", "decode", "10", 0, 1, 50, 25),
			},
			newWorkers: map[string]instance_metrics.WorkerStats{
				"worker1": createWorkerStats("worker1", "decode", "10", 1, 2, 200, 50),
			},
			maxNumSeqs:        128,
			expectedWorkerNum: 1,
			expectedError:     "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, err := setupABC6(tt.maxNumSeqs, tt.initialWorkers, nil)
			if err != nil {
				t.Errorf("setupABC6() error = %v", err)
			}

			err = a.UpdateCluster(tt.newWorkers, nil)

			if tt.expectedError == "" {
				if err != nil {
					t.Errorf("UpdateCluster() error = %v, want nil", err)
				}
			} else {
				if err == nil || !strings.Contains(err.Error(), tt.expectedError) {
					t.Errorf("UpdateCluster() error = %v, want %v", err, tt.expectedError)
				}
			}

			if int(a.workerNum.Load()) != tt.expectedWorkerNum {
				t.Errorf("UpdateCluster() workerNum = %d, want %d", a.workerNum.Load(), tt.expectedWorkerNum)
			}

			if tt.expectedError == "" && tt.expectedWorkerNum > 0 {
				assert.EqualValues(t, tt.expectedWorkerNum, a.workerNum.Load())
				for key, _ := range a.DPLoads {
					if _, exists := tt.newWorkers[key]; !exists {
						t.Errorf("UpdateCluster() DPLoads contains unexpected worker %s", key)
					}
				}
			}
		})
	}
}

// TestConcurrentUpdateAndDispatch tests concurrent DispatchRequest and OnRequestEvent.
func TestUpdateClusterWithUnavailable(t *testing.T) {
	var unavailableWorkers map[string]instance_metrics.WorkerStats
	request := &mockGenerateRequest{
		reqId:        "123",
		promptTokens: 100,
	}

	// 3 workers ready
	workers := map[string]instance_metrics.WorkerStats{
		"worker1": createWorkerStats("worker1", "decode", "10", 1, 10, 10, 50),
		"worker2": createWorkerStats("worker2", "decode", "10", 3, 10, 10, 50),
		"worker3": createWorkerStats("worker3", "decode", "10", 5, 10, 10, 50),
	}

	a, _ := setupABC6(128, workers, nil)
	assert.EqualValues(t, 3, len(a.DPLoads))
	assert.EqualValues(t, 0, len(a.unavailable))
	// compute loads using stats
	assert.Equal(t, []int64{60, 1, 10}, a.DPLoads["worker1"])
	assert.Equal(t, []int64{60, 3, 10}, a.DPLoads["worker2"])
	assert.Equal(t, []int64{60, 5, 10}, a.DPLoads["worker3"])

	chosenID, _, _ := a.DispatchRequest(request, nil, nil)
	logger.Log().Infof("Dispatch to [%v]", chosenID)
	assert.EqualValues(t, "worker1", chosenID)
	assert.Equal(t, []int64{60, 2, 10}, a.DPLoads["worker1"])
	assert.Equal(t, []int64{60, 3, 10}, a.DPLoads["worker2"])
	assert.Equal(t, []int64{60, 5, 10}, a.DPLoads["worker3"])

	// set worker1, worker2 unavailable
	// worker3 stats is incomplete, so keep using predict value
	workers = map[string]instance_metrics.WorkerStats{
		"worker3": createIncompleteWorkerStats("worker3", "decode", "10"),
	}
	unavailableWorkers = map[string]instance_metrics.WorkerStats{
		"worker1": createWorkerStats("worker1", "decode", "10", 1, 10, 10, 50),
		"worker2": createWorkerStats("worker2", "decode", "10", 3, 10, 10, 50),
	}
	_ = a.UpdateCluster(workers, unavailableWorkers)
	assert.EqualValues(t, 1, len(a.DPLoads))
	assert.EqualValues(t, 2, len(a.unavailable))
	assert.Equal(t, []int64{60, 5, 10}, a.DPLoads["worker3"])

	chosenID, _, _ = a.DispatchRequest(request, nil, nil)
	logger.Log().Infof("Dispatch to [%v]", chosenID)
	assert.EqualValues(t, "worker3", chosenID)
	assert.Equal(t, []int64(nil), a.DPLoads["worker1"])
	assert.Equal(t, []int64(nil), a.DPLoads["worker2"])
	assert.Equal(t, []int64{60, 6, 10}, a.DPLoads["worker3"])
	assert.Equal(t, []int64{60, 2, 10}, a.unavailable["worker1"])
	assert.Equal(t, []int64{60, 3, 10}, a.unavailable["worker2"])
	assert.Equal(t, []int64(nil), a.unavailable["worker3"])

	// restore worker1
	// worker1, worker3 keeps using predict value
	workers = map[string]instance_metrics.WorkerStats{
		"worker1": createIncompleteWorkerStats("worker1", "decode", "10"),
		"worker3": createIncompleteWorkerStats("worker3", "decode", "10"),
	}
	unavailableWorkers = map[string]instance_metrics.WorkerStats{
		"worker2": createWorkerStats("worker2", "decode", "10", 3, 10, 10, 50),
	}
	_ = a.UpdateCluster(workers, unavailableWorkers)
	assert.EqualValues(t, 2, len(a.DPLoads))
	assert.EqualValues(t, 1, len(a.unavailable))
	assert.Equal(t, []int64{60, 2, 10}, a.DPLoads["worker1"])
	assert.Equal(t, []int64(nil), a.DPLoads["worker2"])
	assert.Equal(t, []int64{60, 6, 10}, a.DPLoads["worker3"])

	chosenID, _, _ = a.DispatchRequest(request, nil, nil)
	logger.Log().Infof("Dispatch to [%v]", chosenID)
	assert.EqualValues(t, "worker1", chosenID)
	assert.Equal(t, []int64{60, 3, 10}, a.DPLoads["worker1"])
	assert.Equal(t, []int64(nil), a.DPLoads["worker2"])
	assert.Equal(t, []int64{60, 6, 10}, a.DPLoads["worker3"])
	assert.Equal(t, []int64(nil), a.unavailable["worker1"])
	assert.Equal(t, []int64{60, 3, 10}, a.unavailable["worker2"])
	assert.Equal(t, []int64(nil), a.unavailable["worker3"])

	// set worker1 unavailable
	// now reduce worker1 (currently unavailable) loads
	workers = map[string]instance_metrics.WorkerStats{
		"worker3": createIncompleteWorkerStats("worker3", "decode", "10"),
		"worker4": createIncompleteWorkerStats("worker4", "decode", "10"),
	}
	unavailableWorkers = map[string]instance_metrics.WorkerStats{
		"worker1": createWorkerStats("worker1", "decode", "10", 1, 10, 10, 50),
	}
	_ = a.UpdateCluster(workers, unavailableWorkers)
	assert.Equal(t, []int64(nil), a.DPLoads["worker1"])
	assert.Equal(t, []int64{0, 0, 0}, a.DPLoads["worker4"])
	assert.Equal(t, []int64{60, 3, 10}, a.unavailable["worker1"])
	a.reduceLoads(request)
	assert.Equal(t, []int64(nil), a.DPLoads["worker1"])
	assert.Equal(t, []int64{0, 0, 0}, a.DPLoads["worker4"])
	assert.Equal(t, []int64{60, 2, 10}, a.unavailable["worker1"])
}

// TestConcurrentUpdateAndDispatch tests concurrent DispatchRequest and OnRequestEvent.
func TestClearLoadAndDispatch(t *testing.T) {
	workers := map[string]instance_metrics.WorkerStats{
		"worker1": createWorkerStats("worker1", "decode", "10", 10, 10, 10, 50),
		"worker2": createWorkerStats("worker2", "decode", "10", 10, 10, 10, 50),
		"worker3": createWorkerStats("worker3", "decode", "10", 10, 10, 10, 50),
	}
	a, err := setupABC6(128, workers, nil)
	if err != nil {
		t.Errorf("setupABC6() error = %v", err)
	}

	// Start 1000 goroutines to test concurrent requests
	startTime := time.Now()
	var wg sync.WaitGroup
	for i := 1; i <= 10000; i++ {
		wg.Add(1)
		req := &mockGenerateRequest{
			reqId:        "req_" + strconv.Itoa(i),
			promptTokens: 1024,
		}
		go concurrentReqWorker(&wg, a, req)
	}
	// Wait for all goroutines to complete
	wg.Wait()
	elapsed := time.Since(startTime)
	t.Logf("Processed 10000 requests in %v", elapsed)

	// Verify all loads are reset to {10, 10, 50}
	expectTargetNodeLoad := []int64{60, 10, 10}
	for key, value := range a.DPLoads {
		if !reflect.DeepEqual(value, expectTargetNodeLoad) {
			t.Errorf("target node %s load info is incorrect, value = %v, expectValue = %v",
				key, value, expectTargetNodeLoad)
		}
	}

	curTime := time.Now()
	a.workerFlushedMap["worker1"] = true
	a.workerLastUpdate["worker1"] = curTime

	load, _ := a.DPLoads["worker1"]
	assert.Equal(t, load, []int64{60, 10, 10})
	load, _ = a.DPLoads["worker2"]
	assert.Equal(t, load, []int64{60, 10, 10})
	load, _ = a.DPLoads["worker3"]
	assert.Equal(t, load, []int64{60, 10, 10})

	ct, exist := a.workerLastUpdate["worker1"]
	assert.Equal(t, ct, curTime)
	assert.Equal(t, exist, true)

	flushed, exist := a.workerFlushedMap["worker1"]
	assert.Equal(t, flushed, true)
	assert.Equal(t, exist, true)

	// after ClearLoad, instBatchSize remain same,
	// DPLoads,workerLastUpdateTime,workerFlushedMap will be reset, but not remain nil
	a.ClearLoad()

	assert.Equal(t, int(a.workerNum.Load()), len(workers))

	bs, exist := a.instBatchSize["worker1"]
	assert.Equal(t, int(bs), 10)
	assert.Equal(t, exist, true)

	load, _ = a.DPLoads["worker1"]
	assert.Equal(t, load, []int64{0, 0, 0})
	load, _ = a.DPLoads["worker2"]
	assert.Equal(t, load, []int64{0, 0, 0})
	load, _ = a.DPLoads["worker3"]
	assert.Equal(t, load, []int64{0, 0, 0})

	res, exist := a.workerLastUpdate["worker1"]
	assert.Equal(t, exist, true)
	assert.Equal(t, res, time.Time{})

	flushed, exist = a.workerFlushedMap["worker1"]
	assert.Equal(t, flushed, true)
	assert.Equal(t, exist, true)

	// ensure Dispatch function working well after ClearLoad()
	startTime = time.Now()
	for i := 1; i <= 10000; i++ {
		wg.Add(1)
		req := &mockGenerateRequest{
			reqId:        "req_" + strconv.Itoa(i),
			promptTokens: 1024,
		}
		go concurrentReqWorker(&wg, a, req)
	}
	// Wait for all goroutines to complete
	wg.Wait()
	elapsed = time.Since(startTime)
	t.Logf("Processed 10000 requests in %v", elapsed)

	expectTargetNodeLoad = []int64{0, 0, 0}
	for key, value := range a.DPLoads {
		if !reflect.DeepEqual(value, expectTargetNodeLoad) {
			t.Errorf("target node %s load info is incorrect, value = %v, expectValue = %v",
				key, value, expectTargetNodeLoad)
		}
	}
}

func TestComputeLoads(t *testing.T) {
	a := &ABC6{
		DPLoads:                  make(map[string][]int64),
		workerLastUpdate:         make(map[string]time.Time),
		overdueMetricsIntervalMs: 10 * time.Second,
	}
	a.DPLoads["worker1"] = []int64{0, 0, 0}
	stats := &instance_metrics.ZeroCopyStats{
		NumericStats: &map[string][]*instance_metrics.MetricInfo{
			monitor_client.PromptTokensTotal:     {{Value: 50}},
			monitor_client.GenerationTokensTotal: {{Value: 50}},
			monitor_client.NumRequestsWaiting:    {{Value: 0}},
			monitor_client.NumRequestsRunning:    {{Value: 1}},
		},
		TimeStamp: time.Now(),
	}

	// Test case 1: Normal load update
	res := a.collectLoadsFromStats(a.DPLoads, a.workerLastUpdate, "worker1", stats)
	assert.EqualValues(t, true, res)

	if loads, exists := a.DPLoads["worker1"]; exists {
		if loads[TokenLoads] != 100 || loads[WaitingQueue] != 0 || loads[RunningQueue] != 1 {
			t.Errorf("computeLoads() DPLoads[worker1] = %v, expected [100 0 1]", loads)
		}
	} else {
		t.Errorf("computeLoads() failed to update DPLoads for worker1")
	}

	// Test case 2: Inadequate stats
	statsInadequate := &instance_metrics.ZeroCopyStats{
		NumericStats: &map[string][]*instance_metrics.MetricInfo{
			monitor_client.PromptTokensTotal:     {{Value: 50}},
			monitor_client.GenerationTokensTotal: {{Value: 50}},
		},
		TimeStamp: time.Now(),
	}
	res = a.collectLoadsFromStats(a.DPLoads, a.workerLastUpdate, "worker1", statsInadequate)
	assert.EqualValues(t, false, res)

	// Test case 3: Outdated stats
	statsOutdated := &instance_metrics.ZeroCopyStats{
		NumericStats: &map[string][]*instance_metrics.MetricInfo{
			monitor_client.PromptTokensTotal:     {{Value: 50}},
			monitor_client.GenerationTokensTotal: {{Value: 50}},
			monitor_client.NumRequestsWaiting:    {{Value: 0}},
			monitor_client.NumRequestsRunning:    {{Value: 1}},
		},
		TimeStamp: time.Now().Add(-10 * time.Minute),
	}
	res = a.collectLoadsFromStats(a.DPLoads, a.workerLastUpdate, "worker1", statsOutdated)
	assert.EqualValues(t, false, res)

	// Test case 4: Force update when RunningQueue transitions to 0 after TimeToForceUpdateSecond
	a.workerLastUpdate["worker2"] = time.Now().Add(-10 * time.Second) // Last update 10 seconds ago
	a.DPLoads["worker2"] = []int64{100, 0, 1}                         // Previous state with RunningQueue > 0
	statsForceUpdate := &instance_metrics.ZeroCopyStats{
		NumericStats: &map[string][]*instance_metrics.MetricInfo{
			monitor_client.PromptTokensTotal:     {{Value: 50}},
			monitor_client.GenerationTokensTotal: {{Value: 50}},
			monitor_client.NumRequestsWaiting:    {{Value: 0}},
			monitor_client.NumRequestsRunning:    {{Value: 0}}, // Now RunningQueue = 0
		},
		TimeStamp: time.Now(),
	}
	// TimeToForceUpdateSecond is 5 seconds
	res = a.collectLoadsFromStats(a.DPLoads, a.workerLastUpdate, "worker2", statsForceUpdate)
	assert.EqualValues(t, true, res)

	if loads, exists := a.DPLoads["worker2"]; exists {
		if loads[TokenLoads] != 100 || loads[WaitingQueue] != 0 || loads[RunningQueue] != 0 {
			t.Errorf("computeLoads() DPLoads[worker2] = %v, expected [100 0 0]", loads)
		}
	} else {
		t.Errorf("computeLoads() failed to update DPLoads for worker2")
	}

	// Test case 5: No force update when time since last update is less than TimeToForceUpdateSecond
	a.workerLastUpdate["worker3"] = time.Now().Add(-3 * time.Second) // Last update 3 seconds ago
	a.DPLoads["worker3"] = []int64{200, 0, 0}                        // Previous state with TokenLoadss = 200
	statsNoForceUpdate := &instance_metrics.ZeroCopyStats{
		NumericStats: &map[string][]*instance_metrics.MetricInfo{
			monitor_client.PromptTokensTotal:     {{Value: 50}},
			monitor_client.GenerationTokensTotal: {{Value: 50}}, // TokenLoadss = 100, but do not refresh
			monitor_client.NumRequestsWaiting:    {{Value: 0}},
			monitor_client.NumRequestsRunning:    {{Value: 0}},
		},
		TimeStamp: time.Now(),
	}
	res = a.collectLoadsFromStats(a.DPLoads, a.workerLastUpdate, "worker3", statsNoForceUpdate)
	assert.EqualValues(t, true, res)

	if loads, exists := a.DPLoads["worker3"]; exists {
		if loads[TokenLoads] != 200 || loads[WaitingQueue] != 0 || loads[RunningQueue] != 0 {
			t.Errorf("computeLoads() DPLoads[worker3] = %v, expected [200 0 0] (no update)", loads)
		}
	} else {
		t.Errorf("computeLoads() failed to load DPLoads for worker3")
	}

	// Test case 6: No last update time exists
	a.DPLoads["worker4"] = []int64{100, 0, 1}
	statsNoLastUpdate := &instance_metrics.ZeroCopyStats{
		NumericStats: &map[string][]*instance_metrics.MetricInfo{
			monitor_client.PromptTokensTotal:     {{Value: 50}},
			monitor_client.GenerationTokensTotal: {{Value: 50}},
			monitor_client.NumRequestsWaiting:    {{Value: 0}},
			monitor_client.NumRequestsRunning:    {{Value: 1}},
		},
		TimeStamp: time.Now(),
	}
	res = a.collectLoadsFromStats(a.DPLoads, a.workerLastUpdate, "worker4", statsNoLastUpdate)
	assert.EqualValues(t, true, res) // Returns true because loads are unchanged
}

// TestGetSortedWorkersByLoad tests the getSortedWorkersByLoad method.
func TestGetSortedWorkersByLoad(t *testing.T) {
	a := &ABC6{
		DPLoads:          make(map[string][]int64),
		reqDispatchInfo:  make(map[string]*abc6RequestInfo),
		workerFlushedMap: make(map[string]bool),
		instBatchSize:    make(map[string]int64),
		workerLastUpdate: make(map[string]time.Time),
		defaultBatchSize: 10,
	}
	a.DPLoads["worker1"] = []int64{0, 0, 0}
	a.DPLoads["worker2"] = []int64{0, 0, 0}
	a.instBatchSize["worker1"] = int64(10)
	a.instBatchSize["worker2"] = int64(10)

	// Test all overloaded case
	a.DPLoads["worker1"] = []int64{100, 2, 9}
	a.DPLoads["worker2"] = []int64{200, 3, 9}
	sortedWorkers, loadType := a.getSortedWorkersByLoad()
	assert.EqualValues(t, WaitingQueue, loadType)
	assert.EqualValues(t, "worker1", sortedWorkers[0].instId)
	assert.EqualValues(t, 2, sortedWorkers[0].load)
	assert.EqualValues(t, "worker2", sortedWorkers[1].instId)
	assert.EqualValues(t, 3, sortedWorkers[1].load)

	// Test some overloaded case
	a.DPLoads["worker1"] = []int64{100, 1, 5}
	a.DPLoads["worker2"] = []int64{200, 3, 9}
	sortedWorkers, loadType = a.getSortedWorkersByLoad()
	assert.EqualValues(t, TokenLoads, loadType)
	assert.EqualValues(t, "worker1", sortedWorkers[0].instId)
	assert.EqualValues(t, 100, sortedWorkers[0].load)
	assert.EqualValues(t, "worker2", sortedWorkers[1].instId)
	assert.EqualValues(t, 1280200, sortedWorkers[1].load)

	// Test none overloaded case
	a.DPLoads["worker1"] = []int64{100, 1, 5}
	a.DPLoads["worker2"] = []int64{200, 1, 5}
	sortedWorkers, loadType = a.getSortedWorkersByLoad()
	assert.EqualValues(t, TokenLoads, loadType)
	assert.EqualValues(t, "worker1", sortedWorkers[0].instId)
	assert.EqualValues(t, 100, sortedWorkers[0].load)
	assert.EqualValues(t, "worker2", sortedWorkers[1].instId)
	assert.EqualValues(t, 200, sortedWorkers[1].load)
}

// TestAddLoads tests the addLoads method.
func TestAddLoads(t *testing.T) {
	a := &ABC6{
		DPLoads:          make(map[string][]int64),
		reqDispatchInfo:  make(map[string]*abc6RequestInfo),
		workerFlushedMap: make(map[string]bool),
		instBatchSize:    make(map[string]int64),
		workerLastUpdate: make(map[string]time.Time),
	}
	a.DPLoads["worker1"] = []int64{100, 1, 2}
	a.DPLoads["worker2"] = []int64{100, 10, 10}
	a.instBatchSize["worker1"] = int64(10)
	a.instBatchSize["worker2"] = int64(10)

	request := &mockGenerateRequest{
		reqId:        "req1",
		promptTokens: 100,
	}

	// Test TokenLoads
	a.addLoads("worker1", request, TokenLoads)
	if loads, exists := a.DPLoads["worker1"]; exists {
		assert.EqualValues(t, 200, loads[TokenLoads])
		assert.EqualValues(t, 3, loads[RunningQueue])
		assert.EqualValues(t, 1, loads[WaitingQueue])
	}

	// Test WaitingQueue on same worker
	a.addLoads("worker1", request, WaitingQueue)
	if loads, exists := a.DPLoads["worker1"]; exists {
		assert.EqualValues(t, 200, loads[TokenLoads])
		assert.EqualValues(t, 3, loads[RunningQueue])
		assert.EqualValues(t, 2, loads[WaitingQueue])
	}

	// Test WaitingQueue on different worker
	a.addLoads("worker2", request, WaitingQueue)
	if loads, exists := a.DPLoads["worker2"]; exists {
		assert.EqualValues(t, 100, loads[TokenLoads])
		assert.EqualValues(t, 10, loads[RunningQueue])
		assert.EqualValues(t, 11, loads[WaitingQueue])
	}
}

// TestReduceLoads tests the reduceLoads method.
func TestReduceLoads(t *testing.T) {
	a := &ABC6{
		DPLoads:          make(map[string][]int64),
		reqDispatchInfo:  make(map[string]*abc6RequestInfo),
		workerFlushedMap: make(map[string]bool),
		instBatchSize:    make(map[string]int64),
		workerLastUpdate: make(map[string]time.Time),
	}
	a.DPLoads["worker1"] = []int64{200, 1, 3}
	a.reqDispatchInfo["req1"] = &abc6RequestInfo{
		instId:       "worker1",
		load:         100,
		sortLoadType: TokenLoads,
	}

	request := &mockGenerateRequest{
		reqId:        "req1",
		promptTokens: 100,
	}

	// Test TokenLoads
	a.reduceLoads(request)
	if loads, exists := a.DPLoads["worker1"]; exists {
		assert.EqualValues(t, 100, loads[TokenLoads])
		assert.EqualValues(t, 2, loads[RunningQueue])
	}

	// Test WaitingQueue
	a.DPLoads["worker1"] = []int64{100, 1, 2}
	a.reqDispatchInfo["req1"] = &abc6RequestInfo{
		instId: "worker1",
		load:   100,
	}

	a.reqDispatchInfo["req1"].sortLoadType = WaitingQueue
	a.reduceLoads(request)
	if loads, exists := a.DPLoads["worker1"]; exists {
		assert.EqualValues(t, 0, loads[WaitingQueue])
	}
}

// TestConcurrentUpdateAndDispatch tests concurrent DispatchRequest and OnRequestEvent.
func TestConcurrentUpdateAndDispatch(t *testing.T) {
	workers := map[string]instance_metrics.WorkerStats{
		"worker1": createWorkerStats("worker1", "decode", "10", 0, 0, 0, 0),
		"worker2": createWorkerStats("worker2", "decode", "10", 0, 0, 0, 0),
		"worker3": createWorkerStats("worker3", "decode", "10", 0, 0, 0, 50),
	}
	a, err := setupABC6(128, workers, nil)
	if err != nil {
		t.Errorf("setupABC6() error = %v", err)
	}

	// Start 1000 goroutines to test concurrent requests
	startTime := time.Now()
	var wg sync.WaitGroup
	for i := 1; i <= 10000; i++ {
		wg.Add(1)
		req := &mockGenerateRequest{
			reqId:        "req_" + strconv.Itoa(i),
			promptTokens: 1024,
		}
		go concurrentReqWorker(&wg, a, req)
	}
	// Wait for all goroutines to complete
	wg.Wait()
	elapsed := time.Since(startTime)
	t.Logf("Processed 10000 requests in %v", elapsed)

	// Verify all loads are reset to 0
	expectTargetNodeLoad := []int64{0, 0, 0}
	for key, value := range a.DPLoads {
		if !reflect.DeepEqual(value, expectTargetNodeLoad) {
			t.Errorf("target node %s load info is incorrect, value = %v, expectValue = %v",
				key, value, expectTargetNodeLoad)
		}
	}
}

func concurrentReqWorker(wg *sync.WaitGroup, algo scheduler.ScheduleAlgorithm, req *mockGenerateRequest) {
	defer wg.Done() // Mark goroutine as complete
	algo.DispatchRequest(req, nil, nil)

	// use fixed random num to mock process time.
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	sleepDuration := time.Duration(rng.Intn(200)) * time.Millisecond
	time.Sleep(sleepDuration)

	event := utils.RequestEventMsg{
		Request:        utils.WrapRequest(req, false),
		TargetInstance: "",
		EventType:      constant.RequestDecodeSuccess,
	}
	algo.OnRequestEvent(&event)
}
