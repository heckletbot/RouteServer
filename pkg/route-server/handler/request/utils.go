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
	"math"
	"math/rand/v2"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"route-server/cmd/route_server/server/options"
	"route-server/pkg/common/anthropic"
	"route-server/pkg/common/constant"
	"route-server/pkg/common/logger"
	"route-server/pkg/common/openai"
	"route-server/pkg/common/utils"
)

// We require stream_options.include_usage = true for token count
func PrepareStreamOptions(reqBody *utils.JSONModifier) {
	options, exist := reqBody.GetObject(openai.StreamOptionsKey)
	if !exist {
		reqBody.Set(openai.StreamOptionsKey, map[string]interface{}{
			openai.IncludeUsageKey: true,
		})
	} else {
		filteredOptions := make(map[string]interface{})
		if val, exists := options[openai.ContinuousUsageStatsKey]; exists {
			filteredOptions[openai.ContinuousUsageStatsKey], _ = utils.TryParseBool(val)
		}
		filteredOptions[openai.IncludeUsageKey] = true
		reqBody.Set(openai.StreamOptionsKey, filteredOptions)
	}
}

func parseMaxTokens(raw *utils.JSONModifier, isChat bool) (int32, error) {
	if isChat {
		if !raw.IsInt(openai.MaxTokenKey) {
			return 0, fmt.Errorf("max_tokens must be an integer")
		}
		if !raw.IsInt(openai.MaxCompletionTokenKey) {
			return 0, fmt.Errorf("max_completion_tokens must be an integer")
		}
		if maxCompletionTokens, exists := raw.GetInt(openai.MaxCompletionTokenKey); exists {
			if maxCompletionTokens < 1 {
				return 0, fmt.Errorf("max_completion_tokens must be at least 1, got %d", maxCompletionTokens)
			}
			if maxCompletionTokens > math.MaxInt32 {
				return 0, fmt.Errorf("max_completion_tokens must not exceed %d, got %d", math.MaxInt32, maxCompletionTokens)
			}
			return int32(maxCompletionTokens), nil
		}
		if maxTokens, exists := raw.GetInt(openai.MaxTokenKey); exists {
			if maxTokens < 1 {
				return 0, fmt.Errorf("max_tokens must be at least 1, got %d", maxTokens)
			}
			if maxTokens > math.MaxInt32 {
				return 0, fmt.Errorf("max_tokens must not exceed %d, got %d", math.MaxInt32, maxTokens)
			}
			return int32(maxTokens), nil
		}
		return 0, nil
	}
	if !raw.IsInt(openai.MaxTokenKey) {
		_, exists := raw.GetRaw(openai.MaxTokenKey)
		if exists {
			return 0, fmt.Errorf("max_tokens must be an integer")
		}
	}
	if maxTokens, exists := raw.GetInt(openai.MaxTokenKey); exists {
		if maxTokens < 1 {
			return 0, fmt.Errorf("max_tokens must be at least 1, got %d", maxTokens)
		}
		if maxTokens > math.MaxInt32 {
			return 0, fmt.Errorf("max_tokens must not exceed %d, got %d", math.MaxInt32, maxTokens)
		}
		return int32(maxTokens), nil
	}
	return 0, nil
}

func GetInferRequestId(header *http.Header, isChat bool) string {
	requestId := header.Get(constant.XRequestId)
	if isChat {
		requestId = ChatReqIdPrefix + requestId
	} else {
		requestId = CompletionReqIdPrefix + requestId
	}
	return requestId
}

func TryParseContentAndLen(
	body *utils.JSONModifier, reqId string, isChat bool, isMessages bool) ([]utils.ChatContent, int32) {
	var parseFunc func(body *utils.JSONModifier) ([]utils.ChatContent, int32, bool, interface{})
	switch {
	case isMessages:
		parseFunc = tryParseMessages
	case isChat:
		parseFunc = tryParseChat
	default:
		parseFunc = tryParseCompletion
	}
	content, promptLen, rawExists, rawContent := parseFunc(body)

	if content != nil {
		return content, promptLen
	}

	var strLenErr error
	if rawExists {
		var marshal []byte
		if marshal, strLenErr = utils.SonicApi.Marshal(rawContent); strLenErr == nil {
			contentStr := string(marshal)
			logger.Log().Debugf(
				"req [%s]: try use json str len [%v] as prompt len", reqId, len(contentStr))
			return []utils.ChatContent{{Content: contentStr}}, int32(len(contentStr))
		} else {
			logger.Log().Warnf(
				"req [%s]: failed to get req prompt length when tries to marshal prompt field val, "+
					"err = %v, use 0 as prompt len", reqId, strLenErr)
		}
	}

	logger.Log().Warnf("req [%s]: request don't contains standard prompt val, request len is set to 0",
		reqId)
	return make([]utils.ChatContent, 0), 0
}

func tryParseChat(body *utils.JSONModifier) ([]utils.ChatContent, int32, bool, interface{}) {
	contentArr, exist := body.GetArray(openai.ChatMessagesKey)
	if exist {
		messages := make([]*openai.ChatCompletionMessage, 0)
		parsedContent := make([]utils.ChatContent, 0)
		var promptLen int32 = 0

		if parseErr := utils.ReparseArray(contentArr, &messages); parseErr == nil {
			for _, v := range messages {
				parsedContent = append(parsedContent, utils.ChatContent{
					Role:    v.Role,
					Content: v.Content,
				})
				promptLen += int32(len(v.Content))
			}
			return parsedContent, promptLen, true, nil
		}
	}
	val, valExists := body.GetRaw(openai.ChatMessagesKey)
	return nil, 0, valExists, val
}

func tryParseCompletion(body *utils.JSONModifier) ([]utils.ChatContent, int32, bool, interface{}) {
	if prompt, exists := body.GetString(openai.PromptKey); exists {
		return []utils.ChatContent{{
			Content: prompt,
		}}, int32(len(prompt)), true, nil
	}
	val, valExists := body.GetRaw(openai.PromptKey)
	return nil, 0, valExists, val
}

// tryParseMessages parses an Anthropic messages request body: an optional
// "system" field plus a "messages" array whose content items may be a plain
// string or an array of content blocks.
func tryParseMessages(body *utils.JSONModifier) ([]utils.ChatContent, int32, bool, interface{}) {
	messagesRaw, exist := body.GetRaw(anthropic.ChatMessagesKey)
	if !exist {
		return nil, 0, false, nil
	}

	messagesArr, ok := messagesRaw.([]interface{})
	if !ok {
		return nil, 0, exist, messagesRaw
	}

	var result []utils.ChatContent
	var totalLen int32

	// handle optional system field
	if systemRaw, ok := body.GetRaw(anthropic.SystemKey); ok && systemRaw != nil {
		var systemStr string
		switch v := systemRaw.(type) {
		case string:
			systemStr = v
		default:
			jsonBytes, err := utils.SonicApi.Marshal(systemRaw)
			if err != nil {
				logger.Log().Warnf("failed to marshal system raw content: %v", err)
				break
			}
			systemStr = string(jsonBytes)
		}

		result = append(result, utils.ChatContent{Role: anthropic.SystemKey, Content: systemStr})
		totalLen += int32(len(systemStr))
	}

	for _, item := range messagesArr {
		msg, ok := item.(map[string]interface{})
		if !ok {
			continue
		}

		role := fmt.Sprintf("%v", msg["role"])
		if role == "<nil>" {
			role = ""
		}
		content := parseContent(msg["content"])

		result = append(result, utils.ChatContent{Role: role, Content: content})
		totalLen += int32(len(content))
	}

	if len(result) == 0 {
		return nil, 0, true, nil
	}
	return result, totalLen, true, nil
}

// parseContent extracts the content of an Anthropic message item, supporting
// both a plain string and an array of content blocks.
func parseContent(raw interface{}) string {
	if raw == nil {
		return ""
	}
	if str, ok := raw.(string); ok {
		return str
	}
	jsonBytes, err := utils.SonicApi.Marshal(raw)
	if err != nil {
		logger.Log().Warnf("failed to marshal content: %v", err)
		return fmt.Sprintf("%v", raw)
	}
	return string(jsonBytes)
}

// GetOrGenerateRequestId If ReWriteRequestID switch is enabled, determine the rewritten requestid according to the following rules:
// 1. If X-Request-Id specifies a value, ignore the request_id field in the body, when forwarding downstream, add suffix to X-Request-Id
// 2. If X-Request-Id does not specify a value, but request_id in body specifies a value, use body.request_id as the original reqId. Then when forwarding downstream, X-Request-Id is set to body.request_id + suffix; in response, body.request_id + suffix is replaced with body.request_id
// 3. If neither X-Request-Id nor request_id in body specifies a value, then when forwarding downstream, X-Request-Id is set to a random value
func GetOrGenerateRequestId(c *gin.Context, isChat bool, bodyReqId string) string {
	if c == nil {
		return "unknown"
	}
	httpReq := c.Request
	rewrite := options.GetConfig().ServerConfig.ReWriteRequestID
	originReqId := httpReq.Header.Get(constant.XRequestId)
	if originReqId == "" {
		if rewrite && bodyReqId != "" {
			rewriteReqId := bodyReqId + fmt.Sprintf("-%08x", rand.Uint32())
			c.Set(constant.RewriteRequestID, rewriteReqId)
			httpReq.Header.Set(constant.XRequestId, rewriteReqId)
		} else {
			httpReq.Header.Set(constant.XRequestId, fmt.Sprintf("%x", uuid.New())[0:32])
		}

	} else if rewrite {
		rewriteReqId := originReqId + fmt.Sprintf("-%08x", rand.Uint32())
		c.Set(constant.RewriteRequestID, rewriteReqId)
		httpReq.Header.Set(constant.XRequestId, rewriteReqId)
	}
	return GetInferRequestId(&httpReq.Header, isChat)
}

func RewriteRequestIDDetail(c *gin.Context) string {
	newRequestId := c.GetString(constant.RewriteRequestID)
	if newRequestId == "" {
		return ""
	}

	idx := strings.LastIndexByte(newRequestId, '-')
	if idx == -1 {
		return ""
	}

	return fmt.Sprintf("original request id [%s] will be rewrite to [%s]", newRequestId[:idx], newRequestId)
}
