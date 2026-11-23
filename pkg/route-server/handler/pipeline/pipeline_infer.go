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
	"strconv"
	"sync"

	"github.com/gin-gonic/gin"
	"route-server/pkg/common/client"
	commoncfg "route-server/pkg/common/config"
	"route-server/pkg/common/constant"
	"route-server/pkg/common/errs"
	"route-server/pkg/common/health"
	"route-server/pkg/common/logger"
	"route-server/pkg/route-server/handler"
	"route-server/pkg/route-server/instances"
	"route-server/pkg/route-server/metrics"
	"route-server/pkg/route-server/scheduler"
)

type PipelineInfer struct {
	Cfg                   *commoncfg.ServerConfig
	SchedulerMgr          scheduler.Manager
	InstanceMgr           instances.Manager
	RunningRequestManager *handler.RunningRequestsManager
	ResHeaderBlackSet     map[string]bool
	WorkerEventCollector  *health.WorkerEventCollector

	pipeline     *Pipeline
	subPipelines []*PipelineInfer

	vllmAscendAbortedMu       sync.Mutex
	vllmAscendAbortedRequests map[string]map[string]struct{}
}

func (pi *PipelineInfer) RecordVllmAscendAbortedRequest(prefillerInstanceId string, requestId string) {
	pi.vllmAscendAbortedMu.Lock()
	defer pi.vllmAscendAbortedMu.Unlock()
	if pi.vllmAscendAbortedRequests == nil {
		pi.vllmAscendAbortedRequests = make(map[string]map[string]struct{})
	}
	if _, ok := pi.vllmAscendAbortedRequests[prefillerInstanceId]; !ok {
		pi.vllmAscendAbortedRequests[prefillerInstanceId] = make(map[string]struct{})
	}
	pi.vllmAscendAbortedRequests[prefillerInstanceId][requestId] = struct{}{}
}

func (pi *PipelineInfer) AcquireVllmAscendAbortedRequests(prefillerInstanceId string) []string {
	pi.vllmAscendAbortedMu.Lock()
	defer pi.vllmAscendAbortedMu.Unlock()
	requests, ok := pi.vllmAscendAbortedRequests[prefillerInstanceId]
	if !ok {
		return nil
	}
	delete(pi.vllmAscendAbortedRequests, prefillerInstanceId)
	result := make([]string, 0, len(requests))
	for id := range requests {
		result = append(result, id)
	}
	return result
}

func NewPipelineInferWithMode(inferMode string, isMain bool, instanceMgr instances.Manager, schedulerMgr scheduler.Manager,
	cfg commoncfg.ServerConfig, workerEventCollector *health.WorkerEventCollector, runningRequestsManager *handler.RunningRequestsManager) (*PipelineInfer, error) {
	if instanceMgr == nil {
		return nil, fmt.Errorf("given instance manager is nil")
	}

	if schedulerMgr == nil {
		return nil, fmt.Errorf("given scheduler manager is nil")
	}

	if workerEventCollector == nil {
		return nil, fmt.Errorf("given workerEventCollector is nil")
	}

	if runningRequestsManager == nil {
		return nil, fmt.Errorf("running requests manager is nil")
	}

	pipeline, err := NewPipeline(inferMode, isMain)
	if err != nil {
		return nil, fmt.Errorf("failed to create pipeline for type [%s], error: %v", inferMode, err)
	}

	if err := configureCoordRouting(pipeline, &cfg); err != nil {
		return nil, err
	}

	phasesNameLookUp := make([]string, 0)
	for _, phase := range pipeline.phases {
		phasesNameLookUp = append(phasesNameLookUp, phase.PhaseName())
	}
	runningRequestsManager.SetPhases(inferMode, phasesNameLookUp)

	pipelineInfer := &PipelineInfer{
		Cfg:                   &cfg,
		SchedulerMgr:          schedulerMgr,
		InstanceMgr:           instanceMgr,
		RunningRequestManager: runningRequestsManager,
		WorkerEventCollector:  workerEventCollector,
		ResHeaderBlackSet:     commoncfg.NewResHeaderBlackSet(cfg.ResponseHeaderBlacklist),
		pipeline:              pipeline,
	}

	return pipelineInfer, nil
}

func NewPipelineInfer(instanceMgr instances.Manager, schedulerMgr scheduler.Manager,
	cfg commoncfg.ServerConfig, workerEventCollector *health.WorkerEventCollector, runningRequestsManager *handler.RunningRequestsManager) (*PipelineInfer, error) {
	inferMode := cfg.InferMode
	return NewPipelineInferWithMode(inferMode, true, instanceMgr, schedulerMgr, cfg, workerEventCollector, runningRequestsManager)
}

func (pi *PipelineInfer) CompletionsInfer(ctx *gin.Context) {
	pipelineCtx := NewPipelineContext(ctx, pi)
	pi.doInfer(pipelineCtx)
}

func (pi *PipelineInfer) ChatCompletionsInfer(ctx *gin.Context) {
	pipelineCtx := NewPipelineContext(ctx, pi)
	pi.doInfer(pipelineCtx)
}

// MessagesInfer handles the Anthropic /v1/messages endpoint through the pipeline.
func (pi *PipelineInfer) MessagesInfer(ctx *gin.Context) {
	pipelineCtx := NewPipelineContext(ctx, pi)
	pi.doInfer(pipelineCtx)
}

func (pi *PipelineInfer) IsStageInPipelines(stageName string) bool {
	if pi.pipeline.IsStageInPipeline(stageName) {
		return true
	}
	for _, subPipeline := range pi.subPipelines {
		if subPipeline.pipeline.IsStageInPipeline(stageName) {
			return true
		}
	}
	return false
}

func (pi *PipelineInfer) getAllStageChain() []string {
	allStages := make([]string, 0)

	allStages = append(allStages, pi.pipeline.GetStageChain()...)

	for _, subPipeline := range pi.subPipelines {
		allStages = append(allStages, subPipeline.pipeline.GetStageChain()...)
	}

	return allStages
}

func (pi *PipelineInfer) SetSubPipelines(subPipelines []*PipelineInfer) {
	pi.subPipelines = subPipelines
}

func (pi *PipelineInfer) TestInstance(ctx *gin.Context, path string, isChat bool, instance *client.Instance) (successToCheck bool, testSuccess bool, errDetails *errs.RSError) {
	if instance == nil {
		return false, false, errs.NewRSMaError(errs.InternalErr.BuildArgs(
			"given test instance is nil"), "")
	}

	role := instance.Stats.GetRole()
	targetStageName, exists := constant.FailureRoleStageMap[role]
	if !exists || !pi.IsStageInPipelines(targetStageName) {
		logger.Log().Warnf("skip prob stage for [%s] instance [%s] because serving mode is [%s]",
			instance.Stats.GetRole(), instance.ID, pi.pipeline.Name)
		return true, true, nil
	}

	inferMsg := &health.InferMsg{
		Instances: map[string]string{
			role: instance.ID,
		},
		IsTesting: true,
		SkipFlags: map[string]string{},
	}

	pipelineCtx := NewPipelineContext(ctx, pi)
	pipelineCtx.RequestContext.InferMsg = inferMsg
	pi.doInfer(pipelineCtx)

	// For multi pipelines, Because the subPipeline should be evoked by vllm instance
	// So we just need to send probe request once and get the result from RequestContext
	inferErr := pipelineCtx.RequestContext.RsErr

	if inferErr == nil {
		return true, true, nil
	}

	if !pi.IsStageInPipelines(inferErr.ErrorStage) {
		logger.Log().Errorf("error stage [%s] not found in pipeline [%s] stage chain [%v] for prob req [%s], details err = %s",
			inferErr.ErrorStage, pi.pipeline.Name,
			pi.pipeline.GetStageChain(), pipelineCtx.RequestId(), inferErr.Error())
		return false, false, inferErr
	}

	successToCheck, testSuccess = handler.IsTargetPhasePassed(targetStageName, pi.getAllStageChain(), instance.FaultType, inferErr)
	return successToCheck, testSuccess, inferErr
}

func (pi *PipelineInfer) doInfer(ctx *PipelineContext) {
	if ctx == nil {
		logger.Log().Warnf("pipeline doInfer called with nil pipeline context")
		return
	}
	if ctx.ClientGinCtx == nil {
		logger.Log().Warnf("pipeline doInfer called with nil gin context")
		return
	}

	if ctx.ClientGinCtx == nil || ctx.ClientGinCtx.Request == nil {
		ctx.ClientGinCtx.JSON(errs.EmptyReqErr.Status, errs.EmptyReqErr.Json())
		metrics.RecordFailedRequestStatus(strconv.Itoa(errs.EmptyReqErr.Status), ctx.RequestPath())
		ctx.SetError(errs.NewRSMaError(errs.EmptyReqErr.BuildArgs(), constant.StagePrepare), nil)
		return
	}

	// Release the coord reservation however this request ends: success, failure,
	// cancellation or timeout. The normal path releases earlier once the
	// response is written; releasing again is a no-op because the slot is keyed
	// by the reservation token.
	defer releaseCoordReservation(ctx)

	metrics.UpdateRunningRequests(constant.RequestSrcUser, 1)
	defer metrics.UpdateRunningRequests(constant.RequestSrcUser, -1)

	pi.pipeline.ExecutePipeline(ctx)
}
