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
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/goccy/go-yaml"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gbytes"
	"github.com/onsi/gomega/gexec"
	"infer-dp-scheduler/cmd/route_server/server/options"
	commoncfg "infer-dp-scheduler/pkg/common/config"
	"infer-dp-scheduler/pkg/common/constant"
	"infer-dp-scheduler/pkg/common/errs"
	"infer-dp-scheduler/pkg/common/health"
	"infer-dp-scheduler/pkg/common/openai"
)

const (
	PDTestContext = "pd-test-context"
)

var _ = Describe(PDTestContext, Ordered, func() {
	var (
		session     *gexec.Session
		err         error
		mockServers []*gexec.Session
	)

	BeforeAll(func() {
		By("copy pipelines config")
		configPath := filepath.Join(projectRoot, "e2etest", "environment", PDTestContext, "config")
		Expect(os.MkdirAll(filepath.Join(configPath, "pipelines"), 0755)).To(BeNil())

		deployPath := filepath.Join(projectRoot, "deploy")
		Expect(copyDir(filepath.Join(deployPath, "pipelines"), filepath.Join(configPath, "pipelines"))).To(BeNil())

		By("modify route-server temp config")
		var cfg commoncfg.Config
		templateContent, err := os.ReadFile(filepath.Join(deployPath, ROUTE_SERVER_TEMPLATE))
		Expect(yaml.Unmarshal(templateContent, &cfg)).To(BeNil())
		cfg.InstanceManagerCfg.HealthCheckConfig.RestHealthCommonConfig.Interval = 2000
		cfg.InstanceManagerCfg.HealthCheckConfig.UnavailableCheckersCfg.RestHealthCfg.ConsecutiveTime = 3
		cfg.InstanceManagerCfg.HealthCheckConfig.AvailableCheckersCfg.LinkBlacklistConfig.LinkBlockWindowsMs = 100
		cfg.InstanceManagerCfg.HealthCheckConfig.AvailableCheckersCfg.LinkBlacklistConfig.LinkBlockTagValidTimeMs = 10000
		outData, err := yaml.Marshal(&cfg)
		Expect(err).To(BeNil())
		Expect(os.WriteFile(filepath.Join(deployPath, TEMP_TEMPLATE), outData, 0644)).To(BeNil())

		By("gen route-server config")
		toolParam := DisableHTTPSConfig([]string{
			"-template", filepath.Join(deployPath, TEMP_TEMPLATE),
			"-host", "127.0.0.1",
			"-target", filepath.Join(projectRoot, "e2etest", "environment", PDTestContext),
			"-infer-mode", "PD",
			"-prefills", "prefill1#127.0.0.1:8081",
			"-decodes", "decode1#127.0.0.1:8082",
			"-name", "route-server-pd-test",
			"-remote-scheduler-type", "disable",
			"-port", listenPort,
			"-mode", "singleton",
			"-infer-module-version", "pipeline",
		})
		Expect(exec.Command(configToolPath, toolParam...).Run()).To(BeNil())

		By("modify recover health config - set consecutive-time to 1")
		configFile := filepath.Join(configPath, "model-orch.yaml")
		Expect(InjectRecoverHealthConfigToYaml(configFile)).To(BeNil())

		Expect(os.Setenv(constant.EnvServerConfigPath, configPath)).To(BeNil())
		Expect(options.InitConfig()).To(BeNil())

		By("cp metrics config")
		metricsCollectorConfigFileName := options.GetConfig().MetricsCfg.MetricsCollectorConfigFileName
		Expect(copyFile(filepath.Join(deployPath, metricsCollectorConfigFileName), filepath.Join(configPath, metricsCollectorConfigFileName))).
			To(BeNil())

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
		By("cleanup temp template")
		os.Remove(filepath.Join(projectRoot, "deploy", TEMP_TEMPLATE))
	})

	BeforeEach(func() {
		// Before new test logic starts, consume all current content
		// Matching ".*" moves the read pointer to the end of current buffer
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
			clientRs = NewRSClient("http://localhost:" + listenPort)
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
			Expect(result.Code).Should(ContainSubstring("4200"))

		})

	})

	// Request ID Rewrite Tests
	// Each TC covers /v1/chat/completions and /v1/completions endpoints, both non-streaming and streaming requests
	Context("Request ID Rewrite Tests", func() {
		var clientRs *RSClient

		BeforeEach(func() {
			clientRs = NewRSClient("http://localhost:" + listenPort)
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

				// Verify response id is original value
				Expect(responseMap["id"]).To(Equal(headerXRequestId))

				// Verify log
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

				// Verify streaming response contains original id
				Expect(string(respBody)).To(ContainSubstring(headerXRequestId))

				// Verify log
				expectedRegexp := fmt.Sprintf(`original request id \[%s\] will be rewrite to`, headerXRequestId)
				Eventually(session.Out, 5*time.Second).Should(gbytes.Say(expectedRegexp))
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

				// Verify response id is original value (completions endpoint will add cmpl- prefix)
				Expect(responseMap["id"]).To(Equal("cmpl-" + headerXRequestId))

				// Verify log
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

				// Verify streaming response contains original id (completions endpoint will add cmpl- prefix)
				Expect(string(respBody)).To(ContainSubstring("cmpl-" + headerXRequestId))

				// Verify log
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

				// Verify response id is original body request_id
				Expect(responseMap["id"]).To(Equal(bodyRequestId))

				// Verify log
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

				// Verify streaming response contains original body request_id (completions endpoint will add cmpl- prefix)
				Expect(string(respBody)).To(ContainSubstring("cmpl-" + bodyRequestId))

				// Verify log
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

				// Verify streaming response contains original body request_id (completions endpoint will add cmpl- prefix)
				Expect(string(respBody)).To(ContainSubstring("cmpl-" + bodyRequestId))

				// Verify log
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
				// Verify response id is randomly generated UUID format (32-character hex)
				Expect(responseId).To(MatchRegexp(`^[0-9a-f]{32}$`))

				// Verify no rewrite record in log
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

				// Streaming response id is randomly generated (chatcmpl- prefix)
				Expect(string(respBody)).To(MatchRegexp(`"id":"chatcmpl-[0-9a-zA-Z]+"`))

				// Verify no rewrite record in log
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
				// Verify response id is randomly generated UUID format (completions endpoint will add cmpl- prefix)
				Expect(responseId).To(MatchRegexp(`^cmpl-[0-9a-f]{32}$`))

				// Verify no rewrite record in log
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

				// Streaming response id is randomly generated (completions endpoint will add cmpl- prefix)
				Expect(string(respBody)).To(MatchRegexp(`"id":"cmpl-[0-9a-f]{32}"`))

				// Verify no rewrite record in log
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

				// Verify response id is header value (body value ignored)
				Expect(responseMap["id"]).To(Equal(headerXRequestId))

				// Verify log uses header value
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

				// Verify streaming response contains header value (completions endpoint will add cmpl- prefix)
				Expect(string(respBody)).To(ContainSubstring("cmpl-" + headerXRequestId))
				Expect(string(respBody)).ToNot(ContainSubstring(bodyRequestId))

				// Verify log uses header value
				expectedRegexp := fmt.Sprintf(`original request id \[%s\] will be rewrite to`, headerXRequestId)
				notExpectedRegexp := fmt.Sprintf(`original request id \[%s\] will be rewrite to`, bodyRequestId)
				Eventually(session.Out, 5*time.Second).Should(gbytes.Say(expectedRegexp))
				Eventually(session.Out, 5*time.Second).ShouldNot(gbytes.Say(notExpectedRegexp))
			})
		})
	})

	// ================================================================
	// PD Link Down Test
	// ================================================================
	Context("PD Link Down Test", func() {
		var (
			clientRs         *RSClient
			mockDecodeClient *MockServerClient

			healthCheckIntervalMs   int
			consecutiveFailureCount int
			recoverConsecutiveCount int

			recoveryWaitTime time.Duration
		)

		BeforeAll(func() {
			healthCheckIntervalMs = options.GetConfig().InstanceManagerCfg.HealthCheckConfig.RestHealthCommonConfig.Interval
			consecutiveFailureCount = options.GetConfig().InstanceManagerCfg.HealthCheckConfig.AvailableCheckersCfg.RestHealthCfg.ConsecutiveTime
			recoverConsecutiveCount = options.GetConfig().InstanceManagerCfg.HealthCheckConfig.UnavailableCheckersCfg.RestHealthCfg.ConsecutiveTime
			// Add one buffer
			recoveryWaitTime = time.Duration(healthCheckIntervalMs*(recoverConsecutiveCount+1)) * time.Millisecond
		})

		BeforeEach(func() {

			// prefill port 8081, decode port 8082
			mockDecodeClient = NewMockServerClient("8082")
			_, err = mockDecodeClient.LinkUp()
			Expect(err).NotTo(HaveOccurred())
			Expect(restartProcess(&session)).To(BeNil())
			clientRs = NewRSClient("http://localhost:" + listenPort)
			// Wait for status to take effect
			By("wait for health check completed: move 2 to available, move 0 to unavailable")
			Eventually(session.Out, 10*time.Second).Should(gbytes.Say("move 2 to available, move 0 to unavailable"))
		})

		// -------------------------------------------------------
		// TC1: Single PD Link Down failure (not added to blacklist)
		// -------------------------------------------------------
		Context("TC1: Single PD Link Down failure (not added to blacklist)", func() {
			It("non-streaming - single link down then recovery, request returns link down error", func() {
				_, err := mockDecodeClient.LinkDown("127.0.0.1", "8082")
				Expect(err).NotTo(HaveOccurred())
				time.Sleep(500 * time.Millisecond)

				// Verify request returns 500 (link down error in response body)
				resp, err := clientRs.SendChatCompletion(context.Background(), NewBasicChatRequestBody(), nil)
				Expect(err).NotTo(HaveOccurred())
				Expect(resp.StatusCode).To(Equal(http.StatusInternalServerError))

				// Read response body, check if kv_transfer_params is included (link down error flag)
				defer resp.Body.Close()
				respBody, _ := io.ReadAll(resp.Body)
				respStr := string(respBody)
				// When link is down, response body contains kv_transfer_params field
				Expect(respStr).To(ContainSubstring("Abort for KV loading failure"))

				// Recover link
				_, err = mockDecodeClient.LinkUp()
				Expect(err).NotTo(HaveOccurred())
				time.Sleep(500 * time.Millisecond)

				// Verify request succeeds after recovery
				resp, err = clientRs.SendChatCompletion(context.Background(), NewBasicChatRequestBody(), nil)
				Expect(err).NotTo(HaveOccurred())
				Expect(resp.StatusCode).To(Equal(http.StatusOK))

				var result openai.ChatCompletionResponse
				Expect(ParseResponse(resp, &result)).NotTo(HaveOccurred())
				Expect(result.Choices).NotTo(BeEmpty())
			})

			It("streaming - single link down then recovery, request returns link down error", func() {
				body := map[string]interface{}{
					"model":      "pangu",
					"messages":   []map[string]string{{"role": "user", "content": "hello"}},
					"max_tokens": 10,
					"stream":     true,
				}

				// Set link down
				_, err := mockDecodeClient.LinkDown("127.0.0.1", "8082")
				Expect(err).NotTo(HaveOccurred())
				time.Sleep(500 * time.Millisecond)

				// Verify request returns 200 (link down error in response body)
				resp, err := clientRs.SendChatCompletion(context.Background(), body, nil)
				Expect(err).NotTo(HaveOccurred())
				Expect(resp.StatusCode).To(Equal(http.StatusOK))

				// Check response body contains link down related info
				defer resp.Body.Close()
				respBody, _ := io.ReadAll(resp.Body)
				respStr := string(respBody)
				// When link is down in streaming, error info is included in response
				Expect(respStr).To(Or(ContainSubstring("link"), ContainSubstring("PDLinkDownError"), ContainSubstring("kv_transfer_params")))

				// Recover link
				_, err = mockDecodeClient.LinkUp()
				Expect(err).NotTo(HaveOccurred())
				time.Sleep(500 * time.Millisecond)

				// Verify request succeeds after recovery
				resp, err = clientRs.SendChatCompletion(context.Background(), body, nil)
				Expect(err).NotTo(HaveOccurred())
				Expect(resp.StatusCode).To(Equal(http.StatusOK))

				respBody, _ = io.ReadAll(resp.Body)
				Expect(string(respBody)).To(ContainSubstring("data:"))
			})
		})

		// -------------------------------------------------------
		// TC2: Multiple consecutive PD Link Down failures (added to blacklist)
		// -------------------------------------------------------
		Context("TC2: Multiple consecutive PD Link Down failures", func() {
			It("non-streaming - 3 consecutive link downs, verify logs and normal after recovery", func() {
				_, err := mockDecodeClient.LinkDown("127.0.0.1", "8082")
				Expect(err).NotTo(HaveOccurred())
				// Trigger consecutiveFailureCount times link down
				for i := 0; i < consecutiveFailureCount; i++ {

					time.Sleep(200 * time.Millisecond)
					// After each trigger, verify request returns non-200 (link down error in response body)
					resp, err := clientRs.SendChatCompletion(context.Background(), NewBasicChatRequestBody(), nil)
					Expect(err).NotTo(HaveOccurred())
					Expect(resp.StatusCode).To(Equal(http.StatusInternalServerError))

					// Check response body contains kv_transfer_params (link down error flag)
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

				// Check metrics
				resp, err = clientRs.GetInternalMetrics()
				Expect(err).NotTo(HaveOccurred())
				Expect(resp.StatusCode).To(Equal(http.StatusOK))
				Expect(ExtractMetricValue(resp,
					"rs:number_faulty_endpoints", map[string]string{"fault_type": health.LinkBlacklist, "role": "decode"})).
					To(Equal(1.0))

				// Recover link
				_, err = mockDecodeClient.LinkUp()
				Expect(err).NotTo(HaveOccurred())
				time.Sleep(recoveryWaitTime)

				// Verify request succeeds after recovery
				resp, err = clientRs.SendChatCompletion(context.Background(), NewBasicChatRequestBody(), nil)
				Expect(err).NotTo(HaveOccurred())
				Expect(resp.StatusCode).To(Equal(http.StatusOK))

				var result openai.ChatCompletionResponse
				Expect(ParseResponse(resp, &result)).NotTo(HaveOccurred())
				Expect(result.Choices).NotTo(BeEmpty())
			})

			It("streaming - 3 consecutive link downs added to blacklist, normal after recovery", func() {
				body := map[string]interface{}{
					"model":      "pangu",
					"messages":   []map[string]string{{"role": "user", "content": "hello"}},
					"max_tokens": 10,
					"stream":     true,
				}

				// Trigger consecutiveFailureCount times link down
				for i := 0; i < consecutiveFailureCount; i++ {
					_, err := mockDecodeClient.LinkDown("127.0.0.1", "8082")
					Expect(err).NotTo(HaveOccurred())
					time.Sleep(200 * time.Millisecond)

					// Streaming returns 200, link down info in response body
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

				// Recover link
				_, err = mockDecodeClient.LinkUp()
				Expect(err).NotTo(HaveOccurred())
				time.Sleep(recoveryWaitTime)

				// Verify request succeeds after recovery
				resp, err = clientRs.SendChatCompletion(context.Background(), body, nil)
				Expect(err).NotTo(HaveOccurred())
				Expect(resp.StatusCode).To(Equal(http.StatusOK))

				defer resp.Body.Close()
				respBody, _ = io.ReadAll(resp.Body)
				Expect(string(respBody)).To(ContainSubstring("data:"))
			})
		})

	})

	Context("RS Link Health Check Test", func() {
		// Health check config constants - should match config file
		var (
			clientRs          *RSClient
			mockPrefillClient *MockServerClient
			mockDecodeClient  *MockServerClient

			healthCheckIntervalMs   int
			consecutiveFailureCount int
			recoverConsecutiveCount int
			maxFailureWaitTime      time.Duration
			recoveryWaitTime        time.Duration
		)

		BeforeAll(func() {
			healthCheckIntervalMs = options.GetConfig().InstanceManagerCfg.HealthCheckConfig.RestHealthCommonConfig.Interval
			consecutiveFailureCount = options.GetConfig().InstanceManagerCfg.HealthCheckConfig.AvailableCheckersCfg.RestHealthCfg.ConsecutiveTime
			recoverConsecutiveCount = options.GetConfig().InstanceManagerCfg.HealthCheckConfig.UnavailableCheckersCfg.RestHealthCfg.ConsecutiveTime
			// Add 5s buffer
			maxFailureWaitTime = time.Duration(healthCheckIntervalMs*consecutiveFailureCount+5000) * time.Millisecond
			// Add one buffer
			recoveryWaitTime = time.Duration(healthCheckIntervalMs*(recoverConsecutiveCount+1)) * time.Millisecond
		})

		BeforeEach(func() {
			clientRs = NewRSClient("http://localhost:" + listenPort)
			// According to pd_test.go config, prefill port is 8081, decode port is 8082
			mockPrefillClient = NewMockServerClient("8081")
			mockDecodeClient = NewMockServerClient("8082")

			// Ensure all instances start with healthy status
			_, err := mockPrefillClient.MockHealth()
			Expect(err).NotTo(HaveOccurred())
			_, err = mockDecodeClient.MockHealth()
			Expect(err).NotTo(HaveOccurred())

			// Wait for RS health check to take effect
			time.Sleep(1 * time.Second)
		})

		It("verify RS healthy status - returns 200 when all instances are healthy", func() {
			// Check overall health status via RS /health endpoint
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

		It("verify RS fault status detection - RS returns 500 when Mock instance is faulty, RS returns 200 after recovery", func() {
			// Set one Mock instance to unhealthy status
			_, err := mockPrefillClient.MockUnHealth()
			Expect(err).NotTo(HaveOccurred())

			// Use config constants to calculate wait time
			time.Sleep(maxFailureWaitTime)

			// RS should detect fault and return non-200 status
			resp, err := clientRs.CheckRSHealth()
			Expect(err).NotTo(HaveOccurred())
			Expect(resp.StatusCode).To(Equal(http.StatusInternalServerError))
			resp.Body.Close()

			var result openai.ChatCompletionResponse

			// Recover Mock instance health status
			_, err = mockPrefillClient.MockHealth()
			Expect(err).NotTo(HaveOccurred())

			// Wait for RS health check to detect recovery (requires consecutive successes, interval 10s)
			time.Sleep(recoveryWaitTime)

			// RS should recover healthy status
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
})

func InjectRecoverHealthConfigToYaml(configPath string) error {
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
	recoverHealthCfg["consecutive-time"] = 1

	data, err = yaml.Marshal(configMap)
	if err != nil {
		return err
	}

	return os.WriteFile(configPath, data, 0644)
}
