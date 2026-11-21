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
	"math"

	"route-server/pkg/common/logger"
	"route-server/pkg/route-server/scheduler/agentic_balancer/state"
)

// checkAndDequeue hands off one queued request per step until the queue is empty or nothing
// is dispatchable; a false step means no capacity now and the queue stays armed. Each step runs
// under DecisionMu, blocked entries keep their aging clock, spare slots are informational only.
func (c *Controller) checkAndDequeue() {
	if c.pq == nil || c.drainStep == nil || c.pq.Len() == 0 {
		return
	}

	spareEstimate := c.computeSpareSlots()

	dequeued := 0
	for c.pq.Len() > 0 {
		if !c.drainStep() {
			logger.Log().Debugf("controller: no dispatchable queued request (queueLen=%d, spareEstimate=%d), round ends",
				c.pq.Len(), spareEstimate)
			break
		}
		dequeued++
	}
	if dequeued > 0 {
		logger.Log().Infof("controller: dequeued %d requests this tick (spareEstimate=%d)", dequeued, spareEstimate)
	}
}

func (c *Controller) computeWorkerUtil(w *state.WorkerState) float64 {
	return state.ComputeWorkerUtil(c.sm, w)
}

func (c *Controller) computeSpareSlots() int {
	overloadUtil, targetRole := c.capacityParams()
	total := 0
	for _, w := range c.sm.Workers.ListAll() {
		if !w.Available || w.Role != targetRole {
			continue
		}
		util := c.computeWorkerUtil(w)
		if util < overloadUtil {
			headroom := overloadUtil - util
			if w.MaxNumSeqs > 0 {
				total += int(math.Ceil(headroom * float64(w.MaxNumSeqs)))
			} else {
				total += 1
			}
		}
	}
	return total
}
