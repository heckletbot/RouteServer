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

import "time"

// LocalCache is the local cache interface
// Supports multiple implementations: Ristretto, FreeCache, etc.
type LocalCache interface {
	// Set sets a cache value
	Set(key string, value interface{}, ttl time.Duration) error

	// Get gets a cache value
	// Returns the value and a flag indicating whether it was found
	Get(key string) (interface{}, bool)

	// GetAndRefreshTTL gets a cache value and refreshes its TTL
	// If found, the TTL will be reset
	GetAndRefreshTTL(key string, ttl time.Duration) (interface{}, bool)

	// Delete deletes a cache value
	Delete(key string) error

	// Clear clears all cache
	Clear() error

	// Size gets the cache size
	Size() int64

	// Close closes the cache and releases resources
	Close() error
}
