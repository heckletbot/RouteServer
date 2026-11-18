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

package openai

type CompletionRequest struct {
	Model            string            `json:"model"`
	Prompt           any               `json:"prompt,omitempty"`
	BestOf           int               `json:"best_of,omitempty"`
	Echo             bool              `json:"echo,omitempty"`
	FrequencyPenalty float32           `json:"frequency_penalty,omitempty"`
	LogitBias        map[string]int    `json:"logit_bias,omitempty"`
	Store            bool              `json:"store,omitempty"`
	Metadata         map[string]string `json:"metadata,omitempty"`
	LogProbs         int               `json:"logprobs,omitempty"`
	MaxTokens        int               `json:"max_tokens,omitempty"`
	N                int               `json:"n,omitempty"`
	PresencePenalty  float32           `json:"presence_penalty,omitempty"`
	Seed             *int              `json:"seed,omitempty"`
	Stop             []string          `json:"stop,omitempty"`
	Stream           bool              `json:"stream,omitempty"`
	Suffix           string            `json:"suffix,omitempty"`
	Temperature      float32           `json:"temperature,omitempty"`
	TopP             float32           `json:"top_p,omitempty"`
	User             string            `json:"user,omitempty"`
	StreamOptions    *StreamOptions    `json:"stream_options,omitempty"`
	KvTransferParams map[string]any    `json:"kv_transfer_params,omitempty"`
}

type StreamOptions struct {
	IncludeUsage         bool `json:"include_usage,omitempty"`
	ContinuousUsageStats bool `json:"continuous_usage_stats,omitempty"`
}

type CompletionResponse struct {
	ID               string             `json:"id"`
	Object           string             `json:"object"`
	Created          int64              `json:"created"`
	Model            string             `json:"model"`
	Choices          []CompletionChoice `json:"choices"`
	Usage            *Usage             `json:"usage,omitempty"`
	KvTransferParams map[string]any     `json:"kv_transfer_params,omitempty"`
}

// CompletionChoice represents one of possible completions.
type CompletionChoice struct {
	Text         string         `json:"text"`
	Index        int            `json:"index"`
	FinishReason string         `json:"finish_reason"`
	LogProbs     *LogprobResult `json:"logprobs,omitempty"`
}

type LogprobResult struct {
	Tokens        []string             `json:"tokens"`
	TokenLogprobs []float32            `json:"token_logprobs"`
	TopLogprobs   []map[string]float32 `json:"top_logprobs"`
	TextOffset    []int                `json:"text_offset"`
}
