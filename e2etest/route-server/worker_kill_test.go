package route_server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
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
	commonScheduler "infer-dp-scheduler/pkg/common/scheduler"
)

const workerKillTestCtx = "worker-kill-test"

var _ = Describe(workerKillTestCtx, Ordered, func() {
	var (
		leaderInstances []rsInstance
		workerInstances []rsInstance
		allInstances    []rsInstance
		mockServers     []*gexec.Session
		etcdServer      *embed.Etcd
		etcdClient      *clientv3.Client

		leaderClient  *RSClient
		worker1Client *RSClient
		worker2Client *RSClient

		deployPath         string
		testCtxDir         string
		targetTemplateFile string
	)

	const (
		leader1HTTPPort = "40200"
		leader1OpsPort  = "40210"
		worker1HTTPPort = "40201"
		worker1OpsPort  = "40211"
		worker2HTTPPort = "40202"
		worker2OpsPort  = "40212"
		prefill1Port    = "40203"
		prefill2Port    = "40204"
		decodePort      = "40205"
		etcdClientPort  = "40206"
		etcdPeerPort    = "40207"

		rsLeaderKey   = "/rs/leader"
		rsPeersAllDir = "/rs/cluster/state/peers/all/"
	)

	BeforeAll(func() {
		var err error

		deployPath = filepath.Join(projectRoot, "deploy")
		testCtxDir = filepath.Join(projectRoot, "e2etest", "environment", workerKillTestCtx)
		targetTemplateFile = filepath.Join(testCtxDir, TEMP_TEMPLATE)

		By("step 0: ensure ports are free")
		ports := []string{etcdClientPort, etcdPeerPort,
			prefill1Port, prefill2Port, decodePort,
			leader1HTTPPort, worker1HTTPPort, worker2HTTPPort,
			leader1OpsPort, worker1OpsPort, worker2OpsPort}
		EnsurePorts(ports)
		time.Sleep(500 * time.Millisecond)

		By("step 1: start embedded etcd via embed.StartEtcd on port " + etcdClientPort)
		etcdCfg := embed.NewConfig()
		etcdCfg.Name = "test-etcd"
		etcdCfg.Dir = filepath.Join(os.TempDir(), fmt.Sprintf("etcd-test-%d", time.Now().UnixNano()))
		etcdCfg.ListenPeerUrls = []url.URL{{Scheme: "http", Host: fmt.Sprintf("127.0.0.1:%s", etcdPeerPort)}}
		etcdCfg.ListenClientUrls = []url.URL{{Scheme: "http", Host: fmt.Sprintf("127.0.0.1:%s", etcdClientPort)}}
		etcdCfg.AdvertisePeerUrls = []url.URL{{Scheme: "http", Host: fmt.Sprintf("127.0.0.1:%s", etcdPeerPort)}}
		etcdCfg.AdvertiseClientUrls = []url.URL{{Scheme: "http", Host: fmt.Sprintf("127.0.0.1:%s", etcdClientPort)}}
		etcdCfg.InitialCluster = fmt.Sprintf("test-etcd=http://127.0.0.1:%s", etcdPeerPort)
		etcdCfg.InitialClusterToken = "test-token"
		etcdCfg.ClusterState = embed.ClusterStateFlagNew
		etcdCfg.LogLevel = "error"

		etcdServer, err = embed.StartEtcd(etcdCfg)
		Expect(err).NotTo(HaveOccurred())
		select {
		case <-etcdServer.Server.ReadyNotify():
			GinkgoWriter.Printf("[etcd] embedded etcd server ready\n")
		case <-time.After(10 * time.Second):
			Fail("etcd server did not become ready within 10s")
		}

		cli, err := clientv3.New(clientv3.Config{
			Endpoints:   []string{fmt.Sprintf("127.0.0.1:%s", etcdClientPort)},
			DialTimeout: 5 * time.Second,
		})
		Expect(err).NotTo(HaveOccurred())
		etcdClient = cli
		GinkgoWriter.Printf("[etcd] client connected to 127.0.0.1:%s\n", etcdClientPort)

		By("step 2: start mock vllm instances (2 prefill + 1 decode)")
		mockServers = StartMockVLLMInstances([]struct{ Port, Name string }{
			{prefill1Port, "prefill1"},
			{prefill2Port, "prefill2"},
			{decodePort, "decode1"},
		})
		time.Sleep(500 * time.Millisecond)

		By("step 3: generate config and start route-servers")

		var cfg commoncfg.Config
		templateContent, err := os.ReadFile(filepath.Join(deployPath, ROUTE_SERVER_TEMPLATE))
		Expect(yaml.Unmarshal(templateContent, &cfg)).To(BeNil())
		cfg.MetaCleanupCfg = &commoncfg.MetaCleanupConfig{
			MaxInstanceDownTimeSec: 30,
			MaxWorkerDownTimeSec:   15,
		}
		// set failure time window to 10s, so that we can test fallback error
		cfg.InstanceManagerCfg.HealthCheckConfig.AvailableCheckersCfg.RequestFailuresCfg.TimeWindow = 10000
		// set fallback failure resume windows to 10s for quicker resume
		cfg.InstanceManagerCfg.HealthCheckConfig.FallbackHealthCheckConfig.ResumeTimeMs = 10000
		outData, err := yaml.Marshal(&cfg)
		Expect(err).To(BeNil())

		Expect(os.MkdirAll(testCtxDir, 0755)).To(BeNil())
		Expect(os.WriteFile(targetTemplateFile, outData, 0644)).To(BeNil())

		prefillsStr := fmt.Sprintf("prefill1#127.0.0.1:%s,prefill2#127.0.0.1:%s", prefill1Port, prefill2Port)
		decodesStr := fmt.Sprintf("decode1#127.0.0.1:%s", decodePort)
		etcdEndpoint := fmt.Sprintf("127.0.0.1:%s", etcdClientPort)

		leaderCfgs := []InstanceConfigCtx{
			{
				httpPort:      leader1HTTPPort,
				name:          "leader1",
				targetCfgDir:  filepath.Join(testCtxDir, "leader1"),
				prefillsStr:   prefillsStr,
				decodesStr:    decodesStr,
				etcdEndpoints: etcdEndpoint,
				isLeader:      true,
			},
		}
		workerCfgs := []InstanceConfigCtx{
			{
				httpPort:      worker1HTTPPort,
				name:          "worker1",
				targetCfgDir:  filepath.Join(testCtxDir, "worker1"),
				prefillsStr:   prefillsStr,
				decodesStr:    decodesStr,
				etcdEndpoints: etcdEndpoint,
				isLeader:      false,
			},
			{
				httpPort:      worker2HTTPPort,
				name:          "worker2",
				targetCfgDir:  filepath.Join(testCtxDir, "worker2"),
				prefillsStr:   prefillsStr,
				decodesStr:    decodesStr,
				etcdEndpoints: etcdEndpoint,
				isLeader:      false,
			},
		}

		for _, cfg := range leaderCfgs {
			inst := SetupRouteServerConfig(cfg, deployPath, targetTemplateFile, "PD", "pipeline")
			leaderInstances = append(leaderInstances, inst)
			allInstances = append(allInstances, inst)
		}
		for _, cfg := range workerCfgs {
			inst := SetupRouteServerConfig(cfg, deployPath, targetTemplateFile, "PD", "pipeline")
			workerInstances = append(workerInstances, inst)
			allInstances = append(allInstances, inst)
		}

		DeferCleanup(func() {
			for _, inst := range allInstances {
				os.RemoveAll(inst.CfgDir)
			}
		})

		By("step 4: wait for leader election via etcd")
		WaitForLeaderElection(etcdClient, rsLeaderKey, "leader1", 30*time.Second)

		By("step 5: wait for worker registration via etcd")
		for _, w := range workerCfgs {
			WaitForWorkerRegistration(etcdClient, rsPeersAllDir, w.name, 30*time.Second)
		}

		By("step 6: wait for workers to sync health cache from leader via events")
		for _, w := range workerInstances {
			workerClient := NewRSClient("http://localhost:" + w.HTTPPort)
			Eventually(func() bool {
				ready, msg, err := workerClient.GetWorkerReadiness()
				if err != nil {
					GinkgoWriter.Printf("[worker %s] readiness check error: %v\n", w.Name, err)
					return false
				}
				GinkgoWriter.Printf("[worker %s] readiness: ready=%v, msg=%s\n", w.Name, ready, msg)
				return strings.Contains(msg, `"message":"ready"`)
			}, 60*time.Second, 2*time.Second).Should(BeTrue(), "worker %s health cache should be synced from leader", w.Name)
		}

		leaderClient = NewRSClient("http://localhost:" + leader1HTTPPort)
		worker1Client = NewRSClient("http://localhost:" + worker1HTTPPort)
		worker2Client = NewRSClient("http://localhost:" + worker2HTTPPort)
	})

	AfterAll(func() {
		By("cleanup route-servers")
		CleanupSessions(sessionsFromInstances(allInstances))

		By("cleanup mock servers")
		CleanupSessions(mockServers)

		By("cleanup etcd server")
		if etcdClient != nil {
			etcdClient.Close()
		}
		if etcdServer != nil {
			etcdServer.Close()
		}
	})

	// Consume existing leader log content in BeforeEach to avoid interfering with subsequent log assertions
	BeforeEach(func() {
		Eventually(leaderInstances[0].Session.Out).Should(gbytes.Say(".*"))
		for _, w := range workerInstances {
			Eventually(w.Session.Out).Should(gbytes.Say(".*"))
		}
		// unblock iptables
		UnblockTrafficToPort(leader1HTTPPort)
	})

	// sendFastInferRequest sends a non-streaming inference request (fast return) to the specified client.
	sendFastInferRequest := func(client *RSClient, requestId string) (*http.Response, error) {
		body := NewBasicChatRequestBody()
		body.MaxTokens = 10
		body.Stream = false
		headers := map[string]string{constant.XRequestId: requestId}
		return client.SendChatCompletion(TimeoutCtx(backgroundCtx(), 60*time.Second), body, headers)
	}

	// sendLongInferRequest constructs a streaming inference request (large MaxTokens, long-running output), used for kill tests.
	// Returns the response and body content (streaming body may be truncated after kill, caller should check).
	sendLongInferRequest := func(client *RSClient, requestId string) (*http.Response, string, error) {
		body := NewBasicChatRequestBody()
		body.MaxTokens = 1000000
		body.Stream = true
		headers := map[string]string{constant.XRequestId: requestId}
		resp, err := client.SendChatCompletion(TimeoutCtx(backgroundCtx(), 60*time.Second), body, headers)
		if err != nil {
			return resp, "", err
		}
		defer resp.Body.Close()
		respBody, err := io.ReadAll(resp.Body)
		if err != nil {
			return resp, string(respBody), err
		}
		return resp, string(respBody), nil
	}

	// schedulerLoadResponse corresponds to the response structure of GET /scheduler/load
	type schedulerLoadResponse struct {
		RsName string                                  `json:"rs_name"`
		Loads  map[string]*commonScheduler.SLOLoadInfo `json:"loads,omitempty"`
	}

	// parseLoadResponse parses the /scheduler/load response and returns a map of instance loads
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

	Context("test stop worker scenario", func() {
		It("test cluster can process infer request", func() {
			By("send infer requests to both workers directly")
			reqId1 := "kill-test-req-worker1"
			reqId2 := "kill-test-req-worker2"

			resp1, err1 := sendFastInferRequest(worker1Client, reqId1)
			Expect(err1).NotTo(HaveOccurred())
			Expect(resp1.StatusCode).To(Equal(http.StatusOK))
			resp1.Body.Close()

			resp2, err2 := sendFastInferRequest(worker2Client, reqId2)
			Expect(err2).NotTo(HaveOccurred())
			Expect(resp2.StatusCode).To(Equal(http.StatusOK))
			resp2.Body.Close()

			By("check leader log for remote schedule messages")
			leaderSession := leaderInstances[0].Session
			// request id in logs is rewritten as chatcmpl-{originalId}-{randomSuffix}
			Eventually(leaderSession.Out, 10*time.Second).Should(gbytes.Say(
				fmt.Sprintf(`remote schedule for .* req \[chatcmpl-%s-`, reqId1)))
			Eventually(leaderSession.Out, 10*time.Second).Should(gbytes.Say(
				fmt.Sprintf(`remote schedule for .* req \[chatcmpl-%s-`, reqId2)))
		})

		It("test kill gracefully", func() {
			if os.PathSeparator == '\\' {
				Skip("windows can't kill gracefully, skip use case")
			}

			worker1 := workerInstances[0]
			leaderSession := leaderInstances[0].Session
			worker1CfgDir := worker1.CfgDir

			By("step 1: send infer requests to both workers directly")
			reqId1 := "graceful-kill-req-w1"
			reqId2 := "graceful-kill-req-w2"

			// worker1 uses streaming + large max_tokens to keep long-running output on decode
			longReqSent := make(chan struct{}, 1)
			var longRespErr error
			go func() {
				defer GinkgoRecover()
				resp1, body1, err1 := sendLongInferRequest(worker1Client, reqId1)
				longRespErr = err1
				if resp1 != nil {
					GinkgoWriter.Printf("[long request] worker1 stream resp status=%d, body_len=%d\n",
						resp1.StatusCode, len(body1))
					resp1.Body.Close()
				}
				close(longReqSent)
			}()
			// wait for streaming request to be established (already sent to worker1)
			time.Sleep(1 * time.Second)

			// worker2 uses non-streaming fast request
			resp2, err2 := sendFastInferRequest(worker2Client, reqId2)
			Expect(err2).NotTo(HaveOccurred())
			Expect(resp2.StatusCode).To(Equal(http.StatusOK))
			resp2.Body.Close()

			By("step 2: send SIGTERM to worker1 for graceful shutdown")
			worker1.Session.Terminate().Wait(10 * time.Second)
			GinkgoWriter.Printf("[test] worker1 (%s) terminated gracefully\n", worker1.Name)

			By("step 3: check worker1 log shows leave request was sent to leader")
			Eventually(worker1.Session.Out, 5*time.Second).Should(gbytes.Say(
				"rs peer successfully sent leave request to current leader"))

			// wait for streaming request goroutine to complete (connection should break after worker is killed, request should error)
			Eventually(longReqSent, 15*time.Second).Should(BeClosed())
			GinkgoWriter.Printf("[test] long request to worker1 completed, err=%v\n", longRespErr)

			By("step 4: send request to worker2, should still work fine")
			reqId3 := "graceful-kill-req-w2-after"
			resp3, err3 := sendFastInferRequest(worker2Client, reqId3)
			Expect(err3).NotTo(HaveOccurred())
			Expect(resp3.StatusCode).To(Equal(http.StatusOK))
			resp3.Body.Close()

			By("step 5: check leader log for load cleanup of leaved worker")
			Eventually(leaderSession.Out, 10*time.Second).Should(gbytes.Say(
				`scheduler cleaned up.*requests' load of leaved worker \[worker1\]`))

			By("step 5a: wait for 2 sec for load sync to leader")
			time.Sleep(2 * time.Second)

			By("step 5b: check leader scheduler/load shows all decode loads are 0 (graceful cleanup)")
			loadResp, loadErr := leaderClient.GetSchedulerLoad()
			Expect(loadErr).NotTo(HaveOccurred())
			allInstanceLoads := parseLoadResponse(loadResp)
			for id, load := range allInstanceLoads {
				GinkgoWriter.Printf("[load] instance %s load = %f\n", id, load)
				Expect(load).To(BeNumerically("==", 0), "decode load for %s should be 0 after graceful cleanup", id)
			}

			By("step 6: restart worker1 (re-setup config and start)")
			worker1Cfg := InstanceConfigCtx{
				httpPort:      worker1HTTPPort,
				name:          "worker1",
				targetCfgDir:  filepath.Dir(worker1CfgDir),
				prefillsStr:   fmt.Sprintf("prefill1#127.0.0.1:%s,prefill2#127.0.0.1:%s", prefill1Port, prefill2Port),
				decodesStr:    fmt.Sprintf("decode1#127.0.0.1:%s", decodePort),
				etcdEndpoints: fmt.Sprintf("127.0.0.1:%s", etcdClientPort),
				isLeader:      false,
			}
			deployPath := filepath.Join(projectRoot, "deploy")
			newWorker1 := SetupRouteServerConfig(worker1Cfg, deployPath, targetTemplateFile, "PD", "pipeline")
			// replace references in workerInstances and allInstances
			workerInstances[0] = newWorker1
			for i, inst := range allInstances {
				if inst.Name == "worker1" {
					allInstances[i] = newWorker1
					break
				}
			}

			By("step 6b: check worker1 log shows cluster state restored")
			Eventually(newWorker1.Session.Out, 10*time.Second).Should(gbytes.Say(
				"ClusterState Changes: state restored"))

			By("step 7: check worker1 readiness returns 200")
			w1Client := NewRSClient("http://localhost:" + worker1HTTPPort)
			Eventually(func() bool {
				ready, msg, err := w1Client.GetWorkerReadiness()
				if err != nil {
					return false
				}
				GinkgoWriter.Printf("[worker1] readiness after restart: ready=%v, msg=%s\n", ready, msg)
				return ready
			}, 30*time.Second, 2*time.Second).Should(BeTrue(), "worker1 should be ready after restart")

			// recreate worker1Client since worker1 restarted with same port but different process
			newWorker1Client := NewRSClient("http://localhost:" + worker1HTTPPort)

			By("step 7b: send infer request to restarted worker1, should succeed")
			reqId4 := "graceful-kill-req-w1-restarted"
			resp4, err4 := sendFastInferRequest(newWorker1Client, reqId4)
			Expect(err4).NotTo(HaveOccurred())
			Expect(resp4.StatusCode).To(Equal(http.StatusOK))
			resp4.Body.Close()

			By("step 7c: check leader log shows remote schedule to worker1 after restart")
			Eventually(leaderSession.Out, 10*time.Second).Should(gbytes.Say(
				fmt.Sprintf(`remote schedule for .* req \[chatcmpl-%s-`, reqId4)))
		})

		It("test force kill", func() {
			worker1 := workerInstances[0]
			leaderSession := leaderInstances[0].Session
			worker1CfgDir := worker1.CfgDir

			By("step 1: send infer requests to both workers directly")
			reqId1 := "force-kill-req-w1"
			reqId2 := "force-kill-req-w2"

			// worker1 uses streaming + large max_tokens to keep long-running output on decode
			longReqSent := make(chan struct{}, 1)
			go func() {
				defer GinkgoRecover()
				resp1, body1, err1 := sendLongInferRequest(worker1Client, reqId1)
				if resp1 != nil {
					GinkgoWriter.Printf("[long request] worker1 stream resp status=%d, body_len=%d, err=%v\n",
						resp1.StatusCode, len(body1), err1)
					resp1.Body.Close()
				}
				close(longReqSent)
			}()
			time.Sleep(1 * time.Second)

			// worker2 uses non-streaming fast request
			resp2, err2 := sendFastInferRequest(worker2Client, reqId2)
			Expect(err2).NotTo(HaveOccurred())
			Expect(resp2.StatusCode).To(Equal(http.StatusOK))
			resp2.Body.Close()

			By("step 2: force kill worker1 with SIGKILL")
			worker1.Session.Kill().Wait(5 * time.Second)
			GinkgoWriter.Printf("[test] worker1 (%s) force killed\n", worker1.Name)

			By("step 3: send request to worker2, should still work fine")
			reqId3 := "force-kill-req-w2-after"
			resp3, err3 := sendFastInferRequest(worker2Client, reqId3)
			Expect(err3).NotTo(HaveOccurred())
			Expect(resp3.StatusCode).To(Equal(http.StatusOK))
			resp3.Body.Close()

			By("step 4: check leader scheduler/load shows stale decode load remains (load != 0)")
			// give leader some time to detect worker1 disconnection
			time.Sleep(3 * time.Second)
			loadResp, loadErr := leaderClient.GetSchedulerLoad()
			Expect(loadErr).NotTo(HaveOccurred())
			allInstanceLoads := parseLoadResponse(loadResp)
			Expect(len(allInstanceLoads)).To(BeNumerically(">", 0), "should have decode loads")
			hasStaleLoad := false
			for id, load := range allInstanceLoads {
				GinkgoWriter.Printf("[load after force kill] instance %s load = %f\n", id, load)
				if load > 0 {
					hasStaleLoad = true
				}
			}
			Expect(hasStaleLoad).To(BeTrue(), "force kill should leave stale decode load > 0")

			By("step 5: restart worker1")
			worker1Cfg := InstanceConfigCtx{
				httpPort:      worker1HTTPPort,
				name:          "worker1",
				targetCfgDir:  filepath.Dir(worker1CfgDir),
				prefillsStr:   fmt.Sprintf("prefill1#127.0.0.1:%s,prefill2#127.0.0.1:%s", prefill1Port, prefill2Port),
				decodesStr:    fmt.Sprintf("decode1#127.0.0.1:%s", decodePort),
				etcdEndpoints: fmt.Sprintf("127.0.0.1:%s", etcdClientPort),
				isLeader:      false,
			}
			deployPath := filepath.Join(projectRoot, "deploy")
			newWorker1 := SetupRouteServerConfig(worker1Cfg, deployPath, targetTemplateFile, "PD", "pipeline")
			workerInstances[0] = newWorker1
			for i, inst := range allInstances {
				if inst.Name == "worker1" {
					allInstances[i] = newWorker1
					break
				}
			}

			By("step 5b: check worker1 log shows cluster state restored")
			Eventually(newWorker1.Session.Out, 10*time.Second).Should(gbytes.Say(
				"ClusterState Changes: state restored"))

			By("step 6: check worker1 readiness returns 200")
			w1Client := NewRSClient("http://localhost:" + worker1HTTPPort)
			Eventually(func() bool {
				ready, msg, err := w1Client.GetWorkerReadiness()
				if err != nil {
					return false
				}
				GinkgoWriter.Printf("[worker1] readiness after restart: ready=%v, msg=%s\n", ready, msg)
				return ready
			}, 30*time.Second, 2*time.Second).Should(BeTrue(), "worker1 should be ready after restart")

			By("step 6a: send request to worker1, should trigger bootId cleanup")
			reqId6a := "force-kill-req-w2-after"
			resp6a, err6a := sendFastInferRequest(worker1Client, reqId6a)
			Expect(err6a).NotTo(HaveOccurred())
			Expect(resp6a.StatusCode).To(Equal(http.StatusOK))
			resp6a.Body.Close()

			By("step 6b: check leader log shows bootId cleanup message")
			Eventually(leaderSession.Out, 10*time.Second).Should(gbytes.Say(
				`worker \[worker1\] bootId changed, cleaning up.*stale request loads`))

			// recreate worker1Client since worker1 restarted with same port but different process
			newWorker1Client := NewRSClient("http://localhost:" + worker1HTTPPort)

			By("step 7: send infer request to restarted worker1, should succeed")
			reqId4 := "force-kill-req-w1-restarted"
			resp4, err4 := sendFastInferRequest(newWorker1Client, reqId4)
			Expect(err4).NotTo(HaveOccurred())
			Expect(resp4.StatusCode).To(Equal(http.StatusOK))
			resp4.Body.Close()

			By("step 7b: check leader log shows remote schedule to worker1 after restart")
			Eventually(leaderSession.Out, 10*time.Second).Should(gbytes.Say(
				fmt.Sprintf(`remote schedule for .* req \[chatcmpl-%s-`, reqId4)))

			By("step 8: wait for bootId cleanup, check all decode loads are 0")
			// wait for leader to complete bootId load cleanup
			time.Sleep(5 * time.Second)
			loadResp2, loadErr2 := leaderClient.GetSchedulerLoad()
			Expect(loadErr2).NotTo(HaveOccurred())
			allInstanceLoads2 := parseLoadResponse(loadResp2)
			for id, load := range allInstanceLoads2 {
				GinkgoWriter.Printf("[load after restart] instance %s load = %f\n", id, load)
				Expect(load).To(BeNumerically("==", 0), "decode load for %s should be 0 after bootId cleanup", id)
			}
		})

		It("test force kill to clean automatically", func() {
			worker1 := workerInstances[0]
			leaderSession := leaderInstances[0].Session
			By("step 1: check worker1 readiness returns 200")
			w1Client := NewRSClient("http://localhost:" + worker1HTTPPort)
			Eventually(func() bool {
				ready, msg, err := w1Client.GetWorkerReadiness()
				if err != nil {
					return false
				}
				GinkgoWriter.Printf("[worker1] readiness after restart: ready=%v, msg=%s\n", ready, msg)
				return ready
			}, 30*time.Second, 2*time.Second).Should(BeTrue(), "worker1 should be ready after restart")

			By("step 2: send long req to worker1")
			longReqSent := make(chan struct{}, 1)
			reqId1 := "long_req_before_kill_worker_1"
			go func() {
				defer GinkgoRecover()
				resp1, body1, err1 := sendLongInferRequest(worker1Client, reqId1)
				if resp1 != nil {
					GinkgoWriter.Printf("[long request] worker1 stream resp status=%d, body_len=%d, err=%v\n",
						resp1.StatusCode, len(body1), err1)
					resp1.Body.Close()
				}
				close(longReqSent)
			}()
			time.Sleep(1 * time.Second)

			By("step 3: force kill worker1 with SIGKILL")
			worker1.Session.Kill().Wait(5 * time.Second)
			GinkgoWriter.Printf("[test] worker1 (%s) force killed\n", worker1.Name)

			By("step 4: check leader scheduler/load shows stale decode load remains (load != 0)")
			// give leader some time to detect worker1 disconnection
			time.Sleep(3 * time.Second)
			loadResp, loadErr := leaderClient.GetSchedulerLoad()
			Expect(loadErr).NotTo(HaveOccurred())
			allInstanceLoads := parseLoadResponse(loadResp)
			Expect(len(allInstanceLoads)).To(BeNumerically(">", 0), "should have decode loads")
			hasStaleLoad := false
			for id, load := range allInstanceLoads {
				GinkgoWriter.Printf("[load after force kill] instance %s load = %f\n", id, load)
				if load > 0 {
					hasStaleLoad = true
				}
			}
			Expect(hasStaleLoad).To(BeTrue(), "force kill should leave stale decode load > 0")

			By("step 5: should see clean up")
			Eventually(leaderSession.Out, 30*time.Second).Should(gbytes.Say(
				`detect leave peers not sync event at least .*, cleaning up these peers .*worker1.*`))

			By("step 5b: check leader scheduler/load shows all decode loads are 0 (graceful cleanup)")
			time.Sleep(1 * time.Second)
			loadResp, loadErr = leaderClient.GetSchedulerLoad()
			Expect(loadErr).NotTo(HaveOccurred())
			allInstanceLoads = parseLoadResponse(loadResp)
			for id, load := range allInstanceLoads {
				GinkgoWriter.Printf("[load] instance %s load = %f\n", id, load)
				Expect(load).To(BeNumerically("==", 0), "decode load for %s should be 0 after graceful cleanup", id)
			}
		})

		It("test worker disconnect with leader", func() {
			if !AllowIptables() {
				Skip(fmt.Sprintf("%s not set to true, skip iptables-based test", ROUTER_TEST_ENABLE_SU_ENV))
			}
			worker1CfgDir := workerInstances[0].CfgDir
			leaderSession := leaderInstances[0].Session

			By("step 1: restart worker1")
			workerInstances[0].Session.Kill().Wait(5 * time.Second)
			GinkgoWriter.Printf("[test] worker1 (%s) force killed\n", workerInstances[0].Name)

			worker1Cfg := InstanceConfigCtx{
				httpPort:      worker1HTTPPort,
				name:          "worker1",
				targetCfgDir:  filepath.Dir(worker1CfgDir),
				prefillsStr:   fmt.Sprintf("prefill1#127.0.0.1:%s,prefill2#127.0.0.1:%s", prefill1Port, prefill2Port),
				decodesStr:    fmt.Sprintf("decode1#127.0.0.1:%s", decodePort),
				etcdEndpoints: fmt.Sprintf("127.0.0.1:%s", etcdClientPort),
				isLeader:      false,
			}
			deployPath := filepath.Join(projectRoot, "deploy")
			newWorker1 := SetupRouteServerConfig(worker1Cfg, deployPath, targetTemplateFile, "PD", "pipeline")
			workerInstances[0] = newWorker1
			for i, inst := range allInstances {
				if inst.Name == "worker1" {
					allInstances[i] = newWorker1
					break
				}
			}

			By("step 2: check worker1 readiness returns 200")
			w1Client := NewRSClient("http://localhost:" + worker1HTTPPort)
			Eventually(func() bool {
				ready, msg, err := w1Client.GetWorkerReadiness()
				if err != nil {
					return false
				}
				GinkgoWriter.Printf("[worker1] readiness after restart: ready=%v, msg=%s\n", ready, msg)
				return ready
			}, 30*time.Second, 2*time.Second).Should(BeTrue(), "worker1 should be ready after restart")

			By("step 3: block all traffic to leader's HTTP port")
			err := BlockTrafficToPort(leader1HTTPPort)
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(func() {
				UnblockTrafficToPort(leader1HTTPPort)
			})
			GinkgoWriter.Printf("[test] blocked all traffic to leader port %s\n", leader1HTTPPort)

			By("step 4: verify fallback on leader failure")
			reqId := "disconnect-test-req-w2"
			resp, err2 := sendFastInferRequest(worker1Client, reqId)
			Expect(err2).NotTo(HaveOccurred())
			Expect(resp.StatusCode).To(Equal(http.StatusOK))
			resp.Body.Close()
			Eventually(newWorker1.Session.Out, 2*time.Second).Should(gbytes.Say("Fallback sched"))

			By("step 5: check leader detects worker1 is unreachable but alive")
			Eventually(leaderSession.Out, 30*time.Second).Should(gbytes.Say(
				`The unreachable peer \[worker1\] .* is alive, skip this discarding check next time`))
		})

		It("test worker with no leader", func() {
			worker1CfgDir := workerInstances[0].CfgDir
			leaderSession := leaderInstances[0].Session

			By("step 1: restart worker1")

			workerInstances[0].Session.Kill().Wait(5 * time.Second)
			GinkgoWriter.Printf("[test] worker1 (%s) force killed\n", workerInstances[0].Name)

			worker1Cfg := InstanceConfigCtx{
				httpPort:      worker1HTTPPort,
				name:          "worker1",
				targetCfgDir:  filepath.Dir(worker1CfgDir),
				prefillsStr:   fmt.Sprintf("prefill1#127.0.0.1:%s,prefill2#127.0.0.1:%s", prefill1Port, prefill2Port),
				decodesStr:    fmt.Sprintf("decode1#127.0.0.1:%s", decodePort),
				etcdEndpoints: fmt.Sprintf("127.0.0.1:%s", etcdClientPort),
				isLeader:      false,
			}
			deployPath := filepath.Join(projectRoot, "deploy")
			newWorker1 := SetupRouteServerConfig(worker1Cfg, deployPath, targetTemplateFile, "PD", "pipeline")
			workerInstances[0] = newWorker1
			for i, inst := range allInstances {
				if inst.Name == "worker1" {
					allInstances[i] = newWorker1
					break
				}
			}

			By("step 2: check worker1 readiness returns 200")
			w1Client := NewRSClient("http://localhost:" + worker1HTTPPort)
			Eventually(func() bool {
				ready, msg, err := w1Client.GetWorkerReadiness()
				if err != nil {
					return false
				}
				GinkgoWriter.Printf("[worker1] readiness after restart: ready=%v, msg=%s\n", ready, msg)
				return ready
			}, 30*time.Second, 2*time.Second).Should(BeTrue(), "worker1 should be ready after restart")

			By("step 3: force kill leader1 with SIGKILL")
			leaderSession.Kill().Wait(5 * time.Second)
			GinkgoWriter.Printf("[test] leader1 (%s) force killed\n", leaderInstances[0].Name)

			By("step 6: record initial metrics before fallback")
			w1MetricsClient := NewRSClient("http://localhost:" + worker1HTTPPort)
			initialFallback, initBody, initErr := w1MetricsClient.GetMetricsValue("rs:num_fallback_scheduled", "")
			GinkgoWriter.Printf("[metrics] initial rs:num_fallback_scheduled = %f (body: %s)\n", initialFallback, initBody)
			Expect(initErr).NotTo(HaveOccurred())
			initialEventsRemaining, initEventsBody, initEventsErr := w1MetricsClient.GetMetricsValue("rs:number_events_remaining", "")
			GinkgoWriter.Printf("[metrics] initial rs:number_events_remaining = %f (body: %s)\n", initialEventsRemaining, initEventsBody)
			Expect(initEventsErr).NotTo(HaveOccurred())
			initialFailure500, initFailBody, initFailErr := w1MetricsClient.GetMetricsValue("rs:infer_requests_failure_status_total", `status_code="500"`)
			GinkgoWriter.Printf("[metrics] initial rs:infer_requests_failure_status_total{status_code=500} = %f (body: %s)\n", initialFailure500, initFailBody)
			// should be zero when start up
			Expect(initFailErr).To(HaveOccurred())

			By("step 6: fallback 2 times in 10s, will NOT trigger worker local blacklist")
			// we have changed failure request time window to 10sec in this test
			// 1. curl mock error to decode1, GET /mock_error with body '{"code": 500}'
			mockErrorURL := fmt.Sprintf("http://127.0.0.1:%s/mock_error", decodePort)
			errorBody := strings.NewReader(`{"code": 500}`)
			mockErrResp, mockErr := http.Post(mockErrorURL, "application/json", errorBody)
			Expect(mockErr).NotTo(HaveOccurred())
			mockErrResp.Body.Close()
			GinkgoWriter.Printf("[mock] set decode1 to error mode\n")

			// 2. send 2 short requests concurrently (will trigger fallback since leader is blocked)
			var wg sync.WaitGroup
			for i := 0; i < 2; i++ {
				wg.Add(1)
				go func(idx int) {
					defer wg.Done()
					defer GinkgoRecover()
					reqId := fmt.Sprintf("disconnect-fallback-2-%d", idx)
					resp, err := sendFastInferRequest(worker1Client, reqId)
					if err != nil {
						GinkgoWriter.Printf("[fallback req %s] error: %v\n", reqId, err)
						return
					}
					resp.Body.Close()
				}(i)
			}
			wg.Wait()

			// 3. worker log should NOT output "added to the blacklist" in 10s (threshold is 3 failures)
			Consistently(newWorker1.Session.Out, 10*time.Second).ShouldNot(gbytes.Say(
				`added to the blacklist`))

			// 4. verify rs:num_fallback_scheduled increased by 2
			after2Fallback, _, _ := w1MetricsClient.GetMetricsValue("rs:num_fallback_scheduled", "")
			GinkgoWriter.Printf("[metrics] after 2 fallbacks, rs:num_fallback_scheduled = %f (delta = %f)\n",
				after2Fallback, after2Fallback-initialFallback)
			Expect(after2Fallback-initialFallback).To(BeNumerically("==", 4),
				"rs:num_fallback_scheduled should increase by 5 after 2 fallback requests")

			By("step 7: fallback 3 times in 10s, WILL trigger worker local blacklist")
			// 1. send 3 short requests concurrently
			for i := 0; i < 3; i++ {
				wg.Add(1)
				go func(idx int) {
					defer wg.Done()
					defer GinkgoRecover()
					reqId := fmt.Sprintf("disconnect-fallback-3-%d", idx)
					resp, err := sendFastInferRequest(worker1Client, reqId)
					if err != nil {
						GinkgoWriter.Printf("[fallback req %s] error: %v\n", reqId, err)
						return
					}
					resp.Body.Close()
				}(i)
			}
			wg.Wait()

			// 2. worker log should output blacklist message in 10s
			Eventually(newWorker1.Session.Out, 10*time.Second).Should(gbytes.Say(
				`\[fallback health check\] Instance decode1 detects .* failures in last .* ms, added to the blacklist`))

			// 3. verify rs:num_fallback_scheduled increased by 3 (cumulative +5)
			after5Fallback, _, _ := w1MetricsClient.GetMetricsValue("rs:num_fallback_scheduled", "")
			GinkgoWriter.Printf("[metrics] after total 5 fallbacks, rs:num_fallback_scheduled = %f (cumulative delta = %f)\n",
				after5Fallback, after5Fallback-initialFallback)
			Expect(after5Fallback-initialFallback).To(BeNumerically("==", 10),
				"rs:num_fallback_scheduled should increase by 10 after total 5 fallback requests")

			// 4. verify rs:number_events_remaining increased by 20 (5 requests * 4 events per request)
			time.Sleep(2 * time.Second)
			afterEventsRemaining, _, _ := w1MetricsClient.GetMetricsValue("rs:number_events_remaining", "")
			GinkgoWriter.Printf("[metrics] after 5 fallbacks, rs:number_events_remaining = %f (delta = %f)\n",
				afterEventsRemaining, afterEventsRemaining-initialEventsRemaining)
			Expect(afterEventsRemaining-initialEventsRemaining).To(BeNumerically("==", 20),
				"rs:number_events_remaining should increase by 20 (5 requests * 4 events per request)")

			// 5. verify rs:infer_requests_failure_status_total with status_code=500 increased by 5
			afterFailure500, _, _ := w1MetricsClient.GetMetricsValue("rs:infer_requests_failure_status_total", `status_code="500"`)
			GinkgoWriter.Printf("[metrics] after 5 fallbacks, rs:infer_requests_failure_status_total{status_code=500} = %f (delta = %f)\n",
				afterFailure500, afterFailure500-initialFailure500)
			Expect(afterFailure500-initialFailure500).To(BeNumerically("==", 5),
				"rs:infer_requests_failure_status_total with status_code=500 should increase by 5 after 5 failed fallback requests")

			Eventually(newWorker1.Session.Out, 10*time.Second).Should(gbytes.Say(
				`\[fallback health check\] Instance decode1 is resumed from the blacklist after`))
		})
	})
})

// sessionsFromInstances extracts gexec.Session slice from rsInstance slice.
func sessionsFromInstances(instances []rsInstance) []*gexec.Session {
	var sessions []*gexec.Session
	for _, inst := range instances {
		sessions = append(sessions, inst.Session)
	}
	return sessions
}

func backgroundCtx() context.Context {
	return context.Background()
}
