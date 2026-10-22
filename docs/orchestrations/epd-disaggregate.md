# EPD Disaggregate

## Deployment Mode Overview

EPD Disaggregate fully separates the inference lifecycle into three independent roles — Encode, Prefill, and Decode — each running on dedicated vLLM instances. This is the most granular deployment mode in ModelOrch, designed for multi-modal inference scenarios where visual inputs (images) need to be encoded before text generation.

In this mode:

- **Encode** instances process visual inputs (images) and produce encoded representations (visual embeddings) and EC (Expert Cache) transfer parameters.
- **Prefill** instances process the combined text and visual embeddings to compute KV Cache.
- **Decode** instances consume the KV Cache to generate the output tokens.

This mode is suitable for scenarios where:

- Multi-modal models (e.g., vision-language models) are deployed with separate Encode instances
- Each stage has distinct resource requirements and needs independent scaling
- Encode stage can be skipped for text-only requests (no images)

### Enabling EPD Disaggregate Mode

EPD Disaggregate mode is enabled by setting `infer-mode` to `EPD`:

```bash
deploy.sh ... --infer-mode EPD ...
```

The valid infer mode value for EPD Disaggregate is `EPD`.

### Supported API Endpoints

EPD Disaggregate mode supports the following OpenAI-compatible endpoints:

- `POST /v1/chat/completions` — Chat completions
- `POST /v1/completions` — Text completions

## Pipeline Definition

### Pipeline Configuration

The EPD Disaggregate pipeline is defined in [deploy/pipelines/epd.yaml](../../deploy/pipelines/epd.yaml). It consists of six phases:

| Phase | Type | Instance Role | Skip Policy | Depends On |
|---|---|---|---|---|
| `encode_schedule` | Schedule | encode | `skip_on_zero_image` | — |
| `encode_execute` | Execute | encode | — | `encode_schedule` |
| `prefill_schedule` | Schedule | prefill | `no_skip` | `encode_execute` |
| `prefill_execute` | Execute | prefill | — | `prefill_schedule` |
| `decode_schedule` | Schedule | decode | — | `prefill_execute` |
| `decode_execute` | Execute | decode | — | `decode_schedule` |

### Execution Flow

The phases execute in strict serial order across three stages. The Encode stage is conditionally skipped when the request contains no images:

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

The detailed steps are:

1. **Preprocess** (built-in): Parse the request body, validate parameters, detect whether the request contains images.
2. **encode_schedule** (Schedule Phase): Schedule an Encode instance if the request contains images. If no images are present, this phase is skipped entirely (controlled by `skip_policy: skip_on_zero_image`).
3. **encode_execute** (Execute Phase): Construct an Encode request, send it to the scheduled Encode instance, process the response to extract EC transfer parameters and prompt token count, then store them in PipelineContext.
4. **prefill_schedule** (Schedule Phase): Schedule an appropriate Prefill instance based on the configured scheduling algorithm and request load metrics (`usage`, `text`, `bytes`).
5. **prefill_execute** (Execute Phase): Construct a Prefill request (injecting the `ec_transfer_params` from the Encode stage), send it to the scheduled Prefill instance, process the response to extract KV Cache transfer parameters.
6. **decode_schedule** (Schedule Phase): Schedule an appropriate Decode instance using the calibrated prompt token count from the Prefill response.
7. **decode_execute** (Execute Phase): Construct a Decode request (injecting the `kv_transfer_params` from Prefill), send it to the scheduled Decode instance, process the response and stream it back to the client.
8. **PostProcess** (built-in): Finalize the request, update metrics, handle any errors.

### Request Construction

#### Encode Request Construction (`encode_prepare_request`)

The Encode request is derived from the original client request with modifications similar to the Prefill request, since the goal is only to compute visual embeddings:

1. **Force `max_tokens=1`**: The `max_tokens` parameter is set to `1` (and `max_completion_tokens` similarly to `1`). The Encode instance only needs to produce the encoded representations, not generate tokens.

2. **Disable Streaming**: `"stream": false` is forced and `stream_options` is removedasi. The Encode stage always runs in non-streaming mode.

3. **Strip Transfer Parameters**: Both `ec_transfer_params` and `kv_transfer_params` are removed from the request body. These will be produced by the Encode and Prefill instances respectively, not consumed.

4. **Remove `min_tokens`**: The `min_tokens` parameter is removed.

**Example — Client sends a multi-modal chat completion request with an image:**

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

**The Encode request forwarded to the vLLM instance is modified to:**

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

The response from the Encode instance contains the EC transfer parameters (`ec_transfer_params`) and usage information (prompt token count), which are extracted and stored in PipelineContext.

#### Prefill Request Construction (`prefill_prepare_request`)

The Prefill request is constructed similarly to PD Disaggregate mode, with one additional step:

1. **Force `max_tokens=1`**: Set to `1` (and `max_completion_tokens` similarly to `1`). Only need to compute KV Cache.

2. **Disable Streaming**: `"stream": false` is forced, `stream_options` is removed.

3. **Strip KV Transfer Parameters**: Any existing `kv_transfer_params` are removed (these will come from the Prefill response itself).

4. **Inject EC Transfer Parameters**: The `ec_transfer_params` extracted from the Encode response are injected into the Prefill request body. This tells the Prefill instance where to find the encoded visual representations.

5. **Remove `min_tokens`**: Removed.

**Example — The Prefill request forwarded to the vLLM instance:**

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

The response from the Prefill instance contains the KV Cache transfer parameters (`kv_transfer_params`), which are extracted and stored in PipelineContext for the Decode stage.

#### Decode Request Construction (`decode_prepare_request`)

The Decode request is identical to PD Disaggregate mode:

1. **Inject KV Transfer Parameters**: The `kv_transfer_params` extracted from the Prefill response are injected into the request body.

2. **Preserve Stream Options**: If the original client request is a streaming request, the `stream_options` parameter is preserved.

3. **Preserve Original `max_tokens`**: The original `max_tokens` value from the client is kept unchanged.

**Example — The Decode request forwarded to the vLLM instance:**

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

### Skip Policy: Encode on Zero Images

The Encode stage is conditionally skipped for text-only requests. This is controlled by the `skip_policy: skip_on_zero_image` configuration on the `encode_schedule` phase:

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

When a request contains no images:

- The `encode_schedule` and `encode_execute` phases are skipped entirely.
- The pipeline proceeds directly from Preprocess to `prefill_schedule`.
- No `ec_transfer_params` are injected into the Prefill request (since there is no visual data to transfer).

When a request contains one or more images:

- The Encode stage executes normally.
- The `ec_transfer_params` from the Encode response are injected into the Prefill request.
- The pipeline behaves as described in the execution flow above.

### Response Handling

#### Encode Response (`encode_post_response`)

The Encode response is not forwarded to the client. The following data is extracted:

- **EC Transfer Parameters**: The `ec_transfer_params` from the Encode response body are extracted and stored in PipelineContext for the subsequent Prefill stage.
- **Prompt Token Count**: The actual `prompt_tokens` count is extracted and used for load-aware scheduling.

#### Prefill Response (`prefill_post_response`)

Same as PD Disaggregate mode:

- **KV Transfer Parameters**: Extracted and stored for the Decode stage.
- **Prompt Token Count**: Calibrated and used for Decode scheduling.
- **Cached Tokens**: Recorded for metrics.
- **First Token Body**: Saved for potential reuse by the Decode stage.

#### Decode Response (`decode_post_response`)

The Decode response is written back to the client:

- **Streaming**: Forwarded chunk by chunk using SSE.
- **Non-streaming**: Full response body written at once.

### Post-Phase Processing

**Encode** (`regular_post_phase`): Only records a trace event indicating the Encode phase has completed.

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

| Aspect | EPD Disaggregate | PD Disaggregate | PD Hybrid |
|---|---|---|---|
| **Infer mode** | `EPD` | `PD` / `PD-AM` | `Hybrid` |
| **Phases** | Schedule + Execute x3 | Schedule + Execute x2 | Schedule + Execute |
| **Instance roles** | Encode + Prefill + Decode | Prefill + Decode | Decode only |
| **KV transfer** | Required (Encode -> Prefill -> Decode) | Required (Prefill -> Decode) | Not needed |
| **Encode skip** | `skip_on_zero_image` | N/A | N/A |
| **Prefill request** | `max_tokens=1`, non-stream, injects EC params | `max_tokens=1`, non-stream | N/A (built into Decode) |
| **Decode request** | Injects KV params | Injects KV params | Full original request |
| **Use case** | Multi-modal, full separation | Independent P/D scaling | Simple single-instance |

### Related Documentation

- [About Infer Pipeline](./about-infer-pipeline.md)
- [PD Hybrid](./pd-hybrid.md)
- [PD Disaggregate](./pd-disaggregate.md)
- [Layerwise CDCP](./layerwise-cdcp.md)
