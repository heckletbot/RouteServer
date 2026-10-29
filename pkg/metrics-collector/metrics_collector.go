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

package metrics_collector

import (
	"crypto/tls"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"infer-dp-scheduler/pkg/common/constant"
	"infer-dp-scheduler/pkg/metrics-collector/logger"
	metrics2 "infer-dp-scheduler/pkg/route-server/metrics"
)

var metricChan chan struct{}

type Instance struct {
	Role          string
	IP            string
	Port          int
	MetricsLabels map[string]map[string]prometheus.Labels
}

func (i *Instance) getKey() string {
	return fmt.Sprintf("IP_%s_PORT_%d", i.IP, i.Port)
}

// updateInstanceLabels updates the collected label information for an instance
func (i *Instance) updateInstanceLabels(name string, labels prometheus.Labels) {
	// Initialize if the metric has not been recorded
	if i.MetricsLabels[name] == nil {
		i.MetricsLabels[name] = make(map[string]prometheus.Labels)
	}

	// Generate label key for fast deduplication
	labelKey := generateLabelKey(labels)

	// Check if label exists to avoid duplicates
	if _, exists := i.MetricsLabels[name][labelKey]; exists {
		return
	}

	i.MetricsLabels[name][labelKey] = labels
}

// generateLabelKey generates a unique key for labels used for fast deduplication
func generateLabelKey(labels prometheus.Labels) string {
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	// Build the string
	var builder strings.Builder
	for i, k := range keys {
		if i > 0 {
			builder.WriteString("|")
		}
		builder.WriteString(k)
		builder.WriteString("=")
		builder.WriteString(labels[k])
	}
	return builder.String()
}

// getInstanceLabelsForMetrics retrieves all label information for a specific metric on this instance
func (i *Instance) getInstanceLabelsForMetrics(metricsName string) []prometheus.Labels {
	var allLabels []prometheus.Labels
	if labels, exists := i.MetricsLabels[metricsName]; exists {
		for _, label := range labels {
			allLabels = append(allLabels, label)
		}
	}
	return allLabels
}

type Collector struct {
	mapMutex                   sync.RWMutex
	collectEnable              bool
	modelName                  string
	instances                  map[string]Instance
	registry                   *prometheus.Registry
	aggRegistry                *prometheus.Registry
	interval                   time.Duration
	gaugeMetrics               map[string]*prometheus.GaugeVec
	counterMetrics             map[string]*prometheus.CounterVec
	histogramMetrics           map[string]*CustomHistogram
	aggregatedGaugeMetrics     map[string]*prometheus.GaugeVec
	aggregatedCounterMetrics   map[string]*prometheus.CounterVec
	aggregatedHistogramMetrics map[string]*CustomHistogram
	metricsConfig              MetricsConfig

	peerClientTlsCfg     *tls.Config
	instanceClientTlsCfg *tls.Config
	selfName             string
}

var isRunning func() bool

func defaultRunning() bool {
	return true
}

func InitRunning() {
	if isRunning == nil {
		isRunning = defaultRunning
	}
}

func SetRunningFun(customizeRunning func() bool) {
	if isRunning == nil {
		logger.Logger().Infof("customize running function")
		isRunning = customizeRunning
	}
}

var getLatestInstances func() map[string]Instance

func SetGetLatestInstancesFun(customizeGetLatestInstances func() map[string]Instance) {
	if getLatestInstances == nil {
		getLatestInstances = customizeGetLatestInstances
	}
}

func NewMetricsCollector(instances []Instance, yamlPath string) (*Collector, error) {
	logger.InitLogger()
	InitRunning()
	registry := prometheus.NewRegistry()
	aggRegistry := prometheus.NewRegistry()
	metricsConfig, err := loadMetricsYamlConfig(yamlPath)
	if err != nil {
		logger.Logger().Errorf("error when load metrics yaml config %s", err.Error())
		return &Collector{
			collectEnable: false,
			registry:      registry,
			aggRegistry:   aggRegistry,
		}, err
	}

	// Initialize per-instance metrics
	gaugeMetrics := make(map[string]*prometheus.GaugeVec)
	counterMetrics := make(map[string]*prometheus.CounterVec)
	histogramMetrics := make(map[string]*CustomHistogram)

	// Initialize aggregated metrics
	aggregatedGaugeMetrics := make(map[string]*prometheus.GaugeVec)
	aggregatedCounterMetrics := make(map[string]*prometheus.CounterVec)
	aggregatedHistogramMetrics := make(map[string]*CustomHistogram)

	// 1.If PROMETHEUS_MULTIPROC_DIR is set in vllm, logs will be written to disk at the specified directory.
	// When requesting metrics from each api-server, full logs will be returned. To avoid duplicate logs,
	// each node only needs to fetch logs once. Therefore, when CollectFromUniqIp is true, prefill service
	// occupies one machine and collects from one ip:port, while decode service occupies two machines,
	// collecting from ip1:port1 and ip2:port2.
	// 2.If PROMETHEUS_MULTIPROC_DIR is not set in vllm, logs will not be written to disk. All instances
	// must be requested to get logs. CollectFromUniqIp should not be set, otherwise logs collection
	// will be incomplete.
	CollectFromUniqIp := metricsConfig.CollectFromUniqIp
	collectInstances := make(map[string]Instance)

	if !CollectFromUniqIp {
		for _, ins := range instances {
			ins.MetricsLabels = make(map[string]map[string]prometheus.Labels)
			collectInstances[ins.getKey()] = ins
		}
	} else {
		logger.Logger().Infof("collect metrics from unique instance to avoid repeat metrics info.")
		uniqueInstances := make(map[string]Instance)
		seenIp := make(map[string]bool)
		seenRole := make(map[string]bool)
		for _, instance := range instances {
			// Current implementation ensures prefill, decode, and scheduler instances are all reachable
			// through OR operation of IP and role
			if !seenIp[instance.IP] || !seenRole[instance.Role] {
				seenIp[instance.IP] = true
				seenRole[instance.Role] = true
				instance.MetricsLabels = make(map[string]map[string]prometheus.Labels)
				uniqueInstances[instance.getKey()] = instance
			}
		}
		collectInstances = uniqueInstances
	}
	metricChan = make(chan struct{})
	interval := metricsConfig.MetricsCollectInterval

	metricsCollector := &Collector{
		mapMutex:      sync.RWMutex{},
		collectEnable: true,
		modelName:     "",
		// All instances
		instances: collectInstances,
		// Two registries to ensure visibility
		registry:    registry,
		aggRegistry: aggRegistry,
		// Collection interval
		interval: time.Duration(interval) * time.Second,
		// Raw metrics data
		gaugeMetrics:     gaugeMetrics,
		counterMetrics:   counterMetrics,
		histogramMetrics: histogramMetrics,
		// Aggregated metrics data
		aggregatedGaugeMetrics:     aggregatedGaugeMetrics,
		aggregatedCounterMetrics:   aggregatedCounterMetrics,
		aggregatedHistogramMetrics: aggregatedHistogramMetrics,
		// Aggregation configuration
		metricsConfig: *metricsConfig,
	}
	logger.Logger().Infof("init metrics collector successful and interval time is %d seconds", interval)
	SetGetLatestInstancesFun(metricsCollector.getAllInstances)
	metricsCollector.runMetricsCollectLoop()
	return metricsCollector, nil
}

func (c *Collector) getAllInstances() map[string]Instance {
	return c.instances
}

func (c *Collector) HandleMetricsRequest(ctx *gin.Context) {
	// Check URL parameters
	detailEnabled := ctx.Query("detailEnable")
	var registry *prometheus.Registry

	if strings.ToLower(detailEnabled) == "true" {
		// Return raw metrics
		registry = c.registry
	} else {
		// Return aggregated metrics
		registry = c.aggRegistry
	}

	// Use Prometheus HTTP handler to output metrics
	handler := promhttp.HandlerFor(registry, promhttp.HandlerOpts{})
	handler.ServeHTTP(ctx.Writer, ctx.Request)
}

func (c *Collector) runMetricsCollectLoop() {
	if !c.collectEnable {
		logger.Logger().Errorf("fail to init metrics collector")
		return
	}
	// First trigger: execute serially to collect all metrics
	if isRunning() {
		c.collectMetrics()
	}

	go func() {
		logger.Logger().Infof("start server to collect metrics")
		ticker := time.NewTicker(c.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if isRunning() {
					c.collectMetrics()
				}
			case <-metricChan:
				logger.Logger().Infof("metrics collector stop")
				return
			}
		}
	}()
}

func (c *Collector) updateInstanceStatus(instanceMap map[string]Instance) {
	// Scale-in cases are handled properly
	deletedInstance := make([]Instance, 0)
	seenInferInstanceIp := make(map[string]bool)
	seenInferRole := make(map[string]bool)
	for _, instance := range c.instances {
		// If instance exists in original map but not in latest map, it's a scale-in
		// Scale-in instances need to have their metrics removed
		if instance.Role != "Scheduler" {
			seenInferInstanceIp[instance.IP] = true
			seenInferRole[instance.Role] = true
		}

		_, ok := instanceMap[instance.getKey()]
		if !ok {
			deletedInstance = append(deletedInstance, instance)
		}
	}
	CollectFromUniqIp := c.metricsConfig.CollectFromUniqIp
	for _, instance := range instanceMap {
		// If instance doesn't exist in original map, it's a scale-out
		if _, ok := c.instances[instance.getKey()]; !ok {
			if CollectFromUniqIp {
				if instance.Role == "Scheduler" {
					// Scheduler node may have multiple services on one IP
					instance.MetricsLabels = make(map[string]map[string]prometheus.Labels)
					c.instances[instance.getKey()] = instance
					continue
				}
				if !seenInferInstanceIp[instance.IP] || !seenInferRole[instance.Role] {
					seenInferInstanceIp[instance.IP] = true
					seenInferRole[instance.Role] = true
					instance.MetricsLabels = make(map[string]map[string]prometheus.Labels)
					c.instances[instance.getKey()] = instance
				}
			} else {
				instance.MetricsLabels = make(map[string]map[string]prometheus.Labels)
				c.instances[instance.getKey()] = instance
			}
		}
	}

	// For scale-in, remove instances from collector
	for _, instance := range deletedInstance {
		if deleted := c.removeInstanceMetrics(instance); deleted {
			logger.Logger().Infof("scale down instance %s:%d, delete this instance's metrics", instance.IP, instance.Port)
			delete(c.instances, instance.getKey())
		}
	}
}

func (c *Collector) removeInstanceMetrics(instance Instance) bool {
	metricsLabels := instance.MetricsLabels
	c.mapMutex.Lock()
	defer c.mapMutex.Unlock()

	var failedDeletions int
	var totalMetrics int

	for metricsName, labels := range metricsLabels {
		totalMetrics++
		if gauge, ok := c.gaugeMetrics[metricsName]; ok {
			if failed := removeMetricsVc(labels, gauge.MetricVec); failed > 0 {
				failedDeletions += failed
				logger.Logger().Errorf("failed to delete %d gauge metrics %s for instance %s:%d",
					failed, metricsName, instance.IP, instance.Port)
			}
		}
		if counter, ok := c.counterMetrics[metricsName]; ok {
			if failed := removeMetricsVc(labels, counter.MetricVec); failed > 0 {
				failedDeletions += failed
				logger.Logger().Errorf("failed to delete %d counter metrics %s for instance %s:%d",
					failed, metricsName, instance.IP, instance.Port)
			}
		}
		if histogram, ok := c.histogramMetrics[metricsName]; ok {
			if failed := removeHistogramMetrics(labels, histogram); failed > 0 {
				failedDeletions += failed
				logger.Logger().Errorf("failed to delete %d histogram metrics %s for instance %s:%d",
					failed, metricsName, instance.IP, instance.Port)
			}
		}
	}

	if failedDeletions > 0 {
		logger.Logger().Errorf("cleanup completed for instance %s:%d: %d/%d metrics failed to delete",
			instance.IP, instance.Port, failedDeletions, totalMetrics)
	} else {
		logger.Logger().Infof("all metrics successfully deleted for instance %s:%d (%d metrics)",
			instance.IP, instance.Port, totalMetrics)
	}

	// Always return true to ensure instance is deleted and avoid retry attempts
	// Even if some metrics fail to delete, continue cleaning up other resources
	return true
}

func removeMetricsVc(labelsMap map[string]prometheus.Labels, metricsVec *prometheus.MetricVec) int {
	var failedCount int
	for _, labels := range labelsMap {
		if !metricsVec.Delete(labels) {
			failedCount++
		}
	}
	return failedCount
}

func removeHistogramMetrics(labelsMap map[string]prometheus.Labels, metricsVec *CustomHistogram) int {
	var failedCount int
	for _, labels := range labelsMap {
		if !metricsVec.Delete(labels) {
			failedCount++
		}
	}
	return failedCount
}

func (c *Collector) collectMetrics() {
	var wg sync.WaitGroup
	// Update instances
	c.updateInstanceStatus(getLatestInstances())
	for _, instance := range c.instances {
		wg.Add(1)
		// Start a goroutine for each instance for concurrent collection
		go func(instance Instance) {
			defer wg.Done()
			metrics, err := c.fetchMetrics(instance)
			if err != nil {
				metrics2.RecordMetricsCollectionFailures()
				logger.Logger().Errorf("failed to fetch metrics: %s:%d, role %s error %s", instance.IP, instance.Port, instance.Role, err.Error())
				return
			}
			c.recordMetrics(&instance, metrics)
		}(instance)
	}

	// Wait for all metrics collection to complete
	wg.Wait()

	// Perform metrics aggregation
	c.AggregateMetrics()
}

func (c *Collector) fetchMetrics(instance Instance) (string, error) {
	// Set timeout for metrics fetching requests
	client := &http.Client{Timeout: time.Duration(c.metricsConfig.MetricsRequestTimeout) * time.Second}

	// build request with port and ip
	var url string
	if instance.Role == "Scheduler" {
		client.Transport = &http.Transport{TLSClientConfig: c.peerClientTlsCfg}
		url = fmt.Sprintf("http://%s:%d/internal/metrics", instance.IP, instance.Port)
	} else {
		client.Transport = &http.Transport{TLSClientConfig: c.instanceClientTlsCfg}
		url = fmt.Sprintf("http://%s:%d/metrics", instance.IP, instance.Port)
	}

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", err
	}
	if constant.ServerName != "" {
		req.Header.Set(constant.XRouterId, constant.ServerName)
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to fetch metrics: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected status: %s", resp.Status)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response body: %w", err)
	}

	return string(body), nil
}

func (c *Collector) recordMetrics(instance *Instance, metrics string) {
	// Create Prometheus text format parser
	parser := expfmt.TextParser{}
	reader := strings.NewReader(metrics)

	// Parse metrics
	metricFamilies, err := parser.TextToMetricFamilies(reader)
	if err != nil {
		logger.Logger().Infof("failed to parse metrics: %v", err)
		return
	}

	// Logic can be optimized to filter only needed metrics
	for name, m := range metricFamilies {
		for _, metric := range m.GetMetric() {
			// Newly added labels, currently two:
			// 1. role: Prefill, Decode
			// 2. instance: currently defaults to instance IP
			labels := prometheus.Labels{
				"role":     instance.Role,
				"instance": fmt.Sprintf("%s:%d", instance.IP, instance.Port),
			}

			// Add existing labels from the metric
			var list []string
			for _, label := range metric.GetLabel() {
				if *label.Name == "pid" {
					continue
				}
				list = append(list, *label.Name)
				labels[*label.Name] = *label.Value
				if *label.Name == ModelName {
					c.modelName = *label.Value
				}
			}

			var names []string
			if strings.ToLower(instance.Role) == "scheduler" {
				vllmName := getRealMetricsName(name)
				if vllmName == nil {
					continue
				}
				names = vllmName
				// For scheduler, engine and model_name labels might be missing
				if _, exist := labels[Engine]; !exist {
					list = append(list, Engine)
					labels[Engine] = ""
				}
				if _, exist := labels[ModelName]; !exist {
					list = append(list, ModelName)
					labels[ModelName] = ""
				}
			}

			if len(names) != 0 {
				for _, name := range names {
					c.registerAndRecordMetrics(m, name, list, labels, metric, instance)
				}
			} else {
				c.registerAndRecordMetrics(m, name, list, labels, metric, instance)
			}
		}
	}
}

func (c *Collector) registerAndRecordMetrics(m *dto.MetricFamily, name string, list []string,
	labels prometheus.Labels, metric *dto.Metric, instance *Instance) {
	c.registerDiscoveredMetrics(m, list, name)
	switch m.GetType() {
	case dto.MetricType_GAUGE:
		recordGaugeMetrics(c, name, labels, metric)
	case dto.MetricType_COUNTER:
		recordCounterMetrics(c, name, labels, metric)
	case dto.MetricType_HISTOGRAM:
		recordHistogramMetrics(c, name, labels, metric)
	default:
		logger.Infof("unknown type [%v]", m.GetType())
	}
	// Maintain collected label information for the metric
	instance.updateInstanceLabels(name, labels)
}

func (c *Collector) AggregateMetrics() {
	// Aggregate Gauge metrics
	for _, cfg := range c.metricsConfig.Configurations {
		err := processMetricsAccordingConfiguration(cfg, c)
		if err != nil {
			logger.Logger().Errorf("Error processing configuration %s: %v", cfg.MetricsName, err)
			continue
		}
	}

}

func (c *Collector) isMetricRegistered(metricsName string) bool {
	c.mapMutex.RLock()
	defer c.mapMutex.RUnlock()
	return c.isMetricRegisteredUnsafe(metricsName)
}

// isMetricRegisteredUnsafe is the lock-free version, used when caller already holds the lock
func (c *Collector) isMetricRegisteredUnsafe(metricsName string) bool {
	if _, ok := c.gaugeMetrics[metricsName]; ok {
		return true
	}
	if _, ok := c.counterMetrics[metricsName]; ok {
		return true
	}
	if _, ok := c.histogramMetrics[metricsName]; ok {
		return true
	}
	return false
}

// Auto-register metrics
func (c *Collector) registerDiscoveredMetrics(m *dto.MetricFamily, labels []string, metricsName string) {
	// The first-level metrics map is only written here, read elsewhere. Second-level maps like
	// *prometheus.GaugeVec have internal locking
	c.mapMutex.Lock()
	defer c.mapMutex.Unlock()
	// Check if metric is already registered
	if c.isMetricRegisteredUnsafe(metricsName) {
		return
	}
	switch m.GetType() {
	case dto.MetricType_GAUGE:
		registerGaugeMetrics(c, m, labels, metricsName)
	case dto.MetricType_COUNTER:
		registerCounterMetrics(c, m, labels, metricsName)
	case dto.MetricType_HISTOGRAM:
		registerHistogramMetrics(c, m, labels, metricsName)
	default:
		logger.Infof("unknown type [%v]", m.GetType())
	}
}

func recordGaugeMetrics(c *Collector, name string, labels prometheus.Labels, metric *dto.Metric) {
	if metric.Gauge == nil {
		return
	}

	c.mapMutex.RLock()
	gauge, ok := c.gaugeMetrics[name]
	c.mapMutex.RUnlock()

	if ok {
		gauge.With(labels).Set(metric.GetGauge().GetValue())
	}
}

func recordHistogramMetrics(c *Collector, name string, labels prometheus.Labels, metric *dto.Metric) {
	if metric.Histogram == nil {
		return
	}

	c.mapMutex.RLock()
	customHist, ok := c.histogramMetrics[name]
	c.mapMutex.RUnlock()

	if !ok {
		return
	}

	// Original histogram doesn't support direct assignment, need a custom histogram for calculation
	his := metric.Histogram
	sampleCount := his.GetSampleCount()
	sampleSum := his.GetSampleSum()
	buckets := his.GetBucket()

	hisData := &HistogramData{
		SampleCount: sampleCount,
		SampleSum:   sampleSum,
		Buckets:     make(map[float64]uint64),
	}
	for _, bucket := range buckets {
		hisData.Buckets[bucket.GetUpperBound()] = bucket.GetCumulativeCount()
	}

	customHist.SetHistogramData(labels, hisData)
}

func registerGaugeMetrics(c *Collector, m *dto.MetricFamily, labels []string, metricsName string) {
	registerLabels := getCollectLabels()
	registerLabels = append(registerLabels, labels...)
	gauge := createNewGaugeVec(metricsName, *m.Help, registerLabels)
	err := c.registry.Register(gauge)
	if err != nil {
		logger.Logger().Errorf("fail to register metrics %s, error %s", metricsName, err.Error())
		return
	}
	// After successful registration, update map
	c.gaugeMetrics[metricsName] = gauge
	// Check if it's a Union metric
	isUnionMetrics := registerUnionMetrics(c, metricsName, gauge)
	if isUnionMetrics {
		return
	}
	// Register metrics that need aggregation
	aggGauge := createNewGaugeVec(metricsName, *m.Help, labels)
	err = c.aggRegistry.Register(aggGauge)
	if err != nil {
		logger.Logger().Errorf("fail to register aggregated metrics %s, error %s", metricsName, err.Error())
		return
	}
	// After successful registration, update map
	c.aggregatedGaugeMetrics[metricsName] = aggGauge
}

func registerCounterMetrics(c *Collector, m *dto.MetricFamily, labels []string, metricsName string) {
	registerLabels := getCollectLabels()
	registerLabels = append(registerLabels, labels...)
	counter := createNewCounterVec(metricsName, *m.Help, registerLabels)
	err := c.registry.Register(counter)
	if err != nil {
		logger.Logger().Errorf("fail to register metrics %s, error %s", metricsName, err.Error())
		return
	}
	c.counterMetrics[metricsName] = counter
	// Check if it's a Union metric
	isUnionMetrics := registerUnionMetrics(c, metricsName, counter)
	if isUnionMetrics {
		return
	}

	aggCounter := createNewCounterVec(metricsName, *m.Help, labels)
	err = c.aggRegistry.Register(aggCounter)
	if err != nil {
		logger.Logger().Errorf("fail to register aggregated metrics %s, error %s", metricsName, err.Error())
		return
	}
	c.aggregatedCounterMetrics[metricsName] = aggCounter
}

func registerHistogramMetrics(c *Collector, m *dto.MetricFamily, labels []string, metricsName string) {
	registerLabels := getCollectLabels()
	registerLabels = append(registerLabels, labels...)
	var buckets []float64
	for _, metric := range m.Metric {
		if metric.Histogram != nil {
			for _, bucket := range metric.Histogram.GetBucket() {
				upperBound := bucket.GetUpperBound()
				// Filter +Inf boundary as it's not a real bucket boundary
				if !math.IsInf(upperBound, 1) {
					buckets = append(buckets, upperBound)
				}
			}
			// Only process bucket info from the first metric instance
			break
		}
	}
	histogram := createHistogramVec(metricsName, *m.Help, registerLabels)
	err := c.registry.Register(histogram)
	if err != nil {
		logger.Logger().Errorf("fail to register metrics %s error %s", metricsName, err.Error())
		return
	}
	c.histogramMetrics[metricsName] = histogram
	// Check if it's a Union metric
	isUnionMetrics := registerUnionMetrics(c, metricsName, histogram)
	if isUnionMetrics {
		return
	}

	aggHistogram := createHistogramVec(metricsName, *m.Help, labels)
	err = c.aggRegistry.Register(aggHistogram)
	if err != nil {
		logger.Logger().Errorf("fail to register aggregated metrics %s, error %s", metricsName, err.Error())
		return
	}
	c.aggregatedHistogramMetrics[metricsName] = aggHistogram
}

func registerUnionMetrics(c *Collector, metricsName string, metrics prometheus.Collector) bool {
	if op, ok := c.metricsConfig.metricOperation[metricsName]; ok {
		// For union metrics, no need to create aggregated metric object, reuse the metrics list from each instance
		if op == "union" {
			err := c.aggRegistry.Register(metrics)
			if err != nil {
				logger.Logger().Errorf("fail to register union metrics %s, error %s", metricsName, err.Error())
				return true
			}
			return true
		}
	}
	return false
}

func createNewGaugeVec(metricsName string, help string, labels []string) *prometheus.GaugeVec {
	return prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: metricsName,
			Help: help,
		},
		labels,
	)
}

func createNewCounterVec(metricsName string, help string, labels []string) *prometheus.CounterVec {
	return prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: metricsName,
			Help: help,
		},
		labels,
	)
}

func createHistogramVec(metricsName string, help string, labels []string) *CustomHistogram {
	return NewCustomHistogram(
		prometheus.HistogramOpts{
			Name: metricsName,
			Help: help,
		}, labels,
	)
}

func TransInstanceMap(latestInstances []Instance) map[string]Instance {
	instanceMap := make(map[string]Instance)
	for _, instance := range latestInstances {
		instanceMap[instance.getKey()] = instance
	}
	return instanceMap
}

func StopMetricsCollector() {
	if metricChan != nil {
		logger.Logger().Infof("Stop metrics collector")
		close(metricChan)
	}
}
