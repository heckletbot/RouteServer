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
	"context"
	"testing"
	"time"

	"route-server/pkg/common/utils"
)

type testRequest struct {
	id        string
	sessionId string
	priority  int32
	outputLen int32
	promptKey string
	promptTok int32
	maxTokens int32
}

func (r *testRequest) GetRequestId() string                   { return r.id }
func (r *testRequest) GetType() string                        { return "prefill" }
func (r *testRequest) GetSLOTag() string                      { return "" }
func (r *testRequest) GetSLOInterval() *utils.SLOInterval     { return nil }
func (r *testRequest) GetChat() []utils.ChatContent           { return nil }
func (r *testRequest) GetMaxToken() int32                     { return r.maxTokens }
func (r *testRequest) GetPromptTokenCount() int32             { return r.promptTok }
func (r *testRequest) IsStream() bool                         { return false }
func (r *testRequest) GetModel() string                       { return "test" }
func (r *testRequest) GetPromptCacheKey() string              { return r.promptKey }
func (r *testRequest) GetApcStrategyType() string             { return "" }
func (r *testRequest) GetSessionId() string                   { return r.sessionId }
func (r *testRequest) GetPriority() int32                     { return r.priority }
func (r *testRequest) GetOutputLength() int32                 { return r.outputLen }
func (r *testRequest) GetScheduleContext() context.Context    { return nil }
func (r *testRequest) SetScheduleContext(ctx context.Context) {}

func makeReq(id string, priority int32, outputLen int32) *testRequest {
	return &testRequest{id: id, priority: priority, outputLen: outputLen}
}

func TestEnqueueDequeue(t *testing.T) {
	pq := NewPriorityQueue(100, DefaultAgingRate)

	_ = pq.Enqueue(makeReq("r1", 1, 100), nil, nil, nil)
	_ = pq.Enqueue(makeReq("r2", 5, 100), nil, nil, nil)
	_ = pq.Enqueue(makeReq("r3", 3, 100), nil, nil, nil)

	if pq.Len() != 3 {
		t.Fatalf("expected len 3, got %d", pq.Len())
	}

	e := pq.Dequeue()
	if e.Priority != 5 {
		t.Fatalf("expected priority 5 first, got %d", e.Priority)
	}
	e = pq.Dequeue()
	if e.Priority != 3 {
		t.Fatalf("expected priority 3 second, got %d", e.Priority)
	}
	e = pq.Dequeue()
	if e.Priority != 1 {
		t.Fatalf("expected priority 1 third, got %d", e.Priority)
	}
	if pq.Dequeue() != nil {
		t.Fatal("expected nil after draining")
	}
}

func TestFCFS(t *testing.T) {
	pq := NewPriorityQueue(100, DefaultAgingRate)

	_ = pq.Enqueue(makeReq("r1", 5, 100), nil, nil, nil)
	time.Sleep(10 * time.Millisecond)
	_ = pq.Enqueue(makeReq("r2", 5, 100), nil, nil, nil)

	e := pq.Dequeue()
	if e.Request.GetRequestId() != "r1" {
		t.Fatalf("expected r1 first (FCFS), got %s", e.Request.GetRequestId())
	}
}

func TestWSPT(t *testing.T) {
	pq := NewPriorityQueue(100, DefaultAgingRate)

	_ = pq.Enqueue(makeReq("short-high", 5, 10), nil, nil, nil)
	_ = pq.Enqueue(makeReq("long-high", 5, 1000), nil, nil, nil)

	e := pq.Dequeue()
	if e.Request.GetRequestId() != "short-high" {
		t.Fatalf("WSPT: expected short-high first (5/10 > 5/1000), got %s", e.Request.GetRequestId())
	}
}

func TestAging(t *testing.T) {
	// legacy factor=10.0 / ttl=2s ≡ η = 10/2 = 5 priority/s
	pq := NewPriorityQueue(100, 10.0/2.0)

	_ = pq.Enqueue(makeReq("low", 0, 100), nil, nil, nil)

	time.Sleep(500 * time.Millisecond)

	_ = pq.Enqueue(makeReq("high", 5, 100), nil, nil, nil)

	h := pq.inner
	wLow := h.effectiveWeight(pq.Peek())
	_ = wLow

	e := pq.Dequeue()
	if e.Request.GetRequestId() != "high" {
		t.Fatalf("Aging: high-priority should still come first, got %s", e.Request.GetRequestId())
	}
}

func TestAgingPromotion(t *testing.T) {
	// legacy factor=10.0 / ttl=100ms ≡ η = 10/0.1 = 100 priority/s
	pq := NewPriorityQueue(100, 10.0/0.1)

	_ = pq.Enqueue(makeReq("low-old", 0, 100), nil, nil, nil)

	time.Sleep(200 * time.Millisecond)

	_ = pq.Enqueue(makeReq("mid-new", 3, 100), nil, nil, nil)

	e := pq.Dequeue()
	if e.Request.GetRequestId() != "low-old" {
		t.Fatalf("Aging promotion: low-old should surpass mid-new after waiting, got %s", e.Request.GetRequestId())
	}
}

func TestQueueFull(t *testing.T) {
	pq := NewPriorityQueue(2, DefaultAgingRate)

	if err := pq.Enqueue(makeReq("r1", 1, 100), nil, nil, nil); err != nil {
		t.Fatalf("first enqueue should succeed: %v", err)
	}
	if err := pq.Enqueue(makeReq("r2", 1, 100), nil, nil, nil); err != nil {
		t.Fatalf("second enqueue should succeed: %v", err)
	}
	if err := pq.Enqueue(makeReq("r3", 1, 100), nil, nil, nil); err != ErrQueueFull {
		t.Fatalf("third enqueue should return ErrQueueFull, got %v", err)
	}
}

func TestReEnqueue(t *testing.T) {
	pq := NewPriorityQueue(2, DefaultAgingRate)

	_ = pq.Enqueue(makeReq("r1", 1, 100), nil, nil, nil)
	_ = pq.Enqueue(makeReq("r2", 1, 100), nil, nil, nil)

	entry := &QueueEntry{
		Request:   makeReq("r3", 1, 100),
		SessionId: "",
		Priority:  1,
		OutputLen: 100,
		EnqueueAt: time.Now(),
	}
	if err := pq.ReEnqueue(entry); err != nil {
		t.Fatalf("ReEnqueue should succeed even when full, got %v", err)
	}
	if pq.Len() != 3 {
		t.Fatalf("expected len 3 after ReEnqueue, got %d", pq.Len())
	}
}

func TestPeek(t *testing.T) {
	pq := NewPriorityQueue(100, DefaultAgingRate)

	_ = pq.Enqueue(makeReq("r1", 1, 100), nil, nil, nil)
	_ = pq.Enqueue(makeReq("r2", 5, 100), nil, nil, nil)

	e := pq.Peek()
	if e.Priority != 5 {
		t.Fatalf("Peek: expected priority 5, got %d", e.Priority)
	}
	if pq.Len() != 2 {
		t.Fatalf("Peek should not remove, expected len 2, got %d", pq.Len())
	}
}

func TestClear(t *testing.T) {
	pq := NewPriorityQueue(100, DefaultAgingRate)

	_ = pq.Enqueue(makeReq("r1", 1, 100), nil, nil, nil)
	_ = pq.Enqueue(makeReq("r2", 2, 100), nil, nil, nil)
	pq.Clear()

	if pq.Len() != 0 {
		t.Fatalf("expected len 0 after Clear, got %d", pq.Len())
	}
	if pq.Dequeue() != nil {
		t.Fatal("expected nil Dequeue after Clear")
	}
}

func TestDefaultValues(t *testing.T) {
	pq := NewPriorityQueue(0, -1)
	if pq.capacity != 1000 {
		t.Fatalf("expected default capacity 1000, got %d", pq.capacity)
	}
	if pq.inner.agingRate != DefaultAgingRate {
		t.Fatalf("expected default agingRate %v, got %v", DefaultAgingRate, pq.inner.agingRate)
	}
	// η=0 is an explicit valid value (aging disabled), not the default.
	pq0 := NewPriorityQueue(10, 0)
	if pq0.inner.agingRate != 0 {
		t.Fatalf("expected explicit agingRate 0 preserved, got %v", pq0.inner.agingRate)
	}
}

func TestAgingRateConversionEquivalence(t *testing.T) {
	// spec §8.1: factor=2, ttl=60s, wait=30s → priority gain 1 ⇒ η=1/30/s.
	// Also verifies the repo-default migration: factor=10, ttl=60s ⇒ η=1/6.
	cases := []struct {
		factor   float64
		ttl      time.Duration
		wait     time.Duration
		wantGain float64
	}{
		{2.0, 60 * time.Second, 30 * time.Second, 1.0},
		{10.0, 60 * time.Second, 60 * time.Second, 10.0},
		{10.0, 100 * time.Millisecond, 200 * time.Millisecond, 20.0},
	}
	for _, c := range cases {
		rate := c.factor / c.ttl.Seconds()
		got := rate * c.wait.Seconds()
		if got != c.wantGain {
			t.Errorf("factor=%v ttl=%v wait=%v: legacy gain %v, new η·t %v",
				c.factor, c.ttl, c.wait, c.wantGain, got)
		}
	}
}

func TestAgingRateZeroNoDivByZero(t *testing.T) {
	pq := NewPriorityQueue(100, 0)
	_ = pq.Enqueue(makeReq("r1", 1, 100), nil, nil, nil)
	_ = pq.Enqueue(makeReq("r2", 2, 100), nil, nil, nil)
	e := pq.Dequeue()
	if e.Priority != 2 {
		t.Fatalf("η=0 should fall back to plain priority order, got %d", e.Priority)
	}
}

func TestUpdateAgingRate(t *testing.T) {
	pq := NewPriorityQueue(100, 0) // aging off
	_ = pq.Enqueue(makeReq("low-old", 0, 1), nil, nil, nil)
	first := pq.Peek()
	// Deterministic 2s wait; at η=0 there is no promotion head start yet.
	first.EnqueueAt = time.Now().Add(-2 * time.Second)
	oldAt := first.EnqueueAt

	pq.UpdateAgingRate(50) // +50/s ⇒ the 2s wait ≙ +100 ≫ any small priority delta
	if pq.inner.agingRate != 50 {
		t.Fatalf("expected agingRate 50, got %v", pq.inner.agingRate)
	}
	if pq.Peek().EnqueueAt != oldAt {
		t.Fatal("UpdateAgingRate must not reset EnqueueAt")
	}
	_ = pq.Enqueue(makeReq("mid-new", 3, 1), nil, nil, nil)
	e := pq.Dequeue()
	if e.Request.GetRequestId() != "low-old" {
		t.Fatalf("after rate hot-update the old low-priority entry should be promoted, got %s", e.Request.GetRequestId())
	}
	// negative rate normalizes to default, not to a magic number
	pq.UpdateAgingRate(-1)
	if pq.inner.agingRate != DefaultAgingRate {
		t.Fatalf("expected default rate after negative update, got %v", pq.inner.agingRate)
	}
}

func TestRemove(t *testing.T) {
	pq := NewPriorityQueue(100, DefaultAgingRate)

	_ = pq.Enqueue(makeReq("r1", 1, 100), nil, nil, nil)
	_ = pq.Enqueue(makeReq("r2", 5, 100), nil, nil, nil)
	_ = pq.Enqueue(makeReq("r3", 3, 100), nil, nil, nil)

	if !pq.Remove("r2") {
		t.Fatal("Remove should find r2")
	}
	if pq.Len() != 2 {
		t.Fatalf("expected len 2 after Remove, got %d", pq.Len())
	}
	if pq.Remove("r2") {
		t.Fatal("Remove should not find r2 again")
	}

	e := pq.Dequeue()
	if e.Priority != 3 {
		t.Fatalf("expected priority 3 first after remove, got %d", e.Priority)
	}
	e = pq.Dequeue()
	if e.Priority != 1 {
		t.Fatalf("expected priority 1 second after remove, got %d", e.Priority)
	}
}

func TestEnqueueWithResultChan(t *testing.T) {
	pq := NewPriorityQueue(100, DefaultAgingRate)

	ch := make(chan DispatchResult, 1)
	_ = pq.Enqueue(makeReq("r1", 5, 100), nil, nil, ch)

	e := pq.Dequeue()
	if e.ResultChan == nil {
		t.Fatal("expected ResultChan to be set")
	}
	select {
	case e.ResultChan <- DispatchResult{WorkerId: "w1"}:
	default:
		t.Fatal("should be able to write to buffered ResultChan")
	}
}

func TestSetCapacityAppliesToNextPush(t *testing.T) {
	pq := NewPriorityQueue(2, DefaultAgingRate)
	for _, id := range []string{"r1", "r2"} {
		if err := pq.Enqueue(makeReq(id, 1, 100), nil, nil, nil); err != nil {
			t.Fatalf("enqueue %s: %v", id, err)
		}
	}

	pq.SetCapacity(3)
	if err := pq.Enqueue(makeReq("r3", 1, 100), nil, nil, nil); err != nil {
		t.Fatalf("a larger limit should admit r3, got %v", err)
	}

	pq.SetCapacity(1)
	if err := pq.Enqueue(makeReq("r4", 1, 100), nil, nil, nil); err != ErrQueueFull {
		t.Fatalf("a limit below the current depth should reject new entries, got %v", err)
	}
	if got := pq.Len(); got != 3 {
		t.Fatalf("queued entries must survive a shrink, want 3 got %d", got)
	}
}
