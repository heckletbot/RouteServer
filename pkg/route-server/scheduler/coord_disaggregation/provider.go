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
	instance_metrics "route-server/pkg/common/instance-metrics"
	"route-server/pkg/route-server/scheduler/agentic_balancer/state"
)

// StatsReader is the slice of the instance manager that coord routing needs:
// the latest worker stats of every role.
type StatsReader interface {
	GetAllInstanceStats() map[string]map[string]instance_metrics.WorkerStats
}

// WorkerSnapshotProvider supplies the worker candidates of one decision.
type WorkerSnapshotProvider interface {
	Snapshots() []*state.WorkerState
}

// InstanceStatsProvider turns the instance manager's stats into worker
// snapshots.
//
// It keeps its own WorkerStateTable instead of reading the global StateManager:
// the global table is only refreshed when the agentic-balancer scheduler is the
// configured scheduler, so relying on it would silently disable coord routing
// under any other scheduler.
type InstanceStatsProvider struct {
	reader StatsReader
	table  *state.WorkerStateTable
}

// NewInstanceStatsProvider returns a provider reading from the given stats source.
func NewInstanceStatsProvider(reader StatsReader) *InstanceStatsProvider {
	return &InstanceStatsProvider{
		reader: reader,
		table:  state.NewWorkerStateTable(),
	}
}

// Snapshots refreshes the private worker table from the current instance stats
// and returns the available workers. Each call returns copies, so a later
// refresh cannot mutate a snapshot being used to decide.
func (p *InstanceStatsProvider) Snapshots() []*state.WorkerState {
	available := make(map[string]instance_metrics.WorkerStats)
	unavailable := make(map[string]instance_metrics.WorkerStats)
	for _, roleStats := range p.reader.GetAllInstanceStats() {
		for id, stat := range roleStats {
			if stat.GetStatus() == instance_metrics.Available {
				available[id] = stat
			} else {
				unavailable[id] = stat
			}
		}
	}

	p.table.UpdateFromStats(available, unavailable)
	return p.table.ListAvailable()
}
