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
	"infer-dp-scheduler/cmd/route_server/server/options"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"

	"infer-dp-scheduler/pkg/common/config"
	"infer-dp-scheduler/pkg/common/constant"
	"infer-dp-scheduler/pkg/common/errs"
	"infer-dp-scheduler/pkg/common/health"
	"infer-dp-scheduler/pkg/common/logger"
	"infer-dp-scheduler/pkg/common/openai"
	"infer-dp-scheduler/pkg/common/trace"
	"infer-dp-scheduler/pkg/common/utils"
	"infer-dp-scheduler/pkg/route-server/handler"
	"infer-dp-scheduler/pkg/route-server/handler/request"
)

type PipelineContext struct {
	// whether this request is chat or completion
	IsChat      bool
	IsLayerWise bool
	// ClientGinCtx of client request
	ClientGinCtx *gin.Context
	ServerCfg    *config.ServerConfig

	FinishReason    string
	FinalUsage      *openai.Usage
	FinalStatusCode int

	// channel for layerwiseMM
	IsFirstTokenDone      bool
	FirstTokenSendChannel chan struct{}
	firstTokenMutex       sync.Mutex

	// managers
	InferManager *PipelineInfer

	// request related infos, can be shared between pipelines
	RequestContext *RequestContext
}

type RequestContext struct {
	MainGinCtx       *gin.Context
	reqIdMutex       sync.Mutex
	ServingRequestId string
	// message for skip first token in D
	IsFirstTokenFromPrefill *atomic.Bool
	ForceReuseFirstToken    *atomic.Bool
	// parsed request, produced by PreparePhase
	ParsedRequest  *request.GenerateRequestImplV1
	UsagePromptLen atomic.Int32
	// request info for each span
	SubRequests sync.Map
	// request load info
	RequestLoadsCount sync.Map

	Tracer    *trace.TracerWithContext
	CancelCtx *handler.ReqCancelCtx

	// print error by stream format
	IsStreamResponse bool
	PerfMetrics      *handler.PerfMetrics
	// track whether data:[DONE] has been written
	HasDoneWritten bool

	// Synchronization channels
	DoneChannel chan struct{}
	IsDone      bool
	DoneMutex   sync.Mutex

	KVTransferSend   chan struct{}
	IsKVTransferSent bool
	kvTransferMutex  sync.Mutex

	// Pre-appointed instance id, used for instance recover probing, shared between pipelines
	InferMsg *health.InferMsg

	// Error handling across pipelines (moved from PipelineContext)
	errorMutex             sync.RWMutex
	RsErr                  *errs.RSError
	InstanceFailureDetails *utils.InstanceFailureDetails
}

func NewRequestContext(c *gin.Context) *RequestContext {
	forceReuseFirstToken := &atomic.Bool{}
	forceReuseFirstToken.Store(true)
	return &RequestContext{
		SubRequests:             sync.Map{},
		RequestLoadsCount:       sync.Map{},
		MainGinCtx:              c,
		IsFirstTokenFromPrefill: &atomic.Bool{},
		ForceReuseFirstToken:    forceReuseFirstToken,
		HasDoneWritten:          false,
		DoneChannel:             make(chan struct{}),
		KVTransferSend:          make(chan struct{}),
	}
}

func NewPipelineContext(c *gin.Context, isChat bool, infer *PipelineInfer) *PipelineContext {
	if c == nil || infer == nil {
		return nil
	}
	return &PipelineContext{
		IsChat:                isChat,
		IsLayerWise:           infer.Cfg.InferMode == constant.LayerwiseMode || infer.Cfg.InferMode == constant.LayerwiseMMMode,
		ClientGinCtx:          c,
		ServerCfg:             infer.Cfg,
		RequestContext:        NewRequestContext(c),
		IsFirstTokenDone:      false,
		FirstTokenSendChannel: make(chan struct{}),
		InferManager:          infer,
	}
}

func (ctx *PipelineContext) RequestId() string {
	reqCtx := ctx.RequestContext
	if reqCtx.ServingRequestId == "" {
		reqCtx.reqIdMutex.Lock()
		if reqCtx.ServingRequestId == "" {
			var bodyReqId string
			if reqCtx.ParsedRequest != nil {
				bodyReqId = reqCtx.ParsedRequest.BodyReqId
			}
			reqCtx.ServingRequestId = request.GetOrGenerateRequestId(ctx.ClientGinCtx, ctx.IsChat, bodyReqId)
		}
		reqCtx.reqIdMutex.Unlock()
	}
	return reqCtx.ServingRequestId
}

func (ctx *PipelineContext) RequestPath() string {
	if ctx.IsChat {
		return handler.ChatCompletionsPath
	}
	return handler.CompletionsPath
}

func (ctx *PipelineContext) SetError(rsErr *errs.RSError, details *utils.InstanceFailureDetails) {
	reqCtx := ctx.RequestContext
	reqCtx.errorMutex.Lock()
	defer reqCtx.errorMutex.Unlock()

	if !ctx.InferManager.pipeline.isMain && reqCtx.RsErr != nil {
		return
	}
	reqCtx.RsErr = rsErr
	reqCtx.InstanceFailureDetails = details
}

func (ctx *PipelineContext) SetSubRequests(spanName string) {
	ctx.RequestContext.SubRequests.Store(spanName, utils.WrapRequest(ctx.RequestContext.ParsedRequest, false))
	return
}

func (ctx *PipelineContext) GetSubRequests(spanName string) *utils.RemoteRequestWrapper {
	subCtx, exist := ctx.RequestContext.SubRequests.Load(spanName)
	if !exist {
		return nil
	}
	return subCtx.(*utils.RemoteRequestWrapper)
}

func (ctx *PipelineContext) SetRequestCancelCtx(cancelCtx *handler.ReqCancelCtx) {
	ctx.RequestContext.CancelCtx = cancelCtx
}

func (ctx *PipelineContext) GetRequestCancelCtx() *handler.ReqCancelCtx {
	return ctx.RequestContext.CancelCtx
}

func (ctx *PipelineContext) SetTracer(tracer *trace.TracerWithContext) {
	ctx.RequestContext.Tracer = tracer
}

func (ctx *PipelineContext) GetTracer() *trace.TracerWithContext {
	reqCtx := ctx.RequestContext
	if reqCtx.Tracer == nil {
		tracer := trace.NewTracer(ctx.ClientGinCtx, ctx.InferManager.Cfg.TraceCfg, ctx.RequestId(), ctx.ClientGinCtx.Request.Header)
		reqCtx.Tracer = &tracer
	}
	return reqCtx.Tracer
}

func (ctx *PipelineContext) SetParsedRequest(parsedRequest *request.GenerateRequestImplV1) {
	ctx.RequestContext.ParsedRequest = parsedRequest
}

func (ctx *PipelineContext) GetParsedRequest() *request.GenerateRequestImplV1 {
	return ctx.RequestContext.ParsedRequest
}

func (ctx *PipelineContext) GetRequestCtx() *RequestContext {
	return ctx.RequestContext
}

func (ctx *PipelineContext) GetPerfMetrics() *handler.PerfMetrics {
	return ctx.RequestContext.PerfMetrics
}

func (ctx *PipelineContext) WaitForKVTransferSend(ReqTimeoutMS uint) bool {
	reqCtx := ctx.GetRequestCtx()
	if reqCtx.ParsedRequest == nil {
		return false
	}
	if ReqTimeoutMS <= 0 {
		logger.Log().Warnf("timeout is 0 or negative for request [%s], setting default timeout to http client time", ctx.RequestId())
		ReqTimeoutMS = uint(options.GetConfig().HttpConfig.RequestTimeout)
	}

	select {
	case <-reqCtx.KVTransferSend:
		return true
	case <-time.After(time.Duration(ReqTimeoutMS) * time.Millisecond):
		return false
	}
}

func (ctx *PipelineContext) SetKVTransferParams(params map[string]interface{}) {
	reqCtx := ctx.GetRequestCtx()
	if reqCtx.ParsedRequest == nil {
		return
	}
	reqCtx.kvTransferMutex.Lock()
	if !reqCtx.IsKVTransferSent {
		reqCtx.ParsedRequest.KvTransferParams = params
		reqCtx.IsKVTransferSent = true
		close(reqCtx.KVTransferSend)
	}
	reqCtx.kvTransferMutex.Unlock()
}

func (ctx *PipelineContext) SetDone() {
	reqCtx := ctx.GetRequestCtx()
	reqCtx.DoneMutex.Lock()
	if !reqCtx.IsDone {
		reqCtx.IsDone = true
		close(reqCtx.DoneChannel)
	}
	reqCtx.DoneMutex.Unlock()
}

func (ctx *PipelineContext) SetFirstTokenSend() {
	ctx.firstTokenMutex.Lock()
	defer ctx.firstTokenMutex.Unlock()
	if !ctx.IsFirstTokenDone {
		ctx.IsFirstTokenDone = true
		close(ctx.FirstTokenSendChannel)
	}
}
