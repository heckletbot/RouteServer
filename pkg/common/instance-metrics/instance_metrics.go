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

package instance_metrics

import io_prometheus_client "github.com/prometheus/client_model/go"

const (
	MetricBatchSizeDefault      = 5
	MetricMaxConcurrencyDefault = 10
)

type PrometheusConfig struct {
	PrometheusConfigPath string `yaml:"prometheus-config-path"`
}

type MetricInfo struct {
	MetricType io_prometheus_client.MetricType
	MetricName string
	Value      float64
	Histogram  *HistogramMetricValue
}

type HistogramMetricValue struct {
	Sum     float64
	Count   float64
	Buckets map[float64]float64 // e.g., {"0.1": 5, "0.5": 3, "1.0": 2}
}
