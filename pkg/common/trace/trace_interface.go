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

package trace

import (
	"context"
	"fmt"
	"net/http"
	"sync"

	"go.opentelemetry.io/otel/trace"
	"infer-dp-scheduler/pkg/common/config"
	"infer-dp-scheduler/pkg/common/constant"
	"infer-dp-scheduler/pkg/common/logger"
	"infer-dp-scheduler/pkg/common/openai"
)

type TracerWithContext struct {
	Tracer     RequestTracer
	E2ECtx     context.Context
	EncodeCtx  context.Context
	PrefillCtx context.Context
	DecodeCtx  context.Context
	// spanName -> context
	subCtxMutex sync.Mutex
	SubCtx      map[string]context.Context
}

func (t *TracerWithContext) GetSubCtx(spanName string) context.Context {
	t.subCtxMutex.Lock()
	defer t.subCtxMutex.Unlock()
	ctx := t.SubCtx[spanName]
	return ctx
}

func (t *TracerWithContext) NewSubCtx(spanName string) bool {
	t.subCtxMutex.Lock()
	defer t.subCtxMutex.Unlock()
	if _, exist := t.SubCtx[spanName]; exist {
		return false
	}
	t.SubCtx[spanName] = t.Tracer.StartSpan(t.E2ECtx, spanName, "", EnableOtelTrace)
	return true
}

func TraceTokenLog(xSpanId string, xUserAlias string, traceId string,
	usage *openai.Usage, ttft float64, tpot float64, latency float64) string {
	promptTokens := 0
	outputTokens := 0
	cachedTokens := 0
	if usage != nil {
		promptTokens = usage.PromptTokens
		outputTokens = usage.CompletionTokens
		if usage.PromptTokensDetails != nil {
			cachedTokens = usage.PromptTokensDetails.CachedTokens
		}
	}
	ttft = ttft * 1000
	tpot = tpot * 1000
	latency = latency * 1000
	logInfo := fmt.Sprintf("x-span-id=%s|x-user-alias=%s|"+
		"CompletionMetric: {'trace_id': '%s', 'num_prompt_tokens': %d, 'num_output_tokens': %d, 'num_cached_tokens': %d, "+
		"'ttft': %.2f, 'tpot': %.2f, 'latency': %.2f}",
		xSpanId,
		xUserAlias,
		traceId,
		promptTokens,
		outputTokens,
		cachedTokens,
		ttft,
		tpot,
		latency,
	)
	logger.Log().Info(logInfo)
	return logInfo
}

const (
	RequestSpanE2E    = "RequestE2E"
	RequestEncode     = "encode_stage"
	RequestPrefill    = "prefill_stage"
	RequestDecode     = "decode_stage"
	RequestDispatch   = "request_dispatch"
	RequestSubProcess = "request_sub_process"

	TraceEventEncodeStart        = "encode_start"
	TraceEventEncodeDone         = "encode_done"
	TraceEventPrefillStart       = "prefill_start"
	TraceEventPrefillDone        = "prefill_done"
	TraceEventDecodeStart        = "decode_start"
	TraceEventDecodeDone         = "decode_done"
	TraceEventUserCancelled      = "user_cancelled"
	TraceEventStreamProcessDone  = "stream_process_done"
	TraceEventDecodeGenerateDone = "decode_generate_done"

	TraceEventReceiveMetaServerReceived = "metaserver_received"
	TraceEventReceiveMetaServerDone     = "metaserver_done"

	TraceEventRemoteSchedule     = "remote_schedule"
	TraceEventDealRemoteSchedule = "deal_remote_schedule"

	spanStart = "start"
	spanEnd   = "end"
	spanError = "error"

	EnableNoTrace      = 1 << 0
	EnableLogTrace     = 1 << 1
	EnableOtelTrace    = 1 << 2
	EnableComposeTrace = 1 << 3

	GtLogPermission = EnableLogTrace | EnableOtelTrace | EnableComposeTrace
)

type RequestTracer interface {
	Init(ctx context.Context, traceCfg *config.TraceConfig, reqId string, header http.Header)
	Event(spanName string, eventName string, eventMsg string)
	StartSpan(ctx context.Context, spanName string, extraInfo string, tracePermission int) context.Context
	EndSpan(spanName string, extraInfo string, tracePermission int)
	ErrorSpan(spanName string, err error, tracePermission int)
	TraceTokenLog(spanName string, usage *openai.Usage, ttft float64, tpot float64, latency float64)
	GetTracePermission() int
}

var otelTracer trace.Tracer

func NewTracer(ctx context.Context, traceCfg *config.TraceConfig, reqId string, header http.Header) TracerWithContext {
	var tracer RequestTracer
	if traceCfg == nil {
		logger.Log().Errorf("trace configuration is empty, ignore and use no trace log")
		tracer = &noTracer{}
	} else {
		if len(traceCfg.Type) == 0 {
			logger.Log().Errorf("trace type list is empty, ignore and use no trace log")
			tracer = &noTracer{}
		} else {
			tracer = &ComposeTracer{}
		}
	}
	tracer.Init(ctx, traceCfg, reqId, header)
	tracerWithContext := TracerWithContext{
		Tracer: tracer,
		SubCtx: make(map[string]context.Context),
	}
	return tracerWithContext
}

func GetSpanId(header http.Header) string {
	return header.Get(constant.HeaderXSpanId)
}

func GetUserAlias(header http.Header) string {
	return header.Get(constant.HeaderXUserAlias)
}

type Attribute struct {
	Key   string
	Value float64
}
