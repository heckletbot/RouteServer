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

// Package configmap implements a simplified ClusterService backend that derives
// cluster topology from a K8s ConfigMap instead of CRDs + Pod Informer.
//
// Key differences from the CRD-based K8s mode:
//   - Identity = "IP:HttpPort" (directly parseable, no RouteServerPeer CR lookup)
//   - Topology read from a ConfigMap JSON key (rank_table.json)
//   - Failure snapshots persisted in a separate rs-status ConfigMap (requires owner-reference)
//   - No CRD registration, no Pod Informer
//   - ConfigMap Informer lifecycle is decoupled from Leader Election
package configmap

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	peer_client "route-server/pkg/common/client/peer-client"
	"route-server/pkg/common/cluster"
	"route-server/pkg/common/config"
	"route-server/pkg/common/constant"
	"route-server/pkg/common/logger"
	"route-server/pkg/common/utils"
	cluster2 "route-server/pkg/route-server/cluster"
	rsmetrics "route-server/pkg/route-server/metrics"

	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
	"k8s.io/utils/ptr"
)

const (
	// nodeRole* constants mirror the k8s CRD mode values so metrics stay
	// comparable across backends.
	nodeRoleWorker    = 0
	nodeRoleLeader    = 1
	nodeRoleCandidate = 2

	// leaderElection* defaults match the design §7.4.2. ConfigMap mode defaults
	// are shorter than CRD mode (15s vs 30s) because recovery reads a single
	// ConfigMap key rather than listing CRD status objects.
	defaultLeaseDurationSec = 15
	defaultRenewDeadlineSec = 10
	defaultRetryPeriodSec   = 2

	// defaultMinFailurePersistIntervalSec throttles ConfigMap writes during
	// failure storms. Health-check interval is typically 10s and snapshot
	// interval 20s, so 3s is enough to cover normal operation.
	defaultMinFailurePersistIntervalSec = 3

	// defaultFallbackPodListIntervalSec is the default Pod List reconciliation
	// interval (design §6.6). A single parameter expresses both the tick
	// interval and the "no ConfigMap update" threshold: when the ConfigMap is
	// actively updating, 0 Lists fire; when it is stale, one List per interval.
	defaultFallbackPodListIntervalSec = 60

	informerResyncPeriod  = 30 * time.Second
	cacheSyncTimeout      = 30 * time.Second
	statusUpdateTimeout   = 10 * time.Second
	leaderWatchRetryDelay = 5 * time.Second
	resignBackoffDelay    = 10 * time.Second
	connCheckInterval     = 10 * time.Second

	defaultClientQPS   = 50.0
	defaultClientBurst = 100
)

// ConfigMapClusterService implements cluster.ClusterService by watching a
// ConfigMap for topology and persisting failure snapshots to a separate
// rs-status ConfigMap (requires owner-reference). Leader election reuses
// client-go's Lease-based elector.
type ConfigMapClusterService struct {
	cfg                  config.ClusterConfig
	cmCfg                *config.ConfigMapConfig
	initRoutes           *config.RouteTable
	localNode            *utils.Peer
	stateChangeListeners []cluster.StateApplier
	stopChan             chan struct{}
	peerClient           *peer_client.PeerClient

	namespace     string
	kubeCfg       *rest.Config
	kubeClient    kubernetes.Interface
	dynamicClient dynamic.Interface

	// statusConfigMapName is the name of the rs-status ConfigMap (RS-owned).
	// Set by ensureStatusConfigMap when owner-reference is configured.
	// Empty means failures cannot be persisted (owner-reference required).
	statusConfigMapName string

	// configMapInformer watches the topology ConfigMap. It is started
	// once at Start() and never stopped on leader change (design §7.4.7).
	configMapInformer cache.SharedIndexInformer
	// statusConfigMapInformer watches the rs-status ConfigMap (failures).
	// Started after ensureStatusConfigMap succeeds. Its cache is the primary
	// source for GetHealthStats; the in-memory healthStatsCache is kept fresh
	// by the Informer's UpdateFunc callback.
	statusConfigMapInformer cache.SharedIndexInformer
	informerStopChan        chan struct{}
	statusInformerStopChan  chan struct{}
	informerStarted         atomic.Bool

	leaderElector *leaderelection.LeaderElector
	resignChan    chan struct{}

	// atomicState holds *cluster.ClusterState built from the latest topology.
	atomicState atomic.Value
	// currentTopology holds *Topology parsed from the last ConfigMap update,
	// used for health-stats aggregation.
	currentTopology atomic.Value

	isLeader           atomic.Bool
	leaderId           atomic.Value
	connStatus         atomic.Bool
	lastLeaseRenewTime atomic.Value

	// healthStats* cache failure stats. Kept fresh by the statusConfigMapInformer
	// callback; GetHealthStats reads this cache directly (no API call).
	// healthStatsInitialized distinguishes "Informer not synced yet" (fall back to
	// API) from "synced but zero failures" (return empty cache, no API call).
	healthStatsCache       cluster.EtcdHealthStatsCache
	healthStatsLock        sync.RWMutex
	healthStatsInitialized bool

	// localRound increments on each PersistInstanceFailures call. It is written
	// into the snapshot so consumers can detect stale snapshots.
	localRound atomic.Int32

	// failure persist throttling (design §6.4). Guarded by failurePersistMu.
	failurePersistMu       sync.Mutex
	lastFailurePersistTime time.Time
	pendingFailureData     string

	// reconciler is the Pod List periodic reconciliation (design §6.6). Non-nil
	// only when FallbackPodDiscoveryEnabled is true. Started/stopped on leader
	// transitions; the ConfigMap Informer refreshes its staleness baseline.
	reconciler *podListReconciler
}

// IsSingleton returns false. ConfigMap mode is a distributed mode.
func (cs *ConfigMapClusterService) IsSingleton() bool {
	return false
}

// Init stores config and local peer. K8s clients and the Informer are created
// in Start() so Init remains cheap and side-effect free.
func (cs *ConfigMapClusterService) Init(cfg config.ClusterConfig, initRoute *config.RouteTable, localPeer *utils.Peer, peerClient *peer_client.PeerClient) {
	cs.cfg = cfg
	cs.localNode = localPeer
	cs.initRoutes = initRoute
	cs.peerClient = peerClient
	cs.stateChangeListeners = make([]cluster.StateApplier, 0)
	cs.stopChan = make(chan struct{})
	cs.resignChan = make(chan struct{}, 1)
	cs.informerStopChan = make(chan struct{})

	// ConfigMapConfig is loaded by InitClusterConfig in pkg/common/cluster/utils.go
	// and attached to cfg.ConfigMapCfg. Keep a local pointer for convenience.
	cs.cmCfg = cfg.ConfigMapCfg

	// Override localNode.Name to EndpointName format so that Leader Identity
	// (stored in Lease)
	// Format is <role>_<ip_underscored>_<port>, e.g. candidate_127_0_0_1_9800
	if cs.localNode != nil {
		role := constant.ClusterRoleWorker
		if cs.localNode.IsCandidate {
			role = constant.ClusterRoleCandidate
		}
		cs.localNode.Name = EndpointName(role, cs.localNode.Addr.Host, cs.localNode.Addr.HttpPort)
	}

	if cs.cmCfg != nil && cs.cmCfg.Namespace != "" {
		cs.namespace = cs.cmCfg.Namespace
	} else if ns := os.Getenv("POD_NAMESPACE"); ns != "" {
		cs.namespace = ns
	} else if data, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace"); err == nil {
		cs.namespace = strings.TrimSpace(string(data))
	}
	if cs.namespace == "" {
		cs.namespace = "default"
	}

	cs.leaderId.Store("")
	cs.lastLeaseRenewTime.Store(time.Time{})
	cs.setConnStatus(true)

	if cfg.Verbose {
		cs.RegisterStateApplier(cluster2.StateChangeReport{})
	}
}

// Start builds the K8s client, starts the ConfigMap Informer (decoupled from
// leader election per design §7.4.7), and kicks off leader election or lease
// watching depending on the local node's role.
func (cs *ConfigMapClusterService) Start() error {
	if cs.cmCfg == nil {
		return fmt.Errorf("configmap config is nil, check configmap-config-file loading")
	}
	var err error
	cs.kubeCfg, err = buildRestConfig(cs.cmCfg.KubeConfigPath)
	if err != nil {
		return fmt.Errorf("failed to build rest config: %v", err)
	}
	cs.kubeClient, err = kubernetes.NewForConfig(cs.kubeCfg)
	if err != nil {
		return fmt.Errorf("failed to create kubernetes client: %v", err)
	}
	cs.dynamicClient, err = dynamic.NewForConfig(cs.kubeCfg)
	if err != nil {
		return fmt.Errorf("failed to create dynamic client: %v", err)
	}

	// Build the Pod List reconciler up-front so its staleness baseline can be
	// refreshed by the Informer from the very first ConfigMap event. It is not
	// started here — only the Leader starts it (onStartedLeading).
	if cs.cmCfg.FallbackPodDiscoveryEnabled {
		if cs.cmCfg.InferGroupName == "" {
			return fmt.Errorf("fallback-pod-discovery-enabled is true but infer-group-name is empty")
		}
		if cs.cmCfg.InferGroupLabelKey == "" {
			return fmt.Errorf("fallback-pod-discovery-enabled is true but infer-group-label-key is empty")
		}
		if cs.cmCfg.InferGroupRoleTypeLabelKey == "" {
			return fmt.Errorf("fallback-pod-discovery-enabled is true but infer-group-role-type-label-key is empty")
		}
		cs.reconciler = newPodListReconciler(cs, cs.kubeClient, cs.namespace, cs.cmCfg.InferGroupName,
			cs.cmCfg.FallbackPodListIntervalSec, cs.cmCfg.InferGroupLabelKey, cs.cmCfg.InferGroupRoleTypeLabelKey)
	}

	if err := cs.startConfigMapInformer(); err != nil {
		return fmt.Errorf("failed to start configmap informer: %v", err)
	}

	cs.startStatusConfigMapInformer()

	go cs.startConnHealthCheck()

	if cs.localNode.IsCandidate {
		rsmetrics.SetCurrentNodeRole(nodeRoleCandidate)
		go cs.startLeaderCampaign()
	} else {
		rsmetrics.SetCurrentNodeRole(nodeRoleWorker)
		go cs.startLeaderWatch()
	}
	return nil
}

// startConfigMapInformer builds and starts the ConfigMap Informer. It runs for
// the whole pod lifetime (design §7.4.7) — it must NOT be stopped on leader
// step-down, otherwise disconnect-time topology changes would be missed.
func (cs *ConfigMapClusterService) startConfigMapInformer() error {
	if cs.informerStarted.Load() {
		return nil
	}
	factory := informers.NewSharedInformerFactoryWithOptions(
		cs.kubeClient,
		informerResyncPeriod,
		informers.WithNamespace(cs.namespace),
		informers.WithTweakListOptions(func(lo *metav1.ListOptions) {
			lo.FieldSelector = fmt.Sprintf("metadata.name=%s", cs.cmCfg.ConfigMapName)
		}),
	)
	cs.configMapInformer = factory.Core().V1().ConfigMaps().Informer()
	cs.configMapInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    cs.onConfigMapAdd,
		UpdateFunc: cs.onConfigMapUpdate,
		DeleteFunc: cs.onConfigMapDelete,
	})

	go cs.configMapInformer.Run(cs.informerStopChan)

	syncCtx, syncCancel := context.WithTimeout(context.Background(), cacheSyncTimeout)
	defer syncCancel()
	if !cache.WaitForCacheSync(syncCtx.Done(), cs.configMapInformer.HasSynced) {
		logger.Log().Warnf("configmap informer cache sync timeout, will continue with retry/resync")
		cs.setConnStatus(false)
	} else {
		logger.Log().Infof("configmap informer started and synced, namespace=%s, configmap=%s",
			cs.namespace, cs.cmCfg.ConfigMapName)
	}
	cs.informerStarted.Store(true)
	return nil
}

// startStatusConfigMapInformer builds and starts an Informer for the rs-status
// ConfigMap. The CM name is derived from owner-reference config so both Leader
// and Workers can start it at Start() time without waiting for ensureStatusConfigMap.
// If owner-reference or FailuresKey is not configured, this is a no-op.
func (cs *ConfigMapClusterService) startStatusConfigMapInformer() {
	if cs.cmCfg.FailuresKey == "" || !cs.cmCfg.OwnerReference.IsConfigured() {
		// No status Informer — mark initialized so GetHealthStats returns empty
		// cache without retrying.
		cs.markHealthStatsInitializedIfNot()
		return
	}
	if cs.statusInformerStopChan != nil {
		return
	}

	statusCMName := cs.cmCfg.OwnerReference.Name + "-rs-status"
	cs.statusConfigMapName = statusCMName
	cs.statusInformerStopChan = make(chan struct{})

	factory := informers.NewSharedInformerFactoryWithOptions(
		cs.kubeClient,
		informerResyncPeriod,
		informers.WithNamespace(cs.namespace),
		informers.WithTweakListOptions(func(lo *metav1.ListOptions) {
			lo.FieldSelector = fmt.Sprintf("metadata.name=%s", statusCMName)
		}),
	)
	cs.statusConfigMapInformer = factory.Core().V1().ConfigMaps().Informer()
	cs.statusConfigMapInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    cs.onStatusConfigMapAdd,
		UpdateFunc: cs.onStatusConfigMapUpdate,
		DeleteFunc: cs.onStatusConfigMapDelete,
	})

	go cs.statusConfigMapInformer.Run(cs.statusInformerStopChan)

	syncCtx, syncCancel := context.WithTimeout(context.Background(), cacheSyncTimeout)
	defer syncCancel()
	if !cache.WaitForCacheSync(syncCtx.Done(), cs.statusConfigMapInformer.HasSynced) {
		logger.Log().Warnf("configmap status informer cache sync timeout, will continue with retry/resync")
	} else {
		logger.Log().Infof("configmap status informer started and synced, namespace=%s, configmap=%s",
			cs.namespace, statusCMName)
	}

	// If the rs-status CM doesn't exist yet (no onStatusConfigMapAdd fired during
	// initial List), mark initialized to prevent GetHealthStats from hammering
	// the API. The Informer will fire onStatusConfigMapAdd when the Leader
	// eventually creates the CM, refreshing the cache.
	cs.markHealthStatsInitializedIfNot()
}

// markHealthStatsInitializedIfNot sets healthStatsInitialized to true with an
// empty cache if it hasn't been initialized yet. Race-safe: does nothing if
// already initialized (e.g. onStatusConfigMapAdd already fired).
func (cs *ConfigMapClusterService) markHealthStatsInitializedIfNot() {
	cs.healthStatsLock.Lock()
	if !cs.healthStatsInitialized {
		cs.healthStatsCache = cluster.EtcdHealthStatsCache{
			EveryRoleAvailableCountMap: make(map[string]int),
			FailureInstanceDetail:      make(map[string]map[string]*utils.InstanceFailureRecord),
		}
		cs.healthStatsInitialized = true
	}
	cs.healthStatsLock.Unlock()
}

// onStatusConfigMapAdd handles the initial load of the rs-status ConfigMap.
func (cs *ConfigMapClusterService) onStatusConfigMapAdd(obj interface{}) {
	cm, ok := obj.(*corev1.ConfigMap)
	if !ok || cm == nil || cm.Data == nil {
		return
	}
	cs.refreshHealthStatsFromFailures(cm.Data[cs.cmCfg.FailuresKey], nil)
}

// onStatusConfigMapUpdate refreshes healthStatsCache when the failures key changes.
func (cs *ConfigMapClusterService) onStatusConfigMapUpdate(oldObj, newObj interface{}) {
	newCM, ok := newObj.(*corev1.ConfigMap)
	if !ok || newCM == nil || newCM.Data == nil {
		return
	}
	if oldObj != nil {
		if oldCM, ok := oldObj.(*corev1.ConfigMap); ok && oldCM.ResourceVersion == newCM.ResourceVersion {
			return
		}
	}
	cs.refreshHealthStatsFromFailures(newCM.Data[cs.cmCfg.FailuresKey], nil)
}

// onStatusConfigMapDelete clears the healthStatsCache when the rs-status ConfigMap is deleted.
func (cs *ConfigMapClusterService) onStatusConfigMapDelete(obj interface{}) {
	cs.healthStatsLock.Lock()
	cs.healthStatsCache = cluster.EtcdHealthStatsCache{
		EveryRoleAvailableCountMap: make(map[string]int),
		FailureInstanceDetail:      make(map[string]map[string]*utils.InstanceFailureRecord),
	}
	cs.healthStatsInitialized = false
	cs.healthStatsLock.Unlock()
}

// onConfigMapAdd handles the initial load of the ConfigMap. We parse topology,
// build the first ClusterState, and notify listeners via OnStateRestored.
func (cs *ConfigMapClusterService) onConfigMapAdd(obj interface{}) {
	cm, ok := obj.(*corev1.ConfigMap)
	if !ok {
		return
	}
	cs.applyConfigMap(cm, cluster.StateUpdateReasonRestore)
}

// onConfigMapUpdate handles topology/failures changes. We diff against the
// in-memory state and emit OnAdd*/OnRemove* events.
func (cs *ConfigMapClusterService) onConfigMapUpdate(oldObj, newObj interface{}) {
	newCM, ok := newObj.(*corev1.ConfigMap)
	if !ok {
		return
	}
	if oldObj != nil {
		if oldCM, ok := oldObj.(*corev1.ConfigMap); ok && oldCM.ResourceVersion == newCM.ResourceVersion {
			return
		}
	}
	cs.applyConfigMap(newCM, cluster.StateUpdateReasonInstanceChange)
}

func (cs *ConfigMapClusterService) onConfigMapDelete(obj interface{}) {
	logger.Log().Warnf("configmap %s/%s deleted, topology will be empty until recreated",
		cs.namespace, cs.cmCfg.ConfigMapName)
	// Clear peers/instances; keep leader as-is (Lease is authoritative).
	cs.applyTopology(&Topology{}, cluster.StateUpdateReasonInstanceChange)
}

// applyConfigMap parses the topology key from the ConfigMap and applies it.
// Topology changes trigger peer/instance diff + listener notify.
// Failures are stored in the separate rs-status ConfigMap (requires owner-reference)
// and are NOT read here — they are fetched on demand via GetHealthStats and
// restoreFailuresFromConfigMap.
func (cs *ConfigMapClusterService) applyConfigMap(cm *corev1.ConfigMap, reason string) {
	if cm == nil || cm.Data == nil {
		return
	}
	topologyData := cm.Data[cs.cmCfg.TopologyKey]
	topo, err := ParseTopology(topologyData)
	if err != nil {
		logger.Log().Warnf("configmap failed to parse topology key %s: %v", cs.cmCfg.TopologyKey, err)
		return
	}

	cs.applyTopology(topo, reason)

	if cs.reconciler != nil {
		cs.reconciler.updateConfigMapTimestamp()
	}
}

// applyTopology rebuilds peers/instances from topo, diffs against
// the current state, and notifies listeners.
func (cs *ConfigMapClusterService) applyTopology(topo *Topology, reason string) {
	if topo == nil {
		topo = &Topology{}
	}
	cs.currentTopology.Store(topo)

	localHttpPort := 0
	localOpsPort := 0
	if cs.localNode != nil {
		localHttpPort = cs.localNode.Addr.HttpPort
		localOpsPort = cs.localNode.Addr.OpsPort
	}
	newPeers, newInstances := BuildPeersAndInstances(topo, cs.cmCfg, localHttpPort, localOpsPort)

	oldPeers, oldInstances := cs.peekOldPeersAndInstances()
	addedPeers, removedPeers := DiffPeers(oldPeers, newPeers)
	addedInst, removedInst := DiffInstances(oldInstances, newInstances)

	cs.logDiffSummary(reason, addedPeers, removedPeers, addedInst, removedInst)

	newState := &cluster.ClusterState{
		Initialized:    true,
		Peers:          newPeers,
		InstanceRoutes: newInstances,
		Version:        time.Now().UnixMilli(),
	}
	cs.atomicState.Store(newState)

	cs.notifyStateChangeListeners(newState, reason, addedPeers, removedPeers, addedInst, removedInst)

	if cs.cfg.Verbose {
		logger.Log().Infof("configmap topology applied, reason=%s, peers=%d, instances=%d (added peers=%d, removed peers=%d, added inst=%d, removed inst=%d)",
			reason, len(newPeers), len(newInstances), len(addedPeers), len(removedPeers), len(addedInst), len(removedInst))
	}
}

func (cs *ConfigMapClusterService) peekOldPeersAndInstances() (map[string]*utils.Peer, map[string]*config.Route) {
	oldState := cs.State()
	var oldPeers map[string]*utils.Peer
	var oldInstances map[string]*config.Route
	if oldState != nil {
		oldPeers = oldState.Peers
		oldInstances = oldState.InstanceRoutes
	}
	if oldPeers == nil {
		oldPeers = make(map[string]*utils.Peer)
	}
	if oldInstances == nil {
		oldInstances = make(map[string]*config.Route)
	}
	return oldPeers, oldInstances
}

func (cs *ConfigMapClusterService) logDiffSummary(reason string, addedPeers []*utils.Peer, removedPeers []string, addedInst map[string]*config.Route, removedInst []string) {
	if len(addedPeers) > 0 || len(removedPeers) > 0 || len(addedInst) > 0 || len(removedInst) > 0 {
		logger.Log().Infof("configmap state updated: reason=%s, added_peers=%d, removed_peers=%d, added_instances=%d, removed_instances=%d",
			reason, len(addedPeers), len(removedPeers), len(addedInst), len(removedInst))
	} else {
		logger.Log().Debugf("configmap state unchanged: reason=%s", reason)
	}
}

func (cs *ConfigMapClusterService) notifyStateChangeListeners(newState *cluster.ClusterState, reason string, addedPeers []*utils.Peer, removedPeers []string, addedInst map[string]*config.Route, removedInst []string) {
	if reason == cluster.StateUpdateReasonRestore {
		for _, listener := range cs.stateChangeListeners {
			listener.OnStateRestored(newState)
		}
		return
	}
	for _, peer := range addedPeers {
		for _, listener := range cs.stateChangeListeners {
			listener.OnAddPeer(peer)
		}
	}
	for _, name := range removedPeers {
		for _, listener := range cs.stateChangeListeners {
			listener.OnRemovePeer(name)
		}
	}
	if len(addedInst) > 0 {
		for _, listener := range cs.stateChangeListeners {
			listener.OnAddInstance(addedInst, "configmap_topology_update")
		}
	}
	if len(removedInst) > 0 {
		for _, listener := range cs.stateChangeListeners {
			listener.OnRemoveInstance(removedInst, "configmap_topology_update")
		}
	}
}

// ---- Leader election ----

func (cs *ConfigMapClusterService) startLeaderCampaign() {
	leaseDuration := time.Duration(defaultLeaseDurationSec) * time.Second
	renewDeadline := time.Duration(defaultRenewDeadlineSec) * time.Second
	retryPeriod := time.Duration(defaultRetryPeriodSec) * time.Second
	if cs.cmCfg.LeaderElectionLeaseDurationSec > 0 {
		leaseDuration = time.Duration(cs.cmCfg.LeaderElectionLeaseDurationSec) * time.Second
	}
	if cs.cmCfg.LeaderElectionRenewDeadlineSec > 0 {
		renewDeadline = time.Duration(cs.cmCfg.LeaderElectionRenewDeadlineSec) * time.Second
	}
	if cs.cmCfg.LeaderElectionRetryPeriodSec > 0 {
		retryPeriod = time.Duration(cs.cmCfg.LeaderElectionRetryPeriodSec) * time.Second
	}

	role := "worker"
	if cs.localNode.IsCandidate {
		role = "candidate"
	}
	identity := EndpointName(role, cs.localNode.Addr.Host, cs.localNode.Addr.HttpPort)
	lockName := cs.cmCfg.LeaderElectionKey
	if lockName == "" {
		lockName = cluster.LeaderElectionKey
	}

	for {
		select {
		case <-cs.stopChan:
			return
		default:
		}

		lock, err := resourcelock.New(
			resourcelock.LeasesResourceLock,
			cs.namespace,
			lockName,
			cs.kubeClient.CoreV1(),
			cs.kubeClient.CoordinationV1(),
			resourcelock.ResourceLockConfig{Identity: identity},
		)
		if err != nil {
			logger.Log().Warnf("configmap failed to create leader election lock: %v, retrying", err)
			time.Sleep(retryPeriod)
			continue
		}

		campaignCtx, campaignCancel := context.WithCancel(context.Background())
		elector, err := leaderelection.NewLeaderElector(leaderelection.LeaderElectionConfig{
			Lock:          lock,
			LeaseDuration: leaseDuration,
			RenewDeadline: renewDeadline,
			RetryPeriod:   retryPeriod,
			Callbacks: leaderelection.LeaderCallbacks{
				OnStartedLeading: cs.onStartedLeading,
				OnStoppedLeading: cs.onStoppedLeading,
				OnNewLeader:      cs.onNewLeader,
			},
			Name: lockName,
		})
		if err != nil {
			campaignCancel()
			logger.Log().Warnf("configmap failed to create leader elector: %v, retrying", err)
			time.Sleep(retryPeriod)
			continue
		}
		cs.leaderElector = elector

		go func() {
			select {
			case _, _ = <-cs.stopChan:
				campaignCancel()
				return
			case _, _ = <-cs.resignChan:
				logger.Log().Warnf("configmap leader resigning due to recovery failure")
				campaignCancel()
				return
			case _, _ = <-campaignCtx.Done():
				return
			}
		}()

		elector.Run(campaignCtx)
		campaignCancel()

		select {
		case <-cs.stopChan:
			return
		default:
		}
		if !cs.isLeader.Load() {
			logger.Log().Warnf("configmap leader resigned or lost lease, re-campaigning with backoff")
			time.Sleep(resignBackoffDelay)
		} else {
			logger.Log().Infof("configmap leader elector exited, re-campaigning")
		}
	}
}

// startLeaderWatch is the worker path: instead of campaigning, workers watch
// the Lease object to learn about leader changes. The Identity string carries
// the leader's IP:HttpPort directly (design §6.1), so no CR lookup is needed.
func (cs *ConfigMapClusterService) startLeaderWatch() {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-cs.stopChan
		cancel()
	}()

	lockName := cs.cmCfg.LeaderElectionKey
	if lockName == "" {
		lockName = cluster.LeaderElectionKey
	}
	for {
		watcher, err := cs.kubeClient.CoordinationV1().Leases(cs.namespace).Watch(ctx, metav1.ListOptions{
			FieldSelector: fmt.Sprintf("metadata.name=%s", lockName),
		})
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			logger.Log().Warnf("configmap leader watch failed, will retry: %v", err)
			time.Sleep(leaderWatchRetryDelay)
			continue
		}
		for event := range watcher.ResultChan() {
			lease, ok := event.Object.(*coordinationv1.Lease)
			if !ok {
				continue
			}
			holder := lease.Spec.HolderIdentity
			if holder == nil || *holder == "" {
				continue
			}
			current := cs.leaderId.Load()
			if current == *holder {
				continue
			}
			cs.onNewLeader(*holder)
		}
		if ctx.Err() != nil {
			return
		}
	}
}

// onStartedLeading is called by client-go when this node wins the lease. Per
// design §6.5, we read the failures key from the ConfigMap cache and restore
// the failure snapshot before notifying listeners.
func (cs *ConfigMapClusterService) onStartedLeading(ctx context.Context) {
	logger.Log().Infof("configmap leader election: became leader, starting recovery")

	identity := EndpointName(cs.localNodeRole(), cs.localNode.Addr.Host, cs.localNode.Addr.HttpPort)
	cs.isLeader.Store(true)
	cs.leaderId.Store(identity)
	cs.lastLeaseRenewTime.Store(time.Now())
	cs.setConnStatus(true)

	rsmetrics.RecordLeaderChange(cs.localNode.Name, "true")
	rsmetrics.SetCurrentNodeRole(nodeRoleLeader)

	if err := cs.ensureStatusConfigMap(); err != nil {
		logger.Log().Errorf("configmap failed to ensure status ConfigMap: %v", err)
	}

	restoredFailures := cs.restoreFailuresFromConfigMap()

	for _, listener := range cs.stateChangeListeners {
		listener.OnLeaderChanged(cs.localNode, true, restoredFailures)
	}

	state := cs.State()
	peerCount, instCount := 0, 0
	if state != nil {
		peerCount = len(state.Peers)
		instCount = len(state.InstanceRoutes)
	}
	logger.Log().Infof("configmap leader recovery complete, peers=%d, instances=%d", peerCount, instCount)

	// Start the Pod List reconciliation ticker (design §6.6). Only the Leader
	// runs it. No-op when reconciliation is disabled.
	if cs.reconciler != nil {
		cs.reconciler.start()
	}
}

func (cs *ConfigMapClusterService) onStoppedLeading() {
	if !cs.isLeader.Load() {
		return
	}
	cs.isLeader.Store(false)
	cs.leaderId.Store("")
	logger.Log().Warnf("configmap leader election: lost leadership, reason=lease_expired")
	cs.setConnStatus(false)
	rsmetrics.RecordETCDConnectionsRetries()
	rsmetrics.RecordLeaderChange(cs.localNode.Name, "false")
	cs.setCurrentNodeRole()

	for _, listener := range cs.stateChangeListeners {
		listener.OnLeaderChanged(&utils.Peer{}, false, nil)
	}

	// Stop the Pod List reconciliation ticker — only the Leader runs it.
	if cs.reconciler != nil {
		cs.reconciler.stop()
	}

	select {
	case cs.resignChan <- struct{}{}:
	default:
	}
}

// onNewLeader handles a leader-change notification. If the new leader is
// another node, we parse its IP:HttpPort from the Identity and construct a
// temporary Peer if it isn't in our topology yet (design §6.3 scenario 1).
func (cs *ConfigMapClusterService) onNewLeader(identity string) {
	current := cs.leaderId.Load()
	if current == identity {
		return
	}
	logger.Log().Infof("configmap leader election: new leader detected: %s", identity)
	cs.lastLeaseRenewTime.Store(time.Now())

	if identity == EndpointName(cs.localNodeRole(), cs.localNode.Addr.Host, cs.localNode.Addr.HttpPort) {
		return
	}

	wasLeader := cs.isLeader.Load()
	if wasLeader {
		logger.Log().Warnf("configmap leader election: lost leadership, reason=new_leader_elected, newLeader=%s", identity)
		cs.isLeader.Store(false)
		cs.leaderId.Store(identity)
		rsmetrics.RecordLeaderChange(cs.localNode.Name, "false")
		cs.setCurrentNodeRole()
		for _, listener := range cs.stateChangeListeners {
			listener.OnLeaderChanged(nil, false, nil)
		}
		return
	}

	cs.leaderId.Store(identity)
	cs.setCurrentNodeRole()

	leaderPeer := cs.resolveLeaderPeer(identity)
	for _, listener := range cs.stateChangeListeners {
		listener.OnLeaderChanged(leaderPeer, false, nil)
	}
}

// resolveLeaderPeer looks up the leader's Peer in the current topology. If not
// found, we construct a temporary Peer from the Identity string so HTTP event
// sync can still be established (design §6.3 scenario 1).
func (cs *ConfigMapClusterService) resolveLeaderPeer(identity string) *utils.Peer {
	host, port, ok := ParseIdentity(identity)
	if !ok {
		logger.Log().Warnf("configmap failed to parse leader identity %s", identity)
		return nil
	}
	state := cs.State()
	if state != nil && state.Peers != nil {
		for _, peer := range state.Peers {
			if peer.Addr.Host == host && peer.Addr.HttpPort == port {
				return peer
			}
		}
	}
	// Not in topology yet — construct a temporary peer from the identity. When
	// the ConfigMap later catches up, the diff sees no change for this peer.
	// OpsPort must be filled too: ops-port URIs (request_event, schedule) would
	// otherwise resolve to port 0 and fail to connect.
	opsPort := 0
	if cs.localNode != nil {
		opsPort = cs.localNode.Addr.OpsPort
	}
	return &utils.Peer{
		Name: host,
		Addr: utils.PeerAddr{Host: host, HttpPort: port, OpsPort: opsPort},
	}
}

// ---- ClusterService interface ----

func (cs *ConfigMapClusterService) localNodeRole() string {
	if cs.localNode != nil && cs.localNode.IsCandidate {
		return "candidate"
	}
	return "worker"
}

func (cs *ConfigMapClusterService) IsLeader() bool {
	return cs.isLeader.Load()
}

func (cs *ConfigMapClusterService) GetLeader() (*utils.Peer, string) {
	leaderStr := ""
	if v := cs.leaderId.Load(); v != nil {
		if s, ok := v.(string); ok {
			leaderStr = s
		}
	}
	if leaderStr == "" {
		return nil, ""
	}
	// Identity already carries IP:HttpPort, so we can always resolve the leader
	// peer even before topology sync completes.
	if peer := cs.resolveLeaderPeer(leaderStr); peer != nil {
		return peer, leaderStr
	}
	return nil, leaderStr
}

func (cs *ConfigMapClusterService) LocalNode() *utils.Peer {
	return cs.localNode
}

func (cs *ConfigMapClusterService) State() *cluster.ClusterState {
	if raw := cs.atomicState.Load(); raw != nil {
		if state, ok := raw.(*cluster.ClusterState); ok {
			return state
		}
	}
	return nil
}

// Stats exposes configmap-mode runtime info for /cluster/consistent_status.
func (cs *ConfigMapClusterService) Stats() map[string]string {
	conn := "connected"
	if !cs.connStatus.Load() {
		conn = "disconnected"
	}
	lastRenew := ""
	if v := cs.lastLeaseRenewTime.Load(); v != nil {
		if t, ok := v.(time.Time); ok && !t.IsZero() {
			lastRenew = t.Format(time.RFC3339)
		}
	}
	stats := map[string]string{
		"cluster_mode":      "k8s-configmap",
		"namespace":         cs.namespace,
		"is_leader":         fmt.Sprintf("%v", cs.IsLeader()),
		"api_server_status": conn,
		"last_lease_renew":  lastRenew,
	}
	if cs.cmCfg != nil {
		stats["configmap_name"] = cs.cmCfg.ConfigMapName
		stats["fallback_enabled"] = fmt.Sprintf("%v", cs.cmCfg.FallbackPodDiscoveryEnabled)
		if cs.statusConfigMapName != "" {
			stats["status_configmap_name"] = cs.statusConfigMapName
		}
	}
	return stats
}

func (cs *ConfigMapClusterService) IsWorkerAlive(peerName string) (bool, error) {
	state := cs.State()
	if state == nil || state.Peers == nil {
		return false, fmt.Errorf("configmap cluster state not initialized")
	}
	_, exists := state.Peers[peerName]
	return exists, nil
}

// Topology-driven mode: HTTP join/leave APIs are unsupported (design §4, §8.3).
func (cs *ConfigMapClusterService) HandleNewPeer(peer *utils.Peer) error {
	return cluster.ErrPeerManagementNotSupported
}
func (cs *ConfigMapClusterService) HandleLeavePeer(names []string, reason string) error {
	return cluster.ErrPeerManagementNotSupported
}
func (cs *ConfigMapClusterService) HandleNewInstance(req *cluster.InstanceJoinRequest) error {
	return cluster.ErrPeerManagementNotSupported
}
func (cs *ConfigMapClusterService) HandleLeaveInstance(req *cluster.InstanceLeaveRequest, leaveReason string) error {
	return cluster.ErrPeerManagementNotSupported
}

// PersistInstanceFailures serializes the failure map to JSON and writes it to
// the ConfigMap failures-key. Writes are throttled by MinFailurePersistIntervalSec
// (design §6.4): if called again within the interval, we store the latest
// snapshot in pendingFailureData and defer the write to the next call.
func (cs *ConfigMapClusterService) PersistInstanceFailures(failures map[string]*utils.InstanceFailureRecord) error {
	if !cs.IsLeader() {
		return fmt.Errorf("only leader can persist instance failures in configmap mode")
	}
	if cs.cmCfg.FailuresKey == "" {
		return nil
	}
	if !cs.cmCfg.OwnerReference.IsConfigured() {
		logger.Log().Warnf("configmap cannot persist instance failures: owner-reference not configured, no rs-status ConfigMap available")
		return fmt.Errorf("owner-reference not configured, cannot persist instance failures to rs-status ConfigMap")
	}

	snapshot := &utils.AllInstanceFailuresSnapshot{
		LeaderId:     cs.localNode.Addr.Host,
		LocalRound:   cs.localRound.Add(1),
		SnapshotTime: time.Now().Format(time.RFC3339),
		Failures:     failures,
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		return fmt.Errorf("failed to marshal failure snapshot: %v", err)
	}

	cs.failurePersistMu.Lock()
	defer cs.failurePersistMu.Unlock()

	cs.pendingFailureData = string(data)
	minInterval := time.Duration(defaultMinFailurePersistIntervalSec) * time.Second
	if cs.cmCfg.MinFailurePersistIntervalSec > 0 {
		minInterval = time.Duration(cs.cmCfg.MinFailurePersistIntervalSec) * time.Second
	}
	now := time.Now()
	if !cs.lastFailurePersistTime.IsZero() && now.Sub(cs.lastFailurePersistTime) < minInterval {
		logger.Log().Debugf("configmap write skipped: interval not reached, pending next cycle")
		return nil
	}
	if err := cs.updateConfigMapKey(cs.cmCfg.FailuresKey, cs.pendingFailureData); err != nil {
		rsmetrics.RecordClusterStateWriteFailure("update_failures", "configmap")
		logger.Log().Warnf("configmap persist instance failures failed: %v", err)
		return err
	}
	cs.lastFailurePersistTime = now
	cs.pendingFailureData = ""
	return nil
}

// ensureStatusConfigMap creates the rs-status ConfigMap when owner-reference is
// configured. If the ConfigMap already exists it reuses it; otherwise it creates
// one with ownerReferences pointing to the configured Owner resource. The uid
// is resolved at runtime via API Server Get (design §6.4.2).
func (cs *ConfigMapClusterService) ensureStatusConfigMap() error {
	ref := cs.cmCfg.OwnerReference
	if !ref.IsConfigured() {
		return nil
	}

	statusCMName := ref.Name + "-rs-status"

	ctx, cancel := context.WithTimeout(context.Background(), statusUpdateTimeout)
	defer cancel()

	existing, err := cs.kubeClient.CoreV1().ConfigMaps(cs.namespace).Get(ctx, statusCMName, metav1.GetOptions{})
	if err == nil && existing != nil {
		cs.statusConfigMapName = statusCMName
		logger.Log().Infof("configmap status ConfigMap already exists: %s", statusCMName)
		return nil
	}
	if err != nil && !errors.IsNotFound(err) {
		logger.Log().Warnf("configmap get status ConfigMap %s failed: %v", statusCMName, err)
		return fmt.Errorf("failed to get status ConfigMap %s: %v", statusCMName, err)
	}

	gvr, err := parseGVR(ref.APIVersion, ref.Kind)
	if err != nil {
		return fmt.Errorf("failed to parse owner GVR from %s/%s: %v", ref.APIVersion, ref.Kind, err)
	}
	owner, err := cs.dynamicClient.Resource(gvr).Namespace(cs.namespace).Get(ctx, ref.Name, metav1.GetOptions{})
	if err != nil {
		logger.Log().Warnf("configmap get owner resource %s/%s failed: %v", ref.Kind, ref.Name, err)
		return fmt.Errorf("failed to get owner %s/%s: %v", ref.Kind, ref.Name, err)
	}

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      statusCMName,
			Namespace: cs.namespace,
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion:         ref.APIVersion,
					Kind:               ref.Kind,
					Name:               ref.Name,
					UID:                owner.GetUID(),
					BlockOwnerDeletion: ptr.To(true),
					Controller:         ptr.To(true),
				},
			},
		},
		Data: map[string]string{
			cs.cmCfg.FailuresKey: "{}",
		},
	}

	_, err = cs.kubeClient.CoreV1().ConfigMaps(cs.namespace).Create(ctx, cm, metav1.CreateOptions{})
	if err != nil && !errors.IsAlreadyExists(err) {
		logger.Log().Warnf("configmap create status ConfigMap %s failed: %v", statusCMName, err)
		return fmt.Errorf("failed to create status ConfigMap %s: %v", statusCMName, err)
	}

	cs.statusConfigMapName = statusCMName
	logger.Log().Infof("configmap created status ConfigMap: %s, owner=%s/%s", statusCMName, ref.Kind, ref.Name)
	return nil
}

// parseGVR converts apiVersion and kind into a GroupVersionResource for use
// with the dynamic client. apiVersion is expected in "group/version" format.
func parseGVR(apiVersion, kind string) (schema.GroupVersionResource, error) {
	gv, err := schema.ParseGroupVersion(apiVersion)
	if err != nil {
		return schema.GroupVersionResource{}, fmt.Errorf("invalid apiVersion %q: %v", apiVersion, err)
	}
	resource := strings.ToLower(kind)
	if !strings.HasSuffix(resource, "s") {
		resource += "s"
	}
	return gv.WithResource(resource), nil
}

// statusConfigMapTarget returns the rs-status ConfigMap name for writing failure
// snapshots. Returns "" if owner-reference is not configured or the rs-status
// ConfigMap has not been created yet.
func (cs *ConfigMapClusterService) statusConfigMapTarget() string {
	return cs.statusConfigMapName
}

// updateConfigMapKey performs a read-modify-write of the ConfigMap data key.
// We re-fetch the live ConfigMap (rather than trusting the Informer cache) to
// pick up the latest resourceVersion and avoid 409 conflicts under contention.
func (cs *ConfigMapClusterService) updateConfigMapKey(key, value string) error {
	ctx, cancel := context.WithTimeout(context.Background(), statusUpdateTimeout)
	defer cancel()

	targetName := cs.statusConfigMapTarget()
	if targetName == "" {
		if err := cs.ensureStatusConfigMap(); err != nil {
			logger.Log().Warnf("configmap ensure status ConfigMap failed: %v", err)
			return fmt.Errorf("failed to ensure status ConfigMap: %v", err)
		}
		targetName = cs.statusConfigMapTarget()
	}
	if targetName == "" {
		logger.Log().Warnf("configmap status ConfigMap not available: owner-reference configured but creation pending")
		return fmt.Errorf("status ConfigMap not available, owner-reference configured but creation pending")
	}
	cm, err := cs.kubeClient.CoreV1().ConfigMaps(cs.namespace).Get(ctx, targetName, metav1.GetOptions{})
	if err != nil {
		logger.Log().Warnf("configmap get status ConfigMap %s failed: %v", targetName, err)
		return fmt.Errorf("failed to get configmap %s: %v", targetName, err)
	}
	if cm.Data == nil {
		cm.Data = make(map[string]string)
	}
	cm.Data[key] = value
	if _, err := cs.kubeClient.CoreV1().ConfigMaps(cs.namespace).Update(ctx, cm, metav1.UpdateOptions{}); err != nil {
		logger.Log().Warnf("configmap write failed: operation=update_failures, key=%s, err=%v", key, err)
		return fmt.Errorf("failed to update configmap key %s: %v", key, err)
	}
	return nil
}

// restoreFailuresFromConfigMap reads the failures-key from the rs-status
// ConfigMap Informer cache (or API fallback) and deserializes it.
// Used at OnStartedLeading (design §6.5).
func (cs *ConfigMapClusterService) restoreFailuresFromConfigMap() *utils.AllInstanceFailuresSnapshot {
	if cs.cmCfg.FailuresKey == "" {
		return nil
	}
	data := cs.getFailuresDataFromInformerCache()
	if data == "" {
		data = cs.getFailuresDataFromAPIDirect()
	}
	if data == "" {
		return nil
	}
	var snapshot utils.AllInstanceFailuresSnapshot
	if err := json.Unmarshal([]byte(data), &snapshot); err != nil {
		logger.Log().Warnf("configmap failed to unmarshal failures snapshot on leader start: %v", err)
		return nil
	}
	logger.Log().Infof("configmap restored %d failure records from failures-key", len(snapshot.Failures))
	return &snapshot
}

// getFailuresDataFromInformerCache reads the failures-key from the rs-status
// ConfigMap Informer's local cache. Returns "" if the Informer is not running
// or the ConfigMap/key is absent.
func (cs *ConfigMapClusterService) getFailuresDataFromInformerCache() string {
	if cs.statusConfigMapInformer == nil {
		return ""
	}
	obj, exists, err := cs.statusConfigMapInformer.GetIndexer().GetByKey(fmt.Sprintf("%s/%s", cs.namespace, cs.statusConfigMapName))
	if err != nil || !exists {
		return ""
	}
	cm, ok := obj.(*corev1.ConfigMap)
	if !ok || cm == nil || cm.Data == nil {
		return ""
	}
	return cm.Data[cs.cmCfg.FailuresKey]
}

// getFailuresDataFromAPIDirect reads the failures-key from the rs-status
// ConfigMap via API Server Get. Returns "" if the ConfigMap or key is absent.
func (cs *ConfigMapClusterService) getFailuresDataFromAPIDirect() string {
	if cs.statusConfigMapName == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), statusUpdateTimeout)
	defer cancel()
	cm, err := cs.kubeClient.CoreV1().ConfigMaps(cs.namespace).Get(ctx, cs.statusConfigMapName, metav1.GetOptions{})
	if err != nil || cm == nil || cm.Data == nil {
		return ""
	}
	return cm.Data[cs.cmCfg.FailuresKey]
}

// GetHealthStats returns the cached health stats. The cache is kept fresh by
// the rs-status ConfigMap Informer callbacks. Falls back to a direct API read
// only when the Informer has not yet populated the cache (e.g. rs-status CM
// not created yet at startup).
func (cs *ConfigMapClusterService) GetHealthStats() (cluster.EtcdHealthStatsCache, error) {
	cs.healthStatsLock.RLock()
	initialized := cs.healthStatsInitialized
	cache := cs.healthStatsCache
	cs.healthStatsLock.RUnlock()
	if initialized {
		return cache, nil
	}
	if cs.cmCfg.FailuresKey != "" && cs.statusConfigMapName != "" {
		data := cs.getFailuresDataFromAPIDirect()
		if data != "" {
			var snapshot utils.AllInstanceFailuresSnapshot
			if err := json.Unmarshal([]byte(data), &snapshot); err == nil {
				topo, _ := cs.currentTopology.Load().(*Topology)
				if topo == nil {
					topo = &Topology{}
				}
				c := buildHealthStatsFromFailures(&snapshot, topo, cs.cmCfg)
				cs.healthStatsLock.Lock()
				cs.healthStatsCache = c
				cs.healthStatsInitialized = true
				cs.healthStatsLock.Unlock()
				return c, nil
			}
		}
	}
	return cache, nil
}

func (cs *ConfigMapClusterService) UpdateHealthStats(cache cluster.EtcdHealthStatsCache) error {
	cs.healthStatsLock.Lock()
	defer cs.healthStatsLock.Unlock()
	cs.healthStatsCache = cache
	return nil
}

func (cs *ConfigMapClusterService) RegisterStateApplier(applier cluster.StateApplier) {
	if applier != nil {
		cs.stateChangeListeners = append(cs.stateChangeListeners, applier)
	}
}

func (cs *ConfigMapClusterService) GetApplierNames() []string {
	names := make([]string, 0, len(cs.stateChangeListeners))
	for _, listener := range cs.stateChangeListeners {
		names = append(names, listener.GetName())
	}
	return names
}

// GetInstanceCleanHandler returns a handler that marks failed instances in the
// in-memory failure snapshot. The actual persistence happens via
// PersistInstanceFailures on the next health-check cycle.
func (cs *ConfigMapClusterService) GetInstanceCleanHandler() utils.Subscriber {
	return &configmapInstanceLeaveHandler{cs: cs}
}

// Close stops the informer and cancels leader election. Idempotent.
func (cs *ConfigMapClusterService) Close() {
	select {
	case <-cs.stopChan:
		// already closed
		return
	default:
		close(cs.stopChan)
	}
	if cs.informerStarted.Load() {
		close(cs.informerStopChan)
	}
	if cs.statusInformerStopChan != nil {
		close(cs.statusInformerStopChan)
	}
	// Stop the Pod List reconciler if running (design §6.6.8).
	if cs.reconciler != nil {
		cs.reconciler.stop()
	}
	logger.Log().Infof("configmap cluster service closed")
}

// ---- helpers ----

func (cs *ConfigMapClusterService) setCurrentNodeRole() {
	if cs.isLeader.Load() {
		rsmetrics.SetCurrentNodeRole(nodeRoleLeader)
	} else if cs.localNode != nil && cs.localNode.IsCandidate {
		rsmetrics.SetCurrentNodeRole(nodeRoleCandidate)
	} else {
		rsmetrics.SetCurrentNodeRole(nodeRoleWorker)
	}
}

func (cs *ConfigMapClusterService) setConnStatus(connected bool) {
	was := cs.connStatus.Swap(connected)
	if was == connected {
		return
	}
	rsmetrics.SetClusterServiceConnStatus(connected)
	if connected {
		logger.Log().Infof("configmap api-server connection restored")
	} else {
		logger.Log().Warnf("configmap api-server connection lost")
		rsmetrics.RecordETCDConnectionsRetries()
	}
}

// startConnHealthCheck polls the APIServer discovery endpoint when the
// connection is marked down, so connStatus recovers even if the Informer hasn't
// produced an event yet.
func (cs *ConfigMapClusterService) startConnHealthCheck() {
	go func() {
		ticker := time.NewTicker(connCheckInterval)
		defer ticker.Stop()
		for {
			select {
			case <-cs.stopChan:
				return
			case <-ticker.C:
				if cs.connStatus.Load() {
					continue
				}
				if _, err := cs.kubeClient.Discovery().ServerVersion(); err == nil {
					cs.setConnStatus(true)
				}
			}
		}
	}()
}

// refreshHealthStatsFromFailures reparses the failures JSON and updates the
// healthStatsCache. Called from the rs-status ConfigMap Informer callbacks so
// the cache stays fresh without API calls. When topo is nil, the current
// topology from the topology Informer is used. Even an empty failuresData
// (no failures) marks the cache as initialized so GetHealthStats does not
// fall back to API.
func (cs *ConfigMapClusterService) refreshHealthStatsFromFailures(failuresData string, topo *Topology) {
	var snapshot utils.AllInstanceFailuresSnapshot
	if failuresData != "" {
		if err := json.Unmarshal([]byte(failuresData), &snapshot); err != nil {
			return
		}
	}
	if topo == nil {
		topo, _ = cs.currentTopology.Load().(*Topology)
	}
	cache := buildHealthStatsFromFailures(&snapshot, topo, cs.cmCfg)
	cs.healthStatsLock.Lock()
	cs.healthStatsCache = cache
	cs.healthStatsInitialized = true
	cs.healthStatsLock.Unlock()
}

// buildHealthStatsFromFailures aggregates failure records by role and computes
// the available count as (total - faulty). Total per role comes from the
// current topology's instance routes (design §8.2).
func buildHealthStatsFromFailures(snapshot *utils.AllInstanceFailuresSnapshot, topo *Topology, cfg *config.ConfigMapConfig) cluster.EtcdHealthStatsCache {
	cache := cluster.EtcdHealthStatsCache{
		EveryRoleAvailableCountMap: make(map[string]int),
		FailureInstanceDetail:      make(map[string]map[string]*utils.InstanceFailureRecord),
	}
	if topo == nil {
		topo = &Topology{}
	}
	// total per role from topology
	routerGroupType := effectiveRouterGroupType(cfg)
	totalByRole := make(map[string]int)
	for _, group := range topo.ServerGroupList {
		if group.GroupType == routerGroupType {
			continue
		}
		totalByRole[group.GroupType] += len(group.ServerList)
	}
	// faulty per role from snapshot
	faultyByRole := make(map[string]int)
	for name, rec := range snapshot.Failures {
		if rec == nil {
			continue
		}
		role := rec.Role
		if role == "" {
			continue
		}
		if rec.Status != constant.RunningStatus {
			faultyByRole[role]++
			if cache.FailureInstanceDetail[role] == nil {
				cache.FailureInstanceDetail[role] = make(map[string]*utils.InstanceFailureRecord)
			}
			cache.FailureInstanceDetail[role][name] = rec
		}
	}
	for role, total := range totalByRole {
		cache.EveryRoleAvailableCountMap[role] = total - faultyByRole[role]
	}
	if snapshot.SnapshotTime != "" {
		if t, err := time.Parse(time.RFC3339, snapshot.SnapshotTime); err == nil {
			cache.UpdateTime = t
		}
	}
	if cache.UpdateTime.IsZero() {
		cache.UpdateTime = time.Now()
	}
	return cache
}

func buildRestConfig(kubeConfigPath string) (*rest.Config, error) {
	var rc *rest.Config
	var err error
	if kubeConfigPath != "" {
		rc, err = clientcmd.BuildConfigFromFlags("", kubeConfigPath)
		if err != nil {
			return nil, fmt.Errorf("failed to build config from kubeconfig %s: %v", kubeConfigPath, err)
		}
	} else if kubeconfig := os.Getenv("KUBECONFIG"); kubeconfig != "" {
		rc, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
		if err != nil {
			return nil, fmt.Errorf("failed to build config from KUBECONFIG env %s: %v", kubeconfig, err)
		}
	} else {
		rc, err = rest.InClusterConfig()
		if err != nil {
			return nil, fmt.Errorf("failed to get in-cluster config (no kubeconfig specified): %v", err)
		}
	}
	rc.QPS = defaultClientQPS
	rc.Burst = defaultClientBurst
	return rc, nil
}

// configmapInstanceLeaveHandler marks failed instances. Unlike the CRD mode
// handler, there is no per-endpoint status subresource to update — the failure
// is recorded in the next PersistInstanceFailures snapshot.
type configmapInstanceLeaveHandler struct {
	cs *ConfigMapClusterService
}

func (h *configmapInstanceLeaveHandler) HandleMessage(message interface{}) {
	leaveRequest, ok := message.(*cluster.InstanceLeaveRequest)
	if !ok {
		logger.Log().Errorf("configmap invalid message type, expected *cluster.InstanceLeaveRequest, got %T", message)
		return
	}
	logger.Log().Tracef("configmap receive instance leave: %v", leaveRequest.InstanceNames)
	cleanRes := make(map[string]int)
	if state := h.cs.State(); state != nil && state.InstanceRoutes != nil {
		for _, insName := range leaveRequest.InstanceNames {
			if route := state.InstanceRoutes[insName]; route != nil {
				cleanRes[route.Role]++
			}
		}
	}
	for role, count := range cleanRes {
		rsmetrics.RecordDiscardUnreachableEndpoints(role, count)
	}
}
