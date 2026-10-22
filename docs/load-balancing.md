# Scheduling Algorithms

## Overview

In inference services, the scheduler dispatches incoming requests to appropriate worker instances. In a PD-disaggregated architecture, the Prefill and Decode phases each need to route requests to their respective instances.

This system supports the following four scheduling algorithms:

| Algorithm | Use Case | Core Strategy |
|---|---|---|
| round-robin | Simple scenarios; works for both Prefill and Decode | Round-robin dispatch |
| least-total-load | Prefill-side load balancing | Minimum load first based on load formula |
| apc | Prefill-side with KV Cache reuse | Prefix cache awareness + load prediction |
| abc6 | Decode-side load balancing | State awareness + memory correction + overload penalty |

---

## round-robin

### 1. Principle

The most basic scheduling algorithm, dispatching requests to instances in round-robin order. It is highly robust and widely applicable, but does not perceive instance load status — it only ensures even distribution of requests across instances. When request sequence lengths vary significantly, true load balance cannot be guaranteed, which may cause fast-slow bottleneck effects.

![Traditional Scheduling Algorithm](imgs/rr.jpg)
<center>Round-Robin Scheduling Algorithm Diagram</center>

This algorithm has no state awareness or load modeling. It is simple to implement with minimal overhead, and serves as the fallback algorithm for other algorithms.

### 2. Dispatch Logic

1. Maintains an instance list and a current index `currIndex`.
2. On each incoming request, `currIndex` increments modulo the number of instances, selecting the corresponding instance.
3. If the target instance is in the blacklist, skip and try the next one.
4. Requires no metric collection (`NeedStatsList` is empty) and no periodic updates.

### 3. Configuration Parameters

No additional configuration parameters required.

---

## least-total-load

### 1. Principle

Calculates the load of each instance using a formula based on request Token length and BatchSize constraints, then dispatches requests to the instance with the lowest load. Suitable for the Prefill side, where each request completes in a single step and the load can be measured by the sum of dispatched Token lengths.

**Load Modeling:** The load of each request is approximated by its Prompt Token length:

$$l_q \approx \text{length}(q)$$

The instance load formula introduces a BatchSize penalty term:

$$\text{Load} = \text{total\_len} \times \frac{\lceil \text{req\_num} / \text{batchsize} \rceil + 1}{2}$$

- When `req_num < batchsize`, the load equals the sum of Token lengths.
- When `req_num >= batchsize`, the load equals the sum of Token lengths multiplied by 1.5x (assuming `req_num` just exceeds `batchsize`), acting as a soft penalty to avoid sending further requests to an instance whose BatchSize is already full.

**Memory Correction:** After dispatching a request, the scheduler immediately accumulates the estimated load (request count +1, Token length adds the current request length), and subtracts it when the request completes. Even when metric feedback is delayed, scheduling decisions remain based on up-to-date load estimates, preventing a large number of requests from being concentrated on the same instance within a short period.

![Prefill Scheduling Algorithm Diagram](imgs/p.jpg)
<center>least-total-load Scheduling Algorithm Diagram</center>

### 2. Dispatch Logic

1. On each incoming request, calculate the load of each instance using the load formula and sort in ascending order.
2. Select the instance with the lowest load; skip any blacklisted instances.
3. Immediately update the instance's request count and cumulative Token length after dispatch (memory correction).
4. When a request completes, subtract its load from the corresponding instance.

### 3. Configuration Parameters

| Parameter | Description | Default |
|---|---|---|
| max_num_seqs | Maximum BatchSize per instance | 16 |
| verbose | Whether to print detailed load logs | false |

---

## apc

### 1. Principle

The APC (Aware Prefix Cache) algorithm builds upon least-total-load by adding prefix cache awareness. When a request's Prompt has a prefix match with an instance's existing KV Cache, dispatching the request to that instance enables cache reuse, reducing redundant computation. Suitable for Prefill-side scenarios with multi-turn conversations or other KV Cache reuse opportunities.

**Architecture:** APC consists of two layers:

1. **APCScheduler**: The scheduler layer, responsible for load calculation, instance selection, and load increment/decrement management.
2. **APCManager**: The global manager, responsible for cache lookup, matching, updates, and persistence. Supports coordination across multiple schedulers via shared memory.

**Cache Matching Strategy:** Currently supports the RollingHash strategy — the Prompt is partitioned into fixed-size blocks (`block_size_bytes`), a rolling hash is computed for each block, and the hash values are used to look up instances with prefix matches for the current Prompt, returning a match ratio (`matched_ratio`). The Progressive Block strategy is also supported, which performs coarse filtering with larger blocks first, then fine-grained matching.

**Load Modeling:** Building on the least-total-load formula, the request's Prompt Token length is discounted by the match ratio:

$$\text{reqLen} = \text{prompt\_tokens} \times (1 - \text{matched\_ratio}) + \text{prefix\_load\_base}$$

$$\text{Load} = \text{total\_len} \times \frac{\lceil \text{req\_num} / \text{batchsize} \rceil + 1}{2}$$

A higher match ratio means the request contributes less load to the instance, guiding the scheduler to prefer instances with cache reuse advantages.

### 2. Dispatch Logic

1. Obtain the cache match ratio (`matchedInstances`) for each instance from the APCManager.
2. For each available instance, predict the load after adding the current request.
3. Select the instance with the lowest predicted load; if loads are equal, select the one with the highest match ratio.
4. Update instance load and request dispatch records after dispatch.

**Cache Update:** The APC cache is updated only after a request has been successfully executed in the Prefill phase (`RequestPrefillSuccess` event), ensuring that the KV Cache actually exists. Cache update operations are performed only by the cluster Leader node.

**Degradation:** When APC is not enabled or unavailable (e.g., Manager not initialized, non-Leader node), `DispatchRequest` returns an error, and the scheduler automatically degrades to the fallback algorithm (round-robin).

### 3. Configuration Parameters

**Scheduler Parameters:**

| Parameter | Description | Default |
|---|---|---|
| default-batch-size | Default BatchSize | 16 |
| prefix-load-base | Minimum prefix load base | 1 |

**Manager Parameters:**

| Parameter | Description | Default |
|---|---|---|
| enable | Whether to enable APC | false |
| strategies.hash.block_size_bytes | Block size in bytes | 64 |
| strategies.hash.max_blocks | Maximum number of blocks | 128 |
| strategies.hash.max_entries | Maximum cache entries | 100000 |
| strategies.hash.max_memory_mb | Maximum cache memory (MB) | 512 |
| strategies.hash.cache_ttl_sec | Cache TTL (seconds) | 3600 |
| strategies.hash.progressive_base_block_size_bytes | Progressive block base size | 256 |
| persistence.enable | Whether to enable persistence | false |
| persistence.provider | Persistence provider | - |

---

## abc6

### 1. Principle

A load balancing algorithm for the Decode side that integrates three key mechanisms: state awareness, memory correction, and overload penalty. During the Decode phase, requests execute over multiple steps, so load cannot be characterized by request count alone — it requires combining Token length, waiting queue, and running queue status for comprehensive decision-making.

**State Awareness:** The following metrics are obtained from the inference engine and fed back to the scheduler:

| Metric | Description |
|---|---|
| PromptTokensTotal + GenerationTokensTotal | Current sum of Token lengths |
| NumRequestsWaiting | Current number of requests in the waiting queue |
| NumRequestsRunning | Current number of requests in the running queue |

**Memory Correction:** There is a delay from request dispatch to actual execution and then to load metric feedback. Relying solely on feedback metrics would cause requests to concentrate on the instance that appears to have the "lowest load" in a short period. abc6's memory correction mechanism works as follows:

- When dispatching a request, immediately accumulate the estimated load to the target instance (Token length +1, running queue / waiting queue +1).
- When a request completes, subtract the corresponding estimated load.
- If feedback metrics have already been refreshed, skip the subtraction to avoid negative values.

**Overload Determination and Penalty:** An instance is considered overloaded when:

```
waiting_queue >= 2  OR  running_queue >= batchsize - 1
```

Overloaded instances are penalized during sorting (default penalty value: 1,280,000), pushing them toward the end of the sorted list.

![Scheduling Algorithm Diagram](imgs/dp.jpg)
<center>abc6 Scheduling Algorithm Diagram</center>

### 2. Dispatch Logic

1. Check whether all instances are overloaded:
   - **All overloaded**: Sort by waiting queue count (`waiting_queue`), select the instance with the fewest waiting requests, degrading to a request-count-based balancing strategy.
   - **Partially overloaded**: Sort by Token length load, apply penalty to overloaded instances, and select the non-overloaded instance with the lowest load.
2. Skip blacklisted instances in the sorted result and select the first available instance.
3. Update memory load after dispatch.
4. When a request completes, subtract the corresponding estimated load.

**Metric Expiration:** If collected metrics have not been updated for longer than `metrics_overdue_interval_ms` (default 1000ms), they are deemed expired and not adopted; the last valid value is retained. When an instance's running queue transitions from greater than 0 to 0, and the time since the last update exceeds 5 seconds, metrics are force-updated.

### 3. Configuration Parameters

| Parameter | Description | Default |
|---|---|---|
| max_num_seqs | Maximum BatchSize per instance | 16 |
| update_interval_ms | Metric sync interval (ms); -1 means update only on node changes | -1 |
| metrics_overdue_interval_ms | Metric expiration threshold (ms) | 1000 |

---

## Algorithm Selection and Configuration

Scheduling algorithms are specified through the configuration file. Each request type (InstanceRole, such as `prefill`, `decode`) can be configured with an independent scheduling algorithm. When the primary scheduling algorithm fails, the system automatically degrades to the fallback algorithm (round-robin).

### Configuration Example

```yaml
schedulers:
  - instance_role: decode
    scheduler_name: abc6
    params:
      max_num_seqs: 32
      update_interval_ms: 500
      metrics_overdue_interval_ms: 1000

  - instance_role: prefill
    scheduler_name: least-total-load
    params:
      max_num_seqs: 16
      verbose: false

  # Prefill scheduling with cache awareness
  - instance_role: prefill
    scheduler_name: apc
    params:
      default-batch-size: 16
      prefix-load-base: 1
```

### SLO Grouping

For the Prefill side, SLO (Service Level Objective) grouped scheduling is supported. When enabled, requests of different SLO levels are managed by independent algorithm instances, ensuring that high-priority requests receive better scheduling resources. Each SLO group has its own independent fallback algorithm.

### Multi-Scheduler Coordination

In high-concurrency scenarios, multiple scheduler instances process requests in parallel. abc6 and least-total-load protect shared state through read-write locks; APC achieves cross-scheduler cache sharing through a global Manager instance. Each scheduler can read the load status of all instances when dispatching requests, enabling globally optimal decisions.

---

## Algorithm Comparison

| Dimension | round-robin | least-total-load | apc | abc6 |
|---|---|---|---|---|
| Load Awareness | None | Token length + BatchSize penalty | Token length + Cache match + BatchSize penalty | Token length + Queue status + Overload penalty |
| Metric Collection | None | None (self-maintained) | Self-maintained + Cache lookup | 4 metrics |
| Applicable Phase | Any | Prefill | Prefill (KV Cache reuse) | Decode |
| Cache Awareness | None | None | Yes (RollingHash strategy) | None |
| Overload Protection | None | Soft penalty (formula coefficient) | Soft penalty (formula coefficient) | Hard determination + Penalty value |
| Memory Correction | None | Yes | Yes | Yes |
| Complexity | O(1) | O(n log n) | O(n log n) + Cache lookup | O(n log n) |
| Fallback | - | round-robin | round-robin | round-robin |
