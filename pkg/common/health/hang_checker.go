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

package health

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"infer-dp-scheduler/pkg/common/client"
	"infer-dp-scheduler/pkg/common/config"
	"infer-dp-scheduler/pkg/common/constant"
	"infer-dp-scheduler/pkg/common/logger"
	"infer-dp-scheduler/pkg/common/utils"
)

// InstanceRecorder Hang Checker on Worker
type InstanceRecorder struct {
	RequestsTimeList      map[string]int64
	LastSuccessTime       int64
	Role                  string
	NonStreamCnt          int
	LastNonStreamWarnTime int64
	MayHang               bool
}

type RoleThreshold struct {
	ThresholdTime int64
	SkipNonStream bool
}

type WorkerHangChecker struct {
	InstancesList       map[string]*InstanceRecorder
	Thresholds          map[string]*RoleThreshold
	HangWarningInterval int64
	cfg                 *config.HangCheckerConfig
	mu                  sync.Mutex
}

func (checker *WorkerHangChecker) SetInstanceRecorder(instanceId string, instanceType string, timestamp int64) *InstanceRecorder {
	if _, exist := checker.InstancesList[instanceId]; !exist {
		ins := &InstanceRecorder{
			RequestsTimeList:      make(map[string]int64),
			LastSuccessTime:       timestamp,
			Role:                  instanceType,
			NonStreamCnt:          0,
			LastNonStreamWarnTime: 0,
			MayHang:               false,
		}
		checker.InstancesList[instanceId] = ins
		return ins
	}
	return nil
}

func (checker *WorkerHangChecker) GetInstanceRecorder(instanceId string) *InstanceRecorder {
	if _, exist := checker.InstancesList[instanceId]; !exist {
		return nil
	}
	return checker.InstancesList[instanceId]
}

func (checker *WorkerHangChecker) RemoveInstance(instanceId string) {
	checker.InstancesList[instanceId].RequestsTimeList = make(map[string]int64)
	if checker.InstancesList[instanceId].MayHang {
		logger.Log().Infof("no request on instance %s, this worker think it is not hanging now", instanceId)
	}
	delete(checker.InstancesList, instanceId)
}

func (checker *WorkerHangChecker) Init(config *config.HangCheckerConfig) {
	checker.cfg = config
	if !checker.cfg.Enable {
		return
	}
	checker.mu = sync.Mutex{}
	checker.HangWarningInterval = constant.NonStreamHangIntervalMs
	if checker.InstancesList == nil {
		checker.InstancesList = make(map[string]*InstanceRecorder)
	}
	checker.Thresholds = make(map[string]*RoleThreshold)
	for _, threshold := range config.HangThresholds {
		checker.Thresholds[threshold.Role] = &RoleThreshold{
			ThresholdTime: threshold.ThresholdTime,
			SkipNonStream: threshold.SkipNonStream,
		}
	}
}

func (checker *WorkerHangChecker) OnRequestEvent(reqMsg *utils.RequestEventMsg) {
	if !checker.cfg.Enable || reqMsg.InternalTestingReq {
		return
	}
	request := reqMsg.Request
	insId := reqMsg.TargetInstance
	timestamp := reqMsg.TimeStamp
	if reqMsg.EventType == constant.RequestNew {
		checker.OnNewRequest(request, insId, timestamp)
	} else {
		checker.OnFinishedRequest(request, insId, timestamp)
	}
}

func (checker *WorkerHangChecker) OnNewRequest(request *utils.RemoteRequestWrapper, insId string, timestamp int64) {
	checker.mu.Lock()
	defer checker.mu.Unlock()
	reqId := request.GetRequestId()
	targetInstance := checker.GetInstanceRecorder(insId)
	if targetInstance == nil {
		if checker.Thresholds[request.GetType()] == nil {
			logger.Log().Warnf("invalid role %v for hang check", request.GetType())
			return
		}
		targetInstance = checker.SetInstanceRecorder(insId, request.GetType(), timestamp)
	}
	if _, exist := targetInstance.RequestsTimeList[reqId]; exist {
		if timestamp > targetInstance.RequestsTimeList[reqId] {
			targetInstance.RequestsTimeList[reqId] = timestamp
		}
	} else {
		targetInstance.RequestsTimeList[reqId] = timestamp
		if !request.IsStream() {
			targetInstance.NonStreamCnt++
		}
	}
}

func (checker *WorkerHangChecker) OnFinishedRequest(request *utils.RemoteRequestWrapper, insId string, timestamp int64) {
	checker.mu.Lock()
	defer checker.mu.Unlock()
	targetInstance := checker.GetInstanceRecorder(insId)
	if targetInstance == nil {
		return
	}
	reqId := request.GetRequestId()
	if _, exist := targetInstance.RequestsTimeList[reqId]; !exist {
		return
	}
	targetInstance.LastSuccessTime = timestamp
	if !request.IsStream() {
		targetInstance.NonStreamCnt--
	}
	delete(targetInstance.RequestsTimeList, reqId)
	if len(targetInstance.RequestsTimeList) == 0 {
		checker.RemoveInstance(insId)
	}
}

func (checker *WorkerHangChecker) OnLocalDecodeTokens(tokenMsg *WorkerLocalTokenEvent) {
	if !checker.cfg.Enable {
		return
	}
	checker.mu.Lock()
	defer checker.mu.Unlock()
	instanceId := tokenMsg.instanceId
	reqId := tokenMsg.reqId
	timestamp := tokenMsg.timestamp
	targetInstance := checker.GetInstanceRecorder(instanceId)
	if targetInstance == nil {
		return
	}
	if _, exist := targetInstance.RequestsTimeList[reqId]; !exist {
		return
	}
	targetInstance.LastSuccessTime = timestamp
}

func (checker *WorkerHangChecker) IsInstanceHanging(targetInstance *InstanceRecorder, instanceId string, timestamp int64) bool {
	roleThreshold := checker.Thresholds[targetInstance.Role]
	// Role with threshold <= 0 skips hang check
	if roleThreshold.ThresholdTime <= 0 {
		return false
	}
	hangtime := timestamp - targetInstance.LastSuccessTime
	if targetInstance.NonStreamCnt > 0 && roleThreshold.SkipNonStream {
		if hangtime > roleThreshold.ThresholdTime && timestamp-targetInstance.LastNonStreamWarnTime >= checker.HangWarningInterval {
			// Only warn, do not report as hung
			logger.Log().Warnf("instance %s may hang %v ms for this worker, but has %d non-stream request and skip hang check", instanceId, hangtime, targetInstance.NonStreamCnt)
			targetInstance.LastNonStreamWarnTime = timestamp
		}
		return false
	}
	if hangtime > roleThreshold.ThresholdTime {
		if !targetInstance.MayHang {
			targetInstance.MayHang = true
			var notFinishedReqIds []string
			for reqId, reqTimestamp := range targetInstance.RequestsTimeList {
				if reqTimestamp <= targetInstance.LastSuccessTime {
					notFinishedReqIds = append(notFinishedReqIds, reqId)
				}
			}
			var reqIdsStr string
			if len(notFinishedReqIds) > 3 {
				reqIdsStr = strings.Join(notFinishedReqIds[:3], ", ") + " and more"
			} else {
				reqIdsStr = strings.Join(notFinishedReqIds, ", ")
			}
			hangLog := fmt.Sprintf("instance %s may hang %v ms for this worker, %d req is not finished: %s", instanceId, hangtime, len(notFinishedReqIds), reqIdsStr)
			logger.Log().Warn(hangLog)
		}
		return true
	}
	if targetInstance.MayHang {
		targetInstance.MayHang = false
		logger.Log().Infof("instance %s is not hang for this worker, last event is at %dms ago", instanceId, hangtime)
	}
	return false
}

func (checker *WorkerHangChecker) InstanceHangCheck(timestamp int64) []*utils.InstanceEventMsg {
	if !checker.cfg.Enable {
		return nil
	}
	checker.mu.Lock()
	defer checker.mu.Unlock()
	var instanceEvents []*utils.InstanceEventMsg
	for instanceId, targetInstance := range checker.InstancesList {
		if checker.IsInstanceHanging(targetInstance, instanceId, timestamp) {
			instanceMsg := &utils.InstanceEventMsg{
				InstanceId: instanceId,
				IsHang:     true,
			}
			instanceEvents = append(instanceEvents, instanceMsg)
		}
	}
	return instanceEvents
}

// Hang Checker on Leader
type WorkerRecorder struct {
	RequestsList    map[string]int64
	MayHang         bool
	LastHangMsgTime int64
}

type LeaderHangDecider struct {
	leaderHealthChecker *LeaderHealthChecker
	WorkerList          map[string]map[string]*WorkerRecorder
	HangTag             map[string]bool
	cfg                 *config.HangCheckerConfig
	mu                  sync.Mutex
	closeChan           chan struct{}

	// Pre-hang probe related fields
	probeInferRequest *ProbeInferRequest
	lastProbeTime     map[string]int64
	probingInstances  map[string]bool
	probeStates       map[string]ProbeState // Cache for instance probe state
	invalidProbeCount map[string]int        // Counter for consecutive invalid probes
}

func NewLeaderHangDecider(checker *LeaderHealthChecker) *LeaderHangDecider {
	hangCheckCfg := &checker.cfg.AvailableCheckersCfg.HangCheckerCfg
	leaderHangDecider := &LeaderHangDecider{
		leaderHealthChecker: checker,
		WorkerList:          make(map[string]map[string]*WorkerRecorder),
		HangTag:             make(map[string]bool),
		cfg:                 hangCheckCfg,
		mu:                  sync.Mutex{},
		closeChan:           make(chan struct{}),
		// Initialize probe-related fields
		lastProbeTime:     make(map[string]int64),
		probingInstances:  make(map[string]bool),
		probeStates:       make(map[string]ProbeState),
		invalidProbeCount: make(map[string]int),
	}
	if !hangCheckCfg.Enable {
		return leaderHangDecider
	}
	leaderHangDecider.OnAllInstancesRenewed(checker.healthInstance, checker.unhealthyInstance)
	return leaderHangDecider
}

func (l *LeaderHangDecider) GetWorker(instanceId string, workerId string) *WorkerRecorder {
	if _, exist := l.WorkerList[instanceId]; !exist {
		logger.Log().Warnf("instance %s is nil", instanceId)
		return nil
	}
	if _, exist := l.WorkerList[instanceId][workerId]; !exist {
		return nil
	}
	return l.WorkerList[instanceId][workerId]
}

func (l *LeaderHangDecider) SetWorker(instanceId string, workerId string) *WorkerRecorder {
	if _, exist := l.WorkerList[instanceId]; !exist {
		logger.Log().Warnf("instance %s is nil", instanceId)
		return nil
	}
	if _, exist := l.WorkerList[instanceId][workerId]; !exist {
		timestamp := time.Now().UnixMilli()
		l.WorkerList[instanceId][workerId] = &WorkerRecorder{
			RequestsList:    make(map[string]int64),
			MayHang:         false,
			LastHangMsgTime: timestamp,
		}
	}
	return l.WorkerList[instanceId][workerId]
}

func (l *LeaderHangDecider) ClearInstance(instanceId string) {
	if _, exist := l.WorkerList[instanceId]; !exist {
		logger.Log().Warnf("instance %s is nil", instanceId)
		return
	}
	if len(l.WorkerList) != 0 {
		for _, worker := range l.WorkerList[instanceId] {
			worker.RequestsList = make(map[string]int64)
		}
	}
	l.WorkerList[instanceId] = make(map[string]*WorkerRecorder)
}

func (l *LeaderHangDecider) OnAllInstancesRenewed(available, unavailable map[string]*client.Instance) {
	if !l.cfg.Enable {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	instancesId := make(map[string]bool)
	for _, instance := range available {
		instancesId[instance.ID] = true
	}
	for _, instance := range unavailable {
		instancesId[instance.ID] = true
	}
	// delete instances
	var emptyInstancesId []string
	for insId := range l.WorkerList {
		if _, exist := instancesId[insId]; !exist {
			emptyInstancesId = append(emptyInstancesId, insId)
		}
	}
	for _, insId := range emptyInstancesId {
		l.ClearInstance(insId)
		delete(l.WorkerList, insId)
		delete(l.HangTag, insId)
	}
	// add instances
	for insId := range instancesId {
		if _, exist := l.WorkerList[insId]; !exist {
			l.WorkerList[insId] = make(map[string]*WorkerRecorder)
			l.HangTag[insId] = false
		}
	}
}

func (l *LeaderHangDecider) OnInstanceTokenEvent(workerId string, instanceEvents []*utils.InstanceEventMsg) {
	if !l.cfg.Enable {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, hangMsg := range instanceEvents {
		targetWorker := l.GetWorker(hangMsg.InstanceId, workerId)
		if targetWorker == nil {
			continue
		}
		targetWorker.MayHang = true
		targetWorker.LastHangMsgTime = time.Now().UnixMilli()
	}
}

func (l *LeaderHangDecider) OnRequestEvents(workerId string, requestEvents []*utils.RequestEventMsg) {
	if !l.cfg.Enable {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, reqMsg := range requestEvents {
		if reqMsg.EventType == constant.RequestNew {
			l.OnNewRequest(workerId, reqMsg)
		} else {
			l.OnFinishedRequest(workerId, reqMsg)
		}
	}
}

func (l *LeaderHangDecider) OnNewRequest(workerId string, reqMsg *utils.RequestEventMsg) {
	reqId := reqMsg.Request.GetRequestId()
	if _, exist := l.WorkerList[reqMsg.TargetInstance]; !exist {
		logger.Log().Warnf("instance %s is nil", reqMsg.TargetInstance)
		return
	}
	targetWorker := l.GetWorker(reqMsg.TargetInstance, workerId)
	if targetWorker == nil {
		targetWorker = l.SetWorker(reqMsg.TargetInstance, workerId)
	}
	targetWorker.RequestsList[reqId] = reqMsg.TimeStamp
}

func (l *LeaderHangDecider) OnFinishedRequest(workerId string, reqMsg *utils.RequestEventMsg) {
	reqId := reqMsg.Request.GetRequestId()
	targetWorker := l.GetWorker(reqMsg.TargetInstance, workerId)
	if targetWorker == nil {
		return
	}
	if _, exist := targetWorker.RequestsList[reqId]; exist {
		delete(targetWorker.RequestsList, reqId)
		if len(targetWorker.RequestsList) == 0 {
			delete(l.WorkerList[reqMsg.TargetInstance], workerId)
		}
	}
}

func (l *LeaderHangDecider) runHangCheckerLoop() {
	if !l.cfg.Enable {
		return
	}
	if l.cfg.Interval <= 0 {
		logger.Log().Errorf("invalid interval of hang check, hang checker loop stop")
		return
	}
	hangCheckInterval := time.Duration(l.cfg.Interval) * time.Millisecond
	ticker := time.NewTicker(hangCheckInterval)
	defer ticker.Stop()
	for {
		select {
		case <-l.closeChan:
			logger.Log().Infof("hang checker stopped")
			return
		case <-ticker.C:
			l.checkLeaderHang()
		}
	}
}

func (l *LeaderHangDecider) checkLeaderHang() {
	l.mu.Lock()
	timestamp := time.Now().UnixMilli()
	hangEvents := make([]*LeaderSubCheckModuleEvent, 0)
	for insId := range l.WorkerList {
		if len(l.WorkerList[insId]) == 0 {
			if l.HangTag[insId] {
				l.HangTag[insId] = false
				logger.Log().Infof("instance %s is not hang now, no hanging message from worker", insId)
			}
			continue
		}
		isHang := true
		for _, worker := range l.WorkerList[insId] {
			if worker.LastHangMsgTime+l.cfg.HangTagValidTime <= timestamp {
				worker.MayHang = false
			}
			if !worker.MayHang {
				isHang = false
			}
		}
		if isHang {
			// Pre-hang probe logic
			if l.cfg.EnableProbeBeforeHangCheck {
				var shouldSkip = false
				if shouldSkip, hangEvents = l.probeBeforeHang(insId, hangEvents); shouldSkip {
					continue
				}
			} else {
				// Probe functionality not enabled, isolate directly
				warnMsg := fmt.Sprintf("[HangCheck] instance %s is hang (all workers agree), trigger isolation", insId)
				hangEvents = addHangEvent(l, insId, warnMsg, hangEvents)
			}
		} else if l.HangTag[insId] {
			oldCount := l.invalidProbeCount[insId]
			l.HangTag[insId] = false
			l.invalidProbeCount[insId] = 0
			logger.Log().Infof("[HangCheck] instance %s is not hang now, reset invalidProbeCount from %d to 0", insId, oldCount)
		}
	}
	if len(hangEvents) != 0 {
		l.leaderHealthChecker.OnSubCheckModuleEvent(hangEvents)
	}
	l.mu.Unlock()
}

func (l *LeaderHangDecider) probeBeforeHang(insId string, hangEvents []*LeaderSubCheckModuleEvent) (shouldSkipThisRound bool, outHangEvents []*LeaderSubCheckModuleEvent) {
	// Get probe status uniformly at the beginning to avoid state inconsistency
	probeStatus := l.getProbeStatus(insId)

	switch probeStatus {
	case ProbeStatusProbing:
		// Probing in progress, skip isolation decision for this round
		return true, hangEvents
	case ProbeStatusSuccess:
		// Probe succeeded and not expired, skip isolation decision for this round
		return true, hangEvents
	case ProbeStatusFailed:
		warnMsg := fmt.Sprintf("[HangCheck] instance %s is hang (probe failed), trigger isolation", insId)
		// Probe failed, proceed with isolation
		hangEvents = addHangEvent(l, insId, warnMsg, hangEvents)
		return false, hangEvents
	case ProbeStatusNone:
		// No probe record or probe success has expired (reset to None by getProbeStatus)
		// First check if probing can be initiated (including checking if instance is healthy)
		if l.leaderHealthChecker.isInstanceHealthy(insId) {
			l.startProbeAsync(insId)
			return true, hangEvents
		} else {
			// Instance already in unhealthy sequence, no need to probe
			warnMsg := fmt.Sprintf("[HangCheck] instance %s is hang (cannot probe, instance unhealthy), trigger isolation", insId)
			hangEvents = addHangEvent(l, insId, warnMsg, hangEvents)
			return false, hangEvents
		}
	default:
		warnMsg := fmt.Sprintf("[HangCheck] instance %s is hang (cannot probe, unrecognized probestatus %v), trigger isolation", insId, probeStatus)
		hangEvents = addHangEvent(l, insId, warnMsg, hangEvents)
		return false, hangEvents
	}
}

func addHangEvent(l *LeaderHangDecider, insId string, warnMsg string, hangEvents []*LeaderSubCheckModuleEvent) []*LeaderSubCheckModuleEvent {
	var workerList []string
	for workerId, _ := range l.WorkerList[insId] {
		workerList = append(workerList, workerId)
	}
	if !l.HangTag[insId] {
		l.HangTag[insId] = true
		logger.Log().Warn(warnMsg)
	}
	hangEvent := &LeaderSubCheckModuleEvent{
		EventType:  InstanceHang,
		EventMsg:   "Hang",
		InstanceId: insId,
	}
	hangEvents = append(hangEvents, hangEvent)
	return hangEvents
}

// ProbeResult represents the result type of a probe
type ProbeResult int

const (
	// ProbeResultSkip indicates no probing needed, proceed directly (isolate or skip)
	ProbeResultSkip ProbeResult = iota
	// ProbeResultProbing indicates probing is in progress, defer decision
	ProbeResultProbing
	// ProbeResultPending indicates probing interval not yet reached, defer decision
	ProbeResultPending
)

// ProbeStatus represents the probe state used for isolation decisions
type ProbeStatus int

const (
	ProbeStatusNone    ProbeStatus = iota // No probe record or probe success has expired
	ProbeStatusProbing                    // Probing in progress
	ProbeStatusSuccess                    // Probe succeeded (not expired)
	ProbeStatusFailed                     // Probe failed
)

// getProbeStatus retrieves the probe status of an instance
// Returns: probeStatus - current probe status
// Description:
// - ProbeStatusNone: No probe record or probe success has expired (can initiate new probe)
// - ProbeStatusProbing: Probing in progress (cannot initiate new probe)
// - ProbeStatusSuccess: Probe succeeded (cannot initiate new probe)
// - ProbeStatusFailed: Probe failed (cannot initiate new probe)
func (l *LeaderHangDecider) getProbeStatus(instanceId string) ProbeStatus {
	// 1. Check if probing is in progress
	if l.probingInstances[instanceId] {
		return ProbeStatusProbing
	}

	// 2. Check probe result state
	lastState, hasState := l.probeStates[instanceId]
	if !hasState {
		// No probe record, can initiate probing
		return ProbeStatusNone
	}

	switch lastState {
	case ProbeStateProbing:
		return ProbeStatusProbing
	case ProbeStateSuccess:
		return l.checkAndResetProbeTimeout(instanceId, ProbeStatusSuccess)
	case ProbeStateFailed:
		return l.checkAndResetProbeTimeout(instanceId, ProbeStatusFailed)
	default:
		// No state or None, can initiate probing
		return ProbeStatusNone
	}
}

// checkAndResetProbeTimeout checks if probe state has expired and resets it
// Returns: probeStatus - current probe status
func (l *LeaderHangDecider) checkAndResetProbeTimeout(instanceId string, defaultStatus ProbeStatus) ProbeStatus {
	lastTime, hasLastProbe := l.lastProbeTime[instanceId]
	if hasLastProbe {
		now := time.Now().UnixMilli()
		if now-lastTime > l.cfg.HangProbeIntervalMs {
			l.probeStates[instanceId] = ProbeStateNone
			return ProbeStatusNone
		}
	}
	return defaultStatus
}

// ProbeState represents the instance probe state
type ProbeState int

const (
	ProbeStateNone    ProbeState = iota // No probe state
	ProbeStateProbing                   // Probing in progress
	ProbeStateSuccess                   // Probe succeeded (prevents isolation)
	ProbeStateFailed                    // Probe failed
)

// SetProbeInferRequest sets the probe inference request module
func (l *LeaderHangDecider) SetProbeInferRequest(probe *ProbeInferRequest) {
	l.probeInferRequest = probe
}

// startProbeAsync initiates probing asynchronously without blocking the main loop
// This function runs within mutex lock
func (l *LeaderHangDecider) startProbeAsync(instanceId string) {
	// Double-check to prevent duplicate initiation
	if l.probingInstances[instanceId] {
		return
	}
	l.probingInstances[instanceId] = true
	l.probeStates[instanceId] = ProbeStateProbing

	logger.Log().Warnf("[HangProbe] instance %s start async probe inference request", instanceId)

	// Get instance object using LeaderHealthChecker's safe method
	instance := l.leaderHealthChecker.GetInstanceById(instanceId)

	// Execute probing asynchronously
	go func() {
		// Ensure state cleanup
		defer func() {
			l.mu.Lock()
			delete(l.probingInstances, instanceId)
			l.mu.Unlock()
		}()

		// Check probe module
		if l.probeInferRequest == nil || instance == nil {
			l.mu.Lock()
			l.probeStates[instanceId] = ProbeStateFailed
			l.lastProbeTime[instanceId] = time.Now().UnixMilli()
			l.mu.Unlock()
			return
		}

		// Execute probe
		successToCheck, testSuccess, _ := l.probeInferRequest.sendProbeInferRequestWithTimeout(
			instance, "hang-probe")

		now := time.Now().UnixMilli()
		l.mu.Lock()
		l.lastProbeTime[instanceId] = now

		if successToCheck {
			// Valid probe: reset counter
			oldCount := l.invalidProbeCount[instanceId]
			l.invalidProbeCount[instanceId] = 0
			logger.Log().Infof("[HangProbe] instance %s valid probe received, reset invalidProbeCount from %d to 0",
				instanceId, oldCount)

			if testSuccess {
				// Probe succeeded
				l.probeStates[instanceId] = ProbeStateSuccess
				logger.Log().Infof("[HangProbe] instance %s probe success, will block hang isolation", instanceId)
			} else {
				// Valid probe but failed
				l.probeStates[instanceId] = ProbeStateFailed
				logger.Log().Warnf("[HangProbe] instance %s probe failed (valid probe)", instanceId)
			}
		} else {
			// Invalid probe: increment counter
			l.invalidProbeCount[instanceId]++
			currentCount := l.invalidProbeCount[instanceId]
			maxCount := l.cfg.MaxConsecutiveInvalidProbes

			if currentCount >= maxCount {
				// Maximum consecutive invalid probes reached, proceed with isolation
				l.probeStates[instanceId] = ProbeStateFailed
				logger.Log().Warnf("[HangProbe] instance %s has %d consecutive invalid probes (max: %d), force isolation",
					instanceId, currentCount, maxCount)
			} else if currentCount >= maxCount-1 {
				// Threshold-1 reached, print warning log
				l.probeStates[instanceId] = ProbeStateNone
				logger.Log().Warnf("[HangProbe] instance %s has %d consecutive invalid probes, will force isolation on next failure (max: %d)",
					instanceId, currentCount, maxCount)
			} else {
				// Threshold not reached, set state to None and wait for next probe
				l.probeStates[instanceId] = ProbeStateNone
				logger.Log().Infof("[HangProbe] instance %s has %d consecutive invalid probes (max: %d), wait for next probe",
					instanceId, currentCount, maxCount)
			}
		}
		l.mu.Unlock()
	}()
}

func (l *LeaderHangDecider) Stop() {
	close(l.closeChan)
}
