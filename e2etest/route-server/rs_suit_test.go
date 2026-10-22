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
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gexec"
)

func DisableHTTPSConfig(params []string) []string {
	return append(params,
		"-enable-https", "false",
		"-ops-enable-https", "false",
		"-client-https-enable", "false",
		"-etcd-tls-enable", "false",
	)
}

const (
	ROUTE_SERVER_TEMPLATE = "model-orch-template.yaml"
	TEMP_TEMPLATE         = "temp-template.yaml"
)

var (
	rsPath         string
	mockVLLMPath   string
	configToolPath string

	projectRoot string
)

func TestRouteServer(t *testing.T) {
	RegisterFailHandler(Fail)

	// Get default configuration
	suiteConfig, reporterConfig := GinkgoConfiguration()

	// Force enable verbose mode in code (equivalent to adding -v on command line)
	reporterConfig.Verbose = true

	RunSpecs(t, "RouteServer Suite", suiteConfig, reporterConfig)
}

var _ = BeforeSuite(func() {
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

})

var _ = AfterSuite(func() {
	// cleanup
	gexec.CleanupBuildArtifacts()
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
	*psession, err = gexec.Start(command, GinkgoWriter, os.Stderr)
	return err
}
