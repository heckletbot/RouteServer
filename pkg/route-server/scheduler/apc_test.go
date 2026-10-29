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

package scheduler

import (
	"fmt"
	"testing"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	peerClient "infer-dp-scheduler/pkg/common/client/peer-client"
	"infer-dp-scheduler/pkg/common/cluster"
	commoncfg "infer-dp-scheduler/pkg/common/config"
	"infer-dp-scheduler/pkg/common/constant"
	instanceMetrics "infer-dp-scheduler/pkg/common/instance-metrics"
	"infer-dp-scheduler/pkg/common/logger"
	"infer-dp-scheduler/pkg/common/scheduler"
	"infer-dp-scheduler/pkg/common/utils"
	"infer-dp-scheduler/pkg/route-server/apc"
	"infer-dp-scheduler/pkg/route-server/cluster/singleton"
	"infer-dp-scheduler/pkg/route-server/metrics"
)

// createTestManagerConfig creates manager config for testing
func createTestManagerConfig(enable bool) *commoncfg.APCManagerConfig {
	return &commoncfg.APCManagerConfig{
		Enable: enable,
	}
}

// createTestClusterService creates cluster service for testing
func createTestClusterService() cluster.ClusterService {
	service := &singleton.SingletonClusterService{}

	clusterConfig := commoncfg.ClusterConfig{
		ClusterServiceType: cluster.TypeSingleton,
	}

	localPeer := &utils.Peer{
		Name: "test-peer",
		Addr: utils.PeerAddr{
			Host:     "127.0.0.1",
			HttpPort: 8080,
			OpsPort:  8180,
		},
		IsCandidate: true,
	}

	service.Init(clusterConfig, &commoncfg.RouteTable{}, localPeer, &peerClient.PeerClient{})

	return service
}

// createTestRequest creates test request
func createTestRequest(requestId, model, reqType string, promptTokens int32, apcStrategyType string) *utils.RemoteRequestWrapper {
	return &utils.RemoteRequestWrapper{
		Id:              requestId,
		Type:            reqType,
		SloTag:          "test-slo",
		SLOInterval:     nil,
		Content:         nil,
		PromptCacheKey:  "test-key",
		ApcStrategyType: apcStrategyType,
		MaxTokens:       1000,
		PromptTokens:    promptTokens,
		Stream:          false,
		Model:           model,
	}
}

// createTestWorkerStats creates worker stats for testing
func createTestWorkerStats(instanceId, role string, status instanceMetrics.InstanceStatus, maxNumSeqs int) instanceMetrics.WorkerStats {
	stats := &instanceMetrics.ZeroCopyStats{
		Id:         instanceId,
		Role:       role,
		Status:     status,
		StaticInfo: make(map[string]string),
	}
	if maxNumSeqs > 0 {
		stats.StaticInfo[constant.MaxNumSeqs] = fmt.Sprintf("%d", maxNumSeqs)
	}
	return stats
}

// TestAPCScheduler tests APC scheduler using Ginkgo + Gomega framework
func TestAPCScheduler(t *testing.T) {
	gomega.RegisterFailHandler(ginkgo.Fail)
	ginkgo.RunSpecs(t, "APCScheduler Suite")
}

var _ = ginkgo.Describe("APCScheduler", func() {
	var (
		apcScheduler *APCScheduler
		apcManager   *apc.Manager
		testConfig   scheduler.ScheduleAlgorithm
		err          error
	)

	ginkgo.BeforeEach(func() {
		// Initialize logger to avoid panic
		logger.Init(logger.Config{Level: "info"}, "")

		// Initialize metrics
		metricsCfg := &commoncfg.MetricsCfg{
			Enable: true,
		}
		metrics.InitMetrics(metricsCfg)

		// Create APC scheduler
		testConfig, err = NewAPCScheduler()
		gomega.Expect(err).ToNot(gomega.HaveOccurred())
		apcScheduler = testConfig.(*APCScheduler)
		gomega.Expect(apcScheduler).ToNot(gomega.BeNil())

		// Cleanup global manager
		apc.SetGlobalManager(nil)
	})

	ginkgo.AfterEach(func() {
		if apcManager != nil {
			apcManager.Close()
			apcManager = nil
		}
		apc.SetGlobalManager(nil)
	})

	ginkgo.Describe("NewAPCScheduler", func() {
		ginkgo.It("should create scheduler with correct initialization", func() {
			scheduler, err := NewAPCScheduler()
			gomega.Expect(err).ToNot(gomega.HaveOccurred())
			gomega.Expect(scheduler).ToNot(gomega.BeNil())
			gomega.Expect(scheduler.GetName()).To(gomega.Equal(APCSchedulerName))

			apcSched := scheduler.(*APCScheduler)
			gomega.Expect(apcSched.defaultBatchSize).To(gomega.Equal(int64(16)))
			gomega.Expect(apcSched.prefixLoadBase).To(gomega.Equal(int64(1)))
			gomega.Expect(apcSched.running.Load()).To(gomega.BeFalse())
		})
	})

	ginkgo.Describe("Init", func() {
		ginkgo.Context("when APC manager is not initialized", func() {
			ginkgo.It("should return error", func() {
				err := apcScheduler.Init(nil)
				gomega.Expect(err).To(gomega.HaveOccurred())
				gomega.Expect(err.Error()).To(gomega.ContainSubstring("APC manager not initialized"))
			})
		})

		ginkgo.Context("when APC manager is initialized but disabled", func() {
			ginkgo.BeforeEach(func() {
				cfg := createTestManagerConfig(false)
				apcManager, err = apc.NewManager(cfg, createTestClusterService())
				gomega.Expect(err).ToNot(gomega.HaveOccurred())
				apc.SetGlobalManager(apcManager)
			})

			ginkgo.It("should return error when APC is disabled", func() {
				err := apcScheduler.Init(nil)
				gomega.Expect(err).To(gomega.HaveOccurred())
				gomega.Expect(err.Error()).To(gomega.ContainSubstring("APC manager disable"))
			})
		})

		ginkgo.Context("when APC manager is enabled", func() {
			ginkgo.BeforeEach(func() {
				cfg := createTestManagerConfig(true)
				apcManager, err = apc.NewManager(cfg, createTestClusterService())
				gomega.Expect(err).ToNot(gomega.HaveOccurred())
				err = apcManager.Start()
				gomega.Expect(err).ToNot(gomega.HaveOccurred())
				apc.SetGlobalManager(apcManager)
			})

			ginkgo.It("should initialize successfully", func() {
				config := map[string]string{
					"default-batch-size": "32",
					"prefix-load-base":   "5",
				}
				err := apcScheduler.Init(config)
				gomega.Expect(err).ToNot(gomega.HaveOccurred())
				gomega.Expect(apcScheduler.running.Load()).To(gomega.BeTrue())
				gomega.Expect(apcScheduler.defaultBatchSize).To(gomega.Equal(int64(32)))
				gomega.Expect(apcScheduler.prefixLoadBase).To(gomega.Equal(int64(5)))
			})

			ginkgo.It("should use default values for invalid config", func() {
				config := map[string]string{
					"default-batch-size": "invalid",
					"prefix-load-base":   "-1",
				}
				err := apcScheduler.Init(config)
				gomega.Expect(err).ToNot(gomega.HaveOccurred())
				gomega.Expect(apcScheduler.defaultBatchSize).To(gomega.Equal(int64(16)))
				gomega.Expect(apcScheduler.prefixLoadBase).To(gomega.Equal(int64(1)))
			})
		})
	})

	ginkgo.Describe("GetName", func() {
		ginkgo.It("should return correct scheduler name", func() {
			gomega.Expect(apcScheduler.GetName()).To(gomega.Equal(APCSchedulerName))
		})
	})

	ginkgo.Describe("NeedStatsList", func() {
		ginkgo.It("should return required statistics", func() {
			stats := apcScheduler.NeedStatsList()
			gomega.Expect(stats).To(gomega.ContainElement("ActiveRequests"))
			gomega.Expect(stats).To(gomega.ContainElement("TotalTokens"))
			gomega.Expect(stats).To(gomega.ContainElement("QueueLength"))
			gomega.Expect(stats).To(gomega.ContainElement("CacheSize"))
		})
	})

	ginkgo.Describe("GetUpdateInterval", func() {
		ginkgo.It("should return -1", func() {
			gomega.Expect(apcScheduler.GetUpdateInterval()).To(gomega.Equal(int64(-1)))
		})
	})

	ginkgo.Describe("UpdateCluster", func() {
		ginkgo.BeforeEach(func() {
			// Create and start real APC manager
			cfg := createTestManagerConfig(true)
			apcManager, err = apc.NewManager(cfg, createTestClusterService())
			gomega.Expect(err).ToNot(gomega.HaveOccurred())
			err = apcManager.Start()
			gomega.Expect(err).ToNot(gomega.HaveOccurred())
			apc.SetGlobalManager(apcManager)

			err := apcScheduler.Init(nil)
			gomega.Expect(err).ToNot(gomega.HaveOccurred())
		})

		ginkgo.Context("when scheduler is not running", func() {
			ginkgo.BeforeEach(func() {
				apcScheduler.running.Store(false)
			})

			ginkgo.It("should return error", func() {
				err := apcScheduler.UpdateCluster(nil, nil)
				gomega.Expect(err).To(gomega.HaveOccurred())
				gomega.Expect(err.Error()).To(gomega.ContainSubstring("not running"))
			})
		})

		ginkgo.Context("when updating with valid instances", func() {
			ginkgo.It("should update available and unavailable instances", func() {
				newAvailable := map[string]instanceMetrics.WorkerStats{
					"prefill-1": createTestWorkerStats("prefill-1", constant.Prefill, instanceMetrics.Available, 32),
					"decode-1":  createTestWorkerStats("decode-1", "decode", instanceMetrics.Available, 16),
				}
				newUnavailable := map[string]instanceMetrics.WorkerStats{
					"prefill-2": createTestWorkerStats("prefill-2", constant.Prefill, instanceMetrics.Faulty, 0),
				}

				err := apcScheduler.UpdateCluster(newAvailable, newUnavailable)
				gomega.Expect(err).ToNot(gomega.HaveOccurred())

				// Verify only prefill instances are processed
				apcScheduler.instancesMutex.RLock()
				gomega.Expect(len(apcScheduler.availableInstanceLoads)).To(gomega.Equal(1))
				gomega.Expect(len(apcScheduler.unavailableInstanceLoads)).To(gomega.Equal(1))
				gomega.Expect(len(apcScheduler.instBatchSize)).To(gomega.Equal(1))
				gomega.Expect(apcScheduler.instBatchSize["prefill-1"]).To(gomega.Equal(int64(32)))
				apcScheduler.instancesMutex.RUnlock()
			})
		})
	})

	ginkgo.Describe("DispatchRequest", func() {
		ginkgo.BeforeEach(func() {
			// Create and start real APC manager
			cfg := createTestManagerConfig(true)
			apcManager, err = apc.NewManager(cfg, createTestClusterService())
			gomega.Expect(err).ToNot(gomega.HaveOccurred())
			err = apcManager.Start()
			gomega.Expect(err).ToNot(gomega.HaveOccurred())
			apc.SetGlobalManager(apcManager)

			err := apcScheduler.Init(nil)
			gomega.Expect(err).ToNot(gomega.HaveOccurred())

			// Add some instances
			newAvailable := map[string]instanceMetrics.WorkerStats{
				"instance-1": createTestWorkerStats("instance-1", constant.Prefill, instanceMetrics.Available, 32),
				"instance-2": createTestWorkerStats("instance-2", constant.Prefill, instanceMetrics.Available, 32),
			}
			err = apcScheduler.UpdateCluster(newAvailable, nil)
			gomega.Expect(err).ToNot(gomega.HaveOccurred())
		})

		ginkgo.Context("when scheduler is not running", func() {
			ginkgo.BeforeEach(func() {
				apcScheduler.running.Store(false)
			})

			ginkgo.It("should return error", func() {
				request := createTestRequest("req-1", "test-model", constant.Prefill, 100, constant.APCStrategyHash)
				instance, retry, err := apcScheduler.DispatchRequest(request, nil, nil)
				gomega.Expect(err).To(gomega.HaveOccurred())
				gomega.Expect(instance).To(gomega.BeEmpty())
				gomega.Expect(retry).To(gomega.BeFalse())
			})
		})

		ginkgo.Context("when APC manager is not set", func() {
			ginkgo.BeforeEach(func() {
				apc.SetGlobalManager(nil)
			})

			ginkgo.It("should return error", func() {
				request := createTestRequest("req-1", "test-model", constant.Prefill, 100, constant.APCStrategyHash)
				instance, retry, err := apcScheduler.DispatchRequest(request, nil, nil)
				gomega.Expect(err).To(gomega.HaveOccurred())
				gomega.Expect(instance).To(gomega.BeEmpty())
				gomega.Expect(retry).To(gomega.BeFalse())
			})
		})

		ginkgo.Context("with valid prefill request", func() {
			ginkgo.It("should dispatch successfully and update load", func() {
				request := createTestRequest("req-1", "test-model", constant.Prefill, 100, constant.APCStrategyHash)
				instance, retry, err := apcScheduler.DispatchRequest(request, nil, nil)
				gomega.Expect(err).ToNot(gomega.HaveOccurred())
				gomega.Expect(instance).ToNot(gomega.BeEmpty())
				gomega.Expect(retry).To(gomega.BeFalse())

				// Verify load has been updated
				apcScheduler.instancesMutex.RLock()
				reqInfo := apcScheduler.reqDispatchInfo["req-1"]
				gomega.Expect(reqInfo).ToNot(gomega.BeNil())
				gomega.Expect(reqInfo.instanceId).To(gomega.Equal(instance))
				gomega.Expect(reqInfo.promptTokenCnt).To(gomega.Equal(int64(100)))
				apcScheduler.instancesMutex.RUnlock()
			})
		})
	})

	ginkgo.Describe("IsRequestEventNeedProcess", func() {
		ginkgo.Context("with invalid target instance", func() {
			ginkgo.It("should return false", func() {
				event := &utils.RequestEventMsg{
					TargetInstance: "",
					EventType:      constant.RequestPrefillSuccess,
				}
				gomega.Expect(apcScheduler.IsRequestEventNeedProcess(event)).To(gomega.BeFalse())
			})
		})

		ginkgo.Context("with prefill success event", func() {
			ginkgo.It("should return true", func() {
				event := &utils.RequestEventMsg{
					TargetInstance:     "instance-1",
					EventType:          constant.RequestPrefillSuccess,
					InternalTestingReq: false,
				}
				gomega.Expect(apcScheduler.IsRequestEventNeedProcess(event)).To(gomega.BeTrue())
			})
		})

		ginkgo.Context("with other event types", func() {
			ginkgo.It("should return false", func() {
				event := &utils.RequestEventMsg{
					TargetInstance:     "instance-1",
					EventType:          constant.RequestNew,
					InternalTestingReq: false,
				}
				gomega.Expect(apcScheduler.IsRequestEventNeedProcess(event)).To(gomega.BeFalse())
			})
		})
	})

	ginkgo.Describe("UpdateConfig", func() {
		ginkgo.BeforeEach(func() {
			// Create and start real APC manager
			cfg := createTestManagerConfig(true)
			apcManager, err = apc.NewManager(cfg, createTestClusterService())
			gomega.Expect(err).ToNot(gomega.HaveOccurred())
			err = apcManager.Start()
			gomega.Expect(err).ToNot(gomega.HaveOccurred())
			apc.SetGlobalManager(apcManager)

			err := apcScheduler.Init(nil)
			gomega.Expect(err).ToNot(gomega.HaveOccurred())
		})

		ginkgo.It("should update config successfully", func() {
			config := map[string]string{
				"default-batch-size": "64",
				"prefix-load-base":   "10",
			}

			err := apcScheduler.UpdateConfig(config)
			gomega.Expect(err).ToNot(gomega.HaveOccurred())

			apcScheduler.instancesMutex.Lock()
			gomega.Expect(apcScheduler.defaultBatchSize).To(gomega.Equal(int64(64)))
			gomega.Expect(apcScheduler.prefixLoadBase).To(gomega.Equal(int64(10)))
			apcScheduler.instancesMutex.Unlock()
		})

		ginkgo.It("should handle nil config", func() {
			err := apcScheduler.UpdateConfig(nil)
			gomega.Expect(err).ToNot(gomega.HaveOccurred())
		})
	})

	ginkgo.Describe("OnRequestEvent", func() {
		var event *utils.RequestEventMsg

		ginkgo.BeforeEach(func() {
			// Create and start real APC manager
			cfg := createTestManagerConfig(true)
			apcManager, err = apc.NewManager(cfg, createTestClusterService())
			gomega.Expect(err).ToNot(gomega.HaveOccurred())
			err = apcManager.Start()
			gomega.Expect(err).ToNot(gomega.HaveOccurred())
			apc.SetGlobalManager(apcManager)

			err := apcScheduler.Init(nil)
			gomega.Expect(err).ToNot(gomega.HaveOccurred())

			// Add instances and requests
			newAvailable := map[string]instanceMetrics.WorkerStats{
				"instance-1": createTestWorkerStats("instance-1", constant.Prefill, instanceMetrics.Available, 32),
			}
			err = apcScheduler.UpdateCluster(newAvailable, nil)
			gomega.Expect(err).ToNot(gomega.HaveOccurred())

			// Add load
			request := createTestRequest("req-1", "test-model", constant.Prefill, 100, constant.APCStrategyHash)
			apcScheduler.addLoadUnsafe("instance-1", request, 0.5)
		})

		ginkgo.Context("with nil event", func() {
			ginkgo.It("should handle gracefully", func() {
				apcScheduler.OnRequestEvent(nil)
			})
		})

		ginkgo.Context("with RequestNew event", func() {
			ginkgo.BeforeEach(func() {
				event = &utils.RequestEventMsg{
					Request:        createTestRequest("req-1", "test-model", constant.Prefill, 100, constant.APCStrategyHash),
					EventType:      constant.RequestNew,
					TargetInstance: "instance-1",
				}
			})

			ginkgo.It("should not call reduceLoad", func() {
				apcScheduler.OnRequestEvent(event)

				apcScheduler.instancesMutex.RLock()
				gomega.Expect(len(apcScheduler.reqDispatchInfo)).To(gomega.Equal(1))
				apcScheduler.instancesMutex.RUnlock()
			})
		})

		ginkgo.Context("with RequestPrefillSuccess event", func() {
			ginkgo.BeforeEach(func() {
				event = &utils.RequestEventMsg{
					Request:        createTestRequest("req-1", "test-model", constant.Prefill, 100, constant.APCStrategyHash),
					EventType:      constant.RequestPrefillSuccess,
					TargetInstance: "instance-1",
					TimeStamp:      time.Now().Unix(),
				}
			})

			ginkgo.It("should call reduceLoad and update cache", func() {
				apcScheduler.OnRequestEvent(event)

				apcScheduler.instancesMutex.RLock()
				gomega.Expect(len(apcScheduler.reqDispatchInfo)).To(gomega.Equal(0))
				apcScheduler.instancesMutex.RUnlock()
			})
		})

		ginkgo.Context("with different load scenarios", func() {
			ginkgo.BeforeEach(func() {
				// Cleanup existing instances
				apcScheduler.ClearLoad()

				// Add instances
				newAvailable := map[string]instanceMetrics.WorkerStats{
					"instance-1": createTestWorkerStats("instance-1", constant.Prefill, instanceMetrics.Available, 32),
					"instance-2": createTestWorkerStats("instance-2", constant.Prefill, instanceMetrics.Available, 16),
				}
				err = apcScheduler.UpdateCluster(newAvailable, nil)
				gomega.Expect(err).ToNot(gomega.HaveOccurred())

				// Add different loads
				request1 := createTestRequest("req-1", "test-model", constant.Prefill, 100, constant.APCStrategyHash)
				apcScheduler.addLoadUnsafe("instance-1", request1, 0.8) // 80% match rate

				request2 := createTestRequest("req-2", "test-model", constant.Prefill, 200, constant.APCStrategyHash)
				apcScheduler.addLoadUnsafe("instance-2", request2, 0.25) // 25% match rate

				request3 := createTestRequest("req-3", "test-model", constant.Prefill, 150, constant.APCStrategyHash)
				apcScheduler.addLoadUnsafe("instance-1", request3, 0.2) // 20% match rate
			})

			ginkgo.It("should report correct loads with match rates", func() {
				loads := apcScheduler.ReportLoads(true, false)
				gomega.Expect(loads).ToNot(gomega.BeNil())
				gomega.Expect(len(loads.Available)).To(gomega.Equal(2))

				// Verify instance-1
				var inst1 *scheduler.InstanceLoadInfo
				var inst2 *scheduler.InstanceLoadInfo
				for _, inst := range loads.Available {
					if inst.Id == "instance-1" {
						inst1 = inst
					} else if inst.Id == "instance-2" {
						inst2 = inst
					}
				}
				gomega.Expect(inst1).ToNot(gomega.BeNil())
				gomega.Expect(inst2).ToNot(gomega.BeNil())

				// Verify load value calculation
				gomega.Expect(inst1.LoadVal).To(gomega.BeNumerically("==", 141.0))
				gomega.Expect(len(inst1.Requests)).To(gomega.Equal(2))

				// Verify request match rate
				req1Info := inst1.Requests["req-1"]
				gomega.Expect(req1Info).ToNot(gomega.BeNil())
				gomega.Expect(req1Info.LoadVal).To(gomega.BeNumerically("==", 0.8)) // 80/100

				req3Info := inst1.Requests["req-3"]
				gomega.Expect(req3Info).ToNot(gomega.BeNil())
				gomega.Expect(req3Info.LoadVal).To(gomega.BeNumerically("==", 0.2)) // 30/150

				// instance-2: 1 request, total prompt length=200, batch_size=16
				gomega.Expect(inst2.LoadVal).To(gomega.BeNumerically("==", 151.0))

				req2Info := inst2.Requests["req-2"]
				gomega.Expect(req2Info).ToNot(gomega.BeNil())
				gomega.Expect(req2Info.LoadVal).To(gomega.BeNumerically("==", 0.25)) // 50/200
			})
		})

		ginkgo.Context("when including unavailable instances", func() {
			ginkgo.BeforeEach(func() {
				// Add unavailable instances
				newUnavailable := map[string]instanceMetrics.WorkerStats{
					"instance-3": createTestWorkerStats("instance-3", constant.Prefill, instanceMetrics.Faulty, 8),
				}
				err = apcScheduler.UpdateCluster(nil, newUnavailable)
				gomega.Expect(err).ToNot(gomega.HaveOccurred())

				// Also add some load for unavailable instances
				apcScheduler.unavailableInstanceLoads["instance-3"] = apc.InstanceLoad{
					PromptTotalLen:   100,
					RequestsTotalCnt: 1,
				}
			})

			ginkgo.It("should report unavailable instances without batch size", func() {
				loads := apcScheduler.ReportLoads(true, true)
				gomega.Expect(len(loads.Unavailable)).To(gomega.Equal(1))

				unavailableInst := loads.Unavailable[0]
				gomega.Expect(unavailableInst.Id).To(gomega.Equal("instance-3"))

				// Unavailable instances do not contain batch size info
				gomega.Expect(unavailableInst.Desc).To(gomega.Equal("Total request num: 1, total prompt length: 100"))
			})
		})
	})

	ginkgo.Describe("ClearLoad", func() {
		ginkgo.BeforeEach(func() {
			// Create and start real APC manager
			cfg := createTestManagerConfig(true)
			apcManager, err = apc.NewManager(cfg, createTestClusterService())
			gomega.Expect(err).ToNot(gomega.HaveOccurred())
			err = apcManager.Start()
			gomega.Expect(err).ToNot(gomega.HaveOccurred())
			apc.SetGlobalManager(apcManager)

			err := apcScheduler.Init(nil)
			gomega.Expect(err).ToNot(gomega.HaveOccurred())

			// Add instances and requests
			newAvailable := map[string]instanceMetrics.WorkerStats{
				"instance-1": createTestWorkerStats("instance-1", constant.Prefill, instanceMetrics.Available, 32),
			}
			err = apcScheduler.UpdateCluster(newAvailable, nil)
			gomega.Expect(err).ToNot(gomega.HaveOccurred())

			// Add load
			request := createTestRequest("req-1", "test-model", constant.Prefill, 100, constant.APCStrategyHash)
			apcScheduler.addLoadUnsafe("instance-1", request, 0.5)
		})

		ginkgo.AfterEach(func() {
			err = apcManager.Stop()
			gomega.Expect(err).ToNot(gomega.HaveOccurred())
		})

		ginkgo.It("should clear all loads", func() {
			// Verify load exists
			apcScheduler.instancesMutex.RLock()
			gomega.Expect(len(apcScheduler.reqDispatchInfo)).To(gomega.Equal(1))
			gomega.Expect(apcScheduler.availableInstanceLoads["instance-1"].RequestsTotalCnt).To(gomega.Equal(uint64(1)))
			gomega.Expect(apcScheduler.availableInstanceLoads["instance-1"].PromptTotalLen).To(gomega.Equal(uint64(51.0)))
			apcScheduler.instancesMutex.RUnlock()

			apcScheduler.ClearLoad()

			// Verify load is cleared but instances remain
			apcScheduler.instancesMutex.RLock()
			gomega.Expect(len(apcScheduler.reqDispatchInfo)).To(gomega.Equal(0))
			gomega.Expect(len(apcScheduler.availableInstanceLoads)).To(gomega.Equal(1)) // instances still remain
			gomega.Expect(apcScheduler.availableInstanceLoads["instance-1"].RequestsTotalCnt).To(gomega.Equal(uint64(0)))
			gomega.Expect(apcScheduler.availableInstanceLoads["instance-1"].PromptTotalLen).To(gomega.Equal(uint64(0)))
			apcScheduler.instancesMutex.RUnlock()
		})
	})

	ginkgo.Describe("computeInstLoad and Load Calculation", func() {
		ginkgo.BeforeEach(func() {
			// Create and start real APC manager
			cfg := createTestManagerConfig(true)
			apcManager, err = apc.NewManager(cfg, createTestClusterService())
			gomega.Expect(err).ToNot(gomega.HaveOccurred())
			err = apcManager.Start()
			gomega.Expect(err).ToNot(gomega.HaveOccurred())
			apc.SetGlobalManager(apcManager)

			err := apcScheduler.Init(nil)
			gomega.Expect(err).ToNot(gomega.HaveOccurred())
		})

		ginkgo.Context("with different batch sizes", func() {
			ginkgo.It("should calculate load correctly for small batch size", func() {
				// Add instance with batch_size=8
				newAvailable := map[string]instanceMetrics.WorkerStats{
					"small-batch": createTestWorkerStats("small-batch", constant.Prefill, instanceMetrics.Available, 8),
				}
				err = apcScheduler.UpdateCluster(newAvailable, nil)
				gomega.Expect(err).ToNot(gomega.HaveOccurred())

				// Add 20 requests
				for i := 0; i < 20; i++ {
					request := createTestRequest(fmt.Sprintf("req-%d", i), "test-model", constant.Prefill, 50, constant.APCStrategyHash)
					apcScheduler.addLoadUnsafe("small-batch", request, 0)
				}

				// Calculate expected load
				// prompt total length = 20 * 50 = 1000
				// batch_size = 8
				loads := apcScheduler.ReportLoads(true, false)
				gomega.Expect(len(loads.Available)).To(gomega.Equal(1))
				gomega.Expect(loads.Available[0].LoadVal).To(gomega.BeNumerically("==", 2040.0))
			})

			ginkgo.It("should calculate load correctly for large batch size", func() {
				// Add instance with batch_size=64
				newAvailable := map[string]instanceMetrics.WorkerStats{
					"large-batch": createTestWorkerStats("large-batch", constant.Prefill, instanceMetrics.Available, 64),
				}
				err = apcScheduler.UpdateCluster(newAvailable, nil)
				gomega.Expect(err).ToNot(gomega.HaveOccurred())

				// Add 20 requests
				for i := 0; i < 20; i++ {
					request := createTestRequest(fmt.Sprintf("req-%d", i), "test-model", constant.Prefill, 50, constant.APCStrategyHash)
					apcScheduler.addLoadUnsafe("large-batch", request, 0)
				}

				// Calculate expected load
				// prompt total length = 20 * 50 = 1000
				// batch_size = 64
				loads := apcScheduler.ReportLoads(true, false)
				gomega.Expect(len(loads.Available)).To(gomega.Equal(1))
				gomega.Expect(loads.Available[0].LoadVal).To(gomega.BeNumerically("==", 1020.0))
			})
		})

		ginkgo.Context("with instances without batch size", func() {
			ginkgo.It("should use default batch size", func() {
				// Add instance without max_num_seqs
				stats := &instanceMetrics.ZeroCopyStats{
					Id:         "no-batch",
					Role:       constant.Prefill,
					Status:     instanceMetrics.Available,
					StaticInfo: make(map[string]string),
				}
				newAvailable := map[string]instanceMetrics.WorkerStats{
					"no-batch": stats,
				}
				err = apcScheduler.UpdateCluster(newAvailable, nil)
				gomega.Expect(err).ToNot(gomega.HaveOccurred())

				// Add 5 requests
				for i := 0; i < 5; i++ {
					request := createTestRequest(fmt.Sprintf("req-%d", i), "test-model", constant.Prefill, 40, constant.APCStrategyHash)
					apcScheduler.addLoadUnsafe("no-batch", request, 0)
				}

				// Calculate using default batch_size=16
				// prompt total length = 5 * 40 = 200
				// batch_size = 16 (default value)
				loads := apcScheduler.ReportLoads(true, false)
				gomega.Expect(len(loads.Available)).To(gomega.Equal(1))
				gomega.Expect(loads.Available[0].LoadVal).To(gomega.BeNumerically("==", 205.0))
			})
		})

		ginkgo.Context("after load reduction", func() {
			ginkgo.It("should update load correctly", func() {
				// Add instances
				newAvailable := map[string]instanceMetrics.WorkerStats{
					"test-instance": createTestWorkerStats("test-instance", constant.Prefill, instanceMetrics.Available, 16),
				}
				err = apcScheduler.UpdateCluster(newAvailable, nil)
				gomega.Expect(err).ToNot(gomega.HaveOccurred())

				// Add initial load
				for i := 0; i < 10; i++ {
					request := createTestRequest(fmt.Sprintf("req-%d", i), "test-model", constant.Prefill, 30, constant.APCStrategyHash)
					apcScheduler.addLoadUnsafe("test-instance", request, 0)
				}

				// Verify initial load
				loads := apcScheduler.ReportLoads(true, false)
				initialLoad := loads.Available[0].LoadVal
				gomega.Expect(initialLoad).To(gomega.BeNumerically(">", 0))

				// Reduce load
				apcScheduler.reduceLoad("req-1")
				apcScheduler.reduceLoad("req-2")

				// Verify load reduced
				loads = apcScheduler.ReportLoads(true, false)
				finalLoad := loads.Available[0].LoadVal
				gomega.Expect(finalLoad).To(gomega.BeNumerically("<", initialLoad))

				// Verify request count reduced
				apcScheduler.instancesMutex.RLock()
				gomega.Expect(len(apcScheduler.reqDispatchInfo)).To(gomega.Equal(8))
				apcScheduler.instancesMutex.RUnlock()
			})
		})
	})
})
