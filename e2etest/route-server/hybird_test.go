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
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gbytes"
	"github.com/onsi/gomega/gexec"
	"infer-dp-scheduler/cmd/route_server/server/options"
	"infer-dp-scheduler/pkg/common/constant"
	"infer-dp-scheduler/pkg/common/openai"
)

const (
	HybridTestContext = "hybrid-test-context"
	hybridListenPort  = "30300"
	hybridOpsPort     = "30310"
	hybridDecodePort  = "30301"
)

var _ = Describe(HybridTestContext, Ordered, func() {
	var (
		session     *gexec.Session
		err         error
		mockServers []*gexec.Session
	)

	BeforeAll(func() {
		EnsurePorts([]string{hybridListenPort, hybridOpsPort, hybridDecodePort})

		By("copy pipelines config")
		configPath := filepath.Join(projectRoot, "e2etest", "environment", HybridTestContext, "config")
		Expect(os.MkdirAll(filepath.Join(configPath, "pipelines"), 0755)).To(BeNil())

		deployPath := filepath.Join(projectRoot, "deploy")
		Expect(copyDir(filepath.Join(deployPath, "pipelines"), filepath.Join(configPath, "pipelines"))).To(BeNil())

		By("gen route-server config")
		toolParam := DisableHTTPSConfig([]string{
			"-template", filepath.Join(deployPath, ROUTE_SERVER_TEMPLATE),
			"-host", "127.0.0.1",
			"-target", filepath.Join(projectRoot, "e2etest", "environment", HybridTestContext),
			"-infer-mode", "Hybrid",
			"-decodes", fmt.Sprintf("decode1#127.0.0.1:%s", hybridDecodePort),
			"-name", "route-server-hybrid-test",
			"-remote-scheduler-type", "disable",
			"-port", hybridListenPort,
			"-infer-module-version", "pipeline",
		})
		Expect(exec.Command(configToolPath, toolParam...).Run()).To(BeNil())

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
			clientRs = NewRSClient("http://localhost:" + hybridListenPort)
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

		Context("Finish Reason Log Verification Test", func() {
			var (
				clientRs         *RSClient
				mockDecodeClient *MockServerClient
			)

			BeforeEach(func() {
				clientRs = NewRSClient("http://localhost:" + hybridListenPort)
				mockDecodeClient = NewMockServerClient(hybridDecodePort)
			})

			It("finish reason verification: when max_tokens=2, finish_reason is length", func() {
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

			It("finish reason verification: when no max_tokens, finish_reason is stop", func() {
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

			It("finish reason verification: with tools, finish_reason is tool_calls", func() {
				reqId := "test-finish-tool"
				body := map[string]interface{}{
					"model":      "pangu",
					"max_tokens": 12,
					"messages":   []map[string]string{{"role": "user", "content": "Get Beijing weather in Celsius"}},
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

			It("finish reason verification: when decode returns 500, finish_reason is unknown", func() {
				By("set decode1 instance to return 500 error")
				errBody := strings.NewReader(`{"code": 500}`)
				req, err := http.NewRequest("POST", fmt.Sprintf("http://127.0.0.1:%s/mock_error", hybridDecodePort), errBody)
				Expect(err).NotTo(HaveOccurred())
				req.Header.Set("Content-Type", "application/json")
				resp, err := mockDecodeClient.HTTPClient.Do(req)
				Expect(err).NotTo(HaveOccurred())
				resp.Body.Close()
				Expect(resp.StatusCode).To(Equal(http.StatusOK))

				DeferCleanup(func() {
					mockDecodeClient.RemoveError()
				})

				By("send request and verify finish_reason = unknown in logs")
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

				expectedRegexp := fmt.Sprintf(`request_id.*.%s.*finish_reason = engine.*failure_type = Decode`, reqId)
				Eventually(session.Out, 10*time.Second).Should(gbytes.Say(expectedRegexp))
			})
		})
	})
})
