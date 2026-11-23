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

// RouteRequest is everything a routing decision depends on. The caller resolves
// the request properties once; the router never reads the pipeline context.
type RouteRequest struct {
	RequestID           string
	PromptTokens        int32
	HasKVTransferParams bool
}

// Router turns a request plus a worker snapshot into a decision.
//
// The decision order is fixed: request gates, then the strategy, then admission.
// Every step that cannot produce a local route returns ModePD with the reason
// recorded, which is the designed behaviour of the feature rather than a
// recovery path.
type Router struct {
	params       Params
	strategy     Strategy
	provider     WorkerSnapshotProvider
	reservations *Manager
}

// NewRouter wires the decision inputs. All arguments are required for the
// feature to work; the pipeline refuses to start a coord phase without them.
func NewRouter(params Params, strategy Strategy, provider WorkerSnapshotProvider, reservations *Manager) *Router {
	return &Router{
		params:       params,
		strategy:     strategy,
		provider:     provider,
		reservations: reservations,
	}
}

// StrategyName reports the configured strategy name.
func (r *Router) StrategyName() string {
	return r.strategy.Name()
}

// Route decides how to serve req.
func (r *Router) Route(req RouteRequest) Decision {
	strategyName := r.strategy.Name()

	if req.HasKVTransferParams {
		return Decision{Mode: ModePD, Strategy: strategyName, Fallback: FallbackInternalPD}
	}
	if req.PromptTokens < int32(r.params.LocalMinTokens) {
		return Decision{Mode: ModePD, Strategy: strategyName, Fallback: FallbackPromptBelowMin}
	}

	workerID, role := r.strategy.Decide(r.provider.Snapshots(), req.PromptTokens, r.reservations)
	if workerID == "" {
		return Decision{Mode: ModePD, Strategy: strategyName, Fallback: FallbackNoIdleWorker}
	}

	reservation, admitted := r.reservations.TryAcquire(workerID, r.params.MaxInflightFor(role))
	if !admitted {
		return Decision{Mode: ModePD, Strategy: strategyName, Fallback: FallbackReservationRace}
	}

	return Decision{
		Mode:        ModeLocal,
		Role:        role,
		WorkerID:    workerID,
		Strategy:    strategyName,
		Reservation: reservation,
	}
}
