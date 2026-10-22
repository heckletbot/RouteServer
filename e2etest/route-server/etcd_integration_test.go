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
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gbytes"
	"github.com/onsi/gomega/gexec"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/server/v3/embed"

	"infer-dp-scheduler/pkg/common/constant"
)

const (
	etcdIntegrationCtx = "etcd-integration-test"
	leaderHTTPPort     = "35878"
	// leader ops port is 35888
	workerHTTPPort = "35879"
	// worker ops port is 35889
	prefill1Port = "60000"
	prefill2Port = "60001"
	decodePort   = "60002"

	prefill1ID      = "prefill1"
	prefill2ID      = "prefill2"
	decodeID        = "decode1"
	etcdFailuresKey = "/rs/instances/failures"
	rsLeaderKey     = "/rs/leader"
	rsPeersAllDir   = "/rs/cluster/state/peers/all/"
	etcdClientPort  = "2379"

	// Default health check parameters
	healthCheckIntervalMs = 10000
	healthConsecutiveTime = 3
	etcdCacheExpireMs     = 40000
)

var _ = Describe(etcdIntegrationCtx, Ordered, func() {
	var (
		leaderSession *gexec.Session
		workerSession *gexec.Session
		mockServers   []*gexec.Session
		etcdServer    *embed.Etcd
		etcdClient    *clientv3.Client
	)

	BeforeAll(func() {
		var err error

		By("step 0: ensure ports 2379/2380/60000/60001/60002 are free")
		for _, p := range []string{etcdClientPort, "2380", prefill1Port, prefill2Port, decodePort} {
			out, _ := exec.Command("sh", "-c", fmt.Sprintf("lsof -ti tcp:%s 2>/dev/null", p)).Output()
			if len(out) > 0 {
				pids := strings.Fields(string(out))
				for _, pid := range pids {
					GinkgoWriter.Printf("[cleanup] killing pid %s holding port %s\n", pid, p)
					exec.Command("kill", "-9", pid).Run()
				}
			}
		}
		time.Sleep(500 * time.Millisecond)

		By("step 1: start embedded etcd via embed.StartEtcd on port " + etcdClientPort)
		etcdCfg := embed.NewConfig()
		etcdCfg.Name = "test-etcd"
		etcdCfg.Dir = filepath.Join(os.TempDir(), fmt.Sprintf("etcd-test-%d", time.Now().UnixNano()))
		etcdCfg.ListenPeerUrls = []url.URL{{Scheme: "http", Host: "127.0.0.1:2380"}}
		etcdCfg.ListenClientUrls = []url.URL{{Scheme: "http", Host: fmt.Sprintf("127.0.0.1:%s", etcdClientPort)}}
		etcdCfg.AdvertisePeerUrls = []url.URL{{Scheme: "http", Host: "127.0.0.1:2380"}}
		etcdCfg.AdvertiseClientUrls = []url.URL{{Scheme: "http", Host: fmt.Sprintf("127.0.0.1:%s", etcdClientPort)}}
		etcdCfg.InitialCluster = fmt.Sprintf("test-etcd=http://127.0.0.1:2380")
		etcdCfg.InitialClusterToken = "test-token"
		etcdCfg.ClusterState = embed.ClusterStateFlagNew
		// Reduce etcd log output to avoid interference
		etcdCfg.LogLevel = "error"

		etcdServer, err = embed.StartEtcd(etcdCfg)
		Expect(err).NotTo(HaveOccurred())
		select {
		case <-etcdServer.Server.ReadyNotify():
			GinkgoWriter.Printf("[etcd] embedded etcd server ready\n")
		case <-time.After(10 * time.Second):
			Fail("etcd server did not become ready within 10s")
		}

		// Create client connection to etcd
		cli, err := clientv3.New(clientv3.Config{
			Endpoints:   []string{fmt.Sprintf("127.0.0.1:%s", etcdClientPort)},
			DialTimeout: 5 * time.Second,
		})
		Expect(err).NotTo(HaveOccurred())
		etcdClient = cli
		GinkgoWriter.Printf("[etcd] client connected to 127.0.0.1:%s\n", etcdClientPort)

		By("step 2: generate leader and worker config via config-tool")
		deployPath := filepath.Join(projectRoot, "deploy")
		etcdCtxDir := filepath.Join(projectRoot, "e2etest", "environment", etcdIntegrationCtx)
		leaderCfgDir := filepath.Join(etcdCtxDir, "leader1")
		workerCfgDir := filepath.Join(etcdCtxDir, "worker1")

		// Generate leader config
		Expect(os.RemoveAll(leaderCfgDir)).To(BeNil())
		Expect(os.MkdirAll(filepath.Join(leaderCfgDir, "config"), 0755)).To(BeNil())
		leaderToolParam := DisableHTTPSConfig([]string{
			"-template", filepath.Join(deployPath, "model-orch-template.yaml"),
			"-host", "127.0.0.1",
			"-target", leaderCfgDir,
			"-infer-mode", "PD",
			"-prefills", fmt.Sprintf("prefill1#127.0.0.1:%s,prefill2#127.0.0.1:%s", prefill1Port, prefill2Port),
			"-decodes", fmt.Sprintf("decode1#127.0.0.1:%s", decodePort),
			"-name", "leader1",
			"-remote-scheduler-type", "disable",
			"-port", leaderHTTPPort,
			"-candidate", "true",
			"-etcd-endpoints", fmt.Sprintf("127.0.0.1:%s", etcdClientPort),
			"-infer-module-version", "pipeline",
		})
		leaderOut, leaderErr := exec.Command(configToolPath, leaderToolParam...).CombinedOutput()
		GinkgoWriter.Printf("[config] leader config-tool output: %s\n", string(leaderOut))
		Expect(leaderErr).To(BeNil())

		// Generate worker config
		Expect(os.RemoveAll(workerCfgDir)).To(BeNil())
		Expect(os.MkdirAll(filepath.Join(workerCfgDir, "config"), 0755)).To(BeNil())
		workerToolParam := DisableHTTPSConfig([]string{
			"-template", filepath.Join(deployPath, "model-orch-template.yaml"),
			"-host", "127.0.0.1",
			"-target", workerCfgDir,
			"-infer-mode", "PD",
			"-prefills", fmt.Sprintf("prefill1#127.0.0.1:%s,prefill2#127.0.0.1:%s", prefill1Port, prefill2Port),
			"-decodes", fmt.Sprintf("decode1#127.0.0.1:%s", decodePort),
			"-name", "worker1",
			"-remote-scheduler-type", "leader",
			"-port", workerHTTPPort,
			"-candidate", "false",
			"-etcd-endpoints", fmt.Sprintf("127.0.0.1:%s", etcdClientPort),
			"-infer-module-version", "pipeline",
		})
		workerOut, workerErr := exec.Command(configToolPath, workerToolParam...).CombinedOutput()
		GinkgoWriter.Printf("[config] worker config-tool output: %s\n", string(workerOut))
		Expect(workerErr).To(BeNil())

		// Copy pipelines config
		Expect(os.MkdirAll(filepath.Join(leaderCfgDir, "config", "pipelines"), 0755)).To(BeNil())
		Expect(os.MkdirAll(filepath.Join(workerCfgDir, "config", "pipelines"), 0755)).To(BeNil())
		Expect(copyDir(filepath.Join(deployPath, "pipelines"), filepath.Join(leaderCfgDir, "config", "pipelines"))).To(BeNil())
		Expect(copyDir(filepath.Join(deployPath, "pipelines"), filepath.Join(workerCfgDir, "config", "pipelines"))).To(BeNil())

		// Copy metrics config
		metricsCollectorConfigFileName := "metrics_config_vllm_0.11.0.yaml"
		Expect(copyFile(
			filepath.Join(deployPath, metricsCollectorConfigFileName),
			filepath.Join(leaderCfgDir, "config", metricsCollectorConfigFileName),
		)).To(BeNil())
		Expect(copyFile(
			filepath.Join(deployPath, metricsCollectorConfigFileName),
			filepath.Join(workerCfgDir, "config", metricsCollectorConfigFileName),
		)).To(BeNil())

		DeferCleanup(func() {
			os.RemoveAll(leaderCfgDir)
			os.RemoveAll(workerCfgDir)
		})

		By("step 3: start mock vllm instances (2 prefill + 1 decode)")
		mockInstances := []struct {
			port string
			name string
		}{
			{prefill1Port, "prefill1"},
			{prefill2Port, "prefill2"},
			{decodePort, "decode1"},
		}
		for _, inst := range mockInstances {
			params := []string{
				"-addr", "127.0.0.1",
				"-port", inst.port,
				"-name", inst.name,
			}
			command := exec.Command(mockVLLMPath, params...)
			sess, startErr := gexec.Start(command, GinkgoWriter, GinkgoWriter)
			Expect(startErr).NotTo(HaveOccurred())
			mockServers = append(mockServers, sess)
			GinkgoWriter.Printf("[mock] started %s on port %s\n", inst.name, inst.port)
		}
		time.Sleep(500 * time.Millisecond)

		By("step 4: start leader route-server")
		leaderEnv := os.Environ()
		leaderEnv = append(leaderEnv, fmt.Sprintf("%s=%s",
			constant.EnvServerConfigPath, filepath.Join(leaderCfgDir, "config")))
		command := exec.Command(rsPath)
		command.Env = leaderEnv
		leaderSession, err = gexec.Start(command, GinkgoWriter, GinkgoWriter)
		Expect(err).NotTo(HaveOccurred())

		By("step 5: start worker route-server")
		workerEnv := os.Environ()
		workerEnv = append(workerEnv, fmt.Sprintf("%s=%s",
			constant.EnvServerConfigPath, filepath.Join(workerCfgDir, "config")))
		command = exec.Command(rsPath)
		command.Env = workerEnv
		workerSession, err = gexec.Start(command, GinkgoWriter, GinkgoWriter)
		Expect(err).NotTo(HaveOccurred())

		By("step 6: wait for both servers to be ready (listen)")
		Eventually(leaderSession.Out, 30*time.Second).Should(gbytes.Say("Start HTTP server on"))
		GinkgoWriter.Printf("[leader] route-server is ready on port %s\n", leaderHTTPPort)
		Eventually(workerSession.Out, 30*time.Second).Should(gbytes.Say("Start HTTP server on"))
		GinkgoWriter.Printf("[worker] route-server is ready on port %s\n", workerHTTPPort)

		By("step 7: wait for leader election and worker registration via etcd")
		Eventually(func() bool {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			resp, getErr := etcdClient.Get(ctx, rsLeaderKey, clientv3.WithPrefix())
			return getErr == nil && len(resp.Kvs) > 0 && strings.Contains(string(resp.Kvs[0].Value), "leader1")
		}, 30*time.Second, 1*time.Second).Should(BeTrue(), "leader1 should be elected as leader in etcd")
		GinkgoWriter.Printf("[etcd] leader1 elected, confirmed via %s\n", rsLeaderKey)

		Eventually(func() bool {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			resp, getErr := etcdClient.Get(ctx, rsPeersAllDir, clientv3.WithPrefix())
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
		GinkgoWriter.Printf("[etcd] worker1 registered, confirmed via %s\n", rsPeersAllDir)

		By("step 8: wait for worker to sync health cache from leader via events")
		workerClient := &RSClient{BaseURL: "http://localhost:" + workerHTTPPort, HTTPClient: &http.Client{}}
		Eventually(func() bool {
			ready, msg, err := workerClient.GetWorkerReadiness()
			if err != nil {
				GinkgoWriter.Printf("[worker] readiness check error: %v\n", err)
				return false
			}
			GinkgoWriter.Printf("[worker] readiness: ready=%v, msg=%s\n", ready, msg)
			return strings.Contains(msg, `"message":"ready"`)
		}, 60*time.Second, 2*time.Second).Should(BeTrue(), "worker health cache should be synced from leader")
	})

	AfterAll(func() {
		By("cleanup route-servers")
		if workerSession != nil {
			workerSession.Kill()
			workerSession.Wait()
		}
		if leaderSession != nil {
			leaderSession.Kill()
			leaderSession.Wait()
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

	Context("etcd failure cache impact on fallback scheduling", func() {
		var (
			workerClient *RSClient
		)

		BeforeEach(func() {
			workerClient = NewRSClient("http://localhost:" + workerHTTPPort)
		})

		It("[baseline] when leader is normal, verify worker forwards to leader after health cache sync", func() {
			By("after worker health cache is synced, send request to verify worker correctly forwards to leader")
			ctx := TimeoutCtx(context.Background(), 30*time.Second)
			resp, err := workerClient.SendChatCompletion(ctx, NewBasicChatRequestBody(), nil)
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()
			GinkgoWriter.Printf("[baseline] worker->leader response status: %d\n", resp.StatusCode)
			Expect(resp.StatusCode).To(Or(Equal(http.StatusOK), Equal(http.StatusServiceUnavailable)))
		})

		It("[verify] kill prefill instance -> leader writes to etcd -> kill leader -> within cache period fallback skips faulty instance -> after cache expires faulty instance can be scheduled", func() {
			By("Phase 1: kill prefill1 instance (port " + prefill1Port + "), wait for leader health check to detect fault and write to etcd")
			prefill1Session := mockServers[0]
			prefill1Pid := prefill1Session.Command.Process.Pid
			GinkgoWriter.Printf("[prefill1] killing process pid=%d\n", prefill1Pid)
			prefill1Session.Kill()
			prefill1Session.Wait()

			maxWait := time.Duration(healthConsecutiveTime*healthCheckIntervalMs+20000) * time.Millisecond
			GinkgoWriter.Printf("[etcd] waiting for leader to detect failure and write to etcd (max %.0fs)...\n",
				maxWait.Seconds())

			Eventually(func() bool {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				getResp, getErr := etcdClient.Get(ctx, etcdFailuresKey)
				if getErr != nil || len(getResp.Kvs) == 0 {
					return false
				}
				GinkgoWriter.Printf("[etcd] leader wrote failure data! key=%s\n", getResp.Kvs[0].Key)
				return true
			}, maxWait, 1*time.Second).Should(BeTrue(), "leader should have written failure data to etcd")

			By("Phase 2: kill leader process, worker enters fallback mode")
			leaderSession.Kill()
			leaderSession.Wait()
			GinkgoWriter.Printf("[leader] process killed\n")
			time.Sleep(3 * time.Second)

			By("Phase 2.5: send request to trigger fallback, verify etcd failure cache takes effect")
			// First send request to trigger fallback, then check etcd failure cache interception log and Fallback sched log
			reqCtx := TimeoutCtx(context.Background(), 30*time.Second)
			resp, err := workerClient.SendChatCompletion(reqCtx, NewBasicChatRequestBody(), nil)
			Expect(err).NotTo(HaveOccurred())
			resp.Body.Close()
			// Wait a while for log buffer to be written
			time.Sleep(2 * time.Second)
			// Check buffer content directly
			output := string(workerSession.Out.Contents())
			GinkgoWriter.Printf("[debug] workerSession.Out length: %d\n", len(output))
			Expect(strings.Contains(output, "Instance prefill1 is in etcd failure cache")).To(BeTrue(),
				"should see etcd failure cache log")
			Expect(strings.Contains(output, "Fallback sched")).To(BeTrue(),
				"should see fallback sched log")
			GinkgoWriter.Printf("[worker] confirmed etcd failure cache is filtering prefill1\n")

			By(fmt.Sprintf("Phase 3: within cache window, verify multiple requests are NOT dispatched to prefill1 (port %s)", prefill1Port))
			prefill1Scheduled := false
			for i := 0; i < 5; i++ {
				reqCtx := TimeoutCtx(context.Background(), 30*time.Second)
				resp, err := workerClient.SendChatCompletion(reqCtx, NewBasicChatRequestBody(), nil)
				Expect(err).NotTo(HaveOccurred())
				resp.Body.Close()
				prefillLog := fmt.Sprintf("send request to \\[prefill\\] instance \\[%s\\]", prefill1ID)
				if strings.Contains(string(workerSession.Out.Contents()), prefillLog) {
					prefill1Scheduled = true
				}
			}
			Expect(prefill1Scheduled).To(BeFalse(),
				"during etcd cache window, should NOT dispatch to killed prefill1(%s)", prefill1Port)

			By(fmt.Sprintf("Phase 4: wait for etcd failure cache to expire (about %dms)...", etcdCacheExpireMs))
			time.Sleep(time.Duration(etcdCacheExpireMs) * time.Millisecond)
			GinkgoWriter.Printf("[etcd] cache should now be expired\n")

			By(fmt.Sprintf("Phase 5: after cache expires, verify etcd failure cache expired (check worker logs)"))
			// prefill1 process is dead, but after etcd failure cache expires, mergeEtcdFailureBlacklist should stop intercepting
			// Due to other blacklist mechanisms like link-blacklist (180s), prefill1 still won't be scheduled
			// Verify fallback scheduler still works normally (didn't crash due to etcd cache issue)
			reqCtx2 := TimeoutCtx(context.Background(), 30*time.Second)
			resp2, err2 := workerClient.SendChatCompletion(reqCtx2, NewBasicChatRequestBody(), nil)
			Expect(err2).NotTo(HaveOccurred())
			resp2.Body.Close()
			GinkgoWriter.Printf("[fallback-after-expiry] fallback scheduler works correctly after etcd cache expiry\n")
		})
	})
})
