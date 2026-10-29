# 发行说明 - RouteServer v0.1.0

[English](release-note-v0.1.0.md) | [简体中文](release-note-v0.1.0.zh-CN.md)

## 关于

RouteServer 是面向大语言模型（LLM）服务场景的分布式 OpenAI 推理编排前端。它支持多种 vLLM 部署模式下的推理流程。RouteServer 以提供分布式高可用的推理请求执行为目标，通过集中式请求调度、推理引擎故障感知与隔离、集群状态协调，构建高效、可扩展、可靠的推理负载均衡服务。

## 特性

### 部署模式

- **单机部署**：简单的单机部署，适用于推理实例地址事先固定的场景。（[部署指南](../deploy.zh-CN.md)）
- **分布式部署**：支持分布式请求处理与水平扩展，避免 RouteServer 成为推理性能瓶颈。通过 leader 选举提供集中统一的推理请求调度，避免分布式路由器各自独立调度带来的调度冲突性能问题。（[部署指南](../deploy.zh-CN.md)）

### 支持的 vLLM 部署模式

- **PD 混合**：Prefill 与 Decode 阶段运行在同一个 vLLM 实例中。（[PD 混合编排](../orchestrations/pd-hybrid.zh-CN.md)）
- **PD 分离（Disaggregated）**：Prefill 与 Decode 阶段分离到不同的 vLLM 实例，可独立扩缩容。（[PD 分离编排](../orchestrations/pd-disaggregate.zh-CN.md)）
- **EPD 分离（Disaggregated）**：Encode、Prefill、Decode 阶段完全分离到不同的 vLLM 实例。（[EPD 分离编排](../orchestrations/epd-disaggregate.zh-CN.md)）
- **Layerwise CDCP**：以 PD 分离方式部署 vLLM，Prefill 在预填充过程中以逐层方式主动把 prompt 的 KV cache 推送给 Decode。（[Layerwise CDCP 编排](../orchestrations/layerwise-cdcp.zh-CN.md)）

### 负载均衡与调度

- 支持多种专为 LLM 推理场景设计的负载均衡与调度算法。（[负载均衡算法](../load-balancing.zh-CN.md)）

### 故障检测与恢复

- 多粒度的故障检测、隔离与自动恢复：
  - 请求错误类型识别
  - 挂起检测机制
  - 主动探针请求
  - PD 链路级黑名单机制（PD 混合场景不适用）
- 单机与分布式系统均可实时隔离故障实例。（[推理实例健康管理](../infer-instance-health.zh-CN.md)）

### 可观测性

提供丰富的可观测接口，用于获取集群内的各类运行时状态：

- **集群健康与就绪**：`/health`（推理实例健康检查，校验所有必需角色的实例是否可用，返回 200/500）、`/status`（集群整体状态：running/concerning/deploying/fault）、`/worker/readiness`（worker 就绪状态，检查实例数量是否满足最低要求）
- **集群管理与拓扑**：`/cluster/*`（运维端口，包含节点/实例的加入与退出、leader 信息、本地节点状态、一致性状态等）
- **负载与调度**：`/scheduler/load`（按 SLO 分组的实例负载详情，支持按角色过滤以及包含不可用实例）
- **故障隔离状态**：`/link_black_instance`（PD 链路黑名单快照，展示被拉黑的对端实例与剩余拉黑时长）
- **运行中请求详情**：`/cluster/running_details`（集群内全部运行中请求及其各阶段执行状态）
- **性能指标**：`/internal/metrics`（Prometheus 格式指标，支持用 `only_rs=true` 过滤出 RouteServer 自定义指标）

详情参见（[可观测性文档](../observability.zh-CN.md)）与（[RESTful API 参考](../restful-api.zh-CN.md)）。

## 已知问题

- 无（初始版本）

## 说明

- 这是 RouteServer 的首个开源版本。
- 生产环境强烈建议启用 HTTPS 与 mTLS。详情参见安全指南。
- 完整 API 文档请参阅 RESTful API 参考与配置参考。

## 参考资料

- [架构文档](../architecture.zh-CN.md)
- [代码结构](../code-structure.zh-CN.md)
- [负载均衡算法](../load-balancing.zh-CN.md)
- [安全指南](../security.zh-CN.md)
- [部署指南](../deploy.zh-CN.md)
- [可观测性文档](../observability.zh-CN.md)
- [RESTful API 参考](../restful-api.zh-CN.md)
- [配置规范](../config-spec.zh-CN.md)

## 许可

RouteServer 采用 Apache License 2.0 许可协议。
