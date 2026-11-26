# RouteServer 配置参考

[English](config-spec.md) | [简体中文](config-spec.zh-CN.md)

本文档说明 RouteServer（`route-server.yaml`）的全部配置参数。

## 配置设置

1. 将环境变量 `ENV_ROUTE_SERVER_CONFIG_PATH` 设置为配置目录。
2. 在该目录下放置名为 `route-server.yaml` 的文件。

## 部署说明

约束列中标记为"模板默认值"的参数，指发布包内附带的 `route-server.yaml` 所提供的默认值。通过 `deploy.sh` 部署时，会使用该模板文件生成最终部署配置。

发布模板（含节点间通信）与 `deploy.sh` 的 HTTPS 默认均关闭；mTLS 开关仅在对应 HTTPS 开启后生效。
生产环境建议显式开启所需平面并配置证书，见[安全指南](security.zh-CN.md)。

本文档涉及两类默认值：

- 模板默认值：deploy/route-server-template.yaml 部署模板中预先填入的值。
- 默认值：Go 源码中硬编码的默认值。当配置字段被设为该类型的零值（例如 0、false、空字符串 ""），或完全从 YAML 文件中省略时，该默认值生效。

若模板默认值为空，或该字段在 YAML 中缺失，运行时将使用默认值。默认值即最终回退值。若不存在显式默认值，则适用该类型的 Go 零值（例如整数为 0、布尔为 false、字符串为 ""），这可能导致功能关闭，或表现为约束列所述的具体行为。

---

## server

`server` 段定义 RouteServer 的核心身份与服务设置，包括监听地址、推理模式、链路追踪、限流、TLS/mTLS 以及响应头过滤。

```yaml
server:
  addr: <ip>
  http-port: <port>
  name: <name>
  infer-mode: PD
  inference-module-type: v0
  v1-stream-writing-enabled: true
  client-write-timeout-ms: 0
  rewrite-request-id: true
  trace:
    enable-trace-token-log: true
    type:
      - log
    otel-config:
      jaeger-endpoint:
      jaeger-service-name: RouteServer
      span-update-seconds: 5
      trace-sample-rate: 1.0
  response-header-blacklist:
    - header: "Date"
      prefix-matching: false
    - header: "Server"
      prefix-matching: false
    - header: "X-Pad"
      prefix-matching: false
    - header: "X-Accel-"
      prefix-matching: true
  rate-limit:
    max-concurrent-requests: 10000
    non-reference-apis:
      enabled: true
      requests-per-minute: 60
      exempt-paths:
        - "/v1/chat/completions"
        - "/v1/completions"
        - "/v1/metaserver"
      cleanup-interval-minutes: 5
  tls:
    enable-https: false
    cert-file:
    key-file:
    ca-cert-file:
    skip-verify: false
    enable-mtls: true
  ops-tls:
    enable-https: false
    cert-file:
    key-file:
    ca-cert-file:
  enable-ops-apis-on-service-port: true
```

| 参数 | 类型 | 说明                                                                                                            | 约束                                                                                                                                           |
| --- | --- |---------------------------------------------------------------------------------------------------------------|----------------------------------------------------------------------------------------------------------------------------------------------|
| `server.name` | string | 在集群内唯一标识该 RouteServer 节点的名称                                                                                   | 必须在集群内唯一；重名会覆盖既有状态，并可能导致数据不一致                                                                                                                |
| `server.addr` | string | RouteServer 监听 IP 地址                                                                                          | 必须是合法的 IP 地址                                                                                                                                 |
| `server.http-port` | int | RouteServer 监听 HTTP 端口                                                                                        | 必须是合法且可用的端口                                                                                                                                  |
| `server.infer-mode` | string | 影响请求处理流程的推理部署模式                                                                                               | 默认值：`PD`。取值必须为以下之一：`PD`、`EPD`、`PD-AM`、`Hybrid`、`E-Hybrid`、`Layerwise-CDCP`、`Layerwise-CPCD`、`MetaServer-CDCP`、`MetaServer-CPCD`<br />详见部署文档。 |
| `server.inference-module-type` | string | 流水线调度模式：`v0` 为基本线性调度，`pipeline` 为可配置的流水线调度                                                                    | 默认值：`v0`。必须为 `v0` 或 `pipeline`                                                                                                               |
| `server.v1-stream-writing-enabled` | bool | 启用 v1 流式写入模式                                                                                                  | 模板默认值：`true`                                                                                                                                 |
| `server.client-write-timeout-ms` | int | 客户端写超时，单位毫秒（0 = 不超时）                                                                                          | 模板默认值：`0`                                                                                                                                    |
| `server.rewrite-request-id` | bool | 启用 X-Request-Id 请求头重写                                                                                         | 模板默认值：`true`                                                                                                                                 |
| `server.trace.enable-trace-token-log` | bool | 启用概要链路追踪日志（TTFT、TPOT、prompt token 等）                                                                          | 默认值：`false`；模板默认值：`true`                                                                                                                     |
| `server.trace.type` | List | 链路追踪能力类型：`log`、`otel`、`none`、`compose`。`log` 打印 reqId、span_id 与执行阶段；`otel` 通过 Jaeger 采集完整请求生命周期；`none` 关闭链路追踪 | 模板默认值：`["log"]`                                                                                                                              |
| `server.trace.otel-config.jaeger-endpoint` | string | OpenTelemetry 链路追踪所用的 Jaeger/OTLP 端点，格式为 `IP:Port`（例如 `127.0.0.1:4317`）                                       | 模板默认值：空                                                                                                                                      |
| `server.trace.otel-config.jaeger-service-name` | string | Jaeger 服务名                                                                                                    | 默认值：`RouteServer`                                                                                                                            |
| `server.trace.otel-config.span-update-seconds` | int | 向 Jaeger 发送 span 数据的间隔（秒）                                                                                     | 默认值：`5`。一次完整请求约产生 7 个 span；该值设置过大时，高并发下可能堆积过多本地 span 数据                                                                                      |
| `server.trace.otel-config.trace-sample-rate` | float | 链路追踪采样率。若上游父级已决定采样，则以其决定为准；否则按该采样率执行                                                                          | 必须位于 `[0.0, 1.0]` 区间                                                                                                                         |
| `server.response-header-blacklist[].header` | string | 从转发给客户端的响应中剥离的响应头名称                                                                                           | 模板默认值：`Date`、`Server`、`X-Pad`、`X-Accel-`                                                                                                     |
| `server.response-header-blacklist[].prefix-matching` | bool | 是否按前缀匹配响应头名称                                                                                                  | 模板默认值：精确匹配的响应头为 `false`，`X-Accel-` 为 `true`                                                                                                  |
| `server.rate-limit.max-concurrent-requests` | int | RouteServer 可处理的最大并发活跃请求数                                                                                     | 默认值：`5000`；模板默认值：`10000`                                                                                                                     |
| `server.rate-limit.non-reference-apis.enabled` | bool | 对非参考 API 端点启用限流；令牌桶按 API 路径共享                                                                                 | 模板默认值：`true`                                                                                                                                 |
| `server.rate-limit.non-reference-apis.requests-per-minute` | int | 非参考 API 每个 API 路径每分钟的最大请求数                                                                                    | 模板默认值：`60`                                                                                                                                   |
| `server.rate-limit.non-reference-apis.exempt-paths` | List | 免于限流的 API 路径                                                                                                  | 模板默认值：`/v1/chat/completions`、`/v1/completions`、`/v1/metaserver`                                                                              |
| `server.rate-limit.non-reference-apis.cleanup-interval-minutes` | int | 清理过期限流记录的间隔                                                                                                   | 模板默认值：`5`                                                                                                                                    |
| `server.tls.enable-https` | bool | 为主服务端口启用 HTTPS                                                                                                | 模板默认值：`false`                                                                                                                                |
| `server.tls.cert-file` | string | TLS 证书文件路径                                                                                                    | 模板默认值：空                                                                                                                                      |
| `server.tls.key-file` | string | TLS 私钥文件路径                                                                                                    | 模板默认值：空                                                                                                                                      |
| `server.tls.ca-cert-file` | string | 用于 mTLS 的 CA 证书文件路径                                                                                           | 模板默认值：空                                                                                                                                      |
| `server.tls.skip-verify` | bool | 跳过 TLS 证书校验（不建议用于生产环境）                                                                                        | 模板默认值：`false`                                                                                                                                |
| `server.tls.enable-mtls` | bool | 为主服务端口启用双向 TLS（mTLS）                                                                                          | 模板默认值：`true`                                                                                                                                 |
| `server.ops-tls.enable-https` | bool | 为 OPS（管理）端口启用 HTTPS                                                                                           | 模板默认值：`false`                                                                                                                                |
| `server.ops-tls.cert-file` | string | OPS TLS 证书文件路径                                                                                                | 模板默认值：空                                                                                                                                      |
| `server.ops-tls.key-file` | string | OPS TLS 私钥文件路径                                                                                                | 模板默认值：空                                                                                                                                      |
| `server.ops-tls.ca-cert-file` | string | 用于 mTLS 的 OPS CA 证书文件路径                                                                                       | 模板默认值：空                                                                                                                                      |
| `server.enable-ops-apis-on-service-port` | bool | 在 Ops 端口之外，将全部运维 API 同时注册到主服务端口                                                                               | 模板默认值：`true`。**生产环境推荐 `false`**                                                                                                              |
| `server.ops-port` | / | 未在 yaml 中列出，使用 `deploy.sh --ops-port` 设置，详见部署文档。                                                              |                                                                                                                                              |

---

## coord-routing

`coord-routing` 是可选项。只有本节和 pipeline 里的 `CoordRoute` 阶段同时存在才开启；只写其中一个，启动失败。没有结构体标签默认值：没写的数字是 0，半填的配置在启动期失败，而不是悄悄变成永不命中。

恰好一个角色空闲，且 prompt 长度落在该角色窗口内时，请求由那个空闲 worker 整段服务。`*-hybrid-max-input-tokens: 0` 表示该角色不参与本地路由，此时对应的 `*-local-min-input-tokens` 必须为 0。长度为 0 的 prompt 仍走原来的 PD。decode 下限配 0 时，实际命中区间是 `(0, 1024]`。

旧键 `local-min-input-tokens` 会在启动时报错。改用下面的分角色下限。

```yaml
coord-routing:
  strategy: imbalance
  metrics-stale-seconds: 3
  idle-max-waiting: 0
  idle-max-running-p: 0
  idle-max-running-d: 0
  idle-max-kv-usage: 0.50
  p-hybrid-max-inflight: 2
  d-hybrid-max-inflight: 2
  p-local-min-input-tokens: 32768
  d-local-min-input-tokens: 0
  p-hybrid-max-input-tokens: 204800
  d-hybrid-max-input-tokens: 1024
```

| 参数 | 类型 | 说明 | 约束 |
| --- | --- | --- | --- |
| `coord-routing.strategy` | string | 决策名 | 已注册的名字。推荐 `imbalance` |
| `coord-routing.metrics-stale-seconds` | float | 超过该秒数的快照不算空闲 | 必须 `> 0`。推荐 `3` |
| `coord-routing.idle-max-waiting` | int | 空闲 worker 允许的排队请求数 | 必须 `>= 0`。推荐 `0` |
| `coord-routing.idle-max-running-p` | int | 空闲 prefill 允许的在飞请求数 | 必须 `>= 0`。推荐 `0` |
| `coord-routing.idle-max-running-d` | int | 空闲 decode 允许的在飞请求数 | 必须 `>= 0`。推荐 `0` |
| `coord-routing.idle-max-kv-usage` | float | 空闲 worker 允许的 KV 占用 | 必须在 `[0, 1]`。推荐 `0.50`。省略为 `0` 时，只有 KV 占用为 0 才算空闲 |
| `coord-routing.p-hybrid-max-inflight` | int | 单个 prefill 上的本地在飞上限 | prefill 窗口打开时必须 `>= 1`。推荐 `2` |
| `coord-routing.d-hybrid-max-inflight` | int | 单个 decode 上的本地在飞上限 | decode 窗口打开时必须 `>= 1`。推荐 `2` |
| `coord-routing.p-local-min-input-tokens` | int | prefill 窗口下限，含本数 | `>= 0` 且不超过 prefill 上限。推荐 `32768` |
| `coord-routing.d-local-min-input-tokens` | int | decode 窗口下限，含本数 | `>= 0` 且不超过 decode 上限。推荐 `0`。长度为 0 仍走 PD |
| `coord-routing.p-hybrid-max-input-tokens` | int | prefill 窗口上限，含本数。`0` 表示 prefill 不参与本地路由 | `>= 0`。推荐 `204800` |
| `coord-routing.d-hybrid-max-input-tokens` | int | decode 窗口上限，含本数。`0` 表示 decode 不参与本地路由 | `>= 0`。推荐 `1024`，实际命中 `(0, 1024]` |

---

## meta-cleanup

`meta-cleanup` 段控制如何自动清理集群状态中陈旧的 instance 与 worker 元数据。该段为**可选项**，单机部署的默认模板中不包含。

```yaml
meta-cleanup:
  max-instances-down-time-sec: -1
  max-workers-down-time-sec: -1
```

| 参数 | 类型 | 说明 | 约束 |
| --- | --- | --- | --- |
| `meta-cleanup.max-instances-down-time-sec` | int | 当 leader 检测到某实例的 GET /health 检查失败持续时间超过该值，即认为该实例地址无法恢复，并将其从集群状态中移除。取值 <= 0 时关闭清理。运行期间，该超时会依据 `rest-health-common-config.interval-ms` 换算为失败次数（例如 900s 配合 10s 间隔折算为 90 次连续失败，向上取整；最小 6 次）。受分布式 leader 切换影响，健康检查进度可能不完全同步，从而延长实际清理时间 | 模板默认值：`-1`（单机模式，关闭清理）；`900`（etcd 模式，900s） |
| `meta-cleanup.max-workers-down-time-sec` | int | worker 可处于宕机状态的最大秒数，超过后该 worker 会被主动清理 | 取值 <= 0 时关闭清理。默认值：`300`（5 分钟）。最小值：`30`（30s） |

---

## logger

`logger` 段配置文件日志，包含轮转、压缩与保留策略。

```yaml
logger:
  enabled: true
  filename: /path/to/route-server.log
  max-size: 1024
  max-age: 15
  max-backups: 10
  local-time: true
  compress: true
  level: info
  write-format: text
```

| 参数 | 类型 | 说明 | 约束 |
| --- | --- | --- | --- |
| `logger.enabled` | bool | 启用文件日志。为 `false` 时日志写入 stderr | 默认值：`false`；模板默认值：`true` |
| `logger.filename` | string | 日志文件路径。为空时在系统临时目录中创建名为 `scheduler.logger` 的文件 | 默认值：空（使用临时目录） |
| `logger.max-size` | int | 单个日志文件在轮转前的最大大小，单位 MB | 默认值：`20` MB；模板默认值：`1024` MB |
| `logger.max-age` | int | 轮转日志文件的最大保留天数 | 默认值：`30` 天；模板默认值：`15` 天 |
| `logger.max-backups` | int | 轮转日志文件的最大保留个数。取值 <= 0 表示不限制 | 默认值：`50`；模板默认值：`10` |
| `logger.local-time` | bool | 备份文件时间戳使用本机本地时间。为 `false` 时使用 UTC 时间 | 默认值：`false`；模板默认值：`true` |
| `logger.compress` | bool | 使用 gzip 压缩轮转日志文件 | 默认值：`false`；模板默认值：`true` |
| `logger.level` | string | 日志输出级别 | 默认值：`info`。支持的值：`trace`、`debug`、`info`、`warn`、`error`、`fatal`、`panic` |
| `logger.write-format` | string | 日志输出格式 | 可选值：`json`、`text`。模板默认值：`text` |

---

## http

`http` 段配置全局 HTTP 客户端连接池，用于向推理实例和对等 RouteServer 节点发起出站请求。

```yaml
http:
  global-client-en: true
  request-timeout: 54000
  max-conns-per-host: 2048
  max-idle-conns: 40960
  max-idle-conns-per-host: 1800
  idle-conn-timeout: 110
  dialer-timeout: 600
  dialer-keepalive: 5
  tls:
    enable-https: false
    enable-mtls: true
    cert-file:
    key-file:
    ca-cert-file:
    skip-verify: false
```

| 参数 | 类型 | 说明 | 约束 |
| --- | --- | --- | --- |
| `http.request-timeout` | int | HTTP 请求超时，单位秒。设置过小可能导致推理请求超时 | 默认值：`100`；模板默认值：`54000`（15 小时） |
| `http.dialer-timeout` | int | TCP 拨号超时，单位秒。0 表示不限制 | 默认值：`5`；模板默认值：`600` |
| `http.dialer-keepalive` | int | TCP keep-alive 探针间隔，单位秒。负值表示关闭 keep-alive | 默认值：`-1`；模板默认值：`5` |
| `http.max-idle-conns` | int | 所有对等主机上空闲 keep-alive 连接的总数上限。仅在 `dialer-keepalive` > 0 时有效 | 默认值：`20480`；模板默认值：`40960` |
| `http.max-idle-conns-per-host` | int | 每个对等主机的空闲 keep-alive 连接数上限。设为 0 会重置为 transport 默认值 2。仅在 `dialer-keepalive` > 0 时有效 | 默认值：`1800`；模板默认值：`1800` |
| `http.idle-conn-timeout` | int | 空闲 keep-alive 连接超时，单位秒。超过该时长的空闲连接会被销毁 | 默认值：`10`；模板默认值：`110` |
| `http.max-conns-per-host` | int | 每个主机的连接总数上限（包含非空闲连接） | 模板默认值：`2048` |
| `http.tls.enable-https` | bool | 为 HTTP 客户端连接启用 HTTPS | 模板默认值：`false` |
| `http.tls.enable-mtls` | bool | 为 HTTP 客户端连接启用 mTLS | 模板默认值：`true` |
| `http.tls.cert-file` | string | HTTP 客户端连接的 TLS 证书文件路径 | 模板默认值：空 |
| `http.tls.key-file` | string | HTTP 客户端连接的 TLS 私钥文件路径 | 模板默认值：空 |
| `http.tls.ca-cert-file` | string | 用于 mTLS 的 CA 证书文件路径 | 模板默认值：空 |
| `http.tls.skip-verify` | bool | 为 HTTP 客户端连接跳过 TLS 证书校验 | 模板默认值：`false` |

---

## instance-manager

`instance-manager` 段是最复杂的配置块。它管理推理实例，包括健康检查、故障检测、路由表以及广播请求路由。

```yaml
instance-manager:
  required-roles:
    - prefill
    - decode
  instance-client-type: vllm
  health-check-config:
    event-threshold-interval-ms: 1000
    probe-infer-request-timeout-ms: 10000
    rest-health-common-config:
      interval-ms: 10000
      health-check-timeout-ms: 10000
      max-concurrency: 1000
      batch-size: 1
    fallback-health-checker:
      enable: true
      resume-check-interval-ms: 1000
      resume-time-ms: 90000
    available-checkers:
      rest-health:
        enable: true
        fail-on-500-directly: true
        consecutive-time: 3
      request-failures:
        enable: true
        consecutive-failure: false
        time-window-ms: 90000
        max-failures: 3
        failure-conditions:
          request-timeout: true
          status-codes:
            - 500
            - 501
            - 502
            - 503
        fake-request:
          enable: true
          time-interval-ms: 12000
          max-fake-request-time: 5
          failure-threshold: 3
      link-down-failures:
        enable: false
        time-window-ms: 90000
        max-failures: 3
      link-blacklist-config:
        enable: true
        link-block-threshold: 3
        entry-gc-interval-ms: 1000
        link-block-windows-ms: 180000
        link-block-tag-valid-time-ms: 600000
        instance-block-ratio: 70
      hang-checker:
        enable: true
        leader-check-interval-ms: 1000
        worker-hang-tag-valid-time-ms: 10000
        enable-probe-before-hang-check: true
        hang-probe-interval-ms: 60000
        hang-probe-timeout-ms: 10000
        max-consecutive-invalid-probes: 3
        hang-message-keywords:
          - "KV cache load failed"
        hang-thresholds:
          - role: encode
            max-non-active-time-ms: -1
            skip-non-stream: false
          - role: prefill
            max-non-active-time-ms: -1
            skip-non-stream: false
          - role: decode
            max-non-active-time-ms: 60000
            skip-non-stream: true
    unavailable-checkers:
      rest-health:
        enable: true
        consecutive-time: 9
        enable-probe-infer-request: true
  broadcast-request-whitelist:
    - path: "/version"
      methods: ["GET"]
    - path: "/v1/models"
      methods: ["GET"]
    # ... (full list in template)
```

| 参数 | 类型 | 说明 | 约束 |
| --- | --- | --- | --- |
| `instance-manager.required-roles` | List | 集群中必须存在的 worker 角色 | 模板默认值：`["prefill", "decode"]` |
| `instance-manager.instance-client-type` | string | RouteServer 所连接的推理引擎类型 | 目前仅支持 `vllm` |
| `instance-manager.broadcast-request-whitelist` | List | 需要广播到所有 vLLM 实例的请求白名单。每项指定 `path`（URL 路径）与 `methods`（HTTP 方法）。不建议修改 | 模板默认值包含：GET `/version`、`/v1/models`、`/server_info`、`/load`、`/ping`、`/is_sleeping`；POST `/start_profile`、`/stop_profile`、`/ping`、`/tokenize`、`/detokenize`、`/v1/embeddings`、`/pooling`、`/classify`、`/score`、`/v1/score`、`/v1/audio/transcriptions`、`/rerank`、`/v1/rerank`、`/v2/rerank`、`/reset_prefix_cache`、`/sleep`、`/wake_up`、`/invocations`、`/v1/load_lora_adapter`、`/v1/unload_lora_adapter` |
| `instance-manager.health-check-config` | HealthCfg | 配置 RouteServer leader 上的健康检查行为 | 见下文各嵌套小节 |

### 健康检查配置（HealthCfg）

健康检查系统通过多种检查器判定实例健康状态。详细说明请参阅 FAQ 中的"RouteServer 健康检查机制"一节。

| 参数 | 类型 | 说明 | 约束 |
| --- | --- | --- | --- |
| `event-threshold-interval-ms` | int | 多节点模式下，从 worker 向 leader 同步健康检查事件的间隔 | 默认值：`1000`（1s）。最小值：`10`。模板默认值：`1000` |
| `rest-health-common-config` | RestHealthCommonCfg | `available-checkers.rest-health` 与 `unavailable-checkers.rest-health` 共用的 HTTP 健康检查配置 | 见下文小节 |
| `probe-infer-request-timeout-ms` | int | 健康检查期间发送的探针推理请求超时 | 默认值：`10000`（10s）。模板默认值：`10000` |
| `fallback-health-checker` | FallbackHealthChecker | 回退的本地健康检查机制配置 | 见下文小节 |
| `available-checkers` | AvailCheckersConfig | 判定健康实例何时应被标记为不健康的检查器机制集合。任一检查器满足其失败条件，就会将该实例标记为不健康 | 见下文各小节 |
| `unavailable-checkers` | UnavailCheckersConfig | 判定不健康实例何时应恢复为健康状态的检查器机制集合 | 见下文各小节 |

#### REST 健康检查公共配置（RestHealthCommonCfg）

本小节配置 RouteServer leader 如何调用 prefill/decode 实例上的 GET /health 端点。

| 参数 | 类型 | 说明 | 约束 |
| --- | --- | --- | --- |
| `interval-ms` | int | 对每个实例连续发起 GET /health 请求的间隔 | 默认值：`10000`（10s）。最小值：`100`。模板默认值：`10000`（10s） |
| `health-check-timeout-ms` | int | 每次 GET /health 请求的超时 | 默认值：`10000`（10s）。最小值：`1`。模板默认值：`10000`（10s） |
| `max-concurrency` | int | 批量健康检查请求的最大并发数 | 默认值：`1000`。最小值：`1`。模板默认值：`1000` |
| `batch-size` | int | 每批检查的实例数。设置为 > 1 可能因连接超时导致健康检查延迟。推荐值：`1` | 默认值：`1`。最小值：`1`。模板默认值：`1` |

#### 回退健康检查器（FallbackHealthChecker）

一种本地回退健康检查机制，用于在健康检查数据不可靠时隔离实例。

| 参数 | 类型 | 说明 | 约束 |
| --- | --- | --- | --- |
| `fallback-health-checker.enable` | bool | 启用回退的本地健康机制 | 默认值：`false`；模板默认值：`true` |
| `fallback-health-checker.resume-check-interval-ms` | int | 检查被回退隔离的实例是否应移出隔离列表的间隔 | 模板默认值：`1000`（1s） |
| `fallback-health-checker.resume-time-ms` | int | 实例在回退隔离列表中保留多久（毫秒）后才被移除 | 默认值：`90000`（90s）。最小值：`1`。模板默认值：`90000` |

#### 可用性检查器（AvailCheckersConfig）

##### 1. REST 健康检查器（rest-health）

定期对每个实例调用 GET /health 端点。若该端点连续多次未返回 HTTP 200，则将该实例标记为不健康。

| 参数 | 类型 | 说明 | 约束 |
| --- | --- | --- | --- |
| `available-checkers.rest-health.enable` | bool | 是否依据 GET /health 失败将健康实例标记为不健康 | 默认值：`false`；模板默认值：`true` |
| `available-checkers.rest-health.fail-on-500-directly` | bool | 当 GET /health 端点明确返回 HTTP 500 时，是立即判定失败（`true`），还是等待达到配置的连续失败次数（`false`） | 默认值：`false`；模板默认值：`true` |
| `available-checkers.rest-health.consecutive-time` | int | 实例由健康转为不健康（或反之）所需的连续失败健康检查次数。这可避免网络状况不佳时状态抖动 | 默认值：`3`。最小值：`1`。模板默认值：`3` |

##### 2. 请求失败检查器（request-failures）

在滑动时间窗口内监测推理请求错误。若窗口内错误数超过阈值，则将该实例标记为不健康。

| 参数 | 类型 | 说明 | 约束 |
| --- | --- | --- | --- |
| `available-checkers.request-failures.enable` | bool | 是否将推理请求错误作为健康指标加以分析 | 默认值：`false`；模板默认值：`true` |
| `available-checkers.request-failures.time-window-ms` | int | 滑动时间窗口，单位毫秒。窗口之外的错误被忽略 | 默认值：`10000`（10s）。最小值：`1`。模板默认值：`90000`（90s） |
| `available-checkers.request-failures.consecutive-failure` | bool | 为 `true` 时，窗口内的成功请求会重置失败计数。为 `false` 时，窗口内的失败会累计，不受其间成功请求影响 | 默认值：`false`；模板默认值：`false` |
| `available-checkers.request-failures.max-failures` | int | 触发故障检测机制的时间窗口内失败次数 | 默认值：`3`。最小值：`1`。模板默认值：`3` |
| `available-checkers.request-failures.failure-conditions.request-timeout` | bool | 请求超时是否计为一次失败 | 默认值：`false`；模板默认值：`true` |
| `available-checkers.request-failures.failure-conditions.status-codes` | List | 计为失败的 HTTP 状态码 | 默认值：`[500, 501, 502, 503]`；模板默认值：`[500, 501, 502, 503]` |
| `available-checkers.request-failures.fake-request.enable` | bool | 当窗口内的失败全部是 HTTP 500 响应时，发送探针请求以确认实例是否真的故障。为 `false` 时，任何错误达到阈值即立刻将实例标记为不健康 | 默认值：`false`；模板默认值：`true` |
| `available-checkers.request-failures.fake-request.time-interval-ms` | int | 探针请求之间的间隔 | 默认值：`60000`（60s）。最小值：`1`。模板默认值：`12000`（12s） |
| `available-checkers.request-failures.fake-request.max-fake-request-time` | int | 探针请求的最大重试次数。若探针在该次数后仍持续失败，系统会记录一条警告日志并中止（此时集群应已因 prefill 问题而降级） | 默认值：`5`。必须 >= `failure-threshold`。模板默认值：`5` |
| `available-checkers.request-failures.fake-request.failure-threshold` | int | 确认实例存在问题前所需的探针请求失败次数（连接失败或 5xx） | 默认值：`3`。最小值：`1`。模板默认值：`3` |

##### 3. 卡死检查器（hang-checker）

检测特定角色的实例是否已卡死（hang）。若实例在超过配置阈值的时间内未处理任何活跃请求，则视为卡死。

> [!WARNING]
> 卡死阈值必须依据实际业务需求以及可接受的 decode 响应时延上限来设置。实例一旦超过该阈值，就会被标记为失败（状态：`Hang`），集群至少需要 80 秒才能将其恢复。在此期间，RouteServer 的健康端点返回 HTTP 500。

| 参数 | 类型 | 说明 | 约束 |
| --- | --- | --- | --- |
| `available-checkers.hang-checker.enable` | bool | 是否将实例卡死作为健康检查判据 | 默认值：`false`；模板默认值：`true` |
| `available-checkers.hang-checker.leader-check-interval-ms` | int | leader 上卡死检查循环的间隔 | 默认值：`1000`（1s）。最小值：`10`。模板默认值：`1000` |
| `available-checkers.hang-checker.worker-hang-tag-valid-time-ms` | int | 多 RouteServer 部署时，worker 会向 leader 同步卡死实例列表。若某实例在该时长内未收到卡死更新，则认为该实例已恢复。单节点模式下不适用 | 默认值：`10000`（10s）。最小值：`100`。模板默认值：`10000` |
| `available-checkers.hang-checker.enable-probe-before-hang-check` | bool | 在标记实例卡死前先向其发送探针请求，以确认卡死状态 | 模板默认值：`true` |
| `available-checkers.hang-checker.hang-probe-interval-ms` | int | 卡死探针请求之间的间隔 | 模板默认值：`60000`（60s） |
| `available-checkers.hang-checker.hang-probe-timeout-ms` | int | 每次卡死探针请求的超时 | 模板默认值：`10000`（10s） |
| `available-checkers.hang-checker.max-consecutive-invalid-probes` | int | 采取动作前允许的连续无效探针结果最大次数 | 模板默认值：`3` |
| `available-checkers.hang-checker.hang-message-keywords` | List | worker 消息中表示卡死状态的关键字 | 模板默认值：`["KV cache load failed"]` |
| `available-checkers.hang-checker.hang-thresholds[].role` | string | 该阈值适用的实例角色 | 必须为 `encode`、`prefill` 或 `decode` |
| `available-checkers.hang-checker.hang-thresholds[].max-non-active-time-ms` | int | 最大非活跃时间阈值，单位毫秒。取值 <= 0 时对该角色跳过卡死检查 | 模板默认值：`encode`：`-1`（跳过）、`prefill`：`-1`（跳过）、`decode`：`60000`（60s） |
| `available-checkers.hang-checker.hang-thresholds[].skip-non-stream` | bool | 卡死检测是否跳过非流式请求。为 `true` 时，该角色上的非流式请求不参与卡死检测 | 默认值：`false`。模板默认值：`encode`：`false`、`prefill`：`false`、`decode`：`true` |

##### 4. 链路中断失败检查器（link-down-failures）

对每个 prefill-decode（PD）链路对，若在 Y 秒窗口内出现 X 次链路中断错误，则为该请求提供服务的 prefill 实例会被标记为不健康。

| 参数 | 类型 | 说明 | 约束 |
| --- | --- | --- | --- |
| `available-checkers.link-down-failures.enable` | bool | 是否将链路中断错误作为健康指标加以分析 | **不可与 `link-blacklist-config.enable` 同时为 `true`**。默认值：`false`；模板默认值：`false` |
| `available-checkers.link-down-failures.time-window-ms` | int | 统计链路中断错误的滑动时间窗口，单位毫秒 | 默认值：`10000`（10s）。最小值：`1`。模板默认值：`90000`（90s） |
| `available-checkers.link-down-failures.max-failures` | int | 触发故障检测的窗口内链路中断失败次数 | 默认值：`3`。最小值：`1`。模板默认值：`3` |

##### 5. 链路黑名单配置（link-blacklist-config）

对每个 prefill-decode（PD）链路对，若在 Y 秒窗口内出现 Z 次链路中断错误，则认为该链路故障并进行链路级隔离。若某实例被阻断的链路占比足够高，则该实例本身也会被隔离。

| 参数 | 类型 | 说明 | 约束 |
| --- | --- | --- | --- |
| `available-checkers.link-blacklist-config.enable` | bool | 启用链路黑名单管理模块 | **不可与 `link-down-failures.enable` 同时为 `true`**。默认值：`false`；模板默认值：`true` |
| `available-checkers.link-blacklist-config.link-block-threshold` | int | 触发拉黑 PD 链路对的窗口内 PD 链路中断错误次数 | 必须 > 0。默认值：`3`；模板默认值：`3` |
| `available-checkers.link-blacklist-config.entry-gc-interval-ms` | int | 清理过期黑名单条目的垃圾回收间隔 | 必须 > 10。默认值：`1000`（1s）；模板默认值：`1000` |
| `available-checkers.link-blacklist-config.link-block-windows-ms` | int | 记录 PD 链路中断错误的时间窗口。早于该窗口的记录会被丢弃 | 必须 > 100。默认值：`90000`；模板默认值：`180000`（180s） |
| `available-checkers.link-blacklist-config.link-block-tag-valid-time-ms` | int | PD 链路黑名单条目的有效期 | 必须 > 100。默认值：`90000`；模板默认值：`600000`（10 分钟） |
| `available-checkers.link-blacklist-config.instance-block-ratio` | int | 触发实例被视为不健康的阈值：被阻断链路占总链路的比例（百分比） | 必须位于 `[0, 100]`。默认值：`70`；模板默认值：`70` |

#### 不可用恢复检查器（UnAvailCheckersConfig）

目前仅提供一种机制：若 GET /health 端点连续成功 N 次，则不健康实例恢复为健康状态，并被移出调度隔离。

| 参数 | 类型 | 说明 | 约束 |
| --- | --- | --- | --- |
| `unavailable-checkers.rest-health.enable` | bool | 是否依据 GET /health 连续成功响应恢复不健康实例 | 默认值：`false`；模板默认值：`true`。**不应设为 `false`**，否则被标记为失败的实例无法恢复 |
| `unavailable-checkers.rest-health.consecutive-time` | int | 实例恢复为健康状态所需的连续成功健康检查次数 | 默认值：`3`。最小值：`1`。模板默认值：`9`（按 10s 间隔计，即连续成功 90s） |
| `unavailable-checkers.rest-health.enable-probe-infer-request` | bool | 在将不健康实例恢复为健康状态前，是否先执行一次探针推理请求 | 默认值：`false`；模板默认值：`true` |

---

## apc-manager 与 apc-scheduler

`apc-manager` 段配置自动前缀缓存（Automatic Prefix Caching，APC），它为重复出现的 prompt 前缀缓存并复用 KV cache 计算。`apc` 调度算法（在 `scheduler-manager` 中配置）与该模块协同工作。

```yaml
apc-manager:
  enable: true
  strategies:
    hash:
      block-size-bytes: 8000
      max-blocks: 32
      match-threshold: 0.0
      max-entries: 32768
      max-memory-mb: 512
      cache-ttl-sec: 3600
```

| 参数 | 类型 | 说明 | 约束 |
| --- | --- | --- | --- |
| `apc-manager.enable` | bool | 启用 APC 管理器 | 模板默认值：`true` |
| `apc-manager.strategies.hash.block-size-bytes` | int | 滚动哈希策略的块大小，单位字节 | 模板默认值：`8000` |
| `apc-manager.strategies.hash.max-blocks` | int | 每个请求的最大块数 | 模板默认值：`32` |
| `apc-manager.strategies.hash.match-threshold` | float | 最小前缀匹配比例阈值（0.0-1.0）。匹配比例超过该阈值时触发 APC 调度 | 模板默认值：`0.0` |
| `apc-manager.strategies.hash.max-entries` | int | 缓存条目数上限 | 模板默认值：`32768` |
| `apc-manager.strategies.hash.max-memory-mb` | int | APC 缓存的最大内存占用，单位 MB | 模板默认值：`512` |
| `apc-manager.strategies.hash.cache-ttl-sec` | int | 缓存条目 TTL，单位秒 | 模板默认值：`3600`（1 小时） |

---

## conductor

`conductor` 段配置可选的 [Mooncake Conductor](https://github.com/kvcache-ai/Mooncake) 集成。
启用后，RouteServer leader 会把选定角色的推理实例注册到 Conductor，并构建前缀命中查询客户端。
Conductor 维护所有已注册实例发布的 KV cache block 索引，回答每个实例已经持有多长的前缀。

本分支只提供注册与查询能力，尚未把命中结果接入调度决策。查询通过仅监听 ops 端口的诊断接口暴露：

```
POST /internal/conductor/query
Content-Type: application/json

{"model":"qwen3-0.6b","token_ids":[1,2,3,4],"cache_salt":"可选"}
```

响应给出每个实例的 `device`、`cpu_local`、`cpu_share`、`disk` 累计命中。集成未启用时该接口不存在。

```yaml
conductor:
  enabled: false
  base-url: http://127.0.0.1:13333
  request-timeout-ms: 1000
  query-timeout-ms: 100
  retry-interval-ms: 30000
  block-size: 128
  register-roles: prefill
```

| 参数 | 类型 | 说明 | 约束 |
| --- | --- | --- | --- |
| `conductor.enabled` | bool | 启用 Conductor 注册与命中查询 | 默认值：`false`；模板默认值：`false` |
| `conductor.base-url` | string | Conductor 服务的绝对 HTTP(S) URL | 默认值：`http://127.0.0.1:13333`。必须包含 scheme 与 host，不能带 userinfo、query 或 fragment |
| `conductor.request-timeout-ms` | int | 单次 `/register` 或 `/unregister` 的超时 | 默认值：`1000`；`0` 表示使用默认值，负数报错 |
| `conductor.query-timeout-ms` | int | 单次 `/query` 的超时。该调用位于请求路径上，应保持较短 | 默认值：`100`；`0` 表示使用默认值，负数报错 |
| `conductor.retry-interval-ms` | int | 非瞬时错误导致注册失败后的低频重试周期 | 默认值：`30000`；`0` 表示使用默认值，负数报错 |
| `conductor.block-size` | int | 注册和前缀查询共用的 KV cache block size | 默认值：`128`。**必须与推理实例实际值一致**；混合注意力模型可能是 `1024` |
| `conductor.register-roles` | string | 需要注册的角色，逗号分隔；空值等价于 `prefill` | 允许值：`prefill`、`decode`；未知角色启动报错 |

---

## scheduler-manager

`scheduler-manager` 段配置面向不同实例角色的请求调度策略，包括基于 SLO 的调度与远程调度。

```yaml
scheduler-manager:
  system-prompt-length: 100
  slo-scheduling:
    slo-enable: false
    slo-fallback-enable: true
    slo-group-healthy-threshold: 1
  schedulers:
    - instance-role: encode
      scheduler-name: least-total-load
      params:
        verbose: false
    - instance-role: prefill
      scheduler-name: apc
      params:
        default-batch-size: 64
        prefix-load-base: 1
    - instance-role: decode
      scheduler-name: abc6
      params:
        metrics-overdue-interval-ms: 100
        update-interval-ms: -1
        needStats: "vllm:num_requests_running,vllm:num_requests_waiting"
  remote-schedule:
    enabled: true
    remote-type: endpoint
    remote-endpoint: <remote_endpoint>
    need-content: false
    timeout-ms: 1000
    cleanup-interval-ms: 60000
```

| 参数 | 类型 | 说明 | 约束 |
| --- | --- | --- | --- |
| `scheduler-manager.system-prompt-length` | int | 用于调度决策的系统 prompt 长度 | 模板默认值：`100` |
| `scheduler-manager.schedulers` | List | 按实例角色划分的调度器配置。每个角色必须定义且只能定义一个调度器；同一角色配置多个会报错 | 见下文 ScheduleCfg |
| `scheduler-manager.remote-schedule.enabled` | bool | 启用远程调度（将调度决策委托给外部端点） | 模板默认值：`true` |
| `scheduler-manager.remote-schedule.remote-type` | string | 远程调度器类型 | 可选值：`endpoint` 或 `leader` |
| `scheduler-manager.remote-schedule.remote-endpoint` | string | 远程调度器端点 URL | 启用远程调度时必填 |
| `scheduler-manager.remote-schedule.need-content` | bool | 在远程调度请求中携带请求内容 | 默认值：`false` |
| `scheduler-manager.remote-schedule.timeout-ms` | int | 远程调度请求超时，单位毫秒 | 默认值：`1000` |
| `scheduler-manager.remote-schedule.cleanup-interval-ms` | int | 清理陈旧远程调度记录的间隔 | 默认值：`60000`（60s） |

### ScheduleCfg

| 参数 | 类型 | 说明 | 约束 |
| --- | --- | --- | --- |
| `instance-role` | string | 该调度器处理的实例角色 | 有效值：`encode`、`prefill`、`decode` |
| `scheduler-name` | string | 调度算法名称 | 支持的算法：`round-robin`（轮询转发）、`least-total-load`（按请求长度做负载均衡，适用于 prefill）、`apc`（面向 prefill 的 APC 感知调度）、`abc6`（面向 decode 的高级调度）、`StatsPrintingRandom`（带日志的随机分发，仅用于测试）。模板默认值：`encode`：`least-total-load`、`prefill`：`apc`、`decode`：`abc6` |
| `params` | map | 调度器专属参数，为任意键值对 | 各算法参数见下文 |

### SLO 调度配置

| 参数 | 类型 | 说明 | 约束 |
| --- | --- | --- | --- |
| `scheduler-manager.slo-scheduling.slo-enable` | bool | 启用基于 SLO 的调度 | 模板默认值：`false` |
| `scheduler-manager.slo-scheduling.slo-fallback-enable` | bool | 为 `true` 时，若没有实例组满足 SLO 要求，则回退到任一可用组。为 `false` 时，若没有合格组具备足够健康实例，则调度失败 | 模板默认值：`true` |
| `scheduler-manager.slo-scheduling.slo-group-healthy-threshold` | int | SLO 组被视为可用所需的最少健康实例数 | 必须 > 0。模板默认值：`1` |

### 各算法参数

**StatsPrintingRandom**（仅用于测试）：

- `needStats`：要采集并记录的指标名称列表，以逗号分隔。指标每 100ms 同步一次，并在每次更新时以 trace 级别打印。默认值：空。

**apc**（prefill 调度器）：

- `default-batch-size`：当 prefill 实例未上报其有效批大小时使用的默认批大小。默认值：`64`。
- `prefix-load-base`：预测新请求影响时叠加的基础负载。设为 `0` 时，前缀完全匹配的请求不增加额外负载。默认值：`1`。

**abc6**（decode 调度器）：

- `metrics-overdue-interval-ms`：指标过期时间间隔。同步时早于该时长的指标会被丢弃。默认值：`100`。
- `update-interval-ms`：指标更新间隔。启用 Prometheus 指标时应设为正值；`-1` 表示关闭指标更新。默认值：`-1`。
- `needStats`：要采集的 Prometheus 指标名称列表，以逗号分隔（例如 `vllm:num_requests_running,vllm:num_requests_waiting`）。

---

## monitor

`monitor` 段配置监控客户端。当前标准配置使用 `none` 关闭监控。

```yaml
monitor:
  monitor-client-type: none
```

| 参数 | 类型 | 说明 | 约束 |
| --- | --- | --- | --- |
| `monitor.monitor-client-type` | string | 监控客户端类型 | 支持的值：`none`、`redis`、`prometheus`。模板默认值：`none` |

---

## metrics

`metrics` 段配置性能指标采集。

```yaml
metrics:
  enable: true
  duration: 60
  metrics-collector-config-file-name: metrics_config_vllm_0.11.0.yaml
```

| 参数 | 类型 | 说明 | 约束 |
| --- | --- | --- | --- |
| `metrics.enable` | bool | 启用指标采集 | 默认值：`false`；模板默认值：`true` |
| `metrics.duration` | int | 指标采集间隔，单位秒 | 模板默认值：`60` |
| `metrics.metrics-collector-config-file-name` | string | 指标采集器配置的文件名 | 模板默认值：`metrics_config_vllm_0.11.0.yaml` |

---

## cluster

`cluster` 段配置集群成员关系、协调与 leader 选举。对于单机（单节点）部署，deploy.sh 脚本会自动生成这些值，通常不建议修改。

```yaml
cluster:
  type: singleton
  verbose: true
  role_candidate: true
  peer_http:
    dialer-timeout: 600
    dialer-keepalive: 5
    request-timeout: 14400
    max-idle-conns: 40960
    max-idle-conns-per-host: 1800
    idle-conn-timeout: 110
    max-conns-per-host: 2048
  etcd-health-expire-time-ms: 0
  etcd:
    etcd-endpoints:
      - <etcd_endpoint1>
      - <etcd_endpoint2>
    dial-timeout-ms: 5000
    retry-time: 10
    retry-interval-sec: 5
    lease-ttl-sec: 3
    lease-refresh-interval-ms: 1000
    tls:
      enable-https: false
      enable-mtls: true
      cert-file:
      key-file:
      ca-cert-file:
      skip-verify: false
```

| 参数 | 类型 | 说明 | 约束 |
| --- | --- | --- | --- |
| `cluster.type` | string | 集群管理模式。`singleton`：单机模式，不做状态同步（仅使用 `verbose`，其余集群配置被忽略）。`etcd`：使用外部 etcd 服务进行 leader 选举与状态同步（`peer_http` 与 `etcd` 配置生效） | 可选值：`singleton`、`etcd`。默认值：`singleton` |
| `cluster.verbose` | bool | 为集群操作启用额外的调试日志 | 默认值：`false`；模板默认值：`true` |
| `cluster.role_candidate` | bool | 该 RouteServer 节点是否有资格成为 leader | 默认值：`false`。当 `cluster.type` 为 `singleton` 时，该项被强制为 `true`。模板默认值：`true` |
| `cluster.peer_http` | HttpConfig | 用于节点间点对点通信的 HTTP 客户端配置。用于向种子节点发送加入请求，以及将非 leader 节点的请求转发给 leader 节点。未定义时使用顶层 `http` 配置 | 模板默认值：见上方 YAML |
| `cluster.etcd-health-expire-time-ms` | int | etcd 健康缓存的过期时间。缓存过期后，来自 etcd 的故障数据不再被信任 | 默认值：`0`（为 0 或未设置时，使用 2 倍 `FailureSnapshotIntervalMs`；回退值约 40s） |

### Etcd 配置（EtcdConfig）

| 参数 | 类型 | 说明 | 约束 |
| --- | --- | --- | --- |
| `cluster.etcd.etcd-endpoints` | List | etcd 服务器端点 | 必填；不可为空 |
| `cluster.etcd.dial-timeout-ms` | int | etcd 客户端首次拨号的超时 | 默认值：`10000`（10s）；模板默认值：`5000`（5s） |
| `cluster.etcd.retry-time` | int | etcd 初始化、重连、状态恢复与服务注册的重试次数 | 默认值：`10`。必须 >= 0。模板默认值：`10` |
| `cluster.etcd.retry-interval-sec` | int | 各次重试之间的等待间隔 | 默认值：`5`。范围：`[0, 60]`。模板默认值：`5` |
| `cluster.etcd.lease-ttl-sec` | int | 用于 leader 选举与服务注册的 etcd 会话 TTL。若网络隔离超过该时长，会话过期，节点被逐出 leader 地位 | 默认值：`3`。范围：`[1, 600]`。模板默认值：`3`（秒） |
| `cluster.etcd.lease-refresh-interval-ms` | int | 刷新 etcd 租约的间隔 | 默认值：`1000`。范围：`[50, 60000]`。模板默认值：`1000` |
| `cluster.etcd.tls.enable-https` | bool | 为 etcd 连接启用 HTTPS | 模板默认值：`false` |
| `cluster.etcd.tls.enable-mtls` | bool | 为 etcd 连接启用 mTLS | 模板默认值：`true` |
| `cluster.etcd.tls.cert-file` | string | etcd 客户端 TLS 证书文件路径 | 模板默认值：空 |
| `cluster.etcd.tls.key-file` | string | etcd 客户端 TLS 私钥文件路径 | 模板默认值：空 |
| `cluster.etcd.tls.ca-cert-file` | string | 用于 mTLS 的 etcd CA 证书文件路径 | 模板默认值：空 |
| `cluster.etcd.tls.skip-verify` | bool | 为 etcd 连接跳过 TLS 证书校验 | 模板默认值：`false` |

---

## probe

`probe` 段配置 Kubernetes 风格的 readiness 探针，以及用于实例健康监控的熔断设置。

```yaml
probe:
  readiness:
    prefill: 1
    decode: 1
  circuit-breaker:
    enable: true
    failure-rate-threshold: 0.8
    min-request-count: 10
    check-window-sec: 600
```

| 参数 | 类型 | 说明 | 约束 |
| --- | --- | --- | --- |
| `probe.readiness.prefill` | int | RouteServer 报告就绪所需的最少健康 prefill 实例数 | 必须 > 0。默认值：`1` |
| `probe.readiness.decode` | int | RouteServer 报告就绪所需的最少健康 decode 实例数 | 必须 > 0。默认值：`1` |
| `probe.circuit-breaker.enable` | bool | 启用熔断功能 | 模板默认值：`true` |
| `probe.circuit-breaker.failure-rate-threshold` | float | 失败率阈值。当检查窗口内的失败率超过该值时，熔断器触发 | 必须位于 `(0.0, 1.0]`。默认值：`0.6`。模板默认值：`0.8` |
| `probe.circuit-breaker.min-request-count` | int | 评估是否触发熔断前，检查窗口内所需的最小请求数 | 必须 > 0。默认值：`10` |
| `probe.circuit-breaker.check-window-sec` | int | 熔断检查窗口时长，单位秒 | 必须 > 0。默认值：`600`（10 分钟） |

---

## peer-client

`peer-client` 段配置用于 RouteServer 节点之间点对点通信的 HTTP 客户端。

```yaml
peer-client:
  http:
    request-timeout: 14400
    max-conns-per-host: 2048
    max-idle-conns: 40960
    max-idle-conns-per-host: 1800
    idle-conn-timeout: 110
    dialer-timeout: 600
    dialer-keepalive: 5
  send-with-domain: false
  domain: leader_domain
```

| 参数 | 类型 | 说明 | 约束 |
| --- | --- | --- | --- |
| `peer-client.http.request-timeout` | int | HTTP 请求超时，单位秒。设置过小可能导致请求超时 | 模板默认值：`14400` |
| `peer-client.http.dialer-timeout` | int | TCP 拨号超时，单位秒。0 表示不限制 | 模板默认值：`600` |
| `peer-client.http.dialer-keepalive` | int | TCP keep-alive 探针间隔，单位秒。负值表示关闭 keep-alive | 模板默认值：`5` |
| `peer-client.http.max-conns-per-host` | int | 每个主机的连接总数上限 | 模板默认值：`2048` |
| `peer-client.http.max-idle-conns` | int | 所有对等主机上空闲 keep-alive 连接的总数上限。仅在 `dialer-keepalive` > 0 时有效 | 模板默认值：`40960` |
| `peer-client.http.max-idle-conns-per-host` | int | 每个主机的空闲 keep-alive 连接数上限。设为 0 会重置为 transport 默认值 2。仅在 `dialer-keepalive` > 0 时有效 | 模板默认值：`1800` |
| `peer-client.http.idle-conn-timeout` | int | 空闲 keep-alive 连接超时，单位秒 | 模板默认值：`110` |
| `peer-client.send-with-domain` | bool | 为 `true` 时，使用域名而非 IP 地址访问 leader | 模板默认值：`false` |
| `peer-client.domain` | string | `send-with-domain` 为 `true` 时使用的域名。启用该设置时不可为空 | 模板默认值：`leader_domain` |

---
