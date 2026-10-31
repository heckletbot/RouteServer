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

package etcd

import (
	"context"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	peer_client "infer-dp-scheduler/pkg/common/client/peer-client"
	"infer-dp-scheduler/pkg/common/cluster"
	"infer-dp-scheduler/pkg/common/config"
	"infer-dp-scheduler/pkg/common/logger"
	"infer-dp-scheduler/pkg/common/utils"
	cluster2 "infer-dp-scheduler/pkg/route-server/cluster"
	"infer-dp-scheduler/pkg/route-server/metrics"
)

type EtcdClusterService struct {
	config               config.ClusterConfig
	initRoutes           *config.RouteTable ``
	localNode            *utils.Peer
	stateChangeListeners []cluster.StateApplier
	stopChan             chan struct{}
	status               EtcdStatus
	healthStatsCache     cluster.EtcdHealthStatsCache
	healthStatsLock      sync.RWMutex
	peerClient           *peer_client.PeerClient

	etcdWrapper atomic.Value

	// value is cluster.ClusterState
	atomicState atomic.Value

	failureUpdateRound atomic.Int32
}

func (ecs *EtcdClusterService) GetInstanceCleanHandler() utils.Subscriber {
	return cluster.NewInstanceLeaveMessageHandler(ecs)
}

func (ecs *EtcdClusterService) IsSingleton() bool {
	return false
}
func (ecs *EtcdClusterService) getCfg() config.ClusterConfig {
	return ecs.config
}
func (ecs *EtcdClusterService) GetHealthStats() (cluster.EtcdHealthStatsCache, error) {
	ecs.healthStatsLock.RLock()
	defer ecs.healthStatsLock.RUnlock()

	if ecs.config.EtcdHealthExpireTimeMs <= 0 {
		// expiration time not set, skip expiration check
		return ecs.healthStatsCache, nil
	}
	expireDuration := time.Duration(ecs.config.EtcdHealthExpireTimeMs) * time.Millisecond
	if time.Since(ecs.healthStatsCache.UpdateTime) > expireDuration {
		logger.Log().Warnf("ETCD health stats cache expired, updateTime: %v, expireTime: %v",
			ecs.healthStatsCache.UpdateTime, expireDuration)
		return cluster.EtcdHealthStatsCache{}, nil
	}

	return ecs.healthStatsCache, nil
}

func (ecs *EtcdClusterService) UpdateHealthStats(cache cluster.EtcdHealthStatsCache) error {
	ecs.healthStatsLock.Lock()
	defer ecs.healthStatsLock.Unlock()

	ecs.healthStatsCache = cache
	return nil
}

func (ecs *EtcdClusterService) Init(config config.ClusterConfig, initRoute *config.RouteTable, localPeer *utils.Peer, peerClient *peer_client.PeerClient) {
	ecs.config = config
	ecs.localNode = localPeer
	ecs.initRoutes = initRoute
	ecs.peerClient = peerClient
	ecs.stateChangeListeners = make([]cluster.StateApplier, 0)
	if ecs.config.Verbose {
		ecs.RegisterStateApplier(cluster2.StateChangeReport{})
	}
	ecs.failureUpdateRound.Store(0)
	etcdWrapper := &EtcdWrapper{
		logId: 0,
	}
	etcdWrapper.setStatus(StatusDisconnected)
	etcdWrapper.localNode = localPeer
	etcdWrapper.leaderId.Store("")
	ecs.etcdWrapper.Store(etcdWrapper)
}

func (ecs *EtcdClusterService) Start() error {
	etcdCfg := ecs.config.EtcdCfg
	if etcdCfg == nil {
		return fmt.Errorf("etcd cfg is nil")
	}
	go ecs.wrapperLoop()
	return nil
}

func (ecs *EtcdClusterService) wrapperLoop() {
	checkInterval := time.Duration((ecs.config.EtcdCfg.LeaseTtlSec*1000)/3) * time.Millisecond
	ticker := time.NewTicker(checkInterval)
	defer ticker.Stop()

	wrapperId := 0
	logger.Log().Infof("starting etcd-wrapper loop")
	for {
		select {
		case <-ecs.stopChan:
			logger.Log().Infof("stop etcd-wrapper loop")
			return
		case <-ticker.C:
			if !ecs.wrapper().CheckConnection() {
				logger.Log().Warnf("etcd wrapper [%v] connection lost, reconnecting", ecs.wrapper().logId)
				logger.Log().Infof("etcd reconnect: 1. closing old [%v] connection", ecs.wrapper().logId)
				ecs.wrapper().Close()
				wrapperId++
				metrics.RecordETCDConnectionsRetries()
				etcdWrapper := &EtcdWrapper{
					logId: wrapperId,
				}
				logger.Log().Infof("etcd reconnect: 2. start new connection, new etcd wrapperId = [%v]", etcdWrapper.logId)
				err := etcdWrapper.Init(ecs.localNode, &ecs.config, ecs.initRoutes, ecs.updateLocalState, ecs.State, &ecs.stateChangeListeners, ecs.failureUpdateRound.Load(), ecs.UpdateHealthStats)
				if err != nil {
					logger.Log().Errorf("failed to start new wrapper [%v], retry later, err = %v", etcdWrapper.logId, err)
					etcdWrapper.Close()
					continue
				}
				ecs.etcdWrapper.Store(etcdWrapper)
				logger.Log().Infof("etcd wrapper [%v] reconnect: connection resumed", etcdWrapper.logId)
			}
		}
	}
}

func (ecs *EtcdClusterService) IsLeader() bool {
	curLeaderId := ecs.getLeaderIdFromWrapper()
	if curLeaderId == "" {
		return false
	}
	return curLeaderId == ecs.localNode.Name
}

func (ecs *EtcdClusterService) getLeaderIdFromWrapper() string {
	if ecs.wrapper() == nil {
		return ""
	}
	if !ecs.wrapper().CheckConnection() {
		return ""
	}
	curLeaderId := ecs.wrapper().getLeaderId()
	return curLeaderId
}

func (ecs *EtcdClusterService) GetLeader() (*utils.Peer, string) {
	curLeaderId := ecs.getLeaderIdFromWrapper()
	if curLeaderId == "" {
		return nil, ""
	}
	state := ecs.State()

	exists := false
	var leaderPeer *utils.Peer
	if state == nil || state.Peers == nil {
		exists = true
	} else {
		leaderPeer, exists = state.Peers[curLeaderId]
	}
	if !exists {
		logger.Log().Warnf("leader [%s] not exists in clusterstate.peers yet", curLeaderId)
	}
	return leaderPeer, curLeaderId
}

func (ecs *EtcdClusterService) IsWorkerAlive(peerName string) (bool, error) {
	if ecs.wrapper() == nil {
		return false, fmt.Errorf("etcd wrapper is nil")
	}
	if !ecs.wrapper().CheckConnection() {
		return false, fmt.Errorf("etcd wrapper is disconnected")
	}

	return ecs.wrapper().isPeerRegistered(peerName)
}

func (ecs *EtcdClusterService) LocalNode() *utils.Peer {
	return ecs.localNode
}

func (ecs *EtcdClusterService) Stats() map[string]string {
	return map[string]string{}
}

func (ecs *EtcdClusterService) RegisterStateApplier(applier cluster.StateApplier) {
	if applier != nil {
		ecs.stateChangeListeners = append(ecs.stateChangeListeners, applier)
	}
}

func (ecs *EtcdClusterService) GetApplierNames() []string {
	names := make([]string, 0, len(ecs.stateChangeListeners))
	for _, listener := range ecs.stateChangeListeners {
		names = append(names, listener.GetName())
	}
	return names
}

func (ecs *EtcdClusterService) Close() {
	leaderPeer, _ := ecs.GetLeader()
	if ecs.stopChan != nil {
		close(ecs.stopChan)
	}
	ecs.wrapper().Close()
	logger.Log().Infof("etcd cluster service closed")
	ecs.workerGracefulExit(leaderPeer)
}

func (ecs *EtcdClusterService) workerGracefulExit(leaderPeer *utils.Peer) {
	if leaderPeer == nil {
		logger.Log().Warnf("can't find leader, skip graceful de-register.")
		return
	}
	if ecs.peerClient == nil {
		return
	}
	if leaderPeer.Name == ecs.LocalNode().Name {
		logger.Log().Infof("skip graceful de-register due to exit as current elect leader")
		return
	}
	logger.Log().Infof("rs peer is going to close gracefully by sending leave request to leader [%s]", leaderPeer.Name)
	leaveRequest := cluster.LeaveRequest{
		LeavePeerName: ecs.LocalNode().Name,
	}
	body, err := utils.SonicApi.Marshal(leaveRequest)
	if err != nil {
		logger.Log().Warnf("rs peer failed to close gracefully due to marshal err for leave req, err = %v", err)
		return
	}
	res, err := ecs.peerClient.SendToPeer(context.Background(), body, leaderPeer,
		utils.LeaveHttpPath, http.MethodPost, 5000)
	if err != nil {
		logger.Log().Warnf("rs peer failed to close gracefully due to send err for leave req, err = %v", err)
		return
	}
	logger.Log().Infof("rs peer successfully sent leave request to current leader [%s], response %s",
		leaderPeer.Name, res)
}

func (ecs *EtcdClusterService) State() *cluster.ClusterState {
	rawState := ecs.atomicState.Load()
	if rawState == nil {
		return nil
	}
	if statePtr, ok := rawState.(*cluster.ClusterState); !ok {
		logger.Log().Warnf(
			"when load state, state is not clusterState ptr in atomic.Value, got %v",
			reflect.TypeOf(statePtr).Name())
		return nil
	} else {
		return statePtr
	}
}

func (ecs *EtcdClusterService) PersistInstanceFailures(failures map[string]*utils.InstanceFailureRecord) error {
	if err := ecs.ensureLeader(); err != nil {
		return err
	}
	round := ecs.failureUpdateRound.Add(1)
	if err := ecs.wrapper().writeInstanceFailures(failures, round); err != nil {
		ecs.failureUpdateRound.Add(-1)
		return err
	}
	return nil
}

func (ecs *EtcdClusterService) HandleNewPeer(peer *utils.Peer) error {
	if err := ecs.ensureLeader(); err != nil {
		return err
	}
	err := cluster.CheckPeerValid(peer)
	if err != nil {
		return fmt.Errorf("peer [%s] is invalid, err = %v", peer.Name, err)
	}
	err = ecs.wrapper().writePeerToEtcd(RSClusterStateAllPeersDir, peer, false)
	if err != nil {
		return fmt.Errorf("failed to write peer [%s] to etcd state, err = %v", peer.Name, err)
	}
	logger.Log().Infof("successfully add peer [%s] to etcd state", peer.Name)
	return nil
}

func (ecs *EtcdClusterService) HandleLeavePeer(names []string, reason string) error {
	if err := ecs.ensureLeader(); err != nil {
		return err
	}
	if names == nil || len(names) == 0 {
		return nil
	}
	peerKeys := make([]string, 0, len(names))
	for _, peerName := range names {
		if peerName == "" {
			continue
		}
		peerKey := fmt.Sprintf("%s%s", RSClusterStateAllPeersDir, peerName)
		peerKeys = append(peerKeys, peerKey)
	}

	if err := ecs.wrapper().deleteKeysFromEtcd(peerKeys); err != nil {
		return fmt.Errorf("failed to delete peers [%v] from etcd state, reason [%s], err = %v", names, reason, err)
	}
	logger.Log().Infof("successfully delete peer [%v] from etcd state, reason [%s]", names, reason)
	return nil
}

func (ecs *EtcdClusterService) HandleNewInstance(req *cluster.InstanceJoinRequest) error {
	if err := ecs.ensureLeader(); err != nil {
		return err
	}
	if req == nil {
		return fmt.Errorf("req is nil")
	}
	if err := req.ValidCheck(); err != nil {
		return err
	}
	if err := ecs.checkInstancesExistInState(req.Instances); err != nil {
		return err
	}
	if err := ecs.wrapper().writeInstancesToEtcd(req.Instances); err != nil {
		return fmt.Errorf("failed to write instance [%s] to etcd state, err = %v",
			utils.MapKeysStr(req.Instances), err)
	}
	logger.Log().Infof("successfully add instance [%s] to etcd state", utils.MapKeysStr(req.Instances))
	return nil
}

func (ecs *EtcdClusterService) checkInstancesExistInState(instances map[string]*config.Route) error {
	if len(instances) == 0 {
		return nil
	}
	state := ecs.State()
	if state == nil || state.InstanceRoutes == nil {
		return nil
	}
	var duplicateNames []string
	for name, newInstance := range instances {
		if existInstance, exists := state.InstanceRoutes[name]; exists {
			if existInstance.Ip == newInstance.Ip &&
				existInstance.Port == newInstance.Port &&
				existInstance.Role == newInstance.Role {
				continue
			}
			duplicateNames = append(duplicateNames, name)
		}
	}
	if len(duplicateNames) > 0 {
		return fmt.Errorf("instance(s) [%s] already exist in cluster, reject duplicate instance(s)",
			strings.Join(duplicateNames, ","))
	}
	return nil
}

func (ecs *EtcdClusterService) HandleLeaveInstance(req *cluster.InstanceLeaveRequest, leaveReason string) error {
	if err := ecs.ensureLeader(); err != nil {
		return err
	}
	if req == nil {
		return fmt.Errorf("req is nil")
	}
	if err := req.ValidCheck(); err != nil {
		return err
	}
	removeKeys := make([]string, 0, len(req.InstanceNames))
	for _, name := range req.InstanceNames {
		removeKeys = append(removeKeys, fmt.Sprintf("%s%s", RSClusterStateAllInstancesDir, name))
	}
	if err := ecs.wrapper().deleteKeysFromEtcd(removeKeys); err != nil {
		return fmt.Errorf("failed to delete instance [%s] from etcd state, reason [%s] , err = %v", req.InstanceNames, leaveReason, err)
	}
	logger.Log().Infof("successfully delete instance [%s] from etcd state, reason [%s]", req.InstanceNames, leaveReason)
	return nil
}

// ----------  start up ------------

func (ecs *EtcdClusterService) updateLocalState(state *cluster.ClusterState, reason string) {
	state.Version = time.Now().UnixMilli()
	ecs.atomicState.Store(state)
	if reason == cluster.StateUpdateReasonRestore {
		for _, listener := range ecs.stateChangeListeners {
			listener.OnStateRestored(state)
		}
	}
	if ecs.config.Verbose {
		stateStr := ""
		if marshal, err := utils.SonicApi.Marshal(state); err != nil {
			stateStr = fmt.Sprintf("%v", state)
		} else {
			stateStr = string(marshal)
		}
		logger.Log().Infof("successfully update local state, reason [%s], %s", reason, stateStr)
	}
}

// ----------------- utils ----------------

func (ecs *EtcdClusterService) ensureLeader() error {
	if !ecs.IsLeader() {
		leaderId := ecs.getLeaderIdFromWrapper()
		if leaderId == "" {
			return fmt.Errorf("no leader is elected yet")
		}
		return fmt.Errorf("peer [%s] is not a leader, current leader is %s", ecs.localNode.Name, leaderId)
	}
	return nil
}

func (ecs *EtcdClusterService) wrapper() *EtcdWrapper {
	if wrapper, ok := ecs.etcdWrapper.Load().(*EtcdWrapper); !ok {
		panic("etcd wrapper is not a EtcdWrapper")
	} else {
		return wrapper
	}
}
