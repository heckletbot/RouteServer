# 关于 Infer 流水线

[English](about-infer-pipeline.md) | [简体中文](about-infer-pipeline.zh-CN.md)

Infer 流水线是 RouteServer 的核心能力。它提供了一套配置驱动的方式来组装推理工作流：通过声明 Phase 及其依赖关系生成基于 DAG 的执行流水线，从而灵活适配各种推理部署模式。本文面向开源用户与开发者，涵盖设计动因、核心概念，以及从配置声明到部署的完整流程。

## 设计背景

作为 OpenAI 兼容的分布式推理编排前端，RouteServer 需要支持多种 vLLM 部署模式：

- **PD Hybrid**：Prefill 与 Decode 运行在同一个 vLLM 实例中
- **PD Disaggregate**：Prefill 与 Decode 分离到不同实例，便于独立扩缩容
- **EPD Disaggregate**：Encode、Prefill 与 Decode 完全分离为独立实例
- **Layerwise CDCP**：Prefill 以分层方式主动向 Decode 推送 KV Cache

此外，部分推理场景需要**并行执行**——Prefill 与 Decode 的调度和请求发送可以并发进行，而不必严格串行。

Infer 流水线正是为应对这些多样场景而设计。它把推理工作流拆解为可复用的 Phase 单元，并通过配置声明 DAG 依赖关系，从而为不同部署模式提供灵活的组装能力。

## 设计目标

1. **配置驱动**：通过 YAML 配置以声明方式组装推理步骤，生成 DAG 执行流水线，无需改动代码即可适配不同部署模式
2. **串行与并行执行**：Phase 通过 `dependsOn` 声明依赖；彼此独立的 Phase 自动并行执行
3. **组件复用**：将调度、请求构造、请求发送与响应处理抽象为标准 Phase 接口，供不同流水线共用
4. **可扩展性**：提供 Phase 接口与函数级插件机制，用于承载自定义处理逻辑
5. **可观测性**：支持步骤级 trace 日志，跟踪每个请求在各流水线 Phase 中的执行进展与结果

## 核心概念

### Phase

Phase 是流水线中最小的执行单元。所有 Phase 都必须实现 `Phase` 接口：

```go
type Phase interface {
    PhaseName() string                              // Phase name
    PhaseTypeName() string                          // Phase type identifier
    Init(phaseCfg PhaseConfig) error                // Configuration initialization
    ExecutePhase(ctx PipelineContext) *InferError   // Core execution logic
}
```

系统内置两种核心 Phase 类型：

| Phase 类型 | 职责 |
|---|---|
| **Schedule** | 依据调度算法选择合适的推理目标实例（如 Prefill 实例、Decode 实例），并将结果写入 PipelineContext |
| **Execute** | 从 PipelineContext 读取调度结果，构造并发送推理请求，处理响应，并将结果写回上下文或返回给客户端 |

### Pipeline

流水线是由多个 Phase 通过依赖关系连接而成的有向无环图（DAG）。执行时，各 Phase 按拓扑顺序被驱动（串行或并行）。每个 Phase 从 PipelineContext 读取上游产出的数据，并产出下游所需的数据。

### PipelineContext

PipelineContext 是 Phase 之间传递数据的载体，贯穿一次推理请求的整个生命周期：

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

PhaseRegistry 管理所有可用的 Phase 类型。系统启动时，内置 Phase 与用户自定义 Phase 通过 `init()` 注册到注册表。加载配置时，流水线依据配置中的 `type` 字段从注册表创建对应的 Phase 实例。

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

## 使用：从配置到部署

### 步骤 1：声明流水线配置

在 `pipeline_cfg.phases` 中声明所有 Phase 及其依赖关系：

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

每个 Phase 的关键字段：

| 字段 | 说明 |
|---|---|
| `name` | Phase 唯一标识 |
| `type` | Phase 类型（`Schedule` / `Execute` / 自定义类型） |
| `dependsOn` | 本 Phase 所依赖的上游 Phase 列表，决定 DAG 拓扑 |
| `schedule_params` / `execute_params` | 调度/执行参数，包含实例类型、预处理函数、后处理函数等 |

### 步骤 2：通过 infer_mode 关联流水线

配置中的 `infer_modes` 列表把部署模式名称与 `pipeline_cfg` 关联起来：

```yaml
infer_modes:
  - PD-Disaggregate   # Deployment pattern name
```

### 步骤 3：部署时指定流水线

启动 RouteServer 时，通过命令行选项或环境变量指定推理模式：

```bash
# Via command-line option
routeserver --infer-mode PD-Disaggregate

# Or via environment variable
export INFER_MODE=PD-Disaggregate
```

启动时，RouteServer 依据 `infer_mode` 从配置中加载对应的流水线，构建 Phase DAG；此后每个推理请求都按该 DAG 的执行流程处理。

## 编排示例

### 示例 1：vLLM PD Disaggregate（串行）

典型的 PD 分离推理工作流，各 Phase 串行执行：

```
Preprocess ──► Schedule_Prefill ──► Execute_Prefill ──► Schedule_Decode ──► Execute_Decode ──► PostProcess
```

1. **Preprocess**：解析请求体，读取 prompt 长度
2. **Schedule_Prefill**：调度 Prefill 实例，选出合适的实例地址
3. **Execute_Prefill**：构造并发送 Prefill 请求，从响应中提取 KV Transfer 参数，校准 prompt 长度
4. **Schedule_Decode**：调度 Decode 实例（可利用 Prefill 阶段校准后的信息，使调度更准确）
5. **Execute_Decode**：构造并发送 Decode 请求，将推理结果写回客户端
6. **PostProcess**：统计信息计算、更新指标、失败时处理错误

### 示例 2：并行调度与执行

在某些场景中，Prefill 与 Decode 的调度和请求发送可以并发进行：

```
          ┌── Schedule_Prefill ──┐
Preprocess ┤                      ├──► Execute_Prefill ──┐
          └── Schedule_Decode ───┘                       ├──► PostProcess
                                          Execute_Decode ──┘
```

带依赖关系的配置如下：

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

### 示例 3：EPD Disaggregate（多角色 + 条件跳过）

EPD 模式涉及 Encode、Prefill 与 Decode 三种角色。Encode 阶段支持条件跳过——当请求中不含图片时，Encode 会被自动跳过：

```
Preprocess ──► Encode_Schedule ──► Encode_Execute ──► Prefill_Schedule ──► Prefill_Execute ──► Decode_Schedule ──► Decode_Execute ──► PostProcess
               ↑ conditional skip
               （skip_on_zero_image）
```

Encode 跳过策略的配置如下：

```yaml
phases:
  - name: encode_schedule
    type: Schedule
    schedule_params:
      instance_type: encode
      skip_policy: skip_on_zero_image   # Skip when no image present
      request_load: [image, text, bytes]
```

## 扩展机制

### 1. 自定义 Phase

实现 `Phase` 接口并通过 `init()` 注册，即可在配置中直接使用：

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

在配置中引用它：

```yaml
phases:
  - name: my_custom
    type: CustomPhase
    dependsOn: [previous_phase]
```

### 2. 处理函数插件

Execute 类型的 Phase 支持通过配置指定请求预处理函数与响应后处理函数：

- **`pre_process_request_func`**：自定义请求改写逻辑
- **`post_process_funcs`**：自定义响应后处理函数列表，按顺序串行执行

### 3. 跳过策略

Schedule 类型的 Phase 支持 `skip_policy` 配置，依据请求特征有条件地跳过某个阶段：

| 策略 | 说明 |
|---|---|
| `skip_on_zero_image` | 请求中不含图片时跳过 Encode 阶段 |

## 执行流程

一次推理请求在流水线中的完整生命周期：

### 初始化阶段

```
Load configuration → Validate configuration → Create Phase instances via PhaseRegistry → Build DAG based on dependsOn → Topological sort
```

### 执行阶段

```
Create PipelineContext → Execute Phases with in-degree 0 in parallel → Wait for Phase completion → Trigger downstream Phases → Loop until all Phases complete
```

### 完成阶段

```
Wait for all Phases to complete → Execute PostProcess (success/failure handling) → Clean up resources → Return result
```

## 架构总览

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

## 小结

Infer 流水线通过**配置声明 + DAG 驱动 + 插件扩展**的设计，提供了一套灵活且可复用的推理工作流组装框架：

- **对用户**：通过 YAML 配置组装不同部署模式的推理工作流，无需改动代码
- **对开发者**：复用已有 Phase，或通过 Phase 接口与函数插件扩展自定义逻辑
- **对社区**：灵活编排能力，可快速适配新的推理架构与部署模式

相关文档：

- [PD Hybrid](./pd-hybrid.zh-CN.md)
- [PD Disaggregate](./pd-disaggregate.zh-CN.md)
- [EPD Disaggregate](./epd-disaggregate.zh-CN.md)
- [Layerwise CDCP](./layerwise-cdcp.zh-CN.md)
