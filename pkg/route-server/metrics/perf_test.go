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

package metrics

import (
	commoncfg "route-server/pkg/common/config"
	"route-server/pkg/common/utils/list"
	"sync"
	"testing"
	"time"
)

func TestInitPerfMetrics_Enable(t *testing.T) {
	cfg := &commoncfg.MetricsCfg{
		Enable:   true,
		Duration: 5,
	}
	InitPerfMetrics(cfg)

	if perfMetrics == nil {
		t.Error("perfMetrics should not be nil when enabled")
	}
	if !perfMetrics.enable {
		t.Error("enable should be true")
	}
	if len(perfMetrics.realTimePerfMetrics) != 0 {
		t.Error("realTimePerfMetrics should be empty")
	}
	if len(perfMetrics.pastPerfMetrics) != 0 {
		t.Error("pastPerfMetrics should be empty")
	}
	if len(perfMetrics.prometheusPerfMetrics) != 0 {
		t.Error("prometheusPerfMetrics should be empty")
	}
	if perfMetrics.lock == nil {
		t.Error("lock should not be nil")
	}
	if perfMetrics.period != 5 {
		t.Errorf("period should be 5, got %d", perfMetrics.period)
	}
	if perfMetrics.time != 0 {
		t.Error("time should be 0")
	}
	if perfMetrics.done == nil {
		t.Error("done channel should not be nil")
	}
}

func TestInitPerfMetrics_Period(t *testing.T) {
	// Test non-zero Duration
	cfg := &commoncfg.MetricsCfg{
		Enable:   true,
		Duration: 10,
	}
	InitPerfMetrics(cfg)
	if perfMetrics.period != 10 {
		t.Errorf("period should be 10, got %d", perfMetrics.period)
	}

	// Test when Duration is 0
	cfg = &commoncfg.MetricsCfg{
		Enable:   true,
		Duration: 0,
	}
	InitPerfMetrics(cfg)
	if perfMetrics.period != 1 {
		t.Errorf("period should be 1 when Duration is 0, got %d", perfMetrics.period)
	}
}

func TestInitPerfMetrics_Lock(t *testing.T) {
	cfg := &commoncfg.MetricsCfg{
		Enable:   true,
		Duration: 5,
	}
	InitPerfMetrics(cfg)

	if perfMetrics.lock == nil {
		t.Error("lock should not be nil")
	}

	// Test basic lock operations
	perfMetrics.lock.Lock()
	perfMetrics.lock.Unlock()

	// Test lock protection under multiple goroutines
	var wg sync.WaitGroup
	const numGoroutines = 10
	wg.Add(numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func() {
			defer wg.Done()
			perfMetrics.lock.Lock()
			defer perfMetrics.lock.Unlock()
			// Simulate read/write operations on perfMetrics
			key := "test_metric"
			perfMetrics.realTimePerfMetrics[key] = list.NewStatisticsList()
			delete(perfMetrics.realTimePerfMetrics, key)
		}()
	}

	wg.Wait()
}

func TestInitPerfMetrics_DoneChannel(t *testing.T) {
	cfg := &commoncfg.MetricsCfg{
		Enable:   true,
		Duration: 5,
	}
	InitPerfMetrics(cfg)

	if perfMetrics.done == nil {
		t.Error("done channel should not be nil")
	}

	// Test channel closure
	StopMetric()
	select {
	case <-perfMetrics.done:
		// Channel closed, expected behavior
	default:
		t.Error("done channel should be closed after StopMetric")
	}
}

func TestInitPerfMetrics_ZeroDuration(t *testing.T) {
	cfg := &commoncfg.MetricsCfg{
		Enable:   true,
		Duration: 0,
	}
	InitPerfMetrics(cfg)

	if perfMetrics.period != 1 {
		t.Errorf("period should be 1 when Duration is 0, got %d", perfMetrics.period)
	}
}

func TestRegisterMetric(t *testing.T) {
	cfg := &commoncfg.MetricsCfg{
		Enable:   true,
		Duration: 5,
	}
	InitPerfMetrics(cfg)

	RegisterMetric("module1", "metric1")

	key := "module1_metric1"
	if perfMetrics.realTimePerfMetrics[key] == nil {
		t.Error("metric should be registered in realTimePerfMetrics")
	}
	if perfMetrics.prometheusPerfMetrics[key] == nil {
		t.Error("metric should be registered in prometheusPerfMetrics")
	}
}

func TestStartMetric_Enable(t *testing.T) {
	cfg := &commoncfg.MetricsCfg{
		Enable:   true,
		Duration: 1,
	}
	InitPerfMetrics(cfg)

	err := StartMetric()
	if err != nil {
		return
	}

	if !perfMetrics.timerStarted {
		t.Error("timer should be started")
	}

	StopMetric()

	// Wait for timer to run
	time.Sleep(time.Second)

	if perfMetrics.timerStarted {
		t.Error("timer should be stopped")
	}
}

func TestStopMetric_Enable(t *testing.T) {
	cfg := &commoncfg.MetricsCfg{
		Enable:   true,
		Duration: 5,
	}
	InitPerfMetrics(cfg)
	err := StartMetric()
	if err != nil {
		return
	}

	if !perfMetrics.timerStarted {
		t.Error("timer should be started")
	}

	StopMetric()

	// Wait for one period
	time.Sleep(time.Second)

	if perfMetrics.timerStarted {
		t.Error("timer should be stopped")
	}

	// Check if done channel is closed
	select {
	case <-perfMetrics.done:
		// Channel closed, expected behavior
	default:
		t.Error("done channel should be closed after StopMetric")
	}
}
