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
	"sync"
	"testing"
	"time"
)

func TestInflightTableAddRemove(t *testing.T) {
	table := NewInflightTable()

	req1 := &InflightRequest{
		RequestId: "req-1",
		WorkerId:  "worker-a",
		SessionId: "sess-1",
		StartTime: time.Now(),
	}
	table.Add(req1)

	if table.Count() != 1 {
		t.Errorf("expected count=1, got %d", table.Count())
	}

	got := table.Get("req-1")
	if got == nil || got.WorkerId != "worker-a" {
		t.Error("expected to get req-1 with worker-a")
	}

	table.Remove("req-1")
	if table.Count() != 0 {
		t.Errorf("expected count=0 after remove, got %d", table.Count())
	}
	if table.Get("req-1") != nil {
		t.Error("expected nil after remove")
	}
}

func TestInflightTableCountByWorkerTracksEveryMutation(t *testing.T) {
	table := NewInflightTable()

	table.Add(&InflightRequest{RequestId: "req-1", WorkerId: "worker-a", SessionId: "sess-1"})
	table.Add(&InflightRequest{RequestId: "req-2", WorkerId: "worker-a", SessionId: "sess-1"})
	table.Add(&InflightRequest{RequestId: "req-3", WorkerId: "worker-b", SessionId: "sess-2"})

	// Re-adding the same request on another worker moves the count, not doubles it.
	table.Add(&InflightRequest{RequestId: "req-1", WorkerId: "worker-b", SessionId: "sess-1"})
	if got := table.CountByWorker("worker-a"); got != 1 {
		t.Errorf("worker-a count = %d, want 1 after re-add moved req-1", got)
	}
	if got := table.CountByWorker("worker-b"); got != 2 {
		t.Errorf("worker-b count = %d, want 2 after re-add", got)
	}
	if got := table.Count(); got != 3 {
		t.Errorf("total count = %d, want 3", got)
	}

	// Removing an unknown id must leave the index untouched.
	table.Remove("missing")
	if got := table.CountByWorker("worker-b"); got != 2 {
		t.Errorf("worker-b count = %d, want 2 after removing unknown id", got)
	}

	// RemoveBySession drops every request of that session from both maps.
	table.RemoveBySession("sess-1")
	if got := table.CountByWorker("worker-a"); got != 0 {
		t.Errorf("worker-a count = %d, want 0 after session removal", got)
	}
	if got := table.CountByWorker("worker-b"); got != 1 {
		t.Errorf("worker-b count = %d, want 1 after session removal", got)
	}

	// Clear resets the index as well, so a later Add starts from zero.
	table.Clear()
	if got := table.CountByWorker("worker-b"); got != 0 {
		t.Errorf("worker-b count = %d, want 0 after clear", got)
	}
	table.Add(&InflightRequest{RequestId: "req-9", WorkerId: "worker-b"})
	if got := table.CountByWorker("worker-b"); got != 1 {
		t.Errorf("worker-b count = %d, want 1 after add post-clear", got)
	}
}

func TestInflightTableGetByWorker(t *testing.T) {
	table := NewInflightTable()

	table.Add(&InflightRequest{RequestId: "req-1", WorkerId: "worker-a", SessionId: "sess-1"})
	table.Add(&InflightRequest{RequestId: "req-2", WorkerId: "worker-a", SessionId: "sess-2"})
	table.Add(&InflightRequest{RequestId: "req-3", WorkerId: "worker-b", SessionId: "sess-1"})

	result := table.GetByWorker("worker-a")
	if len(result) != 2 {
		t.Errorf("expected 2 requests on worker-a, got %d", len(result))
	}

	result = table.GetByWorker("worker-b")
	if len(result) != 1 {
		t.Errorf("expected 1 request on worker-b, got %d", len(result))
	}

	result = table.GetByWorker("worker-c")
	if len(result) != 0 {
		t.Errorf("expected 0 requests on worker-c, got %d", len(result))
	}
}

func TestInflightTableGetBySession(t *testing.T) {
	table := NewInflightTable()

	table.Add(&InflightRequest{RequestId: "req-1", WorkerId: "worker-a", SessionId: "sess-1"})
	table.Add(&InflightRequest{RequestId: "req-2", WorkerId: "worker-b", SessionId: "sess-1"})
	table.Add(&InflightRequest{RequestId: "req-3", WorkerId: "worker-a", SessionId: "sess-2"})

	result := table.GetBySession("sess-1")
	if len(result) != 2 {
		t.Errorf("expected 2 requests for sess-1, got %d", len(result))
	}
}

func TestInflightTableCountByWorker(t *testing.T) {
	table := NewInflightTable()

	table.Add(&InflightRequest{RequestId: "req-1", WorkerId: "worker-a", SessionId: "sess-1"})
	table.Add(&InflightRequest{RequestId: "req-2", WorkerId: "worker-a", SessionId: "sess-2"})
	table.Add(&InflightRequest{RequestId: "req-3", WorkerId: "worker-b", SessionId: "sess-1"})

	if table.CountByWorker("worker-a") != 2 {
		t.Errorf("expected 2 for worker-a, got %d", table.CountByWorker("worker-a"))
	}
	if table.CountByWorker("worker-b") != 1 {
		t.Errorf("expected 1 for worker-b, got %d", table.CountByWorker("worker-b"))
	}
	if table.CountByWorker("worker-c") != 0 {
		t.Errorf("expected 0 for worker-c, got %d", table.CountByWorker("worker-c"))
	}
}

func TestInflightTableNilGuard(t *testing.T) {
	table := NewInflightTable()
	table.Add(nil)
	table.Add(&InflightRequest{RequestId: ""})
	if table.Count() != 0 {
		t.Errorf("expected 0 entries with nil/empty requests, got %d", table.Count())
	}
}

func TestInflightTableRemoveBySession(t *testing.T) {
	table := NewInflightTable()

	table.Add(&InflightRequest{RequestId: "req-1", WorkerId: "worker-a", SessionId: "sess-1"})
	table.Add(&InflightRequest{RequestId: "req-2", WorkerId: "worker-b", SessionId: "sess-1"})
	table.Add(&InflightRequest{RequestId: "req-3", WorkerId: "worker-a", SessionId: "sess-2"})
	table.Add(&InflightRequest{RequestId: "req-4", WorkerId: "worker-c", SessionId: "sess-3"})

	table.RemoveBySession("sess-1")

	if table.Get("req-1") != nil || table.Get("req-2") != nil {
		t.Error("expected req-1 and req-2 to be removed")
	}
	if table.Get("req-3") == nil || table.Get("req-4") == nil {
		t.Error("expected req-3 and req-4 to remain")
	}
	if table.Count() != 2 {
		t.Errorf("expected count=2 after RemoveBySession, got %d", table.Count())
	}

	table.RemoveBySession("nonexistent")
	if table.Count() != 2 {
		t.Errorf("expected count=2 after removing nonexistent session, got %d", table.Count())
	}
}

func TestInflightTableClear(t *testing.T) {
	table := NewInflightTable()

	table.Add(&InflightRequest{RequestId: "req-1", WorkerId: "worker-a", SessionId: "sess-1"})
	table.Add(&InflightRequest{RequestId: "req-2", WorkerId: "worker-b", SessionId: "sess-2"})
	table.Add(&InflightRequest{RequestId: "req-3", WorkerId: "worker-a", SessionId: "sess-3"})

	if table.Count() != 3 {
		t.Fatalf("expected count=3 before clear, got %d", table.Count())
	}

	table.Clear()

	if table.Count() != 0 {
		t.Errorf("expected count=0 after clear, got %d", table.Count())
	}
	if table.Get("req-1") != nil {
		t.Error("expected nil after clear")
	}
}

func TestInflightTableConcurrency(t *testing.T) {
	table := NewInflightTable()
	const goroutines = 8
	const iterations = 500

	var wg sync.WaitGroup
	wg.Add(goroutines)

	for g := 0; g < goroutines; g++ {
		go func(gid int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				reqId := fmt.Sprintf("req-%d-%d", gid, i)
				table.Add(&InflightRequest{
					RequestId: reqId,
					WorkerId:  fmt.Sprintf("worker-%d", gid%3),
					SessionId: fmt.Sprintf("sess-%d", gid%2),
					StartTime: time.Now(),
				})
				_ = table.Get(reqId)
				_ = table.CountByWorker(fmt.Sprintf("worker-%d", gid%3))
				_ = table.GetBySession(fmt.Sprintf("sess-%d", gid%2))
				_ = table.Count()
				table.Remove(reqId)
			}
		}(g)
	}
	wg.Wait()
}

func TestInflightTableRemoveBySessionConcurrency(t *testing.T) {
	table := NewInflightTable()
	const goroutines = 4
	const iterations = 300

	var wg sync.WaitGroup
	wg.Add(goroutines)

	for g := 0; g < goroutines; g++ {
		go func(gid int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				reqId := fmt.Sprintf("req-%d-%d", gid, i)
				table.Add(&InflightRequest{
					RequestId: reqId,
					WorkerId:  "worker-shared",
					SessionId: "sess-shared",
				})
				if i%50 == 0 {
					table.RemoveBySession("sess-shared")
				}
			}
		}(g)
	}
	wg.Wait()
}
