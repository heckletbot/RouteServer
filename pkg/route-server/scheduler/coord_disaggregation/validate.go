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
	"errors"
	"fmt"
	"math"
)

// Validate checks the decision inputs that would otherwise silently disable
// local routing. It returns every violation together so a half-filled config
// can be corrected in one restart.
func (p Params) Validate() error {
	var errs []error

	if p.StaleDuration <= 0 {
		errs = append(errs, errors.New("metrics-stale-seconds must be > 0"))
	}
	if math.IsNaN(p.IdleMaxKVUsage) || p.IdleMaxKVUsage < 0 || p.IdleMaxKVUsage > 1 {
		errs = append(errs, errors.New("idle-max-kv-usage must be within [0,1]"))
	}
	if p.IdleMaxWaiting < 0 {
		errs = append(errs, errors.New("idle-max-waiting must not be negative"))
	}
	if p.IdleMaxRunningP < 0 {
		errs = append(errs, errors.New("idle-max-running-p must not be negative"))
	}
	if p.IdleMaxRunningD < 0 {
		errs = append(errs, errors.New("idle-max-running-d must not be negative"))
	}
	if p.PMaxInflight < 0 {
		errs = append(errs, errors.New("p-hybrid-max-inflight must not be negative"))
	}
	if p.DMaxInflight < 0 {
		errs = append(errs, errors.New("d-hybrid-max-inflight must not be negative"))
	}

	errs = append(errs, validateWindow("prefill", "p-local-min-input-tokens", "p-hybrid-max-input-tokens", "p-hybrid-max-inflight",
		p.PMinTokens, p.PInputTokenGate, p.PMaxInflight)...)
	errs = append(errs, validateWindow("decode", "d-local-min-input-tokens", "d-hybrid-max-input-tokens", "d-hybrid-max-inflight",
		p.DMinTokens, p.DInputTokenGate, p.DMaxInflight)...)

	if p.PInputTokenGate <= 0 && p.DInputTokenGate <= 0 {
		errs = append(errs, errors.New("coord-routing is configured but both role windows are disabled"))
	}

	return errors.Join(errs...)
}

func validateWindow(role, minKey, gateKey, inflightKey string, minTokens, gate, inflight int) []error {
	var errs []error
	if minTokens < 0 {
		errs = append(errs, fmt.Errorf("%s must not be negative", minKey))
	}
	if gate < 0 {
		errs = append(errs, fmt.Errorf("%s must not be negative", gateKey))
	}
	if gate > 0 && minTokens > gate {
		errs = append(errs, fmt.Errorf("%s (%d) exceeds %s (%d)", minKey, minTokens, gateKey, gate))
	}
	if gate == 0 && minTokens != 0 {
		errs = append(errs, fmt.Errorf("%s role is disabled by %s=0, so %s must be 0", role, gateKey, minKey))
	}
	if gate > 0 && inflight < 1 {
		errs = append(errs, fmt.Errorf("%s must be >= 1 when the %s window is enabled", inflightKey, role))
	}
	if minTokens > math.MaxInt32 {
		errs = append(errs, fmt.Errorf("%s must be <= %d", minKey, math.MaxInt32))
	}
	if gate > math.MaxInt32 {
		errs = append(errs, fmt.Errorf("%s must be <= %d", gateKey, math.MaxInt32))
	}
	return errs
}
