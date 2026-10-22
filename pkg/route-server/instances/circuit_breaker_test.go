/*
 * Copyright 2026 The vLLM-Cloud Authors.
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

	"github.com/stretchr/testify/assert"
	"infer-dp-scheduler/pkg/common/config"
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

	// Reset should not change state when StateClosed
	cb.Reset()
	assert.Equal(t, StateClosed, cb.state)

	// Reset should restore to StateClosed when StateOpen
	cb.state = StateOpen
	cb.Reset()
	assert.Equal(t, StateClosed, cb.state)
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
