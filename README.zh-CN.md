# RouteServer

[English](README.md) | [简体中文](README.zh-CN.md)

# 关于

RouteServer 是一个面向大语言模型（LLM）服务场景的分布式 OpenAI 推理编排前端。它支持多种 vLLM 部署模式下的推理工作流。它旨在提供分布式高可用推理请求执行工作流，通过集中式请求调度、推理引擎故障感知与隔离、集群状态协调，实现高效、可扩展且可靠的推理负载均衡服务。

- 支持两种部署模式
  - 单机部署：简单的单机部署方式，适用于推理实例地址事先固定的场景
  - 分布式部署：支持分布式请求处理与水平扩展，避免 RouteServer 成为推理性能瓶颈。通过 leader 选举提供集中统一的推理请求调度，避免分布式 router 各自调度引发的调度冲突性能问题
- 支持多种 vLLM 部署模式：
  - [PD 混合](docs/orchestrations/pd-hybrid.zh-CN.md)：Prefill 与 Decode 阶段运行在同一个 vLLM 实例中。
  - [PD 分离](docs/orchestrations/pd-disaggregate.zh-CN.md)：Prefill 与 Decode 阶段分离到不同的 vLLM 实例，可独立扩缩容。
  - [EPD 分离](docs/orchestrations/epd-disaggregate.zh-CN.md)：Encode、Prefill 与 Decode 阶段完全分离到不同的 vLLM 实例。
  - [Layerwise CDCP](docs/orchestrations/layerwise-cdcp.zh-CN.md)：采用 vLLM PD 分离部署，其中 Prefill 在预填充过程中会以 layerwise 方式主动将 prompt kv cache 推送到 Decode。参见 [vllm-ascend issue](https://github.com/vllm-project/vllm-ascend/issues/4842)
- 支持多种面向 LLM 推理的负载均衡与调度算法。参见 [load-balancing](docs/load-balancing.zh-CN.md)
- 多粒度故障检测、隔离与自动恢复：在单机与分布式系统中，均通过请求错误类型识别、实例输出 token 性能、主动探针请求等机制收集请求处理信息并感知推理实例故障，实时进行隔离。
- 提供可观测接口，用于获取集群内的各类运行时状态。

更多细节参见我们的 [架构文档](docs/architecture.zh-CN.md) 与 [代码库结构](docs/code-structure.zh-CN.md)

# 快速开始

## 快速上手

### 前置条件

- Go 1.25.0 或更高版本
- Make 3.82 或更高版本

### 构建与打包

```bash
# Clean previous builds and create the deploy package
make clean && make package
```

这将在 `output/DistributedScheduler-<version>-<timestamp>.zip` 生成一个 zip 包。

详细的构建说明与可用的 Makefile 目标，参见 [构建指南](docs/build.zh-CN.md)。

### 以 PD 分离示例部署

1. 进入包目录（`make package` 创建的 `output/` 目录）：
```bash
cd output
```

2. 为推理框架实例设置环境变量（兼容 OpenAI 格式）：
```bash
export PREFILL_SERVERS="<YOUR_PREFILL_IP>:<PORT>"
export DECODE_SERVERS="<YOUR_DECODE_IP>:<PORT>"
```

3. 在本地部署单个 RouteServer：
```bash
bash deploy.sh \
    --port 9800 \
    --mode singleton \
    --host <YOUR_IP>  \
    --enable-https false \
    --ops-enable-https false \
    --client-https-enable false \
    --etcd-tls-enable false
```

4. 检查集群状态：
```bash
curl -X GET http://<YOUR_IP>:9800/status
```

更多部署选项与配置，参见我们的 [部署脚本参考](docs/deploy.zh-CN.md)

## 安全

我们强烈建议在生产环境启用 https 与 mTLS，参见我们的 [安全指南](docs/security.zh-CN.md)

## API 参考

使用者请访问我们的 [RESTful API 参考](docs/restful-api.zh-CN.md)

开发者请访问我们的 [配置参考](docs/config-spec.zh-CN.md)

## 贡献

我们欢迎贡献！完整指南见 [CONTRIBUTING.md](CONTRIBUTING.md) ——
中文版见 [CONTRIBUTING.zh-CN.md](CONTRIBUTING.zh-CN.md)（含分支模型、提交规范与评审流程）。

简版如下：

1. **Fork** 本仓库
2. 从 `main` **创建主题分支**（`git checkout -b feat/my-feature`）
3. **完成你的修改**并确保代码质量：
   ```bash
   make lint   # go fmt + go vet
   make test   # go test ./...
   ```
4. 使用 Conventional Commits 规范的信息**提交**并签署（`git commit -s`）
5. 针对 `main` **提交 Pull Request**

对于较大的改动，请先开 issue 讨论你希望修改的内容。请勿针对 `intranet-sync` 提交 pull
request —— 该分支由机器写入，其中的改动会被覆盖。

- 分支模型：[docs/branching.md](docs/branching.md) · [中文](docs/branching.zh-CN.md)
- 角色与决策：[GOVERNANCE.md](GOVERNANCE.md) · [中文](GOVERNANCE.zh-CN.md)
- 安全报告：[SECURITY.md](SECURITY.md) · [中文](SECURITY.zh-CN.md)

## 许可证

RouteServer 基于 Apache License 2.0 许可发布
