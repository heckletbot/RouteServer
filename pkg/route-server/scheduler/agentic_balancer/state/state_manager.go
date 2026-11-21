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

package state

import "sync"

// StateManager is the top-level aggregate providing access to all state tables.
type StateManager struct {
	// DecisionMu serializes every decision that reads worker state and reserves inflight
	// load (direct dispatch, controller dequeue). Lock order: DecisionMu → pq.mu; never take
	// it while holding a table lock or from an OnRequestEvent callback.
	DecisionMu sync.Mutex

	Sessions *SessionTable
	Workers  *WorkerStateTable
	Inflight *InflightTable
	TTL      *TTLManager
}

// NewStateManager creates a StateManager with all sub-tables initialized; zero ttlCfg
// means defaults.
func NewStateManager(ttlCfg TTLConfig) *StateManager {
	return &StateManager{
		Sessions: NewSessionTable(),
		Workers:  NewWorkerStateTable(),
		Inflight: NewInflightTable(),
		TTL:      NewTTLManager(ttlCfg),
	}
}

var (
	globalSM   *StateManager
	globalSMMu sync.RWMutex
)

// SetGlobalStateManager installs the process-wide StateManager.
func SetGlobalStateManager(sm *StateManager) {
	globalSMMu.Lock()
	globalSM = sm
	globalSMMu.Unlock()
}

// GetGlobalStateManager returns the process-wide StateManager, or nil if unset.
func GetGlobalStateManager() *StateManager {
	globalSMMu.RLock()
	defer globalSMMu.RUnlock()
	return globalSM
}
