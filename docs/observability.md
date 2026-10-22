# Observability

Observability is a cross-cutting concern that spans all layers of ModelOrch with three pillars: metrics, tracing, and awareness.

---

## Metrics (Prometheus)

Prometheus metrics are defined in `pkg/route-server/metrics/metrics.go` and exposed via the `/metrics` endpoint.

See api details at [GET /internal/metrics](./restful-api.md#get-internalmetrics).

### Request Volume

| Metric | Type | Description |
|---|---|---|
| `num_running_requests` | GaugeVec | Number of requests currently being processed, labeled by instance type |
| `num_failed_requests` | Counter | Total number of failed inference requests |
| `num_successed_requests` | Counter | Total number of successful inference requests |
| `num_scheduled_requests` | Counter | Total number of scheduled inference requests |
| `num_requests_with_error_code` | CounterVec | Number of requests grouped by error code (including successful requests) |

### Performance

| Metric | Type | Description |
|---|---|---|
| `request_total_time_secs` | Histogram | End-to-end request processing time distribution |
| `time_to_first_token_sec` | Histogram | TTFT (Time to First Token) distribution |
| `time_per_output_token_ms` | Histogram | TPOT (Time Per Output Token) distribution |
| `tokens_output_per_request` | Histogram | Number of output tokens per request distribution |
| `prompt_token_num` | Histogram | Prompt token count distribution |

### Scheduling

| Metric | Type | Description |
|---|---|---|
| `request_schedule_time` | HistogramVec | Scheduling time distribution, labeled by instance type |
| `num_fallback_scheduled` | Counter | Number of requests dispatched via fallback scheduling |
| `num_leader_remote_scheduled` | Counter | Number of requests forwarded to leader for remote scheduling |

### Instance Health

| Metric | Type | Description |
|---|---|---|
| `number_healthy_endpoints` | GaugeVec | Number of healthy inference instances, labeled by role (prefill/decode) |
| `number_faulty_endpoints` | GaugeVec | Number of faulty inference instances, labeled by role |
| `worker_readiness_state` | Gauge | Worker readiness state: 0=force_true, 1=force_false, 2=normal, 3=broken |
| `link_blacklist_size` | GaugeVec | Link blacklist size, labeled by role |
| `num_unreachable_workers` | Gauge | Number of unreachable worker nodes |
| `num_discarded_unreachable_rs_workers` | Counter | Number of RS workers discarded due to unreachability |
| `num_discarded_unreachable_endpoints` | CounterVec | Number of inference endpoints discarded due to unreachability, labeled by role |

### APC (Attention Prefix Caching)

| Metric | Type | Description |
|---|---|---|
| `apc_query_latency_seconds` | Histogram | APC query latency distribution |
| `apc_query_match_ratio` | Histogram | APC cache match ratio distribution |
| `prefill_cached_tokens` | Histogram | Number of cached tokens from prefix matching distribution |

### Cluster

| Metric | Type | Description |
|---|---|---|
| `leader_change_total` | CounterVec | Number of leader changes, labeled by type |
| `node_role` | Gauge | Current node role (leader/follower) |
| `events_remaining` | Gauge | Number of remaining events in the event queue |
| `etcd_connection_retries` | Counter | Number of Etcd connection retries |
| `metrics_collection_failures` | Counter | Number of metrics collection failures |

---

## Trace Logging

Distributed tracing is implemented via the `RequestTracer` interface (`pkg/common/trace/trace_interface.go`), supporting multiple backends.

The trace log output follows the structured log format of ModelOrch. Below is an example trace for a request processed in distributed mode (worker receives and processes request, leader provides scheduling service):

| Backend | File | Description |
|---|---|---|
| **LogTrace** | `pkg/common/trace/trace_log.go` | Structured logging-based tracing. Records span start/end/error timestamps, events, and token-level performance (TTFT, TPOT, latency). |
| **NoTrace** | `pkg/common/trace/trace_empty.go` | No-op tracer for environments where tracing is disabled. |

See following example for log trace in distributed mode (i.e. ModelOrch worker receive and process request. ModelOrch leader provide schedule service for workers). A request's trace will be as follows:

```text
1. Request end-to-end start. If the request specifies a request_id via X-Request-Id, the original reqId and the ID with a random suffix will be printed here.
2026-05-10 16:20:07.829 +0800 | INFO | worker_x_x_x_x_19800:trace_log.go:77 | Trace Span [RequestE2E] stage [start] request_id: chatcmpl-66663064323166342d623561312d3439-efb70b3b, x-span-id: 3066333632, x-user-alias: , original request id [66663064323166342d623561312d3439] will be rewrite to [66663064323166342d623561312d3439-efb70b3b]

2. In distributed mode, the Worker requests Prefill scheduling from the Leader. The Leader address is printed here. (Only printed in distributed mode)
2026-05-10 16:20:07.829 +0800 | INFO | worker_x_x_x_x_19800:trace_log.go:38 | Trace Event [remote_schedule] request_id: chatcmpl-66663064323166342d623561312d3439-efb70b3b, x-span-id: 3066333632, x-user-alias: , event details: send scheduler for [prefill] request [chatcmpl-66663064323166342d623561312d3439-efb70b3b] to remote scheduler [http://******]

3. Send request to Prefill
2026-05-10 16:20:07.829 +0800 | INFO | worker_x_x_x_x_19800:trace_log.go:38 | Trace Event [Prefill_start] request_id: chatcmpl-66663064323166342d623561312d3439-efb70b3b, x-span-id: 3066333632, x-user-alias: , event details: send request to [prefill] instance [prefill_******] [http://******], slo tag is [default], stream = [true], reqLoad = [39]

4. Prefill request completed
2026-05-10 16:20:17.838 +0800 | INFO | worker_x_x_x_x_19800:trace_log.go:38 | Trace Event [Prefill_done] request_id: chatcmpl-66663064323166342d623561312d3439-efb70b3b, x-span-id: 3066333632, x-user-alias: , event details: execute instance [prefill_******], cost [10.00864646] sec

5. In distributed mode, the Worker requests Decode scheduling from the Leader. The Leader address is printed here. (Only printed in distributed mode)
2026-05-10 16:20:17.838 +0800 | INFO | worker_x_x_x_x_19800:trace_log.go:38 | Trace Event [remote_schedule] request_id: chatcmpl-66663064323166342d623561312d3439-efb70b3b, x-span-id: 3066333632, x-user-alias: , event details: send scheduler for [decode] request [chatcmpl-66663064323166342d623561312d3439-efb70b3b] to remote scheduler [http://******]

6. Send request to Decode
2026-05-10 16:20:17.838 +0800 | INFO | worker_x_x_x_x_19800:trace_log.go:38 | Trace Event [Decode_start] request_id: chatcmpl-66663064323166342d623561312d3439-efb70b3b, x-span-id: 3066333632, x-user-alias: , event details: send request to [decode] instance [decode_******] [http://*****], slo tag is [default], stream = [true], reqLoad = [2000]

7. Decode streaming output completed
2026-05-10 16:20:18.382 +0800 | INFO | worker_x_x_x_x_19800:trace_log.go:38 | Trace Event [stream_process_done] request_id: chatcmpl-66663064323166342d623561312d3439-efb70b3b, x-span-id: 3066333632, x-user-alias: , event details: decode process finished, readtime [506.564116ms], writetime [334.145µs]
2026-05-10 16:20:18.382 +0800 | INFO | worker_x_x_x_x_19800:utils.go:376 | [STREAM_DEBUG] request chatcmpl-66663064323166342d623561312d3439-efb70b3b write, writetime [334.145µs], total_non_read_time [365.597µs]

8. When the request completes normally, the collected ttft, tpot and other information are printed
2026-05-10 16:20:18.382 +0800 | INFO | worker_x_x_x_x_19800:trace_log.go:72 | x-span-id=3066333632|x-user-alias=|CompletionMetric: {'trace_id': 'chatcmpl-66663064323166342d623561312d3439-efb70b3b', 'num_prompt_tokens': 2000, 'num_output_tokens': 10, 'num_cached_tokens': 1000, 'ttft': 10046.21, 'tpot': 56.33, 'latency': 10553.14}

9. Decode request completed
2026-05-10 16:20:18.382 +0800 | INFO | worker_x_x_x_x_19800:trace_log.go:38 | Trace Event [Decode_done] request_id: chatcmpl-66663064323166342d623561312d3439-efb70b3b, x-span-id: 3066333632, x-user-alias: , event details: execute instance [decode_******], cost [0.543511141] sec

10. Request end-to-end completed
2026-05-10 16:20:18.382 +0800 | INFO | worker_x_x_x_x_19800:trace_log.go:77 | Trace Span [RequestE2E] stage [end] request_id: chatcmpl-66663064323166342d623561312d3439-efb70b3b, x-span-id: 3066333632, x-user-alias: , msg: [success to infer request, finish_reason = ,SecondDTokenLatencyMs=50,FirstDTokenLatencyMs=36,FirstTokenLatencyMs=10046], duration time: [10.553215504s]
```

Correspondingly, the Trace logs for this request on the Leader are as follows:
```text
1. Leader receives a prefill scheduling request from the worker. (Only printed in distributed mode)
2026-05-10 16:20:07.829 +0800 | INFO | candidate_a_a_a_a_19800:trace_log.go:38 | Trace Event [deal_remote_schedule] request_id: chatcmpl-66663064323166342d623561312d3439-efb70b3b, x-span-id: , x-user-alias: , event details: remote schedule for [prefill] req [chatcmpl-66663064323166342d623561312d3439-efb70b3b] to [prefill_******]

2. Leader receives a Decode scheduling request from the worker. (Only printed in distributed mode)
2026-05-10 16:20:17.838 +0800 | INFO | route1:trace_log.go:38 | Trace Event [deal_remote_schedule] request_id: chatcmpl-66663064323166342d623561312d3439-efb70b3b, x-span-id: , x-user-alias: , event details: remote schedule for [decode] req [chatcmpl-66663064323166342d623561312d3439-efb70b3b] to [decode_******]

```


---

## Request Phase Awareness

Each pipeline phase records its execution context with phase name, stage name, and span name. This enables end-to-end visibility into which phase a request is in at any given time, and how long each phase takes.

The pipeline execution engine tracks:
- Phase start time
- Phase completion time
- Phase dependencies and execution order
- Sub-pipeline coordination state

See api details at [GET /cluster/running_details](./restful-api.md#get-clusterrunning_details)

---

## Scheduling Load Awareness

The scheduler layer exposes load information through multiple channels:

### Instance-Level Metrics
- Running requests per instance
- Waiting requests per instance
- Token throughput (prompt tokens, generation tokens)
- These metrics are collected by the `MonitorClient` from the Metrics Collector or Prometheus

### SLO Group Utilization
- SLO group instance counts and availability
- Per-group load distribution

### APC Cache Metrics
- Cache match ratio per instance
- Cache query latency
- These metrics feed into CacheAware scheduling algorithm decisions

All load information is also exposed via Prometheus for external monitoring and dashboarding.

See api details at [GET /scheduler/load](./restful-api.md#get-schedulerload)

---

## Engine Status Awareness

The instance manager exposes runtime status of inference engines:

| Status | Description |
|---|---|
| **Health Status** | Available / Unavailable |
| **Readiness State** | `force_true`, `force_false`, `normal`, `broken` |
| **Link Blacklist** | KV transfer link status for PD disaggregated deployments |
| **Consecutive Counts** | Consecutive failure/success counts |

See api details at [GET /status](./restful-api.md#get-status)

---
