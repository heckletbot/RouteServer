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

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"infer-dp-scheduler/pkg/common/utils"

	"github.com/gin-gonic/gin"
	"infer-dp-scheduler/pkg/common/cluster"
	"infer-dp-scheduler/pkg/common/config"
	"infer-dp-scheduler/pkg/common/constant"
	commonHttp "infer-dp-scheduler/pkg/common/http"
	"infer-dp-scheduler/pkg/common/logger"
	scheduler2 "infer-dp-scheduler/pkg/common/scheduler"
	"infer-dp-scheduler/pkg/common/trace"
	"infer-dp-scheduler/pkg/route-server/metrics"
	"infer-dp-scheduler/pkg/route-server/scheduler"
)

type RemoteScheduleHandler struct {
	tracerCfg      *config.TraceConfig
	manager        scheduler.Manager
	clusterService cluster.ClusterService
	selfName       string
	requiredRoles  []string
}

func (rsh *RemoteScheduleHandler) HandleSchedule(ctx *gin.Context) {
	if !rsh.clusterService.IsLeader() {
		ctx.JSON(http.StatusServiceUnavailable, commonHttp.GinError("not leader"))
		return
	}
	request, err := ParseScheduleRequest(ctx)
	defer func(Body io.ReadCloser) {
		if bodyErr := Body.Close(); bodyErr != nil {
			logger.Log().Warnf("failed to close schedule req body, err = %v", bodyErr)
		}
	}(ctx.Request.Body)
	if err != nil {
		ctx.JSON(http.StatusBadRequest,
			commonHttp.GinError(fmt.Sprintf("failed to parse scheudle request, err = %v", err)))
		return
	}
	tracer := trace.NewTracer(ctx, rsh.tracerCfg, request.Request.GetRequestId(), ctx.Request.Header)

	res, err := rsh.doHandleSchedule(ctx.Request.Context(), request, tracer.Tracer)
	metrics.RecordRemoteScheduled()
	if err != nil {
		tracer.Tracer.Event("", trace.TraceEventDealRemoteSchedule, fmt.Sprintf(
			"failed to deal income remote schedule for [%s] req [%s], details %s",
			request.Request.GetType(), request.Request.GetRequestId(), err.Error()))
		ctx.JSON(http.StatusInternalServerError, commonHttp.GinError(err.Error()))
		return
	}
	tracer.Tracer.Event("", trace.TraceEventDealRemoteSchedule, fmt.Sprintf(
		"remote schedule for [%s] req [%s] to [%v]", request.Request.GetType(),
		request.Request.GetRequestId(), res.Result))
	ctx.JSON(http.StatusOK, res)
}

func (rsh *RemoteScheduleHandler) doHandleSchedule(ctx context.Context, request *scheduler.RemoteScheduleRequest,
	tracer trace.RequestTracer) (*scheduler.ScheduleResponse, error) {
	res, err := rsh.manager.DispatchRequest(ctx, request.Request, request.Params, tracer, request.PreInstanceId, scheduler.RemoteRequestInfo{IsLocal: false, WorkerId: request.WorkerId})
	if err != nil {
		return nil, fmt.Errorf("failed to schedule request, err = %v", err)
	}
	if res.Result == "" {
		logger.Log().Warnf("remote schedule provide empty array for [%s] request [%s]",
			request.Request.GetRequestId(), request.Request.GetType())
	}
	return res, nil
}

func ParseScheduleRequest(ctx *gin.Context) (*scheduler.RemoteScheduleRequest, error) {
	req := scheduler.RemoteScheduleRequest{}
	if err := json.NewDecoder(ctx.Request.Body).Decode(&req); err != nil {
		return nil, fmt.Errorf("failed to parse req body, expect RemoteScheduleRequest, err = %v", err)
	}
	if req.Request == nil {
		return nil, fmt.Errorf("req is required field, must not be empty")
	}
	if err := utils.CheckResourceNameValid(req.Request.Id); err != nil {
		return nil, fmt.Errorf("invalid request_id: %v", err)
	}
	if err := utils.CheckResourceNameValid(req.Request.Type); err != nil {
		return nil, fmt.Errorf("invalid request type: %v", err)
	}
	if req.CandidateNum == 0 {
		req.CandidateNum = 1
	}
	return &req, nil
}

func (rsh *RemoteScheduleHandler) ReportLoads(ctx *gin.Context) {
	includeRequests, err := strconv.ParseBool(ctx.DefaultQuery("include_requests", "false"))
	if err != nil {
		includeRequests = false
	}
	includeUnavailable, err := strconv.ParseBool(ctx.DefaultQuery("include_unavailable", "false"))
	if err != nil {
		includeUnavailable = false
	}
	roleParam := ctx.Query("role")
	roles := make([]string, 0)
	if roleParam == "" {
		roles = []string{}
		for _, role := range rsh.requiredRoles {
			if !utils.InMap(role, constant.NeedHandleReqRoleMap) {
				continue
			}
			roles = append(roles, role)
		}
	} else {
		roles = strings.Split(roleParam, ",")
	}
	res := &SchedulerLoadResponse{
		RsName: rsh.selfName,
		Loads:  rsh.manager.ReportLoads(includeRequests, includeUnavailable, roles),
	}
	ctx.JSON(http.StatusOK, res)
}

type SchedulerLoadResponse struct {
	RsName string                             `json:"rs_name"`
	Loads  map[string]*scheduler2.SLOLoadInfo `json:"loads,omitempty"`
}
