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

package instances

import (
	"fmt"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"infer-dp-scheduler/pkg/common/client"
	instance_client "infer-dp-scheduler/pkg/common/client/instance-client"
	monitor_client "infer-dp-scheduler/pkg/common/client/monitor-client"
	"infer-dp-scheduler/pkg/common/config"
	"infer-dp-scheduler/pkg/common/constant"
	instance_metrics "infer-dp-scheduler/pkg/common/instance-metrics"
	"infer-dp-scheduler/pkg/common/logger"
	"infer-dp-scheduler/pkg/common/utils"
	"infer-dp-scheduler/pkg/route-server/cluster/singleton"
)

func TestGetInstanceStatusEmpty(t *testing.T) {
	// Create a mock InstanceManager
	m := &InstanceManager{
		routeMapMutex: sync.RWMutex{},
		available:     make(map[string]map[string]*client.Instance),
		unavailable:   make(map[string]map[string]*client.Instance),
		requiredRoles: []string{constant.Prefill, constant.Decode},
	}

	// Create a mock gin.Context
	w := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(w)

	// Test the scenario when both m.available and m.unavailable are empty
	m.GetInstanceStatus(ctx)
	var rsp InstanceStatusRsp
	utils.SonicApi.Unmarshal(w.Body.Bytes(), &rsp)
	if rsp.Status != "deploying" {
		t.Errorf("Expected status to be 'deploying', but got '%s'", rsp.Status)
	}
}

func TestGetInstanceStatus(t *testing.T) {
	// Create a mock InstanceManager
	m := &InstanceManager{
		routeMapMutex:  sync.RWMutex{},
		available:      make(map[string]map[string]*client.Instance),
		unavailable:    make(map[string]map[string]*client.Instance),
		requiredRoles:  []string{constant.Prefill, constant.Decode},
		healthCache:    NewInstanceHealthCache(nil, newTestHealthCheckConfig()),
		clusterService: &singleton.SingletonClusterService{},
	}

	// Create a mock gin.Context
	w := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(w)
	tests := struct {
		name           string
		clientType     string
		route          config.Route
		httpCfg        config.HttpConfig
		newClientErr   error
		expectedErrMsg string
	}{
		name:       "ValidVllmClient",
		clientType: "vllm",
		route: config.Route{
			Ip:          "localhost",
			Port:        8080,
			Role:        "prefill",
			StaticInfos: map[string]string{},
		},
		httpCfg: config.HttpConfig{},
	}

	client_prefill, _ := instance_client.NewClient(tests.clientType, tests.route, instance_client.ClientConfig{
		HttpCfg:      tests.httpCfg,
		CheckTimeout: 0,
	})

	stats1 := CreateStats("instance01", "prefill", "10", 0, 1, 100, 50)
	stats2 := CreateStats("instance01", "decode", "10", 0, 1, 100, 50)

	instance01 := &client.Instance{
		ID:        "01",
		Stats:     stats1,
		Client:    client_prefill,
		LastError: nil,
	}

	instance02 := &client.Instance{
		ID:        "02",
		Stats:     stats2,
		Client:    client_prefill,
		LastError: nil,
	}
	m.available[constant.Prefill] = make(map[string]*client.Instance)
	m.available[constant.Prefill][instance01.ID] = instance01
	m.availableMap = make(map[string]*client.Instance)
	m.availableMap[instance01.ID] = instance01
	m.available[constant.Decode] = make(map[string]*client.Instance)
	m.available[constant.Decode][instance02.ID] = instance02
	m.availableMap[instance02.ID] = instance02
	fmt.Println(len(m.available[constant.Prefill]))
	m.GetInstanceStatus(ctx)
	var rsp InstanceStatusRsp
	utils.SonicApi.Unmarshal(w.Body.Bytes(), &rsp)
	if rsp.Status != "running" || len(rsp.InstanceStatus[constant.Prefill]) != 1 {
		t.Errorf("Expected status to be 'running' and length of Prefill to be 1, but got '%s' and '%d'", rsp.Status, len(rsp.InstanceStatus[constant.Prefill]))
	}

	m.allInstanceMap = make(map[string]*client.Instance)
	m.allInstanceMap[instance01.ID] = instance01
	m.allInstanceMap[instance02.ID] = instance02
	readinessStatus := m.GetReadinessStatus(map[string]int{constant.Prefill: 1, constant.Decode: 1})
	assert.Equal(t, true, readinessStatus.Ready)
	assert.NotEmpty(t, readinessStatus.Message)

	readinessStatus = m.GetReadinessStatus(map[string]int{constant.Prefill: 3, constant.Decode: 3})
	assert.Equal(t, false, readinessStatus.Ready)
	logger.Log().Infof("%v", readinessStatus.Message)
}

// createWorkerStats creates a mock WorkerStats for testing.
func CreateStats(id, role, maxNumSeqs string, waiting, running, promptTokens, genTokens float64) instance_metrics.WorkerStats {
	return &instance_metrics.ZeroCopyStats{
		Id:   id,
		Role: role,
		NumericStats: &map[string][]*instance_metrics.MetricInfo{
			monitor_client.NumRequestsWaiting:    {{MetricName: monitor_client.NumRequestsWaiting, Value: waiting}},
			monitor_client.NumRequestsRunning:    {{MetricName: monitor_client.NumRequestsRunning, Value: running}},
			monitor_client.PromptTokensTotal:     {{MetricName: monitor_client.PromptTokensTotal, Value: promptTokens}},
			monitor_client.GenerationTokensTotal: {{MetricName: monitor_client.GenerationTokensTotal, Value: genTokens}},
		},
		Status: instance_metrics.Available,
		StaticInfo: map[string]string{
			constant.MaxNumSeqs: maxNumSeqs,
		},
		TimeStamp: time.Now(),
	}
}

func BuildPDInstance(deploying *PDArgs, running *PDArgs, fault *PDArgs, m *InstanceManager) {
	index := 0
	if deploying != nil {
		CreateInstance(index, deploying.prefill, constant.Prefill, instance_metrics.Init, m)
		CreateInstance(index, deploying.decode, constant.Decode, instance_metrics.Init, m)
		index += deploying.decode + deploying.prefill
	}
	if running != nil {
		CreateInstance(index, running.prefill, constant.Prefill, instance_metrics.Available, m)
		CreateInstance(index, running.decode, constant.Decode, instance_metrics.Available, m)
		index += running.decode + running.prefill
	}
	if fault != nil {
		CreateInstance(index, fault.prefill, constant.Prefill, instance_metrics.Faulty, m)
		CreateInstance(index, fault.decode, constant.Decode, instance_metrics.Faulty, m)
		index += fault.decode + fault.prefill
	}
}

func CreateInstance(index int, nums int, role string, status instance_metrics.InstanceStatus, m *InstanceManager) {
	for range nums {
		instanceID := fmt.Sprintf("%s_%d", role, index)
		states := &instance_metrics.ZeroCopyStats{
			Id:     instanceID,
			Role:   role,
			Status: status,
		}
		if states.Status == instance_metrics.Available {
			m.available[role][instanceID] = &client.Instance{
				ID:     instanceID,
				Stats:  states,
				Client: &instance_client.VllmClient{},
			}
			m.availableMap[instanceID] = m.available[role][instanceID]
		} else {
			m.unavailable[role][instanceID] = &client.Instance{
				ID:     instanceID,
				Stats:  states,
				Client: &instance_client.VllmClient{},
			}
			m.unavailableMap[instanceID] = m.unavailable[role][instanceID]
		}
		index++
	}
}

type PDArgs struct {
	prefill int
	decode  int
}

func TestInstanceManager_GetInstanceStatus(t *testing.T) {
	tests := []struct {
		name      string
		deploying *PDArgs
		running   *PDArgs
		fault     *PDArgs
		want      string
	}{
		{
			name: "all instances are deploying",
			deploying: &PDArgs{
				decode:  2,
				prefill: 2,
			},
			want: constant.DeployingStatus,
		},
		{
			name: "some instances are running and some instances are deploying but cant service",
			deploying: &PDArgs{
				decode:  1,
				prefill: 1,
			},
			running: &PDArgs{
				decode:  1,
				prefill: 0,
			},
			want: constant.DeployingStatus,
		},
		{
			name: "some instances are fault and some instances are running, cluster can service",
			running: &PDArgs{
				decode:  2,
				prefill: 1,
			},
			fault: &PDArgs{
				decode:  0,
				prefill: 1,
			},
			want: constant.ConcerningStatus,
		},
		{
			name: "some instances are running and some instances are deploying, cluster can service",
			deploying: &PDArgs{
				decode:  1,
				prefill: 1,
			},
			running: &PDArgs{
				decode:  2,
				prefill: 1,
			},
			want: constant.ConcerningStatus,
		},
		{
			name: "all instances are running and can service",
			running: &PDArgs{
				decode:  2,
				prefill: 2,
			},
			want: constant.RunningStatus,
		},
		{
			name: "some instances are running but some instances are fault and can not service",
			running: &PDArgs{
				decode:  2,
				prefill: 0,
			},
			fault: &PDArgs{
				decode:  0,
				prefill: 1,
			},
			want: constant.FaultStatus,
		},
		{
			name: "some instances are deploying but some instances are fault and can not service",
			deploying: &PDArgs{
				decode:  2,
				prefill: 0,
			},
			running: &PDArgs{
				decode:  0,
				prefill: 2,
			},
			fault: &PDArgs{
				decode:  0,
				prefill: 1,
			},
			want: constant.FaultStatus,
		},
		{
			name: "all instances are fault and can not service",
			fault: &PDArgs{
				decode:  0,
				prefill: 1,
			},
			want: constant.FaultStatus,
		},
		{
			name: "all instances are running and can not service",
			running: &PDArgs{
				decode:  0,
				prefill: 2,
			},
			want: constant.FaultStatus,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(w)
			m := &InstanceManager{
				routeMapMutex: sync.RWMutex{},
				available:     make(map[string]map[string]*client.Instance),
				unavailable:   make(map[string]map[string]*client.Instance),
				requiredRoles: []string{constant.Prefill, constant.Decode},
			}
			m.available[constant.Prefill] = make(map[string]*client.Instance)
			m.available[constant.Decode] = make(map[string]*client.Instance)
			m.availableMap = make(map[string]*client.Instance)
			m.unavailable[constant.Prefill] = make(map[string]*client.Instance)
			m.unavailable[constant.Decode] = make(map[string]*client.Instance)
			m.unavailableMap = make(map[string]*client.Instance)
			BuildPDInstance(tt.deploying, tt.running, tt.fault, m)
			m.GetInstanceStatus(ctx)
			var rsp InstanceStatusRsp
			utils.SonicApi.Unmarshal(w.Body.Bytes(), &rsp)
			assert.Equal(t, tt.want, rsp.Status)
		})
	}
}

// ===== Health Cache Test Cases =====

func newTestHealthCheckConfig() *config.HealthCheckConfig {
	return &config.HealthCheckConfig{
		EventThresholdIntervalMs: 1000,
		HealthCacheExpireTimeMs:  100,
		RestHealthCommonConfig: config.RestHealthCommonConfig{
			Interval: 100,
		},
	}
}

// TestInstanceHealthCache_NewInstanceHealthCache test creating cache instance
func TestInstanceHealthCache_NewInstanceHealthCache(t *testing.T) {
	cache := NewInstanceHealthCache(nil, newTestHealthCheckConfig())

	assert.NotNil(t, cache)
	assert.NotNil(t, cache.cache)
	assert.Equal(t, map[string]int{}, cache.cache.RoleHealthyCounts)
	assert.False(t, cache.cache.UpdateTime.IsZero())
	assert.Nil(t, cache.clusterService)
}

// TestInstanceHealthCache_UpdateHealthStats test updating health statistics
func TestInstanceHealthCache_UpdateHealthStats(t *testing.T) {
	cache := NewInstanceHealthCache(nil, newTestHealthCheckConfig())

	// Update health statistics
	now := time.Now()
	roleHealthyCounts := map[string]int{
		"prefill": 2,
		"decode":  3,
	}
	err := cache.UpdateHealthStats(roleHealthyCounts, &now)

	// Verify update results
	assert.Nil(t, err)
	assert.Equal(t, 2, cache.cache.RoleHealthyCounts["prefill"])
	assert.Equal(t, 3, cache.cache.RoleHealthyCounts["decode"])
	assert.Equal(t, now, cache.cache.UpdateTime)
}

// TestInstanceHealthCache_GetHealthStats test getting health statistics
func TestInstanceHealthCache_GetHealthStats(t *testing.T) {
	cache := NewInstanceHealthCache(nil, newTestHealthCheckConfig())

	// Set initial data
	now := time.Now()
	roleHealthyCounts := map[string]int{
		"prefill": 5,
		"decode":  4,
	}
	cache.UpdateHealthStats(roleHealthyCounts, &now)

	// Get statistics (cache not expired, return cached data)
	stats := cache.GetOrFetchHealthStats()

	// Verify results
	assert.Equal(t, 5, stats.RoleHealthyCounts["prefill"])
	assert.Equal(t, 4, stats.RoleHealthyCounts["decode"])
	assert.False(t, stats.UpdateTime.IsZero())
}

// TestInstanceHealthCache_ResetCache test resetting cache
func TestInstanceHealthCache_ResetCache(t *testing.T) {
	cache := NewInstanceHealthCache(nil, newTestHealthCheckConfig())

	// Set some data
	now := time.Now()
	roleHealthyCounts := map[string]int{
		"prefill": 100,
		"decode":  100,
	}
	cache.UpdateHealthStats(roleHealthyCounts, &now)

	// Reset cache
	cache.ResetCache()

	// Verify cache has been reset
	assert.Equal(t, map[string]int{}, cache.cache.RoleHealthyCounts)
	assert.True(t, cache.cache.UpdateTime.IsZero())
}

// TestInstanceHealthCache_ConcurrentAccess test concurrent access
func TestInstanceHealthCache_ConcurrentAccess(t *testing.T) {
	cache := NewInstanceHealthCache(nil, newTestHealthCheckConfig())

	var wg sync.WaitGroup
	numGoroutines := 100

	// Concurrent update and read
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			now := time.Now()
			roleHealthyCounts := map[string]int{
				"prefill": index,
				"decode":  index * 2,
			}
			cache.UpdateHealthStats(roleHealthyCounts, &now)
			cache.GetOrFetchHealthStats()
		}(i)
	}

	wg.Wait()

	// Should have some data finally
	stats := cache.GetOrFetchHealthStats()
	assert.True(t, stats.RoleHealthyCounts["prefill"] >= 0)
	assert.True(t, stats.RoleHealthyCounts["decode"] >= 0)
}

// TestInstanceHealthCache_GetHealthStatsFromClusterService_Nil test when ClusterService is nil
func TestInstanceHealthCache_GetHealthStatsFromClusterService_Nil(t *testing.T) {
	cache := NewInstanceHealthCache(nil, newTestHealthCheckConfig())

	// clusterService is nil, GetHealthStatsFromClusterService should return nil
	result := cache.GetHealthStatsFromClusterService()
	assert.Nil(t, result)

	// Cache should remain unchanged
	assert.Equal(t, map[string]int{}, cache.cache.RoleHealthyCounts)
}

// ===== GetReadinessStatus Related Tests =====

// TestGetReadinessStatus_ForceModeTrue test force mode true
func TestGetReadinessStatus_ForceModeTrue(t *testing.T) {
	m := &InstanceManager{
		circuitBreaker: NewCircuitBreaker(&config.CircuitBreakerConfig{
			Enable:               true,
			CheckWindowSec:       10,
			FailureRateThreshold: 0.5,
			MinRequestCount:      5,
		}),
	}
	m.circuitBreaker.SetForceMode(ForceModeTrue)

	status := m.GetReadinessStatus(map[string]int{constant.Prefill: 1, constant.Decode: 1})
	assert.True(t, status.Ready)
	assert.Equal(t, "force_true", status.StateName)
	assert.Equal(t, 1, status.State)
}

// TestGetReadinessStatus_ForceModeFalse test force mode false
func TestGetReadinessStatus_ForceModeFalse(t *testing.T) {
	m := &InstanceManager{
		circuitBreaker: NewCircuitBreaker(&config.CircuitBreakerConfig{
			Enable:               true,
			CheckWindowSec:       10,
			FailureRateThreshold: 0.5,
			MinRequestCount:      5,
		}),
	}
	m.circuitBreaker.SetForceMode(ForceModeFalse)

	status := m.GetReadinessStatus(map[string]int{constant.Prefill: 1, constant.Decode: 1})
	assert.False(t, status.Ready)
	assert.Equal(t, "force_false", status.StateName)
	assert.Equal(t, 2, status.State)
}

// TestGetReadinessStatus_SingletonMode test singleton mode (get health statistics from available)
func TestGetReadinessStatus_SingletonMode(t *testing.T) {
	m := &InstanceManager{
		routeMapMutex:  sync.RWMutex{},
		available:      make(map[string]map[string]*client.Instance),
		unavailable:    make(map[string]map[string]*client.Instance),
		requiredRoles:  []string{constant.Prefill, constant.Decode},
		clusterService: &singleton.SingletonClusterService{},
		healthCache:    NewInstanceHealthCache(nil, newTestHealthCheckConfig()),
	}
	m.available[constant.Prefill] = make(map[string]*client.Instance)
	m.available[constant.Decode] = make(map[string]*client.Instance)

	stats := CreateStats("inst01", "prefill", "10", 0, 1, 100, 50)
	m.available[constant.Prefill]["inst01"] = &client.Instance{
		ID: "inst01", Stats: stats, Client: &instance_client.VllmClient{},
	}
	m.availableMap = map[string]*client.Instance{"inst01": m.available[constant.Prefill]["inst01"]}

	// 1p0d, requires 1p1d - not ready
	status := m.GetReadinessStatus(map[string]int{constant.Prefill: 1, constant.Decode: 1})
	assert.False(t, status.Ready)
	assert.Equal(t, "normal", status.StateName)

	// 1p1d, requires 1p1d - ready
	stats2 := CreateStats("inst02", "decode", "10", 0, 1, 100, 50)
	m.available[constant.Decode]["inst02"] = &client.Instance{
		ID: "inst02", Stats: stats2, Client: &instance_client.VllmClient{},
	}
	m.availableMap["inst02"] = m.available[constant.Decode]["inst02"]
	status = m.GetReadinessStatus(map[string]int{constant.Prefill: 1, constant.Decode: 1})
	assert.True(t, status.Ready)
	assert.Equal(t, "normal", status.StateName)
}

// TestGetReadinessStatus_NoCircuitBreaker test no circuit breaker and no valid information
func TestGetReadinessStatus_NoCircuitBreaker(t *testing.T) {
	// Use expired cache to ensure checkHealthCache returns nil
	// HealthCacheExpireTimeMs=1 means 1ms expiration
	cfg := &config.HealthCheckConfig{
		HealthCacheExpireTimeMs:  1,
		EventThresholdIntervalMs: 1000,
	}
	cache := NewInstanceHealthCache(nil, cfg)
	time.Sleep(5 * time.Millisecond) // Wait for cache to expire

	m := &InstanceManager{
		routeMapMutex: sync.RWMutex{},
		requiredRoles: []string{constant.Prefill, constant.Decode},
		healthCache:   cache,
	}

	status := m.GetReadinessStatus(map[string]int{constant.Prefill: 1, constant.Decode: 1})
	assert.False(t, status.Ready)
	assert.Contains(t, status.Message, "no valid instance info and circuit breaker not enabled")
}

// TestGetReadinessStatus_CircuitBreakerBroken test circuit breaker is broken
func TestGetReadinessStatus_CircuitBreakerBroken(t *testing.T) {
	cb := NewCircuitBreaker(&config.CircuitBreakerConfig{
		Enable:               true,
		CheckWindowSec:       10,
		FailureRateThreshold: 0.5,
		MinRequestCount:      5,
	})
	defer cb.Close()
	cb.state = StateOpen
	cb.updateWorkerReadinessState()

	// Use expired cache to ensure checkHealthCache returns nil
	cfg := &config.HealthCheckConfig{
		HealthCacheExpireTimeMs:  1,
		EventThresholdIntervalMs: 1000,
	}
	cache := NewInstanceHealthCache(nil, cfg)
	time.Sleep(5 * time.Millisecond)

	m := &InstanceManager{
		routeMapMutex:  sync.RWMutex{},
		requiredRoles:  []string{constant.Prefill, constant.Decode},
		healthCache:    cache,
		circuitBreaker: cb,
	}

	status := m.GetReadinessStatus(map[string]int{constant.Prefill: 1, constant.Decode: 1})
	assert.False(t, status.Ready)
	assert.Equal(t, "broken", status.StateName)
	assert.Equal(t, 4, status.State)
}

// TestGetReadinessStatus_CircuitBreakerClosed test circuit breaker closed state
func TestGetReadinessStatus_CircuitBreakerClosed(t *testing.T) {
	cb := NewCircuitBreaker(&config.CircuitBreakerConfig{
		Enable:               true,
		CheckWindowSec:       10,
		FailureRateThreshold: 0.5,
		MinRequestCount:      5,
	})
	defer cb.Close()
	cb.state = StateClosed

	// Use expired cache to ensure checkHealthCache returns nil
	cfg := &config.HealthCheckConfig{
		HealthCacheExpireTimeMs:  1,
		EventThresholdIntervalMs: 1000,
	}
	cache := NewInstanceHealthCache(nil, cfg)
	time.Sleep(5 * time.Millisecond)

	m := &InstanceManager{
		routeMapMutex:  sync.RWMutex{},
		requiredRoles:  []string{constant.Prefill, constant.Decode},
		healthCache:    cache,
		circuitBreaker: cb,
	}

	status := m.GetReadinessStatus(map[string]int{constant.Prefill: 1, constant.Decode: 1})
	assert.True(t, status.Ready)
	assert.Equal(t, "normal", status.StateName)
	assert.Equal(t, 3, status.State)
}

// ===== InstanceHealthCache Complete Scenario Tests =====

// TestInstanceHealthCache_GetOrFetchHealthStats_Expired test cache expiration and attempt to refresh from etcd
func TestInstanceHealthCache_GetOrFetchHealthStats_Expired(t *testing.T) {
	cfg := newTestHealthCheckConfig()
	cfg.HealthCacheExpireTimeMs = 1 // 1ms expiration
	cache := NewInstanceHealthCache(nil, cfg)

	// Initial update time is Now, expires immediately
	time.Sleep(2 * time.Millisecond)

	// clusterService is nil, refresh fails after expiration and returns nil
	stats := cache.GetOrFetchHealthStats()
	assert.Nil(t, stats)
}

// TestNewInstanceHealthCache_HealthCacheExpireTimeConfig test HealthCacheExpireTimeMs configuration
func TestNewInstanceHealthCache_HealthCacheExpireTimeConfig(t *testing.T) {
	// Explicitly specify HealthCacheExpireTimeMs
	cfg := &config.HealthCheckConfig{
		EventThresholdIntervalMs: 1000,
		HealthCacheExpireTimeMs:  200,
	}
	cache := NewInstanceHealthCache(nil, cfg)
	assert.Equal(t, time.Duration(200)*time.Millisecond, cache.expireTime)

	// When HealthCacheExpireTimeMs is 0, use default value 3*EventThresholdIntervalMs
	cfg2 := &config.HealthCheckConfig{
		EventThresholdIntervalMs: 1000,
		HealthCacheExpireTimeMs:  0,
	}
	cache2 := NewInstanceHealthCache(nil, cfg2)
	assert.Equal(t, time.Duration(3000)*time.Millisecond, cache2.expireTime)
}
