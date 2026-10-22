package route_server

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/goccy/go-yaml"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gbytes"
	"github.com/onsi/gomega/gexec"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/server/v3/embed"

	commoncfg "infer-dp-scheduler/pkg/common/config"
	"infer-dp-scheduler/pkg/common/constant"
)

const (
	probeEtcdCtx = "probe-etcd-test"

	// Separate ports to avoid conflict with etcd_integration_test.go
	probeEtcdClientPort = "40000"
	probeEtcdPeerPort   = "40001"

	probeCandidate1HTTPPort = "40002"
	probeCandidate1OpsPort  = "40012"
	probeCandidate2HTTPPort = "40003"
	probeCandidate2OpsPort  = "40013"
	probeWorkerHTTPPort     = "40004"
	probeWorkerOpsPort      = "40014"
	probeWorker2HTTPPort    = "40005"
	probeWorker2OpsPort     = "40015"

	probePrefill1Port = "40006"
	probePrefill2Port = "40007"
	probeDecode1Port  = "40008"
	probeDecode2Port  = "40009"

	probePrefill1ID = "prefill1"
	probePrefill2ID = "prefill2"
	probeDecode1ID  = "decode1"
	probeDecode2ID  = "decode2"

	probeRsLeaderKey   = "/rs/leader"
	probeRsPeersAllDir = "/rs/cluster/state/peers/all/"

	probeHealthCheckIntervalMs    = 3000 // Shortened health check interval (original 10s->3s) to speed up tests
	probeHealthConsecutiveTime    = 2    // Shortened consecutive check count (original 3->2) to speed up tests
	probeHealthRecoverTime        = 3
	probeEventThresholdIntervalMs = 1000
)

var _ = Describe(probeEtcdCtx, Ordered, func() {
	var (
		candidate1Session    *gexec.Session
		candidate2Session    *gexec.Session
		workerSession        *gexec.Session
		worker2Session       *gexec.Session
		electedLeaderSession *gexec.Session
		mockServers          []*gexec.Session
		etcdServer           *embed.Etcd
		etcdClient           *clientv3.Client

		candidate1CfgDir string
		candidate2CfgDir string
		workerCfgDir     string
		worker2CfgDir    string

		workerClient     *RSClient
		worker2Client    *RSClient
		candidate1Client *RSClient
		candidate2Client *RSClient
	)

	// Start etcd and all mock instances (2p2d), only once for the entire suite
	BeforeAll(func() {
		By("step 0: ensure all ports are free")
		EnsurePorts([]string{probeEtcdClientPort, probeEtcdPeerPort,
			probeCandidate1HTTPPort, probeCandidate2HTTPPort, probeWorkerHTTPPort, probeWorker2HTTPPort,
			probeCandidate1OpsPort, probeCandidate2OpsPort, probeWorkerOpsPort, probeWorker2OpsPort,
			probePrefill1Port, probePrefill2Port, probeDecode1Port, probeDecode2Port})
		time.Sleep(500 * time.Millisecond)

		By("step 1: start embedded etcd")
		etcdServer, etcdClient = StartEmbeddedEtcd(probeEtcdClientPort, probeEtcdPeerPort, "probe-test-etcd")

		By("step 2: generate RS configs via config-tool with optimized health check params")
		deployPath := filepath.Join(projectRoot, "deploy")
		etcdCtxDir := filepath.Join(projectRoot, "e2etest", "environment", probeEtcdCtx)
		candidate1CfgDir = filepath.Join(etcdCtxDir, "candidate1")
		candidate2CfgDir = filepath.Join(etcdCtxDir, "candidate2")
		workerCfgDir = filepath.Join(etcdCtxDir, "worker1")
		worker2CfgDir = filepath.Join(etcdCtxDir, "worker2")

		// Optimization: modify health check params in the template to shorten interval and consecutive count
		// interval-ms: 10000->3000, consecutive-time: 3->2, resume-time-ms: 90000->10000
		// This reduces instance fault detection from ~30s to ~6s
		var cfg commoncfg.Config
		templateContent, err := os.ReadFile(filepath.Join(deployPath, ROUTE_SERVER_TEMPLATE))
		Expect(err).To(BeNil())
		Expect(yaml.Unmarshal(templateContent, &cfg)).To(BeNil())
		cfg.InstanceManagerCfg.HealthCheckConfig.RestHealthCommonConfig.Interval = probeHealthCheckIntervalMs
		cfg.InstanceManagerCfg.HealthCheckConfig.AvailableCheckersCfg.RestHealthCfg.ConsecutiveTime = probeHealthConsecutiveTime
		cfg.InstanceManagerCfg.HealthCheckConfig.UnavailableCheckersCfg.RestHealthCfg.ConsecutiveTime = probeHealthConsecutiveTime
		cfg.InstanceManagerCfg.HealthCheckConfig.FallbackHealthCheckConfig.ResumeTimeMs = 10000
		outData, err := yaml.Marshal(&cfg)
		Expect(err).To(BeNil())
		customTemplateFile := filepath.Join(etcdCtxDir, "custom-template.yaml")
		Expect(os.MkdirAll(filepath.Dir(customTemplateFile), 0755)).To(BeNil())
		Expect(os.WriteFile(customTemplateFile, outData, 0644)).To(BeNil())

		genRsConfig := func(name, targetDir, port, remoteSchedulerType string, candidate bool) {
			Expect(os.RemoveAll(targetDir)).To(BeNil())
			Expect(os.MkdirAll(filepath.Join(targetDir, "config"), 0755)).To(BeNil())
			out, err := exec.Command(configToolPath, DisableHTTPSConfig([]string{
				"-template", customTemplateFile,
				"-host", "127.0.0.1",
				"-target", targetDir,
				"-infer-mode", "PD",
				"-prefills", fmt.Sprintf("prefill1#127.0.0.1:%s,prefill2#127.0.0.1:%s", probePrefill1Port, probePrefill2Port),
				"-decodes", fmt.Sprintf("decode1#127.0.0.1:%s,decode2#127.0.0.1:%s", probeDecode1Port, probeDecode2Port),
				"-name", name,
				"-remote-scheduler-type", remoteSchedulerType,
				"-port", port,
				"-candidate", fmt.Sprintf("%t", candidate),
				"-etcd-endpoints", fmt.Sprintf("127.0.0.1:%s", probeEtcdClientPort),
				"-infer-module-version", "pipeline",
				"-readiness-cfg", "prefill:2#decode:2",
			})...).CombinedOutput()
			GinkgoWriter.Printf("[config] %s config-tool output: %s\n", name, string(out))
			Expect(err).To(BeNil())
		}
		genRsConfig("candidate1", candidate1CfgDir, probeCandidate1HTTPPort, "disable", true)
		genRsConfig("candidate2", candidate2CfgDir, probeCandidate2HTTPPort, "disable", true)
		genRsConfig("worker1", workerCfgDir, probeWorkerHTTPPort, "leader", false)
		genRsConfig("worker2", worker2CfgDir, probeWorker2HTTPPort, "leader", false)

		// Copy pipelines and metrics configs
		for _, cfgDir := range []string{candidate1CfgDir, candidate2CfgDir, workerCfgDir, worker2CfgDir} {
			Expect(os.MkdirAll(filepath.Join(cfgDir, "config", "pipelines"), 0755)).To(BeNil())
			Expect(copyDir(filepath.Join(deployPath, "pipelines"), filepath.Join(cfgDir, "config", "pipelines"))).To(BeNil())
		}
		metricsCollectorConfigFileName := "metrics_config_vllm_0.11.0.yaml"
		for _, cfgDir := range []string{candidate1CfgDir, candidate2CfgDir, workerCfgDir, worker2CfgDir} {
			Expect(copyFile(
				filepath.Join(deployPath, metricsCollectorConfigFileName),
				filepath.Join(cfgDir, "config", metricsCollectorConfigFileName),
			)).To(BeNil())
		}

		DeferCleanup(func() {
			os.RemoveAll(path.Join(candidate1CfgDir, "config"))
			os.RemoveAll(path.Join(candidate2CfgDir, "config"))
			os.RemoveAll(path.Join(workerCfgDir, "config"))
			os.RemoveAll(path.Join(worker2CfgDir, "config"))
		})

		By("step 3: start mock vllm instances (2 prefill + 2 decode)")
		mockInstances := []struct {
			port string
			name string
		}{
			{probePrefill1Port, "prefill1"},
			{probePrefill2Port, "prefill2"},
			{probeDecode1Port, "decode1"},
			{probeDecode2Port, "decode2"},
		}
		for _, inst := range mockInstances {
			params := []string{
				"-addr", "127.0.0.1",
				"-port", inst.port,
				"-name", inst.name,
			}
			command := exec.Command(mockVLLMPath, params...)
			sess, startErr := gexec.Start(command, io.Discard, GinkgoWriter)
			Expect(startErr).NotTo(HaveOccurred())
			checkMockVllmListenSucceess(sess)
			mockServers = append(mockServers, sess)
			GinkgoWriter.Printf("[mock] started %s on port %s\n", inst.name, inst.port)
		}
		time.Sleep(500 * time.Millisecond)

		workerClient = NewRSClient("http://localhost:" + probeWorkerHTTPPort)
		worker2Client = NewRSClient("http://localhost:" + probeWorker2HTTPPort)
		candidate1Client = NewRSClient("http://localhost:" + probeCandidate1HTTPPort)
		candidate2Client = NewRSClient("http://localhost:" + probeCandidate2HTTPPort)
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
		if candidate2Session != nil {
			candidate2Session.Kill()
			candidate2Session.Wait()
		}
		if candidate1Session != nil {
			candidate1Session.Kill()
			candidate1Session.Wait()
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
	// startRsInstanceNoWait starts a single RS instance without waiting for log readiness
	// -------------------------------------------------------
	startRsInstanceNoWait := func(cfgDir string) *gexec.Session {
		env := os.Environ()
		env = append(env, fmt.Sprintf("%s=%s",
			constant.EnvServerConfigPath, filepath.Join(cfgDir, "config")))
		command := exec.Command(rsPath)
		command.Env = env
		sess, err := gexec.Start(command, io.Discard, GinkgoWriter)
		Expect(err).NotTo(HaveOccurred())
		return sess
	}

	// -------------------------------------------------------
	// startAllRs starts all RS instances in parallel and waits for readiness
	// Optimization: start 4 RS instances in parallel, then wait for leader election and health sync
	// -------------------------------------------------------
	startAllRs := func() {
		By("start all RS instances in parallel")
		var wg sync.WaitGroup
		type rsStartResult struct {
			session *gexec.Session
			name    string
		}
		results := make([]rsStartResult, 4)

		startOne := func(idx int, name, cfgDir string) {
			defer wg.Done()
			sess := startRsInstanceNoWait(cfgDir)
			results[idx] = rsStartResult{session: sess, name: name}
		}

		wg.Add(4)
		go startOne(0, "candidate1", candidate1CfgDir)
		go startOne(1, "candidate2", candidate2CfgDir)
		go startOne(2, "worker1", workerCfgDir)
		go startOne(3, "worker2", worker2CfgDir)
		wg.Wait()

		candidate1Session = results[0].session
		candidate2Session = results[1].session
		workerSession = results[2].session
		worker2Session = results[3].session

		// Wait for all RS instances to be listening (parallel checks to avoid serial waiting)
		By("wait for all RS instances to be listening")
		var checkWg sync.WaitGroup
		checkWg.Add(len(results))
		for i, r := range results {
			go func(idx int, name string, sess *gexec.Session) {
				defer checkWg.Done()
				checkRsListenSucceess(sess)
			}(i, r.name, r.session)
		}
		checkWg.Wait()

		By("wait for leader election and detect which candidate became the leader")
		var leaderValue string
		Eventually(func() bool {
			leaderID, _, err := workerClient.GetLeader()
			if err != nil {
				GinkgoWriter.Printf("[leader] get leader error: %v\n", err)
				return false
			}
			leaderValue = leaderID
			GinkgoWriter.Printf("[leader] worker reported leader: %s\n", leaderValue)
			if strings.Contains(leaderValue, "candidate1") {
				electedLeaderSession = candidate1Session
				return true
			}
			if strings.Contains(leaderValue, "candidate2") {
				electedLeaderSession = candidate2Session
				return true
			}
			return false
		}, 10*time.Second, 1*time.Second).Should(BeTrue(), "a leader should be elected")
		GinkgoWriter.Printf("[leader] detected leader session: %s\n", leaderValue)

		Eventually(func() bool {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			resp, getErr := etcdClient.Get(ctx, probeRsPeersAllDir, clientv3.WithPrefix())
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

		By("wait for worker health cache sync")
		Eventually(func() bool {
			ready, msg, err := workerClient.GetWorkerReadiness()
			if err != nil {
				GinkgoWriter.Printf("[worker] readiness check error: %v\n", err)
				return false
			}
			GinkgoWriter.Printf("[worker] readiness: ready=%v, msg=%s\n", ready, msg)
			return strings.Contains(msg, `"message":"ready"`)
		}, 60*time.Second, 2*time.Second).Should(BeTrue(), "worker health cache should be synced")
	}

	// -------------------------------------------------------
	// killAllRs kills all RS processes
	// -------------------------------------------------------
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
		if candidate2Session != nil {
			candidate2Session.Kill()
			candidate2Session.Wait()
		}
		if candidate1Session != nil {
			candidate1Session.Kill()
			candidate1Session.Wait()
		}

		// Ensure all RS ports are released (process may have exited but port still occupied)
		// RS HTTP port + ops port (default http-port + 10)
		By("ensure all RS ports are free")
		httpPorts := []string{probeCandidate1HTTPPort, probeCandidate2HTTPPort, probeWorkerHTTPPort, probeWorker2HTTPPort}
		var rsPorts []string
		rsPorts = append(rsPorts, httpPorts...)
		for _, p := range httpPorts {
			portNum, err := strconv.Atoi(p)
			Expect(err).NotTo(HaveOccurred())
			rsPorts = append(rsPorts, strconv.Itoa(portNum+10)) // ops port = http-port + 10
		}
		Eventually(func() bool {
			EnsurePorts(rsPorts)
			for _, p := range rsPorts {
				ln, err := net.Listen("tcp", "127.0.0.1:"+p)
				if err != nil {
					return false
				}
				ln.Close()
			}
			return true
		}, 10*time.Second, 500*time.Millisecond).Should(BeTrue(), "all RS ports should be free after kill")
	}

	// -------------------------------------------------------
	// ensureMockInstancesHealthy ensures all mock instances are healthy
	// If an instance process is dead, restart it; otherwise call /mock_health to recover
	// -------------------------------------------------------
	ensureMockInstancesHealthy := func() {
		mockPorts := []string{probePrefill1Port, probePrefill2Port, probeDecode1Port, probeDecode2Port}
		for i, port := range mockPorts {
			// Try /mock_health first
			client := NewMockServerClient(port)
			resp, err := client.MockHealth()
			if err != nil || resp == nil || resp.StatusCode != http.StatusOK {
				GinkgoWriter.Printf("[mock] instance on port %s not responding, restarting...\n", port)
				// Process may be dead, restart it
				if mockServers[i] != nil {
					mockServers[i].Kill()
					mockServers[i].Wait()
				}
				params := []string{
					"-addr", "127.0.0.1",
					"-port", port,
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

	// -------------------------------------------------------
	// ensureEtcdClean cleans RS-related keys from etcd
	// -------------------------------------------------------
	ensureEtcdClean := func() {
		ctx := context.Background()
		etcdClient.Delete(ctx, "", clientv3.WithPrefix())
		GinkgoWriter.Printf("[etcd] cleaned RS-related keys\n")
	}

	// -------------------------------------------------------
	// waitForHealthCheck waits for health check to detect status change
	// Health check config: interval=3s, consecutive-time=2
	// Theoretically takes at most ~12s (interval + timeout*consecutive + buffer)
	// -------------------------------------------------------
	healthCheckTimeout := time.Duration(probeHealthCheckIntervalMs+10000+probeHealthConsecutiveTime*10000+5000) * time.Millisecond

	// ================================================================
	// 1. Role fault traffic drainage test
	// ================================================================
	Context("Role fault traffic drainage test", func() {
		BeforeEach(func() {
			// Ensure clean environment before each It:
			// 1. Kill RS first (otherwise surviving RS will write data to etcd)
			// 2. Clean etcd
			// 3. Ensure all mock instances are healthy
			// 4. Restart all RS
			killAllRs()
			ensureEtcdClean()
			ensureMockInstancesHealthy()
			startAllRs()
		})

		// test_xpyd_rolesfault_027: After all prefill instances fail, requests cannot be processed, the entire cluster should drain traffic
		It("test_xpyd_rolesfault_027: After all prefill instances fail, requests cannot be processed, the entire cluster should drain traffic", func() {
			By("Kill all prefill instances")
			mockServers[0].Kill()
			mockServers[0].Wait()
			mockServers[1].Kill()
			mockServers[1].Wait()

			By("Wait for leader to detect prefill instances as unavailable")
			WaitForInstanceHealthStatus(electedLeaderSession, probePrefill1ID, "http://127.0.0.1:"+probePrefill1Port, HealthStatusUnavailable, healthCheckTimeout)
			WaitForInstanceHealthStatus(electedLeaderSession, probePrefill2ID, "http://127.0.0.1:"+probePrefill2Port, HealthStatusUnavailable, healthCheckTimeout)

			By("Verify worker1 readiness drains traffic (all prefill instances down)")
			Eventually(func() bool {
				ready, msg, err := workerClient.GetWorkerReadiness()
				if err != nil {
					return false
				}
				GinkgoWriter.Printf("[027] worker1 readiness: ready=%v, msg=%s\n", ready, msg)
				return !ready && strings.Contains(msg, "0 prefill")
			}, 60*time.Second, 1*time.Second).Should(BeTrue(), "worker1 should not be ready when all prefill instances are down")
		})

		// test_xpyd_rolesfault_030: After all decode instances fail, requests cannot be processed, the entire cluster should drain traffic
		It("test_xpyd_rolesfault_030: After all decode instances fail, requests cannot be processed, the entire cluster should drain traffic", func() {
			By("Kill all decode instances")
			mockServers[2].Kill()
			mockServers[2].Wait()
			mockServers[3].Kill()
			mockServers[3].Wait()

			By("Wait for leader to detect decode instances as unavailable")
			WaitForInstanceHealthStatus(electedLeaderSession, probeDecode1ID, "http://127.0.0.1:"+probeDecode1Port, HealthStatusUnavailable, healthCheckTimeout)
			WaitForInstanceHealthStatus(electedLeaderSession, probeDecode2ID, "http://127.0.0.1:"+probeDecode2Port, HealthStatusUnavailable, healthCheckTimeout)

			By("Verify worker1 readiness drains traffic (all decode instances down)")
			Eventually(func() bool {
				ready, msg, err := workerClient.GetWorkerReadiness()
				if err != nil {
					return false
				}
				GinkgoWriter.Printf("[030] worker1 readiness: ready=%v, msg=%s\n", ready, msg)
				return !ready && strings.Contains(msg, "0 decode")
			}, 30*time.Second, 1*time.Second).Should(BeTrue(), "worker1 should not be ready when all decode instances are down")
		})
	})

	// ================================================================
	// 2. Leader probe test
	// ================================================================
	Context("Leader probe test", func() {
		BeforeEach(func() {
			killAllRs()
			ensureEtcdClean()
			ensureMockInstancesHealthy()
			startAllRs()
		})

		// test_xpyd_probe_001: RS leader probe interface works normally
		// test_xpyd_probe_002: After killing leader, verify leader probe interface returns failure
		It("[test_xpyd_probe_001][test_xpyd_probe_002] RS leader probe normal and probe failure after leader fault verification", func() {
			By("test_xpyd_probe_001: Identify the candidate elected as leader")
			var leaderClient *RSClient
			var leaderSession *gexec.Session
			if candidate1Client.CheckLeaderReadiness() {
				leaderClient = candidate1Client
				leaderSession = candidate1Session
			} else {
				leaderClient = candidate2Client
				leaderSession = candidate2Session
			}
			Expect(leaderClient).NotTo(BeNil())

			By("test_xpyd_probe_001: Verify leader liveness probe returns 200 ok")
			livenessResp, err := leaderClient.GetLiveness()
			Expect(err).NotTo(HaveOccurred())
			defer livenessResp.Body.Close()
			Expect(livenessResp.StatusCode).To(Equal(http.StatusOK))
			body, err := io.ReadAll(livenessResp.Body)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(body)).To(Equal("ok"))

			By("test_xpyd_probe_001: Verify leader /internal/leader/readiness returns is_leader=true")
			Expect(leaderClient.CheckLeaderReadiness()).To(BeTrue())

			By("test_xpyd_probe_002: Kill the current leader process")
			leaderSession.Kill()
			leaderSession.Wait()
			time.Sleep(1 * time.Second)

			By("test_xpyd_probe_002: Verify leader liveness probe returns failure")
			_, err = leaderClient.GetLiveness()
			Expect(err).To(HaveOccurred())

			By("test_xpyd_probe_002: Verify leader /internal/leader/readiness returns failure")
			_, err = http.Get(leaderClient.BaseURL + "/internal/leader/readiness")
			Expect(err).To(HaveOccurred())
		})
	})

	// ================================================================
	// 3. Candidate non-leader probe test
	// ================================================================
	Context("Candidate non-leader probe test", func() {
		BeforeEach(func() {
			killAllRs()
			ensureEtcdClean()
			ensureMockInstancesHealthy()
			startAllRs()
		})

		// test_xpyd_probe_003: Non-leader candidate probe interface works normally
		// test_xpyd_probe_004: After killing non-leader candidate, verify its probe interface returns failure
		It("[test_xpyd_probe_003][test_xpyd_probe_004] Non-leader candidate probe normal and probe failure after fault verification", func() {
			By("test_xpyd_probe_003: Identify which candidate is the current non-leader")
			// candidate1 and candidate2 are both candidates, etcd elects one as leader
			var nonLeaderClient *RSClient
			var nonLeaderSession *gexec.Session
			if candidate1Client.CheckLeaderReadiness() {
				nonLeaderClient = candidate2Client
				nonLeaderSession = candidate2Session
			} else {
				nonLeaderClient = candidate1Client
				nonLeaderSession = candidate1Session
			}
			GinkgoWriter.Printf("[003] non-leader candidate client: %s\n", nonLeaderClient.BaseURL)
			Expect(nonLeaderClient).NotTo(BeNil(), "there should be a non-leader candidate")

			By("test_xpyd_probe_003: Verify non-leader candidate liveness probe returns 200 ok")
			livenessResp, err := nonLeaderClient.GetLiveness()
			Expect(err).NotTo(HaveOccurred())
			defer livenessResp.Body.Close()
			Expect(livenessResp.StatusCode).To(Equal(http.StatusOK))
			body, err := io.ReadAll(livenessResp.Body)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(body)).To(Equal("ok"))

			By("test_xpyd_probe_003: Verify non-leader candidate leader/readiness returns is_leader=false")
			Expect(nonLeaderClient.CheckLeaderReadiness()).To(BeFalse())

			By("test_xpyd_probe_004: Kill the non-leader candidate process")
			nonLeaderSession.Kill()
			nonLeaderSession.Wait()
			time.Sleep(1 * time.Second)

			By("test_xpyd_probe_004: Verify non-leader candidate liveness probe returns failure")
			_, err = nonLeaderClient.GetLiveness()
			Expect(err).To(HaveOccurred())

			By("test_xpyd_probe_004: Verify non-leader candidate leader/readiness returns failure")
			_, err = http.Get(nonLeaderClient.BaseURL + "/internal/leader/readiness")
			Expect(err).To(HaveOccurred())
		})

		// test_xpyd_rolesfault_009: Non-leader candidate fault does not affect inference requests
		It("test_xpyd_rolesfault_009: Non-leader candidate fault does not affect inference requests", func() {
			By("Identify the non-leader candidate")
			var nonLeaderSession *gexec.Session
			if candidate1Client.CheckLeaderReadiness() {
				nonLeaderSession = candidate2Session
			} else {
				nonLeaderSession = candidate1Session
			}
			Expect(nonLeaderSession).NotTo(BeNil())

			By("Ensure worker1 readiness is currently normal")
			Eventually(func() bool {
				ready, _, err := workerClient.GetWorkerReadiness()
				return err == nil && ready
			}, 10*time.Second, 1*time.Second).Should(BeTrue(), "worker should be ready before test")

			By("Kill the non-leader candidate process")
			nonLeaderSession.Kill()
			nonLeaderSession.Wait()
			time.Sleep(1 * time.Second)

			By("Verify inference requests are not affected and still succeed")
			ctx := TimeoutCtx(context.Background(), 30*time.Second)
			resp, err := workerClient.SendChatCompletion(ctx, NewBasicChatRequestBody(), nil)
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusOK), "inference request should succeed, non-leader candidate fault does not affect inference plane")
			GinkgoWriter.Printf("[009] inference request after non-leader candidate killed: status=%d\n", resp.StatusCode)
		})

		// test_xpyd_rolesfault_010: Non-leader candidate fault does not affect management plane requests, GET /status
		It("test_xpyd_rolesfault_010: Non-leader candidate fault does not affect management plane requests, GET /status", func() {
			By("Identify the leader and non-leader candidate")
			var leaderClient *RSClient
			var nonLeaderSession *gexec.Session
			if candidate1Client.CheckLeaderReadiness() {
				leaderClient = candidate1Client
				nonLeaderSession = candidate2Session
			} else {
				leaderClient = candidate2Client
				nonLeaderSession = candidate1Session
			}
			Expect(leaderClient).NotTo(BeNil())
			Expect(nonLeaderSession).NotTo(BeNil())

			By("1. Initial state: status=running, 2 prefill and 2 decode all running")
			VerifyInstanceStatus(leaderClient, constant.RunningStatus, 2, 2)

			By("2. Kill non-leader candidate, status and instance_status unchanged")
			nonLeaderSession.Kill()
			nonLeaderSession.Wait()
			time.Sleep(1 * time.Second)
			VerifyInstanceStatus(leaderClient, constant.RunningStatus, 2, 2)

			By("3. Kill one prefill, status=concerning, 1 prefill running, 2 decode running")
			mockServers[0].Kill()
			mockServers[0].Wait()
			WaitForInstanceHealthStatus(electedLeaderSession, probePrefill1ID, "http://127.0.0.1:"+probePrefill1Port, HealthStatusUnavailable, healthCheckTimeout)
			VerifyInstanceStatus(leaderClient, constant.ConcerningStatus, 1, 2)

			By("4. Kill the second prefill, status=fault, 0 prefill running, 2 decode running")
			mockServers[1].Kill()
			mockServers[1].Wait()
			WaitForInstanceHealthStatus(electedLeaderSession, probePrefill2ID, "http://127.0.0.1:"+probePrefill2Port, HealthStatusUnavailable, healthCheckTimeout)
			VerifyInstanceStatus(leaderClient, constant.FaultStatus, 0, 2)
		})
	})

	// ================================================================
	// 4. Worker traffic acceptance test when leader fails
	// ================================================================
	Context("Worker traffic acceptance test when leader fails", func() {
		BeforeEach(func() {
			killAllRs()
			ensureEtcdClean()
			ensureMockInstancesHealthy()
			startAllRs()
		})

		// test_xpyd_probe_0012: When a candidate (leader) fails, does the worker accept traffic
		// test_xpyd_probe_006: When all candidates (leaders) fail, does the worker accept traffic
		It("[test_xpyd_probe_0012][test_xpyd_probe_006] Worker traffic acceptance verification under candidate failure scenarios", func() {
			By("test_xpyd_probe_0012: Ensure worker1 readiness is currently normal")
			Eventually(func() bool {
				ready, _, err := workerClient.GetWorkerReadiness()
				return err == nil && ready
			}, 10*time.Second, 1*time.Second).Should(BeTrue(), "worker should be ready before leader kill")

			By("test_xpyd_probe_0012: Find the candidate elected as leader and kill it")
			var killedIsCandidate1 bool
			if candidate1Client.CheckLeaderReadiness() {
				candidate1Session.Kill()
				candidate1Session.Wait()
				killedIsCandidate1 = true
			} else {
				candidate2Session.Kill()
				candidate2Session.Wait()
				killedIsCandidate1 = false
			}
			time.Sleep(3 * time.Second)

			By("test_xpyd_probe_0012: Wait for another candidate to be elected as the new leader")
			var newLeaderID string
			Eventually(func() bool {
				leaderID, _, err := workerClient.GetLeader()
				if err != nil {
					return false
				}
				newLeaderID = leaderID
				if killedIsCandidate1 {
					return strings.Contains(leaderID, "candidate2")
				}
				return strings.Contains(leaderID, "candidate1")
			}, 10*time.Second, 1*time.Second).Should(BeTrue(), "another candidate should become the new leader")
			GinkgoWriter.Printf("[0012] new leader after kill: %s\n", newLeaderID)

			By("test_xpyd_probe_0012: Wait for worker1 health cache to sync with the new leader")
			Eventually(func() bool {
				ready, msg, err := workerClient.GetWorkerReadiness()
				if err != nil {
					return false
				}
				GinkgoWriter.Printf("[0012] worker1 readiness after leader re-election: ready=%v, msg=%s\n", ready, msg)
				return ready
			}, 60*time.Second, 2*time.Second).Should(BeTrue(), "worker1 should be ready after new leader elected")

			By("test_xpyd_probe_0012: Verify worker1 forwards inference request to new leader (remote_schedule), result must be 200")
			ctx := TimeoutCtx(context.Background(), 30*time.Second)
			resp, err := workerClient.SendChatCompletion(ctx, NewBasicChatRequestBody(), nil)
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusOK), "inference request result must be 200")
			GinkgoWriter.Printf("[0012] worker1 response status after leader re-election: %d\n", resp.StatusCode)

			By("test_xpyd_probe_0012: Verify worker log contains remote_schedule (scheduled via new leader)")
			Eventually(workerSession.Out, 5*time.Second).Should(gbytes.Say("remote_schedule"),
				"worker should use remote_schedule to forward to the new leader")

			By("test_xpyd_probe_006: Kill the remaining candidate (all candidates are dead)")
			if killedIsCandidate1 {
				candidate2Session.Kill()
				candidate2Session.Wait()
			} else {
				candidate1Session.Kill()
				candidate1Session.Wait()
			}
			time.Sleep(3 * time.Second)

			By("test_xpyd_probe_006: Wait for worker to detect etcd lease expiry, enter no leader state")
			Eventually(workerSession.Out, 6*time.Second, time.Second).Should(gbytes.Say("detect no leader is trying to obtain leader key, entering no leader state"),
				"worker should enter no leader state after etcd lease expires")

			By("test_xpyd_probe_006: Verify worker GET /cluster/leader response is 503 (no leader)")
			_, statusCode, _ := workerClient.GetLeader()
			GinkgoWriter.Printf("[006] worker /cluster/leader status: %d\n", statusCode)
			Expect(statusCode).To(Equal(http.StatusServiceUnavailable),
				"worker should return 503 when no leader is elected")

			By("test_xpyd_probe_006: Wait for etcd health cache to expire, worker enters degraded service state")
			Eventually(func() bool {
				ready, msg, err := workerClient.GetWorkerReadiness()
				if err != nil {
					return false
				}
				GinkgoWriter.Printf("[006] worker readiness: ready=%v, msg=%s\n", ready, msg)
				return ready && strings.Contains(msg, "degraded service (circuit breaker closed)")
			}, 60*time.Second, 2*time.Second).Should(BeTrue(),
				"worker should be ready with degraded service message after etcd cache expiry")

			By("test_xpyd_probe_006: Record current rs:num_fallback_scheduled metric value")
			fallbackBefore, err := workerClient.GetRSMetric("rs:num_fallback_scheduled", map[string]string{"only_rs": "true"})
			if err != nil {
				fallbackBefore = -1
			}
			GinkgoWriter.Printf("[006] rs:num_fallback_scheduled before request: %.2f\n", fallbackBefore)

			By("test_xpyd_probe_006: Send inference request, verify result is 200")
			reqCtx := TimeoutCtx(context.Background(), 30*time.Second)
			resp, err = workerClient.SendChatCompletion(reqCtx, NewBasicChatRequestBody(), nil)
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusOK), "inference request result must be 200")
			GinkgoWriter.Printf("[006] worker1 response status in degraded mode: %d\n", resp.StatusCode)

			By("test_xpyd_probe_006: Verify rs:num_fallback_scheduled metric has increased")
			Eventually(func() bool {
				val, err := workerClient.GetRSMetric("rs:num_fallback_scheduled", map[string]string{"only_rs": "true"})
				if err != nil {
					return false
				}
				GinkgoWriter.Printf("[006] rs:num_fallback_scheduled after request: %.2f\n", val)
				return val > fallbackBefore
			}, 10*time.Second, 1*time.Second).Should(BeTrue(),
				"rs:num_fallback_scheduled should increase after fallback request")
		})
	})

	// ================================================================
	// 5. Worker probe basic functionality test
	// ================================================================
	Context("Worker probe basic functionality test", func() {
		BeforeEach(func() {
			killAllRs()
			ensureEtcdClean()
			ensureMockInstancesHealthy()
			startAllRs()
		})

		// test_xpyd_probe_005: RS worker instance probe interface works normally
		It("test_xpyd_probe_005: RS worker instance probe interface works normally", func() {
			By("Verify worker1 worker/readiness probe returns ready, and msg is ready")
			ready, msg, err := workerClient.GetWorkerReadiness()
			Expect(err).NotTo(HaveOccurred())
			GinkgoWriter.Printf("[005] worker1 readiness: ready=%v, msg=%s\n", ready, msg)
			Expect(ready).To(BeTrue())
			Expect(strings.Contains(msg, `"message":"ready"`)).To(BeTrue(), "msg should be ready, indicating live instances meet traffic acceptance requirements")

			By("Verify liveness probe returns 200 ok")
			livenessResp, err := workerClient.GetLiveness()
			Expect(err).NotTo(HaveOccurred())
			defer livenessResp.Body.Close()
			Expect(livenessResp.StatusCode).To(Equal(http.StatusOK))
			body, err := io.ReadAll(livenessResp.Body)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(body)).To(Equal("ok"))
		})
	})

	// ================================================================
	// 6. No-P/No-D/No-PD scenario probe test
	// ================================================================
	Context("Role missing scenario probe test", func() {
		BeforeEach(func() {
			killAllRs()
			ensureEtcdClean()
			ensureMockInstancesHealthy()
			startAllRs()
		})

		// test_xpyd_probe_007: Worker probe under no-prefill scenario
		It("test_xpyd_probe_007: Worker probe under no-prefill scenario", func() {
			By("Kill all prefill instances")
			mockServers[0].Kill()
			mockServers[0].Wait()
			mockServers[1].Kill()
			mockServers[1].Wait()

			By("Wait for leader to detect prefill instances as unavailable")
			WaitForInstanceHealthStatus(electedLeaderSession, probePrefill1ID, "http://127.0.0.1:"+probePrefill1Port, HealthStatusUnavailable, healthCheckTimeout)
			WaitForInstanceHealthStatus(electedLeaderSession, probePrefill2ID, "http://127.0.0.1:"+probePrefill2Port, HealthStatusUnavailable, healthCheckTimeout)
			By("Wait for worker1 readiness to reflect prefill unavailability")
			Eventually(func() bool {
				ready, msg, err := workerClient.GetWorkerReadiness()
				if err != nil {
					return false
				}
				return !ready && strings.Contains(msg, "0 prefill")
			}, 30*time.Second, 1*time.Second).Should(BeTrue(), "worker1 should not be ready when all prefill instances are down")

			By("Verify worker1 probe returns not ready, msg contains 0 prefill")
			ready, msg, err := workerClient.GetWorkerReadiness()
			Expect(err).NotTo(HaveOccurred())
			GinkgoWriter.Printf("[007] worker1 readiness without prefill: ready=%v, msg=%s\n", ready, msg)
			Expect(ready).To(BeFalse())
			Expect(msg).To(ContainSubstring("0 prefill"), "msg should contain 0 prefill, indicating prefill instance count does not meet traffic acceptance condition")

			By("Verify liveness probe is still normal (liveness is independent of instance status)")
			livenessResp, err := workerClient.GetLiveness()
			Expect(err).NotTo(HaveOccurred())
			defer livenessResp.Body.Close()
			Expect(livenessResp.StatusCode).To(Equal(http.StatusOK))
		})

		// test_xpyd_probe_008: Worker probe under no-decode scenario
		It("test_xpyd_probe_008: Worker probe under no-decode scenario", func() {
			By("Kill all decode instances")
			mockServers[2].Kill()
			mockServers[2].Wait()
			mockServers[3].Kill()
			mockServers[3].Wait()

			By("Wait for leader to detect decode instances as unavailable")
			WaitForInstanceHealthStatus(electedLeaderSession, probeDecode1ID, "http://127.0.0.1:"+probeDecode1Port, HealthStatusUnavailable, healthCheckTimeout)
			WaitForInstanceHealthStatus(electedLeaderSession, probeDecode2ID, "http://127.0.0.1:"+probeDecode2Port, HealthStatusUnavailable, healthCheckTimeout)
			By("Wait for worker1 readiness to reflect decode unavailability")
			Eventually(func() bool {
				ready, msg, err := workerClient.GetWorkerReadiness()
				if err != nil {
					return false
				}
				return !ready && strings.Contains(msg, "0 decode")
			}, 30*time.Second, 1*time.Second).Should(BeTrue(), "worker1 should not be ready when all decode instances are down")

			By("Verify worker1 probe returns not ready, msg contains 0 decode")
			ready, msg, err := workerClient.GetWorkerReadiness()
			Expect(err).NotTo(HaveOccurred())
			GinkgoWriter.Printf("[008] worker1 readiness without decode: ready=%v, msg=%s\n", ready, msg)
			Expect(ready).To(BeFalse())
			Expect(msg).To(ContainSubstring("0 decode"), "msg should contain 0 decode, indicating decode instance count does not meet traffic acceptance condition")

			By("Verify liveness probe is still normal")
			livenessResp, err := workerClient.GetLiveness()
			Expect(err).NotTo(HaveOccurred())
			defer livenessResp.Body.Close()
			Expect(livenessResp.StatusCode).To(Equal(http.StatusOK))
		})

		// test_xpyd_probe_009: Worker probe under no-prefill no-decode scenario
		It("test_xpyd_probe_009: Worker probe under no-prefill no-decode scenario", func() {
			By("Kill all prefill and decode instances")
			mockServers[0].Kill()
			mockServers[0].Wait()
			mockServers[1].Kill()
			mockServers[1].Wait()
			mockServers[2].Kill()
			mockServers[2].Wait()
			mockServers[3].Kill()
			mockServers[3].Wait()

			By("Wait for leader to detect all instances as unavailable")
			WaitForInstanceHealthStatus(electedLeaderSession, probePrefill1ID, "http://127.0.0.1:"+probePrefill1Port, HealthStatusUnavailable, healthCheckTimeout)
			WaitForInstanceHealthStatus(electedLeaderSession, probePrefill2ID, "http://127.0.0.1:"+probePrefill2Port, HealthStatusUnavailable, healthCheckTimeout)
			WaitForInstanceHealthStatus(electedLeaderSession, probeDecode1ID, "http://127.0.0.1:"+probeDecode1Port, HealthStatusUnavailable, healthCheckTimeout)
			WaitForInstanceHealthStatus(electedLeaderSession, probeDecode2ID, "http://127.0.0.1:"+probeDecode2Port, HealthStatusUnavailable, healthCheckTimeout)
			By("Wait for worker1 readiness to reflect all instances unavailable")
			Eventually(func() bool {
				ready, msg, err := workerClient.GetWorkerReadiness()
				if err != nil {
					return false
				}
				return !ready && strings.Contains(msg, "0 prefill") && strings.Contains(msg, "0 decode")
			}, 30*time.Second, 1*time.Second).Should(BeTrue(), "worker1 should not be ready when all instances are down")

			By("Verify worker1 probe returns not ready, msg contains 0 prefill and 0 decode")
			ready, msg, err := workerClient.GetWorkerReadiness()
			Expect(err).NotTo(HaveOccurred())
			GinkgoWriter.Printf("[009] worker1 readiness without any instance: ready=%v, msg=%s\n", ready, msg)
			Expect(ready).To(BeFalse())
			Expect(msg).To(ContainSubstring("0 prefill"), "msg should contain 0 prefill")
			Expect(msg).To(ContainSubstring("0 decode"), "msg should contain 0 decode")

			By("Verify liveness probe is still normal")
			livenessResp, err := workerClient.GetLiveness()
			Expect(err).NotTo(HaveOccurred())
			defer livenessResp.Body.Close()
			Expect(livenessResp.StatusCode).To(Equal(http.StatusOK))
		})
	})

	// ================================================================
	// 7. 1p1d + Worker process fault scenario probe test
	// ================================================================
	Context("Partial fault scenario probe test", func() {
		BeforeEach(func() {
			killAllRs()
			ensureEtcdClean()
			ensureMockInstancesHealthy()
			startAllRs()
		})

		// test_xpyd_probe_010: Whether traffic is accepted under 1p1d scenario with prefill fault
		It("test_xpyd_probe_010: Whether traffic is accepted under 1p1d scenario with prefill fault", func() {
			By("Kill one prefill and one decode, resulting in 1p1d")
			mockServers[1].Kill() // prefill2
			mockServers[2].Kill() // decode1
			mockServers[1].Wait()
			mockServers[2].Wait()

			By("Wait for leader to detect instances as unavailable")
			WaitForInstanceHealthStatus(electedLeaderSession, probePrefill2ID, "http://127.0.0.1:"+probePrefill2Port, HealthStatusUnavailable, healthCheckTimeout)
			WaitForInstanceHealthStatus(electedLeaderSession, probeDecode1ID, "http://127.0.0.1:"+probeDecode1Port, HealthStatusUnavailable, healthCheckTimeout)
			By("Wait for worker1 readiness to reflect insufficient instances")
			Eventually(func() bool {
				ready, _, err := workerClient.GetWorkerReadiness()
				if err != nil {
					return false
				}
				return !ready
			}, 30*time.Second, 1*time.Second).Should(BeTrue(), "worker1 should not be ready with 1p1d")

			By("Verify worker1 probe returns not ready (1p1d does not meet prefill:2 decode:2 requirement)")
			ready, msg, err := workerClient.GetWorkerReadiness()
			Expect(err).NotTo(HaveOccurred())
			GinkgoWriter.Printf("[010] worker1 readiness 1p1d: ready=%v, msg=%s\n", ready, msg)
			Expect(ready).To(BeFalse())
		})

		// test_xpyd_probe_011: Whether probe works normally when a worker process fails
		// test_xpyd_probe_1_014: Whether probe works normally when all worker processes fail
		It("[test_xpyd_probe_011][test_xpyd_probe_1_014] Worker process fault scenario probe verification", func() {
			By("test_xpyd_probe_011: Verify both workers' readiness and liveness initial states are normal")
			for _, wc := range []*RSClient{workerClient, worker2Client} {
				ready, msg, err := wc.GetWorkerReadiness()
				Expect(err).NotTo(HaveOccurred())
				GinkgoWriter.Printf("[011] worker(%s) readiness: ready=%v, msg=%s\n", wc.BaseURL, ready, msg)
				Expect(ready).To(BeTrue())
				Expect(strings.Contains(msg, `"message":"ready"`)).To(BeTrue())

				livenessResp, err := wc.GetLiveness()
				Expect(err).NotTo(HaveOccurred())
				livenessResp.Body.Close()
				Expect(livenessResp.StatusCode).To(Equal(http.StatusOK))
			}

			By("test_xpyd_probe_011: Kill worker1 process")
			workerSession.Kill()
			workerSession.Wait()
			time.Sleep(1 * time.Second)

			By("test_xpyd_probe_011: Verify worker1 probe returns error (process is dead)")
			_, _, err := workerClient.GetWorkerReadiness()
			Expect(err).To(HaveOccurred(), "worker1 readiness should return error")
			_, err = workerClient.GetLiveness()
			Expect(err).To(HaveOccurred(), "worker1 liveness should return error")

			By("test_xpyd_probe_011: Verify worker2 probe is still normal")
			ready, msg, err := worker2Client.GetWorkerReadiness()
			Expect(err).NotTo(HaveOccurred())
			GinkgoWriter.Printf("[011] worker2 readiness after worker1 killed: ready=%v, msg=%s\n", ready, msg)
			Expect(ready).To(BeTrue())
			Expect(strings.Contains(msg, `"message":"ready"`)).To(BeTrue())

			livenessResp, err := worker2Client.GetLiveness()
			Expect(err).NotTo(HaveOccurred())
			livenessResp.Body.Close()
			Expect(livenessResp.StatusCode).To(Equal(http.StatusOK))

			By("test_xpyd_probe_1_014: Kill worker2 process (all workers are down)")
			worker2Session.Kill()
			worker2Session.Wait()
			time.Sleep(1 * time.Second)

			By("test_xpyd_probe_1_014: Verify worker2 probe returns error (process is dead)")
			_, _, err = worker2Client.GetWorkerReadiness()
			Expect(err).To(HaveOccurred(), "worker2 readiness should return error")
			_, err = worker2Client.GetLiveness()
			Expect(err).To(HaveOccurred(), "worker2 liveness should return error")
		})
	})
})
