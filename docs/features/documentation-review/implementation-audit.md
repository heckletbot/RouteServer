# Component naming audit / 组件命名审查

This audit retains the historical inventory below. The subsequently authorized module migration is
tracked in [module migration progress](../module-path-migration/progress.md); its working changes do
not rewrite this audit's baseline counts.
本审查保留下述历史清单；后续已授权的模块迁移见[模块迁移进度](../module-path-migration/progress.md)，
当前工作区改动不改写本审查的基线数量。

## Scope and baseline / 范围与基线

Audit requested on 2026-10-31. Occurrence counts below use tracked text at documentation commit
`899deca`, excluding Git history, generated output and untracked internal analysis. Counts include
imports, comments and copyright notices, so a spelling match is not itself a defect.
本审查于 2026-10-31 获授权。以下数量以文档提交 `899deca` 的已跟踪文本为基线，不含 Git 历史、生成物或未跟踪内部分析。
统计包含 import、注释和版权头；命中字样本身不代表缺陷。

Upstream `273fcea` changes only the deployment package directory from `DistributedScheduler` to
`route-server`. It was merged into local `dev` as `5f55114`. The user separately approved aligning
Makefile, CodeArts packaging and bilingual documentation; those fixes were committed locally as
`f7e8185` on the subsequent instruction. This separate audit remains outside that commit.
上游 `273fcea` 只将部署包目录从 `DistributedScheduler` 改为 `route-server`，已通过 `5f55114` 合入本地 `dev`。
用户另行批准对齐 Makefile、CodeArts 打包与中英文文档，随后指令已将修复以 `f7e8185` 本地提交；本独立审查不包含在该提交中。

## Findings / 发现

| Name / 名称 | Evidence and role / 证据与用途 | Assessment / 判断 |
| --- | --- | --- |
| `infer-dp-scheduler` | [go.mod](../../../go.mod), [Makefile](../../../Makefile), [CodeArts build](../../../.codearts/build.sh), [E2E build entry](../../../e2etest/route-server/rs_suit_test.go). 694 occurrences in 162 files at baseline. / 基线 162 个文件、694 处，用于模块、import、版本注入和测试构建。 | A consistent legacy module identity, but different from the product name. Renaming needs a coordinated import/build/linker migration and full validation; it is not part of this fix. / 模块内部一致，但与产品名不同；需同步迁移 import、构建和链接参数并完整验证，本轮未改。 |
| `DistributedScheduler` | Baseline: 41 exact occurrences in 11 files; package directory and ZIP prefix in [Makefile](../../../Makefile), [CodeArts](../../../.codearts/build.sh), [deployment](../../../deploy/deploy.sh) and guides. / 基线 11 个文件、41 处，作为包目录和 ZIP 前缀。 | Upstream changed the consumer before both producers, causing a real package-layout mismatch. The approved fix in `f7e8185` aligns producers, the deployment help text and guides to `route-server`. / 上游先改消费方、未改两套生产方，确有目录不匹配；已按授权通过 `f7e8185` 将配套流程对齐到 `route-server`。 |
| `route-server`, `route_server`, `RouteServer`, `ROUTE_SERVER`, `routeserver` | Binary/config filenames; Go source paths/package identifiers; product/copyright identity; environment variable names; instance examples. / 分别用于二进制与配置文件、Go 路径和标识符、产品与版权、环境变量及实例示例。 | Some variants reflect syntax or interface requirements. Do not replace them all mechanically. / 部分变体由语言语法或接口用途决定，不能机械全局替换。 |
| `model orch`, `mo-deploy` | [Performance log](../../../pkg/route-server/metrics/perf.go): `model orch Perf Metrics: `; [error comment](../../../pkg/common/errs/common.go): `model orch internal error code`; [config-tool default](../../../cmd/config-tool/config_generator.go): `/opt/route_server/mo-deploy/route-server-template.yaml`. / 分别是旧日志前缀、注释、CLI 默认模板路径。 | Three legacy residues. The comment is cosmetic; the log may be consumed by parsers; the default path affects direct CLI callers. The deployment script supplies an explicit template path. / 共三处旧名称；注释可单独清理，日志需核查解析器，默认路径需考虑直接调用 CLI 的用户；部署脚本已显式传入模板路径。 |
| `RouteServer` in `--log-file-path` help | [config-tool help](../../../cmd/config-tool/config_generator.go) still describes `<rs_name>/RouteServer/log/route-server.log`; generated logs use the target directory's `log/route-server.log`. / CLI 帮助仍描述 `RouteServer` 子目录，实际日志位于目标目录下。 | Help text is stale; a later focused change can describe the target-relative path without changing defaults. / 帮助文案过期；后续可按目标目录说明修正，避免连带改变默认行为。 |
| `ENV_ROUTE_SERVER_CONFIG_PATH`, `RouteServer.`, `rs:`, OTEL `RouteServer` | [Environment constant](../../../pkg/common/constant/const.go), [error prefix](../../../pkg/common/errs/common.go), [metrics](../../../pkg/route-server/metrics/metrics.go), [trace identity](../../../pkg/common/trace/trace_otel.go). / 环境变量、错误码前缀、指标名称及追踪身份。 | Externally observable names. Renaming can break deployment configuration, clients, alerts or trace queries and needs separate compatibility approval. / 对外可观察名称，改动可能影响部署、客户端、告警或追踪查询，需另行确认兼容方案。 |
| `vLLM-Cloud` and separator/case variants | No matches in the tracked baseline text. / 已跟踪基线文本中未命中。 | No remaining source-text occurrence found; this does not cover Git history or untracked files. / 源文本未发现残留，不代表历史或未跟踪文件中不存在。 |

The phrase “distributed scheduler” in architecture/security prose is a role description, not an
artifact identifier. Copyright headers are not a target for cosmetic naming replacements.
架构/安全文档中的 “distributed scheduler” 是职责描述，并非制品标识；版权头也不属于普通命名美化的替换范围。

## Recommended boundaries / 建议边界

- Keep `RouteServer` as the product/display name and `route-server` for executable and package naming.
  The package alignment is approved; this audit does not authorize other renames.
  产品展示名采用 `RouteServer`，可执行文件和包名采用 `route-server`；包名对齐已获批准，本审查不授权其他重命名。
- Treat `route_server` Go identifiers and stable environment/metric/error names as separate naming
  domains. Plan any module-path migration as its own milestone, including linker version injection,
  tooling, tests and downstream import compatibility.
  将 Go 标识符及稳定环境变量/指标/错误名分开管理；模块路径迁移需独立里程碑，覆盖链接版本注入、工具、测试及下游 import 兼容。
- Fix old comments/help text separately; check log consumers and CLI compatibility before changing
  runtime strings or defaults.
  旧注释/帮助文字可另行修正；变更运行日志或 CLI 默认值前，先核实消费者与兼容性。

## Validation and remaining risk / 验证与剩余风险

Tracked baseline scans used case-insensitive patterns for optional hyphens, underscores and spaces in
the module, package and product-name families. Build/deployment consumers and version-injection sites
were inspected directly. No runtime identity, module path or public protocol name was changed.
基线扫描按大小写不敏感、可选连字符/下划线/空格覆盖模块、包和产品名变体，并直接复核构建/部署消费方及版本注入点。
未修改运行身份、模块路径或公开协议名称。

The package directory change does not migrate existing installations. Old archives and existing
`DistributedScheduler` installation trees need an explicit upgrade plan; the deploy script's process
lookup uses the new path and must not be assumed to stop a process launched from the old directory.
No installation, process stop or cleanup was performed in this milestone.
包目录变更不会迁移已有安装。旧归档和 `DistributedScheduler` 安装目录仍需明确升级方案；部署脚本按新路径查找进程，
不能假定它会停止旧目录启动的进程。本里程碑没有执行安装、停进程或清理操作。
