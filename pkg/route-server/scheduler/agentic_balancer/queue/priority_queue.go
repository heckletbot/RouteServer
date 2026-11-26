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

package queue

import (
	"container/heap"
	"errors"
	"sync"
	"time"

	"route-server/pkg/common/utils"
)

var ErrQueueFull = errors.New("priority queue full")

// DefaultAgingRate keeps the legacy aging-factor=10 / aging-ttl=60s pair: 10 priority per 60s.
const DefaultAgingRate = 10.0 / 60.0

// defaultCapacity applies when the configured admission limit is not positive.
const defaultCapacity = 1000

// aging-rate < 0 falls back to DefaultAgingRate; 0 disables aging promotion.
func normalizeAgingRate(rate float64) float64 {
	if rate < 0 {
		return DefaultAgingRate
	}
	return rate
}

func normalizeCapacity(capacity int) int {
	if capacity <= 0 {
		return defaultCapacity
	}
	return capacity
}

type DispatchResult struct {
	WorkerId string
	Err      error
}

// EntryState tracks who owns a queued request's reservation.
type EntryState int8

// Transitions and reads happen under DecisionMu; pq.mu guards heap placement, never State.
// queued: nothing reserved · reserved: inflight committed, result not visible · handed:
// result in ResultChan · abandoned: the waiter undid its own reservation.
const (
	StateQueued EntryState = iota
	StateReserved
	StateHanded
	StateAbandoned
)

type QueueEntry struct {
	Request    utils.GenerateRequest
	SessionId  string
	Priority   int32
	OutputLen  int32
	EnqueueAt  time.Time
	Params     map[string]string
	BlackMap   map[string]interface{}
	ResultChan chan DispatchResult
	State      EntryState
}

// PriorityQueue is the overload queue. Push/pop/re-enqueue move the same *QueueEntry pointer,
// so a waiter holding its entry observes State under the decision lock (order: DecisionMu → pq.mu).
type PriorityQueue struct {
	mu       sync.Mutex
	inner    pqHeap
	capacity int
}

func NewPriorityQueue(capacity int, agingRate float64) *PriorityQueue {
	agingRate = normalizeAgingRate(agingRate)
	pq := &PriorityQueue{
		capacity: normalizeCapacity(capacity),
	}
	pq.inner = pqHeap{
		entries:   make([]*QueueEntry, 0),
		agingRate: agingRate,
	}
	heap.Init(&pq.inner)
	return pq
}

// SetCapacity hot-applies the admission limit. Queued entries stay: each has a waiter, so a
// limit below the current depth only rejects new arrivals until the backlog drains.
func (pq *PriorityQueue) SetCapacity(capacity int) {
	capacity = normalizeCapacity(capacity)
	pq.mu.Lock()
	pq.capacity = capacity
	pq.mu.Unlock()
}

// UpdateAgingRate hot-swaps the aging rate and re-heaps so queued entries are re-ranked.
// EnqueueAt is untouched: the aging clock starts at enqueue and must not reset.
func (pq *PriorityQueue) UpdateAgingRate(rate float64) {
	rate = normalizeAgingRate(rate)
	pq.mu.Lock()
	defer pq.mu.Unlock()
	pq.inner.agingRate = rate
	heap.Init(&pq.inner)
}

// Enqueue is Push(NewQueueEntry(...)); call under the decision lock.
func (pq *PriorityQueue) Enqueue(req utils.GenerateRequest, params map[string]string, blackMap map[string]interface{}, resultChan chan DispatchResult) error {
	return pq.Push(NewQueueEntry(req, params, blackMap, resultChan))
}

// NewQueueEntry builds the entry the waiter submits via Push. The metadata is taken
// once here, so the heap key stays stable across reinserts.
func NewQueueEntry(req utils.GenerateRequest, params map[string]string, blackMap map[string]interface{}, resultChan chan DispatchResult) *QueueEntry {
	sessionId, priority, outputLen := utils.ExtractSchedulingMetadata(req)
	return &QueueEntry{
		Request:    req,
		SessionId:  sessionId,
		Priority:   priority,
		OutputLen:  outputLen,
		EnqueueAt:  time.Now(),
		Params:     params,
		BlackMap:   blackMap,
		ResultChan: resultChan,
		State:      StateQueued,
	}
}

// Push inserts a waiter-owned entry and keeps the caller's pointer, so the waiter can
// observe State transitions. Call under the decision lock.
func (pq *PriorityQueue) Push(entry *QueueEntry) error {
	pq.mu.Lock()
	defer pq.mu.Unlock()
	if len(pq.inner.entries) >= pq.capacity {
		return ErrQueueFull
	}
	entry.State = StateQueued
	heap.Push(&pq.inner, entry)
	return nil
}

func (pq *PriorityQueue) Dequeue() *QueueEntry {
	pq.mu.Lock()
	defer pq.mu.Unlock()
	if pq.inner.Len() == 0 {
		return nil
	}
	return heap.Pop(&pq.inner).(*QueueEntry)
}

// ReEnqueue re-inserts the same pointer after a real dispatch failure and restarts its
// aging clock. Call under the decision lock.
func (pq *PriorityQueue) ReEnqueue(entry *QueueEntry) error {
	return pq.reenqueue(entry, true)
}

// ReEnqueueKeepingTime re-inserts the same pointer without resetting its aging clock:
// a capacity miss is not a dispatch failure, so the wait keeps accumulating.
func (pq *PriorityQueue) ReEnqueueKeepingTime(entry *QueueEntry) error {
	return pq.reenqueue(entry, false)
}

func (pq *PriorityQueue) reenqueue(entry *QueueEntry, resetClock bool) error {
	pq.mu.Lock()
	defer pq.mu.Unlock()
	// No capacity check: the entry was just popped here, and legacy allows over-capacity reinsertion.
	if resetClock {
		entry.EnqueueAt = time.Now()
	}
	entry.State = StateQueued
	heap.Push(&pq.inner, entry)
	return nil
}

func (pq *PriorityQueue) Len() int {
	pq.mu.Lock()
	defer pq.mu.Unlock()
	return pq.inner.Len()
}

func (pq *PriorityQueue) Peek() *QueueEntry {
	pq.mu.Lock()
	defer pq.mu.Unlock()
	if pq.inner.Len() == 0 {
		return nil
	}
	return pq.inner.entries[0]
}

func (pq *PriorityQueue) Clear() {
	pq.mu.Lock()
	defer pq.mu.Unlock()
	pq.inner.entries = pq.inner.entries[:0]
}

func (pq *PriorityQueue) Remove(requestId string) bool {
	pq.mu.Lock()
	defer pq.mu.Unlock()
	for i, e := range pq.inner.entries {
		if e.Request.GetRequestId() == requestId {
			heap.Remove(&pq.inner, i)
			return true
		}
	}
	return false
}

type pqHeap struct {
	entries   []*QueueEntry
	agingRate float64
}

func (h pqHeap) Len() int { return len(h.entries) }

func (h pqHeap) Less(i, j int) bool {
	wi := h.effectiveWeight(h.entries[i])
	wj := h.effectiveWeight(h.entries[j])
	if wi != wj {
		return wi > wj
	}
	return h.entries[i].EnqueueAt.Before(h.entries[j].EnqueueAt)
}

func (h pqHeap) Swap(i, j int) {
	h.entries[i], h.entries[j] = h.entries[j], h.entries[i]
}

func (h *pqHeap) Push(x interface{}) {
	h.entries = append(h.entries, x.(*QueueEntry))
}

func (h *pqHeap) Pop() interface{} {
	old := h.entries
	n := len(old)
	item := old[n-1]
	old[n-1] = nil
	h.entries = old[:n-1]
	return item
}

// effectiveWeight is the heap key: (priority + aging·wait) / outputLen, so long outputs
// rank lower and waiting raises priority. OutputLen is floored at 1.
func (h pqHeap) effectiveWeight(e *QueueEntry) float64 {
	waitSeconds := time.Since(e.EnqueueAt).Seconds()
	effectivePriority := float64(e.Priority) + h.agingRate*waitSeconds
	outputLen := float64(e.OutputLen)
	if outputLen < 1 {
		outputLen = 1
	}
	return effectivePriority / outputLen
}
