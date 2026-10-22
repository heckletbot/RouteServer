package route_server

import (
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/goccy/go-yaml"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gbytes"
	"github.com/onsi/gomega/gexec"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/server/v3/embed"
	commoncfg "infer-dp-scheduler/pkg/common/config"
)

const instanceKillTestCtx = "instance-kill-test"

var _ = Describe(instanceKillTestCtx, Ordered, func() {
	var (
		leaderInstances []rsInstance
		workerInstances []rsInstance
		allInstances    []rsInstance
		mockServers     []*gexec.Session
		etcdServer      *embed.Etcd
		etcdClient      *clientv3.Client

		leaderClient  *RSClient
		worker1Client *RSClient

		mockPrefill1Session *gexec.Session

		deployPath         string
		testCtxDir         string
		targetTemplateFile string
	)

	const (
		leader1HTTPPort = "30400"
		leader1OpsPort  = "30410"
		worker1HTTPPort = "30401"
		worker1OpsPort  = "30411"
		worker2HTTPPort = "30402"
		worker2OpsPort  = "30412"
		prefill1Port    = "30403"
		prefill2Port    = "30404"
		decodePort      = "30405"

		rsLeaderKey    = "/rs/leader"
		rsPeersAllDir  = "/rs/cluster/state/peers/all/"
		etcdClientPort = "30406"
		etcdPeerPort   = "30407"
	)

	BeforeAll(func() {
		var err error

		deployPath = filepath.Join(projectRoot, "deploy")
		testCtxDir = filepath.Join(projectRoot, "e2etest", "environment", instanceKillTestCtx)
		Expect(os.MkdirAll(testCtxDir, 0755)).To(BeNil())
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
		mockPrefill1Session = mockServers[0]
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
			workerClient := NewRSClient(fmt.Sprintf("http://localhost:%s", w.HTTPPort))
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

	BeforeEach(func() {
		Eventually(leaderInstances[0].Session.Out).Should(gbytes.Say(".*"))
		for _, w := range workerInstances {
			Eventually(w.Session.Out).Should(gbytes.Say(".*"))
		}
	})

	Context("P/D instance cleanup test", func() {
		var clientRs *RSClient
		var leaderClientForPD *RSClient

		BeforeEach(func() {
			clientRs = worker1Client
			leaderClientForPD = leaderClient
		})

		It("in PD scenario, killed P instance should be cleaned up via status API", func() {
			By("kill prefill1 instance (simulate complete unavailability)")
			mockPrefill1Session.Kill()
			mockPrefill1Session.Wait()

			By("wait for status API to show P instance in fault state")
			var statusResp map[string]interface{}
			Eventually(func() bool {
				resp, err := clientRs.GetStatus()
				if err != nil {
					GinkgoWriter.Printf("[status check] GetStatus error: %v\n", err)
					return false
				}
				err = ParseResponse(resp, &statusResp)
				resp.Body.Close()
				if err != nil || statusResp == nil {
					GinkgoWriter.Printf("[status check] ParseResponse error: %v, statusResp: %v\n", err, statusResp)
					return false
				}
				GinkgoWriter.Printf("[status check] current status response: %+v\n", statusResp)
				prefills, ok := statusResp["prefills"]
				if !ok {
					GinkgoWriter.Printf("[status check] no prefills field in response\n")
					return false
				}
				prefillList, ok := prefills.([]interface{})
				if !ok {
					GinkgoWriter.Printf("[status check] prefills is not array type\n")
					return false
				}
				for _, p := range prefillList {
					pInfo, ok := p.(map[string]interface{})
					if !ok {
						continue
					}
					if pInfo["url"] == fmt.Sprintf("http://127.0.0.1:%s", prefill1Port) {
						GinkgoWriter.Printf("[status check] prefill1 found at url: %v\n", pInfo["url"])
						return true
					}
				}
				GinkgoWriter.Printf("[status check] prefill1 not found yet\n")
				return false
			}, 60*time.Second, 5*time.Second).Should(BeTrue(), "status API should show P instance in fault state")

			By("wait 45s then check status API to verify faulty P instance is cleaned up")
			time.Sleep(45 * time.Second)

			By("check status API to confirm P instance has been cleaned up")
			Eventually(func() bool {
				resp, err := clientRs.GetStatus()
				if err != nil {
					GinkgoWriter.Printf("[status check] GetStatus error: %v\n", err)
					return false
				}
				err = ParseResponse(resp, &statusResp)
				resp.Body.Close()
				if err != nil || statusResp == nil {
					GinkgoWriter.Printf("[status check] ParseResponse error: %v, statusResp: %v\n", err, statusResp)
					return false
				}
				GinkgoWriter.Printf("[status check] current status response: %+v\n", statusResp)
				prefills, ok := statusResp["prefills"]
				if !ok {
					GinkgoWriter.Printf("[status check] no prefills field in response\n")
					return true
				}
				prefillList, ok := prefills.([]interface{})
				if !ok {
					GinkgoWriter.Printf("[status check] prefills is not array type\n")
					return true
				}
				for _, p := range prefillList {
					pInfo, ok := p.(map[string]interface{})
					if !ok {
						continue
					}
					if pInfo["url"] == fmt.Sprintf("http://127.0.0.1:%s", prefill1Port) {
						GinkgoWriter.Printf("[status check] prefill1 still present at url: %v\n", pInfo["url"])
						return false
					}
				}
				GinkgoWriter.Printf("[status check] prefill1 not found, instance cleaned up\n")
				return true
			}, 60*time.Second, 5*time.Second).Should(BeTrue(), "faulty instance should be cleaned up in PD scenario")

			By("check leader internal/metrics API to verify rs:num_discarded_unreachable_infer_endpoints metric")
			Eventually(func() bool {
				resp, err := leaderClientForPD.GetInternalMetrics()
				if err != nil {
					if resp != nil {
						resp.Body.Close()
					}
					return false
				}
				body, _ := io.ReadAll(resp.Body)
				resp.Body.Close()

				re := regexp.MustCompile(`rs:num_discarded_unreachable_infer_endpoints\{[^}]*\}\s+([0-9.]+)`)
				matches := re.FindStringSubmatch(string(body))
				if len(matches) < 2 {
					GinkgoWriter.Printf("[metrics] rs:num_discarded_unreachable_infer_endpoints not found or value is 0\n")
					return false
				}
				var val float64
				fmt.Sscanf(matches[1], "%f", &val)
				GinkgoWriter.Printf("[metrics] rs:num_discarded_unreachable_infer_endpoints = %f\n", val)
				return val > 0
			}, 30*time.Second, 2*time.Second).Should(BeTrue(), "rs:num_discarded_unreachable_infer_endpoints in leader metrics should be greater than 0")

			By("in PD scenario, status API should clean up faulty P instance and metrics should increase")
		})
	})

})
