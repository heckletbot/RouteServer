package route_server

// ==========================
// test 5.7.8
// ==========================

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/goccy/go-yaml"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gbytes"
	"github.com/onsi/gomega/gexec"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/server/v3/embed"

	"infer-dp-scheduler/cmd/route_server/server/options"
	commoncfg "infer-dp-scheduler/pkg/common/config"
	"infer-dp-scheduler/pkg/common/constant"
	"infer-dp-scheduler/pkg/common/openai"
)

const (
	pdBlacklistCtx = "pd-blacklist-xpyd-test"
)

var _ = Describe(pdBlacklistCtx, Ordered, func() {
	const (
		candidate1 = "candidate1"
		candidate2 = "candidate2"
		worker1    = "worker1"
		worker2    = "worker2"

		blacklistPrefill1ID = "prefill1"
		blacklistDecode1ID  = "decode1"
		blacklistDecode2ID  = "decode2"
	)

	const (
		etcdListenPort = iota + 30800
		etcdPeerPort
		etcdCandidate1ProxyPort
		etcdCandidate2ProxyPort

		prefill1Port
		decode1Port
		decode2Port
		proxyPrefill1Port
		proxyDecode1Port
		proxyDecode2Port

		candidate1HTTPPort
		candidate1OpsPort
		candidate2HTTPPort
		candidate2OpsPort
		worker1HTTPPort
		worker1OpsPort
		worker2HTTPPort
		worker2OpsPort
	)

	var (
		candidate1Session *gexec.Session
		candidate2Session *gexec.Session
		worker1Session    *gexec.Session
		worker2Session    *gexec.Session

		mockServers []*gexec.Session
		etcdServer  *embed.Etcd
		etcdClient  *clientv3.Client

		instanceProxyRules = make(map[string]*proxyRuleEntry)
		etcdProxyRules     = make(map[string]*etcdProxyRule)

		// short aging time for testing: 10s valid, 0.1s window
		linkBlockTagValidTimeMs int64 = 10000
	)

	BeforeAll(func() {
		var err error
		By("ensure ports are free")
		ports := []string{}
		for i := etcdListenPort; i <= worker2HTTPPort; i++ {
			ports = append(ports, strconv.Itoa(i))
		}
		EnsurePorts(ports)
		time.Sleep(500 * time.Millisecond)

		By("start embedded etcd via embed.StartEtcd on port " + strconv.Itoa(etcdListenPort))
		etcdCfg := embed.NewConfig()
		etcdCfg.Name = "test-etcd"
		etcdCfg.Dir = filepath.Join(os.TempDir(), fmt.Sprintf("etcd-test-%d", time.Now().UnixNano()))
		etcdCfg.ListenPeerUrls = []url.URL{{Scheme: "http", Host: fmt.Sprintf("127.0.0.1:%v", etcdPeerPort)}}
		etcdCfg.ListenClientUrls = []url.URL{{Scheme: "http", Host: fmt.Sprintf("127.0.0.1:%v", etcdListenPort)}}
		etcdCfg.AdvertisePeerUrls = []url.URL{{Scheme: "http", Host: fmt.Sprintf("127.0.0.1:%v", etcdPeerPort)}}
		etcdCfg.AdvertiseClientUrls = []url.URL{{Scheme: "http", Host: fmt.Sprintf("127.0.0.1:%v", etcdListenPort)}}
		etcdCfg.InitialCluster = fmt.Sprintf("test-etcd=http://127.0.0.1:%v", etcdPeerPort)
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

		By("create etcd proxy")
		candidate1ProxyOption := new(etcdProxyRule)
		candidate2ProxyOption := new(etcdProxyRule)
		etcdProxyRules[candidate1], etcdProxyRules[candidate2] = candidate1ProxyOption, candidate2ProxyOption
		go StartTCPProxy(fmt.Sprintf("127.0.0.1:%d", etcdCandidate1ProxyPort), fmt.Sprintf("127.0.0.1:%d", etcdListenPort), candidate1ProxyOption)
		go StartTCPProxy(fmt.Sprintf("127.0.0.1:%d", etcdCandidate2ProxyPort), fmt.Sprintf("127.0.0.1:%d", etcdListenPort), candidate2ProxyOption)

		By("create client connection to etcd")
		etcdClient, err = clientv3.New(clientv3.Config{
			Endpoints:   []string{fmt.Sprintf("127.0.0.1:%d", etcdListenPort)},
			DialTimeout: 5 * time.Second,
		})
		Expect(err).NotTo(HaveOccurred())

		By("read template config and modify blacklist aging parameters for testing")
		deployPath := filepath.Join(projectRoot, "deploy")
		var cfg commoncfg.Config
		templateContent, err := os.ReadFile(filepath.Join(deployPath, ROUTE_SERVER_TEMPLATE))
		Expect(yaml.Unmarshal(templateContent, &cfg)).To(BeNil())
		cfg.ServerConfig.RateLimitCfg = &commoncfg.RateLimitCfg{
			NonReferenceAPIs: &commoncfg.NonReferenceAPIRateLimit{
				Enabled:                false,
				RequestsPerMinute:      10000,
				CleanupIntervalMinutes: 5,
			},
		}
		// accelerate health check
		cfg.InstanceManagerCfg.HealthCheckConfig.RestHealthCommonConfig.Interval = 2000
		cfg.InstanceManagerCfg.HealthCheckConfig.UnavailableCheckersCfg.RestHealthCfg.ConsecutiveTime = 3
		// blacklist config: shorten window and valid time for testing
		cfg.InstanceManagerCfg.HealthCheckConfig.AvailableCheckersCfg.LinkBlacklistConfig.LinkBlockWindowsMs = 100
		cfg.InstanceManagerCfg.HealthCheckConfig.AvailableCheckersCfg.LinkBlacklistConfig.LinkBlockTagValidTimeMs = 10000
		// instance-block-ratio = 70: 1 blocked decode / 2 total = 50% < 70%, prefill NOT isolated
		cfg.InstanceManagerCfg.HealthCheckConfig.AvailableCheckersCfg.LinkBlacklistConfig.InstanceBlockRatio = 70
		tempTemplatePath := filepath.Join(deployPath, TEMP_TEMPLATE)
		modifiedYaml, err := yaml.Marshal(&cfg)
		Expect(err).To(BeNil())
		Expect(os.WriteFile(tempTemplatePath, modifiedYaml, 0644)).To(BeNil())

		DeferCleanup(func() {
			os.Remove(tempTemplatePath)
		})

		By("create rs config paths")
		etcdCtxDir := filepath.Join(projectRoot, "e2etest", "environment", pdBlacklistCtx)
		Expect(os.RemoveAll(etcdCtxDir)).To(BeNil())
		candidate1CfgDir := filepath.Join(etcdCtxDir, candidate1)
		candidate2CfgDir := filepath.Join(etcdCtxDir, candidate2)
		worker1CfgDir := filepath.Join(etcdCtxDir, worker1)
		worker2CfgDir := filepath.Join(etcdCtxDir, worker2)
		Expect(os.MkdirAll(filepath.Join(candidate1CfgDir, "config", "pipelines"), 0755)).To(BeNil())
		Expect(os.MkdirAll(filepath.Join(candidate2CfgDir, "config", "pipelines"), 0755)).To(BeNil())
		Expect(os.MkdirAll(filepath.Join(worker1CfgDir, "config", "pipelines"), 0755)).To(BeNil())
		Expect(os.MkdirAll(filepath.Join(worker2CfgDir, "config", "pipelines"), 0755)).To(BeNil())

		By("copy pipelines config")
		Expect(copyDir(filepath.Join(deployPath, "pipelines"), filepath.Join(candidate1CfgDir, "config", "pipelines"))).To(BeNil())
		Expect(copyDir(filepath.Join(deployPath, "pipelines"), filepath.Join(candidate2CfgDir, "config", "pipelines"))).To(BeNil())
		Expect(copyDir(filepath.Join(deployPath, "pipelines"), filepath.Join(worker1CfgDir, "config", "pipelines"))).To(BeNil())
		Expect(copyDir(filepath.Join(deployPath, "pipelines"), filepath.Join(worker2CfgDir, "config", "pipelines"))).To(BeNil())

		// 1p2d: 1 prefill + 2 decode
		By("generate node configs using temp template (1p2d scenario)")
		decodeStr := fmt.Sprintf("decode1#127.0.0.1:%d,decode2#127.0.0.1:%d", proxyDecode1Port, proxyDecode2Port)
		prefillStr := fmt.Sprintf("prefill1#127.0.0.1:%d", proxyPrefill1Port)

		candidate1ToolParam := DisableHTTPSConfig([]string{
			"-template", tempTemplatePath,
			"-host", "127.0.0.1",
			"-target", candidate1CfgDir,
			"-infer-mode", "PD",
			"-prefills", prefillStr,
			"-decodes", decodeStr,
			"-name", candidate1,
			"-remote-scheduler-type", "disable",
			"-port", strconv.Itoa(candidate1HTTPPort),
			"-candidate", "true",
			"-etcd-endpoints", fmt.Sprintf("127.0.0.1:%d", etcdCandidate1ProxyPort),
			"-infer-module-version", "pipeline",
			"-ops-port", strconv.Itoa(candidate1OpsPort),
		})
		Expect(exec.Command(configToolPath, candidate1ToolParam...).Run()).To(BeNil())

		candidate2ToolParam := DisableHTTPSConfig([]string{
			"-template", tempTemplatePath,
			"-host", "127.0.0.1",
			"-target", candidate2CfgDir,
			"-infer-mode", "PD",
			"-prefills", prefillStr,
			"-decodes", decodeStr,
			"-name", candidate2,
			"-remote-scheduler-type", "disable",
			"-port", strconv.Itoa(candidate2HTTPPort),
			"-candidate", "true",
			"-etcd-endpoints", fmt.Sprintf("127.0.0.1:%d", etcdCandidate2ProxyPort),
			"-infer-module-version", "pipeline",
			"-ops-port", strconv.Itoa(candidate2OpsPort),
		})
		Expect(exec.Command(configToolPath, candidate2ToolParam...).Run()).To(BeNil())

		worker1ToolParam := DisableHTTPSConfig([]string{
			"-template", tempTemplatePath,
			"-host", "127.0.0.1",
			"-target", worker1CfgDir,
			"-infer-mode", "PD",
			"-prefills", prefillStr,
			"-decodes", decodeStr,
			"-name", worker1,
			"-remote-scheduler-type", "leader",
			"-port", strconv.Itoa(worker1HTTPPort),
			"-candidate", "false",
			"-etcd-endpoints", fmt.Sprintf("127.0.0.1:%d", etcdListenPort),
			"-infer-module-version", "pipeline",
			"-ops-port", strconv.Itoa(worker1OpsPort),
		})
		Expect(exec.Command(configToolPath, worker1ToolParam...).Run()).To(BeNil())

		worker2ToolParam := DisableHTTPSConfig([]string{
			"-template", tempTemplatePath,
			"-host", "127.0.0.1",
			"-target", worker2CfgDir,
			"-infer-mode", "PD",
			"-prefills", prefillStr,
			"-decodes", decodeStr,
			"-name", worker2,
			"-remote-scheduler-type", "leader",
			"-port", strconv.Itoa(worker2HTTPPort),
			"-candidate", "false",
			"-etcd-endpoints", fmt.Sprintf("127.0.0.1:%d", etcdListenPort),
			"-infer-module-version", "pipeline",
			"-ops-port", strconv.Itoa(worker2OpsPort),
		})
		Expect(exec.Command(configToolPath, worker2ToolParam...).Run()).To(BeNil())

		By("initialize config and copy metrics config")
		configPath := filepath.Join(etcdCtxDir, candidate1, "config")
		Expect(os.Setenv(constant.EnvServerConfigPath, configPath)).To(BeNil())
		Expect(options.InitConfig()).To(BeNil())
		metricsCollectorConfigFileName := options.GetConfig().MetricsCfg.MetricsCollectorConfigFileName
		for _, dir := range []string{candidate1CfgDir, candidate2CfgDir, worker1CfgDir, worker2CfgDir} {
			Expect(copyFile(
				filepath.Join(deployPath, metricsCollectorConfigFileName),
				filepath.Join(dir, "config", metricsCollectorConfigFileName),
			)).To(BeNil())
		}

		DeferCleanup(func() {
			os.RemoveAll(path.Join(candidate1CfgDir, "config"))
			os.RemoveAll(path.Join(worker1CfgDir, "config"))
			os.RemoveAll(path.Join(candidate2CfgDir, "config"))
			os.RemoveAll(path.Join(worker2CfgDir, "config"))
		})

		By("modify recover health config - set consecutive-time to 1")
		candidate1ConfigFile := filepath.Join(candidate1CfgDir, "config", "route-server.yaml")
		Expect(InjectRecoverHealthConfigToYaml(candidate1ConfigFile, 5)).To(BeNil())
		candidate2ConfigFile := filepath.Join(candidate2CfgDir, "config", "route-server.yaml")
		Expect(InjectRecoverHealthConfigToYaml(candidate2ConfigFile, 5)).To(BeNil())

		By("start mock vllm instances (1 prefill + 2 decode)")
		mockInstances := []struct {
			port      int
			proxyPort int
			name      string
		}{
			{prefill1Port, proxyPrefill1Port, prefill1ID},
			{decode1Port, proxyDecode1Port, blacklistDecode1ID},
			{decode2Port, proxyDecode2Port, blacklistDecode2ID},
		}
		for _, inst := range mockInstances {
			params := []string{
				"-addr", "127.0.0.1",
				"-port", strconv.Itoa(inst.port),
				"-name", inst.name,
			}
			command := exec.Command(mockVLLMPath, params...)
			sess, startErr := gexec.Start(command, io.Discard, GinkgoWriter)
			Expect(startErr).NotTo(HaveOccurred())
			mockServers = append(mockServers, sess)
			GinkgoWriter.Printf("[mock] started %s on port %d\n", inst.name, inst.port)
			ruleEntry := new(proxyRuleEntry)
			instanceProxyRules[inst.name] = ruleEntry
			go startProxy(fmt.Sprintf(":%d", inst.proxyPort), fmt.Sprintf("http://localhost:%d", inst.port), ruleEntry)
		}
		time.Sleep(500 * time.Millisecond)

		By("start candidate route-server")
		Env := os.Environ()
		candidate1Env := append(Env, fmt.Sprintf("%s=%s",
			constant.EnvServerConfigPath, filepath.Join(candidate1CfgDir, "config")))
		command := exec.Command(rsPath)
		command.Env = candidate1Env
		candidate1Session, err = gexec.Start(command, io.Discard, GinkgoWriter)
		Expect(err).NotTo(HaveOccurred())

		candidate2Env := append(Env, fmt.Sprintf("%s=%s",
			constant.EnvServerConfigPath, filepath.Join(candidate2CfgDir, "config")))
		command = exec.Command(rsPath)
		command.Env = candidate2Env
		candidate2Session, err = gexec.Start(command, io.Discard, GinkgoWriter)
		Expect(err).NotTo(HaveOccurred())

		By("start worker route-server")
		worker1Env := append(Env, fmt.Sprintf("%s=%s",
			constant.EnvServerConfigPath, filepath.Join(worker1CfgDir, "config")))
		command = exec.Command(rsPath)
		command.Env = worker1Env
		worker1Session, err = gexec.Start(command, io.Discard, GinkgoWriter)
		Expect(err).NotTo(HaveOccurred())

		worker2Env := append(Env, fmt.Sprintf("%s=%s",
			constant.EnvServerConfigPath, filepath.Join(worker2CfgDir, "config")))
		command = exec.Command(rsPath)
		command.Env = worker2Env
		worker2Session, err = gexec.Start(command, io.Discard, GinkgoWriter)
		Expect(err).NotTo(HaveOccurred())

		By("wait for all servers to be ready (listen)")
		Eventually(candidate1Session.Out, 30*time.Second).Should(gbytes.Say("Start HTTP server on"))
		Eventually(candidate2Session.Out, 30*time.Second).Should(gbytes.Say("Start HTTP server on"))
		Eventually(worker1Session.Out, 30*time.Second).Should(gbytes.Say("Start HTTP server on"))
		Eventually(worker2Session.Out, 30*time.Second).Should(gbytes.Say("Start HTTP server on"))

		By("wait for leader election and worker registration via etcd")
		Eventually(func() bool {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			resp, getErr := etcdClient.Get(ctx, rsLeaderKey, clientv3.WithPrefix())
			if getErr != nil || len(resp.Kvs) == 0 {
				return false
			}
			for _, kv := range resp.Kvs {
				if strings.Contains(string(kv.Value), "candidate1") {
					return true
				}
			}
			return false
		}, 30*time.Second, 1*time.Second).Should(BeTrue(), "candidate1 should be elected as leader")

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

		By("wait for worker to sync health cache from leader via events")
		workerReadinessClient := &RSClient{BaseClient{BaseURL: "http://localhost:" + strconv.Itoa(worker1HTTPPort), HTTPClient: &http.Client{}}}
		Eventually(func() bool {
			_, msg, err := workerReadinessClient.GetWorkerReadiness()
			if err != nil {
				return false
			}
			return strings.Contains(msg, `"message":"ready"`)
		}, 60*time.Second, 2*time.Second).Should(BeTrue(), "worker health cache should be synced from leader")
	})

	BeforeEach(func() {
		for _, rule := range instanceProxyRules {
			rule.Clear()
		}
		for _, rule := range etcdProxyRules {
			rule.Clear()
		}
	})

	// ==========================
	// 1p2d blacklist test: roles reversed
	// 1/1 decode link blacklist/prefill num = 100%, one decode will be isolated, status changed to fault
	// 1/2 prefill link blacklist/decode num = 50% < 70%, decode NOT isolated.
	// Cover test_faulttest_019 verify pd link blacklist triggers instance isolation and recovery, 1p2d verify isolating d
	// Cover test_xpyd_rolesfault_031 | after single d fault, isolate the instance, new requests no longer dispatched to it
	// ==========================
	It("1p2d: after blacklisting a <p-d> link, prefill not isolated (50%<70%), d isolated, requests not routed to blacklisted d; after aging requests can reach d", func() {
		leaderClient := NewRSClient("http://localhost:" + strconv.Itoa(candidate1HTTPPort))
		workerClient := NewRSClient("http://localhost:" + strconv.Itoa(worker1HTTPPort))
		extraHeaders := make(map[string]string)
		reqBody := openai.ChatCompletionRequestV1{
			Model: "pangu",
			Messages: []openai.ChatCompletionMessage{{
				Role:    "user",
				Content: "hello",
			}},
			Stream: false,
		}

		By("step 1: verify initial blacklist is empty")
		Eventually(func() bool {
			blResp, err := leaderClient.GetLinkBlackInstances()
			if err != nil {
				return false
			}
			prefillList := blResp.InsBlackLists["prefill"]
			decodeList := blResp.InsBlackLists["decode"]
			return len(prefillList) == 0 && len(decodeList) == 0
		}, 15*time.Second, 1*time.Second).Should(BeTrue(), "initial blacklist should be empty")

		By("step 2: verify normal request succeeds")
		resp, err := workerClient.SendChatCompletion(TimeoutCtx(context.Background(), 30*time.Second), reqBody, extraHeaders)
		Expect(err).NotTo(HaveOccurred())
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		var result openai.ChatCompletionResponse
		Expect(ParseResponse(resp, &result)).NotTo(HaveOccurred())
		Expect(result.Choices).NotTo(BeEmpty())

		// Key: must use mock server's LinkDown API to make decode1 return PDLinkDownError,
		// not proxy blocking. Proxy blocking produces connection error (IsConnectionErr=true),
		// blacklist only handles events with IsKVTransferErr=true.
		By("step 3: use mock server LinkDown API to make decode1 return PDLinkDownError, triggering KV transfer error")
		mockDecode1Client := NewMockServerClient(strconv.Itoa(decode1Port))
		resp, err = mockDecode1Client.LinkDown("127.0.0.1", strconv.Itoa(decode1Port))
		bodyBytes, err := io.ReadAll(resp.Body)
		if err == nil {
			fmt.Printf("[1p2d-test] Set LinkDown Response Body: %s\n", string(bodyBytes))
			resp.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))
		}
		Expect(err).NotTo(HaveOccurred())

		By("step 4: trigger link-block-threshold 3 times consecutively to add <prefill-decode1> link to blacklist")
		for i := 0; i < 10; i++ {
			time.Sleep(100 * time.Millisecond)
			resp, err = workerClient.SendChatCompletion(TimeoutCtx(context.Background(), 5*time.Second), reqBody, extraHeaders)
			Expect(err).NotTo(HaveOccurred())
			resp.Body.Close()
		}

		By("step 5: verify <prefill-decode1> link has been added to blacklist via GetLinkBlackInstances")
		Eventually(func() bool {
			blResp, err := leaderClient.GetLinkBlackInstances()
			if err != nil {
				GinkgoWriter.Printf("[1p2d-test] GetLinkBlackInstances error: %v\n", err)
				return false
			}
			GinkgoWriter.Printf("[1p2d-test] blacklist response: %v\n", blResp)
			// check that prefill blacklist contains decode1
			prefillList := blResp.InsBlackLists["prefill"]
			GinkgoWriter.Printf("prefillList: %v", prefillList)
			for _, ins := range prefillList {
				for _, detail := range ins.LinkBlackList {
					if detail.InsId == blacklistDecode1ID {
						GinkgoWriter.Printf("[1p2d-test] prefill %s has blacklisted %s, remainingTime=%dms\n",
							ins.InsId, detail.InsId, detail.RemainingTimeMs)
						return true
					}
				}
			}
			return false
		}, 15*time.Second, 1*time.Second).Should(BeTrue(), "prefill1-decode1 should be in blacklist")

		Eventually(func() bool {
			blResp, err := leaderClient.GetLinkBlackInstances()
			if err != nil {
				GinkgoWriter.Printf("[1p2d-test] GetLinkBlackInstances error: %v\n", err)
				return false
			}
			GinkgoWriter.Printf("[1p2d-test] blacklist response: %v\n", blResp)
			// check that decode1 blacklist contains prefill1
			decodeList := blResp.InsBlackLists["decode"]
			GinkgoWriter.Printf("decodeList: %v", decodeList)
			for _, ins := range decodeList {
				for _, detail := range ins.LinkBlackList {
					if detail.InsId == blacklistPrefill1ID {
						GinkgoWriter.Printf("[1p2d-test] decode %s has blacklisted %s, remainingTime=%dms\n",
							ins.InsId, detail.InsId, detail.RemainingTimeMs)
						return true
					}
				}
			}
			return false
		}, 15*time.Second, 1*time.Second).Should(BeTrue(), "decode1-prefill1 should be in blacklist")

		// Verify blacklist details: prefill blacklist count 1 / decode total 2 = 50% < 70%, prefill1 not isolated
		// Verify blacklist details: decode blacklist count 1 / prefill total 1 = 100%, decode1 isolated, moved to unavailable
		blResp, err := leaderClient.GetLinkBlackInstances()
		Expect(err).NotTo(HaveOccurred())
		GinkgoWriter.Printf("[1p2d-test] blacklist details: totalHealthy=%v, insBlackLists=%v\n",
			blResp.TotalHealthyInstance, blResp.InsBlackLists)

		By("step 6: verify requests not routed to blacklisted decode1, only to decode2; and decode1 status is fault")
		decode1Scheduled := false
		for i := 0; i < 6; i++ {
			resp, err := workerClient.SendChatCompletion(TimeoutCtx(context.Background(), 30*time.Second), reqBody, extraHeaders)
			Expect(err).NotTo(HaveOccurred())
			if resp.StatusCode == http.StatusOK {
				Expect(ParseResponse(resp, &result)).NotTo(HaveOccurred())
				Expect(result.Choices).NotTo(BeEmpty())
			} else {
				bodyStr := ReadBodyToString(resp)
				GinkgoWriter.Printf("[1p2d-test] request %d returned non-200: status=%d, body=%s\n", i, resp.StatusCode, bodyStr)
			}
		}
		// check worker log to confirm no dispatch to decode1
		workerLog := string(worker1Session.Out.Contents())
		decode1DispatchStr := fmt.Sprintf("send request to decode instance [%s]", blacklistDecode1ID)
		if strings.Contains(workerLog, decode1DispatchStr) {
			decode1Scheduled = true
		}
		Expect(decode1Scheduled).To(BeFalse(), "decode1 should be blocked by blacklist, no requests dispatched to it")
		GinkgoWriter.Printf("[1p2d-test] verified: decode1 blocked by blacklist, requests only routed to decode2\n")

		// verify decode1 status is faulty via GetStatusJson
		Eventually(func() string {
			statusResp, err := leaderClient.GetStatusJson()
			if err != nil {
				return ""
			}
			decodeInstances := statusResp.InstanceStatus["decode"]
			for _, inst := range decodeInstances {
				if strings.Contains(inst.URL, fmt.Sprintf("127.0.0.1:%d", proxyDecode1Port)) {
					return inst.Status
				}
			}
			return ""
		}, 15*time.Second, 1*time.Second).Should(Equal(constant.FaultStatus), "decode1 should be in fault status after link down and blacklist")
		GinkgoWriter.Printf("[1p2d-test] verified: decode1 status is fault\n")

		// verify prefill status is running via GetStatusJson
		Eventually(func() string {
			statusResp, err := leaderClient.GetStatusJson()
			if err != nil {
				return ""
			}
			prefillInstances := statusResp.InstanceStatus["prefill"]
			for _, inst := range prefillInstances {
				if strings.Contains(inst.URL, fmt.Sprintf("127.0.0.1:%d", proxyPrefill1Port)) {
					return inst.Status
				}
			}
			return ""
		}, 15*time.Second, 1*time.Second).Should(Equal(constant.RunningStatus), "prefill1 should be running after blacklist (50% < 70%, not isolated)")
		GinkgoWriter.Printf("[1p2d-test] verified: prefill1 status is running\n")

		By("step 7: restore decode1 link status (LinkUp)")
		_, err = mockDecode1Client.LinkUp()
		Expect(err).NotTo(HaveOccurred())

		By("step 8: send 30 requests consecutively, confirm no dispatch to isolated Decode, log does not contain that Decode, and all return 200")
		blockDecodeId := decodeID
		GinkgoWriter.Printf("[1p2d-test] isolated Decode is [%v], non-isolated Decode is [%v]:\n", blockDecodeId, "decode2") // record current log length, only check new entries later
		logOffset := len(worker1Session.Out.Contents())
		allOK := true
		for i := 0; i < 30; i++ {
			resp, err := workerClient.SendChatCompletion(TimeoutCtx(context.Background(), 30*time.Second), reqBody, extraHeaders)
			if err != nil || resp.StatusCode != http.StatusOK {
				allOK = false
				GinkgoWriter.Printf("[1p2d-test] request %d failed: err=%v, status=%d\n", i, err, resp.StatusCode)
			}
			if resp.StatusCode == http.StatusOK {
				var chatResult openai.ChatCompletionResponse
				Expect(ParseResponse(resp, &chatResult)).NotTo(HaveOccurred())
				Expect(chatResult.Choices).NotTo(BeEmpty())
			} else {
				resp.Body.Close()
			}
		}
		Expect(allOK).To(BeTrue(), "30 requests should all return 200, isolated decode not participating in scheduling")
		// only check the new log entries from these 30 requests for the isolated prefill
		newWorkerLog := string(worker1Session.Out.Contents()[logOffset:])
		dispatchStr30 := fmt.Sprintf("send request to [decode] instance [%s]", blockDecodeId)
		Expect(strings.Contains(newWorkerLog, dispatchStr30)).To(BeFalse(), "new log entries from 30 requests should not contain dispatch to isolated %s", blockDecodeId)
		GinkgoWriter.Printf("[1p2d-test] verified: 30 requests all returned 200, no dispatch to %v in new log entries\n", blockDecodeId)

		By("step 9: wait for aging time (link-block-tag-valid-time-ms=10s), verify blacklist has been aged out")
		agingWait := time.Duration(linkBlockTagValidTimeMs)*time.Millisecond + 2*time.Second
		GinkgoWriter.Printf("[1p2d-test] waiting for aging: %v\n", agingWait)
		time.Sleep(agingWait)

		Eventually(func() bool {
			blResp, err := leaderClient.GetLinkBlackInstances()
			if err != nil {
				return false
			}
			// check that decode1 has disappeared from prefill blacklist
			prefillList := blResp.InsBlackLists["prefill"]
			for _, ins := range prefillList {
				for _, detail := range ins.LinkBlackList {
					if detail.InsId == blacklistDecode1ID {
						return false // if decode1 is still in blacklist
					}
				}
			}
			return true // if decode1 is not in blacklist
		}, 15*time.Second, 1*time.Second).Should(BeTrue(), "decode1 should be removed from blacklist after aging")
		GinkgoWriter.Printf("[1p2d-test] blacklist aged out, decode1 removed from blacklist\n")

		// verify decode1 recovers to running status after aging
		Eventually(func() string {
			statusResp, err := leaderClient.GetStatusJson()
			if err != nil {
				return ""
			}
			decodeInstances := statusResp.InstanceStatus["decode"]
			for _, inst := range decodeInstances {
				if strings.Contains(inst.URL, fmt.Sprintf("127.0.0.1:%d", proxyDecode1Port)) {
					return inst.Status
				}
			}
			return ""
		}, 30*time.Second, 2*time.Second).Should(Equal(constant.RunningStatus), "decode1 should be running after blacklist aging and recovery")
		GinkgoWriter.Printf("[1p2d-test] verified: decode1 status recovered to running\n")

		By("step 10: send 30 requests consecutively, confirm no dispatch to isolated decode, log does not contain that decode, and all return 200")
		GinkgoWriter.Printf("[1p2d-test] isolated decode is [%v], non-isolated decode is [%v]:\n", blockDecodeId, "decode2") // record current log length, only check new entries later
		logOffset = len(worker1Session.Out.Contents())
		allOK = true
		for i := 0; i < 30; i++ {
			resp, err := workerClient.SendChatCompletion(TimeoutCtx(context.Background(), 30*time.Second), reqBody, extraHeaders)
			if err != nil || resp.StatusCode != http.StatusOK {
				allOK = false
				GinkgoWriter.Printf("[1p2d-test] request %d failed: err=%v, status=%d\n", i, err, resp.StatusCode)
			}
			if resp.StatusCode == http.StatusOK {
				var chatResult openai.ChatCompletionResponse
				Expect(ParseResponse(resp, &chatResult)).NotTo(HaveOccurred())
				Expect(chatResult.Choices).NotTo(BeEmpty())
			} else {
				resp.Body.Close()
			}
		}
		Expect(allOK).To(BeTrue(), "30 requests should all return 200, isolated prefill not participating in scheduling")
		// only check the new log entries from these 30 requests for the isolated decode
		newWorkerLog = string(worker1Session.Out.Contents()[logOffset:])
		dispatchStr30 = fmt.Sprintf("send request to [prefill] instance [%s]", blockDecodeId)
		Expect(strings.Contains(newWorkerLog, dispatchStr30)).To(BeFalse(), "new log entries from 30 requests should not contain dispatch to isolated %s", blockDecodeId)
		GinkgoWriter.Printf("[1p2d-test] verified: 30 requests all returned 200, no dispatch to %v in new log entries\n", blockDecodeId)
	})

	AfterAll(func() {
		By("cleanup route-servers")
		if worker1Session != nil {
			worker1Session.Kill()
			worker1Session.Wait()
		}
		if worker2Session != nil {
			worker2Session.Kill()
			worker2Session.Wait()
		}
		if candidate1Session != nil {
			candidate1Session.Kill()
			candidate1Session.Wait()
		}
		if candidate2Session != nil {
			candidate2Session.Kill()
			candidate2Session.Wait()
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
})

// ==========================
// 2p1d blacklist test 2 prefills + 1 decode
// ==========================
const (
	pdBlacklist2p1dCtx = "pd-blacklist-2p1d-test"
)

var _ = Describe(pdBlacklist2p1dCtx, Ordered, func() {
	const (
		candidate1 = "candidate1"
		candidate2 = "candidate2"
		worker1    = "worker1"
		worker2    = "worker2"
	)

	const (
		// port range offset from 1p2d test to avoid conflicts
		etcd2p1dListenPort = iota + 36700
		etcd2p1dPeerPort
		etcd2p1dCandidate1ProxyPort
		etcd2p1dCandidate2ProxyPort

		prefill2p1d1Port
		prefill2p1d2Port
		decode2p1d1Port
		proxyPrefill2p1d1Port
		proxyPrefill2p1d2Port
		proxyDecode2p1d1Port

		candidate2p1d1HTTPPort
		candidate2p1d1OpsPort
		candidate2p1d2HTTPPort
		candidate2p1d2POpsPort
		worker2p1d1HTTPPort
		worker2p1d1OpsPort
		worker2p1d2HTTPPort
		worker2p1d2OpsPort
	)

	var (
		candidate2p1d1Session *gexec.Session
		candidate2p1d2Session *gexec.Session
		worker2p1d1Session    *gexec.Session
		worker2p1d2Session    *gexec.Session

		mock2p1dServers []*gexec.Session
		etcd2p1dServer  *embed.Etcd
		etcd2p1dClient  *clientv3.Client

		instance2p1dProxyRules = make(map[string]*proxyRuleEntry)
		etcd2p1dProxyRules     = make(map[string]*etcdProxyRule)

		linkBlockTagValidTimeMs2p1d int64 = 20000 //20s

		blockPrefillID      string
		blockPrefillPort    int
		nonBlockPrefillID   string
		nonBlockPrefillPort int
	)

	BeforeAll(func() {
		var err error
		By("ensure ports are free")
		ports := []string{}
		for i := etcd2p1dListenPort; i <= worker2p1d2HTTPPort; i++ {
			ports = append(ports, strconv.Itoa(i))
		}
		EnsurePorts(ports)
		time.Sleep(500 * time.Millisecond)

		By("start embedded etcd for 2p1d on port " + strconv.Itoa(etcd2p1dListenPort))
		etcdCfg := embed.NewConfig()
		etcdCfg.Name = "test-etcd-2p1d"
		etcdCfg.Dir = filepath.Join(os.TempDir(), fmt.Sprintf("etcd-test-2p1d-%d", time.Now().UnixNano()))
		etcdCfg.ListenPeerUrls = []url.URL{{Scheme: "http", Host: fmt.Sprintf("127.0.0.1:%v", etcd2p1dPeerPort)}}
		etcdCfg.ListenClientUrls = []url.URL{{Scheme: "http", Host: fmt.Sprintf("127.0.0.1:%v", etcd2p1dListenPort)}}
		etcdCfg.AdvertisePeerUrls = []url.URL{{Scheme: "http", Host: fmt.Sprintf("127.0.0.1:%v", etcd2p1dPeerPort)}}
		etcdCfg.AdvertiseClientUrls = []url.URL{{Scheme: "http", Host: fmt.Sprintf("127.0.0.1:%v", etcd2p1dListenPort)}}
		etcdCfg.InitialCluster = fmt.Sprintf("test-etcd-2p1d=http://127.0.0.1:%v", etcd2p1dPeerPort)
		etcdCfg.InitialClusterToken = "test-token-2p1d"
		etcdCfg.ClusterState = embed.ClusterStateFlagNew
		etcdCfg.LogLevel = "error"

		etcd2p1dServer, err = embed.StartEtcd(etcdCfg)
		Expect(err).NotTo(HaveOccurred())
		select {
		case <-etcd2p1dServer.Server.ReadyNotify():
			GinkgoWriter.Printf("[etcd-2p1d] embedded etcd server ready\n")
		case <-time.After(10 * time.Second):
			Fail("etcd 2p1d server did not become ready within 10s")
		}

		By("create etcd proxy for 2p1d")
		candidate1ProxyOption := new(etcdProxyRule)
		candidate2ProxyOption := new(etcdProxyRule)
		etcd2p1dProxyRules[candidate1], etcd2p1dProxyRules[candidate2] = candidate1ProxyOption, candidate2ProxyOption
		go StartTCPProxy(fmt.Sprintf("127.0.0.1:%d", etcd2p1dCandidate1ProxyPort), fmt.Sprintf("127.0.0.1:%d", etcd2p1dListenPort), candidate1ProxyOption)
		go StartTCPProxy(fmt.Sprintf("127.0.0.1:%d", etcd2p1dCandidate2ProxyPort), fmt.Sprintf("127.0.0.1:%d", etcd2p1dListenPort), candidate2ProxyOption)

		By("create client connection to etcd for 2p1d")
		etcd2p1dClient, err = clientv3.New(clientv3.Config{
			Endpoints:   []string{fmt.Sprintf("127.0.0.1:%d", etcd2p1dListenPort)},
			DialTimeout: 5 * time.Second,
		})
		Expect(err).NotTo(HaveOccurred())

		By("read template config and modify blacklist aging parameters for 2p1d test")
		deployPath := filepath.Join(projectRoot, "deploy")
		var cfg commoncfg.Config
		templateContent, err := os.ReadFile(filepath.Join(deployPath, ROUTE_SERVER_TEMPLATE))
		Expect(yaml.Unmarshal(templateContent, &cfg)).To(BeNil())
		cfg.ServerConfig.RateLimitCfg = &commoncfg.RateLimitCfg{
			NonReferenceAPIs: &commoncfg.NonReferenceAPIRateLimit{
				Enabled:                false,
				RequestsPerMinute:      10000,
				CleanupIntervalMinutes: 5,
			},
		}
		cfg.InstanceManagerCfg.HealthCheckConfig.RestHealthCommonConfig.Interval = 2000
		cfg.InstanceManagerCfg.HealthCheckConfig.UnavailableCheckersCfg.RestHealthCfg.ConsecutiveTime = 3
		cfg.InstanceManagerCfg.HealthCheckConfig.AvailableCheckersCfg.LinkBlacklistConfig.LinkBlockWindowsMs = 100
		cfg.InstanceManagerCfg.HealthCheckConfig.AvailableCheckersCfg.LinkBlacklistConfig.LinkBlockTagValidTimeMs = linkBlockTagValidTimeMs2p1d //20s
		cfg.InstanceManagerCfg.HealthCheckConfig.AvailableCheckersCfg.LinkBlacklistConfig.InstanceBlockRatio = 70
		tempTemplatePath := filepath.Join(deployPath, TEMP_TEMPLATE)
		modifiedYaml, err := yaml.Marshal(&cfg)
		Expect(err).To(BeNil())
		Expect(os.WriteFile(tempTemplatePath, modifiedYaml, 0644)).To(BeNil())

		DeferCleanup(func() {
			os.Remove(tempTemplatePath)
		})

		By("create rs config paths for 2p1d")
		etcdCtxDir := filepath.Join(projectRoot, "e2etest", "environment", pdBlacklist2p1dCtx)
		Expect(os.RemoveAll(etcdCtxDir)).To(BeNil())
		candidate1CfgDir := filepath.Join(etcdCtxDir, candidate1)
		candidate2CfgDir := filepath.Join(etcdCtxDir, candidate2)
		worker1CfgDir := filepath.Join(etcdCtxDir, worker1)
		worker2CfgDir := filepath.Join(etcdCtxDir, worker2)
		Expect(os.MkdirAll(filepath.Join(candidate1CfgDir, "config", "pipelines"), 0755)).To(BeNil())
		Expect(os.MkdirAll(filepath.Join(candidate2CfgDir, "config", "pipelines"), 0755)).To(BeNil())
		Expect(os.MkdirAll(filepath.Join(worker1CfgDir, "config", "pipelines"), 0755)).To(BeNil())
		Expect(os.MkdirAll(filepath.Join(worker2CfgDir, "config", "pipelines"), 0755)).To(BeNil())

		By("copy pipelines config for 2p1d")
		Expect(copyDir(filepath.Join(deployPath, "pipelines"), filepath.Join(candidate1CfgDir, "config", "pipelines"))).To(BeNil())
		Expect(copyDir(filepath.Join(deployPath, "pipelines"), filepath.Join(candidate2CfgDir, "config", "pipelines"))).To(BeNil())
		Expect(copyDir(filepath.Join(deployPath, "pipelines"), filepath.Join(worker1CfgDir, "config", "pipelines"))).To(BeNil())
		Expect(copyDir(filepath.Join(deployPath, "pipelines"), filepath.Join(worker2CfgDir, "config", "pipelines"))).To(BeNil())

		// 2p1d: 2 prefill + 1 decode
		By("generate node configs using temp template (2p1d scenario)")
		prefillStr := fmt.Sprintf("prefill1#127.0.0.1:%d,prefill2#127.0.0.1:%d", prefill2p1d1Port, prefill2p1d2Port)
		decodeStr := fmt.Sprintf("decode1#127.0.0.1:%d", decode2p1d1Port)

		candidate1ToolParam := DisableHTTPSConfig([]string{
			"-template", tempTemplatePath,
			"-host", "127.0.0.1",
			"-target", candidate1CfgDir,
			"-infer-mode", "PD",
			"-prefills", prefillStr,
			"-decodes", decodeStr,
			"-name", candidate1,
			"-remote-scheduler-type", "disable",
			"-port", strconv.Itoa(candidate2p1d1HTTPPort),
			"-candidate", "true",
			"-etcd-endpoints", fmt.Sprintf("127.0.0.1:%d", etcd2p1dCandidate1ProxyPort),
			"-infer-module-version", "pipeline",
			"-ops-port", strconv.Itoa(candidate2p1d1OpsPort),
		})
		Expect(exec.Command(configToolPath, candidate1ToolParam...).Run()).To(BeNil())

		candidate2ToolParam := DisableHTTPSConfig([]string{
			"-template", tempTemplatePath,
			"-host", "127.0.0.1",
			"-target", candidate2CfgDir,
			"-infer-mode", "PD",
			"-prefills", prefillStr,
			"-decodes", decodeStr,
			"-name", candidate2,
			"-remote-scheduler-type", "disable",
			"-port", strconv.Itoa(candidate2p1d2HTTPPort),
			"-candidate", "true",
			"-etcd-endpoints", fmt.Sprintf("127.0.0.1:%d", etcd2p1dCandidate2ProxyPort),
			"-infer-module-version", "pipeline",
			"-ops-port", strconv.Itoa(candidate2p1d2POpsPort),
		})
		Expect(exec.Command(configToolPath, candidate2ToolParam...).Run()).To(BeNil())

		worker1ToolParam := DisableHTTPSConfig([]string{
			"-template", tempTemplatePath,
			"-host", "127.0.0.1",
			"-target", worker1CfgDir,
			"-infer-mode", "PD",
			"-prefills", prefillStr,
			"-decodes", decodeStr,
			"-name", worker1,
			"-remote-scheduler-type", "leader",
			"-port", strconv.Itoa(worker2p1d1HTTPPort),
			"-candidate", "false",
			"-etcd-endpoints", fmt.Sprintf("127.0.0.1:%d", etcd2p1dListenPort),
			"-infer-module-version", "pipeline",
			"-ops-port", strconv.Itoa(worker2p1d1OpsPort),
		})
		Expect(exec.Command(configToolPath, worker1ToolParam...).Run()).To(BeNil())

		worker2ToolParam := DisableHTTPSConfig([]string{
			"-template", tempTemplatePath,
			"-host", "127.0.0.1",
			"-target", worker2CfgDir,
			"-infer-mode", "PD",
			"-prefills", prefillStr,
			"-decodes", decodeStr,
			"-name", worker2,
			"-remote-scheduler-type", "leader",
			"-port", strconv.Itoa(worker2p1d2HTTPPort),
			"-candidate", "false",
			"-etcd-endpoints", fmt.Sprintf("127.0.0.1:%d", etcd2p1dListenPort),
			"-infer-module-version", "pipeline",
			"-ops-port", strconv.Itoa(worker2p1d2OpsPort),
		})
		Expect(exec.Command(configToolPath, worker2ToolParam...).Run()).To(BeNil())

		By("initialize config and copy metrics config for 2p1d")
		configPath := filepath.Join(etcdCtxDir, candidate1, "config")
		Expect(os.Setenv(constant.EnvServerConfigPath, configPath)).To(BeNil())
		Expect(options.InitConfig()).To(BeNil())
		metricsCollectorConfigFileName := options.GetConfig().MetricsCfg.MetricsCollectorConfigFileName
		for _, dir := range []string{candidate1CfgDir, candidate2CfgDir, worker1CfgDir, worker2CfgDir} {
			Expect(copyFile(
				filepath.Join(deployPath, metricsCollectorConfigFileName),
				filepath.Join(dir, "config", metricsCollectorConfigFileName),
			)).To(BeNil())
		}

		DeferCleanup(func() {
			os.RemoveAll(path.Join(candidate1CfgDir, "config"))
			os.RemoveAll(path.Join(worker1CfgDir, "config"))
			os.RemoveAll(path.Join(candidate2CfgDir, "config"))
			os.RemoveAll(path.Join(worker2CfgDir, "config"))
		})

		By("modify recover health config - set consecutive-time to 1 for 2p1d")
		Expect(InjectRecoverHealthConfigToYaml(filepath.Join(candidate1CfgDir, "config", "route-server.yaml"), 5)).To(BeNil())
		Expect(InjectRecoverHealthConfigToYaml(filepath.Join(candidate2CfgDir, "config", "route-server.yaml"), 5)).To(BeNil())

		By("start mock vllm instances (2 prefill + 1 decode)")
		mockInstances := []struct {
			port      int
			proxyPort int
			name      string
		}{
			{prefill2p1d1Port, proxyPrefill2p1d1Port, prefill1ID},
			{prefill2p1d2Port, proxyPrefill2p1d2Port, prefill2ID},
			{decode2p1d1Port, proxyDecode2p1d1Port, decodeID},
		}
		for _, inst := range mockInstances {
			params := []string{
				"-addr", "127.0.0.1",
				"-port", strconv.Itoa(inst.port),
				"-name", inst.name,
			}
			command := exec.Command(mockVLLMPath, params...)
			sess, startErr := gexec.Start(command, io.Discard, GinkgoWriter)
			Expect(startErr).NotTo(HaveOccurred())
			mock2p1dServers = append(mock2p1dServers, sess)
			GinkgoWriter.Printf("[mock-2p1d] started %s on port %d\n", inst.name, inst.port)
			ruleEntry := new(proxyRuleEntry)
			instance2p1dProxyRules[inst.name] = ruleEntry
			go startProxy(fmt.Sprintf(":%d", inst.proxyPort), fmt.Sprintf("http://localhost:%d", inst.port), ruleEntry)
		}
		time.Sleep(500 * time.Millisecond)

		By("start candidate route-server for 2p1d")
		Env := os.Environ()
		candidate1Env := append(Env, fmt.Sprintf("%s=%s",
			constant.EnvServerConfigPath, filepath.Join(candidate1CfgDir, "config")))
		command := exec.Command(rsPath)
		command.Env = candidate1Env
		candidate2p1d1Session, err = gexec.Start(command, io.Discard, GinkgoWriter)
		Expect(err).NotTo(HaveOccurred())

		candidate2Env := append(Env, fmt.Sprintf("%s=%s",
			constant.EnvServerConfigPath, filepath.Join(candidate2CfgDir, "config")))
		command = exec.Command(rsPath)
		command.Env = candidate2Env
		candidate2p1d2Session, err = gexec.Start(command, io.Discard, GinkgoWriter)
		Expect(err).NotTo(HaveOccurred())

		By("start worker route-server for 2p1d")
		worker1Env := append(Env, fmt.Sprintf("%s=%s",
			constant.EnvServerConfigPath, filepath.Join(worker1CfgDir, "config")))
		command = exec.Command(rsPath)
		command.Env = worker1Env
		worker2p1d1Session, err = gexec.Start(command, io.Discard, GinkgoWriter)
		Expect(err).NotTo(HaveOccurred())

		worker2Env := append(Env, fmt.Sprintf("%s=%s",
			constant.EnvServerConfigPath, filepath.Join(worker2CfgDir, "config")))
		command = exec.Command(rsPath)
		command.Env = worker2Env
		worker2p1d2Session, err = gexec.Start(command, io.Discard, GinkgoWriter)
		Expect(err).NotTo(HaveOccurred())

		By("wait for all 2p1d servers to be ready (listen)")
		Eventually(candidate2p1d1Session.Out, 30*time.Second).Should(gbytes.Say("Start HTTP server on"))
		Eventually(candidate2p1d2Session.Out, 30*time.Second).Should(gbytes.Say("Start HTTP server on"))
		Eventually(worker2p1d1Session.Out, 30*time.Second).Should(gbytes.Say("Start HTTP server on"))
		Eventually(worker2p1d2Session.Out, 30*time.Second).Should(gbytes.Say("Start HTTP server on"))

		By("wait for leader election and worker registration via etcd for 2p1d")
		Eventually(func() bool {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			resp, getErr := etcd2p1dClient.Get(ctx, rsLeaderKey, clientv3.WithPrefix())
			if getErr != nil || len(resp.Kvs) == 0 {
				return false
			}
			for _, kv := range resp.Kvs {
				if strings.Contains(string(kv.Value), "candidate1") {
					return true
				}
			}
			return false
		}, 30*time.Second, 1*time.Second).Should(BeTrue(), "candidate1 should be elected as leader in 2p1d etcd")

		Eventually(func() bool {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			resp, getErr := etcd2p1dClient.Get(ctx, rsPeersAllDir, clientv3.WithPrefix())
			if getErr != nil || len(resp.Kvs) == 0 {
				return false
			}
			for _, kv := range resp.Kvs {
				if strings.Contains(string(kv.Key), "worker1") {
					return true
				}
			}
			return false
		}, 30*time.Second, 1*time.Second).Should(BeTrue(), "worker1 should be registered in 2p1d etcd")

		By("wait for worker to sync health cache from leader via events for 2p1d")
		workerReadinessClient := &RSClient{BaseClient{BaseURL: "http://localhost:" + strconv.Itoa(worker2p1d1HTTPPort), HTTPClient: &http.Client{}}}
		Eventually(func() bool {
			_, msg, err := workerReadinessClient.GetWorkerReadiness()
			if err != nil {
				return false
			}
			return strings.Contains(msg, `"message":"ready"`)
		}, 60*time.Second, 2*time.Second).Should(BeTrue(), "2p1d worker health cache should be synced from leader")
	})

	BeforeEach(func() {
		for _, rule := range instance2p1dProxyRules {
			rule.Clear()
		}
		for _, rule := range etcd2p1dProxyRules {
			rule.Clear()
		}
	})

	// ==========================
	// under 2p1d conditions, set decode to link down
	// Cover test_faulttest_011 construct pd link pdlinkdown fault, check streaming request failure
	// Cover test_faulttest_012 construct pd link pdlinkdown fault, check non-streaming request failure
	// ==========================
	It("2p1d: construct pd link pdlinkdown fault, check streaming/non-streaming request failure", func() {
		workerClient := NewRSClient("http://localhost:" + strconv.Itoa(worker2p1d1HTTPPort))
		extraHeaders := make(map[string]string)

		nonStreamReqBody := openai.ChatCompletionRequestV1{
			Model: "pangu",
			Messages: []openai.ChatCompletionMessage{{
				Role:    "user",
				Content: "hello",
			}},
			Stream: false,
		}

		streamReqBody := map[string]interface{}{
			"model":      "pangu",
			"messages":   []map[string]string{{"role": "user", "content": "hello"}},
			"max_tokens": 10,
			"stream":     true,
		}

		By("step 1: verify non-streaming normal request succeeds")
		resp, err := workerClient.SendChatCompletion(TimeoutCtx(context.Background(), 30*time.Second), nonStreamReqBody, extraHeaders)
		Expect(err).NotTo(HaveOccurred())
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		var result openai.ChatCompletionResponse
		Expect(ParseResponse(resp, &result)).NotTo(HaveOccurred())
		Expect(result.Choices).NotTo(BeEmpty())

		By("step 2: verify streaming normal request succeeds")
		resp, err = workerClient.SendChatCompletion(TimeoutCtx(context.Background(), 30*time.Second), streamReqBody, extraHeaders)
		Expect(err).NotTo(HaveOccurred())
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		respBody, err := io.ReadAll(resp.Body)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(respBody)).To(ContainSubstring("data:"))

		By("step 3: use mock server LinkDown API to make decode1 return PDLinkDownError")
		mockDecode1Client := NewMockServerClient(strconv.Itoa(decode2p1d1Port))
		resp, err = mockDecode1Client.LinkDown("127.0.0.1", strconv.Itoa(decode2p1d1Port))
		Expect(err).NotTo(HaveOccurred())
		bodyBytes, err := io.ReadAll(resp.Body)
		if err == nil {
			fmt.Printf("[test] Set LinkDown Response Body: %s\n", string(bodyBytes))
			resp.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))
		}
		resp.Body.Close()
		time.Sleep(500 * time.Millisecond)

		By("step 4: verify non-streaming request returns 500, body contains Abort for KV loading failure")
		resp, err = workerClient.SendChatCompletion(TimeoutCtx(context.Background(), 10*time.Second), nonStreamReqBody, extraHeaders)
		Expect(err).NotTo(HaveOccurred())
		Expect(resp.StatusCode).To(Equal(http.StatusInternalServerError))
		respBody, err = io.ReadAll(resp.Body)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(respBody)).To(ContainSubstring("Abort for KV loading failure"))

		By("step 5: verify streaming request returns 200, body contains link/PDLinkDownError/kv_transfer_params")
		resp, err = workerClient.SendChatCompletion(TimeoutCtx(context.Background(), 10*time.Second), streamReqBody, extraHeaders)
		Expect(err).NotTo(HaveOccurred())
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		respBody, err = io.ReadAll(resp.Body)
		Expect(err).NotTo(HaveOccurred())
		respStr := string(respBody)
		Expect(respStr).To(Or(ContainSubstring("link"), ContainSubstring("PDLinkDownError"), ContainSubstring("kv_transfer_params")))

		By("step 6: restore decode1 link status (LinkUp)")
		_, err = mockDecode1Client.LinkUp()
		Expect(err).NotTo(HaveOccurred())
		time.Sleep(500 * time.Millisecond)

		By("step 7: verify non-streaming request recovers successfully")
		Eventually(func() int {
			resp, err := workerClient.SendChatCompletion(TimeoutCtx(context.Background(), 30*time.Second), nonStreamReqBody, extraHeaders)
			if err != nil {
				return -1
			}
			defer resp.Body.Close()
			return resp.StatusCode
		}, 15*time.Second, 1*time.Second).Should(Equal(http.StatusOK), "non-streaming request should recover successfully")

		By("step 8: verify streaming request recovers successfully")
		Eventually(func() bool {
			resp, err := workerClient.SendChatCompletion(TimeoutCtx(context.Background(), 30*time.Second), streamReqBody, extraHeaders)
			if err != nil {
				return false
			}
			respBody, err := io.ReadAll(resp.Body)
			Expect(err).NotTo(HaveOccurred())
			return strings.Contains(string(respBody), "data:")
		}, 15*time.Second, 1*time.Second).Should(BeTrue(), "streaming request should recover successfully, body contains data:")
	})

	// ==========================
	// 1p2d blacklist test: roles reversed
	// 1/1 prefill link blacklist/decode num = 100%, decode will be isolated, status changed to fault
	// 1/2 decode link blacklist/prefill num = 50% < 70%, prefill NOT isolated.
	// Cover test_faulttest_020 | verify pd link blacklist triggers instance isolation and recovery, 2p1d verify isolating p
	// Cover test_faulttest_021 | verify blacklist mechanism: construct >3 PDLinkDownError requests within 90s, add link to blacklist and isolate corresponding d, subsequent requests not dispatched to that d
	// Cover test_faulttest_022 | verify blacklist aging mechanism: construct >3 PDLinkDownError requests within 90s, add link to blacklist and isolate corresponding d, subsequent requests not dispatched to that d, after aging d can accept traffic again
	// Cover test_xpyd_rolesfault_030 | after single p fault, isolate the instance, new requests no longer dispatched to it
	// ==========================
	It("2p1d: after blacklisting a <p-d> link, prefill isolated (blacklisted by decode), decode not isolated (50%<70%); recovers after aging", func() {
		leaderClient := NewRSClient("http://localhost:" + strconv.Itoa(candidate2p1d1HTTPPort))
		workerClient := NewRSClient("http://localhost:" + strconv.Itoa(worker2p1d1HTTPPort))
		extraHeaders := make(map[string]string)
		reqBody := openai.ChatCompletionRequestV1{
			Model: "pangu",
			Messages: []openai.ChatCompletionMessage{{
				Role:    "user",
				Content: "hello",
			}},
			Stream: false,
		}

		By("step 1: verify initial blacklist is empty")
		Eventually(func() bool {
			blResp, err := leaderClient.GetLinkBlackInstances()
			if err != nil {
				return false
			}
			prefillList := blResp.InsBlackLists["prefill"]
			decodeList := blResp.InsBlackLists["decode"]
			return len(prefillList) == 0 && len(decodeList) == 0
		}, 15*time.Second, 1*time.Second).Should(BeTrue(), "2p1d initial blacklist should be empty")

		By("step 2: verify normal request succeeds")
		resp, err := workerClient.SendChatCompletion(TimeoutCtx(context.Background(), 30*time.Second), reqBody, extraHeaders)
		Expect(err).NotTo(HaveOccurred())
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		var result openai.ChatCompletionResponse
		Expect(ParseResponse(resp, &result)).NotTo(HaveOccurred())
		Expect(result.Choices).NotTo(BeEmpty())

		// 2p1d scenario: decode returns PDLinkDownError
		By("step 3: use mock server LinkDown API to make decode1 return PDLinkDownError, triggering KV transfer error")
		mockDecodeClient := NewMockServerClient(strconv.Itoa(proxyDecode2p1d1Port))
		resp, err = mockDecodeClient.LinkDown("127.0.0.1", strconv.Itoa(proxyDecode2p1d1Port))
		Expect(err).NotTo(HaveOccurred())
		Expect(resp.StatusCode).To(Equal(200))

		By("step 4: send requests consecutively to trigger link-block-threshold, 3 failures trigger isolation, one <prefill-decode1> link added to blacklist, the other link healthy")
		for i := 0; i < 5; i++ {
			resp, err = workerClient.SendChatCompletion(TimeoutCtx(context.Background(), 5*time.Second), reqBody, extraHeaders)
			Expect(err).NotTo(HaveOccurred())
			resp.Body.Close()
		}

		By("step 5: verify <prefill-decode1> link has been added to blacklist via GetLinkBlackInstances")
		// in 2p1d scenario, decode1 blacklist contains prefill
		Eventually(func() bool {
			blResp, err := leaderClient.GetLinkBlackInstances()
			if err != nil {
				GinkgoWriter.Printf("[2p1d-test] GetLinkBlackInstances error: %v\n", err)
				return false
			}
			GinkgoWriter.Printf("[2p1d-test] blacklist response: %v\n", blResp)
			decodeList := blResp.InsBlackLists["decode"]
			for _, ins := range decodeList {
				for _, detail := range ins.LinkBlackList {
					if detail.InsId == prefill1ID || detail.InsId == prefill2ID {
						GinkgoWriter.Printf("[2p1d-test] decode %s has blacklisted %s, remainingTime=%dms\n",
							ins.InsId, detail.InsId, detail.RemainingTimeMs)
						blockPrefillID = detail.InsId // determine which prefill link is isolated
						return true
					}
				}
			}
			return false
		}, 15*time.Second, 1*time.Second).Should(BeTrue(), "decode1-prefill1 should be in blacklist")

		// also check that prefill1 blacklist contains decode1
		Eventually(func() bool {
			blResp, err := leaderClient.GetLinkBlackInstances()
			if err != nil {
				return false
			}
			prefillList := blResp.InsBlackLists["prefill"]
			for _, ins := range prefillList {
				for _, detail := range ins.LinkBlackList {
					if detail.InsId == decodeID {
						GinkgoWriter.Printf("[2p1d-test] prefill %s has blacklisted %s, remainingTime=%dms\n",
							ins.InsId, detail.InsId, detail.RemainingTimeMs)
						return true
					}
				}
			}
			return false
		}, 15*time.Second, 1*time.Second).Should(BeTrue(), "prefill-decode1 should be in blacklist")

		// Verify blacklist details: 1/2 prefills blacklisted = 50% < 70%, decode not isolated
		// Verify blacklist details: 1/1 decode blacklisted = 100%, prefill isolated
		blResp, err := leaderClient.GetLinkBlackInstances()
		Expect(err).NotTo(HaveOccurred())
		GinkgoWriter.Printf("[2p1d-test] blacklist details: totalHealthy=%v, insBlackLists=%v\n",
			blResp.TotalHealthyInstance, blResp.InsBlackLists)

		By("step 6: verify isolated prefill status is fault, decode1 status is running (50%<70%, not isolated)")
		// first determine the isolated prefill
		if blockPrefillID == prefill1ID {
			blockPrefillPort = prefill2p1d1Port
			nonBlockPrefillID = prefill2ID
			nonBlockPrefillPort = prefill2p1d2Port
		} else {
			blockPrefillPort = prefill2p1d2Port
			nonBlockPrefillID = prefill1ID
			nonBlockPrefillPort = prefill2p1d1Port
		}
		GinkgoWriter.Printf("[2p1d-test] isolated Prefill is [%v], non-isolated Prefill is [%v]:\n", blockPrefillID, nonBlockPrefillID)
		Eventually(func() string {
			statusResp, err := leaderClient.GetStatusJson()
			if err != nil {
				return ""
			}
			prefillInstances := statusResp.InstanceStatus["prefill"]
			for _, inst := range prefillInstances {
				if strings.Contains(inst.URL, fmt.Sprintf("127.0.0.1:%d", blockPrefillPort)) {
					return inst.Status
				}
			}
			return ""
		}, 15*time.Second, 1*time.Second).Should(Equal(constant.FaultStatus), "prefill1 should be in fault status after blacklist")
		GinkgoWriter.Printf("[2p1d-test] verified: %v status is fault\n", blockPrefillID)

		Eventually(func() string {
			statusResp, err := leaderClient.GetStatusJson()
			if err != nil {
				return ""
			}
			prefillInstances := statusResp.InstanceStatus["prefill"]
			for _, inst := range prefillInstances {
				if strings.Contains(inst.URL, fmt.Sprintf("127.0.0.1:%d", nonBlockPrefillPort)) {
					return inst.Status
				}
			}
			return ""
		}, 15*time.Second, 1*time.Second).Should(Equal(constant.RunningStatus), "prefill2 should be running (not blacklisted)")
		GinkgoWriter.Printf("[2p1d-test] verified: %v status is running\n", nonBlockPrefillID)

		// decode1 not isolated (50% < 70%), status remains running
		Eventually(func() string {
			statusResp, err := leaderClient.GetStatusJson()
			if err != nil {
				return ""
			}
			decodeInstances := statusResp.InstanceStatus["decode"]
			for _, inst := range decodeInstances {
				if strings.Contains(inst.URL, fmt.Sprintf("127.0.0.1:%d", decode2p1d1Port)) {
					return inst.Status
				}
			}
			return ""
		}, 15*time.Second, 1*time.Second).Should(Equal(constant.RunningStatus), "decode1 should still be running (50% < 70%, not isolated)")
		GinkgoWriter.Printf("[2p1d-test] verified: decode1 status is running\n")

		By("step 7: restore decode link status (LinkUp)")
		_, err = mockDecodeClient.LinkUp()
		Expect(err).NotTo(HaveOccurred())
		time.Sleep(500 * time.Millisecond)

		By("step 8: send 30 requests consecutively, confirm no dispatch to isolated Prefill, log does not contain that Prefill, and all return 200")
		GinkgoWriter.Printf("[2p1d-test] isolated Prefill is [%v], non-isolated Prefill is [%v]:\n", blockPrefillID, nonBlockPrefillID) // record current log length, only check new entries later
		logOffset := len(worker2p1d1Session.Out.Contents())
		allOK := true
		for i := 0; i < 30; i++ {
			resp, err := workerClient.SendChatCompletion(TimeoutCtx(context.Background(), 30*time.Second), reqBody, extraHeaders)
			if err != nil || resp.StatusCode != http.StatusOK {
				allOK = false
				GinkgoWriter.Printf("[2p1d-test] request %d failed: err=%v, status=%d\n", i, err, resp.StatusCode)
			}
			if resp.StatusCode == http.StatusOK {
				var chatResult openai.ChatCompletionResponse
				Expect(ParseResponse(resp, &chatResult)).NotTo(HaveOccurred())
				Expect(chatResult.Choices).NotTo(BeEmpty())
			} else {
				resp.Body.Close()
			}
		}
		Expect(allOK).To(BeTrue(), "30 requests should all return 200, isolated prefill not participating in scheduling")
		// only check the new log entries from these 30 requests for the isolated prefill
		newWorkerLog := string(worker2p1d1Session.Out.Contents()[logOffset:])
		dispatchStr30 := fmt.Sprintf("send request to [prefill] instance [%s]", blockPrefillID)
		Expect(strings.Contains(newWorkerLog, dispatchStr30)).To(BeFalse(), "new log entries from 30 requests should not contain dispatch to isolated prefill %s", blockPrefillID)
		GinkgoWriter.Printf("[2p1d-test] verified: 30 requests all returned 200, no dispatch to %v in new log entries\n", blockPrefillID)

		By("step 9: wait for aging time, verify blacklist has been aged out")
		agingWait := time.Duration(linkBlockTagValidTimeMs2p1d)*time.Millisecond + 2*time.Second
		GinkgoWriter.Printf("[2p1d-test] waiting for aging: %v\n", agingWait)
		time.Sleep(agingWait)

		Eventually(func() bool {
			blResp, err := leaderClient.GetLinkBlackInstances()
			if err != nil {
				return false
			}
			// check that prefill has disappeared from decode blacklist
			decodeList := blResp.InsBlackLists["decode"]
			for _, ins := range decodeList {
				for _, detail := range ins.LinkBlackList {
					if detail.InsId == blockPrefillID {
						return false
					}
				}
			}
			// check that decode1 has disappeared from prefill blacklist
			prefillList := blResp.InsBlackLists["prefill"]
			for _, ins := range prefillList {
				for _, detail := range ins.LinkBlackList {
					if detail.InsId == decodeID {
						return false
					}
				}
			}
			return true
		}, 15*time.Second, 1*time.Second).Should(BeTrue(), "blacklist should be cleared after aging")
		GinkgoWriter.Printf("[2p1d-test] blacklist aged out and cleared\n")

		// verify prefill recovers to running after aging
		Eventually(func() string {
			statusResp, err := leaderClient.GetStatusJson()
			if err != nil {
				return ""
			}
			prefillInstances := statusResp.InstanceStatus["prefill"]
			for _, inst := range prefillInstances {
				if strings.Contains(inst.URL, fmt.Sprintf("127.0.0.1:%d", blockPrefillPort)) {
					return inst.Status
				}
			}
			return ""
		}, 30*time.Second, 2*time.Second).Should(Equal(constant.RunningStatus), "%v should be running after blacklist aging", blockPrefillID)
		GinkgoWriter.Printf("[2p1d-test] verified: prefill status recovered to running\n")

		By("step 10: verify requests can reach prefill normally after aging")
		GinkgoWriter.Printf("[2p1d-test] verify whether dispatch can reach previously isolated Prefill [%v]\n", blockPrefillID) // record current log length, only check new entries later
		logOffset = len(worker2p1d1Session.Out.Contents())
		allOK = true
		for i := 0; i < 30; i++ {
			resp, err := workerClient.SendChatCompletion(TimeoutCtx(context.Background(), 30*time.Second), reqBody, extraHeaders)
			if err != nil || resp.StatusCode != http.StatusOK {
				allOK = false
				GinkgoWriter.Printf("[2p1d-test] request %d failed: err=%v, status=%d\n", i, err, resp.StatusCode)
			}
			if resp.StatusCode == http.StatusOK {
				var chatResult openai.ChatCompletionResponse
				Expect(ParseResponse(resp, &chatResult)).NotTo(HaveOccurred())
				Expect(chatResult.Choices).NotTo(BeEmpty())
			} else {
				resp.Body.Close()
			}
		}
		Expect(allOK).To(BeTrue())
		// only check the new log entries from these 30 requests for the isolated prefill
		newWorkerLog = string(worker2p1d1Session.Out.Contents()[logOffset:])
		dispatchStr30 = fmt.Sprintf("send request to [prefill] instance [%s]", blockPrefillID)
		Expect(strings.Contains(newWorkerLog, dispatchStr30)).To(BeTrue(), "new log entries from 30 requests should contain dispatch to prefill [%s]", blockPrefillID)
		GinkgoWriter.Printf("[2p1d-test] test complete: requests can reach prefill normally after aging\n")
	})

	It("2p1d: all three linkdown versions (omni/vllm_ascend/vllm_new) streaming and non-streaming errors can be detected", func() {
		workerClient := NewRSClient("http://localhost:" + strconv.Itoa(worker2p1d1HTTPPort))
		mockDecode1Client := NewMockServerClient(strconv.Itoa(decode2p1d1Port))
		extraHeaders := make(map[string]string)

		nonStreamReqBody := openai.ChatCompletionRequestV1{
			Model: "pangu",
			Messages: []openai.ChatCompletionMessage{{
				Role:    "user",
				Content: "hello",
			}},
			Stream: false,
		}

		streamReqBody := map[string]interface{}{
			"model":      "pangu",
			"messages":   []map[string]string{{"role": "user", "content": "hello"}},
			"max_tokens": 10,
			"stream":     true,
		}

		versions := []struct {
			name           string
			version        string
			nonStreamCode  int
			streamContains string
		}{
			{"omni", "omni", http.StatusInternalServerError, "abort"},
			{"vllm_ascend", "vllm_ascend", http.StatusInternalServerError, "pd link is down"},
			{"vllm_new", "vllm_new", http.StatusInternalServerError, "KV cache load failed"},
		}

		for _, v := range versions {
			By(fmt.Sprintf("testing %s version linkdown", v.name))

			By(fmt.Sprintf("%s: inject linkdown", v.name))
			resp, err := mockDecode1Client.LinkDown("127.0.0.1", strconv.Itoa(decode2p1d1Port), v.version)
			Expect(err).NotTo(HaveOccurred())
			resp.Body.Close()
			time.Sleep(300 * time.Millisecond)

			By(fmt.Sprintf("%s: verify non-streaming request returns %d", v.name, v.nonStreamCode))
			Eventually(func() int {
				resp, err := workerClient.SendChatCompletion(TimeoutCtx(context.Background(), 10*time.Second), nonStreamReqBody, extraHeaders)
				if err != nil {
					return -1
				}
				defer resp.Body.Close()
				return resp.StatusCode
			}, 10*time.Second, 1*time.Second).Should(Equal(v.nonStreamCode), fmt.Sprintf("%s non-streaming should return %d", v.name, v.nonStreamCode))

			By(fmt.Sprintf("%s: verify streaming request body contains %q", v.name, v.streamContains))
			Eventually(func() bool {
				resp, err := workerClient.SendChatCompletion(TimeoutCtx(context.Background(), 10*time.Second), streamReqBody, extraHeaders)
				if err != nil {
					return false
				}
				defer resp.Body.Close()
				respBody, err := io.ReadAll(resp.Body)
				Expect(err).NotTo(HaveOccurred())
				return strings.Contains(string(respBody), v.streamContains)
			}, 10*time.Second, 1*time.Second).Should(BeTrue(), fmt.Sprintf("%s streaming should contain %q", v.name, v.streamContains))

			By(fmt.Sprintf("%s: restore link", v.name))
			_, err = mockDecode1Client.LinkUp()
			Expect(err).NotTo(HaveOccurred())
			time.Sleep(500 * time.Millisecond)

			By(fmt.Sprintf("%s: verify request recovers normally", v.name))
			Eventually(func() int {
				resp, err := workerClient.SendChatCompletion(TimeoutCtx(context.Background(), 30*time.Second), nonStreamReqBody, extraHeaders)
				if err != nil {
					return -1
				}
				defer resp.Body.Close()
				return resp.StatusCode
			}, 15*time.Second, 1*time.Second).Should(Equal(http.StatusOK), fmt.Sprintf("%s non-streaming should return 200 after recovery", v.name))
		}
	})

	AfterAll(func() {
		By("cleanup 2p1d route-servers")
		if worker2p1d1Session != nil {
			worker2p1d1Session.Kill()
			worker2p1d1Session.Wait()
		}
		if worker2p1d2Session != nil {
			worker2p1d2Session.Kill()
			worker2p1d2Session.Wait()
		}
		if candidate2p1d1Session != nil {
			candidate2p1d1Session.Kill()
			candidate2p1d1Session.Wait()
		}
		if candidate2p1d2Session != nil {
			candidate2p1d2Session.Kill()
			candidate2p1d2Session.Wait()
		}
		By("cleanup 2p1d mock servers")
		for _, mock := range mock2p1dServers {
			mock.Kill()
			mock.Wait()
		}
		By("cleanup 2p1d etcd server")
		if etcd2p1dClient != nil {
			etcd2p1dClient.Close()
		}
		if etcd2p1dServer != nil {
			etcd2p1dServer.Close()
		}
	})
})
