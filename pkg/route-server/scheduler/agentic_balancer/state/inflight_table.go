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
	"sync"
	"time"
)

// InflightRequest tracks a single in-flight request from the scheduler's perspective.
// Unlike handler.RunningRequestsManager (which manages cancellation/phase tracking),
// this table tracks worker/session associations for scheduling decisions.
type InflightRequest struct {
	RequestId       string
	WorkerId        string
	SessionId       string
	StartTime       time.Time
	EstimatedTokens int64 // estimated token count (for capacity calculations)
}

// InflightTable maintains request_id → InflightRequest mappings, plus a
// per-worker count index so worker load lookups are O(1) on the dispatch path
// instead of a full-table scan per candidate.
type InflightTable struct {
	mu       sync.RWMutex
	requests map[string]*InflightRequest
	byWorker map[string]int
}

func NewInflightTable() *InflightTable {
	return &InflightTable{
		requests: make(map[string]*InflightRequest),
		byWorker: make(map[string]int),
	}
}

func (t *InflightTable) Add(req *InflightRequest) {
	if req == nil || req.RequestId == "" {
		return
	}
	t.mu.Lock()
	if old, exists := t.requests[req.RequestId]; exists {
		t.decWorkerLocked(old.WorkerId)
	}
	t.requests[req.RequestId] = req
	t.byWorker[req.WorkerId]++
	t.mu.Unlock()
}

func (t *InflightTable) Remove(requestId string) {
	t.mu.Lock()
	if old, exists := t.requests[requestId]; exists {
		t.decWorkerLocked(old.WorkerId)
		delete(t.requests, requestId)
	}
	t.mu.Unlock()
}

func (t *InflightTable) Get(requestId string) *InflightRequest {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.requests[requestId]
}

// GetByWorker returns all in-flight requests on the given worker.
func (t *InflightTable) GetByWorker(workerId string) []*InflightRequest {
	t.mu.RLock()
	defer t.mu.RUnlock()
	result := make([]*InflightRequest, 0)
	for _, req := range t.requests {
		if req.WorkerId == workerId {
			result = append(result, req)
		}
	}
	return result
}

// GetBySession returns all in-flight requests for the given session.
func (t *InflightTable) GetBySession(sessionId string) []*InflightRequest {
	t.mu.RLock()
	defer t.mu.RUnlock()
	result := make([]*InflightRequest, 0)
	for _, req := range t.requests {
		if req.SessionId == sessionId {
			result = append(result, req)
		}
	}
	return result
}

// CountByWorker returns the number of in-flight requests on the given worker.
func (t *InflightTable) CountByWorker(workerId string) int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.byWorker[workerId]
}

func (t *InflightTable) Count() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return len(t.requests)
}

// RemoveBySession removes all in-flight requests for the given session.
func (t *InflightTable) RemoveBySession(sessionId string) {
	t.mu.Lock()
	for rid, req := range t.requests {
		if req.SessionId == sessionId {
			t.decWorkerLocked(req.WorkerId)
			delete(t.requests, rid)
		}
	}
	t.mu.Unlock()
}

// Clear removes all in-flight entries.
func (t *InflightTable) Clear() {
	t.mu.Lock()
	t.requests = make(map[string]*InflightRequest)
	t.byWorker = make(map[string]int)
	t.mu.Unlock()
}

// decWorkerLocked drops one count for workerId. The index entry is deleted at
// zero so workers that leave the pool never linger.
func (t *InflightTable) decWorkerLocked(workerId string) {
	if n := t.byWorker[workerId]; n > 1 {
		t.byWorker[workerId] = n - 1
		return
	}
	delete(t.byWorker, workerId)
}
