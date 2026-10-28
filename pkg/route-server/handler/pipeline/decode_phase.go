/*
 * Copyright 2026 The vLLM-Cloud Authors.
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
	"strings"
	"time"

	"infer-dp-scheduler/cmd/route_server/server/options"
	"infer-dp-scheduler/pkg/common/constant"
	"infer-dp-scheduler/pkg/common/errs"
	"infer-dp-scheduler/pkg/common/openai"
	"infer-dp-scheduler/pkg/common/utils"
	"infer-dp-scheduler/pkg/route-server/handler"
	"infer-dp-scheduler/pkg/route-server/handler/request"
	"infer-dp-scheduler/pkg/route-server/metrics"
)

func PrepareDecodeRequestBody(context *PipelineContext, copiedBody *utils.JSONModifier) *utils.JSONModifier {
	parsedReq := context.RequestContext.ParsedRequest

	if parsedReq.KvTransferParams != nil {
		copiedBody.Set(openai.KvTransferParamsKey, parsedReq.KvTransferParams)
	}

	if parsedReq.IsStream() {
		request.PrepareStreamOptions(copiedBody)
	}
	return copiedBody
}

func RemoveMessageRequestBody(context *PipelineContext, copiedBody *utils.JSONModifier) *utils.JSONModifier {
	copiedBody.Set(openai.ChatMessagesKey, []interface{}{})
	return copiedBody
}

func PostDecodeProcessResponse(res *http.Response, ctx *PipelineContext, phase Phase) (rsError *errs.RSError, details *utils.InstanceFailureDetails) {
	if ctx.RequestContext.CancelCtx != nil && ctx.RequestContext.CancelCtx.IsCancel.Load() {
		return errs.NewRSMaError(&errs.RequestForcedCancelledErr, phase.StageName(), errs.FailureTypeCanceled), nil
	}
	return writeResToClient(res, ctx, phase)
}

func PostDecodePhaseProcess(ctx *PipelineContext, insId string, startTime time.Time, stopTime time.Time, phase Phase) {
	perfMetrics := ctx.GetPerfMetrics()
	perfMetrics.FinishTime = stopTime
	var ttftSec float64 = 0
	var tpotSec float64 = 0
	usage := ctx.FinalUsage
	if perfMetrics.FirstToken.IsZero() {
		// no token is output
		return
	}
	if perfMetrics.FirstToken.After(perfMetrics.StartTime) {
		decodeDuration := stopTime.Sub(perfMetrics.FirstToken)
		ttftSec = perfMetrics.FirstToken.Sub(perfMetrics.StartTime).Seconds()
		if usage != nil && usage.CompletionTokens > 1 {
			if tpotSec = computeTPOT(ctx, startTime, stopTime, usage.CompletionTokens-1, decodeDuration); tpotSec > 0 {
				metrics.RecordTPOT(tpotSec, usage.CompletionTokens-1)
			}
		}
	}
	if ttftSec > 0 {
		metrics.RecordTTFT(ttftSec)
	}
	latency := stopTime.Sub(ctx.GetPerfMetrics().StartTime).Seconds()

	perfMetrics.Metrics.Store(constant.FirstTokenLatencyMs, perfMetrics.FirstToken.Sub(perfMetrics.StartTime).Milliseconds())
	perfMetrics.Metrics.Store(constant.FirstDTokenLatencyMs, perfMetrics.FirstDTokenTime.Sub(startTime).Milliseconds())
	perfMetrics.Metrics.Store(constant.SecondDTokenLatencyMs, perfMetrics.SecondDTokenTime.Sub(perfMetrics.FirstDTokenTime).Milliseconds())
	ctx.GetTracer().Tracer.TraceTokenLog(phase.SpanName(), ctx.FinalUsage, ttftSec, tpotSec, latency)
	traceEventPhaseDone(ctx, insId, startTime, stopTime, phase)
}

func writeResToClient(res *http.Response, ctx *PipelineContext, phase Phase) (rsError *errs.RSError, details *utils.InstanceFailureDetails) {
	var usage *openai.Usage
	var finishReason string
	var hasDoneWritten bool
	instanceId := ctx.RequestContext.InferMsg.GetInstance(phase.RelateInstanceType())
	reqCtx := ctx.GetRequestCtx()

	defer func() {
		ctx.FinalUsage = usage
		ctx.FinishReason = finishReason
		if details != nil {
			rsError = handler.RsErrorFromResponse(reqCtx.ParsedRequest.IsStream(), details, instanceId, phase.StageName())
		}
	}()

	if ctx.HasMainErr() {
		return nil, nil
	}
	ctx.GetGinCtxMutex().Lock()
	defer ctx.GetGinCtxMutex().Unlock()
	writerResponseHeaders(res, ctx)

	if reqCtx.ParsedRequest.IsStream() {
		passTokenTime := handler.GetPassTokenTime(ctx.IsChat(), ctx.IsLayerWise, reqCtx.ParsedRequest.GetMaxToken(), ctx.GetRequestCtx().IsFirstTokenFromPrefill.Load())
		if options.GetConfig().ServerConfig.V1StreamWritingEnable {
			usage, details, finishReason, hasDoneWritten = handler.WriteStreamResV1(ctx.GetRequestCtx().MainGinCtx, reqCtx.ParsedRequest, res,
				reqCtx.Tracer.Tracer, ctx.GetPerfMetrics(), instanceId, constant.StageDecode, ctx.InferManager.WorkerEventCollector,
				passTokenTime, ctx.GetRequestCtx().IsFirstTokenFromPrefill)
			ctx.GetRequestCtx().HasDoneWritten = hasDoneWritten
		} else {
			usage, details, finishReason, hasDoneWritten = handler.WriteStreamRes(ctx.GetRequestCtx().MainGinCtx, reqCtx.ParsedRequest, res,
				reqCtx.Tracer.Tracer, ctx.GetPerfMetrics(), instanceId, constant.StageDecode, ctx.InferManager.WorkerEventCollector,
				passTokenTime, ctx.GetRequestCtx().IsFirstTokenFromPrefill)
			ctx.GetRequestCtx().HasDoneWritten = hasDoneWritten
		}
	} else {
		var firstTokenBody []byte
		if ctx.GetRequestCtx().IsFirstTokenFromPrefill.Load() {
			firstTokenBody = ctx.RequestContext.ParsedRequest.GetFirstTokenBody()
		}
		usage, details, finishReason = handler.WriteNonStreamRes(ctx.GetRequestCtx().MainGinCtx, reqCtx.ParsedRequest, res, reqCtx.Tracer.Tracer, ctx.GetPerfMetrics(), instanceId, constant.StageDecode, ctx.IsLayerWise, ctx.IsMessages(), firstTokenBody)
	}
	return
}

func writerResponseHeaders(res *http.Response, ctx *PipelineContext) {
	rewriteReqID := ctx.ClientGinCtx.GetString(constant.RewriteRequestID)
	for key, vals := range res.Header {
		// if decodeRes.Header's key in resHeaderBlackSet -> filter
		if _, ok := ctx.InferManager.ResHeaderBlackSet[key]; ok {
			continue
		}
		// if resHeaderBlackSet has a key of prefix match mode, and decodeRes.Header's key match it -> filter
		if handler.IsHeaderPrefixMatching(key, ctx.InferManager.ResHeaderBlackSet) {
			continue
		}
		if key == constant.XRequestId && rewriteReqID != "" {
			idx := strings.LastIndexByte(rewriteReqID, '-')
			if idx != -1 {
				vals = []string{rewriteReqID[:idx]}
			}
		}

		if key == constant.HeaderContentLength {
			// Skip contentLength, will be filled when writing Body
			continue
		}
		for _, val := range vals {
			ctx.GetRequestCtx().MainGinCtx.Writer.Header().Set(key, val)
		}
	}
}
