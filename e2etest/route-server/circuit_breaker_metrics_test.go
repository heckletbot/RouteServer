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

	"github.com/goccy/go-yaml"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gbytes"
	"github.com/onsi/gomega/gexec"
	clientv3 "go.etcd.io/etcd/client/v3"

	commoncfg "infer-dp-scheduler/pkg/common/config"
	"infer-dp-scheduler/pkg/common/constant"
)

const (
	cbTestContext = "circuit-breaker-metrics-test"

	// dedicated port range to avoid conflicts with other tests
	cbEtcdClientPort = "40500"
	cbEtcdPeerPort   = "40501"
	cbLeaderHTTPPort = "40502"
	cbLeaderOpsPort  = "40512"
	cbWorkerHTTPPort = "40503"
	cbWorkerOpsPort  = "40513"
	cbPrefillPort    = "40504"
	cbDecodePort     = "40505"

	cbRsLeaderKey     = "/rs/leader"
	cbRsPeersAllDir   = "/rs/cluster/state/peers/all/"
	cbEtcdFailuresKey = "/rs/instances/failures"
)

// mockErrorWithCode sends a /mock_error request to the mock server, setting the specified error code
func mockErrorWithCode(mockClient *MockServerClient, code int) (*http.Response, error) {
	type mockErrorBody struct {
		RetCode int `json:"code"`
	}
	body := &mockErrorBody{RetCode: code}
	return mockClient.sendRequest(context.Background(), "/mock_error", "POST", body, nil)
}

var _ = Describe(cbTestContext, Ordered, func() {
	var (
		leaderSession *gexec.Session
		workerSession *gexec.Session
		mockServers   []*gexec.Session
		etcdClient    *clientv3.Client
		workerClient  *RSClient
		prefillMock   *MockServerClient
		decodeMock    *MockServerClient

		leaderCfgDir string
		workerCfgDir string
	)

	BeforeAll(func() {
		By("step 0: cleanup old environment and ensure all ports are free")
		os.RemoveAll(filepath.Join(projectRoot, "e2etest", "environment", cbTestContext))

		EnsurePorts([]string{cbEtcdClientPort, cbEtcdPeerPort,
			cbLeaderHTTPPort, cbWorkerHTTPPort,
			cbLeaderOpsPort, cbWorkerOpsPort,
			cbPrefillPort, cbDecodePort})
		time.Sleep(500 * time.Millisecond)

		By("step 1: start embedded etcd")
		etcdServer, etcdCl := StartEmbeddedEtcd(cbEtcdClientPort, cbEtcdPeerPort, "cb-test-etcd")
		etcdClient = etcdCl

		By("step 2: modify route-server template with circuit-breaker config for testing")
		deployPath := filepath.Join(projectRoot, "deploy")
		var cfg commoncfg.Config
		templateContent, err := os.ReadFile(filepath.Join(deployPath, ROUTE_SERVER_TEMPLATE))
		Expect(err).To(BeNil())
		Expect(yaml.Unmarshal(templateContent, &cfg)).To(BeNil())

		// accelerate health checks
		cfg.InstanceManagerCfg.HealthCheckConfig.RestHealthCommonConfig.Interval = 2000
		cfg.InstanceManagerCfg.HealthCheckConfig.AvailableCheckersCfg.RestHealthCfg.ConsecutiveTime = 1
		cfg.InstanceManagerCfg.HealthCheckConfig.UnavailableCheckersCfg.RestHealthCfg.ConsecutiveTime = 1

		// prevent health checks from marking instances as unavailable before circuit breaker kicks in
		cfg.InstanceManagerCfg.HealthCheckConfig.AvailableCheckersCfg.RestHealthCfg.FailOn500 = false
		cfg.InstanceManagerCfg.HealthCheckConfig.AvailableCheckersCfg.RequestFailuresCfg.Enable = false

		// shorten health cache expiration time to facilitate testing degradation to circuit breaker Level 3
		cfg.InstanceManagerCfg.HealthCheckConfig.HealthCacheExpireTimeMs = 3000

		// configure circuit breaker: short window, low threshold for easy testing
		cfg.ProbeCfg.CircuitBreaker = &commoncfg.CircuitBreakerConfig{
			Enable:               true,
			FailureRateThreshold: 0.5,
			MinRequestCount:      3,
			CheckWindowSec:       5,
		}

		outData, err := yaml.Marshal(&cfg)
		Expect(err).To(BeNil())

		By("step 3: generate RS configs via config-tool (etcd mode)")
		etcdCtxDir := filepath.Join(projectRoot, "e2etest", "environment", cbTestContext)
		Expect(os.MkdirAll(etcdCtxDir, 0755)).To(BeNil())
		tempTemplateFile := filepath.Join(etcdCtxDir, "cb-test-template.yaml")
		Expect(os.WriteFile(tempTemplateFile, outData, 0644)).To(BeNil())

		DeferCleanup(func() {
			os.RemoveAll(etcdCtxDir)
		})

		leaderCfgDir = filepath.Join(etcdCtxDir, "leader1")
		workerCfgDir = filepath.Join(etcdCtxDir, "worker1")

		genRsConfig := func(name, targetDir, port, remoteSchedulerType string, candidate bool) {
			Expect(os.RemoveAll(targetDir)).To(BeNil())
			Expect(os.MkdirAll(filepath.Join(targetDir, "config"), 0755)).To(BeNil())
			out, err := exec.Command(configToolPath, DisableHTTPSConfig([]string{
				"-template", tempTemplateFile,
				"-host", "127.0.0.1",
				"-target", targetDir,
				"-infer-mode", "PD",
				"-prefills", fmt.Sprintf("prefill1#127.0.0.1:%s", cbPrefillPort),
				"-decodes", fmt.Sprintf("decode1#127.0.0.1:%s", cbDecodePort),
				"-name", name,
				"-remote-scheduler-type", remoteSchedulerType,
				"-port", port,
				"-candidate", fmt.Sprintf("%t", candidate),
				"-etcd-endpoints", fmt.Sprintf("127.0.0.1:%s", cbEtcdClientPort),
				"-infer-module-version", "pipeline",
			})...).CombinedOutput()
			GinkgoWriter.Printf("[config] %s config-tool output: %s\n", name, string(out))
			Expect(err).To(BeNil())
		}
		genRsConfig("leader1", leaderCfgDir, cbLeaderHTTPPort, "disable", true)
		genRsConfig("worker1", workerCfgDir, cbWorkerHTTPPort, "leader", false)

		for _, cfgDir := range []string{leaderCfgDir, workerCfgDir} {
			Expect(os.MkdirAll(filepath.Join(cfgDir, "config", "pipelines"), 0755)).To(BeNil())
			Expect(copyDir(filepath.Join(deployPath, "pipelines"), filepath.Join(cfgDir, "config", "pipelines"))).To(BeNil())
			metricsCollectorConfigFileName := "metrics_config_vllm_0.11.0.yaml"
			Expect(copyFile(
				filepath.Join(deployPath, metricsCollectorConfigFileName),
				filepath.Join(cfgDir, "config", metricsCollectorConfigFileName),
			)).To(BeNil())
		}

		By("step 4: start mock vllm instances (1 prefill + 1 decode)")
		mockInstances := []struct {
			port string
			name string
		}{
			{cbPrefillPort, "prefill1"},
			{cbDecodePort, "decode1"},
		}
		for _, inst := range mockInstances {
			params := []string{
				"-addr", "127.0.0.1",
				"-port", inst.port,
				"-name", inst.name,
				"-ttft", "500",
			}
			command := exec.Command(mockVLLMPath, params...)
			sess, startErr := gexec.Start(command, io.Discard, GinkgoWriter)
			Expect(startErr).NotTo(HaveOccurred())
			checkMockVllmListenSucceess(sess)
			mockServers = append(mockServers, sess)
			GinkgoWriter.Printf("[mock] started %s on port %s\n", inst.name, inst.port)
		}
		time.Sleep(500 * time.Millisecond)

		By("step 5: start leader RS")
		env := os.Environ()
		env = append(env, fmt.Sprintf("%s=%s", constant.EnvServerConfigPath, filepath.Join(leaderCfgDir, "config")))
		command := exec.Command(rsPath)
		command.Env = env
		leaderSession, err = gexec.Start(command, io.Discard, GinkgoWriter)
		Expect(err).NotTo(HaveOccurred())
		checkRsListenSucceess(leaderSession)
		GinkgoWriter.Printf("[rs] leader route-server is ready on port %s\n", cbLeaderHTTPPort)

		By("step 6: start worker RS")
		env = os.Environ()
		env = append(env, fmt.Sprintf("%s=%s", constant.EnvServerConfigPath, filepath.Join(workerCfgDir, "config")))
		command = exec.Command(rsPath)
		command.Env = env
		workerSession, err = gexec.Start(command, io.Discard, GinkgoWriter)
		Expect(err).NotTo(HaveOccurred())
		checkRsListenSucceess(workerSession)
		GinkgoWriter.Printf("[rs] worker route-server is ready on port %s\n", cbWorkerHTTPPort)

		By("step 7: wait for leader election")
		Eventually(func() bool {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			resp, getErr := etcdClient.Get(ctx, cbRsLeaderKey, clientv3.WithPrefix())
			if getErr != nil || len(resp.Kvs) == 0 {
				return false
			}
			leaderValue := string(resp.Kvs[0].Value)
			GinkgoWriter.Printf("[leader] etcd reported leader: %s\n", leaderValue)
			return strings.Contains(leaderValue, "leader1")
		}, 30*time.Second, 1*time.Second).Should(BeTrue(), "a leader should be elected")

		By("step 8: wait for worker registration in etcd")
		Eventually(func() bool {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			resp, getErr := etcdClient.Get(ctx, cbRsPeersAllDir, clientv3.WithPrefix())
			if getErr != nil || len(resp.Kvs) == 0 {
				return false
			}
			for _, kv := range resp.Kvs {
				if strings.Contains(string(kv.Key), "worker1") {
					return true
				}
			}
			return false
		}, 30*time.Second, 1*time.Second).Should(BeTrue(), "worker1 should be registered in etcd")

		prefillMock = NewMockServerClient(cbPrefillPort)
		decodeMock = NewMockServerClient(cbDecodePort)
		workerClient = NewRSClient("http://127.0.0.1:" + cbWorkerHTTPPort)

		By("step 9: trigger etcd health data initialization by briefly killing/restarting a mock")
		// Worker's EtcdClusterService.healthStatsCache needs to be initialized via etcd watch events.
		// If all instances are healthy and FailOn500=false, the leader won't call SnapshotUnhealthyInstances,
		// causing healthStatsCache.UpdateTime to be zero, and GetOrFetchHealthStats to always return nil.
		// Solution: briefly kill the prefill mock to trigger the leader to write failure data to etcd,
		// so the worker receives the data via watch and healthStatsCache gets initialized.
		prefillSess := mockServers[0]
		prefillSess.Kill()
		prefillSess.Wait()
		GinkgoWriter.Printf("[mock] killed prefill1 to trigger etcd health data write\n")

		// wait for leader to detect the failure and write to etcd
		Eventually(func() bool {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			getResp, getErr := etcdClient.Get(ctx, cbEtcdFailuresKey)
			if getErr != nil || len(getResp.Kvs) == 0 {
				return false
			}
			GinkgoWriter.Printf("[etcd] leader wrote failure data\n")
			return true
		}, 30*time.Second, 1*time.Second).Should(BeTrue(), "leader should have written failure data to etcd")

		// restart prefill mock
		params := []string{
			"-addr", "127.0.0.1",
			"-port", cbPrefillPort,
			"-name", "prefill1",
			"-ttft", "500",
		}
		restartCmd := exec.Command(mockVLLMPath, params...)
		newSess, startErr := gexec.Start(restartCmd, io.Discard, GinkgoWriter)
		Expect(startErr).NotTo(HaveOccurred())
		checkMockVllmListenSucceess(newSess)
		mockServers[0] = newSess
		GinkgoWriter.Printf("[mock] restarted prefill1\n")

		By("step 10: wait for worker health cache sync from leader")
		Eventually(func() bool {
			ready, msg, err := workerClient.GetWorkerReadiness()
			if err != nil {
				GinkgoWriter.Printf("[worker] readiness check error: %v\n", err)
				return false
			}
			GinkgoWriter.Printf("[worker] readiness: ready=%v, msg=%s\n", ready, msg)
			return strings.Contains(msg, `"message":"ready"`)
		}, 60*time.Second, 2*time.Second).Should(BeTrue(), "worker health cache should be synced from leader")

		DeferCleanup(func() {
			if workerSession != nil {
				workerSession.Kill()
				workerSession.Wait()
			}
			if leaderSession != nil {
				leaderSession.Kill()
				leaderSession.Wait()
			}
			CleanupSessions(mockServers)
			if etcdClient != nil {
				etcdClient.Close()
			}
			etcdServer.Close()
			// do not clean up environment directories and templates for log analysis
		})
	})

	BeforeEach(func() {
		// ensure mocks are normal before each test case
		prefillMock.RemoveError()
		decodeMock.RemoveError()
	})

	// ================================================================
	// Core test: verify that 2xx/4xx/499 are not counted in circuit breaker failure rate, only 5xx triggers circuit break
	// ================================================================
	Context("circuit breaker failure rate metrics validation", func() {
		// helper: get metric value
		getMetricValue := func(metricName, labelFilter string) float64 {
			val, _, err := workerClient.GetMetricsValue(metricName, labelFilter)
			if err != nil {
				return 0
			}
			return val
		}

		// helper: send a streaming request then cancel immediately, simulating client cancellation (499)
		sendCancelledRequest := func(requestId string) {
			body := NewBasicChatRequestBody()
			body.MaxTokens = 10
			body.Stream = true
			headers := map[string]string{constant.XRequestId: requestId}
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			go func() {
				defer close(done)
				resp, err := workerClient.SendChatCompletion(ctx, body, headers)
				if err == nil {
					io.ReadAll(resp.Body)
					resp.Body.Close()
				}
			}()
			time.Sleep(100 * time.Millisecond)
			cancel()
			<-done
		}

		It("2xx/4xx/499 not counted in circuit breaker failure rate, only 5xx triggers circuit break at Level 3", func() {
			// ---- Phase 1: send 5xx requests at Level 2, verify circuit breaker does not trigger ----
			By("set decode mock to return 500 error")
			mockErrorWithCode(decodeMock, 500)

			By("send multiple 5xx requests at Level 2")
			initial500Val := getMetricValue("rs:infer_requests_failure_status_total", `status_code="500"`)
			for i := 0; i < 5; i++ {
				body := NewBasicChatRequestBody()
				body.MaxTokens = 10
				body.Stream = false
				headers := map[string]string{constant.XRequestId: fmt.Sprintf("cb-5xx-l2-%d", i)}
				ctx := TimeoutCtx(context.Background(), 30*time.Second)
				resp, err := workerClient.SendChatCompletion(ctx, body, headers)
				if err == nil {
					io.ReadAll(resp.Body)
					resp.Body.Close()
				}
			}

			By("verify 5xx metric increment (metrics are recorded normally, independent of circuit breaker)")
			Eventually(func() float64 {
				return getMetricValue("rs:infer_requests_failure_status_total", `status_code="500"`) - initial500Val
			}, 10*time.Second, 1*time.Second).Should(BeNumerically(">=", 5),
				"5xx requests should be recorded by NumRequestsWithErrorCode metric, increment should be at least 5")

			By("verify circuit breaker does not trigger when not degraded to Level 3, no related logs")
			Consistently(func() bool {
				ready, msg, err := workerClient.GetWorkerReadiness()
				if err != nil {
					return false
				}
				GinkgoWriter.Printf("[cb-not-degraded] ready=%v, msg=%s\n", ready, msg)
				return !strings.Contains(msg, "circuit breaker is open")
			}, 10*time.Second, 2*time.Second).Should(BeTrue(),
				"circuit breaker should not trigger when not degraded to Level 3, even with 5xx requests")

			Consistently(workerSession.Out, 10*time.Second).ShouldNot(gbytes.Say(`\[CircuitBreaker\] Period check`),
				"no periodic check logs should appear when not degraded to Level 3")
			Consistently(workerSession.Out, 10*time.Second).ShouldNot(gbytes.Say(`\[CircuitBreaker\] Probe check`),
				"no probe check logs should appear when not degraded to Level 3")

			// ---- Phase 2: kill leader to make worker degrade to Level 3 ----
			By("kill leader to make worker degrade to Level 3 (no health cache)")
			leaderSession.Kill()
			leaderSession.Wait()
			GinkgoWriter.Printf("[leader] process killed, worker will degrade to Level 3\n")

			By("wait for worker to degrade to Level 3")
			Eventually(func() bool {
				ready, msg, err := workerClient.GetWorkerReadiness()
				if err != nil {
					return false
				}
				GinkgoWriter.Printf("[cb-degrade] ready=%v, msg=%s\n", ready, msg)
				return strings.Contains(msg, "circuit breaker")
			}, 30*time.Second, 2*time.Second).Should(BeTrue(),
				"worker should degrade to Level 3, readiness message should contain circuit breaker")

			By("wait one CheckWindowSec (5s) for circuit breaker to record baseline")
			time.Sleep(6 * time.Second)

			// ---- Phase 2a: send 2xx/4xx/499 requests at Level 3, verify circuit breaker does not trigger ----
			By("restore mock to normal (ensure 2xx requests can return normally)")
			decodeMock.RemoveError()

			By("send multiple 2xx requests at Level 3 (normal inference), verify circuit breaker does not trigger")
			l3Initial200 := getMetricValue("rs:infer_requests_failure_status_total", `status_code="200"`)
			for i := 0; i < 5; i++ {
				body := NewBasicChatRequestBody()
				body.MaxTokens = 10
				body.Stream = false
				headers := map[string]string{constant.XRequestId: fmt.Sprintf("cb-2xx-l3-%d", i)}
				ctx := TimeoutCtx(context.Background(), 30*time.Second)
				resp, err := workerClient.SendChatCompletion(ctx, body, headers)
				if err == nil {
					io.ReadAll(resp.Body)
					resp.Body.Close()
				}
			}

			By("send multiple 4xx requests at Level 3 (invalid request body), verify circuit breaker does not trigger")
			l3Initial400 := getMetricValue("rs:infer_requests_failure_status_total", `status_code="400"`)
			for i := 0; i < 5; i++ {
				headers := map[string]string{constant.XRequestId: fmt.Sprintf("cb-4xx-l3-%d", i)}
				ctx := TimeoutCtx(context.Background(), 10*time.Second)
				resp, err := workerClient.PostRequest(ctx, "/v1/chat/completions", InvalidRequestBody, headers)
				if err == nil {
					io.ReadAll(resp.Body)
					resp.Body.Close()
				}
			}

			By("send multiple 499 requests at Level 3 (client cancellation), verify circuit breaker does not trigger")
			l3Initial499 := getMetricValue("rs:infer_requests_failure_status_total", `status_code="499"`)
			for i := 0; i < 3; i++ {
				sendCancelledRequest(fmt.Sprintf("cb-499-l3-%d", i))
			}

			By("verify 2xx/4xx/499 metric increment (metrics are recorded normally)")
			Eventually(func() float64 {
				return getMetricValue("rs:infer_requests_failure_status_total", `status_code="200"`) - l3Initial200
			}, 10*time.Second, 1*time.Second).Should(BeNumerically(">=", 3),
				"2xx requests at Level 3 should be recorded by metrics")
			Eventually(func() float64 {
				return getMetricValue("rs:infer_requests_failure_status_total", `status_code="400"`) - l3Initial400
			}, 10*time.Second, 1*time.Second).Should(BeNumerically(">=", 3),
				"4xx requests at Level 3 should be recorded by metrics")
			Eventually(func() float64 {
				return getMetricValue("rs:infer_requests_failure_status_total", `status_code="499"`) - l3Initial499
			}, 10*time.Second, 1*time.Second).Should(BeNumerically(">=", 3),
				"499 requests at Level 3 should be recorded by metrics")

			By("verify 2xx/4xx/499 requests at Level 3 do not trigger circuit breaker")
			// 2xx/4xx/499 are filtered by is2xxStatusCode/is4xxStatusCode and not counted in errorCount,
			// so even if request count reaches MinRequestCount, failure rate is still 0 and circuit breaker won't trigger
			Consistently(func() bool {
				ready, msg, err := workerClient.GetWorkerReadiness()
				if err != nil {
					return false
				}
				GinkgoWriter.Printf("[cb-non5xx-l3] ready=%v, msg=%s\n", ready, msg)
				return !strings.Contains(msg, "circuit breaker is open")
			}, 10*time.Second, 2*time.Second).Should(BeTrue(),
				"2xx/4xx/499 requests at Level 3 should not trigger circuit breaker")

			// ---- Phase 2b: send 5xx requests at Level 3, verify circuit breaker triggers ----
			By("set decode mock to return 500 error again")
			mockErrorWithCode(decodeMock, 500)

			By("record metric values before sending 5xx requests at Level 3")
			l3Initial500 := getMetricValue("rs:infer_requests_failure_status_total", `status_code="500"`)
			l3InitialTotal, _, _ := workerClient.GetMetricsValue("rs:infer_requests_failure_status_total", "")
			GinkgoWriter.Printf("[l3-before-5xx] 500=%.0f, total_raw=%.0f\n", l3Initial500, l3InitialTotal)

			By("send 5xx requests at Level 3 to make circuit breaker detect high failure rate within check window")
			for i := 0; i < 5; i++ {
				body := NewBasicChatRequestBody()
				body.MaxTokens = 10
				body.Stream = false
				headers := map[string]string{constant.XRequestId: fmt.Sprintf("cb-5xx-l3-%d", i)}
				ctx := TimeoutCtx(context.Background(), 30*time.Second)
				resp, err := workerClient.SendChatCompletion(ctx, body, headers)
				if err == nil {
					io.ReadAll(resp.Body)
					resp.Body.Close()
				}
			}

			By("verify 5xx metric increment at Level 3")
			Eventually(func() float64 {
				return getMetricValue("rs:infer_requests_failure_status_total", `status_code="500"`) - l3Initial500
			}, 10*time.Second, 1*time.Second).Should(BeNumerically(">=", 3),
				"5xx requests at Level 3 should be recorded by metrics")

			By("wait for circuit breaker to trigger (timer or probe detects failure rate exceeding threshold)")
			Eventually(func() bool {
				ready, msg, err := workerClient.GetWorkerReadiness()
				if err != nil {
					return false
				}
				GinkgoWriter.Printf("[cb-check] ready=%v, msg=%s\n", ready, msg)
				return !ready && strings.Contains(msg, "circuit breaker is open")
			}, 30*time.Second, 2*time.Second).Should(BeTrue(),
				"5xx request failure rate exceeds threshold at Level 3, circuit breaker should trigger")

			By("restore mock to normal")
			decodeMock.RemoveError()

			// ---- Phase 3: restart leader, verify worker readiness recovers and circuit breaker closes ----
			By("restart leader route-server")
			env := os.Environ()
			env = append(env, fmt.Sprintf("%s=%s", constant.EnvServerConfigPath, filepath.Join(leaderCfgDir, "config")))
			restartCmd := exec.Command(rsPath)
			restartCmd.Env = env
			newLeaderSess, startErr := gexec.Start(restartCmd, io.Discard, GinkgoWriter)
			Expect(startErr).NotTo(HaveOccurred())
			checkRsListenSucceess(newLeaderSess)
			leaderSession = newLeaderSess
			GinkgoWriter.Printf("[leader] restarted on port %s\n", cbLeaderHTTPPort)

			By("wait for leader to be re-elected")
			Eventually(func() bool {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				resp, getErr := etcdClient.Get(ctx, cbRsLeaderKey, clientv3.WithPrefix())
				if getErr != nil || len(resp.Kvs) == 0 {
					return false
				}
				return strings.Contains(string(resp.Kvs[0].Value), "leader1")
			}, 30*time.Second, 1*time.Second).Should(BeTrue(), "leader1 should be re-elected after restart")

			By("wait for worker health cache to sync from leader, readiness to recover, and circuit breaker to close")
			Eventually(func() bool {
				ready, msg, err := workerClient.GetWorkerReadiness()
				if err != nil {
					GinkgoWriter.Printf("[recovery] readiness check error: %v\n", err)
					return false
				}
				GinkgoWriter.Printf("[recovery] ready=%v, msg=%s\n", ready, msg)
				return ready && !strings.Contains(msg, "circuit breaker is open")
			}, 60*time.Second, 2*time.Second).Should(BeTrue(),
				"after leader recovers, worker health cache should sync, readiness should recover, and circuit breaker should close")
		})
	})
})
