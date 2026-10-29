# 推理实例健康检查

[English](infer-instance-health.md) | [简体中文](infer-instance-health.zh-CN.md)

RouteServer 为推理引擎（vLLM）实例提供多粒度的故障检测、隔离与自动恢复能力。健康检查系统由 `LeaderHealthChecker` 编排，覆盖 leader 与 worker 两类节点。

---

## 架构总览

```
┌─────────────────────────────────────────────────────────────────────┐
│                     LeaderHealthChecker                              │
│  (pkg/common/health/leader_health.go)                               │
│                                                                     │
│  Aggregates events from all sub-modules, drives instance state      │
│  transitions between healthy and unhealthy.                         │
└─────────────────────────────────────────────────────────────────────┘
                              │
        ┌─────────────────────┼─────────────────────┐
        │                     │                     │
        ▼                     ▼                     ▼
┌───────────────┐   ┌───────────────────┐   ┌───────────────┐
│ HealthyChecker│   │  FailureChecker   │   │  HangChecker  │
│ (Active Probe)│   │ (Request Failure) │   │(Instance Hang)│
└───────────────┘   └───────────────────┘   └───────────────┘
                                                                   
┌───────────────┐   ┌───────────────────┐   ┌───────────────┐
│LinkdownChecker│   │FallbackHealthCheck│   │WorkerEventColl│
│(KV Transfer)  │   │ (Local Blacklist) │   │ (Event Report)│
└───────────────┘   └───────────────────┘   └───────────────┘
```

---

## 多粒度故障检测

故障检测在多个粒度层级上运行，实现在 `pkg/common/health/` 中。

### 1. HealthyChecker — 主动健康探针

**文件**：`pkg/common/health/healthy_checker.go`

周期性调用每个实例的 `/health` 端点，以验证其存活性。

API 详情见 [GET /health](./restful-api.zh-CN.md#get-health)。

| 特性 | 说明 |
|---|---|
| **批量执行** | 按批探测实例，并发度可配置，避免压垮集群 |
| **恢复探针** | 在把此前故障的实例重新置为可用之前，先发送一次探针推理请求，确认该实例确实能正常工作 |
| **可配置间隔** | 健康检查频率可按部署配置 |

### 2. FailureChecker — 请求失败分析

**文件**：`pkg/common/health/failure_checker.go`

分析推理请求事件，以识别出故障的实例。

| 特性 | 说明 |
|---|---|
| **事件类型** | 超时、连接错误、HTTP 状态码（4xx、5xx） |
| **阈值判定** | 若时间窗口内的失败次数超过可配置阈值（`MaxFailuresTime`），该实例被标记为故障 |
| **虚拟探针** | 在宣布失败之前，先发送一次伪造的推理请求（`sendFakeRequestTestIsAlive`），确认实例确实无响应 |
| **可配置窗口** | 失败累计的时间窗口可配置 |

### 3. HangChecker — 实例挂起检测

**文件**：`pkg/common/health/hang_checker.go`

检测那些仍然存活（能响应健康检查）、但在推理请求上不再推进的实例。

| 特性 | 说明 |
|---|---|
| **worker 侧记录** | 每个 worker 记录请求时间戳与 token 生成速率 |
| **leader 侧判定** | leader 将记录的数据与可配置的超时阈值比较，判定实例是否挂起 |
| **双端实现** | leader 侧为 `LeaderHangDecider`，worker 侧为 `WorkerHangChecker` |

### 4. LinkdownChecker — KV 传输链路监控

**文件**：`pkg/common/health/linkdown_checker.go`

在 PD 分离部署中监控 Prefill 与 Decode 实例之间的 KV cache 传输链路。

| 特性 | 说明 |
|---|---|
| **用途** | 检测 Prefill 到 Decode 的 KV 传输路径上的故障 |
| **适用范围** | 仅在 PD 分离与 EPD 分离模式下适用 |

查询链路黑名单的 API 详情见 [GET /link_black_instance](./restful-api.zh-CN.md#get-link_black_instance)。

### 5. FallbackHealthChecker — 本地黑名单

**文件**：`pkg/common/health/fallback_checker.go`

worker 侧的本地健康检查，无需等待 leader 协调即可快速发现故障。

| 特性 | 说明 |
|---|---|
| **本地黑名单** | 维护故障黑名单，`ResumeTimeMs` 可配置 |
| **快速故障切换** | 无需与 leader 往返通信，即可立即回退到健康实例 |
| **自动恢复** | 恢复时间到期后，黑名单中的实例会被重新评估 |

---

## 健康检查编排

### LeaderHealthChecker

**文件**：`pkg/common/health/leader_health.go`

`LeaderHealthChecker` 是中心编排者，它：

1. 集成全部五个子模块（HealthyChecker、FailureChecker、HangChecker、LinkdownChecker、FallbackHealthChecker）
2. 汇聚所有子模块的事件
3. 驱动实例在健康与不健康状态之间迁移
4. 维护集群健康的全局视图

leader 健康探测的 API 详情见 [GET /internal/leader/readiness](./restful-api.zh-CN.md#get-internalleaderreadiness)。

### WorkerEventCollector

**文件**：`pkg/route-server/health/worker_health.go`

运行在每个 worker 节点上，收集：
- 请求成功/失败事件
- token 生成事件
- 实例健康检查结果

这些事件会批量汇总，并异步上报到 leader 节点，用于集中式故障分析。

worker 健康探测的 API 详情见 [GET /internal/worker/readiness](./restful-api.zh-CN.md#get-internalworkerreadiness) 与 [GET /worker/readiness](./restful-api.zh-CN.md#get-workerreadiness)。

---

## 故障隔离与恢复

### 熔断器

**文件**：`pkg/route-server/instances/circuit_breaker.go`

三级熔断器用于防止级联故障：

| 层级 | 说明 |
|---|---|
| **ForceMode** | 在维护或紧急场景下，把实例强制置为就绪（`force_true`）或未就绪（`force_false`）状态 |
| **失败率监控** | 定期按可配置阈值评估请求失败率 |
| **自动恢复** | 冷却期结束后自动重新评估实例 |

熔断器暴露 Prometheus 指标 `rs:worker_readiness_state`，取值如下：
- `0`：force_true（强制就绪）
- `1`：force_false（强制未就绪）
- `2`：normal（熔断未生效）
- `3`：broken（熔断已触发）

强制就绪操作的 API 详情见 [POST /internal/worker/force-readiness](./restful-api.zh-CN.md#post-internalworkerforce-readiness)。

### 实例状态管理

实例由两个 map 维护：
- **`available`**：健康、可正常处理请求的实例
- **`unavailable`**：故障实例，正在被监控或处于冷却期

这两种状态之间的迁移由健康检查结果与故障检测事件驱动。

查询实例状态的 API 详情见 [GET /status](./restful-api.zh-CN.md#get-status)。

### 故障持久化

实例故障信息会持久化到 Etcd（分布式模式下），以便重启后依然保留，确保 leader 的瞬时故障不会重置实例的故障状态。

### 恢复探针

在把实例从 `unavailable` 移回 `available` 之前，会发送一次探针推理请求，确认该实例确实能正常工作。这可以避免状态抖动，并确保只有真正恢复的实例才重新投入服务。

---

## 相关指标

| 指标 | 说明 |
|---|---|
| `number_healthy_endpoints` | 健康的推理实例数量，按角色打标签 |
| `number_faulty_endpoints` | 故障的推理实例数量，按角色打标签 |
| `worker_readiness_state` | worker 就绪状态（force_true/force_false/normal/broken） |
| `link_blacklist_size` | KV 传输链路黑名单大小，按角色打标签 |
| `num_unreachable_workers` | 不可达的 worker 节点数量 |
| `events_remaining` | 队列中剩余的健康检查事件数量 |

完整指标文档见 [可观测性](observability.zh-CN.md)。
