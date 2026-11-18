# PD Disaggregate

[English](pd-disaggregate.md) | [简体中文](pd-disaggregate.zh-CN.md)

## Deployment Mode Overview

PD Disaggregate separates the Prefill and Decode stages into independent vLLM instances, allowing each to be scaled independently. In this mode, the Prefill instance processes the prompt and computes KV Cache, which is then transferred to a Decode instance for token generation.

This mode is suitable for scenarios where:

- Prefill and Decode have different resource requirements (Prefill is compute-bound, Decode is memory-bound) and need independent scaling
- High throughput is required by decoupling prompt processing from token generation
- KV Cache transfer between Prefill and Decode instances is acceptable

### Enabling PD Disaggregate Mode

PD Disaggregate mode is enabled by setting `infer-mode` to `PD` (or `PD-AM` for deployments with additional MoE expert instances):

```bash
# Standard PD disaggregate
deploy.sh ... --infer-mode PD ...
```

The valid infer mode values for PD Disaggregate are:

| Value | Description |
|---|---|
| `PD` | Standard PD disaggregate with Prefill and Decode roles |


### Supported API Endpoints

PD Disaggregate mode supports the following OpenAI-compatible endpoints:

- `POST /v1/chat/completions` — Chat completions
- `POST /v1/completions` — Text completions

## Pipeline Definition

### Pipeline Configuration

The PD Disaggregate pipeline is defined in [deploy/pipelines/pd_separate.yaml](../../deploy/pipelines/pd_separate.yaml). It consists of four phases:

| Phase | Type | Instance Role | Depends On |
|---|---|---|---|
| `prefill_schedule` | Schedule | prefill | — |
| `prefill_execute` | Execute | prefill | `prefill_schedule` |
| `decode_schedule` | Schedule | decode | `prefill_execute` |
| `decode_execute` | Execute | decode | `decode_schedule` |

### Execution Flow

The phases execute in strict serial order — Prefill must complete before Decode begins, because the Decode stage depends on the KV Cache produced by the Prefill stage:

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

The detailed steps are:

1. **Preprocess** (built-in): Parse the request body, validate parameters, extract prompt length and other metadata.
2. **prefill_schedule** (Schedule Phase): Schedule an appropriate Prefill instance based on the configured scheduling algorithm and request load metrics (`usage`, `text`, `bytes`). The selected instance address is written into PipelineContext.
3. **prefill_execute** (Execute Phase): Construct a Prefill request, send it to the scheduled Prefill instance, process the response to extract KV Cache transfer parameters and prompt token count, then store them in PipelineContext for the Decode stage.
4. **decode_schedule** (Schedule Phase): Schedule an appropriate Decode instance. The scheduling can leverage the calibrated prompt token count from the Prefill response for more accurate load balancing.
5. **decode_execute** (Execute Phase): Construct a Decode request (injecting the KV transfer parameters from Prefill), send it to the scheduled Decode instance, process the response and stream it back to the client.
6. **PostProcess** (built-in): Finalize the request, update metrics, handle any errors.

### Request Construction

#### Prefill Request Construction (`prefill_prepare_request`)

The Prefill request is derived from the original client request with significant modifications, since the goal of the Prefill stage is only to compute the KV Cache (not to generate the full completion):

1. **Force `max_tokens=1`**: The `max_tokens` parameter is set to `1` (and `max_completion_tokens` is similarly set to `1` if present). The Prefill instance only needs to generate a single token to compute the full KV Cache for the prompt.

2. **Disable Streaming**: `"stream": false` is forced regardless of the original request, and `stream_options` is removed. The Prefill stage always runs in non-streaming mode.

3. **Strip KV Transfer Parameters**: Any existing `kv_transfer_params` in the request body are removed. These will be produced by the Prefill instance in its response, not consumed.

4. **Inject EC Transfer Parameters (if present)**: When running in EPD mode (with an upstream Encode stage), the `ec_transfer_params` from the Encode phase are injected into the Prefill request body. In standard PD mode, this step is a no-op.

5. **Remove `min_tokens`**: The `min_tokens` parameter is removed, as it is only relevant during the Decode stage.

**Example — Client sends a streaming chat completion request:**

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

**The Prefill request forwarded to the vLLM instance is modified to:**

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

The response from the Prefill instance contains the KV Cache transfer parameters (`kv_transfer_params`) and usage information (prompt token count), which are extracted and stored in PipelineContext.

#### Decode Request Construction (`decode_prepare_request`)

The Decode request is also derived from the original client request, but with different modifications:

1. **Inject KV Transfer Parameters**: The `kv_transfer_params` extracted from the Prefill response are injected into the request body. This tells the Decode instance where to find the precomputed KV Cache.

2. **Preserve Stream Options**: If the original client request is a streaming request (`"stream": true`), the `stream_options` parameter is preserved as-is, allowing the Decode instance to stream tokens back to the client.

3. **Preserve Original `max_tokens`**: The original `max_tokens` value from the client is kept unchanged, allowing the Decode instance to generate the full completion.

**Example — The Decode request forwarded to the vLLM instance:**

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

### Response Handling

#### Prefill Response (`prefill_post_response`)

The Prefill response is not forwarded to the client. Instead, the following data is extracted:

- **KV Transfer Parameters**: The `kv_transfer_params` from the Prefill response body are extracted and stored in PipelineContext for the subsequent Decode stage.
- **Prompt Token Count**: The actual `prompt_tokens` count from the usage information is extracted and used to calibrate the prompt length. This calibrated length provides more accurate load information for Decode scheduling.
- **Cached Tokens**: If present, the number of cached (prefix-matched) tokens is recorded for metrics.

If the Prefill response lacks `kv_transfer_params` and the request is a streaming chat request, this is treated as an error.

#### Decode Response (`decode_post_response`)

The Decode response is written back to the client:

- **Streaming**: The response is forwarded chunk by chunk using SSE (Server-Sent Events). Both V1 and legacy streaming writers are supported.
- **Non-streaming**: The full response body is written at once. If the first token was already produced by Prefill, it is prepended to the response.

#### Post-Phase Processing

**Prefill** (`regular_post_phase`): Only records a trace event indicating the Prefill phase has completed.

**Decode** (`complete_post_phase`): After the response is sent, the following metrics are computed and recorded into trace log:

| Metric | Description |
|---|---|
| **TTFT** (Time to First Token) | Time from request start to first output token |
| **TPOT** (Time Per Output Token) | Average time per output token during Decode |
| **Latency** | Total request latency |
| **First Token Latency** | Time from Decode start to first token |
| **First D-Token Latency** | Time from first Decode token to second token |
| **Second D-Token Latency** | Time between first and second Decode tokens |

### Comparison with Other Modes

| Aspect | PD Disaggregate | PD Hybrid | EPD Disaggregate |
|---|---|---|---|
| **Infer mode** | `PD` / `PD-AM` | `Hybrid` | `EPD` |
| **Phases** | Schedule + Execute x2 | Schedule + Execute | Schedule + Execute x3 |
| **Instance roles** | Prefill + Decode | Decode only | Encode + Prefill + Decode |
| **KV transfer** | Required (Prefill -> Decode) | Not needed | Required (Encode -> Prefill -> Decode) |
| **Prefill request** | `max_tokens=1`, non-stream | N/A (built into Decode) | `max_tokens=1`, non-stream |
| **Decode request** | Injects KV params | Full original request | Injects KV params |
| **Use case** | Independent P/D scaling | Simple single-instance | Multi-modal, full separation |

### Related Documentation

- [About Infer Pipeline](./about-infer-pipeline.md)
- [PD Hybrid](./pd-hybrid.md)
- [EPD Disaggregate](./epd-disaggregate.md)
- [Layerwise CDCP](./layerwise-cdcp.md)
