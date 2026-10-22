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

package pipeline

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"infer-dp-scheduler/pkg/common/constant"
	"infer-dp-scheduler/pkg/common/errs"
	"infer-dp-scheduler/pkg/common/openai"
	"infer-dp-scheduler/pkg/common/trace"
	"infer-dp-scheduler/pkg/common/utils"
	"infer-dp-scheduler/pkg/route-server/handler/request"
)

const (
	MetaServicePrepareName    = "meta_service_prepare"
	LayerwiseErrorPhaseName   = "layerwise_error"
	LayerwiseSuccessPhaseName = "layerwise_success"

	DoRemoteDecodeKey       = "do_remote_decode"
	DoRemotePrefillKey      = "do_remote_prefill"
	MetaserverKey           = "metaserver"
	RequestIdKey            = "request_id"
	ReusePrefilledTokensKey = "reuse_prefilled_tokens"
)

func PrepareLayerwiseRequestBody(ctx *PipelineContext, copiedBody *utils.JSONModifier) *utils.JSONModifier {
	parsedReq := ctx.RequestContext.ParsedRequest

	metaserverURL := "http://" + ctx.ServerCfg.Addr + ":" + ctx.ServerCfg.HttpPort + "/v1/metaserver"

	var kvTransferParams map[string]interface{}
	switch ctx.ServerCfg.InferMode {
	case constant.LayerwiseMode:
		kvTransferParams = map[string]interface{}{
			DoRemoteDecodeKey:  false,
			DoRemotePrefillKey: true,
			MetaserverKey:      metaserverURL,
			RequestIdKey:       parsedReq.GetRequestId(),
		}
	case constant.LayerwiseMMMode:
		kvTransferParams = map[string]interface{}{
			DoRemoteDecodeKey:  true,
			DoRemotePrefillKey: false,
			MetaserverKey:      metaserverURL,
			RequestIdKey:       parsedReq.GetRequestId(),
		}
	default:
		// set to empty
		kvTransferParams = map[string]interface{}{}
	}
	copiedBody.Set(openai.KvTransferParamsKey, kvTransferParams)
	parsedReq.KvTransferParams = kvTransferParams

	return copiedBody
}

type MetaServicePrepare struct {
	*BasePhase
}

func NewMetaServicePrepare() Phase {
	return &MetaServicePrepare{
		BasePhase: NewBasePhase(MetaServicePrepareName, constant.StagePrepare),
	}
}

func (m *MetaServicePrepare) ExecutePhase(context *PipelineContext) (rsError *errs.RSError) {

	if err := parseMetaRequest(context); err != nil {
		return errs.NewRSMaError(
			errs.BadReqErr.BuildArgs(fmt.Sprintf("failed to parse meta request, err: %s", err.Error())),
			m.StageName())
	}

	if context.RequestContext.CancelCtx != nil && context.RequestContext.CancelCtx.IsCancel.Load() {
		return errs.NewRSMaError(&errs.RequestForcedCancelledErr, m.StageName())
	}

	return nil
}

func parseMetaRequest(context *PipelineContext) error {
	startTime := time.Now()

	req := context.ClientGinCtx.Request
	bodyBytes, err := io.ReadAll(req.Body)
	if err != nil {
		return err
	}

	raw, err := utils.NewJSONModifier(bodyBytes)
	if err != nil {
		return fmt.Errorf("failed to unmarshal req body to raw json obj: %v", err)
	}

	kvTransferParams, exists := raw.GetData()
	if !exists {
		return fmt.Errorf("missing %s key in meta server req body", openai.KvTransferParamsKey)
	}

	reqId, err := tryGetReqId(kvTransferParams)
	if err != nil {
		return err
	}

	// in /v1/completions ,the request id has a "-0" suffix
	originReqId := reqId
	if strings.HasPrefix(reqId, request.CompletionReqIdPrefix) {
		originReqId = reqId[:len(reqId)-2]
	}

	reqCtx := context.InferManager.RunningRequestManager.GetReqCancelCtx(originReqId)
	if reqCtx == nil {
		return fmt.Errorf("in meta server, req ctx for [%s] is missing", originReqId)
	}

	runningPipelineCtx, ok := reqCtx.PipelineReqCtx.(*PipelineContext)
	if !ok {
		return fmt.Errorf("in meta server, req ctx for [%s] is invalid", originReqId)
	}

	context.IsChat = runningPipelineCtx.IsChat
	context.RequestContext = runningPipelineCtx.RequestContext

	// tracing for MetaServer request parsing
	tracer := context.GetTracer()
	tracer.Tracer.Event("", trace.TraceEventReceiveMetaServerReceived, fmt.Sprintf("metaserver request parsed complete, received at time [%s] , bodyLength is [%v]", startTime, len(bodyBytes)))

	runningPipelineCtx.SetKVTransferParams(kvTransferParams)

	if reusePrefilledTokens, exists := kvTransferParams[ReusePrefilledTokensKey]; exists {
		if reuseBool, ok := reusePrefilledTokens.(bool); ok {
			runningPipelineCtx.RequestContext.ForceReuseFirstToken.Store(reuseBool)
		}
	}

	context.IsChat = runningPipelineCtx.IsChat

	return nil
}

func tryGetReqId(kvTransferParams map[string]interface{}) (string, error) {
	if reqId, hasId := kvTransferParams[RequestIdKey]; !hasId {
		return "", fmt.Errorf("missing %s key in meta server req body", RequestIdKey)
	} else {
		if reqIdStr, ok := reqId.(string); !ok {
			return "", fmt.Errorf("non-string field %s in meta server req body", RequestIdKey)
		} else {
			if err := utils.CheckResourceNameValid(reqIdStr); err != nil {
				return "", fmt.Errorf("invalid request_id in meta server request: %v", err)
			}
			return reqIdStr, nil
		}
	}
}

func newLayerWiseMetaServerErrorPhase() Phase {
	return &LayerwiseErrorPhase{
		BasePhase: NewBasePhase(LayerwiseErrorPhaseName, constant.StageFinalize),
	}
}

type LayerwiseErrorPhase struct {
	*BasePhase
}

func (l *LayerwiseErrorPhase) ExecutePhase(ctx *PipelineContext) (rsError *errs.RSError) {
	if ctx.RequestContext.RsErr != nil {
		tracer := ctx.GetTracer()
		tracer.Tracer.Event("", "metaserver_error", fmt.Sprintf("request [%s] layerwise error: %v", ctx.RequestId(), ctx.RequestContext.RsErr.Error()))
	} else {
		tracer := ctx.GetTracer()
		tracer.Tracer.Event("", "metaserver_error", fmt.Sprintf("request [%s] run fail phase but no rsError is found", ctx.RequestId()))
	}
	ctx.ClientGinCtx.Writer.WriteHeader(http.StatusServiceUnavailable)
	return nil
}

func newLayerWiseMetaServerSuccessPhase() Phase {
	return &LayerwiseSuccessPhase{
		BasePhase: NewBasePhase(LayerwiseSuccessPhaseName, constant.StageFinalize),
	}
}

type LayerwiseSuccessPhase struct {
	*BasePhase
}

func (m *LayerwiseSuccessPhase) ExecutePhase(ctx *PipelineContext) (rsError *errs.RSError) {
	ctx.ClientGinCtx.Status(http.StatusOK)
	tracer := ctx.GetTracer()
	// todo: add cost logging
	tracer.Tracer.Event("", trace.TraceEventReceiveMetaServerDone, fmt.Sprintf("metaserver request success for request [%s]", ctx.RequestId()))
	return nil
}
