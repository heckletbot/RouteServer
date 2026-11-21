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
	"testing"
	"time"
)

func TestStateManagerCreation(t *testing.T) {
	sm := NewStateManager(TTLConfig{})
	if sm.Sessions == nil || sm.Workers == nil ||
		sm.Inflight == nil || sm.TTL == nil {
		t.Fatal("expected all sub-tables to be initialized")
	}
}

func TestStateManagerDefaultTTL(t *testing.T) {
	sm := NewStateManager(TTLConfig{})
	if sm.TTL.cfg.SessionTTL != 30*time.Minute {
		t.Errorf("expected default SessionTTL=30m, got %v", sm.TTL.cfg.SessionTTL)
	}
}

func TestGlobalStateManagerConcurrency(t *testing.T) {
	const goroutines = 8
	const iterations = 500

	var wg sync.WaitGroup
	wg.Add(goroutines)

	for g := 0; g < goroutines; g++ {
		go func(gid int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				sm := NewStateManager(TTLConfig{})
				SetGlobalStateManager(sm)
				_ = GetGlobalStateManager()
			}
		}(g)
	}
	wg.Wait()
}
