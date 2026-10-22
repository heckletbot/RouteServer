# Inference Instance Health

RouteServer provides multi-granularity fault detection, isolation, and automatic recovery for inference engine (vLLM) instances. The health check system is orchestrated by `LeaderHealthChecker` and spans both leader and worker nodes.

---

## Architecture Overview

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

## Multi-granularity Fault Detection

Fault detection operates at multiple levels of granularity, implemented in `pkg/common/health/`.

### 1. HealthyChecker — Active Health Probe

**File**: `pkg/common/health/healthy_checker.go`

Periodically calls each instance's `/health` endpoint to verify liveness.

See api details at [GET /health](./restful-api.md#get-health).

| Feature | Description |
|---|---|
| **Batch Execution** | Instances are probed in batches with configurable concurrency to avoid overwhelming the cluster |
| **Recovery Probe** | Before marking a previously-faulty instance as available again, a probe inference request is sent to confirm the instance is truly operational |
| **Configurable Interval** | Health check frequency is configurable per deployment |

### 2. FailureChecker — Request Failure Analysis

**File**: `pkg/common/health/failure_checker.go`

Analyzes inference request events to detect failing instances.

| Feature | Description |
|---|---|
| **Event Types** | Timeouts, connection errors, HTTP status codes (4xx, 5xx) |
| **Threshold-based** | If failures exceed a configurable threshold (`MaxFailuresTime`) within a time window, the instance is marked as faulty |
| **Virtual Probe** | Before declaring failure, sends a fake inference request (`sendFakeRequestTestIsAlive`) to confirm the instance is truly unresponsive |
| **Configurable Window** | The time window for failure accumulation is configurable |

### 3. HangChecker — Instance Hang Detection

**File**: `pkg/common/health/hang_checker.go`

Detects instances that are alive (respond to health checks) but not making progress on inference requests.

| Feature | Description |
|---|---|
| **Worker-side Recording** | Each worker records request timestamps and token generation rates |
| **Leader-side Decision** | The leader compares recorded data against configurable timeout thresholds to determine if an instance is hung |
| **Dual Implementation** | `LeaderHangDecider` on the leader side, `WorkerHangChecker` on the worker side |

### 4. LinkdownChecker — KV Transfer Link Monitoring

**File**: `pkg/common/health/linkdown_checker.go`

Monitors the KV cache transfer link between Prefill and Decode instances in PD disaggregated deployments.

| Feature | Description |
|---|---|
| **Purpose** | Detects failures in the Prefill-to-Decode KV transfer path |
| **Scope** | Only applicable in PD Disaggregate and EPD Disaggregate modes |

See api details at [GET /link_black_instance](./restful-api.md#get-link_black_instance) for querying the link blacklist.

### 5. FallbackHealthChecker — Local Blacklist

**File**: `pkg/common/health/fallback_checker.go`

Worker-side local health check that provides fast failure detection without waiting for leader coordination.

| Feature | Description |
|---|---|
| **Local Blacklist** | Maintains a failure blacklist with configurable `ResumeTimeMs` |
| **Fast Failover** | Enables immediate fallback to healthy instances without round-trip to leader |
| **Automatic Recovery** | Blacklisted instances are re-evaluated after the resume time expires |

---

## Health Check Orchestration

### LeaderHealthChecker

**File**: `pkg/common/health/leader_health.go`

The `LeaderHealthChecker` is the central orchestrator that:

1. Integrates all five sub-modules (HealthyChecker, FailureChecker, HangChecker, LinkdownChecker, FallbackHealthChecker)
2. Aggregates events from all sub-modules
3. Drives instance state transitions between healthy and unhealthy
4. Maintains a global view of cluster health

See api details at [GET /internal/leader/readiness](./restful-api.md#get-internalleaderreadiness) for leader health probe.

### WorkerEventCollector

**File**: `pkg/route-server/health/worker_health.go`

Runs on each worker node and collects:
- Request success/failure events
- Token generation events
- Instance health check results

These events are batched and asynchronously reported to the leader node for centralized fault analysis.

See api details at [GET /internal/worker/readiness](./restful-api.md#get-internalworkerreadiness) and [GET /worker/readiness](./restful-api.md#get-workerreadiness) for worker health probes.

---

## Fault Isolation and Recovery

### Circuit Breaker

**File**: `pkg/route-server/instances/circuit_breaker.go`

A three-level circuit breaker protects against cascading failures:

| Level | Description |
|---|---|
| **ForceMode** | Force instances to ready (`force_true`) or not-ready (`force_false`) state for maintenance or emergency scenarios |
| **Failure Rate Monitoring** | Periodically evaluates request failure rates against configurable thresholds |
| **Automatic Recovery** | Instances are automatically re-evaluated after a cooldown period |

The circuit breaker exposes the `rs:worker_readiness_state` Prometheus metric with the following values:
- `0`: force_true (forced ready)
- `1`: force_false (forced not-ready)
- `2`: normal (no circuit breaker active)
- `3`: broken (circuit breaker tripped)

See api details at [POST /internal/worker/force-readiness](./restful-api.md#post-internalworkerforce-readiness) for forced readiness operations.

### Instance State Management

Instances are maintained in two maps:
- **`available`**: Healthy instances ready to serve requests
- **`unavailable`**: Faulty instances being monitored or in cooldown

Transitions between these states are driven by health check results and fault detection events.

See api details at [GET /status](./restful-api.md#get-status) for querying instance status.

### Failure Persistence

Instance failure information is persisted to Etcd (in distributed mode) for durability across restarts, ensuring that transient leader failures do not reset the fault state of instances.

### Recovery Probe

Before moving an instance from `unavailable` back to `available`, a probe inference request is sent to confirm the instance is truly operational. This prevents flapping and ensures only genuinely recovered instances are returned to service.

---

## Related Metrics

| Metric | Description |
|---|---|
| `number_healthy_endpoints` | Number of healthy inference instances, labeled by role |
| `number_faulty_endpoints` | Number of faulty inference instances, labeled by role |
| `worker_readiness_state` | Worker readiness state (force_true/force_false/normal/broken) |
| `link_blacklist_size` | KV transfer link blacklist size, labeled by role |
| `num_unreachable_workers` | Number of unreachable worker nodes |
| `events_remaining` | Number of remaining health check events in the queue |

See [Observability](observability.md) for full metrics documentation.
