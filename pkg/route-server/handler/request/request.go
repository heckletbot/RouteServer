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
	"html"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"infer-dp-scheduler/pkg/common/constant"
	"infer-dp-scheduler/pkg/common/logger"
	"infer-dp-scheduler/pkg/common/openai"
	"infer-dp-scheduler/pkg/common/utils"
	"infer-dp-scheduler/pkg/route-server/apc"
	"infer-dp-scheduler/pkg/route-server/metrics"
)

const (
	ChatReqIdPrefix             = "chatcmpl-"
	CompletionReqIdPrefix       = "cmpl-"
	OutputFirstTokenFromPrefill = false
)

var streamPrefix = []byte("data: ")
var streamSuffix = []byte{'\n', '\n'}

type ResponseBody struct {
	Usage   *openai.Usage        `json:"usage,omitempty"`
	Choices []*openai.ChoiceBody `json:"choices,omitempty"`
}

type GenerateRequestImpl struct {
	isChat           bool
	reqId            string
	header           http.Header
	EncoderBody      *utils.JSONModifier
	prefillBody      *utils.JSONModifier
	decodeBody       *utils.JSONModifier
	originBodyBytes  []byte
	kvTransferParams map[string]interface{}
	ecTransferParams map[string]interface{}

	outputFirstTokenFromPrefill bool

	inferType       string
	sloTag          string
	chat            []utils.ChatContent
	promptLength    int32
	promptCacheKey  string
	apcStrategyType string

	originMaxTokens int32
	originIsStream  bool
	firstTokenBody  []byte

	sloInterval *utils.SLOInterval

	BodyReqId string
}

func (req *GenerateRequestImpl) GetPromptString() string {
	if len(req.chat) == 0 {
		return ""
	}

	// completion mode
	if req.isChat && len(req.chat) == 1 {
		return req.chat[0].Content
	}

	// For chat messages, convert to ChatML format
	var builder strings.Builder
	for _, msg := range req.chat {
		// Escape special characters to prevent injection attacks
		escapedRole := html.EscapeString(msg.Role)
		escapedContent := html.EscapeString(msg.Content)
		builder.WriteString(fmt.Sprintf("<|im_start|>%s\n%s<|im_end|>\n", escapedRole, escapedContent))
	}
	return builder.String()
}

func (req *GenerateRequestImpl) GeneratePromptCacheKey() {
	apcManager := apc.GetGlobalManager()
	if apcManager == nil || !apcManager.IsRunning() {
		// apc_manager not enabled, return directly
		return
	}

	strategy := apcManager.GetDefaultStrategyInstance()
	if strategy == nil {
		logger.Log().Errorf("apc strategy nil")
		return
	}

	promptString := req.GetPromptString()
	key, err := strategy.ComputePromptKey(promptString)
	if err != nil {
		logger.Log().Errorf("compute prompt key failed: %v", err)
		return
	}

	req.promptCacheKey = strategy.GenerateCacheKey(req.GetModel(), key)
	req.apcStrategyType = strategy.Name()
	return
}

func NewGenerateRequestImpl(request *http.Request, inferType string, isChat bool) (*GenerateRequestImpl, error) {
	startTime := time.Now()
	defer func() {
		duration := time.Since(startTime).Seconds()
		metrics.RecordNewGenerateRequestImplLatency(duration)
	}()

	bodyBytes, err := io.ReadAll(request.Body)
	if err != nil {
		return nil, err
	}

	impl := &GenerateRequestImpl{
		reqId:           GetInferRequestId(&request.Header, isChat),
		originBodyBytes: bodyBytes,
		inferType:       inferType,

		outputFirstTokenFromPrefill: OutputFirstTokenFromPrefill,
	}

	if isChat {
		err = impl.parseChatCompletion(bodyBytes)
	} else {
		err = impl.parseCompletion(bodyBytes)
	}

	impl.GeneratePromptCacheKey()

	impl.sloInterval = &utils.SLOInterval{PromptStrLengthMax: strconv.Itoa(int(impl.promptLength))}

	if err != nil {
		return nil, err
	}

	impl.header = request.Header
	impl.isChat = isChat
	return impl, nil
}

func (req *GenerateRequestImpl) GetApcStrategyType() string {
	return req.apcStrategyType
}

func (req *GenerateRequestImpl) GetPromptCacheKey() string {
	return req.promptCacheKey
}

func (req *GenerateRequestImpl) parseCompletion(bodyBytes []byte) error {
	raw, err := utils.NewJSONModifier(bodyBytes)
	if err != nil {
		return fmt.Errorf("failed to unmarshal request body to raw json obj: %v", err)
	}

	req.prefillBody = raw
	req.decodeBody = raw.ShadowCopy()

	maxTokens, err := parseMaxTokens(raw, false)
	if err != nil {
		return err
	}
	req.originMaxTokens = maxTokens

	if isStream, exists := raw.GetBool(openai.StreamKey); exists {
		req.originIsStream = isStream
	}

	if reqId, exists := raw.GetString(openai.RequestId); exists {
		if err := utils.CheckResourceNameValid(reqId); err != nil {
			return fmt.Errorf("invalid request_id: %v", err)
		}
		req.BodyReqId = reqId
	}

	chat, promptLen := TryParseContentAndLen(raw, req.GetRequestId(), false)
	req.chat = chat
	req.SetPromptLen(promptLen)
	return nil
}

func (req *GenerateRequestImpl) SetSLOTag(sloTag string) {
	req.sloTag = sloTag
}

func (req *GenerateRequestImpl) GetSLOTag() string {
	return req.sloTag
}

func (req *GenerateRequestImpl) GetSLOInterval() *utils.SLOInterval {
	return req.sloInterval
}

func (req *GenerateRequestImpl) parseChatCompletion(bodyBytes []byte) error {
	raw, err := utils.NewJSONModifier(bodyBytes)
	if err != nil {
		return fmt.Errorf("failed to unmarshal request body to raw json obj: %v", err)
	}

	req.prefillBody = raw
	req.decodeBody = raw.ShadowCopy()

	maxTokens, err := parseMaxTokens(raw, true)
	if err != nil {
		return err
	}
	req.originMaxTokens = maxTokens

	if isStream, exists := raw.GetBool(openai.StreamKey); exists {
		req.originIsStream = isStream
	}

	if reqId, exists := raw.GetString(openai.RequestId); exists {
		if err := utils.CheckResourceNameValid(reqId); err != nil {
			return fmt.Errorf("invalid request_id: %v", err)
		}
		req.BodyReqId = reqId
	}

	chat, promptLen := TryParseContentAndLen(raw, req.GetRequestId(), true)
	req.chat = chat
	req.SetPromptLen(promptLen)
	return nil
}

func (req *GenerateRequestImpl) IsStream() bool {
	return req.originIsStream
}

func (req *GenerateRequestImpl) OutputFirstTokenFromPrefill() bool {
	return req.outputFirstTokenFromPrefill && req.IsStream()
}

func (req *GenerateRequestImpl) IsChat() bool {
	return req.isChat
}

func (req *GenerateRequestImpl) GetRequestId() string {
	return req.reqId
}

func (req *GenerateRequestImpl) GetHeader() http.Header {
	return req.header
}

func (req *GenerateRequestImpl) GetType() string {
	return req.inferType
}

func (req *GenerateRequestImpl) GetChat() []utils.ChatContent {
	return req.chat
}

func (req *GenerateRequestImpl) GetModel() string {
	// Extract model info from prefillBody
	if req.prefillBody != nil {
		if model, exists := req.prefillBody.GetString("model"); exists {
			return model
		}
	}
	return ""
}

func (req *GenerateRequestImpl) GetMaxToken() int32 {
	return req.originMaxTokens
}

func (req *GenerateRequestImpl) GetPromptTokenCount() int32 {
	return req.promptLength
}

func (req *GenerateRequestImpl) PrepareEncoder() {
	req.inferType = constant.Encode
	req.EncoderBody.Set(openai.MaxTokenKey, 1)
	maxCompletionTokenExists := req.EncoderBody.Exists(openai.MaxCompletionTokenKey)
	if maxCompletionTokenExists {
		req.EncoderBody.Set(openai.MaxCompletionTokenKey, 1)
	}
	req.EncoderBody.Remove(openai.StreamOptionsKey)
	req.EncoderBody.Set(openai.StreamKey, false)
	req.EncoderBody.Remove(openai.MinTokensKey)
	req.prefillBody.Remove(openai.EcTransferParamsKey)
	req.prefillBody.Remove(openai.KvTransferParamsKey)
}

func (req *GenerateRequestImpl) SetPromptLen(promptLen int32) {
	req.promptLength = promptLen
	req.sloInterval = &utils.SLOInterval{PromptStrLengthMax: strconv.Itoa(int(req.promptLength))}
}

func (req *GenerateRequestImpl) PreparePrefill() {
	req.inferType = constant.Prefill
	req.prefillBody.Set(openai.MaxTokenKey, 1)
	maxCompletionTokenExists := req.prefillBody.Exists(openai.MaxCompletionTokenKey)
	if maxCompletionTokenExists {
		req.prefillBody.Set(openai.MaxCompletionTokenKey, 1)
	}
	req.prefillBody.Remove(openai.StreamOptionsKey)
	req.prefillBody.Set(openai.StreamKey, false)
	req.prefillBody.Remove(openai.MinTokensKey)
	req.prefillBody.Remove(openai.KvTransferParamsKey)
	if req.ecTransferParams != nil {
		req.prefillBody.Set(openai.EcTransferParamsKey, req.ecTransferParams)
	}
}

func (req *GenerateRequestImpl) PrepareDecode() {
	req.inferType = constant.Decode
	if req.kvTransferParams != nil {
		req.decodeBody.Set(openai.KvTransferParamsKey, req.kvTransferParams)
	}
	if req.IsStream() {
		PrepareStreamOptions(req.decodeBody)
	}
}

func (req *GenerateRequestImpl) ParseEncodeRespAndResetPromptLen(encodeRes *http.Response) error {
	bodyBytes, err := io.ReadAll(encodeRes.Body)
	if err != nil {
		err = fmt.Errorf("read body failed during encode stage, err = %v", err)
		return err
	}
	rawResJson, err := utils.NewJSONModifier(bodyBytes)
	if err != nil {
		return fmt.Errorf("failed to unmarshal encode response body: %v", err)
	}
	if usageRawMap, exists := rawResJson.GetObject(openai.UsageKey); exists {
		if promptLen, valid := utils.TryParseInt(usageRawMap[openai.PromptTokensKey]); valid {
			req.SetPromptLen(int32(promptLen))
		}
	}
	transParams, exists := rawResJson.GetObject(openai.EcTransferParamsKey)
	if exists {
		req.ecTransferParams = transParams
	} else {
		logger.Log().Warnf("encode res for request [%s] don't contains [%s]", req.GetRequestId(), openai.EcTransferParamsKey)
	}
	return nil
}

func (req *GenerateRequestImpl) ParsePrefillResp(prefillRes *http.Response) error {
	bodyBytes, err := io.ReadAll(prefillRes.Body)
	if err != nil {
		return fmt.Errorf("read response body err:%v", err)
	}
	rawResJson, err := utils.NewJSONModifier(bodyBytes)
	if err != nil {
		return fmt.Errorf("failed to unmarshal prefill response body: %v", err)
	}
	if usageRawMap, exists := rawResJson.GetObject(openai.UsageKey); exists {
		if promptLen, valid := utils.TryParseInt(usageRawMap[openai.PromptTokensKey]); valid {
			req.SetPromptLen(int32(promptLen))
		}
		promptTokensDetailsRaw := usageRawMap[openai.PromptTokensDetailsKey]
		if promptTokensDetailsRaw != nil {
			if promptTokensDetails, ok := promptTokensDetailsRaw.(map[string]interface{}); ok {
				if cachedTokens, valid := utils.TryParseInt(promptTokensDetails[openai.CachedTokensKey]); valid {
					metrics.RecordAPCCachedTokens(cachedTokens)
				}
			}
		}
	}
	transParams, exists := rawResJson.GetObject(openai.KvTransferParamsKey)
	if exists {
		req.kvTransferParams = transParams
	} else {
		logger.Log().Warnf("prefill res for request [%s] don't contains [%s]", req.GetRequestId(), openai.KvTransferParamsKey)
	}

	if req.OutputFirstTokenFromPrefill() {
		if err = req.fillFirstToken(bodyBytes); err != nil {
			return fmt.Errorf("failed fill first token: %v", err)
		}
	}
	return nil
}

func (req *GenerateRequestImpl) GetFirstToken() []byte {
	return req.firstTokenBody
}

func (req *GenerateRequestImpl) fillFirstToken(bodyBytes []byte) error {
	if !req.IsStream() {
		return nil
	}

	var chatCompletionRes = &openai.ChatCompletionResponse{}
	var completionRes = &openai.CompletionResponse{}
	if req.isChat {
		if err := utils.SonicApi.Unmarshal(bodyBytes, chatCompletionRes); err != nil {
			return fmt.Errorf("failed to unmarshal request body: %v", err)
		}
	} else {
		if err := utils.SonicApi.Unmarshal(bodyBytes, completionRes); err != nil {
			return fmt.Errorf("failed to unmarshal request body: %v", err)
		}
	}
	req.firstTokenBody = append(req.firstTokenBody, streamPrefix...)
	if req.isChat {
		streamRes := &openai.ChatCompletionStreamResponse{
			ID:      chatCompletionRes.ID,
			Object:  "chat.completion.chunk",
			Created: chatCompletionRes.Created,
			Model:   chatCompletionRes.Model,
			Choices: []openai.ChatCompletionStreamChoice{
				{
					Index: chatCompletionRes.Choices[0].Index,
					Delta: openai.ChatCompletionStreamChoiceDelta{
						Role:    chatCompletionRes.Choices[0].Message.Role,
						Content: "",
					},
				},
			},
		}
		tokenBytes, err := utils.SonicApi.Marshal(streamRes)
		if err != nil {
			return fmt.Errorf("failed to marshal first token body: %v", err)
		}
		req.firstTokenBody = append(req.firstTokenBody, tokenBytes...)
		req.firstTokenBody = append(req.firstTokenBody, streamSuffix...)
		req.firstTokenBody = append(req.firstTokenBody, streamPrefix...)
		streamRes.Choices[0].PromptNumToken = chatCompletionRes.Usage.PromptTokens
		streamRes.Choices[0].OutputNumToken = 1
		streamRes.Choices[0].Delta.Role = ""
		streamRes.Choices[0].Delta.Content = chatCompletionRes.Choices[0].Message.Content
		tokenBytes, err = utils.SonicApi.Marshal(streamRes)
		if err != nil {
			return fmt.Errorf("failed to marshal first token body: %v", err)
		}
		req.firstTokenBody = append(req.firstTokenBody, tokenBytes...)
	} else {
		completionRes.KvTransferParams = nil
		completionRes.Usage = nil
		completionRes.Choices[0].FinishReason = ""
		tokenBytes, err := utils.SonicApi.Marshal(completionRes)
		if err != nil {
			return fmt.Errorf("failed to marshal first token body: %v", err)
		}
		req.firstTokenBody = append(req.firstTokenBody, tokenBytes...)
	}
	req.firstTokenBody = append(req.firstTokenBody, streamSuffix...)
	return nil
}
func (req *GenerateRequestImpl) GetEncoderBody() (io.Reader, error) {
	return req.EncoderBody.ToReader()
}

func (req *GenerateRequestImpl) GetPrefillBody() (io.Reader, error) {
	return req.prefillBody.ToReader()
}

func (req *GenerateRequestImpl) GetDecodeBody() (io.Reader, error) {
	return req.decodeBody.ToReader()
}
func NewMMGenerateRequestImpl(request *http.Request, inferType string, isChat bool) (*GenerateRequestImpl, error) {
	bodyBytes, err := io.ReadAll(request.Body)
	if err != nil {
		return nil, err
	}

	raw, err := utils.NewJSONModifier(bodyBytes)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal request body to raw json obj: %v", err)
	}

	impl := &GenerateRequestImpl{
		reqId:           GetInferRequestId(&request.Header, isChat),
		originBodyBytes: bodyBytes,
		inferType:       inferType,

		outputFirstTokenFromPrefill: OutputFirstTokenFromPrefill,
	}

	impl.EncoderBody = raw
	impl.prefillBody = raw.ShadowCopy()
	impl.decodeBody = raw.ShadowCopy()

	imageCount, textStringLen := raw.GetImageCount()
	if imageCount != 0 {
		impl.promptLength = int32(imageCount)
	} else {
		impl.promptLength = int32(textStringLen)
	}
	impl.sloInterval = &utils.SLOInterval{PromptStrLengthMax: strconv.Itoa(int(impl.promptLength))}

	maxTokens, err := parseMaxTokens(raw, isChat)
	if err != nil {
		return nil, err
	}
	impl.originMaxTokens = maxTokens

	if isStream, exists := raw.GetBool(openai.StreamKey); exists {
		impl.originIsStream = isStream
	}

	impl.header = request.Header
	impl.isChat = isChat
	return impl, nil
}

func (req *GenerateRequestImpl) SetRequestId(reqId string) {
	req.reqId = reqId
}
