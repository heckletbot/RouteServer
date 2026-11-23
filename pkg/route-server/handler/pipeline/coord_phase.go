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
	"time"

	"route-server/pkg/common/config"
	"route-server/pkg/common/constant"
	"route-server/pkg/common/errs"
	"route-server/pkg/common/logger"
	"route-server/pkg/route-server/scheduler/coord_disaggregation"
)

// CoordRoutePhase decides, once per request, whether it can be served entirely
// by one idle worker. It only orchestrates and writes the result back: the
// snapshot provider, the strategy and the reservation table all live in the
// scheduler layer. The phase never dispatches a request, so the regular
// schedule phase still does the network work.
type CoordRoutePhase struct {
	*BasePhase

	params     coord_disaggregation.Params
	strategy   coord_disaggregation.Strategy
	loadSource *SchedulePhase
}

func newCoordRoutePhase() Phase {
	return &CoordRoutePhase{
		BasePhase: NewBasePhase(constant.CoordRoutePhase, ""),
	}
}

// Configure injects the inputs resolved from the server config and pipeline at
// startup, so a request only does the work of a decision. loadSource is the
// prefill schedule phase: the phase runs its request-load estimator instead of
// keeping a second request_load list that could drift away from it.
func (p *CoordRoutePhase) Configure(params coord_disaggregation.Params, strategy coord_disaggregation.Strategy, loadSource *SchedulePhase) {
	p.params = params
	p.strategy = strategy
	p.loadSource = loadSource
}

func (p *CoordRoutePhase) ExecutePhase(ctx *PipelineContext) *errs.RSError {
	start := time.Now()

	p.loadSource.InitRequestLoad(ctx, p.SpanName())
	promptTokens := ctx.GetSubRequests(p.SpanName()).GetPromptTokenCount()

	router := coord_disaggregation.NewRouter(
		p.params,
		p.strategy,
		coord_disaggregation.NewInstanceStatsProvider(ctx.InferManager.InstanceMgr),
		coord_disaggregation.DefaultManager(),
	)
	decision := router.Route(coord_disaggregation.RouteRequest{
		RequestID:           ctx.RequestId(),
		PromptTokens:        promptTokens,
		HasKVTransferParams: ctx.RequestContext.ParsedRequest.KvTransferParams != nil,
	})

	ctx.RequestContext.CoordDecision = decision
	if decision.Mode == coord_disaggregation.ModeLocal {
		ctx.RequestContext.CoordReservation = decision.Reservation
		// A local route is served by one worker; pre-appointing it makes the
		// schedule phase skip DispatchRequest and send the request there.
		ctx.RequestContext.InferMsg.SetInstance(constant.Prefill, decision.WorkerID)
	}

	logger.Log().Infof("coord-route: request %s mode=%s fallback=%q worker=%q role=%q strategy=%s prompt_tokens=%d cost_ms=%.3f",
		ctx.RequestId(), decision.Mode, decision.Fallback, decision.WorkerID, decision.Role,
		decision.Strategy, promptTokens, float64(time.Since(start).Microseconds())/1000)

	return nil
}

// coordRoutingParams maps the routing config onto the decision inputs.
func coordRoutingParams(cfg *config.CoordRoutingConfig) coord_disaggregation.Params {
	return coord_disaggregation.Params{
		StaleDuration:   time.Duration(cfg.MetricsStaleSeconds * float64(time.Second)),
		IdleMaxWaiting:  cfg.IdleMaxWaiting,
		IdleMaxRunningP: cfg.IdleMaxRunningP,
		IdleMaxRunningD: cfg.IdleMaxRunningD,
		IdleMaxKVUsage:  cfg.IdleMaxKVUsage,
		PMaxInflight:    cfg.PHybridMaxInflight,
		DMaxInflight:    cfg.DHybridMaxInflight,
		PInputTokenGate: cfg.PHybridMaxInputTokens,
		DInputTokenGate: cfg.DHybridMaxInputTokens,
		LocalMinTokens:  cfg.LocalMinInputTokens,
	}
}
