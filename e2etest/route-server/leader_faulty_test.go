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
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/goccy/go-yaml"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gbytes"
	"github.com/onsi/gomega/gexec"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/server/v3/embed"
	commoncfg "infer-dp-scheduler/pkg/common/config"
	"infer-dp-scheduler/pkg/common/constant"
	"infer-dp-scheduler/pkg/common/openai"
)

const (
	leaderFaultyCtx = "leader-faulty-test"
)

var _ = Describe(leaderFaultyCtx, Ordered, func() {
	const (
		candidate1 = "candidate1"
		candidate2 = "candidate2"

		worker1 = "worker1"
		worker2 = "worker2"

		prefill1 = "prefill1"
		prefill2 = "prefill2"
		decode1  = "decode1"
	)

	const (
		etcdListenPort = iota + 30700
		etcdPeerPort
		etcdCandidate1ProxyPort
		etcdCandidate2ProxyPort

		prefill1Port
		prefill2Port
		decodePort
		proxyPrefill1Port
		proxyPrefill2Port
		proxyDecodePort

		proxyCandidate1HTTPPort
		proxyCandidate2HTTPPort
		proxyWorker1HTTPPort
		proxyWorker2HTTPPort

		candidate1HTTPPort
		candidate2HTTPPort
		worker1HTTPPort
		worker2HTTPPort
	)

	var (
		candidate1Session *gexec.Session
		candidate2Session *gexec.Session

		worker1Session *gexec.Session
		worker2Session *gexec.Session

		mockServers []*gexec.Session
		etcdServer  *embed.Etcd

		etcdClient          *clientv3.Client
		rsClients           = make(map[string]*RSClient)
		mockInstanceClients = make(map[string]*MockServerClient)

		instanceProxyRules = make(map[string]*proxyRuleEntry)
		etcdProxyRules     = make(map[string]*etcdProxyRule)

		reqId = 0

		candidate1CfgDir string
		candidate2CfgDir string
		worker1CfgDir    string
		worker2CfgDir    string

		cfgConsecutiveFailureCount int
		cfgRecoverConsecutiveCount int
		cfgFakeReqInterval         int
		cfgFakeReqThreadhold       int

		recoveryWaitTime time.Duration
		etcdRevoteTime   time.Duration
	)

	BeforeAll(func() {
		var err error
		By("ensure ports are free")
		var ports []string
		for i := etcdListenPort; i <= worker2HTTPPort; i++ {
			ports = append(ports, strconv.Itoa(i))
		}

		EnsurePorts(ports)
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
		// Reduce etcd log output to avoid noise
		etcdCfg.LogLevel = "info"

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
		GinkgoWriter.Printf("[etcd] client connected to 127.0.0.1:%d\n", etcdListenPort)

		By("read template config")
		deployPath := filepath.Join(projectRoot, "deploy")
		var cfg commoncfg.Config
		templateContent, err := os.ReadFile(filepath.Join(deployPath, ROUTE_SERVER_TEMPLATE))
		Expect(yaml.Unmarshal(templateContent, &cfg)).To(BeNil())
		configPath := filepath.Join(projectRoot, "e2etest", "environment", leaderFaultyCtx, "config")
		configFile := filepath.Join(configPath, "route-server.yaml")
		Expect(os.MkdirAll(configPath, 0755)).To(BeNil())
		Expect(copyFile(filepath.Join(deployPath, ROUTE_SERVER_TEMPLATE), configFile)).
			To(BeNil())

		By("create rs config paths")
		candidateFaultyCtxDir := filepath.Join(projectRoot, "e2etest", "environment", leaderFaultyCtx)
		Expect(os.RemoveAll(candidateFaultyCtxDir)).To(BeNil())
		candidate1CfgDir = filepath.Join(candidateFaultyCtxDir, candidate1)
		candidate2CfgDir = filepath.Join(candidateFaultyCtxDir, candidate2)
		worker1CfgDir = filepath.Join(candidateFaultyCtxDir, worker1)
		worker2CfgDir = filepath.Join(candidateFaultyCtxDir, worker2)
		Expect(os.MkdirAll(filepath.Join(candidate1CfgDir, "config", "pipelines"), 0755)).To(BeNil())
		Expect(os.MkdirAll(filepath.Join(candidate2CfgDir, "config", "pipelines"), 0755)).To(BeNil())
		Expect(os.MkdirAll(filepath.Join(worker1CfgDir, "config", "pipelines"), 0755)).To(BeNil())
		Expect(os.MkdirAll(filepath.Join(worker2CfgDir, "config", "pipelines"), 0755)).To(BeNil())

		By("copy pipelines config")
		Expect(copyDir(filepath.Join(deployPath, "pipelines"), filepath.Join(candidate1CfgDir, "config", "pipelines"))).To(BeNil())
		Expect(copyDir(filepath.Join(deployPath, "pipelines"), filepath.Join(worker1CfgDir, "config", "pipelines"))).To(BeNil())
		Expect(copyDir(filepath.Join(deployPath, "pipelines"), filepath.Join(candidate2CfgDir, "config", "pipelines"))).To(BeNil())
		Expect(copyDir(filepath.Join(deployPath, "pipelines"), filepath.Join(worker2CfgDir, "config", "pipelines"))).To(BeNil())

		By("cp metrics config")
		metricsCollectorConfigFileName := cfg.MetricsCfg.MetricsCollectorConfigFileName
		Expect(copyFile(filepath.Join(deployPath, metricsCollectorConfigFileName), filepath.Join(candidate1CfgDir, "config", metricsCollectorConfigFileName))).
			To(BeNil())
		Expect(copyFile(filepath.Join(deployPath, metricsCollectorConfigFileName), filepath.Join(candidate2CfgDir, "config", metricsCollectorConfigFileName))).
			To(BeNil())
		Expect(copyFile(filepath.Join(deployPath, metricsCollectorConfigFileName), filepath.Join(worker1CfgDir, "config", metricsCollectorConfigFileName))).
			To(BeNil())
		Expect(copyFile(filepath.Join(deployPath, metricsCollectorConfigFileName), filepath.Join(worker2CfgDir, "config", metricsCollectorConfigFileName))).
			To(BeNil())

		By("modify template config")
		cfgRecoverConsecutiveCount, cfg.InstanceManagerCfg.HealthCheckConfig.UnavailableCheckersCfg.RestHealthCfg.ConsecutiveTime = 3, 3
		cfgConsecutiveFailureCount = cfg.InstanceManagerCfg.HealthCheckConfig.AvailableCheckersCfg.RestHealthCfg.ConsecutiveTime
		cfg.ClusterCfg.EtcdCfg.DialTimeout = 500
		etcdRevoteTime = time.Duration(cfg.ClusterCfg.EtcdCfg.LeaseTtlSec+3) * time.Second

		cfgFakeReqInterval, cfg.InstanceManagerCfg.HealthCheckConfig.AvailableCheckersCfg.RequestFailuresCfg.FakeRequest.SendInterval = 500, 500
		cfgFakeReqThreadhold = cfg.InstanceManagerCfg.HealthCheckConfig.AvailableCheckersCfg.RequestFailuresCfg.FakeRequest.FailureThreshold
		// Extra buffer wait
		recoveryWaitTime = time.Duration(healthCheckIntervalMs*(cfgRecoverConsecutiveCount+1)) * time.Millisecond

		outData, err := yaml.Marshal(&cfg)
		Expect(err).To(BeNil())
		Expect(os.WriteFile(filepath.Join(candidateFaultyCtxDir, TEMP_TEMPLATE), outData, 0644)).To(BeNil())

		By("generate candidate1 config")
		candidate1ToolParam := DisableHTTPSConfig([]string{
			"-template", filepath.Join(candidateFaultyCtxDir, TEMP_TEMPLATE),
			"-host", "127.0.0.1",
			"-target", candidate1CfgDir,
			"-infer-mode", "PD",
			"-prefills", fmt.Sprintf("prefill1#127.0.0.1:%d,prefill2#127.0.0.1:%d", proxyPrefill1Port, proxyPrefill2Port),
			"-decodes", fmt.Sprintf("decode1#127.0.0.1:%d", proxyDecodePort),
			"-name", candidate1,
			"-remote-scheduler-type", "disable",
			"-port", strconv.Itoa(candidate1HTTPPort),
			"-candidate", "true",
			"-etcd-endpoints", fmt.Sprintf("127.0.0.1:%d", etcdCandidate1ProxyPort),
			"-infer-module-version", "pipeline",
		})
		Expect(exec.Command(configToolPath, candidate1ToolParam...).Run()).To(BeNil())
		By("generate candidate2 config")
		candidate2ToolParam := DisableHTTPSConfig([]string{
			"-template", filepath.Join(candidateFaultyCtxDir, TEMP_TEMPLATE),
			"-host", "127.0.0.1",
			"-target", candidate2CfgDir,
			"-infer-mode", "PD",
			"-prefills", fmt.Sprintf("prefill1#127.0.0.1:%d,prefill2#127.0.0.1:%d", proxyPrefill1Port, proxyPrefill2Port),
			"-decodes", fmt.Sprintf("decode1#127.0.0.1:%d", proxyDecodePort),
			"-name", candidate2,
			"-remote-scheduler-type", "disable",
			"-port", strconv.Itoa(candidate2HTTPPort),
			"-candidate", "true",
			"-etcd-endpoints", fmt.Sprintf("127.0.0.1:%d", etcdCandidate2ProxyPort),
			"-infer-module-version", "pipeline",
		})
		Expect(exec.Command(configToolPath, candidate2ToolParam...).Run()).To(BeNil())

		By("generate worker1 config")
		worker1ToolParam := DisableHTTPSConfig([]string{
			"-template", filepath.Join(candidateFaultyCtxDir, TEMP_TEMPLATE),
			"-host", "127.0.0.1",
			"-target", worker1CfgDir,
			"-infer-mode", "PD",
			"-prefills", fmt.Sprintf("prefill1#127.0.0.1:%d,prefill2#127.0.0.1:%d", proxyPrefill1Port, proxyPrefill2Port),
			"-decodes", fmt.Sprintf("decode1#127.0.0.1:%d", proxyDecodePort),
			"-name", worker1,
			"-remote-scheduler-type", "leader",
			"-port", strconv.Itoa(worker1HTTPPort),
			"-candidate", "false",
			"-etcd-endpoints", fmt.Sprintf("127.0.0.1:%d", etcdListenPort),
			"-infer-module-version", "pipeline",
		})
		Expect(exec.Command(configToolPath, worker1ToolParam...).Run()).To(BeNil())

		By("generate worker2 config")
		worker2ToolParam := DisableHTTPSConfig([]string{
			"-template", filepath.Join(candidateFaultyCtxDir, TEMP_TEMPLATE),
			"-host", "127.0.0.1",
			"-target", worker2CfgDir,
			"-infer-mode", "PD",
			"-prefills", fmt.Sprintf("prefill1#127.0.0.1:%d,prefill2#127.0.0.1:%d", proxyPrefill1Port, proxyPrefill2Port),
			"-decodes", fmt.Sprintf("decode1#127.0.0.1:%d", proxyDecodePort),
			"-name", worker2,
			"-remote-scheduler-type", "leader",
			"-port", strconv.Itoa(worker2HTTPPort),
			"-candidate", "false",
			"-etcd-endpoints", fmt.Sprintf("127.0.0.1:%d", etcdListenPort),
			"-infer-module-version", "pipeline",
		})
		Expect(exec.Command(configToolPath, worker2ToolParam...).Run()).To(BeNil())

		DeferCleanup(func() {
			os.RemoveAll(path.Join(candidate1CfgDir, "config"))
			os.RemoveAll(path.Join(worker1CfgDir, "config"))
			os.RemoveAll(path.Join(candidate2CfgDir, "config"))
			os.RemoveAll(path.Join(worker2CfgDir, "config"))
		})

		By("start mock vllm instances (2 prefill + 1 decode)")
		mockInstances := []struct {
			port      int
			proxyPort int
			name      string
		}{
			{prefill1Port, proxyPrefill1Port, "prefill1"},
			{prefill2Port, proxyPrefill2Port, "prefill2"},
			{decodePort, proxyDecodePort, "decode1"},
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
			ruleEntry := &proxyRuleEntry{
				disableConnByRole: make(map[string]bool),
			}
			instanceProxyRules[inst.name] = ruleEntry
			mockInstanceClients[inst.name] = NewMockServerClient(strconv.Itoa(inst.port))
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

		time.Sleep(time.Second * 2)
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
		WaitForLeaderElection(etcdClient, rsLeaderKey, candidate1, 30*time.Second)
		WaitForWorkerRegistration(etcdClient, rsPeersAllDir, worker1, 30*time.Second)
		WaitForWorkerRegistration(etcdClient, rsPeersAllDir, worker2, 30*time.Second)

		By("wait for worker to sync health cache from leader via events")
		rsClients[worker1] = NewRSClient("http://localhost:" + strconv.Itoa(worker1HTTPPort))
		rsClients[worker2] = NewRSClient("http://localhost:" + strconv.Itoa(worker2HTTPPort))
		rsClients[candidate1] = NewRSClient("http://localhost:" + strconv.Itoa(candidate1HTTPPort))
		rsClients[candidate2] = NewRSClient("http://localhost:" + strconv.Itoa(candidate2HTTPPort))

	})

	BeforeEach(func() {
		By("clear proxy rules before new test")
		for _, rule := range instanceProxyRules {
			rule.Clear()
		}
		for _, rule := range etcdProxyRules {
			rule.Clear()
		}

		By("ensure all roles agree candidate1 is leader before new test")
		ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
		defer cancel()
		resp, err := etcdClient.Get(ctx, rsLeaderKey, clientv3.WithPrefix())
		Expect(err).To(BeNil())
		Expect(len(resp.Kvs)).To(BeNumerically(">", 0))
		if !strings.Contains(string(resp.Kvs[0].Value), candidate1) {
			By("candidate1 is not leader, start re-election")
			etcdProxyRules[candidate2].Drop.Store(true)
			WaitForLeaderElection(etcdClient, rsLeaderKey, candidate1, etcdRevoteTime)
			etcdProxyRules[candidate2].Clear()
			time.Sleep(time.Second * 4)
		}

		for role, rs := range rsClients {
			curLeader, err := rs.GetCurrentLeader()
			// todo candidate takes longer to realize it's not leader? Will return 503
			Expect(err).To(BeNil(), "role[%v] error happens", role)
			Expect(curLeader).To(Equal(candidate1), "%v's leader is %v", role, curLeader)
		}

	})

	It("xpyd test", func() {
		clientRs := NewRSClient("http://localhost:" + strconv.Itoa(worker1HTTPPort))
		extraHeaders := make(map[string]string)
		spanId := uuid.NewString()
		extraHeaders[constant.HeaderXSpanId] = spanId
		reqBody := openai.ChatCompletionRequestV1{
			Model: "pangu",
			Messages: []openai.ChatCompletionMessage{{
				Role:    "user",
				Content: "hello",
			},
			},
		}
		resp, err := clientRs.SendChatCompletion(TimeoutCtx(context.Background(), 30*time.Second), reqBody, extraHeaders)
		Expect(err).NotTo(HaveOccurred())
		Expect(resp.StatusCode).To(Equal(http.StatusOK))

		var result openai.ChatCompletionResponse
		err = ParseResponse(resp, &result)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Choices).NotTo(BeEmpty())

		instanceProxyRules[decodeID].SetRule(func(r *http.Request) bool {
			return false
		}, "deny all", 500)

		resp, err = clientRs.SendChatCompletion(TimeoutCtx(context.Background(), 1*time.Second), reqBody, extraHeaders)
		Expect(err).NotTo(HaveOccurred())
		Expect(resp.StatusCode).NotTo(Equal(http.StatusOK))
	})

	// 1) Impact on worker: inference degraded, availability not affected
	// 2) Impact on centralized health check, ops, address management: unavailable during leader failover
	It("Fault scenario 1: elected leader down, ETCD available, leader service unavailable during failover", func() {
		By("query worker1 rs:num_fallback_scheduled and rs:leader_change_total metrics, record current values")
		resp, err := rsClients[worker1].GetInternalMetrics()
		Expect(err).To(BeNil())
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		lastFallbackScheduled, err := ExtractMetricValue(resp, "rs:num_fallback_scheduled", nil)
		Expect(err).To(BeNil())
		lastLeaderChangeCnt, err := ExtractMetricValue(resp, "rs:leader_change_total", nil)
		Expect(err).To(BeNil())

		By("kill candidate1 session")
		c1Session := newSessionWrapper(&candidate1Session,
			[]string{fmt.Sprintf("%s=%s",
				constant.EnvServerConfigPath, filepath.Join(candidate1CfgDir, "config"))})
		Expect(c1Session).NotTo(BeNil())
		c1Session.Stop()

		By("send infer req during failover")
		resp, err = sendFastInferRequest(rsClients[worker1], leaderFaultyCtx+strconv.Itoa(reqId), 0)
		Expect(err).To(BeNil())
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		resp.Body.Close()

		By("wait for candidate2 to be elected leader")
		WaitForLeaderElection(etcdClient, rsLeaderKey, candidate2, etcdRevoteTime)
		worker1Leader, err := rsClients[worker1].GetCurrentLeader()
		Expect(err).To(BeNil())
		Expect(worker1Leader).To(Equal(candidate2))

		resp, err = rsClients[worker1].GetInternalMetrics()
		Expect(err).To(BeNil())
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		curLeaderChangeCnt, err := ExtractMetricValue(resp, "rs:leader_change_total", nil)
		Expect(err).To(BeNil())
		curFallbackScheduled, err := ExtractMetricValue(resp, "rs:num_fallback_scheduled", nil)
		Expect(err).To(BeNil())

		Expect(curFallbackScheduled).To(BeNumerically(">", lastFallbackScheduled))
		Expect(curLeaderChangeCnt).To(Or(Equal(lastLeaderChangeCnt+1), Equal(lastLeaderChangeCnt+2)))
		lastFallbackScheduled, lastLeaderChangeCnt = curFallbackScheduled, curLeaderChangeCnt

		By("send infer req after election, observe rs:num_fallback_scheduled unchanged")
		reqId++
		resp, err = sendFastInferRequest(rsClients[worker1], leaderFaultyCtx+strconv.Itoa(reqId), 0)
		Expect(err).To(BeNil())
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		resp, err = rsClients[worker1].GetInternalMetrics()
		Expect(err).To(BeNil())
		curFallbackScheduled, err = ExtractMetricValue(resp, "rs:num_fallback_scheduled", nil)
		Expect(err).To(BeNil())
		Expect(curFallbackScheduled).To(Equal(lastFallbackScheduled))
		Expect(c1Session.Restart()).To(BeNil())

	})

	// 1) Impact on worker: inference degraded, availability not affected
	// 2) Impact on centralized health check, ops, address management: unavailable during leader failover
	It("Fault scenario 2: elected leader down, ETCD unavailable, failover impossible", func() {
		By("query worker1 rs:num_fallback_scheduled and rs:leader_change_total metrics, record current values")
		resp, err := rsClients[worker1].GetInternalMetrics()
		Expect(err).To(BeNil())
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		lastFallbackScheduled, err := ExtractMetricValue(resp, "rs:num_fallback_scheduled", nil)
		Expect(err).To(BeNil())
		lastLeaderChangeCnt, err := ExtractMetricValue(resp, "rs:leader_change_total", nil)
		Expect(err).To(BeNil())

		By("query candidate2 rs:etcd_connection_retries metric, record current value")
		resp, err = rsClients[candidate2].GetInternalMetrics()
		Expect(err).To(BeNil())
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		lastETCDRetries, err := ExtractMetricValue(resp, "rs:etcd_connection_retries", nil)
		Expect(err).To(BeNil())

		By("[manage]kill candidate1,disable network of candidate2 to etcd")
		c1Session := newSessionWrapper(&candidate1Session,
			[]string{fmt.Sprintf("%s=%s",
				constant.EnvServerConfigPath, filepath.Join(candidate1CfgDir, "config"))})
		Expect(c1Session).NotTo(BeNil())
		c1Session.Stop()
		etcdProxyRules[candidate2].Drop.Store(true)
		time.Sleep(time.Millisecond * 3500)

		reqId++
		resp, err = sendFastInferRequest(rsClients[worker1], leaderFaultyCtx+strconv.Itoa(reqId), 0)
		Expect(err).To(BeNil())
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		resp.Body.Close()

		resp, err = rsClients[worker1].GetInternalMetrics()
		Expect(err).To(BeNil())
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		curFallbackScheduled, err := ExtractMetricValue(resp, "rs:num_fallback_scheduled", nil)
		Expect(err).To(BeNil())
		Expect(curFallbackScheduled).To(BeNumerically(">", lastFallbackScheduled))
		lastFallbackScheduled = curFallbackScheduled

		resp, err = rsClients[candidate2].GetInternalMetrics()
		Expect(err).To(BeNil())
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		curETCDRetries, err := ExtractMetricValue(resp, "rs:etcd_connection_retries", nil)
		Expect(err).To(BeNil())
		Expect(curETCDRetries).To(BeNumerically(">", lastETCDRetries))
		lastETCDRetries = curETCDRetries

		worker1Leader, err := rsClients[worker1].GetCurrentLeader()
		Expect(err).NotTo(BeNil())

		By("[manage]set decode1 faulty")
		resp, err = mockInstanceClients[decode1].MockError(500)
		Expect(err).To(BeNil())
		Expect(resp.StatusCode).To(Equal(http.StatusOK))

		for i := 0; i < cfgConsecutiveFailureCount-1; i++ {
			reqId++
			resp, err = sendFastInferRequest(rsClients[worker1], leaderFaultyCtx+strconv.Itoa(reqId), 0)
			Expect(err).To(BeNil())
			Expect(resp.StatusCode).NotTo(Equal(http.StatusOK))
			resp.Body.Close()
		}

		By("[manage]unset decode1 faulty")
		resp, err = mockInstanceClients[decode1].RemoveMockError()
		Expect(err).To(BeNil())
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		reqId++
		resp, err = sendFastInferRequest(rsClients[worker1], leaderFaultyCtx+strconv.Itoa(reqId), 0)
		Expect(err).To(BeNil())
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		resp.Body.Close()

		By("[manage]set decode faulty")
		resp, err = mockInstanceClients[decode1].MockError(500)
		Expect(err).To(BeNil())
		Expect(resp.StatusCode).To(Equal(http.StatusOK))

		Eventually(worker1Session.Out, 10*time.Second).ShouldNot(
			gbytes.Say(`Instance decode1.* added to the blacklist`),
		)
		for i := 0; i < cfgConsecutiveFailureCount; i++ {
			reqId++
			resp, err = sendFastInferRequest(rsClients[worker1], leaderFaultyCtx+strconv.Itoa(reqId), 0)
			Expect(err).To(BeNil())
			Expect(resp.StatusCode).NotTo(Equal(http.StatusOK))
			resp.Body.Close()
		}
		Eventually(worker1Session.Out, 10*time.Second).Should(
			gbytes.Say(`Instance decode1.* added to the blacklist`),
		)
		// todo observe worker1 API and metrics

		By("[manage]unset decode faulty")
		resp, err = mockInstanceClients[decode1].RemoveMockError()
		Expect(err).To(BeNil())
		Expect(resp.StatusCode).To(Equal(http.StatusOK))

		time.Sleep(recoveryWaitTime)
		reqId++
		resp, err = sendFastInferRequest(rsClients[worker1], leaderFaultyCtx+strconv.Itoa(reqId), 0)
		Expect(err).To(BeNil())
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		resp.Body.Close()

		resp, err = rsClients[worker1].GetInternalMetrics()
		Expect(err).To(BeNil())
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		curFallbackScheduled, err = ExtractMetricValue(resp, "rs:num_fallback_scheduled", nil)
		Expect(err).To(BeNil())
		Expect(curFallbackScheduled).To(BeNumerically(">", lastFallbackScheduled))
		lastFallbackScheduled = curFallbackScheduled

		By("enable network of candidate2 to etcd,wait for candiate2 elected")
		etcdProxyRules[candidate2].Drop.Store(false)
		WaitForLeaderElection(etcdClient, rsLeaderKey, candidate2, etcdRevoteTime)

		worker1Leader, err = rsClients[worker1].GetCurrentLeader()
		Expect(err).To(BeNil())
		Expect(worker1Leader).To(Equal(candidate2))

		time.Sleep(time.Second)
		reqId++
		resp, err = sendFastInferRequest(rsClients[worker1], leaderFaultyCtx+strconv.Itoa(reqId), 0)
		Expect(err).To(BeNil())
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		resp.Body.Close()

		resp, err = rsClients[worker1].GetInternalMetrics()
		Expect(err).To(BeNil())
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		curLeaderChangeCnt, err := ExtractMetricValue(resp, "rs:leader_change_total", nil)
		Expect(err).To(BeNil())
		Expect(curLeaderChangeCnt).To(BeNumerically(">", lastLeaderChangeCnt))
		lastLeaderChangeCnt = curLeaderChangeCnt

		lastFallbackScheduled, err = ExtractMetricValue(resp, "rs:num_fallback_scheduled", nil)
		Expect(err).To(BeNil())

		resp, err = rsClients[candidate2].GetInternalMetrics()
		Expect(err).To(BeNil())
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		lastETCDRetries, err = ExtractMetricValue(resp, "rs:etcd_connection_retries", nil)
		Expect(err).To(BeNil())

		time.Sleep(time.Second * 2)

		reqId++
		resp, err = sendFastInferRequest(rsClients[worker1], leaderFaultyCtx+strconv.Itoa(reqId), 0)
		Expect(err).To(BeNil())
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		resp.Body.Close()

		resp, err = rsClients[worker1].GetInternalMetrics()
		Expect(err).To(BeNil())
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		curFallbackScheduled, err = ExtractMetricValue(resp, "rs:num_fallback_scheduled", nil)
		Expect(err).To(BeNil())
		Expect(curFallbackScheduled).To(Equal(lastFallbackScheduled))

		resp, err = rsClients[candidate2].GetInternalMetrics()
		Expect(err).To(BeNil())
		curETCDRetries, err = ExtractMetricValue(resp, "rs:etcd_connection_retries", nil)
		Expect(err).To(BeNil())
		Expect(curETCDRetries).To(Equal(lastETCDRetries))

		Expect(c1Session.Restart()).To(BeNil())
	})

	It("Fault scenario 3: network fluctuation between elected leader and ETCD, leader loss triggers failover", func() {
		By("query worker1 rs:num_fallback_scheduled and rs:leader_change_total metrics, record current values")
		resp, err := rsClients[worker1].GetInternalMetrics()
		Expect(err).To(BeNil())
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		lastFallbackScheduled, err := ExtractMetricValue(resp, "rs:num_fallback_scheduled", nil)
		Expect(err).To(BeNil())
		lastLeaderChangeCnt, err := ExtractMetricValue(resp, "rs:leader_change_total", nil)
		Expect(err).To(BeNil())

		By("query candidate1 rs:etcd_connection_retries metric, record current value")
		resp, err = rsClients[candidate1].GetInternalMetrics()
		Expect(err).To(BeNil())
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		lastETCDRetries, err := ExtractMetricValue(resp, "rs:etcd_connection_retries", nil)
		Expect(err).To(BeNil())

		By("[manage]disable network of candidate1 to etcd,waiting for candiate2 becomes leader")
		etcdProxyRules[candidate1].Drop.Store(true)
		WaitForLeaderElection(etcdClient, rsLeaderKey, candidate2, etcdRevoteTime)

		loadReset, err := rsClients[candidate2].CheckLoadReset()
		Expect(err).To(BeNil())
		Expect(loadReset).To(Equal(true))

		resp, err = rsClients[candidate1].GetInternalMetrics()
		Expect(err).To(BeNil())
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		curETCDRetries, err := ExtractMetricValue(resp, "rs:etcd_connection_retries", nil)
		Expect(err).To(BeNil())
		Expect(curETCDRetries).Should(BeNumerically(">", lastETCDRetries))
		lastETCDRetries = curETCDRetries

		reqId++
		resp, err = sendFastInferRequest(rsClients[worker1], leaderFaultyCtx+strconv.Itoa(reqId), 0)
		Expect(err).To(BeNil())
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		resp.Body.Close()

		resp, err = rsClients[worker1].GetInternalMetrics()
		Expect(err).To(BeNil())
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		curFallbackScheduled, err := ExtractMetricValue(resp, "rs:num_fallback_scheduled", nil)
		Expect(err).To(BeNil())
		Expect(curFallbackScheduled).Should(Equal(lastFallbackScheduled))
		lastFallbackScheduled = curFallbackScheduled
		curLeaderChangeCnt, err := ExtractMetricValue(resp, "rs:leader_change_total", nil)
		Expect(err).To(BeNil())
		Expect(curLeaderChangeCnt).Should(BeNumerically(">", lastLeaderChangeCnt))
	},
	)

	It("Fault scenario 4: unelected standby candidate failure (down, network fault) test_xpyd_rolesfault_052 test_xpyd_rolesfault_053", func() {
		By("query worker1 rs:num_fallback_scheduled and rs:leader_change_total metrics, record current values")
		resp, err := rsClients[worker1].GetInternalMetrics()
		Expect(err).To(BeNil())
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		lastFallbackScheduled, err := ExtractMetricValue(resp, "rs:num_fallback_scheduled", nil)
		Expect(err).To(BeNil())
		lastLeaderChangeCnt, err := ExtractMetricValue(resp, "rs:leader_change_total", nil)
		Expect(err).To(BeNil())

		By("inference requests should be processed normally throughout candidate failure")
		doneChan := make(chan error, 1)
		errChan := make(chan error, 1)
		ctx, cancel := context.WithCancel(context.Background())
		// Create ticker
		go func() {
			ticker := time.NewTicker(500 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					doneChan <- nil
					GinkgoWriter.Println("received external stop signal, exiting ticker...")
					return

				case err := <-errChan:
					if err != nil {
						GinkgoWriter.Printf("task error: %v, ticker emergency stop!\n", err)
						doneChan <- fmt.Errorf("timer stopped by task error: %w", err)
						return
					}

				case <-ticker.C:
					go func() {
						reqId++
						resp, err := sendFastInferRequest(rsClients[worker1], leaderFaultyCtx+strconv.Itoa(reqId), 5)
						if err != nil {
							select {
							case errChan <- err:
							default:
							}
							return
						}

						if resp.StatusCode != 200 {
							select {
							case errChan <- fmt.Errorf("status code is %v", resp.StatusCode):
							default:
							}
						}
					}()
				}
			}
		}()

		By("[manage]kill candidate2 session")
		c2Session := newSessionWrapper(&candidate2Session,
			[]string{fmt.Sprintf("%s=%s",
				constant.EnvServerConfigPath, filepath.Join(candidate2CfgDir, "config"))})
		Expect(c2Session).NotTo(BeNil())
		c2Session.Stop()
		time.Sleep(etcdRevoteTime)

		reqId++
		resp, err = sendFastInferRequest(rsClients[worker1], leaderFaultyCtx+strconv.Itoa(reqId), 0)
		Expect(err).To(BeNil())
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		resp.Body.Close()

		resp, err = rsClients[worker1].GetInternalMetrics()
		Expect(err).To(BeNil())
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		curFallbackScheduled, err := ExtractMetricValue(resp, "rs:num_fallback_scheduled", nil)
		Expect(err).To(BeNil())
		Expect(curFallbackScheduled).Should(Equal(lastFallbackScheduled))
		lastFallbackScheduled = curFallbackScheduled
		curLeaderChangeCnt, err := ExtractMetricValue(resp, "rs:leader_change_total", nil)
		Expect(err).To(BeNil())
		Expect(curLeaderChangeCnt).Should(Equal(lastLeaderChangeCnt))
		lastLeaderChangeCnt = curLeaderChangeCnt

		Expect(rsClients[candidate1].InstanceLeave([]string{prefill2})).To(BeNil())
		Expect(c2Session.Restart()).To(BeNil())
		time.Sleep(time.Second * 2)
		routes, err := rsClients[candidate2].InstanceRoutesFromClusterState()
		Expect(err).To(BeNil())
		Expect(routes).ShouldNot(HaveKey(prefill2))

		By("[manage]disable network of candidate2 to etcd")
		resp, err = rsClients[candidate2].GetInternalMetrics()
		Expect(err).To(BeNil())
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		lastETCDRetries, err := ExtractMetricValue(resp, "rs:etcd_connection_retries", nil)
		Expect(err).To(BeNil())
		etcdProxyRules[candidate2].Drop.Store(true)
		time.Sleep(etcdRevoteTime)

		Expect(rsClients[candidate1].InstanceJoin(map[string]*commoncfg.Route{prefill2: &commoncfg.Route{
			Ip:   "127.0.0.1",
			Port: proxyPrefill2Port,
			Role: "prefill",
		}})).Should(BeNil())

		reqId++
		resp, err = sendFastInferRequest(rsClients[worker1], leaderFaultyCtx+strconv.Itoa(reqId), 0)
		Expect(err).To(BeNil())
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		resp.Body.Close()

		resp, err = rsClients[worker1].GetInternalMetrics()
		Expect(err).To(BeNil())
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		curFallbackScheduled, err = ExtractMetricValue(resp, "rs:num_fallback_scheduled", nil)
		Expect(err).To(BeNil())
		Expect(curFallbackScheduled).Should(Equal(lastFallbackScheduled))
		lastFallbackScheduled = curFallbackScheduled
		curLeaderChangeCnt, err = ExtractMetricValue(resp, "rs:leader_change_total", nil)
		Expect(err).To(BeNil())
		Expect(curLeaderChangeCnt).Should(Equal(lastLeaderChangeCnt))
		lastLeaderChangeCnt = curLeaderChangeCnt

		resp, err = rsClients[candidate2].GetInternalMetrics()
		Expect(err).To(BeNil())
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		curETCDRetries, err := ExtractMetricValue(resp, "rs:etcd_connection_retries", nil)
		Expect(err).To(BeNil())
		Expect(curETCDRetries).Should(BeNumerically(">", lastETCDRetries))

		By("[manage]recover the network of candidate2")
		etcdProxyRules[candidate2].Drop.Store(false)
		time.Sleep(etcdRevoteTime)
		routes, err = rsClients[candidate2].InstanceRoutesFromClusterState()
		Expect(err).To(BeNil())
		Expect(routes).To(HaveKey(prefill2))

		cancel()
		stopReason := <-doneChan
		Expect(stopReason).Should(BeNil())
	},
	)

	It("leader failure, management requests should fail during failover test_xpyd_rolesfault_001", func() {
		By("[manage]kill candidate1")
		c1Session := newSessionWrapper(&candidate1Session,
			[]string{fmt.Sprintf("%s=%s",
				constant.EnvServerConfigPath, filepath.Join(candidate1CfgDir, "config"))})
		Expect(c1Session).NotTo(BeNil())
		c1Session.Stop()
		Expect(rsClients[candidate2].InstanceLeave([]string{prefill1})).ShouldNot(BeNil())
		Expect(c1Session.Restart()).Should(Succeed())
		time.Sleep(2 * time.Second)
	},
	)

	It("State sync during leader failover test_faulttest_002 test_faulttest_003", func() {
		c1Session := newSessionWrapper(&candidate1Session,
			[]string{fmt.Sprintf("%s=%s",
				constant.EnvServerConfigPath, filepath.Join(candidate1CfgDir, "config"))})

		c2Session := newSessionWrapper(&candidate2Session,
			[]string{fmt.Sprintf("%s=%s",
				constant.EnvServerConfigPath, filepath.Join(candidate2CfgDir, "config"))})
		By("[manage]kill candidate2 session")
		c2Session.Stop()
		time.Sleep(etcdRevoteTime)

		By("prefill2 leaves the cluster")
		Expect(rsClients[candidate1].InstanceLeave([]string{prefill2})).To(BeNil())
		time.Sleep(time.Second * 1)

		By("[manage]kill candidate1, restart candidate2, candidate2 should detect prefill2 leave")
		c1Session.Stop()
		Expect(c2Session.Restart()).To(BeNil())
		WaitForLeaderElection(etcdClient, rsLeaderKey, candidate2, etcdRevoteTime)
		routes, err := rsClients[candidate2].InstanceRoutesFromClusterState()
		Expect(err).To(BeNil())
		Expect(routes).ShouldNot(HaveKey(prefill2))

		By("make prefill1 faulty")
		resp, err := mockInstanceClients[prefill1].MockError(500)
		Expect(err).To(BeNil())
		Expect(resp.StatusCode).To(Equal(200))
		resp.Body.Close()
		for i := 0; i < cfgConsecutiveFailureCount; i++ {
			reqId++
			resp, err = sendFastInferRequest(rsClients[worker1], leaderFaultyCtx+strconv.Itoa(reqId), 0)
			Expect(err).To(BeNil())
			Expect(resp.StatusCode).NotTo(Equal(http.StatusOK))
			resp.Body.Close()
		}

		time.Sleep(time.Millisecond * time.Duration((cfgFakeReqThreadhold+1)*cfgFakeReqInterval))
		instResp, err := rsClients[candidate2].Status()
		Expect(err).To(BeNil())
		Expect(instResp.InstanceStatus).Should(HaveKey("prefill"))
		Expect(instResp.InstanceStatus["prefill"]).To(ContainElement(HaveField("Status", "fault")))

		By("[manage]kill candidate2, restart candidate1, candidate1 should detect prefill1 faulty")
		c2Session.Stop()
		Expect(c1Session.Restart()).Should(BeNil())
		WaitForLeaderElection(etcdClient, rsLeaderKey, candidate1, etcdRevoteTime)
		time.Sleep(time.Millisecond * time.Duration((cfgFakeReqThreadhold+1)*cfgFakeReqInterval))
		instResp, err = rsClients[candidate1].Status()
		Expect(err).To(BeNil())
		Expect(instResp.InstanceStatus).Should(HaveKey("prefill"))
		//Expect(instResp.InstanceStatus["prefill"]).To(ContainElement(HaveField("Status", "fault")))

		By("prefill2 rejoins the cluster, candidate2 should detect it after recovery")
		Expect(rsClients[candidate1].InstanceJoin(map[string]*commoncfg.Route{prefill2: {
			Ip:   "127.0.0.1",
			Port: proxyPrefill2Port,
			Role: "prefill",
		}})).Should(BeNil())

		resp, err = mockInstanceClients[prefill1].RemoveMockError()
		Expect(err).To(BeNil())
		Expect(resp.StatusCode).To(Equal(200))
		resp.Body.Close()

		By("[manage]restart candidate2")
		Expect(c2Session.Restart()).Should(BeNil())
		time.Sleep(etcdRevoteTime)

		routes, err = rsClients[candidate2].InstanceRoutesFromClusterState()
		Expect(err).To(BeNil())
		Expect(routes).Should(HaveKey(prefill2))
	},
	)

	It("Network fault between candidate and p/d test_xpyd_rolesfault_054", func() {
		By("network fault between leader and decode, D will be isolated")
		lastCollectTotal, _, err := rsClients[candidate1].GetMetricsValue("rs:metrics_collection_failures_total", "")
		Expect(err).To(BeNil())

		instanceProxyRules[decode1].SetRule(func(r *http.Request) bool {
			if r.Header.Get(XRouterId) == candidate1 {
				return false
			}
			return true
		}, "block by proxy", 500)
		time.Sleep(time.Second * 10)
		reqId++
		resp, err := sendFastInferRequest(rsClients[worker1], leaderFaultyCtx+strconv.Itoa(reqId), 0)
		Expect(err).To(BeNil())
		Expect(resp.StatusCode).NotTo(Equal(http.StatusOK))
		resp.Body.Close()

		curCollectTotal, _, err := rsClients[candidate1].GetMetricsValue("rs:metrics_collection_failures_total", "")
		Expect(err).To(BeNil())
		Expect(curCollectTotal).Should(BeNumerically(">", lastCollectTotal))

	},
	)

	It("Network fault between candidates test_xpyd_rolesfault_056", func() {
		if !AllowIptables() {
			Skip(fmt.Sprintf("%s not set to true, skip iptables-based test", ROUTER_TEST_ENABLE_SU_ENV))
		}

		doneChan := make(chan error, 1)
		errChan := make(chan error, 1)
		ctx, cancel := context.WithCancel(context.Background())
		// Create ticker
		go func() {
			ticker := time.NewTicker(500 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					doneChan <- nil
					GinkgoWriter.Println("received external stop signal, exiting ticker...")
					return

				case err := <-errChan:
					if err != nil {
						GinkgoWriter.Printf("task error: %v, ticker emergency stop!\n", err)
						doneChan <- fmt.Errorf("timer stopped by task error: %w", err)
						return
					}

				case <-ticker.C:
					go func() {
						reqId++
						resp, err := sendFastInferRequest(rsClients[worker1], leaderFaultyCtx+strconv.Itoa(reqId), 5)
						if err != nil {
							select {
							case errChan <- err:
							default:
							}
							return
						}

						if resp.StatusCode != 200 {
							select {
							case errChan <- fmt.Errorf("status code is %v", resp.StatusCode):
							default:
							}
						}
					}()
				}
			}
		}()

		err := BlockTrafficToPort(strconv.Itoa(candidate1HTTPPort))
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() {
			UnblockTrafficToPort(strconv.Itoa(candidate1HTTPPort))
		})

		err = BlockTrafficToPort(strconv.Itoa(candidate1HTTPPort + 10))
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() {
			UnblockTrafficToPort(strconv.Itoa(candidate1HTTPPort + 10))
		})

		time.Sleep(12 * time.Second)
		cancel()
		stopReason := <-doneChan
		Expect(stopReason).Should(BeNil())
	},
	)
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
		done := make(chan struct{})
		go func() {
			defer close(done)
			GinkgoWriter.Printf("[etcd] Calling etcdServer.Close()...\n")
			etcdServer.Close()
		}()

		select {
		case <-done:
			GinkgoWriter.Printf("[etcd] Embedded etcd closed cleanly.\n")
		case <-time.After(3 * time.Second):
			GinkgoWriter.Printf("[etcd] WARNING: etcdServer.Close() stuck! Forcing cleanup...\n")

			// Force stop the underlying Server state machine (if exposed)
			if etcdServer.Server != nil {
				etcdServer.Server.Stop()
			}

		}
	})

})
