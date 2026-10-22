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
	"infer-dp-scheduler/pkg/common/constant"
	"infer-dp-scheduler/pkg/common/openai"
)

const (
	EPDTestContext = "epd-test-context"

	listenPort = "39876"
)

var _ = Describe(EPDTestContext, Ordered, func() {
	var (
		session     *gexec.Session
		err         error
		mockServers = make(map[string]*gexec.Session)
	)

	BeforeAll(func() {
		By("copy pipelines config")
		configPath := filepath.Join(projectRoot, "e2etest", "environment", EPDTestContext, "config")
		Expect(os.MkdirAll(filepath.Join(configPath, "pipelines"), 0755)).To(BeNil())

		deployPath := filepath.Join(projectRoot, "deploy")
		Expect(copyDir(filepath.Join(deployPath, "pipelines"), filepath.Join(configPath, "pipelines"))).To(BeNil())

		By("gen route-server config")
		toolParam := DisableHTTPSConfig([]string{
			"-template", filepath.Join(deployPath, ROUTE_SERVER_TEMPLATE),
			"-host", "127.0.0.1",
			"-target", filepath.Join(projectRoot, "e2etest", "environment", EPDTestContext),
			"-infer-mode", "EPD",
			"-encodes", "encode1#127.0.0.1:8080",
			"-prefills", "prefill1#127.0.0.1:8081",
			"-decodes", "decode1#127.0.0.1:8082",
			"-name", "route-server-epd-test",
			"-remote-scheduler-type", "disable",
			"-port", listenPort,
			"-infer-module-version", "pipeline",
		})
		Expect(exec.Command(configToolPath, toolParam...).Run()).To(BeNil())
		Expect(InjectConfigToYaml(filepath.Join(configPath, "pipelines", "epd.yaml"))).To(BeNil())

		Expect(os.Setenv(constant.EnvServerConfigPath, configPath)).To(BeNil())
		Expect(options.InitConfig()).To(BeNil())

		By("cp metrics config")
		metricsCollectorConfigFileName := options.GetConfig().MetricsCfg.MetricsCollectorConfigFileName
		Expect(copyFile(filepath.Join(deployPath, metricsCollectorConfigFileName), filepath.Join(configPath, metricsCollectorConfigFileName))).
			To(BeNil())

		By("start epd mock vllm")
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
		session, err = gexec.Start(command, GinkgoWriter, os.Stderr)
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

		It("without an image, should return 200 OK and contain choices, E phase will output a skip log", func() {
			resp, err = clientRs.SendChatCompletion(TimeoutCtx(context.Background(), 30*time.Second), reqBody, extraHeaders)
			Expect(err).NotTo(HaveOccurred())
			Expect(resp.StatusCode).To(Equal(http.StatusOK))

			var result openai.ChatCompletionResponse
			err := ParseResponse(resp, &result)
			Expect(err).NotTo(HaveOccurred())
			Expect(result.Choices).NotTo(BeEmpty())

			Eventually(session.Out, time.Second).Should(gbytes.Say("Encode_skip.*%s", spanId))

		})

		It("with an image, should return 200 OK and contain choices, E phase has no skip log", func() {
			reqBody.Messages = []openai.MMChatCompletionMessage{{
				Role:    "user",
				Content: NewMockMultimodalContent(),
			},
			}
			resp, err = clientRs.SendChatCompletion(TimeoutCtx(context.Background(), 30*time.Second), reqBody, extraHeaders)
			Expect(err).NotTo(HaveOccurred())
			Expect(resp.StatusCode).To(Equal(http.StatusOK))

			var result openai.ChatCompletionResponse
			err := ParseResponse(resp, &result)
			Expect(err).NotTo(HaveOccurred())

			Eventually(session.Out, time.Second).ShouldNot(gbytes.Say("Encode_skip.*%s", spanId))

		})

		It("encode latency > 10S, set timeout 5s, verify timeout takes effect", func() {
			reqBody.Messages = []openai.MMChatCompletionMessage{{
				Role:    "user",
				Content: NewMockMultimodalContent(),
			},
			}

			// encoder ttft set to 10s, exceeds timeout of 5s
			encoder1 := mockServers["encode1"]
			params := encoder1.Command.Args
			params = append(params, "-ttft", "10000")
			encoder1.Kill()
			encoder1.Wait()
			command := exec.Command(mockVLLMPath, params[1:]...)
			mockServers["encode1"], err = gexec.Start(command, io.Discard, os.Stderr)
			Expect(err).NotTo(HaveOccurred())
			time.Sleep(time.Second)
			start := time.Now()
			resp, err = clientRs.SendChatCompletion(TimeoutCtx(context.Background(), 30*time.Second), reqBody, extraHeaders)
			Expect(err).NotTo(HaveOccurred())

			var result openai.ChatCompletionResponse
			err := ParseResponse(resp, &result)
			Expect(err).NotTo(HaveOccurred())
			latency := time.Now().Sub(start).Seconds()

			Expect(latency).To(BeNumerically(">=", 5))
			Expect(latency).To(BeNumerically("<", 10))

		})
	})
})

func InjectConfigToYaml(filePath string) error {
	targetPhaseName := "encode_execute"
	key := "req_timeout_ms"
	value := 5000
	data, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("failed to read file: %w", err)
	}

	var root map[string]interface{}
	if err := yaml.Unmarshal(data, &root); err != nil {
		return fmt.Errorf("failed to unmarshal yaml: %w", err)
	}

	pipelineCfg, ok := root["pipeline_cfg"].(map[string]interface{})
	if !ok {
		return fmt.Errorf("pipeline_cfg not found or not a map")
	}

	phases, ok := pipelineCfg["phases"].([]interface{})
	if !ok {
		return fmt.Errorf("phases not found or not a slice")
	}

	found := false
	for _, p := range phases {
		phaseMap, ok := p.(map[string]interface{})
		if !ok {
			continue
		}

		if name, ok := phaseMap["name"].(string); ok && name == targetPhaseName {
			var configMap map[string]interface{}
			if existingConfig, exists := phaseMap["config"]; exists {
				if m, ok := existingConfig.(map[string]interface{}); ok {
					configMap = m
				} else {
					configMap = make(map[string]interface{})
				}
			} else {
				configMap = make(map[string]interface{})
			}

			configMap[key] = value
			phaseMap["config"] = configMap
			found = true
			break
		}
	}

	if !found {
		return fmt.Errorf("target phase '%s' not found", targetPhaseName)
	}

	out, err := yaml.Marshal(root)
	if err != nil {
		return fmt.Errorf("failed to marshal yaml: %w", err)
	}

	if err := os.WriteFile(filePath, out, 0644); err != nil {
		return fmt.Errorf("failed to write file: %w", err)
	}

	return nil
}
