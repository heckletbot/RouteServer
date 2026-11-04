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

package route_server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"route-server/cmd/route_server/server"
	"route-server/pkg/common/cluster"
	"route-server/pkg/common/config"
	"route-server/pkg/common/constant"
	"route-server/pkg/common/health"
	"route-server/pkg/common/openai"
	"route-server/pkg/common/utils"
	"route-server/pkg/route-server/instances"
)

// -------------------------------------------------------
// 1. Basic Structure Definitions (OpenAI Protocol)
// -------------------------------------------------------

// RSClient encapsulates the configuration needed for testing
type RSClient struct {
	BaseClient
}

// NewOpenAIClient creates a new client instance
func NewRSClient(baseURL string) *RSClient {
	return &RSClient{
		BaseClient: BaseClient{
			BaseURL: baseURL,
			HTTPClient: &http.Client{
				Timeout: 600 * time.Second,
			},
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

func (c *RSClient) GetInternalMetrics(queryParams ...map[string]string) (*http.Response, error) {
	urlStr := c.BaseURL + "/internal/metrics"
	if len(queryParams) > 0 {
		first := true
		for k, v := range queryParams[0] {
			if first {
				urlStr += "?" + k + "=" + v
				first = false
			} else {
				urlStr += "&" + k + "=" + v
			}
		}
	}
	req, err := http.NewRequest("GET", urlStr, nil)
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
	BaseClient
}

// NewMockServerClient creates Mock Server client
func NewMockServerClient(port string) *MockServerClient {
	return &MockServerClient{
		BaseClient: BaseClient{
			BaseURL: fmt.Sprintf("http://127.0.0.1:%s", port),
			HTTPClient: &http.Client{
				Timeout: 10 * time.Second,
			},
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

func (c *MockServerClient) MockError(statusCode ...int) (*http.Response, error) {
	type MockErrorBody struct {
		RetCode int `json:"code"`
	}
	body := new(MockErrorBody)
	if len(statusCode) != 0 {
		body.RetCode = 500
	} else {
		body.RetCode = 400
	}
	return c.sendRequest(context.Background(), "/mock_error", "POST", body, nil)
}

func (c *MockServerClient) RemoveError() (*http.Response, error) {
	req, err := http.NewRequest("GET", c.BaseURL+"/remove_error", nil)
	if err != nil {
		return nil, err
	}
	return c.HTTPClient.Do(req)
}

// LinkDownBody request body: set link down
type LinkDownBody struct {
	MetaDataList     []KVMetaData `json:"meta_data_list"`
	Version          string       `json:"version,omitempty"`
	TokenBeforeError int          `json:"token_before_error,omitempty"`
}

// KVMetaData KV metadata
type KVMetaData struct {
	Address string `json:"address"`
	Port    string `json:"port"`
}

// LinkDown sets the specified target instance's link to down status
// targetAddress: target instance address (e.g., "127.0.0.1")
// targetPort: target instance port (e.g., "8082")
func (c *MockServerClient) LinkDown(targetAddress, targetPort string, version ...string) (*http.Response, error) {
	linkDownBody := LinkDownBody{
		MetaDataList: []KVMetaData{},
	}
	if len(version) > 0 {
		linkDownBody.Version = version[0]
	}
	if len(version) > 1 {
		if t, err := strconv.Atoi(version[1]); err == nil {
			linkDownBody.TokenBeforeError = t
		}
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
// If label values are empty, returns sum
func ExtractMetricValue(resp *http.Response, metricName string, targetLabels map[string]string) (float64, error) {
	if resp == nil || resp.Body == nil {
		return 0, errors.New("invalid http response")
	}
	// 1. Read all body data into memory
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, fmt.Errorf("read body failed: %w", err)
	}

	defer resp.Body.Close()
	// 2. Important: re-wrap the read data back into resp.Body so external callers can still read it
	resp.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))
	decoder := expfmt.NewDecoder(bytes.NewReader(bodyBytes), expfmt.NewFormat(expfmt.TypeTextPlain))
	var family dto.MetricFamily

	sum := 0.0
	found := false
	// Parse MetricFamily one by one to avoid loading all data into memory
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
		found = true
		// 3. After finding target metric, iterate through data points to match labels
		for _, metric := range family.GetMetric() {
			if len(targetLabels) == 0 {
				sum += extractValue(metric)
				continue
			}
			if matchLabels(metric.GetLabel(), targetLabels) {
				// Extract raw value and cast to generic T
				return extractValue(metric), nil
			}
		}

		// The same metric name only appears in one MetricFamily; if labels didn't match in this family, no need to continue reading
		break
	}

	if found {
		return sum, nil
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

// GetSchedulerLoad get leader scheduler load for all instances
func (c *RSClient) GetSchedulerLoad() (*http.Response, error) {
	req, err := http.NewRequest("GET", c.BaseURL+"/scheduler/load", nil)
	if err != nil {
		return nil, err
	}
	return c.HTTPClient.Do(req)
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

// GetLiveness checks the liveness probe of the RS instance
func (c *RSClient) GetLiveness() (*http.Response, error) {
	req, err := http.NewRequest("GET", c.BaseURL+"/internal/liveness", nil)
	if err != nil {
		return nil, err
	}
	return c.HTTPClient.Do(req)
}

// CheckLeaderReadiness checks the leader/readiness of the RS instance, returns is_leader status
func (c *RSClient) CheckLeaderReadiness() bool {
	req, err := http.NewRequest("GET", c.BaseURL+"/internal/leader/readiness", nil)
	if err != nil {
		return false
	}
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	var leaderResp map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&leaderResp); err != nil {
		return false
	}
	if val, ok := leaderResp["is_leader"]; ok {
		if b, ok := val.(bool); ok {
			return b
		}
	}
	return false
}

func (c *RSClient) GetLeader() (string, int, error) {
	req, err := http.NewRequest("GET", c.BaseURL+"/cluster/leader", nil)
	if err != nil {
		return "", 0, err
	}
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", resp.StatusCode, fmt.Errorf("get leader failed, status: %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	var leaderResp struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &leaderResp); err != nil {
		return "", resp.StatusCode, err
	}
	return leaderResp.ID, resp.StatusCode, nil
}

// GetStatus requests the /status endpoint of the RS instance and returns the parsed status response
func (c *RSClient) GetStatus() (*http.Response, error) {
	req, err := http.NewRequest("GET", c.BaseURL+"/status", nil)
	if err != nil {
		return nil, err
	}
	return c.HTTPClient.Do(req)
}

// GetRSMetric queries RS internal metrics and returns the value of the specified metricName.
// queryParams is optional, used to append query parameters such as map[string]string{"only_rs": "true"}.
func (c *RSClient) GetRSMetric(metricName string, queryParams ...map[string]string) (float64, error) {
	resp, err := c.GetInternalMetrics(queryParams...)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("get metrics failed, status: %d", resp.StatusCode)
	}
	return ExtractMetricValue(resp, metricName, nil)
}

func TimeoutCtx(ctx context.Context, timeout time.Duration) context.Context {
	retCtx, cancel := context.WithTimeout(ctx, timeout)
	go func() {
		<-retCtx.Done()
		cancel()
	}()
	return retCtx
}

// GetMetricsValue fetches the value of a specific metric from /internal/metrics (Prometheus text format).
// If labelFilter is non-empty, only lines containing that label substring are matched.
// Returns the first matching metric value and the raw response body.
func (c *RSClient) GetMetricsValue(metricName, labelFilter string) (float64, string, error) {
	req, err := http.NewRequest("GET", c.BaseURL+"/internal/metrics?only_rs=true", nil)
	if err != nil {
		return 0, "", err
	}
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, "", err
	}
	bodyStr := string(body)
	// Prometheus text format examples:
	//   "rs:num_fallback_scheduled 5"
	//   `rs:infer_requests_failure_status_total{status_code="500",url="/v1/chat/completions"} 3`
	for _, line := range strings.Split(bodyStr, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, metricName) {
			continue
		}
		if labelFilter != "" && !strings.Contains(trimmed, labelFilter) {
			continue
		}
		parts := strings.Fields(trimmed)
		if len(parts) >= 2 {
			val, err := strconv.ParseFloat(parts[len(parts)-1], 64)
			if err != nil {
				return 0, bodyStr, err
			}
			return val, bodyStr, nil
		}
	}
	return 0, bodyStr, fmt.Errorf("metric %s not found in response", metricName)
}

type BaseClient struct {
	BaseURL    string
	HTTPClient *http.Client
}

func (c *BaseClient) sendRequest(ctx context.Context, path, method string, body interface{}, extraHeaders map[string]string) (*http.Response, error) {
	// 1. Serialize Body
	var bodyReader io.Reader
	if body != nil {
		switch v := body.(type) {
		case []byte:
			// If byte slice, use directly as underlying data reader
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
	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// 3. Set default headers (OpenAI standard)
	req.Header.Set("Content-Type", "application/json")

	// 4. Inject extra headers (override or add)
	for k, v := range extraHeaders {
		req.Header.Set(k, v)
	}

	// 5. Send request
	// Note: Body is not read or closed here, left for test cases to handle (Ginkgo/Gomega)
	return c.HTTPClient.Do(req)
}

func (c *RSClient) GetCurrentLeader() (string, error) {
	req, err := http.NewRequest("GET", c.BaseURL+"/cluster/leader", nil)
	if err != nil {
		return "", err
	}
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return "", err
	}

	if resp.StatusCode != 200 {
		return "", fmt.Errorf("HTTP statuscode is %v", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	leaderResp := new(cluster.LeaderResponse)
	if err = json.Unmarshal(body, leaderResp); err != nil {
		return "", err
	}

	return leaderResp.Name, nil
}

func (c *RSClient) InstanceLeave(names []string) error {
	resp, err := c.sendRequest(TimeoutCtx(context.Background(), time.Second*2), "/cluster/instance/leave", "POST",
		&cluster.InstanceLeaveRequest{
			InstanceNames: names,
		}, nil)
	if err != nil {
		return err
	}

	if resp.StatusCode != 200 {
		return fmt.Errorf("HTTP statuscode is %v", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	defer resp.Body.Close()
	loadResp := new(ClusterOKResp)
	if err = json.Unmarshal(body, loadResp); err != nil {
		return err
	}

	if loadResp.Result != "accepted" {
		return fmt.Errorf("%s", loadResp.Result)
	}
	return nil
}

func (c *RSClient) InstanceJoin(instances map[string]*config.Route) error {
	resp, err := c.sendRequest(TimeoutCtx(context.Background(), time.Second*2), "/cluster/instance/join", "POST",
		&cluster.InstanceJoinRequest{
			Instances: instances,
		}, nil)
	if err != nil {
		return err
	}

	if resp.StatusCode != 200 {
		return fmt.Errorf("HTTP statuscode is %v", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	loadResp := new(ClusterOKResp)
	if err = json.Unmarshal(body, loadResp); err != nil {
		return err
	}

	if loadResp.Result != "accepted" {
		return fmt.Errorf("%s", loadResp.Result)
	}
	return nil
}

func (c *RSClient) CheckLoadReset() (bool, error) {
	req, err := http.NewRequest("GET", c.BaseURL+"/scheduler/load", nil)
	if err != nil {
		return false, err
	}
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return false, err
	}

	if resp.StatusCode != 200 {
		return false, fmt.Errorf("HTTP statuscode is %v", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return false, err
	}

	loadResp := new(server.SchedulerLoadResponse)
	if err = json.Unmarshal(body, loadResp); err != nil {
		return false, err
	}

	for _, groupLoad := range loadResp.Loads {
		for _, load := range groupLoad.SLOGroupLoads {
			for _, instanceLoad := range load.InstanceLoads.Available {
				if instanceLoad.LoadVal != 0 {
					return false, fmt.Errorf("ins[%v]'s load is [%v]", instanceLoad.Id, instanceLoad.LoadVal)
				}
			}
		}
	}
	return true, nil
}
func (c *RSClient) InstanceRoutesFromClusterState() (map[string]*config.Route, error) {
	resp, err := c.ClusterState()
	if err != nil {
		return nil, err
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	loadResp := new(cluster.LocalStateResponse)
	if err = json.Unmarshal(body, loadResp); err != nil {
		return nil, err
	}
	return loadResp.State.InstanceRoutes, nil
}

func (c *RSClient) ClusterState() (*http.Response, error) {
	return c.sendRequest(TimeoutCtx(context.Background(), time.Second*2), "/cluster/local/state", "GET",
		"", nil)
}

func (c *RSClient) Status() (*instances.InstanceStatusRsp, error) {
	resp, err := c.sendRequest(TimeoutCtx(context.Background(), time.Second*2), "/status", "GET",
		"", nil)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("status code is %d", resp.StatusCode)
	}

	defer resp.Body.Close()
	instResp := new(instances.InstanceStatusRsp)
	err = json.NewDecoder(resp.Body).Decode(instResp)
	return instResp, err
}

func sendFastInferRequest(client *RSClient, requestId string, timeoutSec int) (*http.Response, error) {
	body := NewBasicChatRequestBody()
	body.MaxTokens = 10
	body.Stream = false
	headers := map[string]string{constant.XRequestId: requestId}
	var timeout time.Duration
	if timeoutSec == 0 {
		timeout = 60 * time.Second
	} else {
		timeout = time.Duration(timeoutSec) * time.Second
	}
	return client.SendChatCompletion(TimeoutCtx(context.Background(), timeout), body, headers)
}

func (c *MockServerClient) RemoveMockError() (*http.Response, error) {
	return c.sendRequest(TimeoutCtx(context.Background(), 3*time.Second), "/remove_error", "GET", "", nil)
}

type ClusterOKResp struct {
	Result string `json:"result"`
}

// GetStatusJson accesses /status to get the corresponding health status info [running, fault, deploying]
func (c *RSClient) GetStatusJson() (*instances.InstanceStatusRsp, error) {
	req, err := http.NewRequest("GET", c.BaseURL+"/status", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var result instances.InstanceStatusRsp
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// GetLinkBlackInstances accesses /link_black_instance to get the linkdown-blacklist
func (c *RSClient) GetLinkBlackInstances() (*health.BlacklistResponse, error) {
	req, err := http.NewRequest("GET", c.BaseURL+"/link_black_instance", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var result health.BlacklistResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}
	return &result, nil
}
