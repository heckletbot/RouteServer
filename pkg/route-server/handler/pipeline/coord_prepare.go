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
	"net/http"
	"time"

	"route-server/pkg/common/errs"
	"route-server/pkg/common/utils"
	"route-server/pkg/route-server/scheduler/coord_disaggregation"
)

// isCoordLocal reports whether this request was routed to a single worker.
// The decision is the only source of truth; no skip flag or instance id is
// consulted, so the hooks cannot disagree with the phase that decided.
func isCoordLocal(ctx *PipelineContext) bool {
	return ctx.RequestContext.CoordDecision.Mode == coord_disaggregation.ModeLocal
}

// PrepareCoordRequestBody is the mode-aware prepare hook for prefill_execute.
//
// Local route: the worker serves the whole request, so the body is prepared as
// a decode body (no kv_transfer_params, no max_tokens=1/stream=false rewrite).
// PD route: exactly the body PreparePrefillRequestBody produces for the regular
// pipeline, so turning the feature off restores the baseline request.
func PrepareCoordRequestBody(ctx *PipelineContext, copiedBody *utils.JSONModifier) *utils.JSONModifier {
	if isCoordLocal(ctx) {
		return PrepareDecodeRequestBody(ctx, copiedBody)
	}
	return PreparePrefillRequestBody(ctx, copiedBody)
}

// PostCoordProcessResponse is the mode-aware response hook for prefill_execute.
// A local route forwards the response to the client and releases its
// reservation as soon as the body is written back, which keeps the slot free
// for the next request instead of waiting for the request to fully unwind.
func PostCoordProcessResponse(res *http.Response, ctx *PipelineContext, phase Phase) (*errs.RSError, *utils.InstanceFailureDetails) {
	if isCoordLocal(ctx) {
		rsErr, details := PostDecodeProcessResponse(res, ctx, phase)
		releaseCoordReservation(ctx)
		return rsErr, details
	}
	if rsErr, details := PostPrefillProcessResponse(res, ctx, phase); rsErr != nil {
		return rsErr, details
	}
	return CheckKVTransferParams(res, ctx, phase)
}

// PostCoordPhaseProcess is the mode-aware post-phase hook for prefill_execute.
// A local route records TTFT/TPOT like the decode stage does, because the same
// worker produced the tokens the client saw.
func PostCoordPhaseProcess(ctx *PipelineContext, insId string, startTime time.Time, stopTime time.Time, phase Phase) {
	if isCoordLocal(ctx) {
		PostDecodePhaseProcess(ctx, insId, startTime, stopTime, phase)
		return
	}
	PostPhaseProcess(ctx, insId, startTime, stopTime, phase)
}

// releaseCoordReservation returns the slot held by this request, if any.
// Releasing is idempotent, so the post-response hook and the request exit path
// can both call it without keeping a "already released" flag around.
func releaseCoordReservation(ctx *PipelineContext) {
	coord_disaggregation.DefaultManager().Release(ctx.RequestContext.CoordReservation)
}
