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
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"route-server/pkg/common/constant"
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

// TestConcurrentAccess checks that concurrent reads are safe while the stats sync
// loop writes. Races only surface under -race, so the test also asserts that the
// state stays self-consistent once the workers finish.
func TestConcurrentAccess(t *testing.T) {
	zcs := &ZeroCopyStats{
		Id:           "test_id",
		Role:         "prefill",
		Status:       Init,
		StaticInfo:   make(map[string]string),
		NumericStats: nil,
	}

	const iterations = 100
	var wg sync.WaitGroup

	// Writer: simulates the metric sync loop.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			data := map[string][]*MetricInfo{
				"key1": {{Value: float64(i)}},
			}
			zcs.UpdateNumericStats(&data, time.Now())
		}
	}()

	// Writer: simulates the health check loop.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			zcs.SetStatus(Available)
			zcs.SetStatus(Faulty)
		}
	}()

	// Readers: simulate the scheduling and request paths.
	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				_ = zcs.GetNumericStat("key1")
				_ = zcs.GetNumericStatKeys()
				_ = zcs.GetStatus()
				_ = zcs.GetPreStatus()
				_ = zcs.GetSLOTag()
				_ = zcs.GetTimeStamp()
				_ = zcs.GetStat("missing")
			}
		}()
	}

	wg.Wait()

	// After the writers stop the state must be consistent: SLOTag lazily defaulted
	// and the latest metric key still readable.
	if got, want := zcs.GetSLOTag(), constant.SLOTagDefault; got != want {
		t.Errorf("SLOTag = %q, want %q", got, want)
	}
	keys := zcs.GetNumericStatKeys()
	if keys == nil || len(*keys) != 1 || (*keys)[0] != "key1" {
		t.Errorf("NumericStatKeys = %v, want [key1]", keys)
	}
	if stat := zcs.GetNumericStat("key1"); len(stat) != 1 {
		t.Errorf("GetNumericStat(key1) length = %d, want 1", len(stat))
	}
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
