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

package handler

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"infer-dp-scheduler/pkg/common/utils"
	"infer-dp-scheduler/pkg/route-server/metrics"
)

type fakeWriter struct {
	bytes      [][]byte
	writeCount int
}

func newFakeWriter() *fakeWriter {
	return &fakeWriter{
		bytes:      make([][]byte, 0),
		writeCount: 0,
	}
}

func (f *fakeWriter) Write(p []byte) (n int, err error) {
	f.bytes = append(f.bytes, p)
	f.writeCount++
	return len(p), nil
}

func TestStreamWriteNormalCase(t *testing.T) {
	metrics.InitMetrics(nil)

	w := newFakeWriter()
	firstToken := false
	secToken := false
	fiveTokenCount := 0
	dp := NewEventTrackingWriter(w, 0, 0, "", "", func() {
		firstToken = true
	}, func() {
		secToken = true
	}, func() {
		fiveTokenCount++
	})

	lines := []string{
		"data: {\"id\":\"test\",\"object\":\"test\",\"created\":1,\"model\":\"deepseek-r1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"token_1\"}}]}\n\n",
		"data: {\"id\":\"test\",\"object\":\"test\",\"created\":1,\"model\":\"deepseek-r1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"token_1\"}}]}\n\n",
		"data: {\"id\":\"test\",\"object\":\"test\",\"created\":1,\"model\":\"deepseek-r1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"token_1\"}}]}\n\n",
		"data: {\"id\":\"test\",\"object\":\"test\",\"created\":1,\"model\":\"deepseek-r1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hello\",\"tool_calls\":[]},\"finish_reason\":\"length\",\"stop_reason\":\"length\"}]}\n\n",
		"data: {\"id\":\"test\",\"object\":\"test\",\"created\":1,\"model\":\"deepseek-r1\",\"usage\":{\"prompt_tokens\":2000,\"completion_tokens\":200,\"total_tokens\":2200,\"prompt_tokens_details\":null,\"completion_tokens_details\":null}}\n\n",
		"data: [DONE]\n\n",
	}
	for _, line := range lines {
		_, err := dp.Write([]byte(line))
		if err != nil {
			t.Fatalf("write err")
		}
	}

	detail, finishReason, _ := dp.ParseAndRecordError()
	assert.Nil(t, detail)
	assert.Equal(t, "unknown", finishReason)
	assert.True(t, firstToken)
	assert.True(t, secToken)
	assert.Equal(t, fiveTokenCount, 1)

	usage := dp.ParseAndRecordTokens()
	assert.NotNil(t, usage)
	assert.Equal(t, 200, usage.CompletionTokens)
	assert.Equal(t, 2200, usage.TotalTokens)
	assert.Equal(t, 2000, usage.PromptTokens)
	assert.Equal(t, "length", dp.ParseFinishReason())
}

func TestStreamWriteLinkOmniErrCase(t *testing.T) {
	metrics.InitMetrics(nil)

	w := newFakeWriter()
	firstToken := false
	secToken := false
	fiveTokenCount := 0
	dp := NewEventTrackingWriter(w, 0, 0, "", "", func() {
		firstToken = true
	}, func() {
		secToken = true
	}, func() {
		fiveTokenCount++
	})

	lines := []string{
		"data: {\"id\":\"test\",\"object\":\"test\",\"created\":1,\"model\":\"deepseek-r1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"token_1\"}}]}\n\n",
		"data: {\"id\":\"test\",\"object\":\"test\",\"created\":1,\"model\":\"deepseek-r1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hello\",\"tool_calls\":[]},\"finish_reason\":\"abort\",\"stop_reason\":\"Abort for KV loading failure\"}]}\n\n",
		"data: [DONE]\n\n",
	}
	for _, line := range lines {
		_, err := dp.Write([]byte(line))
		if err != nil {
			t.Fatalf("write err")
		}
	}

	detail, finishReason, _ := dp.ParseAndRecordError()
	assert.NotNil(t, detail)
	assert.True(t, detail.IsKVTransferErr)
	assert.Equal(t, "abort", finishReason)
	assert.True(t, firstToken)
	assert.True(t, secToken)
	assert.Equal(t, fiveTokenCount, 0)
}

func TestStreamWriteLinkAscendVllmErrCase(t *testing.T) {
	metrics.InitMetrics(nil)

	w := newFakeWriter()
	firstToken := false
	secToken := false
	fiveTokenCount := 0
	dp := NewEventTrackingWriter(w, 0, 0, "", "", func() {
		firstToken = true
	}, func() {
		secToken = true
	}, func() {
		fiveTokenCount++
	})

	lines := []string{
		"data: {\"id\":\"test\",\"object\":\"test\",\"created\":1,\"model\":\"deepseek-r1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"token_1\"}}]}\n\n",
		"data: {\"id\":\"test\",\"object\":\"test\",\"created\":1,\"error\": {\"object\": \"error\", \"message\": \"pd link is down!\", \"type\": \"PDLinkDownError\", \"param\": null, \"code\": 500}}\n\n",
		"data: [DONE]\n\n",
	}
	for _, line := range lines {
		_, err := dp.Write([]byte(line))
		if err != nil {
			t.Fatalf("write err")
		}
	}

	detail, finishReason, _ := dp.ParseAndRecordError()
	assert.NotNil(t, detail)
	assert.True(t, detail.IsKVTransferErr)
	assert.Equal(t, "unknown", finishReason)
	assert.True(t, firstToken)
	assert.True(t, secToken)
	assert.Equal(t, fiveTokenCount, 0)
}

func TestStreamWriteLinkAscendVllmNewErrCase(t *testing.T) {
	metrics.InitMetrics(nil)

	w := newFakeWriter()
	firstToken := false
	secToken := false
	fiveTokenCount := 0
	dp := NewEventTrackingWriter(w, 0, 0, "", "", func() {
		firstToken = true
	}, func() {
		secToken = true
	}, func() {
		fiveTokenCount++
	})

	lines := []string{
		"data: {\"id\":\"chatcmpl-xxx\",\"object\":\"chat.completion.chunk\",\"created\":1778758139,\"model\":\"qwen3-32b\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"\",\"reasoning_content\":null},\"logprobs\":null,\"finish_reason\":null}],\"prompt_token_ids\":null}\n\n",
		"data: {\"id\":\"chatcmpl-xxx\",\"object\":\"chat.completion.chunk\",\"created\":1778758139,\"model\":\"qwen3-32b\",\"choices\":[{\"index\":0,\"delta\":{\"reasoning_content\":null},\"logprobs\":null,\"finish_reason\":null,\"token_ids\":null}]}\n\n",
		"data: {\"error\": {\"message\": \"KV cache load failed for one or more remote blocks. The request can be retried.\", \"type\": \"InternalServerError\", \"param\": null, \"code\": 500}}\n\n",
		"data: [DONE]\n\n",
	}
	for _, line := range lines {
		_, err := dp.Write([]byte(line))
		if err != nil {
			t.Fatalf("write err")
		}
	}

	detail, finishReason, _ := dp.ParseAndRecordError()
	assert.NotNil(t, detail)
	assert.True(t, detail.IsKVTransferErr)
	assert.Equal(t, "unknown", finishReason)
	assert.True(t, firstToken)
	assert.True(t, secToken)
	assert.Equal(t, fiveTokenCount, 0)
}

func TestStreamWriteLinkErrorWithSkipLines(t *testing.T) {
	metrics.InitMetrics(nil)

	type args struct {
		linesToSkip int
		inputs      []string
	}

	tests := []struct {
		name                   string
		args                   args
		expectWrittenLineCount int
		expectDetectErr        bool
		expectDetailFunc       func(t *testing.T, details *utils.InstanceFailureDetails) bool
	}{
		{
			name: "normal skip 1 case (v1/completions)",
			args: args{
				linesToSkip: 1,
				inputs: []string{
					"data: {\"id\":\"test\",\"object\":\"test\",\"created\":1,\"model\":\"deepseek-r1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"token_1\"}}]}\n\n",
					"data: {\"id\":\"test\",\"object\":\"test\",\"created\":1,\"model\":\"deepseek-r1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"token_1\"}}]}\n\n",
					"data: [DONE]\n\n",
				},
			},
			expectWrittenLineCount: 2,
			expectDetectErr:        false,
		},
		{
			name: "normal skip 2 case (v1/chat/completions)",
			args: args{
				linesToSkip: 2,
				inputs: []string{
					"data: {\"id\":\"test\",\"object\":\"test\",\"created\":1,\"model\":\"deepseek-r1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"token_1\"}}]}\n\n",
					"data: {\"id\":\"test\",\"object\":\"test\",\"created\":1,\"model\":\"deepseek-r1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"token_1\"}}]}\n\n",
					"data: [DONE]\n\n",
				},
			},
			expectWrittenLineCount: 1,
			expectDetectErr:        false,
		},
		{
			name: "execptional skip 2 case (v1/chat/completions)",
			args: args{
				linesToSkip: 2,
				inputs: []string{
					"data: {\"id\":\"chatcmpl-3232323kkk\",\"object\":\"chat.completion.chunk\",\"created\":1776948784,\"model\":\"qwen3-32b-w8a8c8\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"\",\"reasoning_content\":null},\"logprobs\":null,\"finish_reason\":null}],\"prompt_token_ids\":null}\n\n",
					"data: {\"error\": {\"message\": \"Internal server error\", \"type\": \"InternalServerError\", \"param\": null, \"code\": 500}}\n\n",
					"data: [DONE]\n\n",
				},
			},
			expectWrittenLineCount: 2,
			expectDetectErr:        true,
			expectDetailFunc: func(t *testing.T, details *utils.InstanceFailureDetails) bool {
				assert.Equal(t, "Internal server error", details.ErrorMessage)
				assert.Equal(t, 500, details.StatusCode)
				assert.False(t, details.IsKVTransferErr)
				return true
			},
		},
		{
			name: "execptional skip 1 case (v1/completions)",
			args: args{
				linesToSkip: 1,
				inputs: []string{
					"data: {\"error\": {\"message\": \"Internal server error\", \"type\": \"InternalServerError\", \"param\": null, \"code\": 500}}\n\n",
					"data: [DONE]\n\n",
				},
			},
			expectWrittenLineCount: 2,
			expectDetectErr:        true,
			expectDetailFunc: func(t *testing.T, details *utils.InstanceFailureDetails) bool {
				assert.Equal(t, "Internal server error", details.ErrorMessage)
				assert.Equal(t, 500, details.StatusCode)
				assert.False(t, details.IsKVTransferErr)
				return true
			},
		},

		{
			name: "execptional skip 1 case (v1/completions)",
			args: args{
				linesToSkip: 1,
				inputs: []string{
					"data: {\"error\": {\"message\": \"400 Bad Request\", \"type\": \"BadRequestError\", \"param\": null, \"code\": 400}}\n\n",
					"data: [DONE]\n\n",
				},
			},
			expectWrittenLineCount: 2,
			expectDetectErr:        true,
			expectDetailFunc: func(t *testing.T, details *utils.InstanceFailureDetails) bool {
				assert.Equal(t, "400 Bad Request", details.ErrorMessage)
				assert.Equal(t, 400, details.StatusCode)
				assert.False(t, details.IsKVTransferErr)
				return true
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := newFakeWriter()
			dp := NewEventTrackingWriter(w, tt.args.linesToSkip, 0, "", "", func() {
			}, func() {
			}, func() {
			})

			for _, line := range tt.args.inputs {
				_, err := dp.Write([]byte(line))
				if err != nil {
					t.Fatalf("write err")
				}
			}

			detail, _, _ := dp.ParseAndRecordError()
			assert.Equal(t, tt.expectDetectErr, detail != nil)

			if tt.expectDetectErr {
				if !tt.expectDetailFunc(t, detail) {
					t.Fatalf("details is not as expected")
				}
			}
			assert.Equal(t, tt.expectWrittenLineCount, w.writeCount)
		})
	}

}
