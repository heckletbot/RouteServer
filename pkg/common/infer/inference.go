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

	"github.com/gin-gonic/gin"

	"infer-dp-scheduler/pkg/common/client"
	"infer-dp-scheduler/pkg/common/errs"
)

const (
	HandlerChatCompletions = "chat_completions_infer"
	HandlerCompletions     = "completions_infer"
	HandlerMessages        = "messages_infer"
)

type Inference interface {
	CompletionsInfer(ctx *gin.Context)
	ChatCompletionsInfer(ctx *gin.Context)
	MessagesInfer(ctx *gin.Context)
	TestInstance(ctx *gin.Context, path string, isChat bool, instance *client.Instance) (successToCheck bool, testSuccess bool, errDetails *errs.RSError)
}

func GetHandlerMethod(inference Inference, handlerName string) (gin.HandlerFunc, bool) {
	switch handlerName {
	case HandlerChatCompletions:
		return inference.ChatCompletionsInfer, true
	case HandlerCompletions:
		return inference.CompletionsInfer, true
	case HandlerMessages:
		return inference.MessagesInfer, true
	default:
		return nil, false
	}
}

func RegisterEndpoint(engine *gin.Engine, handler Inference, path string, method string, handlerName string) bool {
	handlerMethod, exists := GetHandlerMethod(handler, handlerName)
	if !exists {
		return false
	}
	switch method {
	case http.MethodPost:
		engine.POST(path, handlerMethod)
	case http.MethodGet:
		engine.GET(path, handlerMethod)
	case http.MethodPut:
		engine.PUT(path, handlerMethod)
	case http.MethodDelete:
		engine.DELETE(path, handlerMethod)
	case http.MethodPatch:
		engine.PATCH(path, handlerMethod)
	default:
		return false
	}

	return true
}
