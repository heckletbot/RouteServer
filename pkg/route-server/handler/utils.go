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

package handler

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"infer-dp-scheduler/cmd/route_server/server/options"
	"infer-dp-scheduler/pkg/common/constant"
	"infer-dp-scheduler/pkg/common/errs"
	"infer-dp-scheduler/pkg/common/health"
	"infer-dp-scheduler/pkg/common/logger"
	"infer-dp-scheduler/pkg/common/openai"
	"infer-dp-scheduler/pkg/common/trace"
	"infer-dp-scheduler/pkg/common/utils"
	"infer-dp-scheduler/pkg/route-server/metrics"
	"infer-dp-scheduler/pkg/route-server/scheduler"
)

const (
	CompletionsPath       = "/v1/completions"
	ChatCompletionsPath   = "/v1/chat/completions"
	ReadBufferSize        = 8 * 1024
	DecodeRemainLineNums  = 3
	PrefillRemainLineNums = 4
)

func IsTargetPhasePassed(targetStage string, errorStage string, stageChain []string) (testValid, testSuccess bool) {
	targetIndex := -1
	errorIndex := -1

	for i, stage := range stageChain {
		if stage == targetStage {
			targetIndex = i
		}
		if stage == errorStage {
			errorIndex = i
		}
	}

	if errorIndex == -1 {
		logger.Log().Warnf("error stage [%s] not in stageChain %v", errorStage, stageChain)
		return false, false
	}

	if targetIndex == -1 {
		logger.Log().Errorf("target stage [%s] not in stageChain %v", targetStage, stageChain)
		return false, false
	}

	if errorIndex < targetIndex {
		// error before target stage, so the test is not valid
		testValid = false
		testSuccess = false
	} else if errorIndex == targetIndex {
		// error happens in the target stage
		testValid = true
		testSuccess = false
	} else {
		// error happens after target stage, so test is success and err should be ignored
		testValid = true
		testSuccess = true
	}

	return testValid, testSuccess
}

type PerfMetrics struct {
	StartTime  time.Time
	FinishTime time.Time

	// Deprecated: Not used after pipeline refactoring, to be deleted
	EncodeFinish  time.Time
	PrefillFinish time.Time
	DecodeStart   time.Time

	FirstToken       time.Time
	FirstDTokenTime  time.Time
	SecondDTokenTime time.Time

	// Metrics are written during each phase and reported/printed when span ends
	Metrics sync.Map
}

func (i *PerfMetrics) MetricsString() string {
	var pairs []string
	i.Metrics.Range(func(key, value interface{}) bool {
		pair := fmt.Sprintf("%s=%v", key, value)
		pairs = append(pairs, pair)
		return true
	})

	return strings.Join(pairs, ",")
}

func (i *PerfMetrics) ReportMetrics() {
	metrics.MetricRecord("PDInfer", "PrefillCost", int(i.PrefillFinish.Sub(i.StartTime).Milliseconds()))
	metrics.MetricRecord("PDInfer", "FirstTokenCost", int(i.FirstToken.Sub(i.StartTime).Milliseconds()))
	metrics.MetricRecord("PDInfer", "DecodeCost", int(i.FinishTime.Sub(i.PrefillFinish).Milliseconds()))
}

type ResponseBody struct {
	Usage   *openai.Usage        `json:"usage,omitempty"`
	Choices []*openai.ChoiceBody `json:"choices,omitempty"`
}

// ReportInstanceFailure 1. collect events, send to leader periodically; 2. update local fallback module of scheduler manage
func ReportInstanceFailure(details *utils.InstanceFailureDetails, inferMsg *health.InferMsg, generateReq utils.GenerateRequest, workerEventCollector *health.WorkerEventCollector, schedulerMgr scheduler.Manager) {
	if details == nil {
		details = &utils.InstanceFailureDetails{
			StatusCode:      0,
			IsTimeout:       false,
			IsConnectionErr: false,
			IsKVTransferErr: false,
			ErrorMessage:    constant.UnKnownReason,
		}
	}
	workerEventCollector.OnRequestFail(generateReq, inferMsg, details)
	schedulerMgr.DealWithRequestFailureLocally(generateReq, inferMsg, details)
}

func IsHeaderPrefixMatching(input string, blackHeaders map[string]bool) bool {
	for blackPrefix, isPrefixRule := range blackHeaders {
		if isPrefixRule && strings.HasPrefix(input, blackPrefix) {
			return true
		}
	}
	return false
}

func WriteNonStreamRes(ctx *gin.Context, generateReq utils.GenerateRequest, decodeRes *http.Response,
	tracer trace.RequestTracer, perfMetrics *PerfMetrics, decodeId string, spanName string, isLayerwise bool, firstTokenBody []byte) (*openai.Usage, *utils.InstanceFailureDetails, string) {
	var usage *openai.Usage
	rawBytes, err := io.ReadAll(decodeRes.Body)
	retTime := time.Now()
	perfMetrics.FirstDTokenTime = retTime
	perfMetrics.SecondDTokenTime = retTime
	perfMetrics.FirstToken = retTime
	if err != nil {
		if errors.Is(ctx.Request.Context().Err(), context.Canceled) {
			tracer.Event(spanName, trace.TraceEventUserCancelled,
				fmt.Sprintf("user request cancelled on decode [%s] during non stream response waiting writing",
					decodeId))
			return nil, utils.DetailsFromError(http.StatusOK, ctx.Request.Context().Err()), constant.UnKnownReason
		}
		return nil, utils.DetailsFromError(http.StatusOK, err), constant.UnKnownReason
	}
	nonStreamError, finishReason := utils.DetailsFromNonStreamResponse(decodeRes.StatusCode, rawBytes)
	if nonStreamError != nil {
		return usage, nonStreamError, finishReason
	}
	if ctx.GetString(constant.RewriteRequestID) != "" {
		rawBytes = RewriteRequestID(rawBytes, ctx.GetString(constant.RewriteRequestID), 1)
	}

	if isLayerwise && len(firstTokenBody) > 0 {
		if generateReq.GetMaxToken() == 1 {
			rawBytes = firstTokenBody
		} else {
			fields := []string{"content", "reasoning"}
			// combining content and reasoning using SonicApi
			var firstTokenMap, decodeMap map[string]interface{}
			if err := utils.SonicApi.Unmarshal(firstTokenBody, &firstTokenMap); err != nil {
				return usage, utils.DetailsFromError(http.StatusOK, err), constant.UnKnownReason
			}
			if err := utils.SonicApi.Unmarshal(rawBytes, &decodeMap); err != nil {
				return usage, utils.DetailsFromError(http.StatusOK, err), constant.UnKnownReason
			}

			// recursively find and merge content and reasoning fields
			for _, field := range fields {
				mergeStringFields(decodeMap, firstTokenMap, field)
			}
			mergedBytes, err := utils.SonicApi.Marshal(decodeMap)
			if err != nil {
				return usage, utils.DetailsFromError(http.StatusOK, err), constant.UnKnownReason
			}
			rawBytes = mergedBytes
		}
	}

	ctx.Writer.Header().Set(constant.HeaderContentLength, strconv.Itoa(len(rawBytes)))
	err = WriteBuffer(ctx, true, &rawBytes, len(rawBytes), nil)
	if err != nil {
		return usage, utils.DetailsFromError(http.StatusOK, ctx.Request.Context().Err()), constant.UnKnownReason
	}
	usage, finishReason = extractNonStreamBody(rawBytes)
	return usage, nil, finishReason
}

func WriteBuffer(ctx *gin.Context, shouldWrite bool, buf *[]byte, n int, readErr error) error {
	// Write valid data
	if shouldWrite && n > 0 {
		if err := Write(ctx, *buf, n); err != nil {
			return err
		}
	}
	if readErr != nil && readErr != io.EOF {
		return fmt.Errorf("decode response read body err:%v", readErr)
	}
	return nil
}

func Write(ctx *gin.Context, buf []byte, n int) error {
	cur := 0
	for {
		m, writeErr := ctx.Writer.Write(buf[cur:n])
		if writeErr != nil {
			return fmt.Errorf("write response body err:%v", writeErr)
		}
		cur += m
		if cur >= n {
			break
		}
	}
	ctx.Writer.Flush()
	return nil
}

func WriteDone(ctx *gin.Context) error {
	_, err := ctx.Writer.Write([]byte("data: "))
	if err != nil {
		return fmt.Errorf("failed to write 'data: ': %v", err)
	}
	_, err = ctx.Writer.Write([]byte{'[', 'D', 'O', 'N', 'E', ']', '\n', '\n'})
	if err != nil {
		return fmt.Errorf("failed to write '[DONE]\\n\\n': %v", err)
	}
	ctx.Writer.Flush()
	return nil
}

func ParseResponseBody(buf []byte) (*ResponseBody, error) {
	var body ResponseBody
	if err := utils.SonicApi.Unmarshal(buf, &body); err != nil {
		logger.Log().Errorf("response json is invalid, %s", utils.AbstractLargeBytes(buf))
		return nil, err
	}
	return &body, nil
}

func extractNonStreamBody(buf []byte) (*openai.Usage, string) {
	body, err := ParseResponseBody(buf)
	if err != nil {
		return nil, constant.UnKnownReason
	}
	var finishReason string
	if len(body.Choices) == 0 {
		finishReason = constant.UnKnownReason
	} else {
		finishReason = body.Choices[0].FinishReason
	}
	if body.Usage == nil {
		logger.Log().Errorf("extract usage failed")
		return nil, finishReason
	}
	if body.Usage.PromptTokens == 0 {
		logger.Log().Errorf("extract usage failed")
		return nil, finishReason
	} else {
		metrics.RecordPromptTokenCount(body.Usage.PromptTokens)
		metrics.RecordOutputTotalTokenCount(body.Usage.CompletionTokens)
		metrics.RecordTotalOutputToken(body.Usage.CompletionTokens)
	}
	return body.Usage, finishReason
}

func RsErrorFromResponse(stream bool, details *utils.InstanceFailureDetails, insId string, stage string) *errs.RSError {
	if details != nil {
		if details.IsClientErr {
			return errs.NewRSMaError(errs.ClientConnectionErr.BuildArgs(details.ErrorMessage), stage)
		}
		if details.IsCanceledErr {
			return errs.NewRSMaError(&errs.RequestCancelledByUserErr, stage)
		}
		if details.IsTimeout {
			return errs.NewRSMaError(errs.GatewayTimeoutErr.BuildArgs(
				fmt.Sprintf("timeout when write response from backend [%s]", insId)), stage)
		} else if details.IsConnectionErr {
			return errs.NewRSMaError(errs.BadGatewayErr.BuildArgs(
				fmt.Sprintf("connection error from backend [%s]", insId)), stage)
		} else if stream && details.StatusCode != http.StatusOK {
			return errs.NewRSStreamError(&errs.EngineError{
				EngineErrBody: []byte(details.ErrorMessage),
				EngineStatus:  details.StatusCode,
				InstanceID:    insId,
			}, stage)
		}
		return errs.NewRSMaError(errs.InternalErr.BuildArgs(
			fmt.Sprintf("failed to write response: err msg: %s", details.ErrorMessage)), stage)
	}
	return nil
}

func WriteStreamRes(ctx *gin.Context, generateReq utils.GenerateRequest,
	decodeRes *http.Response, tracer trace.RequestTracer,
	perfMetrics *PerfMetrics,
	decodeId string,
	spanName string,
	workerEventCollector *health.WorkerEventCollector,
	isChat bool, isLayerwise bool, firstTokenFromPrefill *atomic.Bool) (*openai.Usage, *utils.InstanceFailureDetails, string, bool) {
	passTokenTime := 0
	if isLayerwise {
		passTokenTime = getLayerwisePassTokenTime(isChat, generateReq.GetMaxToken(), firstTokenFromPrefill.Load())
	} else {
		passTokenTime = getPassTokenTime(isChat, firstTokenFromPrefill.Load())
	}
	usage := &openai.Usage{
		PromptTokens:        0,
		CompletionTokens:    0,
		TotalTokens:         0,
		PromptTokensDetails: &openai.PromptTokensDetails{},
	}
	buf := make([]byte, ReadBufferSize)
	processor := &StreamDataProcessor{retainLineNums: DecodeRemainLineNums}
	firstToken := false
	count := 0
	for {
		isWrite := true
		n, readErr := decodeRes.Body.Read(buf)
		newEndOffset := n
		if n == 0 {
			if readErr != nil {
				if readErr == io.EOF {
					break
				}
				if errors.Is(ctx.Request.Context().Err(), context.Canceled) {
					tracer.Event(spanName, trace.TraceEventUserCancelled,
						fmt.Sprintf("user request cancelled on decode [%s] during response stream writing", decodeId))
					return nil, utils.DetailsFromError(http.StatusOK, ctx.Request.Context().Err()), constant.UnKnownReason, false
				}
				err := fmt.Errorf("decode response read body err:%v", readErr)
				return usage, utils.DetailsFromError(http.StatusOK, err), constant.UnKnownReason, false
			}
			continue
		}
		passTokenTime, newEndOffset = SkipOriginalToken(passTokenTime, &isWrite, n, &buf)
		if !firstToken {
			firstToken = true
			perfMetrics.FirstDTokenTime = time.Now()
			if !firstTokenFromPrefill.Load() {
				perfMetrics.FirstToken = perfMetrics.FirstDTokenTime
			}
			if perfMetrics.PrefillFinish.IsZero() {
				perfMetrics.PrefillFinish = perfMetrics.FirstToken
			}
		}

		err := WriteBuffer(ctx, isWrite, &buf, newEndOffset, readErr)
		if err != nil {
			return usage, utils.DetailsFromError(http.StatusOK, err), constant.UnKnownReason, false
		}
		count++
		if count == 2 {
			perfMetrics.SecondDTokenTime = time.Now()
		}
		if count%5 == 0 {
			workerEventCollector.OnLocalTokenEvent(generateReq.GetRequestId(), decodeId, 5)
		}
		processor.ProcessStream(string(buf[:newEndOffset]))
	}
	workerEventCollector.OnLocalTokenEvent(generateReq.GetRequestId(), decodeId, count)

	if details, finishReason, _, hasDoneWritten := processor.ParseAndRecordError(); details != nil {
		return nil, details, finishReason, hasDoneWritten
	} else {
		return processor.ParseAndRecordTokens(), nil, processor.ParseFinishReason(), hasDoneWritten
	}
}

func WriteStreamResV1(ctx *gin.Context, generateReq utils.GenerateRequest, decodeRes *http.Response,
	tracer trace.RequestTracer, perfMetrics *PerfMetrics,
	decodeId string, spanName string, workerEventCollector *health.WorkerEventCollector,
	isChat bool, isLayerwise bool, firstTokenFromPrefill *atomic.Bool) (*openai.Usage, *utils.InstanceFailureDetails, string, bool) {
	passTokenTime := 0
	if isLayerwise {
		passTokenTime = getLayerwisePassTokenTime(isChat, generateReq.GetMaxToken(), firstTokenFromPrefill.Load())
	} else {
		passTokenTime = getPassTokenTime(isChat, firstTokenFromPrefill.Load())
	}
	usage := &openai.Usage{
		PromptTokensDetails: &openai.PromptTokensDetails{},
	}

	writeTimeout := options.GetConfig().ServerConfig.ClientWriteTimeoutMs
	ew := NewEventTrackingWriter(ctx.Writer, passTokenTime, writeTimeout, ctx.ClientIP(), ctx.GetString(constant.RewriteRequestID), func() {
		now := time.Now()
		perfMetrics.FirstDTokenTime = now
		if !firstTokenFromPrefill.Load() {
			perfMetrics.FirstToken = perfMetrics.FirstDTokenTime
		}
	}, func() {
		perfMetrics.SecondDTokenTime = time.Now()
	}, func() {
		workerEventCollector.OnLocalTokenEvent(generateReq.GetRequestId(), decodeId, 5)
	})

	start := time.Now()
	defer func() {
		readDuration := time.Now().Sub(start) - ew.TotalDuration
		tracer.Event(spanName, trace.TraceEventStreamProcessDone,
			fmt.Sprintf("decode process finished, readtime [%s], writetime [%s]", readDuration.String(), ew.WriteDuration.String()))
		logger.Log().Infof("[STREAM_DEBUG] request %s write, writetime [%s], total_non_read_time [%s],"+
			"%v chunks, %v total events,%v events skipped.",
			generateReq.GetRequestId(), ew.WriteDuration.String(), ew.TotalDuration.String(), ew.chunkCnt, ew.written, ew.skipped)
	}()

	_, err := io.Copy(ew, decodeRes.Body)
	if err != nil {
		if errors.Is(err, errs.ClientError{}) {
			return usage, utils.DetailsFromError(constant.StatusClientError, err), constant.ClientErrReason, false
		}

		if errors.Is(ctx.Request.Context().Err(), context.Canceled) {
			tracer.Event(spanName, trace.TraceEventUserCancelled, "user cancelled during streaming")
			return usage, utils.DetailsFromError(http.StatusOK, err), constant.UnKnownReason, false
		}

		return usage, utils.DetailsFromError(http.StatusOK, err), constant.UnKnownReason, false
	}

	workerEventCollector.OnLocalTokenEvent(generateReq.GetRequestId(), decodeId, 1)

	details, finishReason, hasDoneWritten := ew.ParseAndRecordError()
	if details != nil {
		return nil, details, finishReason, hasDoneWritten
	}
	return ew.ParseAndRecordTokens(), nil, ew.ParseFinishReason(), hasDoneWritten
}

func WritePrefillStreamRes(ctx *gin.Context, generateReq utils.GenerateRequest,
	prefillRes *http.Response, tracer trace.RequestTracer, perfMetrics *PerfMetrics,
	prefillId string, stage string, workerEventCollector *health.WorkerEventCollector,
	isChat bool, isForcedReusePrefilledToken *atomic.Bool) ([]byte, *errs.RSError, *utils.InstanceFailureDetails, string) {

	buf := make([]byte, ReadBufferSize)
	totalBuf := make([]byte, 0)
	processor := &StreamDataProcessor{retainLineNums: PrefillRemainLineNums}
	count := 0
	for {
		n, readErr := prefillRes.Body.Read(buf)
		newEndOffset := n
		if n == 0 {
			if readErr != nil {
				if readErr == io.EOF {
					break
				}
				if errors.Is(ctx.Request.Context().Err(), context.Canceled) {
					tracer.Event("", trace.TraceEventUserCancelled,
						fmt.Sprintf("user request cancelled on prefill [%s] during response stream writing", prefillId))
					details := utils.DetailsFromError(http.StatusOK, ctx.Request.Context().Err())
					return nil, RsErrorFromResponse(false, details, prefillId, stage), details, constant.UnKnownReason
				}
				err := fmt.Errorf("prefill response read body err:%v", readErr)
				details := utils.DetailsFromError(http.StatusOK, err)
				return nil, RsErrorFromResponse(false, details, prefillId, stage), details, constant.UnKnownReason
			}
			continue
		}
		count++
		totalBuf = append(totalBuf, buf[:newEndOffset]...)
		processor.ProcessStream(string(buf[:newEndOffset]))
	}

	if details, finishReason, rawBytes, _ := processor.ParseAndRecordError(); details != nil {
		rsErr := errs.NewRSEngineError(&errs.EngineError{
			EngineErrBody: rawBytes,
			EngineStatus:  details.StatusCode,
			InstanceID:    prefillId,
		}, stage)
		return nil, rsErr, details, finishReason
	}

	// ForceNotReuseFirstToken, don't write response
	if !isForcedReusePrefilledToken.Load() {
		return processor.GetRawResBody(), nil, nil, processor.ParseFinishReason()
	}

	perfMetrics.FirstToken = time.Now()
	if perfMetrics.PrefillFinish.IsZero() {
		perfMetrics.PrefillFinish = perfMetrics.FirstToken
	}

	totalBuf = truncateBuffer(totalBuf, len(totalBuf), getPassTokenTime(isChat, true))
	totalBuf = RewriteRequestID(totalBuf, ctx.GetString(constant.RewriteRequestID), -1)
	err := WriteBuffer(ctx, true, &totalBuf, len(totalBuf), nil)
	if err != nil {
		details := utils.DetailsFromError(http.StatusOK, err)
		return nil, RsErrorFromResponse(false, details, prefillId, stage), details, constant.UnKnownReason
	}

	workerEventCollector.OnLocalTokenEvent(generateReq.GetRequestId(), prefillId, count)
	return processor.GetRawResBody(), nil, nil, processor.ParseFinishReason()
}

func truncateBuffer(buf []byte, endOffset int, tokenNum int) (truncatedBuf []byte) {
	truncatedBuf = make([]byte, 0)
	bufToProcess := buf[:endOffset]
	count := 0

	for count < tokenNum {
		startIdx := bytes.Index(bufToProcess, []byte("data:"))
		if startIdx < 0 {
			break
		}
		endIdx := bytes.Index(bufToProcess[startIdx:], []byte("\n\n"))
		if endIdx < 0 {
			break
		}
		tokenLength := startIdx + endIdx + 2
		tokenData := bufToProcess[:tokenLength]

		tokenStr := string(tokenData)
		tokenStr = strings.ReplaceAll(tokenStr, `"finish_reason":"length"`, `"finish_reason":null`)
		tokenStr = strings.ReplaceAll(tokenStr, `,"stop_reason":null`, ``)

		truncatedBuf = append(truncatedBuf, []byte(tokenStr)...)
		bufToProcess = bufToProcess[tokenLength:]
		count++
	}
	return truncatedBuf
}

func getLayerwisePassTokenTime(isChat bool, maxToken int32, outputTokenFromPrefill bool) int {
	if !outputTokenFromPrefill {
		return 0
	}
	passTokenTime := 0
	if maxToken == 1 {
		passTokenTime++
	}
	if isChat {
		passTokenTime++
	}
	return passTokenTime
}

func getPassTokenTime(isChat bool, outputTokenFromPrefill bool) int {
	if !outputTokenFromPrefill {
		return 0
	}
	if isChat {
		return 2 // chatCompletion: skip first two lines (role and first token)
	}
	return 1 // completion: skip first line (first token)
}

func FinishPerfMetrics(perfMetrics *PerfMetrics) (firstDTokenLatencyMs, secondDTokenLatencyMs int64) {
	if !perfMetrics.FirstDTokenTime.IsZero() && !perfMetrics.PrefillFinish.IsZero() {
		firstDTokenLatencyMs = perfMetrics.FirstDTokenTime.Sub(perfMetrics.PrefillFinish).Milliseconds()
		if !perfMetrics.SecondDTokenTime.IsZero() {
			secondDTokenLatencyMs = perfMetrics.SecondDTokenTime.Sub(perfMetrics.FirstDTokenTime).Milliseconds()
		}
	}
	perfMetrics.ReportMetrics()
	return
}

func ProcessRequestError(ctx *gin.Context, rsErr *errs.RSError, tracer trace.RequestTracer, finishReason string) (retCode int) {
	metrics.RecordFailedRequest()
	if rsErr == nil {
		return 0
	}
	// Delete contentLength header, it will be regenerated when writing body
	ctx.Writer.Header().Del(constant.HeaderContentLength)
	if rsErr.EngineErr != nil {
		retCode = rsErr.EngineErr.EngineStatus
		tracer.ErrorSpan(trace.RequestSpanE2E, fmt.Errorf("%s, finish_reason = %s, err_stage = %s",
			string(rsErr.EngineErr.EngineErrBody), finishReason, rsErr.ErrorStage), trace.GtLogPermission)
		ctx.Writer.WriteHeader(retCode)
		reader := bytes.NewReader(rsErr.EngineErr.EngineErrBody)
		_, err := io.Copy(ctx.Writer, reader)
		if err != nil {
			maErr := errs.InternalErr.BuildArgs(fmt.Sprintf("construct error response body failed, err = %v", err))
			ctx.JSON(retCode, maErr.Json())
			return
		}
	} else if rsErr.EngineStreamErr != nil {
		retCode = rsErr.EngineStreamErr.EngineStatus
		// just record error when stream write error
		tracer.ErrorSpan(trace.RequestSpanE2E, fmt.Errorf("error during streaming writing from decode [%s], "+
			"status_code in body is [%v], err = %s, finish_reason = %s, err_stage = %s",
			rsErr.EngineStreamErr.InstanceID, retCode, rsErr.EngineStreamErr.EngineErrBody,
			finishReason, rsErr.ErrorStage), trace.GtLogPermission)
	} else {
		retCode = rsErr.MaErr.Status
		tracer.ErrorSpan(trace.RequestSpanE2E, fmt.Errorf("%s, finish_reason = %s, err_stage = %s",
			rsErr.MaErr.Error(), finishReason, rsErr.ErrorStage), trace.GtLogPermission)
		ctx.JSON(retCode, rsErr.MaErr.Json())
	}
	return
}

func ProcessStreamRequestError(ctx *gin.Context, rsErr *errs.RSError, tracer trace.RequestTracer, finishReason string, hasDoneWritten bool) (retCode int) {
	metrics.RecordFailedRequest()
	defer func() {
		// write data:[DONE] to finish the stream response if not already written
		if !hasDoneWritten {
			if err := WriteDone(ctx); err != nil {
				tracer.ErrorSpan(trace.RequestSpanE2E, fmt.Errorf("failed to write done marker: %v, err_stage = %s", err, constant.StageFinalize), trace.GtLogPermission)
			}
		}
	}()
	if rsErr == nil {
		return 0
	}

	if rsErr.EngineErr != nil {
		logger.Log().Warnf("non-streaming error occured for a stream request, this should not happen")
		retCode = rsErr.EngineErr.EngineStatus
		tracer.ErrorSpan(trace.RequestSpanE2E, fmt.Errorf("%s, finish_reason = %s, err_stage = %s",
			string(rsErr.EngineErr.EngineErrBody), finishReason, rsErr.ErrorStage), trace.GtLogPermission)
		ctx.Writer.WriteHeader(retCode)
		reader := bytes.NewReader(rsErr.EngineErr.EngineErrBody)
		_, err := io.Copy(ctx.Writer, reader)
		if err != nil {
			maErr := errs.InternalErr.BuildArgs(fmt.Sprintf("construct error response body failed, err = %v", err))
			ctx.JSON(retCode, maErr.Json())
			return
		}
	} else if rsErr.EngineStreamErr != nil {
		retCode = rsErr.EngineStreamErr.EngineStatus
		tracer.ErrorSpan(trace.RequestSpanE2E, fmt.Errorf("error during streaming writing from prefill [%s], "+
			"status_code in body is [%v], err = %s, finish_reason = %s, err_stage = %s",
			rsErr.EngineStreamErr.InstanceID, retCode, rsErr.EngineStreamErr.EngineErrBody,
			finishReason, rsErr.ErrorStage), trace.GtLogPermission)
	} else {
		retCode = rsErr.MaErr.Status
		tracer.ErrorSpan(trace.RequestSpanE2E, fmt.Errorf("%s, finish_reason = %s, err_stage = %s",
			rsErr.MaErr.Error(), finishReason, rsErr.ErrorStage), trace.GtLogPermission)
		streamBody := rsErr.MaErr.StreamJson()
		errBytes, err := utils.SonicApi.Marshal(streamBody)
		if err != nil {
			tracer.ErrorSpan(trace.RequestSpanE2E, fmt.Errorf("failed to marshal stream error body: %v, err_stage = %s", err, constant.StageFinalize), trace.GtLogPermission)
			return
		}

		errorBody := fmt.Sprintf("data: %s\n\n", string(errBytes))
		buf := []byte(errorBody)

		if err := Write(ctx, buf, len(buf)); err != nil {
			tracer.ErrorSpan(trace.RequestSpanE2E, fmt.Errorf("failed to write error response: %v, err_stage = %s", err, constant.StageFinalize), trace.GtLogPermission)
		}
	}
	return
}

func BuildSubSpanName(spanName string, subSpanName string) string {
	return spanName + "_" + subSpanName
}

// EventTrackingWriter tracks SSE events (separated by \n\n)
type EventTrackingWriter struct {
	w           io.Writer    // underlying writer
	f           http.Flusher // for flush
	linesToSkip int

	skipped    int      // number of skipped events
	written    int      // number of successfully written events
	lastEvents [][]byte // circular queue, max 3 events
	pending    []byte   // incomplete data (may contain partial events)
	onFirst    func()   // callback triggered on first token
	onSecond   func()   // callback triggered on second token
	onEvery5   func()   // callback triggered every 5 events

	WriteDuration time.Duration
	TotalDuration time.Duration

	clientIP     string
	rewriteReqID string
	eventTimeout int64

	chunkCnt int // number of received chunks
}

// NewEventTrackingWriter creates an event tracking writer
func NewEventTrackingWriter(w io.Writer, linesToSkip int, eventTimeout int64, clientIP, rewriteReqID string, onFirstToken func(), onSecToken func(), onEvery5 func()) *EventTrackingWriter {
	var f http.Flusher = nil
	if eventTimeout >= 0 {
		// Wrap writer with NetTimeoutWriter, timeout covers Write and Flush, Flush happens inside NetTimeoutWriter
		if timeoutWriter := NewNetTimeoutWriter(w, eventTimeout); timeoutWriter != nil {
			w = timeoutWriter
		}
	} else {
		f, _ = w.(http.Flusher)
	}
	return &EventTrackingWriter{
		w:            w,
		f:            f,
		linesToSkip:  linesToSkip,
		onFirst:      onFirstToken,
		onSecond:     onSecToken,
		onEvery5:     onEvery5,
		clientIP:     clientIP,
		rewriteReqID: rewriteReqID,
		lastEvents:   make([][]byte, 0, 3),
		eventTimeout: eventTimeout,
	}
}

type NetTimeoutWriter struct {
	w       io.Writer
	rc      *http.ResponseController
	timeout int64
}

func (w *NetTimeoutWriter) Write(p []byte) (n int, err error) {
	// 1. Set deadline for this Write + Flush
	deadline := time.Now().Add(time.Duration(w.timeout) * time.Millisecond)
	err = w.rc.SetWriteDeadline(deadline)
	if err != nil {
		if errors.Is(err, http.ErrNotSupported) {
			// test mode or other fake clients don't support SetWriteDeadline
			logger.Log().Warnf("current writer[%T] do not support SetWriteDeadline", w.w)
		} else {
			return 0, fmt.Errorf("SetWriteDeadline err:%v", err)
		}
	} else {
		// 2. Ensure deadline is cancelled on exit to prevent "idle kills"
		defer func() {
			_ = w.rc.SetWriteDeadline(time.Time{})
		}()
	}

	// 3. Write to memory buffer
	n, err = w.w.Write(p)
	if err != nil {
		return n, fmt.Errorf("NetTimeoutWriter write err:%v", err)
	}

	// 4. Must execute Flush here to let the timeout umbrella cover real network I/O!
	// ResponseController updated Flush method, added error return
	if err = w.rc.Flush(); err != nil {
		return n, fmt.Errorf("NetTimeoutWriter flush err:%v", err)
	}

	return n, nil
}

func NewNetTimeoutWriter(w io.Writer, timeout int64) io.Writer {
	// gin's responseWriter doesn't have FlushError method, Flush won't throw error, need to unwrap to standard library responseWriter
	if rw, ok := tryUnwrap(w); ok {
		return &NetTimeoutWriter{
			w:       w,                              // gin's responseWriter, used for Write
			rc:      http.NewResponseController(rw), // ResponseController wrapped by standard library, used for Flush
			timeout: timeout,
		}
	}

	return nil
}

func tryUnwrap(w io.Writer) (http.ResponseWriter, bool) {
	// 1. Unwrap writer to standard library writer
	unwrapper, ok := w.(interface{ Unwrap() http.ResponseWriter })
	if !ok {
		return nil, false
	}

	// 2. Check if unwrapWriter supports setting timeout
	unwrapWriter := unwrapper.Unwrap()
	if _, ok = unwrapWriter.(interface{ SetWriteDeadline(time.Time) error }); ok {
		return unwrapWriter, true
	}
	return nil, false
}

// Write implements io.Writer interface
func (ew *EventTrackingWriter) Write(p []byte) (n int, err error) {
	ew.chunkCnt++
	// Merge pending and current data
	data := append(ew.pending, p...)
	ew.pending = nil // Clear pending, will be reset later

	startTime := time.Now()
	defer func() {
		ew.TotalDuration += time.Since(startTime)
	}()

	start := 0
	delim := []byte("\n\n")
	for {
		// Find next \n\n in data[start:]
		idx := bytes.Index(data[start:], delim)
		if idx == -1 {
			break // No more complete events
		}
		// End position of complete event
		end := start + idx + 2 // includes \n\n
		event := data[start:end]
		shouldWriteThisEvent := true
		if ew.linesToSkip > 0 && ew.skipped < ew.linesToSkip {
			ew.skipped++
			ew.written++
			// If it's error content, don't skip
			// If it's not error content, skip it and don't write to client
			shouldWriteThisEvent = ew.isStreamError(event)
		}

		if shouldWriteThisEvent {
			startWriteTime := time.Now()
			// Write this event to client
			_, writeErr := ew.w.Write(RewriteRequestID(event, ew.rewriteReqID, 1))
			writeDone := time.Now()
			curEventWriteDuration := writeDone.Sub(startWriteTime)
			ew.WriteDuration += curEventWriteDuration
			if writeErr != nil {
				if curEventWriteDuration.Milliseconds() >= ew.eventTimeout {
					return 0, errs.NewClientError(ew.clientIP,
						fmt.Errorf("event write timeout: actual time [%v]ms exceeded threshold [%v]ms due to [%v]",
							curEventWriteDuration.Milliseconds(), ew.eventTimeout, writeErr))
				}
				return 0, errs.NewClientError(ew.clientIP, writeErr)
			}

			if ew.f != nil {
				ew.f.Flush() // Flush immediately to ensure streaming
				ew.WriteDuration += time.Since(writeDone)
			}

			ew.written++
			if ew.written%5 == 0 && ew.onEvery5 != nil {
				ew.onEvery5() // Trigger callback every 5 events
			}
		}

		// update last 3 event, including the skipped lines
		ew.addLastEvent(event)

		if ew.written == 1 {
			ew.onFirst()
		} else if ew.written == 2 {
			ew.onSecond()
		}

		start = end
	}

	// Handle remaining incomplete data (may contain partial events)
	if start < len(data) {
		remaining := data[start:]
		ew.pending = remaining
	}

	return len(p), nil
}

func (ew *EventTrackingWriter) isStreamError(event []byte) bool {
	content := string(ew.extractContent(event))
	return strings.Contains(content, "\"error\"") && strings.Contains(content, "\"code\"") && strings.Contains(content, "\"type\"")
}

// RewriteRequestID If rewriteReqID is not empty, it means rs added a suffix to the llm's request id in the upstream process
// In this case, when returning to the client, the request id field in the response should be rewritten to the client's original id
func RewriteRequestID(event []byte, rewriteReqID string, cnt int) []byte {
	if rewriteReqID == "" {
		return event
	}
	// Find the rightmost '-'
	idx := strings.LastIndexByte(rewriteReqID, '-')
	// Fault tolerance: if '-' is not found, it may mean an unprocessed ID was passed
	if idx == -1 {
		return event
	}
	// Take the part before '-'
	originalRequestID := rewriteReqID[:idx]
	// Pre-calculate features (only includes right quote and ID itself)
	searchKey := []byte(rewriteReqID + `"`)
	replaceKey := []byte(originalRequestID + `"`)
	return bytes.Replace(event, searchKey, replaceKey, cnt)
}

// addLastEvent adds an event to the circular queue (copies content)
func (ew *EventTrackingWriter) addLastEvent(event []byte) {
	// Copy event content to avoid referencing the underlying large array
	copyEvent := make([]byte, len(event))
	copy(copyEvent, event)

	if len(ew.lastEvents) < 3 {
		ew.lastEvents = append(ew.lastEvents, copyEvent)
	} else {
		// Remove the oldest event, append new event
		ew.lastEvents = append(ew.lastEvents[1:], copyEvent)
	}
}

func (ew *EventTrackingWriter) ParseAndRecordTokens() *openai.Usage {
	if len(ew.lastEvents) < 2 { // it means that this is an error response
		return nil
	}
	//lastThreeLine[0] for choice, lastThreeLine[1] for usage, lastThreeLine[2] for done in stream mode
	body, err := ParseResponseBody(ew.extractContent(ew.lastEvents[len(ew.lastEvents)-2]))
	if err != nil {
		return nil
	}
	if body.Usage == nil {
		return nil
	}
	if body.Usage.PromptTokens == 0 {
		return nil
	} else {
		metrics.RecordPromptTokenCount(body.Usage.PromptTokens)
		metrics.RecordOutputTotalTokenCount(body.Usage.CompletionTokens)
		metrics.RecordTotalOutputToken(body.Usage.CompletionTokens)
		return body.Usage
	}
}

func (ew *EventTrackingWriter) extractContent(eventLine []byte) []byte {
	str := string(eventLine)
	str = strings.TrimPrefix(str, "data:")
	str = strings.TrimSuffix(str, "\n\n")
	return []byte(str)
}

func (ew *EventTrackingWriter) ParseFinishReason() string {
	if len(ew.lastEvents) < 3 {
		return constant.UnKnownReason
	}
	body, err := ParseResponseBody(ew.extractContent(ew.lastEvents[0]))
	if err != nil {
		return constant.UnKnownReason
	}
	if len(body.Choices) == 0 {
		return constant.UnKnownReason
	}
	return body.Choices[0].FinishReason
}

func (ew *EventTrackingWriter) ParseAndRecordError() (*utils.InstanceFailureDetails, string, bool) {
	hasDoneWritten := false
	if len(ew.lastEvents) > 0 {
		hasDoneWritten = isDone(ew.lastEvents[len(ew.lastEvents)-1])
	}
	if len(ew.lastEvents) < 2 {
		return nil, constant.UnKnownReason, hasDoneWritten
	}
	details, finishReason := utils.TryParseErrFromStreamResponse(ew.extractContent(ew.lastEvents[len(ew.lastEvents)-2]))
	if details == nil && len(ew.lastEvents) >= 3 {
		details, finishReason = utils.TryParseErrFromStreamResponse(ew.extractContent(ew.lastEvents[len(ew.lastEvents)-3]))
	}
	return details, finishReason, hasDoneWritten
}

func mergeStringFields(decodeMap, firstTokenMap map[string]interface{}, fieldName string) {
	choices, exist := decodeMap["choices"].([]interface{})
	if !exist {
		return
	}

	firstChoices, exist := firstTokenMap["choices"].([]interface{})
	if !exist {
		return
	}

	for i := range choices {
		if i >= len(firstChoices) {
			break
		}

		decodeChoice, ok := choices[i].(map[string]interface{})
		firstChoice, ok := firstChoices[i].(map[string]interface{})
		if !ok {
			continue
		}

		decodeMessage, ok := decodeChoice["message"].(map[string]interface{})
		firstMessage, ok := firstChoice["message"].(map[string]interface{})
		if !ok {
			continue
		}

		firstVal, firstOk := firstMessage[fieldName].(string)

		decodeVal, decodeOk := decodeMessage[fieldName].(string)

		if firstOk && decodeOk {
			decodeMessage[fieldName] = firstVal + decodeVal
		} else if firstOk {
			decodeMessage[fieldName] = firstVal
		}
	}
}

func isDone(line []byte) bool {
	target := []byte("[DONE]")
	if len(line) < len(target) {
		return false
	}
	for i := 0; i <= len(line)-len(target); i++ {
		if bytes.Equal(line[i:i+len(target)], target) {
			return true
		}
	}
	return false
}
