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

package cluster

import (
	"infer-dp-scheduler/pkg/common/logger"
	"infer-dp-scheduler/pkg/route-server/metrics"
)

type InstanceLeaveHandler struct {
	cs ClusterService
}

func (i *InstanceLeaveHandler) HandleMessage(message interface{}) {
	leaveRequest, ok := message.(*InstanceLeaveRequest)
	if !ok {
		logger.Log().Errorf("invalid message type, expected *cluster.InstanceLeaveRequest, got %T", message)
		return
	}
	logger.Log().Tracef("receive instance leave from message center, details: %v", leaveRequest.InstanceNames)

	cleanRes := i.getLeaveRoleCount(leaveRequest)

	err := i.cs.HandleLeaveInstance(leaveRequest, LeaveReasonFailed)
	if err != nil {
		logger.Log().Warnf("failed to handle leave instance from message center: %v", err)
	}
	// leave success, record metrics
	for role, count := range cleanRes {
		metrics.RecordDiscardUnreachableEndpoints(role, count)
	}
}

func (i *InstanceLeaveHandler) getLeaveRoleCount(leaveRequest *InstanceLeaveRequest) map[string]int {
	var cleanRes = make(map[string]int)
	if state := i.cs.State(); state != nil && state.InstanceRoutes != nil {
		for _, insName := range leaveRequest.InstanceNames {
			if route := state.InstanceRoutes[insName]; route != nil {
				clean, exists := cleanRes[route.Role]
				if exists {
					clean += 1
				} else {
					clean = 1
				}
				cleanRes[route.Role] = clean
			}
		}
	}
	return cleanRes
}

func NewInstanceLeaveMessageHandler(clusterService ClusterService) *InstanceLeaveHandler {
	return &InstanceLeaveHandler{
		cs: clusterService,
	}
}
