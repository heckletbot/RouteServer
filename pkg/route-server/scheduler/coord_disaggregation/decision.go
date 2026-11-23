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

import (
	"time"

	"route-server/pkg/common/constant"
)

// Mode is the routing outcome of a coord decision.
type Mode string

const (
	// ModePD walks the regular prefill -> decode chain.
	ModePD Mode = "pd"
	// ModeLocal serves the whole request on a single idle worker.
	ModeLocal Mode = "local"
)

// Fallback explains why a request did not take the local path. It is only set
// on ModePD decisions, so every PD outcome is attributable to one reason.
type Fallback string

const (
	FallbackInternalPD      Fallback = "internal_pd_request"
	FallbackPromptBelowMin  Fallback = "prompt_below_min"
	FallbackNoIdleWorker    Fallback = "no_idle_worker"
	FallbackReservationRace Fallback = "reservation_race"
)

// Decision is the result of one routing decision. It carries the reservation
// acquired for ModeLocal so that the caller owns exactly one release token.
type Decision struct {
	Mode        Mode
	Role        string
	WorkerID    string
	Strategy    string
	Fallback    Fallback
	Reservation Reservation
}

// Params are the decision inputs resolved from the coord routing config. They
// are a plain struct so this package stays independent of the config package.
type Params struct {
	StaleDuration   time.Duration
	IdleMaxWaiting  int
	IdleMaxRunningP int
	IdleMaxRunningD int
	IdleMaxKVUsage  float64
	PMaxInflight    int
	DMaxInflight    int
	PInputTokenGate int
	DInputTokenGate int
	LocalMinTokens  int
}

// MaxInflightFor returns the reservation cap of the given worker role.
func (p Params) MaxInflightFor(role string) int {
	if role == constant.Decode {
		return p.DMaxInflight
	}
	return p.PMaxInflight
}

// InputTokenGateFor returns the prompt token gate of the given worker role.
func (p Params) InputTokenGateFor(role string) int {
	if role == constant.Decode {
		return p.DInputTokenGate
	}
	return p.PInputTokenGate
}
