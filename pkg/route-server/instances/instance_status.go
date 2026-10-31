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

package instances

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"infer-dp-scheduler/pkg/common/cluster"
	"infer-dp-scheduler/pkg/common/config"
	"infer-dp-scheduler/pkg/common/constant"
	instance_metrics "infer-dp-scheduler/pkg/common/instance-metrics"
	"infer-dp-scheduler/pkg/common/logger"
	"infer-dp-scheduler/pkg/common/utils"
	"infer-dp-scheduler/pkg/route-server/cluster/singleton"

	"github.com/gin-gonic/gin"
)

type InstanceStatus struct {
	Status       string `json:"status"`
	URL          string `json:"url"`
	PodName      string `json:"pod_name"`
	Role         string `json:"role"`
	SLOTag       string `json:"slo_tag"`
	FaultMessage string `json:"fault message"`
	Alert        string `json:"alert"`
}

type InstanceStatusRsp struct {
	Status         string                      `json:"status"`
	InstanceStatus map[string][]InstanceStatus `json:"instance_status"` // role -> status
}

func (r InstanceStatusRsp) MarshalJSON() ([]byte, error) {
	// Create a new map to contain Status and modified InstanceStatus, remove instance_status layer, and add 's' after role name, e.g., prefill -> prefills
	result := make(map[string]interface{})

	// Specify serialization order, display status field first
	orderKeys := []string{"status"}

	for key, value := range r.InstanceStatus {
		newKey := key + "s"
		result[newKey] = value
	}
	for key := range result {
		orderKeys = append(orderKeys, key)
	}
	result["status"] = r.Status

	// Serialize result in orderKeys order
	var buf []byte
	buf = append(buf, '{')

	for i, key := range orderKeys {
		// Serialize key name
		keyBytes, err := utils.SonicApi.Marshal(key)
		if err != nil {
			return nil, fmt.Errorf("marshal key %s error: %v", key, err)
		}
		buf = append(buf, keyBytes...)
		buf = append(buf, ':')

		// Serialize value
		valueBytes, err := utils.SonicApi.Marshal(result[key])
		if err != nil {
			return nil, fmt.Errorf("marshal key %s value error: %v", key, err)
		}
		buf = append(buf, valueBytes...)

		// Add comma for non-last element
		if i < len(orderKeys)-1 {
			buf = append(buf, ',')
		}
	}

	buf = append(buf, '}')
	return buf, nil
}

func (r *InstanceStatusRsp) UnmarshalJSON(data []byte) error {
	// First parse JSON into temporary map (compatible with flattened structure)
	tmpMap := make(map[string]json.RawMessage)
	if err := utils.SonicApi.Unmarshal(data, &tmpMap); err != nil {
		return fmt.Errorf("unmarshal data error: %v", err)
	}

	// Extract status
	if status, exists := tmpMap["status"]; exists {
		if err := utils.SonicApi.Unmarshal(status, &r.Status); err != nil {
			return fmt.Errorf("unmarshal status error: %v", err)
		}
		delete(tmpMap, "status") // Remove processed key
	}

	// Assign remaining key-value pairs to InstanceStatus (restore original Map structure)
	r.InstanceStatus = make(map[string][]InstanceStatus, len(tmpMap))
	for role, instanceStatus := range tmpMap {
		// Parse []InstanceStatus
		var status []InstanceStatus
		if err := utils.SonicApi.Unmarshal(instanceStatus, &status); err != nil {
			return fmt.Errorf("unmarshal instance status error: %v", err)
		}
		r.InstanceStatus[role[:len(role)-1]] = status
	}
	return nil
}

func (m *InstanceManager) initInstanceStatusRsp() InstanceStatusRsp {
	rsp := InstanceStatusRsp{
		Status:         constant.RunningStatus,
		InstanceStatus: make(map[string][]InstanceStatus),
	}
	return rsp
}

func (m *InstanceManager) canService(rsp InstanceStatusRsp) bool {
	for _, role := range m.requiredRoles {
		roleInstanceNum := 0
		if instance, exist := rsp.InstanceStatus[role]; exist {
			roleInstanceNum += len(instance)
		}
		if roleInstanceNum == 0 {
			return false
		}
	}
	return true
}

func (m *InstanceManager) GetInstanceStatus(ctx *gin.Context) {
	rsp := m.initInstanceStatusRsp()
	m.routeMapMutex.RLock()
	defer func() {
		m.routeMapMutex.RUnlock()
		if rsp.Status == constant.FaultStatus {
			logger.Log().Warnf("[GET /status] Cluster status is %s, detail info recorded in status response", constant.FaultStatus)
		} else {
			logger.Log().Infof("[GET /status] %s", rsp.Status)
		}
	}()
	if len(m.availableMap) == 0 && len(m.unavailableMap) == 0 {
		rsp.Status = constant.DeployingStatus
		ctx.JSON(200, rsp)
		return
	}

	m.collectAvailableInstances(&rsp)

	// Check if service is available
	canService := m.canService(rsp)
	isFaultExists := false
	isDeployingExists := false

	for role := range m.unavailable {
		if !utils.InSlice(role, m.requiredRoles) {
			continue
		}

		for _, instance := range m.unavailable[role] {
			faultMessage := instance.FaultMessage
			if instance.Stats.GetStatus() == instance_metrics.Faulty {
				isFaultExists = true
			}
			if instance.Stats.GetStatus() == instance_metrics.Init {
				isDeployingExists = true
			}
			instanceStatus := constructInstanceStatus(instance.Stats.GetStatus().String(), instance.Client.GetEndpoint(), "", role, instance.Stats.GetSLOTag(), faultMessage, "")
			rsp.InstanceStatus[role] = append(rsp.InstanceStatus[role], instanceStatus)
		}
	}
	rsp.Status = getClusterStatus(isFaultExists, isDeployingExists, canService)
	ctx.JSON(200, rsp)
}

func (m *InstanceManager) collectAvailableInstances(rsp *InstanceStatusRsp) {
	for role := range m.available {
		if !utils.InSlice(role, m.requiredRoles) {
			continue
		}
		for _, instance := range m.available[role] {
			instanceStatus := constructInstanceStatus(constant.RunningStatus, instance.Client.GetEndpoint(), "", role, instance.Stats.GetSLOTag(), instance.FaultMessage, "")
			rsp.InstanceStatus[role] = append(rsp.InstanceStatus[role], instanceStatus)
		}
	}
}

// formatNotReadyMsg constructs not ready error message string
func (m *InstanceManager) formatNotReadyMsg(roleInstNum map[string]int, readiness map[string]int) string {
	formatMsgSlice := make([]string, len(m.requiredRoles))
	for i, role := range m.requiredRoles {
		formatMsgSlice[i] = fmt.Sprintf("%%d %s", role)
	}
	formatMsg := strings.Join(formatMsgSlice, ", ")

	requiredMap := make([]interface{}, len(m.requiredRoles))
	for i, role := range m.requiredRoles {
		requiredMap[i] = readiness[role]
	}
	requiredValues := fmt.Sprintf(formatMsg, requiredMap...)

	actualMap := make([]interface{}, len(m.requiredRoles))
	for i, role := range m.requiredRoles {
		actualMap[i] = roleInstNum[role]
	}
	actualValues := fmt.Sprintf(formatMsg, actualMap...)

	return fmt.Sprintf("the cluster isn't ready, %s required at least, but got %s available", requiredValues, actualValues)
}

func (m *InstanceManager) GetInstanceNum() map[string]int {
	m.routeMapMutex.RLock()
	defer m.routeMapMutex.RUnlock()

	roleInstNum := make(map[string]int)

	for _, inst := range m.allInstanceMap {
		role := inst.Stats.GetRole()
		if !utils.InSlice(role, m.requiredRoles) {
			continue
		}
		if _, exists := roleInstNum[role]; exists {
			roleInstNum[role]++
		} else {
			roleInstNum[role] = 1
		}
	}
	return roleInstNum
}

func getClusterStatus(isFaultExists bool, isDeployingExists bool, canService bool) string {
	var rspStatus string
	// Deploying:
	//    1. All P or D instances are init. Cluster is in deploying state
	//    2. Cannot serve externally if 1p1d condition is not met
	// Running:
	//    1. All P or D instances are healthy
	//    2. No instances can have fault or deploying status
	//    3. Can serve externally if at least 1p1d condition is met
	// Concerning:
	//    1. There are instances with fault or deploying status
	//    2. Can serve externally if at least 1p1d condition is met
	// Fault:
	//    1. Cannot serve externally if 1p1d condition is not met
	//    2. Some instances have fault
	if !isFaultExists && !isDeployingExists && canService {
		rspStatus = constant.RunningStatus
	} else if canService {
		rspStatus = constant.ConcerningStatus
	} else if !isFaultExists && isDeployingExists {
		// All instances have no fault, must have deploying, and cannot serve externally, considered as deploy state
		rspStatus = constant.DeployingStatus
	} else {
		// Cannot provide service and has fault, considered as cluster fault state
		rspStatus = constant.FaultStatus
	}
	return rspStatus
}

// InstanceHealthStats instance health statistics information
type InstanceHealthStats struct {
	RoleHealthyCounts map[string]int `json:"role_healthy_counts"` // role -> healthy count
	UpdateTime        time.Time      `json:"update_time"`         // update timestamp
}

// InstanceHealthCache instance health cache manager
type InstanceHealthCache struct {
	cache          *InstanceHealthStats   // cached health statistics
	mutex          sync.RWMutex           // RW lock
	clusterService cluster.ClusterService // ETCD get information service
	lastSync       time.Time              // last sync time
	expireTime     time.Duration          // cache expire time
}

// NewInstanceHealthCache create instance health cache
func NewInstanceHealthCache(clusterService cluster.ClusterService, healthCfg *config.HealthCheckConfig) *InstanceHealthCache {
	expireTime := 3 * time.Duration(healthCfg.EventThresholdIntervalMs) * time.Millisecond
	if healthCfg.HealthCacheExpireTimeMs != 0 {
		expireTime = time.Duration(healthCfg.HealthCacheExpireTimeMs) * time.Millisecond
	}

	logger.Log().Infof("NewInstanceHealthCache, expireTime: %v", expireTime)

	return &InstanceHealthCache{
		cache: &InstanceHealthStats{
			RoleHealthyCounts: make(map[string]int),
			UpdateTime:        time.Now(),
		},
		clusterService: clusterService,
		expireTime:     expireTime,
		lastSync:       time.Now(),
	}
}

// GetOrFetchHealthStats get healthy instance statistics information
func (c *InstanceHealthCache) GetOrFetchHealthStats() *InstanceHealthStats {
	// First check if cache is expired
	c.mutex.RLock()
	if time.Since(c.cache.UpdateTime) <= c.expireTime {
		result := c.cache
		c.mutex.RUnlock()
		return result
	}
	c.mutex.RUnlock()

	logger.Log().Warnf("Health stats cache expired, last update: %v, expire: %v",
		c.cache.UpdateTime, c.expireTime)

	// Read lock released, can safely get write lock
	etcdCache := c.GetHealthStatsFromClusterService()
	if etcdCache == nil {
		logger.Log().Errorf("Failed to get health stats from etcd or expired")
		return nil
	}

	// Update cache (internally gets write lock), using EveryRoleAvailableCountMap and UpdateTime from etcd
	err := c.UpdateHealthStats(etcdCache.EveryRoleAvailableCountMap, &etcdCache.UpdateTime)
	if err != nil {
		logger.Log().Debugf("Failed to update health stats: %v", err)
		return nil
	}

	// Read again and return
	c.mutex.RLock()
	result := c.cache
	c.mutex.RUnlock()
	return result
}

// UpdateHealthStats update healthy instance statistics information
func (c *InstanceHealthCache) UpdateHealthStats(roleHealthyCounts map[string]int, updateTime *time.Time) error {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	// Update cache
	c.cache.RoleHealthyCounts = make(map[string]int)
	for role, count := range roleHealthyCounts {
		c.cache.RoleHealthyCounts[role] = count
	}
	if updateTime != nil {
		c.cache.UpdateTime = *updateTime
	} else {
		c.cache.UpdateTime = time.Now()
	}

	logger.Log().Tracef("Updated health stats: %+v", c.cache.RoleHealthyCounts)
	return nil
}

func (c *InstanceHealthCache) GetHealthStatsFromClusterService() *cluster.EtcdHealthStatsCache {
	if c.clusterService == nil {
		logger.Log().Error("cluster service is nil, cannot refresh health stats")
		return nil
	}

	// Get health statistics through clusterService
	cache, err := c.clusterService.GetHealthStats()
	if err != nil {
		logger.Log().Errorf("Failed to get health stats from clusterService: %v", err)
		return nil
	}

	// Check if result is valid (UpdateTime zero value means invalid/uninitialized)
	if cache.UpdateTime.IsZero() {
		logger.Log().Warn("Health stats from clusterService is invalid (zero update time)")
		return nil
	}

	return &cache
}

// ResetCache reset cache
func (c *InstanceHealthCache) ResetCache() {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	c.cache.RoleHealthyCounts = make(map[string]int)
	c.cache.UpdateTime = time.Time{}

	logger.Log().Info("Health stats cache reset")
}

func constructInstanceStatus(status string, uri string, podName string, role string, sloTag string, faultMessage string, alert string) InstanceStatus {
	return InstanceStatus{
		Status:       status,
		URL:          uri,
		PodName:      podName,
		Role:         role,
		SLOTag:       sloTag,
		FaultMessage: faultMessage,
		Alert:        alert,
	}
}

// GetReadinessStatus return detailed readiness status information
func (m *InstanceManager) GetReadinessStatus(readiness map[string]int) ReadinessStatus {
	// 1. Check force mode (highest priority)
	if status := m.checkForceMode(); status != nil {
		return *status
	}

	// 2. Try to get instance information from health cache
	if status := m.checkHealthCache(readiness); status != nil {
		return *status
	}

	// 3. No valid information, try third level circuit breaker judgment
	if m.circuitBreaker != nil {
		return m.checkCircuitBreaker()
	}

	// 4. Circuit breaker not enabled and no valid information
	return ReadinessStatus{
		Ready:   false,
		Message: "no valid instance info and circuit breaker not enabled",
	}
}

// checkForceMode check force mode, return nil means non-force mode needs to continue
func (m *InstanceManager) checkForceMode() *ReadinessStatus {
	if m.circuitBreaker == nil {
		return nil
	}

	switch m.circuitBreaker.GetForceMode() {
	case ForceModeTrue:
		return &ReadinessStatus{
			Ready:     true,
			Message:   "ready",
			State:     1,
			StateName: "force_true",
		}
	case ForceModeFalse:
		return &ReadinessStatus{
			Ready:     false,
			Message:   "force not ready",
			State:     2,
			StateName: "force_false",
		}
	default:
		return nil // ForceModeNone, continue
	}
}

// checkHealthCache check health cache, return nil means cache invalid needs to continue
func (m *InstanceManager) checkHealthCache(readiness map[string]int) *ReadinessStatus {
	// In standalone mode (SingletonClusterService), health check information is directly obtained from InstanceManager's available
	// Because worker itself is also leader, its own information is the latest valid information
	if _, ok := m.clusterService.(*singleton.SingletonClusterService); ok {
		return m.checkHealthFromAvailable(readiness)
	}

	stats := m.healthCache.GetOrFetchHealthStats()
	if stats == nil {
		return nil
	}

	// Has valid information, restore circuit breaker state
	if m.circuitBreaker != nil {
		m.circuitBreaker.Reset()
	}

	// Check if healthy instance count for each role meets requirements
	ready := true
	for _, role := range m.requiredRoles {
		leastNum := readiness[role]
		if instNum := stats.RoleHealthyCounts[role]; instNum < leastNum {
			ready = false
			break
		}
	}

	if ready {
		return &ReadinessStatus{
			Ready:     true,
			Message:   "ready",
			State:     3,
			StateName: "normal",
		}
	}

	return &ReadinessStatus{
		Ready:     false,
		Message:   m.formatNotReadyMsg(stats.RoleHealthyCounts, readiness),
		State:     3,
		StateName: "normal",
	}
}

// checkHealthFromAvailable directly get health statistics from InstanceManager's available
func (m *InstanceManager) checkHealthFromAvailable(readiness map[string]int) *ReadinessStatus {
	m.routeMapMutex.RLock()

	roleHealthyCounts := make(map[string]int, len(m.requiredRoles))
	for _, role := range m.requiredRoles {
		if roleInstances, exists := m.available[role]; exists {
			roleHealthyCounts[role] = len(roleInstances)
		} else {
			roleHealthyCounts[role] = 0
		}
	}

	m.routeMapMutex.RUnlock()

	// Restore circuit breaker state (routeMapMutex released, avoid calling Reset while holding lock)
	if m.circuitBreaker != nil {
		m.circuitBreaker.Reset()
	}

	// Check if healthy instance count for each role meets requirements
	ready := true
	for _, role := range m.requiredRoles {
		leastNum := readiness[role]
		if instNum := roleHealthyCounts[role]; instNum < leastNum {
			ready = false
			break
		}
	}

	if ready {
		return &ReadinessStatus{
			Ready:     true,
			Message:   "ready",
			State:     3,
			StateName: "normal",
		}
	}

	return &ReadinessStatus{
		Ready:     false,
		Message:   m.formatNotReadyMsg(roleHealthyCounts, readiness),
		State:     3,
		StateName: "normal",
	}
}

// checkCircuitBreaker check circuit breaker state
func (m *InstanceManager) checkCircuitBreaker() ReadinessStatus {
	// Activate circuit breaker: mark system as degraded to Level 3, enabling timer and probe-triggered failure rate checks
	m.circuitBreaker.SetDegraded(true)
	shouldBreak := m.circuitBreaker.CheckShouldTrip()
	if shouldBreak {
		return ReadinessStatus{
			Ready:     false,
			Message:   "circuit breaker is open, service degraded due to high failure rate",
			State:     4,
			StateName: "broken",
		}
	}

	return ReadinessStatus{
		Ready:     true,
		Message:   "degraded service (circuit breaker closed)",
		State:     3,
		StateName: "normal",
	}
}
