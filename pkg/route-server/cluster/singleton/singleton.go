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

package singleton

import (
	"errors"
	"sync"

	peer_client "infer-dp-scheduler/pkg/common/client/peer-client"
	"infer-dp-scheduler/pkg/common/cluster"
	"infer-dp-scheduler/pkg/common/config"
	"infer-dp-scheduler/pkg/common/constant"
	"infer-dp-scheduler/pkg/common/logger"
	"infer-dp-scheduler/pkg/common/utils"
)

var errorNoJoin = errors.New("cluster type is set to singleton in config, can't accept other nodes")
var errorNoLeave = errors.New("cluster type is set to singleton in config, can't handle leave nodes")
var errorNoCache = errors.New("cluster type is set to singleton in config, can't support get etcd health cache")

type SingletonClusterService struct {
	localPeer  *utils.Peer
	localState *cluster.ClusterState
	appliers   []cluster.StateApplier

	stateMutex sync.Mutex
}

func (s *SingletonClusterService) IsWorkerAlive(peerName string) (bool, error) {
	return true, nil
}

func (s *SingletonClusterService) GetInstanceCleanHandler() utils.Subscriber {
	return cluster.NewInstanceLeaveMessageHandler(s)
}

func (s *SingletonClusterService) PersistInstanceFailures(failures map[string]*utils.InstanceFailureRecord) error {
	logger.Log().Warnf("Singleton impletation ignore instance failures records")
	return nil
}

func (s *SingletonClusterService) HandleNewInstance(req *cluster.InstanceJoinRequest) error {
	s.stateMutex.Lock()
	defer s.stateMutex.Unlock()
	for name, instance := range req.Instances {
		if name == "" || instance == nil {
			logger.Log().Warnf("reject new instance [%s] due to invalid input, empty name or nil route", name)
			continue
		}
		s.localState.InstanceRoutes[name] = instance
	}
	for _, listener := range s.appliers {
		listener.OnAddInstance(req.Instances, constant.InstanceChangeReasonSynced)
	}
	logger.Log().Warnf("done add instances %s", req.InstanceNames())
	return nil
}

func (s *SingletonClusterService) HandleLeaveInstance(req *cluster.InstanceLeaveRequest, leaveReason string) error {
	s.stateMutex.Lock()
	defer s.stateMutex.Unlock()
	for _, name := range req.InstanceNames {
		delete(s.localState.InstanceRoutes, name)
	}
	for _, listener := range s.appliers {
		listener.OnRemoveInstance(req.InstanceNames, constant.InstanceChangeReasonSynced)
	}
	logger.Log().Warnf("done remove instances %s due to [%s]", req.InstanceNames, leaveReason)
	return nil
}

func (s *SingletonClusterService) Stats() map[string]string {
	return make(map[string]string)
}

func (s *SingletonClusterService) IsSingleton() bool {
	return true
}

func (s *SingletonClusterService) GetHealthStats() (cluster.EtcdHealthStatsCache, error) {
	return cluster.EtcdHealthStatsCache{}, errorNoCache
}

func (s *SingletonClusterService) UpdateHealthStats(cache cluster.EtcdHealthStatsCache) error {
	logger.Log().Debug("Singleton implementation ignore EtcdHealthStatsCache, because singleton has no etcd")
	return errorNoCache
}

func (s *SingletonClusterService) Close() {}

func (s *SingletonClusterService) Init(cfg config.ClusterConfig, initRoute *config.RouteTable, localPeer *utils.Peer, peerClient *peer_client.PeerClient) {
	s.localPeer = localPeer
	endPoint, err := utils.PeerHttpEndpoint(localPeer, "", true)
	if err != nil {
		logger.Log().Error(err.Error())
	}
	s.localState = &cluster.ClusterState{
		Initialized: true,
		SeedPeers: []config.SeedInfo{
			{
				Name:         localPeer.Name,
				HttpEndpoint: endPoint,
			},
		},
		Peers: map[string]*utils.Peer{
			localPeer.Name: localPeer,
		},
		InstanceRoutes: make(map[string]*config.Route),
		Version:        0,
	}
	if initRoute != nil {
		for id, route := range initRoute.InstanceRoutes {
			s.localState.InstanceRoutes[id] = &route
		}
	}
}

func (s *SingletonClusterService) Start() error {
	for _, listener := range s.appliers {
		listener.OnStateRestored(s.State())
	}
	for _, listener := range s.appliers {
		listener.OnLeaderChanged(s.localPeer, true, nil)
	}
	return nil
}

func (s *SingletonClusterService) IsLeader() bool {
	return true
}

func (s *SingletonClusterService) GetLeader() (*utils.Peer, string) {
	return s.localPeer, s.localPeer.Name
}

func (s *SingletonClusterService) LocalNode() *utils.Peer {
	return s.localPeer
}

func (s *SingletonClusterService) State() *cluster.ClusterState {
	return s.localState
}

func (s *SingletonClusterService) HandleNewPeer(peer *utils.Peer) error {
	return errorNoJoin
}

func (s *SingletonClusterService) HandleLeavePeer(names []string, reason string) error {
	return errorNoLeave
}

func (s *SingletonClusterService) RegisterStateApplier(applier cluster.StateApplier) {
	if applier != nil {
		s.appliers = append(s.appliers, applier)
	}
}

func (s *SingletonClusterService) GetApplierNames() []string {
	names := make([]string, 0, len(s.appliers))
	for _, listener := range s.appliers {
		names = append(names, listener.GetName())
	}
	return names
}
