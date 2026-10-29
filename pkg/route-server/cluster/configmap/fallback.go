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
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"infer-dp-scheduler/pkg/common/cluster"
	rsmetrics "infer-dp-scheduler/pkg/route-server/metrics"

	"infer-dp-scheduler/pkg/common/logger"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"
)

// podListReconciler implements the Pod List periodic reconciliation
// (design §6.6). It is a stateless ticker + staleness check — there is NO
// "degraded mode" state machine. ConfigMap topology rarely updates when stable
// (no change = no write), so "long time no update" is the NORMAL case, not a
// failure signal.
//
// Lifecycle: created in Start() when FallbackPodDiscoveryEnabled is true;
// started in onStartedLeading (Leader only); stopped in onStoppedLeading and
// Close(). The ConfigMap Informer calls updateConfigMapTimestamp on every
// topology update so the reconciler can skip List when the Watch path already
// handled the change.
//
// The reconciler only updates in-memory routing state via
// applyTopology — it NEVER writes back to the ConfigMap topology
// key. ConfigMap topology is always owned by the external refresh service.
type podListReconciler struct {
	cs       *ConfigMapClusterService
	interval time.Duration

	// lastConfigMapUpdate is the timestamp of the most recent ConfigMap topology
	// update observed by the Informer. Initialized to time.Now() at start so the
	// first tick does not immediately fire a List before any ConfigMap event has
	// arrived.
	lastConfigMapUpdate atomic.Value // time.Time

	// kubeClient is captured at start time from the parent service. It is the
	// same client used by the ConfigMap Informer.
	kubeClient kubernetes.Interface
	// namespace reuses the ConfigMap namespace (design §6.6.3). Instance
	// isolation is by infer-group-name, not by Namespace.
	namespace string
	// inferGroupName is the value of the podgroup-name label to filter on.
	// Required when enabled. Instance-isolation boundary.
	inferGroupName string

	// inferGroupLabelKey is the Pod label key holding the infer-group name.
	// Used to build the List labelSelector. Defaults to
	// defaultInferGroupLabelKey when empty.
	inferGroupLabelKey string
	// inferGroupRoleTypeLabelKey is the Pod label key holding the group_type
	// (e.g. master/prefill/decode). Passed to BuildTopologyFromPods. Defaults to
	// defaultInferGroupRoleTypeLabelKey when empty.
	inferGroupRoleTypeLabelKey string

	stopChan chan struct{}
	running  atomic.Bool
}

// newPodListReconciler builds a reconciler. intervalSec defaults to
// defaultFallbackPodListIntervalSec when <= 0. The label-key arguments default
// to the package-level defaults when empty. The reconciler is not started;
// call start() from onStartedLeading.
func newPodListReconciler(cs *ConfigMapClusterService, kubeClient kubernetes.Interface, namespace, inferGroupName string, intervalSec int, inferGroupLabelKey, inferGroupRoleTypeLabelKey string) *podListReconciler {
	interval := time.Duration(intervalSec) * time.Second
	if interval <= 0 {
		interval = time.Duration(defaultFallbackPodListIntervalSec) * time.Second
	}
	if inferGroupLabelKey == "" {
		inferGroupLabelKey = defaultInferGroupLabelKey
	}
	if inferGroupRoleTypeLabelKey == "" {
		inferGroupRoleTypeLabelKey = defaultInferGroupRoleTypeLabelKey
	}
	return &podListReconciler{
		cs:                         cs,
		interval:                   interval,
		kubeClient:                 kubeClient,
		namespace:                  namespace,
		inferGroupName:             inferGroupName,
		inferGroupLabelKey:         inferGroupLabelKey,
		inferGroupRoleTypeLabelKey: inferGroupRoleTypeLabelKey,
	}
}

// updateConfigMapTimestamp refreshes the staleness baseline. Called by the
// ConfigMap Informer handlers (onConfigMapAdd/Update) after a successful
// topology parse, so the next tick skips List when the Watch path already
// processed the change.
func (r *podListReconciler) updateConfigMapTimestamp() {
	r.lastConfigMapUpdate.Store(time.Now())
}

// start launches the reconciliation ticker. Only the Leader calls this.
// Idempotent: a second call while running is a no-op.
func (r *podListReconciler) start() {
	if r == nil {
		return
	}
	if !r.running.CompareAndSwap(false, true) {
		return
	}
	// Initialize the staleness baseline to now so the first interval does not
	// fire a List before any ConfigMap event has had a chance to arrive.
	r.lastConfigMapUpdate.Store(time.Now())
	r.stopChan = make(chan struct{})
	go r.run()
	logger.Log().Infof("configmap reconcile: started, interval=%s, infer_group=%s, namespace=%s",
		r.interval, r.inferGroupName, r.namespace)
}

// stop halts the reconciliation ticker. Called on Leader step-down and Close.
// Idempotent.
func (r *podListReconciler) stop() {
	if r == nil {
		return
	}
	if !r.running.CompareAndSwap(true, false) {
		return
	}
	close(r.stopChan)
	logger.Log().Debugf("configmap reconcile: stopped")
}

// run is the ticker loop. It exits when stopChan is closed.
func (r *podListReconciler) run() {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-r.stopChan:
			return
		case <-ticker.C:
			r.reconcileTick()
		}
	}
}

// reconcileTick performs one staleness check and, if stale, a Pod List +
// reconcile. See design §6.6.2/§6.6.3.
func (r *podListReconciler) reconcileTick() {
	last, ok := r.lastConfigMapUpdate.Load().(time.Time)
	if !ok {
		last = time.Time{}
	}
	elapsed := time.Since(last)
	if elapsed < r.interval {
		logger.Log().Debugf("configmap reconcile: skip, configmap updated recently, elapsed=%ds", int(elapsed.Seconds()))
		return
	}

	logger.Log().Infof("configmap reconcile: list pods triggered, elapsed=%ds", int(elapsed.Seconds()))
	rsmetrics.RecordConfigmapFallbackPodList()

	topo, err := r.listAndBuildTopology()
	if err != nil {
		logger.Log().Warnf("configmap reconcile: list pods failed: %v", err)
		return
	}

	// Snapshot pre-update state to detect whether the apply actually changed
	// anything. applyTopologyAndFailures diffs internally; we compare the
	// version timestamp to detect a real change.
	prevState := r.cs.State()
	prevVersion := int64(0)
	if prevState != nil {
		prevVersion = prevState.Version
	}

	// Reuse the shared idempotent entry — same path as the ConfigMap Watch
	// handlers. Failures key is empty: Pod List does not carry failure info.
	// NEVER writes back to ConfigMap (topology is read-only for RouteServer).
	r.cs.applyTopology(topo, cluster.StateUpdateReasonPodListReconcile)

	newState := r.cs.State()
	newVersion := int64(0)
	if newState != nil {
		newVersion = newState.Version
	}
	changed := newState != nil && newVersion != prevVersion
	if changed {
		rsmetrics.RecordConfigmapFallbackTopologyChange()
	}

	podCount := 0
	for _, g := range topo.ServerGroupList {
		podCount += len(g.ServerList)
	}
	logger.Log().Infof("configmap reconcile: completed, pods=%d, topology_changed=%v", podCount, changed)
}

// listAndBuildTopology issues a one-shot Pod List with a labelSelector on the
// configured podgroup-name label key and rebuilds a Topology from the result.
func (r *podListReconciler) listAndBuildTopology() (*Topology, error) {
	selector := labels.Set(map[string]string{
		r.inferGroupLabelKey: r.inferGroupName,
	}).AsSelector().String()

	ctx, cancel := context.WithTimeout(context.Background(), statusUpdateTimeout)
	defer cancel()
	podList, err := r.kubeClient.CoreV1().Pods(r.namespace).List(ctx, metav1.ListOptions{
		LabelSelector: selector,
	})
	if err != nil {
		return nil, fmt.Errorf("list pods in namespace %s with selector %q: %v", r.namespace, selector, err)
	}

	// Convert []corev1.Pod to []*corev1.Pod for BuildTopologyFromPods.
	pods := make([]*corev1.Pod, 0, len(podList.Items))
	for i := range podList.Items {
		pods = append(pods, &podList.Items[i])
	}
	return BuildTopologyFromPods(pods, r.inferGroupRoleTypeLabelKey), nil
}
