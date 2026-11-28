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

package conductor

import (
	"testing"
	"time"

	"route-server/pkg/common/config"
)

func TestNormalizeConfigDisabledReturnsZeroValue(t *testing.T) {
	cfg, err := NormalizeConfig(config.ConductorConfig{
		Enabled: false,
		BaseURL: "not-a-url",
	})
	if err != nil {
		t.Fatalf("NormalizeConfig() error = %v", err)
	}
	if cfg.Enabled {
		t.Fatalf("NormalizeConfig() enabled = true, want false")
	}
	if cfg.BaseURL != "" || cfg.BlockSize != 0 {
		t.Fatalf("NormalizeConfig() returned non-zero disabled config: %+v", cfg)
	}
}

func TestNormalizeConfigDefaults(t *testing.T) {
	cfg, err := NormalizeConfig(config.ConductorConfig{Enabled: true})
	if err != nil {
		t.Fatalf("NormalizeConfig() error = %v", err)
	}
	if cfg.BaseURL != DefaultBaseURL {
		t.Errorf("BaseURL = %q, want %q", cfg.BaseURL, DefaultBaseURL)
	}
	if cfg.RequestTimeout != DefaultRequestTimeout {
		t.Errorf("RequestTimeout = %v, want %v", cfg.RequestTimeout, DefaultRequestTimeout)
	}
	if cfg.QueryTimeout != DefaultQueryTimeout {
		t.Errorf("QueryTimeout = %v, want %v", cfg.QueryTimeout, DefaultQueryTimeout)
	}
	if cfg.RetryInterval != DefaultRetryInterval {
		t.Errorf("RetryInterval = %v, want %v", cfg.RetryInterval, DefaultRetryInterval)
	}
	if cfg.BlockSize != DefaultBlockSize {
		t.Errorf("BlockSize = %d, want %d", cfg.BlockSize, DefaultBlockSize)
	}
	if !cfg.Manages(RolePrefill) || cfg.Manages(RoleDecode) {
		t.Errorf("default roles = %v, want only prefill", cfg.Roles())
	}
	if cfg.TokenSource != TokenSourceRender {
		t.Errorf("TokenSource = %q, want %q", cfg.TokenSource, TokenSourceRender)
	}
}

func TestNormalizeConfigOverrides(t *testing.T) {
	cfg, err := NormalizeConfig(config.ConductorConfig{
		Enabled:          true,
		BaseURL:          "https://conductor.example/gateway/",
		RequestTimeoutMs: 2500,
		QueryTimeoutMs:   75,
		RetryIntervalMs:  45000,
		BlockSize:        1024,
		RegisterRoles:    " decode, prefill ,decode ",
		TokenSource:      " tokenize ",
	})
	if err != nil {
		t.Fatalf("NormalizeConfig() error = %v", err)
	}
	if cfg.BaseURL != "https://conductor.example/gateway" {
		t.Errorf("BaseURL = %q, want trailing slash trimmed", cfg.BaseURL)
	}
	if cfg.RequestTimeout != 2500*time.Millisecond {
		t.Errorf("RequestTimeout = %v, want 2.5s", cfg.RequestTimeout)
	}
	if cfg.QueryTimeout != 75*time.Millisecond {
		t.Errorf("QueryTimeout = %v, want 75ms", cfg.QueryTimeout)
	}
	if cfg.RetryInterval != 45*time.Second {
		t.Errorf("RetryInterval = %v, want 45s", cfg.RetryInterval)
	}
	if cfg.BlockSize != 1024 {
		t.Errorf("BlockSize = %d, want 1024", cfg.BlockSize)
	}
	if !cfg.Manages(RolePrefill) || !cfg.Manages(RoleDecode) {
		t.Errorf("roles = %v, want prefill and decode", cfg.Roles())
	}
	if cfg.TokenSource != TokenSourceTokenize {
		t.Errorf("TokenSource = %q, want %q", cfg.TokenSource, TokenSourceTokenize)
	}
}

func TestNormalizeConfigRejectsInvalidValues(t *testing.T) {
	cases := map[string]config.ConductorConfig{
		"relative url": {
			Enabled: true,
			BaseURL: "conductor:13333",
		},
		"unsupported scheme": {
			Enabled: true,
			BaseURL: "ftp://conductor:13333",
		},
		"negative request timeout": {
			Enabled:          true,
			RequestTimeoutMs: -1,
		},
		"negative query timeout": {
			Enabled:        true,
			QueryTimeoutMs: -1,
		},
		"negative retry interval": {
			Enabled:         true,
			RetryIntervalMs: -1,
		},
		"negative block size": {
			Enabled:   true,
			BlockSize: -1,
		},
		"unknown role": {
			Enabled:       true,
			RegisterRoles: "prefill,encoder",
		},
		"empty role list": {
			Enabled:       true,
			RegisterRoles: " , ",
		},
		"unknown token source": {
			Enabled:     true,
			TokenSource: "renderer",
		},
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := NormalizeConfig(raw); err == nil {
				t.Fatalf("NormalizeConfig() error = nil, want error")
			}
		})
	}
}
