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

package request

import (
	"fmt"
	"io"
	"net/http"
	"strconv"

	"infer-dp-scheduler/pkg/common/openai"
	"infer-dp-scheduler/pkg/common/utils"
)

type GenerateRequestImplV1 struct {
	isChat          bool
	reqId           string
	header          http.Header
	RawBody         *utils.JSONModifier
	OriginBodyBytes []byte

	KvTransferParams map[string]interface{}
	EcTransferParams map[string]interface{}

	outputFirstTokenFromPrefill bool

	InferType    string
	sloTag       string
	Chat         []utils.ChatContent
	promptLength int32

	originMaxTokens int32
	originIsStream  bool
	firstTokenBody  []byte

	sloInterval     *utils.SLOInterval
	promptCacheKey  string
	apcStrategyType string

	BodyReqId string
}

func (req *GenerateRequestImplV1) GetModel() string {
	// Extract model info from RawBody
	if req.RawBody != nil {
		if model, exists := req.RawBody.GetString("model"); exists {
			return model
		}
	}
	return ""
}

func (req *GenerateRequestImplV1) GetPromptCacheKey() string {
	return req.promptCacheKey
}

func (req *GenerateRequestImplV1) GetApcStrategyType() string {
	return req.apcStrategyType
}

func NewBasicRequestImplForPipeline(request *http.Request, isChat bool) (*GenerateRequestImplV1, error) {
	bodyBytes, err := io.ReadAll(request.Body)
	if err != nil {
		return nil, err
	}

	raw, err := utils.NewJSONModifier(bodyBytes)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal request body to raw json obj: %v", err)
	}
	impl := &GenerateRequestImplV1{
		OriginBodyBytes:             bodyBytes,
		outputFirstTokenFromPrefill: OutputFirstTokenFromPrefill,
		RawBody:                     raw,
		header:                      request.Header,
		isChat:                      isChat,
	}

	maxTokens, err := parseMaxTokens(raw, isChat)
	if err != nil {
		return nil, err
	}
	impl.originMaxTokens = maxTokens

	if !raw.IsBool(openai.StreamKey) {
		return nil, fmt.Errorf("stream must be a boolean value")
	}
	if isStream, exists := raw.GetBool(openai.StreamKey); exists {
		impl.originIsStream = isStream
	}

	if streamOptionsRaw, exists := raw.GetRaw(openai.StreamOptionsKey); exists {
		streamOptionsObj, isObj := streamOptionsRaw.(map[string]interface{})
		if !isObj {
			return nil, fmt.Errorf("stream_options must be an object")
		}
		isStream, isStreamSet := raw.GetBool(openai.StreamKey)
		if !isStreamSet || !isStream {
			return nil, fmt.Errorf("stream_options can only be used when stream is true")
		}
		for key, val := range streamOptionsObj {
			if key == openai.IncludeUsageKey || key == openai.ContinuousUsageStatsKey {
				if _, parsed := utils.TryParseBool(val); !parsed {
					return nil, fmt.Errorf("stream_options.%s must be a valid boolean value", key)
				}
			}
		}
	}

	if reqId, exists := raw.GetString(openai.RequestId); exists {
		if err := utils.CheckResourceNameValid(reqId); err != nil {
			return nil, fmt.Errorf("invalid request_id: %v", err)
		}
		impl.BodyReqId = reqId
	}

	return impl, nil
}

func (req *GenerateRequestImplV1) SetSLOTag(sloTag string) {
	req.sloTag = sloTag
}

func (req *GenerateRequestImplV1) GetSLOTag() string {
	return req.sloTag
}

func (req *GenerateRequestImplV1) GetSLOInterval() *utils.SLOInterval {
	return req.sloInterval
}

func (req *GenerateRequestImplV1) IsStream() bool {
	return req.originIsStream
}

func (req *GenerateRequestImplV1) IsChat() bool {
	return req.isChat
}

func (req *GenerateRequestImplV1) GetRequestId() string {
	return req.reqId
}

func (req *GenerateRequestImplV1) GetHeader() http.Header {
	return req.header
}

func (req *GenerateRequestImplV1) GetType() string {
	return req.InferType
}

func (req *GenerateRequestImplV1) GetChat() []utils.ChatContent {
	return req.Chat
}

func (req *GenerateRequestImplV1) GetMaxToken() int32 {
	return req.originMaxTokens
}

func (req *GenerateRequestImplV1) GetPromptTokenCount() int32 {
	return req.promptLength
}

func (req *GenerateRequestImplV1) SetPromptLen(promptLen int32) {
	req.promptLength = promptLen
	req.sloInterval = &utils.SLOInterval{PromptStrLengthMax: strconv.Itoa(int(req.promptLength))}
}

func (req *GenerateRequestImplV1) SetRequestId(reqId string) {
	req.reqId = reqId
}

func (req *GenerateRequestImplV1) SetFirstTokenBody(body []byte) {
	req.firstTokenBody = body
}

func (req *GenerateRequestImplV1) GetFirstTokenBody() []byte {
	return req.firstTokenBody
}
