<!--
感谢为 RouteServer 贡献代码 / Thanks for contributing to RouteServer.

提交前请确认 / Before opening this pull request:
  * 读过 CONTRIBUTING.zh-CN.md：一个 PR 只做一件事，提交需 DCO 签署，CI 必须绿
  * 大于 bug 修复或小改进的改动，请先开 issue
  * 目标分支必须是 `main`，绝不是 `intranet-sync`（机器写入的镜像分支）
  * PR 标题与 commit 标题遵循同一规范：<type>(<scope>): <祈使句摘要>，不超过 72 字符，结尾不加句号
    PR titles follow commit subject rules: <type>(<scope>): <imperative summary>, at most 72 characters, no trailing period.
    scope 可选；破坏性变更的标记与说明见 CONTRIBUTING.md §5。
    Scope is optional; see CONTRIBUTING.md §5 for breaking-change markers and details.
  * 不适用的检查项标注 N/A 并说明原因；未运行的测试不可标为通过
    Mark inapplicable checks N/A with a reason; do not mark unrun tests as passed.
  * 审批按 GOVERNANCE.zh-CN.md §5：普通仅文档、仅测试改动需 1 名非作者，代码、敏感改动及政策变更需 2 名；owner/maintainer 计入总人数
    Follow GOVERNANCE.md §5: ordinary docs-only/tests-only changes need 1 non-author approval; code, sensitive and policy changes need 2; required owner/maintainer approvals count within the total.
    混合改动取更严格要求，治理政策变更另需第 10 节投票；必需审批与检查满足后，committer 或 maintainer 可合入。
    Mixed changes take the stricter requirements; governance policy changes also require the §10 vote. Committers or maintainers may merge after required approvals and checks pass.

填完各节后可删除这些注释。Delete these comments once the sections below are filled in.
-->

## 改动内容 / What this changes

<!-- 一两句话说清改动后行为有什么不同 / One or two sentences: what behaviour is different after this change. -->

## 动机 / Why

<!-- 讲问题，不是讲 patch。关联 issue：Fixes #123 / Refs #456 -->

## 类型 / Type of change

- [ ] Bug 修复，无破坏性变更 / Bug fix (non-breaking)
- [ ] 新特性，无破坏性变更 / New feature (non-breaking)
- [ ] 破坏性变更 / Breaking change — 迁移方式见下方
- [ ] 仅文档 / Documentation only
- [ ] 重构、测试、构建或 CI / Refactor / test / build / CI only

## 测试方式 / How it was tested

<!--
写具体：跑了哪些命令、观察到什么。「make test 通过」只有在你说明实际覆盖了哪些套件时才有意义。
Be concrete: the commands you ran and what you observed.
-->

- [ ] `make lint` 干净 / clean
- [ ] `go test ./... -v -timeout 60m` 通过 / passes
- [ ] 端到端套件 / end-to-end: `go test ./e2etest/route-server/ -timeout 60m`
- [ ] 手工验证 / manual check — 说明部署模式、实例数量、端口

```
# 命令 / commands
```

<!--
make test / make test-unit / make test-coverage 当前没有保留 go test 的失败退出码；请直接运行 go test 取得可靠结果。
These Make targets currently lose the go test failure exit status; run go test directly for a reliable result.
make test-unit 的 -short 当前不会跳过 E2E 套件 / -short currently does not skip the E2E suite.
-->

## 检查清单 / Checklist

- [ ] 一个 PR 只有一个逻辑变更，无关修复另开 PR
- [ ] 每个提交都带 `Signed-off-by`（`git commit -s`）
- [ ] 提交标题符合 Conventional Commits（`feat:`、`fix:`、`docs:` …）
- [ ] PR 标题符合 CONTRIBUTING.md §4 的格式与长度要求 / PR title follows the format and length rules in CONTRIBUTING.md §4
- [ ] 新增文件带 Apache-2.0 项目许可证头
- [ ] 为改动逻辑补充或更新了测试
- [ ] 同一 PR 内更新了文档（配置 → `docs/config-spec.md`，API → `docs/restful-api.md`，部署 → `docs/deploy.md`）
- [ ] 没有新增第三方依赖——若有，下方说明理由与许可证
- [ ] diff 中无内网专属信息（内网主机名、工号、工单链接、构建机路径、未脱敏日志）
- [ ] 无任何凭据、密钥或证书，测试夹具里也没有

## 给评审人的说明 / Reviewer notes

<!--
可选：先说该从哪里看起、你自己没把握的地方、希望被讨论的设计点。
涉及调度、实例健康、TLS/中间件或部署逻辑的改动，请指出适用的领域 owner；审批要求以 GOVERNANCE.zh-CN.md §5 为准，owner 计入总人数。
For scheduling, instance health, TLS/middleware or deployment logic, identify the applicable area owner. Follow GOVERNANCE.md §5; required owner approvals count within the total.
-->

## 破坏性变更 / 依赖细节 / Breaking change & dependency details

<!-- 新依赖的许可证、用户迁移步骤、废弃窗口。不适用则删除。 -->

<!--
合入者：squash 前按 CONTRIBUTING.md §5 检查最终提交信息，包括标题、正文、破坏性变更说明、原贡献者签署和署名。
Integrators: before squash merging, check the final subject, body, breaking-change details, contributor sign-offs and attribution per CONTRIBUTING.md §5.
不得代他人生成签署 / Do not generate a sign-off for someone else.
-->
