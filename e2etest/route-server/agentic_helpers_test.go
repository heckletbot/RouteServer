/*
 * Copyright 2026 The RouteServer Authors.
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
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/goccy/go-yaml"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gbytes"
	"github.com/onsi/gomega/gexec"
	"route-server/cmd/route_server/server/options"
	"route-server/pkg/common/constant"
)

const (
	// dispatchWait bounds "the request was dispatched and the dispatch line is
	// in the log".
	dispatchWait = 10 * time.Second
	// statusWait bounds "the worker reached the expected health status".
	statusWait = 30 * time.Second
	// logWait bounds "a request-scoped log line appeared".
	logWait = 15 * time.Second
)

// ensureAgenticScheduler finds the scheduler entry for the given role and
// replaces its scheduler-name with "agentic-balancer" if no agentic-balancer
// entry already exists. This allows reusing the config-tool generated
// route-server.yaml (which uses abc6/apc by default).
func ensureAgenticScheduler(configFile, targetRole string) error {
	data, err := os.ReadFile(configFile)
	if err != nil {
		return err
	}

	var cfgMap map[string]interface{}
	if err := yaml.Unmarshal(data, &cfgMap); err != nil {
		return err
	}

	smRaw, ok := cfgMap["scheduler-manager"].(map[string]interface{})
	if !ok {
		return fmt.Errorf("scheduler-manager not found in config")
	}
	schedulers, ok := smRaw["schedulers"].([]interface{})
	if !ok {
		return fmt.Errorf("schedulers list not found")
	}

	for _, s := range schedulers {
		entry, ok := s.(map[string]interface{})
		if !ok {
			continue
		}
		name, _ := entry["scheduler-name"].(string)
		if name == "agentic-balancer" {
			return nil
		}
	}

	for _, s := range schedulers {
		entry, ok := s.(map[string]interface{})
		if !ok {
			continue
		}
		role, _ := entry["instance-role"].(string)
		if role == targetRole {
			entry["scheduler-name"] = "agentic-balancer"
			entry["params"] = map[string]interface{}{
				"target-role": targetRole,
			}
			out, err := yaml.Marshal(cfgMap)
			if err != nil {
				return err
			}
			return os.WriteFile(configFile, out, 0644)
		}
	}
	return fmt.Errorf("scheduler entry for role %s not found", targetRole)
}

// injectAgenticParams merges the given params into the agentic-balancer
// scheduler entry (existing keys are overwritten, new keys are added).
func injectAgenticParams(configFile string, overrides map[string]string) error {
	data, err := os.ReadFile(configFile)
	if err != nil {
		return err
	}

	var cfgMap map[string]interface{}
	if err := yaml.Unmarshal(data, &cfgMap); err != nil {
		return err
	}

	smRaw, ok := cfgMap["scheduler-manager"].(map[string]interface{})
	if !ok {
		return fmt.Errorf("scheduler-manager not found in config")
	}
	schedulers, ok := smRaw["schedulers"].([]interface{})
	if !ok {
		return fmt.Errorf("schedulers list not found")
	}

	found := false
	for _, s := range schedulers {
		entry, ok := s.(map[string]interface{})
		if !ok {
			continue
		}
		name, _ := entry["scheduler-name"].(string)
		if name != "agentic-balancer" {
			continue
		}
		params, ok := entry["params"].(map[string]interface{})
		if !ok {
			params = make(map[string]interface{})
		}
		for k, v := range overrides {
			params[k] = v
		}
		entry["params"] = params
		found = true
	}
	if !found {
		return fmt.Errorf("agentic-balancer scheduler entry not found")
	}

	out, err := yaml.Marshal(cfgMap)
	if err != nil {
		return err
	}
	return os.WriteFile(configFile, out, 0644)
}

// injectMonitorConfig enables the prometheus monitor client in the
// route-server config so that per-instance vllm metrics (running requests,
// waiting requests, KV cache usage) are scraped and fed to the scheduler.
// prometheusConfigPath is an absolute, clean file path.
func injectMonitorConfig(configFile, prometheusConfigPath string) error {
	data, err := os.ReadFile(configFile)
	if err != nil {
		return err
	}

	var cfgMap map[string]interface{}
	if err := yaml.Unmarshal(data, &cfgMap); err != nil {
		return err
	}

	monitor, ok := cfgMap["monitor"].(map[string]interface{})
	if !ok {
		monitor = make(map[string]interface{})
	}
	monitor["monitor-client-type"] = "prometheus"
	monitor["prometheus"] = map[string]interface{}{
		"prometheus-config-path": prometheusConfigPath,
	}
	cfgMap["monitor"] = monitor

	out, err := yaml.Marshal(cfgMap)
	if err != nil {
		return err
	}
	return os.WriteFile(configFile, out, 0644)
}

// injectFastHealthConfig shrinks the instance health-check timings for this
// suite. Production marks a worker faulted after 9 consecutive failures 10s
// apart (~90s), which would make every fault/recovery spec take minutes; 1s
// interval with a single check exercises the same code path in ~1s.
func injectFastHealthConfig(configFile string) error {
	data, err := os.ReadFile(configFile)
	if err != nil {
		return err
	}

	var cfgMap map[string]interface{}
	if err := yaml.Unmarshal(data, &cfgMap); err != nil {
		return err
	}

	im, ok := cfgMap["instance-manager"].(map[string]interface{})
	if !ok {
		return fmt.Errorf("instance-manager section not found")
	}
	hc, ok := im["health-check-config"].(map[string]interface{})
	if !ok {
		return fmt.Errorf("health-check-config section not found")
	}

	overrides := []struct {
		path   []string
		values map[string]interface{}
	}{
		{[]string{"rest-health-common-config"}, map[string]interface{}{"interval-ms": 1000}},
		{[]string{"available-checkers", "rest-health"}, map[string]interface{}{"consecutive-time": 1}},
		{[]string{"unavailable-checkers", "rest-health"}, map[string]interface{}{
			"consecutive-time":           1,
			"enable-probe-infer-request": false,
		}},
	}
	for _, o := range overrides {
		target, err := digMap(hc, o.path...)
		if err != nil {
			return err
		}
		for k, v := range o.values {
			target[k] = v
		}
	}

	out, err := yaml.Marshal(cfgMap)
	if err != nil {
		return err
	}
	return os.WriteFile(configFile, out, 0644)
}

// digMap walks nested YAML maps, e.g. digMap(hc, "available-checkers", "rest-health").
func digMap(root map[string]interface{}, keys ...string) (map[string]interface{}, error) {
	cur := root
	for _, k := range keys {
		next, ok := cur[k].(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("%s section not found", strings.Join(keys, "."))
		}
		cur = next
	}
	return cur, nil
}

// sendSessionRequest sends a chat completion request with the given
// session_id in the JSON body and X-Request-Id header. The agentic balancer
// reads session_id from the request body via ParseSchedulingMetadata.
// Returns the HTTP response (the caller closes Body).
func sendSessionRequest(client *RSClient, sessionId, xRequestId, prompt string) (*http.Response, error) {
	body := map[string]interface{}{
		"model":    "pangu",
		"messages": []map[string]string{{"role": "user", "content": prompt}},
	}
	if sessionId != "" {
		body["session_id"] = sessionId
	}
	headers := map[string]string{}
	if xRequestId != "" {
		headers["X-Request-Id"] = xRequestId
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return client.SendChatCompletion(ctx, body, headers)
}

// sendStreamSessionRequest sends a streaming chat completion request with the
// given session_id in the JSON body and X-Request-Id header.
// Returns the response and a cancel function that must be called after
// reading the response body.
func sendStreamSessionRequest(client *RSClient, sessionId, xRequestId, prompt string) (*http.Response, context.CancelFunc, error) {
	body := map[string]interface{}{
		"model":    "pangu",
		"messages": []map[string]string{{"role": "user", "content": prompt}},
		"stream":   true,
	}
	if sessionId != "" {
		body["session_id"] = sessionId
	}
	headers := map[string]string{}
	if xRequestId != "" {
		headers["X-Request-Id"] = xRequestId
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	resp, err := client.SendChatCompletion(ctx, body, headers)
	if err != nil {
		cancel()
		return nil, nil, err
	}
	return resp, cancel, nil
}

// sendAffinityRequest sends one request carrying sessionID either in the JSON
// body (headerKey == "") or in the given header, then returns the worker the
// scheduler picked for it.
func sendAffinityRequest(client *RSClient, sess *gexec.Session, headerKey, sessionID, reqID string) string {
	headers := map[string]string{"X-Request-Id": reqID}
	body := map[string]interface{}{
		"model":    "pangu",
		"messages": []map[string]string{{"role": "user", "content": "affinity"}},
	}
	if headerKey == "" {
		body["session_id"] = sessionID
	} else {
		headers[headerKey] = sessionID
	}

	resp, err := client.SendChatCompletion(
		TimeoutCtx(context.Background(), 30*time.Second), body, headers)
	Expect(err).NotTo(HaveOccurred())
	Expect(resp).NotTo(BeNil())
	Expect(resp.StatusCode).To(Equal(http.StatusOK))
	resp.Body.Close()

	return waitDispatchWorker(sess, reqID, dispatchWait)
}

// sendAndWorker sends a plain request and returns the worker it was routed to.
func sendAndWorker(client *RSClient, sess *gexec.Session, sessionID, reqID, prompt string) string {
	resp, err := sendSessionRequest(client, sessionID, reqID, prompt)
	Expect(err).NotTo(HaveOccurred())
	Expect(resp).NotTo(BeNil())
	resp.Body.Close()
	return waitDispatchWorker(sess, reqID, dispatchWait)
}

// probeWorker sends a request and returns the dispatched worker, or "" when
// the request or the log line is not there yet. Safe to call from inside
// Eventually (no gomega assertions).
func probeWorker(client *RSClient, sess *gexec.Session, reqID, prompt string) string {
	resp, err := sendSessionRequest(client, "", reqID, prompt)
	if err != nil || resp == nil {
		return ""
	}
	resp.Body.Close()
	return extractDispatchWorker(sess, reqID)
}

// waitDispatchWorker waits until the dispatch line of the given request shows
// up and returns its worker. Unlike gbytes.Say it is scoped to one request id,
// so an older dispatch line from a previous spec can never satisfy it.
func waitDispatchWorker(sess *gexec.Session, reqID string, timeout time.Duration) string {
	var worker string
	Eventually(func() string {
		worker = extractDispatchWorker(sess, reqID)
		return worker
	}, timeout, 100*time.Millisecond).ShouldNot(BeEmpty(),
		"no dispatch log found for request %q", reqID)
	return worker
}

// waitLogLine waits for a request-scoped log line. The pattern must be
// anchored on a request id: gbytes.Say advances a read cursor and matches the
// earliest line, so an unanchored pattern can match a stale line.
func waitLogLine(sess *gexec.Session, pattern string, timeout time.Duration) {
	Eventually(sess.Out, timeout, 100*time.Millisecond).Should(gbytes.Say(pattern))
}

// waitProbeOutcome reports how the scheduler handled the given probe request:
// "enqueued", "dispatched", or "" when neither line showed up in time.
func waitProbeOutcome(sess *gexec.Session, reqID string, timeout time.Duration) string {
	enqueued := regexp.MustCompile(`agentic-balancer: request chatcmpl-` + regexp.QuoteMeta(reqID) + `[^ ]* enqueued`)
	dispatched := regexp.MustCompile(`agentic-balancer: dispatch req=chatcmpl-` + regexp.QuoteMeta(reqID) + `[^ ]*`)
	deadline := time.Now().Add(timeout)
	for {
		content := string(sess.Out.Contents())
		if enqueued.MatchString(content) {
			return "enqueued"
		}
		if dispatched.MatchString(content) {
			return "dispatched"
		}
		if time.Now().After(deadline) {
			return ""
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// waitUntilOverloaded blocks until the overload injected into the mock workers
// has reached the scheduler. The metric pipeline is asynchronous (scrape + 1s
// refresh), so a request is the only truthful probe: keep sending them until
// one is queued instead of dispatched. Returns the id of the queued request.
func waitUntilOverloaded(client *RSClient, sess *gexec.Session, tag string) string {
	for probe := 1; probe <= 60; probe++ {
		reqID := fmt.Sprintf("%s-%d", tag, probe)
		go func(id string) {
			resp, err := sendSessionRequest(client, "", id, "overload probe")
			if err != nil || resp == nil {
				return
			}
			resp.Body.Close()
		}(reqID)
		if waitProbeOutcome(sess, reqID, 5*time.Second) == "enqueued" {
			return reqID
		}
	}
	panic("injected overload never became visible to the scheduler")
}

// extractDispatchWorker returns the worker of the dispatch line matching the
// given X-Request-Id. Returns "" if not found.
//
// Log format:
//
//	agentic-balancer: dispatch req=chatcmpl-<xRequestId>-<suffix> → worker=<worker> (...)
func extractDispatchWorker(sess *gexec.Session, xRequestId string) string {
	return extractDispatchField(sess, xRequestId, "worker")
}

// dispatchFieldRes holds one precompiled extractor per dispatch field; the log
// format is fixed, so no pattern and no map is built per call.
var dispatchFieldRes = map[string]*regexp.Regexp{
	"worker":     regexp.MustCompile(`worker=(\S+)`),
	"aff":        regexp.MustCompile(`aff=([\d.]+)`),
	"cacheDecay": regexp.MustCompile(`cacheDecay=([\d.]+)`),
	"load":       regexp.MustCompile(`load=([\d.]+)`),
	"score":      regexp.MustCompile(`score=(-?[\d.]+)`),
	"candidates": regexp.MustCompile(`candidates=(\d+)`),
	"session":    regexp.MustCompile(`session=([^,)]*)`),
}

// extractDispatchField extracts a named field from the dispatch log line
// matching xRequestId. fieldName can be: "worker", "aff", "cacheDecay",
// "load", "score", "candidates", "session".
func extractDispatchField(sess *gexec.Session, xRequestId, fieldName string) string {
	re, ok := dispatchFieldRes[fieldName]
	if !ok {
		return ""
	}
	needle := []byte("agentic-balancer: dispatch req=chatcmpl-" + xRequestId)
	buf := sess.Out.Contents()
	for end := len(buf); end > 0; {
		idx := bytes.LastIndex(buf[:end], needle)
		if idx < 0 {
			return ""
		}
		lineEnd := bytes.IndexByte(buf[idx:], '\n')
		if lineEnd < 0 {
			lineEnd = len(buf) - idx
		}
		if m := re.FindSubmatch(buf[idx : idx+lineEnd]); len(m) == 2 {
			return string(m[1])
		}
		end = idx
	}
	return ""
}

// dispatchFieldFloat returns a numeric dispatch field, failing the spec when
// the field is missing (use extractDispatchField inside Eventually loops).
func dispatchFieldFloat(sess *gexec.Session, reqID, fieldName string) float64 {
	raw := extractDispatchField(sess, reqID, fieldName)
	Expect(raw).NotTo(BeEmpty(), "field %q missing in dispatch log of %q", fieldName, reqID)
	val, err := strconv.ParseFloat(raw, 64)
	Expect(err).NotTo(HaveOccurred())
	return val
}

// workerNameToPort maps a worker instance name (e.g. "decode1") to its port,
// which mock server operations (injectMetrics, MockError, ...) require.
func workerNameToPort(name string) string {
	routes := options.GetConfig().InstanceManagerCfg.RouteTable.InstanceRoutes
	if route, ok := routes[name]; ok {
		return fmt.Sprintf("%d", route.Port)
	}
	return ""
}

// waitWorkerStatus polls /status until the worker listening on workerPort
// reports the wanted status ("running", "fault", ...).
func waitWorkerStatus(client *RSClient, workerPort, want string, timeout time.Duration) {
	Eventually(func() string {
		rsp, err := client.GetStatusJson()
		if err != nil || rsp == nil {
			return ""
		}
		for _, items := range rsp.InstanceStatus {
			for _, item := range items {
				if strings.HasSuffix(item.URL, ":"+workerPort) {
					return item.Status
				}
			}
		}
		return ""
	}, timeout, 200*time.Millisecond).Should(Equal(want),
		"worker on port %s should reach status %q", workerPort, want)
}

// waitAllWorkersRunning waits until every worker is reported running by the
// health checker, so the specs never start against a half-ready fleet.
func waitAllWorkersRunning(client *RSClient, workerPorts []string, timeout time.Duration) {
	for _, port := range workerPorts {
		waitWorkerStatus(client, port, constant.RunningStatus, timeout)
	}
}

// readSSEBody reads the full response body and returns it as a string.
func readSSEBody(resp *http.Response) string {
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	Expect(err).NotTo(HaveOccurred())
	return string(body)
}

// verifySSEFormat checks that the response body contains SSE "data:" lines.
func verifySSEFormat(body string) {
	hasData := false
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "data: ") {
			hasData = true
			break
		}
	}
	Expect(hasData).To(BeTrue(), "stream response should contain SSE data: lines")
}
