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
	"testing"

	"route-server/pkg/common/constant"
	"route-server/pkg/route-server/scheduler/agentic_balancer/state"
)

type staticSnapshots struct {
	workers []*state.WorkerState
}

func (s staticSnapshots) Snapshots() []*state.WorkerState { return s.workers }

func TestRouterLeavesWindowDecisionToStrategy(t *testing.T) {
	strategy, err := NewStrategy(StrategyImbalance, shippedParams())
	if err != nil {
		t.Fatalf("build strategy: %v", err)
	}
	router := NewRouter(shippedParams(), strategy, staticSnapshots{
		workers: []*state.WorkerState{
			workerState("p1", constant.Prefill),
			busyWorkerState("d1", constant.Decode),
		},
	}, NewManager())

	// 1024 is below the prefill window. The router must not reject it before
	// the strategy runs: the idle prefill role produces outside_role_window.
	decision := router.Route(RouteRequest{PromptTokens: 1024})
	if decision.Mode != ModePD || decision.Fallback != FallbackOutsideWindow {
		t.Fatalf("Route() = mode %q fallback %q, want pd / outside_role_window", decision.Mode, decision.Fallback)
	}

	busy := router
	busy.provider = staticSnapshots{
		workers: []*state.WorkerState{
			busyWorkerState("p1", constant.Prefill),
			busyWorkerState("d1", constant.Decode),
		},
	}
	decision = busy.Route(RouteRequest{PromptTokens: 1024})
	if decision.Fallback != FallbackNoIdleWorker {
		t.Fatalf("fallback = %q, want no_idle_worker when neither role is idle", decision.Fallback)
	}
}
