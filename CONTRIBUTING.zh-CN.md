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
make test-unit     # go test ./... -short，跳过较慢的集成路径
make lint          # go fmt + go vet
make package       # 构建 + 部署件 + version.info + 打 zip
```

构建产物（以 `Makefile` 为准）：

| 目标 | 产物 |
| --- | --- |
| `make build` | `output/DistributedScheduler/bin/route-server`、`output/config_generator` |
| `make package` | `output/DistributedScheduler-<version>-<timestamp>.zip` |

> 注意：`README.md` 与 `docs/build.md` 中仍残留旧包名（`ModelOrch-*`、`route-server-*`），以上表为准。

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
make lint && make test
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
6. 等 CI 与评审。评审意见用新提交回应；合并方式由 maintainer squash。

**绝对不要向 `intranet-sync` 提 PR**。它不是开发分支，而是内网仓库的机器导出镜像：人工改动或合并进去的内容
会在下次同步时被覆盖，因此向它提交的 PR 会被直接关闭，并指向 `main`。

有写权限的 committer 可以直接在仓内开 topic 分支，评审与合入规则完全相同。

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
  issue。

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

未签署的提交会导致 DCO 检查失败。修正已有分支：

```bash
git rebase --signoff upstream/main
git push --force-with-lease
```

使用 GPG/SSH 签名提交是可选项，但推荐。

## 7. 编码规范

- `gofmt`、`go vet` 必须干净，CI 通过 `make lint` 强制。
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
- 需要 2 个来自作者以外的人 approve。`pkg/route-server/scheduler/`、`pkg/common/scheduler/`、
  `pkg/route-server/middleware/`、`pkg/common/http/`、`deploy/` 下的改动，还需要该领域的 owner approve，
  见 [.github/CODEOWNERS](.github/CODEOWNERS)。
- 评审人可能要求修改设计。评审期间请用新提交回应，不要改写历史；除非被明确要求，否则不要 force-push
  正在评审的分支。
- 通过且 CI 绿后，由 maintainer squash 合并并删除分支，你不需要自己合。
- 30 天无活动且提醒后无响应的 PR 会作为 stale 关闭，之后可重新打开。

## 11. 许可证

RouteServer 采用 [Apache License 2.0](LICENSE)。提交 PR 即表示你同意以相同条款授权你的贡献，并确认你
有权提交该内容（见[第 6 节](#6-dco-签署)）。
