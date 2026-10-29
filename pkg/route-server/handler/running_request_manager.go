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

package handler

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"infer-dp-scheduler/pkg/common/cluster"
	"infer-dp-scheduler/pkg/common/config"
	http2 "infer-dp-scheduler/pkg/common/http"
	"infer-dp-scheduler/pkg/common/logger"
	"infer-dp-scheduler/pkg/common/trace"
	"infer-dp-scheduler/pkg/common/utils"
)

type RunningRequestsManager struct {
	cfg *config.ServerConfig
	// pipelineName -> phaseName
	phasesName map[string]map[string]interface{}
	reqCtx     map[string]*ReqCancelCtx
	reqCtxMtx  sync.Mutex
}

type phaseInfo struct {
	instanceId     string
	startTimestamp int64
	endTimestamp   int64
	cancelFunc     context.CancelFunc
	isEnd          bool
}

type ReqCancelCtx struct {
	IsCancel       atomic.Bool
	cancellerMtx   sync.Mutex
	phaseInfo      map[string]*phaseInfo
	tracer         trace.RequestTracer
	PipelineReqCtx interface{}
}

func NewRunningRequestsManager(cfg config.ServerConfig) *RunningRequestsManager {
	manager := &RunningRequestsManager{
		cfg:        &cfg,
		phasesName: make(map[string]map[string]interface{}),
		reqCtx:     make(map[string]*ReqCancelCtx),
		reqCtxMtx:  sync.Mutex{},
	}
	return manager
}

func (manager *RunningRequestsManager) SetPhases(pipelineName string, phases []string) {
	manager.reqCtxMtx.Lock()
	defer manager.reqCtxMtx.Unlock()
	if _, exists := manager.phasesName[pipelineName]; !exists {
		manager.phasesName[pipelineName] = make(map[string]interface{})
	}
	for _, phase := range phases {
		manager.phasesName[pipelineName][phase] = nil
	}
}

func (manager *RunningRequestsManager) NewReqCancelCtx(reqId string, tracer trace.RequestTracer) (*ReqCancelCtx, error) {
	manager.reqCtxMtx.Lock()
	defer manager.reqCtxMtx.Unlock()
	if len(manager.reqCtx) == manager.cfg.RateLimitCfg.MaxConcurrentRequests {
		err := errors.New("rejected due to too many requests (429)")
		return nil, err
	}
	reqRecorder := &ReqCancelCtx{
		IsCancel:     atomic.Bool{},
		tracer:       tracer,
		phaseInfo:    make(map[string]*phaseInfo),
		cancellerMtx: sync.Mutex{},
	}
	reqRecorder.IsCancel.Store(false)
	manager.reqCtx[reqId] = reqRecorder
	return reqRecorder, nil
}

func (manager *RunningRequestsManager) GetReqCancelCtx(reqId string) *ReqCancelCtx {
	manager.reqCtxMtx.Lock()
	defer manager.reqCtxMtx.Unlock()
	rc, exists := manager.reqCtx[reqId]
	if !exists {
		return nil
	}
	return rc
}

func (manager *RunningRequestsManager) DeleteReqCancelCtx(reqId string) {
	manager.reqCtxMtx.Lock()
	delete(manager.reqCtx, reqId)
	manager.reqCtxMtx.Unlock()
}

func (rc *ReqCancelCtx) RecordNewPhase(phaseName string) {
	rc.cancellerMtx.Lock()
	timestamp := time.Now().UnixMilli()
	rc.phaseInfo[phaseName] = &phaseInfo{
		startTimestamp: timestamp,
		isEnd:          false,
	}
	rc.cancellerMtx.Unlock()
}

func (rc *ReqCancelCtx) RecordEndPhase(phaseName string) {
	rc.cancellerMtx.Lock()
	timestamp := time.Now().UnixMilli()
	rc.phaseInfo[phaseName].endTimestamp = timestamp
	rc.phaseInfo[phaseName].isEnd = true
	rc.cancellerMtx.Unlock()
}

func (rc *ReqCancelCtx) WrapContext(ctx context.Context, instanceId string, phaseName string) context.Context {
	rc.cancellerMtx.Lock()
	cancelCtx, cancelFunc := context.WithCancel(ctx)
	rc.phaseInfo[phaseName].cancelFunc = cancelFunc
	rc.phaseInfo[phaseName].instanceId = instanceId
	rc.cancellerMtx.Unlock()
	return cancelCtx
}

func (rc *ReqCancelCtx) Abort(phaseList map[string]interface{}) {
	if rc.IsCancel.Load() {
		return
	}
	rc.cancellerMtx.Lock()
	defer rc.cancellerMtx.Unlock()
	rc.IsCancel.Store(true)
	for phaseId, phase := range rc.phaseInfo {
		if _, exist := phaseList[phaseId]; exist {
			if phase.cancelFunc != nil {
				phase.cancelFunc()
			}
		}
	}
	return
}

func (manager *RunningRequestsManager) AbortRequests(ctx *gin.Context) {
	bodyBytes, err := io.ReadAll(ctx.Request.Body)
	if err != nil {
		ctx.JSON(http.StatusBadRequest,
			http2.GinError(fmt.Sprintf("failed to read body, err = %v", err)))
		logger.Log().Infof("[AUDIT] src_ip=%s,action=abort_request,result=failed,reason=read_body_error,error=%v", ctx.ClientIP(), err)
		return
	}
	defer ctx.Request.Body.Close()
	var allReqs cluster.AbortRequest
	if err := utils.SonicApi.Unmarshal(bodyBytes, &allReqs); err != nil {
		ctx.JSON(http.StatusBadRequest,
			http2.GinError(fmt.Sprintf("failed to unmarshal request body, err = %v", err)))
		logger.Log().Infof("[AUDIT] src_ip=%s,action=abort_request,result=failed,reason=parse_error error=%v", ctx.ClientIP(), err)
		return
	}
	if len(allReqs.ReqIds) == 0 {
		ctx.JSON(http.StatusBadRequest,
			http2.GinError("request body is empty or contains no req_ids"))
		return
	}

	var abortedReqs []string
	var notAbortedReqs []string
	manager.reqCtxMtx.Lock()
	for _, reqId := range allReqs.ReqIds {
		reqCanceler := manager.reqCtx[reqId]
		if reqCanceler == nil {
			continue
		}
		for _, phaseNames := range manager.phasesName {
			reqCanceler.Abort(phaseNames)
		}
		reqCanceler.tracer.Event("", "request_cancelled", "request is cancelled by manager abort requests")
		abortedReqs = append(abortedReqs, reqId)
	}
	manager.reqCtxMtx.Unlock()
	abortedReqsMap := make(map[string]struct{})
	for _, reqId := range abortedReqs {
		abortedReqsMap[reqId] = struct{}{}
	}
	for _, reqId := range allReqs.ReqIds {
		if _, exists := abortedReqsMap[reqId]; !exists {
			notAbortedReqs = append(notAbortedReqs, reqId)
		}
	}
	response := cluster.AbortResponse{
		AbortedReqIds:  abortedReqs,
		NotFoundReqIds: notAbortedReqs,
	}
	logger.Log().Infof("[AUDIT] src_ip=%s,action=abort_request,result=success,target=%v,aborted=%v not_found=%v", ctx.ClientIP(), allReqs.ReqIds, abortedReqs, notAbortedReqs)
	ctx.JSON(http.StatusOK, response)
}

func (manager *RunningRequestsManager) HandleRunningDetails(ctx *gin.Context) {
	serverDetails := cluster.ServerDetails{
		ServerName:   manager.cfg.Name,
		RequestLists: make(map[string]cluster.RequestDetails),
	}
	timestamp := time.Now().UnixMilli()
	// use map to collect message, but return slice for easier viewing
	manager.reqCtxMtx.Lock()
	for reqId, req := range manager.reqCtx {
		reqDetails := cluster.RequestDetails{
			RunningPhases:  make(map[string]string),
			FinishedPhases: make(map[string]string),
			WaitingPhases:  make([]string, 0),
		}
		req.cancellerMtx.Lock()
		for _, phaseNames := range manager.phasesName {
			for phaseId, _ := range phaseNames {
				manager.getPhaseDetails(req, phaseId, reqDetails, timestamp)
			}
		}
		req.cancellerMtx.Unlock()
		serverDetails.RequestLists[reqId] = reqDetails
	}
	manager.reqCtxMtx.Unlock()
	ctx.JSON(http.StatusOK, serverDetails)
	return
}

func (manager *RunningRequestsManager) getPhaseDetails(req *ReqCancelCtx, phaseId string, reqDetails cluster.RequestDetails, timestamp int64) {
	phase, exist := req.phaseInfo[phaseId]
	if !exist {
		reqDetails.WaitingPhases = append(reqDetails.WaitingPhases, phaseId)
	} else {
		var runningTimeMs int64
		if phase.isEnd {
			runningTimeMs = phase.endTimestamp - phase.startTimestamp
			phaseDesc := fmt.Sprintf("running for %d ms", runningTimeMs)
			if phase.instanceId != "" {
				phaseDesc += fmt.Sprintf(" on instance [%s]", phase.instanceId)
			}
			reqDetails.FinishedPhases[phaseId] = phaseDesc
		} else {
			runningTimeMs = timestamp - phase.startTimestamp
			phaseDesc := fmt.Sprintf("running for %d ms", runningTimeMs)
			if phase.instanceId != "" {
				phaseDesc += fmt.Sprintf(" on instance [%s]", phase.instanceId)
			}
			reqDetails.RunningPhases[phaseId] = phaseDesc
		}
	}
}

func (manager *RunningRequestsManager) AbortByPipeline(reqId string, pipelineName string, subPipelines []string) {
	manager.reqCtxMtx.Lock()
	defer manager.reqCtxMtx.Unlock()

	reqCancelCtx, exist := manager.reqCtx[reqId]
	if !exist {
		return
	}
	if phaseNames, exist := manager.phasesName[pipelineName]; exist {
		reqCancelCtx.Abort(phaseNames)
	}
	for _, subPipeline := range subPipelines {
		if phaseNames, exist := manager.phasesName[subPipeline]; exist {
			reqCancelCtx.Abort(phaseNames)
		}
	}
}
