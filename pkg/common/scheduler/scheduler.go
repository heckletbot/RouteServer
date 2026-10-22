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

package scheduler

import (
	"infer-dp-scheduler/pkg/common/instance-metrics"
	"infer-dp-scheduler/pkg/common/utils"
)

type ScheduleAlgorithm interface {
	Init(config map[string]string) error
	GetName() string
	NeedStatsList() []string                                                                                                    // 给出算法需要的统计量列表信息，用于上层校验，如无法提供该参数，用于管理面后续进行算法降级
	UpdateCluster(available map[string]instance_metrics.WorkerStats, unavailable map[string]instance_metrics.WorkerStats) error // 状态更新接口，每隔声明的GetUpdateInterval时间间隔，就会调用这个一次，同步所有实例上报的状态信息
	UpdateConfig(config map[string]string) error                                                                                // 配置动态更新接口，部分参数可不重启生效（暂未实现Rest配置变更接口，可以先空着）
	GetUpdateInterval() int64                                                                                                   // 需要更新worker状态的周期（ms），-1为不需要更新worker状态，最小10ms
	DispatchRequest(request utils.GenerateRequest,
		params map[string]string, insBlackMap map[string]interface{}) (string, bool, error) // 调度接口， 可支持返回多个候选worker，可用于后续失败顺延
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
