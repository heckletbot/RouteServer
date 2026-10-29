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

package middleware

import (
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"infer-dp-scheduler/pkg/common/config"
	"infer-dp-scheduler/pkg/common/constant"
)

var (
	// Global rate limiter instance
	globalRateLimiter *RateLimiter
)

// APILimiter represents a token bucket rate limiter for a specific API
type APILimiter struct {
	tokens      float64   // Current number of tokens in the bucket
	lastChanged time.Time // Last time tokens were refilled
	burstSize   int       // Maximum tokens the bucket can hold
}

// RateLimiter manages rate limiting for multiple APIs
type RateLimiter struct {
	requestsPerMinute int
	exemptPaths       map[string]bool
	apis              map[string]*APILimiter
	cleanupInterval   time.Duration
	stopCleanup       chan bool
	mu                sync.Mutex
}

// NewRateLimiter creates a new rate limiter
func InitRateLimiter(cfg *config.RateLimitConfig) {
	globalRateLimiter = NewRateLimiter(cfg)
}

// NewRateLimiter creates a new rate limiter with exempt paths
func NewRateLimiter(cfg *config.RateLimitConfig) *RateLimiter {
	cleanupInterval := time.Duration(cfg.NonReferenceAPIs.CleanupIntervalMinutes) * time.Minute
	if cleanupInterval <= 0 {
		cleanupInterval = 5 * time.Minute // Default to 5 minutes if not specified
	}

	rl := &RateLimiter{
		requestsPerMinute: cfg.NonReferenceAPIs.RequestsPerMinute,
		exemptPaths:       make(map[string]bool),
		apis:              make(map[string]*APILimiter),
		cleanupInterval:   cleanupInterval,
		stopCleanup:       make(chan bool),
	}

	// Convert exempt paths to map for O(1) lookup
	for _, path := range cfg.NonReferenceAPIs.ExemptPaths {
		rl.exemptPaths[path] = true
	}

	// Start cleanup goroutine
	go rl.cleanupExpiredAPILimiters()

	return rl
}

// RateLimitMiddleware creates a rate limiting middleware
func RateLimitMiddleware(cfg *config.RateLimitConfig) gin.HandlerFunc {
	// If rate limiting is disabled, return a no-op middleware
	if cfg.NonReferenceAPIs == nil || !cfg.NonReferenceAPIs.Enabled {
		return func(c *gin.Context) {
			c.Next()
		}
	}

	return func(c *gin.Context) {
		// Check rate limit
		allowed, err := globalRateLimiter.AllowRequest(c.Request.URL.Path)
		if err != nil {
			// Check if request was denied due to rate limit
			if !allowed {
				c.Header("X-RateLimit-Limit", fmt.Sprintf("%d", cfg.NonReferenceAPIs.RequestsPerMinute))
				c.Header("X-RateLimit-Remaining", "0")
				c.Header("Retry-After", "60") // Suggest retry after 1 minute
				c.String(http.StatusTooManyRequests, err.Error())
				c.Abort()
				return
			}
			// Some other error occurred
			c.String(http.StatusInternalServerError, err.Error())
			c.Abort()
			return
		}

		// Add rate limit headers
		c.Header("X-RateLimit-Limit", fmt.Sprintf("%d", cfg.NonReferenceAPIs.RequestsPerMinute))
		remaining := globalRateLimiter.GetRemainingTokens(c.Request.URL.Path)
		c.Header("X-RateLimit-Remaining", fmt.Sprintf("%.0f", remaining))

		c.Next()
	}
}

// StopRateLimiter stops the global rate limiter (for graceful shutdown)
func StopRateLimiter() {
	if globalRateLimiter != nil {
		globalRateLimiter.Stop()
	}
}

// AllowRequest checks if a request should be allowed
func (rl *RateLimiter) AllowRequest(path string) (bool, error) {
	// Check if path is exempt
	if rl.isPathExempt(path) {
		return true, nil
	}

	// Get or create API limiter
	key := path

	// Use read lock first for better performance
	rl.mu.Lock()
	defer rl.mu.Unlock()
	apiLimiter, exists := rl.apis[key]

	if exists {
		return apiLimiter.allow(rl.requestsPerMinute)
	}

	apiLimiter = &APILimiter{
		tokens:      float64(rl.requestsPerMinute), // Start with a full bucket
		lastChanged: time.Now(),
		burstSize:   rl.requestsPerMinute, // Allow burst up to the rate limit
	}
	rl.apis[key] = apiLimiter

	return apiLimiter.allow(rl.requestsPerMinute)
}

// allow checks if a request is allowed using token bucket algorithm
func (al *APILimiter) allow(requestsPerMinute int) (bool, error) {
	now := time.Now()

	// Calculate how much time has passed since last refill
	elapsed := now.Sub(al.lastChanged)
	// Refill tokens based on elapsed time
	if elapsed > 0 {
		// Add tokens proportional to elapsed time (tokens per second)
		tokensToAdd := elapsed.Seconds() * float64(requestsPerMinute) / constant.MinuteToSec
		al.tokens += tokensToAdd
		// Don't let tokens exceed burst size
		if al.tokens > float64(al.burstSize) {
			al.tokens = float64(al.burstSize)
		}
		al.lastChanged = now
	}

	// Check if we have enough tokens for this request
	if al.tokens >= 1 {
		al.tokens -= 1
		return true, nil
	}

	// Don't update lastChanged when request is blocked
	return false, fmt.Errorf("rate limit exceeded: maximum allowed is %d RPM", requestsPerMinute)
}

func (rl *RateLimiter) isPathExempt(path string) bool {
	if rl.exemptPaths[path] {
		return true
	}

	return false
}

// getAPIKey generates a unique key for API (using path)
func (rl *RateLimiter) getAPIKey(path string) string {
	return path
}

// cleanupExpiredAPILimiters removes inactive API limiters
func (rl *RateLimiter) cleanupExpiredAPILimiters() {
	ticker := time.NewTicker(rl.cleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			now := time.Now()
			var keysToDelete []string

			// Collect keys to delete first to minimize lock time
			rl.mu.Lock()
			for key, apiLimiter := range rl.apis {
				// Remove if inactive for cleanup interval (using lastChanged time)
				if now.Sub(apiLimiter.lastChanged) > rl.cleanupInterval {
					keysToDelete = append(keysToDelete, key)
				}
			}

			// Delete collected keys with write lock
			if len(keysToDelete) > 0 {
				for _, key := range keysToDelete {
					// Double-check entry still exists and is still expired
					if apiLimiter, exists := rl.apis[key]; exists && now.Sub(apiLimiter.lastChanged) > rl.cleanupInterval {
						delete(rl.apis, key)
					}
				}
			}
			rl.mu.Unlock()
		case <-rl.stopCleanup:
			return
		}
	}
}

// Stop stops the rate limiter cleanup goroutine
func (rl *RateLimiter) Stop() {
	close(rl.stopCleanup)
}

// GetRemainingTokens returns the current number of remaining tokens for an API
func (rl *RateLimiter) GetRemainingTokens(path string) float64 {
	if rl.isPathExempt(path) {
		return float64(rl.requestsPerMinute)
	}

	key := path

	rl.mu.Lock()
	defer rl.mu.Unlock()
	apiLimiter, exists := rl.apis[key]

	if !exists {
		return float64(rl.requestsPerMinute)
	}

	// Calculate current tokens without modifying state
	return rl.calculateCurrentTokens(apiLimiter, rl.requestsPerMinute)
}

// calculateCurrentTokens calculates the current tokens without modifying the limiter state
func (rl *RateLimiter) calculateCurrentTokens(al *APILimiter, requestsPerMinute int) float64 {
	now := time.Now()
	elapsed := now.Sub(al.lastChanged)

	tokens := al.tokens

	// Add tokens based on elapsed time without modifying state
	if elapsed > 0 {
		// Add tokens proportional to elapsed time (tokens per second)
		tokensToAdd := elapsed.Seconds() * float64(requestsPerMinute) / constant.MinuteToSec
		tokens += tokensToAdd
		// Don't let tokens exceed burst size
		if tokens > float64(al.burstSize) {
			tokens = float64(al.burstSize)
		}
	}

	return tokens
}

// GetAPICount returns the number of active API limiters (for monitoring)
func (rl *RateLimiter) GetAPICount() int {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	return len(rl.apis)
}
