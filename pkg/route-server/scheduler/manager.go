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

package scheduler

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	peer_client "infer-dp-scheduler/pkg/common/client/peer-client"
	"infer-dp-scheduler/pkg/common/cluster"
	"infer-dp-scheduler/pkg/common/config"
	"infer-dp-scheduler/pkg/common/constant"
	"infer-dp-scheduler/pkg/common/health"
	instance_metrics "infer-dp-scheduler/pkg/common/instance-metrics"
	"infer-dp-scheduler/pkg/common/logger"
	"infer-dp-scheduler/pkg/common/scheduler"
	"infer-dp-scheduler/pkg/common/trace"
	"infer-dp-scheduler/pkg/common/utils"
	"infer-dp-scheduler/pkg/route-server/metrics"
)

type RequestTypeNotFoundError struct {
	reqId   string
	reqType string
}

func (r RequestTypeNotFoundError) Error() string {
	return fmt.Sprintf("request %s type %s not found in schedulers, check cfg", r.reqId, r.reqType)
}

type SLOTagNotFoundError struct {
	reqId  string
	sloTag string
}

func (s SLOTagNotFoundError) Error() string {
	return fmt.Sprintf("request %s type %s not found in schedulers, check cfg", s.reqId, s.sloTag)
}

type DowngradeError struct {
	reqId         string
	reqType       string
	schedulerName string
	scheduleErr   error
	fallbackErr   error
}

func (r DowngradeError) Error() string {
	if r.fallbackErr == nil {
		return fmt.Sprintf("Scheduler %s: failed to schedule request %s type %s, error %v",
			r.schedulerName, r.reqId, r.reqType, r.scheduleErr)
	} else {
		return fmt.Sprintf("Scheduler %s: failed to schedule request %s type %s, "+
			"both configured Algorithm and fallback are failed, configured algo error: %v, fallback algo error: %v",
			r.schedulerName, r.reqId, r.reqType, r.scheduleErr, r.fallbackErr)
	}
}

type ScheduleResponse struct {
	Result  string `json:"res"`
	SLOTag  string `json:"slo"`
	Skipped bool   `json:"skipped"`
}

type RemoteRequestInfo struct {
	IsLocal  bool
	WorkerId string
}

type Manager interface {
	init() error
	// UpdateStats instance stats, map of role -> stats_map
	// stats_map is map of instanceId -> InstanceStats
	// this is not thread safe, should be called in mutex
	// NestedMap is <Role: <InstanceID: InstanceStats>>
	// NOTE: In CLUSTER, only leader can update stats, worker is not actively updated,
	// only when nodes change, it will trigger workers' UpdateFallbackAlgoStats
	// to update fallback algorithm stats
	UpdateStats(available, unavailable map[string]map[string]instance_metrics.WorkerStats)
	UpdateFallbackAlgoStats(allStats map[string]map[string]instance_metrics.WorkerStats)

	UpdateConfig(requestType string, config map[string]string) error
	MinStatsSyncInterval() int64
	// DispatchRequest give optimal top-k instance id of target request type.
	// this is thread-safe
	DispatchRequest(reqCtx context.Context, request utils.GenerateRequest, params map[string]string,
		tracer trace.RequestTracer, blackID string, remoteRequestInfo RemoteRequestInfo) (*ScheduleResponse, error)
	OnRequestEvent(workerId string, events []*utils.RequestEventMsg)
	GetStatsRegistrations() map[string][]string
	ClearAlgorithmLoadsAndEvents()
	ReportLoads(includeRequests bool, includeUnavailable bool, roles []string) map[string]*scheduler.SLOLoadInfo

	// InitLocalHealthModule register local fallback health checker module
	// it will simply block unhealthy instances for a [resume-time-ms] period
	InitLocalHealthModule(cfg *config.HealthCheckConfig)
	// DealWithRequestFailureLocally scheduler should deal with all failed requests from instances by fallback health checker module
	DealWithRequestFailureLocally(request utils.GenerateRequest, inferMsg *health.InferMsg, details *utils.InstanceFailureDetails)
	// CleanUpAllEventMsgByWorkerId clean up the request loads for the specified worker
	CleanUpAllEventMsgByWorkerId(workerId string)
	GetAllRemoteWorkerIds() []string
	Close()
}

func NewManager(rootCfg *config.Config, clusterService cluster.ClusterService, peerClient *peer_client.PeerClient, pm *health.PDLinkBlacklistManager, rm *RemoteScheduledRequestManager) (Manager, error) {
	cfg := &rootCfg.SchedulersCfg
	var workerId string
	if clusterService != nil {
		workerId = clusterService.LocalNode().Name
	}

	schedulerManager := &SchedulersManager{
		cfg:                    cfg,
		clusterService:         clusterService,
		pdLinkBlackListManager: pm,
		remoteReqMgr:           rm,
		workerId:               workerId,
	}
	var remoteScheduler *RemoteScheduler
	var err error
	if cfg.RemoteSchedule != nil {
		remoteScheduler, err = NewRemoteScheduler(cfg.RemoteSchedule, clusterService, peerClient)
		if err != nil {
			return nil, fmt.Errorf("failed to create remote scheduler, err = %v", err)
		}
	}
	schedulerManager.remoteScheduler = remoteScheduler
	err = schedulerManager.init()
	if err != nil {
		return nil, fmt.Errorf("failed to init scheduler manager, %v", err)
	}

	return schedulerManager, nil
}

// Context for a single request type.
type Context struct {
	TargetInstanceRole string
	SLOTag             string
	Algorithm          scheduler.ScheduleAlgorithm
	lastSyncNodeIds    map[string]struct{}
	lastUpdateTime     int64
}

type SchedulersManager struct {
	workerId  string
	cfg       *config.SchedulersCfg
	closeChan chan struct{}

	sloManager *SLOManager

	// instanceRole -> sloTag-> schedulerCtx
	roleLookUp  map[string]map[string]*Context
	roleInstNum sync.Map

	// instanceId -> schedulerCtx
	insIdLookup      map[string]*Context
	insIdLookupMutex sync.RWMutex

	// instanceRole -> sloTag -> ScheduleAlgorithm(RoundRobin)
	fallbackAlgo map[string]map[string]scheduler.ScheduleAlgorithm

	initialized atomic.Bool
	shouldServe atomic.Bool // whether we should accept new request, will be set to false when closing.
	instanceNum atomic.Int64

	clusterService  cluster.ClusterService
	remoteScheduler *RemoteScheduler
	remoteReqMgr    *RemoteScheduledRequestManager

	pdLinkBlackListManager *health.PDLinkBlacklistManager

	fallbackHealthChecker *health.FallbackHealthChecker
}

func (s *SchedulersManager) InitLocalHealthModule(cfg *config.HealthCheckConfig) {
	s.fallbackHealthChecker = health.NewFallbackHealthChecker(cfg)
}

func (s *SchedulersManager) ReportLoads(includeRequests bool, includeUnavailable bool, roles []string) map[string]*scheduler.SLOLoadInfo {
	if roles == nil || len(roles) == 0 {
		return nil
	}
	result := make(map[string]*scheduler.SLOLoadInfo, len(roles))
	for _, role := range roles {
		if tagLookUp, exists := s.roleLookUp[role]; exists {
			sloLoadInfo := &scheduler.SLOLoadInfo{
				SLOGroupLoads: make(map[string]*scheduler.AlgoLoadInfo, len(tagLookUp)),
			}
			for sloTag, ctx := range tagLookUp {
				allInstLoads := ctx.Algorithm.ReportLoads(includeRequests, includeUnavailable)

				if allInstLoads.Available != nil {
					sort.Slice(allInstLoads.Available, func(i, j int) bool {
						return allInstLoads.Available[i].LoadVal > allInstLoads.Available[j].LoadVal
					})
				}
				if allInstLoads.Unavailable != nil {
					sort.Slice(allInstLoads.Unavailable, func(i, j int) bool {
						return allInstLoads.Unavailable[i].LoadVal > allInstLoads.Unavailable[j].LoadVal
					})
				}
				roleLoadInfo := &scheduler.AlgoLoadInfo{
					AlgorithmName: ctx.Algorithm.GetName(),
					InstanceLoads: allInstLoads,
				}
				sloLoadInfo.SLOGroupLoads[sloTag] = roleLoadInfo
			}
			result[role] = sloLoadInfo
		} else {
			roleLoadInfo := &scheduler.SLOLoadInfo{
				ErrReason: "no such role",
			}
			result[role] = roleLoadInfo
		}
	}
	return result
}

func (s *SchedulersManager) ClearAlgorithmLoadsAndEvents() {
	s.remoteReqMgr.ClearAll()
	for _, tagLookUp := range s.roleLookUp {
		for _, ctx := range tagLookUp {
			ctx.Algorithm.ClearLoad()
		}
	}
}

func (s *SchedulersManager) GetStatsRegistrations() map[string][]string {
	res := make(map[string][]string)
	for role, tagLookUp := range s.roleLookUp {
		for sloTag, ctx := range tagLookUp {
			needStats := make([]string, 0)
			algoStats := ctx.Algorithm.NeedStatsList()
			if algoStats != nil {
				needStats = append(needStats, algoStats...)
			}

			if roleMap, ok := s.fallbackAlgo[role]; ok {
				if fbAlgo, ok := roleMap[sloTag]; ok {
					fallBackStats := fbAlgo.NeedStatsList()
					if fallBackStats != nil {
						needStats = append(needStats, fallBackStats...)
					}
				}
			}
			res[role] = needStats
		}
	}
	return res
}

func (s *SchedulersManager) OnRequestEvent(workerId string, events []*utils.RequestEventMsg) {
	s.insIdLookupMutex.RLock()
	defer s.insIdLookupMutex.RUnlock()

	s.remoteReqMgr.mu.Lock()
	defer s.remoteReqMgr.mu.Unlock()

	start := time.Now()
	for _, event := range events {
		ctx, exists := s.insIdLookup[event.TargetInstance]
		if !exists {
			logger.Log().Warnf(
				"skip requestEvent on instance %v ,due to not found any scheduler for the instance", event.TargetInstance)
			// ignored
			return
		}
		ctx.Algorithm.OnRequestEvent(event)
		if !s.clusterService.IsSingleton() {
			s.remoteReqMgr.OnRequestEventWithNoLock(workerId, event)
		}
	}

	logger.Log().Debugf("success to process request events, events :[%s], duration [%s]",
		eventIds(events), time.Since(start).String())
}

func eventIds(events []*utils.RequestEventMsg) string {
	builder := strings.Builder{}
	for _, event := range events {
		builder.WriteString(fmt.Sprintf("<reqId:%s, insId:%s>, ", event.Request.GetRequestId(), event.TargetInstance))
	}
	return builder.String()
}

// GetContext for tests
func (s *SchedulersManager) GetContext(reqType string, sloTag string) *Context {
	if sloTag == "" {
		sloTag = constant.SLOTagDefault
	}
	return s.roleLookUp[reqType][sloTag]
}

func (s *SchedulersManager) setAlgorithm(algorithm scheduler.ScheduleAlgorithm, role string, sloTag string) error {
	ctx := &Context{
		TargetInstanceRole: role,
		SLOTag:             sloTag,
		Algorithm:          algorithm,
		lastUpdateTime:     0,
	}
	if existingCtx, exists := s.roleLookUp[role][sloTag]; !exists {
		s.roleLookUp[role][sloTag] = ctx
	} else {
		return fmt.Errorf("found 2 algo [%s, %s] for 1 slo group [%s:%s], which is not allowed",
			ctx.Algorithm.GetName(), existingCtx.Algorithm.GetName(), ctx.TargetInstanceRole, sloTag)
	}
	return nil
}

func createAndInitAlgo(managerCfg *config.SchedulersCfg,
	schedulerCfg *config.SchedulerCfg) (scheduler.ScheduleAlgorithm, error) {
	algorithm, err := GetNewSchedulerAlgo(schedulerCfg.SchedulerName)
	if err != nil {
		return algorithm, fmt.Errorf("create SchedulerAlgorithm %s for InstanceRole %s is failed, err %v",
			schedulerCfg.SchedulerName, schedulerCfg.InstanceRole, err)
	}
	params := getAlgoConfigs(managerCfg, schedulerCfg.Params)
	err = algorithm.Init(params)
	if err != nil {
		return algorithm, fmt.Errorf("init SchedulerAlgorithm %s for InstanceRole %s is failed, err %v",
			schedulerCfg.SchedulerName, schedulerCfg.InstanceRole, err)
	}
	logger.Log().Infof("scheduler algo [%s] for requestType [%s] and InstanceRole role [%s] successfully created",
		schedulerCfg.SchedulerName, schedulerCfg.InstanceRole, schedulerCfg.InstanceRole)
	return algorithm, nil
}

func getAlgoConfigs(managerCfg *config.SchedulersCfg, cfgMap map[string]string) map[string]string {
	params := make(map[string]string)
	for k, v := range cfgMap {
		params[k] = v
	}
	params[config.SystemPromptFieldName] = strconv.Itoa(managerCfg.SystemPromptLength)
	return params
}

func (s *SchedulersManager) init() error {
	if !s.initialized.CompareAndSwap(false, true) {
		// only init once.
		return nil
	}
	s.closeChan = make(chan struct{})

	if s.cfg.SLOScheduling == nil {
		s.cfg.SLOScheduling = &config.SLOSchedulerCfg{
			SloEnable: false,
		}
	}
	sloManager, err := newSLOManager(s.cfg)
	s.sloManager = sloManager
	if err != nil {
		return err
	}

	s.roleLookUp = make(map[string]map[string]*Context)
	s.insIdLookup = make(map[string]*Context)
	s.fallbackAlgo = make(map[string]map[string]scheduler.ScheduleAlgorithm)

	s.insIdLookupMutex = sync.RWMutex{}
	err = s.setupWithSchedulerCfg()
	if err != nil {
		return err
	}
	s.shouldServe.Store(true)

	if s.remoteReqMgr != nil && s.cfg.RemoteSchedule != nil && s.cfg.RemoteSchedule.CleanUpIntervalMs > 0 {
		go s.checkExpiredEventsLoop()
	}
	return nil
}

func (s *SchedulersManager) setupWithSchedulerCfg() error {
	hasAlgo := false
	for _, schedulerCfg := range s.cfg.Schedulers {
		role := schedulerCfg.InstanceRole
		if existingAlgo, exists := s.roleLookUp[role]; exists {
			// for now, the same role has the same schedule algorithm for all slo tag
			// sloTag default always exists so use it to confirm the Algorithm name
			return fmt.Errorf("scheduler for InstanceRole %s is assigned to more than 1 scheduler (%s, %s)",
				role, existingAlgo[constant.SLOTagDefault].Algorithm.GetName(), schedulerCfg.SchedulerName)
		}

		s.roleLookUp[role] = make(map[string]*Context)
		// now only prefill will use slo
		if role == constant.Prefill && s.cfg.SLOScheduling.SloEnable {
			for _, slo := range s.cfg.SLOScheduling.SloManager {
				algorithm, err := createAndInitAlgo(s.cfg, &schedulerCfg)
				if err != nil {
					return err
				}
				// each tag should have its own algorithm
				err = s.setAlgorithm(algorithm, schedulerCfg.InstanceRole, slo.Tag)
				if err != nil {
					return err
				}
			}
		}
		// default group always exists
		if s.roleLookUp[role][constant.SLOTagDefault] == nil {
			algorithm, err := createAndInitAlgo(s.cfg, &schedulerCfg)
			if err != nil {
				return err
			}
			err = s.setAlgorithm(algorithm, schedulerCfg.InstanceRole, constant.SLOTagDefault)
			if err != nil {
				return err
			}
		}

		// init fallback algo
		for sloTag := range s.roleLookUp[role] {
			fallbackAlgo, err := GetNewSchedulerAlgo(RoundRobinAlgoName)
			if err != nil {
				return fmt.Errorf("fail to create fallbackAlgo")
			}

			err = fallbackAlgo.Init(make(map[string]string))
			if err != nil {
				return fmt.Errorf("fail to init fallbackAlgo")
			}
			if s.fallbackAlgo[role] == nil {
				s.fallbackAlgo[role] = make(map[string]scheduler.ScheduleAlgorithm)
			}
			s.fallbackAlgo[role][sloTag] = fallbackAlgo
		}

		hasAlgo = true
	}
	if !hasAlgo {
		return fmt.Errorf("no valid scheduler algorithm is created!! please check config for schedulers")
	}
	return nil
}

func (s *SchedulersManager) checkExpiredEventsLoop() {
	interval := time.Duration(s.cfg.RemoteSchedule.CleanUpIntervalMs) * time.Millisecond
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-s.closeChan:
			return
		case <-ticker.C:
			expiredEvents := s.remoteReqMgr.checkExpiredEvents()
			if expiredEvents != nil {
				go s.cleanUpEventMsg(expiredEvents)
			}
		}
	}
}

// send expired events to algo, info it to clean up request loads
func (s *SchedulersManager) cleanUpEventMsg(expiredEvents []*utils.RequestEventMsg) {
	for _, event := range expiredEvents {
		ctx, exists := s.insIdLookup[event.TargetInstance]
		if !exists {
			continue
		}
		event.EventType = constant.RequestExpired
		ctx.Algorithm.OnRequestEvent(event)
	}
}

// send all event of certain worker to algo, info it to clean up request loads
func (s *SchedulersManager) CleanUpAllEventMsgByWorkerId(workerId string) {
	events := s.remoteReqMgr.GetAllEventsByWorkerID(workerId)
	logger.Log().Warnf("scheduler cleaned up [%v] requests' load of leaved worker [%s]", len(events), workerId)
	s.cleanUpEventMsg(events)
	s.remoteReqMgr.CleanUpAllEventsByWorkerID(workerId)
}

func (s *SchedulersManager) GetAllRemoteWorkerIds() []string {
	return s.remoteReqMgr.GetAllRemoteWorkers()
}

func requestIds(events []*utils.RequestEventMsg) string {
	res := strings.Builder{}
	for _, event := range events {
		res.WriteString(fmt.Sprintf("%v, ", event.Request.GetRequestId()))
	}
	return res.String()
}

func (s *SchedulersManager) UpdateStats(available, unavailable map[string]map[string]instance_metrics.WorkerStats) {
	s.sloManager.UpdateCluster(available)
	s.insIdLookupMutex.Lock()
	defer s.insIdLookupMutex.Unlock()
	s.insIdLookup = make(map[string]*Context)
	for role, tagLookUp := range s.roleLookUp {
		// tagAvailableInsMap <Tag: <InstanceId: InstanceStats>>
		tagAvailableInsMap := reorganizeStatsBySLOTag(available, role, s.cfg.SLOScheduling.SloEnable)
		// tagUnavailableInsMap <Tag: []InstanceId>
		tagUnavailableInsMap := reorganizeStatsBySLOTag(unavailable, role, s.cfg.SLOScheduling.SloEnable)
		// update Schedule Algorithm
		for tag, ctx := range tagLookUp {
			s.updateSchedulerAlgorithm(ctx, tag, tagAvailableInsMap[tag], tagUnavailableInsMap[tag])
		}
	}
}

func (s *SchedulersManager) updateSchedulerAlgorithm(ctx *Context, tag string,
	newAvailableInstances map[string]instance_metrics.WorkerStats, newUnavailableInstances map[string]instance_metrics.WorkerStats) {
	needUpdate := false
	hasNodesChanged := utils.HasKeyDifference(ctx.lastSyncNodeIds, newAvailableInstances)
	// if nodes changed, need to update regardless time interval
	needUpdate = hasNodesChanged
	needUpdate = needUpdate ||
		(time.Now().UnixMilli()-ctx.lastUpdateTime > ctx.Algorithm.GetUpdateInterval())
	if needUpdate {
		err := ctx.Algorithm.UpdateCluster(newAvailableInstances, newUnavailableInstances)
		if err != nil {
			logger.Log().Error(fmt.Sprintf(
				"scheduler %s failed to update new clusterInfo, err %v", schedulerKey(ctx), err))
			return
		} else {
			curTime := time.Now().UnixMilli()
			interval := int64(0)
			if ctx.lastUpdateTime > 0 {
				interval = curTime - ctx.lastUpdateTime
			}
			logger.Log().Trace(fmt.Sprintf(
				"success to sync stats for scheduler %s, time: %v, sync interval is %v ms",
				schedulerKey(ctx), curTime, interval))
			ctx.lastUpdateTime = curTime
		}
		ctx.lastSyncNodeIds = updateLastNodesInfo(newAvailableInstances)
	} else {
		logger.Log().Tracef("skipping stat sync for scheduler %s, nodesChangeFlag=%v ",
			schedulerKey(ctx), hasNodesChanged)
	}
	// update insId -> context if ctx update cluster success
	for insId := range newAvailableInstances {
		if existingCtx, exists := s.insIdLookup[insId]; exists {
			logger.Log().Errorf("instance %v has registered to two slo group [%v, %v]", insId, existingCtx.SLOTag, tag)
			continue
		}
		s.insIdLookup[insId] = ctx
	}
	for insId := range newUnavailableInstances {
		if existingCtx, exists := s.insIdLookup[insId]; exists {
			logger.Log().Errorf("instance %v has registered to two slo group [%v, %v]", insId, existingCtx.SLOTag, tag)
			continue
		}
		s.insIdLookup[insId] = ctx
	}
}

// stats: <Role: <InstanceId: InstanceStats>>
// return: <Tag: <InstanceId: InstanceStats>>
func reorganizeStatsBySLOTag(stats map[string]map[string]instance_metrics.WorkerStats, role string, sloEnable bool) map[string]map[string]instance_metrics.WorkerStats {
	var stat map[string]instance_metrics.WorkerStats
	if stats != nil {
		if s, exist := stats[role]; exist {
			stat = s
		} else {
			stat = make(map[string]instance_metrics.WorkerStats)
		}
	}
	// tagInsMap <Tag: <InstanceId: InstanceStats>>
	tagInsMap := make(map[string]map[string]instance_metrics.WorkerStats)
	for instanceId, instanceStats := range stat {
		var sloTag string
		sloTag = instanceStats.GetSLOTag()
		if !sloEnable || role == constant.Decode {
			sloTag = constant.SLOTagDefault
		}
		if _, existed := tagInsMap[sloTag]; !existed {
			tagInsMap[sloTag] = make(map[string]instance_metrics.WorkerStats)
		}
		tagInsMap[sloTag][instanceId] = instanceStats
	}
	return tagInsMap
}

func reorganizeSliceBySLOTag(stats map[string]map[string]instance_metrics.WorkerStats, role string, sloEnable bool) map[string][]string {
	var stat map[string]instance_metrics.WorkerStats
	if stats != nil {
		if s, exist := stats[role]; exist {
			stat = s
		} else {
			stat = make(map[string]instance_metrics.WorkerStats)
		}
	}
	// tagAvailableInsMap <Tag: <InstanceId: InstanceStats>>
	tagInsMap := make(map[string][]string)
	for instanceId, instanceStats := range stat {
		var sloTag string
		sloTag = instanceStats.GetSLOTag()
		if !sloEnable || role == constant.Decode {
			sloTag = constant.SLOTagDefault
		}
		if _, existed := tagInsMap[sloTag]; !existed {
			tagInsMap[sloTag] = make([]string, 0)
		}
		tagInsMap[sloTag] = append(tagInsMap[sloTag], instanceId)
	}
	return tagInsMap
}

func updateLastNodesInfo(lastWorkerStatsNodes map[string]instance_metrics.WorkerStats) map[string]struct{} {
	lastSyncNodeIds := make(map[string]struct{})
	for key := range lastWorkerStatsNodes {
		lastSyncNodeIds[key] = struct{}{}
	}
	return lastSyncNodeIds
}

// UpdateFallbackAlgoStats When nodes changed, worker updates all instances stats for FallbackAlgo at once
func (s *SchedulersManager) UpdateFallbackAlgoStats(allStats map[string]map[string]instance_metrics.WorkerStats) {
	s.sloManager.UpdateCluster(allStats)

	for role := range s.roleLookUp {
		roleInsNum := 0
		tagMap, exists := s.fallbackAlgo[role]
		if !exists {
			continue
		}
		tagInsMap := reorganizeStatsBySLOTag(allStats, role, s.cfg.SLOScheduling.SloEnable)
		for tag, algo := range tagMap {
			// Get stats for this Tag, if nil, also pass it to let algorithm clear state
			specificStats := tagInsMap[tag]

			err := algo.UpdateCluster(specificStats, nil)
			if err != nil {
				logger.Log().Warn(fmt.Sprintf(
					"Fallback scheduler %s (tag: %s) failed to update new clusterInfo, err %v",
					algo.GetName(), tag, err))
			}
			roleInsNum += len(specificStats)
		}
		s.roleInstNum.Store(role, roleInsNum)
	}
}

func schedulerKey(ctx *Context) string {
	return fmt.Sprintf("%s:%s:%s", ctx.TargetInstanceRole, ctx.SLOTag, ctx.Algorithm.GetName())
}

// UpdateConfig for test
func (s *SchedulersManager) UpdateConfig(requestType string, config map[string]string) error {
	tagLookUp, exists := s.roleLookUp[requestType]
	if !exists {
		logger.Log().Infof(
			"skip config update for req type %s, due to not found any scheduler for this type", requestType)
		// ignored
		return nil
	}
	for _, ctx := range tagLookUp {
		err := ctx.Algorithm.UpdateConfig(getAlgoConfigs(s.cfg, config))
		if err != nil {
			logger.Log().Errorf("fail to updated for scheduler %s, err=%v", schedulerKey(ctx), err)
			return err
		} else {
			logger.Log().Infof("success to update config for scheduler %s", schedulerKey(ctx))
		}
	}
	return nil
}

func (s *SchedulersManager) MinStatsSyncInterval() int64 {
	minInterval := int64(math.MaxInt64)
	hasPositive := false

	for _, tagLookUp := range s.roleLookUp {
		for _, ctx := range tagLookUp {
			interval := ctx.Algorithm.GetUpdateInterval()
			if interval > 0 && interval < minInterval {
				minInterval = interval
				hasPositive = true
			}
		}
	}

	if !hasPositive {
		return -1
	}
	return minInterval
}

func (s *SchedulersManager) DispatchRequest(reqCtx context.Context, request utils.GenerateRequest,
	params map[string]string, tracer trace.RequestTracer, blackId string, remoteRequestInfo RemoteRequestInfo) (*ScheduleResponse, error) {
	if !s.shouldServe.Load() {
		return nil, fmt.Errorf("scheduler is stopping, reject new requests")
	}
	// Record start time
	start := time.Now()
	defer func() {
		duration := time.Since(start).Seconds()
		metrics.RecordScheduleDuration(request.GetType(), duration)
	}()
	res, err := s.doDispatch(reqCtx, request, params, tracer, blackId)

	// worker only maintains FallbackAlgo stats(See UpdateFallbackAlgoStats), it doesn't UpdateStats for Algorithm(e.g. ABC6).
	// Cause requests will be dispatched to leader, only if leader throws error, then worker falls back to FallbackAlgo and do it locally.
	if err != nil {
		if remoteRequestInfo.IsLocal {
			return s.doFallbackDispatch(request, params, err, blackId)
		}
		return nil, err
	}

	if !remoteRequestInfo.IsLocal {
		event := &utils.RequestEventMsg{
			Request:        utils.WrapRequest(request, false),
			TargetInstance: res.Result,
			PreInstance:    "",
			EventType:      constant.RequestNew,
			FailureDetails: nil,
			TimeStamp:      time.Now().UnixMilli(),
		}
		s.remoteReqMgr.OnRequestEventNew(remoteRequestInfo.WorkerId, event)
	}

	return res, nil
}

func (s *SchedulersManager) shouldDispatchLocally() (bool, error) {
	if s.cfg.RemoteSchedule == nil || !s.cfg.RemoteSchedule.Enabled {
		return true, nil
	}
	return s.remoteScheduler.ShouldDispatchLocally()
}

// mergeEtcdFailureBlacklist retrieves failure cache within validity period from etcd and merges failed instances of current role into blacklist
func (s *SchedulersManager) mergeEtcdFailureBlacklist(reqType string, insBlackMap map[string]interface{}) map[string]interface{} {
	if s.clusterService == nil {
		return insBlackMap
	}

	etcdCache, err := s.clusterService.GetHealthStats()
	if err != nil || etcdCache.FailureInstanceDetail == nil {
		return insBlackMap
	}

	failureInstances, exists := etcdCache.FailureInstanceDetail[reqType]
	if !exists {
		return insBlackMap
	}

	for instId := range failureInstances {
		if insBlackMap == nil {
			insBlackMap = make(map[string]interface{})
		}
		if _, alreadyBlocked := insBlackMap[instId]; !alreadyBlocked {
			insBlackMap[instId] = nil
			logger.Log().Warnf("[fallback etcd health] Instance %s is in etcd failure cache, added to the blacklist for reqType %s", instId, reqType)
		}
	}
	return insBlackMap
}

// return insId, sloTag, skip by blacklist or not, error
func (s *SchedulersManager) doFallbackDispatch(request utils.GenerateRequest,
	params map[string]string, err error, blackId string) (*ScheduleResponse, error) {
	reqType := request.GetType()
	tagLookUp, exists := s.roleLookUp[reqType]
	if !exists {
		err := &RequestTypeNotFoundError{
			reqId:   request.GetRequestId(),
			reqType: reqType,
		}
		return nil, err
	}
	sloTag := constant.SLOTagDefault
	insBlackMap := s.pdLinkBlackListManager.GetBlacklist(blackId)

	// merge pd_link_blacklist and fallback_local_black_list with the same role
	if s.fallbackHealthChecker != nil {
		insBlackMap = s.fallbackHealthChecker.MergeBlacklist(reqType, insBlackMap)
	}

	// If etcd has failure cache within validity period, use it to avoid sending to failed instances
	insBlackMap = s.mergeEtcdFailureBlacklist(reqType, insBlackMap)

	// if all instances are blocked, then set black map to nil, algo will pick random one.
	val, _ := s.roleInstNum.Load(reqType)
	roleInstNum, _ := val.(int)
	if insBlackMap != nil && len(insBlackMap) >= roleInstNum {
		logger.Log().Warnf("[fallback health check] all [%v] instances are blocked, randomly pick one to send [%v]", reqType, request.GetRequestId())
		insBlackMap = nil
	}

	sloTag, err = s.sloManager.ChooseSLOTag(request, insBlackMap)
	if err != nil {
		return nil, err
	}
	ctx, exists := tagLookUp[sloTag]
	if !exists {
		err := &SLOTagNotFoundError{
			reqId:  request.GetRequestId(),
			sloTag: sloTag,
		}
		return nil, err
	}
	if sloTag == "" {
		res := &ScheduleResponse{
			Result: "",
			SLOTag: sloTag,
		}
		return res, nil
	}

	var fallbackAlgo scheduler.ScheduleAlgorithm
	if roleMap, ok := s.fallbackAlgo[ctx.TargetInstanceRole]; ok {
		fallbackAlgo, exists = roleMap[sloTag]
	}

	if !exists || fallbackAlgo == nil {
		logger.Log().Errorf("fallback algo isn't configured for role %s tag %s", ctx.TargetInstanceRole, sloTag)
		return nil, fmt.Errorf("fallback algo not found for tag %s", sloTag)
	}

	// Remove blacklist members that don't match the sloTag obtained above
	// When SLO is disabled, all instances belong to default group, no need to filter blacklist by SLO Tag
	insBlackMapOfSloTag := insBlackMap
	if s.cfg.SLOScheduling != nil && s.cfg.SLOScheduling.SloEnable {
		insBlackMapOfSloTag = make(map[string]interface{})
		for insId := range insBlackMap {
			if s.sloManager.MatchSloTag(insId, reqType, sloTag) {
				insBlackMapOfSloTag[insId] = struct{}{}
			}
		}
	}

	fallbackRes, _, fallbackErr := fallbackAlgo.DispatchRequest(request, params, insBlackMapOfSloTag)

	res := &ScheduleResponse{
		Result:  fallbackRes,
		SLOTag:  sloTag,
		Skipped: false,
	}
	metrics.RecordFallBackScheduled()
	if fallbackErr != nil {
		finalErr := &DowngradeError{
			reqId:         request.GetRequestId(),
			reqType:       reqType,
			schedulerName: ctx.Algorithm.GetName(),
			scheduleErr:   err,
			fallbackErr:   fallbackErr,
		}
		logger.Log().Errorf("failed to schedule request %s (%s) using fall-back scheduler, err=%v",
			request.GetRequestId(), request.GetType(), finalErr)

		return res, finalErr
	} else {
		logger.Log().Warnf(
			"Fallback sched r[%s] to inst[%s] by[%s] (tag: %s), err=%v",
			request.GetRequestId(), fallbackRes, fallbackAlgo.GetName(), sloTag, err)
	}
	return res, nil
}

// only leader do dispatch locally
func (s *SchedulersManager) doDispatch(reqCtx context.Context, request utils.GenerateRequest,
	params map[string]string, tracer trace.RequestTracer, blackId string) (*ScheduleResponse, error) {
	local, err := s.shouldDispatchLocally()
	if err != nil {
		return nil, err
	}
	if local {
		return s.dispatchLocally(request, params, blackId)
	}
	return s.remoteScheduler.Schedule(request, reqCtx, params, tracer, blackId, s.workerId)
}

// only leader do dispatch locally
func (s *SchedulersManager) dispatchLocally(request utils.GenerateRequest, params map[string]string, blackId string) (*ScheduleResponse, error) {
	reqType := request.GetType()
	tagLookUp, exists := s.roleLookUp[reqType]
	if !exists {
		err := &RequestTypeNotFoundError{
			reqId:   request.GetRequestId(),
			reqType: reqType,
		}
		return nil, err
	}

	sloTag := constant.SLOTagDefault
	insBlackMap := s.pdLinkBlackListManager.GetBlacklist(blackId)
	sloTag, err := s.sloManager.ChooseSLOTag(request, insBlackMap)
	if err != nil {
		return nil, err
	}
	if sloTag == "" {
		res := &ScheduleResponse{
			Result: "",
			SLOTag: sloTag,
		}
		return res, nil
	}
	ctx, exists := tagLookUp[sloTag]
	if !exists {
		err := &SLOTagNotFoundError{
			reqId:  request.GetRequestId(),
			sloTag: sloTag,
		}
		return nil, err
	}

	insId, skipped, err := ctx.Algorithm.DispatchRequest(request, params, insBlackMap)
	res := &ScheduleResponse{
		Result:  insId,
		SLOTag:  sloTag,
		Skipped: skipped,
	}
	return res, err
}

func (s *SchedulersManager) Close() {
	s.shouldServe.Store(false)
	if s.fallbackHealthChecker != nil {
		s.fallbackHealthChecker.Close()
	}
	s.remoteReqMgr.Close()
	close(s.closeChan)
}

func (s *SchedulersManager) DealWithRequestFailureLocally(request utils.GenerateRequest, inferMsg *health.InferMsg, details *utils.InstanceFailureDetails) {
	if s.fallbackHealthChecker != nil {
		event := health.NewRequestEvent(request, inferMsg, details)
		s.fallbackHealthChecker.HandleFallbackFailure(event)
	}
}
