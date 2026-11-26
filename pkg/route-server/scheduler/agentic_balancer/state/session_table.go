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

import (
	"sync"
	"time"
)

// SessionEntry holds the per-session scheduling state: phase, priority, acting timer and the worker history used for affinity.
type SessionEntry struct {
	SessionId string
	TurnId    int
	Phase     SessionPhase

	AssignedWorker   []string
	maxWorkerHistory int

	ActingSince time.Time
	Priority    int32
	Terminated  bool

	entryMu sync.RWMutex
}

// GetAssignedWorker returns the most recent worker assignment, empty when unknown.
func (e *SessionEntry) GetAssignedWorker() string {
	e.entryMu.RLock()
	defer e.entryMu.RUnlock()
	if len(e.AssignedWorker) > 0 {
		return e.AssignedWorker[0]
	}
	return ""
}

// SetAssignedWorker moves w to the front of the history, de-duplicated and capped at maxWorkerHistory.
func (e *SessionEntry) SetAssignedWorker(w string) {
	e.entryMu.Lock()
	defer e.entryMu.Unlock()
	if w == "" {
		return
	}
	for i, id := range e.AssignedWorker {
		if id == w {
			e.AssignedWorker = append(e.AssignedWorker[:i], e.AssignedWorker[i+1:]...)
			break
		}
	}
	e.AssignedWorker = append([]string{w}, e.AssignedWorker...)
	maxLen := e.maxWorkerHistory
	if maxLen <= 0 {
		maxLen = 4
	}
	if len(e.AssignedWorker) > maxLen {
		e.AssignedWorker = e.AssignedWorker[:maxLen]
	}
}

// ClearAssignedWorker drops the whole history, e.g. when the bound worker disappears.
func (e *SessionEntry) ClearAssignedWorker() {
	e.entryMu.Lock()
	e.AssignedWorker = e.AssignedWorker[:0]
	e.entryMu.Unlock()
}

// WorkerHistoryIndex returns the position of workerId in the history, or -1 when absent.
func (e *SessionEntry) WorkerHistoryIndex(workerId string) int {
	e.entryMu.RLock()
	defer e.entryMu.RUnlock()
	for i, id := range e.AssignedWorker {
		if id == workerId {
			return i
		}
	}
	return -1
}

// GetPhase returns the current session phase.
func (e *SessionEntry) GetPhase() SessionPhase {
	e.entryMu.RLock()
	defer e.entryMu.RUnlock()
	return e.Phase
}

// GetPriority returns the scheduler priority of the session.
func (e *SessionEntry) GetPriority() int32 {
	e.entryMu.RLock()
	defer e.entryMu.RUnlock()
	return e.Priority
}

// SetPriority records the scheduler priority of the session.
func (e *SessionEntry) SetPriority(p int32) {
	e.entryMu.Lock()
	e.Priority = p
	e.entryMu.Unlock()
}

// GetActingSince returns when the current ACTING stretch began.
func (e *SessionEntry) GetActingSince() time.Time {
	e.entryMu.RLock()
	defer e.entryMu.RUnlock()
	return e.ActingSince
}

// SetActingSince overrides the acting start time, mainly for tests.
func (e *SessionEntry) SetActingSince(t time.Time) {
	e.entryMu.Lock()
	e.ActingSince = t
	e.entryMu.Unlock()
}

// IsTerminated reports whether the session was marked terminated.
func (e *SessionEntry) IsTerminated() bool {
	e.entryMu.RLock()
	defer e.entryMu.RUnlock()
	return e.Terminated
}

// SetTerminated marks the session as finished.
func (e *SessionEntry) SetTerminated() {
	e.entryMu.Lock()
	e.Terminated = true
	e.entryMu.Unlock()
}

// TransitionToActing switches to ACTING and restarts the acting timer used for affinity decay.
func (e *SessionEntry) TransitionToActing() {
	e.entryMu.Lock()
	e.Phase = PhaseActing
	e.ActingSince = time.Now()
	e.entryMu.Unlock()
}

// TransitionToReasoning switches to REASONING and keeps ActingSince, so decay still measures the last acting stretch.
func (e *SessionEntry) TransitionToReasoning() {
	e.entryMu.Lock()
	e.Phase = PhaseReasoning
	e.entryMu.Unlock()
}

// NextTurn increments the turn counter for the next request of the session.
func (e *SessionEntry) NextTurn() {
	e.entryMu.Lock()
	e.TurnId++
	e.entryMu.Unlock()
}

// SessionTable stores session_id to SessionEntry mappings and is safe for concurrent use.
type SessionTable struct {
	mu               sync.RWMutex
	sessions         map[string]*SessionEntry
	maxWorkerHistory int
}

// NewSessionTable creates an empty table; maxWorkerHistory stays 0 until set.
func NewSessionTable() *SessionTable {
	return &SessionTable{
		sessions: make(map[string]*SessionEntry),
	}
}

// SetMaxWorkerHistory sets the affinity history depth for entries created afterwards.
func (t *SessionTable) SetMaxWorkerHistory(n int) {
	t.mu.Lock()
	t.maxWorkerHistory = n
	t.mu.Unlock()
}

// GetOrCreate returns the session, creating it in ACTING phase with turn 1 if absent.
func (t *SessionTable) GetOrCreate(sessionId string) *SessionEntry {
	t.mu.Lock()
	defer t.mu.Unlock()
	if entry, ok := t.sessions[sessionId]; ok {
		return entry
	}
	entry := &SessionEntry{
		SessionId:        sessionId,
		TurnId:           1,
		Phase:            PhaseActing,
		AssignedWorker:   make([]string, 0, 4),
		maxWorkerHistory: t.maxWorkerHistory,
	}
	t.sessions[sessionId] = entry
	return entry
}

// Get returns the session, or nil when unknown.
func (t *SessionTable) Get(sessionId string) *SessionEntry {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.sessions[sessionId]
}

// Delete removes the session and its history.
func (t *SessionTable) Delete(sessionId string) {
	t.mu.Lock()
	delete(t.sessions, sessionId)
	t.mu.Unlock()
}

// ListByWorker returns sessions whose latest assignment is workerId.
func (t *SessionTable) ListByWorker(workerId string) []*SessionEntry {
	t.mu.RLock()
	defer t.mu.RUnlock()
	result := make([]*SessionEntry, 0)
	for _, e := range t.sessions {
		if e.GetAssignedWorker() == workerId {
			result = append(result, e)
		}
	}
	return result
}

// ListActingByWorker returns ACTING sessions whose latest assignment is workerId.
func (t *SessionTable) ListActingByWorker(workerId string) []*SessionEntry {
	t.mu.RLock()
	defer t.mu.RUnlock()
	result := make([]*SessionEntry, 0)
	for _, e := range t.sessions {
		if e.GetPhase() == PhaseActing && e.GetAssignedWorker() == workerId {
			result = append(result, e)
		}
	}
	return result
}

// ListActive returns the sessions that are not terminated.
func (t *SessionTable) ListActive() []*SessionEntry {
	t.mu.RLock()
	defer t.mu.RUnlock()
	result := make([]*SessionEntry, 0, len(t.sessions))
	for _, e := range t.sessions {
		if !e.IsTerminated() {
			result = append(result, e)
		}
	}
	return result
}

// Count returns the number of live sessions.
func (t *SessionTable) Count() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return len(t.sessions)
}
