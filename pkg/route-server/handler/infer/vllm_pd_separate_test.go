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

package infer

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"infer-dp-scheduler/pkg/common/errs"
	"infer-dp-scheduler/pkg/common/trace"
	"infer-dp-scheduler/pkg/route-server/handler"
	"infer-dp-scheduler/pkg/route-server/handler/request"

	"github.com/stretchr/testify/assert"
	commoncfg "infer-dp-scheduler/pkg/common/config"

	"infer-dp-scheduler/pkg/route-server/metrics"
)

func TestExtractUsage_NormalCase(t *testing.T) {
	metricsCfg := commoncfg.MetricsCfg{
		Enable:   true,
		Duration: 30,
	}
	metrics.InitMetrics(&metricsCfg)
	// Normal case: complete JSON data
	jsonStr := `{"usage":{"prompt_tokens":10,"completion_tokens":20,"total_tokens":30}}`
	body, _ := handler.ParseResponseBody([]byte(jsonStr))
	assert.Equal(t, int(10), body.Usage.PromptTokens)
	assert.Equal(t, int(20), body.Usage.CompletionTokens)
	assert.Equal(t, int(30), body.Usage.TotalTokens)
}

func TestExtractUsage_NumberStopReasonBody(t *testing.T) {
	jsonStr := `{"choices":[{"finish_reason":"stop","stop_reason":163586}],"usage":{"prompt_tokens":26,"total_tokens":82,"completion_tokens":56,"prompt_tokens_details":null}}`
	_, err := handler.ParseResponseBody([]byte(jsonStr))
	if err != nil {
		t.Error(err)
	}
}

func TestExtractUsage_NilStopReasonBody(t *testing.T) {
	jsonStr := `{"choices":[{"finish_reason":"stop","stop_reason":null}],"usage":{"prompt_tokens":26,"total_tokens":82,"completion_tokens":56,"prompt_tokens_details":null}}`
	_, err := handler.ParseResponseBody([]byte(jsonStr))
	if err != nil {
		t.Error(err)
	}
}

func TestExtractUsage_InvalidJSON(t *testing.T) {
	// Invalid JSON
	jsonStr := `invalid json`
	body, _ := handler.ParseResponseBody([]byte(jsonStr))
	assert.Nil(t, body)
}

func TestExtractUsage_EmptyInput(t *testing.T) {
	// Empty input
	body, _ := handler.ParseResponseBody([]byte{})
	assert.Nil(t, body)
}

func TestExtractChoice_MissingFields(t *testing.T) {
	// Missing fields
	jsonStr := `{"usage":{}}`
	body, _ := handler.ParseResponseBody([]byte(jsonStr))
	assert.Nil(t, body.Choices)
}

func TestExtractUsage_MinValues(t *testing.T) {
	// Minimum values
	jsonStr := `{"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":1}}`
	body, _ := handler.ParseResponseBody([]byte(jsonStr))
	assert.Equal(t, int(1), body.Usage.PromptTokens)
	assert.Equal(t, int(1), body.Usage.CompletionTokens)
	assert.Equal(t, int(1), body.Usage.TotalTokens)
}

func TestExtractUsage_MaxValues(t *testing.T) {
	// Maximum values
	jsonStr := `{"usage":{"prompt_tokens":1000000,"completion_tokens":1000000,"total_tokens":2000000}}`
	body, _ := handler.ParseResponseBody([]byte(jsonStr))
	assert.Equal(t, int(1000000), body.Usage.PromptTokens)
	assert.Equal(t, int(1000000), body.Usage.CompletionTokens)
	assert.Equal(t, int(2000000), body.Usage.TotalTokens)
}

func TestPrepareRequest_emptyBody(t *testing.T) {
	pdSeperate := VllmPDSeparateInfer{
		runEncode:              true,
		schedulerMgr:           nil,
		instanceMgr:            nil,
		cfg:                    nil,
		resHeaderBlackSet:      nil,
		workerEventCollector:   nil,
		runningRequestsManager: nil,
	}
	w := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(w)
	ctx.Request, _ = http.NewRequest(http.MethodGet, "/v1/models", bytes.NewBufferString(""))
	httpReq := ctx.Request
	requestId := request.GetInferRequestId(&httpReq.Header, true)
	traceCfg := &commoncfg.TraceConfig{
		EnableTraceTokenLog: true,
		Type:                []string{"log"},
		OtelConfig:          commoncfg.OtelConfig{},
	}
	tracer := trace.NewTracer(ctx, traceCfg, requestId, httpReq.Header)
	tracer.E2ECtx = tracer.Tracer.StartSpan(ctx.Request.Context(), trace.RequestSpanE2E, "", trace.GtLogPermission)
	_, finishReason, _, _ := pdSeperate.prepareRequest(ctx, true, tracer, nil)
	assert.Equal(t, finishReason, "unknown")
}

func TestPrepareRequest(t *testing.T) {
	serverCfg := commoncfg.ServerConfig{
		Addr:                    "127.0.0.1",
		HttpPort:                "50000",
		Name:                    "rs1",
		RateLimitCfg:            &commoncfg.RateLimitCfg{MaxConcurrentRequests: 1000},
		TraceCfg:                nil,
		ResponseHeaderBlacklist: nil,
	}
	runningRequestsManager := handler.NewRunningRequestsManager(serverCfg)
	pdSeperate := VllmPDSeparateInfer{
		runEncode:              true,
		schedulerMgr:           nil,
		instanceMgr:            nil,
		cfg:                    nil,
		resHeaderBlackSet:      nil,
		workerEventCollector:   nil,
		runningRequestsManager: runningRequestsManager,
	}
	w := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(w)
	reqBody := `{
		"model": "deepseek",
		"messages": [
			{
				"role": "user",
				"content": "你好"
			}
		],
		"max_tokens": 32,
		"stream": true
	}`
	ctx.Request, _ = http.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(reqBody))
	ctx.Request.Header.Set("Content-Type", "application/json")
	httpReq := ctx.Request
	requestId := request.GetInferRequestId(&httpReq.Header, true)
	traceCfg := &commoncfg.TraceConfig{
		EnableTraceTokenLog: true,
		Type:                []string{"log"},
		OtelConfig:          commoncfg.OtelConfig{},
	}
	tracer := trace.NewTracer(ctx, traceCfg, requestId, httpReq.Header)
	tracer.E2ECtx = tracer.Tracer.StartSpan(ctx.Request.Context(), trace.RequestSpanE2E, "", trace.GtLogPermission)
	err, finishReason, _, _ := pdSeperate.prepareRequest(ctx, true, tracer, nil)
	var rsErr *errs.RSError
	assert.Equal(t, finishReason, "")
	assert.Equal(t, rsErr, err)
}

func TestIsTestSuccess_ErrorAfterTarget(t *testing.T) {
	stageChain := []string{"stage1", "stage2", "stage3"}
	testValid, testSuccess := handler.IsTargetPhasePassed("stage2", "stage3", stageChain)
	assert.True(t, testValid)
	assert.Equal(t, true, testSuccess)
}

func TestIsTestSuccess_ErrorAtTarget(t *testing.T) {
	stageChain := []string{"stage1", "stage2", "stage3"}
	testValid, testSuccess := handler.IsTargetPhasePassed("stage2", "stage2", stageChain)
	assert.True(t, testValid)
	assert.Equal(t, false, testSuccess)
}

func TestIsTestSuccess_ErrorBeforeTarget(t *testing.T) {
	stageChain := []string{"stage1", "stage2", "stage3"}
	testValid, testSuccess := handler.IsTargetPhasePassed("stage3", "stage2", stageChain)
	assert.False(t, testValid)
	assert.Equal(t, false, testSuccess)
}

func TestIsTestSuccess_TargetStageNotFound(t *testing.T) {
	stageChain := []string{"stage1", "stage2", "stage3"}
	testValid, testSuccess := handler.IsTargetPhasePassed("invalid", "stage2", stageChain)
	assert.False(t, testValid)
	assert.Equal(t, false, testSuccess)
}

func TestIsTestSuccess_ErrorStageNotFound(t *testing.T) {
	stageChain := []string{"stage1", "stage2", "stage3"}
	testValid, testSuccess := handler.IsTargetPhasePassed("stage2", "invalid", stageChain)
	assert.False(t, testValid)
	assert.Equal(t, false, testSuccess)
}
