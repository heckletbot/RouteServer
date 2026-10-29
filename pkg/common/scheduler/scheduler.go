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

package scheduler

import (
	"infer-dp-scheduler/pkg/common/instance-metrics"
	"infer-dp-scheduler/pkg/common/utils"
)

type ScheduleAlgorithm interface {
	Init(config map[string]string) error
	GetName() string
	// NeedStatsList The list of statistics required by the algorithm is provided for upper-layer verification. If this parameter cannot be provided, it will be used for algorithm downgrade on the management plane.
	NeedStatsList() []string
	// UpdateCluster The worker stats update interface. Invoked once at the specified GetUpdateInterval interval to synchronize the status information reported by all instances.
	UpdateCluster(available map[string]instance_metrics.WorkerStats, unavailable map[string]instance_metrics.WorkerStats) error
	// UpdateConfig Reversed interface to dynamically update algo config.
	UpdateConfig(config map[string]string) error
	// GetUpdateInterval Interval needed for updating the worker status, in milliseconds. The value -1 indicates that the worker status does not need to be updated for this algorithm. The minimum value is 10 ms.
	GetUpdateInterval() int64
	// DispatchRequest Scheduling interface, which should return a single optimal candidate worker.
	DispatchRequest(request utils.GenerateRequest,
		params map[string]string, insBlackMap map[string]interface{}) (string, bool, error)
	OnRequestEvent(event *utils.RequestEventMsg)
	ClearLoad()
	ReportLoads(includeRequests bool, includeUnavailable bool) *AllInstanceLoads
}

type AlgoLoadInfo struct {
	AlgorithmName string            `json:"algorithm,omitempty"`
	InstanceLoads *AllInstanceLoads `json:"instance_loads,omitempty"`
}

type SLOLoadInfo struct {
	SLOGroupLoads map[string]*AlgoLoadInfo `json:"slo_group_loads,omitempty"`
	ErrReason     string                   `json:"err_reason,omitempty"`
}

type AllInstanceLoads struct {
	Available   []*InstanceLoadInfo `json:"available,omitempty"`   // sort by loadVal
	Unavailable []*InstanceLoadInfo `json:"unavailable,omitempty"` // sort by loadVal
}

type InstanceLoadInfo struct {
	Id       string                      `json:"id"`
	LoadVal  float64                     `json:"load"`
	Desc     string                      `json:"desc"`
	Requests map[string]*RequestLoadInfo `json:"requests,omitempty"`
}

type RequestLoadInfo struct {
	LoadVal float64 `json:"load"`
	Desc    string  `json:"desc"`
}
