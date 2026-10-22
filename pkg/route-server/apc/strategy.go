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

import "infer-dp-scheduler/pkg/common/utils"

// MatchResult represents the matching result
type MatchResult struct {
	InstanceID string
	Score      int64
	Strategy   string
}

type InstanceLoad struct {
	RequestsTotalCnt uint64
	PromptTotalLen   uint64
}

// PromptKeyCacheEntry is the PromptKey cache entry, supporting multi-instance load balancing
type PromptKeyCacheEntry struct {
	InstanceMap map[string]struct{} // Instance ID map for fast lookup, insertion, deletion O(1)
}

// Strategy is the core strategy interface
// Responsible for the core business logic of prompt key computation and cache management
type Strategy interface {
	// Name returns the strategy name
	Name() string

	// DecodeFromCache parses data from cache
	DecodeFromCache(cacheValue interface{}, target interface{}) error

	// ComputePromptKey computes the prompt key
	// Converts the raw prompt into a key format recognizable by the strategy
	ComputePromptKey(prompt string) (interface{}, error)

	// GenerateCacheKey generates a cache key for local cache
	GenerateCacheKey(model string, promptKey interface{}) string

	// UpdateCache updates the model->promptKey->[]instanceId mapping
	UpdateCache(promptCacheKey string, instanceId string) error

	// UpdateToRemoveInstances updates the list of instances to be removed, for lazy cleanup strategy
	UpdateToRemoveInstances(toRemove map[string]struct{}) error

	// GetMatchedInstances gets matched instance candidates (only responsible for cache lookup and matching)
	// Based on request info (including promptCacheKey, etc.) and the list of available instances,
	// returns all matching instances with their match ratios, and the maximum match ratio
	GetMatchedInstances(request utils.GenerateRequest, availableInstances map[string]InstanceLoad) (map[string]float64, float64, error)

	CleanUp()
}
