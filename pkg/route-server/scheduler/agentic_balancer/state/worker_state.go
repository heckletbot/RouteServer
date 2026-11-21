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
	"math"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"route-server/pkg/common/constant"
	instance_metrics "route-server/pkg/common/instance-metrics"
	"route-server/pkg/common/logger"
)

// kvMetricMissingWarned keeps the version-dependency warning to one log line per process.
var kvMetricMissingWarned atomic.Bool

// ComputeWorkerUtil returns max((liveInflight+pending)/(1+maxSeq), kvUsage): the
// reserved load counts first, and a worker never reports less than its KV pressure.
func (sm *StateManager) ComputeWorkerUtil(w *WorkerState) float64 {
	var liveInflight float64
	if sm != nil {
		liveInflight = float64(sm.Inflight.CountByWorker(w.WorkerId))
	}
	pending := float64(w.Pending)
	maxSeq := float64(w.MaxNumSeqs)
	if maxSeq <= 0 {
		maxSeq = 1
	}
	queueLoad := (liveInflight + pending) / (1.0 + maxSeq)
	return math.Max(queueLoad, w.KVCache.UsagePerc)
}

// ComputeWorkerUtil is the package-level form for callers holding the StateManager;
// it reports 0 when sm is nil.
func ComputeWorkerUtil(sm *StateManager, w *WorkerState) float64 {
	if sm == nil {
		return 0
	}
	return sm.ComputeWorkerUtil(w)
}

// KVCacheState represents the KV cache status of a worker.
type KVCacheState struct {
	UsedTokens  int64   // KVCache used token count
	TotalTokens int64   // KVCache total capacity
	UsagePerc   float64 // usage ratio used/total (0.0-1.0)
}

// WorkerState holds the scheduling-relevant state of a single worker.
type WorkerState struct {
	WorkerId   string
	Role       string // prefill / decode
	InFlight   int64  // current running requests (vllm:num_requests_running)
	Pending    int64  // waiting queue requests (vllm:num_requests_waiting)
	KVCache    KVCacheState
	MaxNumSeqs int64 // max_num_seqs from StaticInfo
	Available  bool  // schedulable
	Healthy    bool  // health status (Status == Available)
	UpdatedAt  time.Time
}

// WorkerStateTable maintains worker_id → WorkerState mappings.
type WorkerStateTable struct {
	mu      sync.RWMutex
	workers map[string]*WorkerState
}

func NewWorkerStateTable() *WorkerStateTable {
	return &WorkerStateTable{
		workers: make(map[string]*WorkerState),
	}
}

// UpdateFromStats refreshes the table from the framework-pushed WorkerStats maps.
// Type-asserts to *ZeroCopyStats, reads StaticInfo for max_num_seqs,
// reads GetNumericStat for running/waiting/kv_usage metrics.
func (t *WorkerStateTable) UpdateFromStats(available, unavailable map[string]instance_metrics.WorkerStats) {
	t.mu.Lock()
	defer t.mu.Unlock()

	// process available workers
	for id, stat := range available {
		ws := t.parseFromStats(id, stat)
		if ws != nil {
			ws.Available = true
			ws.Healthy = stat.GetStatus() == instance_metrics.Available
			t.workers[id] = ws
		}
	}

	// process unavailable workers — mark as unavailable but keep state
	for id, stat := range unavailable {
		ws := t.parseFromStats(id, stat)
		if ws != nil {
			ws.Available = false
			ws.Healthy = stat.GetStatus() == instance_metrics.Available
			t.workers[id] = ws
		}
	}

	// remove workers that are no longer in either map
	known := make(map[string]struct{}, len(available)+len(unavailable))
	for id := range available {
		known[id] = struct{}{}
	}
	for id := range unavailable {
		known[id] = struct{}{}
	}
	for id := range t.workers {
		if _, ok := known[id]; !ok {
			delete(t.workers, id)
		}
	}
}

func (t *WorkerStateTable) MarkUnavailable(workerId string) {
	t.mu.Lock()
	if w, ok := t.workers[workerId]; ok {
		w.Available = false
	}
	t.mu.Unlock()
}

// parseFromStats extracts WorkerState from a WorkerStats instance.
// If the stat is not *ZeroCopyStats, returns a partial WorkerState with basic info.
func (t *WorkerStateTable) parseFromStats(id string, stat instance_metrics.WorkerStats) *WorkerState {
	zcs, ok := stat.(*instance_metrics.ZeroCopyStats)
	if !ok {
		ts := stat.GetTimeStamp()
		if ts.IsZero() {
			ts = time.Now()
		}
		return &WorkerState{
			WorkerId:  id,
			Role:      stat.GetRole(),
			UpdatedAt: ts,
		}
	}

	ws := &WorkerState{
		WorkerId:  id,
		Role:      zcs.GetRole(),
		UpdatedAt: zcs.GetTimeStamp(),
	}
	if ws.UpdatedAt.IsZero() {
		ws.UpdatedAt = time.Now()
	}

	// read max_num_seqs from StaticInfo
	if maxNumSeqsStr, exists := zcs.StaticInfo[constant.MaxNumSeqs]; exists {
		if num, err := strconv.ParseInt(maxNumSeqsStr, 10, 64); err == nil && num > 0 {
			ws.MaxNumSeqs = num
		}
	}

	// read running / waiting / kv_usage from NumericStats
	ws.InFlight = int64(readMetricValue(zcs, constant.MetricNumRequestsRunning))
	ws.Pending = int64(readMetricValue(zcs, constant.MetricNumRequestsWaiting))

	// A missing kv metric means the engine or the metrics config predates vLLM 0.11;
	// the load then reads as 0, so report it once instead of silently underloading.
	if kv := zcs.GetNumericStat(constant.MetricKVCacheUsagePerc); len(kv) > 0 {
		ws.KVCache.UsagePerc = kv[0].Value
	} else if kvMetricMissingWarned.CompareAndSwap(false, true) {
		logger.Log().Warnf("agentic-balancer: worker %s reports no %s, which requires vLLM >= 0.11 "+
			"(0.9/0.10 export vllm:gpu_cache_usage_perc only); its KV load is read as 0",
			id, constant.MetricKVCacheUsagePerc)
	}

	return ws
}

// readMetricValue extracts the first MetricInfo value for a given key.
func readMetricValue(zcs *instance_metrics.ZeroCopyStats, key string) float64 {
	infos := zcs.GetNumericStat(key)
	if len(infos) == 0 {
		return 0
	}
	return infos[0].Value
}

func (t *WorkerStateTable) Get(workerId string) *WorkerState {
	t.mu.RLock()
	defer t.mu.RUnlock()
	ws, ok := t.workers[workerId]
	if !ok {
		return nil
	}
	cp := *ws
	return &cp
}

func (t *WorkerStateTable) ListByRole(role string) []*WorkerState {
	t.mu.RLock()
	defer t.mu.RUnlock()
	result := make([]*WorkerState, 0)
	for _, ws := range t.workers {
		if ws.Role == role {
			cp := *ws
			result = append(result, &cp)
		}
	}
	return result
}

func (t *WorkerStateTable) ListAvailable() []*WorkerState {
	t.mu.RLock()
	defer t.mu.RUnlock()
	result := make([]*WorkerState, 0)
	for _, ws := range t.workers {
		if ws.Available {
			cp := *ws
			result = append(result, &cp)
		}
	}
	return result
}

// ListAvailableByRole returns available workers matching the given role.
func (t *WorkerStateTable) ListAvailableByRole(role string) []*WorkerState {
	t.mu.RLock()
	defer t.mu.RUnlock()
	result := make([]*WorkerState, 0)
	for _, ws := range t.workers {
		if ws.Available && ws.Role == role {
			cp := *ws
			result = append(result, &cp)
		}
	}
	return result
}

func (t *WorkerStateTable) RemoveWorker(workerId string) {
	t.mu.Lock()
	delete(t.workers, workerId)
	t.mu.Unlock()
}

// Set inserts or replaces a worker's state directly. Mostly used by tests and
// controllers that need to seed state without going through WorkerStats.
func (t *WorkerStateTable) Set(ws *WorkerState) {
	if ws == nil {
		return
	}
	t.mu.Lock()
	t.workers[ws.WorkerId] = ws
	t.mu.Unlock()
}

func (t *WorkerStateTable) ListAll() []*WorkerState {
	t.mu.RLock()
	defer t.mu.RUnlock()
	result := make([]*WorkerState, 0, len(t.workers))
	for _, ws := range t.workers {
		cp := *ws
		result = append(result, &cp)
	}
	return result
}

func (t *WorkerStateTable) Count() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return len(t.workers)
}
