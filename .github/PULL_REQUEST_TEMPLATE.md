<!--
感谢为 RouteServer 贡献代码 / Thanks for contributing to RouteServer.

提交前请确认 / Before opening this pull request:
  * 读过 CONTRIBUTING.zh-CN.md：一个 PR 只做一件事，提交需 DCO 签署，CI 必须绿
  * 大于 bug 修复或小改进的改动，请先开 issue
  * 目标分支必须是 `main`，绝不是 `intranet-sync`（机器写入的镜像分支）

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
- [ ] `make test` 通过 / passes
- [ ] 端到端套件 / end-to-end: `go test ./e2etest/route-server/ -timeout 60m`
- [ ] 手工验证 / manual check — 说明部署模式、实例数量、端口

```
# 命令 / commands
```

## 检查清单 / Checklist

- [ ] 一个 PR 只有一个逻辑变更，无关修复另开 PR
- [ ] 每个提交都带 `Signed-off-by`（`git commit -s`）
- [ ] 提交标题符合 Conventional Commits（`feat:`、`fix:`、`docs:` …）
- [ ] 新增文件带 Apache-2.0 项目许可证头
- [ ] 为改动逻辑补充或更新了测试
- [ ] 同一 PR 内更新了文档（配置 → `docs/config-spec.md`，API → `docs/restful-api.md`，部署 → `docs/deploy.md`）
- [ ] 没有新增第三方依赖——若有，下方说明理由与许可证
- [ ] diff 中无内网专属信息（内网主机名、工号、工单链接、构建机路径、未脱敏日志）
- [ ] 无任何凭据、密钥或证书，测试夹具里也没有

## 给评审人的说明 / Reviewer notes

<!--
可选：先说该从哪里看起、你自己没把握的地方、希望被讨论的设计点。
涉及调度、实例健康、TLS/中间件或 `deploy/` 的改动，需要该领域 owner 评审（见 .github/CODEOWNERS）。
-->

## 破坏性变更 / 依赖细节 / Breaking change & dependency details

<!-- 新依赖的许可证、用户迁移步骤、废弃窗口。不适用则删除。 -->
