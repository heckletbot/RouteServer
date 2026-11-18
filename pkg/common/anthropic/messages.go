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

package anthropic

// ============ Request structures ============

// AnthropicRequest is the Anthropic Messages API request body.
type AnthropicRequest struct {
	Model            string             `json:"model"`
	System           string             `json:"system,omitempty"`
	Messages         []AnthropicMessage `json:"messages"`
	MaxTokens        int                `json:"max_tokens"`
	StopSequences    []string           `json:"stop_sequences,omitempty"`
	Stream           bool               `json:"stream,omitempty"`
	Temperature      *float64           `json:"temperature,omitempty"`
	TopP             *float64           `json:"top_p,omitempty"`
	TopK             *int               `json:"top_k,omitempty"`
	Thinking         *ThinkingConfig    `json:"thinking,omitempty"`
	Tools            []AnthropicTool    `json:"tools,omitempty"`
	ToolChoice       *ToolChoice        `json:"tool_choice,omitempty"`
	Metadata         *Metadata          `json:"metadata,omitempty"`
	KvTransferParams map[string]any     `json:"kv_transfer_params,omitempty"`
}

// AnthropicMessage is a single message in the Anthropic conversation.
type AnthropicMessage struct {
	Role    string             `json:"role"`
	Content []AnthropicContent `json:"content"`
}

// AnthropicContent is a content block of an Anthropic message.
type AnthropicContent struct {
	Type      string         `json:"type,omitempty"`
	Text      string         `json:"text,omitempty"`
	Thinking  string         `json:"thinking,omitempty"`
	Signature string         `json:"signature,omitempty"`
	ID        string         `json:"id,omitempty"`
	Name      string         `json:"name,omitempty"`
	Input     map[string]any `json:"input,omitempty"`
	ToolUseID string         `json:"tool_use_id,omitempty"`
	Result    string         `json:"content,omitempty"`
	IsError   bool           `json:"is_error,omitempty"`
}

// Content type constants.
const (
	ContentTypeText       = "text"
	ContentTypeThinking   = "thinking"
	ContentTypeToolUse    = "tool_use"
	ContentTypeToolResult = "tool_result"
)

// ThinkingConfig configures extended thinking.
type ThinkingConfig struct {
	ThinkingTokens int  `json:"thinking_tokens"`
	BudgetTokens   int  `json:"budget_tokens,omitempty"`
	Enabled        bool `json:"enabled,omitempty"`
}

// AnthropicTool is a tool definition usable by the model.
type AnthropicTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"input_schema,omitempty"`
}

// ToolChoice selects how the model should use tools.
type ToolChoice struct {
	Type string `json:"type"`
	Name string `json:"name,omitempty"`
}

// Metadata holds optional request metadata.
type Metadata struct {
	UserID string `json:"user_id,omitempty"`
}

// ============ Response structures ============

// AnthropicResponse is the Anthropic Messages API response body.
type AnthropicResponse struct {
	ID               string             `json:"id"`
	Type             string             `json:"type"`
	Role             string             `json:"role"`
	Content          []AnthropicContent `json:"content"`
	Model            string             `json:"model"`
	StopReason       string             `json:"stop_reason"`
	StopSequence     *string            `json:"stop_sequence,omitempty"`
	Usage            AnthropicUsage     `json:"usage"`
	KvTransferParams map[string]any     `json:"kv_transfer_params,omitempty"`
}

// AnthropicUsageBody wraps the usage field of a streaming response.
type AnthropicUsageBody struct {
	Usage *AnthropicUsage `json:"usage"`
}

type AnthropicUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// AnthropicError is the error response body.
type AnthropicError struct {
	Type  string               `json:"type"`
	Error AnthropicErrorDetail `json:"error"`
}

type AnthropicErrorDetail struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// ============ Streaming response structures ============

// AnthropicStreamEvent is a single streaming event.
type AnthropicStreamEvent struct {
	Type         string                 `json:"type"`
	Message      *AnthropicResponse     `json:"message,omitempty"`
	ContentBlock *AnthropicContentBlock `json:"content_block,omitempty"`
	Delta        *AnthropicDelta        `json:"delta,omitempty"`
	Usage        *AnthropicUsage        `json:"usage,omitempty"`
	Index        int                    `json:"index,omitempty"`
}

// AnthropicContentBlock is a content block in a streaming response.
type AnthropicContentBlock struct {
	Type      string         `json:"type"`
	Text      string         `json:"text,omitempty"`
	Thinking  string         `json:"thinking,omitempty"`
	Signature string         `json:"signature,omitempty"`
	ID        string         `json:"id,omitempty"`
	Name      string         `json:"name,omitempty"`
	Input     map[string]any `json:"input,omitempty"`
}

// AnthropicDelta is the delta payload of a streaming event.
type AnthropicDelta struct {
	Type         string `json:"type,omitempty"`
	Text         string `json:"text,omitempty"`
	Thinking     string `json:"thinking,omitempty"`
	Signature    string `json:"signature,omitempty"`
	PartialJSON  string `json:"partial_json,omitempty"`
	StopReason   string `json:"stop_reason,omitempty"`
	StopSequence string `json:"stop_sequence,omitempty"`
}
