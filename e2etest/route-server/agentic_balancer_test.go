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
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"route-server/cmd/route_server/server/options"
	"route-server/pkg/common/constant"
	"route-server/pkg/common/openai"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gbytes"
	"github.com/onsi/gomega/gexec"
)

// This suite only covers what unit tests cannot: the wiring between the
// route-server pipeline, the health checker and the agentic-balancer
// scheduler. Scoring, queue ordering, session parsing and the state tables are
// covered by pkg/route-server/scheduler/agentic_balancer/**/*_test.go and are
// deliberately NOT repeated here.

const (
	abHybridCtx        = "agentic-balancer-hybrid-test"
	abHybridListenPort = "30720"
	abHybridOpsPort    = "30730"
	abHybridDecode1    = "30721"
	abHybridDecode2    = "30722"

	abPDCtx        = "agentic-balancer-pd-test"
	abPDListenPort = "30650"
	abPDOpsPort    = "30660"
	abPDPrefill1   = "30651"
	abPDPrefill2   = "30652"
	abPDDecode1    = "30653"
)

// injections the specs rely on; see the agentic-balancer scoring formula:
// util = max((inflight+waiting)/(1+max_num_seqs), kv), schedulable while util < overload-util.
const (
	idleKV          = 0.001
	overloadKV      = 0.95 // above overload-util=0.92 => worker is not schedulable
	dispatchTimeout = 10 * time.Second
	// metricsRefreshWait covers the scheduler's worker-state refresh
	// (defaultUpdateIntervalMs = 1s) so an injected load is actually used by
	// the next scoring decision.
	metricsRefreshWait = 1500 * time.Millisecond
)

// sibling maps a worker to its peer, so a spec can load one side only.
var sibling = map[string]string{
	"decode1":  "decode2",
	"decode2":  "decode1",
	"prefill1": "prefill2",
	"prefill2": "prefill1",
}

type updateMetricsReq struct {
	RunningRequest int     `json:"running_request"`
	WaitingRequest int     `json:"waiting_request"`
	KVCacheUsage   float64 `json:"kv_cache_usage"`
}

// injectMetrics overrides the metrics the mock worker reports, which is what
// the prometheus monitor client scrapes.
func injectMetrics(port string, running, waiting int, kvUsage float64) {
	body := updateMetricsReq{
		RunningRequest: running,
		WaitingRequest: waiting,
		KVCacheUsage:   kvUsage,
	}
	jsonBody, err := json.Marshal(body)
	Expect(err).NotTo(HaveOccurred())
	url := fmt.Sprintf("http://127.0.0.1:%s/metrics/update", port)
	req, err := http.NewRequest("POST", url, bytes.NewReader(jsonBody))
	Expect(err).NotTo(HaveOccurred())
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	Expect(err).NotTo(HaveOccurred(), "inject metrics to worker on port %s", port)
	resp.Body.Close()
	Expect(resp.StatusCode).To(Equal(http.StatusOK), "inject metrics to worker on port %s", port)
}

// resetMetrics clears the mock-side overrides so specs cannot leak load into each other.
func resetMetrics(ports ...string) {
	for _, port := range ports {
		injectMetrics(port, 0, 0, idleKV)
	}
}

func startMockWorkersForConfig() []*gexec.Session {
	var servers []*gexec.Session
	for _, instance := range options.GetConfig().InstanceManagerCfg.RouteTable.InstanceRoutes {
		params := []string{
			"-addr", instance.Ip,
			"-port", fmt.Sprintf("%d", instance.Port),
		}
		cmd := exec.Command(mockVLLMPath, params...)
		sess, err := gexec.Start(cmd, io.Discard, os.Stderr)
		Expect(err).NotTo(HaveOccurred())
		servers = append(servers, sess)
	}
	return servers
}

type queueResult struct {
	resp *http.Response
	err  error
}

var _ = Describe(abHybridCtx, Ordered, func() {
	var (
		session     *gexec.Session
		mockServers []*gexec.Session
		clientRs    *RSClient
		err         error
	)
	workerPorts := []string{abHybridDecode1, abHybridDecode2}

	BeforeAll(func() {
		EnsurePorts([]string{abHybridListenPort, abHybridOpsPort, abHybridDecode1, abHybridDecode2})

		By("copy pipelines config")
		configPath := filepath.Join(projectRoot, "e2etest", "environment", abHybridCtx, "config")
		Expect(os.MkdirAll(filepath.Join(configPath, "pipelines"), 0755)).To(BeNil())

		deployPath := filepath.Join(projectRoot, "deploy")
		Expect(copyDir(filepath.Join(deployPath, "pipelines"), filepath.Join(configPath, "pipelines"))).To(BeNil())

		By("gen route-server config")
		toolParam := []string{
			"-template", filepath.Join(deployPath, ROUTE_SERVER_TEMPLATE),
			"-host", "127.0.0.1",
			"-target", filepath.Join(projectRoot, "e2etest", "environment", abHybridCtx),
			"-infer-mode", "Hybrid",
			"-decodes", fmt.Sprintf("decode1#127.0.0.1:%s,decode2#127.0.0.1:%s", abHybridDecode1, abHybridDecode2),
			"-name", "route-server-agentic-hybrid-test",
			"-remote-scheduler-type", "disable",
			"-port", abHybridListenPort,
			"-ops-port", abHybridOpsPort,
			"-mode", "singleton",
			"-infer-module-version", "pipeline",
		}
		Expect(exec.Command(configToolPath, toolParam...).Run()).To(BeNil())

		By("replace decode scheduler with agentic-balancer")
		configFile := filepath.Join(configPath, "route-server.yaml")
		Expect(ensureAgenticScheduler(configFile, "decode")).To(BeNil())
		Expect(injectAgenticParams(configFile, map[string]string{
			"overload-util":      "0.92",
			"dispatch-timeout":   dispatchTimeout.String(),
			"affinity-half-life": "30s",
		})).To(BeNil())

		By("shrink health-check timings (production needs 9x10s to fault/recover)")
		Expect(injectFastHealthConfig(configFile)).To(BeNil())

		By("copy prometheus config and enable the monitor client")
		Expect(copyFile(filepath.Join(deployPath, "prometheus-template.yaml"), filepath.Join(configPath, "prometheus.yaml"))).To(BeNil())
		Expect(injectMonitorConfig(configFile, filepath.Join(configPath, "prometheus.yaml"))).To(BeNil())

		By("set config path and init")
		Expect(os.Setenv(constant.EnvServerConfigPath, configPath)).To(BeNil())
		Expect(options.InitConfig()).To(BeNil())

		By("cp metrics config")
		metricsCollectorConfigFileName := options.GetConfig().MetricsCfg.MetricsCollectorConfigFileName
		anthropicErrorTypeConfigFileName := options.GetConfig().ServerConfig.AnthropicErrorTypeConfigFileName
		copyConfigFilesToDirs(deployPath, []string{filepath.Join(projectRoot, "e2etest", "environment", abHybridCtx)}, metricsCollectorConfigFileName, anthropicErrorTypeConfigFileName)

		By("start mock workers")
		mockServers = startMockWorkersForConfig()
		time.Sleep(2 * time.Second)

		By("start route-server")
		session, err = gexec.Start(exec.Command(rsPath), io.Discard, os.Stderr)
		Expect(err).NotTo(HaveOccurred())
		Eventually(session.Out, 10*time.Second).Should(gbytes.Say("Start to listening on"))

		By("wait until both workers are reported running")
		clientRs = NewRSClient("http://localhost:" + abHybridListenPort)
		waitAllWorkersRunning(clientRs, workerPorts, 60*time.Second)
	})

	AfterAll(func() {
		if session != nil {
			session.Kill()
			session.Wait()
		}
		for _, mock := range mockServers {
			mock.Kill()
			mock.Wait()
		}
	})

	AfterEach(func() {
		for _, port := range workerPorts {
			_, _ = NewMockServerClient(port).MockHealth()
			_, _ = NewMockServerClient(port).RemoveError()
		}
		resetMetrics(workerPorts...)
	})

	It("routes chat and stream requests through the scheduler", func() {
		By("non-stream request reaches a worker and returns 200")
		resp, err := clientRs.SendChatCompletion(
			TimeoutCtx(context.Background(), 30*time.Second),
			openai.ChatCompletionRequestV1{
				Model:    "pangu",
				Messages: []openai.ChatCompletionMessage{{Role: "user", Content: "hello"}},
			}, map[string]string{constant.XRequestId: "smoke-chat"})
		Expect(err).NotTo(HaveOccurred())
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		var result openai.ChatCompletionResponse
		Expect(ParseResponse(resp, &result)).To(BeNil())
		Expect(result.Choices).NotTo(BeEmpty())
		Expect(waitDispatchWorker(session, "smoke-chat", dispatchWait)).To(BeElementOf("decode1", "decode2"))

		By("stream request keeps the SSE format")
		streamResp, cancel, err := sendStreamSessionRequest(clientRs, "", "smoke-stream", "hello stream")
		Expect(err).NotTo(HaveOccurred())
		defer cancel()
		Expect(streamResp.StatusCode).To(Equal(http.StatusOK))
		verifySSEFormat(readSSEBody(streamResp))
		Expect(waitDispatchWorker(session, "smoke-stream", dispatchWait)).To(BeElementOf("decode1", "decode2"))
	})

	It("schedules by the per-worker metrics scraped from prometheus", func() {
		By("make decode1 unschedulable and keep decode2 idle")
		injectMetrics(abHybridDecode1, 0, 0, overloadKV)
		injectMetrics(abHybridDecode2, 0, 0, idleKV)

		By("probe until the scrape lands: decode1 must drop out of the candidates")
		probe := 0
		Eventually(func() bool {
			probe++
			reqID := fmt.Sprintf("metrics-probe-%d", probe)
			worker := probeWorker(clientRs, session, reqID, "metrics probe")
			return worker == "decode2" && extractDispatchField(session, reqID, "candidates") == "1"
		}, 20*time.Second, 200*time.Millisecond).Should(BeTrue(),
			"kv=%.2f on decode1 should leave decode2 as the only candidate", overloadKV)
	})

	// Body session_id and header session_id go through different parsers
	// (ParseSchedulingMetadata vs ParseSessionContext), hence one case each.
	DescribeTable("keeps a session on its worker",
		func(headerKey string) {
			sessionID := "sess-aff-" + headerKey
			reqID1, reqID2 := "aff-1-"+headerKey, "aff-2-"+headerKey

			By("bind the session with the first request")
			worker1 := sendAffinityRequest(clientRs, session, headerKey, sessionID, reqID1)
			Expect(extractDispatchField(session, reqID1, "session")).To(Equal(sessionID))

			By("load the bound worker slightly: only affinity can keep the session there")
			injectMetrics(workerNameToPort(worker1), 0, 0, 0.10)
			injectMetrics(workerNameToPort(sibling[worker1]), 0, 0, idleKV)
			time.Sleep(metricsRefreshWait)

			By("the second request must stick to the bound worker")
			worker2 := sendAffinityRequest(clientRs, session, headerKey, sessionID, reqID2)
			Expect(worker2).To(Equal(worker1),
				"session %q should stay on %s, got %s", sessionID, worker1, worker2)
			// aff is 0 for every worker when the session was never bound, so this
			// alone catches a broken binding; the load above catches "affinity
			// computed but ignored by the scoring".
			Expect(dispatchFieldFloat(session, reqID2, "aff")).To(BeNumerically(">=", 0.9),
				"second dispatch should carry near-max affinity for the bound worker")
		},
		Entry("session_id in the body", ""),
		Entry("session id in the X-Dynamo-Session-ID header", "X-Dynamo-Session-ID"),
	)

	It("stops routing to a faulty worker and resumes after recovery", func() {
		mock := NewMockServerClient(abHybridDecode1)
		By("mark decode1 unhealthy and wait for the health checker")
		_, err := mock.MockUnHealth()
		Expect(err).NotTo(HaveOccurred())
		waitWorkerStatus(clientRs, abHybridDecode1, constant.FaultStatus, statusWait)

		By("requests must be routed to decode2")
		Expect(sendAndWorker(clientRs, session, "", "fault-out", "after fault")).To(Equal("decode2"))

		By("recover decode1")
		_, err = mock.MockHealth()
		Expect(err).NotTo(HaveOccurred())
		waitWorkerStatus(clientRs, abHybridDecode1, constant.RunningStatus, statusWait)

		By("with decode2 overloaded the recovered worker must take requests again")
		injectMetrics(abHybridDecode2, 0, 0, overloadKV)
		probe := 0
		Eventually(func() string {
			probe++
			return probeWorker(clientRs, session, fmt.Sprintf("fault-back-%d", probe), "after recovery")
		}, 20*time.Second, 300*time.Millisecond).Should(Equal("decode1"),
			"recovered worker should be schedulable again")
	})

	It("queues requests while every worker is overloaded and dispatches them after the load drops", func() {
		By("overload both workers")
		for _, port := range workerPorts {
			injectMetrics(port, 0, 2, overloadKV)
		}

		By("wait until the overload is visible to the scheduler")
		waitUntilOverloaded(clientRs, session, "queue-overload-probe")

		By("send a request in the background, it must be enqueued")
		resultCh := make(chan queueResult, 1)
		go func() {
			defer GinkgoRecover()
			resp, err := sendSessionRequest(clientRs, "sess-queue", "queue-1", "queued request")
			resultCh <- queueResult{resp: resp, err: err}
		}()
		waitLogLine(session, `agentic-balancer: request chatcmpl-queue-1[^ ]* enqueued`, logWait)

		By("release the load, the controller must dequeue and dispatch it")
		resetMetrics(workerPorts...)
		var res queueResult
		Eventually(resultCh, 30*time.Second).Should(Receive(&res))
		Expect(res.err).NotTo(HaveOccurred())
		Expect(res.resp).NotTo(BeNil())
		Expect(res.resp.StatusCode).To(Equal(http.StatusOK))
		res.resp.Body.Close()
		waitLogLine(session, `agentic-balancer: request chatcmpl-queue-1[^ ]* dequeued`, logWait)
	})

	It("returns 504 to the client when the queue wait exceeds dispatch-timeout", func() {
		for _, port := range workerPorts {
			injectMetrics(port, 0, 2, overloadKV)
		}
		waitUntilOverloaded(clientRs, session, "timeout-overload-probe")

		start := time.Now()
		resp, err := sendSessionRequest(clientRs, "sess-timeout", "timeout-1", "times out")
		elapsed := time.Since(start)
		Expect(err).NotTo(HaveOccurred())
		Expect(resp).NotTo(BeNil())
		defer resp.Body.Close()

		Expect(elapsed).To(BeNumerically(">=", dispatchTimeout),
			"the request should wait for dispatch-timeout before failing")
		Expect(resp.StatusCode).To(Equal(http.StatusGatewayTimeout))

		var errBody struct {
			Code string `json:"error_code"`
			Msg  string `json:"error_msg"`
		}
		Expect(json.NewDecoder(resp.Body).Decode(&errBody)).To(Succeed())
		Expect(errBody.Code).To(Equal("RouteServer.5001"), "dispatch timeout should map to the gateway-timeout code")

		waitLogLine(session, `agentic-balancer: request chatcmpl-timeout-1[^ ]* dispatch wait timed out`, logWait)
	})
})

var _ = Describe(abPDCtx, Ordered, func() {
	var (
		session     *gexec.Session
		mockServers []*gexec.Session
		clientRs    *RSClient
		err         error
	)
	workerPorts := []string{abPDPrefill1, abPDPrefill2}

	BeforeAll(func() {
		EnsurePorts([]string{abPDListenPort, abPDOpsPort, abPDPrefill1, abPDPrefill2, abPDDecode1})

		By("copy pipelines config")
		configPath := filepath.Join(projectRoot, "e2etest", "environment", abPDCtx, "config")
		Expect(os.MkdirAll(filepath.Join(configPath, "pipelines"), 0755)).To(BeNil())

		deployPath := filepath.Join(projectRoot, "deploy")
		Expect(copyDir(filepath.Join(deployPath, "pipelines"), filepath.Join(configPath, "pipelines"))).To(BeNil())

		By("gen route-server config")
		toolParam := []string{
			"-template", filepath.Join(deployPath, ROUTE_SERVER_TEMPLATE),
			"-host", "127.0.0.1",
			"-target", filepath.Join(projectRoot, "e2etest", "environment", abPDCtx),
			"-infer-mode", "PD",
			"-prefills", fmt.Sprintf("prefill1#127.0.0.1:%s,prefill2#127.0.0.1:%s", abPDPrefill1, abPDPrefill2),
			"-decodes", fmt.Sprintf("decode1#127.0.0.1:%s", abPDDecode1),
			"-name", "route-server-agentic-pd-test",
			"-remote-scheduler-type", "disable",
			"-port", abPDListenPort,
			"-ops-port", abPDOpsPort,
			"-mode", "singleton",
			"-infer-module-version", "pipeline",
		}
		Expect(exec.Command(configToolPath, toolParam...).Run()).To(BeNil())

		By("replace prefill scheduler with agentic-balancer")
		configFile := filepath.Join(configPath, "route-server.yaml")
		Expect(ensureAgenticScheduler(configFile, "prefill")).To(BeNil())
		Expect(injectAgenticParams(configFile, map[string]string{
			"overload-util":      "0.92",
			"dispatch-timeout":   dispatchTimeout.String(),
			"affinity-half-life": "30s",
		})).To(BeNil())

		By("shrink health-check timings")
		Expect(injectFastHealthConfig(configFile)).To(BeNil())

		By("copy prometheus config and enable the monitor client")
		Expect(copyFile(filepath.Join(deployPath, "prometheus-template.yaml"), filepath.Join(configPath, "prometheus.yaml"))).To(BeNil())
		Expect(injectMonitorConfig(configFile, filepath.Join(configPath, "prometheus.yaml"))).To(BeNil())

		By("set config path and init")
		Expect(os.Setenv(constant.EnvServerConfigPath, configPath)).To(BeNil())
		Expect(options.InitConfig()).To(BeNil())

		By("cp metrics config")
		metricsCollectorConfigFileName := options.GetConfig().MetricsCfg.MetricsCollectorConfigFileName
		anthropicErrorTypeConfigFileName := options.GetConfig().ServerConfig.AnthropicErrorTypeConfigFileName
		copyConfigFilesToDirs(deployPath, []string{filepath.Join(projectRoot, "e2etest", "environment", abPDCtx)}, metricsCollectorConfigFileName, anthropicErrorTypeConfigFileName)

		By("start mock workers")
		mockServers = startMockWorkersForConfig()
		time.Sleep(2 * time.Second)

		By("start route-server")
		session, err = gexec.Start(exec.Command(rsPath), io.Discard, os.Stderr)
		Expect(err).NotTo(HaveOccurred())
		Eventually(session.Out, 10*time.Second).Should(gbytes.Say("Start to listening on"))

		By("wait until the prefill workers are reported running")
		clientRs = NewRSClient("http://localhost:" + abPDListenPort)
		// decode1 serves the response after the prefill hop, so it must be up too.
		waitAllWorkersRunning(clientRs, append(workerPorts, abPDDecode1), 60*time.Second)
	})

	AfterAll(func() {
		if session != nil {
			session.Kill()
			session.Wait()
		}
		for _, mock := range mockServers {
			mock.Kill()
			mock.Wait()
		}
	})

	AfterEach(func() {
		for _, port := range workerPorts {
			_, _ = NewMockServerClient(port).MockHealth()
			_, _ = NewMockServerClient(port).RemoveError()
		}
		resetMetrics(workerPorts...)
	})

	It("routes requests to a prefill worker through the scheduler", func() {
		worker := sendAndWorker(clientRs, session, "", "pd-smoke-1", "hello pd")
		Expect(worker).To(BeElementOf("prefill1", "prefill2"))
	})

	It("stops routing to a faulty prefill worker", func() {
		mock := NewMockServerClient(abPDPrefill1)
		_, err := mock.MockUnHealth()
		Expect(err).NotTo(HaveOccurred())
		waitWorkerStatus(clientRs, abPDPrefill1, constant.FaultStatus, statusWait)

		By("requests must be routed to prefill2")
		Expect(sendAndWorker(clientRs, session, "", "pd-fault-out", "after pd fault")).To(Equal("prefill2"))
	})
})
