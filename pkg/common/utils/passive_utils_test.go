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

package utils

import (
	"net/http"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"infer-dp-scheduler/pkg/common/constant"
)

func TestTryParseErrFromStreamResponse(t *testing.T) {
	tests := []struct {
		name string
		body string
		want *InstanceFailureDetails
	}{
		{
			name: "ascend vllm link down error",
			body: `{"error":{"object":"error", "message":"pd link is down!", "type":"PDLinkDownError", "param": null, "code":500}}`,
			want: &InstanceFailureDetails{
				StatusCode:      500,
				IsTimeout:       false,
				IsConnectionErr: false,
				IsKVTransferErr: true,
				ErrorMessage:    "pd link is down!",
			},
		},
		{
			name: "new vllm-ascend link down error",
			body: `{"error": {"message": "KV cache load failed for one or more remote blocks. The request can be retried.", "type": "InternalServerError", "param": null, "code": 500}}`,
			want: &InstanceFailureDetails{
				StatusCode:      500,
				IsTimeout:       false,
				IsConnectionErr: false,
				IsKVTransferErr: true,
				ErrorMessage:    "KV cache load failed for one or more remote blocks. The request can be retried.",
			},
		},
		{
			name: "omini link down error",
			body: `{"model":"deepseek","choices":[{"index":0,"delta":{"content":"Hello","tool_calls":[]},"finish_reason":"abort","stop_reason":"Abort for KV loading failure"}]}`,
			want: &InstanceFailureDetails{
				StatusCode:      500,
				IsTimeout:       false,
				IsConnectionErr: false,
				IsKVTransferErr: true,
				ErrorMessage:    "Abort for KV loading failure",
			},
		},
		{
			name: "vllm success",
			body: `{"id":"test","object":"test","created":1,"model":"deepseek-r1","choices":[{"index":0,"delta":{"content":"token_31"}}],"usage":{"prompt_tokens":2000,"completion_tokens":32,"total_tokens":2032,"prompt_tokens_details":null,"completion_tokens_details":null}}`,
			want: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got, _ := TryParseErrFromStreamResponse([]byte(tt.body)); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("TryParseErrFromStreamResponse() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDetailsFromNonStreamResponse(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		body       string
		want       *InstanceFailureDetails
	}{
		{
			name:       "ascend vllm link down error",
			statusCode: 500,
			body:       `{"object":"error", "message":"pd link is down!", "type":"PDLinkDownError", "param": null, "code":500}`,
			want: &InstanceFailureDetails{
				StatusCode:      500,
				IsTimeout:       false,
				IsConnectionErr: false,
				IsKVTransferErr: true,
				ErrorMessage:    "pd link is down!",
			},
		},
		{
			name:       "omini link down error",
			statusCode: 500,
			body:       `{"model": "deepseek","choices": [{"index": 0,"message": {"role": "assistant","reasoning_content": null,"content": "您好","tool_calls": []},"Logprobs": null,"finish_reason": "abort", "stop_reason": "Abort for KV loading failure"}],"prompt_logprobs": null,"kv_transfer params": null}`,
			want: &InstanceFailureDetails{
				StatusCode:      500,
				IsTimeout:       false,
				IsConnectionErr: false,
				IsKVTransferErr: true,
				ErrorMessage:    "Abort for KV loading failure",
			},
		},
		{
			name:       "vllm direct error",
			statusCode: 500,
			body:       `Internal Server Error`,
			want: &InstanceFailureDetails{
				StatusCode:      500,
				IsTimeout:       false,
				IsConnectionErr: false,
				IsKVTransferErr: false,
				ErrorMessage:    "Internal Server Error",
			},
		},
		{
			name:       "vllm unexpected error",
			statusCode: 500,
			body:       `{"error":"Invalid input", "code":500}`,
			want: &InstanceFailureDetails{
				StatusCode:      500,
				IsTimeout:       false,
				IsConnectionErr: false,
				IsKVTransferErr: false,
				ErrorMessage:    "{\"error\":\"Invalid input\", \"code\":500}",
			},
		},
		{
			name:       "vllm unexpected error",
			statusCode: 500,
			body:       `{"error": {"message": "KV cache load failed for one or more remote blocks. The request can be retried.","type": "InternalServerError","param": null,"code": 500}}`,
			want: &InstanceFailureDetails{
				StatusCode:      500,
				IsTimeout:       false,
				IsConnectionErr: false,
				IsKVTransferErr: true,
				ErrorMessage:    "KV cache load failed for one or more remote blocks. The request can be retried.",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got, _ := DetailsFromNonStreamResponse(tt.statusCode, []byte(tt.body)); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("DetailsFromNonStreamResponse() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_tryParseErrFromOmini(t *testing.T) {
	type args struct {
		statusCode int
		body       []byte
	}
	tests := []struct {
		name  string
		args  args
		want  *InstanceFailureDetails
		want1 string
	}{
		{
			name: "number stop reason",
			args: args{
				statusCode: http.StatusOK,
				body:       []byte(`{"choices":[{"finish_reason":"stop","stop_reason":163586}],"usage":{"prompt_tokens":26,"total_tokens":82,"completion_tokens":56,"prompt_tokens_details":null}}`),
			},
			want:  nil,
			want1: constant.UnKnownReason,
		},
		{
			name: "string stop reason",
			args: args{
				statusCode: http.StatusOK,
				body:       []byte(`{"choices":[{"finish_reason":"stop","stop_reason":"not kv failure"}],"usage":{"prompt_tokens":26,"total_tokens":82,"completion_tokens":56,"prompt_tokens_details":null}}`),
			},
			want:  nil,
			want1: constant.UnKnownReason,
		},
		{
			name: "nil stop reason",
			args: args{
				statusCode: http.StatusOK,
				body:       []byte(`{"choices":[{"finish_reason":"stop","stop_reason":null}],"usage":{"prompt_tokens":26,"total_tokens":82,"completion_tokens":56,"prompt_tokens_details":null}}`),
			},
			want:  nil,
			want1: constant.UnKnownReason,
		},
		{
			name: "nil finish reason",
			args: args{
				statusCode: http.StatusOK,
				body:       []byte(`{"choices":[{"finish_reason":null,"stop_reason":null}],"usage":{"prompt_tokens":26,"total_tokens":82,"completion_tokens":56,"prompt_tokens_details":null}}`),
			},
			want:  nil,
			want1: constant.UnKnownReason,
		},
		{
			name: "kv failure stop reason",
			args: args{
				statusCode: http.StatusOK,
				body:       []byte(`{"choices":[{"finish_reason":"stop","stop_reason":"abort for kv loading failure"}],"usage":{"prompt_tokens":26,"total_tokens":82,"completion_tokens":56,"prompt_tokens_details":null}}`),
			},
			want: &InstanceFailureDetails{
				StatusCode:      http.StatusInternalServerError,
				IsTimeout:       false,
				IsConnectionErr: false,
				IsKVTransferErr: true,
				ErrorMessage:    "abort for kv loading failure",
			},
			want1: "stop",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, got1 := tryParseErrFromOmini(tt.args.statusCode, tt.args.body)
			assert.Equalf(t, tt.want, got, "tryParseErrFromOmini(%v, %v)", tt.args.statusCode, tt.args.body)
			assert.Equalf(t, tt.want1, got1, "tryParseErrFromOmini(%v, %v)", tt.args.statusCode, tt.args.body)
		})
	}
}
