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
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gbytes"
	"github.com/onsi/gomega/gexec"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/server/v3/embed"

	commoncfg "route-server/pkg/common/config"
	"route-server/pkg/common/constant"
	commonScheduler "route-server/pkg/common/scheduler"
)

const (
	workerFaultCtx = "worker-fault-test"

	workerFaultEtcdClientPort  = "40100"
	workerFaultEtcdPeerPort    = "40101"
	workerFaultLeaderHTTPPort  = "40102"
	workerFaultLeaderOpsPort   = "40112"
	workerFaultWorkerHTTPPort  = "40103"
	workerFaultWorkerOpsPort   = "40113"
	workerFaultWorker2HTTPPort = "40104"
	workerFaultWorker2OpsPort  = "40114"
	workerFaultPrefill1Port    = "40105"
	workerFaultDecode1Port     = "40106"

	workerFaultPrefill1ID    = "prefill1"
	workerFaultDecode1ID     = "decode1"
	workerFaultRsLeaderKey   = "/rs/leader"
	workerFaultRsPeersAllDir = "/rs/cluster/state/peers/all/"
	workerFaultRsPeers       = "/rs/peers/"

	workerFaultHealthCheckIntervalMs = 5000
	workerFaultHealthConsecutiveTime = 3
	workerFaultEtcdCacheExpireMs     = 40000
	workerFaultHealthCacheExpireMs   = 3000 // 3 * EventThresholdIntervalMs

	mockVLLMTTFT = 5000
)

var _ = Describe(workerFaultCtx, Ordered, func() {
	var (
		candidateSession *gexec.Session
		workerSession    *gexec.Session
		worker2Session   *gexec.Session
		mockServers      []*gexec.Session
		etcdServer       *embed.Etcd
		etcdClient       *clientv3.Client
		candidateCfgDir  string
		workerCfgDir     string
		worker2CfgDir    string
	)

	// ================================================================
	// BeforeAll / AfterAll shared (start etcd, mock, RS)
	// ================================================================
	BeforeAll(func() {
		By("step 0: clean up any stale config dirs from previous runs")
		candidateCfgDir = filepath.Join(projectRoot, "e2etest", "environment", workerFaultCtx, "candidate1")
		workerCfgDir = filepath.Join(projectRoot, "e2etest", "environment", workerFaultCtx, "worker1")
		worker2CfgDir = filepath.Join(projectRoot, "e2etest", "environment", workerFaultCtx, "worker2")
		os.RemoveAll(candidateCfgDir)
		os.RemoveAll(workerCfgDir)
		os.RemoveAll(worker2CfgDir)

		By("step 0: ensure all ports are free")
		EnsurePorts([]string{workerFaultEtcdClientPort, workerFaultEtcdPeerPort,
			workerFaultLeaderHTTPPort, workerFaultWorkerHTTPPort, workerFaultWorker2HTTPPort,
			workerFaultLeaderOpsPort, workerFaultWorkerOpsPort, workerFaultWorker2OpsPort,
			workerFaultPrefill1Port, workerFaultDecode1Port})
		time.Sleep(500 * time.Millisecond)

		By("step 1: start embedded etcd")
		etcdServer, etcdClient = StartEmbeddedEtcd(workerFaultEtcdClientPort, workerFaultEtcdPeerPort, "worker-fault-test-etcd")

		By("step 2: generate RS configs via config-tool")
		deployPath := filepath.Join(projectRoot, "deploy")
		etcdCtxDir := filepath.Join(projectRoot, "e2etest", "environment", workerFaultCtx)
		candidateCfgDir = filepath.Join(etcdCtxDir, "candidate1")
		workerCfgDir = filepath.Join(etcdCtxDir, "worker1")
		worker2CfgDir = filepath.Join(etcdCtxDir, "worker2")

		genRsConfig := func(name, targetDir, port, remoteSchedulerType string, candidate bool) {
			Expect(os.RemoveAll(targetDir)).To(BeNil())
			Expect(os.MkdirAll(filepath.Join(targetDir, "config"), 0755)).To(BeNil())
			out, err := exec.Command(configToolPath, DisableHTTPSConfig([]string{
				"-template", filepath.Join(deployPath, ROUTE_SERVER_TEMPLATE),
				"-host", "127.0.0.1",
				"-target", targetDir,
				"-infer-mode", "PD",
				"-prefills", fmt.Sprintf("prefill1#127.0.0.1:%s", workerFaultPrefill1Port),
				"-decodes", fmt.Sprintf("decode1#127.0.0.1:%s", workerFaultDecode1Port),
				"-name", name,
				"-remote-scheduler-type", remoteSchedulerType,
				"-port", port,
				"-candidate", fmt.Sprintf("%t", candidate),
				"-etcd-endpoints", fmt.Sprintf("127.0.0.1:%s", workerFaultEtcdClientPort),
				"-infer-module-version", "pipeline",
			})...).CombinedOutput()
			GinkgoWriter.Printf("[config] %s config-tool output: %s\n", name, string(out))
			Expect(err).To(BeNil())
		}
		genRsConfig("candidate1", candidateCfgDir, workerFaultLeaderHTTPPort, "disable", true)
		genRsConfig("worker1", workerCfgDir, workerFaultWorkerHTTPPort, "leader", false)
		genRsConfig("worker2", worker2CfgDir, workerFaultWorker2HTTPPort, "leader", false)

		for _, cfgDir := range []string{candidateCfgDir, workerCfgDir, worker2CfgDir} {
			Expect(os.MkdirAll(filepath.Join(cfgDir, "config", "pipelines"), 0755)).To(BeNil())
			Expect(copyDir(filepath.Join(deployPath, "pipelines"), filepath.Join(cfgDir, "config", "pipelines"))).To(BeNil())
		}
		metricsCollectorConfigFileName := "metrics_config_vllm_0.11.0.yaml"
		for _, cfgDir := range []string{candidateCfgDir, workerCfgDir, worker2CfgDir} {
			Expect(copyFile(
				filepath.Join(deployPath, metricsCollectorConfigFileName),
				filepath.Join(cfgDir, "config", metricsCollectorConfigFileName),
			)).To(BeNil())
		}

		By("step 3: start mock vllm instances (1 prefill + 1 decode) with long ttft")
		mockInstances := []struct {
			port string
			name string
		}{
			{workerFaultPrefill1Port, "prefill1"},
			{workerFaultDecode1Port, "decode1"},
		}
		for _, inst := range mockInstances {
			params := []string{
				"-addr", "127.0.0.1",
				"-port", inst.port,
				"-name", inst.name,
				"-ttft", fmt.Sprintf("%d", mockVLLMTTFT),
			}
			command := exec.Command(mockVLLMPath, params...)
			sess, startErr := gexec.Start(command, io.Discard, GinkgoWriter)
			Expect(startErr).NotTo(HaveOccurred())
			checkMockVllmListenSucceess(sess)
			mockServers = append(mockServers, sess)
			GinkgoWriter.Printf("[mock] started %s on port %s with ttft=%dms\n", inst.name, inst.port, mockVLLMTTFT)
		}
	})

	AfterAll(func() {
		By("cleanup route-servers")
		if workerSession != nil {
			workerSession.Kill()
			workerSession.Wait()
		}
		if worker2Session != nil {
			worker2Session.Kill()
			worker2Session.Wait()
		}
		if candidateSession != nil {
			candidateSession.Kill()
			candidateSession.Wait()
		}
		By("cleanup mock servers")
		for _, mock := range mockServers {
			mock.Kill()
			mock.Wait()
		}
		By("cleanup etcd server")
		if etcdClient != nil {
			etcdClient.Close()
		}
		if etcdServer != nil {
			etcdServer.Close()
		}
	})

	// -------------------------------------------------------
	// Helper functions
	// -------------------------------------------------------
	startRsInstance := func(cfgDir string) *gexec.Session {
		env := os.Environ()
		env = append(env, fmt.Sprintf("%s=%s",
			constant.EnvServerConfigPath, filepath.Join(cfgDir, "config")))
		command := exec.Command(rsPath)
		command.Env = env
		sess, err := gexec.Start(command, io.Discard, GinkgoWriter)
		Expect(err).NotTo(HaveOccurred())
		checkRsListenSucceess(sess)
		return sess
	}

	startCandidateAndWorker := func(workerCount int) {
		By("start candidate1")
		candidateSession = startRsInstance(candidateCfgDir)

		By("start worker1")
		workerSession = startRsInstance(workerCfgDir)

		if workerCount >= 2 {
			By("start worker2")
			worker2Session = startRsInstance(worker2CfgDir)
		}

		By("wait for leader election via etcd")
		Eventually(func() bool {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			resp, getErr := etcdClient.Get(ctx, workerFaultRsLeaderKey, clientv3.WithPrefix())
			if getErr != nil || len(resp.Kvs) == 0 {
				return false
			}
			leaderValue := string(resp.Kvs[0].Value)
			GinkgoWriter.Printf("[leader] etcd reported leader: %s\n", leaderValue)
			return strings.Contains(leaderValue, "candidate1")
		}, 30*time.Second, 1*time.Second).Should(BeTrue(), "a leader should be elected")

		By("wait for worker1 to be registered in etcd")
		Eventually(func() bool {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			resp, getErr := etcdClient.Get(ctx, workerFaultRsPeersAllDir, clientv3.WithPrefix())
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

		if workerCount >= 2 {
			By("wait for worker2 to be registered in etcd")
			Eventually(func() bool {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				resp, getErr := etcdClient.Get(ctx, workerFaultRsPeersAllDir, clientv3.WithPrefix())
				if getErr != nil || len(resp.Kvs) == 0 {
					return false
				}
				for _, kv := range resp.Kvs {
					if strings.Contains(string(kv.Key), "worker2") {
						return true
					}
				}
				return false
			}, 30*time.Second, 1*time.Second).Should(BeTrue(), "worker2 should be registered in etcd")
		}

		By("wait for worker health cache sync")
		wc := NewRSClient("http://localhost:" + workerFaultWorkerHTTPPort)
		Eventually(func() bool {
			ready, msg, err := wc.GetWorkerReadiness()
			if err != nil {
				GinkgoWriter.Printf("[worker] readiness check error: %v\n", err)
				return false
			}
			GinkgoWriter.Printf("[worker] readiness: ready=%v, msg=%s\n", ready, msg)
			return strings.Contains(msg, `"message":"ready"`)
		}, 60*time.Second, 2*time.Second).Should(BeTrue(), "worker health cache should be synced")
	}

	killAllRs := func() {
		By("kill all route-servers")
		if workerSession != nil {
			workerSession.Kill()
			workerSession.Wait()
		}
		if worker2Session != nil {
			worker2Session.Kill()
			worker2Session.Wait()
		}
		if candidateSession != nil {
			candidateSession.Kill()
			candidateSession.Wait()
		}
		time.Sleep(1 * time.Second)
	}

	ensureMockInstancesHealthy := func() {
		mockPorts := []string{workerFaultPrefill1Port, workerFaultDecode1Port}
		for i, port := range mockPorts {
			client := NewMockServerClient(port)
			resp, err := client.MockHealth()
			if err != nil || resp == nil || resp.StatusCode != http.StatusOK {
				GinkgoWriter.Printf("[mock] instance on port %s not responding, restarting...\n", port)
				if mockServers[i] != nil {
					mockServers[i].Kill()
					mockServers[i].Wait()
				}
				params := []string{
					"-addr", "127.0.0.1",
					"-port", port,
					"-ttft", fmt.Sprintf("%d", mockVLLMTTFT),
				}
				command := exec.Command(mockVLLMPath, params...)
				sess, startErr := gexec.Start(command, io.Discard, GinkgoWriter)
				Expect(startErr).NotTo(HaveOccurred())
				checkMockVllmListenSucceess(sess)
				mockServers[i] = sess
				GinkgoWriter.Printf("[mock] restarted instance on port %s\n", port)
			} else {
				resp.Body.Close()
			}
		}
		time.Sleep(500 * time.Millisecond)
	}

	ensureEtcdClean := func() {
		ctx := context.Background()
		etcdClient.Delete(ctx, "", clientv3.WithPrefix())
		GinkgoWriter.Printf("[etcd] cleaned RS keys\n")
	}

	// ================================================================
	// Context1: Leader fault scenario test (1candidate + 1worker)
	// ================================================================
	Context("Leader fault scenario test", func() {
		var workerClient *RSClient

		BeforeAll(func() {
			killAllRs()
			ensureEtcdClean()
			ensureMockInstancesHealthy()
			workerClient = NewRSClient("http://localhost:" + workerFaultWorkerHTTPPort)
			startCandidateAndWorker(1)
		})

		It("leader kill and recovery", func() {
			By("a. worker /worker/readiness should be true")
			Eventually(func() bool {
				ready, msg, err := workerClient.GetWorkerReadiness()
				if err != nil {
					GinkgoWriter.Printf("[worker] readiness check error: %v\n", err)
					return false
				}
				GinkgoWriter.Printf("[worker] readiness: ready=%v, msg=%s\n", ready, msg)
				return strings.Contains(msg, `"message":"ready"`)
			}, 10*time.Second).Should(BeTrue(), "worker health cache should be synced")

			By("b. send inference request to worker, verify remote schedule log and success")
			ctx := TimeoutCtx(context.Background(), 30*time.Second)
			resp, err := workerClient.SendChatCompletion(ctx, NewBasicChatRequestBody(), nil)
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusOK))
			Eventually(workerSession.Out, 5*time.Second).Should(gbytes.Say("send scheduler for"),
				"should see remote schedule log")

			By("c. verify event sync is normal, rs:number_events_remaining should be 0")
			Eventually(func() bool {
				metricsResp, err := workerClient.GetInternalMetrics(map[string]string{"only_rs": "true"})
				if err != nil || metricsResp.StatusCode != http.StatusOK {
					return false
				}
				defer metricsResp.Body.Close()
				val, err := ExtractMetricValue(metricsResp, "rs:number_events_remaining", nil)
				if err != nil {
					return false
				}
				GinkgoWriter.Printf("[001] rs:number_events_remaining = %.2f\n", val)
				return val == 0
			}, 30*time.Second, 2*time.Second).Should(BeTrue(), "rs:number_events_remaining should be 0 after sync")

			By("send a request asynchronously and wait for prefill to start processing")
			prefillStarted := make(chan struct{})
			var sendResp *http.Response
			var sendErr error
			go func() {
				defer GinkgoRecover()
				Eventually(workerSession.Out, 30*time.Second).Should(gbytes.Say("Prefill_start"),
					"should see Prefill_start log indicating prefill has started")
				prefillStarted <- struct{}{}
			}()
			go func() {
				defer GinkgoRecover()
				requestCtx := TimeoutCtx(context.Background(), 60*time.Second)
				sendResp, sendErr = workerClient.SendChatCompletion(requestCtx, NewBasicChatRequestBody(), nil)
			}()

			<-prefillStarted
			GinkgoWriter.Printf("[002] prefill started, now killing leader\n")

			By("kill leader process")
			candidateSession.Kill()
			candidateSession.Wait()
			GinkgoWriter.Printf("[002] leader process killed\n")

			By("a. verify the scheduled request can complete successfully")
			Eventually(func() bool {
				return sendResp != nil
			}, time.Duration(mockVLLMTTFT*2+10000)*time.Millisecond, 1*time.Second).Should(BeTrue(), "request should complete")
			Expect(sendErr).NotTo(HaveOccurred())
			Expect(sendResp.StatusCode).Should(Equal(http.StatusOK))
			defer sendResp.Body.Close()
			GinkgoWriter.Printf("[002] request completed, status: %d\n", sendResp.StatusCode)

			By("b. wait for health cache to expire")
			maxWaitForHealthCache := time.Duration(workerFaultHealthCacheExpireMs+1000) * time.Millisecond
			GinkgoWriter.Printf("[002] waiting for health cache expiry (max %.0fs)...\n", maxWaitForHealthCache.Seconds())
			Eventually(func() bool {
				// Call GetWorkerReadiness to trigger health cache check and produce expiry log
				workerClient.GetWorkerReadiness()
				return strings.Contains(string(workerSession.Out.Contents()), "Health stats cache expired, last update")
			}, maxWaitForHealthCache, 1*time.Second).Should(BeTrue(),
				"should see worker health cache expire log")

			By("c. check rs:number_events_remaining metric increases")
			Eventually(func() bool {
				metricsResp, err := workerClient.GetInternalMetrics(map[string]string{"only_rs": "true"})
				if err != nil || metricsResp.StatusCode != http.StatusOK {
					return false
				}
				defer metricsResp.Body.Close()
				val, err := ExtractMetricValue(metricsResp, "rs:number_events_remaining", nil)
				if err != nil {
					return false
				}
				GinkgoWriter.Printf("[002] rs:number_events_remaining = %.2f\n", val)
				return val > 0
			}, 30*time.Second, 2*time.Second).Should(BeTrue(), "rs:number_events_remaining should increase after leader kill")

			By("d. wait for etcd cache to expire")
			Eventually(workerSession.Out, workerFaultEtcdCacheExpireMs).Should(gbytes.Say("ETCD health stats cache expired, updateTime"),
				"should see worker etcd health cache expire log")
			GinkgoWriter.Printf("[002] waiting for etcd cache expiry (~%dms)...\n", workerFaultEtcdCacheExpireMs)

			By("e. send request to worker, verify rs:num_fallback_scheduled increases and request succeeds")
			fallbackBefore, err := workerClient.GetRSMetric("rs:num_fallback_scheduled", map[string]string{"only_rs": "true"})
			Expect(err).NotTo(HaveOccurred())
			GinkgoWriter.Printf("[002] rs:num_fallback_scheduled before request: %.2f\n", fallbackBefore)

			fallbackCtx := TimeoutCtx(context.Background(), 30*time.Second)
			fallbackResp, fallbackErr := workerClient.SendChatCompletion(fallbackCtx, NewBasicChatRequestBody(), nil)
			Expect(fallbackErr).NotTo(HaveOccurred())
			defer fallbackResp.Body.Close()
			Expect(fallbackResp.StatusCode).To(Equal(http.StatusOK))

			Eventually(func() bool {
				val, err := workerClient.GetRSMetric("rs:num_fallback_scheduled", map[string]string{"only_rs": "true"})
				if err != nil {
					return false
				}
				GinkgoWriter.Printf("[002] rs:num_fallback_scheduled after request: %.2f\n", val)
				return val > fallbackBefore
			}, 10*time.Second, 1*time.Second).Should(BeTrue(), "rs:num_fallback_scheduled should increase after fallback request")

			By("restart leader process")
			candidateSession = startRsInstance(candidateCfgDir)
			GinkgoWriter.Printf("[003] leader restarted\n")

			By("wait for rs:number_events_remaining to become 0 again")
			Eventually(func() bool {
				metricsResp, err := workerClient.GetInternalMetrics(map[string]string{"only_rs": "true"})
				if err != nil || metricsResp.StatusCode != http.StatusOK {
					return false
				}
				defer metricsResp.Body.Close()
				val, err := ExtractMetricValue(metricsResp, "rs:number_events_remaining", nil)
				if err != nil {
					return false
				}
				GinkgoWriter.Printf("[003] rs:number_events_remaining = %.2f\n", val)
				return val == 0
			}, 60*time.Second, 2*time.Second).Should(BeTrue(), "rs:number_events_remaining should be 0 after leader recovery")

			By("verify worker Get /leader returns valid info (not 503)")
			leaderID, statusCode, err := workerClient.GetLeader()
			Expect(err).NotTo(HaveOccurred())
			Expect(leaderID).NotTo(BeEmpty())
			Expect(statusCode).To(Equal(http.StatusOK))
			GinkgoWriter.Printf("[003] worker reported leader: %s (status=%d)\n", leaderID, statusCode)

			By("send request and verify rs:num_fallback_scheduled does not increase (normal scheduling)")
			fallbackBefore2, err := workerClient.GetRSMetric("rs:num_fallback_scheduled", map[string]string{"only_rs": "true"})
			Expect(err).NotTo(HaveOccurred())
			GinkgoWriter.Printf("[003] rs:num_fallback_scheduled before request: %.2f\n", fallbackBefore2)

			normalCtx := TimeoutCtx(context.Background(), 30*time.Second)
			normalResp, normalErr := workerClient.SendChatCompletion(normalCtx, NewBasicChatRequestBody(), nil)
			Expect(normalErr).NotTo(HaveOccurred())
			defer normalResp.Body.Close()
			Expect(normalResp.StatusCode).To(Equal(http.StatusOK))

			time.Sleep(2 * time.Second)
			fallbackAfter, err := workerClient.GetRSMetric("rs:num_fallback_scheduled", map[string]string{"only_rs": "true"})
			Expect(err).NotTo(HaveOccurred())
			GinkgoWriter.Printf("[003] rs:num_fallback_scheduled after request: %.2f\n", fallbackAfter)
			Expect(fallbackAfter).To(Equal(fallbackBefore2), "rs:num_fallback_scheduled should not increase when leader is healthy")
		})
	})

	// ================================================================
	// Context2: etcd fault scenario test (1candidate + 2worker)
	// ================================================================
	Context("etcd fault scenario test", func() {
		var workerClient *RSClient
		var worker2Client *RSClient

		BeforeAll(func() {
			killAllRs()
			ensureEtcdClean()
			ensureMockInstancesHealthy()
			workerClient = NewRSClient("http://localhost:" + workerFaultWorkerHTTPPort)
			worker2Client = NewRSClient("http://localhost:" + workerFaultWorker2HTTPPort)
			startCandidateAndWorker(2)
		})

		It("etcd kill and recovery", func() {
			By("a. worker readiness should be true")
			for _, wc := range []*RSClient{workerClient, worker2Client} {
				ready, msg, err := wc.GetWorkerReadiness()
				Expect(err).NotTo(HaveOccurred())
				GinkgoWriter.Printf("[etcd001] worker(%s) readiness: ready=%v, msg=%s\n", wc.BaseURL, ready, msg)
				Expect(ready).To(BeTrue())
			}

			By("b. send inference request to worker, verify remote schedule log and success")
			ctx := TimeoutCtx(context.Background(), 30*time.Second)
			resp, err := workerClient.SendChatCompletion(ctx, NewBasicChatRequestBody(), nil)
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusOK))
			Eventually(workerSession.Out, 5*time.Second).Should(gbytes.Say("send scheduler for"),
				"should see remote schedule log")

			By("c. verify event sync is normal, rs:number_events_remaining should eventually be 0")
			Eventually(func() bool {
				metricsResp, err := workerClient.GetInternalMetrics(map[string]string{"only_rs": "true"})
				if err != nil || metricsResp.StatusCode != http.StatusOK {
					return false
				}
				defer metricsResp.Body.Close()
				val, err := ExtractMetricValue(metricsResp, "rs:number_events_remaining", nil)
				if err != nil {
					return false
				}
				GinkgoWriter.Printf("[etcd] rs:number_events_remaining = %.2f\n", val)
				return val == 0
			}, 30*time.Second, 2*time.Second).Should(BeTrue(), "rs:number_events_remaining should be 0 after sync")

			By("send a request and wait for prefill to start processing")
			prefillStarted := make(chan struct{})
			var etcdSendResp *http.Response
			var etcdSendErr error
			go func() {
				defer GinkgoRecover()
				Eventually(workerSession.Out, 30*time.Second).Should(gbytes.Say("Prefill_start"),
					"should see Prefill_start log")
				prefillStarted <- struct{}{}
			}()
			go func() {
				defer GinkgoRecover()
				requestCtx := TimeoutCtx(context.Background(), 60*time.Second)
				etcdSendResp, etcdSendErr = workerClient.SendChatCompletion(requestCtx, NewBasicChatRequestBody(), nil)
			}()

			<-prefillStarted
			GinkgoWriter.Printf("[etcd002] prefill started, now killing etcd\n")

			By("kill etcd process")
			etcdServer.Close()
			GinkgoWriter.Printf("[etcd002] etcd server stopped\n")

			By("a. verify the scheduled request can complete successfully")
			Eventually(func() bool {
				return etcdSendResp != nil
			}, time.Duration(mockVLLMTTFT*2+10000)*time.Millisecond, 1*time.Second).Should(BeTrue(), "request should complete")
			Expect(etcdSendErr).NotTo(HaveOccurred())
			Expect(etcdSendResp.StatusCode).To(Equal(http.StatusOK))
			defer etcdSendResp.Body.Close()
			GinkgoWriter.Printf("[etcd002] request completed, status: %d\n", etcdSendResp.StatusCode)

			By("b. wait for health cache to expire")
			maxWaitForHealthCache := time.Duration(workerFaultHealthConsecutiveTime*workerFaultHealthCheckIntervalMs+1000) * time.Millisecond
			GinkgoWriter.Printf("[etcd002] waiting for health cache expiry (max %.0fs)...\n", maxWaitForHealthCache.Seconds())
			Eventually(func() bool {
				workerClient.GetWorkerReadiness()
				return strings.Contains(string(workerSession.Out.Contents()), "Health stats cache expired, last update")
			}, maxWaitForHealthCache, 1*time.Second).Should(BeTrue(),
				"should see worker health cache expire log")

			By("c. verify event send failed log appears")
			Eventually(workerSession.Out, 10*time.Second, time.Second).Should(gbytes.Say("send WorkerEventsRequest to leader failed"),
				"should see event send failed log")

			By("d. verify worker detects etcd connection loss and enters reconnection")
			Eventually(workerSession.Out, 30*time.Second, 2*time.Second).Should(gbytes.Say("connection lost, reconnecting"),
				"should see etcd connection lost log")

			By("e. send request, verify rs:num_fallback_scheduled increases and request succeeds")
			etcdFallbackBefore, err := workerClient.GetRSMetric("rs:num_fallback_scheduled", map[string]string{"only_rs": "true"})
			Expect(err).NotTo(HaveOccurred())
			GinkgoWriter.Printf("[etcd002] rs:num_fallback_scheduled before request: %.2f\n", etcdFallbackBefore)

			fallbackCtx := TimeoutCtx(context.Background(), 30*time.Second)
			fallbackResp, fallbackErr := workerClient.SendChatCompletion(fallbackCtx, NewBasicChatRequestBody(), nil)
			Expect(fallbackErr).NotTo(HaveOccurred())
			defer fallbackResp.Body.Close()
			Expect(fallbackResp.StatusCode).To(Equal(http.StatusOK))

			Eventually(func() bool {
				val, err := workerClient.GetRSMetric("rs:num_fallback_scheduled", map[string]string{"only_rs": "true"})
				if err != nil {
					return false
				}
				GinkgoWriter.Printf("[etcd002] rs:num_fallback_scheduled after request: %.2f\n", val)
				return val > etcdFallbackBefore
			}, 10*time.Second, 1*time.Second).Should(BeTrue(), "rs:num_fallback_scheduled should increase after fallback request")

			By("d. wait for etcd cache to expire")
			Eventually(workerSession.Out, workerFaultEtcdCacheExpireMs).Should(gbytes.Say("ETCD health stats cache expired, updateTime"),
				"should see worker etcd health cache expire log")
			GinkgoWriter.Printf("[etcd002] waiting for etcd cache expiry (~%dms)...\n", workerFaultEtcdCacheExpireMs)

			By("restart etcd and wait for RS auto-reconnect")
			etcdServer, etcdClient = StartEmbeddedEtcd(workerFaultEtcdClientPort, workerFaultEtcdPeerPort, "worker-fault-test-etcd")
			Eventually(workerSession.Out, 30*time.Second, 2*time.Second).Should(gbytes.Say("Successfully connected to etcd cluster"),
				"worker should reconnect to etcd")

			By("wait for candidate to re-elect as leader")
			Eventually(func() bool {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				resp, getErr := etcdClient.Get(ctx, workerFaultRsLeaderKey, clientv3.WithPrefix())
				if getErr != nil || len(resp.Kvs) == 0 {
					return false
				}
				leaderValue := string(resp.Kvs[0].Value)
				GinkgoWriter.Printf("[etcd003] etcd leader: %s\n", leaderValue)
				return strings.Contains(leaderValue, "candidate1")
			}, 60*time.Second, 1*time.Second).Should(BeTrue(), "candidate1 should re-elect as leader")

			By("wait for worker health cache sync")
			Eventually(func() bool {
				ready, msg, err := workerClient.GetWorkerReadiness()
				if err != nil {
					return false
				}
				GinkgoWriter.Printf("[etcd003] worker readiness: ready=%v, msg=%s\n", ready, msg)
				return strings.Contains(msg, `"message":"ready"`)
			}, 60*time.Second, 2*time.Second).Should(BeTrue(), "worker health cache should be synced")

			By("wait for rs:number_events_remaining metric to eventually be 0")
			Eventually(func() bool {
				metricsResp, err := workerClient.GetInternalMetrics(map[string]string{"only_rs": "true"})
				if err != nil || metricsResp.StatusCode != http.StatusOK {
					return false
				}
				defer metricsResp.Body.Close()
				val, err := ExtractMetricValue(metricsResp, "rs:number_events_remaining", nil)
				if err != nil {
					return false
				}
				GinkgoWriter.Printf("[etcd003] rs:number_events_remaining = %.2f\n", val)
				return val == 0
			}, 60*time.Second, 2*time.Second).Should(BeTrue(), "rs:number_events_remaining should be 0 after etcd recovery")

			By("verify worker Get /leader no longer returns 503, and all workers agree")
			var leaderID1, leaderID2 string
			Eventually(func() bool {
				id, _, err := workerClient.GetLeader()
				if err != nil {
					return false
				}
				leaderID1 = id
				return leaderID1 != ""
			}, 30*time.Second, 1*time.Second).Should(BeTrue(), "worker1 should get a leader")
			Eventually(func() bool {
				id, _, err := worker2Client.GetLeader()
				if err != nil {
					return false
				}
				leaderID2 = id
				return leaderID2 != ""
			}, 30*time.Second, 1*time.Second).Should(BeTrue(), "worker2 should get a leader")
			GinkgoWriter.Printf("[etcd003] worker1 leader: %s, worker2 leader: %s\n", leaderID1, leaderID2)
			Expect(leaderID1).To(Equal(leaderID2), "all workers should report the same leader")

			By("a. send request, verify rs:num_fallback_scheduled does not increase")
			etcdFallbackBefore2, err := workerClient.GetRSMetric("rs:num_fallback_scheduled", map[string]string{"only_rs": "true"})
			Expect(err).NotTo(HaveOccurred())
			GinkgoWriter.Printf("[etcd003] rs:num_fallback_scheduled before request: %.2f\n", etcdFallbackBefore2)

			normalCtx := TimeoutCtx(context.Background(), 30*time.Second)
			normalResp, normalErr := workerClient.SendChatCompletion(normalCtx, NewBasicChatRequestBody(), nil)
			Expect(normalErr).NotTo(HaveOccurred())
			defer normalResp.Body.Close()
			Expect(normalResp.StatusCode).To(Equal(http.StatusOK))

			time.Sleep(2 * time.Second)
			etcdFallbackAfter, err := workerClient.GetRSMetric("rs:num_fallback_scheduled", map[string]string{"only_rs": "true"})
			Expect(err).NotTo(HaveOccurred())
			GinkgoWriter.Printf("[etcd003] rs:num_fallback_scheduled after request: %.2f\n", etcdFallbackAfter)
			Expect(etcdFallbackAfter).To(Equal(etcdFallbackBefore2), "rs:num_fallback_scheduled should not increase when etcd is healthy")

			By("b. verify rs:etcd_connection_retries metric does not increase")
			retriesBefore, err := workerClient.GetRSMetric("rs:etcd_connection_retries", map[string]string{"only_rs": "true"})
			if err == nil {
				GinkgoWriter.Printf("[etcd003] rs:etcd_connection_retries = %.2f\n", retriesBefore)
				time.Sleep(5 * time.Second)
				retriesAfter, err := workerClient.GetRSMetric("rs:etcd_connection_retries", map[string]string{"only_rs": "true"})
				if err == nil {
					GinkgoWriter.Printf("[etcd003] rs:etcd_connection_retries after: %.2f\n", retriesAfter)
					Expect(retriesAfter).To(Equal(retriesBefore), "rs:etcd_connection_retries should not increase when etcd is healthy")
				}
			}

		})
	})

	// ================================================================
	// Context3: Worker network partition scenario test (1candidate + 2worker)
	// Simulate worker1 disconnecting from leader and etcd sequentially, verify worker is cleaned by leader
	// ================================================================
	Context("Worker network partition scenario test", func() {
		var (
			workerClient *RSClient
			leaderClient *RSClient

			// etcd proxy: worker1 connects to etcd via this proxy
			worker1EtcdProxyRule *etcdProxyRule
			leaderHTTPProxyRule  *etcdProxyRule
			testCtxDir           string
			initialized          bool
		)

		// Independent ports used by this new Context
		const (
			npEtcdClientPort  = "14890"
			npEtcdPeerPort    = "14891"
			npLeaderHTTPPort  = "45888"
			npWorkerHTTPPort  = "45889"
			npWorkerOpsPort   = "45899"
			npWorker2HTTPPort = "45891"
			npWorker2OpsPort  = "45901"
			npPrefill1Port    = "65010"
			npDecode1Port     = "65012"
			npEtcdProxyPort   = "14895"
			npLeaderProxyPort = "45895"
		)

		BeforeAll(func() {
			if !AllowIptables() {
				Skip(fmt.Sprintf("%s not set to true, skip iptables-based test", ROUTER_TEST_ENABLE_SU_ENV))
			}
			By("step 0: clean up any stale config dirs from previous runs")
			testCtxDir = filepath.Join(projectRoot, "e2etest", "environment", workerFaultCtx+"-network-partition")
			os.RemoveAll(testCtxDir)

			By("step 0b: clean up any stale iptables rules (DROP and REJECT)")
			UnblockTrafficToPort(npLeaderHTTPPort)
			// Clean up residual iptables rules from previous test runs
			exec.Command("sudo", "iptables", "-D", "INPUT", "-p", "tcp", "--dport", npLeaderHTTPPort,
				"-j", "REJECT", "--reject-with", "tcp-reset").Run()
			exec.Command("sudo", "iptables", "-D", "OUTPUT", "-p", "tcp", "--dport", npEtcdProxyPort,
				"-j", "DROP").Run()

			By("step 0b: ensure all ports are free")
			EnsurePorts([]string{
				npEtcdClientPort, npEtcdPeerPort,
				npEtcdProxyPort,
				npLeaderHTTPPort, npLeaderProxyPort,
				npWorkerHTTPPort, npWorkerOpsPort,
				npWorker2HTTPPort, npWorker2OpsPort,
				npPrefill1Port, npDecode1Port,
			})
			time.Sleep(500 * time.Millisecond)

			By("step 1: start embedded etcd")
			etcdServer, etcdClient = StartEmbeddedEtcd(npEtcdClientPort, npEtcdPeerPort,
				"worker-network-partition-etcd")
			ensureEtcdClean()

			By("step 2: start TCP proxy for worker1 -> etcd")
			worker1EtcdProxyRule = new(etcdProxyRule)
			go StartTCPProxy(
				fmt.Sprintf("127.0.0.1:%s", npEtcdProxyPort),
				fmt.Sprintf("127.0.0.1:%s", npEtcdClientPort),
				worker1EtcdProxyRule,
			)

			By("step 2b: start TCP proxy for worker1 -> leader HTTP")
			leaderHTTPProxyRule = new(etcdProxyRule)
			go StartTCPProxy(
				fmt.Sprintf("127.0.0.1:%s", npLeaderProxyPort),
				fmt.Sprintf("127.0.0.1:%s", npLeaderHTTPPort),
				leaderHTTPProxyRule,
			)

			By("step 3: generate RS configs via config-tool with custom template")
			deployPath := filepath.Join(projectRoot, "deploy")
			testCtxDir = filepath.Join(projectRoot, "e2etest", "environment", workerFaultCtx+"-network-partition")
			candidateCfgDir = filepath.Join(testCtxDir, "candidate1")
			workerCfgDir = filepath.Join(testCtxDir, "worker1")
			worker2CfgDir = filepath.Join(testCtxDir, "worker2")

			var cfg commoncfg.Config
			templateContent, err := os.ReadFile(filepath.Join(deployPath, ROUTE_SERVER_TEMPLATE))
			Expect(err).To(BeNil())
			Expect(yaml.Unmarshal(templateContent, &cfg)).To(BeNil())
			cfg.MetaCleanupCfg = &commoncfg.MetaCleanupConfig{
				MaxInstanceDownTimeSec: 30,
				MaxWorkerDownTimeSec:   15,
			}
			cfg.InstanceManagerCfg.HealthCheckConfig.FallbackHealthCheckConfig.ResumeTimeMs = 10000
			outData, err := yaml.Marshal(&cfg)
			Expect(err).To(BeNil())
			targetTemplateFile := filepath.Join(testCtxDir, TEMP_TEMPLATE)
			Expect(os.MkdirAll(filepath.Dir(targetTemplateFile), 0755)).To(BeNil())
			Expect(os.WriteFile(targetTemplateFile, outData, 0644)).To(BeNil())

			genRsConfig := func(name, targetDir, port, remoteSchedulerType string, candidate bool, etcdEndpoint string) {
				Expect(os.RemoveAll(targetDir)).To(BeNil())
				Expect(os.MkdirAll(filepath.Join(targetDir, "config"), 0755)).To(BeNil())
				mode := "etcd"
				out, cmdErr := exec.Command(configToolPath, DisableHTTPSConfig([]string{
					"-template", targetTemplateFile,
					"-host", "127.0.0.1",
					"-target", targetDir,
					"-infer-mode", "PD",
					"-mode", mode,
					"-prefills", fmt.Sprintf("prefill1#127.0.0.1:%s", npPrefill1Port),
					"-decodes", fmt.Sprintf("decode1#127.0.0.1:%s", npDecode1Port),
					"-name", name,
					"-remote-scheduler-type", remoteSchedulerType,
					"-port", port,
					"-candidate", fmt.Sprintf("%t", candidate),
					"-etcd-endpoints", etcdEndpoint,
					"-infer-module-version", "pipeline",
				})...).CombinedOutput()
				GinkgoWriter.Printf("[config] %s config-tool output: %s\n", name, string(out))
				Expect(cmdErr).To(BeNil())
			}

			// Modify worker1 generated config: shorten lease-ttl and extend retry interval
			modifyWorkerEtcdConfig := func(cfgDir string) {
				configFile := filepath.Join(cfgDir, "config", "route-server.yaml")
				data, err := os.ReadFile(configFile)
				Expect(err).To(BeNil())
				var wCfg commoncfg.Config
				Expect(yaml.Unmarshal(data, &wCfg)).To(BeNil())
				if wCfg.ClusterCfg.EtcdCfg != nil {
					wCfg.ClusterCfg.EtcdCfg.LeaseTtlSec = 2
					wCfg.ClusterCfg.EtcdCfg.RetryInterval = 10
				}
				outData, err := yaml.Marshal(&wCfg)
				Expect(err).NotTo(HaveOccurred())
				Expect(os.WriteFile(configFile, outData, 0644)).To(BeNil())
				GinkgoWriter.Printf("[config] modified %s: lease-ttl-sec=2, retry-interval-sec=10\n", configFile)
			}

			genRsConfig("candidate1", candidateCfgDir, npLeaderHTTPPort, "disable", true,
				fmt.Sprintf("127.0.0.1:%s", npEtcdClientPort))
			genRsConfig("worker1", workerCfgDir, npWorkerHTTPPort, "leader", false,
				fmt.Sprintf("127.0.0.1:%s", npEtcdProxyPort))
			modifyWorkerEtcdConfig(workerCfgDir)
			genRsConfig("worker2", worker2CfgDir, npWorker2HTTPPort, "leader", false,
				fmt.Sprintf("127.0.0.1:%s", npEtcdClientPort))

			for _, cfgDir := range []string{candidateCfgDir, workerCfgDir, worker2CfgDir} {
				Expect(os.MkdirAll(filepath.Join(cfgDir, "config", "pipelines"), 0755)).To(BeNil())
				Expect(copyDir(filepath.Join(deployPath, "pipelines"), filepath.Join(cfgDir, "config", "pipelines"))).To(BeNil())
			}
			metricsCollectorConfigFileName := "metrics_config_vllm_0.11.0.yaml"
			for _, cfgDir := range []string{candidateCfgDir, workerCfgDir, worker2CfgDir} {
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
				{npPrefill1Port, "prefill1"},
				{npDecode1Port, "decode1"},
			}
			for _, inst := range mockInstances {
				params := []string{
					"-addr", "127.0.0.1",
					"-port", inst.port,
					"-name", inst.name,
					"-ttft", fmt.Sprintf("%d", mockVLLMTTFT),
				}
				command := exec.Command(mockVLLMPath, params...)
				sess, startErr := gexec.Start(command, io.Discard, GinkgoWriter)
				Expect(startErr).NotTo(HaveOccurred())
				checkMockVllmListenSucceess(sess)
				mockServers = append(mockServers, sess)
				GinkgoWriter.Printf("[mock] started %s on port %s with ttft=%dms\n", inst.name, inst.port, mockVLLMTTFT)
			}

			By("step 4b: double-check ports are free before starting RS")
			EnsurePorts([]string{
				npLeaderHTTPPort, npWorkerHTTPPort, npWorker2HTTPPort,
				npWorkerOpsPort, npWorker2OpsPort,
			})
			time.Sleep(1 * time.Second)

			By("step 5: start candidate1 (leader)")
			candidateSession = startRsInstance(candidateCfgDir)
			Expect(candidateSession.ExitCode()).To(BeNumerically("<", 0),
				"leader should still be running (exit code < 0 means running)")
			// Verify leader is actually listening on npLeaderHTTPPort
			Eventually(func() string {
				cmd := exec.Command("ss", "-tlnp")
				out, err := cmd.CombinedOutput()
				Expect(err).NotTo(HaveOccurred())
				for _, line := range strings.Split(string(out), "\n") {
					if strings.Contains(line, npLeaderHTTPPort) && strings.Contains(line, "LISTEN") {
						return line
					}
				}
				return ""
			}, 15*time.Second, 1*time.Second).Should(ContainSubstring(npLeaderHTTPPort),
				"leader should be listening on port "+npLeaderHTTPPort)
			GinkgoWriter.Printf("[np] leader confirmed listening on port %s\n", npLeaderHTTPPort)

			By("step 6: start worker1")
			workerSession = startRsInstance(workerCfgDir)

			By("step 7: start worker2")
			// Print port 45890 status
			checkPort := exec.Command("ss", "-tlnp", "sport", "= :45890")
			checkOut, _ := checkPort.CombinedOutput()
			GinkgoWriter.Printf("[np] port 45890 before worker2 start:\n%s\n", string(checkOut))
			worker2Session = startRsInstance(worker2CfgDir)

			By("step 8: wait for leader election via etcd")
			Eventually(func() bool {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				resp, getErr := etcdClient.Get(ctx, workerFaultRsLeaderKey, clientv3.WithPrefix())
				if getErr != nil || len(resp.Kvs) == 0 {
					return false
				}
				leaderValue := string(resp.Kvs[0].Value)
				GinkgoWriter.Printf("[leader] etcd reported leader: %s\n", leaderValue)
				return strings.Contains(leaderValue, "candidate1")
			}, 30*time.Second, 1*time.Second).Should(BeTrue(), "a leader should be elected")

			By("step 9: wait for worker1 and worker2 to be registered in etcd")
			for _, workerName := range []string{"worker1", "worker2"} {
				Eventually(func() bool {
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					resp, getErr := etcdClient.Get(ctx, workerFaultRsPeersAllDir, clientv3.WithPrefix())
					if getErr != nil || len(resp.Kvs) == 0 {
						return false
					}
					for _, kv := range resp.Kvs {
						if strings.Contains(string(kv.Key), workerName) {
							return true
						}
					}
					return false
				}, 30*time.Second, 1*time.Second).Should(BeTrue(), "%s should be registered in etcd", workerName)
			}

			By("step 10: wait for worker health cache sync")
			Eventually(func() bool {
				ready, msg, err := NewRSClient("http://localhost:" + npWorkerHTTPPort).GetWorkerReadiness()
				if err != nil {
					GinkgoWriter.Printf("[worker1] readiness check error: %v\n", err)
					return false
				}
				GinkgoWriter.Printf("[worker1] readiness: ready=%v, msg=%s\n", ready, msg)
				return ready
			}, 60*time.Second, 2*time.Second).Should(BeTrue(), "worker1 should be ready")
			Eventually(func() bool {
				ready, msg, err := NewRSClient("http://localhost:" + npWorker2HTTPPort).GetWorkerReadiness()
				if err != nil {
					GinkgoWriter.Printf("[worker2] readiness check error: %v\n", err)
					return false
				}
				GinkgoWriter.Printf("[worker2] readiness: ready=%v, msg=%s\n", ready, msg)
				return ready
			}, 60*time.Second, 2*time.Second).Should(BeTrue(), "worker2 should be ready")

			workerClient = NewRSClient("http://localhost:" + npWorkerHTTPPort)
			leaderClient = NewRSClient("http://localhost:" + npLeaderHTTPPort)

			initialized = true
		})

		AfterAll(func() {
			if !initialized {
				GinkgoWriter.Printf("[np] BeforeAll was skipped, skip AfterAll cleanup\n")
				return
			}

			By("cleanup route-servers")
			killAllRs()

			By("cleanup mock servers")
			for _, mock := range mockServers {
				if mock != nil {
					mock.Kill()
					mock.Wait()
				}
			}

			By("cleanup etcd server")
			if etcdClient != nil {
				etcdClient.Close()
				etcdClient = nil
			}
			if etcdServer != nil {
				done := make(chan struct{})
				go func() {
					etcdServer.Close()
					close(done)
				}()
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					GinkgoWriter.Printf("[np] etcdServer.Close() timed out, force killing\n")
					etcdServer.Server.HardStop()
				}
				etcdServer = nil
			}
		})

		It("worker1 network partition and recovery", func() {
			// ====================================================================
			// Phase 1: Verify initial state is normal after startup
			// ====================================================================
			By("1a. worker readiness should be true")
			ready, msg, err := workerClient.GetWorkerReadiness()
			Expect(err).NotTo(HaveOccurred())
			GinkgoWriter.Printf("[np001] worker1 readiness: ready=%v, msg=%s\n", ready, msg)
			Expect(ready).To(BeTrue())

			By("1b. send inference request to worker1, verify remote schedule log and success")
			ctx := TimeoutCtx(context.Background(), 30*time.Second)
			resp, err := workerClient.SendChatCompletion(ctx, NewBasicChatRequestBody(), nil)
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusOK))
			Eventually(workerSession.Out, 5*time.Second).Should(gbytes.Say("send scheduler for"),
				"should see remote schedule log")

			By("1c. verify event sync is normal, wait for rs:number_events_remaining to eventually be 0")
			// Check worker1 logs for event send related errors
			time.Sleep(3 * time.Second)
			Eventually(func() float64 {
				metricsResp, err := workerClient.GetInternalMetrics(map[string]string{"only_rs": "true"})
				if err != nil || metricsResp.StatusCode != http.StatusOK {
					return -1
				}
				defer metricsResp.Body.Close()
				val, err := ExtractMetricValue(metricsResp, "rs:number_events_remaining", nil)
				if err != nil {
					return -1
				}
				GinkgoWriter.Printf("[np001] rs:number_events_remaining = %.2f\n", val)
				// If not 0 for a long time, print recent event send related lines from worker log
				if val > 0 {
					workerOut := string(workerSession.Out.Contents())
					for _, line := range strings.Split(workerOut, "\n") {
						if strings.Contains(line, "send WorkerEvents") || strings.Contains(line, "event") {
							GinkgoWriter.Printf("[np001] worker log: %s\n", line)
						}
					}
				}
				return val
			}, 60*time.Second, 2*time.Second).Should(BeNumerically("==", 0),
				"rs:number_events_remaining should be 0 after sync")

			// ====================================================================
			// Phase 2: Use iptables REJECT to block worker1->leader communication (covering existing connections)
			// ====================================================================
			By("2. block worker1 -> leader HTTP via iptables REJECT")
			// Use REJECT instead of DROP to ensure existing TCP connections are immediately terminated
			blockLeader := exec.Command("sudo", "iptables", "-A", "INPUT", "-p", "tcp", "--dport", npLeaderHTTPPort,
				"-j", "REJECT", "--reject-with", "tcp-reset")
			blockOut, blockErr := blockLeader.CombinedOutput()
			GinkgoWriter.Printf("[np002] iptables REJECT leader port: %s\n", string(blockOut))
			Expect(blockErr).To(BeNil())
			defer func() {
				unblockLeader := exec.Command("sudo", "iptables", "-D", "INPUT", "-p", "tcp", "--dport", npLeaderHTTPPort,
					"-j", "REJECT", "--reject-with", "tcp-reset")
				unblockOut, unblockErr := unblockLeader.CombinedOutput()
				GinkgoWriter.Printf("[np002] iptables remove REJECT: %s\n", string(unblockOut))
				if unblockErr != nil {
					GinkgoWriter.Printf("[np002] failed to remove iptables rule: %v\n", unblockErr)
				}
			}()

			// ====================================================================
			// Phase 3: Verify worker1 events to leader fail
			// ====================================================================
			By("3. verify worker1 events to leader fail with 'send WorkerEventsRequest to leader failed'")
			Eventually(workerSession.Out, 30*time.Second, 1*time.Second).Should(gbytes.Say("send WorkerEventsRequest to leader failed"),
				"should see event send failed log after network partition")

			// ====================================================================
			// Phase 4: Send inference request, verify fallback scheduling
			// ====================================================================
			By("4a. send inference request, expect fallback with 'Fallback sched' log")
			fallbackBefore, err := workerClient.GetRSMetric("rs:num_fallback_scheduled", map[string]string{"only_rs": "true"})
			Expect(err).NotTo(HaveOccurred())
			GinkgoWriter.Printf("[np004] rs:num_fallback_scheduled before request: %.2f\n", fallbackBefore)

			fallbackCtx := TimeoutCtx(context.Background(), 60*time.Second)
			fallbackResp, fallbackErr := workerClient.SendChatCompletion(fallbackCtx, NewBasicChatRequestBody(), nil)
			Expect(fallbackErr).NotTo(HaveOccurred())
			defer fallbackResp.Body.Close()
			Expect(fallbackResp.StatusCode).To(Equal(http.StatusOK))

			By("4b. verify 'Fallback sched' log appears")
			Eventually(workerSession.Out, 10*time.Second).Should(gbytes.Say("Fallback sched"),
				"should see fallback sched log")

			By("4c. verify rs:num_fallback_scheduled metric increases")
			Eventually(func() bool {
				val, err := workerClient.GetRSMetric("rs:num_fallback_scheduled", map[string]string{"only_rs": "true"})
				if err != nil {
					return false
				}
				GinkgoWriter.Printf("[np004] rs:num_fallback_scheduled after request: %.2f\n", val)
				return val > fallbackBefore
			}, 10*time.Second, 1*time.Second).Should(BeTrue(), "rs:num_fallback_scheduled should increase after fallback request")

			// ====================================================================
			// Phase 5: Wait for max-workers-down-time-sec, verify worker is not removed (etcd still connected)
			// ====================================================================
			By(fmt.Sprintf("5. wait for max-workers-down-time-sec (15s) for leader health check to trigger"))
			time.Sleep(18 * time.Second)

			By("5b. verify worker1 is still in etcd (etcd connection is still up)")
			worker1InEtcd := false
			ctx5, cancel5 := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel5()
			resp5, getErr5 := etcdClient.Get(ctx5, workerFaultRsPeers, clientv3.WithPrefix())
			Expect(getErr5).To(BeNil())
			for _, kv := range resp5.Kvs {
				if strings.Contains(string(kv.Key), "worker1") {
					worker1InEtcd = true
					break
				}
			}
			Expect(worker1InEtcd).To(BeTrue(), "worker1 should still be in etcd because etcd connection is alive")

			By("5c. also verify worker2 is still in etcd")
			worker2InEtcd := false
			for _, kv := range resp5.Kvs {
				if strings.Contains(string(kv.Key), "worker2") {
					worker2InEtcd = true
					break
				}
			}
			Expect(worker2InEtcd).To(BeTrue(), "worker2 should still be in etcd")

			// ====================================================================
			// Phase 6: Simulate network fault between worker1 and etcd
			// ====================================================================
			By("6. block worker1 -> etcd via iptables DROP + conntrack flush")
			exec.Command("sudo", "iptables", "-I", "OUTPUT", "1", "-p", "tcp", "--dport", npEtcdProxyPort,
				"-j", "DROP").Run()
			exec.Command("sudo", "conntrack", "-D", "-p", "tcp", "--dport", npEtcdProxyPort).Run()
			time.Sleep(500 * time.Millisecond)
			GinkgoWriter.Printf("[np006] iptables DROP + conntrack flush for port %s\n", npEtcdProxyPort)

			By("6b. wait for worker1 peer registration key to expire (lease-ttl-sec=2s)")
			// iptables DROP blocks worker1 to etcd connection, lease cannot be renewed
			// etcd automatically deletes keys with expired lease (lease TTL=2s), watch propagation takes at most 5s
			Eventually(func() bool {
				ctx6b, cancel6b := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel6b()
				resp6b, getErr6b := etcdClient.Get(ctx6b, workerFaultRsPeers, clientv3.WithPrefix())
				if getErr6b != nil {
					return false
				}
				for _, kv := range resp6b.Kvs {
					GinkgoWriter.Printf("[np006] etcd key: %s\n", string(kv.Key))
					if strings.Contains(string(kv.Key), "worker1") {
						return false
					}
				}
				return true
			}, 20*time.Second, 1*time.Second).Should(BeTrue(),
				"worker1's lease peer should be removed from etcd after lease expiry")
			GinkgoWriter.Printf("[np006] worker1 lease peer expired, removed from etcd\n")

			By("6c. wait for leader to detect and discard worker1 (checkSingleWorker)")
			// worker1's lease peer has expired, leader's checkSingleWorker
			// isPeerRegistered returns (false, nil), triggering HandleLeavePeer
			// leader's health check runs every 5s, wait at most 2 cycles
			Eventually(func() bool {
				ctx6c, cancel6c := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel6c()
				resp6c, getErr6c := etcdClient.Get(ctx6c, workerFaultRsPeers, clientv3.WithPrefix())
				if getErr6c != nil {
					return false
				}
				for _, kv := range resp6c.Kvs {
					if strings.Contains(string(kv.Key), "worker1") {
						return false
					}
				}
				return true
			}, 15*time.Second, 1*time.Second).Should(BeTrue(),
				"leader should remove worker1 from state peers")
			GinkgoWriter.Printf("[np006] leader removed worker1 from state peers\n")

			By("6d. verify worker2 still exists in etcd (worker1 removed but worker2 remains)")
			ctx6e, cancel6e := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel6e()
			resp6e, getErr6e := etcdClient.Get(ctx6e, workerFaultRsPeers, clientv3.WithPrefix())
			Expect(getErr6e).To(BeNil())
			GinkgoWriter.Printf("[np006] etcd state peers after worker1 removal:\n")
			for _, kv := range resp6e.Kvs {
				GinkgoWriter.Printf("  key=%s\n", string(kv.Key))
			}
			worker2StillExists := false
			for _, kv := range resp6e.Kvs {
				if strings.Contains(string(kv.Key), "worker2") {
					worker2StillExists = true
					break
				}
			}
			Expect(worker2StillExists).To(BeTrue(), "worker2 should still exist in etcd")

			// ====================================================================
			// Phase 7: Clean up injected network faults and wait for recovery
			// ====================================================================
			By("7a. remove iptables rules")
			// Clean up INPUT REJECT on leader HTTP port
			exec.Command("sudo", "iptables", "-D", "INPUT", "-p", "tcp", "--dport", npLeaderHTTPPort,
				"-j", "REJECT", "--reject-with", "tcp-reset").Run()
			// Clean up OUTPUT DROP on etcd proxy port
			exec.Command("sudo", "iptables", "-D", "OUTPUT", "-p", "tcp", "--dport", npEtcdProxyPort,
				"-j", "DROP").Run()

			By("7b. restore worker1 -> leader TCP proxy")
			leaderHTTPProxyRule.Drop.Store(false)
			GinkgoWriter.Printf("[np007] worker1 -> leader TCP proxy restored\n")

			By("7c. restore worker1 -> etcd TCP proxy")
			worker1EtcdProxyRule.Drop.Store(false)
			GinkgoWriter.Printf("[np007] worker1 -> etcd TCP proxy restored\n")

			By("7e. wait for worker1 etcd wrapper reconnect log")
			Eventually(workerSession.Out, 30*time.Second, 1*time.Second).Should(gbytes.Say("etcd wrapper.*reconnect: connection resumed"),
				"worker1 etcd wrapper should reconnect")
			GinkgoWriter.Printf("[np007] worker1 etcd reconnected\n")

			By("7f. verify worker1 re-registers in etcd")
			Eventually(func() bool {
				ctx7, cancel7 := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel7()
				resp7, getErr7 := etcdClient.Get(ctx7, workerFaultRsPeers, clientv3.WithPrefix())
				if getErr7 != nil {
					return false
				}
				for _, kv := range resp7.Kvs {
					if strings.Contains(string(kv.Key), "worker1") {
						return true
					}
				}
				return false
			}, 30*time.Second, 1*time.Second).Should(BeTrue(), "worker1 should re-register in etcd")

			By("7g. verify leader scheduler load is cleared (no inflight requests on leader)")
			// After worker1 recovers connection, events re-sync, leader load info should update
			// Check leader GET /scheduler/load that all instance loads are 0
			type schedulerLoadResponse struct {
				RsName string                                  `json:"rs_name"`
				Loads  map[string]*commonScheduler.SLOLoadInfo `json:"loads,omitempty"`
			}
			parseLoadResponse := func(resp *http.Response) map[string]float64 {
				defer resp.Body.Close()
				body, err := io.ReadAll(resp.Body)
				Expect(err).NotTo(HaveOccurred())
				var wrappedResp schedulerLoadResponse
				Expect(json.Unmarshal(body, &wrappedResp)).To(BeNil())
				loads := make(map[string]float64)
				for _, loadInfo := range wrappedResp.Loads {
					if loadInfo == nil || loadInfo.SLOGroupLoads == nil {
						continue
					}
					for _, algoInfo := range loadInfo.SLOGroupLoads {
						if algoInfo == nil || algoInfo.InstanceLoads == nil {
							continue
						}
						for _, inst := range algoInfo.InstanceLoads.Available {
							loads[inst.Id] = inst.LoadVal
						}
						for _, inst := range algoInfo.InstanceLoads.Unavailable {
							loads[inst.Id] = inst.LoadVal
						}
					}
				}
				return loads
			}
			Eventually(func() bool {
				loadResp, loadErr := leaderClient.GetSchedulerLoad()
				if loadErr != nil {
					GinkgoWriter.Printf("[np007] leader scheduler load error: %v\n", loadErr)
					return false
				}
				allLoads := parseLoadResponse(loadResp)
				GinkgoWriter.Printf("[np007] leader scheduler loads: %v\n", allLoads)
				for _, load := range allLoads {
					if load > 0 {
						return false
					}
				}
				return true
			}, 30*time.Second, 2*time.Second).Should(BeTrue(), "leader scheduler load should be 0 after recovery")

			By("7h. verify worker1 events remaining metric is 0")
			Eventually(func() bool {
				val, err := workerClient.GetRSMetric("rs:number_events_remaining", map[string]string{"only_rs": "true"})
				if err != nil {
					GinkgoWriter.Printf("[np007] failed to get events metric: %v\n", err)
					return false
				}
				GinkgoWriter.Printf("[np007] worker1 rs:number_events_remaining = %.2f\n", val)
				return val == 0
			}, 30*time.Second, 2*time.Second).Should(BeTrue(), "worker1 events remaining should be 0 after recovery")
		})
	})
})
