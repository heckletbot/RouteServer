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
	"sync"
	"sync/atomic"
	"time"

	"infer-dp-scheduler/pkg/common/config"
	"infer-dp-scheduler/pkg/common/logger"
	rsmetrics "infer-dp-scheduler/pkg/route-server/metrics"
)

type CircuitBreakerState int32

const (
	StateClosed CircuitBreakerState = 0 // normal state (accepting traffic)
	StateOpen   CircuitBreakerState = 1 // open state (rejecting traffic)
)

// ForceMode force mode
type ForceMode int32

const (
	ForceModeNone  ForceMode = 0 // not forced, determined by internal logic
	ForceModeTrue  ForceMode = 1 // force ready
	ForceModeFalse ForceMode = 2 // force not ready
)

// WorkerReadinessState unified Worker readiness state
type WorkerReadinessState int32

const (
	// Note: state 0 indicates circuit breaker not enabled, this is the default value for Prometheus metrics, no explicit setting needed
	ReadinessStateForceTrue  WorkerReadinessState = 1 // force true (force accepting traffic)
	ReadinessStateForceFalse WorkerReadinessState = 2 // force false (force rejecting traffic)
	ReadinessStateNormal     WorkerReadinessState = 3 // not broken (accept traffic based on health check cache info)
	ReadinessStateBroken     WorkerReadinessState = 4 // broken (reject traffic because failure rate exceeded threshold)
)

// CircuitBreaker circuit breaker module
// Used at level 3 (when etcd unreachable or cache expired) to determine whether to trip based on request success rate
type CircuitBreaker struct {
	cfg *config.CircuitBreakerConfig

	// state
	state     CircuitBreakerState
	forceMode ForceMode

	mu sync.RWMutex

	// timer driven: timer periodically checks failure rate, sets shouldTrip if threshold exceeded
	shouldTrip atomic.Bool

	// last obtained cumulative values (used to calculate increments within period)
	lastFailedRequests    float64
	lastSuccessedRequests float64
	lastCollectTime       time.Time

	// signal to stop timer
	stopCh chan struct{}

	// wait for timer goroutine to exit
	wg sync.WaitGroup
}

// NewCircuitBreaker creates circuit breaker instance and starts periodic checking
func NewCircuitBreaker(cfg *config.CircuitBreakerConfig) *CircuitBreaker {
	if cfg == nil || !cfg.Enable {
		return nil
	}
	cb := &CircuitBreaker{
		cfg:       cfg,
		state:     StateClosed,
		forceMode: ForceModeNone,
		stopCh:    make(chan struct{}),
	}
	// initialize metrics state
	cb.updateWorkerReadinessState()
	// start timer with CheckWindowSec period to check failure rate
	cb.startTicker()
	return cb
}

// startTicker starts timer, periodically collects data from metrics and checks failure rate
func (cb *CircuitBreaker) startTicker() {
	interval := time.Duration(cb.cfg.CheckWindowSec) * time.Second
	cb.wg.Add(1)
	go func() {
		defer cb.wg.Done()
		defer func() {
			if r := recover(); r != nil {
				logger.Log().Errorf("[CircuitBreaker] Timer goroutine panicked: %v", r)
			}
		}()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				cb.collectAndCheck()
			case <-cb.stopCh:
				return
			}
		}
	}()
}

// collectAndCheck collects metrics increments within a period and checks failure rate
func (cb *CircuitBreaker) collectAndCheck() {
	currentFailed := rsmetrics.GetNumFailedRequests()
	currentSuccessed := rsmetrics.GetNumSuccessedRequests()
	now := time.Now()

	cb.mu.Lock()
	defer cb.mu.Unlock()

	if cb.lastCollectTime.IsZero() {
		// first call, only record baseline
		cb.lastFailedRequests = currentFailed
		cb.lastSuccessedRequests = currentSuccessed
		cb.lastCollectTime = now
		return
	}

	deltaFailed := currentFailed - cb.lastFailedRequests
	deltaSuccessed := currentSuccessed - cb.lastSuccessedRequests

	cb.lastFailedRequests = currentFailed
	cb.lastSuccessedRequests = currentSuccessed
	cb.lastCollectTime = now

	total := deltaFailed + deltaSuccessed
	if total < float64(cb.cfg.MinRequestCount) {
		logger.Log().Debugf("[CircuitBreaker] Period check: total=%.0f < minRequestCount=%d, skip", total, cb.cfg.MinRequestCount)
		return
	}
	if total == 0 {
		logger.Log().Debugf("[CircuitBreaker] Period check: total=0, skip failure rate calculation")
		return
	}

	failureRate := deltaFailed / total
	logger.Log().Debugf("[CircuitBreaker] Period check: total=%.0f, failed=%.0f, successed=%.0f, failureRate=%.4f, threshold=%.4f",
		total, deltaFailed, deltaSuccessed, failureRate, cb.cfg.FailureRateThreshold)

	if failureRate >= cb.cfg.FailureRateThreshold {
		logger.Log().Warnf("[CircuitBreaker] Failure rate %.4f above threshold %.4f, setting shouldTrip flag",
			failureRate, cb.cfg.FailureRateThreshold)
		cb.shouldTrip.Store(true)
	} else {
		cb.shouldTrip.Store(false)
	}
}

// IsOpen checks if in open state
func (cb *CircuitBreaker) IsOpen() bool {
	cb.mu.RLock()
	defer cb.mu.RUnlock()
	return cb.state == StateOpen
}

// Reset resets circuit breaker state to closed (called when valid instance info is available)
func (cb *CircuitBreaker) Reset() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	if cb.state == StateOpen {
		logger.Log().Infof("[CircuitBreaker] Resetting circuit breaker to closed state due to valid instance info")
		cb.state = StateClosed
		cb.shouldTrip.Store(false)
		cb.lastFailedRequests = 0
		cb.lastSuccessedRequests = 0
		cb.lastCollectTime = time.Time{}
		// update unified metrics
		cb.updateWorkerReadinessState()
	}
}

// GetForceMode gets current force mode
func (cb *CircuitBreaker) GetForceMode() ForceMode {
	cb.mu.RLock()
	defer cb.mu.RUnlock()
	return cb.forceMode
}

// SetForceMode sets force mode
func (cb *CircuitBreaker) SetForceMode(mode ForceMode) bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	previousMode := cb.forceMode
	cb.forceMode = mode

	switch mode {
	case ForceModeTrue:
		logger.Log().Infof("[CircuitBreaker] Force mode changed from %d to ForceModeTrue (ready)", previousMode)
	case ForceModeFalse:
		logger.Log().Infof("[CircuitBreaker] Force mode changed from %d to ForceModeFalse (not ready)", previousMode)
	case ForceModeNone:
		logger.Log().Infof("[CircuitBreaker] Force mode changed from %d to ForceModeNone (auto)", previousMode)
	default:
		logger.Log().Warnf("[CircuitBreaker] Unknown Force mode [%d]", mode)
	}

	// update unified metrics
	cb.updateWorkerReadinessState()
	return true
}

// Close closes circuit breaker, stops timer and waits for goroutine to exit
func (cb *CircuitBreaker) Close() {
	close(cb.stopCh)
	cb.wg.Wait()
}

// CheckShouldTrip checks shouldTrip flag, only responsible for opening circuit
// Timer periodically checks failure rate, sets shouldTrip if threshold exceeded
// When probe calls this method, it reads shouldTrip flag to decide whether to trip
// Additionally, probe actively checks incremental failure rate from lastCollectTime to now
// to avoid the issue where failure rate exceeds threshold within timer interval but not detected in time
// Circuit closing is handled by Reset when checkHealthCache gets valid instance info
// Return: whether should trip (reject traffic)
func (cb *CircuitBreaker) CheckShouldTrip() bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	if cb.state == StateClosed {
		if cb.shouldTrip.Load() {
			logger.Log().Warnf("[CircuitBreaker] shouldTrip flag set, opening circuit breaker")
			cb.state = StateOpen
		} else {
			// proactive check: incremental failure rate from lastCollectTime to now
			// to avoid probe returning true when failure rate exceeds threshold within timer interval
			cb.checkProbeTimeDelta()
		}
	}

	// update unified metrics
	cb.updateWorkerReadinessState()

	return cb.state == StateOpen
}

// checkProbeTimeDelta proactively checks incremental failure rate since last collection when probe arrives
// This period is definitely within CheckWindowSec time window, so judgment logic is consistent with collectAndCheck
// Caller must hold cb.mu.Lock()
func (cb *CircuitBreaker) checkProbeTimeDelta() {
	if cb.lastCollectTime.IsZero() {
		// timer hasn't triggered first collection yet, no baseline data, cannot calculate increment
		return
	}

	currentFailed := rsmetrics.GetNumFailedRequests()
	currentSuccessed := rsmetrics.GetNumSuccessedRequests()

	deltaFailed := currentFailed - cb.lastFailedRequests
	deltaSuccessed := currentSuccessed - cb.lastSuccessedRequests

	total := deltaFailed + deltaSuccessed
	if total < float64(cb.cfg.MinRequestCount) {
		logger.Log().Debugf("[CircuitBreaker] Probe check: total=%.0f < minRequestCount=%d, skip", total, cb.cfg.MinRequestCount)
		return
	}
	if total == 0 {
		return
	}

	failureRate := deltaFailed / total
	logger.Log().Debugf("[CircuitBreaker] Probe check: total=%.0f, failed=%.0f, successed=%.0f, failureRate=%.4f, threshold=%.4f",
		total, deltaFailed, deltaSuccessed, failureRate, cb.cfg.FailureRateThreshold)

	if failureRate >= cb.cfg.FailureRateThreshold {
		logger.Log().Warnf("[CircuitBreaker] Probe check: failure rate %.4f above threshold %.4f, setting shouldTrip flag",
			failureRate, cb.cfg.FailureRateThreshold)
		cb.shouldTrip.Store(true)
		cb.state = StateOpen
		// update baseline to avoid timer repeatedly calculating same increment on next trigger
		cb.lastFailedRequests = currentFailed
		cb.lastSuccessedRequests = currentSuccessed
		cb.lastCollectTime = time.Now()
	}
}

// updateWorkerReadinessState updates unified metrics based on current state and force mode
func (cb *CircuitBreaker) updateWorkerReadinessState() {
	if cb.forceMode == ForceModeTrue {
		// force true (force accepting traffic)
		rsmetrics.SetWorkerReadinessState(float64(ReadinessStateForceTrue))
	} else if cb.forceMode == ForceModeFalse {
		// force false (force rejecting traffic)
		rsmetrics.SetWorkerReadinessState(float64(ReadinessStateForceFalse))
	} else {
		// auto mode, determined by circuit breaker state
		if cb.state == StateOpen {
			// broken (reject traffic because failure rate exceeded threshold)
			rsmetrics.SetWorkerReadinessState(float64(ReadinessStateBroken))
		} else {
			// not broken (accept traffic based on health check cache info)
			rsmetrics.SetWorkerReadinessState(float64(ReadinessStateNormal))
		}
	}
}
