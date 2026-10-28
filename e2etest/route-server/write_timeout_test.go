package route_server

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/goccy/go-yaml"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gbytes"
	"github.com/onsi/gomega/gexec"

	"infer-dp-scheduler/cmd/route_server/server/options"
	commoncfg "infer-dp-scheduler/pkg/common/config"
	"infer-dp-scheduler/pkg/common/constant"
	"infer-dp-scheduler/pkg/common/openai"
)

const (
	writeTimeoutTestCtx     = "write-timeout-test-context"
	writeTimeoutListenPort  = "40600"
	writeTimeoutOpsPort     = "40610"
	writeTimeoutPrefillPort = "40601"
	writeTimeoutDecodePort  = "40602"
	writeTimeoutTokenSize   = 524288
	writeTimeoutMs          = 10
)

var _ = Describe(writeTimeoutTestCtx, Ordered, func() {
	var (
		session       *gexec.Session
		err           error
		mockServers   []*gexec.Session
		prefillServer *gexec.Session
		decodeServer  *gexec.Session
	)

	BeforeAll(func() {
		EnsurePorts([]string{writeTimeoutListenPort, writeTimeoutOpsPort, writeTimeoutPrefillPort, writeTimeoutDecodePort})
		By("copy pipelines config")
		configPath := filepath.Join(projectRoot, "e2etest", "environment", writeTimeoutTestCtx, "config")
		Expect(os.MkdirAll(filepath.Join(configPath, "pipelines"), 0755)).To(BeNil())

		deployPath := filepath.Join(projectRoot, "deploy")
		Expect(copyDir(filepath.Join(deployPath, "pipelines"), filepath.Join(configPath, "pipelines"))).To(BeNil())

		By("gen route-server config")
		toolParam := DisableHTTPSConfig([]string{
			"-template", filepath.Join(deployPath, "route-server-template.yaml"),
			"-host", "127.0.0.1",
			"-target", filepath.Join(projectRoot, "e2etest", "environment", writeTimeoutTestCtx),
			"-infer-mode", "PD",
			"-prefills", fmt.Sprintf("prefill1#127.0.0.1:%s", writeTimeoutPrefillPort),
			"-decodes", fmt.Sprintf("decode1#127.0.0.1:%s", writeTimeoutDecodePort),
			"-name", "route-server-write-timeout-test",
			"-remote-scheduler-type", "disable",
			"-port", writeTimeoutListenPort,
			"-mode", "singleton",
			"-infer-module-version", "pipeline",
		})
		Expect(exec.Command(configToolPath, toolParam...).Run()).To(BeNil())

		By("inject client-write-timeout-ms config")
		configFile := filepath.Join(configPath, "route-server.yaml")
		Expect(InjectWriteTimeoutConfig(configFile, writeTimeoutMs)).To(BeNil())

		Expect(os.Setenv(constant.EnvServerConfigPath, configPath)).To(BeNil())
		Expect(options.InitConfig()).To(BeNil())

		By("cp metrics config")
		metricsCollectorConfigFileName := options.GetConfig().MetricsCfg.MetricsCollectorConfigFileName
		Expect(copyFile(filepath.Join(deployPath, metricsCollectorConfigFileName), filepath.Join(configPath, metricsCollectorConfigFileName))).
			To(BeNil())

		By("start prefill mock vllm (normal)")
		prefillCommand := exec.Command(mockVLLMPath,
			"-addr", "127.0.0.1",
			"-port", writeTimeoutPrefillPort,
		)
		prefillServer, err = gexec.Start(prefillCommand, io.Discard, os.Stderr)
		Expect(err).NotTo(HaveOccurred())
		mockServers = append(mockServers, prefillServer)

		By("start decode mock vllm with large token size and zero tpot")
		decodeCommand := exec.Command(mockVLLMPath,
			"-addr", "127.0.0.1",
			"-port", writeTimeoutDecodePort,
			"-token-size", fmt.Sprintf("%d", writeTimeoutTokenSize),
			"-tpot", "0",
		)
		decodeServer, err = gexec.Start(decodeCommand, io.Discard, os.Stderr)
		Expect(err).NotTo(HaveOccurred())
		mockServers = append(mockServers, decodeServer)

		By("start route-server")
		command := exec.Command(rsPath)
		session, err = gexec.Start(command, io.Discard, os.Stderr)
		Expect(err).NotTo(HaveOccurred())

		By("wait for readiness")
		Eventually(session.Out, 5*time.Second).Should(gbytes.Say("Start HTTP server on"))
		time.Sleep(2 * time.Second)
	})

	AfterAll(func() {
		By("destroy rs")
		session.Kill()
		session.Wait()
		By("destroy mock vllm")
		for _, mock := range mockServers {
			mock.Kill()
			mock.Wait()
		}
	})

	BeforeEach(func() {
		Eventually(session.Out).Should(gbytes.Say(".*"))
	})

	Context("NetTimeoutWriter stream write timeout", func() {
		var clientRs *RSClient

		BeforeEach(func() {
			clientRs = NewRSClient("http://localhost:" + writeTimeoutListenPort)
		})

		It("stream write timeout when client does not read and decode outputs large tokens", func() {
			By("send stream request with large maxTokens and do NOT read response body")
			reqBody := openai.ChatCompletionRequestV1{
				Model: "pangu",
				Messages: []openai.ChatCompletionMessage{{
					Role:    "user",
					Content: "hello",
				}},
				MaxTokens: 100,
				Stream:    true,
			}

			resp, err := clientRs.SendChatCompletion(context.Background(), reqBody, nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(resp.StatusCode).To(Equal(http.StatusOK))

			By("do NOT read body - TCP send buffer fills up, NetTimeoutWriter deadline triggers")
			time.Sleep(3 * time.Second)
			resp.Body.Close()

			By("verify NetTimeoutWriter flush/write error or event write timeout in log")
			Eventually(session.Out, 10*time.Second).Should(
				SatisfyAny(
					gbytes.Say("NetTimeoutWriter flush err"),
					gbytes.Say("NetTimeoutWriter write err"),
					gbytes.Say("event write timeout"),
				),
				"should log NetTimeoutWriter error when write deadline exceeded",
			)
		})

		It("stream write timeout produces ClientError", func() {
			By("send stream request and ignore response body")
			reqBody := openai.ChatCompletionRequestV1{
				Model: "pangu",
				Messages: []openai.ChatCompletionMessage{{
					Role:    "user",
					Content: "hello",
				}},
				MaxTokens: 100,
				Stream:    true,
			}

			resp, err := clientRs.SendChatCompletion(context.Background(), reqBody, nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(resp.StatusCode).To(Equal(http.StatusOK))

			time.Sleep(3 * time.Second)
			resp.Body.Close()

			By("verify ClientError log with client IP")
			Eventually(session.Out, 10*time.Second).Should(
				SatisfyAny(
					gbytes.Say("failed to write to client"),
					gbytes.Say("NetTimeoutWriter flush err"),
					gbytes.Say("NetTimeoutWriter write err"),
					gbytes.Say("event write timeout"),
				),
				"should log ClientError after write timeout",
			)
		})

		It("non-stream request completes normally with large token size", func() {
			reqBody := openai.ChatCompletionRequestV1{
				Model: "pangu",
				Messages: []openai.ChatCompletionMessage{{
					Role:    "user",
					Content: "hello",
				}},
				MaxTokens: 10,
				Stream:    false,
			}

			resp, err := clientRs.SendChatCompletion(TimeoutCtx(context.Background(), 30*time.Second), reqBody, nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(resp.StatusCode).To(Equal(http.StatusOK))

			defer resp.Body.Close()
			var result openai.ChatCompletionResponse
			Expect(ParseResponse(resp, &result)).To(BeNil())
			Expect(result.Choices).NotTo(BeEmpty())
		})

		It("stream request completes normally when client reads all data fast enough", func() {
			By("use small maxTokens so total data fits within TCP buffer + timeout")
			reqBody := openai.ChatCompletionRequestV1{
				Model: "pangu",
				Messages: []openai.ChatCompletionMessage{{
					Role:    "user",
					Content: "hello",
				}},
				MaxTokens: 2,
				Stream:    true,
			}

			resp, err := clientRs.SendChatCompletion(TimeoutCtx(context.Background(), 30*time.Second), reqBody, nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(resp.StatusCode).To(Equal(http.StatusOK))

			defer resp.Body.Close()
			respBody, err := io.ReadAll(resp.Body)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(respBody)).To(ContainSubstring("data:"))
		})
	})
})

func InjectWriteTimeoutConfig(configPath string, writeTimeoutMs int64) error {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return err
	}
	var cfg commoncfg.Config
	if err = yaml.Unmarshal(data, &cfg); err != nil {
		return err
	}

	cfg.ServerConfig.ClientWriteTimeoutMs = writeTimeoutMs

	data, err = yaml.Marshal(&cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(configPath, data, 0644)
}
