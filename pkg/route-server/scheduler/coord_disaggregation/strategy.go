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
	"fmt"
	"math"
	"sort"
	"time"

	"route-server/pkg/common/constant"
	"route-server/pkg/route-server/scheduler/agentic_balancer/state"
)

const (
	// StrategyImbalance routes to the idle worker only when exactly one role is
	// fully idle and the other role is entirely busy.
	StrategyImbalance = "imbalance"
)

// Strategy decides where a request should be served locally. It is a pure
// decision over a snapshot: it neither mutates state nor acquires reservations.
type Strategy interface {
	Name() string
	// Decide returns the chosen worker and its role. An empty worker id means
	// the request must walk the regular PD chain. windowMiss is set only when
	// exactly one role is idle and that role's token window rejects the prompt.
	Decide(workers []*state.WorkerState, promptTokens int32, reserved *Manager) (workerID string, role string, windowMiss bool)
}

// StrategyFactory builds a strategy for the given decision inputs.
type StrategyFactory func(params Params) Strategy

var strategyFactories = map[string]StrategyFactory{
	StrategyImbalance: func(params Params) Strategy {
		return &ImbalanceStrategy{params: params}
	},
}

// NewStrategy builds the strategy registered under name.
func NewStrategy(name string, params Params) (Strategy, error) {
	factory, ok := strategyFactories[name]
	if !ok {
		return nil, fmt.Errorf("unknown coord routing strategy [%s], known: %v", name, KnownStrategies())
	}
	return factory(params), nil
}

// KnownStrategies lists the registered strategy names, sorted.
func KnownStrategies() []string {
	names := make([]string, 0, len(strategyFactories))
	for name := range strategyFactories {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// ImbalanceStrategy implements the imbalance policy:
//
//	one P idle and every D busy   -> serve on that P worker
//	one D idle and every P busy   -> serve on that D worker
//	both roles idle or both busy  -> PD
type ImbalanceStrategy struct {
	params Params
}

func (s *ImbalanceStrategy) Name() string {
	return StrategyImbalance
}

func (s *ImbalanceStrategy) Decide(workers []*state.WorkerState, promptTokens int32, reserved *Manager) (string, string, bool) {
	return s.DecidePerWorker(workers, func(string) int32 { return promptTokens }, reserved)
}

// DecidePerWorker applies each worker's own compute length to its role window.
// A role that is idle but has no worker inside the window is a window miss.
func (s *ImbalanceStrategy) DecidePerWorker(workers []*state.WorkerState, tokens func(workerID string) int32, reserved *Manager) (string, string, bool) {
	var idlePrefill, idleDecode []*state.WorkerState
	prefillSeen, decodeSeen := 0, 0
	prefillStale, decodeStale := false, false
	for _, worker := range workers {
		switch worker.Role {
		case constant.Prefill:
			prefillSeen++
			if !s.fresh(worker) {
				prefillStale = true
				continue
			}
			if s.isIdle(worker, reserved) {
				idlePrefill = append(idlePrefill, worker)
			}
		case constant.Decode:
			decodeSeen++
			if !s.fresh(worker) {
				decodeStale = true
				continue
			}
			if s.isIdle(worker, reserved) {
				idleDecode = append(idleDecode, worker)
			}
		}
	}

	// A role is entirely busy only when it has workers, every snapshot is fresh,
	// and none of those workers are idle. A missing or stale role is unknown.
	if len(idlePrefill) > 0 && decodeSeen > 0 && !decodeStale && len(idleDecode) == 0 {
		return s.pickInWindow(idlePrefill, constant.Prefill, tokens)
	}
	if len(idleDecode) > 0 && prefillSeen > 0 && !prefillStale && len(idlePrefill) == 0 {
		return s.pickInWindow(idleDecode, constant.Decode, tokens)
	}
	return "", "", false
}

func (s *ImbalanceStrategy) fresh(worker *state.WorkerState) bool {
	return time.Since(worker.UpdatedAt) <= s.params.StaleDuration
}

func (s *ImbalanceStrategy) pickInWindow(workers []*state.WorkerState, role string, tokens func(workerID string) int32) (string, string, bool) {
	fitting := make([]*state.WorkerState, 0, len(workers))
	for _, worker := range workers {
		if s.params.Accepts(role, tokens(worker.WorkerId)) {
			fitting = append(fitting, worker)
		}
	}
	if len(fitting) == 0 {
		return "", "", true
	}
	return pickLowestUtil(fitting), role, false
}

// isIdle reports whether the worker can take another local request: its
// snapshot is fresh, its load is under the idle thresholds, and it still has a
// reservation slot.
func (s *ImbalanceStrategy) isIdle(worker *state.WorkerState, reserved *Manager) bool {
	if time.Since(worker.UpdatedAt) > s.params.StaleDuration {
		return false
	}

	maxRunning := s.params.IdleMaxRunningP
	if worker.Role == constant.Decode {
		maxRunning = s.params.IdleMaxRunningD
	}

	busy := worker.Pending > int64(s.params.IdleMaxWaiting) ||
		worker.InFlight > int64(maxRunning) ||
		worker.KVCache.UsagePerc > s.params.IdleMaxKVUsage
	if busy {
		return false
	}

	// A closed window never takes a local reservation, so a zero inflight cap
	// must not make every worker of that role look busy. Idle for the imbalance
	// check then comes only from the metrics above.
	if s.params.InputTokenGateFor(worker.Role) <= 0 {
		return true
	}
	return reserved.InFlight(worker.WorkerId) < s.params.MaxInflightFor(worker.Role)
}

// workerUtil mirrors state.ComputeWorkerUtil, using the running request count
// reported by the worker itself rather than the session table, because coord
// routing only has the stats snapshot.
func workerUtil(worker *state.WorkerState) float64 {
	maxNumSeqs := float64(worker.MaxNumSeqs)
	if maxNumSeqs <= 0 {
		maxNumSeqs = 1
	}
	queueLoad := float64(worker.InFlight+worker.Pending) / (1.0 + maxNumSeqs)
	return math.Max(queueLoad, worker.KVCache.UsagePerc)
}

func pickLowestUtil(workers []*state.WorkerState) string {
	bestWorker := ""
	bestUtil := 1.0
	for _, worker := range workers {
		if util := workerUtil(worker); util < bestUtil {
			bestUtil = util
			bestWorker = worker.WorkerId
		}
	}
	return bestWorker
}
