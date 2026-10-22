/*
 * Copyright 2026 The vLLM-Cloud Authors.
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

package route_server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"infer-dp-scheduler/pkg/common/openai"
	"infer-dp-scheduler/pkg/common/utils"
)

// -------------------------------------------------------
// 1. Basic Structure Definitions (OpenAI Protocol)
// -------------------------------------------------------

// RSClient encapsulates the configuration needed for testing
type RSClient struct {
	BaseURL    string
	HTTPClient *http.Client
}

// NewOpenAIClient creates a new client instance
func NewRSClient(baseURL string) *RSClient {
	return &RSClient{
		BaseURL: baseURL,
		HTTPClient: &http.Client{
			Timeout: 600 * time.Second,
		},
	}
}

// -------------------------------------------------------
// 2. Strongly-typed Request/Response Definitions (for Happy Path)
// -------------------------------------------------------
const (
	InvalidRequestBody = `
{
	"model": invalid,
	"temperature": 0.5,
	"max_tokens": 1024,
	"stream": false
}`
)

// -------------------------------------------------------
// 3. Core Send Methods
// -------------------------------------------------------

// SendChatCompletion sends a request to /v1/chat/completions
// body: can be ChatCompletionRequest struct or map[string]interface{} (for testing invalid fields)
// extraHeaders: used to test specific Header logic, such as custom Trace ID or fault injection Header
func (c *RSClient) SendChatCompletion(ctx context.Context, body interface{}, extraHeaders map[string]string) (*http.Response, error) {
	return c.PostRequest(ctx, "/v1/chat/completions", body, extraHeaders)
}

// SendCompletion sends a request to /v1/completions
// body: can be CompletionRequest struct or map[string]interface{} (for testing invalid fields)
// extraHeaders: used to test specific Header logic, such as custom Trace ID or fault injection Header
func (c *RSClient) SendCompletion(ctx context.Context, body interface{}, extraHeaders map[string]string) (*http.Response, error) {
	return c.PostRequest(ctx, "/v1/completions", body, extraHeaders)
}

func (c *RSClient) GetInternalMetrics() (*http.Response, error) {
	req, err := http.NewRequest("GET", c.BaseURL+"/internal/metrics", nil)
	if err != nil {
		return nil, err
	}
	return c.HTTPClient.Do(req)
}

// PostRequest sends a request to the specified endpoint
func (c *RSClient) PostRequest(ctx context.Context, path string, body interface{}, extraHeaders map[string]string) (*http.Response, error) {
	// 1. Serialize Body
	var bodyReader io.Reader
	if body != nil {
		switch v := body.(type) {
		case []byte:
			// If byte slice, directly use as underlying data
			bodyReader = bytes.NewReader(v)
		case string:
			// If string, convert directly to Reader to avoid memory copy or double serialization
			bodyReader = strings.NewReader(v)
		case io.Reader:
			// Additional support: if caller passes a stream directly (e.g., file or another request body), reuse it
			bodyReader = v
		default:
			// Other types (e.g., Struct, Map), use standard JSON serialization
			jsonBytes, err := json.Marshal(v)
			if err != nil {
				return nil, fmt.Errorf("failed to marshal body: %w", err)
			}
			bodyReader = bytes.NewBuffer(jsonBytes)
		}
	}
	// 2. Build Request
	url := fmt.Sprintf("%s%s", c.BaseURL, path)
	req, err := http.NewRequestWithContext(ctx, "POST", url, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// 3. Set default Headers (OpenAI standard)
	req.Header.Set("Content-Type", "application/json")

	// 4. Inject extra Headers (override or add)
	for k, v := range extraHeaders {
		req.Header.Set(k, v)
	}

	// 5. Send request
	// Note: Body is not read or closed here; the caller (test case) is responsible for closing it
	return c.HTTPClient.Do(req)
}

// -------------------------------------------------------
// 4. Helper Functions
// -------------------------------------------------------

// ParseResponse parses the response Body into the target struct
func ParseResponse(resp *http.Response, target interface{}) error {
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	return json.Unmarshal(body, target)
}

// ReadBodyToString reads Body as string (for debugging or asserting error messages)
func ReadBodyToString(resp *http.Response) string {
	defer resp.Body.Close()
	bodyBytes, _ := io.ReadAll(resp.Body)
	return string(bodyBytes)
}

func NewMockMultimodalContent() []openai.ContentPart {
	return []openai.ContentPart{
		{
			Type:     "text",
			Text:     utils.Pointer[string]("hello"),
			ImageURL: nil,
		},
		{
			Type: "image_url",
			ImageURL: &openai.ChatMessageImageURL{
				URL: "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8/5+hHgAHggJ/PchI7wAAAABJRU5ErkJggg==",
			},
		},
	}
}

func NewBasicChatRequestBody() openai.ChatCompletionRequestV1 {
	return openai.ChatCompletionRequestV1{
		Model: "pangu",
		Messages: []openai.ChatCompletionMessage{{
			Role:    "user",
			Content: "hello",
		},
		},
	}
}

// -------------------------------------------------------
// 5. Health Check Related Client Methods
// -------------------------------------------------------

// MockServerClient encapsulates the client for operating Mock Server
type MockServerClient struct {
	BaseURL    string
	HTTPClient *http.Client
}

// NewMockServerClient creates Mock Server client
func NewMockServerClient(port string) *MockServerClient {
	return &MockServerClient{
		BaseURL: fmt.Sprintf("http://127.0.0.1:%s", port),
		HTTPClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// CheckHealth checks instance health status
func (c *MockServerClient) CheckHealth() (*http.Response, error) {
	req, err := http.NewRequest("GET", c.BaseURL+"/health", nil)
	if err != nil {
		return nil, err
	}
	return c.HTTPClient.Do(req)
}

// MockHealth sets instance to healthy status
func (c *MockServerClient) MockHealth() (*http.Response, error) {
	req, err := http.NewRequest("GET", c.BaseURL+"/mock_health", nil)
	if err != nil {
		return nil, err
	}
	return c.HTTPClient.Do(req)
}

// MockUnHealth sets instance to unhealthy status
func (c *MockServerClient) MockUnHealth() (*http.Response, error) {
	req, err := http.NewRequest("GET", c.BaseURL+"/mock_unhealth", nil)
	if err != nil {
		return nil, err
	}
	return c.HTTPClient.Do(req)
}

// LinkDownBody request body: set link down
type LinkDownBody struct {
	MetaDataList []KVMetaData `json:"meta_data_list"`
}

// KVMetaData KV metadata
type KVMetaData struct {
	Address string `json:"address"`
	Port    string `json:"port"`
}

// LinkDown sets the link of the specified target instance to down status
// targetAddress: target instance address (e.g., "127.0.0.1")
// targetPort: target instance port (e.g., "8082")
func (c *MockServerClient) LinkDown(targetAddress, targetPort string) (*http.Response, error) {
	linkDownBody := LinkDownBody{
		MetaDataList: []KVMetaData{},
	}
	bodyBytes, err := json.Marshal(linkDownBody)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest("POST", c.BaseURL+"/linkdown", bytes.NewBuffer(bodyBytes))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return c.HTTPClient.Do(req)
}

// LinkUp restores link status
func (c *MockServerClient) LinkUp() (*http.Response, error) {
	req, err := http.NewRequest("GET", c.BaseURL+"/linkup", nil)
	if err != nil {
		return nil, err
	}
	return c.HTTPClient.Do(req)
}

// -------------------------------------------------------
// 6. RS Health Check Related Client Methods
// -------------------------------------------------------

// CheckRSHealth checks Route Server health status
func (c *RSClient) CheckRSHealth() (*http.Response, error) {
	req, err := http.NewRequest("GET", c.BaseURL+"/health", nil)
	if err != nil {
		return nil, err
	}
	return c.HTTPClient.Do(req)
}

// ExtractMetricValue reads metrics from HTTP Response stream, precisely matches by metric name and labels, returns generic value
func ExtractMetricValue(resp *http.Response, metricName string, targetLabels map[string]string) (float64, error) {
	if resp == nil || resp.Body == nil {
		return 0, errors.New("invalid http response")
	}
	// Note: Follow Go convention - whoever initiates HTTP request is responsible for closing Body

	// 1. Initialize streaming decoder
	decoder := expfmt.NewDecoder(resp.Body, expfmt.NewFormat(expfmt.TypeTextPlain))
	var family dto.MetricFamily

	// 2. Parse MetricFamily one by one to avoid loading all data into memory
	for {
		err := decoder.Decode(&family)
		if err != nil {
			if err == io.EOF {
				break // Done reading
			}
			return 0, fmt.Errorf("decode metric family failed: %w", err)
		}

		// Quickly skip unrelated metrics
		if family.GetName() != metricName {
			continue
		}

		// 3. After finding target metric, iterate through data points to match labels
		for _, metric := range family.GetMetric() {
			if matchLabels(metric.GetLabel(), targetLabels) {
				// Extract raw value and cast to generic T
				return extractValue(metric), nil
			}
		}

		// The same metric name only appears in one MetricFamily; if labels didn't match in this family, no need to continue reading
		break
	}

	return 0, errors.New("ErrMetricNotFound")
}

// matchLabels label exact matching logic
func matchLabels(metricLabels []*dto.LabelPair, targetLabels map[string]string) bool {
	if len(targetLabels) == 0 {
		return true // If caller passes empty map, default to matching the first value of this metric
	}

	matchedCount := 0
	for _, label := range metricLabels {
		if expectedVal, exists := targetLabels[label.GetName()]; exists && expectedVal == label.GetValue() {
			matchedCount++
		}
	}

	return matchedCount == len(targetLabels)
}

// extractValue extracts value based on specific metric type
func extractValue(metric *dto.Metric) float64 {
	if metric.Gauge != nil {
		return metric.Gauge.GetValue()
	}
	if metric.Counter != nil {
		return metric.Counter.GetValue()
	}
	if metric.Untyped != nil {
		return metric.Untyped.GetValue()
	}
	// For composite types like Histogram and Summary, usually need to query _sum, _count and other suffixed metrics
	// Return 0 here, can be extended for further handling
	return 0
}

// GetWorkerReadiness checks worker's readiness status, returns ready and msg
func (c *RSClient) GetWorkerReadiness() (bool, string, error) {
	req, err := http.NewRequest("GET", c.BaseURL+"/internal/worker/readiness", nil)
	if err != nil {
		return false, "", err
	}
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return false, "", err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	ready := resp.StatusCode == http.StatusOK
	msg := strings.TrimSpace(string(body))
	return ready, msg, nil
}

func TimeoutCtx(ctx context.Context, timeout time.Duration) context.Context {
	retCtx, _ := context.WithTimeout(ctx, timeout)
	return retCtx
}
