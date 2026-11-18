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
	"fmt"
	"github.com/go-playground/validator/v10"
	"os"

	"github.com/goccy/go-yaml"
)

// IntegratedConfiguration defines the integrated configuration structure
type IntegratedConfiguration struct {
	// Metrics name: e.g. vllm:num_requests_running
	MetricsName string `yaml:"metric_name" validate:"required"`
	// Target instance: prefill, decode, scheduler, api_server, etc.
	ActingInstance string `yaml:"acting_instance" validate:"required,oneof=prefill decode scheduler api_server option"`
	// Operation type: sum, union, histogram_combine
	Operation string `yaml:"operation" validate:"required,oneof=sum union histogram_combine"`
	// Metrics type: gauge, counter, histogram
	Type string `yaml:"type" validate:"required,oneof=Gauge Counter Histogram"`
}

// MetricsConfig is the top-level configuration structure
type MetricsConfig struct {
	MetricsCollectInterval int                       `yaml:"metrics_collect_interval" default:"5"`
	MetricsRequestTimeout  int                       `yaml:"metrics_request_timeout" default:"10"`
	CollectFromUniqIp      bool                      `yaml:"collect_from_uniq_ip" default:"false"`
	Configurations         []IntegratedConfiguration `yaml:"configurations"`
	metricOperation        map[string]string
}

func loadMetricsYamlConfig(yamlPath string) (*MetricsConfig, error) {
	// Check if file exists
	if _, err := os.Stat(yamlPath); os.IsNotExist(err) {
		return nil, fmt.Errorf("config file %s does not exist", yamlPath)
	}

	data, err := os.ReadFile(yamlPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read file %s: %v", yamlPath, err)
	}

	var config MetricsConfig
	err = yaml.Unmarshal(data, &config)
	if err != nil {
		return nil, fmt.Errorf("failed to parse YAML in file %s: %v", yamlPath, err)
	}

	// Initialize validator
	validate := validator.New()
	// Custom validation: check if config is valid & if metric_name is duplicated
	metricNames := make(map[string]bool)
	for i, configuration := range config.Configurations {
		if err := validate.Struct(configuration); err != nil {
			// Validation failed, output detailed error information
			return nil, fmt.Errorf("metrics yaml config error, specifically for the %dth metric: %v", i+1, err)
		}
		if metricNames[configuration.MetricsName] {
			// Found duplicate metric_name
			return nil, fmt.Errorf("metrics yaml config invalid: "+
				"the %dth metric's metric_name=%s is duplicated", i+1, configuration.MetricsName)
		}
		metricNames[configuration.MetricsName] = true
	}

	metricOperation := make(map[string]string)
	for _, configuration := range config.Configurations {
		metricOperation[configuration.MetricsName] = configuration.Operation
	}
	config.metricOperation = metricOperation

	if config.MetricsRequestTimeout == 0 {
		config.MetricsRequestTimeout = 10
	}

	if config.MetricsCollectInterval == 0 {
		config.MetricsCollectInterval = 5
	}

	return &config, nil
}

// processMetricsAccordingConfiguration performs operations on specified metrics based on operation type defined in config
func processMetricsAccordingConfiguration(cfg IntegratedConfiguration, c *Collector) error {
	// Execute corresponding logic based on operation type
	switch cfg.Operation {
	case "sum":
		return performSumOperation(cfg, c)
	case "union":
		// union is already completed in registration scenario, no operation needed
		return nil
	case "histogram_combine":
		return performHistogramCombineOperation(cfg, c)
	default:
		return fmt.Errorf("unknown operation: %s", cfg.Operation)
	}
}

// performSumOperation performs sum operation
func performSumOperation(cfg IntegratedConfiguration, c *Collector) error {
	// Execute sum logic
	err := sumMetrics(c, cfg.MetricsName, cfg.Type, cfg.ActingInstance)
	if err != nil {
		return err
	}
	return nil
}

// performHistogramCombineOperation performs histogram combine operation
func performHistogramCombineOperation(cfg IntegratedConfiguration, c *Collector) error {
	// Execute histogram combine logic
	err := combineHistogramMetrics(c, cfg.MetricsName, cfg.ActingInstance)
	if err != nil {
		return err
	}

	return nil
}
