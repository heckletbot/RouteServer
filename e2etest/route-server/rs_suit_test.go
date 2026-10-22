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
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"testing"
	"time"

	"infer-dp-scheduler/pkg/common/constant"
	"infer-dp-scheduler/pkg/route-server/instances"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gbytes"
	"github.com/onsi/gomega/gexec"
)

func DisableHTTPSConfig(params []string) []string {
	return append(params,
		"-enable-https", "false",
		"-ops-enable-https", "false",
		"-client-https-enable", "false",
		"-etcd-tls-enable", "false",
		"-enable-ops-apis-on-service-port", "true",
	)
}

const (
	ROUTE_SERVER_TEMPLATE = "route-server-template.yaml"
	TEMP_TEMPLATE       = "temp-template.yaml"
)

var (
	rsPath         string
	mockVLLMPath   string
	configToolPath string

	projectRoot string
)

type SuiteData struct {
	RsPath         string
	MockVLLMPath   string
	ConfigToolPath string
	ProjectRoot    string
}

func TestRouteServer(t *testing.T) {
	RegisterFailHandler(Fail)

	// Get default configuration
	suiteConfig, reporterConfig := GinkgoConfiguration()

	// Force enable verbose mode in code (equivalent to adding -v on command line)
	reporterConfig.Verbose = true

	RunSpecs(t, "RouteServer Suite", suiteConfig, reporterConfig)
}

var _ = SynchronizedBeforeSuite(func() []byte {
	var err error
	projectRoot = getProjectRoot()

	By("build route-server")
	rsPath, err = gexec.Build("infer-dp-scheduler/cmd/route_server")
	Expect(err).NotTo(HaveOccurred())

	By("build mock vllm")
	mockVLLMPath, err = gexec.Build("infer-dp-scheduler/cmd/mock_server")
	Expect(err).NotTo(HaveOccurred())

	By("build config-tool")
	configToolPath, err = gexec.Build("infer-dp-scheduler/cmd/config-tool")
	Expect(err).NotTo(HaveOccurred())

	data := SuiteData{
		RsPath:         rsPath,
		MockVLLMPath:   mockVLLMPath,
		ConfigToolPath: configToolPath,
		ProjectRoot:    projectRoot,
	}

	// Serialize to bytes and pass to all processes
	bytes, err := json.Marshal(data)
	Expect(err).NotTo(HaveOccurred())
	return bytes

}, func(bytes []byte) {
	// [Phase 2: All Nodes (1, 2, 3, 4) execute]
	var data SuiteData
	err := json.Unmarshal(bytes, &data)
	Expect(err).NotTo(HaveOccurred())

	// Assign to global variables in each process's own memory
	rsPath = data.RsPath
	mockVLLMPath = data.MockVLLMPath
	configToolPath = data.ConfigToolPath
	projectRoot = data.ProjectRoot

	fmt.Printf(">>> [Node %d], local initialization complete\n", GinkgoParallelProcess())
})

var _ = SynchronizedAfterSuite(func() {
	// [Function 1]: All-process cleanup phase (Process 1, 2, 3, 4 all execute)
	fmt.Printf(">>> [Node %d] finished\n", GinkgoParallelProcess())

}, func() {
	// [Function 2]: Global cleanup phase (only Process 1 executes)
	// After all processes' [Function 1] complete, Process 1 handles final cleanup.
	gexec.CleanupBuildArtifacts()
	fmt.Println(">>> [Node 1] Performing global teardown, runs once!")
})

// getProjectRoot gets project root directory (by finding go.mod file)
func getProjectRoot() string {
	_, filename, _, _ := runtime.Caller(0)
	dir := filepath.Dir(filename)

	// Search upward for go.mod file
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}

		parent := filepath.Dir(dir)
		if parent == dir { // Reached root directory
			break
		}
		dir = parent
	}

	return dir
}

// copyFile copies a file
func copyFile(src, dst string) error {
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	_, err = io.Copy(out, in)
	return err
}

// copyDir recursively copies a directory
func copyDir(src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		srcPath := filepath.Join(src, entry.Name())
		dstPath := filepath.Join(dst, entry.Name())

		if entry.IsDir() {
			err = os.MkdirAll(dstPath, 0755)
			if err != nil {
				return err
			}
			err = copyDir(srcPath, dstPath)
			if err != nil {
				return err
			}
		} else {
			err = copyFile(srcPath, dstPath)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func restartProcess(psession **gexec.Session) error {
	var err error
	session := (*psession)
	params := session.Command.Args
	session.Kill()
	session.Wait()
	time.Sleep(time.Second * 2)
	command := exec.Command(params[0], params[1:]...)
	*psession, err = gexec.Start(command, io.Discard, os.Stderr)
	return err
}

func checkRsListenSucceess(session *gexec.Session) {
	Eventually(session.Out, 5*time.Second).Should(gbytes.Say("Start HTTP server on"))
	Eventually(session.Out, 5*time.Second).Should(gbytes.Say("Start OPS HTTP server on"))
	// All log levels of RS are output to StdOut
	Consistently(session.Out, 5*time.Second).ShouldNot(gbytes.Say("failed to init server"))
	Consistently(session.Out, 5*time.Second).ShouldNot(gbytes.Say("ops server error"))
}

func checkMockVllmListenSucceess(session *gexec.Session) {
	Eventually(session.Out, 5*time.Second).Should(gbytes.Say("start.*server, listen and serve at"))
	Consistently(session.Err, 5*time.Second).ShouldNot(gbytes.Say("mock engine listen error"))
}

type HealthStatus string

const (
	HealthStatusUnavailable HealthStatus = "unavailable"
	HealthStatusAvailable   HealthStatus = "available"
)

func WaitForInstanceHealthStatus(leaderSession *gexec.Session, instanceID string, endpoint string, expectedStatus HealthStatus, timeout time.Duration) {
	desc := "unavailable"
	if expectedStatus == HealthStatusAvailable {
		desc = "available"
	}
	pattern := regexp.MustCompile(fmt.Sprintf(`instance \[%s\] .* move to %s status`, regexp.QuoteMeta(endpoint), desc))
	var lastIdx int
	Eventually(func() bool {
		content := leaderSession.Out.Contents()
		if len(content) > lastIdx {
			matched := pattern.Match(content[lastIdx:])
			lastIdx = len(content)
			return matched
		}
		return false
	}, timeout).Should(BeTrue())
}

// -------------------------------------------------------
// Status verification
// -------------------------------------------------------

// VerifyInstanceStatus verifies RS leader's /status API returns the specified cluster status and instance counts.
//   - client: RSClient instance
//   - expectedStatus: Expected cluster status (running/concerning/fault/deploying)
//   - runningPrefill: Expected running prefill instance count
//   - runningDecode: Expected running decode instance count
func VerifyInstanceStatus(client *RSClient, expectedStatus string, runningPrefill, runningDecode int) {
	statusResp, err := client.GetStatus()
	Expect(err).NotTo(HaveOccurred())
	defer statusResp.Body.Close()
	Expect(statusResp.StatusCode).To(Equal(http.StatusOK))
	body, _ := io.ReadAll(statusResp.Body)
	GinkgoWriter.Printf("[verify] /status response: %s\n", string(body))

	var statusRespObj instances.InstanceStatusRsp
	Expect(json.Unmarshal(body, &statusRespObj)).To(BeNil())
	Expect(statusRespObj.Status).To(Equal(expectedStatus),
		"status should be %s", expectedStatus)

	prefillRunning := 0
	decodeRunning := 0
	for role, instances := range statusRespObj.InstanceStatus {
		for _, inst := range instances {
			if inst.Status == constant.RunningStatus {
				switch role {
				case "prefill":
					prefillRunning++
				case "decode":
					decodeRunning++
				}
			}
		}
	}
	Expect(prefillRunning).To(Equal(runningPrefill),
		"prefill running instance count should be %d", runningPrefill)
	Expect(decodeRunning).To(Equal(runningDecode),
		"decode running instance count should be %d", runningDecode)
}
