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

package controller

import (
	"math"
	"testing"
	"time"

	"route-server/pkg/common/constant"
	"route-server/pkg/route-server/scheduler/agentic_balancer/state"
)

func newTestController(t *testing.T) (*Controller, *state.StateManager) {
	t.Helper()
	sm := state.NewStateManager(state.TTLConfig{
		SessionTTL: 30 * time.Minute,
	})
	cfg := ControllerConfig{
		Interval:       100 * time.Millisecond,
		StaleWorkerTTL: 60 * time.Second,
		OverloadUtil:   0.90,
	}
	c := NewController(sm, cfg, func() bool { return true }, nil, nil)
	return c, sm
}

func addWorker(sm *state.StateManager, id string, util float64) {
	sm.Workers.Set(&state.WorkerState{
		WorkerId:   id,
		Role:       constant.Prefill,
		MaxNumSeqs: 10,
		KVCache:    state.KVCacheState{UsagePerc: util},
		Available:  true,
		Healthy:    true,
		UpdatedAt:  time.Now(),
	})
}

func TestScheduledCheckNoOpWhenIdle(t *testing.T) {
	c, sm := newTestController(t)
	addWorker(sm, "w1", 0.3)

	c.ScheduledCheck()

	if sm.Sessions.Count() != 0 {
		t.Fatalf("expected 0 sessions, got %d", sm.Sessions.Count())
	}
}

func TestNonLeaderSkipsTick(t *testing.T) {
	sm := state.NewStateManager(state.TTLConfig{
		SessionTTL: 30 * time.Minute,
	})
	cfg := ControllerConfig{Interval: 100 * time.Millisecond}
	c := NewController(sm, cfg, func() bool { return false }, nil, nil)

	addWorker(sm, "w1", 0.95)
	entry := sm.Sessions.GetOrCreate("sess-1")
	entry.SetAssignedWorker("w1")
	entry.TransitionToActing()

	c.ScheduledCheck()

	if entry.GetAssignedWorker() != "w1" {
		t.Fatalf("ScheduledCheck should still run when called directly; expected assigned worker w1, got %s", entry.GetAssignedWorker())
	}
}

func TestComputeWorkerUtilShared(t *testing.T) {
	sm := state.NewStateManager(state.TTLConfig{})
	w := &state.WorkerState{
		WorkerId:   "w1",
		Pending:    5,
		MaxNumSeqs: 10,
		KVCache:    state.KVCacheState{UsagePerc: 0.3},
	}
	util := state.ComputeWorkerUtil(sm, w)
	expected := (0.0 + 5.0) / (1.0 + 10.0)
	if math.Abs(util-expected) > 0.01 {
		t.Errorf("expected util≈%.4f, got %.4f", expected, util)
	}

	sm.Inflight.Add(&state.InflightRequest{RequestId: "r1", WorkerId: "w1"})
	utilLive := state.ComputeWorkerUtil(sm, w)
	expectedLive := (1.0 + 5.0) / (1.0 + 10.0)
	if math.Abs(utilLive-expectedLive) > 0.01 {
		t.Errorf("expected util≈%.4f with live inflight, got %.4f", expectedLive, utilLive)
	}
}
