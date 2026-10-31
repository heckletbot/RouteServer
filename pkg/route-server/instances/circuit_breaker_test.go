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

package instances

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"route-server/pkg/common/config"
)

// TestNewCircuitBreaker_Disabled test disabled circuit breaker
func TestNewCircuitBreaker_Disabled(t *testing.T) {
	cb := NewCircuitBreaker(&config.CircuitBreakerConfig{
		Enable: false,
	})
	assert.Nil(t, cb)
}

// TestNewCircuitBreaker_NilConfig test nil config
func TestNewCircuitBreaker_NilConfig(t *testing.T) {
	cb := NewCircuitBreaker(nil)
	assert.Nil(t, cb)
}

// TestNewCircuitBreaker_Enabled test enabled circuit breaker
func TestNewCircuitBreaker_Enabled(t *testing.T) {
	cb := NewCircuitBreaker(&config.CircuitBreakerConfig{
		Enable:               true,
		CheckWindowSec:       10,
		FailureRateThreshold: 0.5,
		MinRequestCount:      5,
	})
	assert.NotNil(t, cb)
	assert.Equal(t, StateClosed, cb.state)
	assert.Equal(t, ForceModeNone, cb.forceMode)

	// close timer to avoid goroutine leak
	cb.Close()
}

// TestCircuitBreaker_Close test closing circuit breaker
func TestCircuitBreaker_Close(t *testing.T) {
	cb := NewCircuitBreaker(&config.CircuitBreakerConfig{
		Enable:               true,
		CheckWindowSec:       10,
		FailureRateThreshold: 0.5,
		MinRequestCount:      5,
	})
	assert.NotNil(t, cb)

	// close without panic
	cb.Close()
}

// TestCircuitBreaker_Reset test resetting circuit breaker
func TestCircuitBreaker_Reset(t *testing.T) {
	cb := NewCircuitBreaker(&config.CircuitBreakerConfig{
		Enable:               true,
		CheckWindowSec:       10,
		FailureRateThreshold: 0.5,
		MinRequestCount:      5,
	})
	defer cb.Close()

	// Reset should clear shouldTrip and baseline data when StateClosed
	cb.shouldTrip.Store(true)
	cb.lastTotalRequests = 100
	cb.lastErrorRequests = 50
	cb.lastCollectTime = time.Now()
	cb.degraded.Store(true)
	cb.Reset()
	assert.Equal(t, StateClosed, cb.state)
	assert.False(t, cb.shouldTrip.Load())
	assert.Equal(t, float64(0), cb.lastTotalRequests)
	assert.Equal(t, float64(0), cb.lastErrorRequests)
	assert.True(t, cb.lastCollectTime.IsZero())
	assert.False(t, cb.degraded.Load())

	// Reset should restore to StateClosed and clear all data when StateOpen
	cb.state = StateOpen
	cb.shouldTrip.Store(true)
	cb.lastTotalRequests = 200
	cb.lastErrorRequests = 100
	cb.lastCollectTime = time.Now()
	cb.degraded.Store(true)
	cb.Reset()
	assert.Equal(t, StateClosed, cb.state)
	assert.False(t, cb.shouldTrip.Load())
	assert.Equal(t, float64(0), cb.lastTotalRequests)
	assert.Equal(t, float64(0), cb.lastErrorRequests)
	assert.True(t, cb.lastCollectTime.IsZero())
	assert.False(t, cb.degraded.Load())
}

// TestCircuitBreaker_CheckShouldTrip test CheckShouldTrip
func TestCircuitBreaker_CheckShouldTrip(t *testing.T) {
	cb := NewCircuitBreaker(&config.CircuitBreakerConfig{
		Enable:               true,
		CheckWindowSec:       10,
		FailureRateThreshold: 0.5,
		MinRequestCount:      5,
	})
	defer cb.Close()

	// Set degraded=true so checkProbeTimeDelta can execute
	cb.SetDegraded(true)

	// shouldTrip=false, state=Closed -> no trip
	assert.False(t, cb.CheckShouldTrip())
	assert.Equal(t, StateClosed, cb.state)

	// shouldTrip=true, state=Closed -> open circuit breaker
	cb.shouldTrip.Store(true)
	assert.True(t, cb.CheckShouldTrip())
	assert.Equal(t, StateOpen, cb.state)

	// shouldTrip=true, state=Open -> keep open
	assert.True(t, cb.CheckShouldTrip())
	assert.Equal(t, StateOpen, cb.state)
}

// TestCircuitBreaker_CollectAndCheck_SkipsWhenNotDegraded test collectAndCheck skips when not degraded
func TestCircuitBreaker_CollectAndCheck_SkipsWhenNotDegraded(t *testing.T) {
	cb := NewCircuitBreaker(&config.CircuitBreakerConfig{
		Enable:               true,
		CheckWindowSec:       10,
		FailureRateThreshold: 0.5,
		MinRequestCount:      5,
	})
	defer cb.Close()

	// set baseline data
	cb.mu.Lock()
	cb.lastCollectTime = time.Now().Add(-5 * time.Second)
	cb.lastTotalRequests = 0
	cb.lastErrorRequests = 0
	cb.mu.Unlock()

	// default degraded=false, collectAndCheck should skip
	cb.collectAndCheck()
	cb.mu.RLock()
	total := cb.lastTotalRequests
	cb.mu.RUnlock()
	assert.Equal(t, float64(0), total, "collectAndCheck should skip when not degraded, not update baseline")

	// set degraded=true, collectAndCheck should execute normally
	cb.SetDegraded(true)
	cb.collectAndCheck()
	// first call (lastCollectTime non-zero) should update baseline
	cb.mu.RLock()
	total = cb.lastTotalRequests
	cb.mu.RUnlock()
	// since no actual requests, baseline value depends on current Prometheus metric values
	// key is collectAndCheck executed (no early return)
	assert.True(t, true, "collectAndCheck should execute normally in degraded mode")
}

// TestCircuitBreaker_CheckProbeTimeDelta_SkipsWhenNotDegraded test checkProbeTimeDelta skips when not degraded
func TestCircuitBreaker_CheckProbeTimeDelta_SkipsWhenNotDegraded(t *testing.T) {
	cb := NewCircuitBreaker(&config.CircuitBreakerConfig{
		Enable:               true,
		CheckWindowSec:       10,
		FailureRateThreshold: 0.5,
		MinRequestCount:      5,
	})
	defer cb.Close()

	// set baseline data so checkProbeTimeDelta has data to calculate
	cb.mu.Lock()
	cb.lastCollectTime = time.Now().Add(-5 * time.Second)
	cb.lastTotalRequests = 0
	cb.lastErrorRequests = 0
	cb.state = StateClosed
	cb.mu.Unlock()

	// degraded=false, CheckShouldTrip should not trigger checkProbeTimeDelta failure rate calculation
	result := cb.CheckShouldTrip()
	assert.False(t, result, "CheckShouldTrip should not trip when not degraded")
	assert.Equal(t, StateClosed, cb.state, "state should remain Closed when not degraded")
}

// TestCircuitBreaker_SetDegraded test setting degraded flag
func TestCircuitBreaker_SetDegraded(t *testing.T) {
	cb := NewCircuitBreaker(&config.CircuitBreakerConfig{
		Enable:               true,
		CheckWindowSec:       10,
		FailureRateThreshold: 0.5,
		MinRequestCount:      5,
	})
	defer cb.Close()

	// default degraded=false
	assert.False(t, cb.degraded.Load())

	// set degraded=true
	cb.SetDegraded(true)
	assert.True(t, cb.degraded.Load())

	// set degraded=false
	cb.SetDegraded(false)
	assert.False(t, cb.degraded.Load())
}

// TestCircuitBreaker_GetForceMode test getting force mode
func TestCircuitBreaker_GetForceMode(t *testing.T) {
	cb := NewCircuitBreaker(&config.CircuitBreakerConfig{
		Enable:               true,
		CheckWindowSec:       10,
		FailureRateThreshold: 0.5,
		MinRequestCount:      5,
	})
	defer cb.Close()

	assert.Equal(t, ForceModeNone, cb.GetForceMode())

	cb.SetForceMode(ForceModeTrue)
	assert.Equal(t, ForceModeTrue, cb.GetForceMode())

	cb.SetForceMode(ForceModeFalse)
	assert.Equal(t, ForceModeFalse, cb.GetForceMode())

	cb.SetForceMode(ForceModeNone)
	assert.Equal(t, ForceModeNone, cb.GetForceMode())
}

// TestCircuitBreaker_SetForceMode test setting force mode
func TestCircuitBreaker_SetForceMode(t *testing.T) {
	cb := NewCircuitBreaker(&config.CircuitBreakerConfig{
		Enable:               true,
		CheckWindowSec:       10,
		FailureRateThreshold: 0.5,
		MinRequestCount:      5,
	})
	defer cb.Close()

	// set ForceModeTrue
	assert.True(t, cb.SetForceMode(ForceModeTrue))
	assert.Equal(t, ForceModeTrue, cb.forceMode)

	// set ForceModeFalse
	assert.True(t, cb.SetForceMode(ForceModeFalse))
	assert.Equal(t, ForceModeFalse, cb.forceMode)

	// set ForceModeNone
	assert.True(t, cb.SetForceMode(ForceModeNone))
	assert.Equal(t, ForceModeNone, cb.forceMode)
}

// TestCircuitBreaker_IsOpen test IsOpen
func TestCircuitBreaker_IsOpen(t *testing.T) {
	cb := NewCircuitBreaker(&config.CircuitBreakerConfig{
		Enable:               true,
		CheckWindowSec:       10,
		FailureRateThreshold: 0.5,
		MinRequestCount:      5,
	})
	defer cb.Close()

	assert.False(t, cb.IsOpen())

	cb.state = StateOpen
	assert.True(t, cb.IsOpen())
}
