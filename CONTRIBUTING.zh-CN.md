# 为 RouteServer 贡献代码

感谢你对 RouteServer 的关注。本文档说明如何提出改动、如何通过评审与合入，以及我们对一次提交的
最低要求。它对所有人一视同仁——外部贡献者、committer 和 maintainer。

- English version: [CONTRIBUTING.md](CONTRIBUTING.md)
- 分支模型与合入规则：[docs/branching.md](docs/branching.md)
- 角色、晋升与决策方式：[GOVERNANCE.md](GOVERNANCE.md)
- 上报安全漏洞：[SECURITY.md](SECURITY.md)（**不要**通过公开 issue）

## 目录

1. [动手之前](#1-动手之前)
2. [开发环境](#2-开发环境)
3. [构建与测试](#3-构建与测试)
4. [分支与 Pull Request](#4-分支与-pull-request)
5. [提交信息规范](#5-提交信息规范)
6. [DCO 签署](#6-dco-签署)
7. [编码规范](#7-编码规范)
8. [文档要求](#8-文档要求)
9. [我们不接受的改动](#9-我们不接受的改动)
10. [评审流程](#10-评审流程)
11. [许可证](#11-许可证)

## 1. 动手之前

- 先读 [docs/architecture.md](docs/architecture.md) 和 [docs/code-structure.md](docs/code-structure.md)。
  绝大多数评审意见，都源于不清楚某个包该负责什么。
- **修 bug、小改进**（几百行以内）：直接提 PR。
- **更大的改动**——新增调度策略、新增编排模式、修改 API/协议、引入新依赖、新增顶层目录：先开 issue
  说明问题与设计思路，等 maintainer 认可方向后再写代码。这样可以避免你写了一大片却无法合入。
- 提 issue/PR 前先搜索是否已有同类。
- **安全漏洞不要开到公开 issue**，见 [SECURITY.md](SECURITY.md)。

## 2. 开发环境

| 工具 | 最低版本 | 说明 |
| --- | --- | --- |
| Go | 1.25.0 | 模块声明为 `go 1.25.0` |
| Make | 3.82 | 驱动构建、测试与打包 |
| Bash | 3.2 | Make 测试目标使用 `/bin/bash` 的 `pipefail` |
| Git | 2.23 | 需要 `--force-with-lease` 与 worktree |

Go 模块路径是 `infer-dp-scheduler`（不是 URL），因此内部 import 形如
`infer-dp-scheduler/pkg/common/scheduler`。

```bash
# 先在 GitCode 上 fork InferTurbo/RouteServer
git clone git@gitcode.com:<your-user>/RouteServer.git
cd RouteServer
git remote add upstream git@gitcode.com:InferTurbo/RouteServer.git
git fetch upstream
git switch -c feat/my-change upstream/main
```

## 3. 构建与测试

```bash
make build         # 产物见下表
make test          # go test ./... -v，日志写入 build/test.log
make test-unit     # go test ./... -short；当前仍包含 E2E 套件
make lint          # go fmt + go vet
make package       # 构建 + 部署件 + version.info + 打 zip
```

构建产物（以 `Makefile` 为准）：

| 目标 | 产物 |
| --- | --- |
| `make build` | `output/route-server/bin/route-server`、`output/config_generator` |
| `make package` | `output/route-server-<version>-<timestamp>.zip` |

> 测试结果：`make test`、`make test-unit` 和 `make test-coverage` 使用 Bash `pipefail`，
> `go test` 或 `tee` 任一失败都会使目标失败，同时保留终端输出和日志文件。
> `-short` 只会跳过显式检查该标志的测试；当前 E2E 套件没有检查，因此 `make test-unit` 并非只跑单测的快捷方式。

验证 Make 测试管道而不运行项目 E2E 套件，可执行 `python3 .codearts/test_makefile.py`（需要 Python 3）。
它检查受控的命令/日志失败，并在 Go 可用时使用禁止下载的独立模块运行真实的通过和失败测试。
它还使用构建替身检查 Make/CodeArts 归档布局；该布局检查不会编译 RouteServer 二进制或执行部署。

### 端到端测试

E2E 套件会自行编译 route-server、mock vLLM 与 config-tool，并以真实进程运行，占用 `30xxx` 段端口，
etcd 是内嵌启动的，无需外部依赖。

```bash
go test ./e2etest/route-server/ -v -timeout 60m
```

部分用例会操作 `iptables`，默认跳过；需要 root 权限时：

```bash
ROUTER_TEST_ENABLE_SU=true sudo -E go test ./e2etest/route-server/ -v -timeout 60m
```

在 Windows 上，若干进程 kill 用例会被跳过。若你的改动涉及数据面，请在 PR 中说明实际跑了哪些套件。

### 推送前

```bash
make lint && go test ./... -v -timeout 60m
```

两项必须干净。CI 红灯的 PR 不会被评审。

## 4. 分支与 Pull Request

本仓库采用主干开发（trunk-based），只有一条受保护的干线 `main`。细节与必需检查见
[docs/branching.md](docs/branching.md)。

1. 从最新的 `upstream/main` 拉分支，不要从旧 tag 或过期 fork 拉。
2. 使用短生命周期分支：`feat/<slug>`、`fix/<slug>`、`docs/<slug>`、`refactor/<slug>`、`chore/<slug>`，
   存活时间控制在一周内。
3. 一个 PR 只做一件事；顺手发现的无关 bug 另开 PR。
4. 推到你自己的 **fork**，向 `InferTurbo/RouteServer` 的 `main` 提 PR。
5. 按 PR 模板填写，尤其是「如何测试」。
6. 等 CI 与评审。评审意见用新提交回应；必需审批与检查满足后，由 committer 或 maintainer squash 合并。

**绝对不要向 `intranet-sync` 提 PR**。它不是开发分支，而是内网仓库的机器导出镜像：人工改动或合并进去的内容
会在下次同步时被覆盖，因此向它提交的 PR 会被直接关闭，并指向 `main`。

有写权限的 committer 可以直接在仓内开 topic 分支，评审与合入规则完全相同。

### PR 标题与正文

- 使用与[提交标题](#5-提交信息规范)相同的格式和限制：`<type>(<scope>): <祈使句摘要>`，不超过 72 字符，
  结尾不加句号。scope 可省略；适用时添加破坏性变更标记。例如：
  `docs(governance): clarify contribution and review rules`。
- 标题应描述 PR 的最终范围；评审中范围发生变化时，同步更新标题与正文。issue 引用写在正文，不写进标题。
- 填写 [PR 模板](.github/PULL_REQUEST_TEMPLATE.md)：改动内容、动机、类型、实际测试命令与结果，以及迁移或依赖细节。
  不适用的检查项标注 `N/A` 并说明原因；没有运行的测试不得描述为通过。

以上是适用于 GitCode 与 GitHub 的项目约定，并非 GitHub 平台强制的格式。
Markdown 模板只提供正文内容，不会校验标题或强制执行检查。

## 5. 提交信息规范

遵循 [Conventional Commits](https://www.conventionalcommits.org/)。标题格式
`<type>(<scope>): <祈使句摘要>`，不超过 72 字符，结尾不加句号。

| type | 用途 |
| --- | --- |
| `feat` | 用户可见的新能力 |
| `fix` | 缺陷修复 |
| `docs` | 仅文档 |
| `refactor` | 不改变行为 |
| `test` | 仅测试 |
| `perf` | 可度量的性能优化 |
| `build` | 构建系统、Makefile、打包 |
| `ci` | 流水线 |
| `chore` | 其他（依赖、杂务） |

示例：

```
feat(scheduler): add least-requests policy
fix(etcd): retry lease renewal on transient errors
docs(deploy): correct default log file path
```

- scope 可省略但强烈建议，使用包名或子系统名（`scheduler`、`etcd`、`handler`、`metrics`、`deploy`）。
- issue 引用写在正文（`Refs #123`、`Fixes #123`），不要写进标题。
- 破坏性变更用 `feat!:` / `fix!:` 标注，并在正文加 `BREAKING CHANGE:` 说明迁移方式；且必须先有已认可的
  issue。带 scope 时写为 `feat(api)!:` / `fix(api)!:`。同时要求标记与 footer 是本项目比
  [Conventional Commits 1.0.0](https://www.conventionalcommits.org/en/v1.0.0/) 更严格的约定。

### Squash 后的最终提交

对于采用 squash 合并的 PR，包括只有一个 commit 的 PR：

- 使用评审通过后的最终 PR 标题作为 commit 标题，并检查其符合上述标题规则。issue 与 PR 引用放在正文；
  平台自动追加到标题的编号后缀，应在合入前编辑移到正文。
- 正文应让未看过评审讨论的人也能理解：说明问题、改动后的行为及相关验证。小型文档修正用简短说明即可。
  删除临时提交列表、`WIP`/`fixup` 信息和未使用的模板章节。
- 保留相关 issue 引用、迁移说明及适用的 `BREAKING CHANGE:` footer。
- 保留本次 squash 所含改动的原贡献者提供的 `Signed-off-by` 尾注。缺少签署时，应由该贡献者本人补齐；
  不得代他人生成签署，合入者可追加自己的签署。适用的 `Co-authored-by` 署名也应保留，但不能替代 DCO 签署。
- 确认合入前，检查合并预览中的实际标题、正文与尾注。平台默认填充不代表最终提交符合规范；例如
  [GitHub 的 squash 默认信息](https://docs.github.com/en/repositories/configuring-branches-and-merges-in-your-repository/configuring-pull-request-merges/configuring-commit-squashing-for-pull-requests)
  会随仓库设置和提交数量变化。

本节只规定 squash 后的提交信息；合并方式仍以 [docs/branching.zh-CN.md](docs/branching.zh-CN.md) 为准。

## 6. DCO 签署

我们使用 DCO，不要求签 CLA。每个提交都必须带签署：

```bash
git commit -s
```

会追加：

```
Signed-off-by: Your Name <you@example.com>
```

姓名与邮箱必须与你的 `git config user.name` / `user.email` 一致。签署即表示你认可
[DCO 1.1](https://developercertificate.org/)：该改动由你编写，或你有权以本项目许可证提交它。

**请优先使用 GitCode noreply 邮箱**——`<gitcode-username>@noreply.gitcode.com`，而不是公司邮箱：避免把
内网命名带进公开仓库。这条规则之前已经进入历史的身份，由仓库里的 [`.mailmap`](.mailmap) 做归一化显示；
它只改显示，绝不改写历史与 SHA。

缺少 `Signed-off-by` 尾注的提交不符合项目 DCO 要求。只有在托管平台安装并设为必需检查后，自动 DCO 检查
才会拒绝它；本仓库未包含该检查的配置，平台设置也尚未核实。
如果分支上只有你自己的提交，可按以下方式补齐你本人的签署：

```bash
git rebase --signoff upstream/main
git push --force-with-lease
```

使用 GPG/SSH 签名提交是可选项，但推荐。

## 7. 编码规范

- `gofmt`、`go vet` 必须干净。运行 `make lint`，并检查它产生的格式化改动。
  是否自动强制执行，取决于托管平台的 CI 与分支保护配置；见
  [docs/branching.zh-CN.md §4](docs/branching.zh-CN.md#4-评审与-ci-门禁)。
- 每个新增 `.go` 文件都要带项目许可证头（从任意现有文件复制）：

  ```go
  /*
   * Copyright <year> The RouteServer Authors.
   *
   * Licensed under the Apache License, Version 2.0 (the "License");
   * you may not use this file except in compliance with the License.
   * You may obtain a copy of the License at
   *
   *     http://www.apache.org/licenses/LICENSE-2.0
   *
   * Unless required by applicable law or agreed to in writing, software
   * distributed under the License is distributed on an "AS IS" BASIS,
   * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   * See the License for the specific language governing permissions and
   * limitations under the License.
   */
  ```

- 错误用 `%w` 包装并带上足以定位调用点的上下文；不要静默丢弃错误。
- 请求路径上禁止 `panic`，返回 error 由调用方决策。
- 每个导出符号都要有以其名称开头的文档注释。
- 并发：注明某个可变字段由哪个 goroutine 拥有；context 显式透传，不要存进结构体。
- 测试是改动的一部分。新逻辑需要单测；数据面改动应扩展 E2E 套件。纯函数统一用表驱动测试。
- **新增第三方依赖**需在 PR 中获得 maintainer 认可：许可证需与 Apache-2.0 兼容、项目活跃，并说明为何
  标准库或现有依赖无法胜任。提交前执行 `go mod tidy`，一并提交 `go.mod`/`go.sum` 变化。
- 不要提交二进制、压缩包、生成物或编辑器状态。`build/`、`output/`、`.DS_Store`、`.idea/`、`.vscode/`
  已在忽略列表中，请保持。

## 8. 文档要求

- 用户可见的行为变更，必须在**同一个 PR** 内更新 `docs/` 下对应文档：配置改动更新
  [docs/config-spec.md](docs/config-spec.md)，API 改动更新 [docs/restful-api.md](docs/restful-api.md)，
  部署改动更新 [docs/deploy.md](docs/deploy.md)。
- 值得写进版本的特性，同时更新 `docs/versions/release-note-<version>.md`。
- 图片放在 `docs/imgs/`；优先用 Markdown 与代码块，避免易失效的外链。

## 9. 我们不接受的改动

以下 PR 会被直接关闭，反复出现可能被禁止提交：

- **内网专属信息**：内网主机名/域名、工号、内部工单或文档链接、内网构建机路径、非公开仓库地址、
  未脱敏的日志。推送前自查：

  ```bash
  git diff upstream/main... -- . ':!/.mailmap' \
    | grep -nEi 'huawei|codehub|3ms\.com|/home/[a-z]+|10\.[0-9]+\.[0-9]+\.[0-9]+' || true
  ```

  命中不一定算错（私网地址出现在部署示例中是正常的），但必须是有意为之并使用示例占位符。

- 任何形式的凭据：密钥、token、证书、kubeconfig、含真实值的 `.env`。
- 从许可证不兼容的项目抄袭的代码，或抹掉原版权头的代码。
- 与功能改动混在一起的全量格式化、重命名或文件搬迁。
- 把多个互不相关的特性塞进一个 PR。

## 10. 评审流程

- maintainer 或 committer 应在 **7 个自然日内**给出首次回应。若无人响应，在 PR 中 `@InferTurbo/maintainers`
  ——那是我们的流程问题，不是你的。
- 审批以 [GOVERNANCE.zh-CN.md §5](GOVERNANCE.zh-CN.md#5-评审与合入要求) 为准：普通的仅文档、仅测试改动
  需 1 名非作者审批，代码、敏感改动及政策变更需 2 名。必需的领域 owner 或 maintainer 审批计入总人数，
  不额外增加评审人；混合改动取适用的更严格要求。治理政策变更还需执行第 10 节的投票。
  领域归属见 [.github/CODEOWNERS](.github/CODEOWNERS)。
- 评审人可能要求修改设计。评审期间请用新提交回应，不要改写历史；除非被明确要求，否则不要 force-push
  正在评审的分支。
- 必需审批满足、评审对话解决且检查通过后，由 committer 或 maintainer 将普通贡献 PR squash 合并并删除分支。
  两种角色均不得绕过门禁；贡献者不需要自己执行合并。
- 30 天无活动且提醒后无响应的 PR 会作为 stale 关闭，之后可重新打开。

## 11. 许可证

RouteServer 采用 [Apache License 2.0](LICENSE)。提交 PR 即表示你同意以相同条款授权你的贡献，并确认你
有权提交该内容（见[第 6 节](#6-dco-签署)）。
