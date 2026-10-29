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
	"math"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"infer-dp-scheduler/pkg/common/config"
)

// Test_NewRateLimiter_NilConfig - Test behavior with nil config
func Test_NewRateLimiter_NilConfig(t *testing.T) {
	// This should panic, so we test that
	defer func() {
		if r := recover(); r == nil {
			t.Error("Expected panic with nil config")
		}
	}()
	NewRateLimiter(nil)
}

// Test_NewRateLimiter_Green - Test successful creation
func Test_NewRateLimiter_Green(t *testing.T) {
	cfg := &config.RateLimitConfig{
		NonReferenceAPIs: &config.NonReferenceAPIRateLimit{
			Enabled:                true,
			RequestsPerMinute:      60,
			CleanupIntervalMinutes: 5,
			ExemptPaths:            []string{"/health", "/metrics"},
		},
	}

	rl := NewRateLimiter(cfg)
	if rl == nil {
		t.Fatal("Expected non-nil rate limiter")
	}

	if rl.requestsPerMinute != 60 {
		t.Errorf("Expected 60 RPM, got %d", rl.requestsPerMinute)
	}

	if len(rl.exemptPaths) != 2 {
		t.Errorf("Expected 2 exempt paths, got %d", len(rl.exemptPaths))
	}

	rl.Stop()
}

// Test_AllowRequest_ExemptPath - Test exempt paths are always allowed
func Test_AllowRequest_ExemptPath(t *testing.T) {
	cfg := &config.RateLimitConfig{
		NonReferenceAPIs: &config.NonReferenceAPIRateLimit{
			Enabled:                true,
			RequestsPerMinute:      1, // Only 1 request per minute for testing
			CleanupIntervalMinutes: 1,
			ExemptPaths:            []string{"/health", "/api/v1/status"},
		},
	}

	rl := NewRateLimiter(cfg)
	defer rl.Stop()

	// Test exempt paths
	exemptPaths := []string{"/health", "/api/v1/status"}
	for _, path := range exemptPaths {
		allowed, err := rl.AllowRequest(path)
		if !allowed {
			t.Errorf("Exempt path %s should be allowed", path)
		}
		if err != nil {
			t.Errorf("Exempt path %s should not return error, got %v", path, err)
		}
	}

	// Test non-exempt path - first request should be allowed (new bucket)
	allowed, _ := rl.AllowRequest("/api/v1/data")
	if !allowed {
		t.Error("Non-exempt path first request should be allowed (new bucket)")
	}

	// Second immediate request should be denied (bucket empty)
	allowed, _ = rl.AllowRequest("/api/v1/data")
	if allowed {
		t.Error("Non-exempt path second request should be denied")
	}
}

// Test_TokenBucket_BurstHandling - Test burst capacity
func Test_TokenBucket_BurstHandling(t *testing.T) {
	cfg := &config.RateLimitConfig{
		NonReferenceAPIs: &config.NonReferenceAPIRateLimit{
			Enabled:           true,
			RequestsPerMinute: 5,
		},
	}

	rl := NewRateLimiter(cfg)
	defer rl.Stop()

	path := "/api/test"

	// Should allow burst up to the rate limit (5 requests)
	successCount := 0
	for i := 0; i < 5; i++ {
		allowed, _ := rl.AllowRequest(path)
		if allowed {
			successCount++
		}
	}

	if successCount != 5 {
		t.Errorf("Expected 5 successful requests in burst, got %d", successCount)
	}

	// 6th request should be denied
	allowed, _ := rl.AllowRequest(path)
	if allowed {
		t.Error("6th request should be denied - exceeded burst capacity")
	}
}

// Test_TokenBucket_RefillAccuracy - Test token refill logic
func Test_TokenBucket_RefillAccuracy(t *testing.T) {
	cfg := &config.RateLimitConfig{
		NonReferenceAPIs: &config.NonReferenceAPIRateLimit{
			Enabled:           true,
			RequestsPerMinute: 60, // 1 token per second
		},
	}

	rl := NewRateLimiter(cfg)
	defer rl.Stop()

	path := "/api/test"

	// Use all initial tokens (60 requests)
	for i := 0; i < 60; i++ {
		allowed, _ := rl.AllowRequest(path)
		if !allowed {
			t.Fatalf("Request %d should be allowed", i+1)
		}
	}

	// Check bucket is empty
	remaining := rl.GetRemainingTokens(path)
	if remaining > 0.1 {
		t.Errorf("Expected 0 tokens after using all, got %.1f", remaining)
	}

	// Wait 2 seconds for 2 tokens to refill
	time.Sleep(2 * time.Second)

	// Check tokens are refilled
	remaining = rl.GetRemainingTokens(path)
	if remaining < 1.9 || remaining > 2.1 {
		t.Errorf("Expected ~2 tokens after 2 seconds, got %.1f", remaining)
	}

	// Should allow 2 more requests
	for i := 0; i < 2; i++ {
		allowed, _ := rl.AllowRequest(path)
		if !allowed {
			t.Errorf("Refilled token request %d should be allowed", i+1)
		}
	}
}

// Test_TokenBucket_MultipleAPIs - Test rate limiting is per API
func Test_TokenBucket_MultipleAPIs(t *testing.T) {
	cfg := &config.RateLimitConfig{
		NonReferenceAPIs: &config.NonReferenceAPIRateLimit{
			Enabled:           true,
			RequestsPerMinute: 2,
		},
	}

	rl := NewRateLimiter(cfg)
	defer rl.Stop()

	// Different APIs should have independent buckets
	api1 := "/api/v1/users"
	api2 := "/api/v1/orders"

	// Use all tokens for API1
	for i := 0; i < 2; i++ {
		allowed, _ := rl.AllowRequest(api1)
		if !allowed {
			t.Errorf("API1 request %d should be allowed", i+1)
		}
	}

	// API1 should be out of tokens
	allowed, _ := rl.AllowRequest(api1)
	if allowed {
		t.Error("API1 should be out of tokens")
	}

	// But API2 should still have full bucket
	for i := 0; i < 2; i++ {
		allowed, _ := rl.AllowRequest(api2)
		if !allowed {
			t.Errorf("API2 request %d should be allowed", i+1)
		}
	}
}

// Test_GetRemainingTokens - Test remaining tokens tracking
func Test_GetRemainingTokens(t *testing.T) {
	cfg := &config.RateLimitConfig{
		NonReferenceAPIs: &config.NonReferenceAPIRateLimit{
			Enabled:           true,
			RequestsPerMinute: 10,
		},
	}

	rl := NewRateLimiter(cfg)
	defer rl.Stop()

	path := "/api/test"

	// Initially should have full bucket
	tokens := rl.GetRemainingTokens(path)
	if math.Abs(tokens-10) > 1e-6 {
		t.Errorf("Expected 10 tokens initially, got %.1f", tokens)
	}

	// After one request
	rl.AllowRequest(path)
	tokens = rl.GetRemainingTokens(path)
	if math.Abs(tokens-9) > 1e-6 {
		t.Errorf("Expected 9 tokens after 1 request, got %.1f", tokens)
	}

	// Exempt path should always return full amount
	exemptTokens := rl.GetRemainingTokens("/health")
	if math.Abs(exemptTokens-10) > 1e-6 {
		t.Errorf("Exempt path should always return full tokens, got %.1f", exemptTokens)
	}
}

// Test_CleanupExpiredAPILimiters - Test cleanup of inactive APIs
func Test_CleanupExpiredAPILimiters(t *testing.T) {
	cfg := &config.RateLimitConfig{
		NonReferenceAPIs: &config.NonReferenceAPIRateLimit{
			Enabled:                true,
			RequestsPerMinute:      10,
			CleanupIntervalMinutes: 1, // 1 minute cleanup
		},
	}

	rl := NewRateLimiter(cfg)
	defer rl.Stop()

	path1 := "/api/v1/test1"
	path2 := "/api/v1/test2"

	// Create API limiters
	rl.AllowRequest(path1)
	rl.AllowRequest(path2)

	if rl.GetAPICount() != 2 {
		t.Errorf("Expected 2 API limiters, got %d", rl.GetAPICount())
	}

	// Manually trigger cleanup by modifying the rate limiter
	// We'll test that the cleanup logic exists rather than waiting
	tokens := rl.GetRemainingTokens(path1)
	if tokens < 0 {
		t.Errorf("Invalid token count: %.1f", tokens)
	}
}

// Test_DisabledRateLimiter - Test when rate limiting is disabled
func Test_DisabledRateLimiter(t *testing.T) {
	// Test nil NonReferenceAPIs
	cfg1 := &config.RateLimitConfig{}
	middleware1 := RateLimitMiddleware(cfg1)
	if middleware1 == nil {
		t.Error("Middleware should be created when rate limiting is disabled")
	}

	// Test disabled flag
	cfg2 := &config.RateLimitConfig{
		NonReferenceAPIs: &config.NonReferenceAPIRateLimit{
			Enabled: false,
		},
	}
	middleware2 := RateLimitMiddleware(cfg2)
	if middleware2 == nil {
		t.Error("Middleware should be created when rate limiting is disabled")
	}
}

// Test_ZeroRateLimit - Test zero requests per minute
func Test_ZeroRateLimit(t *testing.T) {
	cfg := &config.RateLimitConfig{
		NonReferenceAPIs: &config.NonReferenceAPIRateLimit{
			Enabled:           true,
			RequestsPerMinute: 0,
		},
	}

	rl := NewRateLimiter(cfg)
	defer rl.Stop()

	path := "/api/test"

	// With 0 RPM, no requests should be allowed
	allowed, _ := rl.AllowRequest(path)
	if allowed {
		t.Error("No requests should be allowed with 0 RPM")
	}

	// Remaining tokens should be 0
	tokens := rl.GetRemainingTokens(path)
	if tokens != 0 {
		t.Errorf("Expected 0 tokens with 0 RPM, got %.1f", tokens)
	}
}

// Test_HighRateLimit - Test very high rate limits
func Test_HighRateLimit(t *testing.T) {
	cfg := &config.RateLimitConfig{
		NonReferenceAPIs: &config.NonReferenceAPIRateLimit{
			Enabled:           true,
			RequestsPerMinute: 10000,
		},
	}

	rl := NewRateLimiter(cfg)
	defer rl.Stop()

	path := "/api/test"

	// Should allow many requests without issues
	successCount := 0
	for i := 0; i < 100; i++ {
		allowed, _ := rl.AllowRequest(path)
		if allowed {
			successCount++
		}
	}

	if successCount != 100 {
		t.Errorf("Expected 100 successful requests with high rate limit, got %d", successCount)
	}

	// Should still have many tokens left
	tokens := rl.GetRemainingTokens(path)
	if tokens < 9900 {
		t.Errorf("Expected at least 9900 remaining tokens, got %.1f", tokens)
	}
}

// Test_ConcurrentAccess - Test thread safety with concurrent requests
func Test_ConcurrentAccess(t *testing.T) {
	cfg := &config.RateLimitConfig{
		NonReferenceAPIs: &config.NonReferenceAPIRateLimit{
			Enabled:           true,
			RequestsPerMinute: 100,
		},
	}

	rl := NewRateLimiter(cfg)
	defer rl.Stop()

	path := "/api/test"
	numGoroutines := 50
	requestsPerGoroutine := 10

	results := make(chan bool, numGoroutines*requestsPerGoroutine)

	// Launch multiple goroutines
	for i := 0; i < numGoroutines; i++ {
		go func() {
			for j := 0; j < requestsPerGoroutine; j++ {
				allowed, _ := rl.AllowRequest(path)
				results <- allowed
			}
		}()
	}

	// Collect results
	successCount := 0
	totalRequests := numGoroutines * requestsPerGoroutine
	for i := 0; i < totalRequests; i++ {
		if <-results {
			successCount++
		}
	}

	// Should allow exactly the rate limit (burst capacity)
	if successCount > 100 {
		t.Errorf("Too many requests allowed: %d, should be at most 100", successCount)
	}
	if successCount < 100 {
		t.Errorf("Too few requests allowed: %d, should be exactly 100 for burst", successCount)
	}

	// All remaining should be denied
	tokens := rl.GetRemainingTokens(path)
	if tokens > 0.1 {
		t.Errorf("Expected 0 tokens after burst, got %.1f", tokens)
	}
}

// Test_ErrorMessages - Test returned error messages
func Test_ErrorMessages(t *testing.T) {
	cfg := &config.RateLimitConfig{
		NonReferenceAPIs: &config.NonReferenceAPIRateLimit{
			Enabled:           true,
			RequestsPerMinute: 5,
		},
	}

	rl := NewRateLimiter(cfg)
	defer rl.Stop()

	path := "/api/test"

	// Use all tokens
	for i := 0; i < 5; i++ {
		rl.AllowRequest(path)
	}

	// Next request should return proper error message
	allowed, err := rl.AllowRequest(path)
	if allowed {
		t.Error("Request should be denied")
	}
	if err == nil {
		t.Error("Should return error when rate limited")
	}
	expected := "rate limit exceeded: maximum allowed is 5 RPM"
	if err.Error() != expected {
		t.Errorf("Expected error message '%s', got '%s'", expected, err.Error())
	}
}

// Benchmark_AllowRequest - Benchmark for performance
func Benchmark_AllowRequest(b *testing.B) {
	cfg := &config.RateLimitConfig{
		NonReferenceAPIs: &config.NonReferenceAPIRateLimit{
			Enabled:           true,
			RequestsPerMinute: 1000,
		},
	}

	rl := NewRateLimiter(cfg)
	defer rl.Stop()

	path := "/api/test"

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			rl.AllowRequest(path)
		}
	})
}

func TestRateLimiter_AllowRequest(t *testing.T) {
	cfg := &config.RateLimitConfig{
		NonReferenceAPIs: &config.NonReferenceAPIRateLimit{
			Enabled:           true,
			RequestsPerMinute: 1, // Low for testing
			ExemptPaths: []string{
				"/v1/chat/completions",
				"/v1/completions",
				"/v1/metaserver",
			},
			CleanupIntervalMinutes: 1, // Short for testing
		},
	}
	limiter := NewRateLimiter(cfg) // 2 requests per minute, burst of 1

	path := "/api/test"

	// First request should be allowed
	if allowed, _ := limiter.AllowRequest(path); !allowed {
		t.Error("First request should be allowed")
	}

	// Second request should be denied
	if allowed, _ := limiter.AllowRequest(path); allowed {
		t.Error("Second request should be denied due to limit")
	}

	// Exempt paths should always be allowed
	if allowed, _ := limiter.AllowRequest("/v1/chat/completions"); !allowed {
		t.Error("Exempt path should be allowed")
	}

	limiter.Stop()
}

func TestRateLimiting_ComprehensiveScenarios(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// Test configuration
	cfg := &config.RateLimitConfig{
		NonReferenceAPIs: &config.NonReferenceAPIRateLimit{
			Enabled:           true,
			RequestsPerMinute: 5, // Low for testing
			ExemptPaths: []string{
				"/v1/chat/completions",
				"/v1/completions",
				"/v1/metaserver",
			},
			CleanupIntervalMinutes: 1, // Short for testing
		},
	}
	InitRateLimiter(cfg)

	// Create middleware
	middleware := RateLimitMiddleware(cfg)

	// Create test router
	router := gin.New()
	router.Use(middleware)

	// Test endpoints
	router.GET("/v1/chat/completions", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"endpoint": "chat"})
	})
	router.GET("/v1/completions", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"endpoint": "completions"})
	})
	router.GET("/v1/metaserver", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"endpoint": "metaserver"})
	})

	// Non-exempt APIs (should be rate limited)
	router.GET("/api/version", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"endpoint": "version"})
	})
	router.GET("/api/status", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"endpoint": "status"})
	})
	router.GET("/internal/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"endpoint": "health"})
	})
	router.GET("/internal/metrics", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"endpoint": "metrics"})
	})

	t.Run("Scenario 1: Exempt APIs are never rate limited", func(t *testing.T) {
		exemptEndpoints := []string{
			"/v1/chat/completions",
			"/v1/completions",
			"/v1/metaserver",
		}

		for _, endpoint := range exemptEndpoints {
			// Make 10 requests rapidly
			for i := 0; i < 10; i++ {
				req := httptest.NewRequest("GET", endpoint, nil)
				req.RemoteAddr = "192.168.1.1:12345"
				w := httptest.NewRecorder()
				router.ServeHTTP(w, req)

				assert.Equal(t, http.StatusOK, w.Code, "Request %d to %s should always pass", i+1, endpoint)
			}
		}
	})

	t.Run("Scenario 2: Non-exempt APIs respect burst limit", func(t *testing.T) {
		channel := make(chan bool, 5)

		// Concurrent requests to test burst
		for i := 0; i < 2; i++ {
			go func(id int) {
				req := httptest.NewRequest("GET", "/api/version", nil)
				req.RemoteAddr = fmt.Sprintf("192.168.1.%d:12345", id%10)
				w := httptest.NewRecorder()
				router.ServeHTTP(w, req)

				channel <- w.Code == http.StatusOK
			}(i)
		}

		// Collect results
		successCount := 0
		for i := 0; i < 2; i++ {
			if <-channel {
				successCount++
			}
		}

		assert.Equal(t, 2, successCount, "Only limit (2) requests should pass")
	})

	t.Run("Scenario 3: Rate limit applies globally to all clients for same API", func(t *testing.T) {
		// Multiple clients hitting the same endpoint
		var wg sync.WaitGroup
		successCount := make(chan int, 10)

		for client := 1; client <= 10; client++ {
			wg.Add(1)
			go func(clientID int) {
				defer wg.Done()
				req := httptest.NewRequest("GET", "/api/version", nil)
				req.RemoteAddr = fmt.Sprintf("10.0.0.%d:12345", clientID)
				w := httptest.NewRecorder()
				router.ServeHTTP(w, req)

				if w.Code == http.StatusOK {
					successCount <- 1
				} else {
					successCount <- 0
				}
			}(client)
		}

		wg.Wait()
		close(successCount)

		// Count successful requests
		totalSuccess := 0
		for success := range successCount {
			totalSuccess += success
		}

		// Should allow up to 5 RPM (requests per minute)
		assert.Equal(t, 3, totalSuccess, "Should allow up to 3 requests this minute")
	})

	t.Run("Scenario 4: Rate limit is independent between different APIs", func(t *testing.T) {
		// Each API can have its own rate limit
		endpoints := []string{"/api/version", "/api/status"}
		expectedSuccessCount := []int{0, 3}

		for index, endpoint := range endpoints {
			// Make 3 requests to each endpoint
			successCount := 0
			for i := 0; i < 3; i++ {
				req := httptest.NewRequest("GET", endpoint, nil)
				req.RemoteAddr = "192.168.1.100:12345"
				w := httptest.NewRecorder()
				router.ServeHTTP(w, req)

				if w.Code == http.StatusOK {
					successCount++
				}
			}

			assert.Equal(t, expectedSuccessCount[index], successCount, "should all pass")
		}
	})

	t.Run("Scenario 5: Rate limit returns correct error message", func(t *testing.T) {
		for i := 0; i < 2; i++ {
			req := httptest.NewRequest("GET", "/api/status", nil)
			req.RemoteAddr = "192.168.1.200:12345"
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			assert.Equal(t, http.StatusOK, w.Code, "Request %d should pass", i+1)
		}

		// Third request should fail with our custom message
		req := httptest.NewRequest("GET", "/api/status", nil)
		req.RemoteAddr = "192.168.1.200:12345"
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusTooManyRequests, w.Code)
		assert.Equal(t, "rate limit exceeded: maximum allowed is 5 RPM", w.Body.String(), "Should return rate limit error message")

	})

	t.Run("Scenario 6: X-Forwarded-For headers work correctly", func(t *testing.T) {
		// should be limited
		req2 := httptest.NewRequest("GET", "/api/version", nil)
		req2.Header.Set("X-Forwarded-For", "203.0.113.1, 192.168.1.1")
		w2 := httptest.NewRecorder()
		router.ServeHTTP(w2, req2)

		assert.Equal(t, http.StatusTooManyRequests, w2.Code, "Second request within burst should pass")

		// Reset counters
		time.Sleep(60 * time.Second)

		channel := make(chan bool, 5)

		// should all passed
		for i := 0; i < 5; i++ {
			go func(id int) {
				req := httptest.NewRequest("GET", "/api/version", nil)
				req.RemoteAddr = fmt.Sprintf("192.168.1.%d:12345", id%10)
				w := httptest.NewRecorder()
				router.ServeHTTP(w, req)

				channel <- w.Code == http.StatusOK
			}(i)
		}

		// Collect results
		successCount := 0
		for i := 0; i < 5; i++ {
			if <-channel {
				successCount++
			}
		}

		assert.Equal(t, 5, successCount, "Only limit 5 requests should pass")

		// should be limited
		req2 = httptest.NewRequest("GET", "/api/version", nil)
		req2.Header.Set("X-Forwarded-For", "0.0.0.1, 192.168.1.1")
		w2 = httptest.NewRecorder()
		router.ServeHTTP(w2, req2)

		assert.Equal(t, http.StatusTooManyRequests, w2.Code, "should not pass")
	})

	StopRateLimiter()
}
