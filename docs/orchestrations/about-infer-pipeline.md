# About Infer Pipeline

The Infer Pipeline is a core capability of ModelOrch. It provides a configuration-driven approach to assembling inference workflows, generating a DAG-based execution pipeline by declaring Phases and their dependencies, flexibly adapting to various inference deployment patterns. This document is intended for open-source users and developers, covering the design rationale, core concepts, and the full workflow from configuration declaration to deployment.

## Design Background

As an OpenAI-compatible distributed inference orchestration frontend, ModelOrch needs to support multiple vLLM deployment patterns:

- **PD Hybrid**: Prefill and Decode run in the same vLLM instance
- **PD Disaggregate**: Prefill and Decode are separated into different instances for independent scaling
- **EPD Disaggregate**: Encode, Prefill, and Decode are fully disaggregated into separate instances
- **Layerwise CDCP**: Prefill actively pushes KV Cache to Decode in a layerwise manner

Additionally, some inference scenarios require **parallel execution** — where scheduling and request sending for Prefill and Decode can happen concurrently rather than strictly serially.

The Infer Pipeline is designed precisely to address these diverse scenarios. By decomposing the inference workflow into reusable Phase units and declaring DAG dependencies through configuration, it enables flexible assembly for different deployment patterns.

## Design Goals

1. **Configuration-driven**: Assemble inference steps declaratively via YAML configuration to generate a DAG execution pipeline, adapting to different deployment patterns without code changes
2. **Serial and parallel execution**: Phases declare dependencies via `dependsOn`; independent Phases execute automatically in parallel
3. **Component reuse**: Abstract scheduling, request construction, request sending, and response handling into standard Phase interfaces, shared across different Pipelines
4. **Extensibility**: Provide Phase interfaces and function-level plugin mechanisms for custom processing logic
5. **Observability**: Support step-level trace logging to track each request's execution progress and results across Pipeline Phases

## Core Concepts

### Phase

A Phase is the smallest execution unit in a Pipeline. All Phases must implement the `Phase` interface:

```go
type Phase interface {
    PhaseName() string                              // Phase name
    PhaseTypeName() string                          // Phase type identifier
    Init(phaseCfg PhaseConfig) error                // Configuration initialization
    ExecutePhase(ctx PipelineContext) *InferError   // Core execution logic
}
```

Two core Phase types are built into the system:

| Phase Type | Responsibility |
|---|---|
| **Schedule** | Selects appropriate target inference instances (e.g., Prefill instances, Decode instances) based on scheduling algorithms and writes results to PipelineContext |
| **Execute** | Reads scheduling results from PipelineContext, constructs and sends inference requests, processes responses, and writes results back to context or returns to the client |

### Pipeline

A Pipeline is a Directed Acyclic Graph (DAG) composed of multiple Phases connected by dependency relationships. During execution, Phases are driven in topological order (serially or in parallel). Each Phase reads upstream-produced data from PipelineContext and produces data needed downstream.

### PipelineContext

PipelineContext is the carrier for data passing between Phases, spanning the entire lifecycle of an inference request:

```
PipelineContext
├── ClientGinCtx          // Original client request context
├── ParsedRequest         // Parsed request parameters
├── PreScheduleResult     // Prefill scheduling result (instance addresses, etc.)
├── DecodeScheduleResult  // Decode scheduling result
├── EncodeScheduleResult  // Encode scheduling result
├── KVTransferParams      // KV Cache transfer parameters
├── SubRequests           // Sub-request management
└── ...
```

### PhaseRegistry

PhaseRegistry manages all available Phase types. At system startup, built-in Phases and user-defined Phases register with the registry via `init()`. When loading configuration, the Pipeline creates corresponding Phase instances from the registry based on the `type` field in the configuration.

```
          pipeline.yaml
               │
               ▼
        PhaseRegistry ──────► SchedulePhase (registered)
               │              ExecutePhase  (registered)
               │              CustomPhase  (plugin-registered)
               │
               ▼
         Pipeline
     (create Phase instances + build DAG)
```

## Usage: From Configuration to Deployment

### Step 1: Declare the Pipeline Configuration

Declare all Phases and their dependencies in `pipeline_cfg.phases`:

```yaml
pipeline_cfg:
  phases:
    - name: prefill_schedule
      type: Schedule
      schedule_params:
        instance_type: prefill
        request_load: [text, bytes]

    - name: prefill_execute
      type: Execute
      dependsOn: [prefill_schedule]
      execute_params:
        instance_type: prefill
        pre_process_request_func: prefill_prepare_request

    - name: decode_schedule
      type: Schedule
      dependsOn: [prefill_execute]
      schedule_params:
        instance_type: decode
        request_load: [usage, text, bytes]

    - name: decode_execute
      type: Execute
      dependsOn: [decode_schedule]
      execute_params:
        instance_type: decode
        pre_process_request_func: decode_prepare_request
        post_process_funcs: [decode_post_response]
```

Key fields for each Phase:

| Field | Description |
|---|---|
| `name` | Phase unique identifier |
| `type` | Phase type (`Schedule` / `Execute` / custom type) |
| `dependsOn` | List of upstream Phases this Phase depends on, determines DAG topology |
| `schedule_params` / `execute_params` | Scheduling/execution parameters, including instance type, pre-processing functions, post-processing functions, etc. |

### Step 2: Associate Pipeline via infer_mode

The `infer_modes` list in the configuration associates deployment pattern names with `pipeline_cfg`:

```yaml
infer_modes:
  - PD-Disaggregate   # Deployment pattern name
```

### Step 3: Specify Pipeline at Deployment Time

When starting ModelOrch, specify the inference mode via command-line option or environment variable:

```bash
# Via command-line option
modelorch --infer-mode PD-Disaggregate

# Or via environment variable
export INFER_MODE=PD-Disaggregate
```

At startup, ModelOrch loads the corresponding Pipeline from the configuration based on `infer_mode`, builds the Phase DAG, and each subsequent inference request will follow this DAG execution flow.

## Orchestration Examples

### Example 1: vLLM PD Disaggregate (Serial)

A typical PD disaggregate inference workflow with Phases executing serially:

```
Preprocess ──► Schedule_Prefill ──► Execute_Prefill ──► Schedule_Decode ──► Execute_Decode ──► PostProcess
```

1. **Preprocess**: Parse request body, read prompt length
2. **Schedule_Prefill**: Schedule Prefill instances, select appropriate instance addresses
3. **Execute_Prefill**: Construct and send Prefill request, extract KV Transfer parameters from response, calibrate prompt length
4. **Schedule_Decode**: Schedule Decode instances (can use calibrated information from Prefill phase for more accurate scheduling)
5. **Execute_Decode**: Construct and send Decode request, write inference results back to the client
6. **PostProcess**: Compute statistics, update metrics, handle errors on failure

### Example 2: Parallel Scheduling and Execution

In some scenarios, scheduling and request sending for Prefill and Decode can happen concurrently:

```
          ┌── Schedule_Prefill ──┐
Preprocess ┤                      ├──► Execute_Prefill ──┐
          └── Schedule_Decode ───┘                       ├──► PostProcess
                                          Execute_Decode ──┘
```

Configuration with dependencies:

```yaml
phases:
  - name: prefill_schedule
    type: Schedule
    schedule_params:
      instance_type: prefill
  - name: decode_schedule
    type: Schedule           # No dependency on prefill_schedule, auto-parallel
    schedule_params:
      instance_type: decode
  - name: prefill_execute
    type: Execute
    dependsOn: [prefill_schedule]
  - name: decode_execute
    type: Execute
    dependsOn: [decode_schedule]
```

### Example 3: EPD Disaggregate (Multi-role + Conditional Skip)

EPD mode involves three roles: Encode, Prefill, and Decode. The Encode phase supports conditional skipping — when a request contains no images, Encode is automatically skipped:

```
Preprocess ──► Encode_Schedule ──► Encode_Execute ──► Prefill_Schedule ──► Prefill_Execute ──► Decode_Schedule ──► Decode_Execute ──► PostProcess
               ↑ conditional skip
               （skip_on_zero_image）
```

Encode skip policy configuration:

```yaml
phases:
  - name: encode_schedule
    type: Schedule
    schedule_params:
      instance_type: encode
      skip_policy: skip_on_zero_image   # Skip when no image present
      request_load: [image, text, bytes]
```

## Extension Mechanisms

### 1. Custom Phase

Implement the `Phase` interface and register it via `init()`, then use it directly in configuration:

```go
type CustomPhase struct {
    *BasePhase
}

func (p *CustomPhase) ExecutePhase(ctx *PipelineContext) *InferError {
    // Custom processing logic
    return nil
}

func init() {
    RegisterPhase(func() Phase {
        return &CustomPhase{}
    })
}
```

Reference it in configuration:

```yaml
phases:
  - name: my_custom
    type: CustomPhase
    dependsOn: [previous_phase]
```

### 2. Processing Function Plugins

Execute Phases support specifying request pre-processing and response post-processing functions via configuration:

- **`pre_process_request_func`**: Custom request transformation logic
- **`post_process_funcs`**: List of custom response post-processing functions, executed serially

### 3. Skip Policies

Schedule Phases support `skip_policy` configuration to conditionally skip a phase based on request characteristics:

| Policy | Description |
|---|---|
| `skip_on_zero_image` | Skip Encode phase when the request contains no images |

## Execution Flow

The complete lifecycle of an inference request within a Pipeline:

### Initialization Phase

```
Load configuration → Validate configuration → Create Phase instances via PhaseRegistry → Build DAG based on dependsOn → Topological sort
```

### Execution Phase

```
Create PipelineContext → Execute Phases with in-degree 0 in parallel → Wait for Phase completion → Trigger downstream Phases → Loop until all Phases complete
```

### Completion Phase

```
Wait for all Phases to complete → Execute PostProcess (success/failure handling) → Clean up resources → Return result
```

## Architecture Overview

```
┌──────────────────────────────────────────────────────────────────┐
│                   Configuration-Driven Pipeline                  │
└──────────────────────────────────────────────────────────────────┘

pipeline.yaml              PhaseRegistry                 Pipeline
┌──────────────┐         ┌──────────────────┐         ┌──────────────────┐
│ phases:      │         │  SchedulePhase   │         │  Phase DAG       │
│  - type:     │────────►│  ExecutePhase    │────────►│                  │
│    Schedule  │         │  CustomPhase     │         │  pre_schedule    │
│  - type:     │         │  ...             │         │    │             │
│    Execute   │         └──────────────────┘         │    ▼             │
│  dependsOn   │                                      │  pre_execute     │
└──────────────┘                                      │    │             │
                                                      │    ▼             │
                                                      │  dec_execute     │
                                                      └──────────────────┘

PipelineContext (cross-Phase data passing)
┌──────────────────────────────────────────────────────────────────┐
│ ClientGinCtx │ ParsedRequest │ ScheduleResults │ KVTransferParams │
└──────────────────────────────────────────────────────────────────┘
```

## Summary

The Infer Pipeline, through its **configuration declaration + DAG-driven + plugin extension** design, provides a flexible and reusable inference workflow assembly framework:

- **For users**: Assemble inference workflows for different deployment patterns via YAML configuration without code changes
- **For developers**: Reuse existing Phases, or extend custom logic through Phase interfaces and function plugins
- **For the community**: Flexible orchestration capabilities that can quickly adapt to new inference architectures and deployment patterns

Related documentation:

- [PD Hybrid](./pd-hybrid.md)
- [PD Disaggregate](./pd-disaggregate.md)
- [EPD Disaggregate](./epd-disaggregate.md)
- [Layerwise CDCP](./layerwise-cdcp.md)
