# 安全策略

English version: [SECURITY.md](SECURITY.md)

## 支持的版本

RouteServer 尚未发布 1.0。只有最新版本与 `main` 会收到安全修复。

| 版本 | 是否支持 |
| --- | --- |
| `main`（未发布） | 是 |
| 最新的 `v0.x` 版本 | 是 |
| 更早的 `v0.x` 版本 | 否——请升级到最新版本 |
| `v0.x.y-alpha.N` / `-rc.N` 预发布 | 仅尽力而为；不建议用于生产 |

## 上报漏洞

**不要为安全问题开公开 issue、discussion 或 PR。**

请使用以下私有渠道之一：

1. **首选**——GitCode 的私密安全报告：在仓库页面选择 *Security* → *Report a vulnerability*，会创建仅
   maintainer 可见的私密公告。
2. **直接联系**——若该流程不可用，向 [MAINTAINERS.md](MAINTAINERS.md) 中列出的安全响应人
   发私信。**不要**在公开渠道透露技术细节。

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
- TLS/mTLS 配置处理，包括 [docs/security.md](security.md) 中描述的私钥口令处理。
- `deploy/` 中默认配置即构成现实攻击面的部署件。

**不在范围内**

- vLLM、etcd、Prometheus 等第三方组件自身的漏洞——请上报给上游。
- 文档已明确警告过的错误配置，例如在不可信网络上把业务端口或 ops 端口绑到 `0.0.0.0`，或在生产关闭 TLS。
- 仅因流量超出部署规格而造成的拒绝服务。
- 需要主机或 etcd 集群已被攻陷才能成立的问题。
- 自动化扫描器报出的、无法证明实际影响的结论。

## 部署前的加固

RouteServer 分离了业务端口（默认 `9800`）与 ops 端口，并在两侧都支持 mTLS。在任何超出可信网络的部署
之前，请阅读 [docs/security.md](security.md)；特别是：绑定到明确网卡、不要把 ops 端口暴露到公网、
对下游 vLLM 与 etcd 连接启用 mTLS。

## 与安全相关的贡献

如果你的 PR 涉及 `pkg/route-server/middleware/`、`pkg/common/http/`、`pkg/route-server/handler/` 或
`deploy/` 中的 TLS 处理，除常规的 2 个评审外，还需要该领域 owner 的 approve。见
[GOVERNANCE.zh-CN.md §5](GOVERNANCE.zh-CN.md#5-评审与合入要求)。
