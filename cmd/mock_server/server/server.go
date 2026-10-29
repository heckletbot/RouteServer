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

package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"infer-dp-scheduler/pkg/common/anthropic"
	"infer-dp-scheduler/pkg/common/constant"
	"infer-dp-scheduler/pkg/common/logger"
	"infer-dp-scheduler/pkg/common/openai"
	"infer-dp-scheduler/pkg/common/utils"
)

const (
	Token = "test"
	Model = "deepseek-r1"

	LinkDownVersionOmni       = "omni"
	LinkDownVersionVllmAscend = "vllm_ascend"
	LinkDownVersionVllmNew    = "vllm_new"

	ConnErrModeConnReset = "connection_reset"
	ConnErrModeEof       = "eof"
)

type MMChatCompletionRequest struct {
	Model               string                               `json:"model"`
	Messages            []openai.MMChatCompletionMessage     `json:"messages"`
	MaxTokens           int                                  `json:"max_tokens,omitempty"`
	MaxCompletionTokens int                                  `json:"max_completion_tokens,omitempty"`
	Temperature         float32                              `json:"temperature,omitempty"`
	TopP                float32                              `json:"top_p,omitempty"`
	N                   int                                  `json:"n,omitempty"`
	Stream              bool                                 `json:"stream,omitempty"`
	Stop                []string                             `json:"stop,omitempty"`
	PresencePenalty     float32                              `json:"presence_penalty,omitempty"`
	ResponseFormat      *openai.ChatCompletionResponseFormat `json:"response_format,omitempty"`
	Seed                *int                                 `json:"seed,omitempty"`
	FrequencyPenalty    float32                              `json:"frequency_penalty,omitempty"`
	LogitBias           map[string]int                       `json:"logit_bias,omitempty"`
	LogProbs            bool                                 `json:"logprobs,omitempty"`
	TopLogProbs         int                                  `json:"top_logprobs,omitempty"`
	User                string                               `json:"user,omitempty"`
	Functions           []openai.FunctionDefinition          `json:"functions,omitempty"`
	FunctionCall        any                                  `json:"function_call,omitempty"`
	Tools               []openai.Tool                        `json:"tools,omitempty"`
	ToolChoice          any                                  `json:"tool_choice,omitempty"`
	StreamOptions       *openai.StreamOptions                `json:"stream_options,omitempty"`
	ParallelToolCalls   any                                  `json:"parallel_tool_calls,omitempty"`
	Store               bool                                 `json:"store,omitempty"`
	ReasoningEffort     string                               `json:"reasoning_effort,omitempty"`
	Metadata            map[string]string                    `json:"metadata,omitempty"`
	Prediction          *openai.Prediction                   `json:"prediction,omitempty"`
	ChatTemplateKwargs  map[string]any                       `json:"chat_template_kwargs,omitempty"`
	KvTransferParams    map[string]any                       `json:"kv_transfer_params,omitempty"`
	IgnoreEos           bool                                 `json:"ignore_eos"`
}

type InferPara struct {
	TTFT                     uint
	TPOT                     uint
	TokenSize                int
	Addr                     string
	Port                     string
	Collector                *Collector
	IsError                  bool
	ErrorCode                int
	IsHang                   bool
	IsHealth                 bool
	IsLinkDown               bool
	LinkDownList             map[string]struct{}
	LinkDownVersion          string
	LinkDownTokenBeforeError int
	LinkDownSleepSeconds     int
	IsEncoder                bool
	IsHybrid                 bool
	HangRequestIds           map[string]struct{}
	HangRequestIdsMu         sync.Mutex
	ConnErrConfig            *MockConnErrBody
	ConnErrMu                sync.Mutex
}

type StreamError struct {
	StreamErrorBody *StreamErrorBody `json:"error,omitempty"`
}

type StreamErrorBody struct {
	Object  string  `json:"object"`
	Message string  `json:"message"`
	Type    string  `json:"type"`
	Param   *string `json:"param,omitempty"`
	Code    int     `json:"code"`
}

type UnhealthBody struct {
	FaultMessage string `json:"fault-message"`
}

type Data struct {
	Id     string `json:"id"`
	Object string `json:"object"`
	Owned  string `json:"owned_by"`
}

type ModelNameBody struct {
	Data []*Data `json:"data"`
}

type KVMetaData struct {
	Address string `json:"address"`
	Port    string `json:"port"`
}

type LinkDownBody struct {
	MetaDataList     []KVMetaData `json:"meta_data_list"`
	Version          string       `json:"version"`
	TokenBeforeError int          `json:"token_before_error"`
	SleepSeconds     int          `json:"sleep_seconds"`
}

type MockErrorBody struct {
	RetCode int `json:"code"`
}

func (i *InferPara) ChatCompletionsInfer(ctx *gin.Context) {
	if i.IsEncoder {
		i.dealMMChatCompletion(ctx)
	} else {
		i.dealChatCompletion(ctx)
	}
}

// handleMetaserverRequest sends a layerwise metaserver request if configured in kvTransferParams.
// Returns true when an error response has already been written to ctx, so the caller should return.
func (i *InferPara) handleMetaserverRequest(ctx *gin.Context, kvTransferParams map[string]any) bool {
	if kvTransferParams == nil {
		return false
	}
	metaserverURL, exists := kvTransferParams["metaserver"]
	if !exists {
		return false
	}
	urlStr, ok := metaserverURL.(string)
	if !ok || urlStr == "" {
		return false
	}
	fmt.Printf("Detected metaserver URL: %s, sending request...\n", urlStr)
	// Send request to metaserver and wait for response
	if err := i.sendMetaserverRequest(urlStr, kvTransferParams, false); err != nil {
		fmt.Printf("Failed to send metaserver request: %v\n", err)
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("Metaserver request failed: %v", err)})
		return true
	}
	fmt.Printf("Metaserver request completed successfully\n")
	return false
}

func (i *InferPara) dealChatCompletion(ctx *gin.Context) {
	if i.injectConnErr(ctx) {
		return
	}
	bodyBytes, err := io.ReadAll(ctx.Request.Body)
	if err != nil {
		fmt.Printf("failed to parse req err = %v, context err = %v\n", err, ctx.Request.Context().Err())
		ctx.JSON(http.StatusBadRequest, err)
		return
	}
	request := &openai.ChatCompletionRequestV1{}
	if err = utils.SonicApi.Unmarshal(bodyBytes, request); err != nil {
		fmt.Printf("failed to parse req err = %v, context err = %v\n", err, ctx.Request.Context().Err())
		ctx.JSON(http.StatusBadRequest, err)
		return
	}
	var requestId = ctx.Request.Header.Get("X-Request-Id")

	// layerwise behavior
	if i.handleMetaserverRequest(ctx, request.KvTransferParams) {
		return
	}

	if !request.Stream {
		ctx.Writer.Header().Set("Content-Type", "application/json")
		if i.IsError {
			streamErr := StreamError{
				StreamErrorBody: &StreamErrorBody{
					Object: "error",
					Message: fmt.Sprintf("mock error happened in instance [%s:%s] with code %v",
						i.Addr, i.Port, i.ErrorCode),
					Type: "err",
					Code: i.ErrorCode,
				},
			}
			bytes, err := utils.SonicApi.Marshal(streamErr)
			if err != nil {
				fmt.Printf("failed to write err = %v, context err = %v\n", err, ctx.Request.Context().Err())
				ctx.JSON(http.StatusInternalServerError, err)
				return
			}
			ctx.Writer.WriteHeader(i.ErrorCode)
			_, err = ctx.Writer.Write(bytes)
			ctx.Writer.Flush()
			return
		}
		if i.shouldHang(requestId) {
			i.hangingWithRequestId(requestId)
		}

		if i.IsLinkDown {
			if i.LinkDownSleepSeconds > 0 {
				time.Sleep(time.Duration(i.LinkDownSleepSeconds) * time.Second)
			}
			switch i.LinkDownVersion {
			case LinkDownVersionVllmAscend:
				errData := map[string]any{
					"error": map[string]any{
						"message": "pd link is down!",
						"type":    "PDLinkDownError",
						"param":   nil,
						"code":    500,
					},
				}
				bytes, err := utils.SonicApi.Marshal(errData)
				if err != nil {
					fmt.Printf("failed to write err = %v, context err = %v\n", err, ctx.Request.Context().Err())
					ctx.JSON(http.StatusInternalServerError, err)
					return
				}
				ctx.Writer.WriteHeader(http.StatusInternalServerError)
				ctx.Writer.Write(bytes)
				ctx.Writer.Flush()
				return
			case LinkDownVersionVllmNew:
				errData := map[string]any{
					"error": map[string]any{
						"message": "KV cache load failed for one or more remote blocks. The request can be retried.",
						"type":    "InternalServerError",
						"param":   nil,
						"code":    500,
					},
				}
				bytes, err := utils.SonicApi.Marshal(errData)
				if err != nil {
					fmt.Printf("failed to write err = %v, context err = %v\n", err, ctx.Request.Context().Err())
					ctx.JSON(http.StatusInternalServerError, err)
					return
				}
				ctx.Writer.WriteHeader(http.StatusInternalServerError)
				ctx.Writer.Write(bytes)
				ctx.Writer.Flush()
				return
			default:
				response := openai.ChatCompletionResponse{
					ID:      requestId,
					Object:  "chat.completion",
					Created: 1,
					Model:   Model,
					Choices: []openai.ChatCompletionChoice{
						{
							Index: 0,
							Message: openai.ChatCompletionMessage{
								Role: "assistant",
							},
							FinishReason: "abort",
							StopReason:   "Abort for KV loading failure",
						},
					},
					KvTransferParams: map[string]any{
						"address": i.Addr,
						"port":    i.Port,
					},
					Usage: openai.Usage{
						PromptTokens:            2000,
						CompletionTokens:        request.MaxTokens,
						TotalTokens:             2000 + request.MaxTokens,
						PromptTokensDetails:     &openai.PromptTokensDetails{CachedTokens: 1000},
						CompletionTokensDetails: nil,
					},
				}
				ctx.JSON(http.StatusOK, response)
				return
			}
		}

		response := openai.ChatCompletionResponse{
			ID:      ctx.Request.Header.Get("X-Request-Id"),
			Object:  "test",
			Created: 1,
			Model:   Model,
			Choices: []openai.ChatCompletionChoice{
				{
					Index: 0,
					Message: openai.ChatCompletionMessage{
						Content: Token,
					},
				},
			},
			KvTransferParams: map[string]any{
				"address": i.Addr,
				"port":    i.Port,
			},
			Usage: openai.Usage{
				PromptTokens:            2000,
				CompletionTokens:        request.MaxTokens,
				TotalTokens:             2000 + request.MaxTokens,
				PromptTokensDetails:     &openai.PromptTokensDetails{CachedTokens: 1000},
				CompletionTokensDetails: nil,
			},
		}
		bytes, err := utils.SonicApi.Marshal(response)
		if err != nil {
			fmt.Printf("failed to write err = %v, context err = %v\n", err, ctx.Request.Context().Err())
			ctx.JSON(http.StatusInternalServerError, err)
			return
		}
		time.Sleep(time.Duration(i.TTFT) * time.Millisecond)
		i.Collector.TimeToFirstTime.WithLabelValues(Model).Observe(float64(i.TTFT) / 1000.0)

		ctx.Header("X-Request-Id", ctx.Request.Header.Get("X-Request-Id"))
		ctx.Header(constant.HeaderContentLength, strconv.Itoa(len(bytes)))
		ctx.Writer.WriteHeader(http.StatusOK)
		ctx.Writer.Write(bytes)
		ctx.Writer.Flush()
		return
	}

	ctx.Header("Content-Type", "text/event-stream")
	ctx.Header("Cache-Control", "no-cache")
	ctx.Header("Transfer-Encoding", "chunked")
	ctx.Header("X-Request-Id", ctx.Request.Header.Get("X-Request-Id"))
	ctx.Writer.WriteHeader(http.StatusOK)

	maxTokens := request.MaxTokens
	if maxTokens == 0 {
		maxTokens = 4
	}
	usage := &openai.Usage{
		PromptTokens:            2000,
		CompletionTokens:        maxTokens,
		TotalTokens:             2000 + maxTokens,
		PromptTokensDetails:     &openai.PromptTokensDetails{CachedTokens: 1000},
		CompletionTokensDetails: nil,
	}
	kvTransferMetaData := fmt.Sprintf("%s:%s", request.KvTransferParams["address"], request.KvTransferParams["port"])
	i.writeRes(ctx, maxTokens, i.getChatResToken, usage, kvTransferMetaData, requestId, request.MaxTokens > 0, len(request.Tools) > 0)
}

func (i *InferPara) dealMMChatCompletion(ctx *gin.Context) {
	if i.injectConnErr(ctx) {
		return
	}
	bodyBytes, err := io.ReadAll(ctx.Request.Body)
	if err != nil {
		fmt.Printf("failed to parse req err = %v, context err = %v\n", err, ctx.Request.Context().Err())
		ctx.JSON(http.StatusBadRequest, err)
		return
	}
	request := &MMChatCompletionRequest{}
	if err = utils.SonicApi.Unmarshal(bodyBytes, request); err != nil {
		fmt.Printf("failed to parse req err = %v, context err = %v\n", err, ctx.Request.Context().Err())
		ctx.JSON(http.StatusBadRequest, err)
		return
	}

	requestId := ctx.Request.Header.Get("X-Request-Id")

	// layerwise behavior
	if i.handleMetaserverRequest(ctx, request.KvTransferParams) {
		return
	}

	if !request.Stream {
		ctx.Writer.Header().Set("Content-Type", "application/json")

		if i.IsError == true {
			streamErr := StreamError{
				StreamErrorBody: &StreamErrorBody{
					Object:  "error",
					Message: fmt.Sprintf("mock error happened in instance [%s:%s], with code %v", i.Addr, i.Port, i.ErrorCode),
					Type:    "err",
					Code:    i.ErrorCode,
				},
			}
			bytes, err := utils.SonicApi.Marshal(streamErr)
			if err != nil {
				fmt.Printf("failed to write err = %v, context err = %v\n", err, ctx.Request.Context().Err())
				ctx.JSON(http.StatusInternalServerError, err)
				return
			}
			ctx.Writer.WriteHeader(400)
			_, err = ctx.Writer.Write(bytes)
			ctx.Writer.Flush()
			return
		}
		if i.shouldHang(requestId) {
			i.hangingWithRequestId(requestId)
		}

		if i.IsLinkDown {
			if i.LinkDownSleepSeconds > 0 {
				time.Sleep(time.Duration(i.LinkDownSleepSeconds) * time.Second)
			}
			switch i.LinkDownVersion {
			case LinkDownVersionVllmAscend:
				errData := map[string]any{
					"error": map[string]any{
						"message": "pd link is down!",
						"type":    "PDLinkDownError",
						"param":   nil,
						"code":    500,
					},
				}
				bytes, err := utils.SonicApi.Marshal(errData)
				if err != nil {
					fmt.Printf("failed to write err = %v, context err = %v\n", err, ctx.Request.Context().Err())
					ctx.JSON(http.StatusInternalServerError, err)
					return
				}
				ctx.Writer.WriteHeader(http.StatusInternalServerError)
				ctx.Writer.Write(bytes)
				ctx.Writer.Flush()
				return
			case LinkDownVersionVllmNew:
				errData := map[string]any{
					"error": map[string]any{
						"message": "KV cache load failed for one or more remote blocks. The request can be retried.",
						"type":    "InternalServerError",
						"param":   nil,
						"code":    500,
					},
				}
				bytes, err := utils.SonicApi.Marshal(errData)
				if err != nil {
					fmt.Printf("failed to write err = %v, context err = %v\n", err, ctx.Request.Context().Err())
					ctx.JSON(http.StatusInternalServerError, err)
					return
				}
				ctx.Writer.WriteHeader(http.StatusInternalServerError)
				ctx.Writer.Write(bytes)
				ctx.Writer.Flush()
				return
			default:
				response := openai.ChatCompletionResponse{
					ID:      requestId,
					Object:  "chat.completion",
					Created: 1,
					Model:   Model,
					Choices: []openai.ChatCompletionChoice{
						{
							Index: 0,
							Message: openai.ChatCompletionMessage{
								Role: "assistant",
							},
							FinishReason: "abort",
							StopReason:   "Abort for KV loading failure",
						},
					},
					KvTransferParams: map[string]any{
						"address": i.Addr,
						"port":    i.Port,
					},
					Usage: openai.Usage{
						PromptTokens:            2000,
						CompletionTokens:        request.MaxTokens,
						TotalTokens:             2000 + request.MaxTokens,
						PromptTokensDetails:     &openai.PromptTokensDetails{CachedTokens: 1000},
						CompletionTokensDetails: nil,
					},
				}
				ctx.JSON(http.StatusOK, response)
				return
			}
		}

		response := openai.ChatCompletionResponse{
			ID:      "test",
			Object:  "test",
			Created: 1,
			Model:   Model,
			Choices: []openai.ChatCompletionChoice{
				{
					Index: 0,
					Message: openai.ChatCompletionMessage{
						Content: Token,
					},
				},
			},
			Usage: openai.Usage{
				PromptTokens:            2000,
				CompletionTokens:        request.MaxTokens,
				TotalTokens:             2000 + request.MaxTokens,
				PromptTokensDetails:     nil,
				CompletionTokensDetails: nil,
			},
			EcTransferParams: map[string]any{
				"address": i.Addr,
				"port":    i.Port,
			},
			KvTransferParams: map[string]any{
				"address": i.Addr,
				"port":    i.Port,
			},
		}
		bytes, err := utils.SonicApi.Marshal(response)
		if err != nil {
			fmt.Printf("failed to write err = %v, context err = %v\n", err, ctx.Request.Context().Err())
			ctx.JSON(http.StatusInternalServerError, err)
			return
		}
		time.Sleep(time.Duration(i.TTFT) * time.Millisecond)
		i.Collector.TimeToFirstTime.WithLabelValues(Model).Observe(float64(i.TTFT) / 1000.0)
		ctx.Writer.WriteHeader(http.StatusOK)
		ctx.Writer.Write(bytes)
		ctx.Writer.Flush()
		return
	}

	maxTokens := request.MaxTokens
	if maxTokens == 0 {
		maxTokens = 4
	}
	ctx.Header("Content-Type", "text/event-stream")
	ctx.Header("Transfer-Encoding", "chunked")
	ctx.Writer.WriteHeader(http.StatusOK)

	usage := &openai.Usage{
		PromptTokens:            2000,
		CompletionTokens:        maxTokens,
		TotalTokens:             2000 + maxTokens,
		PromptTokensDetails:     nil,
		CompletionTokensDetails: nil,
	}
	kvTransferMetaData := fmt.Sprintf("%s:%s", request.KvTransferParams["address"], request.KvTransferParams["port"])
	i.writeRes(ctx, maxTokens, i.getChatResToken, usage, kvTransferMetaData, requestId, request.MaxTokens > 0, len(request.Tools) > 0)
}

func (i *InferPara) getChatResToken(reqID string, tokenIdx int, usage *openai.Usage, isLastToken bool, hasMaxTokens bool, hasTools bool) ([]byte, error) {
	var finishReason *openai.FinishReason
	if isLastToken {
		if hasTools {
			reason := openai.FinishReasonToolCalls
			finishReason = &reason
		} else {
			reason := openai.FinishReasonStop
			if hasMaxTokens {
				reason = openai.FinishReasonLength
			}
			finishReason = &reason
		}
	}

	var delta openai.ChatCompletionStreamChoiceDelta
	if isLastToken && hasTools {
		delta = openai.ChatCompletionStreamChoiceDelta{
			ToolCalls: []openai.ToolCall{
				{
					ID:       fmt.Sprintf("call_%s_%d", reqID, tokenIdx),
					Type:     "function",
					Function: openai.FunctionCall{Name: "get_current_time", Arguments: "{}"},
				},
			},
		}
	} else {
		delta = openai.ChatCompletionStreamChoiceDelta{
			Content: i.tokenContent(tokenIdx),
		}
	}

	var response openai.ChatCompletionStreamResponse
	if usage == nil {
		response = openai.ChatCompletionStreamResponse{
			ID:      "chatcmpl-" + reqID,
			Object:  "test",
			Created: 1,
			Model:   Model,
			Choices: []openai.ChatCompletionStreamChoice{
				{
					Index:        0,
					Delta:        delta,
					FinishReason: finishReason,
				},
			},
			Usage: usage,
		}
	} else {
		response = openai.ChatCompletionStreamResponse{
			ID:      "chatcmpl-" + reqID,
			Object:  "test",
			Created: 1,
			Model:   Model,
			Choices: []openai.ChatCompletionStreamChoice{},
			Usage:   usage,
			KvTransferParams: map[string]any{
				"address": i.Addr,
				"port":    i.Port,
			},
		}
	}

	bytes, err := utils.SonicApi.Marshal(response)
	if err != nil {
		return nil, err
	}
	responseBytes := []byte("data: ")
	responseBytes = append(responseBytes, bytes...)
	responseBytes = append(responseBytes, '\n')
	responseBytes = append(responseBytes, '\n')
	return responseBytes, nil
}

func (i *InferPara) tokenContent(tokenIdx int) string {
	if i.TokenSize > 0 {
		return strings.Repeat("a", i.TokenSize)
	}
	return fmt.Sprintf("%s_%v", Token, tokenIdx)
}

func buildSSEEvent(event string, data interface{}) ([]byte, error) {
	var dataBytes []byte
	if data != nil {
		var err error
		dataBytes, err = utils.SonicApi.Marshal(data)
		if err != nil {
			return nil, err
		}
	}

	var b []byte
	b = append(b, []byte("event: ")...)
	b = append(b, []byte(event)...)
	b = append(b, '\n')
	b = append(b, []byte("data: ")...)
	if len(dataBytes) > 0 {
		b = append(b, dataBytes...)
	}
	b = append(b, '\n')
	b = append(b, '\n')
	return b, nil
}

func (i *InferPara) getMessageResToken(reqID string, tokenIdx int, usage *openai.Usage, isLastToken bool, hasMaxTokens bool, hasTools bool) ([]byte, error) {
	var responseBytes []byte
	// 1. First token - message_start + content_block_start
	if tokenIdx == 0 {
		msgStart := map[string]interface{}{
			"type": "message_start",
			"message": map[string]interface{}{
				"id":    "msg_" + reqID,
				"model": Model,
				"usage": map[string]int{
					"input_tokens":  25,
					"output_tokens": 0,
				},
			},
		}
		bytes, err := buildSSEEvent("message_start", msgStart)
		if err != nil {
			return nil, err
		}
		responseBytes = append(responseBytes, bytes...)

		blockStart := map[string]interface{}{
			"type":  "content_block_start",
			"index": 0,
			"content_block": map[string]string{
				"type": "text",
				"text": "",
			},
		}
		bytes, err = buildSSEEvent("content_block_start", blockStart)
		if err != nil {
			return nil, err
		}
		responseBytes = append(responseBytes, bytes...)

		return responseBytes, nil
	}

	// 2. Last token
	if isLastToken {
		textDelta := map[string]interface{}{
			"type":  "content_block_delta",
			"index": 0,
			"delta": map[string]string{
				"type": "text_delta",
				"text": i.tokenContent(tokenIdx),
			},
		}
		bytes, err := buildSSEEvent("content_block_delta", textDelta)
		if err != nil {
			return nil, err
		}
		responseBytes = append(responseBytes, bytes...)

		blockStop := map[string]interface{}{
			"type":  "content_block_stop",
			"index": 0,
		}
		bytes, err = buildSSEEvent("content_block_stop", blockStop)
		if err != nil {
			return nil, err
		}
		responseBytes = append(responseBytes, bytes...)

		stopReason := "end_turn"
		if hasMaxTokens {
			stopReason = "max_tokens"
		}
		if hasTools {
			stopReason = "tool_use"
		}
		msgDelta := map[string]interface{}{
			"type": "message_delta",
			"delta": map[string]string{
				"stop_reason": stopReason,
			},
			"usage": map[string]int{
				"input_tokens":  25,
				"output_tokens": tokenIdx + 1,
			},
		}
		bytes, err = buildSSEEvent("message_delta", msgDelta)
		if err != nil {
			return nil, err
		}
		responseBytes = append(responseBytes, bytes...)

		msgStop := map[string]string{
			"type": "message_stop",
		}
		bytes, err = buildSSEEvent("message_stop", msgStop)
		if err != nil {
			return nil, err
		}
		responseBytes = append(responseBytes, bytes...)

		return responseBytes, nil
	}

	// 3. Middle token - plain text delta
	textDelta := map[string]interface{}{
		"type":  "content_block_delta",
		"index": 0,
		"delta": map[string]string{
			"type": "text_delta",
			"text": i.tokenContent(tokenIdx),
		},
	}
	return buildSSEEvent("content_block_delta", textDelta)
}

func (i *InferPara) getCompletionResToken(reqID string, tokenIdx int, usage *openai.Usage, isLastToken bool, hasMaxTokens bool, hasTools bool) ([]byte, error) {
	var finishReason string
	if isLastToken {
		if hasMaxTokens {
			finishReason = "length"
		} else {
			finishReason = "stop"
		}
	}

	var response openai.CompletionResponse
	if usage == nil {
		response = openai.CompletionResponse{
			ID:      "cmpl-" + reqID,
			Object:  "test",
			Created: 1,
			Model:   Model,
			Choices: []openai.CompletionChoice{
				{
					Index:        0,
					Text:         i.tokenContent(tokenIdx),
					FinishReason: finishReason,
				},
			},
			Usage: usage,
		}
	} else {
		response = openai.CompletionResponse{
			ID:      "cmpl-" + reqID,
			Object:  "test",
			Created: 1,
			Model:   Model,
			Choices: []openai.CompletionChoice{
				{
					Index:        0,
					Text:         i.tokenContent(tokenIdx),
					FinishReason: finishReason,
				},
			},
			Usage: usage,
		}
	}

	bytes, err := utils.SonicApi.Marshal(response)
	if err != nil {
		return nil, err
	}
	responseBytes := []byte("data: ")
	responseBytes = append(responseBytes, bytes...)
	responseBytes = append(responseBytes, '\n')
	responseBytes = append(responseBytes, '\n')
	return responseBytes, nil
}

func (i *InferPara) CompletionsInfer(ctx *gin.Context) {
	if i.injectConnErr(ctx) {
		return
	}
	bodyBytes, err := io.ReadAll(ctx.Request.Body)
	if err != nil {
		fmt.Printf("failed to parse req err = %v, context err = %v\n", err, ctx.Request.Context().Err())
		ctx.JSON(http.StatusBadRequest, err)
		return
	}
	request := &openai.ChatCompletionRequest{}
	if err = utils.SonicApi.Unmarshal(bodyBytes, request); err != nil {
		fmt.Printf("failed to parse req err = %v, context err = %v\n", err, ctx.Request.Context().Err())
		ctx.JSON(http.StatusBadRequest, err)
		return
	}

	requestId := ctx.Request.Header.Get("X-Request-Id")

	// layerwise behavior
	if request.KvTransferParams != nil {
		if metaserverURL, exists := request.KvTransferParams["metaserver"]; exists {
			if urlStr, ok := metaserverURL.(string); ok && urlStr != "" {
				fmt.Printf("Detected metaserver URL: %s, sending request...\n", urlStr)
				// Send request to metaserver and wait for response
				if err := i.sendMetaserverRequest(urlStr, request.KvTransferParams, true); err != nil {
					fmt.Printf("Failed to send metaserver request: %v\n", err)
					ctx.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("Metaserver request failed: %v", err)})
					return
				}
				fmt.Printf("Metaserver request completed successfully\n")
			}
		}
	}

	if !request.Stream {
		ctx.Writer.Header().Set("Content-Type", "application/json")

		if i.IsError {
			streamErr := StreamError{
				StreamErrorBody: &StreamErrorBody{
					Object: "error",
					Message: fmt.Sprintf("mock error happened in instance [%s:%s], with code %v",
						i.Addr, i.Port, i.ErrorCode),
					Type: "err",
					Code: i.ErrorCode,
				},
			}
			bytes, err := utils.SonicApi.Marshal(streamErr)
			if err != nil {
				fmt.Printf("failed to write err = %v, context err = %v\n", err, ctx.Request.Context().Err())
				ctx.JSON(http.StatusInternalServerError, err)
				return
			}
			ctx.Writer.WriteHeader(i.ErrorCode)
			_, err = ctx.Writer.Write(bytes)
			ctx.Writer.Flush()
			return
		}
		if i.shouldHang(requestId) {
			i.hangingWithRequestId(requestId)
		}

		if i.IsLinkDown {
			i.writeLinkDownError(ctx, requestId)
			return
		}

		response := openai.CompletionResponse{
			ID:      "cmpl-" + requestId,
			Object:  "test",
			Created: 1,
			Model:   Model,
			Choices: []openai.CompletionChoice{
				{
					Index: 0,
					Text:  Token,
				},
			},
			// kv transfer metadata for mock pd link down
			KvTransferParams: map[string]any{
				"address": i.Addr,
				"port":    i.Port,
			},
		}
		bytes, err := utils.SonicApi.Marshal(response)
		if err != nil {
			fmt.Printf("failed to write err = %v, context err = %v\n", err, ctx.Request.Context().Err())
			ctx.JSON(http.StatusInternalServerError, err)
			return
		}
		time.Sleep(time.Duration(i.TTFT) * time.Millisecond)
		i.Collector.TimeToFirstTime.WithLabelValues(Model).Observe(float64(i.TTFT) / 1000.0)
		ctx.Writer.WriteHeader(http.StatusOK)
		ctx.Writer.Write(bytes)
		ctx.Writer.Flush()
		return
	}

	ctx.Header("Content-Type", "text/event-stream")
	ctx.Header("Transfer-Encoding", "chunked")
	ctx.Header("X-Request-Id", ctx.Request.Header.Get("X-Request-Id"))
	ctx.Writer.WriteHeader(http.StatusOK)

	usage := &openai.Usage{
		PromptTokens:            2000,
		CompletionTokens:        request.MaxTokens,
		TotalTokens:             2000 + request.MaxTokens,
		PromptTokensDetails:     nil,
		CompletionTokensDetails: nil,
	}
	kvTransferMetaData := fmt.Sprintf("%s:%s", request.KvTransferParams["address"], request.KvTransferParams["port"])
	i.writeRes(ctx, request.MaxTokens, i.getCompletionResToken, usage, kvTransferMetaData, requestId, request.MaxTokens > 0, false)
}

func (i *InferPara) writeLinkDownError(ctx *gin.Context, requestId string) {
	if i.LinkDownSleepSeconds > 0 {
		time.Sleep(time.Duration(i.LinkDownSleepSeconds) * time.Second)
	}

	var errData map[string]any
	switch i.LinkDownVersion {
	case LinkDownVersionVllmAscend:
		errData = map[string]any{
			"error": map[string]any{
				"message": "pd link is down!",
				"type":    "PDLinkDownError",
				"param":   nil,
				"code":    500,
			},
		}
	case LinkDownVersionVllmNew:
		errData = map[string]any{
			"error": map[string]any{
				"message": "KV cache load failed for one or more remote blocks. The request can be retried.",
				"type":    "InternalServerError",
				"param":   nil,
				"code":    500,
			},
		}
	default:
		response := openai.CompletionResponse{
			ID:      "cmpl-" + requestId,
			Object:  "chat.completion",
			Created: 1,
			Model:   Model,
			Choices: []openai.CompletionChoice{
				{
					Index:        0,
					Text:         "Abort for KV loading failure",
					FinishReason: "abort",
				},
			},
			KvTransferParams: map[string]any{
				"address": i.Addr,
				"port":    i.Port,
			},
		}
		ctx.JSON(http.StatusOK, response)
		return
	}

	bytes, err := utils.SonicApi.Marshal(errData)
	if err != nil {
		fmt.Printf("failed to write err = %v, context err = %v\n", err, ctx.Request.Context().Err())
		ctx.JSON(http.StatusInternalServerError, err)
		return
	}
	ctx.Writer.WriteHeader(http.StatusInternalServerError)
	ctx.Writer.Write(bytes)
	ctx.Writer.Flush()
}

// hangingWithRequestId hangs in specific request_id scenarios
func (i *InferPara) hangingWithRequestId(requestId string) {
	for i.IsHang || i.isRequestIdHanging(requestId) {
		time.Sleep(1 * time.Second)
	}
}

func (i *InferPara) sendMetaserverRequest(metaserverURL string, kvTransferParams map[string]any, isCompletion bool) error {
	// For completion requests, add "-0" suffix to request_id to match route server expectations
	requestIdVal, ok := kvTransferParams["request_id"].(string)
	if !ok {
		return fmt.Errorf("invalid request_id type")
	}
	requestId := requestIdVal
	if isCompletion {
		requestId = requestId + "-0"
	}

	reqBody := map[string]any{
		"address":    i.Addr,
		"port":       i.Port,
		"request_id": requestId,
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("failed to marshal request: %w", err)
	}

	client := &http.Client{
		Timeout: 30 * time.Second,
	}

	req, err := http.NewRequest("POST", metaserverURL, bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-Id", fmt.Sprintf("req-%d", time.Now().UnixNano()))

	fmt.Printf("Sending POST request to metaserver: %s\n body is %s\n", metaserverURL, jsonData)
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send request to metaserver: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read metaserver response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("metaserver returned non-OK status: %d, response: %s", resp.StatusCode, string(respBody))
	}

	fmt.Printf("Metaserver response: %s\n", string(respBody))
	return nil
}

func (i *InferPara) writeInitialResponse(ctx *gin.Context) error {
	initialResponse := openai.ChatCompletionStreamResponse{
		ID:      "chatcmpl-" + ctx.Request.Header.Get(constant.XRequestId),
		Object:  "test",
		Created: 1,
		Model:   Model,
		Choices: []openai.ChatCompletionStreamChoice{
			{
				Index: 0,
				Delta: openai.ChatCompletionStreamChoiceDelta{
					Role:    "assistant",
					Content: "",
				},
				Logprobs:     nil,
				FinishReason: nil,
			},
		},
	}

	bytes, err := utils.SonicApi.Marshal(initialResponse)
	if err != nil {
		fmt.Printf("failed to marshal initial response: %v\n", err)
		ctx.JSON(http.StatusInternalServerError, err)
		return err
	}

	_, err = fmt.Fprintf(ctx.Writer, "data: %s\n\n", bytes)
	if err != nil {
		fmt.Printf("failed to write initial response: %v\n", err)
		return err
	}

	ctx.Writer.Flush()
	return nil
}

func writeSSEData(ctx *gin.Context, data []byte) {
	ctx.Writer.Write([]byte("data: "))
	ctx.Writer.Write(data)
	ctx.Writer.Write([]byte("\n\n"))
}

func writeSSEDone(ctx *gin.Context) {
	ctx.Writer.Write([]byte("data: "))
	ctx.Writer.Write([]byte{'[', 'D', 'O', 'N', 'E', ']', '\n', '\n'})
	ctx.Writer.Flush()
}

func (i *InferPara) writeRes(ctx *gin.Context, maxTokens int, tokenBytes func(string, int, *openai.Usage, bool, bool, bool) ([]byte, error),
	usage *openai.Usage, kvTransferMetaData string, requestId string, hasMaxTokens bool, hasTools bool) {
	var err error

	err = i.writeInitialResponse(ctx)
	if err != nil {
		fmt.Printf("failed to write initial response: %v\n", err)
		return
	}

	for j := 0; j < maxTokens; j++ {
		if i.IsError {
			i.writeErrorStreamResponse(ctx)
			return
		}
		if i.IsLinkDown {
			if handled := i.writeLinkDownResponse(ctx, maxTokens, tokenBytes, kvTransferMetaData, requestId, hasMaxTokens, hasTools, j); handled {
				return
			}
		}
		if i.shouldHang(requestId) {
			i.hangingWithRequestId(requestId)
		}
		i.sleepAndObserveTokenLatency(j)

		isLastToken := j == maxTokens-1
		responseBytes, err := tokenBytes(ctx.Request.Header.Get("X-Request-Id"), j, nil, isLastToken, hasMaxTokens, hasTools)
		if err != nil {
			fmt.Printf("failed to write err = %v, context err = %v\n", err, ctx.Request.Context().Err())
			ctx.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		_, err = ctx.Writer.Write(responseBytes)
		if err != nil {
			fmt.Printf("failed to write err = %v, context err = %v\n", err, ctx.Request.Context().Err())
			ctx.JSON(http.StatusInternalServerError, err)
			return
		}
		ctx.Writer.Flush()
	}

	lastResponse, err := tokenBytes(ctx.Request.Header.Get("X-Request-Id"), 0, usage, true, hasMaxTokens, hasTools)
	// For streaming interfaces, when max token is unset or is 1, a single token is emitted
	// Native vLLM apparently does not support max token of 0
	if maxTokens == 0 || maxTokens == 1 {
		i.Collector.TimeToFirstTime.WithLabelValues(Model).Observe(float64(i.TTFT) / 1000.0)
		i.Collector.End2EndTime.WithLabelValues(Model).Observe(float64(i.TTFT) / 1000.0)
	} else {
		i.Collector.TimePerOutputToken.WithLabelValues(Model).Observe(float64(i.TPOT) / 1000.0)
		totalTime := i.TTFT + i.TPOT*(uint(maxTokens)-1)
		i.Collector.End2EndTime.WithLabelValues(Model).Observe(float64(totalTime) / 1000.0)
	}
	ctx.Writer.Write(lastResponse)
	writeSSEDone(ctx)
}

// writeAnthropicRes is dedicated to Anthropic-format streaming responses
func (i *InferPara) writeAnthropicRes(ctx *gin.Context, maxTokens int, tokenBytes func(string, int, *openai.Usage, bool, bool, bool) ([]byte, error),
	usage *openai.Usage, kvTransferMetaData string, requestId string, hasMaxTokens bool, hasTools bool) {
	// Loop over all tokens; the last token's complete end events are handled internally by getMessageResToken
	for j := 0; j < maxTokens; j++ {
		if i.IsError {
			i.writeAnthropicErrorStreamResponse(ctx)
			return
		}
		if i.IsLinkDown {
			if handled := i.writeLinkDownResponse(ctx, maxTokens, tokenBytes, kvTransferMetaData, requestId, hasMaxTokens, hasTools, j); handled {
				return
			}
		}
		if i.shouldHang(requestId) {
			i.hangingWithRequestId(requestId)
		}
		i.sleepAndObserveTokenLatency(j)

		isLastToken := j == maxTokens-1
		responseBytes, err := tokenBytes(ctx.Request.Header.Get("X-Request-Id"), j, nil, isLastToken, hasMaxTokens, hasTools)
		if err != nil {
			fmt.Printf("failed to write err = %v, context err = %v\n", err, ctx.Request.Context().Err())
			ctx.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		_, err = ctx.Writer.Write(responseBytes)
		if err != nil {
			fmt.Printf("failed to write err = %v, context err = %v\n", err, ctx.Request.Context().Err())
			ctx.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		ctx.Writer.Flush()
	}

	// Update monitoring metrics
	if maxTokens == 0 || maxTokens == 1 {
		i.Collector.TimeToFirstTime.WithLabelValues(Model).Observe(float64(i.TTFT) / 1000.0)
		i.Collector.End2EndTime.WithLabelValues(Model).Observe(float64(i.TTFT) / 1000.0)
	} else {
		i.Collector.TimePerOutputToken.WithLabelValues(Model).Observe(float64(i.TPOT) / 1000.0)
		totalTime := i.TTFT + i.TPOT*(uint(maxTokens)-1)
		i.Collector.End2EndTime.WithLabelValues(Model).Observe(float64(totalTime) / 1000.0)
	}
}

func (i *InferPara) writeErrorStreamResponse(ctx *gin.Context) {
	streamErr := StreamError{
		StreamErrorBody: &StreamErrorBody{
			Object: "error",
			Message: fmt.Sprintf("mock error happened in instance [%s:%s] with code %v",
				i.Addr, i.Port, i.ErrorCode),
			Type: "err",
			Code: i.ErrorCode,
		},
	}
	bytes, err := utils.SonicApi.Marshal(streamErr)
	if err != nil {
		fmt.Printf("failed to write err = %v, context err = %v\n", err, ctx.Request.Context().Err())
		ctx.JSON(http.StatusInternalServerError, err)
		return
	}
	writeSSEData(ctx, bytes)
	writeSSEDone(ctx)
}

// writeAnthropicErrorStreamResponse writes an Anthropic-format streaming error event
func (i *InferPara) writeAnthropicErrorStreamResponse(ctx *gin.Context) {
	errData := map[string]any{
		"type": "error",
		"error": map[string]any{
			"type": "internal_error",
			"message": fmt.Sprintf("mock error happened in instance [%s:%s] with code %v",
				i.Addr, i.Port, i.ErrorCode),
		},
	}
	bytes, err := buildSSEEvent("error", errData)
	if err != nil {
		fmt.Printf("failed to write err = %v, context err = %v\n", err, ctx.Request.Context().Err())
		ctx.JSON(http.StatusInternalServerError, err)
		return
	}
	_, err = ctx.Writer.Write(bytes)
	if err != nil {
		fmt.Printf("failed to write err = %v, context err = %v\n", err, ctx.Request.Context().Err())
		ctx.JSON(http.StatusInternalServerError, err)
		return
	}
	ctx.Writer.Flush()
}

func (i *InferPara) sleepAndObserveTokenLatency(j int) {
	if i.IsHybrid && j == 0 {
		time.Sleep(time.Duration(i.TTFT) * time.Millisecond)
		i.Collector.TimePerOutputToken.WithLabelValues(Model).Observe(float64(i.TTFT) / 1000.0)
	} else {
		time.Sleep(time.Duration(i.TPOT) * time.Millisecond)
		i.Collector.TimePerOutputToken.WithLabelValues(Model).Observe(float64(i.TPOT) / 1000.0)
	}
}

func (i *InferPara) writeLinkDownResponse(ctx *gin.Context, maxTokens int, tokenBytes func(string, int, *openai.Usage, bool, bool, bool) ([]byte, error),
	kvTransferMetaData string, requestId string, hasMaxTokens bool, hasTools bool, j int) (handled bool) {

	linkDownTokenBeforeError := i.LinkDownTokenBeforeError
	if linkDownTokenBeforeError > 0 && linkDownTokenBeforeError >= maxTokens {
		linkDownTokenBeforeError = maxTokens - 1
		if linkDownTokenBeforeError < 0 {
			linkDownTokenBeforeError = 0
		}
	}
	_, exist := i.LinkDownList[kvTransferMetaData]
	if len(i.LinkDownList) == 0 || exist {
		if linkDownTokenBeforeError > 0 && j < linkDownTokenBeforeError {
			i.sleepAndObserveTokenLatency(j)
			isLastToken := j == maxTokens-1
			responseBytes, err := tokenBytes(ctx.Request.Header.Get("X-Request-Id"), j, nil, isLastToken, hasMaxTokens, hasTools)
			if err != nil {
				fmt.Printf("failed to write err = %v, context err = %v\n", err, ctx.Request.Context().Err())
				ctx.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return true
			}
			_, err = ctx.Writer.Write(responseBytes)
			if err != nil {
				fmt.Printf("failed to write err = %v, context err = %v\n", err, ctx.Request.Context().Err())
				ctx.JSON(http.StatusInternalServerError, err)
				return true
			}
			ctx.Writer.Flush()
			return false
		}
		if i.LinkDownSleepSeconds > 0 {
			time.Sleep(time.Duration(i.LinkDownSleepSeconds) * time.Second)
		}
		switch i.LinkDownVersion {
		case LinkDownVersionOmni:
			i.writeLinkDownOmni(ctx, requestId)
		case LinkDownVersionVllmNew:
			i.writeLinkDownVllmNew(ctx, requestId)
		default:
			i.writeLinkDownVllmAscend(ctx)
		}
		return true
	}
	return false
}

func (i *InferPara) writeLinkDownOmni(ctx *gin.Context, requestId string) {
	omniAbortChunk := map[string]any{
		"id":      "chatcmpl-" + requestId,
		"object":  "chat.completion.chunk",
		"created": time.Now().Unix(),
		"model":   Model,
		"choices": []map[string]any{
			{
				"index": 0,
				"delta": map[string]any{
					"content":           "",
					"reasoning_content": nil,
				},
				"finish_reason": "abort",
				"stop_reason":   "Abort for KV loading failure",
				"token_ids":     nil,
			},
		},
	}
	omniErrBytes, err := utils.SonicApi.Marshal(omniAbortChunk)
	if err != nil {
		fmt.Printf("failed to write err = %v, context err = %v\n", err, ctx.Request.Context().Err())
		ctx.JSON(http.StatusInternalServerError, err)
		return
	}
	writeSSEData(ctx, omniErrBytes)

	omniUsageChunk := map[string]any{
		"id":      "chatcmpl-" + requestId,
		"object":  "chat.completion.chunk",
		"created": time.Now().Unix(),
		"model":   Model,
		"choices": []map[string]any{},
		"usage": map[string]any{
			"prompt_tokens":     6,
			"total_tokens":      6,
			"completion_tokens": 0,
		},
	}
	usageBytes, err := utils.SonicApi.Marshal(omniUsageChunk)
	if err != nil {
		fmt.Printf("failed to write err = %v, context err = %v\n", err, ctx.Request.Context().Err())
		ctx.JSON(http.StatusInternalServerError, err)
		return
	}
	writeSSEData(ctx, usageBytes)
	writeSSEDone(ctx)
}

func (i *InferPara) writeLinkDownVllmNew(ctx *gin.Context, requestId string) {
	vllmNewChunk := map[string]any{
		"id":      "chatcmpl-" + requestId,
		"object":  "chat.completion.chunk",
		"created": time.Now().Unix(),
		"model":   Model,
		"choices": []map[string]any{
			{
				"index": 0,
				"delta": map[string]any{
					"reasoning_content": nil,
				},
				"finish_reason": nil,
				"token_ids":     nil,
			},
		},
	}
	vllmNewErrBytes, err := utils.SonicApi.Marshal(vllmNewChunk)
	if err != nil {
		fmt.Printf("failed to write err = %v, context err = %v\n", err, ctx.Request.Context().Err())
		ctx.JSON(http.StatusInternalServerError, err)
		return
	}
	writeSSEData(ctx, vllmNewErrBytes)

	vllmNewErrData := map[string]any{
		"error": map[string]any{
			"message": "KV cache load failed for one or more remote blocks. The request can be retried.",
			"type":    "InternalServerError",
			"param":   nil,
			"code":    500,
		},
	}
	errBytes, err := utils.SonicApi.Marshal(vllmNewErrData)
	if err != nil {
		fmt.Printf("failed to write err = %v, context err = %v\n", err, ctx.Request.Context().Err())
		ctx.JSON(http.StatusInternalServerError, err)
		return
	}
	writeSSEData(ctx, errBytes)
	writeSSEDone(ctx)
}

func (i *InferPara) writeLinkDownVllmAscend(ctx *gin.Context) {
	vllmAscendErrData := map[string]any{
		"error": map[string]any{
			"message": "pd link is down!",
			"type":    "PDLinkDownError",
			"param":   nil,
			"code":    500,
		},
	}
	bytes, err := utils.SonicApi.Marshal(vllmAscendErrData)
	if err != nil {
		fmt.Printf("failed to write err = %v, context err = %v\n", err, ctx.Request.Context().Err())
		ctx.JSON(http.StatusInternalServerError, err)
		return
	}
	writeSSEData(ctx, bytes)
	writeSSEDone(ctx)
}

func (i *InferPara) Health(ctx *gin.Context) {
	if i.IsHealth {
		ctx.Writer.WriteHeader(http.StatusOK)
	} else {
		unHealthBody := UnhealthBody{
			FaultMessage: "npu err",
		}
		ctx.JSON(http.StatusInternalServerError, unHealthBody)
	}
}

func (i *InferPara) MockHealth(ctx *gin.Context) {
	i.IsHealth = true
	ctx.Writer.WriteHeader(http.StatusOK)
}

func (i *InferPara) MockUnHealth(ctx *gin.Context) {
	i.IsHealth = false
	ctx.Writer.WriteHeader(http.StatusOK)
}

func (i *InferPara) MockErrorInstanceHandler(ctx *gin.Context) {
	var errBody MockErrorBody
	if err := ctx.ShouldBindJSON(&errBody); err != nil {
		errBody = MockErrorBody{
			RetCode: 400,
		}
	}
	i.IsError = true
	i.ErrorCode = errBody.RetCode
	logger.Log().Infof("istance %s:%s status: no error -> error, code = %v", i.Addr, i.Port, errBody.RetCode)
	ctx.JSON(200,
		fmt.Sprintf("istance %s:%s status: no error -> error, code = %v", i.Addr, i.Port, errBody.RetCode))
}

func (i *InferPara) RemoveErrorInstanceHandler(ctx *gin.Context) {
	i.IsError = false
	logger.Log().Infof("istance %s:%s status: error -> no error ", i.Addr, i.Port)
	ctx.JSON(200, fmt.Sprintf("istance %s:%s status: error -> no error ", i.Addr, i.Port))
}

func (i *InferPara) LinkDownHandler(ctx *gin.Context) {
	var linkDownBody LinkDownBody
	if err := ctx.ShouldBindJSON(&linkDownBody); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body"})
		return
	}
	i.IsLinkDown = true
	if linkDownBody.Version != "" {
		i.LinkDownVersion = linkDownBody.Version
	}
	if linkDownBody.TokenBeforeError > 0 {
		i.LinkDownTokenBeforeError = linkDownBody.TokenBeforeError
	}
	i.LinkDownSleepSeconds = linkDownBody.SleepSeconds
	for _, metaData := range linkDownBody.MetaDataList {
		key := fmt.Sprintf("%s:%s", metaData.Address, metaData.Port)
		i.LinkDownList[key] = struct{}{}
	}
	logger.Log().Infof("instance %s:%s status: link -> linkdown with %+v, version=%s, token_before_error=%d, sleep_seconds=%d",
		i.Addr, i.Port, linkDownBody.MetaDataList, i.LinkDownVersion, i.LinkDownTokenBeforeError, i.LinkDownSleepSeconds)
	ctx.JSON(200, fmt.Sprintf("instance %s:%s status: link -> linkdown", i.Addr, i.Port))
}

func (i *InferPara) LinkUpHandler(ctx *gin.Context) {
	i.IsLinkDown = false
	i.LinkDownList = make(map[string]struct{})
	i.LinkDownVersion = ""
	i.LinkDownTokenBeforeError = 0
	i.LinkDownSleepSeconds = 0
	logger.Log().Infof("instance %s:%s status: linkdown -> link", i.Addr, i.Port)
	ctx.JSON(200, fmt.Sprintf("instance %s:%s status: linkdown -> link", i.Addr, i.Port))
}

func (i *InferPara) HangInstanceHandler(ctx *gin.Context) {
	i.IsHang = true
	logger.Log().Infof("instance %s:%s status: unhang -> hang", i.Addr, i.Port)
	ctx.JSON(200, fmt.Sprintf("instance %s:%s status: unhang -> hang", i.Addr, i.Port))
}

func (i *InferPara) UnHangErrorInstanceHandler(ctx *gin.Context) {
	i.IsHang = false
	logger.Log().Infof("instance %s:%s status: hang -> unhang ", i.Addr, i.Port)
	ctx.JSON(200, fmt.Sprintf("instance %s:%s status: hang -> unhang ", i.Addr, i.Port))
}

// HangSpecificRequestHandler hangs requests with a specific request_id
func (i *InferPara) HangSpecificRequestHandler(ctx *gin.Context) {
	requestId := ctx.Query("request_id")
	i.HangRequestIdsMu.Lock()
	i.HangRequestIds[requestId] = struct{}{}
	i.HangRequestIdsMu.Unlock()
	logger.Log().Infof("instance %s:%s: set request %s to hang", i.Addr, i.Port, requestId)
	ctx.JSON(200, fmt.Sprintf("request %s will hang", requestId))
}

// UnHangSpecificRequestHandler un-hangs requests with a specific request_id
func (i *InferPara) UnHangSpecificRequestHandler(ctx *gin.Context) {
	requestId := ctx.Query("request_id")
	i.HangRequestIdsMu.Lock()
	delete(i.HangRequestIds, requestId)
	i.HangRequestIdsMu.Unlock()
	logger.Log().Infof("instance %s:%s: remove request %s from hang", i.Addr, i.Port, requestId)
	ctx.JSON(200, fmt.Sprintf("request %s will not hang", requestId))
}

// isRequestIdHanging checks whether a specific request_id should hang
func (i *InferPara) isRequestIdHanging(requestId string) bool {
	i.HangRequestIdsMu.Lock()
	defer i.HangRequestIdsMu.Unlock()
	for hangStr := range i.HangRequestIds {
		fmt.Println(fmt.Sprintf("reqId %s, hangStr %s", requestId, hangStr))
		if strings.HasPrefix(requestId, hangStr) {
			return true
		}
	}
	return false
}

// shouldHang checks whether to hang (globally or for a specific request_id)
func (i *InferPara) shouldHang(requestId string) bool {
	if i.IsHang {
		return true
	}
	if requestId != "" && i.isRequestIdHanging(requestId) {
		return true
	}
	return false
}

func (i *InferPara) ModelNameHandler(ctx *gin.Context) {
	logger.Log().Infof("handle model name request, model name  is %s", Model)
	data := &Data{
		Id:     Model,
		Object: "model",
		Owned:  "vllm",
	}
	datas := make([]*Data, 0)
	datas = append(datas, data)
	modelNameBody := ModelNameBody{
		Data: datas,
	}
	ctx.JSON(200, modelNameBody)
}

type MockConnErrBody struct {
	Mode  string `json:"mode"`
	Count int    `json:"count"`
}

func (i *InferPara) MockConnErrHandler(ctx *gin.Context) {
	var body MockConnErrBody
	if err := ctx.ShouldBindJSON(&body); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body"})
		return
	}
	if body.Count <= 0 {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "count must be > 0"})
		return
	}
	i.ConnErrMu.Lock()
	i.ConnErrConfig = &body
	i.ConnErrMu.Unlock()
	logger.Log().Infof("instance %s:%s: set conn err mode=%s count=%d", i.Addr, i.Port, body.Mode, body.Count)
	ctx.JSON(200, fmt.Sprintf("instance %s:%s: conn err mode=%s count=%d", i.Addr, i.Port, body.Mode, body.Count))
}

func (i *InferPara) RemoveConnErrHandler(ctx *gin.Context) {
	i.ConnErrMu.Lock()
	i.ConnErrConfig = nil
	i.ConnErrMu.Unlock()
	logger.Log().Infof("instance %s:%s: removed conn err", i.Addr, i.Port)
	ctx.JSON(200, fmt.Sprintf("instance %s:%s: conn err removed", i.Addr, i.Port))
}

func (i *InferPara) shouldInjectConnErr() (bool, string) {
	i.ConnErrMu.Lock()
	defer i.ConnErrMu.Unlock()
	if i.ConnErrConfig == nil || i.ConnErrConfig.Count <= 0 {
		return false, ""
	}
	mode := i.ConnErrConfig.Mode
	i.ConnErrConfig.Count--
	if i.ConnErrConfig.Count <= 0 {
		i.ConnErrConfig = nil
	}
	return true, mode
}

func (i *InferPara) injectConnErr(ctx *gin.Context) bool {
	should, mode := i.shouldInjectConnErr()
	if !should {
		return false
	}
	hj, ok := ctx.Writer.(http.Hijacker)
	if !ok {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "cannot hijack connection"})
		return true
	}
	conn, _, err := hj.Hijack()
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "hijack failed"})
		return true
	}
	if mode == ConnErrModeConnReset {
		if tcpConn, ok := conn.(*net.TCPConn); ok {
			_ = tcpConn.SetLinger(0)
		}
	}
	conn.Close()
	return true
}

func (i *InferPara) MessageInfer(ctx *gin.Context) {
	i.dealMessagesCompletion(ctx)
}

func (i *InferPara) dealMessagesCompletion(ctx *gin.Context) {
	bodyBytes, err := io.ReadAll(ctx.Request.Body)
	if err != nil {
		fmt.Printf("failed to parse req err = %v, context err = %v\n", err, ctx.Request.Context().Err())
		ctx.JSON(http.StatusBadRequest, err)
		return
	}

	request := &anthropic.AnthropicRequest{}
	if err = utils.SonicApi.Unmarshal(bodyBytes, request); err != nil {
		fmt.Printf("failed to parse req err = %v, context err = %v\n", err, ctx.Request.Context().Err())
		ctx.JSON(http.StatusBadRequest, err)
		return
	}

	requestId := ctx.Request.Header.Get("X-Request-Id")

	// layerwise behavior - metaserver
	if i.handleMetaserverRequest(ctx, request.KvTransferParams) {
		return
	}

	if request.Stream {
		i.handleStreamResponse(ctx, request, requestId)
		return
	}

	i.handleNonStreamResponse(ctx, request, requestId)
}

// handleStreamResponse handles streaming responses
func (i *InferPara) handleStreamResponse(ctx *gin.Context, request *anthropic.AnthropicRequest, requestId string) {
	ctx.Header("Content-Type", "text/event-stream")
	ctx.Header("Cache-Control", "no-cache")
	ctx.Header("Transfer-Encoding", "chunked")
	ctx.Header("X-Request-Id", ctx.Request.Header.Get("X-Request-Id"))
	ctx.Writer.WriteHeader(http.StatusOK)

	maxTokens := request.MaxTokens
	if maxTokens == 0 {
		maxTokens = 4
	}

	usage := &openai.Usage{
		PromptTokens:            2000,
		CompletionTokens:        maxTokens,
		TotalTokens:             2000 + maxTokens,
		PromptTokensDetails:     &openai.PromptTokensDetails{CachedTokens: 1000},
		CompletionTokensDetails: nil,
	}

	kvTransferMetaData := fmt.Sprintf("%s:%s", request.KvTransferParams["address"], request.KvTransferParams["port"])
	i.writeAnthropicRes(ctx, maxTokens, i.getMessageResToken, usage, kvTransferMetaData, requestId, request.MaxTokens > 0, len(request.Tools) > 0)
}

// handleNonStreamResponse handles non-streaming responses
func (i *InferPara) handleNonStreamResponse(ctx *gin.Context, request *anthropic.AnthropicRequest, requestId string) {
	ctx.Writer.Header().Set("Content-Type", "application/json")

	if i.IsError {
		i.writeNonStreamErrorResponse(ctx)
		return
	}

	if i.shouldHang(requestId) {
		i.hangingWithRequestId(requestId)
	}

	i.writeNonStreamNormalResponse(ctx, request, requestId)
}

// writeNonStreamErrorResponse writes a non-streaming error response
func (i *InferPara) writeNonStreamErrorResponse(ctx *gin.Context) {
	// Anthropic error format
	errData := map[string]any{
		"type": "error",
		"error": map[string]any{
			"type": "internal_error",
			"message": fmt.Sprintf("mock error happened in instance [%s:%s] with code %v",
				i.Addr, i.Port, i.ErrorCode),
		},
	}
	rawBytes, err := utils.SonicApi.Marshal(errData)
	if err != nil {
		fmt.Printf("failed to write err = %v, context err = %v\n", err, ctx.Request.Context().Err())
		ctx.JSON(http.StatusInternalServerError, err)
		return
	}
	ctx.Writer.WriteHeader(i.ErrorCode)
	_, err = ctx.Writer.Write(rawBytes)
	ctx.Writer.Flush()
	return
}

// writeNonStreamNormalResponse writes a non-streaming normal response
func (i *InferPara) writeNonStreamNormalResponse(ctx *gin.Context, request *anthropic.AnthropicRequest, requestId string) {
	maxTokens := request.MaxTokens
	if maxTokens == 0 {
		maxTokens = 4
	}

	content := []anthropic.AnthropicContent{
		{Type: anthropic.ContentTypeText, Text: Token},
	}

	anthropicResponse := anthropic.AnthropicResponse{
		ID:           "msg_" + requestId,
		Type:         "message",
		Role:         "assistant",
		Model:        Model,
		Content:      content,
		StopReason:   "end_turn",
		StopSequence: nil,
		Usage: anthropic.AnthropicUsage{
			InputTokens:  2000,
			OutputTokens: maxTokens,
		},
		KvTransferParams: map[string]any{
			"address": i.Addr,
			"port":    i.Port,
		},
	}

	bytes, err := utils.SonicApi.Marshal(anthropicResponse)
	if err != nil {
		fmt.Printf("failed to write err = %v, context err = %v\n", err, ctx.Request.Context().Err())
		ctx.JSON(http.StatusInternalServerError, err)
		return
	}

	time.Sleep(time.Duration(i.TTFT) * time.Millisecond)
	i.Collector.TimeToFirstTime.WithLabelValues(Model).Observe(float64(i.TTFT) / 1000.0)

	ctx.Header("X-Request-Id", ctx.Request.Header.Get("X-Request-Id"))
	ctx.Header(constant.HeaderContentLength, strconv.Itoa(len(bytes)))
	ctx.Writer.WriteHeader(http.StatusOK)
	ctx.Writer.Write(bytes)
	ctx.Writer.Flush()
}
