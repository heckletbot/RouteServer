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
	"strings"
	"sync"
	"time"

	"infer-dp-scheduler/pkg/common/config"
	"infer-dp-scheduler/pkg/common/constant"
	"infer-dp-scheduler/pkg/common/logger"
	"infer-dp-scheduler/pkg/common/utils"

	"github.com/cespare/xxhash/v2"
)

// RollingHashStrategy is the rolling hash strategy implementation
type RollingHashStrategy struct {
	blockSize int
	maxBlocks int

	// Progressive block strategy configuration
	progressiveBlockEnable        bool
	progressiveBaseBlockSizeBytes int

	// Match threshold configuration
	matchThreshold float64 // Minimum match ratio threshold (0.0-1.0)

	// model -> promptKey -> []instanceId cache
	promptKeyCache LocalCache

	// Map of instances pending removal, for lazy cleanup
	toRemoveInstances map[string]struct{}

	// Memory management configuration
	memConfig *config.APCStrategyMemoryConfig

	// Mutex
	mu sync.RWMutex
}

// NewRollingHashStrategy creates a rolling hash strategy instance
func NewRollingHashStrategy(cfg *config.APCRollingHashConfig) (*RollingHashStrategy, error) {
	// Validate parameters
	if cfg.BlockSizeBytes <= 0 {
		return nil, fmt.Errorf("invalid block size: must be positive, got %d", cfg.BlockSizeBytes)
	}
	if cfg.MaxBlocks <= 0 {
		return nil, fmt.Errorf("invalid max blocks: must be positive, got %d", cfg.MaxBlocks)
	}

	// Set progressive block strategy defaults
	progressiveBaseBlockSizeBytes := cfg.ProgressiveBaseBlockSizeBytes
	if progressiveBaseBlockSizeBytes <= 0 {
		progressiveBaseBlockSizeBytes = 256 // Default 256 bytes
	}

	// Create memory configuration
	memConfig := cfg.APCStrategyMemoryConfig

	// Set defaults
	if memConfig.MaxEntries == 0 {
		memConfig.MaxEntries = 32768 // 32K entries
	}
	if memConfig.MaxMemoryMB == 0 {
		memConfig.MaxMemoryMB = 512 // 512MB limit
	}
	if memConfig.CacheTTLSec == 0 {
		memConfig.CacheTTLSec = 3600
	}

	// Set match threshold default
	matchThreshold := cfg.MatchThreshold
	if matchThreshold <= 0 || matchThreshold > 1.0 {
		matchThreshold = 0.0 // Default: no threshold
	}

	promptKeyCache, err := NewRistrettoCache(memConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create promptKeyCache: %v ", err)
	}

	return &RollingHashStrategy{
		blockSize:                     cfg.BlockSizeBytes,
		maxBlocks:                     cfg.MaxBlocks,
		progressiveBlockEnable:        cfg.ProgressiveBlockEnable,
		progressiveBaseBlockSizeBytes: progressiveBaseBlockSizeBytes,
		matchThreshold:                matchThreshold,
		promptKeyCache:                promptKeyCache,
		toRemoveInstances:             make(map[string]struct{}),
		memConfig:                     &memConfig,
	}, nil
}

// Name returns the strategy name
func (rh *RollingHashStrategy) Name() string {
	return constant.APCStrategyHash
}

// DecodeFromCache parses data from cache
// Optimized version: direct type assertion to avoid JSON serialization overhead
func (rh *RollingHashStrategy) DecodeFromCache(cacheValue interface{}, target interface{}) error {
	// Direct type conversion based on target type
	switch t := target.(type) {
	case *PromptKeyCacheEntry:
		if entry, ok := cacheValue.(*PromptKeyCacheEntry); ok {
			*t = *entry // Copy struct
			return nil
		}
		return fmt.Errorf("cache value is not *PromptKeyCacheEntry type")
	default:
		return fmt.Errorf("unsupported target type for decode")
	}
}

// GetMatchedInstances gets matched instance candidates (only responsible for cache lookup and matching)
// Refactored version: reduced nesting depth, extracted methods, applied optimization strategies
func (rh *RollingHashStrategy) GetMatchedInstances(request utils.GenerateRequest, availableInstances map[string]InstanceLoad) (map[string]float64, float64, error) {
	rh.mu.RLock()
	defer rh.mu.RUnlock()

	promptCacheKey := request.GetPromptCacheKey()
	promptTokenCount := request.GetPromptTokenCount()

	// Early return - reduce nesting
	if err := rh.validateInputs(promptCacheKey, availableInstances); err != nil {
		return nil, 0.0, err
	}

	matchedResults := make(map[string]float64)

	// Extract core logic to independent method to reduce depth
	maxRatio := rh.findMatchingInstances(promptCacheKey, availableInstances, matchedResults, promptTokenCount)

	if len(matchedResults) == 0 {
		return nil, 0.0, nil
	}

	return matchedResults, maxRatio, nil
}

// validateInputs validates input parameters - early return pattern
func (rh *RollingHashStrategy) validateInputs(promptCacheKey string, availableInstances map[string]InstanceLoad) error {
	if len(availableInstances) == 0 {
		return fmt.Errorf("no available instances")
	}
	if len(promptCacheKey) == 0 {
		return fmt.Errorf("nil promptKey")
	}
	return nil
}

// findMatchingInstances is the core logic for finding matching instances - reduces main function nesting
// Returns the maximum match ratio
func (rh *RollingHashStrategy) findMatchingInstances(promptCacheKey string,
	availableInstances map[string]InstanceLoad, matchedResults map[string]float64, promptTokenCount int32) float64 {
	hashChain := strings.Split(promptCacheKey, ":")
	currentBlocksCount := len(hashChain) - 1 // Subtract 1 because the first is model
	var maxRatio float64 = 0.0

	// Check if exact length info is present (last element starts with "e_")
	hasExactLength := currentBlocksCount > 0 && strings.HasPrefix(hashChain[currentBlocksCount], "e_")

	// If exact length info exists, adjust hash chain and block count
	if hasExactLength {
		currentBlocksCount-- // Last one is length info, not a hash
	}

	for rh.shouldContinueProcessing(currentBlocksCount, matchedResults, availableInstances) {
		// Try processing current hash chain
		if ratio, foundAll := rh.processCurrentHashChain(hashChain, currentBlocksCount, availableInstances, matchedResults, promptTokenCount); foundAll {
			// Update max ratio
			if ratio > maxRatio {
				maxRatio = ratio
			}
			// Early exit: all available instances found
			break
		} else {
			// Update max ratio
			if ratio > maxRatio {
				maxRatio = ratio
			}
		}

		// Move to next hash chain
		currentBlocksCount--
	}
	return maxRatio
}

// shouldContinueProcessing determines whether processing should continue
func (rh *RollingHashStrategy) shouldContinueProcessing(currentBlocksCount int,
	matchedResults map[string]float64, availableInstances map[string]InstanceLoad) bool {

	// Format check: at least one hash block required
	if currentBlocksCount <= 0 {
		return false
	}

	// Early exit: all available instances found
	return len(matchedResults) < len(availableInstances)
}

// processCurrentHashChain processes the current hash chain - extracts main logic branches
func (rh *RollingHashStrategy) processCurrentHashChain(hashChain []string, currentBlocksCount int,
	availableInstances map[string]InstanceLoad, matchedResults map[string]float64, promptTokenCount int32) (float64, bool) {

	hash := hashChain[0] + ":" + hashChain[currentBlocksCount]

	// Get cache entry and refresh TTL
	ttl := time.Duration(rh.memConfig.CacheTTLSec) * time.Second
	foundValue, found := rh.promptKeyCache.GetAndRefreshTTL(hash, ttl)
	if !found {
		return 0.0, false
	}

	var cacheEntry PromptKeyCacheEntry
	if err := rh.DecodeFromCache(foundValue, &cacheEntry); err != nil {
		return 0.0, false
	}

	// Process instance matching, pass full hashChain to get exact length info
	ratio := rh.updateMatchedInstances(&cacheEntry, currentBlocksCount, availableInstances, matchedResults, promptTokenCount, hash, hashChain)

	// Check if all instances were found
	if len(matchedResults) == len(availableInstances) {
		return ratio, true
	}

	return ratio, false
}

// updateMatchedInstances updates the matched instances
// Returns the calculated match ratio
func (rh *RollingHashStrategy) updateMatchedInstances(cacheEntry *PromptKeyCacheEntry,
	currentBlocksCount int, availableInstances map[string]InstanceLoad, matchedResults map[string]float64, promptTokenCount int32, hash string, hashChain []string) float64 {

	var matchingRatio float64
	// Try using exact match ratio calculation
	if exactRatio := rh.calculateExactMatchingRatioFromCacheChain(currentBlocksCount, promptTokenCount, hashChain); exactRatio > 0 {
		matchingRatio = exactRatio
	} else if rh.progressiveBlockEnable {
		matchingRatio = rh.calculateProgressiveMatchingRatio(currentBlocksCount, promptTokenCount)
	} else {
		matchingRatio = rh.calculateFixedBlockMatchingRatio(currentBlocksCount, promptTokenCount)
	}

	// First copy all instance IDs to a temporary slice to avoid concurrent map iteration and modification
	instanceIDs := make([]string, 0, len(cacheEntry.InstanceMap))
	for instanceID := range cacheEntry.InstanceMap {
		instanceIDs = append(instanceIDs, instanceID)
	}

	// Lazy cleanup: check if instance is in the to-remove list and clean up
	cleaned := false
	for _, instanceID := range instanceIDs {
		if _, exists := availableInstances[instanceID]; exists {
			// Available instance, check if match ratio meets threshold requirement
			if rh.matchThreshold == 0.0 || matchingRatio >= rh.matchThreshold {
				// Meets threshold requirement, add to results
				if _, exists := matchedResults[instanceID]; !exists {
					matchedResults[instanceID] = matchingRatio
					// Already available, ignore even if in toRemove (was re-added later)
					continue
				}
			}
		}

		// Check if pending removal; if so, remove and continue to next, do not add to matchedResults
		if _, inRemoveList := rh.toRemoveInstances[instanceID]; inRemoveList {
			// In the to-remove list, clean it up
			delete(cacheEntry.InstanceMap, instanceID)
			cleaned = true
			continue
		}
	}

	// If cleanup was performed, update the cache entry
	if cleaned {
		ttl := time.Duration(rh.memConfig.CacheTTLSec) * time.Second
		if err := rh.promptKeyCache.Set(hash, &cacheEntry, ttl); err != nil {
			logger.Log().Warnf("Failed to update cache entry after cleaning: %v", err)
		}
	}
	return matchingRatio
}

// calculateProgressiveMatchingRatio calculates the match ratio for progressive block strategy
func (rh *RollingHashStrategy) calculateProgressiveMatchingRatio(currentBlocksCount int, promptTokenCount int32) float64 {
	totalPromptBytes := int(promptTokenCount)

	// Calculate total bytes for the first currentBlocksCount blocks (exponential growth)
	matchedBytes := 0
	currentBlockSize := rh.progressiveBaseBlockSizeBytes

	for i := 0; i < currentBlocksCount; i++ {
		matchedBytes += currentBlockSize
		currentBlockSize *= 2
	}

	// Calculate match ratio
	var matchingRatio float64
	if totalPromptBytes > 0 {
		matchingRatio = float64(matchedBytes) / float64(totalPromptBytes)
		// Handle case where ratio might exceed 1.0
		if matchingRatio > 1.0 {
			matchingRatio = 1.0
		}
	}

	return matchingRatio
}

// calculateFixedBlockMatchingRatio calculates the match ratio for fixed block strategy
func (rh *RollingHashStrategy) calculateFixedBlockMatchingRatio(currentBlocksCount int, promptTokenCount int32) float64 {
	// Direct calculation of match ratio: currentBlocks * blockSize / promptTokenCount
	totalPromptBytes := int(promptTokenCount)

	// Calculate match ratio
	var matchingRatio float64
	if totalPromptBytes > 0 {
		matchingRatio = float64(currentBlocksCount*rh.blockSize) / float64(totalPromptBytes)
		// Handle case where ratio might exceed 1.0 (last block may be partial)
		if matchingRatio > 1.0 {
			matchingRatio = 1.0
		}
	}

	return matchingRatio
}

// extractLastBlockSize extracts the exact size of the last block from the hash chain
func (rh *RollingHashStrategy) extractLastBlockSize(hashChain []string) int64 {
	if len(hashChain) == 0 {
		return 0
	}

	// The last element stores the exact size of the last block, format: "e_<size>"
	lastElement := hashChain[len(hashChain)-1]
	if !strings.HasPrefix(lastElement, "e_") {
		// Compatible with old format, no exact length info
		return 0
	}

	var size int64
	_, err := fmt.Sscanf(lastElement, "e_%d", &size)
	if err != nil {
		return 0
	}

	return size
}

// calculateExactMatchingRatio calculates the exact match ratio
func (rh *RollingHashStrategy) calculateExactMatchingRatio(currentBlocksCount int, promptTokenCount int32, hashChain []string) float64 {
	totalPromptBytes := int(promptTokenCount)
	if totalPromptBytes == 0 {
		return 0.0
	}

	// Get the exact size of the last block
	lastBlockSize := rh.extractLastBlockSize(hashChain)

	// Calculate exact matched bytes
	var matchedBytes int

	if rh.progressiveBlockEnable {
		// Progressive block strategy
		// Total bytes of the first (currentBlocksCount-1) complete blocks
		fullBlocks := currentBlocksCount - 1
		currentBlockSize := rh.progressiveBaseBlockSizeBytes
		for i := 0; i < fullBlocks; i++ {
			matchedBytes += currentBlockSize
			currentBlockSize *= 2
		}

		// If there is a last block, use exact size
		if currentBlocksCount > 0 {
			matchedBytes += int(lastBlockSize)
		}
	} else {
		// Fixed block strategy
		fullBlocks := currentBlocksCount - 1
		matchedBytes = fullBlocks * rh.blockSize

		// If there is a last block, use exact size
		if currentBlocksCount > 0 {
			matchedBytes += int(lastBlockSize)
		}
	}

	// Calculate match ratio
	matchingRatio := float64(matchedBytes) / float64(totalPromptBytes)

	// Ensure it does not exceed 1.0
	if matchingRatio > 1.0 {
		matchingRatio = 1.0
	}

	return matchingRatio
}

// calculateExactMatchingRatioFromCacheChain calculates the exact match ratio from cache key hash chain info
func (rh *RollingHashStrategy) calculateExactMatchingRatioFromCacheChain(currentBlocksCount int, promptTokenCount int32, hashChain []string) float64 {
	// Check if exact length info is present
	if len(hashChain) == 0 || !strings.HasPrefix(hashChain[len(hashChain)-1], "e_") {
		return 0.0 // No exact length info, return 0 to use the old estimation method
	}

	// Use exact match ratio calculation
	return rh.calculateExactMatchingRatio(currentBlocksCount, promptTokenCount, hashChain)
}

// UpdateCache updates the model:promptKey->[]instanceId mapping
func (rh *RollingHashStrategy) UpdateCache(promptCacheKey string, instanceID string) error {
	rh.mu.Lock()
	defer rh.mu.Unlock()

	if len(promptCacheKey) == 0 {
		return fmt.Errorf("empty prompt cache key")
	}

	// Only update forward cache
	if err := rh.updatePromptKeyCache(promptCacheKey, instanceID); err != nil {
		return fmt.Errorf("failed to update cache for %v", err)
	}

	logger.Log().Debugf("prompt cache key %v updated to cache success for instance id %v", promptCacheKey, instanceID)
	return nil
}

// updatePromptKeyCache internal method: model:promptKey -> []instanceId
func (rh *RollingHashStrategy) updatePromptKeyCache(promptCacheKey string, instanceID string) error {
	if len(promptCacheKey) == 0 {
		return fmt.Errorf("empty prompt cache key")
	}

	hashChain := strings.Split(promptCacheKey, ":")

	// Check if exact length info is present
	hasExactLength := len(hashChain) > 1 && strings.HasPrefix(hashChain[len(hashChain)-1], "e_")

	// Adjust loop range to skip length info
	numHashes := len(hashChain) - 1
	if hasExactLength {
		numHashes--
	}

	for i := numHashes; i >= 1; i-- {
		hash := hashChain[0] + ":" + hashChain[i]
		// Try to get from existing cache
		var instanceMap map[string]struct{}
		if value, found := rh.promptKeyCache.Get(hash); found {
			var cacheEntry PromptKeyCacheEntry
			if err := rh.DecodeFromCache(value, &cacheEntry); err == nil {
				instanceMap = cacheEntry.InstanceMap
			}
		}

		// If instance doesn't exist, create or update map
		if instanceMap == nil {
			instanceMap = make(map[string]struct{})
		}

		// Add new instance to map (O(1) operation)
		instanceMap[instanceID] = struct{}{}

		// Create new cache entry
		newEntry := &PromptKeyCacheEntry{
			InstanceMap: instanceMap,
		}

		// Store to Ristretto cache
		ttl := time.Duration(rh.memConfig.CacheTTLSec) * time.Second
		if err := rh.promptKeyCache.Set(hash, newEntry, ttl); err != nil {
			return fmt.Errorf("failed to set cache entry: %v", err)
		}
	}

	return nil
}

// UpdateToRemoveInstances updates the list of instances to be removed
func (rh *RollingHashStrategy) UpdateToRemoveInstances(toRemove map[string]struct{}) error {
	rh.mu.Lock()
	defer rh.mu.Unlock()

	// Directly replace the entire map
	for instanceID := range toRemove {
		rh.toRemoveInstances[instanceID] = struct{}{}
	}

	logger.Log().Infof("Updated toRemove instances list with %d instances", len(rh.toRemoveInstances))
	return nil
}

// CleanUp clears all caches
func (rh *RollingHashStrategy) CleanUp() {
	var err error

	// Clean up promptKeyCache
	if rh.promptKeyCache != nil {
		if err = rh.promptKeyCache.Clear(); err != nil {
			err = fmt.Errorf("promptKeyCache clear failed: %v", err)
		}
	}

	// Clean up to-remove instance list
	rh.toRemoveInstances = make(map[string]struct{})

	if err != nil {
		logger.Log().Errorf("RollingHash cache cleanup completed error: %v", err)
	} else {
		logger.Log().Infof("RollingHash cache cleaned up completely")
	}
}

// ComputePromptKey returns the strategy's prompt key (hash chain string)
func (rh *RollingHashStrategy) ComputePromptKey(prompt string) (interface{}, error) {
	hashChain := rh.computeHashChain(prompt)
	if len(hashChain) == 0 {
		return nil, fmt.Errorf("failed to compute hash chain")
	}
	logger.Log().Tracef("hash chain length %v", len(hashChain))
	// Return []string type directly, allowing callers to use the original data structure
	return hashChain, nil
}

// computeHashChain computes the rolling hash chain for the prompt
// Selects fixed block strategy or progressive block strategy based on configuration
func (rh *RollingHashStrategy) computeHashChain(prompt string) []string {
	if rh.progressiveBlockEnable {
		return rh.computeHashChainWithProgressiveBlocks(prompt)
	}

	return rh.computeHashChainWithFixedBlocks(prompt)
}

// computeHashChainWithFixedBlocks computes the fixed block size hash chain for the prompt
// Uses byte-level chunking, similar to kthena's implementation
// The last element of the returned slice is the exact size of the last block
func (rh *RollingHashStrategy) computeHashChainWithFixedBlocks(prompt string) []string {
	if rh.blockSize <= 0 {
		return nil
	}

	// Generate a hash even if prompt length is smaller than base block size
	if len(prompt) == 0 {
		return nil
	}

	// Generate hash chain based on byte length
	var hashChain []string
	blockCount := 0
	blockStart := 0
	var lastBlockSize int // Record the size of the last block

	// Generate rolling hash chain using byte-level chunking
	for blockCount < rh.maxBlocks && blockStart < len(prompt) {
		blockEnd := blockStart + rh.blockSize
		if blockEnd > len(prompt) {
			blockEnd = len(prompt)
		}

		// Get current block
		block := prompt[blockStart:blockEnd]

		// Compute block hash
		if len(hashChain) != 0 {
			block = hashChain[len(hashChain)-1] + "-" + block
		}
		hash := rh.calculateBlockHash(block)
		hashChain = append(hashChain, fmt.Sprintf("h_%s", hash))

		// Record last block size
		lastBlockSize = blockEnd - blockStart

		// Move to next block
		blockStart = blockEnd
		blockCount++

		// Exit loop if end of prompt reached
		if blockEnd == len(prompt) {
			break
		}
	}

	// Append exact size of the last block
	if len(hashChain) > 0 {
		hashChain = append(hashChain, fmt.Sprintf("e_%d", lastBlockSize))
	}

	return hashChain
}

// computeHashChainWithProgressiveBlocks computes the progressive block size hash chain for the prompt
// Block size grows exponentially: base -> base*2 -> base*4 -> base*8...
// Generates a hash even if the prompt is smaller than the base block size
// The last element of the returned slice is the exact size of the last block
func (rh *RollingHashStrategy) computeHashChainWithProgressiveBlocks(prompt string) []string {
	if rh.progressiveBaseBlockSizeBytes <= 0 {
		return nil
	}

	// Generate a hash even if the prompt is smaller than the base block size
	if len(prompt) == 0 {
		return nil
	}

	var hashChain []string
	blockCount := 0
	blockStart := 0
	currentBlockSize := rh.progressiveBaseBlockSizeBytes
	var lastBlockSize int // Record last block size

	// Generate progressive block size hash chain
	for blockCount < rh.maxBlocks && blockStart < len(prompt) {
		// Calculate end position of current block
		blockEnd := blockStart + currentBlockSize
		if blockEnd > len(prompt) {
			blockEnd = len(prompt)
		}

		// Get current block
		block := prompt[blockStart:blockEnd]

		// Compute block hash
		if len(hashChain) != 0 {
			block = hashChain[len(hashChain)-1] + "-" + block
		}
		hash := rh.calculateBlockHash(block)
		hashChain = append(hashChain, fmt.Sprintf("h_%s", hash))

		// Record last block size
		lastBlockSize = blockEnd - blockStart

		// Move to next block
		blockStart = blockEnd
		blockCount++

		// Double block size (exponential growth)
		currentBlockSize *= 2

		// Exit loop if end of prompt reached
		if blockEnd == len(prompt) {
			break
		}
	}

	// Append exact size of the last block
	if len(hashChain) > 0 {
		hashChain = append(hashChain, fmt.Sprintf("e_%d", lastBlockSize))
	}

	return hashChain
}

// calculateBlockHash computes the hash of a block
func (rh *RollingHashStrategy) calculateBlockHash(block string) string {
	// Use xxhash.Sum64 for high performance hashing
	hash := xxhash.Sum64String(block)
	return fmt.Sprintf("%016x", hash)
}

// GenerateCacheKey generates a cache key for transmission between worker and leader
func (rh *RollingHashStrategy) GenerateCacheKey(model string, promptKey interface{}) string {
	hashChain, ok := promptKey.([]string)
	if !ok {
		return ""
	}

	if len(hashChain) == 0 {
		return model
	}

	// Use strings.Builder for better concatenation performance
	var builder strings.Builder
	builder.WriteString(model)
	for _, hash := range hashChain {
		builder.WriteByte(':')
		builder.WriteString(hash)
	}
	return builder.String()
}
