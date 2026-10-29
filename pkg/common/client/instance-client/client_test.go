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

package instance_client

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"testing"

	"github.com/agiledragon/gomonkey/v2"

	"infer-dp-scheduler/pkg/common/config"
	http2 "infer-dp-scheduler/pkg/common/http"
)

func TestVllmClient_HealthCheck(t *testing.T) {
	tests := []struct {
		name           string
		buildReqErr    error
		doReqResp      *http.Response
		doReqErr       error
		expectedOk     bool
		expectedErrMsg string
	}{
		{
			name:       "Success",
			doReqResp:  &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(nil))},
			expectedOk: true,
		},
		{
			name:           "BuildRequestFails",
			buildReqErr:    errors.New("build request error"),
			expectedOk:     false,
			expectedErrMsg: "build health check request failed: build request error",
		},
		{
			name:           "DoRequestFails",
			doReqErr:       errors.New("do request error"),
			expectedOk:     false,
			expectedErrMsg: "health check failed: do request error",
		},
		{
			name:           "NonOKStatus",
			doReqResp:      &http.Response{StatusCode: http.StatusInternalServerError, Body: io.NopCloser(bytes.NewReader(nil))},
			expectedOk:     false,
			expectedErrMsg: "health check status code: 500, body = ",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &VllmClient{
				endpoint:        "http://localhost:8080",
				longServeClient: &http2.Client{},
				healthClient:    &http2.Client{},
			}

			// Create a new Patches object
			patches := gomonkey.NewPatches()
			defer patches.Reset()

			// Patch BuildHttpRequest
			patches.ApplyFunc(http2.BuildHttpRequest, func(ctx context.Context, method, url string, body io.Reader, header http.Header) (*http.Request, error) {
				if tt.buildReqErr != nil {
					return nil, tt.buildReqErr
				}
				return &http.Request{}, nil
			})

			// Patch DoRequest
			patches.ApplyMethod(reflect.TypeOf(client.longServeClient), "DoRequest", func(*http2.Client, *http.Request) (*http.Response, error) {
				if tt.doReqErr != nil {
					return nil, tt.doReqErr
				}
				return tt.doReqResp, nil
			})

			patches.ApplyMethod(reflect.TypeOf(client.healthClient), "DoRequest", func(*http2.Client, *http.Request) (*http.Response, error) {
				if tt.doReqErr != nil {
					return nil, tt.doReqErr
				}
				return tt.doReqResp, nil
			})

			ok, _, err := client.HealthCheck()
			if ok != tt.expectedOk {
				t.Errorf("HealthCheck() ok = %v, want %v", ok, tt.expectedOk)
			}
			if tt.expectedErrMsg != "" {
				if err == nil || err.Error() != tt.expectedErrMsg {
					t.Errorf("HealthCheck() error = %v, want %v", err, tt.expectedErrMsg)
				}
			} else if err != nil {
				t.Errorf("HealthCheck() unexpected error: %v", err)
			}
		})
	}
}

func TestVllmClient_Generate(t *testing.T) {
	tests := []struct {
		name           string
		buildReqErr    error
		doReqResp      *http.Response
		doReqErr       error
		expectedErrMsg string
	}{
		{
			name:      "Success",
			doReqResp: &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader([]byte("success")))},
		},
		{
			name:           "BuildRequestFails",
			buildReqErr:    errors.New("build request error"),
			expectedErrMsg: "generate request failed: build request error",
		},
		{
			name:           "DoRequestFails",
			doReqErr:       errors.New("do request error"),
			expectedErrMsg: "generate request failed: do request error",
		},
		{
			name:           "NonOKStatus",
			doReqResp:      &http.Response{StatusCode: http.StatusBadRequest, Body: io.NopCloser(bytes.NewReader(nil))},
			expectedErrMsg: "generate request failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &VllmClient{
				endpoint:        "http://localhost:8080",
				longServeClient: &http2.Client{},
				healthClient:    &http2.Client{},
			}

			// Create a new Patches object
			patches := gomonkey.NewPatches()
			defer patches.Reset()

			// Patch BuildHttpRequest
			patches.ApplyFunc(http2.BuildHttpRequest, func(ctx context.Context, method, url string, body io.Reader, header http.Header) (*http.Request, error) {
				if tt.buildReqErr != nil {
					return nil, tt.buildReqErr
				}
				return &http.Request{}, nil
			})

			// Patch DoRequest
			patches.ApplyMethod(reflect.TypeOf(client.longServeClient), "DoRequest", func(*http2.Client, *http.Request) (*http.Response, error) {
				if tt.doReqErr != nil {
					return nil, tt.doReqErr
				}
				return tt.doReqResp, nil
			})

			// Patch DoRequest
			patches.ApplyMethod(reflect.TypeOf(client.healthClient), "DoRequest", func(*http2.Client, *http.Request) (*http.Response, error) {
				if tt.doReqErr != nil {
					return nil, tt.doReqErr
				}
				return tt.doReqResp, nil
			})

			resp, err, _ := client.Generate(context.Background(), http.Header{}, "/generate", nil, "", "", "test-req", false)
			if tt.expectedErrMsg != "" {
				if resp != nil {
					t.Errorf("Generate() resp = %v, want nil", resp)
				}
			} else {
				if err != nil {
					t.Errorf("Generate() unexpected error: %v", err)
				}
				if resp.StatusCode != http.StatusOK {
					t.Errorf("Generate() resp.StatusCode = %d, want %d", resp.StatusCode, http.StatusOK)
				}
			}
		})
	}
}

func TestVllmClient_ModelName(t *testing.T) {
	tests := []struct {
		name           string
		buildReqErr    error
		doReqResp      *http.Response
		doReqErr       error
		expectedOk     bool
		expectedErrMsg string
	}{
		{
			name:       "Success",
			doReqResp:  &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader([]byte("{\n   \"data\": [\n      {\n         \"id\": \"qwen3-32b\"\n      }\n   ]\n}")))},
			expectedOk: true,
		},
		{
			name:           "FailToGetModelName",
			doReqResp:      &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader([]byte("{}")))},
			expectedErrMsg: "fail to get model name from response",
		},
		{
			name:           "BuildRequestFails",
			buildReqErr:    errors.New("build request error"),
			expectedErrMsg: "failed to build request caused by: build request error",
		},
		{
			name:           "DoRequestFails",
			doReqErr:       errors.New("do request error"),
			expectedErrMsg: "failed to fetch model name caused by: do request error",
		},
		{
			name:           "NonOKStatus",
			doReqResp:      &http.Response{StatusCode: http.StatusInternalServerError, Body: io.NopCloser(bytes.NewReader(nil))},
			expectedErrMsg: "fetch model name Status Code: 500",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &VllmClient{
				endpoint:        "http://localhost:8080",
				longServeClient: &http2.Client{},
				healthClient:    &http2.Client{},
			}

			// Create a new Patches object
			patches := gomonkey.NewPatches()
			defer patches.Reset()

			// Patch BuildHttpRequest
			patches.ApplyFunc(http2.BuildHttpRequest, func(ctx context.Context, method, url string, body io.Reader, header http.Header) (*http.Request, error) {
				if tt.buildReqErr != nil {
					return nil, tt.buildReqErr
				}
				return &http.Request{}, nil
			})

			// Patch DoRequest
			patches.ApplyMethod(reflect.TypeOf(client.longServeClient), "DoRequest", func(*http2.Client, *http.Request) (*http.Response, error) {
				if tt.doReqErr != nil {
					return nil, tt.doReqErr
				}
				return tt.doReqResp, nil
			})

			// Patch DoRequest
			patches.ApplyMethod(reflect.TypeOf(client.healthClient), "DoRequest", func(*http2.Client, *http.Request) (*http.Response, error) {
				if tt.doReqErr != nil {
					return nil, tt.doReqErr
				}
				return tt.doReqResp, nil
			})

			bytes, err := client.ModelName()
			if tt.expectedErrMsg != "" {
				if err == nil || err.Error() != tt.expectedErrMsg {
					t.Errorf("ModelName() error = %v, want %v", err, tt.expectedErrMsg)
				}
				if bytes != "" {
					t.Errorf("ModelName() resp = %v, want nil", bytes)
				}
			} else {
				if err != nil {
					t.Errorf("ModelName() unexpected error: %v", err)
				}
			}
		})
	}
}

func TestNewClient(t *testing.T) {
	tests := []struct {
		name           string
		clientType     string
		route          config.Route
		httpCfg        config.HttpConfig
		newClientErr   error
		expectedErrMsg string
	}{
		{
			name:       "ValidVllmClient",
			clientType: "vllm",
			route: config.Route{
				Ip:          "localhost",
				Port:        8080,
				Role:        "prefill",
				StaticInfos: map[string]string{},
			},
			httpCfg: config.HttpConfig{},
		},
		{
			name:           "InvalidRole",
			clientType:     "vllm",
			route:          config.Route{Role: "invalid"},
			expectedErrMsg: "invalid role for vllm client: invalid, must be 'prefill' or 'decode'",
		},
		{
			name:           "UnknownClientType",
			clientType:     "unknown",
			expectedErrMsg: "unknown client type: unknown",
		},
		{
			name:       "HttpClientCreationFails",
			clientType: "vllm",
			route: config.Route{
				Ip:   "localhost",
				Port: 8080,
				Role: "prefill",
			},
			newClientErr:   errors.New("http client error"),
			expectedErrMsg: "failed to create http longServeClient: http client error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a new Patches object
			patches := gomonkey.NewPatches()
			defer patches.Reset()

			// Patch http2.NewClient
			patches.ApplyFunc(http2.NewClient, func(*config.HttpConfig, bool) (*http2.Client, error) {
				if tt.newClientErr != nil {
					return nil, tt.newClientErr
				}
				return &http2.Client{}, nil
			})

			client, err := NewClient(tt.clientType, tt.route, ClientConfig{
				HttpCfg:      tt.httpCfg,
				CheckTimeout: 0,
			})
			if tt.expectedErrMsg != "" {
				if err == nil || err.Error() != tt.expectedErrMsg {
					t.Errorf("NewClient() error = %v, want %v", err, tt.expectedErrMsg)
				}
				if client != nil {
					t.Errorf("NewClient() client = %v, want nil", client)
				}
			} else {
				if err != nil {
					t.Errorf("NewClient() unexpected error: %v", err)
				}
				if client == nil {
					t.Errorf("NewClient() client = nil, want non-nil")
				} else {
					vllmClient, ok := client.(*VllmClient)
					if !ok {
						t.Errorf("NewClient() client type = %T, want *VllmClient", client)
					}
					expectedEndpoint := fmt.Sprintf("http://%s:%d", tt.route.Ip, tt.route.Port)
					if vllmClient.endpoint != expectedEndpoint {
						t.Errorf("NewClient() endpoint = %s, want %s", vllmClient.endpoint, expectedEndpoint)
					}
				}
			}
		})
	}
}

func TestVllmClient_Metrics(t *testing.T) {
	tests := []struct {
		name           string
		buildReqErr    error
		doReqResp      *http.Response
		doReqErr       error
		expectedOk     bool
		expectedErrMsg string
	}{
		{
			name:      "Success",
			doReqResp: &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(nil))},
		},
		{
			name:           "BuildRequestFails",
			buildReqErr:    errors.New("build request error"),
			expectedErrMsg: "failed to build request caused by: build request error",
		},
		{
			name:           "DoRequestFails",
			doReqErr:       errors.New("do request error"),
			expectedErrMsg: "failed to fetch metrics caused by: do request error",
		},
		{
			name:           "NonOKStatus",
			doReqResp:      &http.Response{StatusCode: http.StatusInternalServerError, Body: io.NopCloser(bytes.NewReader(nil))},
			expectedErrMsg: "fetch metrics Status Code: 500",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &VllmClient{
				endpoint:        "http://localhost:8080",
				longServeClient: &http2.Client{},
				healthClient:    &http2.Client{},
			}

			// Create a new Patches object
			patches := gomonkey.NewPatches()
			defer patches.Reset()

			// Patch BuildHttpRequest
			patches.ApplyFunc(http2.BuildHttpRequest, func(ctx context.Context, method, url string, body io.Reader, header http.Header) (*http.Request, error) {
				if tt.buildReqErr != nil {
					return nil, tt.buildReqErr
				}
				return &http.Request{}, nil
			})

			// Patch DoRequest
			patches.ApplyMethod(reflect.TypeOf(client.longServeClient), "DoRequest", func(*http2.Client, *http.Request) (*http.Response, error) {
				if tt.doReqErr != nil {
					return nil, tt.doReqErr
				}
				return tt.doReqResp, nil
			})

			// Patch DoRequest
			patches.ApplyMethod(reflect.TypeOf(client.healthClient), "DoRequest", func(*http2.Client, *http.Request) (*http.Response, error) {
				if tt.doReqErr != nil {
					return nil, tt.doReqErr
				}
				return tt.doReqResp, nil
			})

			bytes, err := client.Metrics()
			if tt.expectedErrMsg != "" {
				if err == nil || err.Error() != tt.expectedErrMsg {
					t.Errorf("Metrics() error = %v, want %v", err, tt.expectedErrMsg)
				}
				if bytes != nil {
					t.Errorf("Metrics() resp = %v, want nil", bytes)
				}
			} else {
				if err != nil {
					t.Errorf("Metrics() unexpected error: %v", err)
				}
			}
		})
	}
}
