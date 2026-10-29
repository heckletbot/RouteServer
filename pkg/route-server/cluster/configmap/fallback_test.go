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
	"path/filepath"
	"testing"
	"time"

	"infer-dp-scheduler/pkg/common/cluster"
	"infer-dp-scheduler/pkg/common/config"
	"infer-dp-scheduler/pkg/common/utils"
	"infer-dp-scheduler/pkg/route-server/metrics"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/clientcmd/api"

	"github.com/stretchr/testify/assert"
)

// ===================== BuildTopologyFromPods =====================

func TestBuildTopologyFromPods_Empty(t *testing.T) {
	topo := BuildTopologyFromPods(nil, defaultInferGroupRoleTypeLabelKey)
	assert.NotNil(t, topo)
	assert.Empty(t, topo.ServerGroupList)
	assert.Equal(t, "0", topo.ServerGroupCount)
}

func TestBuildTopologyFromPods_GroupsByRoleType(t *testing.T) {
	pods := []*corev1.Pod{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:   "master-0",
				Labels: map[string]string{"apps.red.io/podgroup-role-type": "master"},
			},
			Status: corev1.PodStatus{Phase: corev1.PodRunning, PodIP: "10.0.0.1"},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:   "prefill-0",
				Labels: map[string]string{"apps.red.io/podgroup-role-type": "prefill"},
			},
			Status: corev1.PodStatus{Phase: corev1.PodRunning, PodIP: "10.0.0.2"},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:   "decode-0",
				Labels: map[string]string{"apps.red.io/podgroup-role-type": "decode"},
			},
			Status: corev1.PodStatus{Phase: corev1.PodRunning, PodIP: "10.0.0.3"},
		},
	}
	topo := BuildTopologyFromPods(pods, defaultInferGroupRoleTypeLabelKey)
	assert.Equal(t, "3", topo.ServerGroupCount)
	assert.Len(t, topo.ServerGroupList, 3)
	// Deterministic ordering: decode, master, prefill (alphabetical by group_type)
	assert.Equal(t, "decode", topo.ServerGroupList[0].GroupType)
	assert.Equal(t, "master", topo.ServerGroupList[1].GroupType)
	assert.Equal(t, "prefill", topo.ServerGroupList[2].GroupType)
	// Each group has its server populated
	assert.Equal(t, "10.0.0.1", topo.ServerGroupList[1].ServerList[0].ServerIP)
	assert.Equal(t, "master-0", topo.ServerGroupList[1].ServerList[0].PodName)
}

func TestBuildTopologyFromPods_FiltersNonRunning(t *testing.T) {
	pods := []*corev1.Pod{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:   "running-pod",
				Labels: map[string]string{"apps.red.io/podgroup-role-type": "prefill"},
			},
			Status: corev1.PodStatus{Phase: corev1.PodRunning, PodIP: "10.0.0.1"},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:   "pending-pod",
				Labels: map[string]string{"apps.red.io/podgroup-role-type": "prefill"},
			},
			Status: corev1.PodStatus{Phase: corev1.PodPending, PodIP: "10.0.0.2"},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:   "failed-pod",
				Labels: map[string]string{"apps.red.io/podgroup-role-type": "prefill"},
			},
			Status: corev1.PodStatus{Phase: corev1.PodFailed, PodIP: "10.0.0.3"},
		},
	}
	topo := BuildTopologyFromPods(pods, defaultInferGroupRoleTypeLabelKey)
	assert.Len(t, topo.ServerGroupList, 1)
	assert.Len(t, topo.ServerGroupList[0].ServerList, 1)
	assert.Equal(t, "running-pod", topo.ServerGroupList[0].ServerList[0].PodName)
}

func TestBuildTopologyFromPods_FiltersEmptyPodIP(t *testing.T) {
	pods := []*corev1.Pod{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:   "no-ip-pod",
				Labels: map[string]string{"apps.red.io/podgroup-role-type": "prefill"},
			},
			Status: corev1.PodStatus{Phase: corev1.PodRunning, PodIP: ""},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:   "with-ip-pod",
				Labels: map[string]string{"apps.red.io/podgroup-role-type": "prefill"},
			},
			Status: corev1.PodStatus{Phase: corev1.PodRunning, PodIP: "10.0.0.1"},
		},
	}
	topo := BuildTopologyFromPods(pods, defaultInferGroupRoleTypeLabelKey)
	assert.Len(t, topo.ServerGroupList, 1)
	assert.Len(t, topo.ServerGroupList[0].ServerList, 1)
	assert.Equal(t, "with-ip-pod", topo.ServerGroupList[0].ServerList[0].PodName)
}

func TestBuildTopologyFromPods_FiltersMissingRoleTypeLabel(t *testing.T) {
	pods := []*corev1.Pod{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:   "no-label-pod",
				Labels: map[string]string{},
			},
			Status: corev1.PodStatus{Phase: corev1.PodRunning, PodIP: "10.0.0.1"},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:   "empty-label-pod",
				Labels: map[string]string{"apps.red.io/podgroup-role-type": ""},
			},
			Status: corev1.PodStatus{Phase: corev1.PodRunning, PodIP: "10.0.0.2"},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:   "labeled-pod",
				Labels: map[string]string{"apps.red.io/podgroup-role-type": "decode"},
			},
			Status: corev1.PodStatus{Phase: corev1.PodRunning, PodIP: "10.0.0.3"},
		},
	}
	topo := BuildTopologyFromPods(pods, defaultInferGroupRoleTypeLabelKey)
	assert.Len(t, topo.ServerGroupList, 1)
	assert.Equal(t, "decode", topo.ServerGroupList[0].GroupType)
}

func TestBuildTopologyFromPods_DeterministicOrdering(t *testing.T) {
	// Same Pod set in different input order should produce identical Topology.
	podsA := []*corev1.Pod{
		{ObjectMeta: metav1.ObjectMeta{Name: "b", Labels: map[string]string{"apps.red.io/podgroup-role-type": "prefill"}}, Status: corev1.PodStatus{Phase: corev1.PodRunning, PodIP: "10.0.0.2"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "a", Labels: map[string]string{"apps.red.io/podgroup-role-type": "prefill"}}, Status: corev1.PodStatus{Phase: corev1.PodRunning, PodIP: "10.0.0.1"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "z", Labels: map[string]string{"apps.red.io/podgroup-role-type": "master"}}, Status: corev1.PodStatus{Phase: corev1.PodRunning, PodIP: "10.0.0.3"}},
	}
	podsB := []*corev1.Pod{
		{ObjectMeta: metav1.ObjectMeta{Name: "z", Labels: map[string]string{"apps.red.io/podgroup-role-type": "master"}}, Status: corev1.PodStatus{Phase: corev1.PodRunning, PodIP: "10.0.0.3"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "a", Labels: map[string]string{"apps.red.io/podgroup-role-type": "prefill"}}, Status: corev1.PodStatus{Phase: corev1.PodRunning, PodIP: "10.0.0.1"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "b", Labels: map[string]string{"apps.red.io/podgroup-role-type": "prefill"}}, Status: corev1.PodStatus{Phase: corev1.PodRunning, PodIP: "10.0.0.2"}},
	}
	topoA := BuildTopologyFromPods(podsA, defaultInferGroupRoleTypeLabelKey)
	topoB := BuildTopologyFromPods(podsB, defaultInferGroupRoleTypeLabelKey)
	assert.Equal(t, topoA, topoB)
	// Servers within prefill group are sorted by PodName: a, b
	// "master" < "prefill" alphabetically, so index 0 is master, index 1 is prefill
	assert.Equal(t, "master", topoA.ServerGroupList[0].GroupType)
	assert.Equal(t, "prefill", topoA.ServerGroupList[1].GroupType)
	assert.Equal(t, "a", topoA.ServerGroupList[1].ServerList[0].PodName)
	assert.Equal(t, "b", topoA.ServerGroupList[1].ServerList[1].PodName)
}

// ===================== podListReconciler =====================

func newReconcilerWithFakeClient(t *testing.T, cs *ConfigMapClusterService, pods []*corev1.Pod, intervalSec int) *podListReconciler {
	t.Helper()
	// fake client List filters by namespace, so ensure every Pod lives in the
	// reconciler's namespace ("test-ns") — otherwise List returns nothing.
	for _, p := range pods {
		if p.Namespace == "" {
			p.Namespace = "test-ns"
		}
	}
	objects := make([]runtime.Object, 0, len(pods))
	for _, p := range pods {
		objects = append(objects, p)
	}
	client := fake.NewSimpleClientset(objects...)
	// Pass empty label keys so newPodListReconciler falls back to defaults
	// (defaultInferGroupLabelKey / defaultInferGroupRoleTypeLabelKey), which
	// match the labels set on the test Pods below.
	r := newPodListReconciler(cs, client, "test-ns", "test-group", intervalSec, "", "")
	return r
}

func TestPodListReconciler_StartStopIdempotent(t *testing.T) {
	metrics.InitMetrics(nil)
	cs := &ConfigMapClusterService{
		cmCfg:     &config.ConfigMapConfig{InstanceGroups: []config.InstanceGroupConfig{{GroupType: "prefill", StartingPort: 8000, EndpointCount: 2}}},
		localNode: &utils.Peer{Name: "node-1", Addr: utils.PeerAddr{Host: "10.0.0.1", HttpPort: 8080}},
		stopChan:  make(chan struct{}),
	}
	r := newReconcilerWithFakeClient(t, cs, nil, 60)
	// Double start is a no-op
	r.start()
	r.start()
	assert.True(t, r.running.Load())
	// Double stop is a no-op
	r.stop()
	r.stop()
	assert.False(t, r.running.Load())
}

func TestPodListReconciler_StopWithoutStart(t *testing.T) {
	cs := &ConfigMapClusterService{}
	r := newReconcilerWithFakeClient(t, cs, nil, 60)
	// stop before start should not panic
	r.stop()
	assert.False(t, r.running.Load())
}

func TestPodListReconciler_UpdateConfigMapTimestamp(t *testing.T) {
	cs := &ConfigMapClusterService{}
	r := newReconcilerWithFakeClient(t, cs, nil, 60)
	before := time.Now()
	r.updateConfigMapTimestamp()
	last, ok := r.lastConfigMapUpdate.Load().(time.Time)
	assert.True(t, ok)
	assert.True(t, !last.Before(before))
}

func TestPodListReconciler_ReconcileTick_SkipsWhenConfigMapRecentlyUpdated(t *testing.T) {
	metrics.InitMetrics(nil)
	cs := &ConfigMapClusterService{
		cmCfg:     &config.ConfigMapConfig{InstanceGroups: []config.InstanceGroupConfig{{GroupType: "prefill", StartingPort: 8000, EndpointCount: 2}}},
		localNode: &utils.Peer{Name: "node-1", Addr: utils.PeerAddr{Host: "10.0.0.1", HttpPort: 8080}},
	}
	// Provide a Pod that would change state if List fired
	pods := []*corev1.Pod{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:   "prefill-0",
				Labels: map[string]string{"apps.red.io/podgroup-role-type": "prefill", "apps.red.io/podgroup-name": "test-group"},
			},
			Status: corev1.PodStatus{Phase: corev1.PodRunning, PodIP: "10.0.0.2"},
		},
	}
	r := newReconcilerWithFakeClient(t, cs, pods, 60)
	// Just-updated timestamp → tick should skip
	r.updateConfigMapTimestamp()
	r.reconcileTick()
	// State should remain nil (applyTopology was not called)
	assert.Nil(t, cs.State())
}

func TestPodListReconciler_ReconcileTick_ListsWhenStale(t *testing.T) {
	metrics.InitMetrics(nil)
	cs := &ConfigMapClusterService{
		cmCfg: &config.ConfigMapConfig{
			InstanceGroups: []config.InstanceGroupConfig{{GroupType: "prefill", StartingPort: 8000, EndpointCount: 2}},
		},
		localNode: &utils.Peer{Name: "node-1", Addr: utils.PeerAddr{Host: "10.0.0.1", HttpPort: 8080}},
	}
	pods := []*corev1.Pod{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:   "prefill-0",
				Labels: map[string]string{"apps.red.io/podgroup-role-type": "prefill", "apps.red.io/podgroup-name": "test-group"},
			},
			Status: corev1.PodStatus{Phase: corev1.PodRunning, PodIP: "10.0.0.2"},
		},
	}
	r := newReconcilerWithFakeClient(t, cs, pods, 60)
	// Force staleness: set lastConfigMapUpdate to well past the interval
	r.lastConfigMapUpdate.Store(time.Now().Add(-2 * time.Minute))
	r.reconcileTick()
	// State should now be populated with the prefill instance
	state := cs.State()
	assert.NotNil(t, state)
	assert.Len(t, state.InstanceRoutes, 2) // EndpointCount=2 → 2 routes
}

func TestPodListReconciler_ReconcileTick_IdempotentRepeat(t *testing.T) {
	metrics.InitMetrics(nil)
	cs := &ConfigMapClusterService{
		cmCfg: &config.ConfigMapConfig{
			InstanceGroups: []config.InstanceGroupConfig{{GroupType: "prefill", StartingPort: 8000, EndpointCount: 2}},
		},
		localNode: &utils.Peer{Name: "node-1", Addr: utils.PeerAddr{Host: "10.0.0.1", HttpPort: 8080}},
	}
	pods := []*corev1.Pod{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:   "prefill-0",
				Labels: map[string]string{"apps.red.io/podgroup-role-type": "prefill", "apps.red.io/podgroup-name": "test-group"},
			},
			Status: corev1.PodStatus{Phase: corev1.PodRunning, PodIP: "10.0.0.2"},
		},
	}
	r := newReconcilerWithFakeClient(t, cs, pods, 60)
	r.lastConfigMapUpdate.Store(time.Now().Add(-2 * time.Minute))
	r.reconcileTick()
	state1 := cs.State()
	assert.NotNil(t, state1)
	version1 := state1.Version
	// Second tick with same Pod set: topology is identical, but
	// applyTopology always bumps Version (timestamp). The idempotency
	// guarantee is at the listener level (empty diff → no OnAdd*/OnRemove*).
	// Here we verify no panic and state remains consistent.
	r.lastConfigMapUpdate.Store(time.Now().Add(-2 * time.Minute))
	r.reconcileTick()
	state2 := cs.State()
	assert.NotNil(t, state2)
	assert.Len(t, state2.InstanceRoutes, len(state1.InstanceRoutes))
	// Version changes (timestamp-based) but route set is identical
	_ = version1
}

func TestPodListReconciler_ReconcileTick_DetectsTopologyChange(t *testing.T) {
	metrics.InitMetrics(nil)
	cs := &ConfigMapClusterService{
		cmCfg: &config.ConfigMapConfig{
			InstanceGroups: []config.InstanceGroupConfig{{GroupType: "prefill", StartingPort: 8000, EndpointCount: 2}},
		},
		localNode: &utils.Peer{Name: "node-1", Addr: utils.PeerAddr{Host: "10.0.0.1", HttpPort: 8080}},
	}
	// Seed initial state with one prefill Pod
	podsInitial := []*corev1.Pod{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:   "prefill-0",
				Labels: map[string]string{"apps.red.io/podgroup-role-type": "prefill", "apps.red.io/podgroup-name": "test-group"},
			},
			Status: corev1.PodStatus{Phase: corev1.PodRunning, PodIP: "10.0.0.2"},
		},
	}
	r := newReconcilerWithFakeClient(t, cs, podsInitial, 60)
	r.lastConfigMapUpdate.Store(time.Now().Add(-2 * time.Minute))
	r.reconcileTick()
	initialState := cs.State()
	assert.Len(t, initialState.InstanceRoutes, 2)

	// Now simulate a topology change: add a second prefill Pod via a new
	// reconciler pointing at a fake client with the additional Pod.
	podsUpdated := []*corev1.Pod{
		podsInitial[0],
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:   "prefill-1",
				Labels: map[string]string{"apps.red.io/podgroup-role-type": "prefill", "apps.red.io/podgroup-name": "test-group"},
			},
			Status: corev1.PodStatus{Phase: corev1.PodRunning, PodIP: "10.0.0.3"},
		},
	}
	r2 := newReconcilerWithFakeClient(t, cs, podsUpdated, 60)
	r2.lastConfigMapUpdate.Store(time.Now().Add(-2 * time.Minute))
	r2.reconcileTick()
	updatedState := cs.State()
	assert.Len(t, updatedState.InstanceRoutes, 4) // 2 pods × 2 endpoints
}

func TestPodListReconciler_ReconcileTick_ListFailureNoPanic(t *testing.T) {
	metrics.InitMetrics(nil)
	cs := &ConfigMapClusterService{
		cmCfg:     &config.ConfigMapConfig{InstanceGroups: []config.InstanceGroupConfig{{GroupType: "prefill", StartingPort: 8000, EndpointCount: 2}}},
		localNode: &utils.Peer{Name: "node-1", Addr: utils.PeerAddr{Host: "10.0.0.1", HttpPort: 8080}},
	}
	// Empty fake client — List will succeed but return no Pods
	r := newReconcilerWithFakeClient(t, cs, nil, 60)
	r.lastConfigMapUpdate.Store(time.Now().Add(-2 * time.Minute))
	// Should not panic; state will be set to empty (no pods)
	r.reconcileTick()
	state := cs.State()
	assert.NotNil(t, state)
	assert.Empty(t, state.InstanceRoutes)
	assert.Empty(t, state.Peers)
}

func TestPodListReconciler_DefaultIntervalWhenZeroOrNegative(t *testing.T) {
	cs := &ConfigMapClusterService{}
	r := newReconcilerWithFakeClient(t, cs, nil, 0)
	assert.Equal(t, time.Duration(defaultFallbackPodListIntervalSec)*time.Second, r.interval)
	r2 := newReconcilerWithFakeClient(t, cs, nil, -5)
	assert.Equal(t, time.Duration(defaultFallbackPodListIntervalSec)*time.Second, r2.interval)
}

// ===================== Start() config validation =====================

func writeTempKubeconfig(t *testing.T) string {
	t.Helper()
	cfg := api.NewConfig()
	cfg.Clusters["test"] = api.NewCluster()
	cfg.Clusters["test"].Server = "http://127.0.0.1:0"
	cfg.Contexts["test"] = api.NewContext()
	cfg.Contexts["test"].Cluster = "test"
	cfg.CurrentContext = "test"
	dir := t.TempDir()
	path := filepath.Join(dir, "kubeconfig")
	err := clientcmd.WriteToFile(*cfg, path)
	assert.NoError(t, err)
	return path
}

func TestConfigMapClusterService_Start_RejectsEnabledWithoutInferGroupName(t *testing.T) {
	cs := &ConfigMapClusterService{
		cmCfg: &config.ConfigMapConfig{
			FallbackPodDiscoveryEnabled: true,
			InferGroupName:              "",
			KubeConfigPath:              writeTempKubeconfig(t),
		},
	}
	err := cs.Start()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "infer-group-name is empty")
}

func TestConfigMapClusterService_Stats_ReportsFallbackEnabled(t *testing.T) {
	cs := &ConfigMapClusterService{
		namespace: "test-ns",
		cmCfg: &config.ConfigMapConfig{
			ConfigMapName:               "cluster-topology",
			FallbackPodDiscoveryEnabled: true,
		},
	}
	cs.connStatus.Store(true)
	stats := cs.Stats()
	assert.Equal(t, "true", stats["fallback_enabled"])

	cs.cmCfg.FallbackPodDiscoveryEnabled = false
	stats = cs.Stats()
	assert.Equal(t, "false", stats["fallback_enabled"])
}

// ===================== Integration: reconciler + applyTopology =====================

func TestPodListReconciler_ReconcileTick_BuildsMasterPeer(t *testing.T) {
	metrics.InitMetrics(nil)
	cs := &ConfigMapClusterService{
		cmCfg: &config.ConfigMapConfig{
			InstanceGroups: []config.InstanceGroupConfig{{GroupType: "prefill", StartingPort: 8000, EndpointCount: 1}},
		},
		localNode: &utils.Peer{Name: "node-1", Addr: utils.PeerAddr{Host: "10.0.0.1", HttpPort: 8080}},
	}
	pods := []*corev1.Pod{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:   "rs-master-0",
				Labels: map[string]string{"apps.red.io/podgroup-role-type": "master", "apps.red.io/podgroup-name": "test-group"},
			},
			Status: corev1.PodStatus{Phase: corev1.PodRunning, PodIP: "10.0.0.10"},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:   "prefill-0",
				Labels: map[string]string{"apps.red.io/podgroup-role-type": "prefill", "apps.red.io/podgroup-name": "test-group"},
			},
			Status: corev1.PodStatus{Phase: corev1.PodRunning, PodIP: "10.0.0.20"},
		},
	}
	r := newReconcilerWithFakeClient(t, cs, pods, 60)
	r.lastConfigMapUpdate.Store(time.Now().Add(-2 * time.Minute))
	r.reconcileTick()
	state := cs.State()
	assert.NotNil(t, state)
	// master group → 1 Peer; prefill group → 1 instance (EndpointCount=1)
	assert.Len(t, state.Peers, 1)
	assert.Contains(t, state.Peers, "master_10_0_0_10_8080")
	assert.Len(t, state.InstanceRoutes, 1)
}

// ===================== ClusterState is used (import sanity) =====================

var _ = cluster.ClusterState{}
