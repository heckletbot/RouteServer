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
	"strconv"
)

type SLOInterval struct {
	PromptStrLengthMax string `yaml:"prompt-str-length-max" json:"prompt-str-length-max"`
}

type WorkerEventsRequest struct {
	WorkerId       string              `json:"id"`
	RequestEvents  []*RequestEventMsg  `json:"reqs"`
	InstanceEvents []*InstanceEventMsg `json:"inst"`
}

type RequestEventMsg struct {
	Request            *RemoteRequestWrapper   `json:"request"`
	TargetInstance     string                  `json:"instance"`
	PreInstance        string                  `json:"preInstance"`
	EventType          int                     `json:"type"`
	FailureDetails     *InstanceFailureDetails `json:"failure-details"`
	TimeStamp          int64                   `json:"time"`
	InternalTestingReq bool                    `json:"test"`
}

type InstanceEventMsg struct {
	InstanceId string `json:"id"`
	IsHang     bool   `json:"hang"`
}

type RemoteRequestWrapper struct {
	Id              string         `json:"id"`
	Type            string         `json:"type"`
	SloTag          string         `json:"slo"`
	SLOInterval     *SLOInterval   `json:"slo-intervals"`
	Content         *[]ChatContent `json:"content,omitempty"`
	PromptCacheKey  string         `json:"prompt-cache-key,omitempty"`
	ApcStrategyType string         `json:"apc-strategy-type,omitempty"`
	MaxTokens       int32          `json:"max-tokens"`
	PromptTokens    int32          `json:"prompt-tokens"`
	Stream          bool           `json:"stream"`
	Model           string         `json:"model"`
}

func WrapRequest(request GenerateRequest, needContent bool) *RemoteRequestWrapper {
	if request == nil {
		return nil
	}
	var content *[]ChatContent
	if needContent {
		co := request.GetChat()
		content = &co
	}

	return &RemoteRequestWrapper{
		Id:              request.GetRequestId(),
		Type:            request.GetType(),
		SloTag:          request.GetSLOTag(),
		SLOInterval:     request.GetSLOInterval(),
		Content:         content,
		PromptCacheKey:  request.GetPromptCacheKey(),
		ApcStrategyType: request.GetApcStrategyType(),
		MaxTokens:       request.GetMaxToken(),
		PromptTokens:    request.GetPromptTokenCount(),
		Stream:          request.IsStream(),
		Model:           request.GetModel(),
	}
}

func (r *RemoteRequestWrapper) GetApcStrategyType() string {
	return r.ApcStrategyType
}

func (r *RemoteRequestWrapper) GetPromptCacheKey() string {
	return r.PromptCacheKey
}

func (r *RemoteRequestWrapper) IsStream() bool {
	return r.Stream
}

func (r *RemoteRequestWrapper) GetRequestId() string {
	return r.Id
}

func (r *RemoteRequestWrapper) GetType() string {
	return r.Type
}

func (r *RemoteRequestWrapper) SetSLOTag(sloTag string) {
	r.SloTag = sloTag
}

func (r *RemoteRequestWrapper) GetSLOTag() string {
	return r.SloTag
}

func (r *RemoteRequestWrapper) GetSLOInterval() *SLOInterval {
	return r.SLOInterval
}

func (r *RemoteRequestWrapper) GetChat() []ChatContent {
	if r.Content == nil {
		return make([]ChatContent, 0)
	}
	return *r.Content
}

func (r *RemoteRequestWrapper) GetMaxToken() int32 {
	return r.MaxTokens
}

func (r *RemoteRequestWrapper) GetPromptTokenCount() int32 {
	return r.PromptTokens
}

func (r *RemoteRequestWrapper) GetModel() string {
	return r.Model
}

func (r *RemoteRequestWrapper) SetPromptLen(promptLen int32) {
	r.PromptTokens = promptLen
	r.SLOInterval = &SLOInterval{PromptStrLengthMax: strconv.Itoa(int(promptLen))}
}

type ChatContent struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type GenerateRequest interface {
	GetRequestId() string
	GetType() string // prefill 或者 decode
	GetSLOTag() string
	GetSLOInterval() *SLOInterval
	GetChat() []ChatContent     // 请求内容，如果是completion就是长度为1，role为空；如果是chat接口就可能长度大于1
	GetMaxToken() int32         // 请求中的MaxToken
	GetPromptTokenCount() int32 // 如果是Decode请求，则是从prefill response中读出来值；如果是Prefill请求，则为GetPrompt总String长度 + 配置的系统Prompt长度
	IsStream() bool
	GetModel() string // 请求中的model参数
	GetPromptCacheKey() string
	GetApcStrategyType() string
}
