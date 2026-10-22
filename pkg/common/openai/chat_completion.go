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

import (
	"encoding/json"
	"fmt"

	"github.com/bytedance/sonic"
)

var sonicApi = sonic.Config{
	UseNumber:   true,
	SortMapKeys: true,
}.Froze()

type ChatCompletionRequest struct {
	Model               string                        `json:"model"`
	Messages            []ChatCompletionMessage       `json:"messages"`
	MaxTokens           int                           `json:"max_tokens,omitempty"`
	MaxCompletionTokens int                           `json:"max_completion_tokens,omitempty"`
	Temperature         float32                       `json:"temperature,omitempty"`
	TopP                float32                       `json:"top_p,omitempty"`
	N                   int                           `json:"n,omitempty"`
	Stream              bool                          `json:"stream,omitempty"`
	Stop                []string                      `json:"stop,omitempty"`
	PresencePenalty     float32                       `json:"presence_penalty,omitempty"`
	ResponseFormat      *ChatCompletionResponseFormat `json:"response_format,omitempty"`
	Seed                *int                          `json:"seed,omitempty"`
	FrequencyPenalty    float32                       `json:"frequency_penalty,omitempty"`
	LogitBias           map[string]int                `json:"logit_bias,omitempty"`
	LogProbs            bool                          `json:"logprobs,omitempty"`
	TopLogProbs         int                           `json:"top_logprobs,omitempty"`
	User                string                        `json:"user,omitempty"`
	Functions           []FunctionDefinition          `json:"functions,omitempty"`
	FunctionCall        any                           `json:"function_call,omitempty"`
	Tools               []Tool                        `json:"tools,omitempty"`
	ToolChoice          any                           `json:"tool_choice,omitempty"`
	StreamOptions       *StreamOptions                `json:"stream_options,omitempty"`
	ParallelToolCalls   any                           `json:"parallel_tool_calls,omitempty"`
	Store               bool                          `json:"store,omitempty"`
	ReasoningEffort     string                        `json:"reasoning_effort,omitempty"`
	Metadata            map[string]string             `json:"metadata,omitempty"`
	Prediction          *Prediction                   `json:"prediction,omitempty"`
	ChatTemplateKwargs  map[string]any                `json:"chat_template_kwargs,omitempty"`
	KvTransferParams    map[string]any                `json:"kv_transfer_params,omitempty"`
	IgnoreEos           bool                          `json:"ignore_eos"`
}

type ChatCompletionMessage struct {
	Role             string `json:"role"`
	Content          string `json:"content,omitempty"`
	Refusal          string `json:"refusal,omitempty"`
	MultiContent     []ChatMessagePart
	Name             string        `json:"name,omitempty"`
	ReasoningContent string        `json:"reasoning_content,omitempty"`
	FunctionCall     *FunctionCall `json:"function_call,omitempty"`
	ToolCalls        []ToolCall    `json:"tool_calls,omitempty"`
	ToolCallID       string        `json:"tool_call_id,omitempty"`
}

type ChatCompletionResponseFormat struct {
	Type       ChatCompletionResponseFormatType        `json:"type,omitempty"`
	JSONSchema *ChatCompletionResponseFormatJSONSchema `json:"json_schema,omitempty"`
}

type FunctionDefinition struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Strict      bool   `json:"strict,omitempty"`
	Parameters  any    `json:"parameters"`
}

type Prediction struct {
	Content string `json:"content"`
	Type    string `json:"type"`
}

type ChatMessagePart struct {
	Type     ChatMessagePartType  `json:"type,omitempty"`
	Text     string               `json:"text,omitempty"`
	ImageURL *ChatMessageImageURL `json:"image_url,omitempty"`
}

type Tool struct {
	Type     ToolType            `json:"type"`
	Function *FunctionDefinition `json:"function,omitempty"`
}

type ChatCompletionResponseFormatJSONSchema struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Schema      json.Marshaler `json:"schema"`
	Strict      bool           `json:"strict"`
}

type ChatMessageImageURL struct {
	URL    string         `json:"url,omitempty"`
	Detail ImageURLDetail `json:"detail,omitempty"`
}

type FunctionCall struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

type ToolCall struct {
	Index    *int         `json:"index,omitempty"`
	ID       string       `json:"id,omitempty"`
	Type     ToolType     `json:"type"`
	Function FunctionCall `json:"function"`
}

type ToolType string

type ChatMessagePartType string

type ImageURLDetail string

type ChatCompletionResponseFormatType string

type ChatCompletionResponse struct {
	ID                  string                 `json:"id"`
	Object              string                 `json:"object"`
	Created             int64                  `json:"created"`
	Model               string                 `json:"model"`
	Choices             []ChatCompletionChoice `json:"choices"`
	Usage               Usage                  `json:"usage"`
	SystemFingerprint   string                 `json:"system_fingerprint"`
	PromptFilterResults []PromptFilterResult   `json:"prompt_filter_results,omitempty"`
	KvTransferParams    map[string]any         `json:"kv_transfer_params"`
	EcTransferParams    map[string]any         `json:"ec_transfer_params,omitempty"`
}

type PromptFilterResult struct {
	Index                int                  `json:"index"`
	ContentFilterResults ContentFilterResults `json:"content_filter_results,omitempty"`
}

type ChatCompletionChoice struct {
	Index                int                   `json:"index"`
	Message              ChatCompletionMessage `json:"message"`
	FinishReason         FinishReason          `json:"finish_reason"`
	LogProbs             *LogProbs             `json:"logprobs,omitempty"`
	StopReason           interface{}           `json:"stop_reason"`
	ContentFilterResults ContentFilterResults  `json:"content_filter_results,omitempty"`
}

type LogProbs struct {
	// Content is a list of message content tokens with log probability information.
	Content []LogProb `json:"content"`
}

type LogProb struct {
	Token       string        `json:"token"`
	LogProb     float64       `json:"logprob"`
	Bytes       []byte        `json:"bytes,omitempty"` // Omitting the field if it is null
	TopLogProbs []TopLogProbs `json:"top_logprobs"`
}

type TopLogProbs struct {
	Token   string  `json:"token"`
	LogProb float64 `json:"logprob"`
	Bytes   []byte  `json:"bytes,omitempty"`
}

type ContentFilterResults struct {
	Hate      Hate      `json:"hate,omitempty"`
	SelfHarm  SelfHarm  `json:"self_harm,omitempty"`
	Sexual    Sexual    `json:"sexual,omitempty"`
	Violence  Violence  `json:"violence,omitempty"`
	JailBreak JailBreak `json:"jailbreak,omitempty"`
	Profanity Profanity `json:"profanity,omitempty"`
}

type Hate struct {
	Filtered bool   `json:"filtered"`
	Severity string `json:"severity,omitempty"`
}
type SelfHarm struct {
	Filtered bool   `json:"filtered"`
	Severity string `json:"severity,omitempty"`
}
type Sexual struct {
	Filtered bool   `json:"filtered"`
	Severity string `json:"severity,omitempty"`
}
type Violence struct {
	Filtered bool   `json:"filtered"`
	Severity string `json:"severity,omitempty"`
}

type JailBreak struct {
	Filtered bool `json:"filtered"`
	Detected bool `json:"detected"`
}

type Profanity struct {
	Filtered bool `json:"filtered"`
	Detected bool `json:"detected"`
}

type ChatCompletionStreamResponse struct {
	ID                  string                       `json:"id"`
	Object              string                       `json:"object"`
	Created             int64                        `json:"created"`
	Model               string                       `json:"model"`
	Choices             []ChatCompletionStreamChoice `json:"choices"`
	SystemFingerprint   string                       `json:"system_fingerprint,omitempty"`
	PromptAnnotations   []PromptAnnotation           `json:"prompt_annotations,omitempty"`
	PromptFilterResults []PromptFilterResult         `json:"prompt_filter_results,omitempty"`
	Usage               *Usage                       `json:"usage,omitempty"`
	KvTransferParams    map[string]any               `json:"kv_transfer_params,omitempty"`
}

type PromptAnnotation struct {
	PromptIndex          int                  `json:"prompt_index,omitempty"`
	ContentFilterResults ContentFilterResults `json:"content_filter_results,omitempty"`
}

type ChatCompletionStreamChoice struct {
	Index                int                                 `json:"index"`
	PromptNumToken       int                                 `json:"prompt_num_token,omitempty"`
	OutputNumToken       int                                 `json:"output_num_token,omitempty"`
	Delta                ChatCompletionStreamChoiceDelta     `json:"delta"`
	Logprobs             *ChatCompletionStreamChoiceLogprobs `json:"logprobs,omitempty"`
	FinishReason         *FinishReason                       `json:"finish_reason,omitempty"`
	ContentFilterResults *ContentFilterResults               `json:"content_filter_results,omitempty"`
}

type ChatCompletionStreamChoiceDelta struct {
	Content          string        `json:"content,omitempty"`
	Role             string        `json:"role,omitempty"`
	FunctionCall     *FunctionCall `json:"function_call,omitempty"`
	ToolCalls        []ToolCall    `json:"tool_calls,omitempty"`
	Refusal          string        `json:"refusal,omitempty"`
	ReasoningContent string        `json:"reasoning_content,omitempty"`
}

type ChatCompletionStreamChoiceLogprobs struct {
	Content []ChatCompletionTokenLogprob `json:"content,omitempty"`
	Refusal []ChatCompletionTokenLogprob `json:"refusal,omitempty"`
}

type ChatCompletionTokenLogprob struct {
	Token       string                                 `json:"token"`
	Bytes       []int64                                `json:"bytes,omitempty"`
	Logprob     float64                                `json:"logprob,omitempty"`
	TopLogprobs []ChatCompletionTokenLogprobTopLogprob `json:"top_logprobs"`
}

type ChatCompletionTokenLogprobTopLogprob struct {
	Token   string  `json:"token"`
	Bytes   []int64 `json:"bytes"`
	Logprob float64 `json:"logprob"`
}

type MMChatCompletionMessage struct {
	Role             string         `json:"role"`
	Content          MessageContent `json:"content,omitempty"`
	Refusal          string         `json:"refusal,omitempty"`
	MultiContent     []ChatMessagePart
	Name             string        `json:"name,omitempty"`
	ReasoningContent string        `json:"reasoning_content,omitempty"`
	FunctionCall     *FunctionCall `json:"function_call,omitempty"`
	ToolCalls        []ToolCall    `json:"tool_calls,omitempty"`
	ToolCallID       string        `json:"tool_call_id,omitempty"`
}

type MessageContent []ContentPart

type ContentPart struct {
	Type     string               `json:"type"` // "text" or "image_url"
	Text     *string              `json:"text,omitempty"`
	ImageURL *ChatMessageImageURL `json:"image_url,omitempty"`
}

func (mc *MessageContent) UnmarshalJSON(data []byte) error {
	// First try to parse as a string (traditional text message)
	var text string
	if err := sonicApi.Unmarshal(data, &text); err == nil {
		// Successfully parsed as string, wrap into a ContentPart
		*mc = MessageContent{
			ContentPart{
				Type: "text",
				Text: &text,
			},
		}
		return nil
	}

	// If not a string, try to parse as ContentPart array
	var parts []ContentPart
	if err := sonicApi.Unmarshal(data, &parts); err != nil {
		return fmt.Errorf("content must be a string or an array, got: %s", string(data))
	}
	*mc = MessageContent(parts)
	return nil
}

type ChatCompletionRequestV1 struct {
	Model               string                        `json:"model"`
	Messages            any                           `json:"messages"`
	MaxTokens           int                           `json:"max_tokens,omitempty"`
	MaxCompletionTokens int                           `json:"max_completion_tokens,omitempty"`
	Temperature         float32                       `json:"temperature,omitempty"`
	TopP                float32                       `json:"top_p,omitempty"`
	N                   int                           `json:"n,omitempty"`
	Stream              bool                          `json:"stream,omitempty"`
	Stop                []string                      `json:"stop,omitempty"`
	PresencePenalty     float32                       `json:"presence_penalty,omitempty"`
	ResponseFormat      *ChatCompletionResponseFormat `json:"response_format,omitempty"`
	Seed                *int                          `json:"seed,omitempty"`
	FrequencyPenalty    float32                       `json:"frequency_penalty,omitempty"`
	LogitBias           map[string]int                `json:"logit_bias,omitempty"`
	LogProbs            bool                          `json:"logprobs,omitempty"`
	TopLogProbs         int                           `json:"top_logprobs,omitempty"`
	User                string                        `json:"user,omitempty"`
	Functions           []FunctionDefinition          `json:"functions,omitempty"`
	FunctionCall        any                           `json:"function_call,omitempty"`
	Tools               []Tool                        `json:"tools,omitempty"`
	ToolChoice          any                           `json:"tool_choice,omitempty"`
	StreamOptions       *StreamOptions                `json:"stream_options,omitempty"`
	ParallelToolCalls   any                           `json:"parallel_tool_calls,omitempty"`
	Store               bool                          `json:"store,omitempty"`
	ReasoningEffort     string                        `json:"reasoning_effort,omitempty"`
	Metadata            map[string]string             `json:"metadata,omitempty"`
	Prediction          *Prediction                   `json:"prediction,omitempty"`
	ChatTemplateKwargs  map[string]any                `json:"chat_template_kwargs,omitempty"`
	KvTransferParams    map[string]any                `json:"kv_transfer_params,omitempty"`
	IgnoreEos           bool                          `json:"ignore_eos"`
}
