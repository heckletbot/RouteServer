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
	"context"
	"encoding/binary"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ugorji/go/codec"
)

func newTestQuerier(t *testing.T, handler http.HandlerFunc) HitQuerier {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	querier, err := NewHitQuerier(HitQuerierConfig{}, srv.URL, 4)
	if err != nil {
		t.Fatalf("NewHitQuerier() error = %v", err)
	}
	return querier
}

func TestHitQuerierPartialBlockSkipsRequest(t *testing.T) {
	called := false
	querier := newTestQuerier(t, func(w http.ResponseWriter, r *http.Request) {
		called = true
	})

	hits, err := querier.Query(context.Background(), "qwen3-0.6b", []int32{1, 2, 3}, nil)
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}
	if hits != nil {
		t.Errorf("Query() hits = %v, want nil", hits)
	}
	if called {
		t.Errorf("Query() issued a request for a partial block")
	}
}

func TestHitQuerierSuccess(t *testing.T) {
	querier := newTestQuerier(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != pathQuery {
			t.Errorf("path = %q, want %q", r.URL.Path, pathQuery)
		}
		if r.Header.Get("Accept") != ContentTypeMsgpack {
			t.Errorf("Accept = %q", r.Header.Get("Accept"))
		}

		var payload struct {
			Model     string `codec:"model"`
			BlockSize int    `codec:"block_size"`
			TokenIDs  []byte `codec:"token_ids"`
		}
		if err := codec.NewDecoder(r.Body, newQueryMsgpackHandle()).Decode(&payload); err != nil {
			t.Fatalf("decode query request: %v", err)
		}
		if payload.Model != "qwen3-0.6b" {
			t.Errorf("model = %q", payload.Model)
		}
		if payload.BlockSize != 4 {
			t.Errorf("block_size = %d", payload.BlockSize)
		}
		raw := payload.TokenIDs
		if len(raw) != 4*4 {
			t.Fatalf("token_ids length = %d, want 16", len(raw))
		}
		if got := binary.LittleEndian.Uint32(raw[0:4]); got != 10 {
			t.Errorf("first token = %d, want 10", got)
		}

		writeMsgpack(t, w, http.StatusOK, map[string]interface{}{
			fieldInstances: map[string]interface{}{
				"prefill-0": map[string]interface{}{
					fieldDevice:   4,
					fieldCPULocal: 4,
					fieldCPUShare: 4,
					fieldDisk:     4,
				},
				"prefill-1": map[string]interface{}{
					fieldDevice:   0,
					fieldCPULocal: 0,
					fieldCPUShare: 0,
					fieldDisk:     0,
				},
			},
		})
	})

	hits, err := querier.Query(context.Background(), "qwen3-0.6b", []int32{10, 11, 12, 13}, nil)
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("len(hits) = %d, want 2", len(hits))
	}
	if hits["prefill-0"].Disk != 4 {
		t.Errorf("prefill-0 hit = %+v, want disk=4", hits["prefill-0"])
	}
	if hits["prefill-1"].Disk != 0 {
		t.Errorf("prefill-1 hit = %+v, want zero", hits["prefill-1"])
	}
}

func TestHitQuerierDropsOnlyMalformedInstance(t *testing.T) {
	querier := newTestQuerier(t, func(w http.ResponseWriter, r *http.Request) {
		writeMsgpack(t, w, http.StatusOK, map[string]interface{}{
			fieldInstances: map[string]interface{}{
				"good": map[string]interface{}{
					fieldDevice:   0,
					fieldCPULocal: 0,
					fieldCPUShare: 4,
					fieldDisk:     4,
				},
				"bad": map[string]interface{}{
					fieldDevice:   4,
					fieldCPULocal: 0,
					fieldCPUShare: 0,
					fieldDisk:     4,
				},
			},
		})
	})

	hits, err := querier.Query(context.Background(), "qwen3-0.6b", []int32{1, 2, 3, 4}, nil)
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("len(hits) = %d, want 1", len(hits))
	}
	if _, ok := hits["good"]; !ok {
		t.Errorf("hits = %v, want good instance kept", hits)
	}
}

func TestHitQuerierFallsBackToSingleRankMatch(t *testing.T) {
	querier := newTestQuerier(t, func(w http.ResponseWriter, r *http.Request) {
		writeMsgpack(t, w, http.StatusOK, map[string]interface{}{
			fieldInstances: map[string]interface{}{
				"prefill-7": map[string]interface{}{
					fieldRankMatches: map[string]interface{}{
						"7": map[string]interface{}{
							fieldDevice:   0,
							fieldCPULocal: 0,
							fieldCPUShare: 0,
							fieldDisk:     4,
						},
					},
				},
			},
		})
	})

	hits, err := querier.Query(context.Background(), "qwen3-0.6b", []int32{1, 2, 3, 4}, nil)
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}
	if hits["prefill-7"].Disk != 4 {
		t.Errorf("prefill-7 hit = %+v, want disk=4", hits["prefill-7"])
	}
}

func TestHitQuerierStatusError(t *testing.T) {
	querier := newTestQuerier(t, func(w http.ResponseWriter, r *http.Request) {
		writeMsgpack(t, w, http.StatusInternalServerError, map[string]interface{}{
			fieldError: "index unavailable",
		})
	})

	_, err := querier.Query(context.Background(), "qwen3-0.6b", []int32{1, 2, 3, 4}, nil)
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("Query() error = %v, want *APIError", err)
	}
	if apiErr.StatusCode != http.StatusInternalServerError {
		t.Errorf("StatusCode = %d, want 500", apiErr.StatusCode)
	}
	if got := ClassOf(err); got != ClassTransient {
		t.Errorf("ClassOf() = %v, want %v", got, ClassTransient)
	}
}
