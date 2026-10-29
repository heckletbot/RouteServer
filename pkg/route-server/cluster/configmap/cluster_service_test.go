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

package configmap

import (
	"testing"
	"time"

	"infer-dp-scheduler/pkg/common/cluster"
	"infer-dp-scheduler/pkg/common/config"
	"infer-dp-scheduler/pkg/common/constant"
	"infer-dp-scheduler/pkg/common/utils"
	"infer-dp-scheduler/pkg/route-server/metrics"

	"github.com/stretchr/testify/assert"
)

// ===================== IsSingleton =====================

func TestConfigMapClusterService_IsSingleton(t *testing.T) {
	cs := &ConfigMapClusterService{}
	assert.False(t, cs.IsSingleton())
}

// ===================== Unsupported Operations =====================
// Topology-driven mode: HTTP join/leave APIs are unsupported (design §4, §8.3).

func TestConfigMapClusterService_HandleNewPeer(t *testing.T) {
	cs := &ConfigMapClusterService{}
	err := cs.HandleNewPeer(&utils.Peer{Name: "peer-1"})
	assert.Equal(t, cluster.ErrPeerManagementNotSupported, err)
}

func TestConfigMapClusterService_HandleLeavePeer(t *testing.T) {
	cs := &ConfigMapClusterService{}
	err := cs.HandleLeavePeer([]string{"peer-1"}, "shutdown")
	assert.Equal(t, cluster.ErrPeerManagementNotSupported, err)
}

func TestConfigMapClusterService_HandleNewInstance(t *testing.T) {
	cs := &ConfigMapClusterService{}
	err := cs.HandleNewInstance(&cluster.InstanceJoinRequest{
		Instances: map[string]*config.Route{"inst-1": {Ip: "10.0.0.1", Port: 8080}},
	})
	assert.Equal(t, cluster.ErrPeerManagementNotSupported, err)
}

func TestConfigMapClusterService_HandleLeaveInstance(t *testing.T) {
	cs := &ConfigMapClusterService{}
	err := cs.HandleLeaveInstance(&cluster.InstanceLeaveRequest{InstanceNames: []string{"inst-1"}}, "failed")
	assert.Equal(t, cluster.ErrPeerManagementNotSupported, err)
}

// ===================== Init =====================

func TestConfigMapClusterService_Init_WithNamespace(t *testing.T) {
	cs := &ConfigMapClusterService{}
	cfg := config.ClusterConfig{
		ConfigMapCfg: &config.ConfigMapConfig{
			Namespace:     "my-namespace",
			ConfigMapName: "cluster-topology",
			TopologyKey:   "rank_table.json",
			FailuresKey:   "failures.json",
		},
	}
	metrics.InitMetrics(nil)
	cs.Init(cfg, nil, &utils.Peer{Name: "node-1"}, nil)
	assert.Equal(t, "my-namespace", cs.namespace)
	assert.NotNil(t, cs.cmCfg)
	assert.NotNil(t, cs.stopChan)
	assert.NotNil(t, cs.resignChan)
	assert.NotNil(t, cs.informerStopChan)
}

func TestConfigMapClusterService_Init_NamespaceFromEnv(t *testing.T) {
	cs := &ConfigMapClusterService{}
	cfg := config.ClusterConfig{
		ConfigMapCfg: &config.ConfigMapConfig{}, // no Namespace
	}
	t.Setenv("POD_NAMESPACE", "env-namespace")
	cs.Init(cfg, nil, &utils.Peer{Name: "node-1"}, nil)
	assert.Equal(t, "env-namespace", cs.namespace)
}

func TestConfigMapClusterService_Init_DefaultNamespace(t *testing.T) {
	cs := &ConfigMapClusterService{}
	cfg := config.ClusterConfig{
		ConfigMapCfg: &config.ConfigMapConfig{},
	}
	cs.Init(cfg, nil, &utils.Peer{Name: "node-1"}, nil)
	assert.Equal(t, "default", cs.namespace)
}

func TestConfigMapClusterService_Init_LeaderIdInitialized(t *testing.T) {
	cs := &ConfigMapClusterService{}
	cfg := config.ClusterConfig{ConfigMapCfg: &config.ConfigMapConfig{}}
	cs.Init(cfg, nil, &utils.Peer{Name: "node-1"}, nil)
	assert.Equal(t, "", cs.leaderId.Load())
}

func TestConfigMapClusterService_Init_VerboseRegistersApplier(t *testing.T) {
	cs := &ConfigMapClusterService{}
	cfg := config.ClusterConfig{
		Verbose:      true,
		ConfigMapCfg: &config.ConfigMapConfig{},
	}
	cs.Init(cfg, nil, &utils.Peer{Name: "node-1"}, nil)
	assert.Len(t, cs.stateChangeListeners, 1)
}

func TestConfigMapClusterService_Init_ConnStatusInitiallyConnected(t *testing.T) {
	cs := &ConfigMapClusterService{}
	cfg := config.ClusterConfig{ConfigMapCfg: &config.ConfigMapConfig{}}
	cs.Init(cfg, nil, &utils.Peer{Name: "node-1"}, nil)
	assert.True(t, cs.connStatus.Load())
}

// ===================== IsLeader =====================

func TestConfigMapClusterService_IsLeader_Default(t *testing.T) {
	cs := &ConfigMapClusterService{}
	assert.False(t, cs.IsLeader())
}

func TestConfigMapClusterService_IsLeader_AfterSet(t *testing.T) {
	cs := &ConfigMapClusterService{}
	cs.isLeader.Store(true)
	assert.True(t, cs.IsLeader())
}

// ===================== GetLeader =====================

func TestConfigMapClusterService_GetLeader_NoLeader(t *testing.T) {
	cs := &ConfigMapClusterService{}
	peer, name := cs.GetLeader()
	assert.Nil(t, peer)
	assert.Equal(t, "", name)
}

func TestConfigMapClusterService_GetLeader_ResolvedFromIdentity(t *testing.T) {
	cs := &ConfigMapClusterService{}
	cs.leaderId.Store("candidate_10_0_1_1_8080")
	peer, name := cs.GetLeader()
	assert.NotNil(t, peer)
	assert.Equal(t, "candidate_10_0_1_1_8080", name)
	assert.Equal(t, "10.0.1.1", peer.Addr.Host)
	assert.Equal(t, 8080, peer.Addr.HttpPort)
}

func TestConfigMapClusterService_GetLeader_ResolvedFromTopology(t *testing.T) {
	cs := &ConfigMapClusterService{}
	peerName := "candidate_10_0_1_1_8080"
	cs.leaderId.Store(peerName)
	leaderPeer := &utils.Peer{
		Name: peerName,
		Addr: utils.PeerAddr{Host: "10.0.1.1", HttpPort: 8080},
	}
	cs.atomicState.Store(&cluster.ClusterState{
		Peers: map[string]*utils.Peer{peerName: leaderPeer},
	})
	peer, name := cs.GetLeader()
	assert.NotNil(t, peer)
	assert.Equal(t, peerName, name)
	assert.Equal(t, peerName, peer.Name)
}

func TestConfigMapClusterService_GetLeader_LegacyIdentity(t *testing.T) {
	cs := &ConfigMapClusterService{}
	cs.leaderId.Store("10.0.1.1:8080")
	peer, name := cs.GetLeader()
	assert.NotNil(t, peer)
	assert.Equal(t, "10.0.1.1:8080", name)
	assert.Equal(t, "10.0.1.1", peer.Addr.Host)
	assert.Equal(t, 8080, peer.Addr.HttpPort)
}

func TestConfigMapClusterService_GetLeader_InvalidIdentity(t *testing.T) {
	cs := &ConfigMapClusterService{}
	cs.leaderId.Store("not-a-valid-identity")
	peer, name := cs.GetLeader()
	assert.Nil(t, peer)
	assert.Equal(t, "not-a-valid-identity", name)
}

// ===================== State =====================

func TestConfigMapClusterService_State_Nil(t *testing.T) {
	cs := &ConfigMapClusterService{}
	assert.Nil(t, cs.State())
}

func TestConfigMapClusterService_State_Set(t *testing.T) {
	cs := &ConfigMapClusterService{}
	state := &cluster.ClusterState{
		Initialized:    true,
		Peers:          map[string]*utils.Peer{"p1": {Name: "p1"}},
		InstanceRoutes: map[string]*config.Route{"i1": {Ip: "10.0.0.1"}},
	}
	cs.atomicState.Store(state)
	result := cs.State()
	assert.NotNil(t, result)
	assert.True(t, result.Initialized)
	assert.Len(t, result.Peers, 1)
	assert.Len(t, result.InstanceRoutes, 1)
}

func TestConfigMapClusterService_State_WrongType(t *testing.T) {
	cs := &ConfigMapClusterService{}
	cs.atomicState.Store("not-a-state")
	assert.Nil(t, cs.State())
}

// ===================== Stats =====================

func TestConfigMapClusterService_Stats(t *testing.T) {
	cs := &ConfigMapClusterService{
		namespace: "test-ns",
		cmCfg:     &config.ConfigMapConfig{ConfigMapName: "cluster-topology"},
	}
	// Init marks connStatus=true; a freshly-constructed struct (without Init)
	// reports disconnected because the atomic zero value is false.
	cs.connStatus.Store(true)
	stats := cs.Stats()
	assert.Equal(t, "k8s-configmap", stats["cluster_mode"])
	assert.Equal(t, "test-ns", stats["namespace"])
	assert.Equal(t, "false", stats["is_leader"])
	assert.Equal(t, "cluster-topology", stats["configmap_name"])
	assert.Equal(t, "connected", stats["api_server_status"])
}

func TestConfigMapClusterService_Stats_AsLeader(t *testing.T) {
	cs := &ConfigMapClusterService{
		namespace: "leader-ns",
		cmCfg:     &config.ConfigMapConfig{ConfigMapName: "cluster-topology"},
	}
	cs.isLeader.Store(true)
	stats := cs.Stats()
	assert.Equal(t, "true", stats["is_leader"])
}

func TestConfigMapClusterService_Stats_Disconnected(t *testing.T) {
	cs := &ConfigMapClusterService{
		namespace: "test-ns",
	}
	cs.connStatus.Store(false)
	stats := cs.Stats()
	assert.Equal(t, "disconnected", stats["api_server_status"])
}

func TestConfigMapClusterService_Stats_LastLeaseRenew(t *testing.T) {
	cs := &ConfigMapClusterService{namespace: "test-ns"}
	now := time.Now().UTC()
	cs.lastLeaseRenewTime.Store(now)
	stats := cs.Stats()
	assert.NotEmpty(t, stats["last_lease_renew"])
}

// ===================== LocalNode =====================

func TestConfigMapClusterService_LocalNode(t *testing.T) {
	localPeer := &utils.Peer{Name: "original", Addr: utils.PeerAddr{Host: "10.0.0.1", HttpPort: 9800}, IsCandidate: true}
	cs := &ConfigMapClusterService{}
	cs.Init(config.ClusterConfig{ConfigMapCfg: &config.ConfigMapConfig{}}, nil, localPeer, nil)
	assert.Equal(t, "candidate_10_0_0_1_9800", cs.LocalNode().Name)
}

// ===================== IsWorkerAlive =====================

func TestConfigMapClusterService_IsWorkerAlive_NotInState(t *testing.T) {
	cs := &ConfigMapClusterService{}
	alive, err := cs.IsWorkerAlive("peer-1")
	assert.Error(t, err)
	assert.False(t, alive)
}

func TestConfigMapClusterService_IsWorkerAlive_Present(t *testing.T) {
	cs := &ConfigMapClusterService{}
	cs.atomicState.Store(&cluster.ClusterState{
		Peers: map[string]*utils.Peer{"peer-1": {Name: "peer-1"}},
	})
	alive, err := cs.IsWorkerAlive("peer-1")
	assert.NoError(t, err)
	assert.True(t, alive)
}

func TestConfigMapClusterService_IsWorkerAlive_Absent(t *testing.T) {
	cs := &ConfigMapClusterService{}
	cs.atomicState.Store(&cluster.ClusterState{
		Peers: map[string]*utils.Peer{"peer-1": {Name: "peer-1"}},
	})
	alive, err := cs.IsWorkerAlive("peer-2")
	assert.NoError(t, err)
	assert.False(t, alive)
}

// ===================== RegisterStateApplier / GetApplierNames =====================

type fakeApplier struct{ name string }

func (f *fakeApplier) OnLeaderChanged(_ *utils.Peer, _ bool, _ *utils.AllInstanceFailuresSnapshot) {}
func (f *fakeApplier) OnStateRestored(_ *cluster.ClusterState)                                     {}
func (f *fakeApplier) OnAddPeer(_ *utils.Peer)                                                     {}
func (f *fakeApplier) OnRemovePeer(_ string)                                                       {}
func (f *fakeApplier) OnAddInstance(_ map[string]*config.Route, _ string)                          {}
func (f *fakeApplier) OnRemoveInstance(_ []string, _ string)                                       {}
func (f *fakeApplier) GetName() string                                                             { return f.name }

func TestConfigMapClusterService_RegisterStateApplier(t *testing.T) {
	cs := &ConfigMapClusterService{stateChangeListeners: []cluster.StateApplier{}}
	cs.RegisterStateApplier(&fakeApplier{name: "applier-1"})
	cs.RegisterStateApplier(&fakeApplier{name: "applier-2"})
	names := cs.GetApplierNames()
	assert.Len(t, names, 2)
	assert.Contains(t, names, "applier-1")
	assert.Contains(t, names, "applier-2")
}

func TestConfigMapClusterService_RegisterStateApplier_Nil(t *testing.T) {
	cs := &ConfigMapClusterService{stateChangeListeners: []cluster.StateApplier{}}
	cs.RegisterStateApplier(nil)
	assert.Empty(t, cs.stateChangeListeners)
}

// ===================== resolveLeaderPeer =====================

func TestConfigMapClusterService_ResolveLeaderPeer_FromTopology(t *testing.T) {
	cs := &ConfigMapClusterService{}
	peerName := "candidate_10_0_1_1_8080"
	leaderPeer := &utils.Peer{
		Name: peerName,
		Addr: utils.PeerAddr{Host: "10.0.1.1", HttpPort: 8080},
	}
	cs.atomicState.Store(&cluster.ClusterState{
		Peers: map[string]*utils.Peer{peerName: leaderPeer},
	})
	peer := cs.resolveLeaderPeer("candidate_10_0_1_1_8080")
	assert.NotNil(t, peer)
	assert.Equal(t, peerName, peer.Name)
}

func TestConfigMapClusterService_ResolveLeaderPeer_SynthesizedWhenNotInTopology(t *testing.T) {
	cs := &ConfigMapClusterService{}
	cs.atomicState.Store(&cluster.ClusterState{
		Peers: map[string]*utils.Peer{"other": {Name: "other"}},
	})
	peer := cs.resolveLeaderPeer("10.0.2.2:9090")
	assert.NotNil(t, peer)
	// Synthesized peer uses host as Name (design §6.3).
	assert.Equal(t, "10.0.2.2", peer.Name)
	assert.Equal(t, "10.0.2.2", peer.Addr.Host)
	assert.Equal(t, 9090, peer.Addr.HttpPort)
}

func TestConfigMapClusterService_ResolveLeaderPeer_InvalidIdentity(t *testing.T) {
	cs := &ConfigMapClusterService{}
	peer := cs.resolveLeaderPeer("invalid")
	assert.Nil(t, peer)
}

// ===================== PersistInstanceFailures (non-leader guard) =====================

func TestConfigMapClusterService_PersistInstanceFailures_NonLeader(t *testing.T) {
	cs := &ConfigMapClusterService{
		cmCfg: &config.ConfigMapConfig{FailuresKey: "failures.json"},
	}
	err := cs.PersistInstanceFailures(map[string]*utils.InstanceFailureRecord{
		"x": {Role: "prefill", Status: constant.FaultStatus},
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "only leader")
}

func TestConfigMapClusterService_PersistInstanceFailures_DisabledWhenKeyEmpty(t *testing.T) {
	cs := &ConfigMapClusterService{
		cmCfg: &config.ConfigMapConfig{FailuresKey: ""},
	}
	cs.isLeader.Store(true)
	// No failures key configured -> persistence disabled, returns nil.
	err := cs.PersistInstanceFailures(nil)
	assert.NoError(t, err)
}

func TestConfigMapClusterService_PersistInstanceFailures_NoOwnerReference(t *testing.T) {
	cs := &ConfigMapClusterService{
		cmCfg: &config.ConfigMapConfig{FailuresKey: "failures.json"},
	}
	cs.isLeader.Store(true)
	// FailuresKey configured but no owner-reference -> cannot persist.
	err := cs.PersistInstanceFailures(map[string]*utils.InstanceFailureRecord{
		"x": {Role: "prefill", Status: constant.FaultStatus},
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "owner-reference not configured")
}

// ===================== GetInstanceCleanHandler =====================

func TestConfigMapClusterService_GetInstanceCleanHandler(t *testing.T) {
	cs := &ConfigMapClusterService{}
	handler := cs.GetInstanceCleanHandler()
	assert.NotNil(t, handler)
	_, ok := handler.(*configmapInstanceLeaveHandler)
	assert.True(t, ok)
}

func TestConfigMapInstanceLeaveHandler_HandleMessage(t *testing.T) {
	cs := &ConfigMapClusterService{}
	cs.atomicState.Store(&cluster.ClusterState{
		InstanceRoutes: map[string]*config.Route{
			"prefill_10_0_0_1_8000": {Ip: "10.0.0.1", Port: 8000, Role: constant.Prefill},
			"decode_10_0_0_2_9000":  {Ip: "10.0.0.2", Port: 9000, Role: constant.Decode},
		},
	})
	handler := &configmapInstanceLeaveHandler{cs: cs}
	handler.HandleMessage(&cluster.InstanceLeaveRequest{
		InstanceNames: []string{"prefill_10_0_0_1_8000", "unknown-instance"},
	})
	// No panic, no return value — the handler only logs + records metrics.
}

func TestConfigMapInstanceLeaveHandler_HandleMessage_WrongType(t *testing.T) {
	cs := &ConfigMapClusterService{}
	handler := &configmapInstanceLeaveHandler{cs: cs}
	// Should not panic on wrong message type.
	handler.HandleMessage("not-a-leave-request")
}

// ===================== Close =====================

func TestConfigMapClusterService_Close_Idempotent(t *testing.T) {
	cs := &ConfigMapClusterService{
		stopChan:         make(chan struct{}),
		informerStopChan: make(chan struct{}),
	}
	cs.Close()
	// Second call should be a no-op (not panic on double close).
	cs.Close()
}

// ===================== GetHealthStats (fallback) =====================

func TestConfigMapClusterService_GetHealthStats_FallbackCache(t *testing.T) {
	cs := &ConfigMapClusterService{
		cmCfg: &config.ConfigMapConfig{FailuresKey: "failures.json"},
	}
	// No rs-status ConfigMap set up -> falls back to in-memory cache.
	cache := cluster.EtcdHealthStatsCache{
		EveryRoleAvailableCountMap: map[string]int{"prefill": 2},
	}
	cs.UpdateHealthStats(cache)
	got, err := cs.GetHealthStats()
	assert.NoError(t, err)
	assert.Equal(t, 2, got.EveryRoleAvailableCountMap["prefill"])
}

// ===================== UpdateHealthStats =====================

func TestConfigMapClusterService_UpdateHealthStats(t *testing.T) {
	cs := &ConfigMapClusterService{}
	cache := cluster.EtcdHealthStatsCache{
		EveryRoleAvailableCountMap: map[string]int{"decode": 3},
	}
	err := cs.UpdateHealthStats(cache)
	assert.NoError(t, err)
	cs.healthStatsLock.RLock()
	defer cs.healthStatsLock.RUnlock()
	assert.Equal(t, 3, cs.healthStatsCache.EveryRoleAvailableCountMap["decode"])
}
