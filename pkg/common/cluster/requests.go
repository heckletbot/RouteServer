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

package cluster

import (
	"fmt"

	"infer-dp-scheduler/pkg/common/config"
	"infer-dp-scheduler/pkg/common/utils"
)

type LeaveRequest struct {
	LeavePeerName string `json:"name"`
}

type LeaderResponse struct {
	Name           string      `json:"id"`
	LeaderPeerInfo *utils.Peer `json:"peer_info,omitempty"`
}

type LocalStateResponse struct {
	State *ClusterState `json:"state,omitempty"`
}

type ConsistentResponse struct {
	Stats map[string]string `json:"stats,omitempty"`
}

type InstanceJoinRequest struct {
	Instances map[string]*config.Route `json:"instances"`
}

type InstanceLeaveRequest struct {
	InstanceNames []string `json:"instance_names"`
}

func (req *InstanceJoinRequest) InstanceNames() string {
	return utils.MapKeysStr(req.Instances)
}

func (req *InstanceJoinRequest) ValidCheck() error {
	err := CheckRoutesValidity(req.Instances)
	if err != nil {
		return err
	}
	return nil
}

func CheckRoutesValidity(routes map[string]*config.Route) error {
	for insId, route := range routes {
		if err := config.CheckInstanceValid(route); err != nil {
			return err
		}
		if insId == "" {
			return fmt.Errorf("instance id must not be empty, got empty instance id for host=%s, port=%v",
				route.Ip, route.Port)
		}
		if err := utils.CheckResourceNameValid(insId); err != nil {
			return fmt.Errorf("instance id is invalid: %v", err)
		}
		if err := utils.CheckResourceNameValid(route.Role); err != nil {
			return fmt.Errorf("instance role is invalid for host=%s, port=%v: %v", route.Ip, route.Port, err)
		}
		if route.Tag != "" {
			if err := utils.CheckResourceNameValid(route.Tag); err != nil {
				return fmt.Errorf("instance tag is invalid for host=%s, port=%v: %v", route.Ip, route.Port, err)
			}
		}
		if err := utils.CheckSLOTagValid(insId, route.Tag); err != nil {
			return err
		}
	}
	return nil
}

func CheckRoutesInsMapValidity(routes map[string]config.Route) error {
	pointerMap := make(map[string]*config.Route)
	for insId, route := range routes {
		pointerMap[insId] = &route
	}
	return CheckRoutesValidity(pointerMap)
}

func (req *InstanceLeaveRequest) ValidCheck() error {
	for _, name := range req.InstanceNames {
		if name == "" {
			return fmt.Errorf("instance id must not be empty")
		}
		if err := utils.CheckResourceNameValid(name); err != nil {
			return fmt.Errorf("instance id is invalid: %v", err)
		}
	}
	return nil
}

type AbortRequest struct {
	ReqIds []string `json:"req_ids"`
}

type AbortResponse struct {
	AbortedReqIds  []string `json:"aborted_req_ids"`
	NotFoundReqIds []string `json:"not_found_req_ids"`
}

type RunningDetails struct {
	ServerLists []ServerDetails `json:"server_lists"`
}

type ServerDetails struct {
	ServerName   string                    `json:"server_name"`
	RequestLists map[string]RequestDetails `json:"request_lists"`
}

type RequestDetails struct {
	FinishedPhases map[string]string `json:"finished_phases"`
	RunningPhases  map[string]string `json:"running_phases"`
	WaitingPhases  []string          `json:"waiting_phases"`
}
