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
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"route-server/pkg/common/constant"
	"route-server/pkg/common/errs"
	common_infer "route-server/pkg/common/infer"
	"route-server/pkg/common/logger"
	"route-server/pkg/common/openai"
	"route-server/pkg/common/trace"
	"route-server/pkg/common/utils"
	"route-server/pkg/route-server/handler"
	"route-server/pkg/route-server/handler/request"
	"route-server/pkg/route-server/metrics"
)

type prepareRequestFunc func(ctx *PipelineContext, copiedBody *utils.JSONModifier) *utils.JSONModifier
type postProcessFunc func(res *http.Response, ctx *PipelineContext, phase Phase) (rsErr *errs.RSError, instanceFailureDetails *utils.InstanceFailureDetails)
type postPhaseFunc func(ctx *PipelineContext, insId string, startTime time.Time, stopTime time.Time, phase Phase)

const (
	EncodeProcessResponse        = "encode_post_response"
	PrefillProcessResponse       = "prefill_post_response"
	PrefillStreamProcessResponse = "prefill_stream_post_response"
	DecodeProcessResponse        = "decode_post_response"
	FirstTokenPost               = "first_token_post"
	WaitFirstToken               = "wait_first_token"
	CheckKVTransferParamsKey     = "check_kv_transfer_params"

	EncodePrepareRequest        = "encode_prepare_request"
	PrefillPrepareRequest       = "prefill_prepare_request"
	PrefillStreamPrepareRequest = "prefill_stream_prepare_request"
	DecodePrepareRequest        = "decode_prepare_request"
	LayerwisePrepareRequest     = "layerwise_prepare_request"
	KVPrepareRequest            = "set_kv_transfer_params"
	VllmAscendPrepareRequest    = "vllm_ascend_prepare_request"
	RemoveMessages              = "remove_messages"

	RegularPostPhase  = "regular_post_phase"
	CompletePostPhase = "complete_post_phase"

	// CoordPostPhaseFuncs are the mode-aware variants of the prefill hooks: they
	// keep the PD path identical and only divert when the request is local.
	CoordPrepareRequest  = "coord_prepare_request"
	CoordProcessResponse = "coord_post_response"
	CoordPostPhase       = "coord_post_phase"
)

var (
	prepareRequestFuncMap = map[string]prepareRequestFunc{
		EncodePrepareRequest:        PrepareEncodeRequestBody,
		PrefillPrepareRequest:       PreparePrefillRequestBody,
		PrefillStreamPrepareRequest: PreparePrefillStreamRequestBody,
		DecodePrepareRequest:        PrepareDecodeRequestBody,
		LayerwisePrepareRequest:     PrepareLayerwiseRequestBody,
		KVPrepareRequest:            PrepareRequestBodyWithKV,
		VllmAscendPrepareRequest:    PrepareVllmAscendRequestBody,
		RemoveMessages:              RemoveMessageRequestBody,
		CoordPrepareRequest:         PrepareCoordRequestBody,
	}
	postProcessFuncMap = map[string]postProcessFunc{
		EncodeProcessResponse:        PostEncodeProcessResponse,
		PrefillProcessResponse:       PostPrefillProcessResponse,
		PrefillStreamProcessResponse: PostPrefillStreamProcessResponse,
		DecodeProcessResponse:        PostDecodeProcessResponse,
		FirstTokenPost:               FirstTokenReport,
		WaitFirstToken:               FirstTokenWait,
		CheckKVTransferParamsKey:     CheckKVTransferParams,
		CoordProcessResponse:         PostCoordProcessResponse,
	}
	postPhaseFuncMap = map[string]postPhaseFunc{
		RegularPostPhase:  PostPhaseProcess,
		CompletePostPhase: PostDecodePhaseProcess,
		CoordPostPhase:    PostCoordPhaseProcess,
	}
)

// MonoRequestPhase Base implementation for monolic phases (such as encode, prefill, decode execution)
type MonoRequestPhase struct {
	*BasePhase

	relatedInstanceType string

	PrepareRequestBodyFunc  []prepareRequestFunc
	PostProcessResponseFunc []postProcessFunc
	PostPhaseSuccessFunc    []postPhaseFunc

	ReqTimeoutMS uint
}

func newExecutePhase() Phase {
	phase := &MonoRequestPhase{
		BasePhase: NewBasePhase(constant.ExecutePhase, ""),
	}
	return phase
}

func (e *MonoRequestPhase) ExecutePhase(ctx *PipelineContext) (rsError *errs.RSError) {
	metrics.UpdateRunningRequests(e.RelateInstanceType(), 1)
	defer metrics.UpdateRunningRequests(e.RelateInstanceType(), -1)
	if rsError = e.CheckPipelineContext(ctx); rsError != nil {
		return rsError
	}
	start := time.Now()

	var instanceId string
	var details *utils.InstanceFailureDetails
	var skip bool
	defer func() {
		if skip {
			ctx.GetTracer().Tracer.Event(e.SpanName(), fmt.Sprintf("%s_skip", e.StageName()), ctx.RequestContext.InferMsg.SkipFlags[e.StageName()])
			// do not report when phase skipped
			return
		}

		if rsError != nil {
			handler.ReportInstanceFailure(details, ctx.RequestContext.InferMsg, ctx.GetSubRequests(e.SpanName()), ctx.InferManager.WorkerEventCollector, ctx.InferManager.SchedulerMgr)
		} else {
			ctx.InferManager.WorkerEventCollector.OnRequestSuccess(ctx.GetSubRequests(e.SpanName()), ctx.RequestContext.InferMsg)
		}
	}()

	_, skip = ctx.RequestContext.InferMsg.IsSkipped(e.StageName())
	if skip {
		return nil
	}
	instanceId = ctx.RequestContext.InferMsg.GetInstance(e.RelateInstanceType())

	if instanceId == "" {
		rsError = errs.NewRSMaError(errs.ServiceUnavailableErr.BuildArgs(e.RelateInstanceType(),
			fmt.Sprintf("fail to schedule [%s] request, reason: no instance returned by scheduler", e.RelateInstanceType())),
			e.StageName())
		return rsError
	}

	if rsError, details = e.executeRequest(ctx, instanceId); rsError != nil {
		return rsError
	}

	stopTime := time.Now()
	for _, postFunc := range e.PostPhaseSuccessFunc {
		postFunc(ctx, instanceId, start, stopTime, e)
	}

	return nil
}

func (e *MonoRequestPhase) executeRequest(ctx *PipelineContext, instanceId string) (rsError *errs.RSError, details *utils.InstanceFailureDetails) {
	tracer := ctx.GetTracer()
	subSpanName := handler.BuildSubSpanName(e.SpanName(), trace.RequestSubProcess)
	e.StartSubSpan(ctx, subSpanName)
	subRequest := ctx.GetSubRequests(e.SpanName())

	// rsError and traceErr can be different in some case, hence we need to define traceErr separately
	var traceErr error
	defer func() {
		if traceErr == nil {
			tracer.Tracer.EndSpan(subSpanName,
				fmt.Sprintf("success to exec [%s] request", e.RelateInstanceType()), trace.EnableOtelTrace)
		} else {
			tracer.Tracer.ErrorSpan(subSpanName, traceErr, trace.EnableOtelTrace)
		}
	}()

	insClient, err := ctx.InferManager.InstanceMgr.GetInstanceClient(instanceId)
	if err != nil {
		rsError = errs.NewRSMaError(errs.InternalErr.BuildArgs(
			fmt.Sprintf("fail to get instance client for %v, err: %v", instanceId, err)), e.StageName())
		traceErr = rsError
		return
	}

	parsedReq := ctx.GetParsedRequest()
	reqBody := parsedReq.RawBody.ShadowCopy()
	for _, prepareFunc := range e.PrepareRequestBodyFunc {
		reqBody = prepareFunc(ctx, reqBody)
	}
	bodyReader, length, err := reqBody.ToReaderWithLength()
	if err != nil {
		rsError = errs.NewRSMaError(errs.InternalErr.BuildArgs(
			fmt.Sprintf("fail to get %s req body err: %v", e.RelateInstanceType(), err)), e.StageName())
		traceErr = rsError
		return
	}

	ctx.InferManager.WorkerEventCollector.OnRequestNew(subRequest, ctx.RequestContext.InferMsg)
	var res *http.Response
	defer func() {
		if res != nil {
			if err = res.Body.Close(); err != nil {
				logger.Log().Errorf("fail to close [%s] response body: %v", e.RelateInstanceType(), err)
			}
		}
	}()

	requestCancelCtx := e.GetCancelContext(ctx, instanceId, tracer)
	if e.ReqTimeoutMS > 0 {
		var cancel func()
		requestCancelCtx, cancel = context.WithTimeout(requestCancelCtx, time.Duration(e.ReqTimeoutMS)*time.Millisecond)
		defer cancel()
	}

	tracer.Tracer.Event(subSpanName, fmt.Sprintf("%s_start", e.StageName()),
		fmt.Sprintf("send request to [%s] instance [%s] [%s], slo tag is [%s], stream = [%v], reqLoad = [%v], bodyLength = [%v]",
			e.RelateInstanceType(), instanceId, insClient.GetEndpoint(), subRequest.GetSLOTag(),
			subRequest.IsStream(), subRequest.GetPromptTokenCount(), length))
	if ctx.ServerCfg.InferMode == constant.LayerwiseMMMode && e.stageName == constant.StageDecode {
		ctx.SetDone()
	}
	res, rsError, details = insClient.Generate(requestCancelCtx, ctx.RequestContext.ParsedRequest.GetHeader(), ctx.RequestPath(), bodyReader, instanceId, e.StageName(), ctx.RequestId(), ctx.RequestContext.ParsedRequest.IsMessages())
	if rsError != nil {
		if errors.Is(requestCancelCtx.Err(), context.Canceled) {
			tracer.Tracer.Event(subSpanName, trace.TraceEventUserCancelled,
				fmt.Sprintf("user request cancelled on send %s req to [%s]", e.RelateInstanceType(), instanceId))
			if ctx.RequestContext.CancelCtx.IsCancel.Load() {
				rsError = errs.NewRSMaError(&errs.RequestForcedCancelledErr, e.StageName(), errs.FailureTypeCanceled)
			} else {
				rsError = errs.NewRSMaError(&errs.RequestCancelledByUserErr, e.StageName(), errs.FailureTypeCanceled)
			}
			details = utils.DetailsFromError(0, requestCancelCtx.Err())
			traceErr = rsError
			return rsError, details
		}

		if rsError.EngineErr != nil {
			traceErr = fmt.Errorf("[%s] request [%s] for %v returns non 200 res, status code: %d, body = %s",
				e.RelateInstanceType(), instanceId, ctx.RequestId(), rsError.EngineErr.EngineStatus, string(rsError.EngineErr.EngineErrBody))
		} else {
			traceErr = fmt.Errorf("fail to send [%s] request [%s] for %v, err:generate request status code: %d, %v",
				e.RelateInstanceType(), instanceId, ctx.RequestId(), rsError.MaErr.Status, rsError.MaErr.Error())
		}
		logger.Log().Warn(traceErr.Error())
		return rsError, details
	}

	for _, processFunc := range e.PostProcessResponseFunc {
		if rsError, details = processFunc(res, ctx, e); rsError != nil {
			traceErr = rsError
			return rsError, details
		}
	}

	return nil, nil
}

func (e *MonoRequestPhase) RelateInstanceType() string {
	return e.relatedInstanceType
}

func (e *MonoRequestPhase) Init(phaseCfg *common_infer.PhaseConfig) error {
	if err := e.BasePhase.Init(phaseCfg); err != nil {
		return err
	}

	if _, exist := phaseCfg.Config[RequestTimeout]; exist {
		timeoutMs, ok := utils.ParseUint(phaseCfg.Config[RequestTimeout])
		if !ok {
			return fmt.Errorf("invalid req_timeout_ms config")
		}
		e.ReqTimeoutMS = timeoutMs
	} else {
		e.ReqTimeoutMS = 0
	}

	if phaseCfg.ExecuteParams.InstanceType == "" {
		return fmt.Errorf("missing instance type")
	}
	e.relatedInstanceType = phaseCfg.ExecuteParams.InstanceType

	for _, preProcessFuncName := range phaseCfg.ExecuteParams.PreProcessRequestFuncs {
		prepareFunc, exists := prepareRequestFuncMap[preProcessFuncName]
		if !exists {
			return fmt.Errorf("invalid pre process request func [%v]", preProcessFuncName)
		}
		e.PrepareRequestBodyFunc = append(e.PrepareRequestBodyFunc, prepareFunc)
	}

	for _, processFuncName := range phaseCfg.ExecuteParams.PostProcessFuncs {
		processFunc, exists := postProcessFuncMap[processFuncName]
		if !exists {
			return fmt.Errorf("invalid post process func [%v]", processFuncName)
		}
		e.PostProcessResponseFunc = append(e.PostProcessResponseFunc, processFunc)
	}

	for _, postFuncName := range phaseCfg.ExecuteParams.PostPhaseFuncs {
		postFunc, exists := postPhaseFuncMap[postFuncName]
		if !exists {
			return fmt.Errorf("invalid post phase func [%v]", postFuncName)
		}
		e.PostPhaseSuccessFunc = append(e.PostPhaseSuccessFunc, postFunc)
	}

	return nil
}

func (e *MonoRequestPhase) CheckPipelineContext(ctx *PipelineContext) *errs.RSError {
	if ctx.GetSubRequests(e.SpanName()) == nil {
		rsError := errs.NewRSMaError(errs.InternalErr.BuildArgs(
			fmt.Sprintf("missing request for span [%v] in phase [%v]", e.SpanName(), e.PhaseName())), e.StageName())
		return rsError
	}
	return nil
}

func PrepareEncodeRequestBody(context *PipelineContext, copiedBody *utils.JSONModifier) *utils.JSONModifier {
	copiedBody.Set(openai.MaxTokenKey, 1)
	maxCompletionTokenExists := copiedBody.Exists(openai.MaxCompletionTokenKey)
	if maxCompletionTokenExists {
		copiedBody.Set(openai.MaxCompletionTokenKey, 1)
	}
	copiedBody.Remove(openai.StreamOptionsKey)
	copiedBody.Set(openai.StreamKey, false)
	copiedBody.Remove(openai.MinTokensKey)
	copiedBody.Remove(openai.EcTransferParamsKey)
	copiedBody.Remove(openai.KvTransferParamsKey)
	return copiedBody
}

func PostEncodeProcessResponse(encodeRes *http.Response, ctx *PipelineContext, phase Phase) (rsError *errs.RSError, details *utils.InstanceFailureDetails) {
	bodyBytes, err := io.ReadAll(encodeRes.Body)
	if err != nil {
		return wrapInternalPostResponseFailures(encodeRes.StatusCode, phase, fmt.Errorf("read body failed during encode stage, err = %v", err))
	}
	rawResJson, err := utils.NewJSONModifier(bodyBytes)
	if err != nil {
		return wrapInternalPostResponseFailures(encodeRes.StatusCode, phase, fmt.Errorf("failed to unmarshal encode response body: %v", err))
	}
	if usageRawMap, exists := rawResJson.GetObject(openai.UsageKey); exists {
		if promptLen, valid := utils.TryParseInt(usageRawMap[openai.PromptTokensKey]); valid {
			ctx.RequestContext.UsagePromptLen.Store(int32(promptLen))
		}
	}
	transParams, exists := rawResJson.GetObject(openai.EcTransferParamsKey)
	if exists {
		ctx.RequestContext.ParsedRequest.EcTransferParams = transParams
	} else {
		logger.Log().Warnf("encode res for request [%s] don't contains [%s]", ctx.RequestId(), openai.EcTransferParamsKey)
	}
	return nil, nil
}

func wrapInternalPostResponseFailures(statusCode int, phase Phase, err error) (*errs.RSError, *utils.InstanceFailureDetails) {
	rsError := errs.NewRSMaError(errs.InternalErr.BuildArgs(
		fmt.Sprintf("failed to parse %s response, err=%v", phase.RelateInstanceType(), err)), phase.StageName())
	details := utils.DetailsFromError(statusCode, nil)
	return rsError, details
}

func PreparePrefillRequestBody(ctx *PipelineContext, copiedBody *utils.JSONModifier) *utils.JSONModifier {
	copiedBody.Set(openai.MaxTokenKey, 1)
	maxCompletionTokenExists := copiedBody.Exists(openai.MaxCompletionTokenKey)
	if maxCompletionTokenExists {
		copiedBody.Set(openai.MaxCompletionTokenKey, 1)
	}
	copiedBody.Remove(openai.StreamOptionsKey)
	copiedBody.Set(openai.StreamKey, false)
	copiedBody.Remove(openai.MinTokensKey)

	copiedBody.Remove(openai.KvTransferParamsKey)
	if ecParams := ctx.RequestContext.ParsedRequest.EcTransferParams; ecParams != nil {
		copiedBody.Set(openai.EcTransferParamsKey, ecParams)
	}

	return copiedBody
}

func PostPrefillProcessResponse(res *http.Response, ctx *PipelineContext, phase Phase) (rsError *errs.RSError, details *utils.InstanceFailureDetails) {
	bodyBytes, err := io.ReadAll(res.Body)
	if err != nil {
		return wrapInternalPostResponseFailures(res.StatusCode, phase, fmt.Errorf("read body failed during prefill stage, err = %v", err))
	}

	usage := &openai.UsageBody{}
	err = utils.SonicApi.Unmarshal(bodyBytes, usage)
	rawResJson, err := utils.NewJSONModifier(bodyBytes)
	if err != nil {
		return wrapInternalPostResponseFailures(res.StatusCode, phase, fmt.Errorf("failed to unmarshal prefill response body: %v", err))
	}
	if usageRawMap, exists := rawResJson.GetObject(openai.UsageKey); exists {
		if promptLen, valid := utils.TryParseInt(usageRawMap[openai.PromptTokensKey]); valid {
			ctx.RequestContext.UsagePromptLen.Store(int32(promptLen))
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
		ctx.RequestContext.ParsedRequest.KvTransferParams = transParams
	}
	ctx.RequestContext.ParsedRequest.SetFirstTokenBody(bodyBytes)

	return nil, nil
}

// CheckKVTransferParams is a configurable post-process func that fails the
// prefill stage when kv_transfer_params is missing for a streaming chat request.
func CheckKVTransferParams(res *http.Response, ctx *PipelineContext, phase Phase) (rsError *errs.RSError, details *utils.InstanceFailureDetails) {
	if ctx.RequestContext.ParsedRequest.KvTransferParams != nil {
		return nil, nil
	}
	logger.Log().Warnf("prefill res for request [%s] don't contains [%s]", ctx.RequestId(), openai.KvTransferParamsKey)
	if ctx.RequestContext.ParsedRequest.IsStream() && ctx.IsChat() {
		return wrapInternalPostResponseFailures(res.StatusCode, phase, fmt.Errorf(" kv transfer params is null"))
	}
	return nil, nil
}

func PostPhaseProcess(ctx *PipelineContext, insId string, startTime time.Time, stopTime time.Time, phase Phase) {
	traceEventPhaseDone(ctx, insId, startTime, stopTime, phase)
}

func traceEventPhaseDone(ctx *PipelineContext, insId string, startTime time.Time, stopTime time.Time, phase Phase) {
	executeTime := stopTime.Sub(startTime).Seconds()
	ctx.RequestContext.Tracer.Tracer.Event(trace.RequestSpanE2E, fmt.Sprintf("%s_done", phase.StageName()), fmt.Sprintf("execute instance [%s], cost [%v] sec", insId, executeTime))
}

func PreparePrefillStreamRequestBody(ctx *PipelineContext, copiedBody *utils.JSONModifier) *utils.JSONModifier {
	copiedBody.Set(openai.MaxTokenKey, 1)
	maxCompletionTokenExists := copiedBody.Exists(openai.MaxCompletionTokenKey)
	if maxCompletionTokenExists {
		copiedBody.Set(openai.MaxCompletionTokenKey, 1)
	}
	copiedBody.Remove(openai.MinTokensKey)

	if ctx.RequestContext.ParsedRequest.IsStream() && ctx.IsChat() {
		request.PrepareStreamOptions(copiedBody)
	} else {
		// completion use non-stream request
		copiedBody.Set(openai.StreamKey, false)
		copiedBody.Remove(openai.StreamOptionsKey)
	}

	copiedBody.Remove(openai.KvTransferParamsKey)
	if ecParams := ctx.RequestContext.ParsedRequest.EcTransferParams; ecParams != nil {
		copiedBody.Set(openai.EcTransferParamsKey, ecParams)
	}

	return copiedBody
}

func PrepareRequestBodyWithKV(ctx *PipelineContext, copiedBody *utils.JSONModifier) *utils.JSONModifier {
	copiedBody.Set(openai.KvTransferParamsKey, ctx.RequestContext.ParsedRequest.KvTransferParams)
	return copiedBody
}

func PrepareVllmAscendRequestBody(ctx *PipelineContext, copiedBody *utils.JSONModifier) *utils.JSONModifier {
	copiedBody.Set(openai.MinTokensKey, 1)

	var abortedRequests []string
	prefillerId := ctx.RequestContext.InferMsg.GetInstance(constant.Prefill)
	if prefillerId != "" {
		if aborted := ctx.InferManager.AcquireVllmAscendAbortedRequests(prefillerId); len(aborted) > 0 {
			abortedRequests = append(abortedRequests, aborted...)
		}
	}

	kvTransferParams := map[string]interface{}{
		DoRemoteDecodeKey:  true,
		DoRemotePrefillKey: false,
		RemoteEngineIdKey:  nil,
		RemoteBlockIdsKey:  nil,
		RemoteHostKey:      nil,
		RemotePortKey:      nil,
		AbortedRequestKey:  abortedRequests,
	}
	copiedBody.Set(openai.KvTransferParamsKey, kvTransferParams)
	return copiedBody
}

func PostPrefillStreamProcessResponse(res *http.Response, ctx *PipelineContext, phase Phase) (rsError *errs.RSError, details *utils.InstanceFailureDetails) {
	var bodyBytes []byte
	var err error
	if ctx.RequestContext.ParsedRequest.IsStream() && ctx.IsChat() {
		var finishReason string
		instanceId := ctx.RequestContext.InferMsg.GetInstance(phase.RelateInstanceType())
		ctx.GetRequestCtx().IsFirstTokenFromPrefill.Store(true)

		if ctx.HasMainErr() {
			return nil, nil
		}
		ctx.GetGinCtxMutex().Lock()
		writerResponseHeaders(res, ctx)
		// ForceReuseFirstToken is now atomic.Bool, pass its pointer
		passTokenTime := handler.GetPassTokenTime(ctx.IsChat(), ctx.IsLayerWise, ctx.RequestContext.ParsedRequest.GetMaxToken(), ctx.GetRequestCtx().IsFirstTokenFromPrefill.Load())
		if ctx.IsLayerWise {
			passTokenTime++
		}
		bodyBytes, rsError, details, finishReason = handler.WritePrefillStreamRes(ctx.GetRequestCtx().MainGinCtx, ctx.RequestContext.ParsedRequest, res,
			ctx.RequestContext.Tracer.Tracer, ctx.GetPerfMetrics(), instanceId, phase.StageName(), ctx.InferManager.WorkerEventCollector, passTokenTime, ctx.RequestContext.ForceReuseFirstToken)
		if details != nil {
			ctx.FinishReason = finishReason
			// return non-stream error, reset the Header
			ctx.GetRequestCtx().MainGinCtx.Writer.Header().Set("Content-Type", "application/json")
			ctx.GetGinCtxMutex().Unlock()
			return rsError, details
		}
		// stream response is start now
		ctx.GetRequestCtx().IsStreamResponse = true
		ctx.GetGinCtxMutex().Unlock()
	} else {
		bodyBytes, err = io.ReadAll(res.Body)
		if err != nil {
			return wrapInternalPostResponseFailures(res.StatusCode, phase, fmt.Errorf("read body failed during prefill stage, err = %v", err))
		}
	}

	if ctx.IsLayerWise {
		// in layerwise mode, the prefill token always skip in decode
		ctx.GetRequestCtx().IsFirstTokenFromPrefill.Store(true)
		// unless it forced reuse first token
		if !ctx.GetRequestCtx().ForceReuseFirstToken.Load() {
			ctx.GetRequestCtx().IsFirstTokenFromPrefill.Store(false)
		}
	}

	rawResJson, err := utils.NewJSONModifier(bodyBytes)
	if err != nil {
		return wrapInternalPostResponseFailures(res.StatusCode, phase, fmt.Errorf("failed to unmarshal prefill response body: %v", err))
	}
	if usageRawMap, exists := rawResJson.GetObject(openai.UsageKey); exists {
		if promptLen, valid := utils.TryParseInt(usageRawMap[openai.PromptTokensKey]); valid {
			ctx.RequestContext.UsagePromptLen.Store(int32(promptLen))
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
		ctx.RequestContext.ParsedRequest.KvTransferParams = transParams
	} else {
		if !ctx.IsLayerWise {
			logger.Log().Warnf("prefill res for request [%s] don't contains [%s]", ctx.RequestId(), openai.KvTransferParamsKey)
			if ctx.RequestContext.ParsedRequest.IsStream() && ctx.IsChat() {
				return wrapInternalPostResponseFailures(res.StatusCode, phase, fmt.Errorf(" kv transfer params is null"))
			}
		}
	}
	ctx.RequestContext.ParsedRequest.SetFirstTokenBody(bodyBytes)

	return nil, nil
}

func FirstTokenReport(res *http.Response, ctx *PipelineContext, phase Phase) (rsError *errs.RSError, details *utils.InstanceFailureDetails) {
	ctx.SetFirstTokenSend()
	return nil, nil
}

func FirstTokenWait(res *http.Response, ctx *PipelineContext, phase Phase) (rsError *errs.RSError, details *utils.InstanceFailureDetails) {
	select {
	case <-ctx.FirstTokenSendChannel:
	}
	return nil, nil
}
