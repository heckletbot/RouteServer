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

package pipeline

import (
	"strings"
	"testing"

	commoncfg "route-server/pkg/common/config"
	"route-server/pkg/common/constant"
	"route-server/pkg/common/health"
	common_infer "route-server/pkg/common/infer"
	"route-server/pkg/route-server/scheduler/coord_disaggregation"
)

func newTestCoordContext() *PipelineContext {
	return &PipelineContext{
		ServerCfg: &commoncfg.ServerConfig{
			CoordRouting: &commoncfg.CoordRoutingConfig{Strategy: coord_disaggregation.StrategyImbalance},
		},
		RequestContext: &RequestContext{
			InferMsg: &health.InferMsg{SkipFlags: map[string]string{}},
		},
	}
}

func TestSkipIfCoordLocalOnlySkipsLocalRoutes(t *testing.T) {
	ctx := newTestCoordContext()

	if skip, _ := skipIfCoordLocal(ctx); skip {
		t.Fatalf("zero decision must not skip the decode stage")
	}

	ctx.RequestContext.CoordDecision = coord_disaggregation.Decision{Mode: coord_disaggregation.ModeLocal, WorkerID: "p1"}
	skip, reason := skipIfCoordLocal(ctx)
	if !skip {
		t.Fatalf("local route must skip the decode stage")
	}
	if reason == "" {
		t.Fatalf("skip reason must be reported")
	}
}

func TestReleaseCoordReservationIsIdempotentAcrossPaths(t *testing.T) {
	ctx := newTestCoordContext()
	manager := coord_disaggregation.DefaultManager()

	reservation, admitted := manager.TryAcquire("w1", 1)
	if !admitted {
		t.Fatalf("reservation should be admitted")
	}
	ctx.RequestContext.CoordReservation = reservation

	// The post-response path releases first, the request exit path runs later.
	releaseCoordReservation(ctx)
	releaseCoordReservation(ctx)

	if got := manager.InFlight("w1"); got != 0 {
		t.Fatalf("InFlight() = %d, want 0", got)
	}
	if _, admitted := manager.TryAcquire("w1", 1); !admitted {
		t.Fatalf("slot must be reusable after the request ended")
	}
}

func newTestPrefillSchedule(t *testing.T) *SchedulePhase {
	t.Helper()

	phase := &SchedulePhase{BasePhase: NewBasePhase(constant.SchedulePhase, "")}
	cfg := &common_infer.PhaseConfig{StageName: "Prefill"}
	cfg.ScheduleParams.InstanceType = constant.Prefill
	cfg.ScheduleParams.RequestLoad = []string{LoadTypeText}
	if err := phase.Init(cfg); err != nil {
		t.Fatalf("init prefill schedule phase: %v", err)
	}
	return phase
}

func newCoordPipeline(t *testing.T) *Pipeline {
	t.Helper()

	return &Pipeline{
		Name: "pd_coord",
		phases: []Phase{
			&CoordRoutePhase{BasePhase: NewBasePhase(constant.CoordRoutePhase, "")},
			newTestPrefillSchedule(t),
		},
	}
}

func TestConfigureCoordRoutingLeavesBaselinePipelineAlone(t *testing.T) {
	pipeline := &Pipeline{Name: "pd_separate"}

	if err := configureCoordRouting(pipeline, &commoncfg.ServerConfig{}); err != nil {
		t.Fatalf("a pipeline without coord wiring must start unchanged: %v", err)
	}
}

func TestConfigureCoordRoutingRejectsPhaseWithoutConfig(t *testing.T) {
	err := configureCoordRouting(newCoordPipeline(t), &commoncfg.ServerConfig{})
	if err == nil {
		t.Fatalf("coord phase without coord-routing config must fail startup")
	}
	if !strings.Contains(err.Error(), "coord-routing config") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestConfigureCoordRoutingRejectsConfigWithoutPhase(t *testing.T) {
	pipeline := &Pipeline{Name: "pd_separate"}
	cfg := &commoncfg.ServerConfig{CoordRouting: &commoncfg.CoordRoutingConfig{Strategy: coord_disaggregation.StrategyImbalance}}

	err := configureCoordRouting(pipeline, cfg)
	if err == nil {
		t.Fatalf("coord-routing without a coord phase must fail startup")
	}
	if !strings.Contains(err.Error(), "no coord route phase") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func validCoordRouting() *commoncfg.CoordRoutingConfig {
	return &commoncfg.CoordRoutingConfig{
		Strategy:              coord_disaggregation.StrategyImbalance,
		MetricsStaleSeconds:   3,
		IdleMaxKVUsage:        0.5,
		PHybridMaxInflight:    2,
		DHybridMaxInflight:    2,
		PLocalMinInputTokens:  32768,
		DLocalMinInputTokens:  0,
		PHybridMaxInputTokens: 204800,
		DHybridMaxInputTokens: 1024,
	}
}

func TestConfigureCoordRoutingRejectsUnknownStrategy(t *testing.T) {
	routing := validCoordRouting()
	routing.Strategy = "does-not-exist"
	cfg := &commoncfg.ServerConfig{CoordRouting: routing}

	err := configureCoordRouting(newCoordPipeline(t), cfg)
	if err == nil || !strings.Contains(err.Error(), "does-not-exist") {
		t.Fatalf("unknown strategy must fail startup, got %v", err)
	}
}

func TestConfigureCoordRoutingRejectsPipelineWithoutPrefillSchedule(t *testing.T) {
	pipeline := &Pipeline{
		Name:   "pd_coord",
		phases: []Phase{&CoordRoutePhase{BasePhase: NewBasePhase(constant.CoordRoutePhase, "")}},
	}
	cfg := &commoncfg.ServerConfig{CoordRouting: &commoncfg.CoordRoutingConfig{Strategy: coord_disaggregation.StrategyImbalance}}

	err := configureCoordRouting(pipeline, cfg)
	if err == nil {
		t.Fatalf("a coord phase without a prefill schedule phase has no request_load source")
	}
	if !strings.Contains(err.Error(), "prefill schedule phase") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestConfigureCoordRoutingInjectsParamsAndStrategy(t *testing.T) {
	cfg := &commoncfg.ServerConfig{CoordRouting: &commoncfg.CoordRoutingConfig{
		Strategy:              coord_disaggregation.StrategyImbalance,
		MetricsStaleSeconds:   2.5,
		IdleMaxWaiting:        1,
		IdleMaxRunningP:       2,
		IdleMaxRunningD:       3,
		IdleMaxKVUsage:        0.4,
		PHybridMaxInflight:    5,
		DHybridMaxInflight:    6,
		PLocalMinInputTokens:  4096,
		DLocalMinInputTokens:  16,
		PHybridMaxInputTokens: 100000,
		DHybridMaxInputTokens: 2048,
	}}
	pipeline := newCoordPipeline(t)

	if err := configureCoordRouting(pipeline, cfg); err != nil {
		t.Fatalf("configureCoordRouting() error = %v", err)
	}

	phase := pipeline.phaseByType(constant.CoordRoutePhase).(*CoordRoutePhase)
	if phase.strategy == nil || phase.strategy.Name() != coord_disaggregation.StrategyImbalance {
		t.Fatalf("strategy was not injected")
	}
	if phase.loadSource == nil || phase.loadSource != pipeline.schedulePhaseFor(constant.Prefill) {
		t.Fatalf("request load source was not injected")
	}
	if phase.params.PMaxInflight != 5 || phase.params.DMaxInflight != 6 {
		t.Fatalf("reservation caps not mapped: %+v", phase.params)
	}
	if phase.params.PMinTokens != 4096 || phase.params.DMinTokens != 16 || phase.params.DInputTokenGate != 2048 {
		t.Fatalf("token gates not mapped: %+v", phase.params)
	}
	if phase.params.IdleMaxRunningD != 3 || phase.params.IdleMaxKVUsage != 0.4 {
		t.Fatalf("idle thresholds not mapped: %+v", phase.params)
	}
}

func TestConfigureCoordRoutingRejectsInvalidWindows(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*commoncfg.CoordRoutingConfig)
		wantErr string
	}{
		{
			name:    "zero stale duration",
			mutate:  func(cfg *commoncfg.CoordRoutingConfig) { cfg.MetricsStaleSeconds = 0 },
			wantErr: "metrics-stale-seconds must be > 0",
		},
		{
			name: "minimum above gate",
			mutate: func(cfg *commoncfg.CoordRoutingConfig) {
				cfg.PLocalMinInputTokens = 32768
				cfg.PHybridMaxInputTokens = 1024
			},
			wantErr: "p-local-min-input-tokens (32768) exceeds p-hybrid-max-input-tokens (1024)",
		},
		{
			name: "both windows disabled",
			mutate: func(cfg *commoncfg.CoordRoutingConfig) {
				cfg.PLocalMinInputTokens = 0
				cfg.PHybridMaxInputTokens = 0
				cfg.DLocalMinInputTokens = 0
				cfg.DHybridMaxInputTokens = 0
			},
			wantErr: "both role windows are disabled",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			routing := validCoordRouting()
			tc.mutate(routing)
			err := configureCoordRouting(newCoordPipeline(t), &commoncfg.ServerConfig{CoordRouting: routing})
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("configureCoordRouting() = %v, want substring %q", err, tc.wantErr)
			}
		})
	}
}

func TestConfigureCoordRoutingRejectsRenamedMinimum(t *testing.T) {
	routing := validCoordRouting()
	old := 32768
	routing.LocalMinInputTokens = &old

	err := configureCoordRouting(newCoordPipeline(t), &commoncfg.ServerConfig{CoordRouting: routing})
	if err == nil || !strings.Contains(err.Error(), "local-min-input-tokens has been renamed") {
		t.Fatalf("configureCoordRouting() = %v, want the renamed-key error", err)
	}
}
