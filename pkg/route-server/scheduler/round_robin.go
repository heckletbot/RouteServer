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
	"sync"
	"sync/atomic"

	instance_metrics "infer-dp-scheduler/pkg/common/instance-metrics"
	"infer-dp-scheduler/pkg/common/logger"
	"infer-dp-scheduler/pkg/common/scheduler"
	"infer-dp-scheduler/pkg/common/utils"
)

type State struct {
}

type RoundRobin struct {
	mu        sync.RWMutex
	instances []string
	currIndex int64
}

func (r *RoundRobin) ClearLoad() {
}

func (r *RoundRobin) ReportLoads(bool, bool) *scheduler.AllInstanceLoads {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ret := make([]*scheduler.InstanceLoadInfo, 0, len(r.instances))
	for _, instance := range r.instances {
		ret = append(ret, &scheduler.InstanceLoadInfo{
			Id:       instance,
			LoadVal:  0,
			Desc:     "rr",
			Requests: nil,
		})
	}
	res := &scheduler.AllInstanceLoads{
		Available:   ret,
		Unavailable: nil,
	}
	return res
}

func (r *RoundRobin) OnRequestEvent(event *utils.RequestEventMsg) {
	logger.Log().Debugf("[%s] request [%s] is done, event_type is [%v], processed instance [%s]",
		event.Request.GetType(), event.Request.GetRequestId(), event.EventType, event.TargetInstance)
}

// Init make round-robin init don't return error
func (r *RoundRobin) Init(config map[string]string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.instances = make([]string, 0)
	r.currIndex = 0
	return nil
}

func (r *RoundRobin) GetName() string {
	return RoundRobinAlgoName
}

func (r *RoundRobin) NeedStatsList() []string {
	return []string{}
}

func (r *RoundRobin) UpdateCluster(available map[string]instance_metrics.WorkerStats, unavailable map[string]instance_metrics.WorkerStats) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	instancesCopy := make([]string, 0, len(available))
	newNodeIds := make(map[string]struct{}, len(available))
	for id := range available {
		newNodeIds[id] = struct{}{}
		instancesCopy = append(instancesCopy, id)
	}

	if len(r.instances) > 0 && len(instancesCopy) > 0 {
		currentIndex := atomic.LoadInt64(&r.currIndex)
		atomic.StoreInt64(&r.currIndex, currentIndex%int64(len(instancesCopy)))
	} else {
		atomic.StoreInt64(&r.currIndex, 0)
	}

	r.instances = instancesCopy
	return nil
}

func (r *RoundRobin) UpdateConfig(config map[string]string) error {
	return nil
}

func (r *RoundRobin) GetUpdateInterval() int64 {
	// no need update unless nodes changed
	return -1
}

func (r *RoundRobin) DispatchRequest(request utils.GenerateRequest, params map[string]string, insBlackMap map[string]interface{}) (string, bool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	nodesCount := int64(len(r.instances))
	if nodesCount == 0 {
		return "", false, nil
	}
	oldIndex := atomic.AddInt64(&r.currIndex, 1) - 1
	if oldIndex < 0 {
		// overflowed, reset to 1
		oldIndex = 0
	}
	offset := oldIndex % nodesCount

	var idx int64
	skipped := false
	for idx = 0; idx < nodesCount; idx++ {
		if insBlackMap != nil {
			_, exist := insBlackMap[r.instances[(offset+idx)%nodesCount]]
			if exist {
				skipped = true
				continue
			}
		}
		return r.instances[(offset+idx)%nodesCount], skipped, nil
	}
	return "", skipped, nil
}
