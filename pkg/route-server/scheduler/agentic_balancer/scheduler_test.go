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

package agentic_balancer

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"route-server/pkg/common/constant"
	"route-server/pkg/common/errs"
	"route-server/pkg/common/utils"
	"route-server/pkg/route-server/scheduler/agentic_balancer/controller"
	"route-server/pkg/route-server/scheduler/agentic_balancer/queue"
	"route-server/pkg/route-server/scheduler/agentic_balancer/state"
)

// testRequest is a minimal GenerateRequest impl for tests.
type testRequest struct {
	requestId    string
	reqType      string
	model        string
	promptTokens int32
	maxToken     int32
	stream       bool
	sessionId    string
	priority     int32
	outputLength int32
	scheduleCtx  context.Context
}

func (r *testRequest) GetRequestId() string                   { return r.requestId }
func (r *testRequest) GetType() string                        { return r.reqType }
func (r *testRequest) GetSLOTag() string                      { return "default" }
func (r *testRequest) GetSLOInterval() *utils.SLOInterval     { return nil }
func (r *testRequest) GetChat() []utils.ChatContent           { return nil }
func (r *testRequest) GetMaxToken() int32                     { return r.maxToken }
func (r *testRequest) GetPromptTokenCount() int32             { return r.promptTokens }
func (r *testRequest) IsStream() bool                         { return r.stream }
func (r *testRequest) GetModel() string                       { return r.model }
func (r *testRequest) GetPromptCacheKey() string              { return "" }
func (r *testRequest) GetApcStrategyType() string             { return "" }
func (r *testRequest) GetSessionId() string                   { return r.sessionId }
func (r *testRequest) GetPriority() int32                     { return r.priority }
func (r *testRequest) GetOutputLength() int32                 { return r.outputLength }
func (r *testRequest) GetScheduleContext() context.Context    { return r.scheduleCtx }
func (r *testRequest) SetScheduleContext(ctx context.Context) { r.scheduleCtx = ctx }

// setupSM builds a StateManager with the given prefill workers and installs it
// as the global singleton. Returns the manager and a cleanup func.
func setupSM(t *testing.T, workers ...*state.WorkerState) (*state.StateManager, func()) {
	t.Helper()
	sm := state.NewStateManager(state.TTLConfig{})
	for _, w := range workers {
		sm.Workers.Set(w)
	}
	state.SetGlobalStateManager(sm)
	return sm, func() { state.SetGlobalStateManager(nil) }
}

func prefWorker(id string, inflight, pending, maxSeq int64, kvUsage float64) *state.WorkerState {
	return &state.WorkerState{
		WorkerId:   id,
		Role:       constant.Prefill,
		InFlight:   inflight,
		Pending:    pending,
		MaxNumSeqs: maxSeq,
		Available:  true,
		Healthy:    true,
		KVCache:    state.KVCacheState{UsagePerc: kvUsage},
		UpdatedAt:  time.Now(),
	}
}

func newScheduler() *SessionAwareScheduler {
	s, _ := NewSessionAwareScheduler()
	return s.(*SessionAwareScheduler)
}

// --- ComputeWorkerUtil ---

func TestComputeLoadScore(t *testing.T) {
	sm, cleanup := setupSM(t)
	defer cleanup()

	w := prefWorker("w1", 0, 0, 0, 0)
	if got := state.ComputeWorkerUtil(sm, w); got != 0 {
		t.Errorf("maxSeq=0 guard: expected 0, got %v", got)
	}

	w = prefWorker("w2", 2, 1, 10, 0.5)
	if got := state.ComputeWorkerUtil(sm, w); got != 0.5 {
		t.Errorf("expected workerUtil 0.5 to win over queueLoad, got %v", got)
	}

	w = prefWorker("w3", 5, 5, 10, 0.1)
	for i := 0; i < 5; i++ {
		sm.Inflight.Add(&state.InflightRequest{
			RequestId: "w3-req-" + string(rune('0'+i)),
			WorkerId:  "w3",
		})
	}
	if got := state.ComputeWorkerUtil(sm, w); got < 0.9 || got > 0.91 {
		t.Errorf("expected queueLoad≈0.909, got %v", got)
	}

	w = prefWorker("w4", 0, 0, 9, 0.0)
	sm.Inflight.Add(&state.InflightRequest{RequestId: "r1", WorkerId: "w4"})
	sm.Inflight.Add(&state.InflightRequest{RequestId: "r2", WorkerId: "w4"})
	if got := state.ComputeWorkerUtil(sm, w); got != 0.2 {
		t.Errorf("expected live-inflight queueLoad 0.2, got %v", got)
	}
}

// --- computeSessionAffinityScore ---

func TestComputeSessionAffinityScore(t *testing.T) {
	_, cleanup := setupSM(t)
	defer cleanup()
	s := newScheduler()

	// nil entry → 0 (degradation)
	if got := s.computeSessionAffinityScore(nil, "w", 1.0); got != 0 {
		t.Errorf("nil entry should give 0, got %v", got)
	}

	entry := &state.SessionEntry{SessionId: "s1", AssignedWorker: []string{"wa"}}

	// assigned (index 0), cacheDecay=1.0 → 0.5^0 · 1.0 = 1.0
	if got := s.computeSessionAffinityScore(entry, "wa", 1.0); got != 1.0 {
		t.Errorf("assigned+cacheDecay=1 expected 1.0, got %v", got)
	}

	// load no longer enters affinity (admission + gate carry it, §4.4);
	// same inputs as above must give the same result regardless of load.

	// history index 1 (previous worker), cacheDecay=1.0 → 0.5^1 = 0.5
	entry2 := &state.SessionEntry{SessionId: "s2", AssignedWorker: []string{"wc", "wb"}}
	if got := s.computeSessionAffinityScore(entry2, "wb", 1.0); got != 0.5 {
		t.Errorf("history-index-1 expected 0.5, got %v", got)
	}

	// history index 2 → 0.5^2 = 0.25
	entry3 := &state.SessionEntry{SessionId: "s3", AssignedWorker: []string{"wd", "wc", "wb"}}
	if got := s.computeSessionAffinityScore(entry3, "wb", 1.0); got != 0.25 {
		t.Errorf("history-index-2 expected 0.25, got %v", got)
	}

	// unrelated worker → 0
	entry4 := &state.SessionEntry{SessionId: "s4", AssignedWorker: []string{"wa"}}
	if got := s.computeSessionAffinityScore(entry4, "wz", 1.0); got != 0 {
		t.Errorf("unrelated worker expected 0, got %v", got)
	}

	// cacheDecay < 1.0: assigned (index 0) → 1.0*0.5 = 0.5
	if got := s.computeSessionAffinityScore(entry, "wa", 0.5); got != 0.5 {
		t.Errorf("assigned+cacheDecay=0.5 expected 0.5, got %v", got)
	}

	// §4.3: the time factor applies to ALL history entries. wb idx=1 → 0.5^1 · 0.5.
	if got := s.computeSessionAffinityScore(entry2, "wb", 0.5); got != 0.25 {
		t.Errorf("history-index-1+cacheDecay=0.5 expected 0.25 (0.5^1 · 0.5), got %v", got)
	}
}

// --- gated score & admission (spec §4.2, §4.4) ---

func fixedUtil(loads map[string]float64) func(*state.StateManager, *state.WorkerState) float64 {
	return func(_ *state.StateManager, w *state.WorkerState) float64 { return loads[w.WorkerId] }
}

func TestGatedScoreMonotonicity(t *testing.T) {
	weights := scoreWeights{affinityWeight: 0.5, overloadUtil: 0.9}
	// fixed affinity → strictly lower score as load rises (∂S/∂u < 0)
	prev := math.Inf(1)
	for _, u := range []float64{0.1, 0.3, 0.5, 0.7, 0.89} {
		got := scoreCandidate(u, 1.0, weights)
		if got >= prev {
			t.Errorf("score must decrease with load: u=%v score=%v >= prev %v", u, got, prev)
		}
		prev = got
	}
	// gate → 0 as u → U⁻: pure-load score dominates, affinity contributes nothing
	nearGate := scoreCandidate(0.8999, 1.0, weights)
	pureLoad := scoreCandidate(0.8999, 0.0, weights)
	if math.Abs(nearGate-pureLoad) > 1e-4 {
		t.Errorf("affinity contribution must vanish near U: %v vs %v", nearGate, pureLoad)
	}
}

func TestSchedulableSubsetAdmission(t *testing.T) {
	sm, cleanup := setupSM(t)
	defer cleanup()
	s := newScheduler()
	weights := scoreWeights{affinityWeight: 0.5, overloadUtil: 0.9}

	wa := prefWorker("wa", 0, 0, 10, 0.5) // u=0.5
	wb := prefWorker("wb", 0, 0, 10, 0.9) // u=0.9 == U → excluded (u<U strict)
	wc := prefWorker("wc", 0, 0, 10, 0.95)
	cands := []*state.WorkerState{wa, wb, wc}

	usable, _ := s.schedulableSubset(sm, cands, weights.overloadUtil, fixedUtil(map[string]float64{"wa": 0.5, "wb": 0.9, "wc": 0.95}))
	if len(usable) != 1 || usable[0].WorkerId != "wa" {
		t.Fatalf("only wa(u=0.5) passes u<0.9; got %v", usable)
	}

	// over-loaded worker with the best affinity must NOT be selected even if
	// it is the session's assigned worker (§4.2 "important").
	entry := &state.SessionEntry{SessionId: "sess-hot", AssignedWorker: []string{"wc"}}
	req := &testRequest{requestId: "r1", reqType: constant.Prefill, sessionId: "sess-hot"}
	_, err := s.selectWorkerWithUtil(sm, fixedUtil(map[string]float64{"wa": 0.5, "wb": 0.9, "wc": 0.95}), req, entry, cands, true)
	if err != nil {
		t.Fatalf("selectWorker failed: %v", err)
	}
	if sm.Inflight.CountByWorker("wc") != 0 || sm.Inflight.CountByWorker("wa") != 1 {
		t.Fatalf("hot worker with affinity must not be picked: wa=%d wc=%d",
			sm.Inflight.CountByWorker("wa"), sm.Inflight.CountByWorker("wc"))
	}

	// E_r empty → sentinel, not a hard error (caller queues instead)
	_, err = s.selectWorkerWithUtil(sm, fixedUtil(map[string]float64{"wa": 0.95, "wb": 0.9, "wc": 0.95}), req, entry, cands, true)
	if !errors.Is(err, ErrNoSchedulableWorker) {
		t.Fatalf("expected ErrNoSchedulableWorker, got %v", err)
	}

	// commit=false selects but reserves nothing (§5.3 dry-run seam)
	id0, err := s.selectWorkerWithUtil(sm, fixedUtil(map[string]float64{"wa": 0.5, "wb": 0.9, "wc": 0.95}),
		&testRequest{requestId: "r-noop", reqType: constant.Prefill}, nil, cands, false)
	if err != nil || id0 != "wa" {
		t.Fatalf("dry-run select: id=%q err=%v", id0, err)
	}
	if sm.Inflight.Get("r-noop") != nil {
		t.Error("commit=false must not reserve inflight load")
	}

	// bestScore must start at -Inf, not 0: with zero affinity and load 0.8
	// the (negative) score is -0.8 and the candidate must still be selected
	// (§4.4 "even when all scores are negative"; under the current formula
	// S = -u + λ·a·g ≥ -u > -1 for admitted u, so load alone demonstrates the
	// initialization property).
	solo := []*state.WorkerState{prefWorker("wa", 0, 0, 10, 0.8)}
	id, err := s.selectWorkerWithUtil(sm, fixedUtil(map[string]float64{"wa": 0.8}),
		&testRequest{requestId: "r2", reqType: constant.Prefill}, nil, solo, true)
	if err != nil || id != "wa" {
		t.Fatalf("negative-score single candidate must still win: id=%q err=%v", id, err)
	}
}

func TestScoreLambdaZeroPicksLowestLoad(t *testing.T) {
	sm, cleanup := setupSM(t)
	defer cleanup()
	s := newScheduler()
	// λ=0 with session affinity attached to the higher-load worker
	req := &testRequest{requestId: "r1", reqType: constant.Prefill, sessionId: "sess-a"}
	entry := &state.SessionEntry{SessionId: "sess-a", AssignedWorker: []string{"whot"}}
	s.affinityWeight = 0
	id, err := s.selectWorkerWithUtil(sm, fixedUtil(map[string]float64{"wa": 0.1, "whot": 0.5}),
		req, entry, []*state.WorkerState{
			prefWorker("wa", 0, 0, 10, 0.1),
			prefWorker("whot", 0, 0, 10, 0.5),
		}, true)
	if err != nil || id != "wa" {
		t.Fatalf("λ=0 must pick lowest load regardless of affinity: id=%q err=%v", id, err)
	}
}

// --- DispatchRequest ---

func TestDispatchRequestSingleCandidate(t *testing.T) {
	sm, cleanup := setupSM(t, prefWorker("only", 0, 0, 10, 0.1))
	defer cleanup()
	s := newScheduler()
	s.running.Store(true)

	req := &testRequest{requestId: "r1", reqType: constant.Prefill, promptTokens: 100}
	id, skip, err := s.DispatchRequest(req, nil, nil)
	if err != nil || skip || id != "only" {
		t.Fatalf("expected only/skip=false/no-err, got id=%q skip=%v err=%v", id, skip, err)
	}
	if sm.Inflight.CountByWorker("only") != 1 {
		t.Errorf("inflight should record 1 request on 'only', got %d", sm.Inflight.CountByWorker("only"))
	}
}

func TestDispatchRequestBlacklist(t *testing.T) {
	_, cleanup := setupSM(t, prefWorker("wa", 0, 0, 10, 0.1), prefWorker("wb", 0, 0, 10, 0.1))
	defer cleanup()
	s := newScheduler()
	s.running.Store(true)

	black := map[string]interface{}{"wa": nil, "wb": nil}
	_, _, err := s.DispatchRequest(&testRequest{requestId: "r1", reqType: constant.Prefill}, nil, black)
	if err == nil {
		t.Fatal("all blacklisted: expected error to trigger fallback")
	}
}

func TestDispatchRequestNoCandidates(t *testing.T) {
	_, cleanup := setupSM(t) // no workers
	defer cleanup()
	s := newScheduler()
	s.running.Store(true)

	_, _, err := s.DispatchRequest(&testRequest{requestId: "r1", reqType: constant.Prefill}, nil, nil)
	if err == nil {
		t.Fatal("no candidates: expected error to trigger fallback")
	}
}

func TestDispatchRequestSessionAffinity(t *testing.T) {
	// wa is the assigned worker with slightly higher load; wb has lower load.
	// With affinity weight, assigned wa should still win when not overloaded.
	_, cleanup := setupSM(t,
		prefWorker("wa", 1, 0, 10, 0.2),
		prefWorker("wb", 0, 0, 10, 0.1),
	)
	defer cleanup()
	s := newScheduler()
	s.running.Store(true)

	// seed session with assigned worker wa
	entry := sessionSeed("sess-1", "wa")

	req := &testRequest{requestId: "r1", reqType: constant.Prefill, sessionId: "sess-1", priority: 5}
	id, _, err := s.DispatchRequest(req, nil, nil)
	if err != nil || id != "wa" {
		t.Fatalf("session affinity should pick assigned wa, got id=%q err=%v", id, err)
	}
	if entry.GetAssignedWorker() != "wa" {
		t.Errorf("AssignedWorker should be wa, got %q", entry.GetAssignedWorker())
	}
	if entry.GetPriority() != 5 {
		t.Errorf("Priority should be set to 5, got %d", entry.GetPriority())
	}
}

// sessionSeed creates a session entry via the global StateManager so
// DispatchRequest's GetOrCreate finds an assigned worker.
func sessionSeed(sid, assigned string) *state.SessionEntry {
	sm := state.GetGlobalStateManager()
	e := sm.Sessions.GetOrCreate(sid)
	e.SetAssignedWorker(assigned)
	return e
}

// --- degradation chain ---

func TestDispatchRequestNoStateManager(t *testing.T) {
	state.SetGlobalStateManager(nil)
	defer state.SetGlobalStateManager(nil)
	s := newScheduler()
	s.running.Store(true)

	_, _, err := s.DispatchRequest(&testRequest{requestId: "r1", reqType: constant.Prefill}, nil, nil)
	if err == nil {
		t.Fatal("expected error when StateManager is nil")
	}
}

// --- OnRequestEvent ---
//
// Note: RequestEventMsg.Request is a concrete *RemoteRequestWrapper, which does
// not implement sessionAwareRequest until module 6 adds GetSessionId/GetPriority.
// So the session-bookkeeping branch inside OnRequestEvent is not exercised here;
// these tests cover the inflight/prefix-cache paths that run pre-module-6.

func TestOnRequestEventPrefillSuccess(t *testing.T) {
	sm, cleanup := setupSM(t, prefWorker("wp", 0, 0, 10, 0.1))
	defer cleanup()
	s := newScheduler()

	// seed inflight
	sm.Inflight.Add(&state.InflightRequest{RequestId: "r1", WorkerId: "wp"})

	event := &utils.RequestEventMsg{
		Request:        &utils.RemoteRequestWrapper{Id: "r1"},
		TargetInstance: "wp",
		EventType:      constant.RequestPrefillSuccess,
	}
	s.OnRequestEvent(event)

	if sm.Inflight.Get("r1") != nil {
		t.Error("inflight r1 should be removed on PrefillSuccess")
	}
}

func TestOnRequestEventNonNewRemovesInflight(t *testing.T) {
	sm, cleanup := setupSM(t)
	defer cleanup()
	s := newScheduler()

	sm.Inflight.Add(&state.InflightRequest{RequestId: "r2", WorkerId: "wx"})
	// some non-New, non-PrefillSuccess event type
	event := &utils.RequestEventMsg{
		Request:   &utils.RemoteRequestWrapper{Id: "r2"},
		EventType: 2,
	}
	s.OnRequestEvent(event)
	if sm.Inflight.Get("r2") != nil {
		t.Error("inflight r2 should be removed on non-New event")
	}
}

// --- helpers ---

func TestComputeAffinityDecay(t *testing.T) {
	hl := 30 * time.Second
	// nil entry → 1.0 (no decay)
	if decay := computeAffinityDecay(nil, hl); decay != 1.0 {
		t.Errorf("expected 1.0 for nil entry, got %v", decay)
	}

	// no ActingSince set → 1.0 (no decay)
	entry := &state.SessionEntry{Phase: state.PhaseReasoning}
	if decay := computeAffinityDecay(entry, hl); decay != 1.0 {
		t.Errorf("expected 1.0 for no ActingSince, got %v", decay)
	}

	// non-positive half life → 1.0 (disabled, no decay)
	entry.SetActingSince(time.Now().Add(-60 * time.Second))
	if decay := computeAffinityDecay(entry, 0); decay != 1.0 {
		t.Errorf("expected 1.0 for halfLife=0, got %v", decay)
	}

	// just started (0s elapsed) → timeFactor ≈ 1.0
	entry2 := &state.SessionEntry{Phase: state.PhaseActing}
	entry2.SetActingSince(time.Now())
	if decay := computeAffinityDecay(entry2, hl); decay < 0.99 || decay > 1.0 {
		t.Errorf("expected ≈1.0 for just-started ACTING, got %v", decay)
	}

	// one half-life (30s) → 0.5
	entry3 := &state.SessionEntry{Phase: state.PhaseActing}
	entry3.SetActingSince(time.Now().Add(-30 * time.Second))
	if decay := computeAffinityDecay(entry3, hl); decay < 0.49 || decay > 0.51 {
		t.Errorf("expected ≈0.5 at one half-life, got %v", decay)
	}

	// two half-lives (60s) → 0.25 (half-life curve, not the old sigmoid ≈0)
	entry4 := &state.SessionEntry{Phase: state.PhaseActing}
	entry4.SetActingSince(time.Now().Add(-60 * time.Second))
	if decay := computeAffinityDecay(entry4, hl); decay < 0.24 || decay > 0.26 {
		t.Errorf("expected ≈0.25 at two half-lives, got %v", decay)
	}

	// REASONING phase but ActingSince still set → still decays on ActingSince
	entry5 := &state.SessionEntry{Phase: state.PhaseReasoning}
	entry5.SetActingSince(time.Now().Add(-60 * time.Second))
	if decay := computeAffinityDecay(entry5, hl); decay < 0.24 || decay > 0.26 {
		t.Errorf("expected ≈0.25 for REASONING with 60s ActingSince, got %v", decay)
	}
}

func TestLoadDirectionFix(t *testing.T) {
	_, cleanup := setupSM(t,
		prefWorker("wa", 0, 0, 10, 0.2),
		prefWorker("wb", 0, 0, 10, 0.8),
	)
	defer cleanup()
	s := newScheduler()
	s.running.Store(true)

	// no session → no affinity, scoring is just (1-load) (λ·a term is 0)
	// wa: load=0.2, score = 0.8; wb: load=0.8, score = 0.2
	// wa should win (lower load → higher score)
	req := &testRequest{requestId: "r1", reqType: constant.Prefill, promptTokens: 100}
	id, _, err := s.DispatchRequest(req, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != "wa" {
		t.Errorf("expected lower-load worker wa, got %s", id)
	}
}

func TestPhaseTransitionOnDispatchRequest(t *testing.T) {
	sm, cleanup := setupSM(t, prefWorker("wp", 0, 0, 10, 0.1))
	defer cleanup()
	s := newScheduler()
	s.running.Store(true)

	entry := sm.Sessions.GetOrCreate("sess-1")
	if entry.GetPhase() != state.PhaseActing {
		t.Fatalf("new session should be PhaseActing, got %s", entry.GetPhase())
	}

	req := &testRequest{requestId: "r1", reqType: constant.Prefill, sessionId: "sess-1"}
	_, _, err := s.DispatchRequest(req, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if entry.GetPhase() != state.PhaseReasoning {
		t.Errorf("expected PhaseReasoning after dispatch, got %s", entry.GetPhase())
	}
}

func TestPhaseTransitionOnRequestComplete(t *testing.T) {
	sm, cleanup := setupSM(t, prefWorker("wp", 0, 0, 10, 0.1))
	defer cleanup()
	s := newScheduler()

	// create a session in REASONING phase
	entry := sm.Sessions.GetOrCreate("sess-1")
	entry.TransitionToReasoning()
	if entry.GetPhase() != state.PhaseReasoning {
		t.Fatal("expected PhaseReasoning")
	}

	// simulate request completion event
	event := &utils.RequestEventMsg{
		Request:   &utils.RemoteRequestWrapper{Id: "r1", SessionId: "sess-1"},
		EventType: 2, // non-RequestNew
	}
	s.OnRequestEvent(event)

	// session should transition from REASONING → ACTING
	if entry.GetPhase() != state.PhaseActing {
		t.Errorf("expected PhaseActing after request complete, got %s", entry.GetPhase())
	}
	if entry.GetActingSince().IsZero() {
		t.Error("ActingSince should be set after transition")
	}
}

func TestFilterBlacklisted(t *testing.T) {
	in := []*state.WorkerState{
		{WorkerId: "a"}, {WorkerId: "b"}, {WorkerId: "c"},
	}
	out, skipped := filterBlacklisted(in, map[string]interface{}{"b": nil})
	if len(out) != 2 || out[0].WorkerId != "a" || out[1].WorkerId != "c" {
		t.Errorf("expected [a c], got %v", out)
	}
	if !skipped {
		t.Errorf("expected skipped=true when a candidate was filtered")
	}
	if out2, skipped2 := filterBlacklisted(in, nil); len(out2) != 3 || skipped2 {
		t.Errorf("nil blacklist should return all and skipped=false, got %d, %v", len(out2), skipped2)
	}
}

// TestEnqueueTriggerEquivalence pins the PR2a decision-path property: the
// queue trigger is now "E_r = ∅ inside the decision critical section", which
// must be exactly the legacy "all candidates ≥ U" condition (§4.2/§5.1).
func TestEnqueueTriggerEquivalence(t *testing.T) {
	sm, cleanup := setupSM(t)
	defer cleanup()
	s := newScheduler()
	s.overloadUtil = 0.95

	w1 := prefWorker("w1", 9, 0, 10, 0.96)
	w2 := prefWorker("w2", 9, 0, 10, 0.97)
	w3 := prefWorker("w3", 0, 0, 10, 0.80)
	sm.Workers.Set(w1)
	sm.Workers.Set(w2)
	sm.Workers.Set(w3)

	// both > 0.95 → E_r empty → queue trigger
	usable, _ := s.schedulableSubset(sm, []*state.WorkerState{w1, w2}, s.overloadUtil, state.ComputeWorkerUtil)
	if len(usable) != 0 {
		t.Errorf("workers at 0.96/0.97 must leave E_r empty, got %d usable", len(usable))
	}
	// one below → passes admission → no queue trigger
	usable, _ = s.schedulableSubset(sm, []*state.WorkerState{w1, w3}, s.overloadUtil, state.ComputeWorkerUtil)
	if len(usable) != 1 || usable[0] != w3 {
		t.Errorf("w3 at 0.80 must be the sole usable candidate, got %v", usable)
	}
	// u == U strict exclusion
	w4 := prefWorker("w4", 0, 0, 10, 0.95)
	sm.Workers.Set(w4)
	usable, _ = s.schedulableSubset(sm, []*state.WorkerState{w4}, s.overloadUtil, state.ComputeWorkerUtil)
	if len(usable) != 0 {
		t.Error("u == U must not pass admission")
	}
}

func TestDispatchRequestOverloadEnqueue(t *testing.T) {
	sm, cleanup := setupSM(t)
	defer cleanup()
	s := newScheduler()
	s.running.Store(true)
	s.overloadUtil = 0.95
	s.dispatchTimeout = 5 * time.Second
	s.pq = queue.NewPriorityQueue(100, queue.DefaultAgingRate)
	s.ctrl = controller.NewController(sm, controller.ControllerConfig{
		Interval:       100 * time.Millisecond,
		StaleWorkerTTL: 60 * time.Second,
		OverloadUtil:   0.95,
	}, nil, s.pq, s.drainRound)

	w1 := prefWorker("w1", 9, 0, 10, 0.96)
	sm.Workers.Set(w1)

	doneCh := make(chan string, 1)
	go func() {
		id, _, err := s.DispatchRequest(&testRequest{requestId: "r1", reqType: constant.Prefill, priority: 5}, nil, nil)
		if err != nil {
			doneCh <- "err:" + err.Error()
		} else {
			doneCh <- id
		}
	}()

	time.Sleep(200 * time.Millisecond)
	if s.pq.Len() != 1 {
		t.Fatalf("expected 1 queued request, got %d", s.pq.Len())
	}

	go s.ctrl.Run()
	defer s.ctrl.Stop()

	w1.KVCache.UsagePerc = 0.30
	w1.InFlight = 0
	sm.Workers.Set(w1)

	select {
	case result := <-doneCh:
		if result == "w1" {
		} else if len(result) > 4 && result[:4] == "err:" {
			t.Fatalf("dispatch should succeed after load drops, got %s", result)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("dispatch timed out waiting for dequeue")
	}
}

func TestDispatchRequestQueueFull(t *testing.T) {
	sm, cleanup := setupSM(t)
	defer cleanup()
	s := newScheduler()
	s.running.Store(true)
	s.overloadUtil = 0.95
	s.maxQueueSize = 1
	s.dispatchTimeout = 5 * time.Second
	s.pq = queue.NewPriorityQueue(1, queue.DefaultAgingRate)
	s.ctrl = controller.NewController(sm, controller.ControllerConfig{
		Interval:       10 * time.Second,
		StaleWorkerTTL: 60 * time.Second,
		OverloadUtil:   0.95,
	}, nil, s.pq, s.drainRound)

	w1 := prefWorker("w1", 9, 0, 10, 0.96)
	sm.Workers.Set(w1)

	go func() {
		s.DispatchRequest(&testRequest{requestId: "r1", reqType: constant.Prefill}, nil, nil)
	}()
	time.Sleep(100 * time.Millisecond)

	_, _, err := s.DispatchRequest(&testRequest{requestId: "r2", reqType: constant.Prefill}, nil, nil)
	if err != queue.ErrQueueFull {
		t.Fatalf("expected ErrQueueFull when queue is full, got %v", err)
	}
	s.ctrl.Stop()
}

func TestDispatchRequestOverloadTimeout(t *testing.T) {
	sm, cleanup := setupSM(t)
	defer cleanup()
	s := newScheduler()
	s.running.Store(true)
	s.overloadUtil = 0.95
	s.dispatchTimeout = 200 * time.Millisecond
	s.pq = queue.NewPriorityQueue(100, queue.DefaultAgingRate)
	s.ctrl = controller.NewController(sm, controller.ControllerConfig{
		Interval:       10 * time.Second,
		StaleWorkerTTL: 60 * time.Second,
		OverloadUtil:   0.95,
	}, nil, s.pq, s.drainRound)

	w1 := prefWorker("w1", 9, 0, 10, 0.96)
	sm.Workers.Set(w1)

	start := time.Now()
	_, _, err := s.DispatchRequest(&testRequest{requestId: "r1", reqType: constant.Prefill}, nil, nil)
	elapsed := time.Since(start)

	var rsErr *errs.RSError
	if !errors.As(err, &rsErr) || rsErr.MaErr.Code != errs.GatewayTimeoutErr.Code {
		t.Fatalf("expected GatewayTimeoutErr, got %v", err)
	}
	if elapsed < 150*time.Millisecond {
		t.Fatalf("timeout too early: %v", elapsed)
	}
	if s.pq.Len() != 0 {
		t.Fatalf("timed-out request should be removed from queue, got len=%d", s.pq.Len())
	}
}

func TestSelectWorkerTieBreakUniformity(t *testing.T) {
	const numWorkers = 4
	const numRequests = 1000
	// Tolerance must sit comfortably in the sampling-noise tail or the test
	// flakes: with p=1/k and n=1000, σ=sqrt(n·p·(1-p))≈13.7, so the band is
	// mean±(tolerance·mean). 0.15 ≈ 2.7σ fails ~2-3% of runs (observed).
	// 0.25 ≈ 4.5σ keeps it deterministic for a correct reservoir while still
	// rejecting a biased tie-break (which drives a count to ~0 or ~1000).
	const tolerance = 0.25

	sm, cleanup := setupSM(t,
		prefWorker("w1", 0, 0, 10, 0),
		prefWorker("w2", 0, 0, 10, 0),
		prefWorker("w3", 0, 0, 10, 0),
		prefWorker("w4", 0, 0, 10, 0),
	)
	defer cleanup()

	s := newScheduler()
	s.running.Store(true)

	hits := make(map[string]int)
	for i := 0; i < numRequests; i++ {
		req := &testRequest{
			requestId: fmt.Sprintf("r%d", i),
			reqType:   constant.Prefill,
		}
		id, _, err := s.DispatchRequest(req, nil, nil)
		if err != nil {
			t.Fatalf("request %d failed: %v", i, err)
		}
		hits[id]++
		sm.Inflight.Remove(req.GetRequestId())
	}

	expected := float64(numRequests) / float64(numWorkers)
	minHit := int(expected * (1 - tolerance))
	maxHit := int(expected * (1 + tolerance))

	for w := 1; w <= numWorkers; w++ {
		workerId := fmt.Sprintf("w%d", w)
		count := hits[workerId]
		t.Logf("worker %s: %d hits (expected ~%.0f, range [%d, %d])", workerId, count, expected, minHit, maxHit)
		if count < minHit || count > maxHit {
			t.Errorf("worker %s: got %d hits, expected within [%d, %d]", workerId, count, minHit, maxHit)
		}
	}
}

// --- additional ParseSessionContext header cases ---
// The basic cases (single source, absent, conflict, body fallback, correlation
// switch) live in session_context_test.go;
// the cases below add the Claude Code sub-agent parent fallback and Codex parent
// resolution edges.

func TestParseSessionContext_ClaudeCodeSubAgentParentFallback(t *testing.T) {
	h := http.Header{}
	h.Set("X-Claude-Code-Session-ID", "root-sess")
	h.Set("X-Claude-Code-Agent-ID", "sub-agent-1")
	// Without Parent-Agent-ID the parent falls back to the root Session-ID.
	sctx, err := ParseSessionContext(h, "", "", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sctx.SessionID != "sub-agent-1" {
		t.Errorf("expected 'sub-agent-1', got '%s'", sctx.SessionID)
	}
	if sctx.ParentSessionID != "root-sess" {
		t.Errorf("expected parent 'root-sess' (fallback), got '%s'", sctx.ParentSessionID)
	}
}

func TestParseSessionContext_CodexParentResolution(t *testing.T) {
	h := http.Header{}
	h.Set("thread-id", "thread-42")
	h.Set("X-Codex-Parent-Thread-ID", "parent-thread-1")
	sctx, err := ParseSessionContext(h, "", "", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sctx.SessionID != "thread-42" {
		t.Errorf("expected 'thread-42', got '%s'", sctx.SessionID)
	}
	if sctx.ParentSessionID != "parent-thread-1" {
		t.Errorf("expected parent 'parent-thread-1', got '%s'", sctx.ParentSessionID)
	}
}

func TestParseSessionContext_WhitespaceOnlyHeaderIgnored(t *testing.T) {
	h := http.Header{}
	h.Set("X-Dynamo-Session-ID", "   ")
	h.Set("thread-id", "valid-sess")
	sctx, err := ParseSessionContext(h, "", "", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sctx.SessionID != "valid-sess" {
		t.Errorf("expected 'valid-sess' (whitespace-only header ignored), got '%s'", sctx.SessionID)
	}
}

// --- config migration & hot update (spec §6.2/§6.3) ---

func TestApplyConfigLegacyMigration(t *testing.T) {
	_, cleanup := setupSM(t)
	defer cleanup()
	s := newScheduler()

	if err := s.Init(map[string]string{
		"beta-load":                "0.4",
		"gamma-affinity":           "0.2",
		"aging-factor":             "10.0",
		"aging-ttl":                "60s",
		"affinity-decay-mid-point": "30.0",
	}); err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	if s.affinityWeight != 0.5 {
		t.Errorf("β/γ migration: expected λ=0.5, got %v", s.affinityWeight)
	}
	if s.agingRate != 10.0/60.0 {
		t.Errorf("aging pair migration: expected η=1/6, got %v", s.agingRate)
	}
	if s.affinityHalfLife != 30*time.Second {
		t.Errorf("mid-point migration: expected H=30s, got %v", s.affinityHalfLife)
	}
}

func TestApplyConfigExplicitNewKeyWinsOverLegacy(t *testing.T) {
	_, cleanup := setupSM(t)
	defer cleanup()
	s := newScheduler()
	// Ordering guarantee: legacy converted first, explicit new key parsed
	// after → new key wins (spec §6.2 "both configured → no silent override":
	// here affinity-weight is the winner and migration logged a warning).
	if err := s.Init(map[string]string{
		"beta-load":       "0.4",
		"gamma-affinity":  "0.2", // would migrate to 0.5
		"affinity-weight": "0.9", // must win
	}); err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	if s.affinityWeight != 0.9 {
		t.Fatalf("expected explicit affinity-weight 0.9 to win, got %v", s.affinityWeight)
	}
}

func TestApplyConfigDegenerateLegacyFailsLoudNotMagic(t *testing.T) {
	_, cleanup := setupSM(t)
	defer cleanup()
	s := newScheduler()
	sawInit := false
	for _, w := range []struct{ b, g string }{{"0", "0.2"}, {"0", "0"}} {
		s2 := newScheduler()
		if err := s2.Init(map[string]string{"beta-load": w.b, "gamma-affinity": w.g}); err != nil {
			t.Fatalf("Init failed: %v", err)
		}
		// β=0 regimes have no finite λ and must not fabricate one; default kept.
		if s2.affinityWeight != s.affinityWeight {
			t.Errorf("degenerate legacy (β=%v γ=%v) must not change λ: expected %v, got %v",
				w.b, w.g, s.affinityWeight, s2.affinityWeight)
		}
		sawInit = true
	}
	if !sawInit {
		t.Fatal("test setup error")
	}
}

func TestApplyConfigInvalidOverloadUtilReverts(t *testing.T) {
	_, cleanup := setupSM(t)
	defer cleanup()
	s := newScheduler()
	if err := s.Init(map[string]string{"overload-util": "0"}); err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	if s.overloadUtil != 0.90 {
		t.Errorf("U=0 must revert to default 0.90 (gate divides by U), got %v", s.overloadUtil)
	}
	if err := s.UpdateConfig(map[string]string{"overload-util": "1.5"}); err != nil {
		t.Fatalf("UpdateConfig failed: %v", err)
	}
	if s.overloadUtil != 0.90 {
		t.Errorf("U=1.5 out of (0,1] must revert to default, got %v", s.overloadUtil)
	}
}

func TestUpdateConfigPreservesExplicitZeroAcrossRounds(t *testing.T) {
	_, cleanup := setupSM(t)
	defer cleanup()
	s := newScheduler()
	if err := s.Init(map[string]string{"affinity-weight": "0", "aging-rate": "0"}); err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	// Second round omits the keys entirely: explicit 0 (valid value) must
	// survive instead of falling back to defaults (AllowZero semantics).
	if err := s.UpdateConfig(map[string]string{"max-queue-size": "50"}); err != nil {
		t.Fatalf("UpdateConfig failed: %v", err)
	}
	if s.affinityWeight != 0 {
		t.Errorf("λ=0 must persist across config rounds, got %v", s.affinityWeight)
	}
	if s.agingRate != 0 {
		t.Errorf("η=0 must persist across config rounds, got %v", s.agingRate)
	}
}

func TestUpdateConfigHotUpdateInvariants(t *testing.T) {
	sm, cleanup := setupSM(t, prefWorker("wa", 0, 0, 10, 0.1))
	defer cleanup()
	s := newScheduler()
	if err := s.Init(map[string]string{"max-queue-size": "10"}); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	// Build observable state: one inflight, one session history, one queued
	// entry with a pre-aged clock.
	req := &testRequest{requestId: "r1", reqType: constant.Prefill, sessionId: "sess-1"}
	if _, err := s.selectWorker(sm, req, nil, sm.Workers.ListAvailableByRole(constant.Prefill), true); err != nil {
		t.Fatalf("selectWorker failed: %v", err)
	}
	entry := sm.Sessions.GetOrCreate("sess-1")
	entry.SetAssignedWorker("wa")
	if err := s.pq.Enqueue(req, nil, nil, nil); err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}
	queued := s.pq.Peek()
	queued.EnqueueAt = time.Now().Add(-30 * time.Second)

	if err := s.UpdateConfig(map[string]string{
		"affinity-weight":    "0.8",
		"overload-util":      "0.85",
		"aging-rate":         "2.0",
		"affinity-half-life": "10s",
	}); err != nil {
		t.Fatalf("UpdateConfig failed: %v", err)
	}

	if s.affinityWeight != 0.8 || s.overloadUtil != 0.85 || s.agingRate != 2.0 || s.affinityHalfLife != 10*time.Second {
		t.Fatalf("config not applied: %+v", s)
	}
	// §6.3 invariants: no counter reset, no session history wipe, no clock reset.
	if sm.Inflight.Get("r1") == nil {
		t.Error("UpdateConfig must not clear inflight counters")
	}
	if entry.WorkerHistoryIndex("wa") < 0 {
		t.Error("UpdateConfig must not clear session history")
	}
	if !s.pq.Peek().EnqueueAt.Equal(queued.EnqueueAt) {
		t.Error("UpdateConfig must not reset queued entry aging clock")
	}
}

// --- PR2c: event-driven wake (spec §5.4, §8.5) ---

// wakeTestScheduler builds a scheduler wired to a controller whose ticker
// interval is effectively disabled — any dispatch observed must therefore
// come from wake paths, not periodic checks.
func wakeTestScheduler(t *testing.T, sm *state.StateManager) *SessionAwareScheduler {
	t.Helper()
	s := newScheduler()
	s.running.Store(true)
	s.overloadUtil = 0.9
	s.dispatchTimeout = 10 * time.Second
	s.pq = queue.NewPriorityQueue(100, queue.DefaultAgingRate)
	s.ctrl = controller.NewController(sm, controller.ControllerConfig{
		Interval:       time.Hour, // fallback tick never fires during the test
		StaleWorkerTTL: time.Hour,
		OverloadUtil:   0.9,
	}, func() bool { return true }, s.pq, s.drainRound)
	go s.ctrl.Run()
	t.Cleanup(s.ctrl.Stop)
	return s
}

func TestCompletionEventDispatchesQueuedWithoutTick(t *testing.T) {
	sm, cleanup := setupSM(t,
		prefWorker("w1", 9, 0, 10, 0.96), // saturated → arrival queues
		prefWorker("w2", 0, 0, 10, 0.9),  // at U: excluded by u<U
	)
	defer cleanup()
	s := wakeTestScheduler(t, sm)

	done := make(chan string, 1)
	go func() {
		id, _, err := s.DispatchRequest(&testRequest{requestId: "waiter", reqType: constant.Prefill}, nil, nil)
		if err != nil {
			done <- "err:" + err.Error()
		} else {
			done <- id
		}
	}()
	time.Sleep(30 * time.Millisecond) // let it enqueue; wake drains find no capacity

	// A completion frees w1 AND opens w2 below U via a stats update; the
	// inflight release (wake source 2) must dispatch the waiter immediately.
	sm.Inflight.Add(&state.InflightRequest{RequestId: "done-req", WorkerId: "w1", StartTime: time.Now()})
	sm.Workers.Set(prefWorker("w1", 0, 0, 10, 0.1))
	sm.Workers.Set(prefWorker("w2", 0, 0, 10, 0.2))
	s.OnRequestEvent(&utils.RequestEventMsg{
		Request:   &utils.RemoteRequestWrapper{Id: "done-req"},
		EventType: constant.RequestDecodeSuccess,
	})

	select {
	case res := <-done:
		if strings.HasPrefix(res, "err:") {
			t.Fatalf("waiter not dispatched by completion event: %s", res)
		}
		if sm.Inflight.Get("waiter") == nil {
			t.Fatal("dispatched waiter must hold its reservation")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("completion event did not wake the queue (3s ≪ tick interval 1h)")
	}
}

func TestConfigChangeWakeDrainsQueue(t *testing.T) {
	// Wake source 4: UpdateConfig raising U restores capacity for the armed
	// queue; the wake must dispatch the waiter with the ticker disabled.
	sm, cleanup := setupSM(t,
		prefWorker("w1", 0, 0, 10, 0.95), // above U=0.9 → arrival queues it
	)
	defer cleanup()
	s := wakeTestScheduler(t, sm)

	done := make(chan string, 1)
	go func() {
		id, _, err := s.DispatchRequest(&testRequest{requestId: "waker", reqType: constant.Prefill}, nil, nil)
		done <- func() string {
			if err != nil {
				return "err:" + err.Error()
			}
			return id
		}()
	}()
	time.Sleep(30 * time.Millisecond)

	if err := s.UpdateConfig(map[string]string{"overload-util": "0.98"}); err != nil {
		t.Fatalf("UpdateConfig: %v", err)
	}

	select {
	case res := <-done:
		if res != "w1" {
			t.Fatalf("expected dispatch to w1 after capacity-restoring config, got %s", res)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("config change did not wake the queue")
	}
}

func TestNonLeaderNeverDispatchesOnWake(t *testing.T) {
	// §8.5 "no dispatch when not leader or stopping": wake arrives but the leader gate
	// must block the drain.
	sm, cleanup := setupSM(t,
		prefWorker("w1", 9, 0, 10, 0.96), // saturated → queue
	)
	defer cleanup()
	s := newScheduler()
	s.running.Store(true)
	s.overloadUtil = 0.9
	s.dispatchTimeout = 200 * time.Millisecond
	s.pq = queue.NewPriorityQueue(100, queue.DefaultAgingRate)
	s.ctrl = controller.NewController(sm, controller.ControllerConfig{
		Interval:       time.Hour,
		StaleWorkerTTL: time.Hour,
		OverloadUtil:   0.9,
	}, func() bool { return false }, s.pq, s.drainRound)
	go s.ctrl.Run()
	defer s.ctrl.Stop()

	_, _, err := s.DispatchRequest(&testRequest{requestId: "queued", reqType: constant.Prefill}, nil, nil)
	var rsErr *errs.RSError
	if !errors.As(err, &rsErr) || rsErr.MaErr.Code != errs.GatewayTimeoutErr.Code {
		t.Fatalf("non-leader queued request should time out, got %v", err)
	}
	if sm.Inflight.Count() != 0 {
		t.Errorf("non-leader must not dispatch: inflight=%d", sm.Inflight.Count())
	}
	if s.pq.Len() != 0 {
		t.Errorf("timed-out entry must leave the queue, len=%d", s.pq.Len())
	}
}

// --- PR2b: queue gate, scan-skip round, queue-full protocol (§5.1/§5.3/§5.5) ---

func TestGateKeepsNewRequestQueuedWhileBacklogArmed(t *testing.T) {
	sm, cleanup := setupSM(t,
		prefWorker("w1", 9, 0, 10, 0.96), // saturated: 10/11=0.909 ≥ U
		prefWorker("w2", 0, 0, 10, 0),    // free
	)
	defer cleanup()
	s := newScheduler()
	s.running.Store(true)
	s.dispatchTimeout = 60 * time.Second
	s.pq = queue.NewPriorityQueue(10, queue.DefaultAgingRate)

	// Seed the backlog: push a QUEUED entry directly (simulates the armed
	// queue after an overload episode) at lower priority than the newcomer.
	backlogReq := &testRequest{requestId: "backlog", reqType: constant.Prefill, priority: 0, outputLength: 1}
	if err := s.pq.Enqueue(backlogReq, nil, nil, make(chan queue.DispatchResult, 1)); err != nil {
		t.Fatalf("seed enqueue: %v", err)
	}

	// Newcomer sees free capacity (w2) but G_p=1 → must still queue (§5.1:
	// "new requests do not bypass a backlog"; queued, not dispatched directly).
	done := make(chan error, 1)
	go func() {
		_, _, err := s.DispatchRequest(&testRequest{requestId: "newcomer", reqType: constant.Prefill, priority: 9, outputLength: 1}, nil, nil)
		done <- err
	}()
	time.Sleep(50 * time.Millisecond)
	if sm.Inflight.Get("newcomer") != nil {
		t.Fatal("newcomer bypassed the queue gate")
	}
	if s.pq.Len() != 2 {
		t.Fatalf("both requests should be queued, got %d", s.pq.Len())
	}
	select {
	case r := <-done:
		t.Fatalf("newcomer unexpectedly returned early: %v", r)
	default:
	}

	// Hand-off round now dispatches the backlog FIRST (heap priority:
	// newcomer p9 actually wins — verify priority ordering, not FIFO):
	// newcomer (9/1) outranks backlog (0/1).
	if !s.drainRound() {
		t.Fatal("drainRound must dispatch one entry")
	}
	if sm.Inflight.Get("newcomer") == nil {
		t.Fatal("highest-weight entry must be handed off first (no FIFO promise)")
	}
	if sm.Inflight.Count() != 1 {
		t.Fatalf("exactly one hand-off per round step, got %d", sm.Inflight.Count())
	}
	// second round dispatches the backlog into w2 (still free)
	if !s.drainRound() {
		t.Fatal("second round must dispatch the backlog")
	}
	if sm.Inflight.Get("backlog") == nil {
		t.Fatal("backlog must be dispatched by the next round")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("newcomer wait failed after hand-off: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("newcomer waiter never woken")
	}
	// Queue drained → gate opens → a fresh request dispatches directly now.
	_, _, err := s.DispatchRequest(&testRequest{requestId: "afteropen", reqType: constant.Prefill}, nil, nil)
	// w2 now at 2/11=0.18, both w1 (0.909) excluded: afteropen goes to w2 or queues;
	// capacity: 3 on w2 = 3/11=0.27 <0.9 → direct success expected.
	if err != nil {
		t.Fatalf("after gate recovery direct dispatch should succeed: %v", err)
	}
	if sm.Inflight.Get("afteropen") == nil {
		t.Fatal("afteropen must have been reserved directly")
	}
}

func TestScanSkipBlockedHeadDoesNotBlockRest(t *testing.T) {
	sm, cleanup := setupSM(t,
		prefWorker("w1", 0, 0, 10, 0.1), // free
	)
	defer cleanup()
	s := newScheduler()
	s.running.Store(true)
	s.dispatchTimeout = 60 * time.Second
	s.pq = queue.NewPriorityQueue(10, queue.DefaultAgingRate)

	blocked := &testRequest{requestId: "blocked", reqType: constant.Prefill, priority: 5, outputLength: 1}
	other := &testRequest{requestId: "other", reqType: constant.Prefill, priority: 1, outputLength: 1}
	// blocked's blacklist hides the only free worker → head is undispatchable.
	if err := s.pq.Enqueue(blocked, nil, map[string]interface{}{"w1": nil}, make(chan queue.DispatchResult, 1)); err != nil {
		t.Fatal(err)
	}
	if err := s.pq.Enqueue(other, nil, nil, make(chan queue.DispatchResult, 1)); err != nil {
		t.Fatal(err)
	}

	// One round must hand off "other" despite the higher-priority blocked head.
	if !s.drainRound() {
		t.Fatal("round must dispatch the lower-priority dispatchable entry")
	}
	if sm.Inflight.Get("other") == nil {
		t.Fatal("other must be dispatched around the blocked head")
	}
	if sm.Inflight.Get("blocked") != nil {
		t.Fatal("blocked entry must not dispatch")
	}
	// The skipped head keeps its original EnqueueAt (§4.6: scan ≠ re-enqueue).
	remaining := s.pq.Peek()
	if remaining == nil || remaining.Request.GetRequestId() != "blocked" {
		t.Fatal("blocked entry must remain queued")
	}
}

func TestQueueFullProtocolRespectsPriority(t *testing.T) {
	sm, cleanup := setupSM(t,
		prefWorker("w1", 0, 0, 10, 0.1),
	)
	defer cleanup()
	s := newScheduler()
	s.running.Store(true)
	s.dispatchTimeout = 60 * time.Second
	s.pq = queue.NewPriorityQueue(2, queue.DefaultAgingRate)

	// Queue exactly full: low(p1) + fill(p2).
	low := &testRequest{requestId: "low", reqType: constant.Prefill, priority: 1, outputLength: 1}
	if err := s.pq.Enqueue(low, nil, nil, make(chan queue.DispatchResult, 1)); err != nil {
		t.Fatal(err)
	}
	filler := &testRequest{requestId: "fill", reqType: constant.Prefill, priority: 2, outputLength: 1}
	if err := s.pq.Enqueue(filler, nil, nil, make(chan queue.DispatchResult, 1)); err != nil {
		t.Fatal(err)
	}

	newcomer := &testRequest{requestId: "newcomer", reqType: constant.Prefill, priority: 9, outputLength: 1}
	done := make(chan string, 1)
	go func() {
		_, _, err := s.DispatchRequest(newcomer, nil, nil)
		if err != nil {
			done <- "err"
			return
		}
		done <- "ok"
	}()

	// §5.5: on ErrQueueFull the protocol frees a slot by handing off the
	// best dispatchable queued entry (fill p2, not low p1) — observable once
	// fill is reserved. The push of newcomer happens inside the same
	// critical section, so seeing fill reserved means newcomer is queued.
	deadline := time.Now().Add(3 * time.Second)
	for sm.Inflight.Get("fill") == nil && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if sm.Inflight.Get("fill") == nil {
		t.Fatal("queue-full protocol must hand off the best entry first (fill p2)")
	}
	if sm.Inflight.Get("low") != nil {
		t.Fatal("low-priority entry must not be the one preempting the slot")
	}

	// Second round dispatches the admitted newcomer (p9 > p1).
	if !s.drainRound() {
		t.Fatal("drain round must hand off the newcomer")
	}
	select {
	case res := <-done:
		if res != "ok" {
			t.Fatalf("newcomer should have been admitted and dispatched: %s", res)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("newcomer waiter never woken")
	}
	if s.pq.Len() != 1 || s.pq.Peek().Request.GetRequestId() != "low" {
		t.Fatalf("low must remain queued untouched, heap=%v", s.pq.Len())
	}
}

//
// Concurrency here is driven by barriers/channels and lock holds, never by
// sleeps racing against real work.

func TestConcurrentDirectDispatchNeverOverCounts(t *testing.T) {
	// 30 concurrent dispatchers on 4 workers, maxSeq=10, U=0.55.
	// u(n)=n/11: n≤6 admits (6/11=0.545<0.55), n=7 admits its last slot
	// while at n=6 (pre-dispatch contract §5.0: admission checks u *before*
	// the +1), after which u=7/11=0.636 excludes. So the exact cap is 7 per
	// worker, 28 total; the remaining 2 must queue and time out. This pins
	// decision-lock atomicity: a TOCTOU-prone implementation would let two
	// dispatchers both observe n=6 and push the same worker to n=8.
	sm, cleanup := setupSM(t,
		prefWorker("w1", 0, 0, 10, 0),
		prefWorker("w2", 0, 0, 10, 0),
		prefWorker("w3", 0, 0, 10, 0),
		prefWorker("w4", 0, 0, 10, 0),
	)
	defer cleanup()
	s := newScheduler()
	s.overloadUtil = 0.55
	s.running.Store(true)
	s.dispatchTimeout = 50 * time.Millisecond
	s.pq = queue.NewPriorityQueue(100, queue.DefaultAgingRate)

	const dispatchers = 30
	start := make(chan struct{})
	var wg sync.WaitGroup
	var mu sync.Mutex
	direct := map[string]int{}
	for i := 0; i < dispatchers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			req := &testRequest{requestId: fmt.Sprintf("c%d", i), reqType: constant.Prefill}
			id, _, err := s.DispatchRequest(req, nil, nil)
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				direct[id]++
			}
		}(i)
	}
	close(start)
	wg.Wait()

	totalDirect := 0
	for w, n := range direct {
		if n > 7 {
			t.Fatalf("worker %s over-admitted: %d > 7 slots (decision lock violated)", w, n)
		}
		totalDirect += n
	}
	if totalDirect != 28 {
		t.Errorf("expected exactly 28 slots consumed, got %d", totalDirect)
	}
	if s.pq.Len() != 0 {
		// queued requests must have timed out and removed themselves
		t.Errorf("timed-out queue requests not cleaned, len=%d", s.pq.Len())
	}
	if sm.Inflight.Count() != totalDirect {
		t.Errorf("inflight %d != dispatched %d", sm.Inflight.Count(), totalDirect)
	}
}

func TestReserveHandoffSingleAccounting(t *testing.T) {
	// End-to-end ownership: direct dispatch reserves +1 once; a RequestNew
	// event does NOT double-count; completion returns it exactly once.
	sm, cleanup := setupSM(t, prefWorker("wa", 0, 0, 10, 0.1))
	defer cleanup()
	s := newScheduler()
	s.running.Store(true)

	req := &testRequest{requestId: "r1", reqType: constant.Prefill}
	if _, _, err := s.DispatchRequest(req, nil, nil); err != nil {
		t.Fatalf("dispatch failed: %v", err)
	}
	if sm.Inflight.Count() != 1 {
		t.Fatalf("reserve must add exactly 1, got %d", sm.Inflight.Count())
	}

	wrapper := &utils.RemoteRequestWrapper{Id: "r1", SessionId: "s1"}
	s.OnRequestEvent(&utils.RequestEventMsg{Request: wrapper, TargetInstance: "wa", EventType: constant.RequestNew})
	if sm.Inflight.Count() != 1 {
		t.Fatalf("RequestNew must not re-account, got %d", sm.Inflight.Count())
	}

	s.OnRequestEvent(&utils.RequestEventMsg{Request: wrapper, TargetInstance: "wa", EventType: constant.RequestPrefillSuccess})
	if sm.Inflight.Count() != 0 {
		t.Fatalf("completion must release exactly once, got %d", sm.Inflight.Count())
	}
}

func TestTimeoutAfterHandoffAdoptedNoLeak(t *testing.T) {
	// Deterministic §5.6 interleave ("popped+reserved but result not yet
	// observable"): the controller hand-off and the waiter timeout race on
	// the decision lock. Both orders must keep inflight consistent:
	//   controller wins → result adopted (timeout branch drains it) → +1 stays
	//   waiter wins   → QUEUED removed → no reservation existed
	// Inflight can therefore be 0 or 1, never a ghost count above 1.
	sm, cleanup := setupSM(t, prefWorker("w1", 9, 0, 10, 0.96))
	defer cleanup()
	s := newScheduler()
	s.running.Store(true)
	s.overloadUtil = 0.95
	s.dispatchTimeout = 30 * time.Millisecond
	s.pq = queue.NewPriorityQueue(100, queue.DefaultAgingRate)
	s.ctrl = controller.NewController(sm, controller.ControllerConfig{
		Interval:       time.Millisecond,
		StaleWorkerTTL: 60 * time.Second,
		OverloadUtil:   0.95,
	}, func() bool { return true }, s.pq, s.drainRound)

	go s.ctrl.Run()
	defer s.ctrl.Stop()

	req := &testRequest{requestId: "r1", reqType: constant.Prefill}
	// free capacity shortly after the waiter is queued — the race window
	go func() {
		time.Sleep(10 * time.Millisecond)
		sm.Workers.Set(prefWorker("w1", 0, 0, 10, 0.3))
	}()

	_, _, err := s.DispatchRequest(req, nil, nil)
	if err != nil {
		// waiter won the race: timeout, nothing handed off → inflight 0
		if sm.Inflight.Count() != 0 {
			t.Fatalf("timeout path must leave no reservation, got %d", sm.Inflight.Count())
		}
		return
	}
	// controller won the race: timeout must have adopted the buffered result
	if sm.Inflight.Count() != 1 {
		t.Fatalf("adopted hand-off keeps its reservation, got %d", sm.Inflight.Count())
	}
	if sm.Inflight.Get("r1") == nil {
		t.Fatal("adopted request must be the reserved one")
	}
}

func TestReportLoadsMasksSessionID(t *testing.T) {
	const fullSession = "dynamo-secret-session-1234"
	sm, cleanup := setupSM(t, prefWorker("w1", 0, 0, 8, 0))
	defer cleanup()
	sm.Inflight.Add(&state.InflightRequest{
		RequestId:       "req-1",
		WorkerId:        "w1",
		SessionId:       fullSession,
		EstimatedTokens: 12,
	})

	loads := newScheduler().ReportLoads(true, false)
	if len(loads.Available) != 1 {
		t.Fatalf("available workers = %d, want 1", len(loads.Available))
	}
	info := loads.Available[0].Requests["req-1"]
	if info == nil {
		t.Fatal("include_requests must return the in-flight request")
	}
	if strings.Contains(info.Desc, fullSession) {
		t.Fatalf("load desc leaks the full session id: %s", info.Desc)
	}
	want := fmt.Sprintf("session=%s estTokens=12", maskSessionID(fullSession))
	if info.Desc != want {
		t.Fatalf("desc = %q, want %q", info.Desc, want)
	}
}
