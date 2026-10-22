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
	"context"
	"net/http"
	"sync"
	"time"

	peer_client "infer-dp-scheduler/pkg/common/client/peer-client"
	"infer-dp-scheduler/pkg/common/cluster"
	"infer-dp-scheduler/pkg/common/config"
	"infer-dp-scheduler/pkg/common/constant"
	"infer-dp-scheduler/pkg/common/logger"
	"infer-dp-scheduler/pkg/common/utils"
	"infer-dp-scheduler/pkg/route-server/metrics"
)

type WorkerEventCollector struct {
	RequestEventChan  chan *utils.RequestEventMsg
	TokenEventChan    chan *WorkerLocalTokenEvent
	closeChan         chan struct{}
	HangChecker       *WorkerHangChecker
	WorkerId          string
	InstanceEventTime time.Duration
}

func (collector *WorkerEventCollector) Init(cfg *config.Config) {
	collector.RequestEventChan = make(chan *utils.RequestEventMsg, 100000)
	collector.TokenEventChan = make(chan *WorkerLocalTokenEvent, 100000)
	hangChecker := &WorkerHangChecker{}
	hangChecker.Init(&cfg.InstanceManagerCfg.HealthCheckConfig.AvailableCheckersCfg.HangCheckerCfg)
	collector.HangChecker = hangChecker
	collector.closeChan = make(chan struct{})
	collector.WorkerId = cfg.ServerConfig.Name
	collector.InstanceEventTime = time.Duration(cfg.InstanceManagerCfg.HealthCheckConfig.EventThresholdIntervalMs) * time.Millisecond
	go collector.runRequestTokenEventLoop()
}

func (collector *WorkerEventCollector) OnRequestNew(request utils.GenerateRequest, inferMsg *InferMsg) {
	event := &utils.RequestEventMsg{
		Request:            utils.WrapRequest(request, false),
		TargetInstance:     inferMsg.getTargetInstance(request.GetType()),
		PreInstance:        inferMsg.getPreInstance(request.GetType()),
		EventType:          constant.RequestNew,
		FailureDetails:     nil,
		InternalTestingReq: inferMsg.IsTesting,
		TimeStamp:          time.Now().UnixMilli(),
	}
	collector.HangChecker.OnRequestEvent(event)
	collector.RequestEventChan <- event
}

func (collector *WorkerEventCollector) OnRequestSuccess(request utils.GenerateRequest, inferMsg *InferMsg) {
	event := &utils.RequestEventMsg{
		Request:            utils.WrapRequest(request, false),
		TargetInstance:     inferMsg.getTargetInstance(request.GetType()),
		PreInstance:        inferMsg.getPreInstance(request.GetType()),
		FailureDetails:     nil,
		InternalTestingReq: inferMsg.IsTesting,
		TimeStamp:          time.Now().UnixMilli(),
	}
	if eventType, exists := constant.ReqSuccessEventMap[request.GetType()]; exists {
		event.EventType = eventType
	} else {
		logger.Log().Warnf("Unknown request type: %s", request.GetType())
	}
	collector.HangChecker.OnRequestEvent(event)
	collector.RequestEventChan <- event
}

func (collector *WorkerEventCollector) OnRequestFail(request utils.GenerateRequest, inferMsg *InferMsg, details *utils.InstanceFailureDetails) {
	event := NewRequestEvent(request, inferMsg, details)
	collector.RequestEventChan <- event
	collector.HangChecker.OnRequestEvent(event)
}

func (collector *WorkerEventCollector) OnLocalTokenEvent(reqId string, targetInstanceId string, tokenCount int) {
	tokenEvent := &WorkerLocalTokenEvent{
		instanceId: targetInstanceId,
		reqId:      reqId,
		timestamp:  time.Now().UnixMilli(),
		tokenCount: tokenCount,
	}
	collector.TokenEventChan <- tokenEvent
}

func (collector *WorkerEventCollector) runRequestTokenEventLoop() {
	for {
		select {
		case <-collector.closeChan:
			return
		case event, ok := <-collector.TokenEventChan:
			if !ok {
				return
			}
			collector.HangChecker.OnLocalDecodeTokens(event)
		}
	}
}

func (collector *WorkerEventCollector) close() {
	close(collector.closeChan)
}

type WorkerLocalTokenEvent struct {
	instanceId string
	reqId      string
	timestamp  int64
	tokenCount int
}

// HealthStatsUpdater update health stats
type HealthStatsUpdater interface {
	UpdateHealthStats(roleHealthyCounts map[string]int, updateTime *time.Time) error
}

type EventSyncer interface {
	collectEventsLoop()
	sendEventsLoop(eventCheckInterval time.Duration)
	getInstanceHangEvents() []*utils.InstanceEventMsg
	GetWorkerEventCollector() *WorkerEventCollector
	close()
	cluster.StateApplier
}

type WorkerEventSyncer struct {
	collector             *WorkerEventCollector
	closeChan             chan struct{}
	cfg                   *config.Config
	requestEvents         []*utils.RequestEventMsg
	maxRequestEvents      int
	leaderPeer            *utils.Peer
	mu                    sync.Mutex
	muPeer                sync.Mutex
	peerClient            *peer_client.PeerClient
	leaderLocalMsgChannel chan<- any
	clusterService        cluster.ClusterService
	healthStatsUpdater    HealthStatsUpdater
	workerBootId          string
}

func NewWorkerEventSyncer(cfg *config.Config, client *peer_client.PeerClient, workerBootId string, msgCenter utils.MessageCenter, clusterService cluster.ClusterService) EventSyncer {
	workerEventCollector := &WorkerEventCollector{}
	workerEventCollector.Init(cfg)
	// in the same process and channels will ensure that the pointer will not be released
	msgChan, err := msgCenter.Register(constant.LocalWorkerEventsTopic, &utils.WorkerEventsRequest{})
	if err != nil {
		logger.Log().Errorf("error when register to message center: %v", err)
	}
	syncer := &WorkerEventSyncer{
		collector:             workerEventCollector,
		closeChan:             make(chan struct{}),
		cfg:                   cfg,
		requestEvents:         make([]*utils.RequestEventMsg, 0),
		maxRequestEvents:      constant.MaxRequestEventsNum,
		leaderPeer:            nil,
		mu:                    sync.Mutex{},
		peerClient:            client,
		leaderLocalMsgChannel: msgChan,
		clusterService:        clusterService,
		workerBootId:          workerBootId,
	}
	logger.Log().Infof("WorkerEventSyncer generated bootId [%s] for worker [%s]", syncer.workerBootId, workerEventCollector.WorkerId)
	eventCheckInterval := time.Duration(syncer.cfg.InstanceManagerCfg.HealthCheckConfig.EventThresholdIntervalMs) * time.Millisecond
	go syncer.collectEventsLoop()
	go syncer.sendEventsLoop(eventCheckInterval)
	return syncer
}

func (syncer *WorkerEventSyncer) collectEventsLoop() {
	for {
		select {
		case <-syncer.closeChan:
			return
		case reqEvent, ok := <-syncer.collector.RequestEventChan:
			if !ok {
				return
			}
			syncer.mu.Lock()
			syncer.requestEvents = append(syncer.requestEvents, reqEvent)
			if len(syncer.requestEvents) > syncer.maxRequestEvents {
				eventToDelNums := len(syncer.requestEvents) - syncer.maxRequestEvents
				syncer.requestEvents = syncer.requestEvents[eventToDelNums:]
				logger.Log().Errorf("The number of requestEvents exceeds the maxRequestEvents limit; clean up the %d oldest requests.", eventToDelNums)
			}
			syncer.mu.Unlock()
		}
	}
}

func (syncer *WorkerEventSyncer) sendEventsLoop(eventCheckInterval time.Duration) {
	ticker := time.NewTicker(eventCheckInterval)
	defer ticker.Stop()
	var instanceEvents []*utils.InstanceEventMsg
	for {
		select {
		case <-syncer.closeChan:
			return
		case <-ticker.C:
			syncer.mu.Lock()
			instanceEvents = syncer.getInstanceHangEvents()
			tmpReqList := make([]*utils.RequestEventMsg, len(syncer.requestEvents))
			copy(tmpReqList, syncer.requestEvents)
			syncer.requestEvents = make([]*utils.RequestEventMsg, 0)

			syncer.mu.Unlock()
			req := &utils.WorkerEventsRequest{
				WorkerId:       syncer.collector.WorkerId,
				WorkerBootId:   syncer.workerBootId,
				RequestEvents:  tmpReqList,
				InstanceEvents: instanceEvents,
			}
			if syncer.clusterService.IsLeader() {
				syncer.leaderLocalMsgChannel <- req
			} else {
				syncer.sendEventsToLeaderAndUpdateStats(req, tmpReqList)
				metrics.SetEventsRemaining(len(syncer.requestEvents))
			}

		}
	}
}

func (syncer *WorkerEventSyncer) sendEventsToLeaderAndUpdateStats(req *utils.WorkerEventsRequest, tmpReqList []*utils.RequestEventMsg) {
	data, err := utils.SonicApi.Marshal(req)
	if err != nil {
		logger.Log().Errorf("marshal WorkerEventsRequest failed, err = %v", err)
		return
	}
	syncer.muPeer.Lock()
	peer := cluster.CopyLeaderPeer(syncer.leaderPeer)
	syncer.muPeer.Unlock()
	av, err := syncer.peerClient.SendToPeer(context.Background(), data, peer, constant.RequestEventPath, http.MethodPost, 5000)
	if err != nil {
		logger.Log().Errorf("send WorkerEventsRequest to leader failed, err = %v", err)
		syncer.mu.Lock()
		syncer.requestEvents = append(tmpReqList, syncer.requestEvents...)
		syncer.mu.Unlock()
		return
	}
	var everyRoleAvailableCountMap map[string]int
	if err := utils.SonicApi.Unmarshal(av, &everyRoleAvailableCountMap); err != nil {
		logger.Log().Errorf("unmarshal leader response failed, err = %v, body = %s", err, string(av))
		return
	}
	logger.Log().Tracef("recv leader response : %v", everyRoleAvailableCountMap)
	if syncer.healthStatsUpdater != nil {
		if err = syncer.healthStatsUpdater.UpdateHealthStats(everyRoleAvailableCountMap, nil); err != nil {
			logger.Log().Errorf("update health stats failed, err = %v", err)
		}
	}
}

func (syncer *WorkerEventSyncer) getInstanceHangEvents() []*utils.InstanceEventMsg {
	return syncer.collector.HangChecker.InstanceHangCheck(time.Now().UnixMilli())
}

func (syncer *WorkerEventSyncer) GetWorkerEventCollector() *WorkerEventCollector {
	return syncer.collector
}

func (syncer *WorkerEventSyncer) SetHealthStatsUpdater(updater HealthStatsUpdater) {
	syncer.healthStatsUpdater = updater
}

func (syncer *WorkerEventSyncer) close() {
	close(syncer.closeChan)
	syncer.collector.close()
}

func (syncer *WorkerEventSyncer) OnLeaderChanged(newLeader *utils.Peer, isMeLeader bool, leaderRestoreFailures *utils.AllInstanceFailuresSnapshot) {
	logger.Log().Infof("WorkerEventSyncer receives OnLeaderChanged, new leader %v, is me leader? %v", newLeader, isMeLeader)
	if newLeader != nil {
		syncer.muPeer.Lock()
		syncer.leaderPeer = newLeader
		syncer.muPeer.Unlock()
	}
}

func (syncer *WorkerEventSyncer) OnStateRestored(state *cluster.ClusterState) {
}

func (syncer *WorkerEventSyncer) OnAddPeer(peer *utils.Peer) {
}

func (syncer *WorkerEventSyncer) OnRemovePeer(name string) {
}

func (syncer *WorkerEventSyncer) OnAddInstance(map[string]*config.Route, string) {}

func (syncer *WorkerEventSyncer) OnRemoveInstance(names []string, reason string) {}

func (syncer *WorkerEventSyncer) GetName() string { return "WorkerEventSyncer" }
