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

package pipeline

import (
	"fmt"
	"html"
	"strconv"
	"strings"
	"time"

	"infer-dp-scheduler/pkg/common/constant"
	"infer-dp-scheduler/pkg/common/errs"
	common_infer "infer-dp-scheduler/pkg/common/infer"
	"infer-dp-scheduler/pkg/common/logger"
	"infer-dp-scheduler/pkg/common/trace"
	"infer-dp-scheduler/pkg/common/utils"
	"infer-dp-scheduler/pkg/route-server/apc"
	"infer-dp-scheduler/pkg/route-server/handler"
	"infer-dp-scheduler/pkg/route-server/handler/request"
	"infer-dp-scheduler/pkg/route-server/metrics"
	"infer-dp-scheduler/pkg/route-server/scheduler"
)

const (
	LoadTypeUsage        = "usage"
	LoadTypeImage        = "image"
	LoadTypeText         = "text"
	LoadTypeBytes        = "bytes"
	LoadTypeTextAndImage = "text-and-image"
)

const (
	ImageLoadTokensKey     = "image_load_tokens"
	DefaultImageLoadTokens = 1000
)

const (
	SkipPolicyNoSkip    = "no_skip"
	SkipPolicyZeroImage = "skip_on_zero_image"
)

type InitRequestLoadFunc func(ctx *PipelineContext, spanName string, params map[string]interface{}) (load int32, loadType string)
type SkipPolicyFunc func(ctx *PipelineContext) (skip bool, reason string)

var skipPolicyFuncMap = map[string]SkipPolicyFunc{
	"":                  noSkip,
	SkipPolicyNoSkip:    noSkip,
	SkipPolicyZeroImage: skipOnZeroImage,
}

var initRequestLoadFuncMap = map[string]InitRequestLoadFunc{
	LoadTypeUsage:        InitUsageRequestLoad,
	LoadTypeImage:        InitImageRequestLoad,
	LoadTypeText:         InitTextRequestLoad,
	LoadTypeBytes:        InitByteRequestLoad,
	LoadTypeTextAndImage: InitTextAndImageRequestLoad,
}

type SchedulePhase struct {
	*BasePhase

	targetInstanceType  string
	ReqTimeoutMS        uint
	InitRequestLoadFunc func(ctx *PipelineContext, spanName string)
	scheduleParams      map[string]interface{}
	imageLoadTokens     int32
}

func (s *SchedulePhase) Init(phaseCfg *common_infer.PhaseConfig) error {
	if err := s.BasePhase.Init(phaseCfg); err != nil {
		return err
	}

	s.targetInstanceType = phaseCfg.ScheduleParams.InstanceType
	if s.targetInstanceType == "" {
		return fmt.Errorf("missing instance type")
	}

	if phaseCfg.ScheduleParams.Params != nil {
		s.scheduleParams = phaseCfg.ScheduleParams.Params
	} else {
		s.scheduleParams = make(map[string]interface{})
	}

	s.imageLoadTokens = int32(DefaultImageLoadTokens)
	if val, ok := s.scheduleParams[ImageLoadTokensKey]; ok {
		if tokens, err := strconv.ParseInt(fmt.Sprintf("%v", val), 10, 32); err == nil && tokens > 0 {
			s.imageLoadTokens = int32(tokens)
		}
	}
	s.scheduleParams[ImageLoadTokensKey] = s.imageLoadTokens

	if len(phaseCfg.ScheduleParams.RequestLoad) == 0 {
		return fmt.Errorf("at least 1 load type should be contained for schedule")
	}

	for _, loadType := range phaseCfg.ScheduleParams.RequestLoad {
		if _, exists := initRequestLoadFuncMap[loadType]; !exists {
			return fmt.Errorf("invalid load type: %s", loadType)
		}
	}
	if _, exist := phaseCfg.Config[RequestTimeout]; exist {
		timeoutMs, ok := utils.ParseUint(phaseCfg.Config[RequestTimeout])
		if !ok {
			return fmt.Errorf("invalid req_timeout_ms config")
		}
		s.ReqTimeoutMS = timeoutMs
	} else {
		s.ReqTimeoutMS = 0
	}
	return nil
}

func (s *SchedulePhase) ExecutePhase(ctx *PipelineContext) (rsError *errs.RSError) {
	if ctx.ServerCfg.InferMode == constant.LayerwiseMMMode && s.targetInstanceType == constant.Decode {
		flag := ctx.WaitForKVTransferSend(s.ReqTimeoutMS)
		if !flag {
			return errs.NewRSMaError(
				errs.GatewayTimeoutErr.BuildArgs(fmt.Sprintf("Decode wait timeout after %d ms", s.ReqTimeoutMS), "timeout waiting for MetaServer"),
				s.StageName(),
				errs.FailureTypeTimeout,
			)
		}
		if ctx.RequestContext.CancelCtx.IsCancel.Load() {
			return errs.NewRSMaError(
				errs.RequestForcedCancelledErr.BuildArgs("Decode", "decode request is aborted"), s.StageName(),
				errs.FailureTypeCanceled,
			)
		}
	}
	var instanceId string
	var skip bool
	defer func() {
		if rsError != nil && ctx.RequestContext.InferMsg.GetInstance(s.targetInstanceType) != "" {
			// report failure if instance id is already decided by scheduler.
			handler.ReportInstanceFailure(nil, ctx.RequestContext.InferMsg, ctx.GetSubRequests(s.SpanName()), ctx.InferManager.WorkerEventCollector, ctx.InferManager.SchedulerMgr)
		}
	}()

	if rsError = s.CheckPipelineContext(ctx); rsError != nil {
		return rsError
	}

	s.InitRequestLoad(ctx, s.SpanName())
	s.setSkipFlag(ctx, s.StageName(), s.targetInstanceType)

	instanceId, skip, rsError = ScheduleInstanceRequest(ctx, s.targetInstanceType, s.StageName(), s.SpanName(), s.StartSubSpan)
	if skip || rsError != nil {
		return rsError
	}

	if instanceId == "" {
		rsError = errs.NewRSMaError(errs.ServiceUnavailableErr.BuildArgs(s.RelateInstanceType(),
			fmt.Sprintf("fail to schedule [%s] request, reason: no instance returned by scheduler", s.targetInstanceType)),
			s.StageName())
		return rsError
	}

	return nil
}

func (s *SchedulePhase) RelateInstanceType() string {
	return s.targetInstanceType
}

func (s *SchedulePhase) CheckPipelineContext(ctx *PipelineContext) *errs.RSError {
	if ctx.GetSubRequests(s.SpanName()) == nil {
		rsError := errs.NewRSMaError(errs.InternalErr.BuildArgs(
			fmt.Sprintf("missing request for span [%v] in phase [%v]", s.SpanName(), s.PhaseName())), s.StageName())
		return rsError
	}
	return nil
}

func newSchedulePhase() Phase {
	return &SchedulePhase{
		BasePhase: NewBasePhase(constant.SchedulePhase, ""),
	}
}

func ScheduleInstanceRequest(ctx *PipelineContext, instanceType string, stageName string, spanName string, startSubSpanFunc func(ctx *PipelineContext, spanName string)) (instanceId string, skip bool, rsError *errs.RSError) {
	subCtx := ctx.GetSubRequests(spanName)
	subCtx.Type = instanceType

	defer func() {
		if !skip {
			ctx.RequestContext.InferMsg.SetInstance(instanceType, instanceId)
		}
	}()

	if _, skip := ctx.RequestContext.InferMsg.IsSkipped(stageName); skip {
		return instanceId, skip, nil
	}
	instanceId = ctx.RequestContext.InferMsg.GetInstance(instanceType)

	if instanceId != "" {
		// happens in TestInstance scenario, request for a certain target instance, set the default slo tag
		subCtx.SetSLOTag(constant.SLOTagDefault)
		return instanceId, false, nil
	}

	// use scheduler to find an instance
	subSpanName := handler.BuildSubSpanName(spanName, trace.RequestDispatch)
	startSubSpanFunc(ctx, subSpanName)

	rsError = nil
	instanceId = ""
	defer func() {
		if rsError == nil {
			ctx.GetTracer().Tracer.EndSpan(subSpanName,
				fmt.Sprintf("request [%s] schedule to %s instance [%s]",
					ctx.RequestId(), instanceType, instanceId), trace.EnableOtelTrace)
		} else {
			ctx.GetTracer().Tracer.ErrorSpan(subSpanName, rsError, trace.EnableOtelTrace)
		}
	}()

	scheduleRes, err := ctx.InferManager.SchedulerMgr.
		DispatchRequest(ctx.ClientGinCtx.Request.Context(), subCtx, nil,
			ctx.GetTracer().Tracer, ctx.BlackId(instanceType), scheduler.RemoteRequestInfo{IsLocal: true})

	if scheduleRes != nil {
		instanceId = scheduleRes.Result
	}

	if err != nil {
		rsError = errs.NewRSMaError(errs.InternalErr.BuildArgs(
			fmt.Sprintf("fail to schedule %s instances: %v", instanceType, err)), stageName)
		return instanceId, false, rsError
	}

	if scheduleRes != nil {
		if scheduleRes.Skipped {
			logger.Log().Infof("request %v skip some instances during %s scheduling due to link blacklist", ctx.RequestId(), instanceType)
		}
		subCtx.SetSLOTag(scheduleRes.SLOTag)
	}
	return instanceId, false, nil
}

func (s *SchedulePhase) setSkipFlag(ctx *PipelineContext, stageName, instanceType string) {
	skipFunc, exist := skipPolicyFuncMap[s.phaseCfg.ScheduleParams.SkipPolicy]
	if !exist {
		return
	}

	if skip, reason := skipFunc(ctx); skip {
		ctx.RequestContext.InferMsg.SkipInferRole(stageName, reason)
	}
}

func (s *SchedulePhase) InitRequestLoad(ctx *PipelineContext, spanName string) {
	for _, loadType := range s.phaseCfg.ScheduleParams.RequestLoad {
		initRequestLoadFunc, exist := initRequestLoadFuncMap[loadType]
		if !exist {
			logger.Log().Tracef("no reload type [%v] init func found", loadType)
			continue
		}

		count, actualLoadType := initRequestLoadFunc(ctx, spanName, s.scheduleParams)
		ctx.GetRequestCtx().RequestLoadsCount.Store(actualLoadType, count)
		if count > 0 {
			break
		}
	}
}

func noSkip(ctx *PipelineContext) (bool, string) {
	return false, ""
}

func skipOnZeroImage(ctx *PipelineContext) (bool, string) {
	raw, ok := ctx.GetRequestCtx().RequestLoadsCount.Load(LoadTypeImage)
	if !ok {
		return false, ""
	}

	cnt, ok := raw.(int32)
	if !ok {
		logger.Log().Errorf("failed to get count from ctx,raw:[%v]", raw)
		return false, ""
	}

	if cnt == 0 {
		return true, fmt.Sprintf("request %v will skip encode staged for no image found", ctx.GetRequestCtx().ServingRequestId)
	}

	return false, ""
}

func InitUsageRequestLoad(ctx *PipelineContext, spanName string, params map[string]interface{}) (int32, string) {
	subCtx := ctx.GetSubRequests(spanName)
	usage := ctx.GetRequestCtx().UsagePromptLen.Load()
	if usage != 0 {
		subCtx.SetPromptLen(usage)
	}
	return usage, LoadTypeUsage
}

func InitImageRequestLoad(ctx *PipelineContext, spanName string, params map[string]interface{}) (int32, string) {
	parsedReq := ctx.GetRequestCtx().ParsedRequest
	subCtx := ctx.GetSubRequests(spanName)
	imageCount, _ := parsedReq.RawBody.GetImageCount()
	if imageCount != 0 {
		subCtx.SetPromptLen(int32(imageCount))
	}
	return int32(imageCount), LoadTypeImage
}

func GetPromptString(isChat bool, isMessages bool, content []utils.ChatContent) string {
	if len(content) == 0 {
		return ""
	}

	// Anthropic messages mode builds a plain "[role]: content" format
	if isMessages {
		return buildMessagesFormat(content)
	}

	// completion mode
	if !isChat && len(content) == 1 {
		return content[0].Content
	}

	// For chat messages, convert to ChatML format
	return buildChatMLFormat(content)
}

func buildMessagesFormat(content []utils.ChatContent) string {
	var builder strings.Builder
	for _, msg := range content {
		escapedRole := html.EscapeString(msg.Role)
		escapedContent := html.EscapeString(msg.Content)
		builder.WriteString(fmt.Sprintf("[%s]: %s\n", escapedRole, escapedContent))
	}
	return builder.String()
}

func buildChatMLFormat(content []utils.ChatContent) string {
	var builder strings.Builder
	for _, msg := range content {
		escapedRole := html.EscapeString(msg.Role)
		escapedContent := html.EscapeString(msg.Content)
		builder.WriteString(fmt.Sprintf("<|im_start|>%s\n%s<|im_end|>\n", escapedRole, escapedContent))
	}
	return builder.String()
}

func SetApcPromptCacheKeyAndStrategy(parsedReq *request.GenerateRequestImplV1, content []utils.ChatContent, subCtx *utils.RemoteRequestWrapper) error {
	apcManager := apc.GetGlobalManager()
	if apcManager == nil || !apcManager.IsRunning() {
		return nil
	}

	promptString := GetPromptString(parsedReq.IsChat(), parsedReq.IsMessages(), content)

	strategy := apcManager.GetDefaultStrategyInstance()
	if strategy == nil {
		return fmt.Errorf("apc prompt key compute failed: nil strategy")
	}
	key, err := strategy.ComputePromptKey(promptString)
	if err != nil {
		return fmt.Errorf("compute prompt key failed: %v", err)
	}

	subCtx.PromptCacheKey = strategy.GenerateCacheKey(parsedReq.GetModel(), key)
	subCtx.ApcStrategyType = strategy.Name()
	return nil
}

func InitTextRequestLoad(ctx *PipelineContext, spanName string, params map[string]interface{}) (int32, string) {
	startTime := time.Now()
	defer func() {
		duration := time.Since(startTime).Seconds()
		metrics.RecordNewGenerateRequestImplLatency(duration)
	}()
	parsedReq := ctx.GetParsedRequest()
	subCtx := ctx.GetSubRequests(spanName)
	content, promptLen := request.TryParseContentAndLen(parsedReq.RawBody, parsedReq.GetRequestId(), parsedReq.IsChat(), parsedReq.IsMessages())
	if promptLen > 0 {
		parsedReq.Chat = content
		subCtx.SetPromptLen(promptLen)
		err := SetApcPromptCacheKeyAndStrategy(parsedReq, content, subCtx)
		if err != nil {
			logger.Log().Errorf("Request %v set apc cache key and strategy error for %v", parsedReq.GetRequestId(), err)
		}
	}
	return promptLen, LoadTypeText
}

func InitByteRequestLoad(ctx *PipelineContext, spanName string, params map[string]interface{}) (int32, string) {
	parsedReq := ctx.GetParsedRequest()
	subCtx := ctx.GetSubRequests(spanName)
	promptLen := int32(len(parsedReq.OriginBodyBytes))
	subCtx.SetPromptLen(promptLen)
	return promptLen, LoadTypeBytes
}

func InitTextAndImageRequestLoad(ctx *PipelineContext, spanName string, params map[string]interface{}) (int32, string) {
	imageLoadTokens := int32(DefaultImageLoadTokens)
	if val, ok := params[ImageLoadTokensKey]; ok {
		if tokens, ok := val.(int32); ok && tokens > 0 {
			imageLoadTokens = tokens
		}
	}

	parsedReq := ctx.GetRequestCtx().ParsedRequest
	subCtx := ctx.GetSubRequests(spanName)

	imageCount, textStringLen := parsedReq.RawBody.GetImageCount()
	imageTokens := int32(imageCount) * imageLoadTokens
	totalLoad := int32(textStringLen) + imageTokens

	if totalLoad > 0 {
		subCtx.SetPromptLen(totalLoad)
	}
	return totalLoad, LoadTypeTextAndImage
}
