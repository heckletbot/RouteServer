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
	"testing"
	"time"

	"route-server/pkg/common/config"
)

func TestNewRistrettoCache(t *testing.T) {
	testConfig := config.APCStrategyMemoryConfig{
		MaxEntries:  1000,
		MaxMemoryMB: 100,
	}

	t.Run("successful creation", func(t *testing.T) {
		cache, err := NewRistrettoCache(testConfig)
		if err != nil {
			t.Fatalf("Expected no error, got %v", err)
		}
		if cache == nil {
			t.Fatal("Expected cache to be non-nil")
		}
		defer cache.Close()
	})

	t.Run("creation failure", func(t *testing.T) {
		// Zero MaxEntries produces NumCounters=0, which makes ristretto.NewCache
		// return an error (gomonkey cannot patch generic instantiated functions)
		badConfig := config.APCStrategyMemoryConfig{
			MaxEntries:  0,
			MaxMemoryMB: 100,
		}

		cache, err := NewRistrettoCache(badConfig)
		if err == nil {
			t.Fatal("Expected error, got nil")
		}
		if cache != nil {
			t.Fatal("Expected cache to be nil")
		}
		if !strings.Contains(err.Error(), "NumCounters can't be zero") {
			t.Fatalf("Expected specific error message, got %v", err)
		}
	})
}

func TestRistrettoCache_SetAndGet(t *testing.T) {
	testConfig := config.APCStrategyMemoryConfig{
		MaxEntries:  1000,
		MaxMemoryMB: 100,
	}

	cache, err := NewRistrettoCache(testConfig)
	if err != nil {
		t.Fatalf("Failed to create cache: %v", err)
	}
	defer cache.Close()

	t.Run("set and get string", func(t *testing.T) {
		key := "test-string"
		value := "hello world"

		err := cache.Set(key, value, time.Minute)
		if err != nil {
			t.Fatalf("Failed to set value: %v", err)
		}

		retrieved, found := cache.Get(key)
		if !found {
			t.Fatal("Expected to find value")
		}
		if retrieved != value {
			t.Fatalf("Expected %v, got %v", value, retrieved)
		}

		// Test storing and retrieving struct
		testEntry := &PromptKeyCacheEntry{
			InstanceMap: map[string]struct{}{"instance1": {}, "instance2": {}},
		}

		err = cache.Set("test-entry", testEntry, time.Minute*10)
		if err != nil {
			t.Fatalf("cache set failed: %v", err)
		}

		v, found := cache.Get("test-entry")
		if !found {
			t.Fatalf("cache get failed")
		}

		// Verify returned type
		if entry, ok := v.(*PromptKeyCacheEntry); ok {
			if len(entry.InstanceMap) != 2 {
				t.Fatalf("expected 2 instances, got %d", len(entry.InstanceMap))
			}
			t.Logf("retrieved entry: %+v", entry)
		} else {
			t.Fatalf("expected *PromptKeyCacheEntry, got %T", v)
		}
	})

	t.Run("get non-existent key", func(t *testing.T) {
		value, found := cache.Get("non-existent")
		if found {
			t.Fatal("Expected not to find value")
		}
		if value != nil {
			t.Fatalf("Expected nil value, got %v", value)
		}
	})

}

func TestRistrettoCache_Delete(t *testing.T) {
	testConfig := config.APCStrategyMemoryConfig{
		MaxEntries:  1000,
		MaxMemoryMB: 100,
	}

	cache, err := NewRistrettoCache(testConfig)
	if err != nil {
		t.Fatalf("Failed to create cache: %v", err)
	}
	defer cache.Close()

	t.Run("delete existing key", func(t *testing.T) {
		key := "delete-test"
		value := "delete me"

		// Set a value
		err := cache.Set(key, value, time.Minute)
		if err != nil {
			t.Fatalf("Failed to set value: %v", err)
		}

		// Verify it exists
		retrieved, found := cache.Get(key)
		if !found || retrieved != value {
			t.Fatal("Value should exist before deletion")
		}

		// Delete it
		err = cache.Delete(key)
		if err != nil {
			t.Fatalf("Failed to delete value: %v", err)
		}

		// Verify it's gone
		_, found = cache.Get(key)
		if found {
			t.Fatal("Value should not exist after deletion")
		}
	})

	t.Run("delete non-existent key", func(t *testing.T) {
		err := cache.Delete("non-existent-delete")
		if err != nil {
			t.Fatalf("Expected no error deleting non-existent key, got %v", err)
		}
	})
}

func TestRistrettoCache_Clear(t *testing.T) {
	testConfig := config.APCStrategyMemoryConfig{
		MaxEntries:  1000,
		MaxMemoryMB: 100,
	}

	cache, err := NewRistrettoCache(testConfig)
	if err != nil {
		t.Fatalf("Failed to create cache: %v", err)
	}
	defer cache.Close()

	// Add some values
	for i := 0; i < 5; i++ {
		key := fmt.Sprintf("clear-test-%d", i)
		value := fmt.Sprintf("value-%d", i)
		err := cache.Set(key, value, time.Minute)
		if err != nil {
			t.Fatalf("Failed to set value %d: %v", i, err)
		}
	}

	// Verify they exist
	for i := 0; i < 5; i++ {
		key := fmt.Sprintf("clear-test-%d", i)
		_, found := cache.Get(key)
		if !found {
			t.Fatalf("Value %d should exist before clear", i)
		}
	}

	// Clear cache
	err = cache.Clear()
	if err != nil {
		t.Fatalf("Failed to clear cache: %v", err)
	}

	// Verify they're gone
	for i := 0; i < 5; i++ {
		key := fmt.Sprintf("clear-test-%d", i)
		_, found := cache.Get(key)
		if found {
			t.Fatalf("Value %d should not exist after clear", i)
		}
	}
}

func TestRistrettoCache_Size(t *testing.T) {
	testConfig := config.APCStrategyMemoryConfig{
		MaxEntries:  1000,
		MaxMemoryMB: 100,
	}

	cache, err := NewRistrettoCache(testConfig)
	if err != nil {
		t.Fatalf("Failed to create cache: %v", err)
	}
	defer cache.Close()

	// Initial size should be >= 0
	size := cache.Size()
	if size < 0 {
		t.Fatalf("Expected size >= 0, got %d", size)
	}

	// Add some values
	for i := 0; i < 3; i++ {
		key := fmt.Sprintf("size-test-%d", i)
		value := fmt.Sprintf("value-%d", i)
		err := cache.Set(key, value, time.Minute)
		if err != nil {
			t.Fatalf("Failed to set value %d: %v", i, err)
		}
	}

	// KeysAdded should increase
	newSize := cache.Size()
	if newSize < size {
		t.Fatalf("Expected size to increase or stay the same, got old=%d, new=%d", size, newSize)
	}
}

func TestRistrettoCache_getMetrics(t *testing.T) {
	testConfig := config.APCStrategyMemoryConfig{
		MaxEntries:  1000,
		MaxMemoryMB: 100,
	}

	cache, err := NewRistrettoCache(testConfig)
	if err != nil {
		t.Fatalf("Failed to create cache: %v", err)
	}
	defer cache.Close()

	// Get initial metrics
	metrics := cache.getMetrics()
	if metrics == nil {
		t.Fatal("Expected metrics to be non-nil")
	}
	if metrics.Hits < 0 || metrics.Misses < 0 {
		t.Fatalf("Expected non-negative hits and misses, got hits=%d, misses=%d", metrics.Hits, metrics.Misses)
	}
	if metrics.Ratio < 0 {
		t.Fatalf("Expected non-negative ratio, got %f", metrics.Ratio)
	}

	// Add some values and access them
	for i := 0; i < 3; i++ {
		key := fmt.Sprintf("metrics-test-%d", i)
		value := fmt.Sprintf("value-%d", i)
		err := cache.Set(key, value, time.Minute)
		if err != nil {
			t.Fatalf("Failed to set value %d: %v", i, err)
		}
	}

	// Get some hits
	for i := 0; i < 3; i++ {
		key := fmt.Sprintf("metrics-test-%d", i)
		cache.Get(key)
	}

	// Get some misses
	cache.Get("non-existent-1")
	cache.Get("non-existent-2")

	// Check updated metrics
	newMetrics := cache.getMetrics()
	if newMetrics == nil {
		t.Fatal("Expected metrics to be non-nil")
	}
	// Hits and misses should be >= initial values
	if newMetrics.Hits < metrics.Hits {
		t.Fatalf("Expected hits to increase, got old=%d, new=%d", metrics.Hits, newMetrics.Hits)
	}
	if newMetrics.Misses < metrics.Misses {
		t.Fatalf("Expected misses to increase, got old=%d, new=%d", metrics.Misses, newMetrics.Misses)
	}
}

func TestRistrettoCache_GetHitRate(t *testing.T) {
	testConfig := config.APCStrategyMemoryConfig{
		MaxEntries:  1000,
		MaxMemoryMB: 100,
	}

	cache, err := NewRistrettoCache(testConfig)
	if err != nil {
		t.Fatalf("Failed to create cache: %v", err)
	}
	defer cache.Close()

	// Add some values
	err = cache.Set("hit-test-1", "value1", time.Minute)
	if err != nil {
		t.Fatalf("Failed to set value: %v", err)
	}

	// Get a hit
	cache.Get("hit-test-1")

	// Get a miss
	cache.Get("non-existent")

}

func TestRistrettoCache_GetMemoryUsage(t *testing.T) {
	testConfig := config.APCStrategyMemoryConfig{
		MaxEntries:  1000,
		MaxMemoryMB: 100,
	}

	cache, err := NewRistrettoCache(testConfig)
	if err != nil {
		t.Fatalf("Failed to create cache: %v", err)
	}
	defer cache.Close()

	// Add some values
	err = cache.Set("memory-test-1", "a longer string to test memory calculation", time.Minute)
	if err != nil {
		t.Fatalf("Failed to set value: %v", err)
	}

	err = cache.Set("memory-test-2", map[string]interface{}{"test": "value", "number": 123}, time.Minute)
	if err != nil {
		t.Fatalf("Failed to set value: %v", err)
	}

}

func TestRistrettoCache_Health(t *testing.T) {
	testConfig := config.APCStrategyMemoryConfig{
		MaxEntries:  1000,
		MaxMemoryMB: 100,
	}

	cache, err := NewRistrettoCache(testConfig)
	if err != nil {
		t.Fatalf("Failed to create cache: %v", err)
	}
	defer cache.Close()
}

func TestRistrettoCache_Concurrency(t *testing.T) {
	testConfig := config.APCStrategyMemoryConfig{
		MaxEntries:  1000,
		MaxMemoryMB: 100,
	}

	cache, err := NewRistrettoCache(testConfig)
	if err != nil {
		t.Fatalf("Failed to create cache: %v", err)
	}
	defer cache.Close()

	var wg sync.WaitGroup
	numGoroutines := 10
	numOperations := 50

	// Concurrent Set operations
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < numOperations; j++ {
				key := fmt.Sprintf("key-%d-%d", id, j)
				value := fmt.Sprintf("value-%d-%d", id, j)
				err := cache.Set(key, value, time.Minute)
				if err != nil {
					t.Errorf("Failed to set value %s: %v", key, err)
				}
			}
		}(i)
	}

	// Concurrent Get operations
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < numOperations; j++ {
				key := fmt.Sprintf("key-%d-%d", id, j)
				cache.Get(key) // Ignore return values, just test for race conditions
			}
		}(i)
	}

	// Concurrent metrics access
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < numOperations; j++ {
				cache.getMetrics()

				cache.Size()

			}
		}()
	}

	wg.Wait()
}

func TestRistrettoCacheGetAndRefreshTTL(t *testing.T) {
	// Create a cache config with short TTL
	memConfig := config.APCStrategyMemoryConfig{
		MaxEntries:  1000,
		MaxMemoryMB: 100,
		CacheTTLSec: 2, // 2 second TTL for testing
	}

	cache, err := NewRistrettoCache(memConfig)
	if err != nil {
		t.Fatalf("Failed to create cache: %v", err)
	}
	defer cache.Close()

	// Set test data
	testKey := "test-key"
	testValue := "test-value"
	ttl := 3 * time.Second

	err = cache.Set(testKey, testValue, ttl)
	if err != nil {
		t.Fatalf("Failed to set cache: %v", err)
	}

	time.Sleep(2 * time.Second)

	// Get and refresh TTL
	value, found := cache.GetAndRefreshTTL(testKey, ttl)
	if !found {
		t.Error("Expected to find value in cache")
	}
	if value != testValue {
		t.Errorf("Expected %v, got %v", testValue, value)
	}

	// Wait for original TTL to expire, then check again
	time.Sleep(2 * time.Second)

	// TTL was refreshed, so the value should still be found
	value, found = cache.Get(testKey)
	if !found {
		t.Error("Expected to still find value after TTL refresh")
	}
	if value != testValue {
		t.Errorf("Expected %v, got %v", testValue, value)
	}

	// Wait for the refreshed TTL to expire
	time.Sleep(2*time.Second + 100*time.Millisecond) // Extra 100ms to ensure expiry

	// Should not be found now
	_, found = cache.Get(testKey)
	if found {
		t.Error("Expected value to be expired after TTL")
	}
}

func TestRistrettoCacheGetWithoutRefresh(t *testing.T) {
	// Create a cache config with short TTL
	memConfig := config.APCStrategyMemoryConfig{
		MaxEntries:  1000,
		MaxMemoryMB: 100,
		CacheTTLSec: 2, // 2 second TTL
	}

	cache, err := NewRistrettoCache(memConfig)
	if err != nil {
		t.Fatalf("Failed to create cache: %v", err)
	}
	defer cache.Close()

	// Set test data
	testKey := "test-key-no-refresh"
	testValue := "test-value-no-refresh"
	ttl := 2 * time.Second

	err = cache.Set(testKey, testValue, ttl)
	if err != nil {
		t.Fatalf("Failed to set cache: %v", err)
	}

	// Use regular Get method (no TTL refresh)
	value, found := cache.Get(testKey)
	if !found {
		t.Error("Expected to find value in cache")
	}
	if value != testValue {
		t.Errorf("Expected %v, got %v", testValue, value)
	}

	// Wait for TTL to expire
	time.Sleep(2*time.Second + 100*time.Millisecond) // Extra 100ms to ensure expiry

	// Since TTL was not refreshed, should not be found
	_, found = cache.Get(testKey)
	if found {
		t.Error("Expected value to be expired after TTL without refresh")
	}
}
