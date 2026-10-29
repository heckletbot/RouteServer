# 可观测性

[English](observability.md) | [简体中文](observability.zh-CN.md)

可观测性是一项横切关注点，贯穿 RouteServer 的所有层次，包含三大支柱：指标、追踪与感知。

---

## 指标（Prometheus）

Prometheus 指标定义在 `pkg/route-server/metrics/metrics.go` 中，并通过 `/metrics` 端点暴露。

API 详情见 [GET /internal/metrics](./restful-api.zh-CN.md#get-internalmetrics)。

### 请求量

| 指标 | 类型 | 说明 |
|---|---|---|
| `num_running_requests` | GaugeVec | 当前正在处理的请求数量，按实例类型打标签 |
| `num_failed_requests` | Counter | 失败的推理请求总数 |
| `num_successed_requests` | Counter | 成功的推理请求总数 |
| `num_scheduled_requests` | Counter | 已调度的推理请求总数 |
| `num_requests_with_error_code` | CounterVec | 按错误码分组的请求数量（包含成功的请求） |

### 性能

| 指标 | 类型 | 说明 |
|---|---|---|
| `request_total_time_secs` | Histogram | 请求端到端处理耗时分布 |
| `time_to_first_token_sec` | Histogram | TTFT（Time to First Token）分布 |
| `time_per_output_token_ms` | Histogram | TPOT（Time Per Output Token）分布 |
| `tokens_output_per_request` | Histogram | 每个请求的输出 token 数分布 |
| `prompt_token_num` | Histogram | prompt token 数分布 |

### 调度

| 指标 | 类型 | 说明 |
|---|---|---|
| `request_schedule_time` | HistogramVec | 调度耗时分布，按实例类型打标签 |
| `num_fallback_scheduled` | Counter | 通过回退调度派发的请求数量 |
| `num_leader_remote_scheduled` | Counter | 转发给 leader 进行远程调度的请求数量 |

### 实例健康

| 指标 | 类型 | 说明 |
|---|---|---|
| `number_healthy_endpoints` | GaugeVec | 健康的推理实例数量，按角色（prefill/decode）打标签 |
| `number_faulty_endpoints` | GaugeVec | 故障推理实例数量，按角色打标签 |
| `worker_readiness_state` | Gauge | worker 就绪状态：0=force_true，1=force_false，2=normal，3=broken |
| `link_blacklist_size` | GaugeVec | 链路黑名单大小，按角色打标签 |
| `num_unreachable_workers` | Gauge | 不可达的 worker 节点数量 |
| `num_discarded_unreachable_rs_workers` | Counter | 因不可达被丢弃的 RS worker 数量 |
| `num_discarded_unreachable_endpoints` | CounterVec | 因不可达被丢弃的推理端点数量，按角色打标签 |

### APC（Attention Prefix Caching）

| 指标 | 类型 | 说明 |
|---|---|---|
| `apc_query_latency_seconds` | Histogram | APC 查询耗时分布 |
| `apc_query_match_ratio` | Histogram | APC 缓存匹配率分布 |
| `prefill_cached_tokens` | Histogram | 前缀匹配得到的缓存 token 数分布 |

### 集群

| 指标 | 类型 | 说明 |
|---|---|---|
| `leader_change_total` | CounterVec | leader 切换次数，按类型打标签 |
| `node_role` | Gauge | 当前节点角色（leader/follower） |
| `events_remaining` | Gauge | 事件队列中剩余的事件数量 |
| `etcd_connection_retries` | Counter | etcd 连接重试次数 |
| `metrics_collection_failures` | Counter | 指标采集失败次数 |

---

## 追踪日志

分布式追踪通过 `RequestTracer` 接口（`pkg/common/trace/trace_interface.go`）实现，支持多种后端。

追踪日志输出遵循 RouteServer 的结构化日志格式。下面是一个在分布式模式下处理的请求的追踪示例（worker 接收并处理请求，leader 提供调度服务）：

| 后端 | 文件 | 说明 |
|---|---|---|
| **LogTrace** | `pkg/common/trace/trace_log.go` | 基于结构化日志的追踪。记录 span 的 start/end/error 时间戳、事件，以及 token 级性能（TTFT、TPOT、latency）。 |
| **NoTrace** | `pkg/common/trace/trace_empty.go` | 空实现的追踪器，用于关闭追踪的环境。 |

分布式模式下日志追踪的示例如下（即 RouteServer worker 接收并处理请求，RouteServer leader 为 worker 提供调度服务）。一个请求的追踪如下：

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

相应地，该请求在 Leader 上的追踪日志如下：
```text
1. Leader receives a prefill scheduling request from the worker. (Only printed in distributed mode)
2026-05-10 16:20:07.829 +0800 | INFO | candidate_a_a_a_a_19800:trace_log.go:38 | Trace Event [deal_remote_schedule] request_id: chatcmpl-66663064323166342d623561312d3439-efb70b3b, x-span-id: , x-user-alias: , event details: remote schedule for [prefill] req [chatcmpl-66663064323166342d623561312d3439-efb70b3b] to [prefill_******]

2. Leader receives a Decode scheduling request from the worker. (Only printed in distributed mode)
2026-05-10 16:20:17.838 +0800 | INFO | route1:trace_log.go:38 | Trace Event [deal_remote_schedule] request_id: chatcmpl-66663064323166342d623561312d3439-efb70b3b, x-span-id: , x-user-alias: , event details: remote schedule for [decode] req [chatcmpl-66663064323166342d623561312d3439-efb70b3b] to [decode_******]

```


---

## 请求阶段感知

流水线的每个阶段都会记录其执行上下文，包括阶段名、stage 名与 span 名。这使得我们可以端到端地看到请求在任意时刻处于哪个阶段，以及每个阶段耗时多少。

流水线执行引擎跟踪：
- 阶段开始时间
- 阶段完成时间
- 阶段依赖与执行顺序
- 子流水线协同状态

API 详情见 [GET /cluster/running_details](./restful-api.zh-CN.md#get-clusterrunning_details)

---

## 调度负载感知

调度器层通过多种渠道暴露负载信息：

### 实例级指标
- 每个实例的运行中请求数
- 每个实例的等待请求数
- token 吞吐（prompt token、generation token）
- 这些指标由 `MonitorClient` 从 Metrics Collector 或 Prometheus 采集

### SLO 组利用率
- SLO 组的实例数量与可用性
- 各组的负载分布

### APC 缓存指标
- 每个实例的缓存匹配率
- 缓存查询耗时
- 这些指标会参与 CacheAware 调度算法的决策

所有负载信息也通过 Prometheus 暴露，供外部监控与看板使用。

API 详情见 [GET /scheduler/load](./restful-api.zh-CN.md#get-schedulerload)

---

## 引擎状态感知

实例管理器暴露推理引擎的运行时状态：

| 状态 | 说明 |
|---|---|
| **健康状态** | 可用 / 不可用 |
| **就绪状态** | `force_true`、`force_false`、`normal`、`broken` |
| **链路黑名单** | PD 分离部署下的 KV 传输链路状态 |
| **连续计数** | 连续失败/成功次数 |

API 详情见 [GET /status](./restful-api.zh-CN.md#get-status)

---
