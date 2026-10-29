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
	"fmt"
	"math/rand"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	instance_metrics "infer-dp-scheduler/pkg/common/instance-metrics"
	"infer-dp-scheduler/pkg/common/logger"
	"infer-dp-scheduler/pkg/common/scheduler"
	"infer-dp-scheduler/pkg/common/utils"
)

const (
	PrintingRandomSchedulerName = "StatsPrintingRandom"
	UpdateIntervalKey           = "update_interval"
	NeedStatsKey                = "needStats"
)

type TestScheduler struct {
	mutex            sync.RWMutex
	HasInit          bool
	Config           map[string]string
	syncMutex        sync.RWMutex
	stats            map[string]instance_metrics.WorkerStats
	Ids              []string
	UpdateInterval   int
	StatsUpdateCount atomic.Int32
	NeedStats        []string
	instances        []string
	LoadClearTime    int
}

func (t *TestScheduler) ClearLoad() {
	t.LoadClearTime++
}

func (l *TestScheduler) ReportLoads(bool, bool) *scheduler.AllInstanceLoads {
	l.syncMutex.RLock()
	defer l.syncMutex.RUnlock()
	ret := make([]*scheduler.InstanceLoadInfo, 0, len(l.instances))
	for _, instance := range l.instances {
		ret = append(ret, &scheduler.InstanceLoadInfo{
			Id:       instance,
			LoadVal:  0,
			Desc:     "random",
			Requests: nil,
		})
	}
	res := &scheduler.AllInstanceLoads{
		Available:   ret,
		Unavailable: nil,
	}
	return res
}

func (t *TestScheduler) OnRequestEvent(event *utils.RequestEventMsg) {
	logger.Log().Infof("[%s] request [%s] is done, event_type [%v], processed instance [%s]",
		event.Request.GetType(), event.Request.GetRequestId(), event.EventType, event.TargetInstance)
}

func (t *TestScheduler) Init(config map[string]string) error {
	t.HasInit = true
	t.Config = config
	if v, exists := config[UpdateIntervalKey]; exists {
		num, err := strconv.Atoi(v)
		if err == nil {
			t.UpdateInterval = num
		}
	}
	statLine, exists := config["needStats"]
	if len(statLine) > 0 && exists {
		t.NeedStats = strings.Split(statLine, ",")
	}
	t.StatsUpdateCount.Store(0)
	return nil
}

func (t *TestScheduler) GetName() string {
	return PrintingRandomSchedulerName
}

func (t *TestScheduler) NeedStatsList() []string {
	return t.NeedStats
}

func (t *TestScheduler) UpdateCluster(available map[string]instance_metrics.WorkerStats, unavailable map[string]instance_metrics.WorkerStats) error {
	t.syncMutex.Lock()
	defer t.syncMutex.Unlock()
	if reflect.DeepEqual(t.stats, available) {
		logger.Log().Tracef("random algo receive update stats, details: %s", statsToStr(available))
	}
	t.stats = available
	t.StatsUpdateCount.Add(1)

	instancesCopy := make([]string, 0, len(available))
	newNodeIds := make(map[string]struct{}, len(available))
	for id := range available {
		newNodeIds[id] = struct{}{}
		instancesCopy = append(instancesCopy, id)
	}
	t.instances = instancesCopy
	return nil
}

func (t *TestScheduler) GetStats() map[string]instance_metrics.WorkerStats {
	t.syncMutex.RLock()
	defer t.syncMutex.RUnlock()
	res := make(map[string]instance_metrics.WorkerStats)
	for id, instanceStats := range t.stats {
		res[id] = instanceStats
	}
	return res
}

func statsToStr(workers map[string]instance_metrics.WorkerStats) string {
	builder := strings.Builder{}
	builder.WriteString(fmt.Sprintf("stats: instance_num = %v \n", len(workers)))
	for id, instanceStats := range workers {
		builder.WriteString(fmt.Sprintf("\t [id=%v, role=%v], stats: [", id, instanceStats.GetRole()))
		for _, key := range *instanceStats.GetNumericStatKeys() {
			builder.WriteString(fmt.Sprintf(" <%v, %v>, ", key, instanceStats.GetNumericStat(key)))
		}
		builder.WriteString(" ] \n")
	}
	return builder.String()
}

func (t *TestScheduler) UpdateConfig(config map[string]string) error {
	t.mutex.Lock()
	defer t.mutex.Unlock()
	t.Config = config
	ids := make([]string, 0, len(config))
	for id := range config {
		ids = append(ids, id)
	}
	t.Ids = ids
	return nil
}

func (t *TestScheduler) GetUpdateInterval() int64 {
	return 100
}

func (t *TestScheduler) DispatchRequest(request utils.GenerateRequest,
	params map[string]string, insBlackMap map[string]interface{}) (string, bool, error) {
	t.mutex.RLock()
	defer t.mutex.RUnlock()
	selectNum := len(t.Ids)
	if len(t.Ids) == len(insBlackMap) {
		return "", true, nil
	}
	start := rand.Intn(len(t.Ids))
	skipped := false
	for i := 0; i < selectNum; i++ {
		_, exist := insBlackMap[t.Ids[(start+i)%len(t.Ids)]]
		if exist {
			skipped = true
			continue
		} else {
			return t.Ids[(start+i)%len(t.Ids)], skipped, nil
		}
	}
	return "", skipped, nil
}
