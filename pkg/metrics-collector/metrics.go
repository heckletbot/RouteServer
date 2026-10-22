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

package metrics_collector

import (
	"strings"
	"sync"
)

const (
	Engine    = "engine"
	ModelName = "model_name"
)

var (
	staticMap map[string][]string
	initOnce  sync.Once
)

// Mapping between RouteServer metric names and vLLM native metric names
func initStaticMap() {
	staticMap = map[string][]string{
		"rs:time_to_first_token_sec":             {"vllm:time_to_first_token_seconds"},
		"rs:time_per_output_token_ms":            {"vllm:inter_token_latency_seconds", "vllm:time_per_output_token_seconds"},
		"rs:request_total_time_secs":             {"vllm:e2e_request_latency_seconds"},
		"rs:infer_requests_failure_status_total": {"vllm:infer_requests_failure_status_total"},
	}
}

func getStaticMap() map[string][]string {
	initOnce.Do(initStaticMap)
	return staticMap
}

// Labels to add during collection to distinguish different instances
var collectLabels = []string{"role", "instance"}

func getCollectLabels() []string {
	return collectLabels
}

func getRealMetricsName(metricsName string) []string {
	// Metrics fetched from route server
	if strings.HasPrefix(metricsName, "rs:") {
		name, ok := getStaticMap()[metricsName]
		if !ok {
			return nil
		}
		return name
	}
	return nil
}
