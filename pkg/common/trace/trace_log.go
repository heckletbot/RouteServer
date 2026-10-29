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
	"time"

	"infer-dp-scheduler/pkg/common/config"
	"infer-dp-scheduler/pkg/common/logger"
	"infer-dp-scheduler/pkg/common/openai"
)

type LogRequestTracer struct {
	reqId               string
	spanId              string
	userAlias           string
	tracePermission     int
	enableTraceTokenLog bool

	spanStartTimes map[string]time.Time
}

func (lrt *LogRequestTracer) Init(ctx context.Context, traceCfg *config.TraceConfig, reqId string, header http.Header) {
	lrt.reqId = reqId
	lrt.spanId = GetSpanId(header)
	lrt.userAlias = GetUserAlias(header)
	lrt.enableTraceTokenLog = traceCfg.EnableTraceTokenLog
	lrt.spanStartTimes = make(map[string]time.Time)
	lrt.tracePermission = EnableLogTrace
}

func (lrt *LogRequestTracer) Event(spanName string, eventName string, eventMsg string) {
	logger.Log().InfoSkipf(2, "Trace Event [%s] request_id: %s, x-span-id: %s, x-user-alias: %s, event details: %s",
		eventName, lrt.reqId, lrt.spanId, lrt.userAlias, eventMsg)
}

func (lrt *LogRequestTracer) StartSpan(ctx context.Context, spanName string, extraInfo string, tracePermission int) context.Context {
	lrt.logSpan(spanName, spanStart, extraInfo)
	if _, exists := lrt.spanStartTimes[spanName]; exists {
		return nil
	}
	start := time.Now()
	lrt.spanStartTimes[spanName] = start
	return ctx
}

func (lrt *LogRequestTracer) EndSpan(spanName string, extraInfo string, tracePermission int) {
	var duration time.Duration
	if startTime, exists := lrt.spanStartTimes[spanName]; exists {
		duration = time.Since(startTime)
	}
	durationStr := "start time not found"
	if duration != 0 {
		durationStr = duration.String()
	}
	lrt.logSpan(spanName, spanEnd, fmt.Sprintf("msg: [%s], duration time: [%s]", extraInfo, durationStr))
	delete(lrt.spanStartTimes, spanName)
}

func (lrt *LogRequestTracer) ErrorSpan(spanName string, err error, tracePermission int) {
	lrt.logSpan(spanName, spanError, err.Error())
	delete(lrt.spanStartTimes, spanName)
}

func (lrt *LogRequestTracer) TraceTokenLog(spanName string, usage *openai.Usage, ttft float64, tpot float64, latency float64) {
	if lrt.enableTraceTokenLog {
		TraceTokenLog(lrt.spanId, lrt.userAlias, lrt.reqId, usage, ttft, tpot, latency)
	}
}

func (lrt *LogRequestTracer) logSpan(event string, eventStage string, extraInfo string) {
	logger.Log().InfoSkipf(3, "Trace Span [%s] stage [%s] request_id: %s, x-span-id: %s, x-user-alias: %s, %s",
		event, eventStage, lrt.reqId, lrt.spanId, lrt.userAlias, extraInfo)
}

func (lrt *LogRequestTracer) GetTracePermission() int {
	return lrt.tracePermission
}
