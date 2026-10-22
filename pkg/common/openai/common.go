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

package openai

import "encoding/json"

const (
	PromptKey               = "prompt"
	ChatMessagesKey         = "messages"
	MaxTokenKey             = "max_tokens"
	MaxCompletionTokenKey   = "max_completion_tokens"
	KvTransferParamsKey     = "kv_transfer_params"
	UsageKey                = "usage"
	PromptTokensKey         = "prompt_tokens"
	PromptTokensDetailsKey  = "prompt_tokens_details"
	CachedTokensKey         = "cached_tokens"
	EcTransferParamsKey     = "ec_transfer_params"
	StreamOptionsKey        = "stream_options"
	StreamKey               = "stream"
	MinTokensKey            = "min_tokens"
	IncludeUsageKey         = "include_usage"
	ContinuousUsageStatsKey = "continuous_usage_stats"

	// vllm-specific
	RequestId = "request_id"
)

type UsageBody struct {
	Usage Usage `json:"usage"`
}

type Usage struct {
	PromptTokens            int                      `json:"prompt_tokens"`
	CompletionTokens        int                      `json:"completion_tokens"`
	TotalTokens             int                      `json:"total_tokens"`
	PromptTokensDetails     *PromptTokensDetails     `json:"prompt_tokens_details"`
	CompletionTokensDetails *CompletionTokensDetails `json:"completion_tokens_details"`
}

type CompletionTokensDetails struct {
	AudioTokens              int `json:"audio_tokens"`
	ReasoningTokens          int `json:"reasoning_tokens"`
	AcceptedPredictionTokens int `json:"accepted_prediction_tokens"`
	RejectedPredictionTokens int `json:"rejected_prediction_tokens"`
}

type PromptTokensDetails struct {
	AudioTokens  int `json:"audio_tokens"`
	CachedTokens int `json:"cached_tokens"`
}

type FinishReason string

const (
	FinishReasonStop          FinishReason = "stop"
	FinishReasonLength        FinishReason = "length"
	FinishReasonFunctionCall  FinishReason = "function_call"
	FinishReasonToolCalls     FinishReason = "tool_calls"
	FinishReasonContentFilter FinishReason = "content_filter"
	FinishReasonNull          FinishReason = "null"
)

type InstanceResponse struct {
	InstanceName string              `json:"instance_name"`
	StatusCode   int                 `json:"status_code"`
	Header       map[string][]string `json:"header,omitempty"`
	Result       string              `json:"result,omitempty"`
	Error        string              `json:"error,omitempty"`
}

type InstanceStatusCounts struct {
	Success int64 `json:"success"`
	Failed  int64 `json:"failed"`
}

type BroadcastResponse struct {
	Status    string               `json:"status"`    // Status: "success"/"partial"/"failed"
	Total     InstanceStatusCounts `json:"total"`     // Total statistics
	Responses []*InstanceResponse  `json:"responses"` // Instance response list
}

type BroadcastRequest struct {
	Targets string              `json:"targets"` // Lazy parsing
	URI     string              `json:"uri"`
	Method  string              `json:"method"`
	Headers map[string][]string `json:"headers"`
	Body    json.RawMessage     `json:"body"` // Keep original JSON
}

type VllmErrorBody struct {
	Object  string `json:"object,omitempty"`
	Code    int    `json:"code"`
	Message string `json:"message"`
	Type    string `json:"type"`
	Param   string `json:"param,omitempty"`
}

type ChoiceBody struct {
	FinishReason string      `json:"finish_reason"`
	StopReason   interface{} `json:"stop_reason"`
}

type OminiErrorBody struct {
	ChoicesBody []*ChoiceBody `json:"choices,omitempty"`
}

type StreamErrRes struct {
	ErrBody *VllmErrorBody `json:"error,omitempty"`
}
