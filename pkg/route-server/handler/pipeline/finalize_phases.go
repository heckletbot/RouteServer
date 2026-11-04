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
	"fmt"
	"net/http"
	"strconv"
	"time"

	"route-server/pkg/common/constant"
	"route-server/pkg/common/errs"
	"route-server/pkg/common/logger"
	"route-server/pkg/common/trace"
	"route-server/pkg/route-server/handler"
	"route-server/pkg/route-server/metrics"
)

const (
	SuccessPhaseName        = "default_success"
	FailPhaseName           = "default_fail"
	VllmAscendFailPhaseName = "vllm_ascend_fail"
)

func newInferSuccessPhase() Phase {
	return &InferSuccessPhase{
		BasePhase: NewBasePhase(SuccessPhaseName, constant.StageFinalize),
	}
}

func newInferFailurePhase() Phase {
	return &InferFailPhase{
		BasePhase: NewBasePhase(FailPhaseName, constant.StageFinalize),
	}
}

func newVllmAscendFailPhase() Phase {
	return &VllmAscendFailPhase{
		InferFailPhase: &InferFailPhase{
			BasePhase: NewBasePhase(VllmAscendFailPhaseName, constant.StageFinalize),
		},
	}
}

type InferSuccessPhase struct {
	*BasePhase
}

func (i *InferSuccessPhase) ExecutePhase(ctx *PipelineContext) (rsError *errs.RSError) {
	finalizeContext(ctx)
	finishTime := time.Now()
	finishSuccessMetrics(ctx, finishTime)

	return nil
}

func finishSuccessMetrics(ctx *PipelineContext, finishTime time.Time) {
	perfMetrics := ctx.GetPerfMetrics()
	perfMetrics.FinishTime = finishTime
	requestDuration := finishTime.Sub(perfMetrics.StartTime)
	if requestDuration.Nanoseconds() > 0 {
		metrics.RecordRequestDuration(requestDuration.Seconds())
	}
	metrics.RecordSuccessedRequest()
	metrics.RecordFailedRequestStatus(strconv.Itoa(http.StatusOK), ctx.RequestPath())

	ctx.GetTracer().Tracer.EndSpan(trace.RequestSpanE2E,
		fmt.Sprintf("success to infer request, finish_reason = %s,%s",
			ctx.FinishReason, perfMetrics.MetricsString()), trace.GtLogPermission)
}

func computeTPOT(ctx *PipelineContext, startTime, finishTime time.Time, tokens int, decodeDuration time.Duration) float64 {
	if tokens == 0 {
		return 0
	}

	if ctx.GetRequestCtx().ParsedRequest != nil && ctx.GetRequestCtx().ParsedRequest.IsStream() {
		return decodeDuration.Seconds() / float64(tokens)
	}
	perfMetrics := ctx.GetPerfMetrics()
	if !perfMetrics.DecodeStart.IsZero() {
		return finishTime.Sub(perfMetrics.DecodeStart).Seconds() / float64(tokens)
	}
	return finishTime.Sub(startTime).Seconds() / float64(tokens)
}

type InferFailPhase struct {
	*BasePhase
}

func (i *InferFailPhase) ExecutePhase(ctx *PipelineContext) (rsError *errs.RSError) {
	finalizeContext(ctx)

	if ctx.HasMainErr() {
		var retCode int

		ctx.GetGinCtxMutex().Lock()
		if ctx.GetRequestCtx().IsStreamResponse {
			retCode = handler.ProcessStreamRequestError(ctx.ClientGinCtx, ctx.RequestContext.RsErr, ctx.GetTracer().Tracer, ctx.FinishReason, ctx.RequestContext.HasDoneWritten)
		} else {
			retCode = handler.ProcessRequestError(ctx.ClientGinCtx, ctx.RequestContext.RsErr, ctx.GetTracer().Tracer, ctx.FinishReason)
		}
		ctx.GetGinCtxMutex().Unlock()
		if retCode != 0 {
			metrics.RecordFailedRequestStatus(strconv.Itoa(retCode), ctx.RequestPath())
		}
	} else {
		logger.Log().Warnf("request [%s] run fail phase but no rsError is found", ctx.RequestId())
	}
	return nil
}

type VllmAscendFailPhase struct {
	*InferFailPhase
}

func (i *VllmAscendFailPhase) ExecutePhase(ctx *PipelineContext) (rsError *errs.RSError) {
	if ctx.HasMainErr() {
		rsErr := ctx.RequestContext.RsErr
		if rsErr.ErrorStage == constant.StageDecode {
			prefillerId := ctx.RequestContext.InferMsg.GetInstance(constant.Prefill)
			if prefillerId != "" {
				ctx.InferManager.RecordVllmAscendAbortedRequest(prefillerId, ctx.RequestId())
			}
		}
	}

	return i.InferFailPhase.ExecutePhase(ctx)
}

func finalizeContext(ctx *PipelineContext) {
	if reqId := ctx.RequestId(); reqId != "" {
		ctx.InferManager.RunningRequestManager.DeleteReqCancelCtx(reqId)
	}
}
