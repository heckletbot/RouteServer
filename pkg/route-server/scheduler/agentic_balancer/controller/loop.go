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

package controller

import (
	"sync"
	"time"

	"route-server/pkg/common/constant"
	"route-server/pkg/common/logger"
	"route-server/pkg/route-server/scheduler/agentic_balancer/queue"
	"route-server/pkg/route-server/scheduler/agentic_balancer/state"
)

type ControllerConfig struct {
	Interval       time.Duration // tick interval (default 5s)
	StaleWorkerTTL time.Duration // worker not updated for this long → mark unavailable (default 60s)
	OverloadUtil   float64       // worker util above this is considered overloaded, used for dequeue capacity (default 0.90)
	TargetRole     string        // worker role to consider for spare slots (default: prefill)
}

func (c ControllerConfig) withDefaults() ControllerConfig {
	if c.Interval <= 0 {
		c.Interval = 5 * time.Second
	}
	if c.StaleWorkerTTL <= 0 {
		c.StaleWorkerTTL = 60 * time.Second
	}
	if c.OverloadUtil <= 0 || c.OverloadUtil > 1 {
		c.OverloadUtil = 0.90
	}
	if c.TargetRole == "" {
		c.TargetRole = constant.Prefill
	}
	return c
}

type Controller struct {
	sm     *state.StateManager
	cfg    ControllerConfig
	stopCh chan struct{}
	once   sync.Once

	isLeaderFunc func() bool
	pq           *queue.PriorityQueue

	// paramMu guards the drain parameters, hot-refreshed on UpdateConfig. Leaf lock: never
	// hold it with DecisionMu or pq.mu, nor across a drainStep call.
	paramMu      sync.RWMutex
	overloadUtil float64
	targetRole   string
	// drainStep runs one hand-off under the decision lock it takes itself, dispatching at
	// most one entry; the queue-full path reuses it.
	drainStep func() bool

	// wakeCh carries one non-blocking token when the queue may be able to move; capacity 1
	// coalesces concurrent notifications, it is not a rate limit.
	wakeCh chan struct{}
}

func NewController(sm *state.StateManager, cfg ControllerConfig, isLeaderFunc func() bool, pq *queue.PriorityQueue, drainStep func() bool) *Controller {
	cfg = cfg.withDefaults()
	if isLeaderFunc == nil {
		isLeaderFunc = func() bool { return true }
	}
	return &Controller{
		sm:           sm,
		cfg:          cfg,
		stopCh:       make(chan struct{}),
		isLeaderFunc: isLeaderFunc,
		pq:           pq,
		drainStep:    drainStep,
		wakeCh:       make(chan struct{}, 1),

		overloadUtil: cfg.OverloadUtil,
		targetRole:   cfg.TargetRole,
	}
}

// UpdateCapacityParams refreshes the parameters the next drain reads.
func (c *Controller) UpdateCapacityParams(overloadUtil float64, targetRole string) {
	c.paramMu.Lock()
	defer c.paramMu.Unlock()
	c.overloadUtil = overloadUtil
	c.targetRole = targetRole
}

func (c *Controller) capacityParams() (overloadUtil float64, targetRole string) {
	c.paramMu.RLock()
	defer c.paramMu.RUnlock()
	return c.overloadUtil, c.targetRole
}

// Wake is non-blocking, so hot paths (dispatch, event callbacks) may call it.
func (c *Controller) Wake() {
	select {
	case c.wakeCh <- struct{}{}:
	default:
	}
}

func (c *Controller) Run() {
	ticker := time.NewTicker(c.cfg.Interval)
	defer ticker.Stop()

	logger.Log().Infof("agentic-balancer controller started, interval=%s, staleWorkerTTL=%s",
		c.cfg.Interval, c.cfg.StaleWorkerTTL)

	for {
		select {
		// Both channels are read with ok: a closed stopCh exits, a closed wakeCh must not spin.
		case _, ok := <-c.stopCh:
			if !ok {
				logger.Log().Infof("agentic-balancer controller stopped")
				return
			}
		case _, ok := <-c.wakeCh:
			if !ok {
				return
			}
			// A wake only drains; TTL/orphan/stale cleanup stays on the ticker fallback.
			// Non-leaders never dispatch, so the leader check gates this path like the tick.
			if !c.isLeaderFunc() {
				continue
			}
			c.checkAndDequeue()
		case <-ticker.C:
			if !c.isLeaderFunc() {
				continue
			}
			c.ScheduledCheck()
		}
	}
}

func (c *Controller) Stop() {
	c.once.Do(func() {
		close(c.stopCh)
	})
}

func (c *Controller) ScheduledCheck() {
	c.cleanupExpiredSessions()
	c.detectStaleWorkers()
	c.cleanupOrphanSessions()
	c.checkAndDequeue()
}

func (c *Controller) cleanupExpiredSessions() {
	expired := c.sm.TTL.CleanupExpiredAtomic()
	if len(expired) == 0 {
		return
	}
	for _, sid := range expired {
		c.sm.Sessions.Delete(sid)
		c.sm.Inflight.RemoveBySession(sid)
	}
	logger.Log().Infof("agentic-balancer controller: cleaned up %d expired sessions", len(expired))
}

// detectStaleWorkers is a safety net for workers silent beyond StaleWorkerTTL; primary
// health detection is the framework's UpdateCluster callback.
func (c *Controller) detectStaleWorkers() {
	now := time.Now()
	staleTTL := c.cfg.StaleWorkerTTL
	staleCount := 0

	for _, w := range c.sm.Workers.ListAll() {
		if !w.Available {
			continue
		}
		if now.Sub(w.UpdatedAt) > staleTTL {
			c.sm.Workers.MarkUnavailable(w.WorkerId)
			staleCount++
			logger.Log().Warnf("agentic-balancer controller: worker %s stale (last update %v), marked unavailable",
				w.WorkerId, w.UpdatedAt)
		}
	}
	if staleCount > 0 {
		logger.Log().Infof("agentic-balancer controller: marked %d stale workers unavailable", staleCount)
	}
}

func (c *Controller) cleanupOrphanSessions() {
	workerIds := make(map[string]struct{})
	for _, w := range c.sm.Workers.ListAll() {
		workerIds[w.WorkerId] = struct{}{}
	}

	orphanCount := 0
	for _, entry := range c.sm.Sessions.ListActive() {
		aw := entry.GetAssignedWorker()
		if aw == "" {
			continue
		}
		if _, exists := workerIds[aw]; !exists {
			logger.Log().Infof("agentic-balancer controller: session %s orphaned (assigned worker %s gone), clearing binding",
				entry.SessionId, aw)
			entry.ClearAssignedWorker()
			orphanCount++
		}
	}
	if orphanCount > 0 {
		logger.Log().Infof("agentic-balancer controller: cleared %d orphan session bindings", orphanCount)
	}
}
