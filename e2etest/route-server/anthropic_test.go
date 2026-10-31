package route_server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/goccy/go-yaml"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gbytes"
	"github.com/onsi/gomega/gexec"

	"route-server/cmd/route_server/server/options"
	"route-server/pkg/common/anthropic"
	commoncfg "route-server/pkg/common/config"
	"route-server/pkg/common/constant"
)

// ============================================================
// constant definitions
// ============================================================

const (
	AnthropicTestContext = "anthropic-test-context"
	AnthropicListenPort  = "30950"
	AnthropicOpsPort     = "30960"
	AnthropicMockPort    = "30951"

	// Anthropic API related constants
	AnthropicDefaultModel     = "claude-3-sonnet"
	AnthropicDefaultMaxTokens = 1024
	AnthropicDefaultSystem    = "You are a helpful assistant"
)

// AnthropicClient is the Anthropic API client
type AnthropicClient struct {
	BaseURL    string
	HTTPClient *http.Client
}

// NewAnthropicClient creates an Anthropic client
func NewAnthropicClient(baseURL string) *AnthropicClient {
	return &AnthropicClient{
		BaseURL: baseURL,
		HTTPClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// SendMessage sends an Anthropic message request
func (c *AnthropicClient) SendMessage(ctx context.Context, body anthropic.AnthropicRequest, headers map[string]string) (*http.Response, error) {
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.BaseURL+"/v1/messages", bytes.NewReader(jsonBody))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "application/json")

	for k, v := range headers {
		req.Header.Set(k, v)
	}

	return c.HTTPClient.Do(req)
}

// ============================================================
// helper functions - create test requests
// ============================================================

// NewBasicAnthropicRequest creates a basic Anthropic request
func NewBasicAnthropicRequest() anthropic.AnthropicRequest {
	return anthropic.AnthropicRequest{
		Model:     AnthropicDefaultModel,
		MaxTokens: AnthropicDefaultMaxTokens,
		Messages: []anthropic.AnthropicMessage{
			{Role: "user", Content: []anthropic.AnthropicContent{
				{Type: "text", Text: "Hello, how are you?"},
			}},
		},
	}
}

// NewAnthropicRequestWithSystem creates a request with a system prompt
func NewAnthropicRequestWithSystem(system string) anthropic.AnthropicRequest {
	req := NewBasicAnthropicRequest()
	req.System = system
	return req
}

// NewAnthropicStreamRequest creates a streaming request
func NewAnthropicStreamRequest() anthropic.AnthropicRequest {
	req := NewBasicAnthropicRequest()
	req.Stream = true
	return req
}

// NewAnthropicRequestWithMaxTokens creates a request with a specified max_tokens
func NewAnthropicRequestWithMaxTokens(maxTokens int) anthropic.AnthropicRequest {
	req := NewBasicAnthropicRequest()
	req.MaxTokens = maxTokens
	return req
}

// ============================================================
// test suite
// ============================================================

var _ = Describe("Anthropic API tests", Ordered, func() {
	var (
		session           *gexec.Session
		mockServers       []*gexec.Session
		client            *AnthropicClient
		configPath        string
		mockPrefillClient *MockServerClient
	)

	BeforeAll(func() {
		EnsurePorts([]string{AnthropicListenPort, AnthropicOpsPort, AnthropicMockPort})

		deployPath := filepath.Join(projectRoot, "deploy")
		cfgDirs := []string{filepath.Join(projectRoot, "e2etest", "environment", AnthropicTestContext)}

		By("copy pipelines config")
		copyPipelinesToDirs(deployPath, cfgDirs)

		configPath = filepath.Join(projectRoot, "e2etest", "environment", AnthropicTestContext, "config")

		By("modify route-server temp config")
		var cfg commoncfg.Config
		templateContent, err := os.ReadFile(filepath.Join(deployPath, ROUTE_SERVER_TEMPLATE))
		Expect(yaml.Unmarshal(templateContent, &cfg)).To(BeNil())

		cfg.InstanceManagerCfg.HealthCheckConfig.RestHealthCommonConfig.Interval = 2000
		cfg.InstanceManagerCfg.HealthCheckConfig.UnavailableCheckersCfg.RestHealthCfg.ConsecutiveTime = 3
		cfg.InstanceManagerCfg.HealthCheckConfig.AvailableCheckersCfg.LinkBlacklistConfig.LinkBlockWindowsMs = 100
		cfg.InstanceManagerCfg.HealthCheckConfig.AvailableCheckersCfg.LinkBlacklistConfig.LinkBlockTagValidTimeMs = 10000
		cfg.MetaCleanupCfg = &commoncfg.MetaCleanupConfig{
			MaxInstanceDownTimeSec: 30,
			MaxWorkerDownTimeSec:   30,
		}

		outData, err := yaml.Marshal(&cfg)
		Expect(err).To(BeNil())
		tempTemplatePath := filepath.Join(projectRoot, "e2etest", "environment", AnthropicTestContext, TEMP_TEMPLATE)
		Expect(os.WriteFile(tempTemplatePath, outData, 0644)).To(BeNil())

		By("gen route-server config")
		toolParam := DisableHTTPSConfig([]string{
			"-template", tempTemplatePath,
			"-host", "127.0.0.1",
			"-target", filepath.Join(projectRoot, "e2etest", "environment", AnthropicTestContext),
			"-infer-mode", "PD",
			"-prefills", fmt.Sprintf("prefill1#127.0.0.1:%s", AnthropicMockPort),
			"-decodes", fmt.Sprintf("decode1#127.0.0.1:%s", AnthropicMockPort),
			"-name", "route-server-anthropic-test",
			"-remote-scheduler-type", "disable",
			"-port", AnthropicListenPort,
			"-mode", "singleton",
			"-infer-module-version", "pipeline",
		})
		Expect(exec.Command(configToolPath, toolParam...).Run()).To(BeNil())

		By("modify recover health config - set consecutive-time to 1")
		configFile := filepath.Join(configPath, "route-server.yaml")
		Expect(InjectRecoverHealthConfigToYaml(configFile, 1)).To(BeNil())

		Expect(os.Setenv(constant.EnvServerConfigPath, configPath)).To(BeNil())
		Expect(options.InitConfig()).To(BeNil())

		By("cp metrics and anthropic error type config")
		copyConfigFilesToDirs(deployPath, cfgDirs, "metrics_config_vllm_0.11.0.yaml", "anthropic_error_type_config.yaml")

		By("start anthropic mock vllm")
		for _, instance := range options.GetConfig().InstanceManagerCfg.RouteTable.InstanceRoutes {
			params := []string{
				"-addr", instance.Ip,
				"-port", fmt.Sprintf("%d", instance.Port),
			}
			command := exec.Command(mockVLLMPath, params...)
			mockSession, err := gexec.Start(command, io.Discard, os.Stderr)
			Expect(err).NotTo(HaveOccurred())
			mockServers = append(mockServers, mockSession)
		}

		By("start route-server")
		command := exec.Command(rsPath)
		session, err = gexec.Start(command, io.Discard, os.Stderr)
		Expect(err).NotTo(HaveOccurred())

		By("wait for readiness")
		Eventually(session.Out, 10*time.Second).Should(gbytes.Say("Start HTTP server on"))
		time.Sleep(2 * time.Second)

		client = NewAnthropicClient("http://localhost:" + AnthropicListenPort)
		mockPrefillClient = NewMockServerClient(AnthropicMockPort)
	})

	AfterAll(func() {
		By("destroy rs")
		if session != nil {
			session.Kill()
			session.Wait()
		}
		By("destroy mock vllm")
		for _, mock := range mockServers {
			mock.Kill()
			mock.Wait()
		}
	})

	BeforeEach(func() {
		// clear the log buffer
		Eventually(session.Out).Should(gbytes.Say(".*"))
	})

	// ============================================================
	// Test case 1: basic functional test
	// ============================================================
	Context("Basic functional test", func() {

		It("stream request", func() {
			reqId := "test-anthropic-stream-basic"
			body := NewAnthropicStreamRequest()

			resp, err := client.SendMessage(context.Background(), body, map[string]string{
				constant.XRequestId: reqId,
			})
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()

			Expect(resp.StatusCode).To(Equal(http.StatusOK))

			bodyBytes, err := io.ReadAll(resp.Body)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(bodyBytes)).To(ContainSubstring("data:"))

			By("verify the first three SSE events")
			// first event: message_start
			Expect(string(bodyBytes)).To(ContainSubstring(`"type":"message_start"`))
			// second event: content_block_start
			Expect(string(bodyBytes)).To(ContainSubstring(`"type":"content_block_start"`))
			// third event: content_block_delta (first token, text="test_1")
			Expect(string(bodyBytes)).To(ContainSubstring(`"type":"content_block_delta"`))
			Expect(string(bodyBytes)).To(ContainSubstring(`"text":"test_1"`))

			By("verify perfmetrics: ttft and tpot are not 0")
			// verify the log contains CompletionMetric, and ttft and tpot are not 0
			// log format: CompletionMetric: {'trace_id': '...', 'num_prompt_tokens': ..., 'num_output_tokens': ..., 'ttft': ..., 'tpot': ..., 'latency': ...}
			expectedRegexp := fmt.Sprintf(`CompletionMetric.*'trace_id'.*%s.*'ttft': [1-9]`, reqId)
			Eventually(session.Out, 10*time.Second).Should(gbytes.Say(expectedRegexp))
		})

		It("request with system prompt", func() {
			body := NewAnthropicRequestWithSystem(AnthropicDefaultSystem)

			resp, err := client.SendMessage(context.Background(), body, nil)
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()

			Expect(resp.StatusCode).To(Equal(http.StatusOK))

			var result anthropic.AnthropicResponse
			err = ParseResponse(resp, &result)
			Expect(err).NotTo(HaveOccurred())

			By("verify adapted Anthropic response fields")
			Expect(result.ID).NotTo(BeEmpty())
			Expect(result.Type).To(Equal("message"))
			Expect(result.Role).To(Equal("assistant"))
			Expect(result.Model).To(Equal("deepseek-r1"))

			By("verify content is non-empty and contains a text block")
			Expect(result.Content).NotTo(BeEmpty())
			Expect(result.Content[0].Type).To(Equal("text"))
			Expect(result.Content[0].Text).To(Equal("test"))

			By("verify stop_reason is passed through as end_turn")
			Expect(result.StopReason).To(Equal("end_turn"))

			By("verify usage fields")
			Expect(result.Usage.InputTokens).To(Equal(2000))
			Expect(result.Usage.OutputTokens).To(Equal(AnthropicDefaultMaxTokens))
		})

	})

	// ============================================================
	// parameter validation tests
	// ============================================================
	Context("parameter validation tests", func() {

		It("missing max_tokens", func() {
			body := anthropic.AnthropicRequest{
				Model: AnthropicDefaultModel,
				Messages: []anthropic.AnthropicMessage{
					{Role: "user", Content: []anthropic.AnthropicContent{{Type: "text", Text: "Hello"}}},
				},
			}

			resp, err := client.SendMessage(context.Background(), body, nil)
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()

			Expect(resp.StatusCode).To(Equal(http.StatusBadRequest))
		})

		It("max_tokens is 0", func() {
			body := NewAnthropicRequestWithMaxTokens(0)

			resp, err := client.SendMessage(context.Background(), body, nil)
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()

			Expect(resp.StatusCode).To(Equal(http.StatusBadRequest))
		})

		It("max_tokens is negative", func() {
			body := NewAnthropicRequestWithMaxTokens(-1)

			resp, err := client.SendMessage(context.Background(), body, nil)
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()

			Expect(resp.StatusCode).To(Equal(http.StatusBadRequest))
		})

	})

	// ============================================================
	// error handling tests
	// ============================================================
	Context("error handling tests", func() {

		It("invalid API path", func() {
			// use an invalid path
			invalidClient := NewAnthropicClient("http://localhost:" + AnthropicListenPort + "/invalid")

			body := NewBasicAnthropicRequest()
			resp, err := invalidClient.SendMessage(context.Background(), body, nil)
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()

			Expect(resp.StatusCode).To(Equal(http.StatusNotFound))
		})

		It("non-stream request - returns Anthropic error format when mock returns 500", func() {
			By("configure the mock instance to return a 500 error")
			resp, err := mockPrefillClient.MockError(500)
			Expect(err).NotTo(HaveOccurred())
			resp.Body.Close()
			DeferCleanup(func() {
				mockPrefillClient.RemoveError()
			})

			By("send a non-stream request and verify the response")
			reqId := "test-anthropic-nonstream-err"
			body := NewBasicAnthropicRequest()
			httpResp, err := client.SendMessage(context.Background(), body, map[string]string{
				constant.XRequestId: reqId,
			})
			Expect(err).NotTo(HaveOccurred())
			defer httpResp.Body.Close()

			Expect(httpResp.StatusCode).To(Equal(http.StatusInternalServerError))

			var errResp anthropic.AnthropicError
			Expect(ParseResponse(httpResp, &errResp)).NotTo(HaveOccurred())
			Expect(errResp.Type).To(Equal("error"))
			Expect(errResp.Error.Type).To(Equal("internal_error"))
			Expect(errResp.Error.Message).To(ContainSubstring("mock error happened"))

			By("verify failure_type = engine, err_stage = Prefill in the log")
			expectedRegexp := fmt.Sprintf(`request_id.*%s.*finish_reason = unknown.*err_stage = Prefill.*failure_type = engine`, reqId)
			Eventually(session.Out, 10*time.Second).Should(gbytes.Say(expectedRegexp))
		})

		It("stream request - returns Anthropic error event when mock returns 500", func() {
			By("configure the mock instance to return a 500 error")
			resp, err := mockPrefillClient.MockError(500)
			Expect(err).NotTo(HaveOccurred())
			resp.Body.Close()
			DeferCleanup(func() {
				mockPrefillClient.RemoveError()
			})

			By("send a stream request and verify the response")
			reqId := "test-anthropic-stream-err"
			body := NewAnthropicStreamRequest()
			httpResp, err := client.SendMessage(context.Background(), body, map[string]string{
				constant.XRequestId: reqId,
			})
			Expect(err).NotTo(HaveOccurred())
			defer httpResp.Body.Close()

			Expect(httpResp.StatusCode).To(Equal(http.StatusInternalServerError))

			bodyBytes, err := io.ReadAll(httpResp.Body)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(bodyBytes)).To(ContainSubstring(`"type":"error"`))
			Expect(string(bodyBytes)).To(ContainSubstring(`"type":"internal_error"`))

			By("verify failure_type = engine, err_stage = Prefill in the log")
			expectedRegexp := fmt.Sprintf(`request_id.*%s.*finish_reason = unknown.*err_stage = Prefill.*failure_type = engine`, reqId)
			Eventually(session.Out, 10*time.Second).Should(gbytes.Say(expectedRegexp))
		})
	})

	// ============================================================
	// concurrency tests
	// ============================================================
	Context("concurrency tests", func() {

		It("send multiple requests concurrently", func() {
			var wg sync.WaitGroup
			errChan := make(chan error, 20)
			concurrency := 10

			for i := 0; i < concurrency; i++ {
				wg.Add(1)
				go func(idx int) {
					defer wg.Done()
					body := NewBasicAnthropicRequest()
					headers := map[string]string{
						constant.XRequestId: fmt.Sprintf("concurrent-test-%d", idx),
					}
					resp, err := client.SendMessage(context.Background(), body, headers)
					if err != nil {
						errChan <- err
						return
					}
					defer resp.Body.Close()
					if resp.StatusCode != http.StatusOK {
						errChan <- fmt.Errorf("request %d status: %d", idx, resp.StatusCode)
					}
				}(i)
			}

			wg.Wait()
			close(errChan)

			var errors []error
			for err := range errChan {
				errors = append(errors, err)
			}
			Expect(errors).To(BeEmpty())
		})
	})

	// ============================================================
	// response format verification tests
	// ============================================================
	Context("response format verification tests", func() {

		It("stream response contains the correct SSE format", func() {
			body := NewAnthropicStreamRequest()

			resp, err := client.SendMessage(context.Background(), body, nil)
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()

			Expect(resp.StatusCode).To(Equal(http.StatusOK))

			bodyBytes, err := io.ReadAll(resp.Body)
			Expect(err).NotTo(HaveOccurred())

			lines := bytes.Split(bodyBytes, []byte("\n"))
			hasEvent := false
			hasData := false

			for _, line := range lines {
				if bytes.HasPrefix(line, []byte("event: ")) {
					hasEvent = true
				}
				if bytes.HasPrefix(line, []byte("data: ")) {
					hasData = true
				}
			}

			Expect(hasEvent).To(BeTrue(), "should contain an event: line")
			Expect(hasData).To(BeTrue(), "should contain a data: line")
		})
	})
})
