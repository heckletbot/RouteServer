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
	"strings"
	"time"

	"github.com/goccy/go-yaml"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gbytes"
	"github.com/onsi/gomega/gexec"
	"route-server/cmd/route_server/server/options"
	commoncfg "route-server/pkg/common/config"
	"route-server/pkg/common/constant"
	"route-server/pkg/common/errs"
	"route-server/pkg/common/health"
	"route-server/pkg/common/openai"
)

const (
	PDTestContext          = "pd-test-context"
	DefaultConsecutiveTime = 1
	pdListenPort           = "30900"
	pdOpsPort              = "30910"
	pdPrefillPort          = "30901"
	pdDecodePort           = "30902"
)

var _ = Describe(PDTestContext, Ordered, func() {
	var (
		session     *gexec.Session
		err         error
		mockServers []*gexec.Session
	)

	BeforeAll(func() {
		EnsurePorts([]string{pdListenPort, pdOpsPort, pdPrefillPort, pdDecodePort})
		By("copy pipelines config")
		configPath := filepath.Join(projectRoot, "e2etest", "environment", PDTestContext, "config")
		Expect(os.MkdirAll(filepath.Join(configPath, "pipelines"), 0755)).To(BeNil())

		deployPath := filepath.Join(projectRoot, "deploy")
		Expect(copyDir(filepath.Join(deployPath, "pipelines"), filepath.Join(configPath, "pipelines"))).To(BeNil())

		By("modify route-server temp config")
		var cfg commoncfg.Config
		templateContent, err := os.ReadFile(filepath.Join(deployPath, ROUTE_SERVER_TEMPLATE))
		Expect(yaml.Unmarshal(templateContent, &cfg)).To(BeNil())
		cfg.ServerConfig.TLSConfig = &commoncfg.TLSCertificateConfig{
			EnableHTTPS: false,
			EnableMTLS:  false,
		}
		cfg.ServerConfig.OpsTLSConfig = &commoncfg.TLSCertificateConfig{
			EnableHTTPS: false,
			EnableMTLS:  false,
		}
		cfg.InstanceManagerCfg.HealthCheckConfig.RestHealthCommonConfig.Interval = 2000
		cfg.InstanceManagerCfg.HealthCheckConfig.UnavailableCheckersCfg.RestHealthCfg.ConsecutiveTime = 3
		cfg.InstanceManagerCfg.HealthCheckConfig.AvailableCheckersCfg.LinkBlacklistConfig.LinkBlockWindowsMs = 100
		cfg.InstanceManagerCfg.HealthCheckConfig.AvailableCheckersCfg.LinkBlacklistConfig.LinkBlockTagValidTimeMs = 5000
		cfg.MetaCleanupCfg = &commoncfg.MetaCleanupConfig{
			MaxInstanceDownTimeSec: 30,
			MaxWorkerDownTimeSec:   30,
		}
		outData, err := yaml.Marshal(&cfg)
		Expect(err).To(BeNil())
		tempTemplatePath := filepath.Join(projectRoot, "e2etest", "environment", PDTestContext, TEMP_TEMPLATE)
		Expect(os.WriteFile(tempTemplatePath, outData, 0644)).To(BeNil())

		By("gen route-server config")
		toolParam := DisableHTTPSConfig([]string{
			"-template", tempTemplatePath,
			"-host", "127.0.0.1",
			"-target", filepath.Join(projectRoot, "e2etest", "environment", PDTestContext),
			"-infer-mode", "PD",
			"-prefills", fmt.Sprintf("prefill1#127.0.0.1:%s", pdPrefillPort),
			"-decodes", fmt.Sprintf("decode1#127.0.0.1:%s", pdDecodePort),
			"-name", "route-server-pd-test",
			"-remote-scheduler-type", "disable",
			"-port", pdListenPort,
			"-mode", "singleton",
			"-infer-module-version", "pipeline",
		})
		Expect(exec.Command(configToolPath, toolParam...).Run()).To(BeNil())

		By("modify recover health config - set consecutive-time to 1")
		configFile := filepath.Join(configPath, "route-server.yaml")
		Expect(InjectRecoverHealthConfigToYaml(configFile, DefaultConsecutiveTime)).To(BeNil())

		Expect(os.Setenv(constant.EnvServerConfigPath, configPath)).To(BeNil())
		Expect(options.InitConfig()).To(BeNil())

		By("cp metrics config")
		metricsCollectorConfigFileName := options.GetConfig().MetricsCfg.MetricsCollectorConfigFileName
		anthropicErrorTypeConfigFileName := options.GetConfig().ServerConfig.AnthropicErrorTypeConfigFileName
		copyConfigFilesToDirs(deployPath, []string{filepath.Join(projectRoot, "e2etest", "environment", PDTestContext)}, metricsCollectorConfigFileName, anthropicErrorTypeConfigFileName)

		By("start pd mock vllm")
		for _, instance := range options.GetConfig().InstanceManagerCfg.RouteTable.InstanceRoutes {
			var params []string
			params = append(params,
				"-addr", instance.Ip,
				"-port", fmt.Sprintf("%d", instance.Port))
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
		Eventually(session.Out, 5*time.Second).Should(gbytes.Say("Start HTTP server on"))
		time.Sleep(2 * time.Second)
	})

	AfterAll(func() {
		By("destroy rs")
		session.Kill()
		session.Wait()
		By("destroy mock vllm")
		for _, mock := range mockServers {
			mock.Kill()
			mock.Wait()
		}
	})

	BeforeEach(func() {
		// before the new test logic starts, consume all current content first
		// matching ".*" moves the read pointer to the end of the current buffer
		Eventually(session.Out).Should(gbytes.Say(".*"))
	})

	Context("Preliminary Test ", func() {
		var (
			clientRs *RSClient

			spanId       string
			reqBody      openai.ChatCompletionRequestV1
			extraHeaders map[string]string

			resp *http.Response
		)
		BeforeEach(func() {
			clientRs = NewRSClient("http://localhost:" + pdListenPort)
			extraHeaders = make(map[string]string)
			spanId = uuid.NewString()
			extraHeaders[constant.HeaderXSpanId] = spanId
			reqBody = openai.ChatCompletionRequestV1{
				Model: "pangu",
				Messages: []openai.ChatCompletionMessage{{
					Role:    "user",
					Content: "hello",
				},
				},
			}
		})

		It("basic test", func() {
			resp, err = clientRs.SendChatCompletion(TimeoutCtx(context.Background(), 30*time.Second), reqBody, extraHeaders)
			Expect(err).NotTo(HaveOccurred())
			Expect(resp.StatusCode).To(Equal(http.StatusOK))

			var result openai.ChatCompletionResponse
			err := ParseResponse(resp, &result)
			Expect(err).NotTo(HaveOccurred())
			Expect(result.Choices).NotTo(BeEmpty())

		})

		It("invalid body request", func() {
			resp, err = clientRs.SendChatCompletion(TimeoutCtx(context.Background(), 30*time.Second), InvalidRequestBody, extraHeaders)
			Expect(err).NotTo(HaveOccurred())
			Expect(resp.StatusCode).To(Equal(http.StatusBadRequest))

			var result errs.ErrorBody
			err := ParseResponse(resp, &result)
			Expect(err).NotTo(HaveOccurred())
			Expect(result.Code).Should(ContainSubstring(errs.ModelArtsStatusInvalidReq))

		})

	})

	// Request ID Rewrite tests
	// Each TC covers the /v1/chat/completions and /v1/completions endpoints, plus non-streaming and streaming requests
	Context("Request ID Rewrite Tests", func() {
		var clientRs *RSClient

		BeforeEach(func() {
			clientRs = NewRSClient("http://localhost:" + pdListenPort)
		})

		// ============================================
		// TC1: Header X-Request-Id specified, body has no request_id
		// ============================================

		Context("TC1: Header X-Request-Id specified, body has no request_id", func() {
			It("/v1/chat/completions non-stream", func() {
				headerXRequestId := "test-header-req-id"
				body := map[string]interface{}{
					"model":      "test-model",
					"messages":   []map[string]string{{"role": "user", "content": "hello"}},
					"max_tokens": 10,
				}

				resp, err := clientRs.SendChatCompletion(context.Background(), body, map[string]string{constant.XRequestId: headerXRequestId})
				Expect(err).NotTo(HaveOccurred())
				Expect(resp.StatusCode).To(Equal(http.StatusOK))

				defer resp.Body.Close()
				respBody, _ := io.ReadAll(resp.Body)
				var responseMap map[string]interface{}
				Expect(json.Unmarshal(respBody, &responseMap)).To(BeNil())

				// verify the response id is the original value
				Expect(responseMap["id"]).To(Equal(headerXRequestId))

				// verify the log
				expectedRegexp := fmt.Sprintf(`original request id \[%s\] will be rewrite to`, headerXRequestId)
				Eventually(session.Out, 5*time.Second).Should(gbytes.Say(expectedRegexp))
			})

			It("/v1/chat/completions stream", func() {
				headerXRequestId := "test-header-stream"
				body := map[string]interface{}{
					"model":      "test-model",
					"messages":   []map[string]string{{"role": "user", "content": "hello"}},
					"max_tokens": 10,
					"stream":     true,
				}

				resp, err := clientRs.SendChatCompletion(context.Background(), body, map[string]string{constant.XRequestId: headerXRequestId})
				Expect(err).NotTo(HaveOccurred())
				Expect(resp.StatusCode).To(Equal(http.StatusOK))

				defer resp.Body.Close()
				respBody, _ := io.ReadAll(resp.Body)

				// verify the stream response contains the original id
				Expect(string(respBody)).To(ContainSubstring(headerXRequestId))

				// verify the log
				expectedRegexp := fmt.Sprintf(`original request id \[%s\] will be rewrite to`, headerXRequestId)
				Eventually(session.Out, 5*time.Second).Should(gbytes.Say(expectedRegexp))

				By("verify the first three SSE events")
				// first chunk: role="assistant", content=""
				Expect(string(respBody)).To(ContainSubstring(`"role":"assistant"`))
				// second chunk: content="test_0" (tokenIdx=0)
				Expect(string(respBody)).To(ContainSubstring(`"content":"test_0"`))
				// third chunk: content="test_1" (tokenIdx=1)
				Expect(string(respBody)).To(ContainSubstring(`"content":"test_1"`))

				By("verify perfmetrics: ttft and tpot are not 0")
				expectedRegexp = fmt.Sprintf(`CompletionMetric.*'trace_id'.*%s.*'ttft': [1-9]`, headerXRequestId)
				Eventually(session.Out, 10*time.Second).Should(gbytes.Say(expectedRegexp))
			})

			It("/v1/completions non-stream", func() {
				headerXRequestId := "test-comp-req-id"
				body := map[string]interface{}{
					"model":      "test-model",
					"prompt":     "hello",
					"max_tokens": 10,
				}

				resp, err := clientRs.SendCompletion(context.Background(), body, map[string]string{constant.XRequestId: headerXRequestId})
				Expect(err).NotTo(HaveOccurred())
				Expect(resp.StatusCode).To(Equal(http.StatusOK))

				defer resp.Body.Close()
				respBody, _ := io.ReadAll(resp.Body)
				var responseMap map[string]interface{}
				json.Unmarshal(respBody, &responseMap)

				// verify the response id is the original value (the completions endpoint adds a cmpl- prefix)
				Expect(responseMap["id"]).To(Equal("cmpl-" + headerXRequestId))

				// verify the log
				expectedRegexp := fmt.Sprintf(`original request id \[%s\] will be rewrite to`, headerXRequestId)
				Eventually(session.Out, 5*time.Second).Should(gbytes.Say(expectedRegexp))
			})

			It("/v1/completions stream", func() {
				headerXRequestId := "test-comp-stream"
				body := map[string]interface{}{
					"model":      "test-model",
					"prompt":     "hello",
					"max_tokens": 10,
					"stream":     true,
				}

				resp, err := clientRs.SendCompletion(context.Background(), body, map[string]string{constant.XRequestId: headerXRequestId})
				Expect(err).NotTo(HaveOccurred())
				Expect(resp.StatusCode).To(Equal(http.StatusOK))

				defer resp.Body.Close()
				respBody, _ := io.ReadAll(resp.Body)

				// verify the streaming response contains the original id (the completions endpoint adds a cmpl- prefix)
				Expect(string(respBody)).To(ContainSubstring("cmpl-" + headerXRequestId))

				// verify the log
				expectedRegexp := fmt.Sprintf(`original request id \[%s\] will be rewrite to`, headerXRequestId)
				Eventually(session.Out, 5*time.Second).Should(gbytes.Say(expectedRegexp))
			})
		})

		// ============================================
		// TC2: Header X-Request-Id not specified, body has request_id
		// ============================================

		Context("TC2: Header X-Request-Id not specified, body has request_id", func() {
			It("/v1/chat/completions non-stream", func() {
				bodyRequestId := "test-body-req-id"
				body := map[string]interface{}{
					"model":      "test-model",
					"messages":   []map[string]string{{"role": "user", "content": "hello"}},
					"max_tokens": 10,
					"request_id": bodyRequestId,
				}

				resp, err := clientRs.SendChatCompletion(context.Background(), body, nil)
				Expect(err).NotTo(HaveOccurred())
				Expect(resp.StatusCode).To(Equal(http.StatusOK))

				defer resp.Body.Close()
				respBody, _ := io.ReadAll(resp.Body)
				var responseMap map[string]interface{}
				json.Unmarshal(respBody, &responseMap)

				// verify the response id is the original body request_id
				Expect(responseMap["id"]).To(Equal(bodyRequestId))

				// verify the log
				expectedRegexp := fmt.Sprintf(`original request id \[%s\] will be rewrite to`, bodyRequestId)
				Eventually(session.Out, 5*time.Second).Should(gbytes.Say(expectedRegexp))
			})

			It("/v1/chat/completions stream", func() {
				bodyRequestId := "test-body-stream"
				body := map[string]interface{}{
					"model":      "test-model",
					"messages":   []map[string]string{{"role": "user", "content": "hello"}},
					"max_tokens": 10,
					"request_id": bodyRequestId,
					"stream":     true,
				}

				resp, err := clientRs.SendChatCompletion(context.Background(), body, nil)
				Expect(err).NotTo(HaveOccurred())
				Expect(resp.StatusCode).To(Equal(http.StatusOK))

				defer resp.Body.Close()
				respBody, _ := io.ReadAll(resp.Body)

				// verify the streaming response contains the original body request_id (the completions endpoint adds a cmpl- prefix)
				Expect(string(respBody)).To(ContainSubstring("cmpl-" + bodyRequestId))

				// verify the log
				expectedRegexp := fmt.Sprintf(`original request id \[%s\] will be rewrite to`, bodyRequestId)
				Eventually(session.Out, 5*time.Second).Should(gbytes.Say(expectedRegexp))
			})

			It("/v1/completions stream", func() {
				bodyRequestId := "test-comp-body-stream"
				body := map[string]interface{}{
					"model":      "test-model",
					"prompt":     "hello",
					"max_tokens": 10,
					"request_id": bodyRequestId,
					"stream":     true,
				}

				resp, err := clientRs.SendCompletion(context.Background(), body, nil)
				Expect(err).NotTo(HaveOccurred())
				Expect(resp.StatusCode).To(Equal(http.StatusOK))

				defer resp.Body.Close()
				respBody, _ := io.ReadAll(resp.Body)

				// verify the streaming response contains the original body request_id (the completions endpoint adds a cmpl- prefix)
				Expect(string(respBody)).To(ContainSubstring("cmpl-" + bodyRequestId))

				// verify the log
				expectedRegexp := fmt.Sprintf(`original request id \[%s\] will be rewrite to`, bodyRequestId)
				Eventually(session.Out, 5*time.Second).Should(gbytes.Say(expectedRegexp))
			})
		})

		// ============================================
		// TC3: Header X-Request-Id not specified, body has no request_id
		// ============================================

		Context("TC3: Header X-Request-Id not specified, body has no request_id", func() {
			It("/v1/chat/completions non-stream", func() {
				body := map[string]interface{}{
					"model":      "test-model",
					"messages":   []map[string]string{{"role": "user", "content": "hello"}},
					"max_tokens": 10,
				}

				resp, err := clientRs.SendChatCompletion(context.Background(), body, nil)
				Expect(err).NotTo(HaveOccurred())
				Expect(resp.StatusCode).To(Equal(http.StatusOK))

				defer resp.Body.Close()
				respBody, _ := io.ReadAll(resp.Body)
				var responseMap map[string]interface{}
				json.Unmarshal(respBody, &responseMap)

				responseId := responseMap["id"].(string)
				// verify the response id is a randomly generated UUID format (32 hex characters)
				Expect(responseId).To(MatchRegexp(`^[0-9a-f]{32}$`))

				// verify the log should not contain a rewrite record
				expectedRegexp := fmt.Sprintf(`original request id .* will be rewrite to`)
				Eventually(session.Out, 5*time.Second).ShouldNot(gbytes.Say(expectedRegexp))
			})

			It("/v1/chat/completions stream", func() {
				body := map[string]interface{}{
					"model":      "test-model",
					"messages":   []map[string]string{{"role": "user", "content": "hello"}},
					"max_tokens": 10,
					"stream":     true,
				}

				resp, err := clientRs.SendChatCompletion(context.Background(), body, nil)
				Expect(err).NotTo(HaveOccurred())
				Expect(resp.StatusCode).To(Equal(http.StatusOK))

				defer resp.Body.Close()
				respBody, _ := io.ReadAll(resp.Body)

				// the id in the streaming response is randomly generated (chatcmpl- prefix)
				Expect(string(respBody)).To(MatchRegexp(`"id":"chatcmpl-[0-9a-zA-Z]+"`))

				// verify the log should not contain a rewrite record
				expectedRegexp := fmt.Sprintf(`original request id .* will be rewrite to`)
				Eventually(session.Out, 5*time.Second).ShouldNot(gbytes.Say(expectedRegexp))
			})

			It("/v1/completions non-stream", func() {
				body := map[string]interface{}{
					"model":      "test-model",
					"prompt":     "hello",
					"max_tokens": 10,
				}

				resp, err := clientRs.SendCompletion(context.Background(), body, nil)
				Expect(err).NotTo(HaveOccurred())
				Expect(resp.StatusCode).To(Equal(http.StatusOK))

				defer resp.Body.Close()
				respBody, _ := io.ReadAll(resp.Body)
				var responseMap map[string]interface{}
				json.Unmarshal(respBody, &responseMap)

				responseId := responseMap["id"].(string)
				// verify the response id is a randomly generated UUID format (the completions endpoint adds a cmpl- prefix)
				Expect(responseId).To(MatchRegexp(`^cmpl-[0-9a-f]{32}$`))

				// verify the log should not contain a rewrite record
				expectedRegexp := fmt.Sprintf(`original request id .* will be rewrite to`)
				Eventually(session.Out, 5*time.Second).ShouldNot(gbytes.Say(expectedRegexp))
			})

			It("/v1/completions stream", func() {
				body := map[string]interface{}{
					"model":      "test-model",
					"prompt":     "hello",
					"max_tokens": 10,
					"stream":     true,
				}

				resp, err := clientRs.SendCompletion(context.Background(), body, nil)
				Expect(err).NotTo(HaveOccurred())
				Expect(resp.StatusCode).To(Equal(http.StatusOK))

				defer resp.Body.Close()
				respBody, _ := io.ReadAll(resp.Body)

				// the id in the streaming response is randomly generated (the completions endpoint adds a cmpl- prefix)
				Expect(string(respBody)).To(MatchRegexp(`"id":"cmpl-[0-9a-f]{32}"`))

				// verify the log should not contain a rewrite record
				expectedRegexp := fmt.Sprintf(`original request id .* will be rewrite to`)
				Eventually(session.Out, 5*time.Second).ShouldNot(gbytes.Say(expectedRegexp))
			})
		})

		// ============================================
		// TC4: Header X-Request-Id specified, body also has request_id
		// ============================================

		Context("TC4: Header X-Request-Id specified, body also has request_id", func() {
			It("/v1/chat/completions non-stream", func() {
				headerXRequestId := "test-header-id"
				bodyRequestId := "test-body-id-ignored"

				body := map[string]interface{}{
					"model":      "test-model",
					"messages":   []map[string]string{{"role": "user", "content": "hello"}},
					"max_tokens": 10,
					"request_id": bodyRequestId,
				}

				resp, err := clientRs.SendChatCompletion(context.Background(), body, map[string]string{constant.XRequestId: headerXRequestId})
				Expect(err).NotTo(HaveOccurred())
				Expect(resp.StatusCode).To(Equal(http.StatusOK))

				defer resp.Body.Close()
				respBody, _ := io.ReadAll(resp.Body)
				var responseMap map[string]interface{}
				json.Unmarshal(respBody, &responseMap)

				// verify the response id is the header value (the body value is ignored)
				Expect(responseMap["id"]).To(Equal(headerXRequestId))

				// verify the log uses the header value
				expectedRegexp := fmt.Sprintf(`original request id \[%s\] will be rewrite to`, headerXRequestId)
				notExpectedRegexp := fmt.Sprintf(`original request id \[%s\] will be rewrite to`, bodyRequestId)
				Eventually(session.Out, 5*time.Second).Should(gbytes.Say(expectedRegexp))
				Eventually(session.Out, 5*time.Second).ShouldNot(gbytes.Say(notExpectedRegexp))
			})

			It("/v1/chat/completions stream", func() {
				headerXRequestId := "test-header-stream-2"
				bodyRequestId := "test-body-id-ignored-2"

				body := map[string]interface{}{
					"model":      "test-model",
					"messages":   []map[string]string{{"role": "user", "content": "hello"}},
					"max_tokens": 10,
					"request_id": bodyRequestId,
					"stream":     true,
				}

				resp, err := clientRs.SendChatCompletion(context.Background(), body, map[string]string{constant.XRequestId: headerXRequestId})
				Expect(err).NotTo(HaveOccurred())
				Expect(resp.StatusCode).To(Equal(http.StatusOK))

				defer resp.Body.Close()
				respBody, _ := io.ReadAll(resp.Body)

				// verify the streaming response contains the header value (the completions endpoint adds a cmpl- prefix)
				Expect(string(respBody)).To(ContainSubstring("cmpl-" + headerXRequestId))
				Expect(string(respBody)).ToNot(ContainSubstring(bodyRequestId))

				// verify the log uses the header value
				expectedRegexp := fmt.Sprintf(`original request id \[%s\] will be rewrite to`, headerXRequestId)
				notExpectedRegexp := fmt.Sprintf(`original request id \[%s\] will be rewrite to`, bodyRequestId)
				Eventually(session.Out, 5*time.Second).Should(gbytes.Say(expectedRegexp))
				Eventually(session.Out, 5*time.Second).ShouldNot(gbytes.Say(notExpectedRegexp))
			})
		})
	})

	// ================================================================
	// PD Link Down tests
	// ================================================================
	Context("PD Link Down Tests", func() {
		var (
			clientRs         *RSClient
			mockDecodeClient *MockServerClient

			healthCheckIntervalMs      int
			consecutiveFailureCount    int
			recoverConsecutiveCount    int
			linkBlockTagValidTimeMs    int64
			probeInferRequestTimeoutMs int

			recoveryWaitTime   time.Duration
			probeRetryWaitTime time.Duration
		)

		BeforeAll(func() {
			healthCheckIntervalMs = options.GetConfig().InstanceManagerCfg.HealthCheckConfig.RestHealthCommonConfig.Interval
			consecutiveFailureCount = options.GetConfig().InstanceManagerCfg.HealthCheckConfig.AvailableCheckersCfg.RestHealthCfg.ConsecutiveTime
			recoverConsecutiveCount = options.GetConfig().InstanceManagerCfg.HealthCheckConfig.UnavailableCheckersCfg.RestHealthCfg.ConsecutiveTime
			linkBlockTagValidTimeMs = options.GetConfig().InstanceManagerCfg.HealthCheckConfig.AvailableCheckersCfg.LinkBlacklistConfig.LinkBlockTagValidTimeMs
			probeInferRequestTimeoutMs = options.GetConfig().InstanceManagerCfg.HealthCheckConfig.ProbeInferRequestTimeoutMs
			// one extra cycle of buffer, plus the blacklist expiry time
			recoveryWaitTime = time.Duration(healthCheckIntervalMs*(recoverConsecutiveCount+1)+int(linkBlockTagValidTimeMs)) * time.Millisecond
			// wait time = health recovery time + probe request timeout
			probeRetryWaitTime = time.Duration(healthCheckIntervalMs*(recoverConsecutiveCount+1)+probeInferRequestTimeoutMs) * time.Millisecond
		})

		BeforeEach(func() {

			// prefill port 8081, decode port 8082
			mockDecodeClient = NewMockServerClient(pdDecodePort)
			_, err = mockDecodeClient.LinkUp()
			Expect(err).NotTo(HaveOccurred())
			Expect(restartProcess(&session)).To(BeNil())
			clientRs = NewRSClient("http://localhost:" + pdListenPort)
			// wait for the state to take effect
		})

		// -------------------------------------------------------
		// TC1: single PD Link Down failure (not added to blacklist)
		// -------------------------------------------------------
		Context("TC1: single PD Link Down failure (not added to blacklist)", func() {
			It("non-stream - single link down then recovery, request returns link down error", func() {
				_, err := mockDecodeClient.LinkDown("127.0.0.1", pdDecodePort)
				Expect(err).NotTo(HaveOccurred())
				time.Sleep(500 * time.Millisecond)

				// verify the request returns 500 (the link down error is in the response body)
				resp, err := clientRs.SendChatCompletion(context.Background(), NewBasicChatRequestBody(), nil)
				Expect(err).NotTo(HaveOccurred())
				Expect(resp.StatusCode).To(Equal(http.StatusInternalServerError))

				// read the response body and check for kv_transfer_params (the link down error marker)
				defer resp.Body.Close()
				respBody, _ := io.ReadAll(resp.Body)
				respStr := string(respBody)
				// when link down occurs, the response body includes the kv_transfer_params field
				Expect(respStr).To(ContainSubstring("Abort for KV loading failure"))

				// recover the link
				_, err = mockDecodeClient.LinkUp()
				Expect(err).NotTo(HaveOccurred())
				time.Sleep(500 * time.Millisecond)

				// verify the request succeeds after recovery
				resp, err = clientRs.SendChatCompletion(context.Background(), NewBasicChatRequestBody(), nil)
				Expect(err).NotTo(HaveOccurred())
				Expect(resp.StatusCode).To(Equal(http.StatusOK))

				var result openai.ChatCompletionResponse
				Expect(ParseResponse(resp, &result)).NotTo(HaveOccurred())
				Expect(result.Choices).NotTo(BeEmpty())
			})

			It("stream - single link down then recovery, request returns link down error", func() {
				body := map[string]interface{}{
					"model":      "pangu",
					"messages":   []map[string]string{{"role": "user", "content": "hello"}},
					"max_tokens": 10,
					"stream":     true,
				}

				// set link down
				_, err := mockDecodeClient.LinkDown("127.0.0.1", pdDecodePort)
				Expect(err).NotTo(HaveOccurred())
				time.Sleep(500 * time.Millisecond)

				// verify the request returns 200 (the link down error is in the response body)
				resp, err := clientRs.SendChatCompletion(context.Background(), body, nil)
				Expect(err).NotTo(HaveOccurred())
				Expect(resp.StatusCode).To(Equal(http.StatusOK))

				// check that the response body contains link down related information
				defer resp.Body.Close()
				respBody, _ := io.ReadAll(resp.Body)
				respStr := string(respBody)
				// in the streaming case, a link down includes error information in the response
				Expect(respStr).To(Or(ContainSubstring("link"), ContainSubstring("PDLinkDownError"), ContainSubstring("kv_transfer_params")))

				// recover the link
				_, err = mockDecodeClient.LinkUp()
				Expect(err).NotTo(HaveOccurred())
				time.Sleep(500 * time.Millisecond)

				// verify the request succeeds after recovery
				resp, err = clientRs.SendChatCompletion(context.Background(), body, nil)
				Expect(err).NotTo(HaveOccurred())
				Expect(resp.StatusCode).To(Equal(http.StatusOK))

				respBody, _ = io.ReadAll(resp.Body)
				Expect(string(respBody)).To(ContainSubstring("data:"))
			})
		})

		// -------------------------------------------------------
		// TC2: consecutive PD Link Down failures (added to blacklist)
		// -------------------------------------------------------
		Context("TC2: consecutive PD Link Down failures (added to blacklist)", func() {
			It("non-stream - 3 consecutive link downs, verify the log and normal recovery", func() {
				_, err := mockDecodeClient.LinkDown("127.0.0.1", pdDecodePort)
				Expect(err).NotTo(HaveOccurred())
				// trigger link down consecutiveFailureCount times in a row
				for i := 0; i < consecutiveFailureCount; i++ {

					time.Sleep(200 * time.Millisecond)
					// after each trigger, verify the request returns a non-200 status (the link down error is in the response body)
					resp, err := clientRs.SendChatCompletion(context.Background(), NewBasicChatRequestBody(), nil)
					Expect(err).NotTo(HaveOccurred())
					Expect(resp.StatusCode).To(Equal(http.StatusInternalServerError))

					// check that the response body contains kv_transfer_params (the link down error marker)
					respBody, _ := io.ReadAll(resp.Body)
					resp.Body.Close()
					respStr := string(respBody)
					Expect(respStr).To(ContainSubstring("Abort for KV loading failure"))
				}

				time.Sleep(200 * time.Millisecond)
				resp, err := clientRs.SendChatCompletion(context.Background(), NewBasicChatRequestBody(), nil)
				Expect(err).NotTo(HaveOccurred())
				Expect(resp.StatusCode).To(Equal(http.StatusServiceUnavailable))

				respBody, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				respStr := string(respBody)
				Expect(respStr).To(ContainSubstring("fail to schedule"))

				// check the metrics
				resp, err = clientRs.GetInternalMetrics()
				if err != nil {
					if resp != nil {
						resp.Body.Close()
					}
					Fail(fmt.Sprintf("GetInternalMetrics failed: %v", err))
				}
				Expect(resp.StatusCode).To(Equal(http.StatusOK))
				Expect(ExtractMetricValue(resp,
					"rs:number_faulty_endpoints", map[string]string{"fault_type": health.LinkBlacklist, "role": "decode"})).
					To(Equal(1.0))
				resp.Body.Close()

				// do not recover the link immediately; verify probing is skipped while the blacklist is active
				Eventually(session.Out, recoveryWaitTime).Should(gbytes.Say(
					`\[recover\] instance .* is still in pd link blacklist, skip probe`))

				// wait for the blacklist to expire, then the probe request fails because the link is still down
				Eventually(session.Out, recoveryWaitTime).Should(gbytes.Say(
					`\[recover\] send probe infer request to .* error, reset ConsecutiveHealthSuccess to 0`))

				// recover the link
				_, err = mockDecodeClient.LinkUp()
				Expect(err).NotTo(HaveOccurred())

				// wait for the instance to come back online
				time.Sleep(recoveryWaitTime)
				Eventually(session.Out, recoveryWaitTime).Should(gbytes.Say(`instance .* is back online due to healthcheck success`))

				// verify the request succeeds after recovery
				resp, err = clientRs.SendChatCompletion(context.Background(), NewBasicChatRequestBody(), nil)
				Expect(err).NotTo(HaveOccurred())
				Expect(resp.StatusCode).To(Equal(http.StatusOK))

				var result openai.ChatCompletionResponse
				Expect(ParseResponse(resp, &result)).NotTo(HaveOccurred())
				Expect(result.Choices).NotTo(BeEmpty())
			})

			It("stream - 3 consecutive link downs added to blacklist, normal after recovery", func() {
				body := map[string]interface{}{
					"model":      "pangu",
					"messages":   []map[string]string{{"role": "user", "content": "hello"}},
					"max_tokens": 10,
					"stream":     true,
				}

				// trigger link down consecutiveFailureCount times in a row
				for i := 0; i < consecutiveFailureCount; i++ {
					_, err := mockDecodeClient.LinkDown("127.0.0.1", pdDecodePort)
					Expect(err).NotTo(HaveOccurred())
					time.Sleep(200 * time.Millisecond)

					// the streaming response returns 200, with the link down info in the response body
					resp, err := clientRs.SendChatCompletion(context.Background(), body, nil)
					Expect(err).NotTo(HaveOccurred())
					Expect(resp.StatusCode).To(Equal(http.StatusOK))

					defer resp.Body.Close()
					respBody, _ := io.ReadAll(resp.Body)
					respStr := string(respBody)
					Expect(respStr).To(Or(ContainSubstring("link"), ContainSubstring("PDLinkDownError"), ContainSubstring("kv_transfer_params")))
				}

				time.Sleep(200 * time.Millisecond)
				resp, err := clientRs.SendChatCompletion(context.Background(), body, nil)
				Expect(err).NotTo(HaveOccurred())
				Expect(resp.StatusCode).To(Equal(http.StatusServiceUnavailable))

				respBody, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				respStr := string(respBody)
				Expect(respStr).To(ContainSubstring("fail to schedule"))

				// do not recover the link immediately; verify probing is skipped while the blacklist is active
				Eventually(session.Out, recoveryWaitTime).Should(gbytes.Say(
					`\[recover\] instance .* is still in pd link blacklist, skip probe`))

				// wait for the blacklist to expire, then the probe request fails because the link is still down
				Eventually(session.Out, recoveryWaitTime).Should(gbytes.Say(
					`\[recover\] send probe infer request to .* error, reset ConsecutiveHealthSuccess to 0`))

				// recover the link
				_, err = mockDecodeClient.LinkUp()
				Expect(err).NotTo(HaveOccurred())

				// wait for the instance to come back online
				time.Sleep(recoveryWaitTime)
				Eventually(session.Out, recoveryWaitTime).Should(gbytes.Say(`instance .* is back online due to healthcheck success`))

				// verify the request succeeds after recovery
				resp, err = clientRs.SendChatCompletion(context.Background(), body, nil)
				Expect(err).NotTo(HaveOccurred())
				Expect(resp.StatusCode).To(Equal(http.StatusOK))

				defer resp.Body.Close()
				respBody, _ = io.ReadAll(resp.Body)
				Expect(string(respBody)).To(ContainSubstring("data:"))
			})
		})

		// -------------------------------------------------------
		// TC3: 503 recovery scenario (after pd linkdown recovery, D reports 503, P can recover normally)
		// -------------------------------------------------------
		Context("TC3: 503 recovery scenario", func() {
			It("after pd linkdown recovery, D reports 503, P can recover normally", func() {
				reqBody := NewBasicChatRequestBody()

				By("step 1: trigger blacklist to isolate P")
				_, err := mockDecodeClient.LinkDown("127.0.0.1", pdDecodePort, "omni")
				Expect(err).NotTo(HaveOccurred())
				time.Sleep(500 * time.Millisecond)

				// send 5 requests to trigger the blacklist
				for i := 0; i < 5; i++ {
					resp, sendErr := clientRs.SendChatCompletion(TimeoutCtx(context.Background(), 5*time.Second), reqBody, nil)
					if sendErr == nil && resp != nil {
						resp.Body.Close()
					}
				}

				// wait for P to be isolated
				Eventually(func() string {
					statusResp, statusErr := clientRs.GetStatusJson()
					if statusErr != nil {
						return ""
					}
					for _, inst := range statusResp.InstanceStatus["prefill"] {
						if strings.Contains(inst.URL, fmt.Sprintf("127.0.0.1:%s", pdPrefillPort)) {
							return inst.Status
						}
					}
					return ""
				}, 15*time.Second, 1*time.Second).Should(Equal(constant.FaultStatus), "503 scenario: prefill should be fault")

				By("step 2: MockUnHealth makes D unavailable, LinkUp restores the link (D still unhealthy)")
				_, err = mockDecodeClient.MockUnHealth()
				Expect(err).NotTo(HaveOccurred())
				time.Sleep(500 * time.Millisecond)

				_, err = mockDecodeClient.LinkUp()
				Expect(err).NotTo(HaveOccurred())

				// verify D is isolated (fault) - in 1P1D mode D is isolated (100% > 70%)
				Eventually(func() string {
					statusResp, statusErr := clientRs.GetStatusJson()
					if statusErr != nil {
						return ""
					}
					for _, inst := range statusResp.InstanceStatus["decode"] {
						if strings.Contains(inst.URL, fmt.Sprintf("127.0.0.1:%s", pdDecodePort)) {
							return inst.Status
						}
					}
					return ""
				}, 15*time.Second, 1*time.Second).Should(Equal(constant.FaultStatus), "503 scenario: decode should be fault")

				By("step 3: after blacklist expiry, probe P; D unavailable returns 503, P recovers")
				probeWait := time.Duration(linkBlockTagValidTimeMs)*time.Millisecond + probeRetryWaitTime
				time.Sleep(probeWait)

				// verify P recovers to running (the 503 makes P recover)
				Eventually(func() string {
					statusResp, statusErr := clientRs.GetStatusJson()
					if statusErr != nil {
						return ""
					}
					for _, inst := range statusResp.InstanceStatus["prefill"] {
						if strings.Contains(inst.URL, fmt.Sprintf("127.0.0.1:%s", pdPrefillPort)) {
							return inst.Status
						}
					}
					return ""
				}, 30*time.Second, 2*time.Second).Should(Equal(constant.RunningStatus),
					"503 scenario: prefill should recover via 503 probe success")

				By("step 4: MockHealth to recover D")
				_, err = mockDecodeClient.MockHealth()
				Expect(err).NotTo(HaveOccurred())

				Eventually(func() string {
					statusResp, statusErr := clientRs.GetStatusJson()
					if statusErr != nil {
						return ""
					}
					for _, inst := range statusResp.InstanceStatus["decode"] {
						if strings.Contains(inst.URL, fmt.Sprintf("127.0.0.1:%s", pdDecodePort)) {
							return inst.Status
						}
					}
					return ""
				}, 30*time.Second, 2*time.Second).Should(Equal(constant.RunningStatus),
					"503 scenario: decode should recover after MockHealth")

				// cleanup: restore all mock states
				_, _ = mockDecodeClient.LinkUp()
				_, _ = mockDecodeClient.RemoveError()

				GinkgoWriter.Printf("[pd-test] 503 recovery scenario verification completed\n")
			})
		})
	})

	Context("RS link health-check tests", func() {
		// health-check config constants - should match the config file
		var (
			clientRs          *RSClient
			mockPrefillClient *MockServerClient
			mockDecodeClient  *MockServerClient

			healthCheckIntervalMs   int
			consecutiveFailureCount int
			recoverConsecutiveCount int
			linkBlockTagValidTimeMs int64
			maxFailureWaitTime      time.Duration
			recoveryWaitTime        time.Duration
		)

		BeforeAll(func() {
			healthCheckIntervalMs = options.GetConfig().InstanceManagerCfg.HealthCheckConfig.RestHealthCommonConfig.Interval
			consecutiveFailureCount = options.GetConfig().InstanceManagerCfg.HealthCheckConfig.AvailableCheckersCfg.RestHealthCfg.ConsecutiveTime
			recoverConsecutiveCount = options.GetConfig().InstanceManagerCfg.HealthCheckConfig.UnavailableCheckersCfg.RestHealthCfg.ConsecutiveTime
			linkBlockTagValidTimeMs = options.GetConfig().InstanceManagerCfg.HealthCheckConfig.AvailableCheckersCfg.LinkBlacklistConfig.LinkBlockTagValidTimeMs
			// an extra 5s buffer
			maxFailureWaitTime = time.Duration(healthCheckIntervalMs*consecutiveFailureCount+5000) * time.Millisecond
			// one extra cycle of buffer, plus the blacklist expiry time
			recoveryWaitTime = time.Duration(healthCheckIntervalMs*(recoverConsecutiveCount+1)+int(linkBlockTagValidTimeMs)) * time.Millisecond
		})

		BeforeEach(func() {
			clientRs = NewRSClient("http://localhost:" + pdListenPort)
			// per pd_test.go config, the prefill port is 8081 and the decode port is 8082
			mockPrefillClient = NewMockServerClient(pdPrefillPort)
			mockDecodeClient = NewMockServerClient(pdDecodePort)

			// ensure all instances start in a healthy state
			_, err := mockPrefillClient.MockHealth()
			Expect(err).NotTo(HaveOccurred())
			_, err = mockDecodeClient.MockHealth()
			Expect(err).NotTo(HaveOccurred())

			// wait for the RS health check to take effect
			time.Sleep(1 * time.Second)
		})

		It("verify RS health state - returns 200 when all instances are healthy", func() {
			// check the overall health state through the RS /health endpoint
			resp, err := clientRs.CheckRSHealth()
			Expect(err).NotTo(HaveOccurred())
			Expect(resp.StatusCode).To(Equal(http.StatusOK))
			resp.Body.Close()

			resp, err = clientRs.SendChatCompletion(context.Background(), NewBasicChatRequestBody(), nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(resp.StatusCode).To(Equal(http.StatusOK))

			var result openai.ChatCompletionResponse
			Expect(ParseResponse(resp, &result)).NotTo(HaveOccurred())
			Expect(result.Choices).NotTo(BeEmpty())
			resp.Body.Close()
		})

		It("verify RS fault-state detection - RS returns 500 when a mock instance faults, and 200 after recovery", func() {
			// set a mock instance to an unhealthy state
			_, err := mockPrefillClient.MockUnHealth()
			Expect(err).NotTo(HaveOccurred())

			// compute the wait time using the config constants
			time.Sleep(maxFailureWaitTime)

			// RS should detect the fault and return a non-200 status
			resp, err := clientRs.CheckRSHealth()
			Expect(err).NotTo(HaveOccurred())
			Expect(resp.StatusCode).To(Equal(http.StatusInternalServerError))
			resp.Body.Close()

			var result openai.ChatCompletionResponse

			// restore the mock instance to a healthy state
			_, err = mockPrefillClient.MockHealth()
			Expect(err).NotTo(HaveOccurred())

			// wait for the RS health check to detect recovery (requires consecutive successes, at 10s intervals)
			time.Sleep(recoveryWaitTime)

			// RS should return to a healthy state
			resp, err = clientRs.CheckRSHealth()
			Expect(err).NotTo(HaveOccurred())
			Expect(resp.StatusCode).To(Equal(http.StatusOK))
			resp.Body.Close()

			resp, err = clientRs.SendChatCompletion(context.Background(), NewBasicChatRequestBody(), nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(resp.StatusCode).To(Equal(http.StatusOK))

			Expect(ParseResponse(resp, &result)).NotTo(HaveOccurred())
			Expect(result.Choices).NotTo(BeEmpty())
			resp.Body.Close()
		})

	})

	Context("Finish Reason log verification tests", func() {
		var (
			clientRs          *RSClient
			mockPrefillClient *MockServerClient
		)

		BeforeEach(func() {
			clientRs = NewRSClient("http://localhost:" + pdListenPort)
			mockPrefillClient = NewMockServerClient(pdPrefillPort)
		})

		It("finish reason verification: finish_reason is length when max_tokens=2", func() {
			reqId := "test-finish-length"
			body := map[string]interface{}{
				"model":      "pangu",
				"messages":   []map[string]string{{"role": "user", "content": "Hello"}},
				"max_tokens": 2,
				"stream":     true,
			}

			resp, err := clientRs.SendChatCompletion(context.Background(), body, map[string]string{
				constant.XRequestId: reqId,
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(resp.StatusCode).To(Equal(http.StatusOK))
			defer resp.Body.Close()

			expectedRegexp := fmt.Sprintf(`request_id.*.%s.*finish_reason = length`, reqId)
			Eventually(session.Out, 10*time.Second).Should(gbytes.Say(expectedRegexp))
		})

		It("finish reason verification: finish_reason is stop without max_tokens", func() {
			reqId := "test-finish-stop"
			body := map[string]interface{}{
				"model":    "pangu",
				"messages": []map[string]string{{"role": "user", "content": "Hello"}},
				"stream":   true,
			}

			resp, err := clientRs.SendChatCompletion(context.Background(), body, map[string]string{
				constant.XRequestId: reqId,
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(resp.StatusCode).To(Equal(http.StatusOK))
			defer resp.Body.Close()

			expectedRegexp := fmt.Sprintf(`request_id.*.%s.*finish_reason = stop`, reqId)
			Eventually(session.Out, 10*time.Second).Should(gbytes.Say(expectedRegexp))
		})

		It("finish reason verification: finish_reason is tool_calls when tools are supplied", func() {
			reqId := "test-finish-tool"
			body := map[string]interface{}{
				"model":      "pangu",
				"max_tokens": 12,
				"messages":   []map[string]string{{"role": "user", "content": "Get Beijing weather using Celsius"}},
				"tools": []map[string]interface{}{
					{
						"type": "function",
						"function": map[string]interface{}{
							"name":        "get_weather",
							"description": "Get the current weather in a given location",
							"parameters": map[string]interface{}{
								"type": "object",
								"properties": map[string]interface{}{
									"location": map[string]interface{}{
										"type":        "string",
										"description": "City Name",
									},
									"unit": map[string]interface{}{
										"type": "string",
										"enum": []string{"celsius", "fahrenheit"},
									},
								},
								"required": []string{"location", "unit"},
							},
						},
					},
				},
				"tool_choice": "auto",
				"stream":      true,
			}

			resp, err := clientRs.SendChatCompletion(context.Background(), body, map[string]string{
				constant.XRequestId: reqId,
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(resp.StatusCode).To(Equal(http.StatusOK))
			defer resp.Body.Close()

			expectedRegexp := fmt.Sprintf(`request_id.*.%s.*finish_reason = tool_calls`, reqId)
			Eventually(session.Out, 10*time.Second).Should(gbytes.Say(expectedRegexp))
		})

		It("finish reason verification: finish_reason is unknown when prefill returns 500", func() {
			By("configure the prefill1 instance to return a 500 error")
			errBody := strings.NewReader(`{"code": 500}`)
			req, err := http.NewRequest("POST", fmt.Sprintf("http://127.0.0.1:%s/mock_error", pdPrefillPort), errBody)
			Expect(err).NotTo(HaveOccurred())
			req.Header.Set("Content-Type", "application/json")
			resp, err := mockPrefillClient.HTTPClient.Do(req)
			Expect(err).NotTo(HaveOccurred())
			resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusOK))

			DeferCleanup(func() {
				mockPrefillClient.RemoveError()
			})

			By("send the request and verify finish_reason = unknown in the log")
			reqId := "test-finish-unknown"
			body := map[string]interface{}{
				"model":    "pangu",
				"messages": []map[string]string{{"role": "user", "content": "Get Beijing weather"}},
				"stream":   true,
			}

			httpResp, err := clientRs.SendChatCompletion(context.Background(), body, map[string]string{
				constant.XRequestId: reqId,
				"x-span-id":         "p1",
			})
			Expect(err).NotTo(HaveOccurred())
			defer httpResp.Body.Close()

			expectedRegexp := fmt.Sprintf(`request_id.*.%s.*finish_reason = unknown.*err_stage = Prefill`, reqId)
			Eventually(session.Out, 10*time.Second).Should(gbytes.Say(expectedRegexp))
		})
	})

	Context("API Connection Retry Tests", func() {
		var (
			clientRs          *RSClient
			mockPrefillClient *MockServerClient
			mockDecodeClient  *MockServerClient
		)

		BeforeEach(func() {
			clientRs = NewRSClient("http://localhost:" + pdListenPort)
			mockPrefillClient = NewMockServerClient(pdPrefillPort)
			mockDecodeClient = NewMockServerClient(pdDecodePort)

			_, err := mockPrefillClient.RemoveConnErr()
			Expect(err).NotTo(HaveOccurred())
			_, err = mockDecodeClient.RemoveConnErr()
			Expect(err).NotTo(HaveOccurred())
		})

		AfterEach(func() {
			mockPrefillClient.RemoveConnErr()
			mockDecodeClient.RemoveConnErr()
		})

		It("eof: retry succeeds after a prefill EOF", func() {
			By("configure prefill to inject the eof error twice")
			Expect(setMockConnErr(mockPrefillClient, "eof", 2)).To(BeNil())

			By("send the request and verify the retry log")
			resp, err := clientRs.SendChatCompletion(TimeoutCtx(context.Background(), 30*time.Second), NewBasicChatRequestBody(), nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(resp.StatusCode).To(Equal(http.StatusOK))

			var result openai.ChatCompletionResponse
			Expect(ParseResponse(resp, &result)).NotTo(HaveOccurred())
			Expect(result.Choices).NotTo(BeEmpty())

			Eventually(session.Out, 5*time.Second).Should(gbytes.Say("connection error on attempt"))
		})

		It("eof: retry succeeds after a decode EOF", func() {
			By("configure decode to inject the eof error twice")
			Expect(setMockConnErr(mockDecodeClient, "eof", 2)).To(BeNil())

			By("send the request and verify the retry log")
			resp, err := clientRs.SendChatCompletion(TimeoutCtx(context.Background(), 30*time.Second), NewBasicChatRequestBody(), nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(resp.StatusCode).To(Equal(http.StatusOK))

			var result openai.ChatCompletionResponse
			Expect(ParseResponse(resp, &result)).NotTo(HaveOccurred())
			Expect(result.Choices).NotTo(BeEmpty())

			Eventually(session.Out, 5*time.Second).Should(gbytes.Say("connection error on attempt"))
		})

		It("eof: the request fails after exceeding the max retry count", func() {
			By("configure decode to inject the eof error 5 times (exceeds connection-retry=3)")
			Expect(setMockConnErr(mockDecodeClient, "eof", 5)).To(BeNil())

			By("send the request and verify the request fails (eof is classified as InternalErr=500)")
			resp, err := clientRs.SendChatCompletion(TimeoutCtx(context.Background(), 30*time.Second), NewBasicChatRequestBody(), nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(resp.StatusCode).To(Equal(http.StatusInternalServerError))

			Eventually(session.Out, 5*time.Second).Should(gbytes.Say("connection error on attempt"))
		})

		It("connection_reset: retry succeeds after a decode connection reset", func() {
			By("configure decode to inject the connection_reset error twice")
			Expect(setMockConnErr(mockDecodeClient, "connection_reset", 2)).To(BeNil())

			By("send the request and verify the retry log")
			resp, err := clientRs.SendChatCompletion(TimeoutCtx(context.Background(), 30*time.Second), NewBasicChatRequestBody(), nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(resp.StatusCode).To(Equal(http.StatusOK))

			var result openai.ChatCompletionResponse
			Expect(ParseResponse(resp, &result)).NotTo(HaveOccurred())
			Expect(result.Choices).NotTo(BeEmpty())

			Eventually(session.Out, 5*time.Second).Should(gbytes.Say("connection error on attempt"))
		})

		It("connection_reset: the request fails after exceeding the max retry count", func() {
			By("configure decode to inject the connection_reset error 5 times (exceeds connection-retry=3)")
			Expect(setMockConnErr(mockDecodeClient, "connection_reset", 5)).To(BeNil())

			By("send the request and verify the request fails (connection_reset is classified as BadGateway=502)")
			resp, err := clientRs.SendChatCompletion(TimeoutCtx(context.Background(), 30*time.Second), NewBasicChatRequestBody(), nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(resp.StatusCode).To(Equal(http.StatusBadGateway))

			Eventually(session.Out, 5*time.Second).Should(gbytes.Say("connection error on attempt"))
		})
	})

	Context("P/D anomaly reliability tests", func() {
		var (
			clientRs            *RSClient
			mockPrefillClient   *MockServerClient
			mockDecodeClient    *MockServerClient
			mockPrefill1Session *gexec.Session
		)

		BeforeEach(func() {
			clientRs = NewRSClient("http://localhost:" + pdListenPort)
			mockPrefillClient = NewMockServerClient(pdPrefillPort)
			mockDecodeClient = NewMockServerClient(pdDecodePort)
			mockPrefill1Session = mockServers[0]

			_, err := mockPrefillClient.MockHealth()
			Expect(err).NotTo(HaveOccurred())
			_, err = mockDecodeClient.MockHealth()
			Expect(err).NotTo(HaveOccurred())
			time.Sleep(1 * time.Second)

			DeferCleanup(func() {
				mockPrefillClient.MockHealth()
				mockDecodeClient.MockHealth()
				mockPrefillClient.RemoveError()
				mockDecodeClient.RemoveError()
			})
		})

		It("in non-xpyd scenarios, a killed P is not cleaned up by the status endpoint", func() {
			By("kill the prefill1 instance (simulate full unavailability)")
			mockPrefill1Session.Kill()
			mockPrefill1Session.Wait()

			By("wait until the status endpoint shows the P instance in fault state")
			var statusResp map[string]interface{}
			Eventually(func() bool {
				resp, err := clientRs.GetStatus()
				if err != nil {
					if resp != nil {
						resp.Body.Close()
					}
					return false
				}
				err = ParseResponse(resp, &statusResp)
				resp.Body.Close()
				if err != nil || statusResp == nil {
					return false
				}
				prefills, ok := statusResp["prefills"]
				if !ok {
					return false
				}
				prefillList, ok := prefills.([]interface{})
				if !ok {
					return false
				}
				for _, p := range prefillList {
					pInfo, ok := p.(map[string]interface{})
					if !ok {
						continue
					}
					if pInfo["url"] == fmt.Sprintf("http://127.0.0.1:%s", pdPrefillPort) {
						return true
					}
				}
				return false
			}, 30*time.Second, 2*time.Second).Should(BeTrue(), "status endpoint should return data and include the prefill1 instance")

			By("wait 45s then check the status endpoint")
			time.Sleep(45 * time.Second)

			By("check the status endpoint to confirm the P instance still exists and was not cleaned up")
			Eventually(func() bool {
				resp, err := clientRs.GetStatus()
				if err != nil {
					if resp != nil {
						resp.Body.Close()
					}
					return false
				}
				err = ParseResponse(resp, &statusResp)
				resp.Body.Close()
				if err != nil || statusResp == nil {
					return false
				}
				prefills, ok := statusResp["prefills"]
				if !ok {
					return false
				}
				prefillList, ok := prefills.([]interface{})
				if !ok {
					return false
				}
				for _, p := range prefillList {
					pInfo, ok := p.(map[string]interface{})
					if !ok {
						continue
					}
					if pInfo["url"] == fmt.Sprintf("http://127.0.0.1:%s", pdPrefillPort) {
						return true
					}
				}
				return false
			}, 30*time.Second, 2*time.Second).Should(BeTrue(), "in non-xpyd scenarios, the faulted P instance should still exist and not be cleaned up")

			By("in non-xpyd scenarios, the status endpoint should not clean up faulted P instances")
		})
	})
})

func InjectRecoverHealthConfigToYaml(configPath string, consecutiveTime int) error {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return err
	}
	var configMap map[string]interface{}
	err = yaml.Unmarshal(data, &configMap)
	if err != nil {
		return err
	}

	insMgrCfg, ok := configMap["instance-manager"].(map[string]interface{})
	if !ok {
		return fmt.Errorf("instance-manager not found")
	}
	healthCheckConfig, ok := insMgrCfg["health-check-config"].(map[string]interface{})
	if !ok {
		return fmt.Errorf("health-check-config not found")
	}

	unavailableCheckers, ok := healthCheckConfig["unavailable-checkers"].(map[string]interface{})
	if !ok {
		return fmt.Errorf("unavailable-checkers not found")
	}
	recoverHealthCfg, ok := unavailableCheckers["rest-health"].(map[string]interface{})
	if !ok {
		return fmt.Errorf("rest-health not found")
	}
	recoverHealthCfg["consecutive-time"] = consecutiveTime

	data, err = yaml.Marshal(configMap)
	if err != nil {
		return err
	}

	return os.WriteFile(configPath, data, 0644)
}

type connErrRequestBody struct {
	Mode  string `json:"mode"`
	Count int    `json:"count"`
}

func setMockConnErr(client *MockServerClient, mode string, count int) error {
	body := connErrRequestBody{
		Mode:  mode,
		Count: count,
	}
	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequest("POST", client.BaseURL+"/mock_conn_err", bytes.NewBuffer(bodyBytes))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("mock conn err returned status %d", resp.StatusCode)
	}
	return nil
}

func (c *MockServerClient) RemoveConnErr() (*http.Response, error) {
	req, err := http.NewRequest("GET", c.BaseURL+"/remove_conn_err", nil)
	if err != nil {
		return nil, err
	}
	return c.HTTPClient.Do(req)
}
