# Module migration progress / 模块迁移进度

## 2026-10-31: Authorized scope / 授权范围

- Commit the already verified Makefile/package fixes locally; prepare the module migration proposal
  without implementing the rename or pushing. The build fix was committed as `f7e8185` with DCO.
  用户授权本地提交已验证的构建修复，再形成模块迁移方案，不实施重命名、不推送；构建修复已以带 DCO 的 `f7e8185` 提交。
- [English plan](plan.md) and [中文方案](plan.zh-CN.md) cover inventory, source compatibility,
  implementation ordering, acceptance checks and rollback. Implementation required further approval
  at that planning stage; the later authorization is recorded below.
  双语方案覆盖清单、源码兼容、实施顺序、验收和回退；规划时实施仍待批准，后续授权见下文。

## Evidence and validation / 证据与验证

- Read the existing naming audit, build/deployment guidance, module file, both linker configurations,
  E2E bootstrap and the existing version-response handler. A tracked-file scan at `f7e8185` found
  162 files/694 occurrences: 678 imports in 157 Go files, three E2E build arguments, eight linker
  symbols, one module declaration and four guide occurrences. No tracked workspace/vendor tree or
  module replacement directive was found.
  已阅读既有审查、构建/部署指南、模块文件、两套链接配置、E2E 入口及版本响应实现；静态统计见双语方案。
- Consulted official Go module and linker documentation. Downstream use and platform runner access
  remain unverified and are explicit pre-implementation gates.
  已核对 Go 官方模块与链接文档；下游使用及平台 runner 可用性尚未验证，已列为实施前门禁。
- The module-migration commands in the plan are proposed validation, not executed results. No project
  source, go.mod, go.sum or linker configuration was changed during planning.
  方案中的模块迁移命令属于拟执行验证，不是已执行结果；规划期间未修改项目源码、go.mod、go.sum 或链接配置。
- `python3 /private/tmp/routeserver-module-plan-check.py` passed: 53 local Markdown files and
  468 local links/anchors checked; 24 bilingual pairs have matching heading levels and fence counts.
  Inventory and proposed validation gates are present in both plan versions. `git diff --check` passed.
  最终方案检查通过：53 份本地 Markdown、468 个本地链接/锚点及 24 组双语结构有效，双语方案均覆盖清单与验证门禁；
  `git diff --check` 通过。
- Git scope checks confirmed HEAD remains `f7e8185`, the index is empty, and no Go source, module,
  dependency file, Makefile or CodeArts linker configuration changed while planning. Existing unrelated
  `.gitignore` content was compared by hash and preserved. The proposal remains uncommitted.
  Git 范围检查确认 HEAD 保持 `f7e8185`、暂存区为空，规划期间未修改 Go 源码、模块、依赖、Makefile 或 CodeArts 链接配置；
  既有 `.gitignore` 经哈希对比保持不变，方案仍未提交。

## Planning status / 规划状态

Inventory and proposal complete; document and scope checks passed. No rename, implementation commit,
push or deployment was performed or authorized by this planning milestone. Downstream compatibility
and runner access remain explicit gates before a later implementation approval.
清单与方案已完成，文档和范围检查通过；本里程碑未实施、也不授权重命名、实施提交、推送或部署。
下游兼容及 runner 可用性仍是后续实施批准前需要确认的门禁。

## 2026-10-31: Authorized implementation / 已批准实施

The user authorized implementation of the proposed repository migration while preserving directories,
package declarations and runtime protocols, adding validation and bilingual documentation, and making
no commit or push. This authorizes the repository's atomic source-path change without a compatibility
alias; it does not establish that external consumers or native Linux release runners were verified.
用户已批准按方案实施仓内原子迁移，保留目录、包声明和运行协议，补充验证与双语文档，不提交、不推送。
未增加旧路径兼容别名；该授权不代表仓外消费者或 Linux 原生发布 runner 已经完成核实。

### Actual changes / 实际变更

- Go AST editing changed 678 import declarations in 157 existing Go files and the three E2E
  `gexec.Build` strings. The module directive and both producers' eight `-X` targets now use
  `route-server`. Both contribution guides describe the new path.
  AST 编辑同步修改 157 个存量 Go 文件中的 678 条 import 及 3 个 E2E 构建字符串，模块声明和两套构建的
  8 个链接目标均已更新为 `route-server`，贡献指南同步双语示例。
- Reversing only those AST-located edits and formatting both sides reproduced the baseline contents
  of all 157 files. This checks that no existing business code, package clause, protocol/config key,
  environment variable, error prefix, metrics identity, trace identity or copyright text was altered.
  仅反向归一化 AST 定位的改动并格式化后，157 个文件均与基线一致，确认没有混入既有业务实现、包声明、
  协议/配置键、环境变量、错误前缀、指标/追踪身份或版权文本变更。
- Added `TestPrintVersionModuleMetadata` and `.codearts/test_module_metadata.py`. The Go test checks
  the existing response contract and externally supplied injected expectations; the Python check
  builds real Make/CodeArts artifacts in temporary snapshots and checks `go version -m` plus the
  existing loopback `GET /` response, with process termination in a finally block.
  新增 Go 版本响应测试及真实制品验证脚本，检查模块构建信息和已有版本接口；临时 server 在 finally 中退出并等待回收。
- Added bilingual unreleased source-breaking notes rather than inventing a release version. Updated
  plan status and implementation records. Existing dependency versions and go.sum remain unchanged;
  no self-replacement alias, production API or CLI option was added.
  新增双语未发布破坏性源码说明，不假定发版号；更新方案和实施记录，依赖版本及 go.sum 不变，未增加兼容别名或生产接口/CLI 选项。

### Environment / 验证环境

Baseline: a disposable archive of `f7e8185`. Host: macOS arm64, Make 3.81, Bash 3.2.57. The installed
Go 1.27.1 was recorded separately; official Go 1.25.0 was downloaded into the temporary validation
cache without changing global configuration or repository dependencies. Logs and real Linux
cross-build artifacts are under `/private/tmp/routeserver-module-validation-evnncdrj`.
基线为 `f7e8185` 的独立临时副本。本机 macOS arm64、Make 3.81、Bash 3.2.57；分别记录本机 Go 1.27.1，
并将官方 Go 1.25.0 下载到临时验证缓存，未改全局配置或仓库依赖。日志和 Linux 交叉制品位于上述临时目录。
The local Make version is below the documented minimum 3.82; these successful local checks do not
replace validation on the supported build runners.
本机 Make 低于文档最低要求 3.82，本地检查通过不替代受支持构建 runner 的验证。

### Pre-commit validation results / 提交前验证结果

| Check / 检查 | Result / 结果 |
| --- | --- |
| Module and graph / 模块与包图 | `go list -m` gives `route-server`; 44 repository packages, 786 dependency/test package identities and 1,084 module-graph entries match baseline after prefix normalization. Test-package traversal order differs because import order changed; normalized identity sets are equal. / 模块正确，44 个仓内包、786 个依赖/测试包身份和 1,084 条模块图记录归一化后一致；仅遍历顺序变化。 |
| Compile/link / 编译链接 | `go test -mod=readonly ./... -run '^$' -count=1` passed on both Go 1.27.1 and Go 1.25.0; the final Go 1.25 run includes the new metadata test file and passed in 7.64 seconds. The injected test separately passed in 3.15 seconds. / 两个工具链均通过全包编译链接，最终 Go 1.25 检查包含新增文件，注入定向测试另行通过。 |
| Dependencies / 依赖 | `go mod tidy -diff` exited 0 with no diff; dependency directives and go.sum are preserved. / tidy 无差异，依赖声明及校验和保持不变。 |
| Existing build regression / 既有构建回归 | `python3 .codearts/test_makefile.py`: all seven tests passed, including real isolated Go pass/fail fixtures and archive-layout fixtures. / 7 项均通过。 |
| Injected metadata / 注入元数据 | Go 1.25.0 focused test passed with four distinct `-X` sentinel values. A deliberate stale-prefix negative control failed as expected with all four fields still `Unknown`, proving the assertion detects omitted linker migration. / 新前缀哨兵通过，故意保留旧前缀的负对照按预期失败。 |
| Real native artifacts / 真实本机制品 | `python3 .codearts/test_module_metadata.py` passed through both actual producers on darwin/arm64; `GET /` matched all seven response fields, including the four build inputs, and both build-info records identify `route-server`. / 两套实际构建入口的本机产物通过模块及版本接口检查。 |
| Linux cross-builds / Linux 交叉构建 | Actual CodeArts script built ELF/archive artifacts for linux/amd64 and linux/arm64; module/layout checks passed. Reading each ELF's four linked Go string variables matched its recorded build inputs, including branch/commit/time. Native Linux execution was not available. / 两种架构的真实 ELF 与归档通过身份/布局检查，四项链接变量均与记录输入相等；未进行 Linux 原生运行。 |
| Vet / 静态检查 | `go vet -mod=readonly ./...` reports 21 copylock diagnostics; all 21 exactly match baseline after module-prefix normalization. The gate is not green. / 21 条锁值拷贝诊断与基线一致，不记为通过。 |
| Unit tests / 单元测试 | Native Go 1.25.0 with `-gcflags=all=-l` fails in both baseline and migrated `cmd/config-tool` and `pkg/common/client/instance-client`, with gomonkey-related assertion/fault symptoms. Other cmd/pkg packages pass. Go 1.27.1's baseline also fails `Test_getHostIp`; its sandboxed migrated run was interrupted after local-listen restrictions, not counted as a pass. / 同环境基线及迁移后均有上述两个包失败，不计为全量单测通过。 |
| Race / 竞态 | Full native Go 1.25.0 race run reports failures in instances synchronization and metrics tests. The same targeted cases reproduce race reports in the baseline checkout. No concurrency implementation was changed. / 全量竞态失败，基线对应实例同步/指标用例同样复现，未修改并发实现。 |
| E2E / 端到端 | Go 1.25.0 non-root full suite exited 0 in 1,601.49 seconds: 123 passed, 0 failed, 0 pending, 3 privileged iptables cases skipped. All three migrated build entries and suite teardown passed. / 非 root 全套通过，123 个通过、0 个失败、0 个待执行，3 个特权 iptables 用例跳过；三个构建入口及清理阶段通过。 |
| Documents and scope / 文档与范围 | 55 Markdown files, 481 local links/anchors and 25 bilingual structures checked; formatting and `git diff --check` passed. HEAD remains `f7e8185` and the index is empty. No tracked directory/file moves or deletions; all tracked deployment assets, go.sum and pre-existing .gitignore content retain their baseline hashes. / 文档、格式及范围检查通过，未移动或删除存量目录/文件，部署资产、依赖校验和及原有 .gitignore 内容保持不变，HEAD 和暂存区未变。 |

Initial sandbox-only runs could not bind localhost sockets; affected long-running test jobs were
stopped and rerun with the required local-test permissions after checking test listeners. These
interrupted runs are not successful validation. The metadata snapshot test shuts its processes down;
the actual E2E suite retains its existing setup/teardown behaviour.
初始沙箱验证不允许绑定本地端口，相关长测试已停止并在检查端口后以所需权限重跑；中断不记为成功。
元数据检查会回收自身进程，E2E 保留既有启动与清理行为。

### Commands / 主要命令

The native comparison commands used the downloaded Go 1.25.0 on PATH, `GOTOOLCHAIN=local`,
`GOWORK=off`, disabled module downloads and temporary Go build/temp directories. Baseline commands
ran in the archived checkout; migrated commands ran in the working tree. Local-network test commands
required permission outside the restricted sandbox. Exit/results are classified above, not all zero.
原生对照命令使用临时下载的 Go 1.25.0、临时缓存和临时目录，关闭 workspace 与依赖下载；
基线在独立副本运行，迁移后在工作区运行。本地网络测试申请了所需权限；退出结果按上表分类，并非全部为 0。

```bash
go list -m -f '{{.Path}}'
go list -mod=readonly ./...
go list -mod=readonly -deps -test ./...
go mod graph
go test -mod=readonly ./... -run '^$' -count=1
go test -mod=readonly -gcflags=all=-l ./cmd/... ./pkg/... -count=1 -timeout 60m
go vet -mod=readonly ./...
go test -mod=readonly -gcflags=all=-l -race ./cmd/... ./pkg/... -count=1 -timeout 60m
ROUTER_TEST_ENABLE_SU=false go test -mod=readonly ./e2etest/route-server/ -v -count=1 -timeout 60m
go mod tidy -diff
python3 .codearts/test_makefile.py
python3 .codearts/test_module_metadata.py
```

Baseline race reproduction additionally used the same race/compiler flags with
`./pkg/route-server/instances ./pkg/route-server/metrics` and
`-run 'TestPeriodSync_CopyOnWrite|TestLeaderChangesSync|TestMetrics|TestStartMetric_Enable|TestStopMetric_Enable'`.
Both baseline and migration report those same five failing race tests. Linux cross-builds ran the
actual CodeArts script in a temporary snapshot with `GOOS=linux`, `CGO_ENABLED=0` and each target
`GOARCH`; successful artifacts were inspected rather than executed on macOS.
基线竞态复现另用上述两个包及五个用例过滤条件，前后同样复现五个失败用例。
Linux 交叉构建在临时副本中设置目标系统/架构并运行实际 CodeArts 脚本，检查制品而不在 macOS 执行 ELF。

### Remaining gates / 剩余门禁

Do not describe the migration as fully accepted while baseline unit/vet/race defects remain and
native Linux/downstream checks are outstanding. Those failures are recorded for separate follow-up;
this milestone does not authorize fixing unrelated concurrency or monkey-patching behaviour.
No commit, push, deployment or old-installation migration has been performed.
基线单测/vet/竞态缺陷、Linux 原生及下游检查尚未解决前，不将迁移描述为完整验收；这些问题另行跟进，
本轮不授权扩展修复无关并发或 monkey patch 行为。未提交、推送、部署或迁移旧安装。

## Latest status / 最新状态

Source migration, regression coverage, bilingual documentation and progress updates are complete in
the working tree. The non-root E2E suite and scoped build/module checks passed. Existing unit/vet/race
failures were reproduced against the baseline; native Linux execution and external downstream checks
remain outstanding, so this is not an all-green release acceptance. The subsequent local-commit
authorization and delivery scope are recorded below; no push is authorized.
源码迁移、回归覆盖、双语文档及进度更新已在工作区完成，非 root E2E 和本轮构建/模块检查通过。
既有单测/vet/竞态失败已在基线复现，Linux 原生运行及仓外下游检查仍待完成，不记为全部发布门禁通过；
后续本地提交授权和交付范围见下文，不授权推送。

## 2026-10-31: Local commit authorization / 本地提交授权

The user authorized a local commit of the completed migration, without pushing. The dedicated commit
uses `fix(module)!: align module path with route-server`, explains downstream import migration in a
`BREAKING CHANGE:` footer and carries the configured author's DCO sign-off. Its scope includes the
module/import/linker changes, new regression coverage, bilingual guides and unreleased notes, plans,
the supporting historical naming audit and progress records. No release or deployment is implied.
用户已授权将已完成迁移本地提交，不推送。独立提交使用上述标题，在 `BREAKING CHANGE:` 中说明下游 import 迁移，
并附当前配置作者的 DCO 签署。范围包含模块/import/链接修改、新增回归、双语指南及未发布说明、方案、配套历史命名审查
和进度记录，不代表发布或部署。

The pre-commit checks above describe the uncommitted validation snapshot against `f7e8185`. Existing
unrelated `.gitignore` edits, AGENTS.md, editor history and internal production-analysis files are excluded
from the migration commit. Source verification is unchanged; only delivery-status documentation was
updated for this authorization, with document and diff checks repeated before committing.
上表描述相对 `f7e8185` 的提交前验证快照。既有无关 `.gitignore`、AGENTS.md、编辑器历史和内部生产分析文件均不纳入迁移提交。
源码验证结果未变，本次授权仅更新交付状态文档，提交前重做文档及 diff 检查。
