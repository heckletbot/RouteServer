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

package instance_metrics

import (
	"fmt"
	"sync"
	"time"

	"infer-dp-scheduler/pkg/common/constant"
	"infer-dp-scheduler/pkg/common/logger"
)

type WorkerStats interface {
	GetRole() string                               // Node role, prefill, decode or encode
	GetId() string                                 // Node ID
	GetNumericStat(statusKey string) []*MetricInfo // Node status, key is id, return value is status value, Float.Nan if not exists
	GetNumericStatKeys() *[]string
	UpdateNumericStats(*map[string][]*MetricInfo, time.Time) WorkerStats
	GetWorkerStats() WorkerStats
	GetStatus() InstanceStatus
	GetPreStatus() InstanceStatus
	GetSLOTag() string
	GetTimeStamp() time.Time
	SetStatus(status InstanceStatus)
}

type InstanceStatus int

const (
	Init      InstanceStatus = 1
	Available InstanceStatus = 2
	Faulty    InstanceStatus = 3
)

// ZeroCopyStats stats that sync localy in single RouteServer
type ZeroCopyStats struct {
	Id           string
	Role         string
	Status       InstanceStatus
	PreStatus    InstanceStatus
	StaticInfo   map[string]string
	NumericStats *map[string][]*MetricInfo
	statsMutex   sync.RWMutex
	TimeStamp    time.Time
	SLOTag       string
}

func (zcs *ZeroCopyStats) UpdateNumericStats(m *map[string][]*MetricInfo, timeStamp time.Time) WorkerStats {
	if m == nil {
		return zcs
	}
	zcs.statsMutex.Lock()
	defer zcs.statsMutex.Unlock()
	zcs.NumericStats = m
	zcs.TimeStamp = timeStamp
	return zcs
}

func (zcs *ZeroCopyStats) GetWorkerStats() WorkerStats {
	return zcs
}

func (zcs *ZeroCopyStats) GetNumericStatKeys() *[]string {
	zcs.statsMutex.RLock()
	defer zcs.statsMutex.RUnlock()
	keys := make([]string, 0, len(*zcs.NumericStats))
	for k := range *(zcs.NumericStats) {
		keys = append(keys, k)
	}
	return &keys
}

func (zcs *ZeroCopyStats) GetStatus() InstanceStatus {
	return zcs.Status
}

func (zcs *ZeroCopyStats) GetSLOTag() string {
	if zcs.SLOTag == "" {
		zcs.SLOTag = constant.SLOTagDefault
	}
	return zcs.SLOTag
}

func (zcs *ZeroCopyStats) GetPreStatus() InstanceStatus {
	return zcs.PreStatus
}

func (zcs *ZeroCopyStats) GetTimeStamp() time.Time {
	return zcs.TimeStamp
}

func (zcs *ZeroCopyStats) SetStatus(status InstanceStatus) {
	zcs.PreStatus = zcs.Status
	zcs.Status = status
}

func (zcs *ZeroCopyStats) GetNumericStat(statusKey string) []*MetricInfo {
	if zcs.NumericStats != nil {
		zcs.statsMutex.RLock()
		defer zcs.statsMutex.RUnlock()
		if numericStat, exist := (*zcs.NumericStats)[statusKey]; exist {
			return numericStat
		}
	}
	return nil
}

func (zcs *ZeroCopyStats) GetRole() string {
	return zcs.Role
}

func (zcs *ZeroCopyStats) GetId() string {
	return zcs.Id
}

func (zcs *ZeroCopyStats) GetStat(statusName string) string {
	if staticInfo, exists := zcs.StaticInfo[statusName]; exists {
		return staticInfo
	}
	return ""
}

func (s InstanceStatus) String() string {
	switch s {
	case Init:
		return constant.DeployingStatus
	case Available:
		return constant.RunningStatus
	case Faulty:
		return constant.FaultStatus
	default:
		return fmt.Sprintf("Unknown(%d)", s)
	}
}

func InstanceStatusFromString(status string) InstanceStatus {
	switch status {
	case constant.DeployingStatus:
		return Init
	case constant.RunningStatus:
		return Available
	case constant.FaultStatus:
		return Faulty
	default:
		logger.Log().Errorf("Invalid instance status: %s", status)
		return Faulty
	}
}
