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

package request

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"route-server/pkg/common/utils"
	"route-server/pkg/route-server/metrics"

	"route-server/pkg/common/constant"
	"route-server/pkg/common/openai"
)

func TestNewImpl1(t *testing.T) {
	metrics.InitMetrics(nil)
	request := openai.CompletionRequest{
		Model:     "deepseek-r1",
		Prompt:    "user_prompt",
		MaxTokens: 100,
	}
	bytes, _ := utils.SonicApi.Marshal(request)

	httpRequest := httptest.NewRequest(http.MethodPost, "/list", strings.NewReader(string(bytes)))
	_, err := NewGenerateRequestImpl(httpRequest, constant.Prefill, false)
	if err != nil {
		t.Errorf("new request failed: %v", err)
	}
}

func TestNewImpl2(t *testing.T) {
	request := openai.ChatCompletionRequest{
		Model: "deepseek-r1",
		Messages: []openai.ChatCompletionMessage{
			{
				Content: "user_prompt",
			},
		},
		MaxTokens: 100,
	}
	bytes, _ := utils.SonicApi.Marshal(request)

	httpRequest := httptest.NewRequest(http.MethodPost, "/list", strings.NewReader(string(bytes)))
	_, err := NewGenerateRequestImpl(httpRequest, constant.Prefill, true)
	if err != nil {
		t.Errorf("new request failed: %v", err)
	}
}

func TestNewErr1(t *testing.T) {
	request := openai.ChatCompletionRequest{
		Model: "deepseek-r1",
		Messages: []openai.ChatCompletionMessage{
			{
				Content: "user_prompt",
			},
		},
		MaxTokens: 100,
	}
	bytes, _ := utils.SonicApi.Marshal(request)

	httpRequest := httptest.NewRequest(http.MethodPost, "/list", strings.NewReader(string(bytes)))
	defer func() {
		if err := recover(); err != nil {
			assert.NotNil(t, err)
		}
	}()
	_, err := NewGenerateRequestImpl(httpRequest, constant.Prefill, false)
	if err != nil {
		return
	}
}

func TestNewErr2(t *testing.T) {
	request := openai.CompletionRequest{
		Model:     "deepseek-r1",
		Prompt:    "user_prompt",
		MaxTokens: 100,
	}
	bytes, _ := utils.SonicApi.Marshal(request)

	httpRequest := httptest.NewRequest(http.MethodPost, "/list", strings.NewReader(string(bytes)))
	_, err := NewGenerateRequestImpl(httpRequest, constant.Prefill, true)
	if err != nil {
		t.Errorf("new request failed: %v", err)
	}
}

func TestGetRequestId(t *testing.T) {
	request := openai.CompletionRequest{
		Model:     "deepseek-r1",
		Prompt:    "user_prompt",
		MaxTokens: 100,
	}
	bytes, _ := utils.SonicApi.Marshal(request)

	httpRequest := httptest.NewRequest(http.MethodPost, "/list", strings.NewReader(string(bytes)))
	httpRequest.Header.Add(constant.XRequestId, "123456")
	impl, err := NewGenerateRequestImpl(httpRequest, constant.Prefill, false)
	if err != nil {
		t.Errorf("new request failed: %v", err)
	}
	assert.Equal(t, "cmpl-123456", impl.GetRequestId())
}

func TestGetHeader(t *testing.T) {
	request := openai.CompletionRequest{
		Model:     "deepseek-r1",
		Prompt:    "user_prompt",
		MaxTokens: 100,
	}
	bytes, _ := utils.SonicApi.Marshal(request)

	httpRequest := httptest.NewRequest(http.MethodPost, "/list", strings.NewReader(string(bytes)))
	httpRequest.Header.Add(constant.XRequestId, "123456")
	impl, err := NewGenerateRequestImpl(httpRequest, constant.Prefill, false)
	if err != nil {
		t.Errorf("new request failed: %v", err)
	}
	if impl.GetHeader().Get(constant.XRequestId) != "123456" {
		t.Errorf("get id error")
	}
}

func TestGetType(t *testing.T) {
	request := openai.CompletionRequest{
		Model:     "deepseek-r1",
		Prompt:    "user_prompt",
		MaxTokens: 100,
	}
	bytes, _ := utils.SonicApi.Marshal(request)

	httpRequest := httptest.NewRequest(http.MethodPost, "/list", strings.NewReader(string(bytes)))
	httpRequest.Header.Add(constant.XRequestId, "123456")
	impl, err := NewGenerateRequestImpl(httpRequest, constant.Prefill, false)
	if err != nil {
		t.Errorf("new request failed: %v", err)
	}
	if impl.GetType() != constant.Prefill {
		t.Errorf("GetType error")
	}
}

func TestGetChat(t *testing.T) {
	request := openai.CompletionRequest{
		Model:     "deepseek-r1",
		Prompt:    "user_prompt",
		MaxTokens: 100,
	}
	bytes, _ := utils.SonicApi.Marshal(request)

	httpRequest := httptest.NewRequest(http.MethodPost, "/list", strings.NewReader(string(bytes)))
	httpRequest.Header.Add(constant.XRequestId, "123456")
	impl, err := NewGenerateRequestImpl(httpRequest, constant.Prefill, false)
	if err != nil {
		t.Errorf("new request failed: %v", err)
	}
	if impl.GetChat()[0].Content != "user_prompt" {
		t.Errorf("GetChat error")
	}
}

func TestGetMaxToken(t *testing.T) {
	request := openai.CompletionRequest{
		Model:     "deepseek-r1",
		Prompt:    "user_prompt",
		MaxTokens: 100,
	}
	bytes, _ := utils.SonicApi.Marshal(request)

	httpRequest := httptest.NewRequest(http.MethodPost, "/list", strings.NewReader(string(bytes)))
	httpRequest.Header.Add(constant.XRequestId, "123456")
	impl, err := NewGenerateRequestImpl(httpRequest, constant.Prefill, false)
	if err != nil {
		t.Errorf("new request failed: %v", err)
	}
	assert.Equal(t, int32(100), impl.GetMaxToken())
}

func TestRewrite(t *testing.T) {
	request := openai.CompletionRequest{
		Model:     "deepseek-r1",
		Prompt:    "user_prompt",
		MaxTokens: 100,
		Stream:    true,
		StreamOptions: &openai.StreamOptions{
			IncludeUsage:         true,
			ContinuousUsageStats: false,
		},
	}
	bytes, _ := utils.SonicApi.Marshal(request)
	testRequestRewrite(t, false, bytes, nil,
		"{\"max_tokens\":1,\"model\":\"deepseek-r1\",\"prompt\":\"user_prompt\",\"stream\":false}",
		"{\"max_tokens\":100,\"model\":\"deepseek-r1\",\"prompt\":\"user_prompt\",\"stream\":true,\"stream_options\":{\"include_usage\":true}}")

	request = openai.CompletionRequest{
		Model:     "deepseek-r1",
		Prompt:    "user_prompt",
		MaxTokens: 100,
	}
	bytes, _ = utils.SonicApi.Marshal(request)
	testRequestRewrite(t, false, bytes, nil,
		"{\"max_tokens\":1,\"model\":\"deepseek-r1\",\"prompt\":\"user_prompt\",\"stream\":false}", "{\"max_tokens\":100,\"model\":\"deepseek-r1\",\"prompt\":\"user_prompt\"}")
}

func TestTransparentPass(t *testing.T) {
	randJson := "{\"model\":\"deepseek\",\"messages\":[{\"role\":\"user\",\"content\":\"Hello\"}],\"max_tokens\":100,\"top_k\":5,\"top_p\":0.8,\"temperature\":0,\"ignore_eos\":false,\"stream\":true,\"stream_options\":{\"include_usage\":true,\"continuous_usage_stats\":false}}"
	testRequestRewrite(t, true, []byte(randJson), nil,
		"{\"ignore_eos\":false,\"max_tokens\":1,\"messages\":[{\"content\":\"Hello\",\"role\":\"user\"}],\"model\":\"deepseek\",\"stream\":false,\"temperature\":0,\"top_k\":5,\"top_p\":0.8}",
		"{\"ignore_eos\":false,\"max_tokens\":100,\"messages\":[{\"content\":\"Hello\",\"role\":\"user\"}],\"model\":\"deepseek\",\"stream\":true,\"stream_options\":{\"continuous_usage_stats\":false,\"include_usage\":true},\"temperature\":0,\"top_k\":5,\"top_p\":0.8}")

	randJson = "{\"model\":\"deepseek\",\"messages\":[{\"role\":\"user\",\"content\":\"Hello\"}],\"max_completion_tokens\":100,\"top_k\":5,\"top_p\":0.8,\"temperature\":0,\"ignore_eos\":false,\"stream\":true,\"stream_options\":{\"include_usage\":true,\"continuous_usage_stats\":false}}"
	testRequestRewrite(t, true, []byte(randJson), nil,
		"{\"ignore_eos\":false,\"max_completion_tokens\":1,\"max_tokens\":1,\"messages\":[{\"content\":\"Hello\",\"role\":\"user\"}],\"model\":\"deepseek\",\"stream\":false,\"temperature\":0,\"top_k\":5,\"top_p\":0.8}",
		"{\"ignore_eos\":false,\"max_completion_tokens\":100,\"messages\":[{\"content\":\"Hello\",\"role\":\"user\"}],\"model\":\"deepseek\",\"stream\":true,\"stream_options\":{\"continuous_usage_stats\":false,\"include_usage\":true},\"temperature\":0,\"top_k\":5,\"top_p\":0.8}")

	randJson = "{\"model\":\"deepseek\", \"abc\":\"aargs\"}"
	testRequestRewrite(t, true, []byte(randJson), nil,
		"{\"abc\":\"aargs\",\"max_tokens\":1,\"model\":\"deepseek\",\"stream\":false}",
		"{\"abc\":\"aargs\",\"model\":\"deepseek\"}")
}

func TestKvTransParamsPass(t *testing.T) {
	randJson := "{\"model\":\"deepseek\",\"messages\":[{\"role\":\"user\",\"content\":\"Hello\"}],\"max_tokens\":100,\"top_k\":5,\"top_p\":0.8,\"temperature\":0,\"ignore_eos\":false,\"stream\":true,\"stream_options\":{\"include_usage\":true,\"continuous_usage_stats\":false}}"
	testRequestRewrite(t, true, []byte(randJson),
		map[string]any{
			"s1": "v1",
			"s2": "v2",
		},
		"{\"ignore_eos\":false,\"max_tokens\":1,\"messages\":[{\"content\":\"Hello\",\"role\":\"user\"}],\"model\":\"deepseek\",\"stream\":false,\"temperature\":0,\"top_k\":5,\"top_p\":0.8}",
		"{\"ignore_eos\":false,\"kv_transfer_params\":{\"s1\":\"v1\",\"s2\":\"v2\"},\"max_tokens\":100,\"messages\":[{\"content\":\"Hello\",\"role\":\"user\"}],\"model\":\"deepseek\",\"stream\":true,\"stream_options\":{\"continuous_usage_stats\":false,\"include_usage\":true},\"temperature\":0,\"top_k\":5,\"top_p\":0.8}")

	randJson = "{\"model\":\"deepseek\", \"abc\":\"aargs\"}"
	testRequestRewrite(t, true, []byte(randJson), map[string]any{
		"s3": "v3",
		"s4": "v4",
	},
		"{\"abc\":\"aargs\",\"max_tokens\":1,\"model\":\"deepseek\",\"stream\":false}",
		"{\"abc\":\"aargs\",\"kv_transfer_params\":{\"s3\":\"v3\",\"s4\":\"v4\"},\"model\":\"deepseek\"}")
}

func TestChatContentLenParse(t *testing.T) {
	normalChatJson := "{\"model\":\"deepseek\",\"messages\":[{\"role\":\"user\",\"content\":\"Hello\"}],\"max_tokens\":100,\"top_k\":5,\"top_p\":0.8,\"temperature\":0,\"ignore_eos\":false,\"stream\":true,\"stream_options\":{\"include_usage\":true,\"continuous_usage_stats\":false}}"
	expectContent := []utils.ChatContent{
		{
			Role:    "user",
			Content: "Hello",
		},
	}
	testContentParse(t, normalChatJson, true, int32(5), expectContent)
	// test mis send request
	testContentParse(t, normalChatJson, false, int32(0), make([]utils.ChatContent, 0))

	multipleChatJson := "{\"model\":\"deepseek\",\"messages\":[{\"role\":\"user\",\"content\":\"Hello\"},{\"role\":\"assist\",\"content\":\"hello\"}],\"max_tokens\":100,\"top_k\":5,\"top_p\":0.8,\"temperature\":0,\"ignore_eos\":false,\"stream\":true,\"stream_options\":{\"include_usage\":true,\"continuous_usage_stats\":false}}"

	testContentParse(t, multipleChatJson, true, int32(10), []utils.ChatContent{
		{
			Role:    "user",
			Content: "Hello",
		},
		{
			Role:    "assist",
			Content: "hello",
		},
	})

	multipleModeChatJson := "{\"model\":\"deepseek\",\"messages\":[{\"role\":\"user\",\"content\":[{\"type\":\"text\",\"message\":\"Hello\"}]}],\"max_tokens\":100,\"top_k\":5,\"top_p\":0.8,\"temperature\":0,\"ignore_eos\":false,\"stream\":true,\"stream_options\":{\"include_usage\":true,\"continuous_usage_stats\":false}}"
	testContentParse(t, multipleModeChatJson, true, int32(63), []utils.ChatContent{
		{
			Content: "[{\"content\":[{\"message\":\"Hello\",\"type\":\"text\"}],\"role\":\"user\"}]",
		},
	})

	multipleModeChatJson = "{\"model\":\"deepseek\",\"messages\":[{\"role\":\"user\",\"content\":[{\"type\":\"text\",\"message\":\"Hello\"}]},{\"role\":\"assist\",\"content\":[{\"type\":\"text\",\"message\":\"Hello\"}]}],\"max_tokens\":100,\"top_k\":5,\"top_p\":0.8,\"temperature\":0,\"ignore_eos\":false,\"stream\":true,\"stream_options\":{\"include_usage\":true,\"continuous_usage_stats\":false}}"
	testContentParse(t, multipleModeChatJson, true, int32(127), []utils.ChatContent{
		{
			Content: "[{\"content\":[{\"message\":\"Hello\",\"type\":\"text\"}],\"role\":\"user\"},{\"content\":[{\"message\":\"Hello\",\"type\":\"text\"}],\"role\":\"assist\"}]",
		},
	})
}

func TestCompletionLenParse(t *testing.T) {
	normalComp := "{\"model\":\"deepseek\",\"prompt\":\"Hello\",\"max_tokens\":100,\"top_k\":5,\"top_p\":0.8,\"temperature\":0,\"ignore_eos\":false,\"stream\":true,\"stream_options\":{\"include_usage\":true,\"continuous_usage_stats\":false}}"
	testContentParse(t, normalComp, false, int32(5), []utils.ChatContent{
		{
			Content: "Hello",
		},
	})

	testContentParse(t, normalComp, true, int32(0), make([]utils.ChatContent, 0))
	multiContentCmpl := "{\"model\":\"deepseek\",\"prompt\":[\"Hello\", \"hello\", \"123\"],\"max_tokens\":100,\"top_k\":5,\"top_p\":0.8,\"temperature\":0,\"ignore_eos\":false,\"stream\":true,\"stream_options\":{\"include_usage\":true,\"continuous_usage_stats\":false}}"
	testContentParse(t, multiContentCmpl, false, int32(23), []utils.ChatContent{
		{
			Content: "[\"Hello\",\"hello\",\"123\"]",
		},
	})
}

func testContentParse(t *testing.T, normalChatJson string, ischat bool, expectLen int32, expectContent []utils.ChatContent) {
	httpRequest := httptest.NewRequest(http.MethodPost, "/list", strings.NewReader(normalChatJson))
	httpRequest.Header.Add(constant.XRequestId, "123456")
	impl, err := NewGenerateRequestImpl(httpRequest, constant.Prefill, ischat)
	if err != nil {
		t.Errorf("new request failed: %v", err)
	}
	assert.Equal(t, expectLen, impl.promptLength)
	assert.Equal(t, expectContent, impl.chat)
}

func testRequestRewrite(t *testing.T, ischat bool, reqBytes []byte,
	kvTransferParams map[string]any, expectPrefillBody string, expectDecodeBody string) {
	httpRequest := httptest.NewRequest(http.MethodPost, "/list", strings.NewReader(string(reqBytes)))
	httpRequest.Header.Add(constant.XRequestId, "123456")
	impl, err := NewGenerateRequestImpl(httpRequest, constant.Prefill, ischat)
	if err != nil {
		t.Errorf("new request failed: %v", err)
	}
	impl.PreparePrefill()
	body, err := impl.GetPrefillBody()
	if err != nil {
		t.Error(err)
	}
	assertBody(t, body, expectPrefillBody)

	impl.kvTransferParams = kvTransferParams

	impl.PrepareDecode()
	body, err = impl.GetDecodeBody()
	if err != nil {
		t.Error(err)
	}
	assertBody(t, body, expectDecodeBody)
}

func assertBody(t *testing.T, reader io.Reader, expectBody string) {
	all, err := io.ReadAll(reader)
	if err != nil {
		t.Error(err)
	}
	assert.Equal(t, expectBody, string(all))
}

func TestGetBody(t *testing.T) {
	request := openai.CompletionRequest{
		Model:     "deepseek-r1",
		Prompt:    "user_prompt",
		MaxTokens: 100,
	}
	bytes, _ := utils.SonicApi.Marshal(request)

	httpRequest := httptest.NewRequest(http.MethodPost, "/list", strings.NewReader(string(bytes)))
	httpRequest.Header.Add(constant.XRequestId, "123456")
	impl, err := NewGenerateRequestImpl(httpRequest, constant.Prefill, false)
	if err != nil {
		t.Errorf("new request failed: %v", err)
	}
	reader, err := impl.GetPrefillBody()
	if err != nil {
		t.Errorf("get body: %v", err)
	}
	bodyBytes, err := io.ReadAll(reader)
	if err != nil {
		t.Errorf("get body: %v", err)
	}
	completion := &openai.CompletionRequest{}
	if err = utils.SonicApi.Unmarshal(bodyBytes, completion); err != nil {
		t.Errorf("get body: %v", err)
	}
}

func TestFillFirstToken(t *testing.T) {
	t.Run("TestIsStreamFalse", func(t *testing.T) {
		req := &GenerateRequestImpl{
			originIsStream: false,
		}
		err := req.fillFirstToken([]byte(`{}`))
		assert.NoError(t, err)
	})

	t.Run("TestIsChatTrue", func(t *testing.T) {
		req := &GenerateRequestImpl{
			originIsStream: true,
			isChat:         true,
		}
		chatCompletionRes := &openai.ChatCompletionResponse{
			ID:      "123",
			Created: time.Now().Unix(),
			Model:   "model",
			Choices: []openai.ChatCompletionChoice{
				{
					Index: 0,
					Message: openai.ChatCompletionMessage{
						Role:    "role",
						Content: "content",
					},
				},
			},
			Usage: openai.Usage{
				PromptTokens: 1,
			},
		}
		bodyBytes, _ := utils.SonicApi.Marshal(chatCompletionRes)
		err := req.fillFirstToken(bodyBytes)
		assert.NoError(t, err)
	})

	t.Run("TestIsChatFalse", func(t *testing.T) {
		req := &GenerateRequestImpl{
			originIsStream: true,
			isChat:         false,
		}
		completionRes := &openai.CompletionResponse{
			Choices: []openai.CompletionChoice{
				{
					FinishReason: "reason",
				},
			},
		}
		bodyBytes, _ := utils.SonicApi.Marshal(completionRes)
		err := req.fillFirstToken(bodyBytes)
		assert.NoError(t, err)
	})

	t.Run("TestUnmarshalError", func(t *testing.T) {
		req := &GenerateRequestImpl{
			originIsStream: true,
			isChat:         true,
		}
		err := req.fillFirstToken([]byte(`{`))
		assert.Error(t, err)
	})

	t.Run("TestMarshalError", func(t *testing.T) {
		req := &GenerateRequestImpl{
			originIsStream: true,
			isChat:         true,
		}
		chatCompletionRes := &openai.ChatCompletionResponse{
			ID:      "123",
			Created: time.Now().Unix(),
			Model:   "model",
			Choices: []openai.ChatCompletionChoice{
				{
					Index: 0,
					Message: openai.ChatCompletionMessage{
						Role:    "role",
						Content: "content",
					},
				},
			},
			Usage: openai.Usage{
				PromptTokens: 1,
			},
		}
		bodyBytes, _ := utils.SonicApi.Marshal(chatCompletionRes)
		req.firstTokenBody = make([]byte, 0)
		err := req.fillFirstToken(bodyBytes)
		assert.NoError(t, err)
	})
}

func TestParseMaxTokens(t *testing.T) {
	testCases := []struct {
		name        string
		isChat      bool
		body        string
		expectError bool
	}{
		// v1/chat/completions tests
		{"chat max_tokens=-1 max_completion_tokens=-1", true, `{"model":"test","messages":[{"role":"user","content":"hi"}],"max_tokens":-1,"max_completion_tokens":-1}`, true},
		{"chat max_tokens=-1 max_completion_tokens=0", true, `{"model":"test","messages":[{"role":"user","content":"hi"}],"max_tokens":-1,"max_completion_tokens":0}`, true},
		{"chat max_tokens=-1 max_completion_tokens=10", true, `{"model":"test","messages":[{"role":"user","content":"hi"}],"max_tokens":-1,"max_completion_tokens":10}`, false},
		{"chat max_tokens=0 max_completion_tokens=-1", true, `{"model":"test","messages":[{"role":"user","content":"hi"}],"max_tokens":0,"max_completion_tokens":-1}`, true},
		{"chat max_tokens=0 max_completion_tokens=0", true, `{"model":"test","messages":[{"role":"user","content":"hi"}],"max_tokens":0,"max_completion_tokens":0}`, true},
		{"chat max_tokens=0 max_completion_tokens=10", true, `{"model":"test","messages":[{"role":"user","content":"hi"}],"max_tokens":0,"max_completion_tokens":10}`, false},
		{"chat max_tokens=10 max_completion_tokens=-1", true, `{"model":"test","messages":[{"role":"user","content":"hi"}],"max_tokens":10,"max_completion_tokens":-1}`, true},
		{"chat max_tokens=10 max_completion_tokens=0", true, `{"model":"test","messages":[{"role":"user","content":"hi"}],"max_tokens":10,"max_completion_tokens":0}`, true},
		{"chat max_tokens=10 max_completion_tokens=10", true, `{"model":"test","messages":[{"role":"user","content":"hi"}],"max_tokens":10,"max_completion_tokens":10}`, false},
		{"chat max_tokens empty max_completion_tokens empty", true, `{"model":"test","messages":[{"role":"user","content":"hi"}],"max_tokens":"","max_completion_tokens":""}`, true},
		{"chat max_tokens empty max_completion_tokens=10", true, `{"model":"test","messages":[{"role":"user","content":"hi"}],"max_tokens":"","max_completion_tokens":10}`, true},
		{"chat max_tokens=10 max_completion_tokens empty", true, `{"model":"test","messages":[{"role":"user","content":"hi"}],"max_tokens":10,"max_completion_tokens":""}`, true},
		{"chat max_tokens empty max_completion_tokens null", true, `{"model":"test","messages":[{"role":"user","content":"hi"}],"max_tokens":"","max_completion_tokens":null}`, true},

		// v1/completions tests (max_completion_tokens should be ignored)
		{"completion max_tokens=10 max_completion_tokens=-1", false, `{"model":"test","prompt":"hi","max_tokens":10,"max_completion_tokens":-1}`, false},
		{"completion max_tokens=10 max_completion_tokens empty", false, `{"model":"test","prompt":"hi","max_tokens":10,"max_completion_tokens":""}`, false},
		{"completion max_tokens=10 max_completion_tokens={}", false, `{"model":"test","prompt":"hi","max_tokens":10,"max_completion_tokens":{}}`, false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var httpRequest *http.Request
			if tc.isChat {
				httpRequest = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(tc.body))
			} else {
				httpRequest = httptest.NewRequest(http.MethodPost, "/v1/completions", strings.NewReader(tc.body))
			}
			httpRequest.Header.Set("Content-Type", "application/json")

			_, err := NewBasicRequestImplForPipeline(httpRequest)
			if tc.expectError {
				assert.Error(t, err, "expected error but got nil")
			} else {
				assert.NoError(t, err, "expected no error but got: %v", err)
			}
		})
	}
}

// TestParseSchedulingMetadata_ConflictErrorNoSessionIDLeak checks that a
// conflicting session_id fails request creation and the returned error carries
// no session id fragment that could reach the client.
func TestParseSchedulingMetadata_ConflictErrorNoSessionIDLeak(t *testing.T) {
	metrics.InitMetrics(nil)

	const dynamoSess = "dynamo-secret-session-1234"
	const codexSess = "codex-secret-session-5678"

	request := openai.ChatCompletionRequest{
		Model: "deepseek-r1",
		Messages: []openai.ChatCompletionMessage{
			{Content: "huawei"},
		},
		MaxTokens: 100,
	}
	bytes, _ := utils.SonicApi.Marshal(request)

	httpRequest := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(string(bytes)))
	httpRequest.Header.Set("X-Dynamo-Session-ID", dynamoSess)
	httpRequest.Header.Set("thread-id", codexSess)

	_, err := NewGenerateRequestImpl(httpRequest, constant.Prefill, true)
	if err == nil {
		t.Fatal("expected conflict error from NewGenerateRequestImpl, got nil")
	}

	wrappedMsg := fmt.Sprintf("fail to create generate request: %v", err)

	for _, fragment := range []string{
		dynamoSess,
		codexSess,
		"dynamo-secret",
		"codex-secret",
		"dyna",
		"code",
	} {
		if strings.Contains(wrappedMsg, fragment) {
			t.Errorf("wrapped error message leaks session_id fragment %q: %s", fragment, wrappedMsg)
		}
	}

	if !strings.Contains(wrappedMsg, "conflict") {
		t.Errorf("error should mention conflict, got: %s", wrappedMsg)
	}
}

func TestNewMMGenerateRequestImplParsesSchedulingMetadata(t *testing.T) {
	metrics.InitMetrics(nil)

	body := `{
		"model":"m",
		"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"http://example/a.png"}}]}],
		"max_tokens":40,
		"session_id":"sess-mm-1",
		"parent_session_id":"parent-mm-1",
		"priority":3,
		"output_length":17
	}`
	httpRequest := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	impl, err := NewMMGenerateRequestImpl(httpRequest, constant.Prefill, true)
	if err != nil {
		t.Fatalf("NewMMGenerateRequestImpl() error = %v", err)
	}
	if impl.GetSessionId() != "sess-mm-1" || impl.GetParentSessionId() != "parent-mm-1" {
		t.Fatalf("session ids = %q / %q", impl.GetSessionId(), impl.GetParentSessionId())
	}
	if impl.GetPriority() != 3 || impl.GetOutputLength() != 17 {
		t.Fatalf("priority=%d outputLength=%d, want 3 and 17", impl.GetPriority(), impl.GetOutputLength())
	}
}

func TestNewMMGenerateRequestImplOutputLengthFallsBackToMaxTokens(t *testing.T) {
	metrics.InitMetrics(nil)

	body := `{"model":"m","messages":[{"role":"user","content":"hi"}],"max_tokens":40,"session_id":"sess-mm-2"}`
	httpRequest := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	impl, err := NewMMGenerateRequestImpl(httpRequest, constant.Prefill, true)
	if err != nil {
		t.Fatalf("NewMMGenerateRequestImpl() error = %v", err)
	}
	if impl.GetOutputLength() != 40 {
		t.Fatalf("outputLength = %d, want max_tokens 40", impl.GetOutputLength())
	}
}

func TestNewMMGenerateRequestImplRejectsConflictingSessionHeaders(t *testing.T) {
	metrics.InitMetrics(nil)

	const dynamoSess = "dynamo-secret-session-1234"
	const codexSess = "codex-secret-session-5678"
	body := `{"model":"m","messages":[{"role":"user","content":"hi"}],"max_tokens":40}`
	httpRequest := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	httpRequest.Header.Set("X-Dynamo-Session-ID", dynamoSess)
	httpRequest.Header.Set("thread-id", codexSess)

	_, err := NewMMGenerateRequestImpl(httpRequest, constant.Prefill, true)
	if err == nil {
		t.Fatal("expected conflict error from NewMMGenerateRequestImpl, got nil")
	}
	wrappedMsg := fmt.Sprintf("fail to create generate request: %v", err)
	for _, fragment := range []string{dynamoSess, codexSess, "dynamo-secret", "codex-secret", "dyna", "code"} {
		if strings.Contains(wrappedMsg, fragment) {
			t.Errorf("wrapped error message leaks session_id fragment %q: %s", fragment, wrappedMsg)
		}
	}
	if !strings.Contains(wrappedMsg, "conflict") {
		t.Errorf("error should mention conflict, got: %s", wrappedMsg)
	}
}
