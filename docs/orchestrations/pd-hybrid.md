# PD Hybrid

[English](pd-hybrid.md) | [简体中文](pd-hybrid.zh-CN.md)

## Deployment Mode Overview

PD Hybrid is the simplest deployment mode in RouteServer. In this mode, Prefill and Decode stages are co-located on the same vLLM inference instance. A single instance handles the entire inference lifecycle — from prompt processing to token generation — eliminating the need for KV Cache transfer between separate Prefill and Decode instances.

This mode is suitable for scenarios where:

- Inference instances are homogeneous and do not require independent scaling of Prefill and Decode
- KV Cache transfer overhead between instances is a concern
- A simple single-stage scheduling strategy is preferred

### Enabling PD Hybrid Mode

PD Hybrid mode is enabled by setting `infer-mode` to `Hybrid`:

```bash
# Via command-line option (using deploy.sh)
deploy.sh ... --infer-mode Hybrid ...
```

The valid infer mode value for PD Hybrid is `Hybrid`.

### Supported API Endpoints

PD Hybrid mode supports the following OpenAI-compatible endpoints:

- `POST /v1/chat/completions` — Chat completions
- `POST /v1/completions` — Text completions

## Pipeline Definition

### Pipeline Configuration

The PD Hybrid pipeline is defined in [deploy/pipelines/hybrid.yaml](../../deploy/pipelines/hybrid.yaml):

### Execution Flow

Unlike PD Disaggregate or EPD modes, PD Hybrid has no separate Prefill phase. The pipeline consists of only two phases, both targeting the `decode` instance type (which handles both Prefill and Decode):

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

The detailed steps are:

1. **Preprocess** (built-in): Parse the request body, validate parameters, extract prompt length and other metadata.
2. **req_schedule** (Schedule Phase): Schedule an appropriate inference instance of type `decode` based on the configured scheduling algorithm and request load metrics (`usage`, `text`, `bytes`). The selected instance address is written into PipelineContext.
3. **req_execute** (Execute Phase): Read the scheduling result from PipelineContext, construct the Decode request, send it to the scheduled instance, process the response and stream it back to the client, and finally compute performance metrics.
4. **PostProcess** (built-in): Finalize the request, update metrics, handle any errors.

### Request Construction

#### Decode Request Construction (`decode_prepare_request`)

Since PD Hybrid has no separate Prefill phase, the request sent to the inference instance is the original client request with minimal modifications. The request construction logic takes the client's original request body and applies the following changes:

1. **Inject KV Transfer Parameters (if present)**: When running in E-Hybrid mode (with an upstream Encode stage), the `ec_transfer_params` from the Encode phase are injected into the request body. In standard Hybrid mode, this step is a no-op since no Encode phase precedes the Decode stage.

2. **Preserve Stream Options**: If the original client request is a streaming request (`"stream": true`), the `stream_options` parameter is preserved as-is in the request sent to the inference instance. This allows the vLLM instance to stream tokens back.

3. **No max_tokens Modification**: The Decode request preserves the original `max_tokens` value from the client. This allows the inference instance to generate the full completion in a single pass.

**Example — Streaming chat completion request sent to the inference instance:**

Client sends:
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

The request forwarded to the vLLM instance is the same body — no `max_tokens` override, no stream mode change. The inference instance handles both prompt processing and token generation internally.



#### Response Handling (`decode_post_response`)

The response handling for PD Hybrid writes the inference result back to the client:

- **Streaming**: For streaming requests, the response is forwarded chunk by chunk using SSE (Server-Sent Events). Both V1 and legacy streaming writers are supported, controlled by the `v1-stream-writing-enable` config option.
- **Non-streaming**: For non-streaming requests, the full response body is written at once.

#### Post-Phase Processing (`complete_post_phase`)

After the response is sent, the following metrics are computed and recorded into trace log:

| Metric | Description |
|---|---|
| **TTFT** (Time to First Token) | Time from request start to first output token |
| **TPOT** (Time Per Output Token) | Average time per output token during Decode |
| **Latency** | Total request latency |
| **First Token Latency** | Time from Decode start to first token |
| **First D-Token Latency** | Time from first Decode token to second token |
| **Second D-Token Latency** | Time between first and second Decode tokens |

### Comparison with Other Modes

| Aspect | PD Hybrid | PD Disaggregate | EPD Disaggregate |
|---|---|---|---|
| **Infer mode** | `Hybrid` | `PD` | `EPD` |
| **Phases** | Schedule + Execute | Schedule + Execute × 2 | Schedule + Execute × 3 |
| **Instance roles** | Decode only | Prefill + Decode | Encode + Prefill + Decode |
| **KV transfer** | Not needed | Required (Prefill → Decode) | Required (Encode → Prefill → Decode) |
| **Prefill request** | N/A (built into Decode) | `max_tokens=1`, non-stream | `max_tokens=1`, non-stream |
| **Decode request** | Full original request | Injects KV params | Injects KV params |
| **Use case** | Simple single-instance | Independent P/D scaling | Multi-modal, full separation |

### Related Documentation

- [About Infer Pipeline](./about-infer-pipeline.md)
- [PD Disaggregate](./pd-disaggregate.md)
- [EPD Disaggregate](./epd-disaggregate.md)
- [Layerwise CDCP](./layerwise-cdcp.md)
