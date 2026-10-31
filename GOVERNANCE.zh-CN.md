# RouteServer 治理规则

本文档定义 RouteServer 项目中谁能做什么、如何获得这些权限，以及出现分歧时如何决策。它刻意写得很
短：目标是任何贡献者在动手之前，都能预判一个决定会怎么做出。

- English version: [GOVERNANCE.md](GOVERNANCE.md)
- 贡献流程：[CONTRIBUTING.zh-CN.md](CONTRIBUTING.zh-CN.md)
- 分支模型与合入规则：[docs/branching.zh-CN.md](docs/branching.zh-CN.md)
- 安全上报：[SECURITY.zh-CN.md](SECURITY.zh-CN.md)
- 行为准则：[CODE_OF_CONDUCT.zh-CN.md](CODE_OF_CONDUCT.zh-CN.md)

## 1. 角色

| 角色 | 能做什么 | 如何获得 |
| --- | --- | --- |
| **Contributor（贡献者）** | 提 issue、提 PR、参与设计讨论 | 任何人，提交时完成 DCO 签署即可 |
| **Reviewer（评审人）** | 评审并 approve PR，**不能**合入 | 由 maintainer 在持续参与评审后任命 |
| **Committer** | approve 并合入 PR、在仓内推送 topic 分支、处理 issue 与标签 | 提名并表决通过（见 §2） |
| **Maintainer（维护者）** | committer 的全部权限，外加：修改分支保护、管理仓库设置与团队、发版、处理安全报告、任命 reviewer/committer、修订本文档 | 从 committer 中晋升（见 §3） |

角色是累加的，并且是公开的。当前 reviewer、committer、maintainer 名单记录在
[MAINTAINERS.md](MAINTAINERS.md) 与仓库团队设置中；两者不一致时以团队设置为准，文件视为
待修的 bug。

## 2. 成为 committer

committer 既要能写，也要能评。最低门槛：

- **10 个以上非平凡 PR** 合入 `main`（修错别字、升级依赖不算），且跨度**不少于三个月**。
- 有可验证的评审判断力：对他人 PR 留下过**至少 5 次实质性评审意见**，且被作者采纳。
- 没有绕过 CI、自审自合、缺少评审就合入的记录。
- 熟悉其将要评审的子系统，并在 [.github/CODEOWNERS](.github/CODEOWNERS) 中体现领域归属。

流程：

1. 任一 committer 在 maintainer 内部渠道提名，附上上述证据链接。
2. 需要另外 2 名 committer 附议。
3. maintainer 有 **7 天**提出异议。无人异议即通过（lazy consensus）；无法化解的异议会阻止晋升，且必须
   向候选人说明理由。
4. 由 maintainer 将此人加入 committer 团队，同时提交更新 `MAINTAINERS.md` 的 PR 记录生效日期。

来自发起公司之外的持续贡献是明确受欢迎的：committer 席位不预留给任何公司的雇员。

## 3. 成为 maintainer

- 担任 committer **满六个月**，其中至少有**一个发版周期**担任 release manager，或承担同等的持续职责。
- 获得**现有 maintainer 三分之二**同意，投票开放 7 天。
- maintainer 应保持在**三人及以上**，以免任何一个人的缺席就能卡住评审、发版或安全响应。

## 4. 不活跃与荣休

committer 或 maintainer 连续**六个月**没有评审、提交或 triage 活动，转为 *emeritus（荣休）*：姓名保留在
`MAINTAINERS.md` 并注明结束日期，写权限被移除。荣休不是评价——人会换工作、会变优先级。回归也很
简单：向 maintainer 提出即可，转荣休后三个月内无需重新投票即可恢复权限。

## 5. 评审与合入要求

| 改动类型 | 所需 approve | 附加要求 |
| --- | --- | --- |
| 文档、注释、错别字（不改变政策） | 1 | — |
| 仅测试 | 1 | CI 绿 |
| 普通代码（`pkg/`、`cmd/`） | 2 | CI 绿 |
| 调度器、中间件、HTTP/TLS 处理 | 2 | 其中 1 个必须是该领域 owner |
| 构建、部署或 CI 规则/行为（`deploy/`、`Makefile`、`.codearts/`） | 2 | 其中 1 个是 release owner |
| 项目政策或分支保护变更，包括 `GOVERNANCE.md`、`SECURITY.md` 及其翻译 | 2 | 至少 1 个 maintainer approve |
| 安全修复 | 1 名 maintainer + 1 名安全评审人 | 披露前使用私有分支，见 [SECURITY.zh-CN.md](SECURITY.zh-CN.md) |

本表是审批要求的权威依据，人数按不同的非作者评审人计算。按改动的实际影响分类：仅文档、仅测试改动需 1 人审批，
但涉及项目政策、构建、部署或 CI 规则变更时不适用该简化要求。混合改动取适用的更严格人数，
并满足所有适用的 owner 或 maintainer 要求。
必需的领域 owner 与 maintainer 审批均**计入总人数**，不额外增加评审人。治理政策变更还需执行第 10 节的投票；
纯编辑性修正不属于政策变更。

committer 与 maintainer 均可在必需审批满足、评审对话解决且检查通过后合入。
仓库设置、角色管理和发版仍由 maintainer 负责，两种角色均不得绕过门禁。

以下几条没有例外，热修复也适用：

- **作者永远不能 approve 自己的改动**，maintainer 也一样。
- **任何人都不能直接 push 到 `main`**。所有改动，包括 maintainer 自己的，都以 PR 形式进入。
  内网仓库的自动导入也必须通过评审后的 PR（见 [docs/branching.zh-CN.md](docs/branching.zh-CN.md)）。
- **CI 红灯绝不合并**，管理员也不得绕过必需的审批或检查。

## 6. 决策

默认采用 **lazy consensus（无异议即通过）**：issue 或 PR 若无人提出具体技术异议，就继续推进；异议在该
讨论串中解决。

当无法形成共识时：

1. 提议者在 issue 中总结各选项与取舍。
2. maintainer 投票，简单多数通过，投票开放 7 天。
3. 修订本文档、变更许可证、变更分支保护模型，需要 maintainer **三分之二**多数。

决定记录在提出它的 issue 中，**连同理由**——不只是结论。后来的人必须能看出为什么另一个方案被否掉了。

**升级机制。** 如果 PR 或 issue 卡住的原因是评审人不响应，而不是技术分歧，任何 committer 都可以请
maintainer 重新指派评审。评审长期悬空是流程缺陷，不是降低评审标准的理由。

## 7. 发版

- 当前 pre-1.0 阶段，所有版本（含热修复版本）只从 `main` 切出，不使用维护发版分支。
  同时支持多个版本需先明确批准政策变更，同步调整 [docs/branching.zh-CN.md](docs/branching.zh-CN.md) 与
  [SECURITY.zh-CN.md](SECURITY.zh-CN.md)，再创建维护分支。
- **release manager** 协调每次发版，在发版跟踪 issue 中自愿认领该角色；发版 tag 的创建与版本发布仍由 maintainer 负责。
- 只有 maintainer 可以打 tag，且只能是 `vX.Y.Z`（可带 `-rc.N` / `-alpha.N` / `-beta.N` 预发布后缀）——打 tag 规则见
  [docs/branching.zh-CN.md](docs/branching.zh-CN.md)。
- 每个版本都要有 `docs/versions/release-note-<version>.md`，列出用户可见变更、破坏性变更与已知问题。
  发版说明是发版的一部分，不是事后补的。
- 只有标签提交上 CI 为绿，才允许发布。

## 8. 依赖与第三方代码

引入第三方依赖是治理决策，而不只是构建细节：

- 依赖必须仍在活跃维护，且许可证与 Apache-2.0 兼容。
- 引入依赖的 PR 必须说明为什么标准库或现有依赖无法胜任，并由 maintainer 就该点明确 approve。
- 内嵌（vendor）外部代码时保留原始版权头；如其许可证要求，需登记到 `NOTICE`。

## 9. 安全

- 报告按 [SECURITY.zh-CN.md](SECURITY.zh-CN.md) 私下提交，仅 maintainer 及被明确拉入的人可见。
- 安全修复可以在私有分支上开发，待包含修复的版本就绪后以单个提交合入。这是唯一一种合入时公开评审
  链不完整的情况；披露后会把 PR 公开（或发布摘要）。
- maintainer 不得利用权限查阅与自身职责无关的非公开漏洞报告。

## 10. 修订本文档

修订本文档是面向 `main` 的普通 PR，适用 §5 的评审要求，外加在 PR 中记录 maintainer 三分之二投票结果。
编辑性修订（错别字、失效链接）只需 1 个 approve。
