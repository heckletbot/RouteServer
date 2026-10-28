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

package anthropic

import (
	"net/http"
	"strings"
)

const (
	PromptKey               = "prompt"
	ChatMessagesKey         = "messages"
	MaxTokenKey             = "max_tokens"
	MaxCompletionTokenKey   = "max_completion_tokens"
	KvTransferParamsKey     = "kv_transfer_params"
	UsageKey                = "usage"
	SystemKey               = "system"
	PromptTokensKey         = "prompt_tokens"
	PromptTokensDetailsKey  = "prompt_tokens_details"
	CachedTokensKey         = "cached_tokens"
	EcTransferParamsKey     = "ec_transfer_params"
	StreamOptionsKey        = "stream_options"
	StreamKey               = "stream"
	IncludeUsageKey         = "include_usage"
	ContinuousUsageStatsKey = "continuous_usage_stats"

	// RequestId is the vLLM-specific request id field.
	RequestId = "request_id"
)

type AnthropicStopReasonBody struct {
	StopReason string `json:"stop_reason"`
}

type AnthropicDeltaBody struct {
	Delta *AnthropicStopReasonBody `json:"delta"`
}

func MapStopReason(stopReason string) string {
	switch stopReason {
	case "end_turn":
		return "stop"
	case "max_tokens":
		return "length"
	case "stop_sequence":
		return "stop"
	case "tool_use":
		return "tool_calls"
	default:
		return stopReason
	}
}

type ErrorMapping struct {
	Type     string   `yaml:"type,omitempty"`
	Messages []string `yaml:"messages,omitempty"`
	Code     int      `yaml:"code"`
}

var (
	errorMappings []ErrorMapping
)

func InitErrorMapping(mappings []ErrorMapping) {
	errorMappings = mappings
}

func ResolveAnthropicErrorCode(errorType string, message string) int {
	errorTypeLower := strings.ToLower(errorType)
	messageLower := strings.ToLower(message)

	for _, mapping := range errorMappings {
		// match type
		typeMatch := true
		if mapping.Type != "" {
			typeMatch = mapping.Type == errorTypeLower
		}

		// match all messages
		messagesMatch := true
		if len(mapping.Messages) > 0 {
			for _, msg := range mapping.Messages {
				if !strings.Contains(messageLower, msg) {
					messagesMatch = false
					break
				}
			}
		}

		if mapping.Type != "" && len(mapping.Messages) > 0 {
			if typeMatch && messagesMatch {
				return mapping.Code
			}
		} else if mapping.Type != "" {
			if typeMatch {
				return mapping.Code
			}
		} else if len(mapping.Messages) > 0 {
			if messagesMatch {
				return mapping.Code
			}
		}
	}

	return http.StatusInternalServerError
}
