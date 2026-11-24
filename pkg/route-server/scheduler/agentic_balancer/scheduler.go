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

package agentic_balancer

import (
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"route-server/pkg/common/constant"
	"route-server/pkg/common/errs"
	instance_metrics "route-server/pkg/common/instance-metrics"
	"route-server/pkg/common/logger"
	"route-server/pkg/common/scheduler"
	"route-server/pkg/common/utils"
	"route-server/pkg/route-server/scheduler/agentic_balancer/controller"
	"route-server/pkg/route-server/scheduler/agentic_balancer/queue"
	"route-server/pkg/route-server/scheduler/agentic_balancer/state"
)

const scoreEpsilon = 1e-9

var _ scheduler.ScheduleAlgorithm = &SessionAwareScheduler{}
var _ scheduler.LifecycleAlgorithm = &SessionAwareScheduler{}

// scoreWeights is a consistent scoring-config snapshot for one dispatch, so a
// concurrent UpdateConfig cannot change the values mid-computation.
type scoreWeights struct {
	affinityWeight   float64       // λ: affinity gain per unit of gated affinity score
	overloadUtil     float64       // U: single-worker admission threshold and gate scale
	affinityHalfLife time.Duration // H: time factor halves every H of acting idle
}

// agentic-balancer instances are counted per role because the decision lock and
// the priority queue are process-global: a second instance for the same role would
// share one heap while owning its own waiters, so duplicates are logged loudly.
var (
	abInstancesMu     sync.Mutex
	abInstancesByRole = map[string]int{}
)

func noteAgenticBalancerRole(role string) int {
	abInstancesMu.Lock()
	defer abInstancesMu.Unlock()
	abInstancesByRole[role]++
	return abInstancesByRole[role]
}

func dropAgenticBalancerRole(role string) {
	abInstancesMu.Lock()
	defer abInstancesMu.Unlock()
	if abInstancesByRole[role] > 1 {
		abInstancesByRole[role]--
	} else {
		delete(abInstancesByRole, role)
	}
}

// SessionAwareScheduler replaces abc6 as the prefill scheduler for agent traffic.
type SessionAwareScheduler struct {
	name    string
	config  map[string]string
	running atomic.Bool

	// mu guards every config field below. Order: mu → pq.mu/SessionTable.mu/TTLManager.mu;
	// the dispatch path reads under mu.RLock before taking DecisionMu, never after.
	mu sync.RWMutex

	targetRole string // worker role to filter candidates (default: "prefill"; set "decode" for Hybrid mode)

	// scoring weights (parsed from Init config, with defaults)
	affinityWeight   float64 // λ = γ/β, affinity gain relative to load cost
	overloadUtil     float64 // U: single-worker admission threshold and gate scale
	affinityHalfLife time.Duration

	ctrl *controller.Controller

	pq               *queue.PriorityQueue
	maxQueueSize     int
	agingRate        float64 // η: priority gained per second of queue wait
	dispatchTimeout  time.Duration
	maxWorkerHistory int
	sessionTTL       time.Duration

	// updateIntervalMs is the worker-stats refresh cadence requested from the instance manager.
	updateIntervalMs int64

	closeOnce sync.Once
	closeChan chan struct{}

	// countedRole is the role registered in abInstancesByRole, empty when not counted.
	countedRole string
}

func NewSessionAwareScheduler() (scheduler.ScheduleAlgorithm, error) {
	return &SessionAwareScheduler{
		name:             "agentic-balancer",
		targetRole:       constant.Prefill,
		affinityWeight:   0.5,              // λ = legacy γ/β = 0.2/0.4
		overloadUtil:     0.9,              // U: single threshold for admission and gate
		affinityHalfLife: 30 * time.Second, // H = legacy affinity-decay-mid-point 30.0s
		maxQueueSize:     1000,
		agingRate:        queue.DefaultAgingRate, // η = legacy 10.0/60s ≈ 0.167 priority/s
		dispatchTimeout:  30 * time.Second,
		maxWorkerHistory: 4,
		sessionTTL:       30 * time.Minute,
		updateIntervalMs: defaultUpdateIntervalMs,
	}, nil
}

// applyConfig parses the scheduler config for Init and UpdateConfig. Deprecated
// pairs are migrated before the new keys are read, so an explicitly configured
// new key always wins over a migrated value.
func (s *SessionAwareScheduler) applyConfig(config map[string]string) {
	s.migrateLegacyBetaGamma(config)
	s.migrateLegacyAging(config)

	s.affinityWeight = parseFloatConfigAllowZero(config, "affinity-weight", s.affinityWeight, 0.5)
	s.warnRetiredKeys(config)
	s.migrateLegacyAffinityCurve(config)
	s.applySchedulingKnobs(config)
	s.applyUpdateInterval(config)
	s.applyTargetRole(config)
	s.config = config
}

// legacyPairState reports how a deprecated two-key pair fared.
type legacyPairState int

const (
	legacyPairAbsent     legacyPairState = iota // primary key not configured
	legacyPairIncomplete                        // primary set, peer missing
	legacyPairReady                             // both set: migrate the value
)

// readLegacyPair loads a deprecated two-key pair and warns when an explicit new
// key overrides the migrated value.
func readLegacyPair(config map[string]string, primary, peer, newKey string) (string, string, legacyPairState) {
	primaryRaw, hasPrimary := config[primary]
	if !hasPrimary {
		return "", "", legacyPairAbsent
	}
	peerRaw, hasPeer := config[peer]
	if !hasPeer {
		return primaryRaw, "", legacyPairIncomplete
	}
	if _, hasNew := config[newKey]; hasNew {
		logger.Log().Warnf("agentic-balancer: %s and deprecated %s/%s are both configured; %s (explicit) wins",
			newKey, primary, peer, newKey)
	}
	return primaryRaw, peerRaw, legacyPairReady
}

// migrateLegacyBetaGamma migrates the retired load/affinity pair onto affinity-weight;
// degenerate ratios are rejected instead of approximated.
func (s *SessionAwareScheduler) migrateLegacyBetaGamma(config map[string]string) {
	betaRaw, gammaRaw, state := readLegacyPair(config, "beta-load", "gamma-affinity", "affinity-weight")
	if state == legacyPairIncomplete {
		logger.Log().Warnf("agentic-balancer: beta-load set without gamma-affinity; ratio is underdetermined, using affinity-weight=%v", s.affinityWeight)
	}
	if state != legacyPairReady {
		return
	}
	beta, errB := strconv.ParseFloat(betaRaw, 64)
	gamma, errG := strconv.ParseFloat(gammaRaw, 64)
	if errB != nil || errG != nil || beta < 0 || gamma < 0 {
		logger.Log().Warnf("agentic-balancer: legacy beta-load/gamma-affinity invalid (beta=%q gamma=%q); ignoring", betaRaw, gammaRaw)
		return
	}
	switch {
	case beta > 0:
		s.affinityWeight = gamma / beta
		logger.Log().Warnf("agentic-balancer: beta-load/gamma-affinity are deprecated; one-way migrated to affinity-weight=γ/β=%v", s.affinityWeight)
	case gamma > 0:
		// Pure affinity has no finite γ/β: fail loud rather than masking the
		// division with an arbitrary epsilon.
		logger.Log().Errorf("agentic-balancer: beta-load=0 with gamma-affinity=%q is pure affinity, "+
			"not migratable to affinity-weight; configure affinity-weight explicitly", gammaRaw)
	default:
		logger.Log().Errorf("agentic-balancer: beta-load=gamma-affinity=0 tied all candidates under the legacy " +
			"formula; not equivalent to affinity-weight=0 (lowest-load wins). Configure affinity-weight explicitly")
	}
}

// migrateLegacyAging migrates the retired aging knobs onto a per-second rate.
func (s *SessionAwareScheduler) migrateLegacyAging(config map[string]string) {
	factorRaw, ttlRaw, state := readLegacyPair(config, "aging-factor", "aging-ttl", "aging-rate")
	if state == legacyPairIncomplete {
		logger.Log().Warnf("agentic-balancer: aging-factor set without aging-ttl; rate is underdetermined, using aging-rate=%v", s.agingRate)
	}
	if state != legacyPairReady {
		return
	}
	factor, errF := strconv.ParseFloat(factorRaw, 64)
	ttl, errD := time.ParseDuration(ttlRaw)
	if errF != nil || errD != nil || factor < 0 || ttl <= 0 {
		logger.Log().Warnf("agentic-balancer: legacy aging-factor/aging-ttl invalid (factor=%q ttl=%q); ignoring", factorRaw, ttlRaw)
		return
	}
	s.agingRate = factor / ttl.Seconds()
	logger.Log().Warnf("agentic-balancer: aging-factor/aging-ttl are deprecated; one-way migrated to aging-rate=%v priority/s", s.agingRate)
}

// warnRetiredKeys reports knobs that were retired without a direct equivalent.
func (s *SessionAwareScheduler) warnRetiredKeys(config map[string]string) {
	for _, retired := range []string{"load-score-threshold", "decay-strength", "min-decay-factor", "cleanup-interval"} {
		if raw, exists := config[retired]; exists && raw != "" {
			logger.Log().Warnf("agentic-balancer: %s=%q is retired with no direct equivalent "+
				"(single threshold overload-util + continuous gate g_U replace the segmented penalty; "+
				"cleanup cadence is owned by the controller tick, cleanup-interval never drove a timer)",
				retired, raw)
		}
	}
}

// migrateLegacyAffinityCurve maps the deprecated decay knobs onto the half-life curve;
// only the 50% decay point is aligned, decay-k has no equivalent.
func (s *SessionAwareScheduler) migrateLegacyAffinityCurve(config map[string]string) {
	_, hasMid := config["affinity-decay-mid-point"]
	if hasMid {
		if _, hasNew := config["affinity-half-life"]; hasNew {
			logger.Log().Warnf("agentic-balancer: affinity-half-life and deprecated affinity-decay-k/mid-point are both configured; " +
				"affinity-half-life (explicit) wins")
		}
		mid := parseFloatConfig(config, "affinity-decay-mid-point", s.affinityHalfLife.Seconds(), 30.0)
		s.affinityHalfLife = time.Duration(mid * float64(time.Second))
		logger.Log().Warnf("agentic-balancer: affinity-decay-k/affinity-decay-mid-point are deprecated; "+
			"migrated to affinity-half-life=%v (curve replaced, only the 50%% decay point is aligned; k=%q has no new equivalent)",
			s.affinityHalfLife, config["affinity-decay-k"])
	}
	if _, hasK := config["affinity-decay-k"]; hasK && !hasMid {
		logger.Log().Warnf("agentic-balancer: affinity-decay-k is deprecated with no direct equivalent "+
			"(half-life decay has no steepness knob); ignored, using affinity-half-life=%v", s.affinityHalfLife)
	}
	s.affinityHalfLife = parseDurationConfig(config, "affinity-half-life", s.affinityHalfLife, 30*time.Second)
}

// applySchedulingKnobs parses the current, non-deprecated numeric knobs.
func (s *SessionAwareScheduler) applySchedulingKnobs(config map[string]string) {
	s.maxQueueSize = parseIntConfig(config, "max-queue-size", s.maxQueueSize, 1000)
	s.overloadUtil = parseFloatConfig(config, "overload-util", s.overloadUtil, 0.90)
	// U must be finite and in (0,1]: the gate divides by U, so 0 is rejected.
	if s.overloadUtil <= 0 || s.overloadUtil > 1 || math.IsNaN(s.overloadUtil) || math.IsInf(s.overloadUtil, 0) {
		logger.Log().Warnf("agentic-balancer: invalid overload-util=%v (want 0<U<=1), using default 0.90", s.overloadUtil)
		s.overloadUtil = 0.90
	}
	s.agingRate = parseFloatConfigAllowZero(config, "aging-rate", s.agingRate, queue.DefaultAgingRate)
	s.dispatchTimeout = parseDurationConfig(config, "dispatch-timeout", s.dispatchTimeout, 30*time.Second)
	s.maxWorkerHistory = parseIntConfig(config, "max-worker-history", s.maxWorkerHistory, 4)
	s.sessionTTL = parseDurationConfig(config, "session-ttl", s.sessionTTL, 30*time.Minute)
}

// applyUpdateInterval parses the stats refresh interval shared with abc6. A
// negative value means no periodic refresh and is accepted instead of rejected.
func (s *SessionAwareScheduler) applyUpdateInterval(config map[string]string) {
	raw, exists := config[constant.UpdateIntervalMs]
	if !exists || raw == "" {
		return
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		logger.Log().Warnf("agentic-balancer: invalid %s=%q, using %d",
			constant.UpdateIntervalMs, raw, s.updateIntervalMs)
		return
	}
	s.updateIntervalMs = v
}

// applyTargetRole applies target-role and keeps the per-role instance count
// consistent; abInstancesMu is a leaf lock and never re-enters the decision path.
func (s *SessionAwareScheduler) applyTargetRole(config map[string]string) {
	if raw, ok := config["target-role"]; ok && raw != "" {
		s.targetRole = raw
	}
	if s.countedRole == s.targetRole {
		return
	}
	if s.countedRole != "" {
		dropAgenticBalancerRole(s.countedRole)
	}
	s.countedRole = s.targetRole
	if n := noteAgenticBalancerRole(s.targetRole); n > 1 {
		logger.Log().Warnf("agentic-balancer: %d instances now serve role %q; each owns an "+
			"independent queue, so instance A's gate (G_p) cannot defer to instance B's backlog — "+
			"backlogs of different instances sharing one pool dispatch independently and can "+
			"mutely bypass each other's queues. The supported deployment keeps one agentic-balancer "+
			"instance per role (unit tests intentionally create several).",
			n, s.targetRole)
	}
}

// On missing/invalid value returns cur if non-zero, else def.
func parseFloatConfig(config map[string]string, key string, cur, def float64) float64 {
	raw, exists := config[key]
	if !exists {
		if cur > 0 {
			return cur
		}
		return def
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil || v < 0 {
		logger.Log().Warnf("agentic-balancer: invalid %s=%q, using default %v", key, raw, def)
		return def
	}
	return v
}

// parseFloatConfigAllowZero accepts an explicit 0 (λ=0 pure load, η=0 aging off).
// A missing key keeps cur, so UpdateConfig never re-enables a disabled knob.
func parseFloatConfigAllowZero(config map[string]string, key string, cur, def float64) float64 {
	raw, exists := config[key]
	if !exists {
		return cur
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil || v < 0 || math.IsNaN(v) || math.IsInf(v, 0) {
		logger.Log().Warnf("agentic-balancer: invalid %s=%q, using default %v", key, raw, def)
		return def
	}
	return v
}

func parseIntConfig(config map[string]string, key string, cur, def int) int {
	raw, exists := config[key]
	if !exists {
		if cur > 0 {
			return cur
		}
		return def
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v < 0 {
		logger.Log().Warnf("agentic-balancer: invalid %s=%q, using default %v", key, raw, def)
		return def
	}
	return v
}

func parseDurationConfig(config map[string]string, key string, cur, def time.Duration) time.Duration {
	raw, exists := config[key]
	if !exists {
		if cur > 0 {
			return cur
		}
		return def
	}
	v, err := time.ParseDuration(raw)
	if err != nil || v <= 0 {
		logger.Log().Warnf("agentic-balancer: invalid %s=%q, using default %v", key, raw, def)
		return def
	}
	return v
}

func (s *SessionAwareScheduler) Init(config map[string]string) error {
	if config == nil {
		config = make(map[string]string)
	}
	// Held to the end: every field below is read once at startup, and UpdateConfig
	// rewrites them under the same lock.
	s.mu.Lock()
	defer s.mu.Unlock()
	s.applyConfig(config)

	logger.Log().Infof("Initializing agentic-balancer scheduler: targetRole=%s, affinityWeight=%.3f overloadUtil=%.2f affinityHalfLife=%s agingRate=%.4f dispatchTimeout=%s updateIntervalMs=%d",
		s.targetRole, s.affinityWeight, s.overloadUtil, s.affinityHalfLife, s.agingRate, s.dispatchTimeout, s.updateIntervalMs)

	if state.GetGlobalStateManager() == nil {
		state.SetGlobalStateManager(state.NewStateManager(state.TTLConfig{
			SessionTTL: s.sessionTTL,
		}))
		logger.Log().Infof("agentic-balancer: initialized global StateManager (sessionTTL=%s)", s.sessionTTL)
	}

	s.closeChan = make(chan struct{})
	sm := state.GetGlobalStateManager()
	sm.Sessions.SetMaxWorkerHistory(s.maxWorkerHistory)
	s.pq = queue.NewPriorityQueue(s.maxQueueSize, s.agingRate)
	s.ctrl = controller.NewController(sm, controller.ControllerConfig{
		Interval:       5 * time.Second,
		StaleWorkerTTL: 60 * time.Second,
		OverloadUtil:   s.overloadUtil,
		TargetRole:     s.targetRole,
	}, nil, s.pq, s.drainRound)
	s.running.Store(true)
	return nil
}

func (s *SessionAwareScheduler) StartBackgroundLoops() {
	if s.ctrl != nil {
		go s.ctrl.Run()
	}
}

func (s *SessionAwareScheduler) GetName() string {
	return s.name
}

func (s *SessionAwareScheduler) Close() {
	if s.ctrl != nil {
		s.ctrl.Stop()
	}
	s.running.Store(false)
	s.closeOnce.Do(func() {
		s.mu.Lock()
		if s.countedRole != "" {
			dropAgenticBalancerRole(s.countedRole)
			s.countedRole = ""
		}
		s.mu.Unlock()
		if s.closeChan != nil {
			close(s.closeChan)
		}
	})
}

// NeedStatsList declares the worker metrics this scheduler consumes; it requires vLLM
// >= 0.11, since a missing kv_cache_usage_perc reads as 0 KV load. The monitor client
// intersects the list with the prometheus config, so MaxNumSeqs is filtered out.
func (s *SessionAwareScheduler) NeedStatsList() []string {
	return []string{
		constant.MetricNumRequestsRunning,
		constant.MetricNumRequestsWaiting,
		constant.MetricKVCacheUsagePerc,
		constant.MaxNumSeqs,
	}
}

// defaultUpdateIntervalMs is the worker-state refresh interval (1s).
const defaultUpdateIntervalMs int64 = 1000

// GetUpdateInterval reports how often the instance manager should refresh worker
// stats. The manager uses the smallest positive value across bound schedulers, so
// <=0 (no periodic refresh) is excluded from that minimum.
func (s *SessionAwareScheduler) GetUpdateInterval() int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.updateIntervalMs
}

// targetRoleConfig reads targetRole under the config lock: UpdateConfig rewrites it, so
// the dispatch path must never touch the field directly.
func (s *SessionAwareScheduler) targetRoleConfig() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.targetRole
}

// dispatchTimeoutConfig reads dispatchTimeout under the config lock, same rule as the role.
func (s *SessionAwareScheduler) dispatchTimeoutConfig() time.Duration {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.dispatchTimeout
}

func (s *SessionAwareScheduler) UpdateCluster(available, unavailable map[string]instance_metrics.WorkerStats) error {
	if !s.running.Load() {
		return fmt.Errorf("agentic-balancer scheduler is not running")
	}
	sm := state.GetGlobalStateManager()
	if sm == nil {
		return fmt.Errorf("state manager not initialized")
	}
	sm.Workers.UpdateFromStats(available, unavailable)
	// Worker stats, health or membership changed: capacity may have freed or
	// appeared, and draining is cheap when nothing is queued.
	s.wakeController()
	return nil
}

func (s *SessionAwareScheduler) UpdateConfig(config map[string]string) error {
	if config == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	old := scoreWeights{s.affinityWeight, s.overloadUtil, s.affinityHalfLife}
	oldAgingRate, oldQueueSize := s.agingRate, s.maxQueueSize
	oldHistory, oldSessionTTL := s.maxWorkerHistory, s.sessionTTL
	s.applyConfig(config)
	// Object-owned knobs are applied through their setters; writing only the field would be
	// silently ignored. Lock order: s.mu → pq.mu/session locks/TTL lock, never the reverse.
	if s.pq != nil {
		if s.agingRate != oldAgingRate {
			s.pq.UpdateAgingRate(s.agingRate)
		}
		if s.maxQueueSize != oldQueueSize {
			s.pq.SetCapacity(s.maxQueueSize)
			s.warnQueueOverCapacity()
		}
	}
	if sm := state.GetGlobalStateManager(); sm != nil {
		if s.maxWorkerHistory != oldHistory {
			sm.Sessions.SetMaxWorkerHistory(s.maxWorkerHistory)
		}
		if s.sessionTTL != oldSessionTTL {
			sm.TTL.SetSessionTTL(s.sessionTTL)
		}
	}
	// A config change may restore schedulable capacity, so refresh the controller's
	// capacity parameters before waking it; the next drain must see the new threshold.
	changed := old != (scoreWeights{s.affinityWeight, s.overloadUtil, s.affinityHalfLife}) || s.agingRate != oldAgingRate
	if s.ctrl != nil {
		s.ctrl.UpdateCapacityParams(s.overloadUtil, s.targetRole)
	}
	logger.Log().Infof("agentic-balancer config updated, old=%+v oldAgingRate=%.4f, new config=%+v", old, oldAgingRate, config)
	if changed {
		s.wakeController()
	}
	return nil
}

// warnQueueOverCapacity reports a shrink that leaves more requests queued than the limit.
func (s *SessionAwareScheduler) warnQueueOverCapacity() {
	if depth := s.pq.Len(); depth > s.maxQueueSize {
		logger.Log().Warnf("agentic-balancer: max-queue-size set to %d while %d requests are queued; "+
			"queued requests are kept and new arrivals are rejected until the queue drains below %d",
			s.maxQueueSize, depth, s.maxQueueSize)
	}
}

// DispatchRequest decides under the decision lock whether the request is dispatched
// directly (admit, score, reserve) or queued for the controller. A reservation is
// the dispatch promise: nothing observable exists between admission and committing it.
func (s *SessionAwareScheduler) DispatchRequest(
	request utils.GenerateRequest, params map[string]string, insBlackMap map[string]interface{},
) (string, bool, error) {
	if !s.running.Load() {
		return "", false, fmt.Errorf("agentic-balancer scheduler is not running")
	}
	sm := state.GetGlobalStateManager()
	if sm == nil {
		return "", false, fmt.Errorf("state manager unavailable, fallback needed")
	}
	if request == nil {
		return "", false, fmt.Errorf("nil request")
	}

	resultChan := make(chan queue.DispatchResult, 1)
	role := s.targetRoleConfig()

	sm.DecisionMu.Lock()
	candidates, skipped := filterBlacklisted(sm.Workers.ListAvailableByRole(role), insBlackMap)
	if len(candidates) == 0 {
		sm.DecisionMu.Unlock()
		return "", skipped, fmt.Errorf("no available %s worker for request %s", role, request.GetRequestId())
	}

	// The queue gate is the heap length: entries there are exactly the requests
	// still waiting for a reservation. While the gate is closed, capacity stays
	// reserved for the backlog, so a new request never bypasses the queue.
	queueing := s.pq != nil && s.pq.Len() > 0
	if !queueing {
		// Session bookkeeping belongs to the dispatch attempt, not to arrival:
		// a queued request touches its session only when the controller hands it
		// off, so a request that never dispatches cannot mark the session in-flight.
		sessionId, priority, _ := utils.ExtractSchedulingMetadata(request)
		var sessEntry *state.SessionEntry
		if sessionId != "" {
			sessEntry = sm.Sessions.GetOrCreate(sessionId)
			sessEntry.SetPriority(priority)
			if sessEntry.GetPhase() == state.PhaseActing {
				sessEntry.TransitionToReasoning()
			}
			sm.TTL.TouchSession(sessionId)
		}
		bestId, err := s.selectWorkerLocked(sm, request, sessEntry, candidates)
		if err == nil {
			sm.DecisionMu.Unlock()
			return bestId, skipped, nil
		}
		if !errors.Is(err, ErrNoSchedulableWorker) {
			sm.DecisionMu.Unlock()
			return "", skipped, err
		}
		// No schedulable worker left: fall through to the queue in the same
		// critical section, so admission and push stay atomic.
	}
	if s.pq == nil {
		sm.DecisionMu.Unlock()
		return "", skipped, fmt.Errorf("priority queue not initialized")
	}

	reason := "overload"
	if queueing {
		reason = "queue-gate"
	}
	qEntry := queue.NewQueueEntry(request, params, insBlackMap, resultChan)
	pushErr := s.pq.Push(qEntry)
	if pushErr != nil && !errors.Is(pushErr, queue.ErrQueueFull) {
		sm.DecisionMu.Unlock()
		return "", skipped, pushErr
	}
	if pushErr != nil {
		// Queue full: free a slot by handing off one queued entry, then retry the
		// push. The handed-off entry completes its transition even if the newcomer
		// is still rejected, and the newcomer never bypasses the queue.
		if s.drainRoundLocked(sm, role) {
			pushErr = s.pq.Push(qEntry)
		}
		if pushErr != nil {
			sm.DecisionMu.Unlock()
			return "", skipped, pushErr // still ErrQueueFull
		}
		reason = "queue-full-made-room"
	}
	logger.Log().Infof("agentic-balancer: request %s enqueued (%s, queueLen=%d, candidates=%d)",
		request.GetRequestId(), reason, s.pq.Len(), len(candidates))
	sm.DecisionMu.Unlock()
	// An armed queue must be visible to the controller immediately; the periodic
	// tick stays as the fallback.
	s.wakeController()
	return s.waitForQueued(sm, request, qEntry, resultChan, skipped)
}

// wakeController pushes a non-blocking, coalescing wake token; ctrl is nil before
// Init, after Close, and in tests that wire the queue directly.
func (s *SessionAwareScheduler) wakeController() {
	if s.ctrl != nil {
		s.ctrl.Wake()
	}
}

// deferredEntry is a queued entry the round could not hand off, plus whether its
// aging clock must restart (only a real dispatch failure resets it).
type deferredEntry struct {
	entry      *queue.QueueEntry
	resetClock bool
}

// drainRoundLocked tries queued entries in heap-priority order until one is
// handed off or each has been tried once. Callers hold sm.DecisionMu (order:
// DecisionMu → pq.mu); returns whether one entry was dispatched.
func (s *SessionAwareScheduler) drainRoundLocked(sm *state.StateManager, role string) bool {
	// Candidates are fixed for the round; only their load moves, and this
	// round's own reservations are visible to later entries through InflightTable.
	candidates := sm.Workers.ListAvailableByRole(role)
	if len(candidates) == 0 {
		return false
	}

	deferred := make([]deferredEntry, 0, s.pq.Len())
	dispatched := false
	for attempts := s.pq.Len(); attempts > 0 && !dispatched; attempts-- {
		entry := s.pq.Dequeue()
		if entry == nil {
			break
		}
		ok, err := s.tryHandoffLocked(sm, entry, candidates)
		switch {
		case ok:
			logger.Log().Infof("agentic-balancer: queue round handed off req %s (priority=%d, waited=%s)",
				entry.Request.GetRequestId(), entry.Priority,
				time.Since(entry.EnqueueAt).Round(time.Millisecond))
			dispatched = true
		case err == nil:
			// Waiter abandoned the entry between pop and attempt; its
			// reservation is already settled, so drop it without re-insert.
			logger.Log().Warnf("agentic-balancer: queue round dropped abandoned req %s",
				entry.Request.GetRequestId())
		case errors.Is(err, ErrNoSchedulableWorker):
			deferred = append(deferred, deferredEntry{entry: entry})
		default:
			logger.Log().Warnf("agentic-balancer: queued request %s dispatch failed: %v",
				entry.Request.GetRequestId(), err)
			deferred = append(deferred, deferredEntry{entry: entry, resetClock: true})
		}
	}
	for _, d := range deferred {
		if d.resetClock {
			_ = s.pq.ReEnqueue(d.entry)
		} else {
			_ = s.pq.ReEnqueueKeepingTime(d.entry)
		}
	}
	return dispatched
}

// drainRound is the controller-facing drain step: take the decision lock and run
// one hand-off round.
func (s *SessionAwareScheduler) drainRound() bool {
	sm := state.GetGlobalStateManager()
	if sm == nil {
		return false
	}
	role := s.targetRoleConfig()
	sm.DecisionMu.Lock()
	defer sm.DecisionMu.Unlock()
	if s.pq == nil {
		return false
	}
	return s.drainRoundLocked(sm, role)
}

// waitForQueued blocks until the queued request is handed a worker, the client cancels,
// the timeout expires or the scheduler closes; every abandon path finalizes reservation
// ownership. A result arriving as the timer fires is adopted, a cancel never is.
func (s *SessionAwareScheduler) waitForQueued(
	sm *state.StateManager, request utils.GenerateRequest,
	qEntry *queue.QueueEntry, resultChan chan queue.DispatchResult, skipped bool,
) (string, bool, error) {
	// ctxDone stays nil without a schedule context (tests, non-carrier requests);
	// a nil channel never fires, so that select case is skipped.
	var ctxDone <-chan struct{}
	if ctx := utils.ExtractScheduleContext(request); ctx != nil {
		ctxDone = ctx.Done()
	}
	timeout := s.dispatchTimeoutConfig()

	select {
	case r := <-resultChan:
		if r.Err != nil {
			return "", skipped, r.Err
		}
		logger.Log().Infof("agentic-balancer: request %s dequeued → worker %s (waited=%s)",
			request.GetRequestId(), r.WorkerId, time.Since(qEntry.EnqueueAt).Round(time.Millisecond))
		return r.WorkerId, skipped, nil
	case <-ctxDone:
		s.finalizeAbandon(sm, request, qEntry, resultChan, false)
		logger.Log().Warnf("agentic-balancer: request %s cancelled while queued", request.GetRequestId())
		return "", skipped, errs.NewRSMaError(&errs.RequestCancelledByUserErr, "agentic-balancer", errs.FailureTypeCanceled)
	case <-time.After(timeout):
		if id, r, adopted := s.finalizeAbandon(sm, request, qEntry, resultChan, true); adopted {
			if r != nil && r.Err != nil {
				return "", skipped, r.Err
			}
			return id, skipped, nil
		}
		logger.Log().Warnf("agentic-balancer: request %s dispatch wait timed out after %s",
			request.GetRequestId(), timeout)
		return "", skipped, errs.NewRSMaError(&errs.GatewayTimeoutErr, "agentic-balancer", errs.FailureTypeTimeout)
	case <-s.closeChan:
		s.finalizeAbandon(sm, request, qEntry, resultChan, false)
		logger.Log().Warnf("agentic-balancer: request %s aborted, scheduler closing", request.GetRequestId())
		return "", skipped, errs.NewRSMaError(&errs.RequestForcedCancelledErr, "agentic-balancer", errs.FailureTypeCanceled)
	}
}

// finalizeAbandon settles the entry's ownership under the decision lock and reports
// whether a just-arrived result was adopted. QUEUED goes back out of the heap with
// nothing reserved; HANDED releases the reservation when the pipeline will never run.
func (s *SessionAwareScheduler) finalizeAbandon(
	sm *state.StateManager, request utils.GenerateRequest, qEntry *queue.QueueEntry,
	resultChan chan queue.DispatchResult, allowAdopt bool,
) (workerId string, res *queue.DispatchResult, adopted bool) {
	sm.DecisionMu.Lock()
	defer sm.DecisionMu.Unlock()

	switch qEntry.State {
	case queue.StateQueued:
		s.pq.Remove(request.GetRequestId())
	case queue.StateReserved:
		// Unreachable while reserve and hand-off share one lock hold; kept for the
		// split-commit protocol should the two commits ever be separated.
		sm.Inflight.Remove(request.GetRequestId())
		qEntry.State = queue.StateAbandoned
		// The returned reservation may open capacity; wake is lock-free.
		s.wakeController()
	case queue.StateHanded:
		select {
		case r := <-resultChan:
			if r.Err == nil && allowAdopt && !requestCancelled(request) {
				return r.WorkerId, &r, true
			}
			if r.Err == nil {
				// Result taken but never dispatched (client gone, scheduler closing,
				// adoption refused): the pipeline will not run, so release the
				// reservation here exactly once and leave no ghost load.
				sm.Inflight.Remove(request.GetRequestId())
				s.wakeController()
			}
			return "", &r, false
		default:
			// HANDED with an empty channel cannot occur: the send precedes the
			// transition under the same lock discipline. Defensive no-op.
		}
	}
	return "", nil, false
}

// requestCancelled reports whether the client HTTP context is already done; a
// request without context (tests, non-carrier) counts as not cancelled.
func requestCancelled(request utils.GenerateRequest) bool {
	ctx := utils.ExtractScheduleContext(request)
	if ctx == nil {
		return false
	}
	select {
	case <-ctx.Done():
		return true
	default:
		return false
	}
}

// schedulableSubset returns the workers below the admission threshold together with
// their load, both from one util snapshot per call.
func (s *SessionAwareScheduler) schedulableSubset(sm *state.StateManager, candidates []*state.WorkerState, threshold float64, utilFn func(*state.StateManager, *state.WorkerState) float64) ([]*state.WorkerState, map[string]float64) {
	usable := make([]*state.WorkerState, 0, len(candidates))
	loadsByWorker := make(map[string]float64, len(candidates))
	for _, w := range candidates {
		u := utilFn(sm, w)
		loadsByWorker[w.WorkerId] = u
		if u < threshold {
			usable = append(usable, w)
		}
	}
	return usable, loadsByWorker
}

// ErrNoSchedulableWorker marks a transient capacity miss: the direct path queues
// instead of failing, and the drain skips the entry without resetting its aging clock.
var ErrNoSchedulableWorker = errors.New("no schedulable worker below overload threshold")

// selectWorkerLocked admits and scores the candidates and commits the inflight
// reservation inside the caller's decision-critical section. Callers hold sm.DecisionMu.
func (s *SessionAwareScheduler) selectWorkerLocked(
	sm *state.StateManager, request utils.GenerateRequest, entry *state.SessionEntry,
	candidates []*state.WorkerState,
) (string, error) {
	return s.selectWorker(sm, request, entry, candidates, true)
}

// selectWorker scores the candidates and, when commit is true, reserves the winner's
// inflight slot under the same decision lock. The config snapshot is taken once so
// admission and gating use one consistent threshold and weight.
func (s *SessionAwareScheduler) selectWorker(
	sm *state.StateManager, request utils.GenerateRequest, entry *state.SessionEntry,
	candidates []*state.WorkerState, commit bool,
) (string, error) {
	return s.selectWorkerWithUtil(sm, state.ComputeWorkerUtil, request, entry, candidates, commit)
}

// selectWorkerWithUtil is the seam used by tests to inject fixed worker
// utils; production always goes through selectWorker.
func (s *SessionAwareScheduler) selectWorkerWithUtil(
	sm *state.StateManager, utilFn func(*state.StateManager, *state.WorkerState) float64,
	request utils.GenerateRequest, entry *state.SessionEntry,
	candidates []*state.WorkerState, commit bool,
) (string, error) {
	s.mu.RLock()
	weights := scoreWeights{
		affinityWeight:   s.affinityWeight,
		overloadUtil:     s.overloadUtil,
		affinityHalfLife: s.affinityHalfLife,
	}
	role := s.targetRole
	s.mu.RUnlock()

	if len(candidates) == 0 {
		return "", fmt.Errorf("no available %s worker for request %s", role, request.GetRequestId())
	}

	// Single-worker admission: a worker at or above the threshold never enters
	// scoring, even with the best affinity.
	usable, loadsByWorker := s.schedulableSubset(sm, candidates, weights.overloadUtil, utilFn)
	if len(usable) == 0 {
		return "", ErrNoSchedulableWorker
	}

	sessionId, _, outputLength := utils.ExtractSchedulingMetadata(request)

	cacheDecay := computeAffinityDecay(entry, weights.affinityHalfLife)
	// Signed score: bestScore starts at -Inf so negative scores are selectable.
	bestId, bestScore := "", math.Inf(-1)
	var bestLoad, bestAff float64
	tieCount := 0
	for _, w := range usable {
		load := loadsByWorker[w.WorkerId]
		aff := s.computeSessionAffinityScore(entry, w.WorkerId, cacheDecay)
		gate := capacityGate(load, weights.overloadUtil)
		score := scoreCandidate(load, aff, weights)
		logger.Log().Debugf("agentic-balancer: candidate worker=%s (session=%s, score=%.4f, load=%.4f, aff=%.4f, gate=%.4f, cacheDecay=%.4f)",
			w.WorkerId, sessionId, score, load, aff, gate, cacheDecay)
		if score > bestScore+scoreEpsilon {
			bestId, bestScore = w.WorkerId, score
			bestLoad, bestAff = load, aff
			tieCount = 1
		} else if math.Abs(score-bestScore) <= scoreEpsilon {
			tieCount++
			if rand.Float64() < 1.0/float64(tieCount) {
				bestId, bestLoad, bestAff = w.WorkerId, load, aff
				bestScore = score
			}
		}
	}
	if bestId == "" {
		return "", fmt.Errorf("no best worker selected for request %s", request.GetRequestId())
	}

	if commit {
		// Reserve exactly once per successful selection; the later RequestNew event
		// only records the target worker and never re-adds load.
		sm.Inflight.Add(&state.InflightRequest{
			RequestId:       request.GetRequestId(),
			WorkerId:        bestId,
			SessionId:       sessionId,
			StartTime:       time.Now(),
			EstimatedTokens: int64(outputLength),
		})
	}

	logger.Log().Infof("agentic-balancer: dispatch req=%s → worker=%s (session=%s, score=%.4f, load=%.4f, aff=%.4f, cacheDecay=%.4f, candidates=%d)",
		request.GetRequestId(), bestId, sessionId, bestScore, bestLoad, bestAff, cacheDecay, len(usable))
	return bestId, nil
}

// tryHandoffLocked admits, scores and commits the reservation plus the HANDED hand-off
// for one popped entry in a single decision-critical section. The caller holds
// sm.DecisionMu, owns the entry outside the heap and re-inserts it on any non-success.
func (s *SessionAwareScheduler) tryHandoffLocked(
	sm *state.StateManager, qEntry *queue.QueueEntry, candidates []*state.WorkerState,
) (bool, error) {
	if qEntry.State == queue.StateAbandoned {
		// Waiter already finalized between pop and this attempt: hand off
		// nothing, caller drops the entry.
		return false, nil
	}
	eligible, _ := filterBlacklisted(candidates, qEntry.BlackMap)
	if len(eligible) == 0 {
		// This entry's blacklist covers the whole pool (or it shrank): an
		// eligibility miss, not a dispatch failure, so keep its aging clock.
		return false, ErrNoSchedulableWorker
	}
	sessionId, priority, _ := utils.ExtractSchedulingMetadata(qEntry.Request)
	var sessEntry *state.SessionEntry
	if sessionId != "" {
		sessEntry = sm.Sessions.GetOrCreate(sessionId)
		sessEntry.SetPriority(priority)
		if sessEntry.GetPhase() == state.PhaseActing {
			sessEntry.TransitionToReasoning()
		}
		sm.TTL.TouchSession(sessionId)
	}
	workerId, err := s.selectWorkerLocked(sm, qEntry.Request, sessEntry, eligible)
	if err != nil {
		return false, err
	}
	// Commit the hand-off: reserve (inside selectWorkerLocked) then signal.
	// ResultChan is buffered(1) and only written here, so the send never
	// blocks while we hold the decision lock.
	if qEntry.ResultChan != nil {
		qEntry.ResultChan <- queue.DispatchResult{WorkerId: workerId}
	}
	qEntry.State = queue.StateHanded
	return true, nil
}

// computeAffinityDecay returns 2^(-elapsed/H), where elapsed is the session's ACTING
// time and H the affinity half-life: the longer a session acts, the more likely its
// KV cache is gone. Sessions without ActingSince decay not at all (1.0).
func computeAffinityDecay(entry *state.SessionEntry, halfLife time.Duration) float64 {
	if entry == nil {
		return 1.0
	}
	if halfLife <= 0 {
		return 1.0
	}
	actingSince := entry.GetActingSince()
	if actingSince.IsZero() {
		return 1.0
	}
	elapsed := time.Since(actingSince).Seconds()
	if elapsed <= 0 {
		return 1.0
	}
	return math.Pow(0.5, elapsed/halfLife.Seconds())
}

// capacityGate is the remaining-capacity factor of the affinity term; it reaches
// zero at the overload threshold.
func capacityGate(load, overloadUtil float64) float64 {
	return math.Max(0, 1-load/overloadUtil)
}

// scoreCandidate ranks one schedulable worker: load dominates, while affinity
// only counts for the capacity the worker still has left.
func scoreCandidate(load, aff float64, weights scoreWeights) float64 {
	return -load + weights.affinityWeight*aff*capacityGate(load, weights.overloadUtil)
}

// Affinity = 2^-idx · decay: a history entry is discounted by half per position and
// every entry decays with the session's acting time. Load reaches the score only
// through admission and the capacity gate.
func (s *SessionAwareScheduler) computeSessionAffinityScore(entry *state.SessionEntry, workerId string, cacheDecay float64) float64 {
	if entry == nil {
		return 0
	}

	idx := entry.WorkerHistoryIndex(workerId)
	if idx < 0 {
		return 0
	}
	// 0.5^idx · timeFactor(cacheDecay) — applied to all history entries.
	return math.Pow(0.5, float64(idx)) * cacheDecay
}

// OnRequestEvent keeps inflight and session state across the request lifecycle.
// RequestNew records the assigned worker; any other event releases the request and
// wakes the controller so the freed capacity reaches the queue.
func (s *SessionAwareScheduler) OnRequestEvent(event *utils.RequestEventMsg) {
	if event == nil || event.Request == nil {
		return
	}
	sm := state.GetGlobalStateManager()
	if sm == nil {
		return
	}
	reqId := event.Request.GetRequestId()

	if event.EventType == constant.RequestNew {
		if event.TargetInstance != "" {
			if sid := event.Request.GetSessionId(); sid != "" {
				if e := sm.Sessions.Get(sid); e != nil {
					e.SetAssignedWorker(event.TargetInstance)
				}
			}
		}
	} else {
		inflightEntry := sm.Inflight.Get(reqId)
		sm.Inflight.Remove(reqId)

		if inflightEntry != nil && (event.EventType == constant.RequestPrefillSuccess || event.EventType == constant.RequestPrefillFailed) {
			duration := time.Since(inflightEntry.StartTime).Round(time.Millisecond)
			logger.Log().Infof("agentic-balancer: request %s completed on %s (eventType=%d, duration=%s, session=%s)",
				reqId, inflightEntry.WorkerId, event.EventType, duration, inflightEntry.SessionId)
		}

		// A released reservation may have opened capacity for the armed queue.
		if inflightEntry != nil {
			s.wakeController()
		}

		if sid := event.Request.GetSessionId(); sid != "" {
			if e := sm.Sessions.Get(sid); e != nil {
				if e.GetPhase() == state.PhaseReasoning {
					e.TransitionToActing()
				}
			}
		}
	}

	if event.EventType == constant.RequestPrefillSuccess && event.TargetInstance != "" {
		if sid := event.Request.GetSessionId(); sid != "" {
			if e := sm.Sessions.Get(sid); e != nil {
				e.SetAssignedWorker(event.TargetInstance)
			}
		}
	}
}

// OnBatchEvent is deliberately absent: agentic-balancer schedules on per-request
// prefill load and session affinity, so batch details go unused.

// ClearLoad drops inflight tracking. Session state is persistent and is only
// reaped by the TTL manager / controller, so it is left intact here.
func (s *SessionAwareScheduler) ClearLoad() {
	sm := state.GetGlobalStateManager()
	if sm == nil {
		return
	}
	logger.Log().Debugf("agentic-balancer clearing inflight load tracking")
	sm.Inflight.Clear()
}

func (s *SessionAwareScheduler) ReportLoads(includeRequests bool, includeUnavailable bool) *scheduler.AllInstanceLoads {
	sm := state.GetGlobalStateManager()
	res := &scheduler.AllInstanceLoads{}
	if sm == nil {
		return res
	}

	all := sm.Workers.ListAll()
	available := make([]*scheduler.InstanceLoadInfo, 0)
	unavailable := make([]*scheduler.InstanceLoadInfo, 0)
	for _, w := range all {
		loadVal := state.ComputeWorkerUtil(sm, w)
		info := &scheduler.InstanceLoadInfo{
			Id:      w.WorkerId,
			LoadVal: loadVal,
			Desc: fmt.Sprintf("inflight=%d pending=%d maxSeq=%d kvUsage=%.3f role=%s available=%v",
				w.InFlight, w.Pending, w.MaxNumSeqs, w.KVCache.UsagePerc, w.Role, w.Available),
		}
		if includeRequests {
			reqs := make(map[string]*scheduler.RequestLoadInfo)
			for _, r := range sm.Inflight.GetByWorker(w.WorkerId) {
				reqs[r.RequestId] = &scheduler.RequestLoadInfo{
					LoadVal: 1,
					// The load API shares the service port and has no extra auth.
					// A full session id would let a caller reuse another user's affinity.
					Desc: fmt.Sprintf("session=%s estTokens=%d", maskSessionID(r.SessionId), r.EstimatedTokens),
				}
			}
			info.Requests = reqs
		}
		if w.Available {
			available = append(available, info)
		} else if includeUnavailable {
			unavailable = append(unavailable, info)
		}
	}
	res.Available = available
	res.Unavailable = unavailable
	return res
}

// filterBlacklisted removes candidates whose WorkerId is in insBlackMap.
// The returned bool reports whether any candidate was skipped due to the
// blacklist, satisfying DispatchRequest's "skipped" contract.
func filterBlacklisted(candidates []*state.WorkerState, insBlackMap map[string]interface{}) ([]*state.WorkerState, bool) {
	if len(insBlackMap) == 0 {
		return candidates, false
	}
	result := make([]*state.WorkerState, 0, len(candidates))
	skipped := false
	for _, c := range candidates {
		if _, blocked := insBlackMap[c.WorkerId]; blocked {
			skipped = true
			continue
		}
		result = append(result, c)
	}
	return result, skipped
}
