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
	"net/http"

	"infer-dp-scheduler/pkg/common/config"
	"infer-dp-scheduler/pkg/common/openai"
)

type noTracer struct {
	reqId               string
	spanId              string
	userAlias           string
	enableTraceTokenLog bool
	tracePermission     int
}

func (n *noTracer) Init(ctx context.Context, traceCfg *config.TraceConfig, reqId string, header http.Header) {
	n.reqId = reqId
	n.spanId = GetSpanId(header)
	n.userAlias = GetUserAlias(header)
	n.enableTraceTokenLog = traceCfg.EnableTraceTokenLog
	n.tracePermission = EnableNoTrace
}

func (n *noTracer) Event(spanName string, eventName string, eventMsg string) {
}

func (n *noTracer) StartSpan(ctx context.Context, event string, extraInfo string, tracePermission int) context.Context {
	return ctx
}

func (n *noTracer) EndSpan(spanName string, extraInfo string, tracePermission int) {
}

func (n *noTracer) ErrorSpan(spanName string, err error, tracePermission int) {
}

func (n *noTracer) TraceTokenLog(spanName string, usage *openai.Usage, ttft float64, tpot float64, latency float64) {
	if n.enableTraceTokenLog {
		TraceTokenLog(n.spanId, n.userAlias, n.reqId, usage, ttft, tpot, latency)
	}
}

func (n *noTracer) GetTracePermission() int {
	return n.tracePermission
}
