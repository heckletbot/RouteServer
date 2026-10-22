package route_server

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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
	XRouterId                 = "X-Router-Id"
	ROUTER_TEST_ENABLE_SU_ENV = "ROUTER_TEST_ENABLE_SU"
)

// InstanceConfigCtx describes the configuration context for a RouteServer instance.
// After providing the necessary information, call SetupRouteServerConfig to assemble the config and start the instance.
type InstanceConfigCtx struct {
	httpPort string
	// targetCfgDir should be an independent directory, e.g. filepath.Join(etcdCtxDir, "leader1")
	name          string
	targetCfgDir  string
	prefillsStr   string
	decodesStr    string
	etcdEndpoints string
	// When isLeader=true, config-tool args are candidate=true, remote-scheduler-type=disable;
	// When isLeader=false, config-tool args are candidate=false, remote-scheduler-type=leader
	isLeader    bool
	isSingleton bool
}

// rsInstance holds the process info and config path after startup, for use by test cases.
type rsInstance struct {
	Name     string
	Session  *gexec.Session
	CfgDir   string
	HTTPPort string
}

type RuleFunc func(r *http.Request) bool
type proxyRuleEntry struct {
	disableConnByRole map[string]bool
	rule              RuleFunc
	blockMsg          string
	statusCode        int
	mu                sync.RWMutex
}

func (e *proxyRuleEntry) SetRule(fn RuleFunc, msg string, code int) {
	e.mu.Lock()
	e.rule = fn
	e.blockMsg = msg
	e.statusCode = code
	e.mu.Unlock()
}

func (e *proxyRuleEntry) Allow(r *http.Request) bool {
	e.mu.RLock()
	fn := e.rule
	e.mu.RUnlock()
	if fn == nil {
		return true
	}
	return fn(r)
}

func (e *proxyRuleEntry) Clear() {
	e.mu.Lock()
	e.rule = nil
	e.disableConnByRole = make(map[string]bool)
	e.mu.Unlock()
}

func (e *proxyRuleEntry) IsBlockConn(role string) bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.disableConnByRole[role]
}

func (e *proxyRuleEntry) BlockConn(role string) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	e.disableConnByRole[role] = true
}

func startProxy(listen, upstreamAddr string, ruleEntry *proxyRuleEntry) error {
	upstream, err := url.Parse(upstreamAddr)
	if err != nil {
		return err
	}
	proxy := httputil.NewSingleHostReverseProxy(upstream)

	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		req.Host = upstream.Host
	}

	proxy.Transport = &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !ruleEntry.Allow(r) {
			http.Error(w, ruleEntry.blockMsg, ruleEntry.statusCode)
			return
		}
		proxy.ServeHTTP(w, r)
	})

	return http.ListenAndServe(listen, handler)
}

func isPortAvailable(port int) bool {
	address := ":" + strconv.Itoa(port)
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return false
	}

	if listener.Close() != nil {
		return false
	}
	return true
}

func StartTCPProxy(listenAddr, targetAddr string, opts *etcdProxyRule) error {
	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		Expect(err).To(BeNil())
	}
	for {
		in, err := ln.Accept()
		if err != nil {
			Expect(err).To(BeNil())
		}
		go func() {
			out, err := net.Dial("tcp", targetAddr)
			if err != nil {
				Expect(err).To(BeNil())
			}
			// Bidirectional copy (delay/packet-loss/rate-limit logic can be injected in between)
			go proxyConn(in, out, opts)
			go proxyConn(out, in, opts)
		}()
	}
}

func proxyConn(src net.Conn, dst net.Conn, opts *etcdProxyRule) error {
	buf := make([]byte, 4096)
	for {
		n, err := src.Read(buf)
		if err != nil {
			err2 := dst.Close()
			return fmt.Errorf("err1:%v,err2:%v", err, err2)
		}

		// Network fault injection point
		if opts.Delay > 0 {
			time.Sleep(opts.Delay)
		}
		if opts.Drop.Load() {
			dst.Close()
			// Directly close the connection; returning nil means no response
			return src.Close()
		}

		_, err = dst.Write(buf[:n])
		if err != nil {
			return err
		}

	}
}

type etcdProxyRule struct {
	Drop       atomic.Bool
	Delay      time.Duration
	BlockList  []string
	ForceReset bool
}

func (r *etcdProxyRule) Clear() {
	r.Drop.Store(false)
}

// SetupRouteServerConfig assembles the full configuration for a RouteServer instance via InstanceConfigCtx and starts the process.
// This includes: creating directory structure, calling config-tool to generate config, copying pipelines and metrics config files, starting the process and waiting for ready.
//
// Usage:
//
//	ctx := InstanceConfigCtx{
//	    httpPort:      "25879",
//	    name:          "leader1",
//	    targetCfgDir:  filepath.Join(etcdCtxDir, "leader1"),
//	    prefillsStr:   "prefill1#127.0.0.1:25890,prefill2#127.0.0.1:25891",
//	    decodesStr:    "decode1#127.0.0.1:25892",
//	    etcdEndpoints: "127.0.0.1:25893",
//	    isLeader:      true,
//	}
//	inst := SetupRouteServerConfig(ctx, deployPath, "PD", "pipeline")
func SetupRouteServerConfig(ctx InstanceConfigCtx, deployPath, templateFile, inferMode, inferModuleVersion string) rsInstance {
	cfgDir := filepath.Join(ctx.targetCfgDir, "config")

	By(fmt.Sprintf("create config directory for %s: %s", ctx.name, ctx.targetCfgDir))
	Expect(os.RemoveAll(ctx.targetCfgDir)).To(BeNil())
	Expect(os.MkdirAll(cfgDir, 0755)).To(BeNil())

	// Determine config parameters based on isLeader
	remoteSchedulerType := "leader"
	candidate := "false"
	if ctx.isLeader {
		candidate = "true"
	}

	if templateFile == "" {
		templateFile = filepath.Join(deployPath, ROUTE_SERVER_TEMPLATE)
	}

	mode := ""
	if ctx.isSingleton {
		mode = "singleton"
	} else {
		mode = "etcd"
	}

	By(fmt.Sprintf("generate config via config-tool for %s", ctx.name))
	toolParam := DisableHTTPSConfig([]string{
		"-template", templateFile,
		"-host", "127.0.0.1",
		"-target", ctx.targetCfgDir,
		"-infer-mode", inferMode,
		"-prefills", ctx.prefillsStr,
		"-decodes", ctx.decodesStr,
		"-name", ctx.name,
		"-mode", mode,
		"-remote-scheduler-type", remoteSchedulerType,
		"-port", ctx.httpPort,
		"-candidate", candidate,
		"-etcd-endpoints", ctx.etcdEndpoints,
		"-infer-module-version", inferModuleVersion,
	})
	out, err := exec.Command(configToolPath, toolParam...).CombinedOutput()
	GinkgoWriter.Printf("[config] %s config-tool output: %s\n", ctx.name, string(out))
	Expect(err).To(BeNil())

	By(fmt.Sprintf("copy pipelines config for %s", ctx.name))
	pipelinesDir := filepath.Join(cfgDir, "pipelines")
	Expect(os.MkdirAll(pipelinesDir, 0755)).To(BeNil())
	Expect(copyDir(filepath.Join(deployPath, "pipelines"), pipelinesDir)).To(BeNil())

	By(fmt.Sprintf("copy metrics config for %s", ctx.name))
	metricsCollectorConfigFileName := "metrics_config_vllm_0.11.0.yaml"
	Expect(copyFile(
		filepath.Join(deployPath, metricsCollectorConfigFileName),
		filepath.Join(cfgDir, metricsCollectorConfigFileName),
	)).To(BeNil())

	// Start the process
	By(fmt.Sprintf("start %s route-server on port %s", ctx.name, ctx.httpPort))
	env := os.Environ()
	env = append(env, fmt.Sprintf("%s=%s", constant.EnvServerConfigPath, cfgDir))
	command := exec.Command(rsPath)
	command.Env = env
	sess, startErr := gexec.Start(command, io.Discard, GinkgoWriter)
	Expect(startErr).NotTo(HaveOccurred())

	// Wait for ready
	Eventually(sess.Out, 30*time.Second).Should(gbytes.Say("Start HTTP server on"))
	GinkgoWriter.Printf("[%s] route-server is ready on port %s\n", ctx.name, ctx.httpPort)

	return rsInstance{
		Name:     ctx.name,
		Session:  sess,
		CfgDir:   cfgDir,
		HTTPPort: ctx.httpPort,
	}
}

// StartMockVLLMInstances starts a group of mock vllm instances.
func StartMockVLLMInstances(instances []struct{ Port, Name string }) []*gexec.Session {
	var sessions []*gexec.Session
	for _, inst := range instances {
		params := []string{
			"-addr", "127.0.0.1",
			"-port", inst.Port,
			"-name", inst.Name,
		}
		command := exec.Command(mockVLLMPath, params...)
		sess, startErr := gexec.Start(command, io.Discard, GinkgoWriter)
		Expect(startErr).NotTo(HaveOccurred())
		sessions = append(sessions, sess)
		GinkgoWriter.Printf("[mock] started %s on port %s\n", inst.Name, inst.Port)
	}
	return sessions
}

// CleanupSessions batch cleans up gexec sessions.
func CleanupSessions(sessions []*gexec.Session) {
	for _, s := range sessions {
		if s != nil {
			s.Kill()
			s.Wait()
		}
	}
}

// WaitForLeaderElection waits for a leader to be elected in etcd, where the key contains the name identifier.
func WaitForLeaderElection(etcdClient *clientv3.Client, leaderKey, name string, timeout time.Duration) {
	Eventually(func(g Gomega) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		resp, getErr := etcdClient.Get(ctx, leaderKey, clientv3.WithPrefix())

		// Condition 1: check if network/client reported an error
		g.Expect(getErr).NotTo(HaveOccurred(), "failed to get data from etcd: %v", getErr)

		// Condition 2: check if there is data
		g.Expect(resp.Kvs).NotTo(BeEmpty(), "etcd has no leaderKey: %s", leaderKey)

		// Condition 3: check if the elected Leader name matches
		actualLeader := string(resp.Kvs[0].Value)
		g.Expect(actualLeader).To(ContainSubstring(name), "current Leader is '%s', but expected '%s'", actualLeader, name)

	}, timeout, 1*time.Second).Should(Succeed())
	GinkgoWriter.Printf("[etcd] %s elected, confirmed via %s\n", name, leaderKey)
}

// WaitForWorkerRegistration waits for a worker to register in etcd, where the key contains the name identifier.
func WaitForWorkerRegistration(etcdClient *clientv3.Client, peersDir, name string, timeout time.Duration) {
	Eventually(func() bool {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		resp, getErr := etcdClient.Get(ctx, peersDir, clientv3.WithPrefix())
		if getErr != nil || len(resp.Kvs) == 0 {
			return false
		}
		for _, kv := range resp.Kvs {
			if strings.Contains(string(kv.Key), name) {
				return true
			}
		}
		return false
	}, timeout, 1*time.Second).Should(BeTrue(), "%s should be registered in etcd", name)
	GinkgoWriter.Printf("[etcd] %s registered, confirmed via %s\n", name, peersDir)
}

// ensureOnePort checks and cleans up processes on a single port.
func ensureOnePort(p string, wg *sync.WaitGroup) {
	if wg != nil {
		defer wg.Done()
	}
	var pidArgs []string
	if os.PathSeparator == '\\' {
		// Windows: find PID listening on the port via netstat
		pidArgs = []string{"netstat", "-ano", "|", "findstr", fmt.Sprintf(":%s ", p)}
	} else {
		pidArgs = []string{"sh", "-c", fmt.Sprintf("lsof -ti tcp:%s 2>/dev/null", p)}
	}
	out, _ := exec.Command(pidArgs[0], pidArgs[1:]...).CombinedOutput()
	pidStr := strings.TrimSpace(string(out))
	if pidStr == "" {
		return
	}
	if os.PathSeparator == '\\' {
		// netstat output: "  TCP    0.0.0.0:2379   0.0.0.0:0   LISTENING    1234"
		for _, line := range strings.Split(pidStr, "\n") {
			fields := strings.Fields(line)
			if len(fields) >= 5 {
				pid := fields[len(fields)-1]
				GinkgoWriter.Printf("[cleanup] killing pid %s holding port %s\n", pid, p)
				exec.Command("taskkill", "/F", "/PID", pid).Run()
			}
		}
	} else {
		pids := strings.Fields(pidStr)
		for _, pid := range pids {
			GinkgoWriter.Printf("[cleanup] killing pid %s holding port %s\n", pid, p)
			exec.Command("kill", "-9", pid).Run()
		}
	}
}

// EnsurePorts concurrently checks and cleans up processes occupying all given ports.
func EnsurePorts(givenPorts []string) {
	var wg sync.WaitGroup
	for _, p := range givenPorts {
		wg.Add(1)
		go ensureOnePort(p, &wg)
	}
	wg.Wait()
}

// BlockTrafficToPort blocks all inbound TCP traffic to the target port on the local machine.
// Used to simulate leader port unreachable (worker cannot send requests to leader).
// Requires root privileges, controlled by environment variable ENABLE_SU=true.
func BlockTrafficToPort(targetPort string) error {
	if os.PathSeparator == '\\' {
		return fmt.Errorf("BlockTrafficToPort not supported on Windows")
	}
	cmd := exec.Command("sudo", "iptables", "-A", "INPUT", "-p", "tcp", "--dport", targetPort, "-j", "DROP")
	out, err := cmd.CombinedOutput()
	GinkgoWriter.Printf("[iptables] block INPUT -> :%s, output: %s\n", targetPort, string(out))
	return err
}

// UnblockTrafficToPort restores traffic blocked by BlockTrafficToPort.
func UnblockTrafficToPort(targetPort string) error {
	if os.PathSeparator == '\\' {
		return fmt.Errorf("UnblockTrafficToPort not supported on Windows")
	}
	cmd := exec.Command("sudo", "iptables", "-D", "INPUT", "-p", "tcp", "--dport", targetPort, "-j", "DROP")
	out, err := cmd.CombinedOutput()
	GinkgoWriter.Printf("[iptables] unblock INPUT -> :%s, output: %s\n", targetPort, string(out))
	return err
}

func AllowIptables() bool {
	return os.Getenv(ROUTER_TEST_ENABLE_SU_ENV) == "true"
}

// -------------------------------------------------------
// etcd common operations
// -------------------------------------------------------

// StartEmbeddedEtcd starts an embedded etcd instance, returning the etcd server and client.
// Parameters: clientPort (client port), peerPort (inter-node communication port), name (etcd node name)
func StartEmbeddedEtcd(clientPort, peerPort, name string) (*embed.Etcd, *clientv3.Client) {
	etcdCfg := embed.NewConfig()
	etcdCfg.Name = name
	etcdCfg.Dir = filepath.Join(os.TempDir(), fmt.Sprintf("%s-%d", name, time.Now().UnixNano()))
	etcdCfg.ListenPeerUrls = []url.URL{{Scheme: "http", Host: fmt.Sprintf("127.0.0.1:%s", peerPort)}}
	etcdCfg.ListenClientUrls = []url.URL{{Scheme: "http", Host: fmt.Sprintf("127.0.0.1:%s", clientPort)}}
	etcdCfg.AdvertisePeerUrls = []url.URL{{Scheme: "http", Host: fmt.Sprintf("127.0.0.1:%s", peerPort)}}
	etcdCfg.AdvertiseClientUrls = []url.URL{{Scheme: "http", Host: fmt.Sprintf("127.0.0.1:%s", clientPort)}}
	etcdCfg.InitialCluster = fmt.Sprintf("%s=http://127.0.0.1:%s", name, peerPort)
	etcdCfg.InitialClusterToken = fmt.Sprintf("%s-token", name)
	etcdCfg.ClusterState = embed.ClusterStateFlagNew
	etcdCfg.LogLevel = "error"

	etcdServer, err := embed.StartEtcd(etcdCfg)
	Expect(err).NotTo(HaveOccurred())
	select {
	case <-etcdServer.Server.ReadyNotify():
		GinkgoWriter.Printf("[etcd] embedded etcd server ready\n")
	case <-time.After(10 * time.Second):
		Fail("etcd server did not become ready within 10s")
	}

	cli, err := clientv3.New(clientv3.Config{
		Endpoints:   []string{fmt.Sprintf("127.0.0.1:%s", clientPort)},
		DialTimeout: 5 * time.Second,
	})
	Expect(err).NotTo(HaveOccurred())
	GinkgoWriter.Printf("[etcd] client connected to 127.0.0.1:%s\n", clientPort)

	return etcdServer, cli
}

type sessionWrapper struct {
	psession **gexec.Session
	args     []string
	env      []string
	extraEnv []string
}

func newSessionWrapper(psession **gexec.Session, extraEnv []string) *sessionWrapper {
	if psession == nil {
		return nil
	}

	return &sessionWrapper{
		psession: psession,
		args:     (*psession).Command.Args,
		env:      (*psession).Command.Env,
		extraEnv: extraEnv,
	}
}
func (w *sessionWrapper) Stop() {
	session := *w.psession
	session.Kill()
	session.Wait()
}

func (w *sessionWrapper) Restart() error {
	command := exec.Command(w.args[0], w.args[1:]...)
	command.Env = append(w.env, w.extraEnv...)
	session, err := gexec.Start(command, io.Discard, os.Stderr)
	if err != nil {
		return err
	}
	*(w.psession) = session
	return nil
}
