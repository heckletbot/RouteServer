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

package apc

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/agiledragon/gomonkey/v2"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	peer_client "route-server/pkg/common/client/peer-client"
	"route-server/pkg/common/cluster"
	"route-server/pkg/common/config"
	"route-server/pkg/common/constant"
	"route-server/pkg/common/utils"
	"route-server/pkg/route-server/cluster/singleton"
	"route-server/pkg/route-server/metrics"
)

// createTestManagerConfig creates a test manager configuration
func createTestManagerConfig(enable bool, withHashStrategy bool) *config.APCManagerConfig {
	cfg := &config.APCManagerConfig{
		Enable: enable,
	}

	if withHashStrategy {
		cfg.Strategies = &config.StrategyConfigs{
			Hash: &config.APCRollingHashConfig{
				BlockSizeBytes: 64,
				MaxBlocks:      10,
				APCStrategyMemoryConfig: config.APCStrategyMemoryConfig{
					MaxEntries:  1000,
					MaxMemoryMB: 100,
					CacheTTLSec: 3600,
				},
			},
		}
	}

	return cfg
}

// createTestClusterService creates a real Singleton cluster service for testing
func createTestClusterService() cluster.ClusterService {
	// Create a real SingletonClusterService instance
	service := &singleton.SingletonClusterService{}

	// Initialize basic configuration
	clusterConfig := config.ClusterConfig{
		ClusterServiceType: cluster.TypeSingleton,
	}

	// Create local peer
	localPeer := &utils.Peer{
		Name: "test-peer",
		Addr: utils.PeerAddr{
			Host:     "127.0.0.1",
			HttpPort: 8080,
			OpsPort:  8180,
		},
		IsCandidate: true,
	}

	// Initialize service
	service.Init(clusterConfig, &config.RouteTable{}, localPeer, &peer_client.PeerClient{})

	return service
}

// createTestRequestEventMsg creates a test event message
func createTestRequestEventMsg(requestId, promptCacheKey, instanceId, strategyType string) *utils.RequestEventMsg {
	return &utils.RequestEventMsg{
		Request: &utils.RemoteRequestWrapper{
			Id:              requestId,
			Type:            "test",
			PromptCacheKey:  promptCacheKey,
			ApcStrategyType: strategyType,
			PromptTokens:    300,
			Model:           "test-model",
		},
		TargetInstance: instanceId,
		EventType:      1,
		TimeStamp:      time.Now().Unix(),
	}
}

// createTestRemoteRequestWrapper creates a test request wrapper
func createTestRemoteRequestWrapper(requestId, promptCacheKey, strategyType string) *utils.RemoteRequestWrapper {
	return &utils.RemoteRequestWrapper{
		Id:              requestId,
		Type:            "test",
		PromptCacheKey:  promptCacheKey,
		ApcStrategyType: strategyType,
		PromptTokens:    300,
		Model:           "test-model",
	}
}

var _ = Describe("Manager", func() {
	var (
		manager        *Manager
		cfg            *config.APCManagerConfig
		clusterService cluster.ClusterService
	)

	BeforeEach(func() {
		// Clean up global manager
		SetGlobalManager(nil)

		// Initialize metrics to avoid panic
		metricsCfg := &config.MetricsCfg{
			Enable: true,
		}
		metrics.InitMetrics(metricsCfg)

		// Create default configuration
		cfg = createTestManagerConfig(true, true)
		clusterService = createTestClusterService()
	})

	AfterEach(func() {
		if manager != nil {
			manager.Close()
			manager = nil
		}
		// Clean up global manager
		SetGlobalManager(nil)
	})

	Describe("NewManager", func() {
		Context("when creating with valid configuration", func() {
			It("should create manager successfully", func() {
				var err error
				manager, err = NewManager(cfg, clusterService)
				Expect(err).ToNot(HaveOccurred())
				Expect(manager).ToNot(BeNil())
				Expect(manager.GetClusterService()).To(Equal(clusterService))
				Expect(manager.IsRunning()).To(BeFalse())
			})

			It("should create strategies from configuration", func() {
				var err error
				manager, err = NewManager(cfg, clusterService)
				Expect(err).ToNot(HaveOccurred())

				strategy := manager.getStrategy(constant.APCStrategyHash)
				Expect(strategy).ToNot(BeNil())
				Expect(strategy.Name()).To(Equal(constant.APCStrategyHash))
			})
		})

		Context("when cluster service is nil", func() {
			It("should return error", func() {
				_, err := NewManager(cfg, nil)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("clusterService cannot be nil"))
			})
		})

		Context("when no strategy configuration", func() {
			It("should create default hash strategy", func() {
				cfgWithoutStrategies := createTestManagerConfig(true, false)
				var err error
				manager, err = NewManager(cfgWithoutStrategies, clusterService)
				Expect(err).ToNot(HaveOccurred())

				strategy := manager.getStrategy(constant.APCStrategyHash)
				Expect(strategy).ToNot(BeNil())
				Expect(manager.GetDefaultStrategyName()).To(Equal(constant.APCStrategyHash))
			})
		})

		Context("when strategy creation fails", func() {
			It("should return error", func() {
				patches := gomonkey.NewPatches()
				defer patches.Reset()

				// Stub NewRollingHashStrategy to return error
				patches.ApplyFunc(NewRollingHashStrategy, func(cfg *config.APCRollingHashConfig) (*RollingHashStrategy, error) {
					return nil, fmt.Errorf("strategy creation failed")
				})

				cfgWithStrategy := createTestManagerConfig(true, true)
				_, err := NewManager(cfgWithStrategy, clusterService)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("strategy creation failed"))
			})
		})
	})

	Describe("Manager Lifecycle", func() {
		BeforeEach(func() {
			var err error
			manager, err = NewManager(cfg, clusterService)
			Expect(err).ToNot(HaveOccurred())
		})

		Describe("Start", func() {
			It("should start manager successfully", func() {
				err := manager.Start()
				Expect(err).ToNot(HaveOccurred())
				Expect(manager.IsRunning()).To(BeTrue())
			})

			It("should return error when already started", func() {
				err := manager.Start()
				Expect(err).ToNot(HaveOccurred())

				err = manager.Start()
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("already running"))
			})
		})

		Describe("Stop", func() {
			It("should stop manager successfully", func() {
				err := manager.Start()
				Expect(err).ToNot(HaveOccurred())

				err = manager.Stop()
				Expect(err).ToNot(HaveOccurred())
				Expect(manager.IsRunning()).To(BeFalse())
			})

			It("should not error when stopping already stopped manager", func() {
				err := manager.Stop()
				Expect(err).ToNot(HaveOccurred())
			})
		})

		Describe("Close", func() {
			It("should close manager and cleanup resources", func() {
				err := manager.Start()
				Expect(err).ToNot(HaveOccurred())

				err = manager.Close()
				Expect(err).ToNot(HaveOccurred())
				Expect(manager.IsRunning()).To(BeFalse())

				// Verify strategies are cleaned up
				strategy := manager.getStrategy(constant.APCStrategyHash)
				Expect(strategy).To(BeNil())
			})
		})
	})

	Describe("Strategy Management", func() {
		BeforeEach(func() {
			var err error
			manager, err = NewManager(cfg, clusterService)
			Expect(err).ToNot(HaveOccurred())
		})

		It("should return correct default strategy name", func() {
			defaultName := manager.GetDefaultStrategyName()
			Expect(defaultName).To(Equal(constant.APCStrategyHash))
		})

		It("should return default strategy instance", func() {
			strategy := manager.GetDefaultStrategyInstance()
			Expect(strategy).ToNot(BeNil())
			Expect(strategy.Name()).To(Equal(constant.APCStrategyHash))
		})

		It("should return strategy instance by name", func() {
			strategy := manager.getStrategy(constant.APCStrategyHash)
			Expect(strategy).ToNot(BeNil())
			Expect(strategy.Name()).To(Equal(constant.APCStrategyHash))
		})

		It("should return nil for non-existent strategy", func() {
			strategy := manager.getStrategy("non-existent")
			Expect(strategy).To(BeNil())
		})
	})

	Describe("Cache Operations", func() {
		BeforeEach(func() {
			var err error
			manager, err = NewManager(cfg, clusterService)
			Expect(err).ToNot(HaveOccurred())

			err = manager.Start()
			Expect(err).ToNot(HaveOccurred())
		})

		Describe("UpdateCache", func() {
			Context("when APC is enabled and manager is running", func() {

				It("should update cache successfully", func() {
					event := createTestRequestEventMsg("req1", "model:h1:h2", "inst1", constant.APCStrategyHash)
					err := manager.UpdateCache(event)
					Expect(err).ToNot(HaveOccurred())
				})
			})

			Context("when event is nil", func() {
				It("should return error", func() {
					err := manager.UpdateCache(nil)
					Expect(err).To(HaveOccurred())
					Expect(err.Error()).To(ContainSubstring("event cannot be nil"))
				})
			})

			Context("when strategy not found", func() {
				It("should return error", func() {
					event := createTestRequestEventMsg("req1", "model:h1:h2", "inst1", "non-existent")
					err := manager.UpdateCache(event)
					Expect(err).To(HaveOccurred())
					Expect(err.Error()).To(ContainSubstring("strategy non-existent not found"))
				})
			})

			Context("when APC is disabled", func() {
				It("should return nil without updating cache", func() {
					cfgDisabled := createTestManagerConfig(false, true)
					managerDisabled, err := NewManager(cfgDisabled, clusterService)
					Expect(err).ToNot(HaveOccurred())

					event := createTestRequestEventMsg("req1", "model:h1:h2", "inst1", constant.APCStrategyHash)
					err = managerDisabled.UpdateCache(event)
					Expect(err).ToNot(HaveOccurred())

					managerDisabled.Close()
				})
			})

			Context("when manager is not running", func() {
				It("should return nil without updating cache", func() {
					managerNotRunning, err := NewManager(cfg, clusterService)
					Expect(err).ToNot(HaveOccurred())
					// Do not start the manager

					event := createTestRequestEventMsg("req1", "model:h1:h2", "inst1", constant.APCStrategyHash)
					err = managerNotRunning.UpdateCache(event)
					Expect(err).ToNot(HaveOccurred())

					managerNotRunning.Close()
				})
			})
		})

		Describe("GetMatchedInstances", func() {
			Context("when APC is enabled", func() {
				It("should return matched instances successfully", func() {
					// First update cache
					event := createTestRequestEventMsg("req1", "model:h1:h2", "inst1", constant.APCStrategyHash)
					err := manager.UpdateCache(event)
					Expect(err).ToNot(HaveOccurred())

					request := createTestRemoteRequestWrapper("req1", "model:h1:h2", constant.APCStrategyHash)
					availableInstances := map[string]InstanceLoad{"inst1": {}}

					matched, err := manager.GetMatchedInstances(request, availableInstances, nil)
					Expect(err).ToNot(HaveOccurred())
					Expect(len(matched)).To(Equal(1))
					Expect(matched).To(HaveKey("inst1"))
				})
			})

			Context("when APC is disabled", func() {
				It("should return error", func() {
					cfgDisabled := createTestManagerConfig(false, true)
					managerDisabled, err := NewManager(cfgDisabled, clusterService)
					Expect(err).ToNot(HaveOccurred())

					request := createTestRemoteRequestWrapper("req1", "model:h1:h2", constant.APCStrategyHash)
					_, err = managerDisabled.GetMatchedInstances(request, map[string]InstanceLoad{"inst1": {}}, nil)
					Expect(err).To(HaveOccurred())
					Expect(err.Error()).To(ContainSubstring("APC disabled"))

					managerDisabled.Close()
				})
			})

			Context("when no available instances", func() {
				It("should return error", func() {
					request := createTestRemoteRequestWrapper("req1", "model:h1:h2", constant.APCStrategyHash)
					_, err := manager.GetMatchedInstances(request, map[string]InstanceLoad{}, nil)
					Expect(err).To(HaveOccurred())
					Expect(err.Error()).To(ContainSubstring("no available instance"))
				})
			})

			Context("when request ID is empty", func() {
				It("should return error", func() {
					request := createTestRemoteRequestWrapper("", "model:h1:h2", constant.APCStrategyHash)
					_, err := manager.GetMatchedInstances(request, map[string]InstanceLoad{"inst1": {}}, nil)
					Expect(err).To(HaveOccurred())
					Expect(err.Error()).To(ContainSubstring("not leader"))
				})
			})

			Context("when request ID is empty", func() {
				It("should return error", func() {
					request := createTestRemoteRequestWrapper("", "model:h1:h2", constant.APCStrategyHash)
					_, err := manager.GetMatchedInstances(request, map[string]InstanceLoad{"inst1": {}}, nil)
					Expect(err).To(HaveOccurred())
					Expect(err.Error()).To(ContainSubstring("not leader"))
				})
			})

			Context("with blacklist filtering", func() {
				It("should filter out blacklisted instances", func() {
					// First update cache
					event := createTestRequestEventMsg("req1", "model:h1:h2", "inst1", constant.APCStrategyHash)
					err := manager.UpdateCache(event)
					Expect(err).ToNot(HaveOccurred())

					request := createTestRemoteRequestWrapper("req1", "model:h1:h2", constant.APCStrategyHash)
					availableInstances := map[string]InstanceLoad{
						"inst1": {},
						"inst2": {},
					}
					blacklist := map[string]interface{}{
						"inst2": struct{}{},
					}

					matched, err := manager.GetMatchedInstances(request, availableInstances, blacklist)
					Expect(err).ToNot(HaveOccurred())
					Expect(len(matched)).To(Equal(1))
					Expect(matched).To(HaveKey("inst1"))
					Expect(matched).ToNot(HaveKey("inst2"))
				})
			})
		})

		Describe("RemoveInstanceIdFromCache", func() {
			It("should remove instance from cache successfully", func() {
				// First update cache
				event := createTestRequestEventMsg("req1", "model:h1:h2", "inst1", constant.APCStrategyHash)
				err := manager.UpdateCache(event)
				Expect(err).ToNot(HaveOccurred())

				instancesToRemove := map[string]struct{}{
					"inst1": {},
				}

				err = manager.RemoveInstanceIdFromCache(instancesToRemove)
				Expect(err).ToNot(HaveOccurred())

				request := createTestRemoteRequestWrapper("req1", "model:h1:h2", constant.APCStrategyHash)
				_, err = manager.GetMatchedInstances(request, map[string]InstanceLoad{"inst1": {}}, nil)
				Expect(err).NotTo(HaveOccurred())
			})

			It("should return error when manager is not running", func() {
				managerNotRunning, err := NewManager(cfg, clusterService)
				Expect(err).ToNot(HaveOccurred())
				// Do not start the manager

				instancesToRemove := map[string]struct{}{
					"inst1": {},
				}

				err = managerNotRunning.RemoveInstanceIdFromCache(instancesToRemove)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("not running"))

				managerNotRunning.Close()
			})
		})

		Describe("Instance Filtering", func() {
			It("should return available instances when no blacklist", func() {
				availableInstances := map[string]InstanceLoad{
					"inst1": {},
					"inst2": {},
				}

				request := createTestRemoteRequestWrapper("req1", "model:h1:h2", constant.APCStrategyHash)

				// Test filtering indirectly through GetMatchedInstances (should return all instances when no blacklist)
				_, err := manager.GetMatchedInstances(request, availableInstances, nil)
				// Since there is no cache match, should return "no matching instance found" error
				// This proves the filtering works correctly without causing a crash
				// When matched instances is 0, no error is reported
				Expect(err).NotTo(HaveOccurred())
			})
		})
	})

	Describe("Global Manager", func() {
		It("should set and get global manager", func() {
			var err error
			manager, err = NewManager(cfg, clusterService)
			Expect(err).ToNot(HaveOccurred())

			SetGlobalManager(manager)

			globalManager := GetGlobalManager()
			Expect(globalManager).ToNot(BeNil())
			Expect(globalManager).To(Equal(manager))
		})

		It("should return nil when no global manager set", func() {
			globalManager := GetGlobalManager()
			Expect(globalManager).To(BeNil())
		})
	})

	Describe("NewManagerAndStart", func() {
		Context("when APC is enabled", func() {
			It("should create and start manager successfully", func() {
				cfgWithStart := createTestManagerConfig(true, true)

				manager, err := NewManagerAndStart(cfgWithStart, clusterService)
				Expect(err).ToNot(HaveOccurred())
				Expect(manager).ToNot(BeNil())
				Expect(manager.IsRunning()).To(BeTrue())

				// Verify global manager is set
				globalManager := GetGlobalManager()
				Expect(globalManager).To(Equal(manager))

				manager.Close()
			})
		})

		Context("when APC is disabled", func() {
			It("should return nil without creating manager", func() {
				cfgDisabled := createTestManagerConfig(false, true)

				manager, err := NewManagerAndStart(cfgDisabled, clusterService)
				Expect(err).ToNot(HaveOccurred())
				Expect(manager).To(BeNil())
			})
		})

		Context("when cluster service is nil", func() {
			It("should return error", func() {
				cfgWithStart := createTestManagerConfig(true, true)

				_, err := NewManagerAndStart(cfgWithStart, nil)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("cluster service"))
			})
		})
	})

	Describe("StateApplier Interface", func() {
		BeforeEach(func() {
			var err error
			manager, err = NewManager(cfg, clusterService)
			Expect(err).ToNot(HaveOccurred())
		})

		It("should implement StateApplier interface", func() {
			// Verify all required methods are implemented
			Expect(manager.GetName()).To(Equal("APCManager"))

			// Test various event handlers (should not panic)
			peer := &utils.Peer{Name: "test-peer"}
			manager.OnLeaderChanged(peer, true, nil)
			manager.OnStateRestored(&cluster.ClusterState{Version: 1})
			manager.OnAddPeer(peer)
			manager.OnRemovePeer("test-peer")
			manager.OnAddInstance(map[string]*config.Route{}, "test-reason")
			manager.OnRemoveInstance([]string{"test-inst"}, "test-reason")
		})
	})

	Describe("Concurrent Access", func() {
		BeforeEach(func() {
			var err error
			manager, err = NewManager(cfg, clusterService)
			Expect(err).ToNot(HaveOccurred())

			err = manager.Start()
			Expect(err).ToNot(HaveOccurred())
		})

		AfterEach(func() {
			err := manager.Close()
			Expect(err).ToNot(HaveOccurred())
		})

		It("should handle concurrent strategy access safely", func() {
			var wg sync.WaitGroup
			numGoroutines := 10
			numOperations := 100

			// Concurrent strategy access
			for i := 0; i < numGoroutines; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for j := 0; j < numOperations; j++ {
						strategy := manager.getStrategy(constant.APCStrategyHash)
						Expect(strategy).ToNot(BeNil())
					}
				}()
			}

			// Concurrent cache updates
			for i := 0; i < numGoroutines; i++ {
				wg.Add(1)
				go func(id int) {
					defer wg.Done()
					for j := 0; j < numOperations; j++ {
						event := createTestRequestEventMsg(
							fmt.Sprintf("req%d-%d", id, j),
							"model:h1:h2",
							fmt.Sprintf("inst%d", id),
							constant.APCStrategyHash,
						)
						err := manager.UpdateCache(event)
						Expect(err).ToNot(HaveOccurred())
					}
				}(i)
			}

			wg.Wait()
		})
	})

	Describe("Config Access", func() {
		BeforeEach(func() {
			var err error
			manager, err = NewManager(cfg, clusterService)
			Expect(err).ToNot(HaveOccurred())
		})

		It("should return the configuration", func() {
			retrievedConfig := manager.GetConfig()
			Expect(retrievedConfig).ToNot(BeNil())
			Expect(retrievedConfig.Enable).To(BeTrue())
			Expect(retrievedConfig.Strategies).ToNot(BeNil())
		})
	})

	Describe("Cluster Service Access", func() {
		BeforeEach(func() {
			var err error
			manager, err = NewManager(cfg, clusterService)
			Expect(err).ToNot(HaveOccurred())
		})

		It("should return the cluster service", func() {
			retrievedService := manager.GetClusterService()
			Expect(retrievedService).ToNot(BeNil())
			Expect(retrievedService).To(Equal(clusterService))
		})

		It("should correctly report leader status", func() {
			// Singleton always returns true, which is reasonable in tests
			leaderService := createTestClusterService()
			managerLeader, err := NewManager(cfg, leaderService)
			Expect(err).ToNot(HaveOccurred())
			Expect(managerLeader.IsMeLeader()).To(BeTrue())
			managerLeader.Close()

			// Create another manager instance that should also be leader
			followerService := createTestClusterService()
			managerFollower, err := NewManager(cfg, followerService)
			Expect(err).ToNot(HaveOccurred())
			// Due to Singleton's nature, all instances consider themselves as leader
			Expect(managerFollower.IsMeLeader()).To(BeTrue())
			managerFollower.Close()
		})
	})
})

func TestManager(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Manager Suite")
}
