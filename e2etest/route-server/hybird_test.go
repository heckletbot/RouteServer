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
)

var _ = Describe(HybridTestContext, Ordered, func() {
	var (
		session     *gexec.Session
		err         error
		mockServers []*gexec.Session
	)

	BeforeAll(func() {
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
			"-decodes", "decode1#127.0.0.1:8082",
			"-name", "route-server-hybrid-test",
			"-remote-scheduler-type", "disable",
			"-port", listenPort,
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

	})
})
