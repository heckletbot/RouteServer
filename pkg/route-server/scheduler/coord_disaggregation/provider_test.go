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

package coord_disaggregation

import (
	"testing"
	"time"

	"route-server/pkg/common/constant"
	instance_metrics "route-server/pkg/common/instance-metrics"
	"route-server/pkg/route-server/scheduler/agentic_balancer/state"
)

type fakeStatsReader struct {
	stats map[string]map[string]instance_metrics.WorkerStats
}

func (f fakeStatsReader) GetAllInstanceStats() map[string]map[string]instance_metrics.WorkerStats {
	return f.stats
}

func buildStats(id, role string, status instance_metrics.InstanceStatus,
	maxNumSeqs string, running, waiting, kvUsage float64) *instance_metrics.ZeroCopyStats {
	staticInfo := map[string]string{constant.MaxNumSeqs: maxNumSeqs}
	metrics := map[string][]*instance_metrics.MetricInfo{
		constant.MetricNumRequestsRunning: {{Value: running}},
		constant.MetricNumRequestsWaiting: {{Value: waiting}},
		constant.MetricKVCacheUsagePerc:   {{Value: kvUsage}},
	}
	return &instance_metrics.ZeroCopyStats{
		Id:           id,
		Role:         role,
		Status:       status,
		StaticInfo:   staticInfo,
		NumericStats: &metrics,
		TimeStamp:    time.Now(),
	}
}

func TestInstanceStatsProviderReturnsHealthySnapshots(t *testing.T) {
	reader := fakeStatsReader{stats: map[string]map[string]instance_metrics.WorkerStats{
		constant.Prefill: {
			"p1": buildStats("p1", constant.Prefill, instance_metrics.Available, "32", 1, 0, 0.2),
			"p2": buildStats("p2", constant.Prefill, instance_metrics.Faulty, "32", 0, 0, 0.1),
		},
		constant.Decode: {
			"d1": buildStats("d1", constant.Decode, instance_metrics.Available, "16", 0, 2, 0.3),
		},
	}}

	snapshots := NewInstanceStatsProvider(reader).Snapshots()
	if len(snapshots) != 2 {
		t.Fatalf("Snapshots() returned %d workers, want 2 (the faulty worker must be excluded)", len(snapshots))
	}

	byID := make(map[string]*state.WorkerState, len(snapshots))
	for _, snapshot := range snapshots {
		byID[snapshot.WorkerId] = snapshot
	}

	prefill, ok := byID["p1"]
	if !ok {
		t.Fatalf("available prefill worker missing from snapshots: %v", byID)
	}
	if prefill.Role != constant.Prefill || prefill.InFlight != 1 || prefill.KVCache.UsagePerc != 0.2 {
		t.Fatalf("prefill snapshot not mapped: %+v", prefill)
	}
	if prefill.MaxNumSeqs != 32 {
		t.Fatalf("MaxNumSeqs = %d, want 32", prefill.MaxNumSeqs)
	}

	decode, ok := byID["d1"]
	if !ok {
		t.Fatalf("available decode worker missing from snapshots: %v", byID)
	}
	if decode.Role != constant.Decode || decode.Pending != 2 || decode.KVCache.UsagePerc != 0.3 {
		t.Fatalf("decode snapshot not mapped: %+v", decode)
	}

	if _, found := byID["p2"]; found {
		t.Fatalf("faulty worker must not be a routing candidate")
	}
}
