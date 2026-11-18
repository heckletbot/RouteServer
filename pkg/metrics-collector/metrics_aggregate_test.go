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
	"github.com/prometheus/client_golang/prometheus"
	"reflect"
	"testing"
)

func TestOptionForRequestSuccess(t *testing.T) {
	tests := []struct {
		name         string
		role         string
		instanceRole string
		metricsName  string
		targetLabel  map[string]string
		want         bool
	}{
		{
			name:         "role is not option",
			role:         "api_server",
			instanceRole: "scheduler",
			metricsName:  "vllm:request_success_total",
			targetLabel:  map[string]string{"finished_reason": "abort"},
			want:         false,
		},
		{
			name:         "metricsName is not vllm:request_success_total",
			role:         "option",
			instanceRole: "scheduler",
			metricsName:  "not_vllm:request_success_total",
			targetLabel:  map[string]string{"finished_reason": "abort"},
			want:         false,
		},
		{
			name:         "instanceRole is scheduler and finished_reason is abort",
			role:         "option",
			instanceRole: "scheduler",
			metricsName:  "vllm:request_success_total",
			targetLabel:  map[string]string{"finished_reason": "abort"},
			want:         false,
		},
		{
			name:         "instanceRole is not scheduler and finished_reason is abort",
			role:         "option",
			instanceRole: "prefill",
			metricsName:  "vllm:request_success_total",
			targetLabel:  map[string]string{"finished_reason": "abort"},
			want:         true,
		},
		{
			name:         "instanceRole is decode and finished_reason is stop",
			role:         "option",
			instanceRole: "decode",
			metricsName:  "vllm:request_success_total",
			targetLabel:  map[string]string{"finished_reason": "stop"},
			want:         true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := optionForRequestSuccess(tt.role, tt.instanceRole, tt.metricsName, tt.targetLabel)
			if got != tt.want {
				t.Errorf("optionForRequestSuccess() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFilterCustomLabels(t *testing.T) {
	// Test case 1: when src is empty, dst should also be empty
	t.Run("EmptySrc", func(t *testing.T) {
		src := prometheus.Labels{}
		dst := filterCustomLabels(src)
		if len(dst) != 0 {
			t.Errorf("Expected dst to be empty, but got %v", dst)
		}
	})

	// Test case 2: when src contains "instance" and "role", dst should not contain these two labels
	t.Run("SrcContainsInstanceAndRole", func(t *testing.T) {
		src := prometheus.Labels{
			"instance": "localhost:9090",
			"role":     "alertmanager",
			"job":      "prometheus",
		}
		dst := filterCustomLabels(src)
		expected := prometheus.Labels{
			"job": "prometheus",
		}
		if !reflect.DeepEqual(dst, expected) {
			t.Errorf("Expected dst to be %v, but got %v", expected, dst)
		}
	})

	// Test case 3: when src does not contain "instance" and "role", dst should be the same as src
	t.Run("SrcNotContainsInstanceAndRole", func(t *testing.T) {
		src := prometheus.Labels{
			"job": "prometheus",
			"env": "production",
		}
		dst := filterCustomLabels(src)
		if !reflect.DeepEqual(dst, src) {
			t.Errorf("Expected dst to be %v, but got %v", src, dst)
		}
	})
}

func TestIsEmptyInstance(t *testing.T) {
	// Test case 1: instance is an empty instance
	{
		instance := Instance{}
		if !isEmptyInstance(instance) {
			t.Errorf("Expected true, got false")
		}
	}

	// Test case 2: instance is not an empty instance
	{
		// Assuming Instance has a field named 'Field'
		instance := Instance{Role: "Prefill"}
		if isEmptyInstance(instance) {
			t.Errorf("Expected false, got true")
		}
	}
}

func TestGetTargetLabels(t *testing.T) {
	// Test case 1: when instance list is empty, should return empty list
	var instances []Instance
	metricsName := "testMetrics"
	labels := getTargetLabels(TransInstanceMap(instances), metricsName)
	if len(labels) != 0 {
		t.Errorf("Expected an empty list, but got %v", labels)
	}

	// Test case 2: when instance list has no instances with "prefill" or "decode" role, should return empty list
	instances = []Instance{
		{
			Role: "role1",
		},
		{
			Role: "role2",
		},
	}
	labels = getTargetLabels(TransInstanceMap(instances), metricsName)
	if len(labels) != 0 {
		t.Errorf("Expected an empty list, but got %v", labels)
	}

	// Test case 3: when instance list has instances with "prefill" or "decode" role, should return non-empty list
	instances = []Instance{
		{
			Role: "Decode",
			MetricsLabels: map[string]map[string]prometheus.Labels{
				"testMetrics": {
					"engine=31|finished_reason=abort|instance=127.0.0.1:9116|model_name=deepseek|role=decode": {
						"engine":          "31",
						"instance":        "127.0.0.1:9116",
						"model_name":      "deepseek",
						"decode":          "decode",
						"finished_reason": "abort",
					},
				},
			},
		},
	}
	labels = getTargetLabels(TransInstanceMap(instances), metricsName)
	if len(labels) == 0 {
		t.Errorf("Expected a non-empty list, but got %v", labels)
	}
}

func TestFilterTargetLabels(t *testing.T) {
	// Test case 1: no labels need to be filtered in source labels
	src := prometheus.Labels{
		"job": "prometheus",
		"env": "production",
	}
	dst := filterTargetLabels(src)
	if len(dst) != len(src) {
		t.Errorf("Expected %d labels, got %d", len(src), len(dst))
	}

	// Test case 2: one label needs to be filtered in source labels
	src = prometheus.Labels{
		"job":      "prometheus",
		"instance": "localhost:9090",
	}
	dst = filterTargetLabels(src)
	if len(dst) != len(src)-1 {
		t.Errorf("Expected %d labels, got %d", len(src)-1, len(dst))
	}

	// Test case 3: multiple labels need to be filtered in source labels
	src = prometheus.Labels{
		"job":      "prometheus",
		"instance": "localhost:9090",
		"role":     "server",
		"engine":   "docker",
	}
	dst = filterTargetLabels(src)
	if len(dst) != len(src)-3 {
		t.Errorf("Expected %d labels, got %d", len(src)-3, len(dst))
	}

	// Test case 4: all labels in source need to be filtered
	src = prometheus.Labels{
		"instance": "localhost:9090",
		"role":     "server",
		"engine":   "docker",
	}
	dst = filterTargetLabels(src)
	if len(dst) != 0 {
		t.Errorf("Expected %d labels, got %d", 0, len(dst))
	}
}

func TestLabelsMatch(t *testing.T) {
	// Test case 1: when target labels are empty, should return true regardless of full labels
	{
		fullLabels := prometheus.Labels{
			"key1": "value1",
			"key2": "value2",
		}
		targetLabels := prometheus.Labels{}
		if !labelsMatch(fullLabels, targetLabels) {
			t.Errorf("Want true, got false")
		}
	}

	// Test case 2: when key in target labels doesn't exist in full labels, should return false
	{
		fullLabels := prometheus.Labels{
			"key1": "value1",
			"key2": "value2",
		}
		targetLabels := prometheus.Labels{
			"key3": "value3",
		}
		if labelsMatch(fullLabels, targetLabels) {
			t.Errorf("Want false, got true")
		}
	}

	// Test case 3: when key in target labels exists in full labels but value doesn't match, should return false
	{
		fullLabels := prometheus.Labels{
			"key1": "value1",
			"key2": "value2",
		}
		targetLabels := prometheus.Labels{
			"key1": "value3",
		}
		if labelsMatch(fullLabels, targetLabels) {
			t.Errorf("Want false, got true")
		}
	}

	// Test case 4: when key in target labels exists in full labels and value matches, should return true
	{
		fullLabels := prometheus.Labels{
			"key1": "value1",
			"key2": "value2",
		}
		targetLabels := prometheus.Labels{
			"key1": "value1",
		}
		if !labelsMatch(fullLabels, targetLabels) {
			t.Errorf("Want true, got false")
		}
	}

	// Test case 5: when key in target labels exists in full labels and value matches, but full labels have extra key-value pairs, should return true
	{
		fullLabels := prometheus.Labels{
			"key1": "value1",
			"key2": "value2",
			"key3": "value3",
		}
		targetLabels := prometheus.Labels{
			"key1": "value1",
		}
		if !labelsMatch(fullLabels, targetLabels) {
			t.Errorf("Want true, got false")
		}
	}
}
