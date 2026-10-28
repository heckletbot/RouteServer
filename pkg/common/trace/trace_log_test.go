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
	"github.com/stretchr/testify/assert"
	"infer-dp-scheduler/pkg/common/openai"

	"testing"
)

func TestTraceTokenLog(t *testing.T) {

	xSpanId := "span123456"
	xUserAlias := "userABC"
	traceId := "chat-789b"

	usage := &openai.Usage{
		PromptTokens:     50,
		CompletionTokens: 80,
	}
	ttft := 0.3
	tpot := 0.052
	latency := 1.2

	result := TraceTokenLog(xSpanId, xUserAlias, traceId, usage, ttft, tpot, latency)

	expected := `x-span-id=span123456|x-user-alias=userABC|CompletionMetric: ` +
		`{'trace_id': 'chat-789b', 'num_prompt_tokens': 50, 'num_output_tokens': 80, 'num_cached_tokens': 0, 'ttft': 300.00, 'tpot': 52.00, 'latency': 1200.00}`

	assert.Equal(t, expected, result, "TraceTokenLog return value mismatch")
}

func TestTraceTokenLogWithCacheToken(t *testing.T) {

	xSpanId := "span123456"
	xUserAlias := "userABC"
	traceId := "chat-789b"

	usage := &openai.Usage{
		PromptTokens:     50,
		CompletionTokens: 80,
		PromptTokensDetails: &openai.PromptTokensDetails{
			AudioTokens:  0,
			CachedTokens: 15,
		},
	}
	ttft := 0.3
	tpot := 0.052
	latency := 1.2

	result := TraceTokenLog(xSpanId, xUserAlias, traceId, usage, ttft, tpot, latency)

	expected := `x-span-id=span123456|x-user-alias=userABC|CompletionMetric: ` +
		`{'trace_id': 'chat-789b', 'num_prompt_tokens': 50, 'num_output_tokens': 80, 'num_cached_tokens': 15, 'ttft': 300.00, 'tpot': 52.00, 'latency': 1200.00}`

	assert.Equal(t, expected, result, "TraceTokenLog return value mismatch")
}
