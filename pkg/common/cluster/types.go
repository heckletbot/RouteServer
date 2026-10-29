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

package cluster

import (
	"time"

	peer_client "infer-dp-scheduler/pkg/common/client/peer-client"
	"infer-dp-scheduler/pkg/common/config"
	"infer-dp-scheduler/pkg/common/utils"
)

const (
	TypeSingleton = "singleton"
	TypeEtcd      = "etcd"
)

type ClusterState struct {
	Initialized    bool                     `json:"initialized"`
	SeedPeers      []config.SeedInfo        `json:"seed_peers"`
	Peers          map[string]*utils.Peer   `json:"peers"`
	InstanceRoutes map[string]*config.Route `yaml:"instance-routes"`
	Version        int64                    `json:"version"`
}

type EtcdHealthStatsCache struct {
	EveryRoleAvailableCountMap map[string]int
	FailureInstanceDetail      map[string]map[string]*utils.InstanceFailureRecord // role id InstanceFailureRecord
	UpdateTime                 time.Time
}

func DeepCopyState(cs *ClusterState) *ClusterState {
	if cs == nil {
		return nil
	}
	copied := &ClusterState{
		Initialized: cs.Initialized,
		Version:     cs.Version,
	}
	copySeeds(cs, copied)
	copyPeers(cs, copied)
	copyInstances(cs, copied)
	return copied
}

func copyInstances(cs *ClusterState, copied *ClusterState) {
	copied.InstanceRoutes = make(map[string]*config.Route)
	for insName, instance := range cs.InstanceRoutes {
		copiedInstance := config.Route{
			Ip:          instance.Ip,
			Port:        instance.Port,
			Role:        instance.Role,
			StaticInfos: instance.StaticInfos,
		}
		copied.InstanceRoutes[insName] = &copiedInstance
	}
}

func copyPeers(cs *ClusterState, copied *ClusterState) {
	copied.Peers = make(map[string]*utils.Peer)
	for peerId, peer := range cs.Peers {
		copiedPeer := CopyLeaderPeer(peer)
		if copiedPeer == nil {
			continue
		}
		copied.Peers[peerId] = copiedPeer
	}
}

func CopyLeaderPeer(peer *utils.Peer) *utils.Peer {
	if peer == nil {
		return nil
	}
	newPeer := &utils.Peer{
		Name: peer.Name,
		Addr: utils.PeerAddr{
			Host:     peer.Addr.Host,
			HttpPort: peer.Addr.HttpPort,
			OpsPort:  peer.Addr.OpsPort,
			Domain:   peer.Addr.Domain,
		},
		IsCandidate: peer.IsCandidate,
	}
	return newPeer
}

func copySeeds(cs *ClusterState, copied *ClusterState) {
	seedPeers := make([]config.SeedInfo, 0, len(cs.SeedPeers))
	for _, seed := range cs.SeedPeers {
		copiedSeed := config.SeedInfo{
			Name:         seed.Name,
			HttpEndpoint: seed.HttpEndpoint,
		}
		seedPeers = append(seedPeers, copiedSeed)
	}
	copied.SeedPeers = seedPeers
}

type StateApplier interface {
	// called When leader is changed, newLeader will set to nil if no leader now.
	// Note that this will not be called if service is boot up and no leader is found since.
	// So must be initialized as no leader.
	OnLeaderChanged(newLeader *utils.Peer, isMeLeader bool, leaderRestoreFailures *utils.AllInstanceFailuresSnapshot)
	OnStateRestored(state *ClusterState)
	OnAddPeer(peer *utils.Peer)
	OnRemovePeer(name string)
	OnAddInstance(routes map[string]*config.Route, reason string)
	OnRemoveInstance(names []string, reason string)
	GetName() string
}

type ClusterService interface {
	// IsSingleton if return true, a http client will be created to proxy the admin request to remote leader.
	// if return false, http client will be assigned to nil
	IsSingleton() bool
	Init(config config.ClusterConfig, initRoute *config.RouteTable, localPeer *utils.Peer, peerClient *peer_client.PeerClient)
	// Start the cluster, including service discovery and leader election.
	// Should never be blocked
	Start() error
	IsLeader() bool
	GetLeader() (*utils.Peer, string)
	LocalNode() *utils.Peer
	State() *ClusterState
	Stats() map[string]string
	IsWorkerAlive(peerName string) (bool, error)
	HandleNewPeer(peer *utils.Peer) error
	HandleLeavePeer(names []string, reason string) error
	HandleNewInstance(req *InstanceJoinRequest) error
	HandleLeaveInstance(req *InstanceLeaveRequest, leaveReason string) error
	PersistInstanceFailures(failures map[string]*utils.InstanceFailureRecord) error
	RegisterStateApplier(applier StateApplier)
	GetApplierNames() []string
	// GetInstanceCleanHandler handler for leader to clean up the downed instances
	GetInstanceCleanHandler() utils.Subscriber
	// GetHealthStats gets healthy instance statistics, returns a map of role to healthy instance count
	GetHealthStats() (EtcdHealthStatsCache, error)
	UpdateHealthStats(cache EtcdHealthStatsCache) error
	Close()
}
