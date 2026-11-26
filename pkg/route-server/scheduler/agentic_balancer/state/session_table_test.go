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
	"testing"
)

func TestSessionTableGetOrCreate(t *testing.T) {
	table := NewSessionTable()

	e1 := table.GetOrCreate("sess-1")
	if e1 == nil {
		t.Fatal("expected non-nil entry")
	}
	if e1.SessionId != "sess-1" {
		t.Errorf("expected SessionId=sess-1, got %s", e1.SessionId)
	}
	if e1.GetPhase() != PhaseActing {
		t.Errorf("expected PhaseActing, got %s", e1.GetPhase())
	}
	if e1.TurnId != 1 {
		t.Errorf("expected TurnId=1, got TurnId=%d", e1.TurnId)
	}

	e2 := table.GetOrCreate("sess-1")
	if e1 != e2 {
		t.Error("GetOrCreate should return the same pointer for same session")
	}

	if table.Count() != 1 {
		t.Errorf("expected count=1, got %d", table.Count())
	}
}

func TestSessionTableDelete(t *testing.T) {
	table := NewSessionTable()
	table.GetOrCreate("sess-1")
	table.GetOrCreate("sess-2")

	if table.Count() != 2 {
		t.Fatalf("expected count=2, got %d", table.Count())
	}

	table.Delete("sess-1")
	if table.Count() != 1 {
		t.Errorf("expected count=1 after delete, got %d", table.Count())
	}
	if table.Get("sess-1") != nil {
		t.Error("expected nil after delete")
	}
}

func TestSessionTableListByWorker(t *testing.T) {
	table := NewSessionTable()
	e1 := table.GetOrCreate("sess-1")
	e1.SetAssignedWorker("worker-a")

	e2 := table.GetOrCreate("sess-2")
	e2.SetAssignedWorker("worker-b")

	e3 := table.GetOrCreate("sess-3")
	e3.SetAssignedWorker("worker-a")

	result := table.ListByWorker("worker-a")
	if len(result) != 2 {
		t.Fatalf("expected 2 sessions on worker-a, got %d", len(result))
	}

	result = table.ListByWorker("worker-b")
	if len(result) != 1 {
		t.Errorf("expected 1 session on worker-b, got %d", len(result))
	}

	result = table.ListByWorker("worker-c")
	if len(result) != 0 {
		t.Errorf("expected 0 sessions on unknown worker, got %d", len(result))
	}
}

func TestSessionTableListActingByWorker(t *testing.T) {
	table := NewSessionTable()

	e1 := table.GetOrCreate("sess-1")
	e1.SetAssignedWorker("worker-a")
	e1.TransitionToActing()

	e2 := table.GetOrCreate("sess-2")
	e2.SetAssignedWorker("worker-a")
	e2.TransitionToReasoning()

	e3 := table.GetOrCreate("sess-3")
	e3.SetAssignedWorker("worker-b")
	e3.TransitionToActing()

	result := table.ListActingByWorker("worker-a")
	if len(result) != 1 {
		t.Fatalf("expected 1 acting session on worker-a, got %d", len(result))
	}
	if result[0].SessionId != "sess-1" {
		t.Errorf("expected sess-1, got %s", result[0].SessionId)
	}

	result = table.ListActingByWorker("worker-b")
	if len(result) != 1 {
		t.Errorf("expected 1 acting session on worker-b, got %d", len(result))
	}

	result = table.ListActingByWorker("worker-c")
	if len(result) != 0 {
		t.Errorf("expected 0 acting sessions on unknown worker, got %d", len(result))
	}
}

func TestSessionEntryTerminated(t *testing.T) {
	e := &SessionEntry{}

	if e.IsTerminated() {
		t.Error("new entry should not be terminated")
	}

	e.SetTerminated()
	if !e.IsTerminated() {
		t.Error("entry should be terminated after SetTerminated")
	}
}

func TestSessionEntryTransitions(t *testing.T) {
	e := &SessionEntry{}

	e.TransitionToActing()
	if e.GetPhase() != PhaseActing {
		t.Errorf("expected PhaseActing, got %s", e.GetPhase())
	}
	if e.GetActingSince().IsZero() {
		t.Error("ActingSince should be set")
	}

	e.TransitionToReasoning()
	if e.GetPhase() != PhaseReasoning {
		t.Errorf("expected PhaseReasoning, got %s", e.GetPhase())
	}

	e.NextTurn()
	if e.TurnId != 1 {
		t.Errorf("expected TurnId=1, got %d", e.TurnId)
	}
}

func TestSessionEntryAccessorConcurrency(t *testing.T) {
	e := &SessionEntry{
		AssignedWorker: make([]string, 0, 4),
	}
	done := make(chan struct{})

	const iterations = 1000
	go func() {
		for i := 0; i < iterations; i++ {
			e.SetAssignedWorker("w1")
			_ = e.GetAssignedWorker()
			_ = e.WorkerHistoryIndex("w1")
			e.SetPriority(int32(i))
			_ = e.GetPriority()
			e.SetTerminated()
			_ = e.IsTerminated()
		}
		done <- struct{}{}
	}()

	go func() {
		for i := 0; i < iterations; i++ {
			e.SetAssignedWorker("w2")
			_ = e.GetAssignedWorker()
			_ = e.WorkerHistoryIndex("w2")
			e.SetPriority(int32(i))
			_ = e.GetPriority()
			e.SetTerminated()
			_ = e.IsTerminated()
		}
		done <- struct{}{}
	}()

	<-done
	<-done
}

func TestSessionEntryWorkerHistory(t *testing.T) {
	e := &SessionEntry{
		AssignedWorker: make([]string, 0, 4),
	}

	if e.GetAssignedWorker() != "" {
		t.Error("expected empty assigned worker on new entry")
	}

	e.SetAssignedWorker("w1")
	if e.GetAssignedWorker() != "w1" {
		t.Errorf("expected w1, got %s", e.GetAssignedWorker())
	}
	if idx := e.WorkerHistoryIndex("w1"); idx != 0 {
		t.Errorf("expected index 0 for w1, got %d", idx)
	}

	e.SetAssignedWorker("w2")
	if e.GetAssignedWorker() != "w2" {
		t.Errorf("expected w2, got %s", e.GetAssignedWorker())
	}
	if idx := e.WorkerHistoryIndex("w1"); idx != 1 {
		t.Errorf("expected index 1 for w1, got %d", idx)
	}
	if idx := e.WorkerHistoryIndex("w2"); idx != 0 {
		t.Errorf("expected index 0 for w2, got %d", idx)
	}

	e.SetAssignedWorker("w3")
	if e.GetAssignedWorker() != "w3" {
		t.Errorf("expected w3, got %s", e.GetAssignedWorker())
	}
	if idx := e.WorkerHistoryIndex("w1"); idx != 2 {
		t.Errorf("expected index 2 for w1, got %d", idx)
	}
	if idx := e.WorkerHistoryIndex("w2"); idx != 1 {
		t.Errorf("expected index 1 for w2, got %d", idx)
	}
	if idx := e.WorkerHistoryIndex("w3"); idx != 0 {
		t.Errorf("expected index 0 for w3, got %d", idx)
	}
}

func TestSessionEntryWorkerHistoryDedup(t *testing.T) {
	e := &SessionEntry{
		AssignedWorker: make([]string, 0, 4),
	}

	e.SetAssignedWorker("w1")
	e.SetAssignedWorker("w2")
	e.SetAssignedWorker("w1")

	if e.GetAssignedWorker() != "w1" {
		t.Errorf("expected w1 after re-push, got %s", e.GetAssignedWorker())
	}
	if idx := e.WorkerHistoryIndex("w2"); idx != 1 {
		t.Errorf("expected w2 at index 1 after dedup, got %d", idx)
	}
	if e.WorkerHistoryIndex("w1") != 0 {
		t.Error("expected w1 at index 0")
	}
}

func TestSessionEntryWorkerHistoryMaxLength(t *testing.T) {
	e := &SessionEntry{
		AssignedWorker:   make([]string, 0, 4),
		maxWorkerHistory: 3,
	}

	e.SetAssignedWorker("w1")
	e.SetAssignedWorker("w2")
	e.SetAssignedWorker("w3")
	e.SetAssignedWorker("w4")

	if e.WorkerHistoryIndex("w1") != -1 {
		t.Error("w1 should be evicted when maxWorkerHistory=3")
	}
	if e.GetAssignedWorker() != "w4" {
		t.Errorf("expected w4, got %s", e.GetAssignedWorker())
	}
	if idx := e.WorkerHistoryIndex("w2"); idx != 2 {
		t.Errorf("expected w2 at index 2, got %d", idx)
	}
}

func TestSessionTableMaxWorkerHistory(t *testing.T) {
	table := NewSessionTable()
	table.SetMaxWorkerHistory(2)

	e := table.GetOrCreate("sess-1")
	e.SetAssignedWorker("w1")
	e.SetAssignedWorker("w2")
	e.SetAssignedWorker("w3")

	if e.WorkerHistoryIndex("w1") != -1 {
		t.Error("w1 should be evicted with maxWorkerHistory=2")
	}
	if e.GetAssignedWorker() != "w3" {
		t.Errorf("expected w3, got %s", e.GetAssignedWorker())
	}
}

func TestSessionTableListActive(t *testing.T) {
	table := NewSessionTable()

	table.GetOrCreate("sess-1")
	table.GetOrCreate("sess-2")

	e3 := table.GetOrCreate("sess-3")
	e3.SetTerminated()

	active := table.ListActive()
	if len(active) != 2 {
		t.Fatalf("expected 2 active sessions, got %d", len(active))
	}

	ids := make(map[string]struct{}, len(active))
	for _, entry := range active {
		ids[entry.SessionId] = struct{}{}
	}

	for _, expected := range []string{"sess-1", "sess-2"} {
		if _, ok := ids[expected]; !ok {
			t.Errorf("expected %s in active list, got %v", expected, ids)
		}
	}

	if _, ok := ids["sess-3"]; ok {
		t.Error("sess-3 should be excluded from active list")
	}
}

func TestSessionEntryClearAssignedWorker(t *testing.T) {
	e := &SessionEntry{
		AssignedWorker: make([]string, 0, 4),
	}

	e.SetAssignedWorker("w1")
	e.SetAssignedWorker("w2")
	e.SetAssignedWorker("w3")

	if e.GetAssignedWorker() != "w3" {
		t.Fatalf("expected w3 before clear, got %s", e.GetAssignedWorker())
	}

	e.ClearAssignedWorker()

	if e.GetAssignedWorker() != "" {
		t.Errorf("expected empty assigned worker after clear, got %s", e.GetAssignedWorker())
	}
	if idx := e.WorkerHistoryIndex("w1"); idx != -1 {
		t.Errorf("expected index -1 for w1 after clear, got %d", idx)
	}
	if idx := e.WorkerHistoryIndex("w2"); idx != -1 {
		t.Errorf("expected index -1 for w2 after clear, got %d", idx)
	}

	e.SetAssignedWorker("w4")
	if e.GetAssignedWorker() != "w4" {
		t.Errorf("expected w4 after rebuild, got %s", e.GetAssignedWorker())
	}
	if idx := e.WorkerHistoryIndex("w4"); idx != 0 {
		t.Errorf("expected index 0 for w4 after rebuild, got %d", idx)
	}
}
