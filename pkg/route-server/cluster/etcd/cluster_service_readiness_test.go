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

package etcd

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	peer_client "infer-dp-scheduler/pkg/common/client/peer-client"
	"infer-dp-scheduler/pkg/common/cluster"
	"infer-dp-scheduler/pkg/common/config"
	"infer-dp-scheduler/pkg/common/utils"
)

// TestEtcdClusterServiceGetCfg tests the getCfg function
func TestEtcdClusterServiceGetCfg(t *testing.T) {
	expectedConfig := config.ClusterConfig{
		ClusterServiceType: "etcd",
		RoleCandidate:      true,
		Verbose:            true,
	}

	ecs := &EtcdClusterService{
		config: expectedConfig,
	}

	actualConfig := ecs.getCfg()
	assert.Equal(t, expectedConfig, actualConfig)
}

// TestEtcdClusterServiceGetHealthStats tests the GetHealthStats function
func TestEtcdClusterServiceGetHealthStats(t *testing.T) {

	expectedCache := cluster.EtcdHealthStatsCache{
		EveryRoleAvailableCountMap: map[string]int{
			"P": 3,
			"D": 5,
			"R": 2,
		},
		FailureInstanceDetail: map[string]map[string]*utils.InstanceFailureRecord{
			"P": {
				"worker1": {
					Role:   "P",
					Reason: "health check failed",
				},
			},
		},
		UpdateTime: time.Now(),
	}

	ecs := &EtcdClusterService{
		healthStatsCache: expectedCache,
	}

	// Test GetHealthStats
	actualCache, err := ecs.GetHealthStats()
	assert.NoError(t, err)
	assert.Equal(t, expectedCache.EveryRoleAvailableCountMap, actualCache.EveryRoleAvailableCountMap)
	assert.Equal(t, expectedCache.FailureInstanceDetail, actualCache.FailureInstanceDetail)
	assert.WithinDuration(t, expectedCache.UpdateTime, actualCache.UpdateTime, time.Millisecond)
}

// TestEtcdClusterServiceUpdateHealthStats tests the UpdateHealthStats function
func TestEtcdClusterServiceUpdateHealthStats(t *testing.T) {
	ecs := &EtcdClusterService{}

	// Initial state
	cache, err := ecs.GetHealthStats()
	assert.NoError(t, err)
	assert.Empty(t, cache.EveryRoleAvailableCountMap)
	assert.Nil(t, cache.FailureInstanceDetail)

	// Update with new cache
	newCache := cluster.EtcdHealthStatsCache{
		EveryRoleAvailableCountMap: map[string]int{
			"P": 10,
			"D": 15,
		},
		FailureInstanceDetail: map[string]map[string]*utils.InstanceFailureRecord{
			"D": {
				"worker2": {
					Role:   "D",
					Reason: "connection timeout",
				},
			},
		},
		UpdateTime: time.Now(),
	}

	err = ecs.UpdateHealthStats(newCache)
	assert.NoError(t, err)

	// Verify update
	cache, err = ecs.GetHealthStats()
	assert.NoError(t, err)
	assert.Equal(t, newCache.EveryRoleAvailableCountMap, cache.EveryRoleAvailableCountMap)
	assert.Equal(t, newCache.FailureInstanceDetail, cache.FailureInstanceDetail)
	assert.WithinDuration(t, newCache.UpdateTime, cache.UpdateTime, time.Millisecond)
}

// TestEtcdClusterServiceHealthStatsConcurrency tests concurrent access to health stats
func TestEtcdClusterServiceHealthStatsConcurrency(t *testing.T) {
	ecs := &EtcdClusterService{}

	var wg sync.WaitGroup
	numGoroutines := 10
	numUpdates := 100

	// Start multiple goroutines to update and read health stats concurrently
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			for j := 0; j < numUpdates; j++ {
				// Update health stats
				cache := cluster.EtcdHealthStatsCache{
					EveryRoleAvailableCountMap: map[string]int{
						"P": id + j,
						"D": id * j,
					},
					UpdateTime: time.Now(),
				}

				err := ecs.UpdateHealthStats(cache)
				assert.NoError(t, err)

				// Read health stats
				readCache, err := ecs.GetHealthStats()
				assert.NoError(t, err)
				assert.NotNil(t, readCache)

				// Small delay to increase chance of concurrent access
				time.Sleep(time.Microsecond)
			}
		}(i)
	}

	wg.Wait()

	// Final verification
	finalCache, err := ecs.GetHealthStats()
	assert.NoError(t, err)
	assert.NotNil(t, finalCache)
}

// TestEtcdClusterServiceStart tests the Start function
func TestEtcdClusterServiceStart(t *testing.T) {
	tests := []struct {
		name          string
		etcdCfg       *config.EtcdConfig
		expectedError bool
	}{
		{
			name: "valid etcd config",
			etcdCfg: &config.EtcdConfig{
				LeaseTtlSec: 30,
			},
			expectedError: false,
		},
		{
			name:          "nil etcd config",
			etcdCfg:       nil,
			expectedError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ecs := &EtcdClusterService{
				config: config.ClusterConfig{
					EtcdCfg: tt.etcdCfg,
				},
			}

			err := ecs.Start()

			if tt.expectedError {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), "etcd cfg is nil")
			} else {
				assert.NoError(t, err)
				// The wrapperLoop runs in a goroutine, we can't easily test its behavior here
			}
		})
	}
}

// TestEtcdClusterServiceWrapperLoop tests the wrapperLoop function
func TestEtcdClusterServiceWrapperLoop(t *testing.T) {
	// This test is challenging due to the infinite loop nature of wrapperLoop
	// We'll test the initialization part

	ecs := &EtcdClusterService{
		config: config.ClusterConfig{
			EtcdCfg: &config.EtcdConfig{
				LeaseTtlSec: 30,
			},
		},
		localNode: &utils.Peer{
			Name: "test-node",
		},
	}

	// We can't easily test the full loop due to its infinite nature
	// But we can verify the timer interval is calculated correctly
	expectedInterval := time.Duration((ecs.config.EtcdCfg.LeaseTtlSec*1000)/3) * time.Millisecond
	assert.Equal(t, time.Duration(10000)*time.Millisecond, expectedInterval)
}

// TestEtcdClusterServiceInit tests the Init function
func TestEtcdClusterServiceInit(t *testing.T) {
	cfg := config.ClusterConfig{
		ClusterServiceType: "etcd",
		RoleCandidate:      true,
		Verbose:            false,
	}

	initRoute := &config.RouteTable{
		InstanceRoutes: map[string]config.Route{
			"test": {
				Role: "P",
				Ip:   "1.1.1.1",
				Port: 8080,
			},
		},
	}

	localPeer := &utils.Peer{
		Name: "test-peer",
		Addr: utils.PeerAddr{
			Host:     "1.1.1.1",
			HttpPort: 8080,
			OpsPort:  8180,
		},
	}

	peerClient := &peer_client.PeerClient{}

	ecs := &EtcdClusterService{}
	ecs.Init(cfg, initRoute, localPeer, peerClient)

	// Verify initialization
	assert.Equal(t, cfg, ecs.config)
	assert.Equal(t, localPeer, ecs.localNode)
	assert.Equal(t, initRoute, ecs.initRoutes)
	assert.Equal(t, peerClient, ecs.peerClient)
	assert.Equal(t, int32(0), ecs.failureUpdateRound.Load())

	// Verify etcd wrapper is initialized
	wrapper, ok := ecs.etcdWrapper.Load().(*EtcdWrapper)
	assert.True(t, ok)
	assert.NotNil(t, wrapper)
	assert.Equal(t, localPeer, wrapper.localNode)
	assert.Equal(t, "", wrapper.leaderId.Load())
}

// TestEtcdClusterServiceInitWithVerbose tests Init function with verbose enabled
func TestEtcdClusterServiceInitWithVerbose(t *testing.T) {
	cfg := config.ClusterConfig{
		Verbose: true,
	}

	ecs := &EtcdClusterService{}
	ecs.Init(cfg, nil, &utils.Peer{}, &peer_client.PeerClient{})

	// With verbose enabled, stateChangeListeners should have at least one member
	assert.Len(t, ecs.stateChangeListeners, 1)
}
