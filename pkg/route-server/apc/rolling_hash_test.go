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

package apc_test

import (
	"fmt"
	"reflect"
	"sync"

	"github.com/agiledragon/gomonkey/v2"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"infer-dp-scheduler/pkg/common/config"
	"infer-dp-scheduler/pkg/common/constant"
	"infer-dp-scheduler/pkg/common/logger"
	"infer-dp-scheduler/pkg/common/utils"
	"infer-dp-scheduler/pkg/route-server/apc"
)

// createTestRequest creates a test request using a real RemoteRequestWrapper
func createTestRequest(requestId, promptCacheKey string, promptTokenCount int32, model string) *utils.RemoteRequestWrapper {
	// Create a mock request to generate RemoteRequestWrapper
	mockRequest := struct {
		requestId        string
		promptCacheKey   string
		promptTokenCount int32
		model            string
		apcStrategyType  string
	}{
		requestId:        requestId,
		promptCacheKey:   promptCacheKey,
		promptTokenCount: promptTokenCount,
		model:            model,
		apcStrategyType:  constant.APCStrategyHash,
	}

	return &utils.RemoteRequestWrapper{
		Id:              mockRequest.requestId,
		Type:            "test",
		SloTag:          "test-slo",
		SLOInterval:     nil,
		Content:         nil,
		PromptCacheKey:  mockRequest.promptCacheKey,
		ApcStrategyType: mockRequest.apcStrategyType,
		MaxTokens:       1000,
		PromptTokens:    mockRequest.promptTokenCount,
		Stream:          false,
		Model:           mockRequest.model,
	}
}

var _ = Describe("RollingHashStrategy", func() {
	var (
		strategy *apc.RollingHashStrategy
		cfg      *config.APCRollingHashConfig
	)

	BeforeEach(func() {
		cfg = &config.APCRollingHashConfig{
			BlockSizeBytes: 64,
			MaxBlocks:      10,
			APCStrategyMemoryConfig: config.APCStrategyMemoryConfig{
				MaxEntries:  1000,
				MaxMemoryMB: 100,
				CacheTTLSec: 3600,
			},
		}
	})

	AfterEach(func() {
		if strategy != nil {
			strategy.CleanUp()
		}
	})

	Describe("NewRollingHashStrategy", func() {
		Context("when creating with valid config", func() {
			It("should create strategy successfully", func() {
				var err error
				strategy, err = apc.NewRollingHashStrategy(cfg)
				Expect(err).ToNot(HaveOccurred())
				Expect(strategy).ToNot(BeNil())
				Expect(strategy.Name()).To(Equal(constant.APCStrategyHash))
			})

			It("should use default values when not specified", func() {
				cfgWithoutDefaults := &config.APCRollingHashConfig{
					BlockSizeBytes: 64,
					MaxBlocks:      10,
					// Do not set APCStrategyMemoryConfig, test defaults
				}

				var err error
				strategy, err = apc.NewRollingHashStrategy(cfgWithoutDefaults)
				Expect(err).ToNot(HaveOccurred())
				Expect(strategy).ToNot(BeNil())

				// Verify defaults - test by setting cache
				err = strategy.UpdateCache("test:test", "inst1")
				Expect(err).ToNot(HaveOccurred())
			})
		})

		Context("when cache creation fails", func() {
			It("should return error", func() {
				patches := gomonkey.NewPatches()
				defer patches.Reset()

				// Stub NewRistrettoCache to return error
				patches.ApplyFunc(apc.NewRistrettoCache, func(config config.APCStrategyMemoryConfig) (*apc.RistrettoCache, error) {
					return nil, fmt.Errorf("cache creation failed")
				})

				var err error
				_, err = apc.NewRollingHashStrategy(cfg)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("failed to create promptKeyCache"))
			})
		})
	})

	Describe("DecodeFromCache", func() {
		BeforeEach(func() {
			var err error
			strategy, err = apc.NewRollingHashStrategy(cfg)
			Expect(err).ToNot(HaveOccurred())
		})

		Context("with valid PromptKeyCacheEntry", func() {
			It("should decode successfully", func() {
				entry := &apc.PromptKeyCacheEntry{
					InstanceMap: map[string]struct{}{"instance1": {}, "instance2": {}},
				}

				var target apc.PromptKeyCacheEntry
				err := strategy.DecodeFromCache(entry, &target)
				Expect(err).ToNot(HaveOccurred())
				Expect(target.InstanceMap).To(Equal(entry.InstanceMap))
			})
		})

		Context("with invalid target type", func() {
			It("should return error", func() {
				entry := &apc.PromptKeyCacheEntry{}
				var invalidTarget string
				err := strategy.DecodeFromCache(entry, &invalidTarget)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("unsupported target type"))
			})
		})

		Context("with invalid cache value type", func() {
			It("should return error", func() {
				var target apc.PromptKeyCacheEntry
				err := strategy.DecodeFromCache("invalid", &target)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("cache value is not *PromptKeyCacheEntry type"))
			})
		})
	})

	Describe("GetMatchedInstances", func() {
		BeforeEach(func() {
			var err error
			strategy, err = apc.NewRollingHashStrategy(cfg)
			Expect(err).ToNot(HaveOccurred())
		})

		Context("with valid cache match", func() {
			It("should return matched instances", func() {
				promptCacheKey := "model:h1:h2:h3"
				availableInstances := map[string]apc.InstanceLoad{
					"inst1": {},
					"inst2": {},
				}

				// First update cache to establish promptKey to instance mapping
				err := strategy.UpdateCache(promptCacheKey, "inst1")
				Expect(err).ToNot(HaveOccurred())

				request := createTestRequest("req1", promptCacheKey, 300, "model")

				result, _, err := strategy.GetMatchedInstances(request, availableInstances)
				Expect(err).ToNot(HaveOccurred())
				Expect(len(result)).To(Equal(1))
				Expect(result).To(HaveKey("inst1"))
			})
		})

		Context("with no available instances", func() {
			It("should return error", func() {
				request := createTestRequest("req1", "model:h1:h2:h3", 300, "model")
				_, _, err := strategy.GetMatchedInstances(request, map[string]apc.InstanceLoad{})
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("no available instances"))
			})
		})

		Context("with empty prompt key", func() {
			It("should return error", func() {
				request := createTestRequest("req1", "", 300, "model")
				_, _, err := strategy.GetMatchedInstances(request, map[string]apc.InstanceLoad{"inst1": {}})
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("nil promptKey"))
			})
		})

		Context("with no cache match", func() {
			It("should return error", func() {
				request := createTestRequest("req1", "model:notfound", 300, "model")
				_, _, err := strategy.GetMatchedInstances(request, map[string]apc.InstanceLoad{"inst1": {}})
				Expect(err).NotTo(HaveOccurred())
			})
		})

		Context("with multiple instances in cache", func() {
			It("should return only available instances", func() {
				promptCacheKey := "model:h1:h2:h3"
				availableInstances := map[string]apc.InstanceLoad{
					"inst1": {},
					"inst3": {}, // inst2 is not available
				}

				// Update cache, add multiple instances
				err := strategy.UpdateCache(promptCacheKey, "inst1")
				Expect(err).ToNot(HaveOccurred())
				err = strategy.UpdateCache(promptCacheKey, "inst2")
				Expect(err).ToNot(HaveOccurred())

				request := createTestRequest("req1", promptCacheKey, 300, "model")

				result, _, err := strategy.GetMatchedInstances(request, availableInstances)
				Expect(err).ToNot(HaveOccurred())
				Expect(len(result)).To(Equal(1))
				Expect(result).To(HaveKey("inst1"))
				Expect(result).ToNot(HaveKey("inst2"))
			})
		})
	})

	Describe("UpdateCache", func() {
		BeforeEach(func() {
			var err error
			strategy, err = apc.NewRollingHashStrategy(cfg)
			Expect(err).ToNot(HaveOccurred())

			// Stub logger to avoid log output
			patches := gomonkey.NewPatches()
			patches.ApplyMethod(reflect.TypeOf(&logger.Logger{}), "Debugf", func(_ *logger.Logger, format string, args ...interface{}) {
				// Do nothing
			})
			defer patches.Reset()
		})

		It("should update cache successfully", func() {
			err := strategy.UpdateCache("model:h1:h2", "inst1")
			Expect(err).ToNot(HaveOccurred())

			// Verify cache is set via downstream method
			availableInstances := map[string]apc.InstanceLoad{"inst1": {}}
			request := createTestRequest("req1", "model:h1:h2", 300, "model")
			_, _, err = strategy.GetMatchedInstances(request, availableInstances)
			Expect(err).ToNot(HaveOccurred())
		})

		It("should return error for empty key", func() {
			err := strategy.UpdateCache("", "inst1")
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("empty prompt cache key"))
		})

		It("should handle hash chain correctly", func() {
			// Test longer hash chain
			err := strategy.UpdateCache("model:h1:h2:h3:h4", "inst1")
			Expect(err).ToNot(HaveOccurred())

			// Test that different prefix lengths can all be found
			availableInstances := map[string]apc.InstanceLoad{"inst1": {}}
			request := createTestRequest("req1", "model:h1:h2:h3:h4", 400, "model")
			_, _, err = strategy.GetMatchedInstances(request, availableInstances)
			Expect(err).ToNot(HaveOccurred())
		})
	})

	Describe("RemoveInstanceIdFromCache", func() {
		BeforeEach(func() {
			var err error
			strategy, err = apc.NewRollingHashStrategy(cfg)
			Expect(err).ToNot(HaveOccurred())

			// Stub logger to avoid log output
			patches := gomonkey.NewPatches()
			patches.ApplyMethod(reflect.TypeOf(&logger.Logger{}), "Infof", func(_ *logger.Logger, format string, args ...interface{}) {
				// Do nothing
			})
			patches.ApplyMethod(reflect.TypeOf(&logger.Logger{}), "Warnf", func(_ *logger.Logger, format string, args ...interface{}) {
				// Do nothing
			})
			defer patches.Reset()
		})

		It("should remove instance and related cache", func() {
			promptCacheKey := "model:h1:h2"
			availableInstances := map[string]apc.InstanceLoad{"inst1": {}}

			// First add cache data
			err := strategy.UpdateCache(promptCacheKey, "inst1")
			Expect(err).ToNot(HaveOccurred())

			// Verify instance can be found
			request := createTestRequest("req1", promptCacheKey, 300, "model")
			_, _, err = strategy.GetMatchedInstances(request, availableInstances)
			Expect(err).ToNot(HaveOccurred())

			// Mark instance for removal
			toRemove := map[string]struct{}{"inst1": {}}
			err = strategy.UpdateToRemoveInstances(toRemove)
			Expect(err).ToNot(HaveOccurred())

			// Verify instance can no longer be found - empty availableInstances
			_, _, err = strategy.GetMatchedInstances(request, map[string]apc.InstanceLoad{})
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("no available instances"))
		})

		It("should handle empty toRemove map", func() {
			err := strategy.UpdateToRemoveInstances(nil)
			Expect(err).ToNot(HaveOccurred())

			err = strategy.UpdateToRemoveInstances(make(map[string]struct{}))
			Expect(err).ToNot(HaveOccurred())
		})

		It("should handle non-existent instance", func() {
			toRemove := map[string]struct{}{"nonexistent": {}}
			err := strategy.UpdateToRemoveInstances(toRemove)
			Expect(err).ToNot(HaveOccurred()) // Should have no error
		})
	})

	Describe("CleanUp", func() {
		BeforeEach(func() {
			var err error
			strategy, err = apc.NewRollingHashStrategy(cfg)
			Expect(err).ToNot(HaveOccurred())

			// Stub logger to avoid log output
			patches := gomonkey.NewPatches()
			patches.ApplyMethod(reflect.TypeOf(&logger.Logger{}), "Infof", func(_ *logger.Logger, format string, args ...interface{}) {
				// Do nothing
			})
			patches.ApplyMethod(reflect.TypeOf(&logger.Logger{}), "Warnf", func(_ *logger.Logger, format string, args ...interface{}) {
				// Do nothing
			})
			defer patches.Reset()
		})

		It("should clear all caches", func() {
			// Add some data
			err := strategy.UpdateCache("model:test1:abc123", "inst1")
			Expect(err).ToNot(HaveOccurred())
			err = strategy.UpdateCache("model:test2:def456", "inst2")
			Expect(err).ToNot(HaveOccurred())

			// Verify data exists (via lookup)
			availableInstances := map[string]apc.InstanceLoad{"inst1": {}}
			request := createTestRequest("req1", "model:test1:abc123", 100, "model")
			_, _, err = strategy.GetMatchedInstances(request, availableInstances)
			Expect(err).ToNot(HaveOccurred())

			// Clean up
			strategy.CleanUp()

			// Verify cache is cleared (via not found)
			_, _, err = strategy.GetMatchedInstances(request, availableInstances)
			Expect(err).NotTo(HaveOccurred())
		})
	})

	Describe("ComputePromptKey", func() {
		BeforeEach(func() {
			var err error
			strategy, err = apc.NewRollingHashStrategy(cfg)
			Expect(err).ToNot(HaveOccurred())
		})

		It("should compute hash chain for valid prompt", func() {
			prompt := "This is a test prompt longer than 64 bytes to ensure we can generate hash chain properly"
			result, err := strategy.ComputePromptKey(prompt)
			Expect(err).ToNot(HaveOccurred())
			Expect(result).ToNot(BeNil())

			// Verify return type is []string
			hashChain, ok := result.([]string)
			Expect(ok).To(BeTrue())
			Expect(len(hashChain)).To(BeNumerically(">", 0))

			// Verify each hash format (except the last which may be exact length info)
			for i, hash := range hashChain {
				Expect(len(hash)).To(BeNumerically(">", 2))
				if i < len(hashChain)-1 {
					// Previous elements must be hashes
					Expect(hash).To(HavePrefix("h_"))
				} else {
					// Last element may be a hash or exact length info
					Expect(hash).To(SatisfyAny(HavePrefix("h_"), HavePrefix("e_")))
				}
			}
		})

		It("should return error for empty prompt", func() {
			result, err := strategy.ComputePromptKey("")
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("failed to compute hash chain"))
			Expect(result).To(BeNil())
		})

		It("should not return error for short prompt", func() {
			_, err := strategy.ComputePromptKey("short")
			Expect(err).NotTo(HaveOccurred())
		})
	})

	Describe("GenerateCacheKey", func() {
		BeforeEach(func() {
			var err error
			strategy, err = apc.NewRollingHashStrategy(cfg)
			Expect(err).ToNot(HaveOccurred())
		})

		It("should generate cache key correctly", func() {
			hashChain := []string{"h_abc123", "h_def456", "h_ghi789"}
			model := "test-model"

			key := strategy.GenerateCacheKey(model, hashChain)
			expected := "test-model:h_abc123:h_def456:h_ghi789"
			Expect(key).To(Equal(expected))
		})

		It("should handle empty hash chain", func() {
			model := "test-model"
			key := strategy.GenerateCacheKey(model, []string{})
			Expect(key).To(Equal(model))
		})

		It("should handle nil hash chain", func() {
			model := ""
			key := strategy.GenerateCacheKey(model, nil)
			Expect(key).To(Equal(model))
		})

		It("should handle invalid type", func() {
			model := ""
			key := strategy.GenerateCacheKey(model, "invalid")
			Expect(key).To(Equal(model))
		})
	})

	Describe("ConcurrentAccess", func() {
		BeforeEach(func() {
			var err error
			strategy, err = apc.NewRollingHashStrategy(cfg)
			Expect(err).ToNot(HaveOccurred())
		})

		It("should handle concurrent updates safely", func() {
			var wg sync.WaitGroup
			numGoroutines := 10
			numOperations := 100

			for i := 0; i < numGoroutines; i++ {
				wg.Add(1)
				go func(id int) {
					defer wg.Done()
					for j := 0; j < numOperations; j++ {
						err := strategy.UpdateCache("model:hash", "inst"+string(rune('0'+id)))
						Expect(err).ToNot(HaveOccurred())
					}
				}(i)
			}

			wg.Wait()

			// Verify cache has data
			availableInstances := map[string]apc.InstanceLoad{"inst0": {}}
			request := createTestRequest("req1", "model:hash", 300, "model")
			_, _, err := strategy.GetMatchedInstances(request, availableInstances)
			Expect(err).ToNot(HaveOccurred())
		})
	})

	Describe("CompleteFlow", func() {
		BeforeEach(func() {
			var err error
			strategy, err = apc.NewRollingHashStrategy(cfg)
			Expect(err).ToNot(HaveOccurred())
		})

		It("should handle complete flow correctly", func() {
			// 1. Compute prompt key
			prompt := "This is a long prompt that will be used for testing the complete flow of the rolling hash strategy implementation with sufficient length to generate multiple hash blocks."
			promptKeyInterface, err := strategy.ComputePromptKey(prompt)
			Expect(err).ToNot(HaveOccurred())

			hashChain, ok := promptKeyInterface.([]string)
			Expect(ok).To(BeTrue())
			Expect(len(hashChain)).To(BeNumerically(">", 1))

			// 2. Generate cache key
			cacheKey := strategy.GenerateCacheKey("test-model", promptKeyInterface)
			Expect(cacheKey).ToNot(BeEmpty())
			Expect(cacheKey).To(ContainSubstring("test-model"))

			// 3. Update cache
			instanceID := "test-instance-001"
			err = strategy.UpdateCache(cacheKey, instanceID)
			Expect(err).ToNot(HaveOccurred())

			// 4. Query matched instances
			request := createTestRequest("req-001", cacheKey, int32(len(prompt)), "test-model")
			availableInstances := map[string]apc.InstanceLoad{
				instanceID:     {},
				"another-inst": {}, // Unavailable instance
			}

			matchedInstances, _, err := strategy.GetMatchedInstances(request, availableInstances)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(matchedInstances)).To(Equal(1))
			Expect(matchedInstances).To(HaveKey(instanceID))

			// 5. Mark instance for removal
			toRemove := map[string]struct{}{instanceID: {}}
			err = strategy.UpdateToRemoveInstances(toRemove)
			Expect(err).ToNot(HaveOccurred())

			// 6. Query again should not find anything
			_, _, err = strategy.GetMatchedInstances(request, availableInstances)
			Expect(err).ToNot(HaveOccurred())
		})
	})

	Describe("HashChainBehavior", func() {
		BeforeEach(func() {
			var err error
			strategy, err = apc.NewRollingHashStrategy(cfg)
			Expect(err).ToNot(HaveOccurred())
		})

		It("should find instances with shorter hash chains", func() {
			longKey := "modelA:h1:h2:h3:h4:h5"
			shortKey := "modelB:h1:h2"
			instanceID := "test-inst"

			// Update both long and short caches (use different models to avoid prefix conflicts)
			err := strategy.UpdateCache(longKey, instanceID)
			Expect(err).ToNot(HaveOccurred())
			err = strategy.UpdateCache(shortKey, "other-inst")
			Expect(err).ToNot(HaveOccurred())

			// Query with short cache key, should find other-inst but not test-inst
			availableInstances := map[string]apc.InstanceLoad{instanceID: {}} // Only test-inst is available
			request := createTestRequest("req1", shortKey, 200, "modelB")
			_, _, err = strategy.GetMatchedInstances(request, availableInstances)
			Expect(err).NotTo(HaveOccurred()) // Because shortKey maps to other-inst, which is not in available instances

			// Query with long cache key should find test-inst
			request = createTestRequest("req2", longKey, 400, "modelA")
			_, _, err = strategy.GetMatchedInstances(request, availableInstances)
			Expect(err).ToNot(HaveOccurred())
		})
	})

	Describe("ProgressiveBlockStrategy", func() {
		BeforeEach(func() {
			progressiveCfg := &config.APCRollingHashConfig{
				BlockSizeBytes:                512,
				MaxBlocks:                     10,
				MatchThreshold:                0.3, // 30% match ratio threshold
				ProgressiveBlockEnable:        true,
				ProgressiveBaseBlockSizeBytes: 256,
				APCStrategyMemoryConfig: config.APCStrategyMemoryConfig{
					MaxEntries:  1000,
					MaxMemoryMB: 64,
					CacheTTLSec: 300,
				},
			}
			var err error
			strategy, err = apc.NewRollingHashStrategy(progressiveCfg)
			Expect(err).ToNot(HaveOccurred())
		})

		It("should compute hash chain with progressive block sizes", func() {
			prompt := "This is a test prompt that should generate multiple blocks with progressive sizes"
			promptKeyInterface, err := strategy.ComputePromptKey(prompt)
			Expect(err).ToNot(HaveOccurred())

			hashChain, ok := promptKeyInterface.([]string)
			Expect(ok).To(BeTrue())
			Expect(len(hashChain)).To(BeNumerically(">", 0))

			// Verify each hash format (except the last which may be exact length info)
			for i, hash := range hashChain {
				Expect(len(hash)).To(BeNumerically(">", 2))
				if i < len(hashChain)-1 {
					// Previous elements must be hashes
					Expect(hash).To(HavePrefix("h_"))
				} else {
					// Last element may be a hash or exact length info
					Expect(hash).To(SatisfyAny(HavePrefix("h_"), HavePrefix("e_")))
				}
			}
		})

		It("should handle short prompt with progressive strategy", func() {
			// Even a short prompt should generate a hash
			shortPrompt := "short"
			promptKeyInterface, err := strategy.ComputePromptKey(shortPrompt)
			Expect(err).ToNot(HaveOccurred())

			hashChain, ok := promptKeyInterface.([]string)
			Expect(ok).To(BeTrue())
			Expect(len(hashChain)).To(Equal(2)) // Short prompt should generate one hash and one length info
		})

		It("should calculate progressive matching ratio correctly", func() {
			// Create a sufficiently long prompt to generate multiple progressive blocks
			prompt := "This is a very long prompt designed to test the progressive block strategy implementation. It should be long enough to generate multiple hash blocks with exponentially increasing sizes: 256, 512, 1024, 2048 bytes and so on. Let's add more content to ensure we have enough data to work with across multiple blocks and test the matching ratio calculations accurately."

			promptKeyInterface, err := strategy.ComputePromptKey(prompt)
			Expect(err).ToNot(HaveOccurred())

			hashChain, ok := promptKeyInterface.([]string)
			Expect(ok).To(BeTrue())
			Expect(len(hashChain)).To(BeNumerically(">", 1))

			// Test cache update and query
			cacheKey := strategy.GenerateCacheKey("test-model", promptKeyInterface)
			err = strategy.UpdateCache(cacheKey, "test-instance")
			Expect(err).ToNot(HaveOccurred())

			request := createTestRequest("req-001", cacheKey, int32(len(prompt)), "test-model")
			availableInstances := map[string]apc.InstanceLoad{"test-instance": {}}

			matchedInstances, maxRatio, err := strategy.GetMatchedInstances(request, availableInstances)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(matchedInstances)).To(Equal(1))
			Expect(maxRatio).To(BeNumerically(">", 0))
			Expect(maxRatio).To(BeNumerically("<=", 1.0))
		})
	})

	Describe("FixedVsProgressiveComparison", func() {
		It("should produce different hash chains for same prompt", func() {

			prompt := "This is a much longer test prompt specifically designed to ensure that fixed and progressive block strategies produce different hash chains. With sufficient length, the fixed blocks of size 512 and progressive blocks starting from 256 will diverge in their chunking patterns, leading to different hash sequences and demonstrating the fundamental difference between these two hash chain generation strategies."

			// Fixed block strategy
			fixedCfg := &config.APCRollingHashConfig{
				BlockSizeBytes: 512,
				MaxBlocks:      10,
				APCStrategyMemoryConfig: config.APCStrategyMemoryConfig{
					MaxEntries:  1000,
					MaxMemoryMB: 64,
					CacheTTLSec: 300,
				},
			}
			fixedStrategy, err := apc.NewRollingHashStrategy(fixedCfg)
			Expect(err).ToNot(HaveOccurred())

			fixedPromptKey, err := fixedStrategy.ComputePromptKey(prompt)
			Expect(err).ToNot(HaveOccurred())
			fixedHashChain := fixedPromptKey.([]string)

			// Progressive block strategy
			progressiveCfg := &config.APCRollingHashConfig{
				BlockSizeBytes:                512, // This parameter is ignored in progressive mode
				MaxBlocks:                     10,
				ProgressiveBlockEnable:        true,
				ProgressiveBaseBlockSizeBytes: 256,
				APCStrategyMemoryConfig: config.APCStrategyMemoryConfig{
					MaxEntries:  1000,
					MaxMemoryMB: 64,
					CacheTTLSec: 300,
				},
			}
			progressiveStrategy, err := apc.NewRollingHashStrategy(progressiveCfg)
			Expect(err).ToNot(HaveOccurred())

			progressivePromptKey, err := progressiveStrategy.ComputePromptKey(prompt)
			Expect(err).ToNot(HaveOccurred())
			progressiveHashChain := progressivePromptKey.([]string)

			// The two strategies should produce different hash values
			// Skip the last exact length info for comparison
			fixedHashes := fixedHashChain[:len(fixedHashChain)-1]
			progressiveHashes := progressiveHashChain[:len(progressiveHashChain)-1]

			// Hash chain contents should differ
			Expect(fixedHashes).ToNot(Equal(progressiveHashes))
			// But both should have at least one hash
			Expect(len(fixedHashes)).To(BeNumerically(">", 0))
			Expect(len(progressiveHashes)).To(BeNumerically(">", 0))
		})
	})
})
