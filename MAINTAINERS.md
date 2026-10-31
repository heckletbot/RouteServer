# 维护者名单 / Maintainers

本文件记录 RouteServer 项目中各角色的持有人。角色如何获得、如何因不活跃而退出，见
[GOVERNANCE.zh-CN.md](GOVERNANCE.zh-CN.md)。若本文件与仓库团队设置不一致，以团队设置为准——请提 PR 修正
本文件。

This file records who holds which role. The rules for gaining a role, and for losing it through
inactivity, are in [GOVERNANCE.md](GOVERNANCE.md).

> 登记状态：以下角色表仍为占位内容，尚未提供可联系的维护者或安全响应人。公开发布前需登记真实账号，
> 核实私密联系渠道，并确认 CODEOWNERS 中团队存在且具备所需权限；本文件不表示平台设置已验证。
>
> Registration status: the role tables below still contain placeholders and do not provide reachable
> maintainers or security responders. Before public release, register real accounts, verify a private
> contact channel, and confirm that CODEOWNERS teams exist with the required permissions. This file
> does not establish that the platform settings have been verified.

<!--
把下面的占位 handle 换为真实的 GitCode 账号；表格按角色分组、组内按字母序。
角色变更与更新本文件的 PR 必须在同一次提交中完成——文件没改，就等于角色没变。
Replace the placeholder handles below with real GitCode accounts. Update this file in the same pull
request that changes someone's role: a role change is not complete until this file says so.
-->

## Maintainers（维护者）

必需审批与检查满足后可合入；负责仓库设置、角色管理、发版及安全报告处理。
May merge after required approvals and checks pass; responsible for repository settings, role management,
releases and security reports.

| 姓名 Name | GitCode | 领域 Areas | 生效日期 Since |
| --- | --- | --- | --- |
| _TBD_ | `@<handle>` | all | _YYYY-MM_ |

期望保持三人以上，见 [GOVERNANCE.zh-CN.md §3](GOVERNANCE.zh-CN.md#3-成为-maintainer)。

## Committers

可 approve；必需审批与检查满足后可合入 PR，可在仓内推送 topic 分支；不得绕过门禁。
May approve, merge after required approvals and checks pass, and push topic branches; must not bypass gates.

| 姓名 Name | GitCode | 领域 Areas | 生效日期 Since |
| --- | --- | --- | --- |
| _TBD_ | `@<handle>` | all | _YYYY-MM_ |

## Reviewers（评审人）

可评审并 approve PR，但不能合入。

| 姓名 Name | GitCode | 领域 Areas | 生效日期 Since |
| --- | --- | --- | --- |
| _TBD_ | `@<handle>` | _例如 scheduler_ | _YYYY-MM_ |

## Security responders（安全响应人）

maintainer 以及此处明确列出的人会收到非公开漏洞报告。见 [SECURITY.zh-CN.md](SECURITY.zh-CN.md)。

| 姓名 Name | GitCode |
| --- | --- |
| _TBD_ | `@<handle>` |

## Emeritus（荣休）

已退出的前 committer / maintainer。见
[GOVERNANCE.zh-CN.md §4](GOVERNANCE.zh-CN.md#4-不活跃与荣休)。

| 姓名 Name | 原角色 Former role | 任期 Period |
| --- | --- | --- |
| _TBD_ | — | — |

## 领域归属 / Area ownership

从目录到必审人的映射在 [.github/CODEOWNERS](.github/CODEOWNERS)。两份文件要保持一致：某人接手某个领域
时，两边都要改。
