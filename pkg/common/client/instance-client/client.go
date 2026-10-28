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

package instance_client

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"infer-dp-scheduler/pkg/common/logger"

	"infer-dp-scheduler/pkg/common/config"
	"infer-dp-scheduler/pkg/common/constant"
	"infer-dp-scheduler/pkg/common/errs"
	http2 "infer-dp-scheduler/pkg/common/http"
	"infer-dp-scheduler/pkg/common/openai"
	"infer-dp-scheduler/pkg/common/utils"

	"github.com/gin-gonic/gin"
	"golang.org/x/net/context"
)

var DefaultTimeout = time.Duration(100) * time.Millisecond

type ClientConfig struct {
	HttpCfg      config.HttpConfig
	CheckTimeout time.Duration
}

type GenerateRequest struct {
	*http.Request
}

type GenerateResponse struct {
	*http.Response
}

type InstanceClient interface {
	HealthCheck() (bool, int, error)
	Generate(ctx context.Context, header http.Header, path string, body io.Reader, instanceId string, stage string, reqId string, isMessages bool) (*http.Response, *errs.RSError, *utils.InstanceFailureDetails)
	Metrics() ([]byte, error)
	HandlerBroadCastRequest(ctx *gin.Context, broadcastRequest *openai.BroadcastRequest, id string) openai.InstanceResponse
	GetEndpoint() string
	ProxyHttp(req *http.Request) *http.Response
	ModelName() (string, error)
}

type VllmClient struct {
	endpoint        string
	longServeClient *http2.Client
	healthClient    *http2.Client
	cfg             *ClientConfig
	proxy           *httputil.ReverseProxy
}

func (v *VllmClient) Metrics() ([]byte, error) {
	timeout := DefaultTimeout
	if v.cfg != nil && v.cfg.CheckTimeout != 0 {
		timeout = v.cfg.CheckTimeout
	}
	timeoutCtx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	req, err := http2.BuildHttpRequest(timeoutCtx, http.MethodGet, v.endpoint+"/metrics", nil, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to build request caused by: %w", err)
	}
	resp, err := v.healthClient.DoRequest(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch metrics caused by: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch metrics Status Code: %d", resp.StatusCode)
	}
	bodyBytes, err := io.ReadAll(resp.Body)
	return bodyBytes, nil
}

func (v *VllmClient) ModelName() (string, error) {
	timeout := DefaultTimeout
	if v.cfg != nil && v.cfg.CheckTimeout != 0 {
		timeout = v.cfg.CheckTimeout
	}
	timeoutCtx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req, err := http2.BuildHttpRequest(timeoutCtx, http.MethodGet, v.endpoint+"/v1/models", nil, nil)
	if err != nil {
		return "", fmt.Errorf("failed to build request caused by: %w", err)
	}
	resp, err := v.healthClient.DoRequest(req)
	if err != nil {
		return "", fmt.Errorf("failed to fetch model name caused by: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fetch model name Status Code: %d", resp.StatusCode)
	}
	bodyBytes, err := io.ReadAll(resp.Body)
	var result response
	err = utils.SonicApi.Unmarshal(bodyBytes, &result)
	if err != nil {
		return "", fmt.Errorf("fail to unmarshal model name request response body: %w", err)
	}
	if len(result.Data) == 0 {
		return "", fmt.Errorf("fail to get model name from response")
	}
	modelName := result.Data[0].ID
	if modelName == "" {
		return "", fmt.Errorf("fail to get model name from response")
	}

	return modelName, nil
}

func (v *VllmClient) HealthCheck() (bool, int, error) {
	timeout := DefaultTimeout
	if v.cfg != nil && v.cfg.CheckTimeout != 0 {
		timeout = v.cfg.CheckTimeout
	}
	timeoutCtx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	req, err := http2.BuildHttpRequest(timeoutCtx, http.MethodGet, v.endpoint+"/health", nil, nil)
	if err != nil {
		return false, 0, fmt.Errorf("build health check request failed: %w", err)
	}
	if constant.ServerName != "" {
		req.Header.Set(constant.XRouterId, constant.ServerName)
	}

	resp, err := v.healthClient.DoRequest(req)
	if err != nil {
		return false, 0, fmt.Errorf("health check failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return false, resp.StatusCode, fmt.Errorf("health check status code: %d, body = %s", resp.StatusCode, string(bodyBytes))
	}

	return true, resp.StatusCode, nil
}

// isRetryableError reports whether an error is a transient connection failure
// that is safe to retry: connection errors, idle-connection resets or EOFs.
func isRetryableError(err error) bool {
	if err == nil {
		return false
	}
	if utils.DetailsFromError(0, err).IsConnectionErr {
		return true
	}
	return isIdleConnResetError(err) || isEofError(err)
}

func isIdleConnResetError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "server closed idle connection")
}

func isEofError(err error) bool {
	if err == nil {
		return false
	}
	return strings.HasSuffix(strings.ToLower(err.Error()), "eof")
}

func (v *VllmClient) Generate(ctx context.Context, header http.Header, path string, body io.Reader, instanceId string, stage string, reqId string, isMessages bool) (*http.Response, *errs.RSError, *utils.InstanceFailureDetails) {
	var bodyBytes []byte
	if body != nil {
		var readErr error
		bodyBytes, readErr = io.ReadAll(body)
		if readErr != nil {
			rsErr := errs.NewRSMaError(errs.InternalErr.BuildArgs(
				fmt.Sprintf("read request body failed: %v", readErr)), stage)
			return nil, rsErr, utils.DetailsFromError(0, readErr)
		}
	}
	req, err := http2.BuildHttpRequest(ctx, http.MethodPost, v.endpoint+path, bytes.NewReader(bodyBytes), header)
	var rsErr *errs.RSError
	if err != nil {
		rsErr = errs.NewRSMaError(errs.InternalErr.BuildArgs(
			fmt.Sprintf("build http request failed: %v", err)), stage)
		return nil, rsErr, utils.DetailsFromError(0, nil)
	}
	maxRetry := 0
	if v.cfg != nil && v.cfg.HttpCfg.ConnectionRetry > 0 {
		maxRetry = v.cfg.HttpCfg.ConnectionRetry
	}
	allRetryIsIdleConnReset := true
	resp, err := v.longServeClient.DoRequest(req)
	for retry := 0; retry < maxRetry && err != nil && isRetryableError(err); retry++ {
		if !isIdleConnResetError(err) {
			allRetryIsIdleConnReset = false
		}
		logger.Log().Warnf("connection error on attempt %d/%d for request [%s] instance %s: %v, retrying", retry+1, maxRetry, reqId, instanceId, err)
		req, err = http2.BuildHttpRequest(ctx, http.MethodPost, v.endpoint+path, bytes.NewReader(bodyBytes), header)
		if err != nil {
			rsErr = errs.NewRSMaError(errs.InternalErr.BuildArgs(
				fmt.Sprintf("build http request failed: %v", err)), stage)
			return nil, rsErr, utils.DetailsFromError(0, nil)
		}
		resp, err = v.longServeClient.DoRequest(req)
	}
	if err != nil {
		resultStage := stage
		if allRetryIsIdleConnReset && isIdleConnResetError(err) {
			resultStage = ""
		}
		rsErr = errs.NewRSMaError(utils.MaErrorFromError(err), resultStage)
		if isEofError(err) {
			return nil, rsErr, utils.DetailsFromEofError(err)
		}
		return nil, rsErr, utils.DetailsFromError(0, err)
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		bodyBytes, err := io.ReadAll(resp.Body)
		if err != nil {
			rsErr = errs.NewRSMaError(errs.BadGatewayErr.BuildArgs(
				fmt.Sprintf("failed to read resp body, err = %v", err)), stage)
			return nil, rsErr, utils.DetailsFromError(resp.StatusCode, err)
		} else {
			rsErr = errs.NewRSEngineError(&errs.EngineError{
				EngineErrBody: bodyBytes,
				EngineStatus:  resp.StatusCode,
				InstanceID:    instanceId,
			}, stage)
			details, _ := utils.DetailsFromNonStreamResponse(resp.StatusCode, bodyBytes, isMessages)
			rsErr.FailureType = details.TypeFromDetails()
			return nil, rsErr, details
		}
	}

	return resp, rsErr, nil
}

func (v *VllmClient) GetEndpoint() string {
	return v.endpoint
}

func NewClient(clientType string, route config.Route, clientCfg ClientConfig) (InstanceClient, error) {
	applyDefaultClientCfg(&clientCfg)

	switch clientType {
	case constant.ClientTypeVllm:
		if !utils.ValidRole(route.Role) {
			return nil, fmt.Errorf("invalid role for vllm client: %s, must be 'prefill' or 'decode'", route.Role)
		}

		scheme := "http"
		if clientCfg.HttpCfg.TLSConfig != nil && clientCfg.HttpCfg.TLSConfig.EnableHTTPS {
			scheme = "https"
		}

		httpClient, err := http2.NewClient(&clientCfg.HttpCfg, true, constant.ClientKeyPasswordEnv)
		if err != nil {
			return nil, fmt.Errorf("failed to create http longServeClient: %w", err)
		}
		healthClient, err := http2.NewClient(&clientCfg.HttpCfg, false, constant.ClientKeyPasswordEnv)
		if err != nil {
			return nil, fmt.Errorf("failed to create http healthCheck client: %w", err)
		}

		client := &VllmClient{
			endpoint:        fmt.Sprintf("%s://%s:%d", scheme, route.Ip, route.Port),
			longServeClient: httpClient,
			healthClient:    healthClient,
			cfg:             &clientCfg,
		}
		err = client.NewClientProxy()
		if err != nil {
			return nil, err
		}
		return client, nil
	default:
		return nil, fmt.Errorf("unknown client type: %s", clientType)
	}
}

func (v *VllmClient) NewClientProxy() error {
	target, err := url.Parse(v.endpoint)
	if err != nil {
		return fmt.Errorf("failed to new client proxy, err = %w", err)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	// Configure TLS transport for proxy if the endpoint is HTTPS
	if target.Scheme == "https" {
		transport := &http.Transport{
			TLSClientConfig: v.longServeClient.Client.Transport.(*http.Transport).TLSClientConfig,
		}
		proxy.Transport = transport
	}
	v.proxy = proxy
	return nil
}

func applyDefaultClientCfg(clientCfg *ClientConfig) {
	if clientCfg.CheckTimeout == 0 {
		clientCfg.CheckTimeout = DefaultTimeout
	}
}

func (v *VllmClient) HandlerBroadCastRequest(ctx *gin.Context, broadcastRequest *openai.BroadcastRequest, id string) openai.InstanceResponse {
	req, err := http.NewRequestWithContext(ctx, broadcastRequest.Method, broadcastRequest.URI, bytes.NewBuffer(broadcastRequest.Body))
	req.Header = broadcastRequest.Headers
	if err != nil {
		response := constructInstanceResp(id, http.StatusNotFound, err.Error(), constant.FailedStatus)
		return response
	}
	resp := v.ProxyHttp(req)
	// Check response status code
	if resp.StatusCode != http.StatusOK {
		// Read response body as error details
		body, readErr := io.ReadAll(resp.Body)
		err := resp.Body.Close()
		if err != nil {
			logger.Log().Errorf("fail to close response body, err = %v", err)
		}
		if readErr != nil {
			body = []byte(fmt.Sprintf("[failed to read response body: %v]", readErr))
		}
		errString := fmt.Sprintf("unexpected status code %d: %s", resp.StatusCode, string(body))
		response := constructInstanceResp(id, resp.StatusCode, errString, constant.FailedStatus)
		return response
	}
	var buf bytes.Buffer
	_, err = io.Copy(&buf, resp.Body)
	if err != nil {
		errString := fmt.Sprintf("read body failed: %v", err)
		response := constructInstanceResp(id, http.StatusInternalServerError, errString, constant.FailedStatus)
		return response
	}
	err = resp.Body.Close()
	if err != nil {
		logger.Log().Errorf("fail to close response body, err = %v", err)
	}
	response := constructInstanceResp(id, resp.StatusCode, buf.String(), constant.SuccessStatus)
	return response
}

func constructInstanceResp(id string, statusCode int, message string, status string) (response openai.InstanceResponse) {
	response = openai.InstanceResponse{
		InstanceName: id,
		StatusCode:   statusCode,
	}
	if statusCode != http.StatusOK {
		response.Error = message
	} else {
		response.Result = message
	}
	return response
}

type captureResponseWriter struct {
	statusCode int
	headers    http.Header
	body       *bytes.Buffer
}

func (w *captureResponseWriter) Header() http.Header {
	return w.headers
}

func (w *captureResponseWriter) WriteHeader(statusCode int) {
	w.statusCode = statusCode
}

func (w *captureResponseWriter) Write(data []byte) (int, error) {
	return w.body.Write(data)
}

func (v *VllmClient) ProxyHttp(req *http.Request) *http.Response {
	crw := &captureResponseWriter{
		headers: make(http.Header),
		body:    new(bytes.Buffer),
	}
	v.proxy.ServeHTTP(crw, req)
	resp := &http.Response{
		StatusCode: crw.statusCode,
		Header:     crw.headers,
		Body:       io.NopCloser(crw.body),
	}
	return resp
}

// Response defines the struct matching the JSON structure
type response struct {
	Data []item `json:"data"` // Maps the top-level data array
}

// Item defines the struct for each element in the array
type item struct {
	ID string `json:"id"` // Maps the id field of each element
}
