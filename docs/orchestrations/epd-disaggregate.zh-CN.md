# EPD 分离

[English](epd-disaggregate.md) | [简体中文](epd-disaggregate.zh-CN.md)

## 部署模式概述

EPD 分离把推理生命周期完全拆分为三个独立角色 —— Encode、Prefill 和 Decode —— 每个角色都运行在专用的 vLLM 实例上。这是 RouteServer 中粒度最细的部署模式，面向需要在文本生成之前对视觉输入（图像）进行编码的多模态推理场景。

在此模式下：

- **Encode** 实例处理视觉输入（图像），产出编码表示（视觉嵌入）和 EC（Expert Cache）传输参数。
- **Prefill** 实例处理文本与视觉嵌入的组合，计算 KV Cache。
- **Decode** 实例消费 KV Cache 生成输出 token。

本模式适用于以下场景：

- 多模态模型（例如视觉语言模型）配合独立的 Encode 实例部署
- 各阶段资源需求不同，需要独立扩缩容
- 纯文本请求（不含图像）可以跳过 Encode 阶段

### 启用 EPD 分离模式

把 `infer-mode` 设为 `EPD` 即可启用 EPD 分离模式：

```bash
deploy.sh ... --infer-mode EPD ...
```

EPD 分离的有效 infer mode 取值为 `EPD`。

### 支持的 API 端点

EPD 分离模式支持以下 OpenAI 兼容端点：

- `POST /v1/chat/completions` — 对话补全
- `POST /v1/completions` — 文本补全

## 流水线定义

### 流水线配置

EPD 分离流水线定义在 [deploy/pipelines/epd.yaml](../../deploy/pipelines/epd.yaml) 中，由六个阶段组成：

| 阶段 | 类型 | 实例角色 | 跳过策略 | 依赖 |
|---|---|---|---|---|
| `encode_schedule` | Schedule | encode | `skip_on_zero_image` | — |
| `encode_execute` | Execute | encode | — | `encode_schedule` |
| `prefill_schedule` | Schedule | prefill | `no_skip` | `encode_execute` |
| `prefill_execute` | Execute | prefill | — | `prefill_schedule` |
| `decode_schedule` | Schedule | decode | — | `prefill_execute` |
| `decode_execute` | Execute | decode | — | `decode_schedule` |

### 执行流程

各阶段在三个 stage 上按严格串行顺序执行。当请求不含图像时，Encode 阶段会被条件跳过：

```
    ┌──────────────────────────────────────────────────┐
    │                   Preprocess                     │
    │                 (built-in)                       │
    └──────────────────────┬───────────────────────────┘
                           │
                           ▼
    ┌──────────────────────────────────────────────────┐
    │           encode_schedule (Schedule)              │
    │  instance_type: encode                           │
    │  skip_policy: skip_on_zero_image                 │
    └──────────┬───────────────────────────────────────┘
               │
               │  has images ──────────────────┐  no images
               ▼                               │  (skipped)
    ┌──────────────────────┐                    │
    │ encode_execute       │                    │
    │ (Execute, encode)    │                    │
    │                      │                    │
    │ 1. encode_prepare    │                    │
    │    _request          │                    │
    │ 2. Send to encode    │                    │
    │    instance          │                    │
    │ 3. encode_post_      │                    │
    │    response          │                    │
    │    (extract ec       │                    │
    │     transfer params) │                    │
    └──────────┬───────────┘                    │
               │                                │
               └──────────┬────────────────────┘
                          │
               │
               ▼
    ┌──────────────────────────────────────────────────┐
    │           prefill_schedule (Schedule)             │
    │  instance_type: prefill                          │
    │  request_load: [usage, text, bytes]              │
    └──────────────────────┬───────────────────────────┘
                           │
                           ▼
    ┌──────────────────────────────────────────────────┐
    │           prefill_execute (Execute)               │
    │  dependsOn: prefill_schedule                     │
    │                                                   │
    │  1. prefill_prepare_request                      │
    │     (inject ec_transfer_params from Encode)      │
    │  2. Send request to scheduled prefill instance   │
    │  3. prefill_post_response                        │
    │     (extract kv_transfer_params)                 │
    └──────────────────────┬───────────────────────────┘
                           │
                           ▼
    ┌──────────────────────────────────────────────────┐
    │           decode_schedule (Schedule)              │
    │  instance_type: decode                            │
    │  dependsOn: prefill_execute                       │
    └──────────────────────┬───────────────────────────┘
                           │
                           ▼
    ┌──────────────────────────────────────────────────┐
    │           decode_execute (Execute)                │
    │  dependsOn: decode_schedule                      │
    │                                                   │
    │  1. decode_prepare_request                       │
    │     (inject kv_transfer_params from Prefill)     │
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

1. **Preprocess**（内置）：解析请求体，校验参数，检测请求是否包含图像。
2. **encode_schedule**（Schedule 阶段）：若请求包含图像，则调度一个 Encode 实例；若不含图像，则整个阶段跳过（由 `skip_policy: skip_on_zero_image` 控制）。
3. **encode_execute**（Execute 阶段）：构造 Encode 请求，发送给已调度的 Encode 实例，处理响应以提取 EC 传输参数和 prompt token 数，然后存入 PipelineContext。
4. **prefill_schedule**（Schedule 阶段）：根据配置的调度算法和请求负载指标（`usage`、`text`、`bytes`）调度合适的 Prefill 实例。
5. **prefill_execute**（Execute 阶段）：构造 Prefill 请求（注入来自 Encode 阶段的 `ec_transfer_params`），发送给已调度的 Prefill 实例，处理响应以提取 KV Cache 传输参数。
6. **decode_schedule**（Schedule 阶段）：使用 Prefill 响应中校准后的 prompt token 数调度合适的 Decode 实例。
7. **decode_execute**（Execute 阶段）：构造 Decode 请求（注入来自 Prefill 的 `kv_transfer_params`），发送给已调度的 Decode 实例，处理响应并以流式回传给客户端。
8. **PostProcess**（内置）：收尾请求，更新指标，处理各种错误。

### 请求构造

#### Encode 请求构造（`encode_prepare_request`）

Encode 请求基于客户端原始请求派生，修改方式与 Prefill 请求类似，因为目标只是计算视觉嵌入：

1. **强制 `max_tokens=1`**：`max_tokens` 参数设为 `1`（`max_completion_tokens` 同样设为 `1`）。Encode 实例只需产出编码表示，不需要生成 token。

2. **禁用流式**：强制 `"stream": false`，并移除 `stream_options`。Encode 阶段始终以非流式模式运行。

3. **剥离传输参数**：从请求体中移除 `ec_transfer_params` 和 `kv_transfer_params`。这两者分别由 Encode 和 Prefill 实例产出，而不是在这里消费。

4. **移除 `min_tokens`**：`min_tokens` 参数被移除。

**示例 —— 客户端发送一个带图像的多模态对话补全请求：**

```json
{
  "model": "my-model",
  "messages": [
    {
      "role": "user",
      "content": [
        {"type": "image_url", "image_url": {"url": "https://example.com/photo.jpg"}},
        {"type": "text", "text": "What is shown in this image?"}
      ]
    }
  ],
  "stream": true,
  "max_tokens": 512
}
```

**转发给 vLLM 实例的 Encode 请求被修改为：**

```json
{
  "model": "my-model",
  "messages": [
    {
      "role": "user",
      "content": [
        {"type": "image_url", "image_url": {"url": "https://example.com/photo.jpg"}},
        {"type": "text", "text": "What is shown in this image?"}
      ]
    }
  ],
  "stream": false,
  "max_tokens": 1
}
```

Encode 实例的响应包含 EC 传输参数（`ec_transfer_params`）和 usage 信息（prompt token 数），这些内容会被提取并存入 PipelineContext。

#### Prefill 请求构造（`prefill_prepare_request`）

Prefill 请求的构造方式与 PD 分离模式类似，只多了一步：

1. **强制 `max_tokens=1`**：设为 `1`（`max_completion_tokens` 同样设为 `1`）。这里只需要计算 KV Cache。

2. **禁用流式**：强制 `"stream": false`，移除 `stream_options`。

3. **剥离 KV 传输参数**：移除任何已存在的 `kv_transfer_params`（它们将由 Prefill 响应本身给出）。

4. **注入 EC 传输参数**：从 Encode 响应中提取的 `ec_transfer_params` 被注入 Prefill 请求体。这告诉 Prefill 实例去哪里取编码后的视觉表示。

5. **移除 `min_tokens`**：移除。

**示例 —— 转发给 vLLM 实例的 Prefill 请求：**

```json
{
  "model": "my-model",
  "messages": [
    {
      "role": "user",
      "content": [
        {"type": "image_url", "image_url": {"url": "https://example.com/photo.jpg"}},
        {"type": "text", "text": "What is shown in this image?"}
      ]
    }
  ],
  "stream": false,
  "max_tokens": 1,
  "ec_transfer_params": {
    "ec_cache_url": "...",
    "ec_cache_id": "..."
  }
}
```

Prefill 实例的响应包含 KV Cache 传输参数（`kv_transfer_params`），这些内容会被提取并存入 PipelineContext，供 Decode 阶段使用。

#### Decode 请求构造（`decode_prepare_request`）

Decode 请求与 PD 分离模式完全一致：

1. **注入 KV 传输参数**：从 Prefill 响应中提取的 `kv_transfer_params` 被注入请求体。

2. **保留流式选项**：如果客户端原始请求是流式请求，则保留 `stream_options` 参数。

3. **保留原始 `max_tokens`**：客户端原始的 `max_tokens` 值保持不变。

**示例 —— 转发给 vLLM 实例的 Decode 请求：**

```json
{
  "model": "my-model",
  "messages": [
    {
      "role": "user",
      "content": [
        {"type": "image_url", "image_url": {"url": "https://example.com/photo.jpg"}},
        {"type": "text", "text": "What is shown in this image?"}
      ]
    }
  ],
  "stream": true,
  "stream_options": {"include_usage": true},
  "max_tokens": 512,
  "kv_transfer_params": {
    "kv_cache_url": "...",
    "kv_cache_id": "..."
  }
}
```

### 跳过策略：零图像时跳过 Encode

对纯文本请求，Encode 阶段会被条件跳过。这由 `encode_schedule` 阶段上的 `skip_policy: skip_on_zero_image` 配置控制：

```yaml
- name: encode_schedule
  type: Schedule
  schedule_params:
    instance_type: encode
    skip_policy: skip_on_zero_image
    request_load:
      - image
      - text
      - bytes
```

当请求不含图像时：

- `encode_schedule` 和 `encode_execute` 阶段整体跳过。
- 流水线从 Preprocess 直接进入 `prefill_schedule`。
- 不会向 Prefill 请求注入 `ec_transfer_params`（因为没有需要传输的视觉数据）。

当请求包含一张或多张图像时：

- Encode 阶段正常执行。
- Encode 响应中的 `ec_transfer_params` 被注入 Prefill 请求。
- 流水线按上文执行流程所述的方式运行。

### 响应处理

#### Encode 响应（`encode_post_response`）

Encode 的响应不会转发给客户端。以下数据会被提取：

- **EC 传输参数**：从 Encode 响应体中提取 `ec_transfer_params`，存入 PipelineContext，供随后的 Prefill 阶段使用。
- **Prompt Token 数**：提取实际的 `prompt_tokens` 数量，用于负载感知调度。

#### Prefill 响应（`prefill_post_response`）

与 PD 分离模式相同：

- **KV 传输参数**：提取并存给 Decode 阶段。
- **Prompt Token 数**：校准后用于 Decode 调度。
- **缓存 Token**：记录到指标。
- **首 Token 响应体**：保存下来，供 Decode 阶段可能复用。

#### Decode 响应（`decode_post_response`）

Decode 的响应写回客户端：

- **流式**：通过 SSE 逐块转发。
- **非流式**：一次性写入完整响应体。

### 后置阶段处理

**Encode**（`regular_post_phase`）：只记录一个 trace 事件，表示 Encode 阶段已完成。

**Prefill**（`regular_post_phase`）：只记录一个 trace 事件，表示 Prefill 阶段已完成。

**Decode**（`complete_post_phase`）：响应发出后，计算以下指标并写入 trace 日志：

| 指标 | 说明 |
|---|---|
| **TTFT**（Time to First Token） | 从请求开始到首个输出 token 的时间 |
| **TPOT**（Time Per Output Token） | Decode 期间每个输出 token 的平均耗时 |
| **Latency** | 请求总延迟 |
| **First Token Latency** | 从 Decode 开始到首个 token 的时间 |
| **First D-Token Latency** | 从 Decode 首个 token 到第二个 token 的时间 |
| **Second D-Token Latency** | Decode 首个与第二个 token 之间的间隔 |

### 与其他模式的对比

| 方面 | EPD 分离 | PD 分离 | PD Hybrid |
|---|---|---|---|
| **Infer mode** | `EPD` | `PD` / `PD-AM` | `Hybrid` |
| **阶段** | Schedule + Execute ×3 | Schedule + Execute ×2 | Schedule + Execute |
| **实例角色** | Encode + Prefill + Decode | Prefill + Decode | 仅 Decode |
| **KV 传输** | 必需（Encode -> Prefill -> Decode） | 必需（Prefill -> Decode） | 不需要 |
| **Encode 跳过** | `skip_on_zero_image` | N/A | N/A |
| **Prefill 请求** | `max_tokens=1`、非流式、注入 EC 参数 | `max_tokens=1`、非流式 | N/A（内建于 Decode） |
| **Decode 请求** | 注入 KV 参数 | 注入 KV 参数 | 完整原始请求 |
| **适用场景** | 多模态、完全分离 | P/D 独立扩缩容 | 简单单实例 |

### 相关文档

- [About Infer Pipeline](./about-infer-pipeline.zh-CN.md)
- [PD Hybrid](./pd-hybrid.zh-CN.md)
- [PD 分离](./pd-disaggregate.zh-CN.md)
- [Layerwise CDCP](./layerwise-cdcp.zh-CN.md)
