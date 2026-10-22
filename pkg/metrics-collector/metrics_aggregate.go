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
	"reflect"
	"strings"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// combineHistogramMetrics performs histogram combine operation on target metrics data
func combineHistogramMetrics(c *Collector, metricsName string, role string) error {
	sampleCount := uint64(0)
	sampleSum := 0.0
	buckets := make(map[float64]uint64)
	var labels = prometheus.Labels{}

	c.mapMutex.RLock()
	metrics, ok := c.histogramMetrics[metricsName]
	c.mapMutex.RUnlock()

	if !ok {
		return nil
	}

	for _, instance := range c.instances {
		// Instance must match target role to be counted. Note: api_server includes all P and D instances
		if strings.ToLower(instance.Role) == role || (role == "api_server" && strings.ToLower(instance.Role) != "scheduler") {
			allLabels := instance.getInstanceLabelsForMetrics(metricsName)
			for _, label := range allLabels {
				if len(labels) == 0 {
					labels = filterCustomLabels(label)
					labels["engine"] = "All"
					labels["model_name"] = c.modelName
				}
				histogramData := metrics.GetHistogramData(label)
				sampleCount += histogramData.SampleCount
				sampleSum += histogramData.SampleSum
				for upperBound, count := range histogramData.Buckets {
					buckets[upperBound] += count
				}
			}
		}
	}

	if len(labels) > 0 {
		// Write to Prometheus if target data is obtained
		c.mapMutex.RLock()
		aggMetrics, ok := c.aggregatedHistogramMetrics[metricsName]
		c.mapMutex.RUnlock()

		if ok && aggMetrics != nil {
			aggMetrics.SetHistogramData(labels, &HistogramData{
				SampleCount: sampleCount,
				SampleSum:   sampleSum,
				Buckets:     buckets,
			})
		} else {
			return fmt.Errorf("aggMetrics is nil")
		}
	}
	return nil
}

// sumMetrics performs sum operation on target metrics
func sumMetrics(c *Collector, metricsName string, metricsType string, role string) error {
	if metricsType == "Gauge" {
		err := sumGaugeMetrics(c, metricsName, role)
		if err != nil {
			return err
		}
	} else if metricsType == "Counter" {
		err := sumCounterMetrics(c, metricsName, role)
		if err != nil {
			return err
		}
	} else {
		return fmt.Errorf("metrics type %s is not supported", metricsType)
	}
	return nil
}

// sumGaugeMetrics performs sum operation on Gauge type metrics data
func sumGaugeMetrics(c *Collector, metricsName string, role string) error {
	gaugeCount := float64(0)
	var labels = prometheus.Labels{}

	c.mapMutex.RLock()
	metrics, ok := c.gaugeMetrics[metricsName]
	c.mapMutex.RUnlock()

	if !ok {
		return nil
	}

	for _, instance := range c.instances {
		// Instance must match target role to be counted. Note: api_server includes all P and D instances
		if strings.ToLower(instance.Role) == role || (role == "api_server" && strings.ToLower(instance.Role) != "scheduler") {
			allLabels := instance.getInstanceLabelsForMetrics(metricsName)
			for _, label := range allLabels {
				if len(labels) == 0 {
					labels = filterCustomLabels(label)
					labels["engine"] = "All"
					labels["model_name"] = c.modelName
				}
				value := getMetricsValueFromSpecificLabels(label, metrics)
				gaugeCount += value
			}
		}
	}

	if len(labels) > 0 {
		// Write to Prometheus if target data is obtained
		c.mapMutex.RLock()
		aggMetrics, ok := c.aggregatedGaugeMetrics[metricsName]
		c.mapMutex.RUnlock()

		if ok {
			aggMetrics.With(labels).Set(gaugeCount)
		}
	}
	return nil
}

// sumCounterMetrics performs sum operation on Counter type metrics data
func sumCounterMetrics(c *Collector, metricsName string, role string) error {
	c.mapMutex.RLock()
	metrics, ok := c.counterMetrics[metricsName]
	c.mapMutex.RUnlock()

	if !ok {
		return nil
	}

	targetLabels := getTargetLabels(c.instances, metricsName)

	for _, targetLabel := range targetLabels {

		var labels = prometheus.Labels{}
		counterCount := float64(0)

		for _, instance := range c.instances {
			// Instance must match target role to be counted. Note: api_server includes all P and D instances
			if strings.ToLower(instance.Role) == role ||
				(role == "api_server" && strings.ToLower(instance.Role) != "scheduler") ||
				optionForRequestSuccess(role, strings.ToLower(instance.Role), metricsName, targetLabel) {

				allLabels := instance.getInstanceLabelsForMetrics(metricsName)
				for _, label := range allLabels {
					if !labelsMatch(label, targetLabel) {
						continue
					}
					if len(labels) == 0 {
						labels = filterCustomLabels(label)
						labels["engine"] = "All"
						labels["model_name"] = c.modelName
					}
					value := getMetricsValueFromSpecificLabels(label, metrics)
					counterCount += value
				}
			}
		}
		if len(labels) > 0 {
			// Write to Prometheus if target data is obtained
			setAggCounterValue(c, metricsName, labels, counterCount)
		}
	}
	return nil
}

// optionForRequestSuccess handles special processing for vllm:request_success_total metric
func optionForRequestSuccess(role string, instanceRole string, metricsName string, targetLabel prometheus.Labels) bool {
	if role != "option" {
		return false
	}
	if metricsName != "vllm:request_success_total" {
		return false
	}

	// finished_reason="abort" -> count P+D
	if labelsMatch(targetLabel, prometheus.Labels{"finished_reason": "abort"}) && instanceRole != "scheduler" {
		return true
	}

	// finished_reason="length" -> count D
	if labelsMatch(targetLabel, prometheus.Labels{"finished_reason": "length"}) && instanceRole == "decode" {
		return true
	}

	// finished_reason="stop" -> count D
	if labelsMatch(targetLabel, prometheus.Labels{"finished_reason": "stop"}) && instanceRole == "decode" {
		return true
	}

	return false
}

// filterCustomLabels filters custom labels and restores to original labels
// Note: instance and role are considered custom added labels by default and should be excluded
func filterCustomLabels(src prometheus.Labels) prometheus.Labels {
	dst := prometheus.Labels{}
	for k, v := range src {
		if k == "instance" || k == "role" {
			continue
		}
		dst[k] = v
	}
	return dst
}

func isEmptyInstance(instance Instance) bool {
	return reflect.DeepEqual(instance, Instance{})
}

// getTargetLabels gets target metrics that need to be filtered
// Note: instance, role, and engine are considered filterable variable labels by default, remaining labels are target labels
func getTargetLabels(instances map[string]Instance, metricsName string) []prometheus.Labels {
	var apiServerInstance Instance
	labelsList := make([]prometheus.Labels, 0)

	for _, instance := range instances {
		if metricsName == "vllm:infer_requests_failure_status_total" {
			if strings.ToLower(instance.Role) == "scheduler" {
				apiServerInstance = instance
				break
			}
		} else {
			if strings.ToLower(instance.Role) == "prefill" || strings.ToLower(instance.Role) == "decode" {
				apiServerInstance = instance
				break
			}
		}
	}

	if isEmptyInstance(apiServerInstance) {
		return labelsList
	}

	allLabels := apiServerInstance.getInstanceLabelsForMetrics(metricsName)
	for _, label := range allLabels {
		targetLabel := filterTargetLabels(label)
		labelsList = append(labelsList, targetLabel)
	}
	return labelsList
}

func filterTargetLabels(src prometheus.Labels) prometheus.Labels {
	dst := prometheus.Labels{}

	for k, v := range src {
		if k == "instance" || k == "role" || k == "engine" {
			continue
		}
		dst[k] = v
	}
	return dst
}

// labelsMatch checks if target labels are contained in the given labels
func labelsMatch(fullLabels, targetLabels prometheus.Labels) bool {
	for key, value := range targetLabels {
		if fullLabelValue, exists := fullLabels[key]; !exists || fullLabelValue != value {
			return false
		}
	}
	return true
}

// getMetricsValueFromSpecificLabels gets the corresponding metrics value through labels
func getMetricsValueFromSpecificLabels(labels prometheus.Labels, sourceMetricVec interface{}) float64 {
	switch metricVec := sourceMetricVec.(type) {
	case *prometheus.GaugeVec:
		gaugeMetrics := metricVec.With(labels)
		metric := &dto.Metric{}
		err := gaugeMetrics.Write(metric)
		if err == nil && metric.Gauge != nil {
			value := metric.Gauge.GetValue()
			return value
		}
	case *prometheus.CounterVec:
		counterMetrics := metricVec.With(labels)
		metric := &dto.Metric{}
		err := counterMetrics.Write(metric)
		if err == nil && metric.Counter != nil {
			value := metric.Counter.GetValue()
			return value
		}
	default:
		return 0.0
	}
	return 0.0
}

// recordCounterMetrics records Counter type metrics data
func recordCounterMetrics(c *Collector, name string, labels prometheus.Labels, metric *dto.Metric) {
	if metric.Counter == nil {
		return
	}

	c.mapMutex.RLock()
	_, ok := c.counterMetrics[name]
	c.mapMutex.RUnlock()

	if ok {
		// Native Counter type only supports Add method for monotonic increment
		// Implemented by calculating growth difference
		currentValue := metric.Counter.GetValue()
		setCounterValue(c, name, labels, currentValue)
	}
}

// setCounterValue sets Counter type data value
func setCounterValue(c *Collector, name string, labels prometheus.Labels, value float64) {
	c.mapMutex.Lock()
	defer c.mapMutex.Unlock()
	writeToCounterMetrics(c.counterMetrics, name, labels, value)
}

// setAggCounterValue sets the value of aggregated Counter type data
func setAggCounterValue(c *Collector, name string, labels prometheus.Labels, value float64) {
	c.mapMutex.Lock()
	defer c.mapMutex.Unlock()
	writeToCounterMetrics(c.aggregatedCounterMetrics, name, labels, value)
}

// writeToCounterMetrics writes Counter metrics to Prometheus
func writeToCounterMetrics(counterMetrics map[string]*prometheus.CounterVec, name string, labels prometheus.Labels,
	value float64) {
	if counter, ok := counterMetrics[name]; ok && value >= 0 {
		counterInstance := counter.With(labels)
		newCounterMetrics := &dto.Metric{}
		err := counterInstance.Write(newCounterMetrics)
		if err == nil && newCounterMetrics.Counter != nil {
			storedValue := newCounterMetrics.Counter.GetValue()
			delta := value - storedValue
			if delta >= 0 {
				counterInstance.Add(delta)
			}
		} else {
			counterInstance.Add(value)
		}
	}
}
