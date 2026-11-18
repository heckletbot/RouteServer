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

package trace

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/trace"
	"route-server/pkg/common/config"
	"route-server/pkg/common/logger"
	"route-server/pkg/common/openai"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/sdk/resource"
	traceSDK "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

var once sync.Once

type OtelRequestTracer struct {
	tracer              trace.Tracer
	span                trace.Span
	reqId               string
	spanId              string
	userAlias           string
	enableTraceTokenLog bool
	mutex               sync.Mutex
	spanMap             map[string]trace.Span
	tracePermission     int
	spanName            string
	spanParentMap       map[string]string
}

func setupTracer(ctx context.Context, traceCfg *config.TraceConfig) error {
	exp, err := otlptracegrpc.New(ctx,
		otlptracegrpc.WithEndpoint(traceCfg.OtelConfig.JaegerEndpoint),
		otlptracegrpc.WithInsecure())
	if err != nil {
		return err
	}

	res, err := resource.New(ctx, resource.WithAttributes(semconv.ServiceName(traceCfg.OtelConfig.JaegerServiceName)))
	if err != nil {
		return err
	}

	ratioSampler := traceSDK.TraceIDRatioBased(traceCfg.OtelConfig.TraceSampleRate)
	sampler := traceSDK.ParentBased(ratioSampler)

	traceProvider := traceSDK.NewTracerProvider(
		traceSDK.WithResource(res),
		traceSDK.WithSampler(sampler),
		traceSDK.WithBatcher(exp, traceSDK.WithBatchTimeout(time.Duration(traceCfg.OtelConfig.SpanUpdateSeconds)*time.Second)),
	)

	otel.SetTracerProvider(traceProvider)
	return nil
}

func (ort *OtelRequestTracer) Init(ctx context.Context, traceCfg *config.TraceConfig, reqId string, header http.Header) {
	ort.reqId = reqId
	ort.spanId = GetSpanId(header)
	ort.userAlias = GetUserAlias(header)
	ort.enableTraceTokenLog = traceCfg.EnableTraceTokenLog
	ort.spanMap = make(map[string]trace.Span)
	ort.spanParentMap = make(map[string]string)
	ort.tracer = otelTracer
	ort.tracePermission = EnableOtelTrace
	ort.mutex = sync.Mutex{}
}

func (ort *OtelRequestTracer) Event(spanName string, eventName string, eventMsg string) {
	ort.mutex.Lock()
	defer ort.mutex.Unlock()
	span := ort.getSpan(spanName)
	if span == nil {
		return
	}
	span.AddEvent(eventName,
		trace.WithAttributes(attribute.String("message", eventMsg)),
		trace.WithTimestamp(time.Now()),
	)
}

func (ort *OtelRequestTracer) StartSpan(ctx context.Context, spanName string, extraInfo string, tracePermission int) context.Context {
	ort.mutex.Lock()
	defer ort.mutex.Unlock()
	childCtx, childSpan := ort.tracer.Start(ctx, spanName,
		trace.WithAttributes(attribute.String("stage.info", extraInfo)))
	if ort.span != nil {
		ort.spanParentMap[spanName] = ort.spanName
	} else {
		ort.spanParentMap[spanName] = ""
	}
	ort.spanMap[spanName] = childSpan
	ort.span = childSpan
	ort.spanName = spanName
	return childCtx
}

func (ort *OtelRequestTracer) EndSpan(spanName string, extraInfo string, tracePermission int) {
	ort.mutex.Lock()
	defer ort.mutex.Unlock()
	if len(ort.spanMap) == 0 {
		return
	}
	span, exists := ort.spanMap[spanName]
	if !exists {
		logger.Log().Errorf("Span [%s] is not exists", spanName)
		return
	}

	if extraInfo != "" {
		span.SetAttributes(attribute.String("end.info", extraInfo))
	}

	if ort.spanName == spanName {
		if parentSpan, exists := ort.spanParentMap[spanName]; exists {
			ort.spanName = parentSpan
			ort.span = ort.spanMap[parentSpan]
		}
	}
	span.End()
	delete(ort.spanMap, spanName)
	delete(ort.spanParentMap, spanName)
}

func (ort *OtelRequestTracer) ErrorSpan(spanName string, err error, tracePermission int) {
	ort.mutex.Lock()
	defer ort.mutex.Unlock()
	if len(ort.spanMap) == 0 {
		return
	}

	span, exists := ort.spanMap[spanName]
	if !exists {
		logger.Log().Errorf("Span [%s] is not exists", spanName)
		return
	}

	span.RecordError(err)
	span.SetStatus(codes.Error, err.Error())
	span.SetAttributes(
		attribute.String("error.type", fmt.Sprintf("%T", err)),
		attribute.String("error.message", err.Error()),
		attribute.String("error.time", time.Now().Format(time.RFC3339Nano)))

	if ort.spanName == spanName {
		if parentSpan, exists := ort.spanParentMap[spanName]; exists {
			ort.spanName = parentSpan
			ort.span = ort.spanMap[parentSpan]
		}
	}
	span.End()
	delete(ort.spanMap, spanName)
	delete(ort.spanParentMap, spanName)
}

func (ort *OtelRequestTracer) TraceTokenLog(spanName string, usage *openai.Usage, ttft float64, tpot float64, latency float64) {
	ort.mutex.Lock()
	defer ort.mutex.Unlock()
	if !ort.enableTraceTokenLog {
		return
	}
	span := ort.getSpan(spanName)
	if span == nil {
		return
	}
	if usage != nil {
		span.SetAttributes(
			attribute.Int("prompt_tokens", usage.PromptTokens),
			attribute.Int("completion_tokens", usage.CompletionTokens),
		)
	}
	span.SetAttributes(
		attribute.Float64("ttft.ms", ttft*1000),
		attribute.Float64("tpot.ms", tpot*1000),
		attribute.Float64("latency.ms", latency*1000),
	)
}

func (ort *OtelRequestTracer) GetTracePermission() int {
	return ort.tracePermission
}

// ort.span is remain for adapting to v0 infer module ,will be ignored in pipeline
func (ort *OtelRequestTracer) getSpan(spanName string) trace.Span {
	if spanName == "" {
		return ort.span
	} else {
		if _, exist := ort.spanMap[spanName]; exist {
			return ort.spanMap[spanName]
		}
		return nil
	}
}

func initOtelTrace(ctx context.Context, traceCfg *config.TraceConfig) {
	err := setupTracer(ctx, traceCfg)
	if err != nil {
		logger.Log().Error(err.Error())
	}
	curTracer := otel.Tracer("RouteServer")
	otelTracer = curTracer
}

func doOnceOtelTrace(ctx context.Context, traceCfg *config.TraceConfig) {
	once.Do(func() { initOtelTrace(ctx, traceCfg) })
}
