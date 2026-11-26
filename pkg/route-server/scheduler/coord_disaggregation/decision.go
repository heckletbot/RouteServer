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
	FallbackOutsideWindow   Fallback = "outside_role_window"
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
	PMinTokens      int
	DMinTokens      int
}

// MinTokensFor returns the lower bound of the local-route window for role.
func (p Params) MinTokensFor(role string) int {
	if role == constant.Decode {
		return p.DMinTokens
	}
	return p.PMinTokens
}

// Accepts reports whether a prompt of tokens fits the window of role.
// A zero gate means the role never takes a local route. A zero token count
// stays on PD: the decode window starts at 0 in config, but an empty prompt
// is not a local generation.
func (p Params) Accepts(role string, tokens int32) bool {
	gate := p.InputTokenGateFor(role)
	return gate > 0 && tokens > 0 &&
		tokens >= int32(p.MinTokensFor(role)) && tokens <= int32(gate)
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
