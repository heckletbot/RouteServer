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

package metrics

import (
	"fmt"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	commoncfg "infer-dp-scheduler/pkg/common/config"
	"infer-dp-scheduler/pkg/common/logger"
	"infer-dp-scheduler/pkg/common/utils/list"
)

var perfMetrics *PerfMetrics

const (
	AverageLatency = "average"
	MaxLatency     = "max"
	MinLatency     = "min"
	P90Latency     = "p90"
	P95Latency     = "p95"
	P99Latency     = "p99"
)

var perfLabels = []string{AverageLatency, MaxLatency, MinLatency, P90Latency, P95Latency, P99Latency}

type PerfMetrics struct {
	enable                bool
	realTimePerfMetrics   map[string]*list.StatisticsList
	pastPerfMetrics       map[string]*list.StatisticsList
	prometheusPerfMetrics map[string]*prometheus.GaugeVec
	lock                  *sync.RWMutex

	period       uint32
	time         uint32
	timerStarted bool

	done chan struct{}
}

func InitPerfMetrics(cfg *commoncfg.MetricsCfg) {
	period := 1
	if cfg.Duration > 0 {
		period = cfg.Duration
	}
	perfMetrics = &PerfMetrics{
		enable:                cfg.Enable,
		realTimePerfMetrics:   make(map[string]*list.StatisticsList),
		pastPerfMetrics:       make(map[string]*list.StatisticsList),
		prometheusPerfMetrics: make(map[string]*prometheus.GaugeVec),
		lock:                  &sync.RWMutex{},
		period:                uint32(period),
		time:                  0,
		done:                  make(chan struct{}),
	}
}

func metricTimer() {
	var lastTick time.Time
	timer := time.NewTicker(time.Second)
	defer func() {
		timer.Stop()
		perfMetrics.timerStarted = false
	}()

	for {
		select {
		case t := <-timer.C:
			if !lastTick.IsZero() {
				elapse := t.Sub(lastTick)
				if elapse > time.Second*2 {
					logger.Log().Warn("metrics timer tick stall.")
				}
			}
			perfMetrics.calculate()
			lastTick = t
		case <-perfMetrics.done:
			return
		}
	}
}

func StartMetric() error {
	if !perfMetrics.enable {
		return nil
	}

	if !perfMetrics.timerStarted {
		perfMetrics.timerStarted = true
		go metricTimer()
	}

	return nil
}

func RegisterMetric(module, metric string) {
	if !perfMetrics.enable {
		return
	}
	perfMetrics.registerMetric(module, metric)
}

func StopMetric() {
	if perfMetrics == nil || !perfMetrics.enable {
		return
	}
	close(perfMetrics.done)
}

func MetricRecord(module, metric string, latencyMs int) {
	if !perfMetrics.enable {
		return
	}
	perfMetrics.record(module, metric, latencyMs)
}

func (m *PerfMetrics) record(module, metric string, latencyMs int) {
	key := fmt.Sprintf("%s_%s", module, metric)
	var recordList *list.StatisticsList
	var ok bool
	m.lock.RLock()
	recordList, ok = m.realTimePerfMetrics[key]
	m.lock.RUnlock()
	if !ok {
		return
	}
	recordList.Append(latencyMs)
}

func (m *PerfMetrics) registerMetric(module, metric string) {
	m.lock.Lock()
	defer m.lock.Unlock()

	key := fmt.Sprintf("%s_%s", module, metric)
	_, ok := m.realTimePerfMetrics[key]
	if !ok {
		m.realTimePerfMetrics[key] = list.NewStatisticsList()
		m.pastPerfMetrics[key] = nil
	}
	gauge := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: key,
	}, perfLabels)
	prometheus.MustRegister(gauge)
	m.prometheusPerfMetrics[key] = gauge
}

func (m *PerfMetrics) calculate() {
	if len(m.realTimePerfMetrics) == 0 {
		return
	}

	m.lock.Lock()
	for k, v := range m.realTimePerfMetrics {
		m.pastPerfMetrics[k] = v
		m.realTimePerfMetrics[k] = list.NewStatisticsList()
	}
	m.lock.Unlock()

	m.time++
	if m.time < m.period {
		return
	}
	isPrint := false
	perfLog := "route server Perf Metrics: "
	for k, v := range m.pastPerfMetrics {
		info := v.Statistics()
		if info.Count == 0 {
			perfLog += fmt.Sprintf("%s: no record data\n", k)
			continue
		}
		isPrint = true
		perfLog += fmt.Sprintf("%s[count:%d, average:%d, min:%d, max:%d, p90:%d, p95:%d, p99:%d];",
			k, info.Count, info.Average, info.Min, info.Max, info.P90, info.P95, info.P99)
	}
	if isPrint {
		logger.Log().Info(perfLog)
	}
	m.time = 0
}
