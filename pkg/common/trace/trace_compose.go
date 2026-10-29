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
	"net/http"

	"infer-dp-scheduler/pkg/common/config"
	"infer-dp-scheduler/pkg/common/logger"
	"infer-dp-scheduler/pkg/common/openai"
)

type ComposeTracer struct {
	reqId               string
	spanId              string
	userAlias           string
	enableTraceTokenLog bool
	traceList           []RequestTracer
	otelIndex           int
	tracePermission     int
}

func (ct *ComposeTracer) Init(ctx context.Context, traceCfg *config.TraceConfig, reqId string, header http.Header) {
	ct.otelIndex = -1
	ct.tracePermission = EnableComposeTrace
	for index, traceType := range traceCfg.Type {
		switch traceType {
		case config.TraceTypeLog:
			ct.traceList = append(ct.traceList, &LogRequestTracer{})
		case config.TraceTypeOtel:
			config.CheckAndApplyDefaultOtelConfig(&traceCfg.OtelConfig)
			doOnceOtelTrace(ctx, traceCfg)
			ct.traceList = append(ct.traceList, &OtelRequestTracer{})
			ct.otelIndex = index
		case config.TraceTypeNone:
			ct.traceList = append(ct.traceList, &noTracer{})
		default:
			logger.Log().Warnf("unrecognized trace type [%v]", traceType)
		}
	}
	if len(ct.traceList) == 0 {
		logger.Log().Errorf("unrecognized trace type, ignore and use no trace log")
		ct.traceList = append(ct.traceList, &noTracer{})
	}
	for _, childTrace := range ct.traceList {
		childTrace.Init(ctx, traceCfg, reqId, header)
	}
}

func (ct *ComposeTracer) Event(spanName string, eventName string, eventMsg string) {
	for _, childTrace := range ct.traceList {
		childTrace.Event(spanName, eventName, eventMsg)
	}
}

func (ct *ComposeTracer) StartSpan(ctx context.Context, event string, extraInfo string, tracePermission int) context.Context {
	childCtx := ctx
	for _, childTrace := range ct.traceList {
		if childTrace.GetTracePermission()&tracePermission != 0 {
			childCtx = childTrace.StartSpan(ctx, event, extraInfo, tracePermission)
		} else {
			continue
		}

	}
	return childCtx
}

func (ct *ComposeTracer) EndSpan(spanName string, extraInfo string, tracePermission int) {
	for _, childTrace := range ct.traceList {
		if childTrace.GetTracePermission()&tracePermission != 0 {
			childTrace.EndSpan(spanName, extraInfo, tracePermission)
		} else {
			continue
		}
	}
}

func (ct *ComposeTracer) ErrorSpan(spanName string, err error, tracePermission int) {
	for _, childTrace := range ct.traceList {
		if childTrace.GetTracePermission()&tracePermission != 0 {
			childTrace.ErrorSpan(spanName, err, tracePermission)
		} else {
			continue
		}
	}
}

func (ct *ComposeTracer) TraceTokenLog(spanName string, usage *openai.Usage, ttft float64, tpot float64, latency float64) {
	for _, childTrace := range ct.traceList {
		childTrace.TraceTokenLog(spanName, usage, ttft, tpot, latency)
	}
}

func (ct *ComposeTracer) GetTracePermission() int {
	return ct.tracePermission
}
