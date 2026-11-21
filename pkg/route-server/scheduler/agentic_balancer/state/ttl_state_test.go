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
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestTTLManagerTouchAndGetExpired(t *testing.T) {
	cfg := TTLConfig{
		SessionTTL: 50 * time.Millisecond,
	}

	base := time.Now()
	mgr := NewTTLManager(cfg).WithNowFunc(func() time.Time {
		return base
	})

	mgr.TouchSession("sess-1")

	mgr.nowFunc = func() time.Time { return base.Add(30 * time.Millisecond) }
	mgr.TouchSession("sess-2")

	mgr.nowFunc = func() time.Time { return base.Add(60 * time.Millisecond) }

	expired := mgr.CleanupExpiredAtomic()
	if len(expired) != 1 {
		t.Fatalf("expected 1 expired session (sess-1), got %d", len(expired))
	}

	mgr.nowFunc = func() time.Time { return base.Add(120 * time.Millisecond) }
	expired = mgr.CleanupExpiredAtomic()
	if len(expired) != 1 {
		t.Errorf("expected 1 remaining expired session (sess-2), got %d", len(expired))
	}
}

func TestTTLManagerNoExpired(t *testing.T) {
	cfg := TTLConfig{
		SessionTTL: 1 * time.Hour,
	}
	mgr := NewTTLManager(cfg)

	mgr.TouchSession("sess-1")
	mgr.TouchSession("sess-2")

	expired := mgr.CleanupExpiredAtomic()
	if len(expired) != 0 {
		t.Errorf("expected 0 expired sessions, got %d", len(expired))
	}
}

func TestTTLManagerDefaultConfig(t *testing.T) {
	mgr := NewTTLManager(TTLConfig{})
	if mgr.cfg.SessionTTL != 30*time.Minute {
		t.Errorf("expected default SessionTTL=30m, got %v", mgr.cfg.SessionTTL)
	}
}

func TestTTLManagerSetSessionTTL(t *testing.T) {
	base := time.Now()
	mgr := NewTTLManager(TTLConfig{SessionTTL: time.Hour}).WithNowFunc(func() time.Time { return base })
	mgr.TouchSession("sess-1")

	mgr.nowFunc = func() time.Time { return base.Add(20 * time.Minute) }
	if expired := mgr.CleanupExpiredAtomic(); len(expired) != 0 {
		t.Fatalf("an hour-long window should keep the session, got %v", expired)
	}

	mgr.SetSessionTTL(10 * time.Minute)
	expired := mgr.CleanupExpiredAtomic()
	if len(expired) != 1 || expired[0] != "sess-1" {
		t.Fatalf("the shortened window should expire sess-1, got %v", expired)
	}
}

func TestTTLManagerConcurrency(t *testing.T) {
	cfg := TTLConfig{
		SessionTTL: 1 * time.Hour,
	}
	mgr := NewTTLManager(cfg)

	const goroutines = 8
	const iterations = 500

	var wg sync.WaitGroup
	wg.Add(goroutines)

	for g := 0; g < goroutines; g++ {
		go func(gid int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				sessionId := fmt.Sprintf("sess-%d-%d", gid, i%10)
				mgr.TouchSession(sessionId)
				_ = mgr.CleanupExpiredAtomic()
			}
		}(g)
	}
	wg.Wait()
}
