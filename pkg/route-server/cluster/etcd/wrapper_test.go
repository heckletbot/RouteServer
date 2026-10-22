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
	"testing"

	"github.com/stretchr/testify/assert"
	"go.etcd.io/etcd/api/v3/mvccpb"
	"infer-dp-scheduler/pkg/common/utils"
)

// TestParseAllInstanceFailuresFromKv_Success tests parsing valid KV data
func TestParseAllInstanceFailuresFromKv_Success(t *testing.T) {
	ew := &EtcdWrapper{}

	failures := &utils.AllInstanceFailuresSnapshot{
		LeaderId:     "leader-1",
		LocalRound:   5,
		SnapshotTime: "2025-01-01T00:00:00Z",
		Failures: map[string]*utils.InstanceFailureRecord{
			"inst01": {
				Role:                           "prefill",
				Status:                         "faulty",
				Reason:                         "health check failed",
				ConsecutiveFailureTime:         3,
				ConsecutiveUnreachableTime:     5,
				HealthCheckCountAfterUnhealthy: 2,
			},
		},
	}

	marshal, err := utils.SonicApi.Marshal(failures)
	assert.Nil(t, err)

	kv := &mvccpb.KeyValue{
		Key:   []byte("/rs/instances/failures"),
		Value: marshal,
	}

	result, err := ew.parseAllInstanceFailuresFromKv(kv)
	assert.Nil(t, err)
	assert.NotNil(t, result)
	assert.Equal(t, "leader-1", result.LeaderId)
	assert.Equal(t, int32(5), result.LocalRound)
	assert.Equal(t, "prefill", result.Failures["inst01"].Role)
	assert.Equal(t, "faulty", result.Failures["inst01"].Status)
	assert.Equal(t, "health check failed", result.Failures["inst01"].Reason)
}

// TestParseAllInstanceFailuresFromKv_EmptyFailures tests parsing KV data with empty failures
func TestParseAllInstanceFailuresFromKv_EmptyFailures(t *testing.T) {
	ew := &EtcdWrapper{}

	failures := &utils.AllInstanceFailuresSnapshot{
		LeaderId:     "leader-1",
		LocalRound:   0,
		SnapshotTime: "2025-01-01T00:00:00Z",
		Failures:     map[string]*utils.InstanceFailureRecord{},
	}

	marshal, err := utils.SonicApi.Marshal(failures)
	assert.Nil(t, err)

	kv := &mvccpb.KeyValue{
		Key:   []byte("/rs/instances/failures"),
		Value: marshal,
	}

	result, err := ew.parseAllInstanceFailuresFromKv(kv)
	assert.Nil(t, err)
	assert.NotNil(t, result)
	assert.Empty(t, result.Failures)
}

// TestParseAllInstanceFailuresFromKv_InvalidJson tests parsing invalid JSON
func TestParseAllInstanceFailuresFromKv_InvalidJson(t *testing.T) {
	ew := &EtcdWrapper{}

	kv := &mvccpb.KeyValue{
		Key:   []byte("/rs/instances/failures"),
		Value: []byte("{invalid json}"),
	}

	result, err := ew.parseAllInstanceFailuresFromKv(kv)
	assert.NotNil(t, err)
	assert.Nil(t, result)
}

// TestParseAllInstanceFailuresFromKv_NilValue tests parsing nil value
func TestParseAllInstanceFailuresFromKv_NilValue(t *testing.T) {
	ew := &EtcdWrapper{}

	kv := &mvccpb.KeyValue{
		Key:   []byte("/rs/instances/failures"),
		Value: nil,
	}

	result, err := ew.parseAllInstanceFailuresFromKv(kv)
	assert.NotNil(t, err)
	assert.Nil(t, result)
}

// TestShouldResumeInstanceFailures_Nil tests nil failures
func TestShouldResumeInstanceFailures_Nil(t *testing.T) {
	ew := &EtcdWrapper{
		localNode: &utils.Peer{Name: "local-peer"},
	}

	should, reason := ew.shouldResumeInstanceFailures(nil)
	assert.False(t, should)
	assert.Equal(t, "no failures", reason)
}

// TestShouldResumeInstanceFailures_EmptyLeaderId tests empty LeaderId
func TestShouldResumeInstanceFailures_EmptyLeaderId(t *testing.T) {
	ew := &EtcdWrapper{
		localNode: &utils.Peer{Name: "local-peer"},
	}

	failures := &utils.AllInstanceFailuresSnapshot{
		LeaderId: "",
	}

	should, reason := ew.shouldResumeInstanceFailures(failures)
	assert.False(t, should)
	assert.Equal(t, "no failures is recorded yet", reason)
}

// TestShouldResumeInstanceFailures_LeaderChanged tests leader change
func TestShouldResumeInstanceFailures_LeaderChanged(t *testing.T) {
	ew := &EtcdWrapper{
		localNode: &utils.Peer{Name: "local-peer"},
	}

	failures := &utils.AllInstanceFailuresSnapshot{
		LeaderId: "old-leader",
	}

	should, reason := ew.shouldResumeInstanceFailures(failures)
	assert.True(t, should)
	assert.Equal(t, "leader changes", reason)
}

// TestShouldResumeInstanceFailures_SameLeaderFirstRound tests same leader with firstRound being 0
func TestShouldResumeInstanceFailures_SameLeaderFirstRound(t *testing.T) {
	ew := &EtcdWrapper{
		localNode: &utils.Peer{Name: "local-peer"},
	}

	failures := &utils.AllInstanceFailuresSnapshot{
		LeaderId: "local-peer",
	}

	should, reason := ew.shouldResumeInstanceFailures(failures)
	assert.True(t, should)
	assert.Equal(t, "leader not changed, but may restarted", reason)
}

// TestShouldResumeInstanceFailures_SameLeaderNoChange tests same leader with no change
func TestShouldResumeInstanceFailures_SameLeaderNoChange(t *testing.T) {
	ew := &EtcdWrapper{
		localNode:                &utils.Peer{Name: "local-peer"},
		lastFailureSnapshotRound: 10,
	}

	failures := &utils.AllInstanceFailuresSnapshot{
		LeaderId: "local-peer",
	}

	should, reason := ew.shouldResumeInstanceFailures(failures)
	assert.False(t, should)
	assert.Equal(t, "leader not changed", reason)
}

// TestGetRestoreInstanceFailures_NilKvs tests GetRestoreInstanceFailures with empty Kvs
// Note: This test depends on the etcd client and cannot run as a unit test
// This method directly calls ew.etcdClient.Get in wrapper.go, requiring an integration test

// TestCheckConnection_StatusConnected tests connected status
func TestCheckConnection_StatusConnected(t *testing.T) {
	ew := &EtcdWrapper{}
	ew.status.Store(StatusConnected)
	assert.True(t, ew.CheckConnection())
}

// TestCheckConnection_StatusDisconnected tests disconnected status
func TestCheckConnection_StatusDisconnected(t *testing.T) {
	ew := &EtcdWrapper{}
	ew.status.Store(StatusDisconnected)
	assert.False(t, ew.CheckConnection())
}

// TestIsLeader_Connected tests leader check when connected
func TestIsLeader_Connected(t *testing.T) {
	ew := &EtcdWrapper{
		localNode: &utils.Peer{Name: "local-peer"},
	}
	ew.status.Store(StatusConnected)
	ew.leaderId.Store("local-peer")

	assert.True(t, ew.isLeader())
}

// TestIsLeader_NotLeader tests when the node is not the leader
func TestIsLeader_NotLeader(t *testing.T) {
	ew := &EtcdWrapper{
		localNode: &utils.Peer{Name: "local-peer"},
	}
	ew.status.Store(StatusConnected)
	ew.leaderId.Store("other-peer")

	assert.False(t, ew.isLeader())
}

// TestIsLeader_Disconnected tests leader check when disconnected
func TestIsLeader_Disconnected(t *testing.T) {
	ew := &EtcdWrapper{
		localNode: &utils.Peer{Name: "local-peer"},
	}
	ew.status.Store(StatusDisconnected)

	assert.False(t, ew.isLeader())
}
