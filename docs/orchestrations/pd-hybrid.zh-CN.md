# PD Hybrid 混合部署

[English](pd-hybrid.md) | [简体中文](pd-hybrid.zh-CN.md)

## 部署模式概览

PD Hybrid 是 RouteServer 中最简单的部署模式。在该模式下，Prefill 与 Decode 阶段共用同一个 vLLM 推理实例。单个实例承担从提示词处理到 token 生成的完整推理生命周期，因此无需在独立的 Prefill 实例与 Decode 实例之间传输 KV cache。

该模式适用于以下场景：

- 推理实例同构，不需要对 Prefill 与 Decode 做独立扩缩容
- 关注实例之间的 KV cache 传输开销
- 希望采用简单的单阶段调度策略

### 启用 PD Hybrid 模式

PD Hybrid 模式通过将 `infer-mode` 设置为 `Hybrid` 来启用：

```bash
# Via command-line option (using deploy.sh)
deploy.sh ... --infer-mode Hybrid ...
```

PD Hybrid 的合法 infer mode 取值为 `Hybrid`。

### 支持的 API 端点

PD Hybrid 模式支持以下 OpenAI 兼容端点：

- `POST /v1/chat/completions` —— 聊天补全
- `POST /v1/completions` —— 文本补全

## 流水线定义

### 流水线配置

PD Hybrid 流水线定义在 [deploy/pipelines/hybrid.yaml](../../deploy/pipelines/hybrid.yaml) 中：

### 执行流程

与 PD Disaggregate 或 EPD 模式不同，PD Hybrid 没有独立的 Prefill 阶段。该流水线仅包含两个阶段，二者都面向 `decode` 实例类型（该类型同时处理 Prefill 与 Decode）：

```
    ┌──────────────────────────────────────────────┐
    │                Preprocess                    │
    │              (built-in)                      │
    └─────────────────────┬────────────────────────┘
                          │
                          ▼
    ┌──────────────────────────────────────────────┐
    │              req_schedule (Schedule)         │
    │  instance_type: decode                       │
    │  request_load: [usage, text, bytes]          │
    └─────────────────────┬────────────────────────┘
                          │
                          ▼
    ┌──────────────────────────────────────────────┐
    │              req_execute (Execute)           │
    │  dependsOn: req_schedule                     │
    │                                              │
    │  1. decode_prepare_request                   │
    │  2. Send request to scheduled instance       │
    │  3. decode_post_response                     │
    │  4. complete_post_phase (metrics)            │
    └─────────────────────┬────────────────────────┘
                          │
                          ▼
    ┌──────────────────────────────────────────────┐
    │                PostProcess                   │
    │              (built-in)                      │
    └──────────────────────────────────────────────┘
```

详细步骤如下：

1. **Preprocess**（内置）：解析请求体，校验参数，提取提示词长度等元数据。
2. **req_schedule**（调度阶段）：根据配置的调度算法与请求负载指标（`usage`、`text`、`bytes`）调度一个类型为 `decode` 的合适推理实例。选中的实例地址写入 PipelineContext。
3. **req_execute**（执行阶段）：从 PipelineContext 读取调度结果，构造 Decode 请求，发送到已调度的实例，处理响应并将其流式回传给客户端，最后计算性能指标。
4. **PostProcess**（内置）：结束请求，更新指标，处理可能出现的错误。

### 请求构造

#### Decode 请求构造（`decode_prepare_request`）

由于 PD Hybrid 没有独立的 Prefill 阶段，发送给推理实例的请求就是原始客户端请求，仅做了最小改动。请求构造逻辑取客户端原始请求体，并施加以下改动：

1. **注入 KV 传输参数（若存在）**：在 E-Hybrid 模式下运行时（上游存在 Encode 阶段），Encode 阶段的 `ec_transfer_params` 会被注入请求体。在标准 Hybrid 模式下，由于 Decode 阶段之前没有 Encode 阶段，该步骤为空操作。

2. **保留流式选项**：若原始客户端请求是流式请求（`"stream": true`），则 `stream_options` 参数会原样保留在发往推理实例的请求中。这样 vLLM 实例即可将 token 流式回传。

3. **不改动 max_tokens**：Decode 请求保留客户端的原始 `max_tokens` 值。这样推理实例可以一次性生成完整补全内容。

**示例 —— 发送给推理实例的流式聊天补全请求：**

客户端发送：
```json
{
  "model": "my-model",
  "messages": [
    {"role": "user", "content": "Hello, what is AI?"}
  ],
  "stream": true,
  "stream_options": {"include_usage": true},
  "max_tokens": 512
}
```

转发给 vLLM 实例的请求就是同一份请求体 —— 不覆盖 `max_tokens`，也不改变流式模式。推理实例在内部同时完成提示词处理与 token 生成。



#### 响应处理（`decode_post_response`）

PD Hybrid 的响应处理将推理结果写回客户端：

- **流式**：对流式请求，响应通过 SSE（Server-Sent Events）逐块转发。同时支持 V1 与旧版流式写入器，由 `v1-stream-writing-enable` 配置项控制。
- **非流式**：对非流式请求，完整响应体一次性写出。

#### 后置阶段处理（`complete_post_phase`）

响应发送完成后，计算以下指标并记录到 trace 日志中：

| 指标 | 说明 |
|---|---|
| **TTFT** (Time to First Token) | 从请求开始到首个输出 token 的时间 |
| **TPOT** (Time Per Output Token) | Decode 期间每个输出 token 的平均耗时 |
| **Latency** | 请求总延迟 |
| **First Token Latency** | 从 Decode 开始到首个 token 的时间 |
| **First D-Token Latency** | 从首个 Decode token 到第二个 token 的时间 |
| **Second D-Token Latency** | 第一与第二个 Decode token 之间的时间 |

### 与其他模式的对比

| 维度 | PD Hybrid | PD Disaggregate | EPD Disaggregate |
|---|---|---|---|
| **Infer mode** | `Hybrid` | `PD` | `EPD` |
| **阶段** | Schedule + Execute | Schedule + Execute × 2 | Schedule + Execute × 3 |
| **实例角色** | 仅 Decode | Prefill + Decode | Encode + Prefill + Decode |
| **KV 传输** | 不需要 | 需要（Prefill → Decode） | 需要（Encode → Prefill → Decode） |
| **Prefill 请求** | 不适用（内置于 Decode） | `max_tokens=1`，非流式 | `max_tokens=1`，非流式 |
| **Decode 请求** | 完整原始请求 | 注入 KV 参数 | 注入 KV 参数 |
| **适用场景** | 简单单实例 | P/D 独立扩缩容 | 多模态、完全分离 |

### 相关文档

- [About Infer Pipeline](./about-infer-pipeline.zh-CN.md)
- [PD Disaggregate](./pd-disaggregate.zh-CN.md)
- [EPD Disaggregate](./epd-disaggregate.zh-CN.md)
- [Layerwise CDCP](./layerwise-cdcp.zh-CN.md)
