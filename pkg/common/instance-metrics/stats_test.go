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

package instance_metrics

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// Test UpdateNumericStats method
func TestUpdateNumericStats(t *testing.T) {
	// Initialize test object
	zcs := &ZeroCopyStats{
		Id:           "test_id",
		Role:         "prefill",
		Status:       Available,
		StaticInfo:   make(map[string]string),
		NumericStats: nil,
	}

	// Test passing nil
	result := zcs.UpdateNumericStats(nil, time.Now())
	assert.Equal(t, zcs, result)
	assert.Nil(t, zcs.NumericStats)

	// Prepare test data
	testData := map[string][]*MetricInfo{
		"key1": {&MetricInfo{Value: 100}},
		"key2": {&MetricInfo{Value: 200}},
	}
	testDataPtr := &testData

	// Test passing non-nil
	result = zcs.UpdateNumericStats(testDataPtr, time.Now())
	assert.Equal(t, zcs, result)
	assert.Equal(t, testDataPtr, zcs.NumericStats)
}

// Test GetNumericStat method
func TestGetNumericStat(t *testing.T) {
	// Initialize test object
	zcs := &ZeroCopyStats{
		Id:           "test_id",
		Role:         "prefill",
		Status:       Available,
		StaticInfo:   make(map[string]string),
		NumericStats: nil,
	}

	// Test when NumericStats is nil
	stats := zcs.GetNumericStat("key")
	assert.Nil(t, stats)

	// Prepare test data
	testData := map[string][]*MetricInfo{
		"key1": {&MetricInfo{Value: 100}},
		"key2": {&MetricInfo{Value: 200}},
	}
	zcs.NumericStats = &testData

	// Test getting existing key
	stats = zcs.GetNumericStat("key1")
	assert.Equal(t, []*MetricInfo{{Value: 100}}, stats)

	// Test getting non-existing key
	stats = zcs.GetNumericStat("key3")
	assert.Nil(t, stats)
}

// Test String method
func TestString(t *testing.T) {
	// Test Init status
	assert.Equal(t, "deploying", Init.String())

	// Test Available status
	assert.Equal(t, "running", Available.String())

	// Test Faulty status
	assert.Equal(t, "fault", Faulty.String())

	// Test unknown status
	unknownStatus := InstanceStatus(999)
	assert.Equal(t, "Unknown(999)", unknownStatus.String())
}
