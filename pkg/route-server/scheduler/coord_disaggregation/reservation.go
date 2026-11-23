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

import "sync"

// Reservation is one admitted in-flight slot on a worker. The token identifies
// the slot, so releasing twice is a no-op and cannot free a slot owned by
// another request.
type Reservation struct {
	WorkerID string
	Token    int64
}

// Manager tracks admitted in-flight slots per worker.
//
// Coord routing sends a burst of requests to the single idle worker found in a
// snapshot; without an admission check every one of them would pick the same
// worker. The cap is the same field used by the idle judgment, so selection and
// admission cannot disagree.
type Manager struct {
	mu     sync.Mutex
	slots  map[string]map[int64]struct{}
	nextID int64
}

// NewManager returns an empty reservation manager.
func NewManager() *Manager {
	return &Manager{slots: make(map[string]map[int64]struct{})}
}

// TryAcquire admits one slot on workerID when fewer than maxInflight slots are
// held. It returns the reservation to release later, and false when the worker
// is already full (the caller then falls back to PD).
func (m *Manager) TryAcquire(workerID string, maxInflight int) (Reservation, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	tokens, ok := m.slots[workerID]
	if !ok {
		tokens = make(map[int64]struct{})
		m.slots[workerID] = tokens
	}
	if len(tokens) >= maxInflight {
		return Reservation{}, false
	}

	m.nextID++
	tokens[m.nextID] = struct{}{}
	return Reservation{WorkerID: workerID, Token: m.nextID}, true
}

// Release returns the slot held by r. Releasing an already released or unknown
// reservation is a no-op, which keeps every release path safe to run.
func (m *Manager) Release(r Reservation) {
	m.mu.Lock()
	defer m.mu.Unlock()

	tokens := m.slots[r.WorkerID]
	delete(tokens, r.Token)
	if len(tokens) == 0 {
		delete(m.slots, r.WorkerID)
	}
}

// InFlight reports how many slots are currently held on workerID.
func (m *Manager) InFlight(workerID string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.slots[workerID])
}

// defaultManager is the process-wide reservation table. Reservations live as
// long as the request they were granted to, which spans pipeline phases and
// therefore cannot belong to a single phase instance.
var defaultManager = NewManager()

// DefaultManager returns the process-wide reservation table.
func DefaultManager() *Manager {
	return defaultManager
}
