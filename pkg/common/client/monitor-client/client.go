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

package monitor_client

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	instance_metrics "infer-dp-scheduler/pkg/common/instance-metrics"
	"infer-dp-scheduler/pkg/common/logger"

	"infer-dp-scheduler/pkg/common/client"
	commoncfg "infer-dp-scheduler/pkg/common/config"
	"infer-dp-scheduler/pkg/common/constant"

	"github.com/goccy/go-yaml"
)

const (
	// vllm metrics name
	NumRequestsWaiting    = "vllm:num_requests_waiting"
	NumRequestsRunning    = "vllm:num_requests_running"
	PromptTokensTotal     = "vllm:prompt_tokens_total"
	GenerationTokensTotal = "vllm:generation_tokens_total"
)

type MonitorClient interface {
	GetMonitorStats(prefix string, roles []string, clients map[string]map[string]*client.Instance) (map[string]map[string]*MonitorStats, error)
	Init(roleSubs map[string][]string) error
}

type noneMonitorClient struct{}

func (n noneMonitorClient) Init(roleSubs map[string][]string) error {
	return nil
}

func (n noneMonitorClient) GetMonitorStats(prefix string, roles []string,
	clients map[string]map[string]*client.Instance) (map[string]map[string]*MonitorStats, error) {
	res := make(map[string]map[string]*MonitorStats)
	for i := range roles {
		res[roles[i]] = make(map[string]*MonitorStats)
	}
	return res, nil
}

type prometheusMonitorClient struct {
	config           *instance_metrics.PrometheusConfig
	subscribeMetrics map[string][]string
}

type LoadMetricConfig struct {
	Metrics              map[string][]string `yaml:"metrics"`
	MetricBatchSize      int                 `yaml:"batch-size"`
	MetricMaxConcurrency int                 `yaml:"max-concurrency"`
	metricRelate         map[string]string
}

var loadMetricConfig LoadMetricConfig

func getLoadMetricInfo(prometheusConfigPath string) error {
	cleaned := filepath.Clean(prometheusConfigPath)
	if cleaned != prometheusConfigPath {
		return fmt.Errorf("the path contains illegal jump symbols")
	}
	configBytes, err := os.ReadFile(cleaned)
	if err != nil {
		return fmt.Errorf("load prometheus config file failed, err: %v", err)
	}
	if err = yaml.Unmarshal(configBytes, &loadMetricConfig); err != nil {
		return fmt.Errorf("unmarshal prometheus config file failed, err: %v", err)
	}
	if loadMetricConfig.MetricBatchSize == 0 {
		loadMetricConfig.MetricBatchSize = instance_metrics.MetricBatchSizeDefault
	}
	if loadMetricConfig.MetricMaxConcurrency == 0 {
		loadMetricConfig.MetricMaxConcurrency = instance_metrics.MetricMaxConcurrencyDefault
	}
	return nil
}

func (c *prometheusMonitorClient) Init(roleSubs map[string][]string) error {
	err := getLoadMetricInfo(c.config.PrometheusConfigPath)
	if err != nil {
		return err
	}
	metricMap := make(map[string]string)
	for metricType, loadMetrics := range loadMetricConfig.Metrics {
		for _, metricKey := range loadMetrics {
			metricMap[metricKey] = metricType
		}
	}
	loadMetricConfig.metricRelate = metricMap
	filteredRoleSubs := make(map[string][]string)
	for role, metricKeys := range roleSubs {
		var validMetrics []string
		for _, metric := range metricKeys {
			if _, ok := metricMap[metric]; ok {
				validMetrics = append(validMetrics, metric)
			}
		}
		if len(validMetrics) > 0 {
			filteredRoleSubs[role] = validMetrics
		}
	}
	c.subscribeMetrics = filteredRoleSubs
	return nil
}

type MonitorStats struct {
	TimeStamp time.Time
	Metrics   *map[string][]*instance_metrics.MetricInfo
}

func NewClient(config *commoncfg.MonitorCfg) (MonitorClient, error) {
	switch config.MonitorClientType {
	case constant.MonitorTypePrometheus:
		return &prometheusMonitorClient{
			config: config.PrometheusCfg,
		}, nil
	case constant.MonitorTypeNone:
		return &noneMonitorClient{}, nil
	default:
		return nil, fmt.Errorf("fail to create monitor client, unrecognized monitor client type %s",
			config.MonitorClientType)
	}
}

func GetMetricsKey(prefix string, role string) string {
	return fmt.Sprintf("%s:%s", prefix, role)
}

func (c *prometheusMonitorClient) GetMonitorStats(prefix string, roles []string,
	clients map[string]map[string]*client.Instance) (map[string]map[string]*MonitorStats, error) {
	result := make(map[string]map[string]*MonitorStats, len(clients))
	sem := make(chan struct{}, loadMetricConfig.MetricMaxConcurrency)
	var wg sync.WaitGroup
	var resMutex sync.Mutex
	var firstError error
	for role, instanceInfo := range clients {
		metricKeys := c.subscribeMetrics[role]
		if len(metricKeys) == 0 {
			continue
		}
		result[role] = make(map[string]*MonitorStats)
		batches := c.createBatches(instanceInfo)
		for _, batch := range batches {
			wg.Add(1)
			sem <- struct{}{}
			go func(batch map[string]*client.Instance) {
				defer wg.Done()
				defer func() { <-sem }()
				var parser expfmt.TextParser
				metricsInfo, err := doMetrics(batch, parser, metricKeys)
				if err != nil {
					resMutex.Lock()
					if firstError == nil {
						firstError = err
					}
					resMutex.Unlock()
					return
				}
				resMutex.Lock()
				for k, v := range metricsInfo {
					result[role][k] = v
				}
				resMutex.Unlock()
			}(batch)
		}
	}
	// wait for all batch returned.
	wg.Wait()
	if firstError != nil {
		return nil, fmt.Errorf("failed to fetch metrics: %v", firstError)
	}
	return result, nil
}

func doMetrics(instanceInfo map[string]*client.Instance, parser expfmt.TextParser, metricKeys []string) (map[string]*MonitorStats, error) {
	result := make(map[string]*MonitorStats)
	for id, instance := range instanceInfo {
		refreshTime := time.Now()
		respBodyBytes, err := instance.Client.Metrics()
		if err != nil {
			return nil, fmt.Errorf("failed to fetch metrics for instance %s: %v", id, err)
		}
		bodyReader := io.NopCloser(bytes.NewReader(respBodyBytes))
		value, err := processMetricsResponse(bodyReader, parser, metricKeys)
		if err != nil {
			return nil, fmt.Errorf("failed to process metrics for instance %s: %v", id, err)
		}
		result[id] = &MonitorStats{
			TimeStamp: refreshTime,
			Metrics:   value,
		}
	}
	return result, nil
}

func processMetricsResponse(body io.ReadCloser, parser expfmt.TextParser, metricKeys []string) (*map[string][]*instance_metrics.MetricInfo, error) {
	defer body.Close() // Ensure the body is closed when the function exits

	metricFamilies, err := parser.TextToMetricFamilies(body)
	if err != nil {
		return nil, fmt.Errorf("failed to parse metric families: %v", err)
	}

	value, err := getMetricValue(metricFamilies, metricKeys)
	if err != nil {
		return nil, err
	}

	return value, nil
}

func getMetricValue(metricFamilies map[string]*dto.MetricFamily, targetMetric []string) (*map[string][]*instance_metrics.MetricInfo, error) {
	result := make(map[string][]*instance_metrics.MetricInfo)
	for _, metricName := range targetMetric {
		metricFamily, exists := metricFamilies[metricName]
		if !exists {
			continue
		}
		inputType := loadMetricConfig.metricRelate[metricName]
		var metricInfos []*instance_metrics.MetricInfo
		for _, familyMetric := range metricFamily.Metric {
			var metricInfo *instance_metrics.MetricInfo
			if metricFamily.GetType() == dto.MetricType_COUNTER || strings.EqualFold(inputType, dto.MetricType_COUNTER.
				String()) {
				metricInfo = &instance_metrics.MetricInfo{
					MetricType: dto.MetricType_COUNTER,
					Value:      familyMetric.GetCounter().GetValue(),
					MetricName: metricName,
				}
			} else if metricFamily.GetType() == dto.MetricType_GAUGE || strings.EqualFold(inputType, dto.MetricType_GAUGE.
				String()) {
				metricInfo = &instance_metrics.MetricInfo{
					MetricType: dto.MetricType_GAUGE,
					Value:      familyMetric.GetGauge().GetValue(),
					MetricName: metricName,
				}
			} else if metricFamily.GetType() == dto.MetricType_HISTOGRAM || strings.EqualFold(inputType, dto.MetricType_HISTOGRAM.
				String()) {
				metricValue, err := GetHistogramValue(familyMetric)
				if err != nil {
					return nil, fmt.Errorf("failed to parse histogram metrics %s caused by: %v", metricName, err)
				}
				metricInfo = &instance_metrics.MetricInfo{
					MetricType: dto.MetricType_HISTOGRAM,
					Histogram:  metricValue,
					MetricName: metricName,
				}
			} else {
				return nil, fmt.Errorf("failed to parse simple metrics %s caused by unsupported metric type: %v", metricName, metricFamily.GetType())
			}
			metricInfos = append(metricInfos, metricInfo)
		}
		logger.Log().Tracef("the %s metrics value is: %v", metricName, metricInfos)
		result[metricName] = metricInfos
	}
	return &result, nil
}

func (c *prometheusMonitorClient) createBatches(instanceInfo map[string]*client.Instance) []map[string]*client.Instance {
	batchSize := loadMetricConfig.MetricBatchSize
	chunks := make([]map[string]*client.Instance, 0, (len(instanceInfo)+batchSize-1)/batchSize)
	currentChunk := make(map[string]*client.Instance, batchSize)
	count := 0
	for id, instance := range instanceInfo {
		if count == batchSize {
			chunks = append(chunks, currentChunk)
			currentChunk = make(map[string]*client.Instance, batchSize)
			count = 0
		}
		currentChunk[id] = instance
		count++
	}
	if count > 0 {
		chunks = append(chunks, currentChunk)
	}
	return chunks
}

func GetHistogramValue(metric *dto.Metric) (*instance_metrics.HistogramMetricValue, error) {
	histogram := &instance_metrics.HistogramMetricValue{
		Buckets: make(map[float64]float64),
	}
	histogramMetric := metric.GetHistogram()
	if histogramMetric == nil {
		return nil, fmt.Errorf("histogram metric not found")
	}
	histogram.Sum = histogramMetric.GetSampleSum()
	histogram.Count = float64(histogramMetric.GetSampleCount())
	for _, bucket := range histogramMetric.GetBucket() {
		histogram.Buckets[bucket.GetUpperBound()] = float64(bucket.GetCumulativeCount())
	}
	return histogram, nil
}
