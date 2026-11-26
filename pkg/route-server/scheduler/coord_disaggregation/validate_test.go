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
	"math"
	"strings"
	"testing"
	"time"
)

func TestValidateAcceptsShippedWindows(t *testing.T) {
	if err := shippedParams().Validate(); err != nil {
		t.Fatalf("shipped params must validate: %v", err)
	}
}

func TestValidateRejectsEachRule(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*Params)
		wantErr string
	}{
		{
			name:    "stale duration must be positive",
			mutate:  func(p *Params) { p.StaleDuration = 0 },
			wantErr: "metrics-stale-seconds must be > 0",
		},
		{
			name:    "kv usage above one",
			mutate:  func(p *Params) { p.IdleMaxKVUsage = 50 },
			wantErr: "idle-max-kv-usage must be within [0,1]",
		},
		{
			name:    "prefill minimum above its gate",
			mutate:  func(p *Params) { p.PMinTokens = 32768; p.PInputTokenGate = 1024 },
			wantErr: "p-local-min-input-tokens (32768) exceeds p-hybrid-max-input-tokens (1024)",
		},
		{
			name: "disabled decode still has a minimum",
			mutate: func(p *Params) {
				p.DInputTokenGate = 0
				p.DMinTokens = 8
			},
			wantErr: "decode role is disabled by d-hybrid-max-input-tokens=0, so d-local-min-input-tokens must be 0",
		},
		{
			name: "both windows disabled",
			mutate: func(p *Params) {
				p.PInputTokenGate = 0
				p.PMinTokens = 0
				p.DInputTokenGate = 0
				p.DMinTokens = 0
			},
			wantErr: "coord-routing is configured but both role windows are disabled",
		},
		{
			name:    "open prefill window with no inflight slots",
			mutate:  func(p *Params) { p.PMaxInflight = 0 },
			wantErr: "p-hybrid-max-inflight must be >= 1 when the prefill window is enabled",
		},
		{
			name:    "token gate above int32",
			mutate:  func(p *Params) { p.PInputTokenGate = math.MaxInt32 + 1 },
			wantErr: "p-hybrid-max-input-tokens must be <= 2147483647",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			params := shippedParams()
			if params.StaleDuration <= 0 {
				params.StaleDuration = 3 * time.Second
			}
			tc.mutate(&params)
			err := params.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Validate() = %v, want substring %q", err, tc.wantErr)
			}
		})
	}
}
