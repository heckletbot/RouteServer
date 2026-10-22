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
	"fmt"
	"github.com/prometheus/client_golang/prometheus"
	"reflect"
	"testing"
)

func Test_recordMetrics(t *testing.T) {
	tests := []struct {
		name        string
		instance    Instance
		metricsText string
		metricsName string
		want        int
	}{
		{
			name: "p0",
			instance: Instance{
				Role:          "Prefill",
				IP:            "1.1.1.1",
				Port:          0,
				MetricsLabels: make(map[string]map[string]prometheus.Labels),
			},
			metricsName: "vllm:generation_tokens_total",
			metricsText: `# HELP vllm:generation_tokens_total Number of generation tokens processed.
                          # TYPE vllm:generation_tokens_total counter
                          vllm:generation_tokens_total{engine="0",model_name="deepseek"} 261
                          `,
			want: 1,
		},
		{
			name: "p1",
			instance: Instance{
				Role:          "Prefill",
				IP:            "1.1.1.3",
				Port:          0,
				MetricsLabels: make(map[string]map[string]prometheus.Labels),
			},
			metricsName: "vllm:num_requests_running",
			metricsText: `# HELP vllm:num_requests_running Number of requests in model execution batches.
                          # TYPE vllm:num_requests_running gauge
                          vllm:num_requests_running{engine="All",model_name="deepseek"} 0
                         `,
			want: 1,
		},
		{
			name: "d0",
			instance: Instance{
				Role:          "Decode",
				IP:            "1.1.1.2",
				Port:          0,
				MetricsLabels: make(map[string]map[string]prometheus.Labels),
			},
			metricsName: "vllm:request_params_n",
			metricsText: `# HELP vllm:request_params_n Histogram of the n request parameter.
                          # TYPE vllm:request_params_n histogram
                          vllm:request_params_n_bucket{engine="0",model_name="deepseek",le="1"} 11
                          vllm:request_params_n_bucket{engine="0",model_name="deepseek",le="2"} 11
                          vllm:request_params_n_bucket{engine="0",model_name="deepseek",le="5"} 11
                          vllm:request_params_n_bucket{engine="0",model_name="deepseek",le="10"} 11
                          vllm:request_params_n_bucket{engine="0",model_name="deepseek",le="20"} 11
                          vllm:request_params_n_bucket{engine="0",model_name="deepseek",le="+Inf"} 11
                          vllm:request_params_n_sum{engine="0",model_name="deepseek"} 11
                          vllm:request_params_n_count{engine="0",model_name="deepseek"} 11
                          `,
			want: 1,
		},
		{
			name: "scheduler",
			instance: Instance{
				Role:          "Scheduler",
				IP:            "1.1.1.4",
				Port:          0,
				MetricsLabels: make(map[string]map[string]prometheus.Labels),
			},
			metricsName: "vllm:time_to_first_token_seconds",
			metricsText: `# HELP rs:time_to_first_token_sec Distribution of Time to First Token (TTFT)
                          # TYPE rs:time_to_first_token_sec histogram
                          rs:time_to_first_token_sec_bucket{le="0.001"} 0
                          rs:time_to_first_token_sec_bucket{le="0.005"} 0
                          rs:time_to_first_token_sec_bucket{le="0.01"} 0
                          rs:time_to_first_token_sec_bucket{le="0.02"} 0
						  rs:time_to_first_token_sec_bucket{le="0.04"} 0
						  rs:time_to_first_token_sec_bucket{le="0.06"} 0
						  rs:time_to_first_token_sec_bucket{le="0.08"} 0
						  rs:time_to_first_token_sec_bucket{le="0.1"} 0
						  rs:time_to_first_token_sec_bucket{le="0.25"} 0
						  rs:time_to_first_token_sec_bucket{le="0.5"} 0
						  rs:time_to_first_token_sec_bucket{le="0.75"} 0
						  rs:time_to_first_token_sec_bucket{le="1"} 0
						  rs:time_to_first_token_sec_bucket{le="2.5"} 0
						  rs:time_to_first_token_sec_bucket{le="5"} 0
						  rs:time_to_first_token_sec_bucket{le="7.5"} 0
						  rs:time_to_first_token_sec_bucket{le="10"} 0
						  rs:time_to_first_token_sec_bucket{le="20"} 0
						  rs:time_to_first_token_sec_bucket{le="40"} 0
						  rs:time_to_first_token_sec_bucket{le="80"} 0
						  rs:time_to_first_token_sec_bucket{le="160"} 0
						  rs:time_to_first_token_sec_bucket{le="640"} 0
						  rs:time_to_first_token_sec_bucket{le="2560"} 0
						  rs:time_to_first_token_sec_bucket{le="+Inf"} 0
						  rs:time_to_first_token_sec_sum 0
						  rs:time_to_first_token_sec_count 0
                          `,
			want: 1,
		},
	}
	for _, tt := range tests {
		c := initCollector()
		t.Run(tt.name, func(t *testing.T) {
			if got := checkRecordSuccess(c, tt.instance, tt.metricsText, tt.metricsName); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("checkRecordSuccess() = %v, want %v", got, tt.want)
			}
		})
	}

}

func checkRecordSuccess(c *Collector, instance Instance, text string, metricsName string) int {
	c.recordMetrics(&instance, text)
	allLabels := instance.getInstanceLabelsForMetrics(metricsName)
	return len(allLabels)
}

func TestMetricsDeletion(t *testing.T) {
	c := initCollector()
	instance := Instance{
		Role:          "Prefill",
		IP:            "1.1.1.3",
		Port:          0,
		MetricsLabels: make(map[string]map[string]prometheus.Labels),
	}
	metricsName := "vllm:num_requests_running"
	metricsText := `# HELP vllm:num_requests_running Number of requests in model execution batches.
                    # TYPE vllm:num_requests_running gauge
                    vllm:num_requests_running{engine="All",model_name="deepseek"} 50
                   `
	labels := map[string]string{
		"role":       instance.Role,
		"instance":   fmt.Sprintf("%s:%d", instance.IP, instance.Port),
		"engine":     "All",
		"model_name": "deepseek",
	}

	t.Run("Delete existing metrics", func(t *testing.T) {
		c.recordMetrics(&instance, metricsText)
		if !metricExists(c.registry, metricsName, labels) {
			t.Error("Fail to record this metrics")
		}
		deleted := c.removeInstanceMetrics(instance)
		if !deleted {
			t.Error("Fail to delete this instance")
		}
		if metricExists(c.registry, metricsName, labels) {
			t.Error("Metrics should not exist after deletion")
		}
	})

	t.Run("Delete non-existing metrics", func(t *testing.T) {
		c.recordMetrics(&instance, metricsText)
		if !metricExists(c.registry, metricsName, labels) {
			t.Error("Fail to record this metrics")
		}
		newLabels := map[string]string{
			"role":       instance.Role,
			"instance":   fmt.Sprintf("%s:%d", instance.IP, instance.Port),
			"engine":     "0",
			"model_name": "deepseek",
		}
		m := instance.MetricsLabels[metricsName]
		for labelKey := range m {
			m[labelKey] = newLabels
		}
		deleted := c.removeInstanceMetrics(instance)
		// Now returns true even if some deletion fails to ensure cleanup continues
		if !deleted {
			t.Error("removeInstanceMetrics should always return true after cleanup attempt")
		}
		// Original metrics should still exist (because no matching labels)
		if !metricExists(c.registry, metricsName, labels) {
			t.Error("Original metrics should still exist")
		}
	})

	instance = Instance{
		Role:          "Prefill",
		IP:            "1.1.1.3",
		Port:          0,
		MetricsLabels: make(map[string]map[string]prometheus.Labels),
	}
	t.Run("Recreate after deletion", func(t *testing.T) {
		c.recordMetrics(&instance, metricsText)
		if !metricExists(c.registry, metricsName, labels) {
			t.Error("Fail to record this metrics")
		}
		deleted := c.removeInstanceMetrics(instance)
		if !deleted {
			t.Error("Fail to delete this instance")
		}
		if metricExists(c.registry, metricsName, labels) {
			t.Error("Metrics should not exist after deletion")
		}
		c.recordMetrics(&instance, metricsText)
		if !metricExists(c.registry, metricsName, labels) {
			t.Error("Metrics should exist after deletion")
		}
	})
}

func TestCustomHistogramDeletion(t *testing.T) {
	c := initCollector()
	instance := Instance{
		Role:          "Decode",
		IP:            "1.1.1.2",
		Port:          0,
		MetricsLabels: make(map[string]map[string]prometheus.Labels),
	}
	metricsName := "vllm:request_params_n"
	metricsText := `# HELP vllm:request_params_n Histogram of the n request parameter.
                    # TYPE vllm:request_params_n histogram
                    vllm:request_params_n_bucket{engine="0",model_name="deepseek",le="1"} 11
                    vllm:request_params_n_bucket{engine="0",model_name="deepseek",le="2"} 11
                    vllm:request_params_n_bucket{engine="0",model_name="deepseek",le="5"} 11
                    vllm:request_params_n_bucket{engine="0",model_name="deepseek",le="10"} 11
                    vllm:request_params_n_bucket{engine="0",model_name="deepseek",le="20"} 11
                    vllm:request_params_n_bucket{engine="0",model_name="deepseek",le="+Inf"} 11
                    vllm:request_params_n_sum{engine="0",model_name="deepseek"} 11
                    vllm:request_params_n_count{engine="0",model_name="deepseek"} 11
                    `
	labels := map[string]string{
		"role":       instance.Role,
		"instance":   fmt.Sprintf("%s:%d", instance.IP, instance.Port),
		"engine":     "0",
		"model_name": "deepseek",
	}

	t.Run("Delete existing metrics", func(t *testing.T) {
		c.recordMetrics(&instance, metricsText)
		if !metricExists(c.registry, metricsName, labels) {
			t.Error("Fail to record this metrics")
		}
		deleted := c.removeInstanceMetrics(instance)
		if !deleted {
			t.Error("Fail to delete this instance")
		}
		if metricExists(c.registry, metricsName, labels) {
			t.Error("Metrics should not exist after deletion")
		}
	})

	t.Run("Delete non-existing metrics", func(t *testing.T) {
		c.recordMetrics(&instance, metricsText)
		if !metricExists(c.registry, metricsName, labels) {
			t.Error("Fail to record this metrics")
		}
		newLabels := map[string]string{
			"role":       instance.Role,
			"instance":   fmt.Sprintf("%s:%d", instance.IP, instance.Port),
			"engine":     "All",
			"model_name": "deepseek",
		}
		m := instance.MetricsLabels[metricsName]
		for labelKey := range m {
			m[labelKey] = newLabels
		}
		deleted := c.removeInstanceMetrics(instance)
		// Now returns true even if some deletion fails to ensure cleanup continues
		if !deleted {
			t.Error("removeInstanceMetrics should always return true after cleanup attempt")
		}
		// Original metrics should still exist (because no matching labels)
		if !metricExists(c.registry, metricsName, labels) {
			t.Error("Original metrics should still exist")
		}
	})

	instance = Instance{
		Role:          "Decode",
		IP:            "1.1.1.2",
		Port:          0,
		MetricsLabels: make(map[string]map[string]prometheus.Labels),
	}
	t.Run("Recreate after deletion", func(t *testing.T) {
		c.recordMetrics(&instance, metricsText)
		if !metricExists(c.registry, metricsName, labels) {
			t.Error("Fail to record this metrics")
		}
		deleted := c.removeInstanceMetrics(instance)
		if !deleted {
			t.Error("Fail to delete this instance")
		}
		if metricExists(c.registry, metricsName, labels) {
			t.Error("Metrics should not exist after deletion")
		}
		c.recordMetrics(&instance, metricsText)
		if !metricExists(c.registry, metricsName, labels) {
			t.Error("Metrics should exist after deletion")
		}
	})
}

// Helper function to check if a metric exists
func metricExists(reg *prometheus.Registry, name string, labels prometheus.Labels) bool {
	metrics, _ := reg.Gather()
	for _, mf := range metrics {
		if mf.GetName() == name {
			for _, metric := range mf.GetMetric() {
				allMatch := true
				for k, v := range labels {
					found := false
					for _, label := range metric.GetLabel() {
						if label.GetName() == k && label.GetValue() == v {
							found = true
							break
						}
					}
					if !found {
						allMatch = false
						break
					}
				}
				if allMatch {
					return true
				}
			}
		}
	}
	return false
}

func initCollector() *Collector {
	registry := prometheus.NewRegistry()
	aggRegistry := prometheus.NewRegistry()
	c := &Collector{
		registry:                   registry,
		aggRegistry:                aggRegistry,
		instances:                  make(map[string]Instance),
		gaugeMetrics:               make(map[string]*prometheus.GaugeVec),
		counterMetrics:             make(map[string]*prometheus.CounterVec),
		histogramMetrics:           make(map[string]*CustomHistogram),
		aggregatedGaugeMetrics:     make(map[string]*prometheus.GaugeVec),
		aggregatedCounterMetrics:   make(map[string]*prometheus.CounterVec),
		aggregatedHistogramMetrics: make(map[string]*CustomHistogram),
	}
	return c
}
