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

package monitor_client

import (
	"os"
	"strings"
	"testing"

	"github.com/prometheus/common/expfmt"
	"github.com/stretchr/testify/assert"

	"infer-dp-scheduler/pkg/common/instance-metrics"
)

var input = `vllm:num_requests_waiting{engine="31",model_name="deepseek"} 0.0
vllm:gpu_prefix_cache_queries_total{engine="31",model_name="deepseek"} 0.0
vllm:gpu_prefix_cache_queries_created{engine="31",model_name="deepseek"} 1.752065577291397e+09
vllm:request_success_created{engine="31",finished_reason="stop",model_name="deepseek"} 1.7520655772914498e+09
vllm:request_success_created{engine="31",finished_reason="length",model_name="deepseek"} 1.7520655772914562e+09
vllm:request_success_created{engine="31",finished_reason="abort",model_name="deepseek"} 1.752065577291462e+09
vllm:request_decode_time_seconds_bucket{engine="31",le="0.3",model_name="deepseek"} 0.0
vllm:request_decode_time_seconds_bucket{engine="31",le="0.5",model_name="deepseek"} 0.0
vllm:request_decode_time_seconds_bucket{engine="31",le="0.8",model_name="deepseek"} 0.0
vllm:request_decode_time_seconds_bucket{engine="31",le="1.0",model_name="deepseek"} 0.0
vllm:request_decode_time_seconds_bucket{engine="31",le="1.5",model_name="deepseek"} 0.0
vllm:request_decode_time_seconds_bucket{engine="31",le="2.0",model_name="deepseek"} 0.0
vllm:request_decode_time_seconds_bucket{engine="31",le="2.5",model_name="deepseek"} 0.0
vllm:request_decode_time_seconds_bucket{engine="31",le="5.0",model_name="deepseek"} 0.0
vllm:request_decode_time_seconds_bucket{engine="31",le="10.0",model_name="deepseek"} 0.0
vllm:request_decode_time_seconds_bucket{engine="31",le="15.0",model_name="deepseek"} 0.0
vllm:request_decode_time_seconds_bucket{engine="31",le="20.0",model_name="deepseek"} 0.0
vllm:request_decode_time_seconds_bucket{engine="31",le="30.0",model_name="deepseek"} 0.0
vllm:request_decode_time_seconds_bucket{engine="31",le="40.0",model_name="deepseek"} 0.0
vllm:request_decode_time_seconds_bucket{engine="31",le="50.0",model_name="deepseek"} 0.0
vllm:request_decode_time_seconds_bucket{engine="31",le="60.0",model_name="deepseek"} 0.0
vllm:request_decode_time_seconds_bucket{engine="31",le="120.0",model_name="deepseek"} 0.0
vllm:request_decode_time_seconds_bucket{engine="31",le="240.0",model_name="deepseek"} 0.0
vllm:request_decode_time_seconds_bucket{engine="31",le="480.0",model_name="deepseek"} 0.0
vllm:request_decode_time_seconds_bucket{engine="31",le="960.0",model_name="deepseek"} 0.0
vllm:request_decode_time_seconds_bucket{engine="31",le="1920.0",model_name="deepseek"} 0.0
vllm:request_decode_time_seconds_bucket{engine="31",le="7680.0",model_name="deepseek"} 0.0
vllm:request_decode_time_seconds_bucket{engine="31",le="+Inf",model_name="deepseek"} 0.0
vllm:request_decode_time_seconds_count{engine="31",model_name="deepseek"} 0.0
vllm:request_decode_time_seconds_sum{engine="31",model_name="deepseek"} 0.0
vllm:request_decode_time_seconds_created{engine="31",model_name="deepseek"} 1.7520655772922766e+09
# HELP vllm:num_requests_running Number of requests in model execution batches.
# TYPE vllm:num_requests_running gauge
vllm:num_requests_running{model_name="meta-llama/Llama-3.1-8B-Instruct"} 9.0
# HELP vllm:generation_tokens_total Number of generation tokens processed.
# TYPE vllm:generation_tokens_total counter
vllm:generation_tokens_total{model_name="meta-llama/Llama-3.1-8B-Instruct"} 27453.0
# HELP vllm:time_to_first_token_seconds Histogram of time to first token in seconds.
# TYPE vllm:time_to_first_token_seconds histogram
vllm:time_to_first_token_seconds_bucket{le="0.001",model_name="meta-llama/Llama-3.1-8B-Instruct"} 0.0
vllm:time_to_first_token_seconds_bucket{le="0.005",model_name="meta-llama/Llama-3.1-8B-Instruct"} 0.0
vllm:time_to_first_token_seconds_bucket{le="0.01",model_name="meta-llama/Llama-3.1-8B-Instruct"} 0.0
vllm:time_to_first_token_seconds_bucket{le="0.02",model_name="meta-llama/Llama-3.1-8B-Instruct"} 13.0
vllm:time_to_first_token_seconds_bucket{le="0.04",model_name="meta-llama/Llama-3.1-8B-Instruct"} 97.0
vllm:time_to_first_token_seconds_bucket{le="0.06",model_name="meta-llama/Llama-3.1-8B-Instruct"} 123.0
vllm:time_to_first_token_seconds_bucket{le="0.08",model_name="meta-llama/Llama-3.1-8B-Instruct"} 138.0
vllm:time_to_first_token_seconds_bucket{le="0.1",model_name="meta-llama/Llama-3.1-8B-Instruct"} 140.0
vllm:time_to_first_token_seconds_count{model_name="meta-llama/Llama-3.1-8B-Instruct"} 140.0
`

var configContent = `metrics:
  gauge:
    - "vllm:num_requests_running"
    - "vllm:num_requests_waiting"
    - "vllm:gpu_prefix_cache_queries_created"
    - "vllm:request_success_created"
  counter:
    - "vllm:generation_tokens_total"
    - "vllm:prompt_tokens_total"
    - "vllm:gpu_prefix_cache_queries_total"
  histogram:
    - "vllm:time_to_first_token_seconds"
    - "vllm:request_generation_tokens"
    - "vllm:request_decode_time_seconds"
batch-size: 3
max-concurrency: 3`

func TestPrometheusMonitorClient_doMetrics(t *testing.T) {
	err := os.WriteFile("config.yaml", []byte(configContent), 0644)
	defer func() {
		err := os.RemoveAll("config.yaml")
		assert.Nil(t, err)
	}()
	assert.Nil(t, err)

	c := prometheusMonitorClient{
		config: &instance_metrics.PrometheusConfig{
			PrometheusConfigPath: "config.yaml",
		},
	}
	roleSubs := make(map[string][]string)
	roleSubs["profile"] = []string{
		"vllm:num_requests_running",
		"vllm:num_requests_waiting",
		"vllm:gpu_prefix_cache_queries_created",
		"vllm:request_success_created",
		"vllm:generation_tokens_total",
		"vllm:prompt_tokens_total",
		"vllm:gpu_prefix_cache_queries_total",
		"vllm:time_to_first_token_seconds",
		"vllm:request_generation_tokens",
		"vllm:request_decode_time_seconds",
	}
	err = c.Init(roleSubs)
	assert.Nil(t, err)

	response := strings.NewReader(input)
	var parser expfmt.TextParser
	metricFamilies, err := parser.TextToMetricFamilies(response)
	assert.Nil(t, err)

	for _, keys := range c.subscribeMetrics {
		value, err := getMetricValue(metricFamilies, keys)
		assert.Nil(t, err)
		infos := (*value)["vllm:time_to_first_token_seconds"]
		assert.Equal(t, infos[0].Histogram.Count, float64(140))
		assert.Equal(t, len(infos[0].Histogram.Buckets), 8)
	}
}
