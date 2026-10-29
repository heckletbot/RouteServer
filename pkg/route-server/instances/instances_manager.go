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
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"infer-dp-scheduler/cmd/route_server/server/options"
	"infer-dp-scheduler/pkg/common/errs"

	"infer-dp-scheduler/pkg/common/client"
	instanceclient "infer-dp-scheduler/pkg/common/client/instance-client"
	monitorclient "infer-dp-scheduler/pkg/common/client/monitor-client"
	"infer-dp-scheduler/pkg/common/cluster"
	"infer-dp-scheduler/pkg/common/config"
	"infer-dp-scheduler/pkg/common/constant"
	"infer-dp-scheduler/pkg/common/health"
	http2 "infer-dp-scheduler/pkg/common/http"
	instance_metrics "infer-dp-scheduler/pkg/common/instance-metrics"
	"infer-dp-scheduler/pkg/common/logger"
	"infer-dp-scheduler/pkg/common/openai"
	"infer-dp-scheduler/pkg/common/utils"
	"infer-dp-scheduler/pkg/route-server/metrics"
	"infer-dp-scheduler/pkg/route-server/scheduler"

	"github.com/gin-gonic/gin"
)

const (
	MinScheduleStatsSyncInterval    = 10                                    // minimum interval allowed for syncing stats to scheduler manager
	StatsSyncWarnThresholds         = time.Duration(100) * time.Millisecond // ms to print warning log for long sync
	InstanceFailureCheckIntervalSec = 5

	SnapshotReasonFailureChanged = "failure instances changed"
	SnapshotReasonTimeRenewed    = "failure time renewed"
	SnapshotReasonNoChanges      = "no changes"
)

var headerSet = map[string]interface{}{
	constant.HeadersContentType: nil,
	constant.HeadersXRequestID:  nil,
	constant.HeadersXSpanId:     nil,
}

type Manager interface {
	health.LeaderEventListener
	InitHealthyModule(
		testInstanceFunc func(ctx *gin.Context,
			path string, isChat bool, instance *client.Instance) (successToCheck bool,
			testSuccess bool, errDetails *errs.RSError),
		clusterService cluster.ClusterService, pdBlacklistManager *health.PDLinkBlacklistManager)
	GetInstanceClient(id string) (instanceclient.InstanceClient, error)
	Stop()
	GetAllInstance() []*config.Route
	SnapshotUnhealthyInstances()
	GetCurrentStage() string
	GetBroadCastURIWhitelist() []config.WhitelistEntry
	HandleBroadcastInstanceRequest(ctx *gin.Context)
	GetInstanceStatus(ctx *gin.Context)
	HandleProxyToSingleInstance(ctx *gin.Context)
	HandleHealthCheck(ctx *gin.Context)
	HandleForceReadiness(ctx *gin.Context)
	GetReadinessStatus(readiness map[string]int) ReadinessStatus
	GetInstanceNum() map[string]int
	IsInstanceAvailable(name string) bool
}

// ReadinessStatus contains detailed status information for readiness check
type ReadinessStatus struct {
	Ready     bool   `json:"ready"`                // whether ready
	Message   string `json:"message"`              // status message
	State     int    `json:"state,omitempty"`      // value of worker_readiness_state
	StateName string `json:"state_name,omitempty"` // status name: disabled, force_true, force_false, normal, broken
}

type InstanceManager struct {
	cfg     *config.InstanceManagerCfg
	httpCfg *config.HttpConfig

	leaderHealthChecker *health.LeaderHealthChecker

	// routeMapMutex for instance maps
	// mutex priority should be allMutex -> routeMapMutex -> syncMutex
	routeMapMutex sync.RWMutex
	// changing following 5 map should happen in the routeMapMutex
	// if there comes a new role, you should initialize following maps
	// once all instances of a role is removed, delete key[role] from following maps
	available        map[string]map[string]*client.Instance // role -> id -> instance
	availableMap     map[string]*client.Instance
	availableStats   map[string]*atomic.Value               // role -> stats
	unavailable      map[string]map[string]*client.Instance // role -> id -> instance
	unavailableMap   map[string]*client.Instance
	unavailableStats map[string]*atomic.Value // role -> stats
	roleInstanceSize map[string]int           // role -> instanceNum
	roleLookUp       map[string]string        // id -> role
	lastWarningTime  time.Time
	allInstances     []*client.Instance
	allInstanceMap   map[string]*client.Instance
	requiredRoles    []string

	// health check properties
	activeCheckPeriod          time.Duration
	activeCheckPeriodNonLeader time.Duration
	checkBatchSize             int
	checkConcurrency           int
	stopChan                   chan struct{}
	exitingWaitGroup           sync.WaitGroup

	schedulerManager scheduler.Manager

	// for monitor stats sync, all stats will be copied before given to scheduler.
	syncMutex     sync.RWMutex
	monitorClient monitorclient.MonitorClient
	seenRoles     []string

	currentStage string

	isPureWorker   bool
	isMeLeader     atomic.Bool
	instanceLeaved atomic.Bool

	failureSnapshotMutex  sync.RWMutex
	lastSnapshotRecords   map[string]*utils.InstanceFailureRecord
	lastSnapshotTimestamp time.Time
	failureSnapshotFunc   func(records map[string]*utils.InstanceFailureRecord) error

	instanceDownMsgChan chan<- interface{}

	// used for transparent transmission of requests
	resHeaderBlackSet map[string]bool

	// Circuit breaker module
	circuitBreaker *CircuitBreaker

	// Instance health cache module
	healthCache *InstanceHealthCache

	// Cluster service
	clusterService cluster.ClusterService
}

func (m *InstanceManager) GetAvailable() map[string]map[string]*client.Instance {
	m.routeMapMutex.RLock()
	defer m.routeMapMutex.RUnlock()

	result := make(map[string]map[string]*client.Instance, len(m.available))
	for role, instances := range m.available {
		roleCopy := make(map[string]*client.Instance, len(instances))
		for id, inst := range instances {
			roleCopy[id] = inst
		}
		result[role] = roleCopy
	}
	return result
}

// GetHealthStatsUpdater gets the health statistics updater
func (m *InstanceManager) GetHealthStatsUpdater() health.HealthStatsUpdater {
	return m.healthCache
}

func (m *InstanceManager) GetName() string {
	return "InstanceManager"
}

func (m *InstanceManager) GetCurrentStage() string {
	return m.currentStage
}

func (m *InstanceManager) GetBroadCastURIWhitelist() []config.WhitelistEntry {
	return m.cfg.URIWhitelist
}

func (m *InstanceManager) GetInstanceClient(id string) (instanceclient.InstanceClient, error) {
	m.routeMapMutex.RLock()
	defer m.routeMapMutex.RUnlock()
	return m.getInstanceClientNoLock(id)
}

func (m *InstanceManager) getInstanceClientNoLock(id string) (instanceclient.InstanceClient, error) {
	if instance, exists := m.allInstanceMap[id]; !exists {
		err := fmt.Errorf("missing role lookup for instance %v when getting client", id)
		logger.Log().Error(err.Error())
		return nil, err
	} else {
		return instance.Client, nil
	}
}

// getAllInstanceStatsNoLock should always operate under routeMapMutex
func (m *InstanceManager) getAllInstanceStatsNoLock() map[string]map[string]instance_metrics.WorkerStats {
	allInstancesStats := make(map[string]map[string]instance_metrics.WorkerStats, len(m.seenRoles))

	// categorized by role
	for _, role := range m.seenRoles {
		roleStats := make(map[string]instance_metrics.WorkerStats, len(m.allInstances))
		for _, instance := range m.allInstances {
			if instance.Stats.GetRole() != role {
				continue
			}
			roleStats[instance.ID] = instance.Stats
		}
		allInstancesStats[role] = roleStats
	}

	return allInstancesStats
}

func (m *InstanceManager) GetAllInstance() []*config.Route {
	m.routeMapMutex.RLock()
	defer m.routeMapMutex.RUnlock()
	routesList := make([]*config.Route, 0, len(m.allInstances))
	for _, instance := range m.allInstances {
		routesList = append(routesList, &config.Route{
			Role: instance.Stats.GetRole(),
			Ip:   instance.IP,
			Port: instance.Port,
		})
	}
	return routesList
}

func (m *InstanceManager) Stop() {
	logger.Log().Infof("instance manager stopping, waiting for health loop to stop")
	close(m.stopChan)
	m.leaderHealthChecker.Stop()
	m.exitingWaitGroup.Wait()

	// Close circuit breaker timer
	if m.circuitBreaker != nil {
		m.circuitBreaker.Close()
	}

	// TODO: close client and clear all maps.
	m.currentStage = "STOPPED"
	logger.Log().Infof("instance manager stopped")
}

func (m *InstanceManager) instanceWarningLoop() {
	defer m.exitingWaitGroup.Done()
	if m.isPureWorker {
		logger.Log().Infof("pure worker, stop unhealthy warning loop")
		return
	}
	reportInterval := time.Duration(10) * time.Second
	ticker := time.NewTicker(reportInterval)
	defer ticker.Stop()
	for {
		select {
		case <-m.stopChan:
			logger.Log().Info("instance warning loop stop due to stopChan")
			return
		case <-ticker.C:
			m.warnUnhealthy()
		}
	}
}

func (m *InstanceManager) consecutiveFailuresInstanceWatcher() {
	defer m.exitingWaitGroup.Done()
	if m.isPureWorker {
		logger.Log().Infof("pure worker, stop instance consecutive failures watcher")
		return
	}
	if options.GetConfig().MetaCleanupCfg == nil || options.GetConfig().MetaCleanupCfg.MaxInstanceDownTimeSec <= 0 {
		logger.Log().Warnf(
			"instance manager stop instance consecutive failures watcher due to non-positive MaxInstanceDownTimeSec")
		return
	}
	checkInterval := time.Duration(InstanceFailureCheckIntervalSec) * time.Second

	if m.cfg.HealthCheckConfig == nil || m.cfg.HealthCheckConfig.RestHealthCommonConfig.Interval <= 0 {
		logger.Log().Errorf(
			"instance manager stop instance consecutive failures watcher due to non-positive health check interval-ms")
		return
	}
	healthCheckIntervalMs := m.cfg.HealthCheckConfig.RestHealthCommonConfig.Interval
	maxDownTimeSec := options.GetConfig().MetaCleanupCfg.MaxInstanceDownTimeSec
	// ceil value
	maxConsecutiveUnreachableTime := int32(((maxDownTimeSec * 1000) + healthCheckIntervalMs - 1) / healthCheckIntervalMs)
	if maxConsecutiveUnreachableTime <= 6 {
		resetDuration := time.Duration(6*healthCheckIntervalMs) * time.Millisecond
		logger.Log().Warnf("route server worker exist check time is reset to [%v] ms due to too short",
			resetDuration)
		maxConsecutiveUnreachableTime = 6
		maxDownTimeSec = int(resetDuration.Seconds())
	}
	logger.Log().Infof("start consecutive unreachable watcher, max consecutive time is [%v]", maxConsecutiveUnreachableTime)
	ticker := time.NewTicker(checkInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if !m.isMeLeader.Load() {
				continue
			}
			m.routeMapMutex.RLock()
			toRemove := make([]string, 0)
			for insName, instance := range m.unavailableMap {
				if instance.Stats.GetStatus() != instance_metrics.Faulty {
					continue
				}
				if instance.ConsecutiveHealthUnreachable >= maxConsecutiveUnreachableTime {
					toRemove = append(toRemove, insName)
				}
			}
			m.routeMapMutex.RUnlock()
			if len(toRemove) == 0 {
				continue
			}
			logger.Log().Warnf(
				"removing failed instances %v for consective GET /health failures over [%v] times in at least [%v] sec, see leader_health for when this instance is set to unhealthy first time",
				toRemove, maxConsecutiveUnreachableTime, maxDownTimeSec)
			leaveRequest := &cluster.InstanceLeaveRequest{
				InstanceNames: toRemove,
			}
			select {
			case m.instanceDownMsgChan <- leaveRequest:
			default:
				logger.Log().Warnf("failed to send instance leave event for [%v] due to channel closed", leaveRequest.InstanceNames)
			}
		case <-m.stopChan:
			logger.Log().Info("instance manager stop failure syncing watcher due to stopChan")
			return
		}
	}
}

func (m *InstanceManager) statsSyncLoop() {
	defer m.exitingWaitGroup.Done()
	if m.isPureWorker {
		logger.Log().Infof("pure worker, stop state sync loop")
		return
	}
	minInterval := m.schedulerManager.MinStatsSyncInterval()

	if minInterval <= 0 {
		logger.Log().Infof("scheduler MinStatsSyncInterval return %v, only sync the stats once whenever nodes changed",
			minInterval)
		// update once if no periodically sync is needed
		m.doStatsSync()
		logger.Log().Infof("instance manager stop syncing due to no needs to sync stats")
		return
	}

	if minInterval < MinScheduleStatsSyncInterval {
		logger.Log().Warn(fmt.Sprintf(
			"got min stats sync interval %v, which smaller than miniumn specs %v ms, reset to %v ms",
			minInterval, MinScheduleStatsSyncInterval, MinScheduleStatsSyncInterval))
		minInterval = MinScheduleStatsSyncInterval
	}

	logger.Log().Infof("Start to sync instances stats every %v ms", minInterval)

	m.doStatsSync()
	ticker := time.NewTicker(time.Duration(minInterval) * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if m.isMeLeader.Load() {
				m.doStatsSync()
			} else {
				logger.Log().Trace("skip auto stats syncing due to not leader")
			}
		case <-m.stopChan:
			logger.Log().Info("instance manager stop sync due to stopChan")
			ticker.Stop()
			return
		}
	}
}

func (m *InstanceManager) leaderDealWithHealthChanges(toAvailable []*client.Instance, toUnavailable []*client.Instance) error {
	if m.isMeLeader.Load() {
		m.dealWithHealthChanges(toAvailable, toUnavailable)
		return nil
	}
	return fmt.Errorf("not leader right now")
}

func (m *InstanceManager) dealWithHealthChanges(toAvailable []*client.Instance, toUnavailable []*client.Instance) {
	m.routeMapMutex.Lock()
	defer m.routeMapMutex.Unlock()
	actualChanged := false
	for _, instance := range toAvailable {
		changed := m.switchToAvailable(instance)
		actualChanged = actualChanged || changed
	}

	for _, instance := range toUnavailable {
		changed := m.switchToUnavailable(instance)
		actualChanged = actualChanged || changed
	}

	if actualChanged {
		m.healthChangesSync()
		logger.Log().Infof("healthy changed, total %v instances, move %v to available, move %v to unavailable",
			len(m.allInstances), len(toAvailable), len(toUnavailable))
	}
}

// collect stats from all available instances, inform the scheduler to update worker stats
// this func should operate under routeMapMutex (at least read lock)
func (m *InstanceManager) healthChangesSync() {
	for role, roleInstances := range m.available {
		metrics.SetHealthyEndpoint(role, len(roleInstances))
	}

	metrics.ResetFaultyEndpoint()
	for role, roleInstances := range m.unavailable {
		for _, instance := range roleInstances {
			metrics.IncFaultyEndpoint(role, instance.FaultType)
		}
	}

	for _, role := range m.seenRoles {
		newAvailableRoleStats := make(map[string]instance_metrics.WorkerStats, len(m.available[role]))
		for id, instance := range m.available[role] {
			newAvailableRoleStats[id] = instance.Stats
		}
		m.availableStats[role].Store(&newAvailableRoleStats)

		newUnavailableRoleStats := make(map[string]instance_metrics.WorkerStats, len(m.unavailable[role]))
		for id, instance := range m.unavailable[role] {
			newUnavailableRoleStats[id] = instance.Stats
		}
		m.unavailableStats[role].Store(&newUnavailableRoleStats)
	}
	go m.doStatsSync()
}

// switch instance to available, must operate under write lock of routeMapMutex
func (m *InstanceManager) switchToAvailable(instance *client.Instance) bool {
	role := instance.Stats.GetRole()
	changed := false
	if _, exists := m.unavailable[role][instance.ID]; exists {
		delete(m.unavailable[role], instance.ID)
		delete(m.unavailableMap, instance.ID)
		m.available[role][instance.ID] = instance
		m.availableMap[instance.ID] = instance
		logger.Log().Infof("instance %v is back online due to healthcheck success, previously in %s status",
			instance.ID, instance.Stats.GetPreStatus().String())
		instance.Stats.SetStatus(instance_metrics.Available)
		atomic.StoreInt32(&instance.HealthCheckCountAfterUnhealthy, 0)
		changed = true
	}
	return changed
}

// switch instance to unavailable, must operate under write lock of routeMapMutex
func (m *InstanceManager) switchToUnavailable(instance *client.Instance) bool {
	role := instance.Stats.GetRole()
	changed := false
	if _, exists := m.available[role][instance.ID]; exists {
		delete(m.available[role], instance.ID)
		delete(m.availableMap, instance.ID)
		m.unavailable[role][instance.ID] = instance
		m.unavailableMap[instance.ID] = instance
		if instance.Stats.GetStatus() != instance_metrics.Init {
			instance.Stats.SetStatus(instance_metrics.Faulty)
			logger.Log().Errorf("health check for previously available instance %v failed, "+
				"moving it to unavailable. err = %v", instance.ID, instance.FaultMessage)
		}
		changed = true
	}
	return changed
}

func (m *InstanceManager) warnUnhealthy() {
	if !m.isMeLeader.Load() {
		return
	}
	for role, roleInstances := range m.available {
		if len(roleInstances) == 0 {
			logger.Log().Errorf("zero instance is healthy for role %v", role)
		}
	}
	for role, unhealthyInstances := range m.unavailable {
		if len(unhealthyInstances) == 0 {
			continue
		}
		namesBuilder := strings.Builder{}
		for name, ins := range unhealthyInstances {
			namesBuilder.WriteString(fmt.Sprintf("<%s,%s,%v,%v>", name, ins.Stats.GetStatus(),
				ins.ConsecutiveHealthFailed, ins.ConsecutiveHealthUnreachable))
			namesBuilder.WriteRune(',')
		}
		logger.Log().Warnf("unhealthy instances found for role [%s]: [%s]", role, namesBuilder.String())
	}
}

// Execute stats sync from monitor, and update to scheduler.
// This should never be called insides of routeMapMutex, because syncer will lock this mutex.
func (m *InstanceManager) doStatsSync() {
	m.routeMapMutex.RLock()
	defer m.routeMapMutex.RUnlock()
	m.syncMutex.Lock()
	defer m.syncMutex.Unlock()
	start := time.Now()
	logger.Log().Tracef("getting stats from Monitor")

	var monitorStats map[string]map[string]*monitorclient.MonitorStats
	var scanLatency time.Duration
	if m.monitorClient != nil {
		var err error
		monitorStats, err = m.monitorClient.GetMonitorStats(constant.MetricsKeyPrefix, m.seenRoles, m.available)
		if err != nil {
			logger.Log().Errorf("failed to sync stats from monitor, err = %v", err)
			return
		}
		scanLatency = time.Since(start)
	} else {
		monitorStats = make(map[string]map[string]*monitorclient.MonitorStats)
		scanLatency = time.Duration(0)
	}

	availableMapToSync := make(map[string]map[string]instance_metrics.WorkerStats, len(m.seenRoles))
	for _, role := range m.seenRoles {
		roleAvailableStats, ok := m.availableStats[role].Load().(*map[string]instance_metrics.WorkerStats)
		if !ok {
			logger.Log().Warnf("failed to convert available WorkerStats map, Can't sync stats to role %s", role)
			continue
		}
		availableMapToSync[role] = make(map[string]instance_metrics.WorkerStats, len(*roleAvailableStats))
		// update m.availableStats[role][id]->*WorkerStats
		for id, stat := range *roleAvailableStats {
			monitorStat, timeStamp := findMonitorStats(monitorStats, role, id)
			availableMapToSync[role][id] = stat.UpdateNumericStats(monitorStat, timeStamp)
		}
	}

	unavailableMapToSync := make(map[string]map[string]instance_metrics.WorkerStats, len(m.seenRoles))
	for _, role := range m.seenRoles {
		roleUnavailableStats, ok := m.unavailableStats[role].Load().(*map[string]instance_metrics.WorkerStats)
		if !ok {
			logger.Log().Warnf("failed to convert unavailable WorkerStats map, Can't sync stats to role %s", role)
			continue
		}
		unavailableMapToSync[role] = make(map[string]instance_metrics.WorkerStats, len(*roleUnavailableStats))
		// do not update, just read for unavailable
		for id, stat := range *roleUnavailableStats {
			unavailableMapToSync[role][id] = stat.GetWorkerStats()
		}
	}
	updateStart := time.Now()

	m.schedulerManager.UpdateStats(availableMapToSync, unavailableMapToSync)
	m.schedulerManager.UpdateFallbackAlgoStats(m.getAllInstanceStatsNoLock())

	updateLatency := time.Since(updateStart)
	totalLatency := time.Since(start)
	if totalLatency > StatsSyncWarnThresholds {
		logger.Log().Warnf(
			"monitor scan stats for %v ms. (Details: scan from monitor for %v ms, update to schedulers for %v ms",
			totalLatency.Milliseconds(), scanLatency.Milliseconds(), updateLatency.Milliseconds())
	}
}

func findMonitorStats(stats map[string]map[string]*monitorclient.MonitorStats,
	role string, id string) (*map[string][]*instance_metrics.MetricInfo, time.Time) {
	if statsMap, exists := stats[role]; exists {
		if instanceMonitorStat, exists := statsMap[id]; exists {
			return instanceMonitorStat.Metrics, instanceMonitorStat.TimeStamp
		}
	}
	return nil, time.Time{}
}

func (m *InstanceManager) OnStateRestored(state *cluster.ClusterState) {
	toRemove, toAdd := m.findRestoreInstanceChanges(state)
	if len(toRemove) > 0 || len(toAdd) > 0 {
		logger.Log().Infof("restoring instance from cluster state, adding new instances [%s], removing [%v]",
			utils.MapKeysStr(toAdd), toRemove)
		if len(toRemove) > 0 {
			m.OnRemoveInstance(toRemove, constant.InstanceChangeReasonRestored)
		}

		if len(toAdd) > 0 {
			m.OnAddInstance(toAdd, constant.InstanceChangeReasonRestored)
		}
		logger.Log().Infof("cluster state restored, found [%v] instance removed and [%v] instance added",
			len(toRemove), len(toAdd))
	}

	knownActiveWorkers := m.schedulerManager.GetAllRemoteWorkerIds()
	removedWorkers := m.findRemovedWorkers(state, knownActiveWorkers)
	for _, removedWorkerId := range removedWorkers {
		m.OnRemovePeer(removedWorkerId)
	}

	m.routeMapMutex.RLock()
	m.schedulerManager.UpdateFallbackAlgoStats(m.getAllInstanceStatsNoLock())
	m.routeMapMutex.RUnlock()
}

func (m *InstanceManager) findRemovedWorkers(state *cluster.ClusterState, knownActiveWorkers []string) []string {
	if state == nil || state.Peers == nil {
		return knownActiveWorkers
	}
	removed := make([]string, 0)
	for _, workerId := range knownActiveWorkers {
		if _, exists := state.Peers[workerId]; !exists {
			removed = append(removed, workerId)
		}
	}
	return removed
}

func (m *InstanceManager) findRestoreHealthChanges(failures map[string]*utils.InstanceFailureRecord) (toAvailable, toUnavailable []*client.Instance) {
	m.routeMapMutex.RLock()
	defer m.routeMapMutex.RUnlock()
	toAvailable = make([]*client.Instance, 0)
	toUnavailable = make([]*client.Instance, 0)
	for resumedFailedId, failureDetail := range failures {
		instance, exists := m.allInstanceMap[resumedFailedId]
		if !exists {
			continue
		}
		resetFailedInstanceFromRecord(failureDetail, instance)
		toUnavailable = append(toUnavailable, instance)
		logger.Log().Infof("resume instance [%s] to unavailable, reason %s", resumedFailedId, failureDetail.Reason)
	}
	for localFailedId, localFailedInstance := range m.unavailableMap {
		if failureDetail, localContains := failures[localFailedId]; !localContains {
			toAvailable = append(toAvailable, localFailedInstance)
			localFailedInstance.FaultMessage = "resume to health from state"
			localFailedInstance.HealthCheckCountAfterUnhealthy = 0
			logger.Log().Infof("resume instance [%s] to available because it's not found in resumed failures anymore", localFailedId)
		} else {
			if localFailedInstance.Stats.GetStatus() == instance_metrics.Faulty {
				logger.Log().Debugf(
					"changed local failed instance [%s] reason to [%s] due to found in resumed failures, previous reason [%s]",
					localFailedId, failureDetail.Reason, localFailedInstance.FaultMessage)
				localFailedInstance.FaultMessage = maybeResumedReason(failureDetail.Reason)
				localFailedInstance.HealthCheckCountAfterUnhealthy = failureDetail.HealthCheckCountAfterUnhealthy
			} else {
				logger.Log().Debugf("local deploying instance [%s] stays the same during failures restore", localFailedId)
			}
		}
	}
	return toAvailable, toUnavailable
}

func resetFailedInstanceFromRecord(failureDetail *utils.InstanceFailureRecord, instance *client.Instance) {
	status := instance_metrics.InstanceStatusFromString(failureDetail.Status)
	if status == instance_metrics.Faulty {
		instance.Stats.SetStatus(status)
		instance.FaultMessage = maybeResumedReason(failureDetail.Reason)
		instance.LastError = fmt.Errorf("resumed err = %s", instance.FaultMessage)
		instance.ConsecutiveHealthUnreachable = failureDetail.ConsecutiveUnreachableTime
		instance.ConsecutiveHealthFailed = failureDetail.ConsecutiveFailureTime
		instance.ConsecutiveHealthSuccess = 0
		instance.HealthCheckCountAfterUnhealthy = failureDetail.HealthCheckCountAfterUnhealthy
	} else if status == instance_metrics.Init {
		instance.Stats.SetStatus(status)
		instance.FaultMessage = "[resume] deploying"
		instance.LastError = nil
		instance.ConsecutiveHealthSuccess = 0
		instance.HealthCheckCountAfterUnhealthy = 0
	} else {
		instance.Stats.SetStatus(instance_metrics.Faulty)
		instance.LastError = fmt.Errorf("status is running but in failures snapshots, resumed as faulty, err = %s", instance.FaultMessage)
		instance.FaultMessage = instance.LastError.Error()
		instance.ConsecutiveHealthUnreachable = failureDetail.ConsecutiveUnreachableTime
		instance.ConsecutiveHealthFailed = failureDetail.ConsecutiveFailureTime
		instance.ConsecutiveHealthSuccess = 0
		instance.HealthCheckCountAfterUnhealthy = failureDetail.HealthCheckCountAfterUnhealthy
	}
}

func maybeResumedReason(resumedReason string) string {
	resumePrefix := "[resumed] "
	if strings.HasPrefix(resumedReason, resumePrefix) {
		return resumedReason
	} else {
		return resumePrefix + resumedReason
	}
}

func (m *InstanceManager) findRestoreInstanceChanges(state *cluster.ClusterState) (toRemove []string, toAdd map[string]*config.Route) {
	m.routeMapMutex.RLock()
	defer m.routeMapMutex.RUnlock()
	allRestoredRoutes := state.InstanceRoutes
	toRemove = make([]string, 0)
	toAdd = make(map[string]*config.Route)

	for insId, _ := range m.allInstanceMap {
		if route, exists := allRestoredRoutes[insId]; !exists || route == nil {
			toRemove = append(toRemove, insId)
		}
	}

	for insId, route := range allRestoredRoutes {
		if _, exists := m.allInstanceMap[insId]; !exists {
			toAdd[insId] = route
		}
	}

	return toRemove, toAdd
}

func (m *InstanceManager) OnLeaderChanged(newLeader *utils.Peer, newIsMeLeader bool, restoreFailures *utils.AllInstanceFailuresSnapshot) {
	logger.Log().Infof("instancesManager receives leader changes, new leader %v, is me leader? %v", newLeader, newIsMeLeader)
	oldIsMeLeader := m.isMeLeader.Load()
	if oldIsMeLeader && !newIsMeLeader {
		// step down
		m.schedulerManager.ClearAlgorithmLoadsAndEvents()
		clear(m.lastSnapshotRecords)
		logger.Log().Infof("cleared algorithm loads due to leader step down")
	}

	if restoreFailures != nil {
		logger.Log().Infof("restoring instance failures, from leader [%s] round [%v], failures [%s]",
			restoreFailures.LeaderId, restoreFailures.LocalRound,
			utils.MapKeysStr(restoreFailures.Failures))
		toAvailable, toUnavailable := m.findRestoreHealthChanges(restoreFailures.Failures)
		m.dealWithHealthChanges(toAvailable, toUnavailable)
		m.leaderHealthChecker.OnAllInstancesRenewed(m.availableMap, m.unavailableMap)
		logger.Log().Infof(
			"done restored instance failures, from leader [%s] round [%v], move [%v] to available, move [%v] to unavailable",
			restoreFailures.LeaderId, restoreFailures.LocalRound, len(toAvailable), len(toUnavailable))
	}

	m.isMeLeader.Store(newIsMeLeader)
}

func (m *InstanceManager) hasSeenSelfLeaderStepUp() bool {
	return m.isMeLeader.Load()
}

func (m *InstanceManager) OnAddPeer(peer *utils.Peer) {
}

func (m *InstanceManager) OnRemovePeer(name string) {
	m.schedulerManager.CleanUpAllEventMsgByWorkerId(name)
}

// 1. add NUM of new instances to unavailable list firstly
// 2. the healthCheck func will add healthy ones to available list
// 3. doStatsSync will inform the scheduler automatically
func (m *InstanceManager) OnAddInstance(routes map[string]*config.Route, reason string) {
	if routes == nil {
		logger.Log().Errorf("given routes to add remote instances is nil")
		return
	}
	logger.Log().Infof("InstancesManager receives OnAddInstance [%s], adding instances %v", reason, utils.MapKeysStr(routes))
	roleSizes := getRoleInstanceSizePtr(routes)

	m.routeMapMutex.Lock()
	m.syncMutex.Lock()
	for id, route := range routes {
		if route == nil {
			logger.Log().Warnf("route for id %v is nil, skipping", id)
			continue
		}
		_, exists := m.roleLookUp[id]
		if exists {
			logger.Log().Warnf("instance with id %v already exists, skip it.", id)
			continue
		}

		if !(len(id) > 0 && len(route.Role) > 0) {
			logger.Log().Errorf("instance with id %v or role %v is invalid, skip it.", id, route.Role)
			continue
		}

		// initialize new role info
		if m.unavailable[route.Role] == nil {
			m.available[route.Role] = make(map[string]*client.Instance, roleSizes[route.Role])
			m.unavailable[route.Role] = make(map[string]*client.Instance, roleSizes[route.Role])
			val := atomic.Value{}
			roleAvailableStats := make(map[string]instance_metrics.WorkerStats)
			val.Store(&roleAvailableStats)
			m.availableStats[route.Role] = &val
			unavailableVal := atomic.Value{}
			m.unavailableStats[route.Role] = &unavailableVal
			m.roleInstanceSize[route.Role] = 0
			m.seenRoles = append(m.seenRoles, route.Role)
		}

		instance, err := newInstance(id, *route, m.cfg, m.httpCfg)
		if err != nil {
			logger.Log().Errorf("skipping instance due to client initial failed for instance with id %v. %v", id, err)
			continue
		}
		m.unavailable[route.Role][id] = instance
		m.unavailableMap[id] = instance
		m.roleLookUp[id] = route.Role
		m.roleInstanceSize[route.Role]++
		m.allInstances = append(m.allInstances, instance)
		if m.allInstanceMap == nil {
			m.allInstanceMap = make(map[string]*client.Instance)
		}
		m.allInstanceMap[id] = instance
		logger.Log().Infof("InstancesManager adds [%s] instance[id:%v, role:%v] successfully", reason, id, route.Role)
	}
	m.syncMutex.Unlock()
	m.routeMapMutex.Unlock()

	m.routeMapMutex.RLock()
	m.schedulerManager.UpdateFallbackAlgoStats(m.getAllInstanceStatsNoLock())
	m.leaderHealthChecker.OnAllInstancesRenewed(m.availableMap, m.unavailableMap)
	m.routeMapMutex.RUnlock()
	return
}

func (m *InstanceManager) OnRemoveInstance(names []string, reason string) {
	if len(names) == 0 {
		logger.Log().Errorf("given routes to remove remote instances is nil")
		return
	}

	logger.Log().Infof("InstancesManager receives OnRemoveInstance [%s], removing instances %v", reason, names)

	m.routeMapMutex.Lock()
	m.syncMutex.Lock()

	for _, id := range names {
		role, exists := m.roleLookUp[id]
		if !exists {
			logger.Log().Warnf("instance with id %v does not exist, skip it.", id)
			continue
		}

		delete(m.unavailable[role], id)
		delete(m.unavailableMap, id)
		delete(m.available[role], id)
		delete(m.availableMap, id)
		delete(m.roleLookUp, id)

		if m.roleInstanceSize[role] > 0 {
			m.roleInstanceSize[role]--
		}
		if m.roleInstanceSize[role] == 0 {
			logger.Log().Warnf("No instance exists as role [%v], all removed by user.", role)
			delete(m.unavailable, role)
			delete(m.unavailableStats, role)
			delete(m.available, role)
			delete(m.availableStats, role)
			delete(m.roleInstanceSize, role)
		}
		logger.Log().Infof("InstancesManager removes [%s] instance[id:%v, role:%v] successfully", reason, id, role)
	}
	// reconstruct the allInstances, excluding removing ones
	newAllInstances := make([]*client.Instance, 0, len(m.allInstances))
	newAllInstancesMap := make(map[string]*client.Instance, len(m.allInstances))
	for _, instance := range m.allInstances {
		role, exists := m.roleLookUp[instance.ID]
		if exists && (m.unavailable[role][instance.ID] != nil || m.available[role][instance.ID] != nil) {
			newAllInstances = append(newAllInstances, instance)
			newAllInstancesMap[instance.ID] = instance
		}
	}
	m.allInstances = newAllInstances
	m.allInstanceMap = newAllInstancesMap

	// reconstruct the seenRoles info, excluding removing ones
	newSeenRoles := make([]string, 0, len(m.unavailable))
	for _, role := range m.seenRoles {
		if m.unavailable[role] != nil && m.available[role] != nil {
			newSeenRoles = append(newSeenRoles, role)
		}
	}
	m.seenRoles = newSeenRoles

	m.syncMutex.Unlock()
	m.routeMapMutex.Unlock()

	m.routeMapMutex.RLock()
	// after remove instances, immediately inform scheduler update stats
	// it will lock syncMutex
	m.healthChangesSync()
	m.leaderHealthChecker.OnAllInstancesRenewed(m.availableMap, m.unavailableMap)
	m.routeMapMutex.RUnlock()
}

func (m *InstanceManager) OnRequestEvents(workerId string, workerBootId string, requestEvents []*utils.RequestEventMsg) {
	m.schedulerManager.OnRequestEvent(workerId, workerBootId, requestEvents)
	m.leaderHealthChecker.OnRequestEvents(workerId, workerBootId, requestEvents)
}

func (m *InstanceManager) OnInstanceTokenEvent(workerId string, instanceEvents []*utils.InstanceEventMsg) {
	m.leaderHealthChecker.OnInstanceTokenEvent(workerId, instanceEvents)
}

func (m *InstanceManager) OnAllInstancesRenewed(available, unavailable map[string]*client.Instance) {
	panic("instances manager On AllInstanceChanged should never be called")
}

func (m *InstanceManager) InitHealthyModule(testInstance func(ctx *gin.Context, path string, isChat bool, instance *client.Instance) (successToCheck bool, testSuccess bool, errDetails *errs.RSError),
	clusterService cluster.ClusterService, pdLinkBlackListManager *health.PDLinkBlacklistManager) {
	// Save clusterService reference
	m.clusterService = clusterService

	// Initialize instance health cache
	m.healthCache = NewInstanceHealthCache(clusterService, m.cfg.HealthCheckConfig)

	isMeCandidate := clusterService.LocalNode().IsCandidate
	healthChecker := health.NewLeaderHealthChecker(m.cfg.HealthCheckConfig, m.leaderDealWithHealthChanges, testInstance, isMeCandidate, pdLinkBlackListManager, m.hasSeenSelfLeaderStepUp)
	healthChecker.Init()
	m.leaderHealthChecker = healthChecker
	m.routeMapMutex.RLock()
	m.leaderHealthChecker.OnAllInstancesRenewed(m.availableMap, m.unavailableMap)
	m.routeMapMutex.RUnlock()

	m.failureSnapshotMutex.Lock()
	m.failureSnapshotFunc = clusterService.PersistInstanceFailures
	m.failureSnapshotMutex.Unlock()

	if m.cfg.HealthCheckConfig.FallbackHealthCheckConfig.Enable {
		m.schedulerManager.InitLocalHealthModule(m.cfg.HealthCheckConfig)
	}
}

func NewManager(rootCfg *config.Config, schedulerManager scheduler.Manager,
	monitorClient monitorclient.MonitorClient, msgCenter utils.MessageCenter) (Manager, error) {
	if rootCfg == nil {
		return nil, fmt.Errorf("root config is nil")
	}
	if schedulerManager == nil || monitorClient == nil {
		return nil, fmt.Errorf("failed to create instance-manager, rootCfg or scheduler or monitor is nil")
	}

	cfg := rootCfg.InstanceManagerCfg
	httpCfg := rootCfg.HttpConfig
	allInstanceSize := len(cfg.RouteTable.InstanceRoutes)
	instanceManager := newInstanceManager(cfg, httpCfg, allInstanceSize, schedulerManager)
	if instanceDownChan, err := msgCenter.Register(constant.InstanceDownTopic, &cluster.InstanceLeaveRequest{}); err != nil {
		return nil, fmt.Errorf("fail to register instance down in instance manager, %v", err)
	} else {
		instanceManager.instanceDownMsgChan = instanceDownChan
	}
	instanceManager.isPureWorker = !rootCfg.ClusterCfg.RoleCandidate
	metricsCfg := config.MetricsCfg{
		Enable:   true,
		Duration: 30,
	}
	metrics.InitMetrics(&metricsCfg)
	err := initMetricsRegistration(schedulerManager, monitorClient)
	if err != nil {
		return nil, err
	}
	instanceManager.resHeaderBlackSet = config.NewResHeaderBlackSet(rootCfg.ServerConfig.ResponseHeaderBlacklist)

	// Initialize circuit breaker module
	instanceManager.circuitBreaker = NewCircuitBreaker(rootCfg.ProbeCfg.CircuitBreaker)
	if instanceManager.circuitBreaker != nil {
		logger.Log().Infof("[CircuitBreaker] enabled with config: threshold=%.2f, minRequestCount=%d",
			rootCfg.ProbeCfg.CircuitBreaker.FailureRateThreshold,
			rootCfg.ProbeCfg.CircuitBreaker.MinRequestCount)
	}

	// construct all instances list
	instanceManager.seenRoles = make([]string, 0, len(instanceManager.unavailable))
	var allInstances = make([]*client.Instance, 0, allInstanceSize)
	var allInstanceMap = make(map[string]*client.Instance, allInstanceSize)
	instanceManager.allInstances = allInstances
	instanceManager.allInstanceMap = allInstanceMap
	instanceManager.monitorClient = monitorClient
	if len(cfg.RequiredRoles) == 0 {
		cfg.RequiredRoles = []string{
			constant.Prefill, constant.Decode,
		}
	}
	instanceManager.requiredRoles = cfg.RequiredRoles
	instanceManager.exitingWaitGroup.Add(4)
	go instanceManager.statsSyncLoop()
	go instanceManager.instanceWarningLoop()
	go instanceManager.failureSnapshotLoop()
	go instanceManager.consecutiveFailuresInstanceWatcher()

	instanceManager.currentStage = "RUNNING"
	logger.Log().Info("instance manager start successfully")
	return instanceManager, nil
}

func newInstanceManager(cfg config.InstanceManagerCfg, httpCfg config.HttpConfig, allInstanceSize int,
	schedulerManager scheduler.Manager) *InstanceManager {
	config.ApplyDefaultInstanceManagerCfg(&cfg)

	instanceManager := &InstanceManager{
		cfg:                 &cfg,
		httpCfg:             &httpCfg,
		leaderHealthChecker: &health.LeaderHealthChecker{},
		available:           make(map[string]map[string]*client.Instance),
		availableMap:        make(map[string]*client.Instance),
		unavailable:         make(map[string]map[string]*client.Instance),
		unavailableMap:      make(map[string]*client.Instance),
		roleLookUp:          make(map[string]string, allInstanceSize),
		roleInstanceSize:    make(map[string]int),
		schedulerManager:    schedulerManager,

		stopChan:         make(chan struct{}),
		availableStats:   make(map[string]*atomic.Value),
		unavailableStats: make(map[string]*atomic.Value),
	}

	return instanceManager
}

func initMetricsRegistration(schedulerManager scheduler.Manager, monitor monitorclient.MonitorClient) error {
	needMetrics := schedulerManager.GetStatsRegistrations()
	err := monitor.Init(needMetrics)
	if err != nil {
		return fmt.Errorf("failed to init monitor with needMetrics %v, err = %v", needMetrics, err)
	} else {
		logger.Log().Infof("success to register metrics %v", needMetrics)
	}
	return nil
}

func newInstance(id string, route config.Route, cfg *config.InstanceManagerCfg,
	httpCfg *config.HttpConfig) (*client.Instance, error) {
	clientType := cfg.InstanceClientType
	clientConfig := instanceclient.ClientConfig{
		HttpCfg:      *httpCfg,
		CheckTimeout: time.Duration(cfg.HealthCheckConfig.RestHealthCommonConfig.HealthCheckTimeoutMs) * time.Millisecond,
	}
	instanceClient, err := instanceclient.NewClient(clientType, route, clientConfig)
	if err != nil {
		logger.Log().Errorf("failed to create instanceClient for %v. %v", id, err)
		return nil, err
	}

	emptyStats := make(map[string][]*instance_metrics.MetricInfo)
	instanceStats := &instance_metrics.ZeroCopyStats{
		Id:           id,
		Role:         route.Role,
		SLOTag:       route.Tag,
		Status:       instance_metrics.Init,
		StaticInfo:   route.StaticInfos,
		NumericStats: &emptyStats,
	}

	instance := client.NewInstance(id, instanceClient, instanceStats, route.Ip, route.Port)

	return instance, err
}

func getRoleInstanceSize(routes map[string]config.Route) map[string]int {
	result := make(map[string]int)
	for _, route := range routes {
		if count, exists := result[route.Role]; exists {
			result[route.Role] = count + 1
		} else {
			result[route.Role] = 1
		}
	}
	return result
}

func getRoleInstanceSizePtr(routes map[string]*config.Route) map[string]int {
	result := make(map[string]int)
	for _, route := range routes {
		if route == nil {
			continue
		}
		if count, exists := result[route.Role]; exists {
			result[route.Role] = count + 1
		} else {
			result[route.Role] = 1
		}
	}
	return result
}

func (m *InstanceManager) relayInstanceRequest(ctx *gin.Context, broadcastRequest *openai.BroadcastRequest) (instanceStatus []*openai.InstanceResponse, availableCount int64, unavailableCount int64) {
	m.routeMapMutex.RLock()
	defer m.routeMapMutex.RUnlock()
	if broadcastRequest.Targets == "_all" {
		instanceStatus = make([]*openai.InstanceResponse, len(m.allInstances))
		for index, instance := range m.allInstances {
			if roleExists, ok := m.unavailable[instance.Stats.GetRole()]; ok {
				if _, ok := roleExists[instance.ID]; ok {
					rsp := openai.InstanceResponse{
						InstanceName: instance.ID,
						StatusCode:   http.StatusInternalServerError,
						Error:        fmt.Sprintf("instance is unavailabel"),
					}
					instanceStatus[index] = &rsp
					unavailableCount++
					continue
				}
			}
			rsp := instance.Client.HandlerBroadCastRequest(ctx, broadcastRequest, instance.ID)
			if rsp.StatusCode != http.StatusOK {
				unavailableCount++
			} else {
				availableCount++
			}
			instanceStatus[index] = &rsp
		}
		return instanceStatus, availableCount, unavailableCount
	}
	idList := strings.Split(broadcastRequest.Targets, ",")
	if len(idList) >= 1 && idList[0] != "" {
		instanceStatus = make([]*openai.InstanceResponse, len(idList)) // ensure at least one non-empty name
		for index, id := range idList {
			instance, preErr := m.getInstanceClientNoLock(id)
			if preErr != nil {
				instanceStatus[index] = &openai.InstanceResponse{
					InstanceName: id,
					StatusCode:   http.StatusNotFound,
					Error:        fmt.Sprintf("instance not found, err = %v", preErr),
				}
				unavailableCount++
				continue
			}
			rsp := instance.HandlerBroadCastRequest(ctx, broadcastRequest, id)
			if rsp.StatusCode != http.StatusOK {
				unavailableCount++
			} else {
				availableCount++
			}
			instanceStatus[index] = &rsp
		}
		return instanceStatus, availableCount, unavailableCount
	}
	return instanceStatus, availableCount, unavailableCount
}

func (m *InstanceManager) HandleBroadcastInstanceRequest(ctx *gin.Context) {
	bodyBytes, err := io.ReadAll(ctx.Request.Body)
	// Error response structure
	errorResponse := func(reason string) {
		ctx.JSON(http.StatusBadRequest, gin.H{
			"status": constant.FailedStatus,
			"reason": reason,
		})
	}

	if err != nil {
		errorResponse(fmt.Sprintf("Failed to read request body: %v", err))
		return
	}

	broadcastRequest := &openai.BroadcastRequest{}
	if err := utils.SonicApi.Unmarshal(bodyBytes, broadcastRequest); err != nil {
		errorResponse(fmt.Sprintf("Invalid request format: %v", err))
		return
	}

	isAllowed := m.whitelistMiddleware(broadcastRequest)

	if !isAllowed {
		response := openai.BroadcastResponse{
			Status: constant.FailedStatus,
			Total: openai.InstanceStatusCounts{
				Success: 0,
				Failed:  0,
			},
		}
		ctx.JSON(http.StatusForbidden, response)
	} else {
		instanceStatus, avaliableCount, unavailableCount := m.relayInstanceRequest(ctx, broadcastRequest)
		response := m.mergeBroadResp(instanceStatus, avaliableCount, unavailableCount)
		ctx.JSON(http.StatusOK, response)
	}

}

func (m *InstanceManager) mergeBroadResp(instanceStatus []*openai.InstanceResponse, avaliableCount int64, unavailableCount int64) (response openai.BroadcastResponse) {
	var status string
	if avaliableCount == 0 {
		status = constant.FailedStatus
	} else {
		if unavailableCount > 0 {
			status = constant.PartialStatus
		} else {
			status = constant.SuccessStatus
		}
	}
	response = openai.BroadcastResponse{
		Status: status,
		Total: openai.InstanceStatusCounts{
			Success: avaliableCount,
			Failed:  unavailableCount,
		},
		Responses: instanceStatus,
	}
	return response
}

func isMethodAllowed(reqMethod string, allowedMethods []string) bool {
	if len(allowedMethods) == 0 {
		return false
	}
	for _, m := range allowedMethods {
		if strings.ToUpper(m) == strings.ToUpper(reqMethod) {
			return true
		}
	}
	return false
}

func (m *InstanceManager) whitelistMiddleware(broadcastReq *openai.BroadcastRequest) bool {
	cfg := m.cfg.URIWhitelist
	if len(cfg) == 0 {
		return false
	}
	uri := broadcastReq.URI
	method := broadcastReq.Method
	for _, entry := range cfg {
		// Path matching check - only exact matching supported, prefix matching removed to prevent security risks
		pathMatch := uri == entry.Path
		// Method matching check
		methodMatch := isMethodAllowed(method, entry.Methods)
		if pathMatch && methodMatch {
			return true
		}
	}
	return false
}

func (m *InstanceManager) HandleProxyToSingleInstance(ctx *gin.Context) {
	allInstancesCopy := make([]*client.Instance, len(m.allInstances))
	m.routeMapMutex.RLock()
	copy(allInstancesCopy, m.allInstances)
	m.routeMapMutex.RUnlock()
	if len(allInstancesCopy) == 0 {
		ctx.JSON(http.StatusInternalServerError, gin.H{
			"status": constant.FailedStatus,
			"reason": "no available instance",
		})
		return
	}
	bodyBytes, err := io.ReadAll(ctx.Request.Body)
	if err != nil {
		logger.Log().Errorf("failed to read request body, err = %v", err)
		return
	}
	var lastError error
	for _, instance := range allInstancesCopy {
		req, err := http.NewRequestWithContext(ctx.Request.Context(), ctx.Request.Method, ctx.Request.URL.Path, io.NopCloser(bytes.NewBuffer(bodyBytes)))
		if err != nil {
			logger.Log().Errorf("failed new request, err = %v", err)
		}
		m.copyHeaders(req.Header, ctx.Request.Header, false)
		success, err := m.handleInstanceResponse(ctx, instance, req)
		reqId := http2.RequestId(ctx)
		if success {
			logger.Log().Infof("successfully proxy request [%s] with path [%s] to instance [%s] [%s]",
				reqId, ctx.Request.URL.RequestURI(), instance.ID, instance.Client.GetEndpoint())
			return
		}
		if err != nil {
			logger.Log().Warnf("failed proxy request [%s] with path [%s] to instance [%s] [%s], err = %v\n",
				reqId, ctx.Request.URL.RequestURI(), instance.ID, instance.Client.GetEndpoint(), err)
			lastError = err
		}
	}
	// Handle when all instances fail
	if lastError != nil {
		ctx.JSON(http.StatusBadGateway, gin.H{
			"error":  "all instances failed",
			"detail": lastError.Error(),
		})
	} else {
		ctx.JSON(http.StatusInternalServerError, gin.H{
			"error": "no valid response from any instance",
		})
	}
}

func (m *InstanceManager) handleInstanceResponse(ctx *gin.Context, instance *client.Instance, req *http.Request) (bool, error) {
	resp := instance.Client.ProxyHttp(req)

	if resp == nil {
		return false, fmt.Errorf("nil response from instance")
	}

	if resp.Body != nil {
		defer resp.Body.Close()
	}

	if resp.StatusCode >= 500 {
		if errBody, err := io.ReadAll(resp.Body); err == nil {
			return false, fmt.Errorf("instance error: status code %d, body = %s",
				resp.StatusCode, utils.AbstractLargeBytes(errBody))
		} else {
			return false, fmt.Errorf("instance error: status code %d, (failed to read res body err = %v)",
				resp.StatusCode, err)
		}
	}

	if resp.Body != nil {
		m.copyHeaders(ctx.Writer.Header(), resp.Header, true)
		_, err := io.Copy(ctx.Writer, resp.Body)
		if err != nil {
			logger.Log().Errorf("Proxy response copy failed: %v", err)
			return false, err
		}
	}

	return true, nil
}

func (m *InstanceManager) HandleHealthCheck(ctx *gin.Context) {
	m.routeMapMutex.RLock()
	defer m.routeMapMutex.RUnlock()
	availableInstLen := 0
	for _, role := range m.requiredRoles {
		if availableInstIds, exists := m.available[role]; exists {
			availableInstLen += len(availableInstIds)
		} else {
			// No required role instance in available, return 500 directly, covering scenarios where required role is not deployed
			ctx.Status(http.StatusInternalServerError)
			logger.Log().Errorf("[GET /health] result from health check is failed")
			return
		}
	}
	if availableInstLen == len(m.allInstances) && len(m.allInstances) > 0 {
		ctx.Status(http.StatusOK)
		logger.Log().Infof("[GET /health] ok")
	} else {
		ctx.Status(http.StatusInternalServerError)
		logger.Log().Errorf("[GET /health] result from health check is failed")
	}
}

// HandleForceReadiness forcibly sets readiness status (emergency escape channel)
// force=true: force ready, return true to make k8s accept traffic
// force=false: force not ready, return false to make k8s stop accepting traffic
func (m *InstanceManager) HandleForceReadiness(ctx *gin.Context) {
	if m.circuitBreaker == nil {
		ctx.JSON(http.StatusOK, gin.H{
			"status":  "disabled",
			"message": "circuit breaker is not enabled",
		})
		return
	}

	// Define request body structure
	type ForceReadinessRequest struct {
		Mode string `json:"mode" binding:"omitempty"`
	}

	var req ForceReadinessRequest
	if err := ctx.ShouldBindJSON(&req); err != nil {
		// If no mode provided, default to auto
		req.Mode = "auto"
	}

	// Get mode parameter from request: true(force ready) / false(force not ready) / auto(self-judgment)
	modeStr := req.Mode
	if modeStr == "" {
		modeStr = "auto"
	}

	var mode ForceMode
	var message string
	var ready bool

	switch modeStr {
	case "true", "1", "yes":
		mode = ForceModeTrue
		message = "force ready (accepting traffic)"
		ready = true
	case "false", "0", "no":
		mode = ForceModeFalse
		message = "force not ready (rejecting traffic)"
		ready = false
	case "auto", "2":
		mode = ForceModeNone
		message = "auto mode (internal logic)"
		// Return current circuit breaker status in auto mode
		ready = !m.circuitBreaker.CheckShouldTrip()
	default:
		logger.Log().Warnf("[AUDIT][CircuitBreaker]src_ip=%s,target=readiness mode,action=Force changed,result=faild,error=invalid request",
			ctx.ClientIP())
		ctx.JSON(http.StatusBadRequest, gin.H{
			"status":  "invalid",
			"message": fmt.Sprintf("invalid mode: %s, use: true/false/auto", modeStr),
		})
		return
	}

	// Set force mode
	success := m.circuitBreaker.SetForceMode(mode)

	if success {
		logger.Log().Warnf("[AUDIT][CircuitBreaker] src_ip=%s,target=readiness mode,action=Force changed to: %s,result=success",
			ctx.ClientIP(), message)
		ctx.JSON(http.StatusOK, gin.H{
			"status":  "success",
			"mode":    modeStr,
			"ready":   ready,
			"message": message,
		})
	} else {
		logger.Log().Warnf("[AUDIT][CircuitBreaker] src_ip=%s,target=readiness mode,action=Force changed to: %s,result=faild",
			ctx.ClientIP(), message)
		ctx.JSON(http.StatusInternalServerError, gin.H{
			"status":  "failed",
			"message": "failed to set circuit breaker mode",
		})
	}
}

func (m *InstanceManager) failureSnapshotLoop() {
	defer m.exitingWaitGroup.Done()
	if m.isPureWorker {
		logger.Log().Infof("pure worker, stop failure instances snapshot loop")
		return
	}
	reportInterval := time.Duration(1) * time.Second
	ticker := time.NewTicker(reportInterval)
	defer ticker.Stop()
	for {
		select {
		case <-m.stopChan:
			logger.Log().Infof("stopping failure instances snapshot loop gracefully, do one last sync")
			m.SnapshotUnhealthyInstances()
			logger.Log().Info("failure instance snapshot loop stop due to stopChan")
			return
		case <-ticker.C:
			m.SnapshotUnhealthyInstances()
		}
	}
}

func (m *InstanceManager) SnapshotUnhealthyInstances() {
	if !m.isMeLeader.Load() {
		return
	}

	if m.isSnapshotFuncNil() {
		return
	}

	m.routeMapMutex.RLock()
	defer m.routeMapMutex.RUnlock()

	shouldSnapshot := false
	snapshotReason := ""
	if shouldSnapshot, snapshotReason = m.shouldSnapshotFailuresNoLock(); !shouldSnapshot {
		return
	}
	logger.Log().Infof("snapshot unhealthy instances [%s] to cluster state due to [%s]",
		utils.MapKeysStr(m.unavailableMap), snapshotReason)
	snapshotRecords := make(map[string]*utils.InstanceFailureRecord, len(m.unavailableMap))
	for insId, instance := range m.unavailableMap {
		snapshotRecords[insId] = &utils.InstanceFailureRecord{
			Role:                           instance.Stats.GetRole(),
			Status:                         instance.Stats.GetStatus().String(),
			Reason:                         instance.FaultMessage,
			ConsecutiveUnreachableTime:     instance.ConsecutiveHealthUnreachable,
			ConsecutiveFailureTime:         instance.ConsecutiveHealthFailed,
			HealthCheckCountAfterUnhealthy: instance.HealthCheckCountAfterUnhealthy,
		}
	}

	if err := m.failureSnapshotFunc(snapshotRecords); err != nil {
		logger.Log().Warnf("failed snapshot unhealthy instances: %v", err)
		return
	}
	m.lastSnapshotTimestamp = time.Now()
	logger.Log().Infof("successfully snapshot unhealthy instances [%s] to cluster state", utils.MapKeysStr(m.unavailableMap))
	m.lastSnapshotRecords = snapshotRecords
}

// operate under routeMapMutex
func (m *InstanceManager) shouldSnapshotFailuresNoLock() (shouldSnapshot bool, reason string) {
	if utils.HasKeyDifference(m.lastSnapshotRecords, m.unavailableMap) {
		return true, SnapshotReasonFailureChanged
	}
	snapshotInterval := time.Duration(m.cfg.HealthCheckConfig.FailureSnapshotIntervalMs) * time.Millisecond
	if snapshotInterval <= 0 {
		snapshotInterval = 2 * time.Duration(m.cfg.HealthCheckConfig.RestHealthCommonConfig.Interval) * time.Millisecond
	}
	now := time.Now()
	if now.Sub(m.lastSnapshotTimestamp) > snapshotInterval {
		if len(m.unavailableMap) > 0 || len(m.lastSnapshotRecords) > 0 {
			return true, SnapshotReasonTimeRenewed
		}
	}
	// TODO: compare consecutive time then decide snapshot
	return false, SnapshotReasonNoChanges
}

func (m *InstanceManager) isSnapshotFuncNil() bool {
	m.failureSnapshotMutex.RLock()
	defer m.failureSnapshotMutex.RUnlock()
	return m.failureSnapshotFunc == nil
}

func (m *InstanceManager) IsInstanceAvailable(name string) bool {
	m.routeMapMutex.RLock()
	defer m.routeMapMutex.RUnlock()
	_, ok := m.availableMap[name]
	return ok
}

func (m *InstanceManager) copyHeaders(dst, src http.Header, useBlackList bool) {
	for key, vals := range src {
		if useBlackList {
			if _, ok := m.resHeaderBlackSet[key]; ok {
				continue
			}
			for blackPrefix, isPrefixRule := range m.resHeaderBlackSet {
				if isPrefixRule && strings.HasPrefix(key, blackPrefix) {
					continue
				}
			}
		}
		for _, val := range vals {
			dst.Set(key, val)
		}
	}
}
