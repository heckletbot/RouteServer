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

package state

import (
	"fmt"
	"math"
	"sync"
	"testing"
	"time"

	"route-server/pkg/common/constant"
	instance_metrics "route-server/pkg/common/instance-metrics"
)

// helper to build a ZeroCopyStats with given metrics
func buildStats(id, role string, status instance_metrics.InstanceStatus,
	maxNumSeqs string, running, waiting, kvUsage float64) *instance_metrics.ZeroCopyStats {
	staticInfo := map[string]string{}
	if maxNumSeqs != "" {
		staticInfo[constant.MaxNumSeqs] = maxNumSeqs
	}
	metrics := map[string][]*instance_metrics.MetricInfo{}
	if running >= 0 {
		metrics[constant.MetricNumRequestsRunning] = []*instance_metrics.MetricInfo{{Value: running}}
	}
	if waiting >= 0 {
		metrics[constant.MetricNumRequestsWaiting] = []*instance_metrics.MetricInfo{{Value: waiting}}
	}
	if kvUsage >= 0 {
		metrics[constant.MetricKVCacheUsagePerc] = []*instance_metrics.MetricInfo{{Value: kvUsage}}
	}
	zcs := &instance_metrics.ZeroCopyStats{
		Id:           id,
		Role:         role,
		Status:       status,
		StaticInfo:   staticInfo,
		NumericStats: &metrics,
		TimeStamp:    time.Now(),
	}
	return zcs
}

func TestWorkerStateTableUpdateFromStats(t *testing.T) {
	table := NewWorkerStateTable()

	available := map[string]instance_metrics.WorkerStats{
		"worker-a": buildStats("worker-a", constant.Prefill, instance_metrics.Available, "256", 10, 5, 0.75),
		"worker-b": buildStats("worker-b", constant.Prefill, instance_metrics.Available, "128", 3, 1, 0.30),
	}
	unavailable := map[string]instance_metrics.WorkerStats{
		"worker-c": buildStats("worker-c", constant.Decode, instance_metrics.Faulty, "64", 0, 0, 0.0),
	}

	table.UpdateFromStats(available, unavailable)

	ws := table.Get("worker-a")
	if ws == nil {
		t.Fatal("expected worker-a to exist")
	}
	if ws.Role != constant.Prefill {
		t.Errorf("expected role=prefill, got %s", ws.Role)
	}
	if ws.InFlight != 10 {
		t.Errorf("expected InFlight=10, got %d", ws.InFlight)
	}
	if ws.Pending != 5 {
		t.Errorf("expected Pending=5, got %d", ws.Pending)
	}
	if ws.KVCache.UsagePerc != 0.75 {
		t.Errorf("expected UsagePerc=0.75, got %f", ws.KVCache.UsagePerc)
	}
	if ws.MaxNumSeqs != 256 {
		t.Errorf("expected MaxNumSeqs=256, got %d", ws.MaxNumSeqs)
	}
	if !ws.Available {
		t.Error("expected Available=true for available worker")
	}
	if !ws.Healthy {
		t.Error("expected Healthy=true for Available status")
	}

	// worker-c is unavailable
	wsC := table.Get("worker-c")
	if wsC == nil {
		t.Fatal("expected worker-c to exist")
	}
	if wsC.Available {
		t.Error("expected Available=false for unavailable worker")
	}
	if wsC.Healthy {
		t.Error("expected Healthy=false for Faulty status")
	}
}

func TestWorkerStateTableListAvailableByRole(t *testing.T) {
	table := NewWorkerStateTable()

	available := map[string]instance_metrics.WorkerStats{
		"pf-1": buildStats("pf-1", constant.Prefill, instance_metrics.Available, "256", 5, 0, 0.5),
		"pf-2": buildStats("pf-2", constant.Prefill, instance_metrics.Available, "128", 3, 0, 0.3),
		"dc-1": buildStats("dc-1", constant.Decode, instance_metrics.Available, "64", 2, 0, 0.1),
	}
	unavailable := map[string]instance_metrics.WorkerStats{
		"pf-3": buildStats("pf-3", constant.Prefill, instance_metrics.Faulty, "256", 0, 0, 0.0),
	}
	table.UpdateFromStats(available, unavailable)

	// list available prefill workers
	prefillWorkers := table.ListAvailableByRole(constant.Prefill)
	if len(prefillWorkers) != 2 {
		t.Errorf("expected 2 available prefill workers, got %d", len(prefillWorkers))
	}

	// list available decode workers
	decodeWorkers := table.ListAvailableByRole(constant.Decode)
	if len(decodeWorkers) != 1 {
		t.Errorf("expected 1 available decode worker, got %d", len(decodeWorkers))
	}

	// pf-3 should not be in available list (it's unavailable)
	for _, ws := range prefillWorkers {
		if ws.WorkerId == "pf-3" {
			t.Error("pf-3 should not be in available list")
		}
	}
}

func TestWorkerStateTableRemoveWorker(t *testing.T) {
	table := NewWorkerStateTable()
	available := map[string]instance_metrics.WorkerStats{
		"worker-a": buildStats("worker-a", constant.Prefill, instance_metrics.Available, "256", 5, 0, 0.5),
	}
	table.UpdateFromStats(available, nil)

	table.RemoveWorker("worker-a")
	if table.Get("worker-a") != nil {
		t.Error("expected nil after RemoveWorker")
	}
	if table.Count() != 0 {
		t.Errorf("expected count=0, got %d", table.Count())
	}
}

func TestWorkerStateTableStaleWorkerRemoved(t *testing.T) {
	table := NewWorkerStateTable()

	// initial: worker-a and worker-b available
	available := map[string]instance_metrics.WorkerStats{
		"worker-a": buildStats("worker-a", constant.Prefill, instance_metrics.Available, "256", 5, 0, 0.5),
		"worker-b": buildStats("worker-b", constant.Prefill, instance_metrics.Available, "128", 3, 0, 0.3),
	}
	table.UpdateFromStats(available, nil)
	if table.Count() != 2 {
		t.Fatalf("expected count=2, got %d", table.Count())
	}

	// next update: worker-b is gone (only worker-a)
	available2 := map[string]instance_metrics.WorkerStats{
		"worker-a": buildStats("worker-a", constant.Prefill, instance_metrics.Available, "256", 7, 0, 0.6),
	}
	table.UpdateFromStats(available2, nil)
	if table.Count() != 1 {
		t.Errorf("expected count=1 after stale worker removed, got %d", table.Count())
	}
	if table.Get("worker-b") != nil {
		t.Error("worker-b should have been removed")
	}
	// worker-a should have updated values
	ws := table.Get("worker-a")
	if ws.InFlight != 7 {
		t.Errorf("expected InFlight=7, got %d", ws.InFlight)
	}
}

func TestWorkerStateTableConcurrency(t *testing.T) {
	table := NewWorkerStateTable()
	const goroutines = 8
	const iterations = 200

	var wg sync.WaitGroup
	wg.Add(goroutines)

	for g := 0; g < goroutines; g++ {
		go func(gid int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				workerId := fmt.Sprintf("worker-%d", gid%4)
				available := map[string]instance_metrics.WorkerStats{
					workerId: buildStats(workerId, constant.Prefill, instance_metrics.Available, "256", 5, 0, 0.5),
				}
				table.UpdateFromStats(available, nil)
				_ = table.Get(workerId)
				_ = table.ListAvailable()
				_ = table.ListAvailableByRole(constant.Prefill)
				_ = table.Count()
				if i%50 == 0 {
					table.MarkUnavailable(workerId)
				}
				if i%100 == 0 {
					table.RemoveWorker(workerId)
				}
			}
		}(g)
	}
	wg.Wait()
}

func TestWorkerStateTableSetConcurrency(t *testing.T) {
	table := NewWorkerStateTable()
	const goroutines = 4
	const iterations = 300

	var wg sync.WaitGroup
	wg.Add(goroutines)

	for g := 0; g < goroutines; g++ {
		go func(gid int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				workerId := fmt.Sprintf("worker-%d", gid%2)
				table.Set(&WorkerState{
					WorkerId:  workerId,
					Role:      constant.Decode,
					InFlight:  int64(i),
					Available: true,
					UpdatedAt: time.Now(),
				})
				_ = table.Get(workerId)
				_ = table.ListAll()
			}
		}(g)
	}
	wg.Wait()
}

func TestComputeWorkerUtil(t *testing.T) {
	sm := NewStateManager(TTLConfig{})
	sm.Inflight.Add(&InflightRequest{RequestId: "req-1", WorkerId: "w1"})
	sm.Inflight.Add(&InflightRequest{RequestId: "req-2", WorkerId: "w1"})

	tests := []struct {
		name     string
		worker   *WorkerState
		expected float64
	}{
		{
			name: "queueLoad dominates",
			worker: &WorkerState{
				WorkerId:   "w1",
				Pending:    10,
				MaxNumSeqs: 4,
				KVCache:    KVCacheState{UsagePerc: 0.3},
			},
			expected: math.Max((2+10)/(1.0+4), 0.3),
		},
		{
			name: "kvUsage dominates",
			worker: &WorkerState{
				WorkerId:   "w1",
				Pending:    0,
				MaxNumSeqs: 256,
				KVCache:    KVCacheState{UsagePerc: 0.9},
			},
			expected: math.Max((2+0)/(1.0+256), 0.9),
		},
		{
			name: "maxNumSeqs=0 fallback",
			worker: &WorkerState{
				WorkerId:   "w2",
				Pending:    5,
				MaxNumSeqs: 0,
				KVCache:    KVCacheState{UsagePerc: 0.0},
			},
			expected: math.Max((0+5)/(1.0+1), 0.0),
		},
		{
			name: "nil StateManager",
			worker: &WorkerState{
				WorkerId:   "w3",
				Pending:    3,
				MaxNumSeqs: 4,
				KVCache:    KVCacheState{UsagePerc: 0.2},
			},
			expected: math.Max((0+3)/(1.0+4), 0.2),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var s *StateManager
			if tt.name != "nil StateManager" {
				s = sm
			}
			got := s.ComputeWorkerUtil(tt.worker)
			if math.Abs(got-tt.expected) > 1e-9 {
				t.Errorf("expected %.6f, got %.6f", tt.expected, got)
			}
		})
	}
}

func TestWorkerStateTableMarkUnavailable(t *testing.T) {
	table := NewWorkerStateTable()
	table.Set(&WorkerState{WorkerId: "w1", Role: constant.Prefill, Available: true})
	table.Set(&WorkerState{WorkerId: "w2", Role: constant.Decode, Available: true})

	table.MarkUnavailable("w1")

	w1 := table.Get("w1")
	if w1.Available {
		t.Error("expected w1 to be unavailable after MarkUnavailable")
	}
	w2 := table.Get("w2")
	if !w2.Available {
		t.Error("expected w2 to remain available")
	}

	table.MarkUnavailable("nonexistent")
}

func TestWorkerStateTableListByRole(t *testing.T) {
	table := NewWorkerStateTable()
	table.Set(&WorkerState{WorkerId: "pf-1", Role: constant.Prefill, Available: true})
	table.Set(&WorkerState{WorkerId: "pf-2", Role: constant.Prefill, Available: false})
	table.Set(&WorkerState{WorkerId: "dc-1", Role: constant.Decode, Available: true})

	prefillWorkers := table.ListByRole(constant.Prefill)
	if len(prefillWorkers) != 2 {
		t.Errorf("expected 2 prefill workers, got %d", len(prefillWorkers))
	}

	decodeWorkers := table.ListByRole(constant.Decode)
	if len(decodeWorkers) != 1 {
		t.Errorf("expected 1 decode worker, got %d", len(decodeWorkers))
	}

	none := table.ListByRole("unknown")
	if len(none) != 0 {
		t.Errorf("expected 0 workers for unknown role, got %d", len(none))
	}
}

func TestWorkerStateTableListAvailable(t *testing.T) {
	table := NewWorkerStateTable()
	table.Set(&WorkerState{WorkerId: "w1", Role: constant.Prefill, Available: true})
	table.Set(&WorkerState{WorkerId: "w2", Role: constant.Decode, Available: false})
	table.Set(&WorkerState{WorkerId: "w3", Role: constant.Prefill, Available: true})

	available := table.ListAvailable()
	if len(available) != 2 {
		t.Errorf("expected 2 available workers, got %d", len(available))
	}

	for _, ws := range available {
		if !ws.Available {
			t.Errorf("worker %s should be available", ws.WorkerId)
		}
	}
}

func TestWorkerStateTableSet(t *testing.T) {
	table := NewWorkerStateTable()

	table.Set(&WorkerState{WorkerId: "w1", Role: constant.Prefill, InFlight: 5, Available: true})
	ws := table.Get("w1")
	if ws == nil {
		t.Fatal("expected w1 to exist after Set")
	}
	if ws.InFlight != 5 {
		t.Errorf("expected InFlight=5, got %d", ws.InFlight)
	}

	table.Set(&WorkerState{WorkerId: "w1", Role: constant.Prefill, InFlight: 10, Available: true})
	ws = table.Get("w1")
	if ws.InFlight != 10 {
		t.Errorf("expected InFlight=10 after replace, got %d", ws.InFlight)
	}

	table.Set(nil)
	if table.Count() != 1 {
		t.Errorf("expected count=1 after Set(nil), got %d", table.Count())
	}
}

func TestWorkerStateTableListAll(t *testing.T) {
	table := NewWorkerStateTable()
	table.Set(&WorkerState{WorkerId: "w1", Role: constant.Prefill, Available: true})
	table.Set(&WorkerState{WorkerId: "w2", Role: constant.Decode, Available: false})
	table.Set(&WorkerState{WorkerId: "w3", Role: constant.Prefill, Available: true})

	all := table.ListAll()
	if len(all) != 3 {
		t.Errorf("expected 3 workers, got %d", len(all))
	}

	empty := NewWorkerStateTable().ListAll()
	if len(empty) != 0 {
		t.Errorf("expected 0 workers for empty table, got %d", len(empty))
	}
}
