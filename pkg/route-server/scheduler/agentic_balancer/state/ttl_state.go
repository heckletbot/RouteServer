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

// TTLConfig controls session expiry. Cleanup cadence is owned by the
// controller tick, not a separate interval here.
type TTLConfig struct {
	SessionTTL time.Duration // session expiry after last access (default 30min)
}

// TTLManager tracks last-access timestamps and provides expiry cleanup.
// Designed to be called periodically by a controller goroutine.
type TTLManager struct {
	cfg      TTLConfig
	mu       sync.RWMutex
	sessions map[string]time.Time // session_id → last_access_time
	nowFunc  func() time.Time
}

// defaultSessionTTL is applied when the configured window is not positive.
const defaultSessionTTL = 30 * time.Minute

func NewTTLManager(cfg TTLConfig) *TTLManager {
	if cfg.SessionTTL <= 0 {
		cfg.SessionTTL = defaultSessionTTL
	}
	return &TTLManager{
		cfg:      cfg,
		sessions: make(map[string]time.Time),
		nowFunc:  time.Now,
	}
}

// SetSessionTTL hot-applies the expiry window; the next cleanup compares against it, so a
// shorter window expires idle sessions that a longer one kept alive.
func (m *TTLManager) SetSessionTTL(ttl time.Duration) {
	if ttl <= 0 {
		ttl = defaultSessionTTL
	}
	m.mu.Lock()
	m.cfg.SessionTTL = ttl
	m.mu.Unlock()
}

// WithNowFunc injects a custom time source for testing. Returns the manager
// for chaining.
func (m *TTLManager) WithNowFunc(f func() time.Time) *TTLManager {
	m.nowFunc = f
	return m
}

// TouchSession updates the last-access time for a session.
func (m *TTLManager) TouchSession(sessionId string) {
	m.mu.Lock()
	m.sessions[sessionId] = m.nowFunc()
	m.mu.Unlock()
}

// CleanupExpiredAtomic scans and deletes expired sessions under a single Lock.
// Returns the list of expired session IDs.
func (m *TTLManager) CleanupExpiredAtomic() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	cutoff := m.nowFunc().Add(-m.cfg.SessionTTL)
	expired := make([]string, 0)
	for id, lastAccess := range m.sessions {
		if lastAccess.Before(cutoff) {
			expired = append(expired, id)
			delete(m.sessions, id)
		}
	}
	return expired
}
