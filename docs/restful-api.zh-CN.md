# RouteServer API 文档

[English](restful-api.md) | [简体中文](restful-api.zh-CN.md)

## 端口说明

每个 API 描述中的**监听端口**表示该 API 监听在哪个端口：

- **Service Port**：对应 `deploy.sh` 的 `--port` 选项，用于业务推理请求。
- **Operations Port**：对应 `deploy.sh` 的 `--ops-port` 选项，用于管理类请求（例如强制设置就绪状态）。
- 部署时若未指定 `--ops-port`，Operations Port 默认为 **Service Port + 10**。

## 目录

- [1. 状态查询 API](#1-状态查询-api)
- [2. 健康检查 API](#2-健康检查-api)
- [3. 推理请求 API](#3-推理请求-api)
- [4. 调度请求 API](#4-调度请求-api)
- [5. 实例管理 API](#5-实例管理-api)
- [6. 请求管理 API](#6-请求管理-api)
- [7. 集群管理 API](#7-集群管理-api)
- [8. 监控指标 API](#8-监控指标-api)
- [9. 健康探针 API](#9-健康探针-api)
- [10. 版本信息 API](#10-版本信息-api)
- [11. 事件同步请求 API](#11-事件同步请求-api)
- [12. 黑名单管理 API](#12-黑名单管理-api)
- [13. 动态代理 API](#13-动态代理-api)

## 1. 状态查询 API

### GET /status

**监听端口**：Service Port

**功能**：检查被请求的 RouteServer 进程下所有推理实例的健康状态，同时覆盖 Prefill 和 Decode 实例。返回全部实例的状态信息，并输出集群整体状态。

**处理者**：被请求的 RouteServer 接收后转发给集群 Leader 处理。

**调用方**：运维平台

**约束/条件**：
- 只有当 leader 存活时，才能成功访问集群内的 RouteServer
- Leader 不可用时，该端点不可用，会返回非 200 状态码

**参数**：该端点无需请求参数

**核心业务逻辑**：
- 系统获取全部实例列表
- 逐个检查每个实例的健康状态，返回实例的访问 URI（IP 地址与端口号）、健康状态（running 或 failed）、错误信息（状态健康时置为 "healthy"）、角色类型（prefill 或 decode）以及告警信息
- 集群状态判定规则如下：
    - deploying：不存在至少 1p1d running 的情况，且所有非 running 实例均处于 deploying 状态（不存在 Fault 状态）
    - fault：不存在至少 1p1d running 的情况，且存在 Fault 状态实例
    - concerning：存在至少 1p1d running 的情况，且存在 fault 实例
    - running：所有实例均为 running

**响应示例**：
```json
{
    "status": "running",
    "prefills": [
        {
            "status": "running",
            "url": "http://*.*.*.*:***",
            "role": "prefill",
            "pod_name": "",
            "fault message": "healthy",
            "slo_tag": "default",
            "alert": ""
        }
    ],
    "decodes": [
        {
            "status": "running",
            "url": "http://*.*.*.*:***",
            "role": "decode",
            "pod_name": "",
            "slo_tag": "default",
            "fault message": "healthy",
            "alert": ""
        }
    ]
}
```

## 2. 健康检查 API

### GET /health

**监听端口**：Service Port

**功能**：检查被请求的 RouteServer 进程下所有推理实例是否健康，覆盖 Prefill 与 Decode 两种实例类型。根据检查结果返回相应的 HTTP 状态码。

**处理者**：被请求的 RouteServer 接收后转发给集群 Leader 处理。

**调用方**：监控系统、运维平台

**约束/条件**：
- 需要通过 adminLeaderProxyMiddleware 获得可用的集群 Leader
- Leader 不可用时，该端点不可用，会返回非 200 状态码

**参数**：无请求参数

**核心业务逻辑**：
- 系统获取全部实例列表
- 逐个检查每个实例的可用状态
- 若全部实例可用则返回 200
- 若任一实例不可用则返回 500

**响应**：该端点仅返回裸 HTTP 状态码，不含 JSON 体。

| 场景 | 结果 |
|----------|--------|
| 全部实例可用 | HTTP 200 OK |
| 任一实例不可用 | HTTP 500 Internal Server Error |
| 全部实例不可用 | HTTP 500 Internal Server Error |
| 配置文件中未配置任何实例 | HTTP 500 Internal Server Error |

## 3. 推理请求 API

推理端点支持 OpenAI 标准 API 格式。RouteServer 依据调度策略将请求代理至相应的推理实例。

**错误码**：

| 状态码 | 场景 | 说明 |
|-------------|----------|-------------|
| 400 | 无效的 body（为空/非 JSON） | 响应中包含 "failed to create generate request" |
| 400 | 后端引擎 400 透传 | body 内容由推理引擎透传 |
| 429 | 并发请求超过单个 RouteServer 上限 | 超出限流 |
| 500 | vLLM 后端错误 | 直接透传 vLLM 错误内容 |
| 500 | 其它 RouteServer 内部错误 | 详见响应体 |
| 504 | 流式超时 | 流式输出过程中网关超时 |
| 502 | 无可用 Prefill 实例 | 网关错误 |
| 502 | 无可用 Decode 实例 | 网关错误 |

**APC 缓存支持**：当 vLLM 启用 APC（Automatic Prefix Caching）时，响应的 `usage` 字段会包含 `prompt_tokens_details`，其中 `cached_tokens` 表示缓存命中的 token 数。首次请求为 null，后续具有相同前缀的请求会显示缓存的 token。

**流式**：在请求 body 中加入 `"stream": true` 即可启用 Server-Sent Events（SSE）流式响应。每个数据块遵循 OpenAI SSE 格式。

### POST /v1/chat/completions

**监听端口**：Service Port

**功能**：处理聊天对话推理请求，支持对话式 AI 推理服务。

**处理者**：RouteServer 依据负载均衡策略转发至相应的推理实例。

**调用方**：客户端应用、AI 服务调用方

**约束/条件**：
- 必须存在可用的推理实例
- RouteServer 服务正常
- 网络连接正常

**参数**：
```json
{
    "model": "model_name",
    "messages": [
        {
            "role": "user",
            "content": "Hello"
        }
    ],
    "max_tokens": 1000,
    "temperature": 0.7
}
```

**核心业务逻辑**：
- 接收客户端的聊天推理请求
- 依据调度算法（基于负载、模型匹配等）选择合适的推理实例
- 将请求转发至选定的推理实例
- 实例执行 Prefill 和/或 Decode 推理
- 将推理结果返回给客户端

**响应示例**：
```json
{
    "id": "chatcmpl-xxxxx",
    "object": "chat.completion",
    "created": 1716161616,
    "model": "model_name",
    "choices": [
        {
            "index": 0,
            "message": {
                "role": "assistant",
                "content": "Hello! How can I help you today?"
            },
            "finish_reason": "stop"
        }
    ],
    "usage": {
        "prompt_tokens": 10,
        "completion_tokens": 9,
        "total_tokens": 19
    }
}
```

### POST /v1/completions

**监听端口**：Service Port

**功能**：处理文本补全推理请求，支持文本生成类 AI 服务。

**处理者**：RouteServer 依据负载均衡策略转发至相应的推理实例。

**调用方**：客户端应用、AI 服务调用方

**约束/条件**：
- 必须存在可用的推理实例
- RouteServer 服务正常
- 请求的 prompt 长度不超过模型上限

**参数**：
```json
{
    "model": "model_name",
    "prompt": "Once upon a time",
    "max_tokens": 1000,
    "temperature": 0.7
}
```

**核心业务逻辑**：
- 接收客户端的文本补全请求
- 校验请求参数与 prompt 长度
- 依据调度算法选择合适的推理实例
- 将请求转发至选定的推理实例
- 实例执行文本生成推理
- 返回生成的文本结果

**响应示例**：
```json
{
    "id": "cmpl-xxxxx",
    "object": "text_completion",
    "created": 1716161616,
    "model": "model_name",
    "choices": [
        {
            "text": "Once upon a time, in a faraway kingdom lived a princess...",
            "index": 0,
            "logprobs": null,
            "finish_reason": "length"
        }
    ],
    "usage": {
        "prompt_tokens": 4,
        "completion_tokens": 20,
        "total_tokens": 24
    }
}
```

### POST /v1/metaserver

**监听端口**：Service Port

**功能**：layerwise PD 分离推理模式的回调接口，供推理实例上报 KV cache 传输元数据。

**处理者**：RouteServer 的元数据管理器处理该请求。

**调用方**：推理实例

**约束/条件**：
- 仅在 layerwise PD 分离推理模式下使用
- 请求方必须是已注册的推理实例
- 需要校验请求的合法性

**参数**：
```json
{
    "kv_transfer_params": {
        "request_id": "req_xxxxx",
        "do_remote_decode": true,
        "do_remote_prefill": false,
        "metaserver": "http://metaserver.address:port"
    }
}
```

**核心业务逻辑**：
- 校验推理实例身份与权限
- 接收并解析 KV cache 传输参数
- 成功时返回 HTTP 200，服务不可用时返回 503

**响应**：返回简单的 HTTP 状态码（成功为 200，服务不可用为 503），不含结构化 JSON 体。

## 4. 调度请求 API

### POST /schedule

**监听端口**：Operations Port

**功能**：分布式远程调度接口，处理推理任务调度请求，并根据集群状态与负载情况分配推理资源。

**处理者**：RouteServer 的调度器处理器处理该请求。

**调用方**：内部服务、推理实例

**约束/条件**：
- RouteServer 处于 running 状态
- 集群中至少有一个可用实例

**参数**：
```json
{
    "req": {
        "id": "chatcmpl-xxx",
        "type": "prefill",
        "slo": "default",
        "slo-intervals": null,
        "max-tokens": 1000,
        "prompt-tokens": 128,
        "stream": false,
        "prompt-cache-key": "",
        "apc-strategy-type": ""
    },
    "size": 1,
    "pre_id": "",
    "params": {},
    "worker_id": "route1",
    "prompt_key": ""
}
```

**核心业务逻辑**：
- 解析调度请求，获取任务需求
- 检查集群资源状态与实例负载
- 依据调度策略选择最优实例（综合考虑负载、时延、成本等）
- 为请求分配资源并生成调度 token
- 记录调度信息，供后续追踪
- 返回调度结果与目标实例信息

**响应示例**：
```json
{
    "res": "instance_id",
    "slo": "default",
    "skipped": false
}
```

| 字段 | 类型 | 说明 |
|-------|------|-------------|
| res | string | 选中的实例 ID |
| slo | string | 服务等级标签 |
| skipped | bool | 调度过程中是否跳过了部分实例 |

### GET /scheduler/load

**监听端口**：Service Port

**功能**：获取集群中各节点的负载信息，用于负载均衡与调度决策。

**处理者**：被请求的 RouteServer 接收后转发给集群 Leader 处理。

**调用方**：运维平台、监控系统

**约束/条件**：
- 需要通过 adminLeaderProxyMiddleware 获得可用的集群 Leader
- Leader 不可用时，该端点不可用，会返回非 200 状态码

**查询参数**：

| 参数 | 类型 | 说明 | 默认值 |
|-----------|------|-------------|---------|
| include_requests | bool | 是否包含逐请求的负载信息 | false |
| include_unavailable | bool | 是否包含被隔离实例的负载信息 | false |
| role | string | 目标实例角色过滤（例如 "prefill,decode"） | 空（全部角色） |

**核心业务逻辑**：
- 从集群 Leader 获取调度器状态
- 按角色、按实例收集负载信息
- 返回用于调度分析的负载数据

**响应示例**：
```json
{
    "rs_name": "route1",
    "loads": {
        "prefill": {
            "slo_group_loads": {
                "default": {
                    "algorithm": "default",
                    "instance_loads": [
                        {
                            "id": "prefill_01",
                            "load": 0.5,
                            "desc": "requests:5,total_prompt_tokens:2560,batch_size:5,max_num_seqs:256",
                            "requests": {
                                "chatcmpl-1": {
                                    "load": 512,
                                    "desc": "prompt_tokens:512"
                                }
                            }
                        }
                    ]
                }
            }
        },
        "decode": {
            "slo_group_loads": {
                "default": {
                    "algorithm": "default",
                    "instance_loads": [
                        {
                            "id": "decode_01",
                            "load": 0.3,
                            "desc": "requests:3,total_prompt_tokens:1280,batch_size:3,max_num_seqs:256",
                            "requests": {}
                        }
                    ]
                }
            }
        }
    }
}
```

| 响应字段 | 说明                                                 |
|----------------|-------------------------------------------------------------|
| rs_name | RouteServer 名称                                       |
| loads | 角色 -> SLOLoadInfo 的映射                                  |
| slo_group_loads | SLO 分组标签 -> 分组负载信息的映射                     |
| algorithm | 使用的调度算法                                   |
| instance_loads | 逐实例负载信息列表                              |
| id | 实例 ID                                                 |
| load | 计算得到的负载值                                       |
| desc | 负载描述（请求数、token 数、batch size） |
| requests | 逐请求的负载明细（仅当 include_requests=true 时）  |

## 5. 实例管理 API

### POST /instance_requests

**监听端口**：Service Port

**功能**：处理实例广播请求，用于实例间通信与协同。

**处理者**：RouteServer 的实例管理器处理该请求。

**调用方**：集群内的其它实例或服务

**约束/条件**：
- 请求方必须位于集群白名单中
- 请求路径必须配置在 BroadcastURIWhitelist 中

**参数**：
```json
{
    "targets": "instance_name_or_all",
    "uri": "/health",
    "method": "GET",
    "headers": {},
    "body": null
}
```

| 字段 | 类型 | 说明 |
|-------|------|-------------|
| targets | string | 目标实例。"all" 表示广播，或指定实例名 |
| uri | string | 在目标实例上请求的 URI 路径 |
| method | string | HTTP 方法（GET、POST 等） |
| headers | object | 转发请求中携带的 HTTP 头 |
| body | raw JSON | 转发请求的请求体 |

**核心业务逻辑**：
- 校验请求来源与权限
- 依据 targets 字段转发至目标实例
- 收集全部目标实例的响应
- 返回聚合结果

**响应示例**：
```json
{
    "status": "success",
    "total": {
        "total": 2,
        "success": 2,
        "failed": 0
    },
    "responses": [
        {
            "instance_id": "prefill_01",
            "status": 200,
            "body": {},
            "error": ""
        }
    ]
}
```

| 字段 | 类型 | 说明 |
|-------|------|-------------|
| status | string | 整体状态："success"、"partial" 或 "failed" |
| total | object | 实例响应的统计信息 |
| responses | array | 逐实例响应明细列表 |

### POST /internal/worker/force-readiness

**监听端口**：Operations Port

**功能**：强制设置 RouteServer worker 节点的就绪状态（应急逃生通道）。当熔断自动判定出现异常时，可通过该接口手动干预 worker 的就绪状态，控制 k8s 是否向该 worker 分发流量。

**处理者**：实例管理器（熔断）本地处理 —— 不转发给 Leader。

**调用方**：运维平台

**约束/条件**：
- 仅在熔断启用时生效（`circuit-breaker.enable: true`）
- 熔断未启用时返回 `disabled` 状态
- 该接口仅绑定在 Operations Port
- 除非再次修改或 RouteServer 重启，否则熔断会一直保持该强制模式

**参数**：

| 参数 | 位置 | 类型 | 必填 | 说明 |
|-----------|----------|------|----------|-------------|
| mode | Body（JSON） | string | 否 | 强制模式。取值：`true` / `false` / `auto`。默认值：`auto` |

**请求体**：
```json
{
    "mode": "true"
}
```

**mode 参数说明**：

| 取值 | 别名 | 行为 |
|-------|---------|----------|
| `true` | `1`、`yes` | 强制就绪 —— 就绪探针返回 200，k8s 分配流量 |
| `false` | `0`、`no` | 强制未就绪 —— 就绪探针返回 500，k8s 停止分流 |
| `auto` | `2` | 自动模式 —— 熔断依据失败率判定就绪状态 |

**核心业务逻辑**：
1. 检查熔断是否启用；若未启用则返回 `disabled`
2. 从请求体解析 `mode`（缺省时默认为 `auto`）
3. 据此设置熔断强制模式
4. 记录操作日志并返回结果

**响应示例**：

```json
// mode=true (force ready)
{
    "status": "success",
    "mode": "true",
    "ready": true,
    "message": "force ready (accepting traffic)"
}

// mode=false (force not ready)
{
    "status": "success",
    "mode": "false",
    "ready": false,
    "message": "force not ready (rejecting traffic)"
}

// mode=auto (circuit breaker auto-judgment)
{
    "status": "success",
    "mode": "auto",
    "ready": true,
    "message": "auto mode (internal logic)"
}

// Circuit breaker not enabled
{
    "status": "disabled",
    "message": "circuit breaker is not enabled"
}

// Invalid mode parameter (HTTP 400)
{
    "status": "invalid",
    "message": "invalid mode: xxx, use: true/false/auto"
}

// Setting failed (HTTP 500)
{
    "status": "failed",
    "message": "failed to set circuit breaker mode"
}
```

## 6. 请求管理 API

### POST /internal/abort

**监听端口**：Operations Port

**功能**：中止正在运行的推理请求，用于请求取消与资源释放。

**处理者**：运行中请求管理器处理该请求。

**调用方**：运维平台、监控系统

**约束/条件**：
- 请求必须处于运行中状态（由 RouteServer 的请求上下文管理器追踪）

**参数**：
```json
{
    "req_ids": [
        "chatcmpl-1",
        "chatcmpl-2",
        "chatcmpl-3"
    ]
}
```

| 字段 | 类型 | 说明 |
|-------|------|-------------|
| req_ids | array of string | 待中止的请求 ID 列表 |

**核心业务逻辑**：
1. RouteServer worker 在转发给 P 或 D 实例之前，使用 context 追踪每个请求
2. 收到中止请求后，遍历请求列表，取消匹配的请求上下文，触发 HTTP 请求自动终止
3. 记录成功中止的 req_ids 并返回结果

**响应示例**：
```json
{
    "aborted_req_ids": [
        "chatcmpl-1"
    ],
    "not_found_req_ids": [
        "chatcmpl-2",
        "chatcmpl-3"
    ]
}
```

| 字段 | 类型 | 说明 |
|-------|------|-------------|
| aborted_req_ids | array of string | 成功中止的请求 ID |
| not_found_req_ids | array of string | 未找到或已完成的请求 ID |

**错误场景**：

| 状态码 | 场景 | 响应 |
|-------------|----------|----------|
| 400 | 缺少请求体 | `{"error": "failed to read body, err = %s"}` |
| 400 | JSON 格式错误 | `{"error": "failed to unmarshal request body, err = %s"}` |
| 400 | req_ids 为空 | `{"error": "request body is empty or contains no req_ids"}` |

### GET /internal/running_details

**监听端口**：Operations Port

**功能**：查看指定 RouteServer 进程下各实例的活跃请求信息。

**处理者**：运行中请求管理器本地处理 —— 不转发给 Leader。

**调用方**：运维平台、监控系统

**约束/条件**：
- 需要管理权限

**参数**：无请求参数

**核心业务逻辑**：
1. RouteServer worker 在转发给 P 或 D 实例之前，使用 context 追踪每个请求
2. 收到请求后，遍历请求列表，收集各 P 与 D 实例上活跃请求的状态
3. 返回每个实例的名称、角色以及各请求的详细状态

**响应示例**：
```json
{
    "server_name": "route1",
    "request_lists": {
        "chatcmpl-1": {
            "finished_phases": {
                "prefill": "instance:prefill_01,start:1745568000000,end:1745568020000"
            },
            "running_phases": {
                "decode": "instance:decode_01,start:1745568020000,end:0"
            },
            "waiting_phases": []
        },
        "chatcmpl-2": {
            "finished_phases": {},
            "running_phases": {
                "prefill": "instance:prefill_01,start:1745568030000,end:0"
            },
            "waiting_phases": ["decode"]
        }
    }
}
```

| 字段 | 类型 | 说明 |
|-------|------|-------------|
| server_name | string | RouteServer 名称 |
| request_lists | map | request_id -> 请求明细的映射 |
| request_lists.{id}.finished_phases | map | 已完成的阶段，含实例与时间信息 |
| request_lists.{id}.running_phases | map | 当前运行的阶段，含实例与时间信息 |
| request_lists.{id}.waiting_phases | array | 等待执行的阶段 |

## 7. 集群管理 API

### POST /cluster/peer/join

**监听端口**：Operations Port

**功能**：处理节点加入集群的请求。

**处理者**：被请求的 RouteServer 接收后转发给集群 Leader 处理。

**调用方**：待加入的节点

**约束/条件**：
- 需要通过 adminLeaderProxyMiddleware 获得可用的集群 Leader
- Leader 不可用时，该端点不可用，会返回非 200 状态码
- 新节点必须与集群网络可达

**参数**：
```json
{
    "node_name": "node_name",
    "peer_addr": {
        "host": "ip_address",
        "service_port": 9800,
        "ops_port": 9810,
        "domain": "domain_name"
    },
    "is_candidate": true
}
```

| 字段 | 类型 | 说明 |
|-------|------|-------------|
| node_name | string | 节点名称 |
| peer_addr.host | string | 节点 IP 地址 |
| peer_addr.service_port | int | 服务端口（HTTP） |
| peer_addr.ops_port | int | 运维端口 |
| peer_addr.domain | string | 域名（可选） |
| is_candidate | bool | 该节点是否参与 Leader 选举 |

**核心业务逻辑**：
- Leader 校验新节点的合法性
- 检查网络连通性与版本兼容性
- 将新节点加入集群成员列表
- 更新集群状态持久化
- 通知全部集群成员有新节点加入
- 若为 candidate 节点，则加入 Leader 选举

**响应**：成功时为 `{"result": "accepted"}`，失败时返回错误 JSON。

### POST /cluster/peer/leave

**监听端口**：Operations Port

**功能**：处理节点离开集群的请求。

**处理者**：被请求的 RouteServer 接收后转发给集群 Leader 处理。

**调用方**：待离开的节点

**约束/条件**：
- 需要通过 adminLeaderProxyMiddleware 获得可用的集群 Leader
- Leader 不可用时，该端点不可用，会返回非 200 状态码
- 离开的节点不能是当前 Leader（需先转移 Leader 角色）

**参数**：
```json
{
    "name": "node_name"
}
```

| 字段 | 类型 | 说明 |
|-------|------|-------------|
| name | string | 要离开集群的节点名称 |

**核心业务逻辑**：
- 校验请求合法性
- 若该节点是当前 Leader，则触发 Leader 选举
- 从集群成员列表中移除该节点
- 将该节点上的请求迁移至其它节点
- 更新集群状态持久化
- 通知全部集群成员有节点离开

**响应**：成功时为 `{"result": "accepted"}`，失败时返回错误 JSON。

### POST /cluster/instance/join

**监听端口**：Operations Port

**功能**：处理实例加入集群的请求。

**处理者**：被请求的 RouteServer 接收后转发给集群 Leader 处理。

**调用方**：推理实例

**约束/条件**：
- 需要通过 adminLeaderProxyMiddleware 获得可用的集群 Leader
- Leader 不可用时，该端点不可用，会返回非 200 状态码
- 实例网络必须可达

**参数**：
```json
{
    "instances": {
        "instance1": {
            "ip": "*.*.*.*",
            "port": 1234,
            "role": "prefill",
            "tag": "short",
            "static-infos": {
                "max_num_seqs": "16"
            }
        },
        "instance2": {
            "ip": "*.*.*.*",
            "port": 1235,
            "role": "decode",
            "tag": "",
            "static-infos": {
                "max_num_seqs": "32"
            }
        }
    }
}
```

| 字段 | 类型 | 说明 |
|-------|------|-------------|
| instances | map | 实例名 -> 实例配置的映射。实例名必须全局唯一。 |
| instances.{name}.ip | string | 实例 IP 地址 |
| instances.{name}.port | int | 实例端口，用于 generate/health 请求 |
| instances.{name}.role | string | 实例角色："prefill" 或 "decode" |
| instances.{name}.tag | string | SLO 实例分组标识（可选） |
| instances.{name}.static-infos | object | 调度算法使用的静态配置（例如 max_num_seqs） |

**核心业务逻辑**：
- 以事务方式将多个实例加入集群状态（全有或全无）
- 若任一实例校验失败，则全部不加入
- 将实例注册到集群路由表
- 更新调度器的可用实例列表

**响应**：成功时为 `{"result": "accepted"}`。

**错误响应示例**：
```json
{
    "error": "no leader elected, can't process cluster admin requests"
}
```

### POST /cluster/instance/leave

**监听端口**：Operations Port

**功能**：处理实例离开集群的请求。

**处理者**：被请求的 RouteServer 接收后转发给集群 Leader 处理。

**调用方**：推理实例

**约束/条件**：
- 需要通过 adminLeaderProxyMiddleware 获得可用的集群 Leader
- Leader 不可用时，该端点不可用，会返回非 200 状态码
- 正在处理请求的实例需要优雅退出

**参数**：
```json
{
    "instance_names": [
        "instance1",
        "instance4"
    ]
}
```

| 字段 | 类型 | 说明 |
|-------|------|-------------|
| instance_names | array of string | 待移除的实例名列表。不存在的名称会被静默忽略，空字符串会被拒绝。 |

**核心业务逻辑**：
- 从集群状态中移除指定实例
- 停止向这些实例分发新请求
- 更新集群状态持久化
- 不存在的实例名会被忽略

**响应**：成功时为 `{"result": "accepted"}`。

**错误响应示例**：
```json
{
    "error": "no leader elected, can't process cluster admin requests"
}
```

### POST /cluster/abort

**监听端口**：Operations Port

**功能**：在整个 RouteServer 集群范围内中止指定请求。

**处理者**：被请求的 RouteServer 本地处理 —— 不通过中间件转发给 Leader。内部包含 `IsLeader()` 检查，非 leader 时返回 503。

**调用方**：运维平台

**约束/条件**：
- 只有集群 Leader 处理该请求；非 leader 节点返回 503
- 请求必须处于运行中状态

**参数**：
```json
{
    "req_ids": [
        "chatcmpl-1",
        "chatcmpl-2",
        "chatcmpl-3"
    ]
}
```

| 字段 | 类型 | 说明 |
|-------|------|-------------|
| req_ids | array of string | 待在整个集群内中止的请求 ID 列表 |

**核心业务逻辑**：
1. 接收方 RouteServer 检查自己是否为 Leader；若不是则返回 503
2. Leader 通过 `/internal/abort` 将中止请求转发给所有 worker 节点
3. Leader 收集全部响应并返回聚合结果

**响应示例**：
```json
{
    "aborted_req_ids": [
        "chatcmpl-1"
    ],
    "not_found_req_ids": [
        "chatcmpl-2",
        "chatcmpl-3"
    ]
}
```

| 字段 | 类型 | 说明 |
|-------|------|-------------|
| aborted_req_ids | array of string | 在整个集群内成功中止的请求 ID |
| not_found_req_ids | array of string | 未找到或已完成的请求 ID |

**错误场景**：

| 状态码 | 场景 | 响应 |
|-------------|----------|----------|
| 400 | req_ids 缺失/为空 | `{"error": "request body is empty or contains no req_ids"}` |
| 400 | JSON 格式错误 | `{"error": "failed to unmarshal request body, err = %s"}` |
| 503 | 无 leader/集群未启动 | `{"error": "no state found"}` |

**警告场景**（worker 通信失败只记录日志，不会导致请求失败）：
- worker 地址无效：`WARN worker [%s] address is invalid`
- 请求创建失败：`WARN failed to create request for worker [%s]`
- 请求发送失败：`WARN failed to send request for worker [%s]`
- 响应解析失败：`WARN failed to unmarshal JSON response from worker [%s]`
- 非成功状态码：`WARN received non-successful status code from worker [%s], status = %d`

### GET /cluster/leader

**监听端口**：Service Port 和 Operations Port

**功能**：获取当前集群 Leader 信息。

**处理者**：被请求的 RouteServer 接收后转发给集群 Leader 处理。

**调用方**：集群节点、监控系统

**约束/条件**：
- 该路径不在 LeaderOperationSet 中 —— 本地处理，不转发给 Leader
- 未选出 leader 时返回 503

**参数**：无请求参数

**核心业务逻辑**：
- 从本地集群状态查询当前集群 Leader
- 返回 Leader 的 ID 与 peer 信息
- 若无 Leader，返回相应的错误

**响应示例**：
```json
{
    "id": "rs-node-01",
    "peer_info": {
        "node_name": "rs-node-01",
        "peer_addr": {
            "host": "*.*.*.*",
            "service_port": 9800,
            "ops_port": 9810,
            "domain": ""
        },
        "is_candidate": true
    }
}
```

| 字段 | 类型 | 说明 |
|-------|------|-------------|
| id | string | Leader 节点名称 |
| peer_info.node_name | string | Leader peer 名称 |
| peer_info.peer_addr.host | string | Leader 主机地址 |
| peer_info.peer_addr.service_port | int | Leader 服务端口 |
| peer_info.peer_addr.ops_port | int | Leader 运维端口 |
| peer_info.is_candidate | bool | 该 leader 是否为 candidate 节点 |

**无 Leader 时的响应**：HTTP 503，内容为 `{"error": "No elected leader is found for this endpoint"}`

### GET /cluster/local/state

**监听端口**：Service Port 和 Operations Port

**功能**：获取本地节点状态信息。

**处理者**：被请求的 RouteServer 接收后转发给集群 Leader 处理。

**调用方**：监控系统、运维平台

**约束/条件**：
- 该路径不在 LeaderOperationSet 中 —— 本地处理，不转发给 Leader
- 集群状态尚未初始化时返回 503

**参数**：无请求参数

**核心业务逻辑**：
- 获取本地集群状态
- 返回完整集群状态，包括 peers、实例路由与版本

**响应示例**：
```json
{
    "state": {
        "initialized": true,
        "seed_peers": [],
        "peers": {
            "rs-node-01": {
                "node_name": "rs-node-01",
                "peer_addr": {
                    "host": "*.*.*.*",
                    "service_port": 9800,
                    "ops_port": 9810,
                    "domain": ""
                },
                "is_candidate": true
            },
            "rs-node-02": {
                "node_name": "rs-node-02",
                "peer_addr": {
                    "host": "*.*.*.*",
                    "service_port": 9800,
                    "ops_port": 9810,
                    "domain": ""
                },
                "is_candidate": false
            }
        },
        "instance-routes": {
            "prefill_01": {
                "ip": "*.*.*.*",
                "port": 9000,
                "role": "prefill",
                "static-infos": {
                    "max_num_seqs": "16"
                },
                "tag": ""
            }
        },
        "version": 15
    }
}
```

| 字段 | 类型 | 说明 |
|-------|------|-------------|
| state.initialized | bool | 集群状态是否已初始化 |
| state.seed_peers | array | 种子 peer 配置 |
| state.peers | map | peer 名称 -> Peer 信息的映射（全部集群节点） |
| state.instance-routes | map | 实例名 -> 路由配置的映射（全部已注册实例） |
| state.version | int | 集群状态版本号 |

### GET /cluster/consistent_status

**监听端口**：Service Port

**功能**：获取集群一致性状态信息。

**处理者**：被请求的 RouteServer 接收后转发给集群 Leader 处理。

**调用方**：监控系统、运维平台

**约束/条件**：
- 该路径不在 LeaderOperationSet 中 —— 本地处理，不转发给 Leader

**参数**：无请求参数

**核心业务逻辑**：
- 从本地集群服务获取集群一致性统计信息
- 返回统计键值对映射

**响应示例**：
```json
{
    "stats": {
        "peers_version": "15",
        "instances_version": "12",
        "last_updated": "2025-05-20T10:30:00Z"
    }
}
```

| 字段 | 类型 | 说明 |
|-------|------|-------------|
| stats | map[string]string | 一致性统计键值对映射 |

### GET /cluster/running_details

**监听端口**：Operations Port

**功能**：获取集群中运行中任务的明细。

**处理者**：被请求的 RouteServer 接收后转发给集群 Leader 处理。

**调用方**：监控系统、运维平台

**约束/条件**：
- 该路径不在 LeaderOperationSet 中 —— 本地处理，不转发给 Leader
- 内部包含 `IsLeader()` 检查 —— 非 leader 返回 503

**参数**：无请求参数

**核心业务逻辑**：
1. 接收方 RouteServer 检查自己是否为 Leader；若不是则返回 503
2. Leader 向所有 worker 节点发送 `/internal/running_details` 请求
3. Leader 收集全部 worker 响应并返回聚合结果

**响应示例**：
```json
{
    "server_lists": [
        {
            "server_name": "route1",
            "request_lists": {
                "chatcmpl-1": {
                    "finished_phases": {
                        "prefill": "instance:prefill_01,start:1745568000000,end:1745568020000"
                    },
                    "running_phases": {
                        "decode": "instance:decode_01,start:1745568020000,end:0"
                    },
                    "waiting_phases": []
                },
                "chatcmpl-2": {
                    "finished_phases": {},
                    "running_phases": {
                        "prefill": "instance:prefill_01,start:1745568030000,end:0"
                    },
                    "waiting_phases": ["decode"]
                }
            }
        },
        {
            "server_name": "route2",
            "request_lists": {
                "chatcmpl-3": {
                    "finished_phases": {},
                    "running_phases": {
                        "decode": "instance:decode_02,start:1745568040000,end:0"
                    },
                    "waiting_phases": ["prefill"]
                }
            }
        }
    ]
}
```

| 字段 | 类型 | 说明 |
|-------|------|-------------|
| server_lists | array | 各 server 运行明细的列表 |
| server_name | string | RouteServer 名称 |
| request_lists | map | request_id -> 阶段级请求明细的映射 |

## 8. 监控指标 API

### GET /metrics

**监听端口**：Service Port

**功能**：获取 RouteServer 与集群的 Prometheus 格式监控指标。

**处理者**：被请求的 RouteServer 接收后转发给集群 Leader 处理。

**调用方**：Prometheus 监控系统、运维平台

**约束/条件**：
- 需要通过 adminLeaderProxyMiddleware 获得可用的集群 Leader
- Leader 不可用时，该端点不可用，会返回非 200 状态码

**参数**：无请求参数

**核心业务逻辑**：
- 采集 RouteServer 自身的运行指标
- 聚合集群内所有实例的指标数据
- 按要求格式化为 Prometheus 指标文本
- 包含请求量、响应时间、错误率、资源使用等
- 支持标签过滤与聚合查询

**响应示例**：
```
# HELP route_server_requests_total Total number of requests
# TYPE route_server_requests_total counter
route_server_requests_total{method="POST",endpoint="/v1/chat/completions",status="200"} 1500

# HELP route_server_request_duration_seconds Request duration in seconds
# TYPE route_server_request_duration_seconds histogram
route_server_request_duration_seconds_bucket{le="0.1"} 100
route_server_request_duration_seconds_bucket{le="0.5"} 800
route_server_request_duration_seconds_bucket{le="1.0"} 1450
route_server_request_duration_seconds_bucket{le="+Inf"} 1500

# HELP cluster_instances_total Total number of instances in cluster
# TYPE cluster_instances_total gauge
cluster_instances_total{role="prefill"} 3
cluster_instances_total{role="decode"} 3

# HELP cluster_instance_load_ratio Instance load ratio
# TYPE cluster_instance_load_ratio gauge
cluster_instance_load_ratio{instance="prefill_01"} 0.65
cluster_instance_load_ratio{instance="decode_01"} 0.72
```

### GET /internal/metrics

**监听端口**：Service Port

**功能**：获取 RouteServer 进程的 Prometheus 监控指标。直接注册在 engine 上（不走 leader 代理）。

**处理者**：内部指标采集器本地处理 —— 不转发给 Leader。

**调用方**：监控系统

**约束/条件**：无需特殊权限

**查询参数**：

| 参数 | 类型 | 说明 | 默认值 |
|-----------|------|-------------|---------|
| only_rs | string | 取值为 "true" 时仅返回 rs 前缀的指标（不含 Go 运行时指标） | 空 |

**核心业务逻辑**：
- 采集 RouteServer 进程的运行指标
- 返回 Prometheus 文本格式指标

**响应格式**：Prometheus 文本格式（非 JSON）

**性能指标**：

| 指标 | 类型 | 说明 | 内部名称 |
|--------|------|-------------|---------------|
| num_failed_requests | Counter | 失败请求数 | rs:num_failed_requests |
| num_total_requests | Counter | 成功请求数 | rs:num_successed_requests |
| num_schedule_requests | Counter | 已调度请求数 | rs:num_scheduled_requests |
| request_total_time_sec | Histogram | 请求总耗时分布 | rs:request_total_time_secs |
| request_schedule_time_ms | Histogram | 调度耗时分布（按 Prefill/Decode） | rs:request_schedule_time |
| time_to_first_token_sec | Histogram | TTFT 分布 | rs:time_to_first_token_sec |
| prompt_token_num | Histogram | 单请求 prompt token 数 | rs:prompt_token_num |
| time_per_output_token_ms | Histogram | TPOT 分布 | rs:time_per_output_token_ms |
| tokens_output_per_request | Histogram | 单请求输出 token 数 | rs:tokens_output_per_request |
| total_output_tokens | Counter | 输出 token 总数 | rs:num_total_output_tokens |
| number_healthy_endpoints | Gauge | 按角色统计的健康端点 | rs:number_healthy_endpoints{role="decode\|prefill"} |
| infer_requests_failure_status_total | Counter | 推理请求状态码 | rs:infer_requests_failure_status_total |

## 9. 健康探针 API

### GET /internal/liveness

**监听端口**：Service Port

**功能**：Kubernetes 存活探针 —— 检查服务进程是否存活（未卡死）。

**处理者**：健康探针本地处理 —— 不转发给 Leader。

**调用方**：Kubernetes、健康检查系统

**约束/条件**：无需特殊权限

**参数**：无请求参数

**核心业务逻辑**：
- 简单的进程存活检查
- 返回 HTTP 200，正文为纯文本 "ok"

**响应**：纯文本 `ok`，HTTP 状态码 200。

### GET /internal/leader/readiness

**监听端口**：Service Port

**功能**：检查 Leader 角色的就绪状态。仅检查当前节点是否为集群 Leader。

**处理者**：健康探针本地处理 —— 不转发给 Leader。

**调用方**：Kubernetes、健康检查系统

**约束/条件**：无需特殊权限

**参数**：无请求参数

**核心业务逻辑**：
- 检查当前节点是否为集群 Leader
- 返回包含 is_leader 布尔值的 JSON

**响应示例**：
```json
{
    "is_leader": true
}
```

### GET /internal/worker/readiness

**监听端口**：Service Port

**功能**：检查 worker 节点的就绪状态，供 k8s 就绪探针使用。决定当前 worker 是否应接收流量。基于三层判定逻辑：先检查是否启用强制模式 > 再检查健康缓存 > 最后检查熔断状态。

**处理者**：健康探针本地处理 —— 不转发给 Leader。

**调用方**：Kubernetes、健康检查系统

**约束/条件**：无需特殊权限

**参数**：无请求参数

**核心业务逻辑（三层判定）**：

1. **第一层：强制模式检查**（熔断是否设置了强制模式）
   - `ForceModeTrue` → 返回就绪（HTTP 200）
   - `ForceModeFalse` → 返回未就绪（HTTP 500）
   - `ForceModeNone` → 继续下一层

2. **第二层：健康缓存检查**（`InstanceHealthCache`）
   - 缓存有效 → 检查各角色的实例数是否满足最低要求
     - 满足 → 就绪，state=3（normal）
     - 不足 → 未就绪，state=3（normal）
   - 缓存无效 → 继续下一层

3. **第三层：熔断检查**（熔断启用时）
   - 熔断打开（StateOpen）→ 未就绪，state=4（broken）
   - 熔断关闭（StateClosed）→ 就绪，state=3（normal）

**响应字段**：

| 字段 | 类型 | 说明 |
|-------|------|-------------|
| is_ready | bool | worker 是否就绪 |
| message | string | 状态描述信息 |
| state | int | worker 就绪状态码（仅熔断启用时返回） |
| state_name | string | 状态名称（仅熔断启用时返回） |

**WorkerReadinessState 枚举**：

| state | state_name | 说明 |
|-------|------------|-------------|
| 0 | disabled | 熔断未启用（不会出现在 API 响应中） |
| 1 | force_true | 强制就绪 —— 手动设置为接收流量 |
| 2 | force_false | 强制未就绪 —— 手动设置为拒绝流量 |
| 3 | normal | 正常模式 —— 由健康缓存或熔断自动判定 |
| 4 | broken | 熔断打开 —— 失败率超过阈值 |

**响应示例**：

```json
// Layer 1: Force ready (circuit breaker force mode=true)
HTTP 200
{
    "is_ready": true,
    "message": "ready",
    "state": 1,
    "state_name": "force_true"
}

// Layer 1: Force not ready (circuit breaker force mode=false)
HTTP 500
{
    "is_ready": false,
    "message": "force not ready",
    "state": 2,
    "state_name": "force_false"
}

// Layer 2: Health check passed, minimum instance count satisfied
HTTP 200
{
    "is_ready": true,
    "message": "ready",
    "state": 3,
    "state_name": "normal"
}

// Layer 2: Health check failed, insufficient instances
HTTP 500
{
    "is_ready": false,
    "message": "the cluster isn't ready, 1 prefill required at least, but got 0 prefill available",
    "state": 3,
    "state_name": "normal"
}

// Layer 3: Circuit breaker open (high failure rate)
HTTP 500
{
    "is_ready": false,
    "message": "circuit breaker is open, service degraded due to high failure rate",
    "state": 4,
    "state_name": "broken"
}

// Layer 3: Circuit breaker closed (degraded service mode)
HTTP 200
{
    "is_ready": true,
    "message": "degraded service (circuit breaker closed)",
    "state": 3,
    "state_name": "normal"
}

// Fallback: Circuit breaker not enabled and no valid cache
HTTP 500
{
    "is_ready": false,
    "message": "no valid instance info and circuit breaker not enabled"
}
```

**状态码说明**：

| HTTP 状态 | 含义 |
|-------------|---------|
| 200 OK | worker 就绪，k8s 可以分配流量 |
| 500 Internal Server Error | worker 未就绪，k8s 应停止分配流量 |

### GET /worker/readiness

**监听端口**：Service Port

**功能**：worker 就绪状态检查（与 `/internal/worker/readiness` 同一 handler）。这是对外可访问的别名。

**处理者**：健康探针本地处理 —— 不转发给 Leader。

**调用方**：外部健康检查系统

**约束/条件**：无需特殊权限

**参数**：无请求参数

**核心业务逻辑**：与 `/internal/worker/readiness` 相同 —— 同一 handler，三层判定逻辑完全一致。

**响应**：与 `/internal/worker/readiness` 的响应格式相同。

## 10. 版本信息 API

### GET /

**监听端口**：Service Port 和 Operations Port

**功能**：获取 RouteServer 版本信息。

**处理者**：根路径 handler。

**调用方**：任何需要版本信息的调用方

**约束/条件**：无需特殊权限

**参数**：无请求参数

**核心业务逻辑**：
- 读取编译期注入的版本信息
- 获取当前节点名称
- 组装版本信息 JSON 响应
- 用于版本管理与问题排查

**响应示例**：
```json
{
    "name": "xxxx",
    "node_name": "node-01",
    "version": "v1.2.3",
    "tag_or_branch": "release/v1.2.3",
    "commit_id": "xxxx1234",
    "build_time": "2025-05-20T08:00:00Z",
    "port_type": "service_port"
}
```

| 字段 | 类型 | 说明 |
|-------|------|-------------|
| name | string | 软件名称 |
| node_name | string | 当前节点名称 |
| version | string | 软件版本 |
| tag_or_branch | string | Git tag 或分支名 |
| commit_id | string | Git commit hash |
| build_time | string | 构建时间戳 |
| port_type | string | 端口类型："service_port" 或 "operations_port" |

## 11. 事件同步请求 API

### POST /request_event

**监听端口**：Operations Port

**功能**：分布式请求事件同步接口，用于请求生命周期管理。worker 向 Leader 发送请求/实例事件，供追踪与健康监控使用。

**处理者**：直接注册在 opsEngine 上（不在 adminLeaderProxyMiddleware 之后）。内部包含 `IsLeader()` 检查，非 leader 时返回 503。

**调用方**：worker 节点、推理实例

**约束/条件**：
- 只有 Leader 处理该请求；非 leader 返回 503
- 直接注册在 opsEngine 上，不经过 adminLeaderProxyMiddleware

**参数**：
```json
{
    "id": "route1",
    "reqs": [
        {
            "request": {
                "id": "chatcmpl-1",
                "type": "prefill",
                "slo": "default",
                "max-tokens": 1000,
                "prompt-tokens": 128,
                "stream": false
            },
            "instance": "prefill_01",
            "preInstance": "",
            "type": 1,
            "failure-details": null,
            "time": 1745568000000,
            "test": false
        }
    ],
    "inst": [
        {
            "instance": "prefill_01",
            "type": 0
        }
    ]
}
```

| 字段 | 类型 | 说明 |
|-------|------|-------------|
| id | string | worker 节点 ID |
| reqs | array | 请求事件列表 |
| reqs[].request | object | 请求明细（id、type、slo 等） |
| reqs[].instance | string | 目标实例名 |
| reqs[].preInstance | string | 前一实例名（用于 PD 链路追踪） |
| reqs[].type | int | 事件类型 |
| reqs[].failure-details | object | 失败明细（如有） |
| reqs[].time | int64 | 事件时间戳（Unix 毫秒） |
| inst | array | 实例事件列表 |

**核心业务逻辑**：
- 校验发送方是否为集群 Leader（内部 IsLeader 检查）
- 处理请求事件，用于生命周期追踪
- 处理实例事件，用于健康监控
- 更新 worker 健康检查时间戳（ping）
- 返回各角色的可用实例数

**响应示例**：
```json
{
    "prefill": 2,
    "decode": 3
}
```

**响应**：返回 `map[string]int` —— 角色到可用实例数的映射。出错时返回 503 或 500，内容为 `{"error": "..."}`。

## 12. 黑名单管理 API

### GET /link_black_instance

**监听端口**：Service Port

**功能**：获取链路黑名单中的实例信息。

**处理者**：被请求的 RouteServer 接收后转发给集群 Leader 处理。

**调用方**：运维平台、监控系统

**约束/条件**：
- 需要通过 adminLeaderProxyMiddleware 获得可用的集群 Leader
- Leader 不可用时，该端点不可用，会返回非 200 状态码

**参数**：无请求参数

**核心业务逻辑**：
- 获取当前黑名单列表
- 收集每个被拉黑实例的详细信息
- 包含被加入黑名单的原因与时间
- 统计黑名单实例的角色分布
- 提供黑名单管理操作历史

**响应示例**：
```json
{
    "total_healthy_instance": {
        "decode": 3,
        "prefill": 2
    },
    "instance_black_lists": {
        "decode": [
            {
                "instance_id": "decode_01",
                "blocks_size": 1,
                "link_black_list": [
                    {
                        "blocked_instance": "prefill_01",
                        "block_time": "2025-11-10T11:11:11.537+08:00",
                        "remaining_time_ms": 70050
                    }
                ]
            }
        ],
        "prefill": [
            {
                "instance_id": "prefill_01",
                "blocks_size": 1,
                "link_black_list": [
                    {
                        "blocked_instance": "decode_01",
                        "block_time": "2025-11-10T11:11:11.537+08:00",
                        "remaining_time_ms": 70050
                    }
                ]
            }
        ]
    }
}
```

| 字段 | 类型 | 说明 |
|-------|------|-------------|
| total_healthy_instance | map[string]int | 按角色（prefill/decode）统计的健康实例数 |
| instance_black_lists | map[string]array | 按角色分组的实例黑名单明细 |
| instance_id | string | 存在被拉黑链路的实例名 |
| blocks_size | int | 该实例拉黑的实例数 |
| link_black_list | array | 被拉黑实例的明细列表 |
| blocked_instance | string | 被拉黑的实例名 |
| block_time | string | 首次发生拉黑的时间戳 |
| remaining_time_ms | int64 | 距离解除拉黑剩余的毫秒数 |

## 13. 动态代理 API

动态代理路径依据配置的白名单动态注册，支持 GET 与 POST 方法。

**监听端口**：Service Port

**功能**：将请求代理至指定的推理实例，用于支持自定义 API 端点。

**处理者**：实例管理器的代理 handler。

**调用方**：客户端应用

**约束/条件**：
- 路径必须配置在 BroadcastURIWhitelist 中
- 不能与内部已注册的路由冲突
- 请求方需要具备访问目标实例的权限

**参数**：取决于具体被代理的 API

**核心业务逻辑**：
- 校验请求路径是否在白名单中
- 依据负载均衡算法选择目标实例
- 将请求转发至选定实例
- 等待并返回实例的响应
- 记录代理日志，用于追踪与统计
- 处理实例故障与重试逻辑

**响应示例**（以代理 /v1/embeddings 为例）：
```json
{
    "object": "list",
    "data": [
        {
            "object": "embedding",
            "embedding": [0.001, -0.002, 0.003, ...],
            "index": 0
        }
    ],
    "model": "text-embedding-ada-002",
    "usage": {
        "prompt_tokens": 8,
        "total_tokens": 8
    }
}
```

**错误示例**：
```json
{
    "error": {
        "message": "Proxy path not found or not authorized",
        "type": "proxy_error",
        "code": "path_not_found"
    }
}
```

## 说明

1. **端口说明**：
  - Service Port：用于处理推理请求与大部分管理 API
  - Operations Port：专用于运维任务，例如强制设置就绪状态

2. **Leader 代理说明**：
  - `adminLeaderProxyMiddleware` 依据路径选择性地转发请求：
    - 位于 `LeaderOperationSet` 中的路径，若当前节点不是 Leader，则转发给 Leader
    - 不在 `LeaderOperationSet` 中的路径始终本地处理（不转发，也不做 leader 检查）
  - `LeaderOperationSet` 包含：`/cluster/peer/join`、`/cluster/peer/leave`、`/cluster/instance/join`、`/cluster/instance/leave`、`/metrics`、`/status`、`/health`、`/link_black_instance`、`/scheduler/load`
  - 诸如 `/cluster/leader`、`/cluster/local/state`、`/cluster/consistent_status`、`/cluster/running_details`、`/cluster/abort` 这些端点不在 `LeaderOperationSet` 中，均为本地处理
  - 当 Leader 不可用时，被转发的端点返回非 200 状态码

3. **流水线模式说明**：
  - 配置为流水线模式时，推理 API 端点依据流水线配置动态注册
  - 默认模式支持 /v1/chat/completions 和 /v1/completions 端点

4. **实例加入/离开脚本**：
  - 批量注册实例时，使用 `scale/` 目录下的 `rs_instance_join.py` 与 `rs_instance_leave.py` 脚本
  - 这些脚本分别调用 `/cluster/instance/join` 与 `/cluster/instance/leave` 端点
  - 脚本支持跨机部署，可接受多个 IP 与起始端口
