package route_server

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"route-server/pkg/common/utils"

	"github.com/goccy/go-yaml"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gbytes"
	"github.com/onsi/gomega/gexec"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/server/v3/embed"
	commoncfg "route-server/pkg/common/config"
	"route-server/pkg/common/constant"
	"route-server/pkg/common/openai"
)

const (
	XPYDReliabilityTestContext = "xpyd-reliability-test"
	XPYD_TEMP_TEMPLATE         = "xpyd-temp-template.yaml"
)

var _ = Describe(XPYDReliabilityTestContext, Ordered, func() {
	const (
		candidate1 = "candidate1"
		candidate2 = "candidate2"

		worker1 = "worker1"
		worker2 = "worker2"
	)

	const (
		etcdListenPort = iota + 31000
		etcdPeerPort

		prefill1Port
		prefill2Port
		decodePort

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

		worker1Session *gexec.Session
		worker2Session *gexec.Session

		mockServers []*gexec.Session
		etcdServer  *embed.Etcd

		etcdClient *clientv3.Client
	)

	BeforeAll(func() {
		var err error
		By("ensure ports are free")
		for i := etcdListenPort; i <= worker2OpsPort; i++ {
			Expect(isPortAvailable(i)).To(BeTrue())
		}

		time.Sleep(100 * time.Millisecond)
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

		By("create client connection to etcd")
		etcdClient, err = clientv3.New(clientv3.Config{
			Endpoints:   []string{fmt.Sprintf("127.0.0.1:%d", etcdListenPort)},
			DialTimeout: 5 * time.Second,
		})
		Expect(err).NotTo(HaveOccurred())
		GinkgoWriter.Printf("[etcd] client connected to 127.0.0.1:%d\n", etcdListenPort)

		By("read and modify template config")
		deployPath := filepath.Join(projectRoot, "deploy")
		var cfg commoncfg.Config
		templateContent, err := os.ReadFile(filepath.Join(deployPath, ROUTE_SERVER_TEMPLATE))
		Expect(yaml.Unmarshal(templateContent, &cfg)).To(BeNil())
		cfg.InstanceManagerCfg.HealthCheckConfig.UnavailableCheckersCfg.RestHealthCfg.ConsecutiveTime = 1
		cfg.InstanceManagerCfg.HealthCheckConfig.AvailableCheckersCfg.HangCheckerCfg.Enable = true
		cfg.InstanceManagerCfg.HealthCheckConfig.AvailableCheckersCfg.HangCheckerCfg.EnableProbeBeforeHangCheck = true
		cfg.InstanceManagerCfg.HealthCheckConfig.AvailableCheckersCfg.HangCheckerCfg.HangProbeIntervalMs = 3000
		cfg.InstanceManagerCfg.HealthCheckConfig.AvailableCheckersCfg.HangCheckerCfg.HangProbeTimeoutMs = 2000
		cfg.InstanceManagerCfg.HealthCheckConfig.AvailableCheckersCfg.HangCheckerCfg.MaxConsecutiveInvalidProbes = 3
		cfg.InstanceManagerCfg.HealthCheckConfig.AvailableCheckersCfg.LinkBlacklistConfig.LinkBlockWindowsMs = 100
		cfg.InstanceManagerCfg.HealthCheckConfig.AvailableCheckersCfg.LinkBlacklistConfig.LinkBlockTagValidTimeMs = 10000
		cfg.InstanceManagerCfg.HealthCheckConfig.AvailableCheckersCfg.LinkBlacklistConfig.InstanceBlockRatio = 70
		cfg.ServerConfig.RateLimitCfg = &commoncfg.RateLimitCfg{
			NonReferenceAPIs: &commoncfg.NonReferenceAPIRateLimit{
				Enabled:                false,
				RequestsPerMinute:      10000,
				CleanupIntervalMinutes: 5,
			},
		}
		for i := range cfg.InstanceManagerCfg.HealthCheckConfig.AvailableCheckersCfg.HangCheckerCfg.HangThresholds {
			if cfg.InstanceManagerCfg.HealthCheckConfig.AvailableCheckersCfg.HangCheckerCfg.HangThresholds[i].Role == "decode" {
				cfg.InstanceManagerCfg.HealthCheckConfig.AvailableCheckersCfg.HangCheckerCfg.HangThresholds[i].ThresholdTime = 5000
			}
		}
		outData, err := yaml.Marshal(&cfg)
		Expect(err).To(BeNil())

		etcdCtxDir := filepath.Join(projectRoot, "e2etest", "environment", XPYDReliabilityTestContext)
		Expect(os.RemoveAll(etcdCtxDir)).To(BeNil())
		Expect(os.MkdirAll(etcdCtxDir, 0755)).To(BeNil())
		tempTemplatePath := filepath.Join(etcdCtxDir, XPYD_TEMP_TEMPLATE)
		Expect(os.WriteFile(tempTemplatePath, outData, 0644)).To(BeNil())
		candidate1CfgDir := filepath.Join(etcdCtxDir, candidate1)
		candidate2CfgDir := filepath.Join(etcdCtxDir, candidate2)
		worker1CfgDir := filepath.Join(etcdCtxDir, worker1)
		worker2CfgDir := filepath.Join(etcdCtxDir, worker2)

		By("create rs config path")
		Expect(os.MkdirAll(filepath.Join(candidate1CfgDir, "config", "pipelines"), 0755)).To(BeNil())
		Expect(os.MkdirAll(filepath.Join(candidate2CfgDir, "config", "pipelines"), 0755)).To(BeNil())
		Expect(os.MkdirAll(filepath.Join(worker1CfgDir, "config", "pipelines"), 0755)).To(BeNil())
		Expect(os.MkdirAll(filepath.Join(worker2CfgDir, "config", "pipelines"), 0755)).To(BeNil())

		By("copy pipelines config")
		Expect(copyDir(filepath.Join(deployPath, "pipelines"), filepath.Join(candidate1CfgDir, "config", "pipelines"))).To(BeNil())
		Expect(copyDir(filepath.Join(deployPath, "pipelines"), filepath.Join(candidate2CfgDir, "config", "pipelines"))).To(BeNil())
		Expect(copyDir(filepath.Join(deployPath, "pipelines"), filepath.Join(worker1CfgDir, "config", "pipelines"))).To(BeNil())
		Expect(copyDir(filepath.Join(deployPath, "pipelines"), filepath.Join(worker2CfgDir, "config", "pipelines"))).To(BeNil())

		By("cp metrics config")
		metricsCollectorConfigFileName := "metrics_config_vllm_0.11.0.yaml"
		Expect(copyFile(filepath.Join(deployPath, metricsCollectorConfigFileName), filepath.Join(candidate1CfgDir, "config", metricsCollectorConfigFileName))).To(BeNil())
		Expect(copyFile(filepath.Join(deployPath, metricsCollectorConfigFileName), filepath.Join(candidate2CfgDir, "config", metricsCollectorConfigFileName))).To(BeNil())
		Expect(copyFile(filepath.Join(deployPath, metricsCollectorConfigFileName), filepath.Join(worker1CfgDir, "config", metricsCollectorConfigFileName))).To(BeNil())
		Expect(copyFile(filepath.Join(deployPath, metricsCollectorConfigFileName), filepath.Join(worker2CfgDir, "config", metricsCollectorConfigFileName))).To(BeNil())

		By("generate candidate1 config")
		candidate1ToolParam := DisableHTTPSConfig([]string{
			"-template", tempTemplatePath,
			"-host", "127.0.0.1",
			"-target", candidate1CfgDir,
			"-infer-mode", "PD",
			"-prefills", fmt.Sprintf("prefill1#127.0.0.1:%d,prefill2#127.0.0.1:%d", prefill1Port, prefill2Port),
			"-decodes", fmt.Sprintf("decode1#127.0.0.1:%d", decodePort),
			"-name", candidate1,
			"-remote-scheduler-type", "disable",
			"-port", strconv.Itoa(candidate1HTTPPort),
			"-ops-port", strconv.Itoa(candidate1OpsPort),
			"-candidate", "true",
			"-etcd-endpoints", fmt.Sprintf("127.0.0.1:%d", etcdListenPort),
			"-infer-module-version", "pipeline",
		})
		Expect(exec.Command(configToolPath, candidate1ToolParam...).Run()).To(BeNil())

		By("generate candidate2 config")
		candidate2ToolParam := DisableHTTPSConfig([]string{
			"-template", tempTemplatePath,
			"-host", "127.0.0.1",
			"-target", candidate2CfgDir,
			"-infer-mode", "PD",
			"-prefills", fmt.Sprintf("prefill1#127.0.0.1:%d,prefill2#127.0.0.1:%d", prefill1Port, prefill2Port),
			"-decodes", fmt.Sprintf("decode1#127.0.0.1:%d", decodePort),
			"-name", candidate2,
			"-remote-scheduler-type", "disable",
			"-port", strconv.Itoa(candidate2HTTPPort),
			"-ops-port", strconv.Itoa(candidate2OpsPort),
			"-candidate", "true",
			"-etcd-endpoints", fmt.Sprintf("127.0.0.1:%d", etcdListenPort),
			"-infer-module-version", "pipeline",
		})
		Expect(exec.Command(configToolPath, candidate2ToolParam...).Run()).To(BeNil())

		By("generate worker1 config")
		worker1ToolParam := DisableHTTPSConfig([]string{
			"-template", tempTemplatePath,
			"-host", "127.0.0.1",
			"-target", worker1CfgDir,
			"-infer-mode", "PD",
			"-prefills", fmt.Sprintf("prefill1#127.0.0.1:%d,prefill2#127.0.0.1:%d", prefill1Port, prefill2Port),
			"-decodes", fmt.Sprintf("decode1#127.0.0.1:%d", decodePort),
			"-name", worker1,
			"-remote-scheduler-type", "leader",
			"-port", strconv.Itoa(worker1HTTPPort),
			"-ops-port", strconv.Itoa(worker1OpsPort),
			"-candidate", "false",
			"-etcd-endpoints", fmt.Sprintf("127.0.0.1:%d", etcdListenPort),
			"-infer-module-version", "pipeline",
		})
		Expect(exec.Command(configToolPath, worker1ToolParam...).Run()).To(BeNil())

		By("generate worker2 config")
		worker2ToolParam := DisableHTTPSConfig([]string{
			"-template", tempTemplatePath,
			"-host", "127.0.0.1",
			"-target", worker2CfgDir,
			"-infer-mode", "PD",
			"-prefills", fmt.Sprintf("prefill1#127.0.0.1:%d,prefill2#127.0.0.1:%d", prefill1Port, prefill2Port),
			"-decodes", fmt.Sprintf("decode1#127.0.0.1:%d", decodePort),
			"-name", worker2,
			"-remote-scheduler-type", "leader",
			"-port", strconv.Itoa(worker2HTTPPort),
			"-ops-port", strconv.Itoa(worker2OpsPort),
			"-candidate", "false",
			"-etcd-endpoints", fmt.Sprintf("127.0.0.1:%d", etcdListenPort),
			"-infer-module-version", "pipeline",
		})
		Expect(exec.Command(configToolPath, worker2ToolParam...).Run()).To(BeNil())

		DeferCleanup(func() {
			os.RemoveAll(etcdCtxDir)
			os.RemoveAll(candidate1CfgDir)
			os.RemoveAll(worker1CfgDir)
			os.RemoveAll(candidate2CfgDir)
			os.RemoveAll(worker2CfgDir)
		})

		By("start mock vllm instances (2 prefill + 1 decode)")
		mockInstances := []struct {
			port int
			name string
		}{
			{prefill1Port, "prefill1"},
			{prefill2Port, "prefill2"},
			{decodePort, "decode1"},
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

		By("wait for both servers to be ready (listen)")
		Eventually(candidate1Session.Out, 30*time.Second).Should(gbytes.Say("Start HTTP server on"))
		GinkgoWriter.Printf("[leader] route-server is ready on port %d\n", candidate1HTTPPort)
		Eventually(candidate2Session.Out, 30*time.Second).Should(gbytes.Say("Start HTTP server on"))
		GinkgoWriter.Printf("[leader] route-server is ready on port %d\n", candidate2HTTPPort)
		Eventually(worker1Session.Out, 30*time.Second).Should(gbytes.Say("Start HTTP server on"))
		GinkgoWriter.Printf("[worker] route-server is ready on port %d\n", worker1HTTPPort)
		Eventually(worker2Session.Out, 30*time.Second).Should(gbytes.Say("Start HTTP server on"))
		GinkgoWriter.Printf("[worker] route-server is ready on port %d\n", worker2HTTPPort)

		By("wait for leader election and worker registration via etcd")
		// Either candidate may win the etcd lease: leader election is
		// first-come-first-served and depends on startup timing. Hard-coding
		// candidate1 makes the test flaky whenever candidate2 connects first.
		Eventually(func() string {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			resp, getErr := etcdClient.Get(ctx, rsLeaderKey, clientv3.WithPrefix())
			if getErr != nil || len(resp.Kvs) == 0 {
				return ""
			}
			if strings.Contains(string(resp.Kvs[0].Value), "candidate1") {
				return "candidate1"
			}
			if strings.Contains(string(resp.Kvs[0].Value), "candidate2") {
				return "candidate2"
			}
			return ""
		}, 30*time.Second, 1*time.Second).ShouldNot(BeEmpty(), "either candidate should be elected as leader in etcd")
		GinkgoWriter.Printf("[etcd] leader elected, confirmed via %s\n", rsLeaderKey)

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

		By("wait for worker to sync health cache from leader via events")
		workerClient := NewRSClient("http://localhost:" + strconv.Itoa(worker1HTTPPort))
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

	BeforeEach(func() {
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

	Context("Instance Reliability Test", func() {
		var (
			clientRs          *RSClient
			mockPrefillClient *MockServerClient
			mockDecodeClient  *MockServerClient
			worker1Client     *RSClient
		)

		BeforeEach(func() {
			worker1Client = NewRSClient("http://localhost:" + strconv.Itoa(worker1HTTPPort))
			clientRs = worker1Client
			mockPrefillClient = NewMockServerClient(strconv.Itoa(prefill1Port))
			mockDecodeClient = NewMockServerClient(strconv.Itoa(decodePort))
		})

		It("MockHealth+MockError scenario - instance should not recover when MockError persists", func() {
			By("set prefill1 instance to unhealth status to trigger fault")
			_, err := mockPrefillClient.MockUnHealth()
			Expect(err).NotTo(HaveOccurred())

			By("wait for prefill1 instance to show fault status via status API")
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
					GinkgoWriter.Printf("[status check] checking prefill entry: url=%v, status=%v\n", pInfo["url"], pInfo["status"])
					if pInfo["url"] == fmt.Sprintf("http://127.0.0.1:%d", prefill1Port) {
						GinkgoWriter.Printf("[status check] prefill1 found at url: %v, status: %v\n", pInfo["url"], pInfo["status"])
						status, ok := pInfo["status"].(string)
						if ok && (status == "fault" || status == "unhealthy") {
							return true
						}
					}
				}
				GinkgoWriter.Printf("[status check] prefill1 not found yet (looking for http://127.0.0.1:%d)\n", prefill1Port)
				return false
			}, 30*time.Second, 5*time.Second).Should(BeTrue(), "status API should show prefill1 instance in fault status")

			By("set prefill1 to return 500 error (MockError) and restore health")
			errBody := strings.NewReader(`{"code": 500}`)
			req, err := http.NewRequest("POST", fmt.Sprintf("http://127.0.0.1:%d/mock_error", prefill1Port), errBody)
			Expect(err).NotTo(HaveOccurred())
			req.Header.Set("Content-Type", "application/json")
			resp, err := mockPrefillClient.HTTPClient.Do(req)
			Expect(err).NotTo(HaveOccurred())
			resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusOK))

			By("restore health (but MockError still effective, probe infer should fail preventing recovery)")
			_, err = mockPrefillClient.MockHealth()
			Expect(err).NotTo(HaveOccurred())

			By("wait for health checker to complete consecutive checks and probe infer, verify instance does not recover due to MockError")
			time.Sleep(35 * time.Second)

			By("verify instance still in fault status, not recovered")
			resp, err = clientRs.GetStatus()
			Expect(err).NotTo(HaveOccurred())
			err = ParseResponse(resp, &statusResp)
			resp.Body.Close()
			Expect(err).NotTo(HaveOccurred())

			prefills, ok := statusResp["prefills"]
			Expect(ok).To(BeTrue())
			prefillList, ok := prefills.([]interface{})
			Expect(ok).To(BeTrue())

			found := false
			for _, p := range prefillList {
				pInfo, ok := p.(map[string]interface{})
				if !ok {
					continue
				}
				if pInfo["url"] == fmt.Sprintf("http://127.0.0.1:%d", prefill1Port) {
					found = true
					status, ok := pInfo["status"].(string)
					Expect(ok).To(BeTrue())
					Expect(status).To(Equal("fault"), "instance still in fault status because MockError is still effective causing probe infer to fail")
				}
			}
			Expect(found).To(BeTrue())

			By("cleanup MockError")
			_, err = mockPrefillClient.RemoveError()
			Expect(err).NotTo(HaveOccurred())

			By("wait for instance to eventually recover to healthy status")
			Eventually(func() bool {
				resp, err := clientRs.GetStatus()
				if err != nil {
					if resp != nil {
						resp.Body.Close()
					}
					return false
				}
				var statusResp map[string]interface{}
				err = ParseResponse(resp, &statusResp)
				resp.Body.Close()
				if err != nil || statusResp == nil {
					return false
				}
				prefills, ok := statusResp["prefills"]
				if !ok {
					return false
				}
				prefillList, ok := prefills.([]interface{})
				if !ok {
					return false
				}
				for _, p := range prefillList {
					pInfo, ok := p.(map[string]interface{})
					if !ok {
						continue
					}
					if pInfo["url"] == fmt.Sprintf("http://127.0.0.1:%d", prefill1Port) {
						status, ok := pInfo["status"].(string)
						if ok && (status == "running" || status == "healthy") {
							return true
						}
					}
				}
				return false
			}, 45*time.Second, 5*time.Second).Should(BeTrue(), "instance should recover to healthy status after MockError is removed")
		})

		It("Three 500 errors within 90 seconds scenario - instance can auto recover after health and remove error", func() {
			mockDecodeClient := NewMockServerClient(strconv.Itoa(decodePort))

			By("set decode instance to health status")
			_, err := mockDecodeClient.MockHealth()
			Expect(err).NotTo(HaveOccurred())
			time.Sleep(1 * time.Second)

			By("remove possible MockError status")
			_, err = mockDecodeClient.RemoveError()
			Expect(err).NotTo(HaveOccurred())
			time.Sleep(1 * time.Second)

			By("set decode instance to return 500 error (MockError)")
			errBody := strings.NewReader(`{"code": 500}`)
			req, err := http.NewRequest("POST", fmt.Sprintf("http://127.0.0.1:%d/mock_error", decodePort), errBody)
			Expect(err).NotTo(HaveOccurred())
			req.Header.Set("Content-Type", "application/json")
			resp, err := mockDecodeClient.HTTPClient.Do(req)
			Expect(err).NotTo(HaveOccurred())
			resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusOK))
			time.Sleep(1 * time.Second)

			By("send 3 inference requests to decode instance via RS to trigger three 500 errors")
			for i := 1; i <= 3; i++ {
				reqBody := openai.ChatCompletionRequestV1{
					Model: "pangu",
					Messages: []openai.ChatCompletionMessage{{
						Role:    "user",
						Content: "hello",
					}},
					MaxTokens: 10,
					Stream:    false,
				}
				resp, err := clientRs.SendChatCompletion(context.Background(), reqBody, nil)
				Expect(err).NotTo(HaveOccurred())
				if resp.StatusCode != 200 {
					GinkgoWriter.Printf("[test] request %d returned status=%d, body:", i, resp.StatusCode)
				}
				resp.Body.Close()
				GinkgoWriter.Printf("[test] inference request %d sent successfully\n", i)
				time.Sleep(1 * time.Second)
			}

			By("wait for status API to show decode instance in fault status")
			var statusRespForFault map[string]interface{}
			Eventually(func() bool {
				resp, err := clientRs.GetStatus()
				if err != nil {
					GinkgoWriter.Printf("[status check] GetStatus error: %v\n", err)
					return false
				}
				err = ParseResponse(resp, &statusRespForFault)
				resp.Body.Close()
				if err != nil || statusRespForFault == nil {
					GinkgoWriter.Printf("[status check] ParseResponse error: %v, statusResp: %v\n", err, statusRespForFault)
					return false
				}
				GinkgoWriter.Printf("[status check] current status response: %+v\n", statusRespForFault)
				decodes, ok := statusRespForFault["decodes"]
				if !ok {
					GinkgoWriter.Printf("[status check] no decodes field in response\n")
					return false
				}
				decodeList, ok := decodes.([]interface{})
				if !ok {
					GinkgoWriter.Printf("[status check] decodes is not array type\n")
					return false
				}
				for _, d := range decodeList {
					dInfo, ok := d.(map[string]interface{})
					if !ok {
						continue
					}
					GinkgoWriter.Printf("[status check] checking decode entry: url=%v, status=%v\n", dInfo["url"], dInfo["status"])
					if dInfo["url"] == fmt.Sprintf("http://127.0.0.1:%d", decodePort) {
						GinkgoWriter.Printf("[status check] decode found at url: %v, status: %v\n", dInfo["url"], dInfo["status"])
						status, ok := dInfo["status"].(string)
						if ok && (status == "fault" || status == "unhealthy") {
							return true
						}
					}
				}
				GinkgoWriter.Printf("[status check] decode not found yet (looking for http://127.0.0.1:%d)\n", decodePort)
				return false
			}, 30*time.Second, 5*time.Second).Should(BeTrue(), "instance should enter fault status after three 500 errors")

			By("call MockHealth to attempt recovery")
			_, err = mockDecodeClient.MockHealth()
			Expect(err).NotTo(HaveOccurred())

			By("remove MockError")
			_, err = mockDecodeClient.RemoveError()
			Expect(err).NotTo(HaveOccurred())

			By("wait for instance to recover to healthy status")
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
				decodes, ok := statusResp["decodes"]
				if !ok {
					GinkgoWriter.Printf("[status check] no decodes field in response\n")
					return false
				}
				decodeList, ok := decodes.([]interface{})
				if !ok {
					GinkgoWriter.Printf("[status check] decodes is not array type\n")
					return false
				}
				for _, d := range decodeList {
					dInfo, ok := d.(map[string]interface{})
					if !ok {
						continue
					}
					GinkgoWriter.Printf("[status check] checking decode entry: url=%v, status=%v\n", dInfo["url"], dInfo["status"])
					if dInfo["url"] == fmt.Sprintf("http://127.0.0.1:%d", decodePort) {
						GinkgoWriter.Printf("[status check] decode found at url: %v, status: %v\n", dInfo["url"], dInfo["status"])
						status, ok := dInfo["status"].(string)
						if ok && (status == "running" || status == "healthy") {
							return true
						}
					}
				}
				GinkgoWriter.Printf("[status check] decode not found yet or not healthy (looking for http://127.0.0.1:%d)\n", decodePort)
				return false
			}, 45*time.Second, 5*time.Second).Should(BeTrue(), "instance should recover to healthy status after MockError is removed")

			By("send request to verify instance fully recovered")
			reqBody := openai.ChatCompletionRequestV1{
				Model: "pangu",
				Messages: []openai.ChatCompletionMessage{{
					Role:    "user",
					Content: "test after recovery",
				}},
				Stream: true,
			}
			resp, err = clientRs.SendChatCompletion(context.Background(), reqBody, nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(resp.StatusCode).To(Equal(http.StatusOK))
			defer resp.Body.Close()
			_, err = io.ReadAll(resp.Body)
			Expect(err).NotTo(HaveOccurred())
		})

		It("Three 500 errors for streaming scenario - instance can auto recover after health and remove error", func() {
			mockDecodeClient := NewMockServerClient(strconv.Itoa(decodePort))

			By("set decode instance to health status")
			_, err := mockDecodeClient.MockHealth()
			Expect(err).NotTo(HaveOccurred())
			time.Sleep(1 * time.Second)

			By("remove possible MockError status")
			_, err = mockDecodeClient.RemoveError()
			Expect(err).NotTo(HaveOccurred())
			time.Sleep(1 * time.Second)

			By("set decode instance to return 500 error (MockError)")
			errBody := strings.NewReader(`{"code": 500}`)
			req, err := http.NewRequest("POST", fmt.Sprintf("http://127.0.0.1:%d/mock_error", decodePort), errBody)
			Expect(err).NotTo(HaveOccurred())
			req.Header.Set("Content-Type", "application/json")
			resp, err := mockDecodeClient.HTTPClient.Do(req)
			Expect(err).NotTo(HaveOccurred())
			resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusOK))
			time.Sleep(1 * time.Second)

			By("send 3 streaming inference requests to decode instance via RS to trigger three 500 errors")
			for i := 1; i <= 3; i++ {
				reqBody := openai.ChatCompletionRequestV1{
					Model: "pangu",
					Messages: []openai.ChatCompletionMessage{{
						Role:    "user",
						Content: "hello stream",
					}},
					MaxTokens: 10,
					Stream:    true,
				}
				resp, err := clientRs.SendChatCompletion(context.Background(), reqBody, nil)
				Expect(err).NotTo(HaveOccurred())
				bodyBytes, err := io.ReadAll(resp.Body)
				Expect(err).NotTo(HaveOccurred())
				resp.Body.Close()
				bodyStr := string(bodyBytes)
				GinkgoWriter.Printf("[test] streaming request %d body length: %d, body preview: %s\n", i, len(bodyBytes), bodyStr[:min(200, len(bodyStr))])
				if strings.Contains(bodyStr, "mock error") || strings.Contains(bodyStr, "error") {
					GinkgoWriter.Printf("[test] streaming request %d got mock error in body\n", i)
				}
				time.Sleep(1 * time.Second)
			}

			By("wait for status API to show decode instance in fault status")
			Eventually(func() bool {
				statusResp, err := clientRs.GetStatusJson()
				if err != nil {
					GinkgoWriter.Printf("[status check] GetStatusJson error: %v\n", err)
					return false
				}
				for _, decode := range statusResp.InstanceStatus["decode"] {
					if strings.Contains(decode.URL, fmt.Sprintf("127.0.0.1:%d", decodePort)) {
						if decode.Status == "fault" {
							return true
						}
					}
				}
				return false
			}, 30*time.Second, 5*time.Second).Should(BeTrue(), "instance should enter fault status after three 500 errors for streaming")

			By("call MockHealth to attempt recovery")
			_, err = mockDecodeClient.MockHealth()
			Expect(err).NotTo(HaveOccurred())

			By("remove MockError")
			_, err = mockDecodeClient.RemoveError()
			Expect(err).NotTo(HaveOccurred())

			By("wait for instance to recover to healthy status")
			Eventually(func() bool {
				statusResp, err := clientRs.GetStatusJson()
				if err != nil {
					return false
				}
				for _, decode := range statusResp.InstanceStatus["decode"] {
					if strings.Contains(decode.URL, fmt.Sprintf("127.0.0.1:%d", decodePort)) {
						if decode.Status == "running" {
							return true
						}
					}
				}
				return false
			}, 45*time.Second, 5*time.Second).Should(BeTrue(), "instance should recover to healthy status after MockError is removed")

			By("send streaming request to verify instance fully recovered")
			reqBody := openai.ChatCompletionRequestV1{
				Model: "pangu",
				Messages: []openai.ChatCompletionMessage{{
					Role:    "user",
					Content: "test after recovery",
				}},
				Stream: true,
			}
			resp, err = clientRs.SendChatCompletion(context.Background(), reqBody, nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(resp.StatusCode).To(Equal(http.StatusOK))
			defer resp.Body.Close()
			_, err = io.ReadAll(resp.Body)
			Expect(err).NotTo(HaveOccurred())
		})

		It("Dual-node confirmed Hang scenario - verify isolation and recovery via status", func() {
			leaderClient := NewRSClient("http://localhost:" + strconv.Itoa(worker1HTTPPort))

			By("Step 1: Ensure decode instance is healthy")
			_, err := mockDecodeClient.MockHealth()
			Expect(err).NotTo(HaveOccurred())
			_, err = mockDecodeClient.RemoveError()
			Expect(err).NotTo(HaveOccurred())
			time.Sleep(2 * time.Second)

			By("Step 2: Set decode instance to hang state")
			resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/hang", decodePort))
			Expect(err).NotTo(HaveOccurred())
			resp.Body.Close()
			GinkgoWriter.Printf("[hang] decode set to hang state on port %d\n", decodePort)

			By("Step 3: Send inference request via leader to trigger hang detection")
			reqBody := openai.ChatCompletionRequestV1{
				Model: "pangu",
				Messages: []openai.ChatCompletionMessage{{
					Role:    "user",
					Content: "trigger hang detection",
				}},
				MaxTokens: 500,
				Stream:    true,
			}
			resp, err = leaderClient.SendChatCompletion(TimeoutCtx(context.Background(), 120*time.Second), reqBody, nil)
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()

			By("Step 4: Wait for hang state to take effect")
			time.Sleep(6 * time.Second)

			By("Step 5: Verify decode status becomes fault")
			Eventually(func() bool {
				statusResp, err := leaderClient.GetStatusJson()
				if err != nil {
					GinkgoWriter.Printf("[status check] GetStatusJson error: %v\n", err)
					return false
				}
				statusBytes, _ := utils.SonicApi.MarshalIndent(statusResp, "", "  ")
				GinkgoWriter.Printf("[status check] status body: %s\n", string(statusBytes))
				for _, decode := range statusResp.InstanceStatus["decode"] {
					GinkgoWriter.Printf("[status check] decode %s status: %s, fault message: %s\n", decode.URL, decode.Status, decode.FaultMessage)
					if decode.Status == "fault" && decode.FaultMessage == "Hang" {
						return true
					}
				}
				return false
			}, 60*time.Second, 2*time.Second).Should(BeTrue(), "decode status should be fault, fault message should be Hang")

			By("Step 6: Recover instance")
			resp3, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/unhang", decodePort))
			Expect(err).NotTo(HaveOccurred())
			resp3.Body.Close()
			GinkgoWriter.Printf("[hang] decode set to unhang state\n")

			By("Step 7: Wait for instance recovery, verify status returns to running")
			Eventually(func() bool {
				statusResp, err := leaderClient.GetStatusJson()
				if err != nil {
					GinkgoWriter.Printf("[status check] GetStatusJson error: %v\n", err)
					return false
				}
				for _, decode := range statusResp.InstanceStatus["decode"] {
					GinkgoWriter.Printf("[status check] decode %s status: %s\n", decode.URL, decode.Status)
					if decode.Status == "running" {
						return true
					}
				}
				return false
			}, 30*time.Second, 2*time.Second).Should(BeTrue(), "decode status should recover to running")

			By("Step 8: Verify requests can be processed again")
			reqBody2 := openai.ChatCompletionRequestV1{
				Model: "pangu",
				Messages: []openai.ChatCompletionMessage{{
					Role:    "user",
					Content: "test after recovery",
				}},
				MaxTokens: 5,
				Stream:    false,
			}
			resp, err = leaderClient.SendChatCompletion(context.Background(), reqBody2, nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(resp.StatusCode).To(Equal(http.StatusOK))
			resp.Body.Close()
		})

		It("Dual-node confirmed Hang scenario - Worker sends requests to verify isolation and recovery", func() {
			workerClient := NewRSClient("http://localhost:" + strconv.Itoa(worker1HTTPPort))

			By("Step 1: Ensure decode instance is healthy")
			_, err := mockDecodeClient.MockHealth()
			Expect(err).NotTo(HaveOccurred())
			_, err = mockDecodeClient.RemoveError()
			Expect(err).NotTo(HaveOccurred())
			time.Sleep(2 * time.Second)

			By("Step 2: Set decode instance to hang state")
			resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/hang", decodePort))
			Expect(err).NotTo(HaveOccurred())
			resp.Body.Close()
			GinkgoWriter.Printf("[hang] decode set to hang state on port %d\n", decodePort)

			By("Step 3: Send inference request via worker to trigger hang detection")
			reqBody := openai.ChatCompletionRequestV1{
				Model: "pangu",
				Messages: []openai.ChatCompletionMessage{{
					Role:    "user",
					Content: "trigger hang detection from worker",
				}},
				MaxTokens: 500,
				Stream:    true,
			}
			resp, err = workerClient.SendChatCompletion(TimeoutCtx(context.Background(), 120*time.Second), reqBody, nil)
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()

			By("Step 4: Wait for hang state to take effect")
			time.Sleep(6 * time.Second)

			By("Step 5: Verify decode status becomes fault")
			Eventually(func() bool {
				statusResp, err := workerClient.GetStatusJson()
				if err != nil {
					GinkgoWriter.Printf("[status check] GetStatusJson error: %v\n", err)
					return false
				}
				statusBytes, _ := utils.SonicApi.MarshalIndent(statusResp, "", "  ")
				GinkgoWriter.Printf("[status check] status body: %s\n", string(statusBytes))
				for _, decode := range statusResp.InstanceStatus["decode"] {
					GinkgoWriter.Printf("[status check] decode %s status: %s, fault message: %s\n", decode.URL, decode.Status, decode.FaultMessage)
					if decode.Status == "fault" && decode.FaultMessage == "Hang" {
						return true
					}
				}
				return false
			}, 120*time.Second, 2*time.Second).Should(BeTrue(), "decode status should be fault, fault message should be Hang")

			By("Step 6: Recover instance")
			resp3, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/unhang", decodePort))
			Expect(err).NotTo(HaveOccurred())
			resp3.Body.Close()
			GinkgoWriter.Printf("[hang] decode set to unhang state\n")

			By("Step 7: Wait for instance recovery, verify status returns to running")
			Eventually(func() bool {
				statusResp, err := workerClient.GetStatusJson()
				if err != nil {
					GinkgoWriter.Printf("[status check] GetStatusJson error: %v\n", err)
					return false
				}
				for _, decode := range statusResp.InstanceStatus["decode"] {
					GinkgoWriter.Printf("[status check] decode %s status: %s\n", decode.URL, decode.Status)
					if decode.Status == "running" {
						return true
					}
				}
				return false
			}, 30*time.Second, 2*time.Second).Should(BeTrue(), "decode status should recover to running")

			By("Step 8: Verify requests can be processed again")
			reqBody2 := openai.ChatCompletionRequestV1{
				Model: "pangu",
				Messages: []openai.ChatCompletionMessage{{
					Role:    "user",
					Content: "test after recovery from worker",
				}},
				MaxTokens: 5,
				Stream:    false,
			}
			resp, err = workerClient.SendChatCompletion(context.Background(), reqBody2, nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(resp.StatusCode).To(Equal(http.StatusOK))
			resp.Body.Close()
		})

		It("False Hang scenario - instance that can respond to probe should not be isolated", func() {
			leaderClient := NewRSClient("http://localhost:" + strconv.Itoa(worker1HTTPPort))

			By("Step 1: Send normal request to verify instance is healthy")
			reqBody := openai.ChatCompletionRequestV1{
				Model: "pangu",
				Messages: []openai.ChatCompletionMessage{{
					Role:    "user",
					Content: "test",
				}},
			}
			resp, err := leaderClient.SendChatCompletion(context.Background(), reqBody, nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(resp.StatusCode).To(Equal(http.StatusOK))
			resp.Body.Close()
			time.Sleep(2 * time.Second)

			By("Step 2: Set up false Hang scenario - set specific request_id to hang")
			testRequestId := "test-false-hang-" + uuid.NewString()
			resp, err = http.Get(fmt.Sprintf("http://127.0.0.1:%d/hang_request?request_id=%s", decodePort, testRequestId))
			Expect(err).NotTo(HaveOccurred())
			resp.Body.Close()
			GinkgoWriter.Printf("[hang] set request_id %s to hang state\n", testRequestId)
			time.Sleep(1 * time.Second)

			By("Step 3: Send request that triggers Hang")
			reqBody2 := openai.ChatCompletionRequestV1{
				Model: "pangu",
				Messages: []openai.ChatCompletionMessage{{
					Role:    "user",
					Content: "trigger hang",
				}},
				MaxTokens: 10,
				Stream:    true,
			}
			extraHeaders := map[string]string{
				constant.HeaderXSpanId:     testRequestId,
				constant.HeadersXRequestID: testRequestId,
			}
			resp2, err := leaderClient.SendChatCompletion(TimeoutCtx(context.Background(), 30*time.Second), reqBody2, extraHeaders)
			Expect(err).NotTo(HaveOccurred())

			By("Step 4: Wait for Hang detection to trigger")
			time.Sleep(6 * time.Second)

			By("Step 5: Verify decode status is still running (successful probe should not isolate)")
			Eventually(func() bool {
				statusResp, err := leaderClient.GetStatusJson()
				if err != nil {
					GinkgoWriter.Printf("[status check] GetStatusJson error: %v\n", err)
					return false
				}
				for _, decode := range statusResp.InstanceStatus["decode"] {
					GinkgoWriter.Printf("[status check] decode %s status: %s\n", decode.URL, decode.Status)
					if decode.Status == "running" {
						return true
					}
				}
				return false
			}, 15*time.Second, 2*time.Second).Should(BeTrue(), "decode status should be running, successful probe should not isolate")

			By("Step 6: Cleanup - remove hang state for specific request")
			resp, err = http.Get(fmt.Sprintf("http://127.0.0.1:%d/unhang_request?request_id=%s", decodePort, testRequestId))
			Expect(err).NotTo(HaveOccurred())
			resp.Body.Close()
			GinkgoWriter.Printf("[hang] unset request_id %s from hang state\n", testRequestId)

			By("Step 7: Close hang inference request")
			resp2.Body.Close()

			By("Step 8: Verify instance can still process requests normally")
			reqBody3 := openai.ChatCompletionRequestV1{
				Model: "pangu",
				Messages: []openai.ChatCompletionMessage{{
					Role:    "user",
					Content: "after cleanup",
				}},
				Stream: true,
			}
			resp, err = leaderClient.SendChatCompletion(context.Background(), reqBody3, nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(resp.StatusCode).To(Equal(http.StatusOK))
			resp.Body.Close()
		})

		It("3 consecutive failed probes should trigger instance isolation", func() {
			leaderClient := NewRSClient("http://localhost:" + strconv.Itoa(worker1HTTPPort))

			By("Step 1: Send normal request to verify instance is healthy")
			reqBody := openai.ChatCompletionRequestV1{
				Model: "pangu",
				Messages: []openai.ChatCompletionMessage{{
					Role:    "user",
					Content: "test",
				}},
			}
			resp, err := leaderClient.SendChatCompletion(context.Background(), reqBody, nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(resp.StatusCode).To(Equal(http.StatusOK))
			resp.Body.Close()
			time.Sleep(2 * time.Second)

			By("Step 2: Set decode instance to hang state")
			resp, err = http.Get(fmt.Sprintf("http://127.0.0.1:%d/hang", decodePort))
			Expect(err).NotTo(HaveOccurred())
			resp.Body.Close()
			GinkgoWriter.Printf("[hang] decode set to hang state on port %d\n", decodePort)

			By("Step 3: Send streaming request that triggers Hang")
			reqBodyTrigger := openai.ChatCompletionRequestV1{
				Model: "pangu",
				Messages: []openai.ChatCompletionMessage{{
					Role:    "user",
					Content: "trigger hang",
				}},
				MaxTokens: 10,
				Stream:    true,
			}
			respTrigger, err := leaderClient.SendChatCompletion(TimeoutCtx(context.Background(), 120*time.Second), reqBodyTrigger, nil)
			Expect(err).NotTo(HaveOccurred())

			By("Step 4: Wait then set prefill to hang to make probe fail")
			time.Sleep(5 * time.Second)
			resp, err = http.Get(fmt.Sprintf("http://127.0.0.1:%d/hang", prefill1Port))
			Expect(err).NotTo(HaveOccurred())
			resp.Body.Close()
			GinkgoWriter.Printf("[hang] prefill1 set to hang state\n")

			By("Step 5: Wait for 3 failed probes then verify isolation is triggered")
			Eventually(func() bool {
				statusResp, err := leaderClient.GetStatusJson()
				if err != nil {
					return false
				}
				for _, decode := range statusResp.InstanceStatus["decode"] {
					if decode.Status == "fault" && decode.FaultMessage == "Hang" {
						return true
					}
				}
				return false
			}, 60*time.Second, 5*time.Second).Should(BeTrue(), "decode should be isolated")

			By("Step 6: Cleanup test environment")
			respTrigger.Body.Close()
			resp, err = http.Get(fmt.Sprintf("http://127.0.0.1:%d/unhang", decodePort))
			Expect(err).NotTo(HaveOccurred())
			resp.Body.Close()
			resp, err = http.Get(fmt.Sprintf("http://127.0.0.1:%d/unhang", prefill1Port))
			Expect(err).NotTo(HaveOccurred())
			resp.Body.Close()
			GinkgoWriter.Printf("[hang] clean up: decode1 and prefill1 set to unhang state\n")

			By("Step 7: Wait for instance recovery")
			Eventually(func() bool {
				statusResp, err := leaderClient.GetStatusJson()
				if err != nil {
					return false
				}
				for _, decode := range statusResp.InstanceStatus["decode"] {
					if decode.Status == "running" {
						return true
					}
				}
				return false
			}, 30*time.Second, 2*time.Second).Should(BeTrue(), "decode should recover to running status")

			By("Step 8: Send request to verify instance has recovered")
			reqBody2 := openai.ChatCompletionRequestV1{
				Model: "pangu",
				Messages: []openai.ChatCompletionMessage{{
					Role:    "user",
					Content: "after cleanup",
				}},
				Stream: true,
			}
			resp, err = leaderClient.SendChatCompletion(context.Background(), reqBody2, nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(resp.StatusCode).To(Equal(http.StatusOK))
			resp.Body.Close()
		})
		It("2p1d: after blacklisting a <p-d> link, prefill is isolated (blacklisted by decode), decode is not isolated (50%<70%); recovers after aging", func() {
			leaderClient := NewRSClient("http://localhost:" + strconv.Itoa(worker1HTTPPort))
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

			By("Step 1: Verify initial blacklist is empty")
			Eventually(func() bool {
				blResp, err := leaderClient.GetLinkBlackInstances()
				if err != nil {
					return false
				}
				prefillList := blResp.InsBlackLists["prefill"]
				decodeList := blResp.InsBlackLists["decode"]
				return len(prefillList) == 0 && len(decodeList) == 0
			}, 15*time.Second, 1*time.Second).Should(BeTrue(), "2p1d initial blacklist should be empty")

			By("Step 2: Verify normal request succeeds")
			resp, err := workerClient.SendChatCompletion(TimeoutCtx(context.Background(), 30*time.Second), reqBody, extraHeaders)
			Expect(err).NotTo(HaveOccurred())
			Expect(resp.StatusCode).To(Equal(http.StatusOK))
			var result openai.ChatCompletionResponse
			Expect(ParseResponse(resp, &result)).NotTo(HaveOccurred())
			Expect(result.Choices).NotTo(BeEmpty())

			By("Step 3: Use mock server LinkDown API to make decode1 return PDLinkDownError, triggering KV transfer error")
			mockDecodeClient := NewMockServerClient(strconv.Itoa(decodePort))
			resp, err = mockDecodeClient.LinkDown("127.0.0.1", strconv.Itoa(decodePort))
			Expect(err).NotTo(HaveOccurred())
			Expect(resp.StatusCode).To(Equal(200))

			By("Step 4: Send consecutive requests to trigger link-block-threshold, adding <prefill-decode1> link to blacklist")
			for i := 0; i < 5; i++ {
				resp, err = workerClient.SendChatCompletion(TimeoutCtx(context.Background(), 5*time.Second), reqBody, extraHeaders)
				Expect(err).NotTo(HaveOccurred())
				resp.Body.Close()
			}

			var blockPrefillID string
			var blockPrefillPort int
			var nonBlockPrefillID string
			var nonBlockPrefillPort int

			By("Step 5: Verify via GetLinkBlackInstances that <prefill-decode1> link has been added to blacklist")
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
							GinkgoWriter.Printf("[2p1d-test] decode %s blacklisted %s, remainingTime=%dms\n",
								ins.InsId, detail.InsId, detail.RemainingTimeMs)
							blockPrefillID = detail.InsId
							return true
						}
					}
				}
				return false
			}, 15*time.Second, 1*time.Second).Should(BeTrue(), "decode1-prefill should be in blacklist")

			Eventually(func() bool {
				blResp, err := leaderClient.GetLinkBlackInstances()
				if err != nil {
					return false
				}
				prefillList := blResp.InsBlackLists["prefill"]
				for _, ins := range prefillList {
					for _, detail := range ins.LinkBlackList {
						if detail.InsId == decodeID {
							GinkgoWriter.Printf("[2p1d-test] prefill %s blacklisted %s, remainingTime=%dms\n",
								ins.InsId, detail.InsId, detail.RemainingTimeMs)
							return true
						}
					}
				}
				return false
			}, 15*time.Second, 1*time.Second).Should(BeTrue(), "prefill-decode1 should be in blacklist")

			blResp, err := leaderClient.GetLinkBlackInstances()
			Expect(err).NotTo(HaveOccurred())
			GinkgoWriter.Printf("[2p1d-test] Blacklist details: totalHealthy=%v, insBlackLists=%v\n",
				blResp.TotalHealthyInstance, blResp.InsBlackLists)

			By("Step 6: Verify isolated prefill status is fault, decode1 status is running (50%<70%, not isolated)")
			if blockPrefillID == prefill1ID {
				blockPrefillPort = prefill1Port
				nonBlockPrefillID = prefill2ID
				nonBlockPrefillPort = prefill2Port
			} else {
				blockPrefillPort = prefill2Port
				nonBlockPrefillID = prefill1ID
				nonBlockPrefillPort = prefill1Port
			}
			GinkgoWriter.Printf("[2p1d-test] Isolated Prefill is [%v], non-isolated Prefill is [%v]:\n", blockPrefillID, nonBlockPrefillID)
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
			}, 15*time.Second, 1*time.Second).Should(Equal(constant.FaultStatus), "prefill should be in fault status after blacklist")
			GinkgoWriter.Printf("[2p1d-test] Verified: %v status is fault\n", blockPrefillID)

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
			}, 15*time.Second, 1*time.Second).Should(Equal(constant.RunningStatus), "non-blocked prefill should be running")
			GinkgoWriter.Printf("[2p1d-test] Verified: %v status is running\n", nonBlockPrefillID)

			Eventually(func() string {
				statusResp, err := leaderClient.GetStatusJson()
				if err != nil {
					return ""
				}
				decodeInstances := statusResp.InstanceStatus["decode"]
				for _, inst := range decodeInstances {
					if strings.Contains(inst.URL, fmt.Sprintf("127.0.0.1:%d", decodePort)) {
						return inst.Status
					}
				}
				return ""
			}, 15*time.Second, 1*time.Second).Should(Equal(constant.RunningStatus), "decode1 should still be running (50% < 70%, not isolated)")
			GinkgoWriter.Printf("[2p1d-test] Verified: decode1 status is running\n")

			By("Step 7: Restore decode link status (LinkUp)")
			_, err = mockDecodeClient.LinkUp()
			Expect(err).NotTo(HaveOccurred())
			time.Sleep(500 * time.Millisecond)

			By("Step 8: Send 30 consecutive requests, confirm none are sent to isolated Prefill, logs do not contain that Prefill, and all return 200")
			GinkgoWriter.Printf("[2p1d-test] Isolated Prefill is [%v], non-isolated Prefill is [%v]:\n", blockPrefillID, nonBlockPrefillID)
			logOffset := len(worker1Session.Out.Contents())
			allOK := true
			for i := 0; i < 30; i++ {
				resp, err := workerClient.SendChatCompletion(TimeoutCtx(context.Background(), 30*time.Second), reqBody, extraHeaders)
				if err != nil || resp.StatusCode != http.StatusOK {
					allOK = false
					GinkgoWriter.Printf("[2p1d-test] Request %d failed: err=%v, status=%d\n", i, err, resp.StatusCode)
				}
				if resp.StatusCode == http.StatusOK {
					var chatResult openai.ChatCompletionResponse
					Expect(ParseResponse(resp, &chatResult)).NotTo(HaveOccurred())
					Expect(chatResult.Choices).NotTo(BeEmpty())
				} else {
					resp.Body.Close()
				}
			}
			Expect(allOK).To(BeTrue(), "All 30 requests should return 200, isolated prefill should not participate in scheduling")
			newWorkerLog := string(worker1Session.Out.Contents()[logOffset:])
			dispatchStr30 := fmt.Sprintf("send request to [prefill] instance [%s]", blockPrefillID)
			Expect(strings.Contains(newWorkerLog, dispatchStr30)).To(BeFalse(), "New logs from 30 requests should not contain scheduling to isolated prefill %s", blockPrefillID)
			GinkgoWriter.Printf("[2p1d-test] Verified: 30 requests all returned 200, and no scheduling to %v in new logs\n", blockPrefillID)

			By("Step 9: Wait for aging time (link-block-tag-valid-time-ms=10s), then verify blacklist has been aged and cleared")
			agingWait := time.Duration(10000)*time.Millisecond + 2*time.Second
			GinkgoWriter.Printf("[2p1d-test] Waiting for aging: %v\n", agingWait)
			time.Sleep(agingWait)

			Eventually(func() bool {
				blResp, err := leaderClient.GetLinkBlackInstances()
				if err != nil {
					return false
				}
				decodeList := blResp.InsBlackLists["decode"]
				for _, ins := range decodeList {
					for _, detail := range ins.LinkBlackList {
						if detail.InsId == blockPrefillID {
							return false
						}
					}
				}
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
			GinkgoWriter.Printf("[2p1d-test] Blacklist has been aged and cleared\n")

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
			GinkgoWriter.Printf("[2p1d-test] Verified: prefill status recovered to running\n")

			By("Step 10: Verify requests can reach prefill normally after aging")
			GinkgoWriter.Printf("[2p1d-test] Verifying if scheduling can reach previously isolated Prefill [%v]\n", blockPrefillID)
			logOffset = len(worker1Session.Out.Contents())
			allOK = true
			for i := 0; i < 30; i++ {
				resp, err := workerClient.SendChatCompletion(TimeoutCtx(context.Background(), 30*time.Second), reqBody, extraHeaders)
				if err != nil || resp.StatusCode != http.StatusOK {
					allOK = false
					GinkgoWriter.Printf("[2p1d-test] Request %d failed: err=%v, status=%d\n", i, err, resp.StatusCode)
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
			newWorkerLog = string(worker1Session.Out.Contents()[logOffset:])
			dispatchStr30 = fmt.Sprintf("send request to [prefill] instance [%s]", blockPrefillID)
			Expect(strings.Contains(newWorkerLog, dispatchStr30)).To(BeTrue(), "New logs from 30 requests should contain scheduling to prefill [%s]", blockPrefillID)
			GinkgoWriter.Printf("[2p1d-test] Test complete: requests can reach prefill normally after aging\n")
		})

		It("2p1d: construct PD link PDLinkDown fault, verify streaming and non-streaming request failures", func() {
			workerClient := NewRSClient("http://localhost:" + strconv.Itoa(worker1HTTPPort))
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

			By("Step 1: Verify non-streaming normal request succeeds")
			resp, err := workerClient.SendChatCompletion(TimeoutCtx(context.Background(), 30*time.Second), nonStreamReqBody, extraHeaders)
			Expect(err).NotTo(HaveOccurred())
			Expect(resp.StatusCode).To(Equal(http.StatusOK))
			var result openai.ChatCompletionResponse
			Expect(ParseResponse(resp, &result)).NotTo(HaveOccurred())
			Expect(result.Choices).NotTo(BeEmpty())

			By("Step 2: Verify streaming normal request succeeds")
			resp, err = workerClient.SendChatCompletion(TimeoutCtx(context.Background(), 30*time.Second), streamReqBody, extraHeaders)
			Expect(err).NotTo(HaveOccurred())
			Expect(resp.StatusCode).To(Equal(http.StatusOK))
			respBody, err := io.ReadAll(resp.Body)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(respBody)).To(ContainSubstring("data:"))

			By("Step 3: Use mock server LinkDown API to make decode1 return PDLinkDownError")
			mockDecodeClient := NewMockServerClient(strconv.Itoa(decodePort))
			resp, err = mockDecodeClient.LinkDown("127.0.0.1", strconv.Itoa(decodePort))
			Expect(err).NotTo(HaveOccurred())
			resp.Body.Close()
			time.Sleep(500 * time.Millisecond)

			By("Step 4: Verify non-streaming request returns 500, body contains Abort for KV loading failure")
			resp, err = workerClient.SendChatCompletion(TimeoutCtx(context.Background(), 10*time.Second), nonStreamReqBody, extraHeaders)
			Expect(err).NotTo(HaveOccurred())
			Expect(resp.StatusCode).To(Equal(http.StatusInternalServerError))
			respBody, err = io.ReadAll(resp.Body)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(respBody)).To(ContainSubstring("Abort for KV loading failure"))

			By("Step 5: Verify streaming request returns 200, body contains link/PDLinkDownError/kv_transfer_params")
			resp, err = workerClient.SendChatCompletion(TimeoutCtx(context.Background(), 10*time.Second), streamReqBody, extraHeaders)
			Expect(err).NotTo(HaveOccurred())
			Expect(resp.StatusCode).To(Equal(http.StatusOK))
			respBody, err = io.ReadAll(resp.Body)
			Expect(err).NotTo(HaveOccurred())
			respStr := string(respBody)
			Expect(respStr).To(Or(ContainSubstring("link"), ContainSubstring("PDLinkDownError"), ContainSubstring("kv_transfer_params")))

			By("Step 6: Restore decode1 link status (LinkUp)")
			_, err = mockDecodeClient.LinkUp()
			Expect(err).NotTo(HaveOccurred())
			time.Sleep(500 * time.Millisecond)

			By("Step 7: Verify non-streaming request recovers successfully")
			Eventually(func() int {
				resp, err := workerClient.SendChatCompletion(TimeoutCtx(context.Background(), 30*time.Second), nonStreamReqBody, extraHeaders)
				if err != nil {
					return -1
				}
				defer resp.Body.Close()
				return resp.StatusCode
			}, 15*time.Second, 1*time.Second).Should(Equal(http.StatusOK), "Non-streaming request should recover successfully")

			By("Step 8: Verify streaming request recovers successfully")
			Eventually(func() bool {
				resp, err := workerClient.SendChatCompletion(TimeoutCtx(context.Background(), 30*time.Second), streamReqBody, extraHeaders)
				if err != nil {
					return false
				}
				respBody, err := io.ReadAll(resp.Body)
				Expect(err).NotTo(HaveOccurred())
				return strings.Contains(string(respBody), "data:")
			}, 15*time.Second, 1*time.Second).Should(BeTrue(), "Streaming request should recover successfully, body should contain data:")
		})
	})
})
