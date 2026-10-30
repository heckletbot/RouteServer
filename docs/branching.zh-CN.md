# 分支与发版模型

[English](branching.md) | [简体中文](branching.zh-CN.md)

本文档是公开仓 `InferTurbo/RouteServer` 中代码如何流动的权威说明：有哪些分支、改动如何合入、
版本如何切出。它描述的是稳态规则；内网导出流水线与一次性迁移步骤属于运维手册，不属于本文档。

相关：[CONTRIBUTING.zh-CN.md](../CONTRIBUTING.zh-CN.md) · [GOVERNANCE.zh-CN.md](../GOVERNANCE.zh-CN.md)

## 1. 模型

RouteServer 采用**单干线、受保护的 trunk-based 开发**。`main` 是唯一长期存在的分支，也是唯一发版源。
topic 分支短生命周期，合入即删除。

```
 topic 分支（fork 或仓内） ──Pull Request──▶ main（受保护干线，唯一发版源）
                                             │
                                           tag vX.Y.Z
```

## 2. 分支

| 分支 | 谁写 | 能否合入 `main` | 生命周期 | 说明 |
| --- | --- | --- | --- | --- |
| `main` | maintainer，且必须通过 PR | — | 永久 | 受保护。唯一发版源。 |
| `intranet-sync` | 只有发布流水线 | 能，通过可评审的导入 PR | 永久 | **只读。** 机器写入并被 force-push，见 §5。 |
| `feat/*`、`fix/*`、`docs/*`、`refactor/*`、`chore/*` | 单个贡献者 | 能，squash 合并 | < 1 周 | 合入即删除。可位于 fork 中。 |
| `release/vX.Y` | release manager | 不能——它只接收 cherry-pick | 到该 minor 版本 EOL | 仅在需要同时维护两个 minor 版本时创建。 |
| tag `vX.Y.Z` | maintainer | — | 永久 | 附注 tag。只在 `main` 上创建。 |

## 3. 合并规则

| 改动 | 方式 | 原因 |
| --- | --- | --- |
| 贡献者 PR | **squash merge** | 一个逻辑变更一个提交，保持 `main` 线性。 |
| 导入 PR（`intranet-sync` → `main`） | **merge commit**（`--no-ff`） | 保住导入历史，并标出内网历史的边界。 |
| 热修复 | squash 合并，再 `cherry-pick -x` 到 `release/vX.Y` | 保证两处修复完全一致。 |
| 发版分支 → `main` | 永不 | 发版是反方向的：`main` → tag。 |

硬规则：

- **禁止直接 push `main`**，maintainer 也不例外；所有改动都通过 PR。
- **`main` 上永久禁止 force-push。** 只有发布流水线会 force-push `intranet-sync`。
- **绝不对 `intranet-sync` 提 PR。** 它是镜像分支，合入的内容会被下一次导入覆盖；这类 PR 会被关闭，
  并指向 `main`。
- **topic 分支内部不要出现 merge commit。** 提交评审前先 rebase 到 `main`。

## 4. 评审与 CI 门禁

在 GitCode 保护分支设置中配置（[文档](https://docs.atomgit.com/en/docs/help/home/org_project/project_manage/branch_management/set_protected_branch/)）：

- 禁止直接 push——包括管理员。
- 必须通过 PR，**至少 2 个 approve**，且作者本人不计入。
- 所有评审对话 resolved 后才允许合并。
- 流水线必须通过。
- 合并权限限制为 `maintainer` 角色。
- 关闭 force-push 与分支删除。

必需的检查项，按贡献者遇到的顺序：

| 检查 | 对应命令 |
| --- | --- |
| `build` | `make build` |
| `lint` | `make lint`（gofmt + go vet） |
| `unit-test` | `make test-unit` |
| `e2e` | `go test ./e2etest/route-server/ -timeout 60m`（runner 较慢时可改为每日跑） |
| `dco` | 每个提交都带 `Signed-off-by` |

领域相关的评审要求由 [.github/CODEOWNERS](../.github/CODEOWNERS) 给出，通用要求见
[GOVERNANCE.zh-CN.md §5](../GOVERNANCE.zh-CN.md#5-评审与合入要求)。

## 5. 导入分支与回灌

`intranet-sync` 由发布流水线写入，不由人写入。流水线会 force-push 内网仓库的一份快照，因此该分支是
只读的：本地跟踪它的分支每次导入都会分叉，推上去的提交会被下一次导入丢弃。

导入以可评审的 PR 形式合入 `main`，使用 merge commit（`--no-ff`），绝不 squash。打开这类 PR 之前，
**内网快照必须已经包含 `main` 上的每一个提交**；否则这次导入会静默回退外部贡献。

这就是回灌规则：`main` 上的修复要在下一次导入之前合回内网仓库。热修复同样适用——没有回灌的外部修复，
最多存活到下一次导入。

## 6. 发版

```bash
# 从最新的 main 出发
git switch main && git pull --ff-only
git tag -a v0.2.0 -m "RouteServer v0.2.0"
git push origin v0.2.0
```

- tag 格式为 `vMAJOR.MINOR.PATCH`，可带 `-alpha.N`、`-beta.N`、`-rc.N`（例如仓里已有的 `v0.1.0-alpha.1`）。
- **被打 tag 的提交上 `main` 必须是绿的**；红灯提交上的 tag 会被删除。
- 发版说明 `docs/versions/release-note-<version>.md` 必须在打 tag **之前**合入。
- 1.0 之前，`MINOR` 版本可以包含破坏性变更；1.0 之后破坏性变更必须升 `MAJOR`。
- 只有 maintainer 可以推送 tag。

## 7. 发版分支与热修复

仅当旧 minor 版本需要在 `main` 继续前进时仍接收修复，才创建 `release/vX.Y`：

```bash
git switch -c release/v0.1 v0.1.3
# 修复：从 main cherry-pick -x <sha>，绝不把 main 合进来
```

热修复：

1. 从 `main` 拉 `fix/<slug>`（不是从发版分支拉）。
2. 正常评审、正常合入。
3. 从 `main` 打补丁版本 tag。
4. 若受影响的 `release/vX.Y` 仍在维护，则 `git cherry-pick -x <merge-sha>` 后在那里也打 tag。

紧急通道：事故处理期间，单个 maintainer 可在只有 1 个 approve 时合入，前提是 PR 打上 `hotfix` 标签，
并在 24 小时内补齐第二次评审。这个标签的作用就是让这类情况可审计。
