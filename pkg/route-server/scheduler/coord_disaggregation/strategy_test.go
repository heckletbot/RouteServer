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

package coord_disaggregation

import (
	"sync"
	"testing"
	"time"

	"route-server/pkg/common/constant"
	"route-server/pkg/route-server/scheduler/agentic_balancer/state"
)

func testParams() Params {
	return Params{
		StaleDuration:   3 * time.Second,
		IdleMaxWaiting:  0,
		IdleMaxRunningP: 0,
		IdleMaxRunningD: 0,
		IdleMaxKVUsage:  0.5,
		PMaxInflight:    2,
		DMaxInflight:    2,
		PInputTokenGate: 204800,
		DInputTokenGate: 1024,
		PMinTokens:      8,
		DMinTokens:      0,
	}
}

func shippedParams() Params {
	params := testParams()
	params.PMinTokens = 32768
	params.PInputTokenGate = 204800
	params.DMinTokens = 0
	params.DInputTokenGate = 1024
	return params
}

func workerState(id, role string) *state.WorkerState {
	return &state.WorkerState{
		WorkerId:   id,
		Role:       role,
		MaxNumSeqs: 4,
		Available:  true,
		Healthy:    true,
		UpdatedAt:  time.Now(),
	}
}

func busyWorkerState(id, role string) *state.WorkerState {
	worker := workerState(id, role)
	worker.InFlight = 3
	worker.Pending = 2
	return worker
}

func TestImbalanceOnlyRoutesWhenOneRoleIsIdle(t *testing.T) {
	strategy, err := NewStrategy(StrategyImbalance, testParams())
	if err != nil {
		t.Fatalf("build strategy: %v", err)
	}

	cases := []struct {
		name           string
		workers        []*state.WorkerState
		prompt         int32
		wantWorker     string
		wantRole       string
		wantWindowMiss bool
	}{
		{
			name:       "single idle prefill and busy decode routes to prefill",
			workers:    []*state.WorkerState{workerState("p1", constant.Prefill), busyWorkerState("d1", constant.Decode)},
			prompt:     100,
			wantWorker: "p1",
			wantRole:   constant.Prefill,
		},
		{
			name:       "single idle decode and busy prefill routes to decode",
			workers:    []*state.WorkerState{busyWorkerState("p1", constant.Prefill), workerState("d1", constant.Decode)},
			prompt:     100,
			wantWorker: "d1",
			wantRole:   constant.Decode,
		},
		{
			name:    "both roles idle stays on PD",
			workers: []*state.WorkerState{workerState("p1", constant.Prefill), workerState("d1", constant.Decode)},
			prompt:  100,
		},
		{
			name:    "both roles busy stays on PD",
			workers: []*state.WorkerState{busyWorkerState("p1", constant.Prefill), busyWorkerState("d1", constant.Decode)},
			prompt:  100,
		},
		{
			name:    "prompt at prefill gate is inclusive",
			workers: []*state.WorkerState{workerState("p1", constant.Prefill), busyWorkerState("d1", constant.Decode)},
			prompt:  204800,
			// the gate is inclusive: 204800 is still allowed
			wantWorker: "p1",
			wantRole:   constant.Prefill,
		},
		{
			name:           "prompt above prefill gate rejects prefill",
			workers:        []*state.WorkerState{workerState("p1", constant.Prefill), busyWorkerState("d1", constant.Decode)},
			prompt:         204801,
			wantWindowMiss: true,
		},
		{
			name:           "prompt above decode gate rejects decode",
			workers:        []*state.WorkerState{busyWorkerState("p1", constant.Prefill), workerState("d1", constant.Decode)},
			prompt:         1025,
			wantWindowMiss: true,
		},
		{
			name:       "stale snapshot is not idle",
			workers:    []*state.WorkerState{staleWorker("p1", constant.Prefill), busyWorkerState("d1", constant.Decode)},
			prompt:     100,
			wantWorker: "", // no idle prefill -> PD
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			worker, role, windowMiss := strategy.Decide(tc.workers, tc.prompt, NewManager())
			if worker != tc.wantWorker || role != tc.wantRole || windowMiss != tc.wantWindowMiss {
				t.Fatalf("Decide() = (%q, %q, %v), want (%q, %q, %v)",
					worker, role, windowMiss, tc.wantWorker, tc.wantRole, tc.wantWindowMiss)
			}
		})
	}
}

func staleWorker(id, role string) *state.WorkerState {
	worker := workerState(id, role)
	worker.UpdatedAt = time.Now().Add(-10 * time.Second)
	return worker
}

func TestImbalancePicksLowestUtilization(t *testing.T) {
	strategy, err := NewStrategy(StrategyImbalance, testParams())
	if err != nil {
		t.Fatalf("build strategy: %v", err)
	}

	loaded := workerState("p1", constant.Prefill)
	loaded.InFlight = 2
	loaded.KVCache.UsagePerc = 0.4
	idlest := workerState("p2", constant.Prefill)

	workers := []*state.WorkerState{loaded, idlest, busyWorkerState("d1", constant.Decode)}
	worker, role, windowMiss := strategy.Decide(workers, 100, NewManager())
	if windowMiss {
		t.Fatalf("a prompt inside the window must not be a window miss")
	}
	if worker != "p2" || role != constant.Prefill {
		t.Fatalf("Decide() = (%q, %q), want (p2, %q)", worker, role, constant.Prefill)
	}
}

func TestImbalanceRespectsReservationCap(t *testing.T) {
	params := testParams()
	params.PMaxInflight = 1
	strategy, err := NewStrategy(StrategyImbalance, params)
	if err != nil {
		t.Fatalf("build strategy: %v", err)
	}

	reserved := NewManager()
	if _, admitted := reserved.TryAcquire("p1", 1); !admitted {
		t.Fatalf("first reservation should be admitted")
	}

	workers := []*state.WorkerState{workerState("p1", constant.Prefill), busyWorkerState("d1", constant.Decode)}
	if worker, _, _ := strategy.Decide(workers, 100, reserved); worker != "" {
		t.Fatalf("full worker must not be chosen, got %q", worker)
	}
}

func TestReservationReleaseIsIdempotent(t *testing.T) {
	manager := NewManager()
	reservation, admitted := manager.TryAcquire("w1", 1)
	if !admitted {
		t.Fatalf("reservation should be admitted")
	}
	if _, admitted := manager.TryAcquire("w1", 1); admitted {
		t.Fatalf("second reservation must be rejected while the cap is 1")
	}

	manager.Release(reservation)
	manager.Release(reservation) // double release must not underflow
	if got := manager.InFlight("w1"); got != 0 {
		t.Fatalf("InFlight() = %d, want 0", got)
	}

	if _, admitted := manager.TryAcquire("w1", 1); !admitted {
		t.Fatalf("slot should be reusable after release")
	}
}

func TestReservationStaleTokenDoesNotFreeForeignSlot(t *testing.T) {
	manager := NewManager()
	first, _ := manager.TryAcquire("w1", 2)
	second, _ := manager.TryAcquire("w1", 2)

	manager.Release(first)
	manager.Release(first) // replaying the first token must not remove the second slot
	if got := manager.InFlight("w1"); got != 1 {
		t.Fatalf("InFlight() = %d, want 1", got)
	}

	manager.Release(second)
	if got := manager.InFlight("w1"); got != 0 {
		t.Fatalf("InFlight() = %d, want 0", got)
	}
}

func TestReservationConcurrentAcquireNeverExceedsCap(t *testing.T) {
	const cap = 2
	manager := NewManager()

	var admittedCount int
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, admitted := manager.TryAcquire("w1", cap); admitted {
				mu.Lock()
				admittedCount++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if admittedCount != cap {
		t.Fatalf("admitted %d reservations, want %d", admittedCount, cap)
	}
	if got := manager.InFlight("w1"); got != cap {
		t.Fatalf("InFlight() = %d, want %d", got, cap)
	}
}

func TestNewStrategyRejectsUnknownName(t *testing.T) {
	if _, err := NewStrategy("nope", testParams()); err == nil {
		t.Fatalf("unknown strategy must be rejected")
	}
}

func TestRoleWindowsUseShippedBounds(t *testing.T) {
	strategy, err := NewStrategy(StrategyImbalance, shippedParams())
	if err != nil {
		t.Fatalf("build strategy: %v", err)
	}

	prefillIdle := []*state.WorkerState{workerState("p1", constant.Prefill), busyWorkerState("d1", constant.Decode)}
	decodeIdle := []*state.WorkerState{busyWorkerState("p1", constant.Prefill), workerState("d1", constant.Decode)}

	cases := []struct {
		name           string
		workers        []*state.WorkerState
		prompt         int32
		wantWorker     string
		wantRole       string
		wantWindowMiss bool
	}{
		{name: "decode idle and 512 tokens routes to decode", workers: decodeIdle, prompt: 512, wantWorker: "d1", wantRole: constant.Decode},
		{name: "decode idle and 2048 tokens misses the decode window", workers: decodeIdle, prompt: 2048, wantWindowMiss: true},
		{name: "prefill idle and 1024 tokens misses the prefill window", workers: prefillIdle, prompt: 1024, wantWindowMiss: true},
		{name: "prefill idle and 40000 tokens routes to prefill", workers: prefillIdle, prompt: 40000, wantWorker: "p1", wantRole: constant.Prefill},
		{name: "zero prompt stays outside every window", workers: decodeIdle, prompt: 0, wantWindowMiss: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			worker, role, windowMiss := strategy.Decide(tc.workers, tc.prompt, NewManager())
			if worker != tc.wantWorker || role != tc.wantRole || windowMiss != tc.wantWindowMiss {
				t.Fatalf("Decide() = (%q, %q, %v), want (%q, %q, %v)",
					worker, role, windowMiss, tc.wantWorker, tc.wantRole, tc.wantWindowMiss)
			}
		})
	}
}

func TestDisabledDecodeWindowIsNeverChosen(t *testing.T) {
	params := shippedParams()
	params.DInputTokenGate = 0
	strategy, err := NewStrategy(StrategyImbalance, params)
	if err != nil {
		t.Fatalf("build strategy: %v", err)
	}

	workers := []*state.WorkerState{busyWorkerState("p1", constant.Prefill), workerState("d1", constant.Decode)}
	worker, role, windowMiss := strategy.Decide(workers, 512, NewManager())
	if worker != "" || role != "" || !windowMiss {
		t.Fatalf("Decide() = (%q, %q, %v), want a window miss", worker, role, windowMiss)
	}
}
