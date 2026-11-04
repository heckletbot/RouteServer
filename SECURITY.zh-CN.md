# 安全策略

English version: [SECURITY.md](SECURITY.md)

## 支持的版本

RouteServer 尚未发布 1.0。只有最新版本与 `main` 会收到安全修复。
所有版本（含安全热修复）只从 `main` 切出，当前不使用维护发版分支。同时支持多个版本需先明确批准，
同步调整本政策、[GOVERNANCE.zh-CN.md](GOVERNANCE.zh-CN.md) 与
[docs/branching.zh-CN.md](docs/branching.zh-CN.md)，再创建维护分支。

| 版本 | 是否支持 |
| --- | --- |
| `main`（未发布） | 是 |
| 最新的 `v0.x` 版本 | 是 |
| 更早的 `v0.x` 版本 | 否——请升级到最新版本 |
| `v0.x.y-alpha.N` / `-rc.N` 预发布 | 仅尽力而为；不建议用于生产 |

## 上报漏洞

**不要为安全问题开公开 issue、discussion 或 PR。**

> 配置状态：尚未核实托管平台的私密报告功能是否可用，[MAINTAINERS.md](MAINTAINERS.md) 中的安全响应人
> 也仍为占位符。公开发布前必须验证并记录可用的私密上报渠道；下列选项仅在条件满足时适用，不表示渠道已经可用。

1. **平台上报（若可用）**——确认报告为私密后，使用仓库的私密漏洞报告功能；不要用公开 issue 替代。
2. **直接联系（完成登记后）**——向 [MAINTAINERS.md](MAINTAINERS.md) 中列出的真实安全响应人发私信。
   占位 handle 不可用于联系。**不要**在公开渠道透露技术细节。

如果你不确定某事是否算漏洞，也请私下报过来。我们宁可多 triage 一次，也不愿漏掉一个真实问题。

请尽量包含：

- 受影响的版本、提交或 tag，以及部署模式（standalone 或 distributed）；
- 问题描述与安全影响（攻击者能获得什么）；
- 复现步骤或 PoC，包含相关配置（`TLS_*` 变量、端口、etcd 部署方式）；
- 已知的缓解措施；
- 你希望如何被致谢，或希望匿名。

## 我们承诺的节奏

| 阶段 | 目标 |
| --- | --- |
| 确认收到报告 | 3 个工作日内 |
| 初步 triage 与定级 | 7 个工作日内 |
| **严重 / 高危** 的修复或书面缓解方案 | 严重 90 天内，高危 180 天内 |
| 公开披露 | 与报告者协商，在含修复的版本发布后 |

每个阶段我们都会同步进展。如果修复时间超出上述目标，我们会明说原因，而不是失联。

## 披露策略

- 我们采用协同披露（coordinated disclosure）。报告者与 maintainer 共同商定公开日期，默认是含修复版本
  发布当天。
- 除非报告者另有要求，我们会在发版说明与公告中致谢。
- 在把修复部署到托管实例的过程中，我们可能先发布细节有限的公告。
- 如果报告已在别处公开，我们仍会尽快修复，但私有渠道此时已失去意义。

## 范围

**在范围内**

- 由本仓库构建的 `route-server` 二进制与 `config_generator` 工具。
- `pkg/` 下的请求处理、调度、健康/故障检测逻辑，以及 etcd 协同路径。
- TLS/mTLS 配置处理，包括 [docs/security.zh-CN.md](docs/security.zh-CN.md) 中描述的私钥口令处理。
- `deploy/` 中默认配置即构成现实攻击面的部署件。

**不在范围内**

- vLLM、etcd、Prometheus 等第三方组件自身的漏洞——请上报给上游。
- 文档已明确警告过的错误配置，例如在不可信网络上把业务端口或 ops 端口绑到 `0.0.0.0`，或在生产关闭 TLS。
- 仅因流量超出部署规格而造成的拒绝服务。
- 需要主机或 etcd 集群已被攻陷才能成立的问题。
- 自动化扫描器报出的、无法证明实际影响的结论。

## 部署前的加固

RouteServer 分离了业务端口（默认 `9800`）与 ops 端口，并在两侧都支持 mTLS。在任何超出可信网络的部署
之前，请阅读 [docs/security.zh-CN.md](docs/security.zh-CN.md)；特别是：绑定到明确网卡、不要把 ops 端口暴露到公网、
对下游 vLLM 与 etcd 连接启用 mTLS。

## 与安全相关的贡献

如果你的 PR 涉及 `pkg/route-server/middleware/`、`pkg/common/http/`、`pkg/route-server/handler/` 或
`deploy/` 中的 TLS 处理，代码改动需 2 名非作者审批，必需的领域 owner 审批包含在这 2 名中。
仅文档、仅测试、政策变更与混合改动按 [GOVERNANCE.zh-CN.md §5](GOVERNANCE.zh-CN.md#5-评审与合入要求)
分类执行；安全修复还需满足其中的 maintainer 与安全评审人要求。
committer 或 maintainer 仅可在必需审批与检查满足后合入，热修复也不得绕过门禁。
