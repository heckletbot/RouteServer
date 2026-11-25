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

package conductor

import (
	"context"
	"fmt"
	"math/rand/v2"
	"sort"
	"sync"
	"time"

	"route-server/pkg/common/cluster"
	"route-server/pkg/common/config"
	"route-server/pkg/common/logger"
	"route-server/pkg/common/utils"
)

const (
	// ApplierName RegistrationManager 在 ClusterService 中注册的稳定标识，
	// 由 app.go 装配时消费，出现在集群状态机日志里，改动会影响运维排查。
	ApplierName = "ConductorRegistrationManager"
)

const (
	// transientBaseBackoff transient 错误首次失败后的等待时长。
	transientBaseBackoff = time.Second
	// maxTransientBackoff transient 退避的硬上限，与 retry-interval 取较小值。
	maxTransientBackoff = 30 * time.Second
	// maxBackoffShift 指数退避的最大位移次数，防止失败次数过大时溢出。
	maxBackoffShift = 16
	// backoffJitterRatio 退避抖动比例。
	backoffJitterRatio = 0.2
	// idleWakeInterval 无待重试任务时 timer 的兜底周期。取一个足够长的值让 worker
	// 实质阻塞，同时避免"timer 为 nil"的分支；顺带兜住万一丢失的唤醒信号。
	idleWakeInterval = time.Hour
)

// ModelNameResolver 查询推理实例当前加载的模型名。
// 刻意只暴露这一个方法：conductor 包不能依赖 instances/instance-client，
// 由 app.go 侧的 adapter 把 InstanceManager 的能力收窄成本接口。
type ModelNameResolver interface {
	ResolveModelName(instanceID string) (string, error)
}

// retryState 单个操作的失败退避状态。class 决定退避档位：
// transient 快速指数退避，其余按固定 retry-interval 低频重试。
type retryState struct {
	failures  int
	class     ErrorClass
	nextRetry time.Time
}

// desiredEntry 一个 Prefill 实例的期望注册状态。
// generation 随 endpoint 变化递增，异步结果写回前用它判断是否已过期。
// 刻意不保存"最近一次成功 apply 的 epoch/generation"：dirty 已经完整表达了
// 是否需要 apply，多存一份只写不读的坐标反而会让状态机多出无人消费的分支。
type desiredEntry struct {
	instanceID string
	ip         string
	port       int
	generation uint64
	modelName  string
	dirty      bool
	conflict   bool
	retry      retryState
}

// tombstone 待执行的注销任务，同时保存被删 entry 的快照，
// 用于区分"相同 route 抖动回归"（撤销注销）与"换了 endpoint 的替换"（保留注销）。
type tombstone struct {
	key ServiceKey
	// lastIP/lastPort 被删时的 endpoint，用于判断回归的是否同一个实例进程。
	lastIP   string
	lastPort int
	// lastGeneration 被删时的 generation。替换时新 entry 从它之上继续递增，
	// 保证在途的旧结果一定被 generation 校验挡掉。
	lastGeneration uint64
	// lastModelName 被删时已缓存的模型名。撤销注销时恢复，省一次 /v1/models。
	lastModelName string
	retry         retryState
}

// RegistrationManager 维护 Prefill 注册的期望状态，并在本副本为 leader 时收敛到 Conductor。
// 它实现 cluster.StateApplier：所有副本都处理回调并维护 desired，只有 leader 执行写操作。
type RegistrationManager struct {
	cfg      Config
	client   Client
	resolver ModelNameResolver

	// mu 保护以下所有字段。回调、worker 和外部查询共用它，
	// 且网络调用一律在锁外执行（设计文档 §13-14）。
	mu          sync.Mutex
	desired     map[string]*desiredEntry
	pending     map[string]*tombstone
	isLeader    bool
	leaderEpoch uint64
	leaderCtx   context.Context
	cancelEpoch context.CancelFunc

	// wake 容量 1，用于合并突发事件：已有待处理信号时新信号直接丢弃。
	wake    chan struct{}
	rootCtx context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup

	started   bool
	closed    bool
	closeOnce sync.Once

	// applyRounds applyOnce 的执行轮次，只被测试读取。
	// 空转缺陷（worker 满速循环但不发请求）在外部完全不可观测：
	// 它不改 desired、不发 HTTP，唯一的痕迹就是轮次疯涨，所以这个计数必须在生产代码里。
	applyRounds uint64

	// nowFn 便于测试注入时间，生产恒为 time.Now。
	nowFn func() time.Time
}

// NewRegistrationManager 构造 manager。cfg.Enabled 为 false 时仍可构造，
// 但所有 apply 都会被 applyOnce 的门禁挡住。
func NewRegistrationManager(cfg Config, client Client, resolver ModelNameResolver) (*RegistrationManager, error) {
	if client == nil {
		return nil, fmt.Errorf("conductor client must not be nil")
	}
	if resolver == nil {
		return nil, fmt.Errorf("conductor model name resolver must not be nil")
	}
	// 只在启用时校验：disabled 时 NormalizeConfig 返回零值 Config，此时该字段无意义。
	// 启用时挡住 0 是为了不让「绕过 NormalizeConfig 直接构造 Config」的调用方注册出
	// block_size=0 —— 那会让前缀切分全错而两侧都不报错，命中恒为 0。
	if cfg.Enabled && cfg.BlockSize <= 0 {
		return nil, fmt.Errorf("conductor block size must be positive, got %d", cfg.BlockSize)
	}

	rootCtx, cancel := context.WithCancel(context.Background())
	return &RegistrationManager{
		cfg:      cfg,
		client:   client,
		resolver: resolver,
		desired:  make(map[string]*desiredEntry),
		pending:  make(map[string]*tombstone),
		// isLeader 显式保持零值 false：cluster.StateApplier 约定服务启动后若一直无
		// leader 则 OnLeaderChanged 不会被调用，初始值错成 true 会让 follower 写 Conductor。
		wake:    make(chan struct{}, 1),
		rootCtx: rootCtx,
		cancel:  cancel,
		nowFn:   time.Now,
	}, nil
}

// Start 启动后台 worker。重复调用或 Close 之后调用都无副作用。
func (m *RegistrationManager) Start() {
	m.mu.Lock()
	if m.started || m.closed {
		m.mu.Unlock()
		return
	}
	m.started = true
	m.mu.Unlock()

	m.wg.Add(1)
	go m.run()
	// 打印 block_size 和注册角色：block_size 配错时全链路不报错、只是命中恒为 0，
	// 启动日志是唯一的事前核对点（须与推理实例日志里的实际值一致，混合注意力模型
	// 不等于命令行参数）。注册角色则决定哪些实例会出现在 Conductor 的索引里。
	logger.Log().Infof("Conductor registration manager started, enabled=%v, roles=%v, block_size=%d",
		m.cfg.Enabled, m.cfg.Roles(), m.cfg.BlockSize)
}

// IsLeader 返回本副本当前是否为 leader。
func (m *RegistrationManager) IsLeader() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.isLeader
}

// BlockSize 返回注册和查询共用的 KV cache block size。
func (m *RegistrationManager) BlockSize() int {
	return m.cfg.BlockSize
}

// StateApplier interface implementation

// GetName 返回本 applier 的稳定名称。
func (m *RegistrationManager) GetName() string {
	return ApplierName
}

// OnLeaderChanged 处理主备切换。升主递增 epoch 并把全部 desired 重新标记为待 apply，
// 以便新 leader 重新声明当前集群的完整注册集合；降备只取消 epoch，不注销任何实例。
func (m *RegistrationManager) OnLeaderChanged(newLeader *utils.Peer, isMeLeader bool,
	leaderRestoreFailures *utils.AllInstanceFailuresSnapshot) {
	logger.Log().Infof("Conductor registration manager leader change: newLeader=%v, isMeLeader=%v",
		newLeader, isMeLeader)

	m.mu.Lock()
	// 无论升主降备都先取消旧 epoch：旧 leader 的在途请求结果不能写回新状态。
	if m.cancelEpoch != nil {
		m.cancelEpoch()
		m.cancelEpoch = nil
		m.leaderCtx = nil
	}
	m.isLeader = isMeLeader
	if isMeLeader {
		m.leaderEpoch++
		m.leaderCtx, m.cancelEpoch = context.WithCancel(m.rootCtx)
		for _, entry := range m.desired {
			entry.dirty = true
			entry.retry = retryState{}
		}
		logger.Log().Infof("Conductor registration manager became leader, epoch=%d, desired=%d",
			m.leaderEpoch, len(m.desired))
	} else {
		logger.Log().Infof("Conductor registration manager stepped down from leader, keeping %d desired entries",
			len(m.desired))
	}
	m.mu.Unlock()

	if isMeLeader {
		m.notify()
	}
}

// OnStateRestored 以 state 中的 Prefill routes 作为权威全量集合重建 desired。
// state 为 nil 是无效回调，不能据此认为集群已无实例：清空 desired 会注销全部 Prefill。
func (m *RegistrationManager) OnStateRestored(state *cluster.ClusterState) {
	if state == nil {
		// 与 apc.Manager 不同，这里必须判 nil：本 applier 会据此决定是否注销。
		logger.Log().Errorf("Conductor registration manager got nil cluster state, keeping current desired state")
		return
	}
	logger.Log().Infof("Conductor registration manager cluster state restored, version: %d, routes: %d",
		state.Version, len(state.InstanceRoutes))

	m.mu.Lock()
	seen := make(map[string]struct{}, len(state.InstanceRoutes))
	for id, route := range state.InstanceRoutes {
		// InstanceRoutes 的 value 是指针，可能为 nil。
		if id == "" || route == nil || !m.shouldManage(route.Role) {
			continue
		}
		seen[id] = struct{}{}
		m.upsertLocked(id, route.Ip, route.Port)
	}
	// 全量语义：本次快照中消失的已管理实例需要注销。
	for id := range m.desired {
		if _, ok := seen[id]; !ok {
			m.removeLocked(id)
		}
	}
	m.mu.Unlock()

	m.notify()
}

// OnAddPeer 无操作：Conductor 注册只关心推理实例，与 RouteServer 集群成员无关。
func (m *RegistrationManager) OnAddPeer(peer *utils.Peer) {
	logger.Log().Infof("Conductor registration manager peer added: %v", peer)
}

// OnRemovePeer 无操作，理由同 OnAddPeer。
func (m *RegistrationManager) OnRemovePeer(name string) {
	logger.Log().Infof("Conductor registration manager peer removed: %s", name)
}

// OnAddInstance 对配置的角色执行 upsert。角色变成未配置注册的类型时等价于删除：
// 该实例不再需要 Conductor 维护索引，必须摘除。
func (m *RegistrationManager) OnAddInstance(routes map[string]*config.Route, reason string) {
	logger.Log().Infof("Conductor registration manager instance added [%s]: %d routes", reason, len(routes))

	m.mu.Lock()
	for id, route := range routes {
		if id == "" || route == nil {
			continue
		}
		if !m.shouldManage(route.Role) {
			m.removeLocked(id)
			continue
		}
		m.upsertLocked(id, route.Ip, route.Port)
	}
	m.mu.Unlock()

	m.notify()
}

// shouldManage 判断该角色的实例是否归本 manager 管理。
func (m *RegistrationManager) shouldManage(role string) bool {
	return m.cfg.Manages(role)
}

// OnRemoveInstance 只为当前属于已管理实例的 ID 创建 tombstone，
// 避免注销未配置注册的角色或本副本从未管理过的服务（removeLocked 内做归属判断）。
func (m *RegistrationManager) OnRemoveInstance(names []string, reason string) {
	logger.Log().Infof("Conductor registration manager instance removed [%s]: %v", reason, names)

	m.mu.Lock()
	for _, id := range names {
		m.removeLocked(id)
	}
	m.mu.Unlock()

	m.notify()
}

// upsertLocked 新建或更新一个 desired entry，调用方必须持有 m.mu。
func (m *RegistrationManager) upsertLocked(id, ip string, port int) {
	entry, ok := m.desired[id]
	if !ok {
		m.desired[id] = m.reviveLocked(id, ip, port)
		return
	}
	// endpoint 变了说明是另一个实例进程，缓存的 model name 和退避/冲突状态全部作废。
	// endpoint 未变时不动 generation，否则每轮状态恢复都会白白失效模型缓存。
	if entry.ip != ip || entry.port != port {
		entry.ip = ip
		entry.port = port
		entry.generation++
		entry.modelName = ""
		entry.retry = retryState{}
		entry.conflict = false
	}
	entry.dirty = true
}

// reviveLocked 为一个当前不在 desired 中的实例构造 entry，调用方必须持有 m.mu。
// 若存在未兑现的 tombstone，按 endpoint 是否变化决定撤销还是保留它。
func (m *RegistrationManager) reviveLocked(id, ip string, port int) *desiredEntry {
	entry := &desiredEntry{
		instanceID: id,
		ip:         ip,
		port:       port,
		generation: 1,
		// 一律 dirty：撤销 tombstone 时注销请求可能已经在途，
		// Conductor 侧或许已经把 service 删了，必须重新声明一次。
		// 重复注册是幂等的（200 already_registered），代价只有一次请求。
		dirty: true,
	}

	tomb, ok := m.pending[id]
	if !ok {
		return entry
	}
	// 注销已经失败过说明请求到过 Conductor，可能已生效：不能撤销，
	// 先把注销做成功再重新注册，否则可能留下方向不明的中间状态。
	if tomb.retry.failures == 0 && tomb.lastIP == ip && tomb.lastPort == port {
		// 相同 endpoint 且注销尚未发出：同一个实例进程抖动回归，撤销注销。
		delete(m.pending, id)
		entry.generation = tomb.lastGeneration
		entry.modelName = tomb.lastModelName
		return entry
	}
	// 换了 endpoint（或注销已失败过）：tombstone 保留，新 payload 用更大的 generation，
	// 让旧 endpoint 在途的结果一定被 generation 校验挡掉。
	entry.generation = tomb.lastGeneration + 1
	return entry
}

// removeLocked 删除 desired entry 并留下注销 tombstone，调用方必须持有 m.mu。
// 只处理当前在 desired 中的 ID：其余 ID 本副本没注册过，注销会误删别人的服务。
func (m *RegistrationManager) removeLocked(id string) {
	entry, ok := m.desired[id]
	if !ok {
		return
	}
	delete(m.desired, id)

	if existing, ok := m.pending[id]; ok {
		// 已有 tombstone：只刷新快照，保留原有退避，否则反复的增删会让注销永远排在未来。
		existing.lastIP = entry.ip
		existing.lastPort = entry.port
		existing.lastGeneration = entry.generation
		existing.lastModelName = entry.modelName
		return
	}
	m.pending[id] = &tombstone{
		key:            NewServiceKey(id),
		lastIP:         entry.ip,
		lastPort:       entry.port,
		lastGeneration: entry.generation,
		lastModelName:  entry.modelName,
	}
}

// notify 非阻塞唤醒 worker。回调可能在 ClusterService 的状态锁内执行，
// 阻塞发送会连带卡死整个集群状态机。
func (m *RegistrationManager) notify() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

// run worker 主循环，串行 apply，保证同一进程内不并发修改同一 Conductor service key。
// wake 合并突发事件，timer 负责退避到期后的重试：集群静默时（没有实例增删）
// 失败的任务只能靠 timer 恢复，否则会一直停在退避里。
func (m *RegistrationManager) run() {
	defer m.wg.Done()

	timer := time.NewTimer(idleWakeInterval)
	defer timer.Stop()

	for {
		m.applyOnce()

		next := m.nextRetryDelay()
		// Stop 返回 false 说明已经触发过，channel 里可能留着值，不排掉会让下一次
		// select 立即返回。
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		if next > 0 {
			timer.Reset(next)
		} else {
			timer.Reset(idleWakeInterval)
		}

		// 取 next 与重置 timer 之间若有回调改了状态，它一定也发过 wake，
		// select 会立刻返回并重算，不会漏。
		select {
		case <-m.rootCtx.Done():
			return
		case _, ok := <-m.wake: // ← 加 ok 检查
			if !ok {
				// m.wake was closed; treat as idle wake
				continue
			}
			// 正常唤醒, 继续下一轮
		case <-timer.C:
		}
	}
}

// nextRetryDelay 返回距最近一个待重试任务的等待时长；无待重试任务返回 0。
// 门禁与 applyOnce 保持一致：不是 leader 时没有任何任务会被执行，排 timer 也无意义。
func (m *RegistrationManager) nextRetryDelay() time.Duration {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.cfg.Enabled || !m.isLeader {
		return 0
	}

	var earliest time.Time
	consider := func(t time.Time) {
		// 零值表示"现在就可以执行"，而它刚刚在本轮 applyOnce 里被处理过。
		// 不排除的话零值会成为最早时间，把真实的到期时间全盖掉并退化成 1ms 一轮空转。
		if t.IsZero() {
			return
		}
		if earliest.IsZero() || t.Before(earliest) {
			earliest = t
		}
	}
	for _, entry := range m.desired {
		// conflict 的 entry 必须排除：applyOnce 会跳过它且不更新退避，
		// 排进来就会让 timer 反复排到同一个过去时刻，1ms 一轮空转。
		// 被 tombstone 挡住的 entry 不需要单独排除：reviveLocked 建 entry 时
		// retry 一定是零值，走不到这里；它的推进由 tombstone 自己的到期时间驱动。
		if entry.dirty && !entry.conflict {
			consider(entry.retry.nextRetry)
		}
	}
	for _, tomb := range m.pending {
		consider(tomb.retry.nextRetry)
	}

	if earliest.IsZero() {
		return 0
	}
	if delay := earliest.Sub(m.nowFn()); delay > 0 {
		return delay
	}
	// 已经过期：给一个最小正值让下一轮尽快跑，而不是返回 0 被当成"无任务"。
	return time.Millisecond
}

// applyTask 一次注册尝试的输入快照。持锁期间取好，锁外使用：
// worker 拿着它做模型发现和 HTTP，期间 desired 可能已被回调改写。
// generation 随快照带出，写回时用它判断结果是否已过期。
type applyTask struct {
	instanceID string
	ip         string
	generation uint64
	modelName  string
}

// applyOnce 执行一轮收敛：先注销后注册，全部网络调用在锁外。
// 注意：末尾不得调用 notify()，否则容量 1 的 wake channel 会被自己填满，
// run() 每轮立刻返回并再次 applyOnce，goroutine 满速空转（测试不会报失败）。
func (m *RegistrationManager) applyOnce() {
	m.mu.Lock()
	m.applyRounds++
	if !m.cfg.Enabled || !m.isLeader || m.leaderCtx == nil {
		m.mu.Unlock()
		return
	}
	epoch := m.leaderEpoch
	ctx := m.leaderCtx
	now := m.nowFn()

	// 每轮重算：route 变化后冲突可能消失，一次性标记会让实例永久不注册。
	m.markEndpointConflictsLocked()

	tombs := make([]ServiceKey, 0, len(m.pending))
	for _, tomb := range m.pending {
		if now.Before(tomb.retry.nextRetry) {
			continue
		}
		tombs = append(tombs, tomb.key)
	}

	tasks := make([]applyTask, 0, len(m.desired))
	for id, entry := range m.desired {
		if !entry.dirty || entry.conflict {
			continue
		}
		if _, blocked := m.pending[id]; blocked {
			// 同一 service key 上还有未兑现的注销：先注册会被随后的注销删掉。
			continue
		}
		if now.Before(entry.retry.nextRetry) {
			continue
		}
		tasks = append(tasks, applyTask{
			instanceID: id,
			ip:         entry.ip,
			generation: entry.generation,
			modelName:  entry.modelName,
		})
	}
	m.mu.Unlock()

	for _, key := range tombs {
		m.applyUnregister(ctx, epoch, key)
	}
	for _, task := range tasks {
		m.applyRegister(ctx, epoch, task)
	}
}

// markEndpointConflictsLocked 标记推导出相同 KV endpoint 的实例，调用方必须持有 m.mu。
// KV endpoint 只由 IP 推导（端口固定），同 IP 的两个 Prefill 会撞同一个 publisher；
// 此时注册任何一方都会让 Conductor 的索引指向错误的实例，只能双方都跳过并告警。
func (m *RegistrationManager) markEndpointConflictsLocked() {
	counts := make(map[string]int, len(m.desired))
	for _, entry := range m.desired {
		counts[BuildKVEventEndpoint(entry.ip)]++
	}
	for _, entry := range m.desired {
		endpoint := BuildKVEventEndpoint(entry.ip)
		conflict := counts[endpoint] > 1
		// 只在状态翻转时打日志，避免每轮刷屏。
		if conflict && !entry.conflict {
			// 措辞不绑定角色：注册范围可以同时包含 prefill 和 decode，冲突对可能是
			// 同机部署的两个不同角色。
			logger.Log().Errorf("Conductor registration skipped for instance %s: KV event endpoint %s "+
				"is shared with another registered instance", entry.instanceID, endpoint)
		}
		entry.conflict = conflict
	}
}

// applyRegister 解析模型名并注册单个实例。全程不持 m.mu：模型发现要访问实例的
// /v1/models，注册要访问 Conductor，任一慢响应都会连带卡死集群回调。
func (m *RegistrationManager) applyRegister(ctx context.Context, epoch uint64, task applyTask) {
	if ctx.Err() != nil {
		return
	}

	modelName := task.modelName
	if modelName == "" {
		resolved, err := m.resolver.ResolveModelName(task.instanceID)
		if err != nil {
			// 模型发现失败按 transient：几乎总是 listener 顺序造成的启动竞态或临时不可达。
			logger.Log().Warnf("Fail to resolve model name for instance %s: %v", task.instanceID, err)
			m.recordFailure(epoch, task, ClassTransient)
			return
		}
		modelName = resolved
		m.cacheModelName(epoch, task, modelName)
	}

	req := RegisterRequest{
		Endpoint:    BuildKVEventEndpoint(task.ip),
		Type:        InstanceTypeVLLM,
		ModelName:   modelName,
		InstanceID:  task.instanceID,
		BlockSize:   m.cfg.BlockSize,
		DpRank:      DefaultDpRank,
		TenantID:    DefaultTenantID,
		LoraName:    "",
		HashProfile: NewDefaultHashProfile(),
	}
	if err := m.client.Register(ctx, req); err != nil {
		class := m.classifyWithContext(ctx, err)
		logger.Log().Warnf("Fail to register instance %s to conductor (class=%s): %v",
			task.instanceID, class, err)
		m.recordFailure(epoch, task, class)
		return
	}
	m.recordRegisterSuccess(epoch, task)
	logger.Log().Infof("Instance %s registered to conductor, model=%s", task.instanceID, modelName)
	// 完整 endpoint 含内部实例 IP 与 KV 事件端口，收敛到 Debug 避免在常规日志中放大网络拓扑暴露面。
	logger.Log().Debugf("Instance %s KV event endpoint=%s", task.instanceID, req.Endpoint)
}

// applyUnregister 注销单个 service key。client 已把 404 收敛为成功，
// 所以走到 err == nil 就说明 Conductor 侧确实不存在该 service，可以删 tombstone。
func (m *RegistrationManager) applyUnregister(ctx context.Context, epoch uint64, key ServiceKey) {
	if ctx.Err() != nil {
		return
	}
	req := UnregisterRequest{InstanceID: key.InstanceID, DpRank: key.DpRank, TenantID: key.TenantID}
	if err := m.client.Unregister(ctx, req); err != nil {
		class := m.classifyWithContext(ctx, err)
		logger.Log().Warnf("Fail to unregister instance %s from conductor (class=%s): %v",
			key.InstanceID, class, err)
		m.recordUnregisterFailure(epoch, key, class)
		return
	}

	m.mu.Lock()
	if m.leaderEpoch == epoch {
		delete(m.pending, key.InstanceID)
	}
	m.mu.Unlock()
	logger.Log().Infof("Instance %s unregistered from conductor", key.InstanceID)

	// 本轮的注册任务快照是在注销之前取的，被 tombstone 挡住的替换任务不在其中，
	// 需要再来一轮才能发出。这不会变成自触发循环：下一轮除非又注销了别的，
	// 否则不再产生新信号，只多一轮（applyOnce 末尾一律不得发信号，理由见其注释）。
	m.notify()
}

// classifyWithContext 在 ClassOf 之上补一种情形：ctx 在请求返回后才被取消，
// 或底层错误没有包装 context.Canceled。两者互补，都要保留。
func (m *RegistrationManager) classifyWithContext(ctx context.Context, err error) ErrorClass {
	class := ClassOf(err)
	if class != ClassCanceled && ctx.Err() != nil {
		return ClassCanceled
	}
	return class
}

// cacheModelName 按 generation 缓存模型名，避免每轮重复访问 /v1/models。
// epoch 或 generation 已变说明这是过期结果：模型名属于旧的实例进程，不能复用。
func (m *RegistrationManager) cacheModelName(epoch uint64, task applyTask, modelName string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.leaderEpoch != epoch {
		return
	}
	if entry, ok := m.desired[task.instanceID]; ok && entry.generation == task.generation {
		entry.modelName = modelName
	}
}

// recordRegisterSuccess 标记注册已收敛。entry 不存在或 generation 已变时直接丢弃结果：
// 前者说明实例已被删除（tombstone 必须保留，否则残留注册永远不被注销），
// 后者说明 route 已变（新 payload 还没发，不能标记为 clean）。
func (m *RegistrationManager) recordRegisterSuccess(epoch uint64, task applyTask) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.leaderEpoch != epoch {
		return
	}
	entry, ok := m.desired[task.instanceID]
	if !ok || entry.generation != task.generation {
		return
	}
	entry.dirty = false
	entry.retry = retryState{}
}

// recordFailure 记录注册失败并排下次重试。ClassCanceled 不计失败：
// 那是本副本降备或进程退出造成的，不是 Conductor 的问题，重新升主后按新 epoch 重放。
func (m *RegistrationManager) recordFailure(epoch uint64, task applyTask, class ErrorClass) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.leaderEpoch != epoch || class == ClassCanceled {
		return
	}
	entry, ok := m.desired[task.instanceID]
	if !ok || entry.generation != task.generation {
		return
	}
	entry.retry.failures++
	entry.retry.class = class
	entry.retry.nextRetry = m.nowFn().Add(m.backoffFor(class, entry.retry.failures))
}

// recordUnregisterFailure 记录注销失败并排下次重试，tombstone 保留待兑现。
func (m *RegistrationManager) recordUnregisterFailure(epoch uint64, key ServiceKey, class ErrorClass) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.leaderEpoch != epoch || class == ClassCanceled {
		return
	}
	tomb, ok := m.pending[key.InstanceID]
	if !ok {
		return
	}
	tomb.retry.failures++
	tomb.retry.class = class
	tomb.retry.nextRetry = m.nowFn().Add(m.backoffFor(class, tomb.retry.failures))
}

// backoffFor 返回下次重试的等待时长。
// transient（网络抖动、超时、5xx）通常几秒内自愈，所以从 1s 起指数增长，
// 上限 min(30s, retry-interval)：快速重试能缩短索引缺失窗口，指数增长又能在
// 持续故障时收敛请求量。其余档位都需要人工介入（改配置、改部署、排查网关），
// 快速重试只会刷日志压 Conductor，一律按 retry-interval 低频重试。
func (m *RegistrationManager) backoffFor(class ErrorClass, failures int) time.Duration {
	// 绕过 NormalizeConfig 直接构造 Config 时可能为 0，回落到默认周期：
	// 返回 0 会让 nextRetry 等于当前时刻，退避形同不存在。
	interval := m.cfg.RetryInterval
	if interval <= 0 {
		interval = DefaultRetryInterval
	}

	if class != ClassTransient {
		return applyJitter(interval)
	}

	limit := interval
	if limit > maxTransientBackoff {
		limit = maxTransientBackoff
	}
	base := transientBaseBackoff
	if failures > 1 {
		shift := failures - 1
		// 夹住位移次数：不夹的话失败次数一大就溢出成负数或极小值，退避反而消失。
		if shift > maxBackoffShift {
			shift = maxBackoffShift
		}
		base <<= uint(shift)
	}
	// 同时兜住两件事：截断指数增长，以及把初始退避压到 retry-interval 之下——
	// 否则配了 40ms 周期的部署第一次失败仍要等 1s，表现为"配置不生效"。
	if base > limit {
		base = limit
	}
	return applyJitter(base)
}

// applyJitter 给退避加 ±20% 抖动。Conductor 重启这类故障会让全部实例同时失败，
// 固定退避会让它们的重试永远对齐在同一时刻，形成周期性尖峰。
func applyJitter(d time.Duration) time.Duration {
	out := time.Duration(float64(d) * (1 + (rand.Float64()*2-1)*backoffJitterRatio))
	// 亚毫秒级配置下抖动可能截断到 0，那会让下一轮立刻重试。
	if out <= 0 {
		return time.Millisecond
	}
	return out
}

// Close 停止 worker。刻意不发送任何 unregister：RouteServer 进程退出不代表推理实例
// 退出，自动注销会让其他 RouteServer 副本暂时失去 Conductor 索引（设计文档 §15.3）。
func (m *RegistrationManager) Close() {
	m.closeOnce.Do(func() {
		logger.Log().Infof("Closing conductor registration manager...")
		m.mu.Lock()
		m.closed = true
		if m.cancelEpoch != nil {
			m.cancelEpoch()
			m.cancelEpoch = nil
			m.leaderCtx = nil
		}
		m.mu.Unlock()

		m.cancel()
		m.wg.Wait()
		logger.Log().Infof("Conductor registration manager closed")
	})
}

// applyRoundsForTest 返回 applyOnce 至今的执行轮次，仅供测试断言 worker 不空转。
func (m *RegistrationManager) applyRoundsForTest() uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.applyRounds
}

// desiredIDsForTest 返回排序后的 desired 实例 ID，仅供测试断言。
func (m *RegistrationManager) desiredIDsForTest() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	ids := make([]string, 0, len(m.desired))
	for id := range m.desired {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
