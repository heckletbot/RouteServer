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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/agiledragon/gomonkey/v2"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	instance_client "infer-dp-scheduler/pkg/common/client/instance-client"
	"infer-dp-scheduler/pkg/common/config"
	"infer-dp-scheduler/pkg/common/constant"
	"infer-dp-scheduler/pkg/common/errs"
	"infer-dp-scheduler/pkg/common/health"
	common_infer "infer-dp-scheduler/pkg/common/infer"
	"infer-dp-scheduler/pkg/common/openai"
	"infer-dp-scheduler/pkg/common/utils"
	"infer-dp-scheduler/pkg/route-server/handler"
	"infer-dp-scheduler/pkg/route-server/instances"
	"infer-dp-scheduler/pkg/route-server/metrics"
	"infer-dp-scheduler/pkg/route-server/scheduler"
)

var serverCfg = &config.ServerConfig{
	Addr:         "127.0.0.1",
	HttpPort:     "50000",
	Name:         "rs1",
	RateLimitCfg: &config.RateLimitCfg{MaxConcurrentRequests: 1000},
	TraceCfg: &config.TraceConfig{
		EnableTraceTokenLog: false,
		Type:                []string{"log"},
	},
	ResponseHeaderBlacklist: nil,
}

// TestEncodePhase_PipelineExecution tests ExecutePhase execution through Pipeline
func TestEncodePhase_PipelineExecution(t *testing.T) {
	phaseCfg := &common_infer.PhaseConfig{
		Name:      "encode_phase",
		SpanName:  "encode_stage",
		StageName: "Encode",
		Config: map[string]interface{}{
			"RequestTimeout": "5000", // timeout in milliseconds
		},
		ExecuteParams: common_infer.ExecuteConfig{
			InstanceType:           constant.Encode,
			PreProcessRequestFuncs: []string{EncodePrepareRequest},
			PostProcessFuncs:       []string{EncodeProcessResponse},
			PostPhaseFuncs:         []string{RegularPostPhase},
		},
	}

	// Use gomonkey to mock NewPipeline function
	patches := gomonkey.ApplyFunc(NewPipeline, func(inferType string) (*Pipeline, error) {
		encodePhase := newExecutePhase()
		err := encodePhase.Init(phaseCfg)
		if err != nil {
			return nil, err
		}
		return NewPipelineWithPhases([]Phase{encodePhase})
	})

	instanceId := "encode-instance-001"

	schedulerManager := &mockSchedulerManager{
		instanceId: instanceId,
	}

	defer patches.Reset()

	tests := []struct {
		name                       string
		requestBody                map[string]interface{}
		encodeResponse             map[string]interface{}
		expectedPromptTokens       int32
		expectedSubCtxPromptTokens int32
		expectedEcTransferParams   map[string]interface{}
		expectSkip                 bool
		description                string
	}{
		{
			name: "Request with image successfully executes Encode phase",
			requestBody: map[string]interface{}{
				"model": "test-model",
				"messages": []map[string]interface{}{
					{
						"role": "user",
						"content": []map[string]interface{}{
							{
								"type": "image_url",
								"image_url": map[string]interface{}{
									"url": "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==",
								},
							},
							{
								"type": "text",
								"text": "What's in this image?",
							},
						},
					},
				},
				"max_tokens": 100,
			},
			encodeResponse: map[string]interface{}{
				"usage": map[string]interface{}{
					"prompt_tokens":     20,
					"completion_tokens": 30,
					"total_tokens":      50,
				},
				"ec_transfer_params": map[string]interface{}{
					"param1": "value1",
					"param2": "value2",
				},
			},
			expectedPromptTokens:       20,
			expectedSubCtxPromptTokens: 1,
			expectedEcTransferParams:   map[string]interface{}{"param1": "value1", "param2": "value2"},
			expectSkip:                 false,
			description:                "Verify Encode phase correctly handles image requests",
		},
		{
			name: "Text-only request skips Encode phase",
			requestBody: map[string]interface{}{
				"model":      "test-model",
				"prompt":     "Hello, how are you? This is a text-only request.",
				"max_tokens": 100,
			},
			encodeResponse:             map[string]interface{}{},
			expectedPromptTokens:       0, // prompt not set when skipped
			expectedSubCtxPromptTokens: 0,
			expectedEcTransferParams:   nil,
			expectSkip:                 true,
			description:                "Text-only request should skip Encode phase",
		},
	}

	metrics.InitMetrics(nil)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockClient := newMockInstanceClient("http://localhost:8080", toJSONBytes(t, tt.encodeResponse))

			instanceManager := &mockInstanceManager{
				client: mockClient,
			}

			workerEventCollector := initMockEventCollector()
			runningRequestManager := initMockRunningRequestManager()

			pipelineInfer, err := NewPipelineInfer(instanceManager, schedulerManager, *serverCfg, workerEventCollector, runningRequestManager)
			if err != nil {
				t.Error(err)
				return
			}
			w := httptest.NewRecorder()
			ginCtx, _ := gin.CreateTestContext(w)
			// Create test context
			ctx := createTestPipelineContext(t, tt.requestBody, ginCtx, pipelineInfer)

			ctx.RequestContext.InferMsg = health.NewInferMsg(true, "", "", "")
			// mock schedule result
			if tt.expectSkip {
				ctx.RequestContext.InferMsg.SkipInferRole(constant.StageEncode, "test skipping")
			}
			ctx.RequestContext.InferMsg.SetInstance(constant.Encode, instanceId)

			// Execute directly through pipelineInfer's pipeline field
			if pipelineInfer.pipeline != nil {
				pipelineInfer.pipeline.ExecutePipeline(ctx)
			} else {
				t.Fatal("pipelineInfer.pipeline is nil")
			}

			// Verify results
			if tt.expectSkip {
				// Verify should skip
				_, skip := ctx.RequestContext.InferMsg.IsSkipped(constant.StageEncode)
				assert.True(t, skip, "Should skip Encode phase")
			} else {
				// Verify should not skip
				_, skip := ctx.RequestContext.InferMsg.IsSkipped(constant.StageEncode)
				assert.False(t, skip, "Should not skip Encode phase")

				// Verify mock client was called
				assert.Len(t, mockClient.recordedCalls, 1, "Should call instance client once")

				// Verify request body was correctly modified
				if len(mockClient.recordedCalls) > 0 {
					var requestBody map[string]interface{}
					err = json.Unmarshal(mockClient.recordedCalls[0].body, &requestBody)
					assert.NoError(t, err)

					maxTokens, exists := requestBody["max_tokens"]
					assert.True(t, exists, "Should contain max_tokens")
					assert.Equal(t, float64(1), maxTokens, "max_tokens should be set to 1")

					stream, exists := requestBody["stream"]
					assert.True(t, exists, "Should contain stream")
					assert.Equal(t, false, stream, "stream should be set to false")
				}
			}

			// Verify PromptTokens was correctly set
			assert.Equal(t, tt.expectedPromptTokens, ctx.GetRequestCtx().UsagePromptLen.Load(),
				fmt.Sprintf("PromptTokens should equal %d", tt.expectedPromptTokens))

			// If transfer params are expected, verify them
			if tt.expectedEcTransferParams != nil {
				assert.Equal(t, tt.expectedEcTransferParams, ctx.GetRequestCtx().ParsedRequest.EcTransferParams,
					"EcTransferParams should be correctly set")
			}
		})
	}
}

func TestPrefillPhase_PipelineExecution(t *testing.T) {
	phaseCfg := &common_infer.PhaseConfig{
		Name:      "prefill_phase",
		SpanName:  "prefill_stage",
		StageName: "Prefill",
		Config: map[string]interface{}{
			"RequestTimeout": "5000", // timeout in milliseconds
		},
		ExecuteParams: common_infer.ExecuteConfig{
			InstanceType:           constant.Prefill,
			PreProcessRequestFuncs: []string{PrefillPrepareRequest},
			PostProcessFuncs:       []string{PrefillProcessResponse},
			PostPhaseFuncs:         []string{RegularPostPhase},
		},
	}

	// Use gomonkey to mock NewPipeline function
	patches := gomonkey.ApplyFunc(NewPipeline, func(inferType string) (*Pipeline, error) {
		prefillPhase := newExecutePhase()
		err := prefillPhase.Init(phaseCfg)
		if err != nil {
			return nil, err
		}
		return NewPipelineWithPhases([]Phase{prefillPhase})
	})

	instanceId := "prefill-instance-001"

	schedulerManager := &mockSchedulerManager{
		instanceId: instanceId,
	}

	defer patches.Reset()

	tests := []struct {
		name                       string
		requestBody                map[string]interface{}
		prefillResponse            map[string]interface{}
		expectedPromptTokens       int32
		expectedKvTransferParams   map[string]interface{}
		expectedSubCtxPromptTokens int32
		description                string
	}{
		{
			name: "Request executes Prefill phase",
			requestBody: map[string]interface{}{
				"model":      "test-model",
				"prompt":     "Hello, how are you? This is a text-only request.",
				"max_tokens": 100,
			},
			prefillResponse: map[string]interface{}{
				"usage": map[string]interface{}{
					"prompt_tokens":     100,
					"completion_tokens": 30,
					"total_tokens":      50,
				},
				"kv_transfer_params": map[string]interface{}{
					"param1": "value1",
					"param2": "value2",
				},
			},
			expectedPromptTokens:       100,
			expectedSubCtxPromptTokens: int32(len("Hello, how are you? This is a text-only request.")),
			expectedKvTransferParams:   map[string]interface{}{"param1": "value1", "param2": "value2"},
			description:                "Verify Prefill phase handles correctly",
		},
	}

	metrics.InitMetrics(nil)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockClient := newMockInstanceClient("http://localhost:8080", toJSONBytes(t, tt.prefillResponse))

			instanceManager := &mockInstanceManager{
				client: mockClient,
			}

			workerEventCollector := initMockEventCollector()
			runningRequestManager := initMockRunningRequestManager()

			pipelineInfer, err := NewPipelineInfer(instanceManager, schedulerManager, *serverCfg, workerEventCollector, runningRequestManager)
			if err != nil {
				t.Error(err)
				return
			}
			w := httptest.NewRecorder()
			ginCtx, _ := gin.CreateTestContext(w)
			// Create test context
			ctx := createTestPipelineContext(t, tt.requestBody, ginCtx, pipelineInfer)

			ctx.RequestContext.InferMsg = health.NewInferMsg(true, "", "", "")
			ctx.RequestContext.InferMsg.SetInstance(constant.Prefill, instanceId)

			// Execute directly through pipelineInfer's pipeline field
			if pipelineInfer.pipeline != nil {
				pipelineInfer.pipeline.ExecutePipeline(ctx)
			} else {
				t.Fatal("pipelineInfer.pipeline is nil")
			}

			// Verify should not skip
			_, skip := ctx.RequestContext.InferMsg.IsSkipped(constant.StagePrefill)
			assert.False(t, skip, "Should not skip Prefill phase")

			// Verify mock client was called
			assert.Len(t, mockClient.recordedCalls, 1, "Should call instance client once")

			// Verify request body was correctly modified
			if len(mockClient.recordedCalls) > 0 {
				var requestBody map[string]interface{}
				err = json.Unmarshal(mockClient.recordedCalls[0].body, &requestBody)
				assert.NoError(t, err)

				maxTokens, exists := requestBody["max_tokens"]
				assert.True(t, exists, "Should contain max_tokens")
				assert.Equal(t, float64(1), maxTokens, "max_tokens should be set to 1")

				stream, exists := requestBody["stream"]
				assert.True(t, exists, "Should contain stream")
				assert.Equal(t, false, stream, "stream should be set to false")
			}

			// Verify PromptTokens was correctly set
			assert.Equal(t, tt.expectedPromptTokens, ctx.GetRequestCtx().UsagePromptLen.Load(),
				fmt.Sprintf("PromptTokens should equal %d", tt.expectedPromptTokens))
		})
	}
}

func TestDecodePhase_PipelineExecution(t *testing.T) {
	phaseCfg := &common_infer.PhaseConfig{
		Name:      "decode_phase",
		SpanName:  "decode_stage",
		StageName: "Decode",
		Config: map[string]interface{}{
			"RequestTimeout": "5000", // timeout in milliseconds
		},
		ExecuteParams: common_infer.ExecuteConfig{
			InstanceType:           constant.Decode,
			PreProcessRequestFuncs: []string{DecodePrepareRequest},
			PostProcessFuncs:       []string{DecodeProcessResponse},
			PostPhaseFuncs:         []string{CompletePostPhase},
		},
	}

	// Use gomonkey to mock NewPipeline function
	patches := gomonkey.ApplyFunc(NewPipeline, func(inferType string) (*Pipeline, error) {
		decodePhase := newExecutePhase()
		err := decodePhase.Init(phaseCfg)
		if err != nil {
			return nil, err
		}
		return NewPipelineWithPhases([]Phase{decodePhase})
	})

	instanceId := "decode-instance-001"

	schedulerManager := &mockSchedulerManager{
		instanceId: instanceId,
	}

	defer patches.Reset()

	tests := []struct {
		name                       string
		requestBody                map[string]interface{}
		decodeResponse             map[string]interface{}
		expectedPromptTokens       int32
		expectedSubCtxPromptTokens int32
		expectedDcTransferParams   map[string]interface{}
		description                string
	}{
		{
			name: "Request executes Decode phase",
			requestBody: map[string]interface{}{
				"model":      "test-model",
				"prompt":     "Hello, how are you? This is a text-only request.",
				"max_tokens": 100,
				"stream":     true,
			},
			decodeResponse: map[string]interface{}{
				"usage": map[string]interface{}{
					"prompt_tokens":     100,
					"completion_tokens": 30,
					"total_tokens":      130,
				},
				"dc_transfer_params": map[string]interface{}{
					"param1": "value1",
					"param2": "value2",
				},
			},
			expectedPromptTokens:       100,
			expectedSubCtxPromptTokens: int32(len("Hello, how are you? This is a text-only request.")),
			expectedDcTransferParams:   map[string]interface{}{"param1": "value1", "param2": "value2"},
			description:                "Verify Decode phase handles correctly",
		},
	}

	metrics.InitMetrics(nil)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockClient := newMockInstanceClient("http://localhost:8080", toJSONBytes(t, tt.decodeResponse))

			instanceManager := &mockInstanceManager{
				client: mockClient,
			}

			workerEventCollector := initMockEventCollector()
			runningRequestManager := initMockRunningRequestManager()

			pipelineInfer, err := NewPipelineInfer(instanceManager, schedulerManager, *serverCfg, workerEventCollector, runningRequestManager)
			if err != nil {
				t.Error(err)
				return
			}
			w := httptest.NewRecorder()
			ginCtx, _ := gin.CreateTestContext(w)
			// Create test context
			ctx := createTestPipelineContext(t, tt.requestBody, ginCtx, pipelineInfer)

			ctx.RequestContext.InferMsg = health.NewInferMsg(true, "", "", "")
			ctx.RequestContext.InferMsg.SetInstance(constant.Decode, instanceId)

			// Execute directly through pipelineInfer's pipeline field
			if pipelineInfer.pipeline != nil {
				pipelineInfer.pipeline.ExecutePipeline(ctx)
			} else {
				t.Fatal("pipelineInfer.pipeline is nil")
			}

			// Verify results

			// Verify mock client was called
			assert.Len(t, mockClient.recordedCalls, 1, "Should call instance client once")

			// Verify request body was correctly modified
			if len(mockClient.recordedCalls) > 0 {
				var requestBody map[string]interface{}
				err = json.Unmarshal(mockClient.recordedCalls[0].body, &requestBody)
				assert.NoError(t, err)

				stream, exists := requestBody["stream"]
				assert.True(t, exists, "Should contain stream")
				assert.Equal(t, true, stream, "stream should be set to true")
			}

		})
	}
}

// mock implementation same as before, redefined for completeness
type mockInstanceClient struct {
	endpoint      string
	responseBody  []byte
	returnError   bool
	errorStatus   int
	recordedCalls []mockCall
}

func (m *mockInstanceClient) ProxyHttp(req *http.Request) *http.Response {
	return nil
}

func (m *mockInstanceClient) ModelName() (string, error) {
	return "test-model", nil
}

type mockCall struct {
	path     string
	body     []byte
	instance string
	stage    string
	headers  http.Header
}

func newMockInstanceClient(endpoint string, responseBody []byte) *mockInstanceClient {
	return &mockInstanceClient{
		endpoint:      endpoint,
		responseBody:  responseBody,
		returnError:   false,
		errorStatus:   500,
		recordedCalls: make([]mockCall, 0),
	}
}

func (m *mockInstanceClient) HealthCheck() (bool, int, error) {
	return true, 200, nil
}

func (m *mockInstanceClient) Generate(ctx context.Context, header http.Header, path string, body io.Reader, instanceId string, stage string, reqId string, isMessages bool) (*http.Response, *errs.RSError, *utils.InstanceFailureDetails) {
	bodyBytes, _ := io.ReadAll(body)
	call := mockCall{
		path:     path,
		body:     bodyBytes,
		instance: instanceId,
		stage:    stage,
		headers:  header,
	}
	m.recordedCalls = append(m.recordedCalls, call)

	response := &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(bytes.NewReader(m.responseBody)),
		Header:     make(http.Header),
	}

	return response, nil, nil
}

func (m *mockInstanceClient) Metrics() ([]byte, error) {
	return []byte("{}"), nil
}

func (m *mockInstanceClient) HandlerBroadCastRequest(ctx *gin.Context, broadcastRequest *openai.BroadcastRequest, id string) openai.InstanceResponse {
	return openai.InstanceResponse{}
}

func (m *mockInstanceClient) GetEndpoint() string {
	return m.endpoint
}

type mockInstanceManager struct {
	instances.Manager
	client *mockInstanceClient
}

func (m *mockInstanceManager) GetInstanceClient(instanceId string) (instance_client.InstanceClient, error) {
	return m.client, nil
}

type mockSchedulerManager struct {
	scheduler.Manager
	instanceId string
}

func initMockEventCollector() *health.WorkerEventCollector {
	workerEventCollector := &health.WorkerEventCollector{}
	cfg := &config.Config{
		InstanceManagerCfg: config.InstanceManagerCfg{
			HealthCheckConfig: &config.HealthCheckConfig{
				EventThresholdIntervalMs: 1000,
				AvailableCheckersCfg: config.AvailableCheckersConfig{
					HangCheckerCfg: config.HangCheckerConfig{
						Enable: false,
					},
				},
			},
		},
	}
	workerEventCollector.Init(cfg)
	return workerEventCollector
}

func initMockRunningRequestManager() *handler.RunningRequestsManager {

	return handler.NewRunningRequestsManager(*serverCfg)
}

// Helper function to create test pipeline context for encode phase
func createTestPipelineContext(t *testing.T, requestBody map[string]interface{}, ginCtx *gin.Context, pipelineInfer *PipelineInfer) *PipelineContext {
	jsonBytes := toJSONBytes(t, requestBody)

	httpReq, err := http.NewRequest("POST", "/v1/completions", bytes.NewReader(jsonBytes))
	assert.NoError(t, err)
	httpReq.Header.Set("Content-Type", "application/json")
	ginCtx.Request = httpReq

	assert.NoError(t, err)
	ctx := NewPipelineContext(ginCtx, pipelineInfer)

	return ctx
}

func toJSONBytes(t *testing.T, data interface{}) []byte {
	bytes, err := json.Marshal(data)
	assert.NoError(t, err)
	return bytes
}

// NewPipelineWithPhases creates a simple pipeline containing only specified phases
func NewPipelineWithPhases(phases []Phase) (*Pipeline, error) {
	pipeline := &Pipeline{
		phases:       phases,
		preparePhase: NewPreparePhase(),      // Add prepare phase
		errorPhase:   newInferFailurePhase(), // Add error phase
		successPhase: newInferSuccessPhase(), // Add success phase
		isMain:       true,
	}

	// Initialize topology structure
	pipeline.nextPhases = make([][]int, len(phases))
	pipeline.inDegree = make([]int, len(phases))

	// Build stage chain - including prepare phase
	stageChain := make([]string, len(phases)+1)
	stageChain[0] = "prepare"
	for i, phase := range phases {
		stageChain[i+1] = phase.StageName()
	}
	pipeline.stageNameChain = stageChain

	return pipeline, nil
}

func generateTestRequestId() string {
	return fmt.Sprintf("test-req-%d", time.Now().UnixNano())
}
