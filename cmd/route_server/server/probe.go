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

package server

import (
	"net/http"

	"route-server/pkg/common/cluster"
	"route-server/pkg/common/config"
	"route-server/pkg/common/constant"
	"route-server/pkg/common/logger"
	"route-server/pkg/route-server/instances"

	"github.com/gin-gonic/gin"
)

type ProbeHandler struct {
	ClusterService  cluster.ClusterService
	InstanceManager instances.Manager
	ProbeCfg        config.ProbeCfg
}

func NewProbeHandler(clusterService cluster.ClusterService, instanceManager instances.Manager, rootCfg *config.Config) *ProbeHandler {
	initReadinessCfg(rootCfg)
	probeCfg := rootCfg.ProbeCfg
	return &ProbeHandler{
		ClusterService:  clusterService,
		InstanceManager: instanceManager,
		ProbeCfg:        probeCfg,
	}
}

func initReadinessCfg(rootCfg *config.Config) {
	requiredRoles := rootCfg.InstanceManagerCfg.RequiredRoles
	readiness := rootCfg.ProbeCfg.Readiness
	for _, role := range requiredRoles {
		if _, exists := readiness[role]; !exists {
			readiness[role] = 1
			logger.Log().Warnf("Role %s readiness least num is not configed, use default 1", role)
		}
	}
}

func (p *ProbeHandler) LivenessCheck(ctx *gin.Context) {
	ctx.String(http.StatusOK, "ok")
}

// Only check if self is Leader
func (p *ProbeHandler) LeaderReadinessCheck(ctx *gin.Context) {
	ctx.JSON(http.StatusOK, gin.H{
		constant.IsLeader: p.ClusterService.IsLeader(),
	})
}

// Only return instances number
func (p *ProbeHandler) WorkerReadinessCheck(ctx *gin.Context) {
	status := p.InstanceManager.GetReadinessStatus(p.ProbeCfg.Readiness)
	response := gin.H{
		constant.IsReady: status.Ready,
		constant.Msg:     status.Message,
	}

	// Add status information
	if status.State > 0 {
		response["state"] = status.State
		response["state_name"] = status.StateName
	}

	if status.Ready {
		ctx.JSON(http.StatusOK, response)
	} else {
		logger.Log().Warnf("[WorkerReadinessCheck] Not ready: %s (state=%d, stateName=%s)",
			status.Message, status.State, status.StateName)
		ctx.JSON(http.StatusInternalServerError, response)
	}
}
