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

package infer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"infer-dp-scheduler/pkg/common/client"
	commoncfg "infer-dp-scheduler/pkg/common/config"
	"infer-dp-scheduler/pkg/common/constant"
	"infer-dp-scheduler/pkg/common/errs"
	"infer-dp-scheduler/pkg/common/health"
	utils2 "infer-dp-scheduler/pkg/common/infer"
	"infer-dp-scheduler/pkg/common/logger"
	"infer-dp-scheduler/pkg/common/openai"
	"infer-dp-scheduler/pkg/common/trace"
	"infer-dp-scheduler/pkg/common/utils"
	"infer-dp-scheduler/pkg/route-server/handler"
	"infer-dp-scheduler/pkg/route-server/handler/request"
	"infer-dp-scheduler/pkg/route-server/instances"
	"infer-dp-scheduler/pkg/route-server/metrics"
	"infer-dp-scheduler/pkg/route-server/scheduler"

	"github.com/gin-gonic/gin"
)

const (
	readBufferSize = 8 * 1024
)

type VllmPDSeparateInfer struct {
	runEncode              bool
	runPrefill             bool
	cfg                    *commoncfg.ServerConfig
	schedulerMgr           scheduler.Manager
	instanceMgr            instances.Manager
	resHeaderBlackSet      map[string]bool
	runningRequestsManager *handler.RunningRequestsManager
	workerEventCollector   *health.WorkerEventCollector
}

func NewPDSeparateInfer(instanceMgr instances.Manager, schedulerMgr scheduler.Manager,
	cfg commoncfg.ServerConfig, workerEventCollector *health.WorkerEventCollector, runningRequestsManager *handler.RunningRequestsManager) utils2.Inference {
	metrics.RegisterMetric("PDInfer", "PrefillCost")
	metrics.RegisterMetric("PDInfer", "FirstTokenCost")
	metrics.RegisterMetric("PDInfer", "DecodeCost")
	resHeaderBlackSet := commoncfg.NewResHeaderBlackSet(cfg.ResponseHeaderBlacklist)
	vllmPDSeparateInfer := &VllmPDSeparateInfer{
		schedulerMgr:           schedulerMgr,
		instanceMgr:            instanceMgr,
		cfg:                    &cfg,
		resHeaderBlackSet:      resHeaderBlackSet,
		workerEventCollector:   workerEventCollector,
		runningRequestsManager: runningRequestsManager,
	}
	var phases []string
	if cfg.InferMode == constant.EPDMode || cfg.InferMode == constant.EPDStreamMode {
		vllmPDSeparateInfer.runPrefill = true
		vllmPDSeparateInfer.runEncode = true
		phases = []string{constant.Encode, constant.Prefill, constant.Decode}
	} else if cfg.InferMode == constant.HybridMode {
		vllmPDSeparateInfer.runPrefill = false
		vllmPDSeparateInfer.runEncode = false
		phases = []string{constant.Decode}
	} else if cfg.InferMode == constant.EHybridMode {
		vllmPDSeparateInfer.runPrefill = false
		vllmPDSeparateInfer.runEncode = true
		phases = []string{constant.Encode, constant.Decode}
	} else {
		vllmPDSeparateInfer.runPrefill = true
		vllmPDSeparateInfer.runEncode = false
		phases = []string{constant.Prefill, constant.Decode}
	}
	runningRequestsManager.SetPhases(cfg.InferMode, phases)
	return vllmPDSeparateInfer
}

func (i *VllmPDSeparateInfer) CompletionsInfer(ctx *gin.Context) {
	i.PdInfer(ctx, handler.CompletionsPath, false, "", "", "", false)
}

func (i *VllmPDSeparateInfer) ChatCompletionsInfer(ctx *gin.Context) {
	i.PdInfer(ctx, handler.ChatCompletionsPath, true, "", "", "", false)
}

// MessagesInfer returns an error because the legacy v0 inference mode does not
// support the Anthropic /v1/messages endpoint; pipeline mode must be used.
func (i *VllmPDSeparateInfer) MessagesInfer(ctx *gin.Context) {
	logger.Log().Warnf("v0 inference mode does not support v1/messages interface, using pipeline mode")
	ctx.JSON(http.StatusNotImplemented, gin.H{
		"error": "MessagesInfer is not supported by VllmPDSeparateInfer",
		"code":  "METHOD_NOT_SUPPORTED",
	})
}

func (i *VllmPDSeparateInfer) TestInstance(ctx *gin.Context, path string, isChat bool, instance *client.Instance) (
	successToCheck bool, testSuccess bool, errDetails *errs.RSError) {
	var inferError *errs.RSError
	targetStage, exists := constant.FailureRoleStageMap[instance.Stats.GetRole()]
	if !exists {
		logger.Log().Warnf("skip prob stage for [%s] instance [%s] no known related execute stage is found", instance.Stats.GetRole(), instance.ID)
		return true, true, nil
	}

	successToCheck = utils.InMap(targetStage, constant.StageMap[i.cfg.InferMode])
	// Probe stage is not in the current deployment scope
	if !successToCheck {
		logger.Log().Warnf("skip prob stage for [%s] instance [%s] because serving mode is [%s]", instance.Stats.GetRole(), instance.ID, i.cfg.InferMode)
		return true, true, nil
	}

	switch instance.Stats.GetRole() {
	case constant.Prefill:
		inferError = i.PdInfer(ctx, handler.ChatCompletionsPath, isChat, "", instance.ID, "", true)
	case constant.Decode:
		inferError = i.PdInfer(ctx, handler.ChatCompletionsPath, isChat, "", "", instance.ID, true)
	case constant.Encode:
		inferError = i.PdInfer(ctx, handler.ChatCompletionsPath, isChat, instance.ID, "", "", true)
	default:
		return false, false, errs.NewRSMaError(
			errs.BadReqErr.BuildArgs(fmt.Sprintf("unknown instance role")), constant.StagePrepare)
	}

	// No error, probe successful
	if inferError == nil {
		return true, true, nil
	}

	// Determine if probe is successful
	successToCheck, testSuccess = handler.IsTargetPhasePassed(targetStage,
		constant.StageChain[i.cfg.InferMode], instance.FaultType, inferError)
	return successToCheck, testSuccess, inferError
}

func (i *VllmPDSeparateInfer) PdInfer(ctx *gin.Context, path string, isChat bool, encodeID string, prefillID string, decodeID string, isTesting bool) *errs.RSError {
	metrics.UpdateRunningRequests(constant.RequestSrcUser, 1)
	defer metrics.UpdateRunningRequests(constant.RequestSrcUser, -1)
	httpReq := ctx.Request
	if httpReq == nil {
		ctx.JSON(errs.EmptyReqErr.Status, errs.EmptyReqErr.Json())
		if !isTesting {
			metrics.RecordFailedRequestStatus(strconv.Itoa(errs.EmptyReqErr.Status), path)
		}
		return errs.NewRSMaError(errs.BadReqErr.BuildArgs(fmt.Sprintf(
			"http request is empty")), constant.StagePrepare)
	}
	perfMetrics := handler.PerfMetrics{
		StartTime: time.Now(),
	}

	var err error
	var generateReq *request.GenerateRequestImpl
	if i.runEncode {
		generateReq, err = request.NewMMGenerateRequestImpl(ctx.Request, constant.Prefill, isChat)
	} else {
		generateReq, err = request.NewGenerateRequestImpl(ctx.Request, constant.Prefill, isChat)
	}
	if err != nil {
		errRs := errs.NewRSMaError(errs.BadReqErr.BuildArgs(
			fmt.Sprintf("fail to create generate request: %v", err)), constant.StagePrepare)
		ctx.JSON(http.StatusBadRequest, errRs.MaErr.Json())
		return errRs
	}
	requestId := request.GetOrGenerateRequestId(ctx, isChat, generateReq.BodyReqId)
	generateReq.SetRequestId(request.GetInferRequestId(&ctx.Request.Header, generateReq.IsChat()))

	tracer := trace.NewTracer(ctx, i.cfg.TraceCfg, requestId, httpReq.Header)
	inferMsg := health.NewInferMsg(isTesting, encodeID, prefillID, decodeID)
	extraInfo := request.RewriteRequestIDDetail(ctx)
	tracer.E2ECtx = tracer.Tracer.StartSpan(ctx.Request.Context(), trace.RequestSpanE2E, extraInfo, trace.GtLogPermission)
	rsErr, finishReason := i.doPdInfer(ctx, path, isChat, &perfMetrics, tracer, inferMsg, generateReq)
	if rsErr != nil {
		retCode := handler.ProcessRequestError(ctx, rsErr, tracer.Tracer, finishReason)
		if retCode != 0 && !isTesting {
			metrics.RecordFailedRequestStatus(strconv.Itoa(retCode), path)
		}
		return rsErr
	}
	processSuccess(&perfMetrics, tracer.Tracer, finishReason)
	if !isTesting {
		metrics.RecordFailedRequestStatus(strconv.Itoa(http.StatusOK), path)
	}
	return nil
}

func processSuccess(perfMetrics *handler.PerfMetrics, tracer trace.RequestTracer, finishReason string) {
	firstDTokenLatencyMs, secondDTokenLatencyMs := handler.FinishPerfMetrics(perfMetrics)
	tracer.EndSpan(trace.RequestSpanE2E, fmt.Sprintf("success to infer request, finish_reason = %s, firstDTokenLatency is %d ms, secondDTokenLatency is %d ms", finishReason, firstDTokenLatencyMs, secondDTokenLatencyMs), trace.GtLogPermission)
	perfMetrics.ReportMetrics()
}

func (i *VllmPDSeparateInfer) prepareRequest(ctx *gin.Context, isChat bool, tracer trace.TracerWithContext, generateReq *request.GenerateRequestImpl) (*errs.RSError, string, *request.GenerateRequestImpl, *handler.ReqCancelCtx) {
	var rsErr *errs.RSError
	var err error
	if generateReq == nil {
		if i.runEncode {
			generateReq, err = request.NewMMGenerateRequestImpl(ctx.Request, constant.Prefill, isChat)
		} else {
			generateReq, err = request.NewGenerateRequestImpl(ctx.Request, constant.Prefill, isChat)
		}
		if err != nil {
			rsErr = errs.NewRSMaError(errs.BadReqErr.BuildArgs(
				fmt.Sprintf("fail to create generate request: %v", err)), constant.StagePrepare)
			return rsErr, constant.UnKnownReason, nil, nil
		}
	}
	reqId := generateReq.GetRequestId()
	cancelCtx, err := i.runningRequestsManager.NewReqCancelCtx(reqId, tracer.Tracer)
	if err != nil {
		rsErr = errs.NewRSMaError(errs.TooManyRequestError.BuildArgs(
			fmt.Sprintf("fail to schedule generate request: %v", err)), constant.StagePrepare)
		return rsErr, constant.UnKnownReason, nil, nil
	}
	if cancelCtx.IsCancel.Load() {
		rsErr = errs.NewRSMaError(&errs.RequestForcedCancelledErr, constant.StagePrepare)
		i.runningRequestsManager.DeleteReqCancelCtx(reqId)
		return rsErr, constant.UnKnownReason, nil, nil
	}
	return nil, "", generateReq, cancelCtx
}

func (i *VllmPDSeparateInfer) doPdInfer(ctx *gin.Context, path string, isChat bool, perfMetrics *handler.PerfMetrics,
	tracer trace.TracerWithContext, inferMsg *health.InferMsg, generateReq *request.GenerateRequestImpl) (*errs.RSError, string) {
	var rsErr *errs.RSError

	// 1.prepare request
	rsErr, finishReason, generateReq, cancelCtx := i.prepareRequest(ctx, isChat, tracer, generateReq)
	if rsErr != nil {
		return rsErr, finishReason
	}
	defer i.runningRequestsManager.DeleteReqCancelCtx(generateReq.GetRequestId())
	metrics.RecordScheduledRequest()
	perfMetrics.EncodeFinish = perfMetrics.StartTime

	// 2. encode stage
	if i.runEncode {
		encodeId, rsErr, details := i.sendEncoderReq(ctx, generateReq, path, perfMetrics, tracer, inferMsg, cancelCtx)
		if encodeId == "" {
			return rsErr, constant.UnKnownReason
		} else if rsErr != nil {
			handler.ReportInstanceFailure(details, inferMsg, generateReq, i.workerEventCollector, i.schedulerMgr)
			return rsErr, constant.UnKnownReason
		} else {
			i.workerEventCollector.OnRequestSuccess(generateReq, inferMsg)
		}
		if cancelCtx.IsCancel.Load() {
			rsErr = errs.NewRSMaError(&errs.RequestForcedCancelledErr, constant.StageEncode)
			return rsErr, constant.UnKnownReason
		}
		perfMetrics.EncodeFinish = time.Now()
		encodeTime := perfMetrics.EncodeFinish.Sub(perfMetrics.StartTime).Seconds()
		tracer.Tracer.Event("", trace.TraceEventEncodeDone, fmt.Sprintf("execute instance [%s], cost [%v] sec", encodeId, encodeTime))
	}

	// 3. prefill stage
	var prefillId string
	var details *utils.InstanceFailureDetails
	if i.runPrefill {
		prefillId, rsErr, details = i.sendPrefillReq(ctx, generateReq, path, perfMetrics, tracer, inferMsg, cancelCtx)
		if prefillId == "" {
			return rsErr, constant.UnKnownReason
		} else if rsErr != nil {
			handler.ReportInstanceFailure(details, inferMsg, generateReq, i.workerEventCollector, i.schedulerMgr)
			return rsErr, constant.UnKnownReason
		} else {
			i.workerEventCollector.OnRequestSuccess(generateReq, inferMsg)
		}
		perfMetrics.PrefillFinish = time.Now()
		prefillTime := perfMetrics.PrefillFinish.Sub(perfMetrics.EncodeFinish).Seconds()
		tracer.Tracer.Event("", trace.TraceEventPrefillDone, fmt.Sprintf("execute instance [%s], cost [%v] sec", prefillId, prefillTime))
		if cancelCtx.IsCancel.Load() {
			rsErr = errs.NewRSMaError(&errs.RequestForcedCancelledErr, constant.StagePrepare)
			return rsErr, constant.UnKnownReason
		}
	} else {
		prefillId = ""
	}

	// 4.1 decode stage of infer
	perfMetrics.DecodeStart = perfMetrics.PrefillFinish
	decodeId, res, rsErr, details := i.sendDecodeReq(ctx, generateReq, path, tracer, inferMsg, cancelCtx, prefillId)
	defer func() {
		metrics.UpdateRunningRequests(constant.Decode, -1)
		if res != nil {
			if closeErr := res.Body.Close(); closeErr != nil {
				logger.Log().Warnf("fail to close decode response body for req %s: %v", generateReq.GetRequestId(), closeErr)
			}
		}
	}()
	inferMsg.SetInstance(constant.Decode, decodeId)
	if decodeId == "" {
		return rsErr, constant.UnKnownReason
	} else if rsErr != nil {
		handler.ReportInstanceFailure(details, inferMsg, generateReq, i.workerEventCollector, i.schedulerMgr)
		return rsErr, constant.UnKnownReason
	}

	// 4.2 decode stage of writing
	usage, errDetails, finishReason := i.writeDecodeRes(ctx, generateReq, res, tracer.Tracer, perfMetrics, decodeId)
	if errDetails != nil {
		handler.ReportInstanceFailure(errDetails, inferMsg, generateReq, i.workerEventCollector, i.schedulerMgr)
		tracer.Tracer.ErrorSpan(trace.RequestSubProcess, fmt.Errorf("%s", errDetails.ErrorMessage), trace.EnableOtelTrace)
		tracer.Tracer.ErrorSpan(trace.RequestDecode, fmt.Errorf("failed to wtrite response"), trace.EnableOtelTrace)
		return handler.RsErrorFromResponse(generateReq.IsStream(), errDetails, decodeId, constant.StageDecode), finishReason
	} else {
		i.workerEventCollector.OnRequestSuccess(generateReq, inferMsg)
	}
	tracer.Tracer.EndSpan(trace.RequestSubProcess, "success to write response", trace.EnableOtelTrace)
	tracer.Tracer.EndSpan(trace.RequestDecode, "success to exec prefill stage", trace.EnableOtelTrace)

	// 5. post process
	cancelCtx.RecordEndPhase(constant.Decode)
	i.handlePostProcess(perfMetrics, tracer, usage, decodeId, generateReq.IsStream())

	return nil, finishReason
}

func (i *VllmPDSeparateInfer) handlePostProcess(perfMetrics *handler.PerfMetrics, tracer trace.TracerWithContext, usage *openai.Usage, decodeId string, isStream bool) {
	perfMetrics.FinishTime = time.Now()
	duration := perfMetrics.FinishTime.Sub(perfMetrics.StartTime)
	metrics.RecordRequestDuration(duration.Seconds())
	// Calculate decode duration
	var decodeDuration float64
	if isStream {
		decodeDuration = perfMetrics.FinishTime.Sub(perfMetrics.FirstToken).Seconds()
	} else {
		decodeDuration = perfMetrics.FinishTime.Sub(perfMetrics.DecodeStart).Seconds()
	}
	// Record to Histogram
	var tpot float64 = 0
	if usage != nil && usage.CompletionTokens > 1 {
		tpot = decodeDuration / float64(usage.CompletionTokens-1)
		metrics.RecordTPOT(tpot, usage.CompletionTokens-1)
	}
	ttft := perfMetrics.FirstToken.Sub(perfMetrics.StartTime).Seconds()
	metrics.RecordTTFT(ttft)
	metrics.RecordSuccessedRequest()
	tracer.Tracer.Event("", trace.TraceEventDecodeDone, fmt.Sprintf("execute instance [%s], cost [%v] sec", decodeId, decodeDuration))
	tracer.Tracer.TraceTokenLog("", usage, ttft, tpot, perfMetrics.FinishTime.Sub(perfMetrics.StartTime).Seconds())
}

func (i *VllmPDSeparateInfer) sendEncoderReq(ctx *gin.Context, generateReq *request.GenerateRequestImpl,
	path string, perfMetrics *handler.PerfMetrics, tracer trace.TracerWithContext, inferMsg *health.InferMsg,
	cancelCtx *handler.ReqCancelCtx) (string, *errs.RSError, *utils.InstanceFailureDetails) {
	cancelCtx.RecordNewPhase(constant.Encode)
	defer cancelCtx.RecordEndPhase(constant.Encode)
	metrics.UpdateRunningRequests(constant.Encode, 1)
	encodeIns := ""
	defer func() {
		metrics.UpdateRunningRequests(constant.Encode, -1)
		inferMsg.SetInstance(constant.Encode, encodeIns)
	}()
	tracer.EncodeCtx = tracer.Tracer.StartSpan(tracer.E2ECtx, trace.RequestEncode, "", trace.EnableOtelTrace)
	generateReq.PrepareEncoder()
	encoderBody, err := generateReq.GetEncoderBody()
	var rsErr *errs.RSError
	if err != nil {
		tracer.Tracer.ErrorSpan(trace.RequestEncode, fmt.Errorf("fail to get encoder req body err:%v", err), trace.EnableOtelTrace)
		rsErr = errs.NewRSMaError(errs.BadReqErr.BuildArgs(
			fmt.Sprintf("fail to get encoder req body err: %v", err)), constant.StageEncode)
		return "", rsErr, utils.DetailsFromError(0, nil)
	}
	tracer.Tracer.StartSpan(tracer.EncodeCtx, trace.RequestDispatch, trace.RequestEncode, trace.EnableOtelTrace)

	var sloTag string
	targetEncodeId := inferMsg.GetInstance(constant.Encode)
	if targetEncodeId == "" {
		var skipped bool
		scheduleRes, err := i.schedulerMgr.DispatchRequest(ctx.Request.Context(), generateReq, nil, tracer.Tracer, "", scheduler.RemoteRequestInfo{IsLocal: true})
		if err != nil {
			rsErr = errs.NewRSMaError(errs.InternalErr.BuildArgs(
				fmt.Sprintf("fail to schedule encode instances: %v", err)), constant.StageEncode)
			return "", rsErr, utils.DetailsFromError(0, nil)
		}
		encodeIns = scheduleRes.Result
		skipped = scheduleRes.Skipped
		sloTag = scheduleRes.SLOTag
		generateReq.SetSLOTag(sloTag)
		if skipped {
			logger.Log().Infof("request %v skip some instances during encode scheduling due to link blacklist", generateReq.GetRequestId())
		}
	} else {
		_, err = i.instanceMgr.GetInstanceClient(targetEncodeId)
		if err != nil {
			rsErr = errs.NewRSMaError(errs.InternalErr.BuildArgs(
				fmt.Sprintf("target encode instance %s not exists", targetEncodeId)), constant.StageEncode)
			return "", rsErr, utils.DetailsFromError(0, nil)
		}
		// request for a certain target instance use the default slo tag
		generateReq.SetSLOTag(constant.SLOTagDefault)
		encodeIns = targetEncodeId
	}
	tracer.Tracer.EndSpan(trace.RequestDispatch, fmt.Sprintf("request [%s] successed to schedule", generateReq.GetRequestId()), trace.EnableOtelTrace)
	if encodeIns != "" {
		encodeIns, err, details := i.doEncodeReq(ctx, generateReq, path, perfMetrics, tracer, inferMsg, cancelCtx, encodeIns, encoderBody)
		return encodeIns, err, details
	} else {
		rsErr = errs.NewRSMaError(errs.ServiceUnavailableErr.BuildArgs(constant.Encode,
			fmt.Sprintf("fail to schedule encode request, reason: no encode instance returned by scheduler")),
			constant.StageEncode)
		tracer.Tracer.ErrorSpan(trace.RequestSubProcess, fmt.Errorf("fail to schedule encode request, reason: no encode instance returned by scheduler"), trace.EnableOtelTrace)
		tracer.Tracer.ErrorSpan(trace.RequestEncode, fmt.Errorf("failed to exec encode stage, detailed error recored in stage of encode"), trace.EnableOtelTrace)
		return "", rsErr, utils.DetailsFromError(0, nil)
	}
}

func (i *VllmPDSeparateInfer) sendPrefillReq(ctx *gin.Context, generateReq *request.GenerateRequestImpl,
	path string, perfMetrics *handler.PerfMetrics, tracer trace.TracerWithContext, inferMsg *health.InferMsg,
	cancelCtx *handler.ReqCancelCtx) (string, *errs.RSError, *utils.InstanceFailureDetails) {
	cancelCtx.RecordNewPhase(constant.Prefill)
	defer cancelCtx.RecordEndPhase(constant.Prefill)
	metrics.UpdateRunningRequests(constant.Prefill, 1)
	prefillIns := ""
	defer func() {
		metrics.UpdateRunningRequests(constant.Prefill, -1)
		inferMsg.SetInstance(constant.Prefill, prefillIns)
	}()
	tracer.PrefillCtx = tracer.Tracer.StartSpan(tracer.E2ECtx, trace.RequestPrefill, "", trace.EnableOtelTrace)
	generateReq.PreparePrefill()
	prefillBody, err := generateReq.GetPrefillBody()
	var rsErr *errs.RSError
	if err != nil {
		tracer.Tracer.ErrorSpan(trace.RequestPrefill, fmt.Errorf("fail to get prefill req body err:%v", err), trace.EnableOtelTrace)
		rsErr = errs.NewRSMaError(errs.BadReqErr.BuildArgs(
			fmt.Sprintf("fail to get prefill req body err: %v", err)), constant.StagePrefill)
		return "", rsErr, utils.DetailsFromError(0, nil)
	}
	tracer.Tracer.StartSpan(tracer.PrefillCtx, trace.RequestDispatch, trace.RequestPrefill, trace.EnableOtelTrace)
	var sloTag string
	targetPrefillId := inferMsg.GetInstance(constant.Prefill)
	if targetPrefillId == "" {
		var skipped bool
		previousEncodeId := inferMsg.GetInstance(constant.Encode)
		scheduleRes, err := i.schedulerMgr.DispatchRequest(ctx.Request.Context(), generateReq, nil, tracer.Tracer, previousEncodeId, scheduler.RemoteRequestInfo{IsLocal: true})
		if err != nil {
			rsErr = errs.NewRSMaError(errs.InternalErr.BuildArgs(
				fmt.Sprintf("fail to schedule prefill instances: %v", err)), constant.StagePrefill)
			return "", rsErr, utils.DetailsFromError(0, nil)
		}
		prefillIns = scheduleRes.Result
		skipped = scheduleRes.Skipped
		sloTag = scheduleRes.SLOTag
		generateReq.SetSLOTag(sloTag)
		if skipped {
			logger.Log().Infof("request %v skip some instances during prefill scheduling due to link blacklist", generateReq.GetRequestId())
		}
	} else {
		_, err = i.instanceMgr.GetInstanceClient(targetPrefillId)
		if err != nil {
			rsErr = errs.NewRSMaError(errs.InternalErr.BuildArgs(
				fmt.Sprintf("target prefill instance %s not exists", targetPrefillId)), constant.StagePrefill)
			return "", rsErr, utils.DetailsFromError(0, nil)
		}
		// request for a certain target instance use the default slo tag
		generateReq.SetSLOTag(constant.SLOTagDefault)
		prefillIns = targetPrefillId
	}
	tracer.Tracer.EndSpan(trace.RequestDispatch, fmt.Sprintf("request [%s] successed to schedule", generateReq.GetRequestId()), trace.EnableOtelTrace)
	if prefillIns != "" {
		prefillIns, err, details := i.doPrefillReq(ctx, generateReq, path, perfMetrics, tracer, inferMsg, cancelCtx, prefillIns, prefillBody)
		return prefillIns, err, details
	} else {
		rsErr = errs.NewRSMaError(errs.ServiceUnavailableErr.BuildArgs(constant.Prefill,
			fmt.Sprintf("fail to schedule prefill request, reason: no prefill instance returned by scheduler")),
			constant.StagePrefill)
		tracer.Tracer.ErrorSpan(trace.RequestSubProcess, fmt.Errorf("fail to schedule prefill request, reason: no prefill instance returned by scheduler"), trace.EnableOtelTrace)
		tracer.Tracer.ErrorSpan(trace.RequestPrefill, fmt.Errorf("failed to exec prefill stage, detailed error recored in stage of forward"), trace.EnableOtelTrace)
		return "", rsErr, utils.DetailsFromError(0, nil)
	}
}

func (i *VllmPDSeparateInfer) doEncodeReq(ctx *gin.Context, generateReq *request.GenerateRequestImpl, path string,
	perfMetrics *handler.PerfMetrics, tracer trace.TracerWithContext, inferMsg *health.InferMsg, cancelCtx *handler.ReqCancelCtx, id string,
	encodeBody io.Reader) (string, *errs.RSError, *utils.InstanceFailureDetails) {
	tracer.Tracer.StartSpan(tracer.EncodeCtx, trace.RequestSubProcess, trace.RequestPrefill, trace.EnableOtelTrace)
	var err error
	var rsErr *errs.RSError
	var details *utils.InstanceFailureDetails
	var res *http.Response
	defer func() {
		if res == nil {
			return
		}
		if err = res.Body.Close(); err != nil {
			logger.Log().Errorf("fail to close response body: %v", err)
		}
	}()

	insClient, preErr := i.instanceMgr.GetInstanceClient(id)
	if preErr != nil {
		logger.Log().Errorf("fail to get instance client for %v, err:%v", id, preErr)
		rsErr = errs.NewRSMaError(errs.BadGatewayErr.BuildArgs(
			fmt.Sprintf("fail to get instance client for %v, err: %v", id, preErr)), constant.StageEncode)
		tracer.Tracer.ErrorSpan(trace.RequestSubProcess, fmt.Errorf("fail to get instance client for %v, err:%v", id, preErr), trace.EnableOtelTrace)
		tracer.Tracer.ErrorSpan(trace.RequestEncode, fmt.Errorf("failed to exec encode stage, detail information recored in stage of encode"), trace.EnableOtelTrace)
		return id, rsErr, utils.DetailsFromError(0, nil)
	}
	inferMsg.SetInstance(constant.Encode, id)
	i.workerEventCollector.OnRequestNew(generateReq, inferMsg)
	encodeCtx := cancelCtx.WrapContext(tracer.EncodeCtx, id, constant.Encode)
	traceEventMsg := fmt.Sprintf("send request to encode instance [%s] [%s], slo tag is [%s], stream = [%v], reqLoad = [%v]", id, insClient.GetEndpoint(), generateReq.GetSLOTag(), false, generateReq.GetPromptTokenCount())
	tracer.Tracer.Event("", trace.TraceEventEncodeStart, traceEventMsg)
	res, rsErr, details = insClient.Generate(encodeCtx, generateReq.GetHeader(), path, encodeBody, id, constant.StageEncode, generateReq.GetRequestId(), generateReq.IsMessages())
	if rsErr != nil {
		if errors.Is(encodeCtx.Err(), context.Canceled) {
			tracer.Tracer.Event("", trace.TraceEventUserCancelled,
				fmt.Sprintf("user request cancelled on send encode req to [%s]", id))
			tracer.Tracer.EndSpan(trace.RequestSubProcess, "user request cancelled", trace.EnableOtelTrace)
			tracer.Tracer.EndSpan(trace.RequestEncode, "user request cancelled during encode stage", trace.EnableOtelTrace)
			if cancelCtx.IsCancel.Load() {
				return id, errs.NewRSMaError(&errs.RequestForcedCancelledErr, constant.StageEncode), utils.DetailsFromError(0, encodeCtx.Err())
			}
			return id, errs.NewRSMaError(&errs.RequestCancelledByUserErr, constant.StageEncode), utils.DetailsFromError(0, encodeCtx.Err())
		}
		if rsErr.EngineErr != nil {
			logger.Log().Warnf(
				"encode request [%s] for %v returns non 200 res, status code: %d, body = %s",
				id, generateReq.GetRequestId(), rsErr.EngineErr.EngineStatus, string(rsErr.EngineErr.EngineErrBody))
			tracer.Tracer.ErrorSpan(trace.RequestSubProcess, fmt.Errorf("encode request [%s] for %v returns non 200 res, status code: %d, body = %s",
				id, generateReq.GetRequestId(), rsErr.EngineErr.EngineStatus, string(rsErr.EngineErr.EngineErrBody)), trace.EnableOtelTrace)
		} else {
			logger.Log().Warnf("fail to send encode request [%s] for %v, err:generate request status code: %d, %v",
				id, generateReq.GetRequestId(), rsErr.MaErr.Status, rsErr.MaErr.Error())
			tracer.Tracer.ErrorSpan(trace.RequestSubProcess, fmt.Errorf("fail to send encode request [%s] for %v, err:generate request status code: %d, %v",
				id, generateReq.GetRequestId(), rsErr.MaErr.Status, rsErr.MaErr.Error()), trace.EnableOtelTrace)
		}
		tracer.Tracer.ErrorSpan(trace.RequestEncode, fmt.Errorf("encode request failed, detailed error recored in stage of encode"), trace.EnableOtelTrace)
		return id, rsErr, details
	}

	if err = generateReq.ParseEncodeRespAndResetPromptLen(res); err != nil {
		rsErr = errs.NewRSMaError(errs.InternalErr.BuildArgs(
			fmt.Sprintf("failed to parse encode response, err=%v", err)), constant.StageEncode)
		tracer.Tracer.ErrorSpan(trace.RequestSubProcess, err, trace.EnableOtelTrace)
		tracer.Tracer.ErrorSpan(trace.RequestEncode, err, trace.EnableOtelTrace)
		return id, rsErr, utils.DetailsFromError(res.StatusCode, nil)
	}

	tracer.Tracer.EndSpan(trace.RequestSubProcess, "success to exec stage of encode stage", trace.EnableOtelTrace)
	tracer.Tracer.EndSpan(trace.RequestEncode, "success to exec encode stage", trace.EnableOtelTrace)
	return id, nil, nil
}

func (i *VllmPDSeparateInfer) doPrefillReq(ctx *gin.Context, generateReq *request.GenerateRequestImpl, path string,
	perfMetrics *handler.PerfMetrics, tracer trace.TracerWithContext, inferMsg *health.InferMsg, cancelCtx *handler.ReqCancelCtx, id string,
	prefillBody io.Reader) (string, *errs.RSError, *utils.InstanceFailureDetails) {
	tracer.Tracer.StartSpan(tracer.PrefillCtx, trace.RequestSubProcess, trace.RequestPrefill, trace.EnableOtelTrace)
	var err error
	var rsErr *errs.RSError
	var details *utils.InstanceFailureDetails
	var res *http.Response
	defer func() {
		if res == nil {
			return
		}
		if err = res.Body.Close(); err != nil {
			logger.Log().Errorf("fail to close response body: %v", err)
		}
	}()

	insClient, preErr := i.instanceMgr.GetInstanceClient(id)
	if preErr != nil {
		logger.Log().Errorf("fail to get instance client for %v, err:%v", id, preErr)
		rsErr = errs.NewRSMaError(errs.BadGatewayErr.BuildArgs(
			fmt.Sprintf("fail to get instance client for %v, err: %v", id, preErr)), constant.StagePrefill)
		tracer.Tracer.ErrorSpan(trace.RequestSubProcess, fmt.Errorf("fail to get instance client for %v, err:%v", id, preErr), trace.EnableOtelTrace)
		tracer.Tracer.ErrorSpan(trace.RequestPrefill, fmt.Errorf("failed to exec prefill stage, detail information recored in stage of forward"), trace.EnableOtelTrace)
		return id, rsErr, utils.DetailsFromError(0, nil)
	}
	inferMsg.SetInstance(constant.Prefill, id)
	i.workerEventCollector.OnRequestNew(generateReq, inferMsg)
	prefillCtx := cancelCtx.WrapContext(tracer.PrefillCtx, id, constant.Prefill)
	traceEventMsg := fmt.Sprintf("send request to prefill instance [%s] [%s], slo tag is [%s], stream = [%v], reqLoad = [%v]", id, insClient.GetEndpoint(), generateReq.GetSLOTag(), false, generateReq.GetPromptTokenCount())
	tracer.Tracer.Event("", trace.TraceEventPrefillStart, traceEventMsg)
	res, rsErr, details = insClient.Generate(prefillCtx, generateReq.GetHeader(), path, prefillBody, id, constant.StagePrefill, generateReq.GetRequestId(), generateReq.IsMessages())
	if rsErr != nil {
		if errors.Is(prefillCtx.Err(), context.Canceled) {
			tracer.Tracer.Event("", trace.TraceEventUserCancelled,
				fmt.Sprintf("user request cancelled on send prefill req to [%s]", id))
			tracer.Tracer.EndSpan(trace.RequestSubProcess, "user request cancelled", trace.EnableOtelTrace)
			tracer.Tracer.EndSpan(trace.RequestPrefill, "user request cancelled during prefill stage", trace.EnableOtelTrace)
			if cancelCtx.IsCancel.Load() {
				return id, errs.NewRSMaError(&errs.RequestForcedCancelledErr, constant.StagePrefill), utils.DetailsFromError(0, prefillCtx.Err())
			}
			return id, errs.NewRSMaError(&errs.RequestCancelledByUserErr, constant.StagePrefill), utils.DetailsFromError(0, prefillCtx.Err())
		}
		if rsErr.EngineErr != nil {
			logger.Log().Warnf(
				"prefill request [%s] for %v returns non 200 res, status code: %d, body = %s",
				id, generateReq.GetRequestId(), rsErr.EngineErr.EngineStatus, string(rsErr.EngineErr.EngineErrBody))
			tracer.Tracer.ErrorSpan(trace.RequestSubProcess, fmt.Errorf("prefill request [%s] for %v returns non 200 res, status code: %d, body = %s",
				id, generateReq.GetRequestId(), rsErr.EngineErr.EngineStatus, string(rsErr.EngineErr.EngineErrBody)), trace.EnableOtelTrace)
		} else {
			logger.Log().Warnf("fail to send prefill request [%s] for %v, status code: %d, err = %v",
				id, generateReq.GetRequestId(), rsErr.MaErr.Status, rsErr.MaErr.Error())
			tracer.Tracer.ErrorSpan(trace.RequestSubProcess, fmt.Errorf("fail to send prefill request [%s] for %v,status code: %d, err = %v",
				id, generateReq.GetRequestId(), rsErr.MaErr.Status, rsErr.MaErr.Error()), trace.EnableOtelTrace)
		}
		tracer.Tracer.ErrorSpan(trace.RequestPrefill, fmt.Errorf("prefill request failed, detailed error recored in stage of forward"), trace.EnableOtelTrace)
		return id, rsErr, details
	}

	if err = generateReq.ParsePrefillResp(res); err != nil {
		rsErr = errs.NewRSMaError(errs.InternalErr.BuildArgs(
			fmt.Sprintf("failed to parse prefill response, err=%v", err)), constant.StagePrefill)
		tracer.Tracer.ErrorSpan(trace.RequestSubProcess, fmt.Errorf("failed to parse prefill response, err=%v", err), trace.EnableOtelTrace)
		tracer.Tracer.ErrorSpan(trace.RequestPrefill, fmt.Errorf("failed to exec prefill stage, detail information recored in stage of forward"), trace.EnableOtelTrace)
		return id, rsErr, utils.DetailsFromError(0, nil)
	}

	if generateReq.OutputFirstTokenFromPrefill() {
		if err = i.returnFirstToken(ctx, generateReq, res); err != nil {
			rsErr = errs.NewRSMaError(errs.InternalErr.BuildArgs(
				fmt.Sprintf("return first token err: %v", err)), constant.StagePrefill)
			tracer.Tracer.ErrorSpan(trace.RequestSubProcess, fmt.Errorf("return first token err:%v", err), trace.EnableOtelTrace)
			tracer.Tracer.ErrorSpan(trace.RequestPrefill, fmt.Errorf("failed to exec prefill stage, detailed error recored in stage of forward"), trace.EnableOtelTrace)
			return id, rsErr, utils.DetailsFromError(0, nil)
		}
	}
	// write FirstToken here anyway in case decode is running abnormal later
	// this will be overridden in decode first token output if everything is running fine.
	perfMetrics.FirstToken = time.Now()
	tracer.Tracer.EndSpan(trace.RequestSubProcess, "success to exec stage of prefill forward", trace.EnableOtelTrace)
	tracer.Tracer.EndSpan(trace.RequestPrefill, "success to exec prefill stage", trace.EnableOtelTrace)
	return id, nil, nil
}

func (i *VllmPDSeparateInfer) sendDecodeReq(ctx *gin.Context, generateReq *request.GenerateRequestImpl,
	path string, tracer trace.TracerWithContext, inferMsg *health.InferMsg, cancelCtx *handler.ReqCancelCtx,
	prefillId string) (string, *http.Response, *errs.RSError, *utils.InstanceFailureDetails) {
	cancelCtx.RecordNewPhase(constant.Decode)
	metrics.UpdateRunningRequests(constant.Decode, 1)
	tracer.DecodeCtx = tracer.Tracer.StartSpan(tracer.E2ECtx, trace.RequestDecode, "", trace.EnableOtelTrace)
	generateReq.PrepareDecode()
	decodeBody, err := generateReq.GetDecodeBody()
	var rsErr *errs.RSError
	if err != nil {
		tracer.Tracer.ErrorSpan(trace.RequestDecode, fmt.Errorf("fail to get decode req body err:%v", err), trace.EnableOtelTrace)
		rsErr = errs.NewRSMaError(errs.BadReqErr.BuildArgs(
			fmt.Sprintf("fail to get decode req body err: %v", err)), constant.StageDecode)
		return "", nil, rsErr, utils.DetailsFromError(0, nil)
	}
	var decodeIns string
	tracer.Tracer.StartSpan(tracer.DecodeCtx, trace.RequestDispatch, trace.RequestDecode, trace.EnableOtelTrace)
	targetDecodeId := inferMsg.GetInstance(constant.Decode)
	if targetDecodeId == "" {
		var skipped bool
		scheduleRes, err := i.schedulerMgr.DispatchRequest(ctx.Request.Context(), generateReq, nil, tracer.Tracer, prefillId, scheduler.RemoteRequestInfo{IsLocal: true})
		if err != nil {
			rsErr = errs.NewRSMaError(errs.InternalErr.BuildArgs(
				fmt.Sprintf("fail to schedule decode instances: %v", err)), constant.StageDecode)
			tracer.Tracer.ErrorSpan(trace.RequestDispatch, fmt.Errorf("fail to schedule decode instances: %v", err), trace.EnableOtelTrace)
			tracer.Tracer.ErrorSpan(trace.RequestDecode, fmt.Errorf("failed to exec decode stage, detailed error recored in stage of forward"), trace.EnableOtelTrace)
			return "", nil, rsErr, utils.DetailsFromError(0, nil)
		}
		decodeIns = scheduleRes.Result
		skipped = scheduleRes.Skipped
		generateReq.SetSLOTag(scheduleRes.SLOTag)
		if skipped {
			logger.Log().Infof("request %v skip some instances during decode scheduling due to link blacklist", generateReq.GetRequestId())
		}
	} else {
		_, err = i.instanceMgr.GetInstanceClient(targetDecodeId)
		if err != nil {
			rsErr = errs.NewRSMaError(errs.InternalErr.BuildArgs(
				fmt.Sprintf("target decode instance %s not exists", targetDecodeId)), constant.StageDecode)
			tracer.Tracer.ErrorSpan(trace.RequestDispatch, fmt.Errorf("target decode instance %s not exists", targetDecodeId), trace.EnableOtelTrace)
			tracer.Tracer.ErrorSpan(trace.RequestDecode, fmt.Errorf("failed to exec decode stage, detailed error recored in stage of forward"), trace.EnableOtelTrace)
			return "", nil, rsErr, utils.DetailsFromError(0, nil)
		}
		// request for a certain target instance use the default slo tag
		generateReq.SetSLOTag(constant.SLOTagDefault)
		decodeIns = targetDecodeId
	}
	if decodeIns != "" {
		tracer.Tracer.EndSpan(trace.RequestDispatch, fmt.Sprintf("request [%s] successed to schedule", generateReq.GetRequestId()), trace.EnableOtelTrace)
		return i.doDecodeReq(ctx, generateReq, path, tracer, inferMsg, cancelCtx, decodeIns, decodeBody)
	} else {
		rsErr = errs.NewRSMaError(errs.ServiceUnavailableErr.BuildArgs(constant.Decode,
			fmt.Sprintf("fail to schedule decode request, reason: no decode instance returned by scheduler")),
			constant.StageDecode)
		tracer.Tracer.ErrorSpan(trace.RequestDispatch, fmt.Errorf("fail to schedule decode request, reason: no decode instance returned by scheduler"), trace.EnableOtelTrace)
		tracer.Tracer.ErrorSpan(trace.RequestDecode, fmt.Errorf("failed to exec decode stage, detailed error recored in stage of forward"), trace.EnableOtelTrace)
		return "", nil, rsErr, utils.DetailsFromError(0, nil)
	}
}

func (i *VllmPDSeparateInfer) doDecodeReq(ctx *gin.Context, generateReq *request.GenerateRequestImpl,
	path string, tracer trace.TracerWithContext, inferMsg *health.InferMsg, cancelCtx *handler.ReqCancelCtx,
	id string, decodeBody io.Reader) (string, *http.Response, *errs.RSError, *utils.InstanceFailureDetails) {
	tracer.Tracer.StartSpan(tracer.DecodeCtx, trace.RequestSubProcess, trace.RequestDecode, trace.EnableOtelTrace)
	var rsErr *errs.RSError
	var details *utils.InstanceFailureDetails
	client, deErr := i.instanceMgr.GetInstanceClient(id)
	if deErr != nil {
		logger.Log().Errorf("fail to get instance client for %v, err:%v", id, deErr)
		rsErr = errs.NewRSMaError(errs.BadGatewayErr.BuildArgs(
			fmt.Sprintf("fail to get instance client for %v, err: %v", id, deErr)), constant.StageDecode)
		tracer.Tracer.ErrorSpan(trace.RequestSubProcess, fmt.Errorf("fail to get instance client for %v, err:%v", id, deErr), trace.EnableOtelTrace)
		tracer.Tracer.ErrorSpan(trace.RequestDecode, fmt.Errorf("failed to exec decode stage, detail information recored in stage of forward"), trace.EnableOtelTrace)
		return id, nil, rsErr, utils.DetailsFromError(0, nil)
	}
	inferMsg.SetInstance(constant.Decode, id)
	i.workerEventCollector.OnRequestNew(generateReq, inferMsg)
	decodeCtx := cancelCtx.WrapContext(tracer.DecodeCtx, id, constant.Decode)
	traceEventMsg := fmt.Sprintf("send request to decode instance [%s] [%s], stream = [%v], reqLoad = [%v]", id, client.GetEndpoint(), generateReq.IsStream(), generateReq.GetPromptTokenCount())
	tracer.Tracer.Event("", trace.TraceEventDecodeStart, traceEventMsg)
	var res *http.Response
	res, rsErr, details = client.Generate(decodeCtx, generateReq.GetHeader(), path, decodeBody, id, constant.StageDecode, generateReq.GetRequestId(), generateReq.IsMessages())
	if rsErr != nil {
		if errors.Is(decodeCtx.Err(), context.Canceled) {
			tracer.Tracer.Event("", trace.TraceEventUserCancelled,
				fmt.Sprintf("user request cancelled on send decode req to [%s]", id))
			tracer.Tracer.EndSpan(trace.RequestSubProcess, "user request cancelled", trace.EnableOtelTrace)
			tracer.Tracer.EndSpan(trace.RequestDecode, "user request cancelled during decode stage", trace.EnableOtelTrace)
			if cancelCtx.IsCancel.Load() {
				return id, nil, errs.NewRSMaError(&errs.RequestForcedCancelledErr, constant.StageDecode), utils.DetailsFromError(0, decodeCtx.Err())
			}
			return id, nil, errs.NewRSMaError(&errs.RequestCancelledByUserErr, constant.StageDecode), utils.DetailsFromError(0, decodeCtx.Err())
		}
		if rsErr.EngineErr != nil {
			logger.Log().Warnf(
				"decode request [%s] for %v returns non 200 res, status code: %d, body = %s",
				id, generateReq.GetRequestId(), rsErr.EngineErr.EngineStatus, string(rsErr.EngineErr.EngineErrBody))
			tracer.Tracer.ErrorSpan(trace.RequestSubProcess, fmt.Errorf("decode request [%s] for %v returns non 200 res, status code: %d, body = %s",
				id, generateReq.GetRequestId(), rsErr.EngineErr.EngineStatus, string(rsErr.EngineErr.EngineErrBody)), trace.EnableOtelTrace)
		} else {
			logger.Log().Warnf(
				"fail to send decode request [%s] for %v, err:generate request status code: %d, %v",
				id, generateReq.GetRequestId(), rsErr.MaErr.Status, rsErr.MaErr.Error())
			tracer.Tracer.ErrorSpan(trace.RequestSubProcess, fmt.Errorf("fail to send decode request [%s] for %v, err:generate request status code: %d, %v",
				id, generateReq.GetRequestId(), rsErr.MaErr.Status, rsErr.MaErr.Error()), trace.EnableOtelTrace)
		}
		tracer.Tracer.ErrorSpan(trace.RequestDecode, fmt.Errorf("fail to send decode request, detailed error recored in stage of forward"), trace.EnableOtelTrace)
		return id, nil, rsErr, details
	}
	return id, res, nil, nil
}

func (i *VllmPDSeparateInfer) returnFirstToken(ctx *gin.Context, generateReq *request.GenerateRequestImpl, res *http.Response) error {
	buf := generateReq.GetFirstToken()
	for k, vals := range res.Header {
		if k == "Content-Length" || k == "Content-Type" {
			continue
		}
		for _, v := range vals {
			ctx.Header(k, v)
		}
		ctx.Header("Content-Type", "text/event-stream; charset=utf-8")
	}
	ctx.Status(res.StatusCode)
	if err := handler.Write(ctx, buf, len(buf)); err != nil {
		return fmt.Errorf("fail to write first token err:%v", err)
	}
	return nil
}

func (i *VllmPDSeparateInfer) writeDecodeRes(ctx *gin.Context, generateReq *request.GenerateRequestImpl,
	decodeRes *http.Response, tracer trace.RequestTracer,
	perfMetrics *handler.PerfMetrics, decodeId string) (usage *openai.Usage, errDetails *utils.InstanceFailureDetails, finishReason string) {
	rewriteReqID := ctx.GetString(constant.RewriteRequestID)
	for key, vals := range decodeRes.Header {
		// if decodeRes.Header's key in resHeaderBlackSet -> filter
		if _, ok := i.resHeaderBlackSet[key]; ok {
			continue
		}
		// if resHeaderBlackSet has a key of prefix match mode, and decodeRes.Header's key match it -> filter
		if handler.IsHeaderPrefixMatching(key, i.resHeaderBlackSet) {
			continue
		}

		if key == constant.XRequestId && rewriteReqID != "" {
			idx := strings.LastIndexByte(rewriteReqID, '-')
			if idx != -1 {
				vals = []string{rewriteReqID[:idx]}
			}
		}

		if key == constant.HeaderContentLength {
			// Skip ContentLength, will be filled when writing Body
			continue
		}

		for _, val := range vals {
			ctx.Writer.Header().Set(key, val)
		}
	}
	if generateReq.IsStream() {
		firstTokenFromPrefill := &atomic.Bool{}
		firstTokenFromPrefill.Store(generateReq.OutputFirstTokenFromPrefill())
		passTokenTime := handler.GetPassTokenTime(generateReq.IsChat(), false, generateReq.GetMaxToken(), firstTokenFromPrefill.Load())
		if i.cfg.V1StreamWritingEnable {
			usage, errDetails, finishReason, _ = handler.WriteStreamResV1(ctx, generateReq, decodeRes, tracer, perfMetrics, decodeId, constant.StageDecode,
				i.workerEventCollector, passTokenTime, firstTokenFromPrefill)
			return
		} else {
			usage, errDetails, finishReason, _ = handler.WriteStreamRes(ctx, generateReq, decodeRes, tracer, perfMetrics, decodeId, constant.StageDecode, i.workerEventCollector, passTokenTime, firstTokenFromPrefill)
			return
		}
	} else {
		return handler.WriteNonStreamRes(ctx, generateReq, decodeRes, tracer, perfMetrics, decodeId, constant.StageDecode, false, generateReq.IsMessages(), generateReq.GetFirstToken())
	}
}
