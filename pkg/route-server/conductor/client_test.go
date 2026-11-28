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
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ugorji/go/codec"
)

func newTestClient(t *testing.T, handler http.HandlerFunc) *HTTPClient {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	client, err := NewHTTPClient(Config{
		Enabled:        true,
		BaseURL:        srv.URL,
		RequestTimeout: DefaultRequestTimeout,
		BlockSize:      DefaultBlockSize,
	})
	if err != nil {
		t.Fatalf("NewHTTPClient() error = %v", err)
	}
	return client
}

func writeMsgpack(t *testing.T, w http.ResponseWriter, status int, payload interface{}) {
	t.Helper()
	var body []byte
	if err := codec.NewEncoderBytes(&body, newMsgpackHandle()).Encode(payload); err != nil {
		t.Fatalf("encode test response: %v", err)
	}
	w.Header().Set("Content-Type", ContentTypeMsgpack)
	w.WriteHeader(status)
	if _, err := w.Write(body); err != nil {
		t.Fatalf("write test response: %v", err)
	}
}

func TestHTTPClientRegisterSuccess(t *testing.T) {
	var got map[string]interface{}
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != pathRegister {
			t.Errorf("path = %q, want %q", r.URL.Path, pathRegister)
		}
		if r.Header.Get("Content-Type") != ContentTypeMsgpack {
			t.Errorf("Content-Type = %q", r.Header.Get("Content-Type"))
		}
		if err := codec.NewDecoder(r.Body, newMsgpackHandle()).Decode(&got); err != nil {
			t.Errorf("decode request: %v", err)
		}
		writeMsgpack(t, w, http.StatusOK, map[string]interface{}{fieldInstanceID: "prefill-0"})
	})

	err := client.Register(context.Background(), RegisterRequest{
		Endpoint:    "tcp://10.0.0.1:5557",
		Type:        InstanceTypeVLLM,
		ModelName:   "qwen3-0.6b",
		InstanceID:  "prefill-0",
		BlockSize:   DefaultBlockSize,
		HashProfile: NewDefaultHashProfile(),
	})
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	if got["instance_id"] != "prefill-0" || got["modelname"] != "qwen3-0.6b" {
		t.Errorf("decoded request = %v", got)
	}
	if got["block_size"] != int64(DefaultBlockSize) {
		t.Errorf("block_size = %v (%T), want %d", got["block_size"], got["block_size"], DefaultBlockSize)
	}
}

func TestHTTPClientUnregisterNotFoundIsSuccess(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeMsgpack(t, w, http.StatusNotFound, map[string]interface{}{
			fieldReason: "instance_not_found",
			fieldError:  "instance is not registered",
		})
	})

	err := client.Unregister(context.Background(), UnregisterRequest{InstanceID: "prefill-0"})
	if err != nil {
		t.Fatalf("Unregister() error = %v, want nil for a valid 404", err)
	}
}

func TestHTTPClientSemanticErrorIsClassified(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeMsgpack(t, w, http.StatusBadRequest, map[string]interface{}{
			fieldReason: reasonInvalidRegistration,
			fieldError:  "block size mismatch",
		})
	})

	err := client.Register(context.Background(), RegisterRequest{InstanceID: "prefill-0"})
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("Register() error = %v, want *APIError", err)
	}
	if apiErr.StatusCode != http.StatusBadRequest {
		t.Errorf("StatusCode = %d, want 400", apiErr.StatusCode)
	}
	if got := ClassOf(err); got != ClassConflict {
		t.Errorf("ClassOf() = %v, want %v", got, ClassConflict)
	}
}

func TestHTTPClientRejectsNonMsgpackSuccess(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("<html>gateway</html>"))
	})

	err := client.Register(context.Background(), RegisterRequest{InstanceID: "prefill-0"})
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("Register() error = %v, want *APIError", err)
	}
	if apiErr.Reason != reasonUnsupportedContentType {
		t.Errorf("Reason = %q, want %q", apiErr.Reason, reasonUnsupportedContentType)
	}
	if got := ClassOf(err); got != ClassProtocol {
		t.Errorf("ClassOf() = %v, want %v", got, ClassProtocol)
	}
}

func TestHTTPClientServices(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		if r.URL.Path != pathServices {
			t.Errorf("path = %q, want %q", r.URL.Path, pathServices)
		}
		if r.Header.Get("Accept") != ContentTypeMsgpack {
			t.Errorf("Accept = %q", r.Header.Get("Accept"))
		}
		writeMsgpack(t, w, http.StatusOK, map[string]interface{}{
			"count": 2,
			"services": []interface{}{
				map[string]interface{}{
					"Endpoint":       "tcp://7.242.105.78:5657",
					"ReplayEndpoint": "tcp://7.242.105.78:5667",
					"Type":           "vLLM",
					"ModelName":      "qwen3.6",
					"TenantID":       "default",
					"InstanceID":     "decode-0",
					"BlockSize":      1152,
					"DPRank":         0,
					"HashProfile": map[string]interface{}{
						"strategy":         "vllm_v1",
						"algorithm":        "sha256",
						"python_hash_seed": "0",
						"index_projection": "low64_be",
					},
				},
				map[string]interface{}{
					"Endpoint":   "tcp://7.242.105.78:5557",
					"InstanceID": "prefill-0",
					"TenantID":   "default",
					"BlockSize":  1152,
					"DPRank":     2,
				},
			},
		})
	})

	got, err := client.Services(context.Background())
	if err != nil {
		t.Fatalf("Services() error = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len(services) = %d, want 2", len(got))
	}
	if got[0].InstanceID != "decode-0" || got[0].Endpoint != "tcp://7.242.105.78:5657" ||
		got[0].BlockSize != 1152 || got[0].DPRank != 0 || got[0].TenantID != "default" {
		t.Errorf("decode service = %+v", got[0])
	}
	if got[1].InstanceID != "prefill-0" || got[1].Endpoint != "tcp://7.242.105.78:5557" || got[1].DPRank != 2 {
		t.Errorf("prefill service = %+v", got[1])
	}
}

func TestHTTPClientServicesRejectsMissingEndpoint(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeMsgpack(t, w, http.StatusOK, map[string]interface{}{
			"count": 1,
			"services": []interface{}{
				map[string]interface{}{
					"InstanceID": "decode-0",
					"TenantID":   "default",
					"BlockSize":  1152,
					"DPRank":     0,
				},
			},
		})
	})

	if _, err := client.Services(context.Background()); err == nil {
		t.Fatal("Services() error = nil, want missing Endpoint")
	}
}

func TestClassifyStatus(t *testing.T) {
	cases := []struct {
		status int
		reason string
		want   ErrorClass
	}{
		{http.StatusInternalServerError, "", ClassTransient},
		{http.StatusBadGateway, reasonUnsupportedContentType, ClassTransient},
		{http.StatusBadRequest, "", ClassSemantic},
		{http.StatusNotFound, "", ClassProtocol},
		{http.StatusUnsupportedMediaType, "", ClassProtocol},
		{http.StatusOK, "", ClassProtocol},
		{http.StatusBadRequest, reasonInvalidRegistration, ClassConflict},
	}
	for _, tc := range cases {
		if got := ClassifyStatus(tc.status, tc.reason); got != tc.want {
			t.Errorf("ClassifyStatus(%d, %q) = %v, want %v", tc.status, tc.reason, got, tc.want)
		}
	}
}
