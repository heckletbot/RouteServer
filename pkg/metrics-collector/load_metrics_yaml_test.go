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
	"os"
	"testing"
)

func TestLoadMetricsYamlConfig(t *testing.T) {
	// Create temporary file
	tmpfile, err := os.CreateTemp("", "testfile")
	if err != nil {
		t.Fatal(err)
	}
	defer tmpfile.Close()           // Close temporary file
	defer os.Remove(tmpfile.Name()) // Delete temporary file

	// Write test data
	_, err = tmpfile.Write([]byte(`configurations:
  - metric_name: metric1
    acting_instance: prefill
    operation: sum
    type: Gauge
  - metric_name: metric2
    acting_instance: decode
    operation: union
    type: Counter`))
	if err != nil {
		t.Fatal(err)
	}

	// Test case when file does not exist
	_, err = loadMetricsYamlConfig("nonexistentfile")
	if err == nil {
		t.Error("Expected an error when file does not exist")
	}

	// Test normal case
	config, err := loadMetricsYamlConfig(tmpfile.Name())
	if err != nil {
		t.Error(err)
	}
	if len(config.Configurations) != 2 {
		t.Errorf("Expected 2 configurations, got %d", len(config.Configurations))
	}
	if config.Configurations[0].MetricsName != "metric1" {
		t.Errorf("Expected metric1, got %s", config.Configurations[0].MetricsName)
	}
	if config.Configurations[1].Operation != "union" {
		t.Errorf("Expected union, got %s", config.Configurations[1].Operation)
	}
}
