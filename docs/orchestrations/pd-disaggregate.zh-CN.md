# PD 分离

[English](pd-disaggregate.md) | [简体中文](pd-disaggregate.zh-CN.md)

## 部署模式概述

PD 分离把 Prefill 与 Decode 阶段拆分为独立的 vLLM 实例，二者可以各自独立扩缩容。在该模式下，Prefill 实例处理 prompt 并计算 KV Cache，随后 KV Cache 被传输到 Decode 实例用于 token 生成。

本模式适用于以下场景：

- Prefill 与 Decode 的资源需求不同（Prefill 是计算密集型，Decode 是内存密集型），需要独立扩缩容
- 通过解耦 prompt 处理与 token 生成来获得高吞吐
- 可以接受 Prefill 与 Decode 实例之间的 KV Cache 传输

### 启用 PD 分离模式

把 `infer-mode` 设为 `PD`（若部署中还有额外的 MoE expert 实例，则为 `PD-AM`）即可启用 PD 分离模式：

```bash
# Standard PD disaggregate
deploy.sh ... --infer-mode PD ...
```

PD 分离的有效 infer mode 取值为：

| 取值 | 说明 |
|---|---|
| `PD` | 带 Prefill 与 Decode 角色的标准 PD 分离 |

### 支持的 API 端点

PD 分离模式支持以下 OpenAI 兼容端点：

- `POST /v1/chat/completions` — 对话补全
- `POST /v1/completions` — 文本补全

## 流水线定义

### 流水线配置

PD 分离流水线定义在 [deploy/pipelines/pd_separate.yaml](../../deploy/pipelines/pd_separate.yaml) 中，由四个阶段组成：

| 阶段 | 类型 | 实例角色 | 依赖 |
|---|---|---|---|
| `prefill_schedule` | Schedule | prefill | — |
| `prefill_execute` | Execute | prefill | `prefill_schedule` |
| `decode_schedule` | Schedule | decode | `prefill_execute` |
| `decode_execute` | Execute | decode | `decode_schedule` |

### 执行流程

各阶段按严格串行顺序执行 —— Prefill 必须先完成，Decode 才能开始，因为 Decode 阶段依赖 Prefill 阶段产出的 KV Cache：

```
    ┌──────────────────────────────────────────────────┐
    │                   Preprocess                     │
    │                 (built-in)                       │
    └──────────────────────┬───────────────────────────┘
                           │
                           ▼
    ┌──────────────────────────────────────────────────┐
    │           prefill_schedule (Schedule)            │
    │  instance_type: prefill                          │
    │  request_load: [usage, text, bytes]              │
    └──────────────────────┬───────────────────────────┘
                           │
                           ▼
    ┌──────────────────────────────────────────────────┐
    │           prefill_execute (Execute)              │
    │  dependsOn: prefill_schedule                     │
    │                                                  │
    │  1. prefill_prepare_request                      │
    │  2. Send request to scheduled prefill instance   │
    │  3. prefill_post_response (extract KV params)    │
    │  4. regular_post_phase (trace event)             │
    └──────────────────────┬───────────────────────────┘
                           │
                           ▼
    ┌──────────────────────────────────────────────────┐
    │           decode_schedule (Schedule)             │
    │  instance_type: decode                           │
    │  dependsOn: prefill_execute                      │
    │  request_load: [usage, text, bytes]              │
    └──────────────────────┬───────────────────────────┘
                           │
                           ▼
    ┌──────────────────────────────────────────────────┐
    │           decode_execute (Execute)               │
    │  dependsOn: decode_schedule                      │
    │                                                  │
    │  1. decode_prepare_request (inject KV params)    │
    │  2. Send request to scheduled decode instance    │
    │  3. decode_post_response (write to client)       │
    │  4. complete_post_phase (metrics)                │
    └──────────────────────┬───────────────────────────┘
                           │
                           ▼
    ┌──────────────────────────────────────────────────┐
    │                  PostProcess                     │
    │                 (built-in)                       │
    └──────────────────────────────────────────────────┘
```

详细步骤如下：

1. **Preprocess**（内置）：解析请求体、校验参数，提取 prompt 长度等元数据。
2. **prefill_schedule**（Schedule 阶段）：依据配置的调度算法与请求负载指标（`usage`、`text`、`bytes`）调度一个合适的 Prefill 实例。被选中实例的地址写入 PipelineContext。
3. **prefill_execute**（Execute 阶段）：构造 Prefill 请求并发送给选定的 Prefill 实例，处理响应以提取 KV Cache 传输参数与 prompt token 数，然后存入 PipelineContext 供 Decode 阶段使用。
4. **decode_schedule**（Schedule 阶段）：调度一个合适的 Decode 实例。调度可以利用 Prefill 响应中校准过的 prompt token 数，实现更准确的负载均衡。
5. **decode_execute**（Execute 阶段）：构造 Decode 请求（注入来自 Prefill 的 KV 传输参数），发送给选定的 Decode 实例，处理响应并以流式方式回写给客户端。
6. **PostProcess**（内置）：收尾请求、更新指标、处理错误。

### 请求构造

#### Prefill 请求构造（`prefill_prepare_request`）

Prefill 请求由客户端原始请求派生，但改动较大 —— Prefill 阶段的目标只是计算 KV Cache，并不生成完整补全结果：

1. **强制 `max_tokens=1`**：`max_tokens` 参数被设为 `1`（若存在 `max_completion_tokens`，同样设为 `1`）。Prefill 实例只需生成一个 token，即可算出 prompt 的完整 KV Cache。

2. **关闭流式**：无论原始请求如何，都强制 `"stream": false`，并移除 `stream_options`。Prefill 阶段始终以非流式方式运行。

3. **剥离 KV 传输参数**：请求体中已有的 `kv_transfer_params` 全部移除。这些参数由 Prefill 实例在响应中产出，而不是被消费。

4. **注入 EC 传输参数（若存在）**：在 EPD 模式下运行（上游有 Encode 阶段）时，Encode 阶段的 `ec_transfer_params` 会被注入 Prefill 请求体。标准 PD 模式下这一步是空操作。

5. **移除 `min_tokens`**：`min_tokens` 参数被移除，因为它只在 Decode 阶段才有意义。

**示例 —— 客户端发送一个流式对话补全请求：**

```json
{
  "model": "my-model",
  "messages": [
    {"role": "user", "content": "Explain quantum computing in simple terms"}
  ],
  "stream": true,
  "stream_options": {"include_usage": true},
  "max_tokens": 1024,
  "temperature": 0.7
}
```

**转发给 vLLM 实例的 Prefill 请求被修改为：**

```json
{
  "model": "my-model",
  "messages": [
    {"role": "user", "content": "Explain quantum computing in simple terms"}
  ],
  "stream": false,
  "max_tokens": 1,
  "temperature": 0.7
}
```

Prefill 实例的响应包含 KV Cache 传输参数（`kv_transfer_params`）与 usage 信息（prompt token 数），这些内容被提取并存入 PipelineContext。

#### Decode 请求构造（`decode_prepare_request`）

Decode 请求同样由客户端原始请求派生，但改动方式不同：

1. **注入 KV 传输参数**：从 Prefill 响应中提取的 `kv_transfer_params` 被注入请求体。这告诉 Decode 实例去哪里找预先算好的 KV Cache。

2. **保留流式选项**：若原始客户端请求是流式请求（`"stream": true`），`stream_options` 参数原样保留，Decode 实例因此可以把 token 流式回传给客户端。

3. **保留原始 `max_tokens`**：客户端原始的 `max_tokens` 取值保持不变，Decode 实例因此能生成完整补全结果。

**示例 —— 转发给 vLLM 实例的 Decode 请求：**

```json
{
  "model": "my-model",
  "messages": [
    {"role": "user", "content": "Explain quantum computing in simple terms"}
  ],
  "stream": true,
  "stream_options": {"include_usage": true},
  "max_tokens": 1024,
  "temperature": 0.7,
  "kv_transfer_params": {
    "kv_cache_url": "...",
    "kv_cache_id": "..."
  }
}
```

### 响应处理

#### Prefill 响应（`prefill_post_response`）

Prefill 响应不会转发给客户端，而是从中提取以下数据：

- **KV 传输参数**：从 Prefill 响应体提取 `kv_transfer_params`，存入 PipelineContext，供后续 Decode 阶段使用。
- **Prompt token 数**：从 usage 信息中提取实际的 `prompt_tokens` 数量，用于校准 prompt 长度。校准后的长度能为 Decode 调度提供更准确的负载信息。
- **缓存 token 数**：若存在，记录被缓存（前缀匹配）的 token 数，用于指标。

若 Prefill 响应缺少 `kv_transfer_params`，且该请求是流式对话请求，则视为错误。

#### Decode 响应（`decode_post_response`）

Decode 响应被写回客户端：

- **流式**：响应通过 SSE（Server-Sent Events）逐块转发。V1 与旧版流式写入器均受支持。
- **非流式**：一次性写出完整响应体。若首个 token 已由 Prefill 产出，则把它前置到响应中。

#### 阶段后处理

**Prefill**（`regular_post_phase`）：只记录一条 trace 事件，表示 Prefill 阶段已完成。

**Decode**（`complete_post_phase`）：响应发出后，计算以下指标并写入 trace 日志：

| 指标 | 说明 |
|---|---|
| **TTFT**（Time to First Token） | 从请求开始到首个输出 token 的时间 |
| **TPOT**（Time Per Output Token） | Decode 期间每个输出 token 的平均耗时 |
| **Latency** | 请求总时延 |
| **First Token Latency** | 从 Decode 开始到首个 token 的时间 |
| **First D-Token Latency** | 从第一个 Decode token 到第二个 token 的时间 |
| **Second D-Token Latency** | 第一个与第二个 Decode token 之间的时间 |

### 与其他模式对比

| 维度 | PD 分离 | PD Hybrid | EPD 分离 |
|---|---|---|---|
| **Infer mode** | `PD` / `PD-AM` | `Hybrid` | `EPD` |
| **阶段** | Schedule + Execute x2 | Schedule + Execute | Schedule + Execute x3 |
| **实例角色** | Prefill + Decode | 仅 Decode | Encode + Prefill + Decode |
| **KV 传输** | 必需（Prefill -> Decode） | 不需要 | 必需（Encode -> Prefill -> Decode） |
| **Prefill 请求** | `max_tokens=1`，非流式 | 不适用（内置于 Decode） | `max_tokens=1`，非流式 |
| **Decode 请求** | 注入 KV 参数 | 完整原始请求 | 注入 KV 参数 |
| **适用场景** | P/D 独立扩缩容 | 简单单实例 | 多模态、完全分离 |

### 相关文档

- [关于 Infer 流水线](./about-infer-pipeline.zh-CN.md)
- [PD Hybrid](./pd-hybrid.zh-CN.md)
- [EPD 分离](./epd-disaggregate.zh-CN.md)
- [Layerwise CDCP](./layerwise-cdcp.zh-CN.md)
