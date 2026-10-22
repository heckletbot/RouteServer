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

package etcd

import (
	"fmt"
	"testing"

	"go.etcd.io/etcd/api/v3/mvccpb"

	"github.com/stretchr/testify/assert"
)

// TestEtcdWrapperParseAllInstanceFailuresFromKv_ValidJsonData tests parsing valid JSON data
func TestEtcdWrapperParseAllInstanceFailuresFromKv_ValidJsonData(t *testing.T) {
	ew := &EtcdWrapper{
		logId: 1001,
	}

	// Prepare valid JSON data with all fields populated
	jsonData := []byte(`{
		"id": "test-leader",
		"local_round": 1,
		"snapshot_time": "2025-05-08T10:00:00Z",
		"failures": {
			"worker1": {
				"role": "P",
				"reason": "failed to ping"
			},
			"worker2": {
				"role": "D",
				"reason": "connection timeout",
				"status": "unhealthy"
			}
		}
	}`)

	kv := &mvccpb.KeyValue{
		Key:   []byte("/cluster/instance_failures"),
		Value: jsonData,
	}

	result, err := ew.parseAllInstanceFailuresFromKv(kv)

	// Verify no error occurred
	assert.NoError(t, err, "Should not return error for valid JSON")
	assert.NotNil(t, result, "Result should not be nil")

	// Verify LeaderId is parsed correctly
	assert.Equal(t, "test-leader", result.LeaderId, "LeaderId should match 'test-leader'")

	// Verify LocalRound is parsed correctly
	assert.Equal(t, int32(1), result.LocalRound, "LocalRound should be 1")

	// Verify SnapshotTime is parsed correctly
	assert.Equal(t, "2025-05-08T10:00:00Z", result.SnapshotTime, "SnapshotTime should match")

	// Verify failures are parsed correctly
	assert.Contains(t, result.Failures, "worker1", "Failures should contain worker1")
	assert.Contains(t, result.Failures, "worker2", "Failures should contain worker2")

	// Verify worker1 details
	worker1Failure := result.Failures["worker1"]
	assert.Equal(t, "P", worker1Failure.Role, "Worker1 role should be 'P'")
	assert.Equal(t, "failed to ping", worker1Failure.Reason, "Worker1 reason should be 'failed to ping'")

	// Verify worker2 details
	worker2Failure := result.Failures["worker2"]
	assert.Equal(t, "D", worker2Failure.Role, "Worker2 role should be 'D'")
	assert.Equal(t, "connection timeout", worker2Failure.Reason, "Worker2 reason should be 'connection timeout'")
	assert.Equal(t, "unhealthy", worker2Failure.Status, "Worker2 status should be 'unhealthy'")
}

// TestEtcdWrapperParseAllInstanceFailuresFromKv_InvalidJsonData tests parsing invalid JSON data
func TestEtcdWrapperParseAllInstanceFailuresFromKv_InvalidJsonData(t *testing.T) {
	ew := &EtcdWrapper{
		logId: 1002,
	}

	// Prepare invalid JSON data
	invalidJsonData := []byte(`{"failures": {"worker1": {"role": "P", "reason": "failed to ping"}}`)

	kv := &mvccpb.KeyValue{
		Key:   []byte("/cluster/instance_failures"),
		Value: invalidJsonData,
	}

	result, err := ew.parseAllInstanceFailuresFromKv(kv)
	fmt.Println(result)
	fmt.Println(err)
	// Verify error is returned for invalid JSON
	assert.Error(t, err, "Should return error for invalid JSON")
	assert.Nil(t, result, "Result should be nil when error occurs")

	// Verify error message contains expected text
	assert.Contains(t, err.Error(), "failed to unmarshal", "Error message should mention unmarshaling failure")
	assert.Contains(t, err.Error(), "/cluster/instance_failures", "Error message should mention the key")
}

// TestEtcdWrapperParseAllInstanceFailuresFromKv_EmptyJsonData tests parsing empty JSON object
func TestEtcdWrapperParseAllInstanceFailuresFromKv_EmptyJsonData(t *testing.T) {
	ew := &EtcdWrapper{
		logId: 1003,
	}

	// Prepare empty JSON object
	emptyJsonData := []byte(`{}`)

	kv := &mvccpb.KeyValue{
		Key:   []byte("/cluster/instance_failures"),
		Value: emptyJsonData,
	}

	result, err := ew.parseAllInstanceFailuresFromKv(kv)

	// Verify no error for empty JSON
	assert.NoError(t, err, "Should not return error for empty JSON")
	assert.NotNil(t, result, "Result should not be nil")

	// Verify default values are set correctly
	assert.Equal(t, "", result.LeaderId, "LeaderId should be empty for empty JSON")
	assert.Equal(t, int32(0), result.LocalRound, "LocalRound should be 0 for empty JSON")
	assert.Equal(t, "", result.SnapshotTime, "SnapshotTime should be empty for empty JSON")

	// Verify failures map is nil for empty JSON (this is expected behavior)
	assert.Nil(t, result.Failures, "Failures map should be nil for empty JSON")
}

// TestEtcdWrapperParseAllInstanceFailuresFromKv_NilData tests parsing nil data
func TestEtcdWrapperParseAllInstanceFailuresFromKv_NilData(t *testing.T) {
	ew := &EtcdWrapper{
		logId: 1004,
	}

	// Prepare nil data
	kv := &mvccpb.KeyValue{
		Key:   []byte("/cluster/instance_failures"),
		Value: nil,
	}

	result, err := ew.parseAllInstanceFailuresFromKv(kv)

	// Verify error is returned for nil data
	assert.Error(t, err, "Should return error for nil data")
	assert.Nil(t, result, "Result should be nil when data is nil")

	// Verify error message contains expected text
	assert.Contains(t, err.Error(), "failed to unmarshal", "Error message should mention unmarshaling failure")
}

// TestEtcdWrapperParseAllInstanceFailuresFromKv_PartialJsonData tests parsing partial JSON data
func TestEtcdWrapperParseAllInstanceFailuresFromKv_PartialJsonData(t *testing.T) {
	ew := &EtcdWrapper{
		logId: 1005,
	}

	// Prepare JSON data with only some fields
	partialJsonData := []byte(`{
		"id": "partial-leader",
		"failures": {
			"worker1": {
				"role": "R"
			}
		}
	}`)

	kv := &mvccpb.KeyValue{
		Key:   []byte("/cluster/instance_failures"),
		Value: partialJsonData,
	}

	result, err := ew.parseAllInstanceFailuresFromKv(kv)

	// Verify no error for partial JSON
	assert.NoError(t, err, "Should not return error for partial JSON")
	assert.NotNil(t, result, "Result should not be nil")

	// Verify parsed fields
	assert.Equal(t, "partial-leader", result.LeaderId, "LeaderId should be parsed")
	assert.Equal(t, int32(0), result.LocalRound, "LocalRound should default to 0")
	assert.Equal(t, "", result.SnapshotTime, "SnapshotTime should default to empty")

	// Verify partial failures are parsed
	assert.Contains(t, result.Failures, "worker1", "Failures should contain worker1")
	worker1 := result.Failures["worker1"]
	assert.Equal(t, "R", worker1.Role, "Worker1 role should be 'R'")
	assert.Equal(t, "", worker1.Reason, "Worker1 reason should default to empty")
	assert.Equal(t, "", worker1.Status, "Worker1 status should default to empty")
}

// TestEtcdWrapperParseAllInstanceFailuresFromKv_DifferentKeys tests parsing with different key names
func TestEtcdWrapperParseAllInstanceFailuresFromKv_DifferentKeys(t *testing.T) {
	ew := &EtcdWrapper{
		logId: 1006,
	}

	testCases := []struct {
		name     string
		key      string
		expected bool
	}{
		{
			name:     "valid key",
			key:      "/cluster/instance_failures",
			expected: true,
		},
		{
			name:     "different key",
			key:      "/some/other/path",
			expected: true,
		},
		{
			name:     "empty key",
			key:      "",
			expected: true,
		},
	}

	jsonData := []byte(`{"id": "test"}`)

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			kv := &mvccpb.KeyValue{
				Key:   []byte(tc.key),
				Value: jsonData,
			}

			result, err := ew.parseAllInstanceFailuresFromKv(kv)

			// The parseAllInstanceFailuresFromKv method doesn't validate the key format
			if tc.expected {
				assert.NoError(t, err, "Should not return error for key: %s", tc.key)
				assert.NotNil(t, result, "Result should not be nil for key: %s", tc.key)
			}
		})
	}
}

// TestEtcdWrapperParseAllInstanceFailuresFromKv_LargeJsonData tests parsing large JSON data
func TestEtcdWrapperParseAllInstanceFailuresFromKv_LargeJsonData(t *testing.T) {
	ew := &EtcdWrapper{
		logId: 1007,
	}

	// Create JSON with many failure records
	jsonData := `{"id": "test-leader", "local_round": 5, "snapshot_time": "2025-05-08T10:00:00Z", "failures": {`
	for i := 0; i < 100; i++ {
		if i > 0 {
			jsonData += ","
		}
		workerId := fmt.Sprintf("worker%d", i)
		jsonData += fmt.Sprintf(`"%s": {"role": "P", "reason": "error %d"}`, workerId, i)
	}
	jsonData += "}}"

	kv := &mvccpb.KeyValue{
		Key:   []byte("/cluster/instance_failures"),
		Value: []byte(jsonData),
	}

	result, err := ew.parseAllInstanceFailuresFromKv(kv)

	// Verify large JSON is parsed correctly
	assert.NoError(t, err, "Should not return error for large JSON")
	assert.NotNil(t, result, "Result should not be nil")
	assert.Equal(t, "test-leader", result.LeaderId)
	assert.Equal(t, int32(5), result.LocalRound)
	assert.Len(t, result.Failures, 100, "Should parse all 100 failure records")
}
