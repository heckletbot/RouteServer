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

package apc

import (
	"fmt"
	"time"
	"unsafe"

	"github.com/dgraph-io/ristretto"
	"infer-dp-scheduler/pkg/common/config"
	"infer-dp-scheduler/pkg/common/logger"
)

// RistrettoCache is the Ristretto cache implementation
type RistrettoCache struct {
	cache *ristretto.Cache
}

// RistrettoStats holds Ristretto statistics
type RistrettoStats struct {
	Hits      int64
	Misses    int64
	Ratio     float64
	KeysAdded int64
	CostAdded int64
}

// NewRistrettoCache creates a Ristretto cache instance
func NewRistrettoCache(config config.APCStrategyMemoryConfig) (*RistrettoCache, error) {
	// Create Ristretto config
	ristrettoConfig := &ristretto.Config{
		NumCounters: 10 * config.MaxEntries,           // Default 10x
		MaxCost:     config.MaxMemoryMB * 1024 * 1024, // Convert to bytes
		BufferItems: 64,                               // Default value
		Metrics:     config.MetricsEnable,
	}

	// Create cache instance
	cache, err := ristretto.NewCache(ristrettoConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create ristretto cache: %v", err)
	}

	return &RistrettoCache{
		cache: cache,
	}, nil
}

// Set sets a cache value - optimized version: stores objects directly, avoiding JSON serialization
func (rc *RistrettoCache) Set(key string, value interface{}, ttl time.Duration) error {
	// Estimate object memory overhead as cost
	cost := rc.estimateObjectCost(value)

	// Store object directly to Ristretto to avoid JSON serialization overhead
	success := rc.cache.SetWithTTL(key, value, cost, ttl)
	if !success {
		return fmt.Errorf("failed to set key %s in ristretto cache", key)
	}

	logger.Log().Debugf("Ristretto cache stats: %+v", rc.getMetrics())
	return nil
}

// estimateObjectCost estimates the memory overhead of an object as cost
// Performs accurate estimation based on actual data structures and formats
func (rc *RistrettoCache) estimateObjectCost(value interface{}) int64 {
	switch v := value.(type) {
	case *PromptKeyCacheEntry:
		// Use unsafe.Sizeof to calculate struct size
		cost := int64(unsafe.Sizeof(PromptKeyCacheEntry{}))
		// Add map overhead
		if v != nil && v.InstanceMap != nil {
			// Map header size (approximate)
			cost += 24
			// Overhead per instanceId (key storage and map entry overhead)
			// instanceId is typically a 10-20 byte string, plus map entry pointer overhead
			cost += int64(len(v.InstanceMap) * (32 + 16)) // 32 bytes for string, 16 bytes for map entry
		}
		return cost
	case map[string]struct{}:
		// Use unsafe.Sizeof to calculate map structure
		cost := int64(24) // Map header size
		// Average overhead per key
		for key := range v {
			// Actual string length + string header overhead
			cost += int64(len(key) + 16) // 16 bytes string header overhead
		}
		return cost
	case string:
		// String length + string header overhead
		return int64(len(v) + 16)
	default:
		// Use unsafe.Sizeof to calculate default overhead
		cost := int64(unsafe.Sizeof(value))
		// If it's an empty interface with actual data, estimate a base overhead
		if cost == 16 { // interface{} pointer size
			cost = 64 // Estimated average overhead for a stored object
		}
		return cost
	}
}

// Get gets a cache value - optimized version: returns objects directly, avoiding JSON deserialization
func (rc *RistrettoCache) Get(key string) (interface{}, bool) {
	rc.cache.Wait()
	value, found := rc.cache.Get(key)
	if !found {
		return nil, false
	}

	// Return the original object directly, no deserialization needed
	return value, true
}

// GetAndRefreshTTL gets a cache value and refreshes its TTL
func (rc *RistrettoCache) GetAndRefreshTTL(key string, ttl time.Duration) (interface{}, bool) {
	value, found := rc.cache.Get(key)
	if !found {
		return nil, false
	}

	// Refresh TTL: re-set the same value with new TTL
	cost := rc.estimateObjectCost(value)
	success := rc.cache.SetWithTTL(key, value, cost, ttl)
	if !success {
		logger.Log().Errorf("Ristretto get and refresh ttl failed.")
	}

	// Return the original object directly, no deserialization needed
	return value, true
}

// Delete deletes a cache value
func (rc *RistrettoCache) Delete(key string) error {
	rc.cache.Del(key)
	return nil
}

// Clear clears all cache
func (rc *RistrettoCache) Clear() error {
	rc.cache.Clear()
	return nil
}

// Size returns the cache size
func (rc *RistrettoCache) Size() int64 {
	return int64(rc.cache.Metrics.KeysAdded())
}

// Close closes the cache
func (rc *RistrettoCache) Close() error {
	rc.cache.Close()
	return nil
}

// getMetrics returns cache statistics
func (rc *RistrettoCache) getMetrics() *RistrettoStats {
	metrics := rc.cache.Metrics

	// Return real-time statistics directly
	stats := &RistrettoStats{
		Hits:      int64(metrics.Hits()),
		Misses:    int64(metrics.Misses()),
		Ratio:     metrics.Ratio(),
		KeysAdded: int64(metrics.KeysAdded()),
		CostAdded: int64(metrics.CostAdded()), // Fix: use metrics instead of rc.cache.Metrics
	}

	return stats
}
