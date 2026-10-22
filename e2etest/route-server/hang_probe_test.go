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
)

const (
	HangProbeTestContext = "hang-probe-test-context"
	hangProbeListenPort  = "30200"
	hangProbOpsPort      = "30210"
	hangEncodePort       = "30201"
	hangPrefillPort      = "30202"
	hangDecodePort       = "30203"
)

var _ = Describe(HangProbeTestContext, Ordered, func() {
	var (
		session     *gexec.Session
		err         error
		mockServers = make(map[string]*gexec.Session)
	)

	BeforeAll(func() {
		EnsurePorts([]string{hangProbeListenPort, hangProbOpsPort, hangEncodePort, hangPrefillPort, hangDecodePort})
		By("copy pipelines config")
		configPath := filepath.Join(projectRoot, "e2etest", "environment", HangProbeTestContext, "config")
		Expect(os.MkdirAll(filepath.Join(configPath, "pipelines"), 0755)).To(BeNil())

		deployPath := filepath.Join(projectRoot, "deploy")
		Expect(copyDir(filepath.Join(deployPath, "pipelines"), filepath.Join(configPath, "pipelines"))).To(BeNil())

		By("gen route-server config with hang probe enabled")
		toolParam := DisableHTTPSConfig([]string{
			"-template", filepath.Join(deployPath, "route-server-template.yaml"),
			"-host", "127.0.0.1",
			"-target", filepath.Join(projectRoot, "e2etest", "environment", HangProbeTestContext),
			"-infer-mode", "PD",
			"-encodes", fmt.Sprintf("encode1#127.0.0.1:%s", hangEncodePort),
			"-prefills", fmt.Sprintf("prefill1#127.0.0.1:%s", hangPrefillPort),
			"-decodes", fmt.Sprintf("decode1#127.0.0.1:%s", hangDecodePort),
			"-name", "route-server-hang-probe-test",
			"-remote-scheduler-type", "disable",
			"-port", hangProbeListenPort,
			"-infer-module-version", "pipeline",
		})
		Expect(exec.Command(configToolPath, toolParam...).Run()).To(BeNil())

		// Modify config to enable pre-hang probe feature
		configFile := filepath.Join(configPath, "route-server.yaml")
		Expect(InjectHangProbeConfigToYaml(configFile)).To(BeNil())

		Expect(os.Setenv(constant.EnvServerConfigPath, configPath)).To(BeNil())
		Expect(options.InitConfig()).To(BeNil())

		By("cp metrics config")
		metricsCollectorConfigFileName := options.GetConfig().MetricsCfg.MetricsCollectorConfigFileName
		Expect(copyFile(filepath.Join(deployPath, metricsCollectorConfigFileName), filepath.Join(configPath, metricsCollectorConfigFileName))).
			To(BeNil())

		By("start mock vllm")
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

	Context("Pre-hang Probe Feature Test", func() {
		var (
			clientRs *RSClient
			spanId   string
		)

		BeforeEach(func() {
			clientRs = NewRSClient("http://localhost:" + hangProbeListenPort)
			spanId = uuid.NewString()
		})

		// Test case 1: Instance truly hangs, trigger isolation
		It("real hang scenario - instance should be isolated", func() {
			GinkgoWriter.Println("Step 1: Send normal request to verify instance is healthy")
			reqBody := map[string]interface{}{
				"model": "pangu",
				"messages": []map[string]string{{
					"role":    "user",
					"content": "test",
				}},
			}
			resp, err := clientRs.SendChatCompletion(TimeoutCtx(context.Background(), 30*time.Second), reqBody, map[string]string{
				constant.HeaderXSpanId: spanId,
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(resp.StatusCode).To(Equal(http.StatusOK))
			time.Sleep(2 * time.Second)

			GinkgoWriter.Println("Step 2: Set Decode instance to Hang state")
			resp, err = http.Get(fmt.Sprintf("http://127.0.0.1:%s/hang", hangDecodePort))
			Expect(err).NotTo(HaveOccurred())
			resp.Body.Close()
			fmt.Println("Set decode1 to hang state")

			GinkgoWriter.Println("Step 3: Send streaming request to trigger hang")
			time.Sleep(5 * time.Second)
			spanId2 := uuid.NewString()
			reqBody2 := map[string]interface{}{
				"model": "pangu",
				"messages": []map[string]string{{
					"role":    "user",
					"content": "hello again",
				}},
				"max_tokens": 10,
				"stream":     true,
			}
			resp2, err := clientRs.SendChatCompletion(TimeoutCtx(context.Background(), 30*time.Second), reqBody2, map[string]string{
				constant.HeaderXSpanId: spanId2,
			})

			GinkgoWriter.Println("Step 4: Verify isolation triggered")
			Eventually(session.Out, 30*time.Second).Should(gbytes.Say("\\[HangCheck\\] instance .* is hang"))
			Eventually(session.Out, 5*time.Second).Should(gbytes.Say("unhealthy instances found for role \\[decode\\]"))

			GinkgoWriter.Println("Step 5: Verify no repeated probes after isolation (within 30s there should be no more hang probe requests)")
			Consistently(session.Out, 30*time.Second).ShouldNot(gbytes.Say("\\[HangProbe\\] instance .* start async probe inference request"))

			GinkgoWriter.Println("Step 6: After hang request returns, should no longer consider it hanging")
			resp2.Body.Close()
			Eventually(session.Out, 5*time.Second).Should(gbytes.Say("no request on instance .*, this worker think it is not hanging now"))
		})

		// Test case 2: Instance recovers from hang
		It("instance recovers from hang - should become healthy", func() {
			GinkgoWriter.Println("Step 1: Confirm instance is currently in unhealthy state waiting for recovery")
			Eventually(session.Out, 20*time.Second).Should(gbytes.Say("\\[recover\\] send probe infer request to instance .* error, reset ConsecutiveHealthSuccess to 0"))

			GinkgoWriter.Println("Step 2: Set Decode instance to Hang state")
			resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%s/hang", hangDecodePort))
			Expect(err).NotTo(HaveOccurred())
			resp.Body.Close()
			time.Sleep(2 * time.Second)
			Eventually(session.Out, 5*time.Second).ShouldNot(gbytes.Say("instance .* is back online due to healthcheck success, previously in fault status"))

			GinkgoWriter.Println("Step 3: Call /unhang endpoint to recover instance")
			resp, err = http.Get(fmt.Sprintf("http://127.0.0.1:%s/unhang", hangDecodePort))
			Expect(err).NotTo(HaveOccurred())
			resp.Body.Close()
			fmt.Println("Set decode1 to unhang state")

			GinkgoWriter.Println("Step 4: Wait for health check recovery")
			Eventually(session.Out, 30*time.Second).Should(gbytes.Say("instance .* is back online due to healthcheck success, previously in fault status"))

			GinkgoWriter.Println("Step 5: Send request to verify instance has recovered")
			spanId3 := uuid.NewString()
			reqBody := map[string]interface{}{
				"model": "pangu",
				"messages": []map[string]string{{
					"role":    "user",
					"content": "recovered",
				}},
				"stream": true,
			}
			resp, err = clientRs.SendChatCompletion(context.Background(), reqBody, map[string]string{
				constant.HeaderXSpanId: spanId3,
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(resp.StatusCode).To(Equal(http.StatusOK))
			_, err = io.ReadAll(resp.Body)
			Expect(err).NotTo(HaveOccurred())
		})

		// Test case 3: False hang scenario - only specific request_id hangs, probe succeeds and should not isolate
		It("false hang scenario - instance that can respond to probe request should not be isolated", func() {
			GinkgoWriter.Println("Step 1: Send normal request to verify instance is healthy")
			reqBody := map[string]interface{}{
				"model": "pangu",
				"messages": []map[string]string{{
					"role":    "user",
					"content": "test",
				}},
			}
			resp, err := clientRs.SendChatCompletion(context.Background(), reqBody, map[string]string{
				constant.HeaderXSpanId: spanId,
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(resp.StatusCode).To(Equal(http.StatusOK))
			time.Sleep(2 * time.Second)

			GinkgoWriter.Printf("%s, Step 2: Set up false hang scenario - set specific request_id to hang\n", time.Now().String())
			testRequestId := "test-false-hang-" + uuid.NewString()
			resp, err = http.Get(fmt.Sprintf("http://127.0.0.1:%s/hang_request?request_id=%s", hangDecodePort, testRequestId))
			Expect(err).NotTo(HaveOccurred())
			resp.Body.Close()
			fmt.Printf("Set request_id %s to hang state\n", testRequestId)
			time.Sleep(1 * time.Second)

			GinkgoWriter.Printf("%s, Step 3: Send request to trigger hang (using context with timeout)\n", time.Now().String())
			reqBody2 := map[string]interface{}{
				"model": "pangu",
				"messages": []map[string]string{{
					"role":    "user",
					"content": "trigger hang",
				}},
				"max_tokens": 10,
				"stream":     true,
			}
			resp2, err := clientRs.SendChatCompletion(TimeoutCtx(context.Background(), 30*time.Second), reqBody2, map[string]string{
				constant.HeaderXSpanId:     testRequestId,
				constant.HeadersXRequestID: testRequestId,
			})
			Expect(err).NotTo(HaveOccurred())

			GinkgoWriter.Printf("%s, Step 4: Wait for hang detection to trigger (decode hang threshold is 5s in config)\n", time.Now().String())
			time.Sleep(6 * time.Second)

			GinkgoWriter.Printf("%s, Step 5: Verify probe success log exists\n", time.Now().String())
			Eventually(session.Out, 15*time.Second).Should(gbytes.Say("\\[HangProbe\\] instance .* probe success"))

			GinkgoWriter.Printf("%s, Step 6: Verify isolation log does not exist (use Consistently to ensure no isolation log for a period), more than 2 consecutive cycles\n", time.Now().String())
			Consistently(session.Out, 30*time.Second).ShouldNot(gbytes.Say("\\[HangCheck\\] instance .* is hang"))

			GinkgoWriter.Printf("%s, Step 7: Cleanup - release hang state for specific request\n", time.Now().String())
			resp, err = http.Get(fmt.Sprintf("http://127.0.0.1:%s/unhang_request?request_id=%s", hangDecodePort, testRequestId))
			Expect(err).NotTo(HaveOccurred())
			resp.Body.Close()
			fmt.Printf("Unset request_id %s from hang state\n", testRequestId)

			GinkgoWriter.Printf("%s, Step 8: Close hang inference request, this will cancel the request\n", time.Now().String())
			resp2.Body.Close()

			GinkgoWriter.Printf("%s, Step 9: Verify instance can still process requests normally\n", time.Now().String())
			spanId3 := uuid.NewString()
			reqBody3 := map[string]interface{}{
				"model": "pangu",
				"messages": []map[string]string{{
					"role":    "user",
					"content": "after cleanup",
				}},
				"stream": true,
			}
			resp, err = clientRs.SendChatCompletion(context.Background(), reqBody3, map[string]string{
				constant.HeaderXSpanId:     spanId3,
				constant.HeadersXRequestID: testRequestId,
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(resp.StatusCode).To(Equal(http.StatusOK))
			_, err = io.ReadAll(resp.Body)
			Expect(err).NotTo(HaveOccurred())
		})

		// Test case 4: Should trigger instance isolation after 3 consecutive invalid probes
		It("should trigger instance isolation after 3 consecutive invalid probes", func() {
			GinkgoWriter.Println("Step 1: Send normal request to verify instance is healthy")
			reqBody := map[string]interface{}{
				"model": "pangu",
				"messages": []map[string]string{{
					"role":    "user",
					"content": "test",
				}},
			}
			resp, err := clientRs.SendChatCompletion(context.Background(), reqBody, map[string]string{
				constant.HeaderXSpanId: spanId,
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(resp.StatusCode).To(Equal(http.StatusOK))
			resp.Body.Close()
			time.Sleep(2 * time.Second)

			GinkgoWriter.Println("Step 2: T0 moment - construct Decode Hang")
			resp, err = http.Get(fmt.Sprintf("http://127.0.0.1:%s/hang", hangDecodePort))
			Expect(err).NotTo(HaveOccurred())
			resp.Body.Close()
			fmt.Println("Set decode1 to hang state")

			GinkgoWriter.Println("Step 3: Send streaming request to trigger hang")
			reqBodyTrigger := map[string]interface{}{
				"model": "pangu",
				"messages": []map[string]string{{
					"role":    "user",
					"content": "trigger hang",
				}},
				"max_tokens": 10,
				"stream":     true,
			}
			respTrigger, err := clientRs.SendChatCompletion(TimeoutCtx(context.Background(), 120*time.Second), reqBodyTrigger, map[string]string{
				constant.HeaderXSpanId:     spanId,
				constant.HeadersXRequestID: "test_hang_request_4",
			})
			Expect(err).NotTo(HaveOccurred())

			time.Sleep(500 * time.Millisecond)

			GinkgoWriter.Println("Step 4: Construct Prefill Hang to make 1st probe fail")
			time.Sleep(5 * time.Second)
			resp, err = http.Get(fmt.Sprintf("http://127.0.0.1:%s/hang", hangPrefillPort))
			Expect(err).NotTo(HaveOccurred())
			resp.Body.Close()
			fmt.Println("Set prefill1 to hang state")

			GinkgoWriter.Println("Step 5: Verify 1st invalid probe - counter should be 1")
			Eventually(session.Out, 30*time.Second).Should(gbytes.Say("\\[HangProbe\\] instance .* has 1 consecutive invalid probe"))
			time.Sleep(2 * time.Second)

			GinkgoWriter.Println("Step 7: Verify 2nd invalid probe and warning log - counter should be 2")
			Eventually(session.Out, 30*time.Second).Should(gbytes.Say("\\[HangProbe\\] instance .* has 2 consecutive invalid probe"))

			GinkgoWriter.Println("Step 9: Verify isolation triggered - should execute isolation after 3rd invalid probe")
			Eventually(session.Out, 30*time.Second).Should(gbytes.Say("\\[HangProbe\\] instance .* has 3 consecutive invalid probe"))
			Eventually(session.Out, 30*time.Second).Should(gbytes.Say("\\[HangCheck\\] instance .* is hang \\(probe failed\\)"))
			Eventually(session.Out, 10*time.Second).Should(gbytes.Say("unhealthy instances found for role \\[decode\\]"))

			GinkgoWriter.Println("Step 10: Cleanup test environment - close request that triggered hang")
			respTrigger.Body.Close()

			resp, err = http.Get(fmt.Sprintf("http://127.0.0.1:%s/unhang", hangDecodePort))
			Expect(err).NotTo(HaveOccurred())
			resp.Body.Close()
			resp, err = http.Get(fmt.Sprintf("http://127.0.0.1:%s/unhang", hangPrefillPort))
			Expect(err).NotTo(HaveOccurred())
			resp.Body.Close()
			fmt.Println("Clean up: set decode1 and prefill1 to unhang state")

			Eventually(session.Out, 35*time.Second).Should(gbytes.Say("instance .* is back online due to healthcheck success, previously in fault status"))

			spanId2 := uuid.NewString()
			reqBody2 := map[string]interface{}{
				"model": "pangu",
				"messages": []map[string]string{{
					"role":    "user",
					"content": "after cleanup",
				}},
				"stream": true,
			}
			resp, err = clientRs.SendChatCompletion(context.Background(), reqBody2, map[string]string{
				constant.HeaderXSpanId: spanId2,
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(resp.StatusCode).To(Equal(http.StatusOK))
			resp.Body.Close()
		})
	})
})

// InjectHangProbeConfigToYaml modifies route-server.yaml config file to enable pre-hang probe
func InjectHangProbeConfigToYaml(configPath string) error {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return err
	}
	var configMap map[string]interface{}
	err = yaml.Unmarshal(data, &configMap)
	if err != nil {
		return err
	}

	// Find health-check-config.available-checkers.hang-checker and modify config
	insMgrCfg, ok := configMap["instance-manager"].(map[string]interface{})
	healthCheckConfig, ok := insMgrCfg["health-check-config"].(map[string]interface{})
	if !ok {
		return fmt.Errorf("health-check-config not found")
	}

	availableCheckers, ok := healthCheckConfig["available-checkers"].(map[string]interface{})
	if !ok {
		return fmt.Errorf("available-checkers not found")
	}

	hangChecker, ok := availableCheckers["hang-checker"].(map[string]interface{})
	if !ok {
		return fmt.Errorf("hang-checker not found")
	}

	unavailableCheckers, ok := healthCheckConfig["unavailable-checkers"].(map[string]interface{})
	if !ok {
		return fmt.Errorf("unavailable-checkers not found")
	}
	recoverHealthCfg, ok := unavailableCheckers["rest-health"].(map[string]interface{})
	recoverHealthCfg["consecutive-time"] = 1

	// Enable pre-hang probe feature
	hangChecker["enable-probe-before-hang-check"] = true
	hangChecker["hang-probe-interval-ms"] = 10000
	hangChecker["hang-probe-timeout-ms"] = 6000

	// Also ensure hang detection itself is enabled
	hangChecker["enable"] = true

	// Modify Decode hang threshold from 60s to 20s (for testing)
	hangThresholds, ok := hangChecker["hang-thresholds"].([]interface{})
	if ok {
		for _, threshold := range hangThresholds {
			thresholdMap, ok := threshold.(map[string]interface{})
			if !ok {
				continue
			}
			role, ok := thresholdMap["role"].(string)
			if !ok {
				continue
			}
			if role == "decode" {
				thresholdMap["max-non-active-time-ms"] = 5000 // Change from 60s to 5s
			}
		}
	}

	data, err = yaml.Marshal(configMap)
	if err != nil {
		return err
	}

	return os.WriteFile(configPath, data, 0644)
}
