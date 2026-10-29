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

	"infer-dp-scheduler/cmd/route_server/server/options"
	"infer-dp-scheduler/pkg/common/constant"
	"infer-dp-scheduler/pkg/common/openai"
)

const (
	LayerwiseCPCDTestContext = "layerwise-cpcd-test-context"
	cpcdListenPort           = "30600"
	cpcdOpsPort              = "30610"
	cpcdPrefillPort          = "30601"
	cpcdDecodePort           = "30602"
)

var _ = Describe(LayerwiseCPCDTestContext, Ordered, func() {
	var (
		session     *gexec.Session
		err         error
		mockServers = make(map[string]*gexec.Session)
	)

	BeforeAll(func() {
		EnsurePorts([]string{cpcdListenPort, cpcdOpsPort, cpcdPrefillPort, cpcdDecodePort})
		By("copy pipelines config")
		configPath := filepath.Join(projectRoot, "e2etest", "environment", LayerwiseCPCDTestContext, "config")
		Expect(os.MkdirAll(filepath.Join(configPath, "pipelines"), 0755)).To(BeNil())

		deployPath := filepath.Join(projectRoot, "deploy")
		Expect(copyDir(filepath.Join(deployPath, "pipelines"), filepath.Join(configPath, "pipelines"))).To(BeNil())

		By("gen route-server config")
		toolParam := DisableHTTPSConfig([]string{
			"-template", filepath.Join(deployPath, ROUTE_SERVER_TEMPLATE),
			"-host", "127.0.0.1",
			"-target", filepath.Join(projectRoot, "e2etest", "environment", LayerwiseCPCDTestContext),
			"-infer-mode", "Layerwise-CPCD",
			"-prefills", fmt.Sprintf("prefill1#127.0.0.1:%s", cpcdPrefillPort),
			"-decodes", fmt.Sprintf("decode1#127.0.0.1:%s", cpcdDecodePort),
			"-name", "route-server-layerwise-cpcd-test",
			"-remote-scheduler-type", "disable",
			"-port", cpcdListenPort,
			"-infer-module-version", "pipeline",
		})
		Expect(exec.Command(configToolPath, toolParam...).Run()).To(BeNil())

		By("modify layerwise config to enable token reuse")
		metaserverConfig := filepath.Join(configPath, "pipelines", "metaserver_cpcd.yaml")
		Expect(modifyMetaserverCPCDConfigForStream(metaserverConfig)).To(BeNil())

		Expect(os.Setenv(constant.EnvServerConfigPath, configPath)).To(BeNil())
		Expect(options.InitConfig()).To(BeNil())

		By("cp metrics config")
		metricsCollectorConfigFileName := options.GetConfig().MetricsCfg.MetricsCollectorConfigFileName
		Expect(copyFile(filepath.Join(deployPath, metricsCollectorConfigFileName), filepath.Join(configPath, metricsCollectorConfigFileName))).
			To(BeNil())

		By("start layerwise mock vllm")
		for name, instance := range options.GetConfig().InstanceManagerCfg.RouteTable.InstanceRoutes {
			var params []string
			params = append(params,
				"-addr", instance.Ip,
				"-port", fmt.Sprintf("%d", instance.Port))

			command := exec.Command(mockVLLMPath, params...)
			mockSession, err := gexec.Start(command, io.Discard, os.Stderr)
			Expect(err).NotTo(HaveOccurred())
			fmt.Println("start mock vllm port", instance.Port)
			mockServers[name] = mockSession
		}

		By("start route-server")
		command := exec.Command(rsPath)
		session, err = gexec.Start(command, io.Discard, os.Stderr)
		Expect(err).NotTo(HaveOccurred())

		By("wait for readiness")
		Eventually(session.Out, 10*time.Second).Should(gbytes.Say("Start HTTP server on"))
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

	Context("Basic Test", func() {
		var (
			clientRs *RSClient
			spanId   string
			reqBody  openai.ChatCompletionRequestV1
		)

		BeforeEach(func() {
			clientRs = NewRSClient("http://localhost:" + cpcdListenPort)
			spanId = uuid.NewString()
			reqBody = openai.ChatCompletionRequestV1{
				Model: "pangu",
				Messages: []openai.ChatCompletionMessage{{
					Role:    "user",
					Content: "hello layerwise cpcd",
				}},
				Stream: false,
			}
		})

		It("chat/completions non-stream returns source first token", func() {
			resp, err := clientRs.SendChatCompletion(context.Background(), reqBody, map[string]string{
				constant.HeaderXSpanId: spanId,
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(resp.StatusCode).To(Equal(http.StatusOK))
			defer resp.Body.Close()

			// Verify response format
			var response openai.ChatCompletionResponse
			err = json.NewDecoder(resp.Body).Decode(&response)
			Expect(err).NotTo(HaveOccurred())
			Expect(len(response.Choices)).To(BeNumerically(">", 0))
			Expect(response.Choices[0].Message.Content).NotTo(BeEmpty())
		})

		It("chat/completions stream returns source first token", func() {
			reqBody.Stream = true
			resp, err := clientRs.SendChatCompletion(context.Background(), reqBody, map[string]string{
				constant.HeaderXSpanId: spanId,
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(resp.StatusCode).To(Equal(http.StatusOK))
			defer resp.Body.Close()

			// Verify streaming response format
			body, err := io.ReadAll(resp.Body)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(body)).To(ContainSubstring("data: "))
		})

		It("completions non-stream returns source first token", func() {
			resp, err := clientRs.SendCompletion(context.Background(), reqBody, map[string]string{
				constant.HeaderXSpanId: spanId,
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(resp.StatusCode).To(Equal(http.StatusOK))
			defer resp.Body.Close()

			// Verify response format
			var response openai.CompletionResponse
			err = json.NewDecoder(resp.Body).Decode(&response)
			Expect(err).NotTo(HaveOccurred())
			Expect(len(response.Choices)).To(BeNumerically(">", 0))
			Expect(response.Choices[0].Text).NotTo(BeEmpty())
		})

		It("completions stream returns source first token", func() {
			reqBody.Stream = true
			resp, err := clientRs.SendCompletion(context.Background(), reqBody, map[string]string{
				constant.HeaderXSpanId: spanId,
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(resp.StatusCode).To(Equal(http.StatusOK))
			defer resp.Body.Close()

			// Verify streaming response format
			body, err := io.ReadAll(resp.Body)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(body)).To(ContainSubstring("data: "))
		})
	})

	Context("Finish Reason Log Verification", func() {
		var (
			clientRs          *RSClient
			mockPrefillClient *MockServerClient
		)

		BeforeEach(func() {
			clientRs = NewRSClient("http://localhost:" + cpcdListenPort)
			mockPrefillClient = NewMockServerClient(cpcdPrefillPort)
		})

		It("finish reason: when max_tokens=2, finish_reason is length", func() {
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

		It("finish reason: without max_tokens, finish_reason is stop", func() {
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

		It("finish reason: with tools, finish_reason is tool_calls", func() {
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

		It("finish reason: when prefill returns 500, finish_reason is unknown", func() {
			By("set prefill1 instance to return 500 error")
			errBody := strings.NewReader(`{"code": 500}`)
			req, err := http.NewRequest("POST", fmt.Sprintf("http://127.0.0.1:%s/mock_error", cpcdPrefillPort), errBody)
			Expect(err).NotTo(HaveOccurred())
			req.Header.Set("Content-Type", "application/json")
			resp, err := mockPrefillClient.HTTPClient.Do(req)
			Expect(err).NotTo(HaveOccurred())
			resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusOK))

			DeferCleanup(func() {
				mockPrefillClient.RemoveError()
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

			expectedRegexp := fmt.Sprintf(`request_id.*.%s.*finish_reason = unknown.*err_stage = Prefill`, reqId)
			Eventually(session.Out, 10*time.Second).Should(gbytes.Say(expectedRegexp))
		})
	})

})

// modifyMetaserverCPCDConfigForStream modifies metaserver CPCD config to enable stream mode
func modifyMetaserverCPCDConfigForStream(configPath string) error {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return err
	}
	var configMap map[string]interface{}
	err = yaml.Unmarshal(data, &configMap)
	if err != nil {
		return err
	}

	// Ensure use of stream functions
	pipelineCfg, ok := configMap["pipeline_cfg"].(map[string]interface{})
	if !ok {
		return fmt.Errorf("pipeline_cfg not found")
	}

	phases, ok := pipelineCfg["phases"].([]interface{})
	if !ok {
		return fmt.Errorf("phases not found")
	}

	// Modify layerwise_prefill_execute phase
	for _, phase := range phases {
		phaseMap, ok := phase.(map[string]interface{})
		if !ok {
			continue
		}
		if phaseMap["name"] == "layerwise_prefill_execute" {
			executeParams, ok := phaseMap["execute_params"].(map[string]interface{})
			if !ok {
				continue
			}
			// Ensure use of stream version
			preProcessFuncs, ok := executeParams["pre_process_request_funcs"].([]interface{})
			if ok {
				for i, funcName := range preProcessFuncs {
					if funcName == "prefill_prepare_request" {
						preProcessFuncs[i] = "prefill_stream_prepare_request"
					}
				}
			}
			postProcessFuncs, ok := executeParams["post_process_funcs"].([]interface{})
			if ok {
				for i, funcName := range postProcessFuncs {
					if funcName == "prefill_post_response" {
						postProcessFuncs[i] = "prefill_stream_post_response"
					}
				}
			}
			break
		}
	}

	data, err = yaml.Marshal(configMap)
	if err != nil {
		return err
	}

	return os.WriteFile(configPath, data, 0644)
}
