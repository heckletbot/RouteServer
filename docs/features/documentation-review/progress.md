# Documentation review progress / 文档审查进度

This record covers the documentation fixes, confirmed policies, authorized local commits and the
subsequent Makefile/package fixes and naming audit. Earlier validation records describe the state at
their respective milestones; the latest milestone and status appear at the end.
本记录涵盖文档修正、已确认政策、授权的本地提交，以及后续 Makefile/包目录修复和命名审查。
早期验证记录反映各里程碑当时状态；最新里程碑与完成状态见末尾。

## 2026-10-31: Authorized milestone / 已批准里程碑

- Baseline: `5881dd2`. Fix factual documentation issues and document PR titles and final squash messages in both languages.
  基线：`5881dd2`。修正文档事实错误，补齐 PR 标题与 squash 后提交信息规范，中英文同步。
- During this first milestone, approval counts, merge permissions and release rules were pending user confirmation. No commits or pushes were authorized.
  第一里程碑执行时，审批人数、合入权限、发版规则待用户确认；未获提交或推送授权。
- Acceptance: working relative links and heading anchors, matching bilingual structure and command examples, documented behaviour checked against current implementation, and existing policy sections preserved.
  验收：相对链接与标题锚点有效、中英文结构和命令示例一致、行为说明与现有实现一致、原政策章节保持不变。

## Changes / 实际变更

- Added PR title and description requirements, final squash subject/body requirements, and preservation of contributor sign-offs and attribution in CONTRIBUTING and the bilingual PR template.
  在贡献指南与双语 PR 模板中补充标题、正文、squash 最终信息，以及贡献者签署和署名保留要求。
- Replaced recommended Make test invocations with direct `go test` commands; documented lost Make test exit codes and the E2E suite's lack of a short-mode skip. Removed the obsolete package-name warning.
  推荐测试命令改为直接调用 `go test`；说明 Make 退出码丢失、E2E 不响应 short 跳过的问题；删除过期包名提示。
- Corrected the Messages API's unregistered v0 route response and the scaling scripts' explicit API-server counts and inclusive port ranges.
  校正 v0 模式未注册 Messages 路由的响应，以及扩缩容脚本显式 server 数量和端口范围的说明。
- Fixed Chinese security-guide links and marked maintainer registration, private reporting channels and platform enforcement as unverified where applicable.
  修复中文安全文档链接，如实标注维护者登记、私密上报渠道和平台强制检查的未验证状态。
- No runtime code, Makefile, API, protocol, schema or deployment configuration changes. Existing unrelated working-tree changes and internal analysis documents were preserved.
  无运行代码、Makefile、API、协议、Schema 或部署配置变更；保留已有无关工作区改动与内部分析文档。

## Validation / 验证记录

- `git diff --check`: passed; no whitespace errors in tracked changes. The new progress file was checked separately for trailing whitespace and a final newline.
  `git diff --check`：通过，已跟踪改动无空白错误；新增进度文件另行检查了尾随空白和末尾换行。
- `python3 /private/tmp/route-server-documentation-review-check.py`: final run exited 0. Checked 49 Markdown files (48 tracked documents plus this progress record) and 411 local links/anchors; no missing targets, anchors or unclosed fences. All 23 bilingual pairs have matching heading levels and fence counts; changed code/commands match after ignoring translated shell comments. Existing translated diagrams were compared with the baseline and preserved.
  `python3 /private/tmp/route-server-documentation-review-check.py`：最终退出码 0。检查 49 份 Markdown（48 份已跟踪文档及本进度）和 411 个本地链接/锚点，无失效目标、失效锚点或未闭合围栏。23 组双语标题层级与围栏数量一致；忽略翻译的 shell 注释后，改动的代码与命令一致；已有双语示意图与基线比对后保留。
- The same temporary validation script confirmed that governance, CODEOWNERS, approval/merge requirements and release sections match HEAD. Changes to branching section 4 are limited to configuration-status and test-result explanations; its policy bullets and gate table are unchanged.
  同一临时校验脚本确认治理、CODEOWNERS、审批/合入要求和发版章节与 HEAD 一致。分支文档第 4 节仅修改配置状态与测试结果说明，政策列表及门禁表未变。
- The script parsed both scaling examples with the existing argument parsers, then exercised the existing registration/deregistration functions with HTTP calls and waits mocked. Each example generated 32 instances on ports 9000–9015 across two IPs; omitted counts and zero counts matched the documentation for both roles. No network requests or real registration/deregistration occurred.
  脚本使用现有参数解析器解析两条扩缩容示例，再以 HTTP 和等待替身调用现有注册/注销函数；每条示例在两个 IP 上生成 9000–9015 端口的 32 个实例，省略数量和数量为 0 的行为在两种角色下均符合文档。没有网络请求或真实注册/注销。
- With temporary build directories and deliberately failing `GO=/usr/bin/false`, `make test`, `make test-unit` and `make test-coverage` all returned 0, confirming the documented existing exit-code defect. Static checks confirmed the v0 default route bindings and the absence of a short-mode check in the E2E entry point. These checks do not establish runtime API or Go test success.
  使用临时构建目录和故意失败的 `GO=/usr/bin/false` 验证，三个 Make 测试目标均返回 0，确认文档描述的既有退出码缺陷。静态复核确认 v0 默认路由绑定及 E2E 入口没有 short 判断。这些验证不代表运行时 API 或 Go 测试通过。
- Go tests, `go vet`, `go test -race`, real deployments and hosting-platform settings are not part of this documentation-only validation.
  本次仅修改文档，不运行 Go 测试、`go vet`、`go test -race` 或真实部署，不将平台设置描述为已验证。

## Confirmed policies / 已确认政策

The user confirmed the following three policies on 2026-10-31 and authorized applying them to the
related documentation in both languages. This confirmation does not authorize commits or pushes.
用户于 2026-10-31 确认以下三项政策，并授权同步修改中英文相关文档；该确认不包含提交或推送授权。

1. One non-author approval for ordinary documentation and test-only changes; two for ordinary code and sensitive changes, with the required area owner included in the two. Policy/security-document changes retain their maintainer approval requirement within the total. Mixed changes take the stricter requirement. Confirm platform support before implementing path-dependent enforcement.
   普通文档与仅测试改动需 1 名非作者审批；普通代码及敏感改动需 2 名，领域 owner 包含在这 2 名中。治理、安全政策变更保留 maintainer 审批要求，并计入总人数；混合改动取更严格要求。按路径自动执行前先核实平台支持。
2. Committers and maintainers may merge after all required approvals and checks; maintainers retain responsibility for repository settings, role management and releases. Neither role bypasses the gates.
   committer 与 maintainer 均可在审批和检查满足后合入；仓库设置、角色管理和发版仍由 maintainer 负责；两种角色均不绕过门禁。
3. For the current pre-1.0 stage, release only from `main`, consistent with supporting only the latest release. Defer maintenance release branches until multiple supported versions are explicitly approved, then update branching and security support policies together.
   当前 pre-1.0 阶段仅从 `main` 发版，与仅支持最新版本保持一致；待明确批准同时维护多个版本后，再引入维护分支并同步调整分支与安全支持政策。

## Remaining work / 剩余问题

- At the documentation milestone, the Make exit-code defect and E2E short-mode behaviour were deferred code issues. The exit-code fix is now covered by the later milestone below; E2E short-mode selection remains unchanged.
  文档里程碑将 Make 退出码缺陷和 E2E short 行为留作后续代码事项；退出码修复现见下方后续里程碑，E2E short 选择行为仍未改变。
- Real maintainers, responders, private channels and platform gates require registration or verification; no identities or configuration state were invented.
  真实维护者、响应人、私密渠道和平台门禁仍需登记或验证，未虚构账号或配置状态。
- Platform-side settings still require a separate verification and configuration step; documentation changes do not activate enforcement. No commit, push or deployment was performed.
  平台设置仍需另行验证与配置，文档修改不会启用自动门禁；未提交、推送或部署。

## 2026-10-31: Policy reconciliation milestone / 政策统一里程碑

- Authorization: apply the three confirmed policies above, update both languages and this progress record, and do not commit or push.
  授权范围：按上述三项确认统一政策，同步中英文和进度记录，不提交、不推送。
- Made GOVERNANCE section 5 the approval source: ordinary documentation/test-only changes need one distinct non-author approval; code, sensitive and policy changes need two, with required owner/maintainer approvals included in the total. Mixed changes take the stricter requirements; existing governance votes and security-reviewer requirements remain applicable.
  以治理文档第 5 节为审批依据：普通仅文档、仅测试改动需 1 名不同的非作者，代码、敏感及政策变更需 2 名，必需 owner/maintainer 计入总人数。混合改动取更严格要求，既有治理投票及安全评审人要求仍适用。
- Updated GOVERNANCE, CONTRIBUTING, SECURITY, branching, the bilingual PR template, CODEOWNERS comments and MAINTAINERS role descriptions. Committers and maintainers may merge after approvals, conversation resolution and passing checks; settings, roles and releases remain maintainer responsibilities.
  同步治理、贡献、安全、分支规范、双语 PR 模板、CODEOWNERS 注释及维护者角色说明。committer 与 maintainer 均可在审批满足、对话解决且检查通过后合入；仓库设置、角色管理及发版仍由 maintainer 负责。
- Removed the active maintenance-branch row, cherry-pick workflow and release-branch tagging instructions. Current releases and hotfixes come only from main; future multi-version support needs explicit approval and coordinated governance, branching and security-policy updates.
  删除当前维护分支表项、cherry-pick 流程及维护分支打 tag 指令。当前普通版本与热修复均仅从 main 发版；未来多版本支持需明确批准并同步修改治理、分支和安全政策。
- Replaced the old one-approval emergency merge exception with prioritized hotfix review under the same approval and CI gates. The hotfix label does not bypass the confirmed requirements.
  将旧的单人审批紧急合入例外改为同等审批和 CI 门禁下优先评审；hotfix 标签不豁免已确认要求。
- No runtime code, Makefile, deployment configuration or platform settings were changed. CODEOWNERS path-to-team mappings were preserved; only explanatory comments changed.
  未修改运行代码、Makefile、部署配置或平台设置；CODEOWNERS 路径与团队映射保持不变，仅更新解释性注释。

### Validation / 验证记录

- `git diff --check`: passed, no whitespace errors. The untracked progress record was checked separately for trailing whitespace and a final newline.
  `git diff --check`：通过，无空白错误；未跟踪的进度记录另行检查尾随空白和末尾换行。
- `python3 /private/tmp/routeserver-policy-documentation-check.py`: exited 0. Checked 49 Markdown files and 429 local links/anchors; no missing targets/anchors or unclosed fences. All 23 bilingual pairs have matching heading levels and fence counts; changed code/command examples match after ignoring translated shell comments.
  `python3 /private/tmp/routeserver-policy-documentation-check.py`：退出码 0。检查 49 份 Markdown 和 429 个本地链接/锚点，无失效目标、失效锚点或未闭合围栏。23 组双语标题层级与围栏数量一致；忽略翻译的 shell 注释后，改动的代码/命令示例一致。
- Structured checks confirmed matching approval tables, owner/maintainer inclusion, mixed-change handling, shared merge permissions, main-only releases and removal of the emergency approval bypass. Source review confirmed the retained governance voting and security-reviewer requirements.
  结构化检查确认审批表一致、owner/maintainer 计入人数、混合改动规则、合入权限、main 唯一发版源及紧急审批豁免移除；文本复核确认保留治理投票与安全评审人要求。
- Compared with a pre-milestone content-hash snapshot: only 11 existing policy-related files changed in this milestone, plus this progress record. CODEOWNERS mappings, unrelated working-tree changes, HEAD and the index were preserved. No Go tests or real platform changes were performed.
  与本里程碑开始前的内容哈希快照对比：本轮仅修改 11 份已有政策相关文件及本进度记录；CODEOWNERS 映射、无关工作区改动、HEAD 和暂存区均保留。未运行 Go 测试或修改真实平台设置。
- Go tests, go vet and race tests are not run for this documentation-only milestone; hosting-platform enforcement remains unverified.
  本里程碑仅修改文档，不运行 Go 测试、go vet 或竞态测试，平台自动执行状态仍未核实。

## Status / 完成状态

Both authorized milestones are complete. The three confirmed policies have been applied consistently
to the documentation in both languages and verified. Platform enforcement and the remaining code issues
listed above are separate follow-up work. No commit, push or deployment was performed during those milestones.
两个已授权里程碑均已完成；三项确认政策已在中英文文档中统一并通过验证。
平台自动执行及上述剩余代码问题属于后续工作；这两个里程碑期间未提交、推送或部署。

## 2026-10-31: Local documentation commit authorized / 文档本地提交授权

The user has now authorized a local commit of the completed documentation changes, following
Conventional Commits and DCO sign-off, without pushing. The commit scope is the reviewed public
documentation, CODEOWNERS comments, PR template and this progress record. Existing unrelated changes,
internal analysis documents and AGENTS.md are excluded. The subsequently authorized Makefile fix,
validation, progress updates and component-naming audit must remain uncommitted and unpushed.
用户现已授权按 Conventional Commits 与 DCO 签署规范在本地提交已完成的文档，不推送。
提交范围为已审查的公开文档、CODEOWNERS 注释、PR 模板和本进度；不包含已有无关改动、内部分析文档或 AGENTS.md。
后续获授权的 Makefile 修复、验证、进度更新及组件命名审查仍需保持未提交、未推送。

## 2026-10-31: Local commits, Makefile fix and naming audit / 本地提交、Makefile 修复与命名审查

### Authorization and integration / 授权与同步

- Created local documentation commit `899deca`, `docs: reconcile contribution policies and correct usage guides`, with the configured author identity and DCO sign-off. It contains only the reviewed 20-file documentation/template scope; unrelated changes were excluded. It was not pushed.
  已按当前 Git 身份与 DCO 签署创建本地文档提交 `899deca`，仅包含已审查的 20 份文档/模板文件，排除无关改动；未推送。
- Fetched `origin/intranet-sync` at `273fcea`. Its one-line package-directory change would conflict with both existing package producers, so integration paused until the user explicitly approved merging and aligning Makefile, CodeArts and bilingual guides to `route-server`.
  fetch 获取 `origin/intranet-sync` 的 `273fcea`。其包目录单行变更会与两套现有打包流程不匹配，因此先暂停合入；用户随后明确批准合入并将 Makefile、CodeArts 和双语指南同步到 `route-server`。
- Merged upstream into local `dev` as `5f55114`, `chore(sync): merge origin/intranet-sync into dev`, with DCO sign-off. The merge contains only the upstream deployment change and preserves local documentation history. Pre-merge working files were checked by hash and preserved. No rebase, reset, force-push or push was performed.
  已通过带 DCO 签署的本地 merge `5f55114` 将上游合入 `dev`；该 merge 仅包含上游部署变更并保留文档历史。合入前工作文件经哈希比对保持不变；未执行 rebase、reset、force-push 或 push。
- The new fixes, regression script, audit and progress updates described below remain uncommitted and unpushed.
  下述新修复、回归脚本、审查与进度更新均保持未提交、未推送。

### Changes and acceptance / 变更与验收

- Limited `/bin/bash` to `test`, `test-unit` and `test-coverage` and enabled `pipefail` for each Go/tee pipeline. A failing Go command or tee now fails Make; the existing arguments and console/file output remain. Coverage success is not announced after a failed pipeline. Other targets keep their existing shell.
  仅为三个测试目标指定 `/bin/bash`，并为 Go/tee 管道启用 `pipefail`。Go 命令或 tee 失败都会使 Make 失败；保留原参数和终端/文件输出，失败后不再打印覆盖率成功提示；其他目标保留原 shell。
- Added `.codearts/test_makefile.py` using Python's standard library. It covers success, Go failure, tee failure, combined failures and header-log failure across all three targets; real Go success/failure cases run in a temporary module with downloads disabled. Fixture build directories are separate from target names so repeated invocations cannot be mistaken for up-to-date file targets.
  新增 Python 标准库回归脚本，覆盖三个目标的成功、Go 失败、tee 失败、两者失败及初始日志失败；真实 Go 正反例在禁止下载的临时模块中运行。测试构建目录与目标名分开，避免重复调用被误认为文件目标已是最新。
- Aligned package directories and ZIP naming in Makefile, CodeArts and public bilingual guides with the imported deploy script's `route-server` directory; corrected the deployment help's log path. Module/import/linker names are unchanged. Package layout checks exercise both ZIP and tar.gz producers using build fixtures, without compiling or deploying RouteServer.
  将 Makefile、CodeArts 与公开双语指南中的包目录和 ZIP 命名对齐到已导入部署脚本的 `route-server`，修正部署帮助中的日志路径。未改模块/import/链接名称。使用构建替身验证 ZIP 和 tar.gz 布局，不编译或部署 RouteServer。
- Updated bilingual build/contribution/readme/branching guidance and the PR template to describe the fixed exit handling and Bash requirement, while retaining the E2E short-mode limitation.
  同步双语构建、贡献、README、分支说明及 PR 模板，说明退出处理已修复及 Bash 要求，保留 E2E short 行为限制说明。
- Recorded the component-name inventory, compatibility boundaries and old-installation migration risk in the local `implementation-audit.md`. This separate naming audit is outside the subsequent build-fix commit. Only package naming was authorized for alignment; other proposed naming changes remain unapplied.
  命名清单、兼容边界及旧安装迁移风险已记录于本地 `implementation-audit.md`，该独立命名审查不纳入后续构建修复提交；仅包名对齐获授权，其他命名建议尚未实施。

### Validation / 验证记录

- Before changing Makefile: `python3 .codearts/test_makefile.py MakefileTests.test_go_failure_fails_make_and_keeps_logs` exited 1 with failures for all three targets, reproducing a failing Go command incorrectly returning Make success.
  修改前单项回归退出 1，三个目标均失败，复现 Go 命令失败却被 Make 判定成功的问题。
- After the fix: `python3 .codearts/test_makefile.py` passed all 7 tests, including all three targets' real Go success/failure cases and both fixture-built archive layouts. Environment: GNU Make 3.81, Bash 3.2.57, Go 1.27.1, macOS arm64. Make 3.81 is the installed local version, below the guide's 3.82 minimum; other runner versions have not been tested here.
  修复后 7 项回归全部通过，包含三个目标的真实 Go 正反例和两套替身归档布局。环境为 macOS arm64、GNU Make 3.81、Bash 3.2.57、Go 1.27.1；本机 Make 低于指南最低 3.82，其他 runner 版本未在此验证。
- `bash -n .codearts/build.sh deploy/deploy.sh` passed. Full project Go tests, vet, race tests, actual RouteServer compilation and real deployment were not run; passing fixtures do not establish these results.
  shell 语法检查通过。未执行项目完整 Go 测试、vet、竞态测试、真实 RouteServer 编译或部署；替身通过不代表这些检查通过。
- `python3 /private/tmp/routeserver-current-work-check.py` passed: 50 Markdown files, 446 local links/anchors and 23 bilingual pairs checked; changed code/commands match in both languages. Existing deployment-guide whitespace was preserved, with no new trailing whitespace. `git diff --check` passed.
  最终文档检查通过：50 份 Markdown、446 个本地链接/锚点及 23 组双语结构有效，改动的代码/命令中英文一致。保留部署指南既有空白格式，未新增尾随空白；`git diff --check` 通过。
- The same check independently reproduced the committed-baseline naming counts and confirmed that dev contains `273fcea`, HEAD remains `5f55114`, DCO trailers are present in both local commits, the index is empty, module/runtime source files were not edited, and the pre-existing `.gitignore` change is unchanged.
  同一检查独立复核已提交基线的命名数量，并确认 dev 已包含 `273fcea`、HEAD 仍为 `5f55114`、两份本地提交均有 DCO、暂存区为空、模块/运行源码未修改，既有 `.gitignore` 改动未变。

### Remaining scope / 剩余范围

- No E2E selection change, module rename, runtime identity rename or automatic migration of existing installations is included. Existing `DistributedScheduler` packages/processes need an upgrade plan; the new deploy path does not automatically stop an old-path process.
  未改变 E2E 选择、模块名或运行身份，也未自动迁移既有安装。旧 `DistributedScheduler` 包/进程仍需升级方案，新部署路径不会自动停止旧路径进程。
- Real platform gates, contacts and deployment compatibility still need external verification. The pre-existing `.gitignore` edit and untracked internal materials are preserved.
  真实平台门禁、联系人和部署兼容仍需外部验证；保留既有 `.gitignore` 改动及未跟踪内部材料。

## Latest status / 最新状态

Documentation and the approved upstream merge are committed locally (`899deca`, `5f55114`) and not
pushed. Makefile/package fixes and the naming audit are complete in the working tree and their scoped
validation has passed. On the user's latest instruction, the verified Makefile/package changes and
their regression tests, documentation and progress record are now authorized for a local signed-off
commit, without pushing. The naming audit and module-migration proposal remain outside that commit;
module renaming is not authorized. Project-wide tests, actual build/deployment and old-installation
migration remain outside this completed milestone's verification.
文档与获授权的上游 merge 已在本地提交，均未推送。Makefile/包目录修复及命名审查已在工作区完成并通过相应验证；
按用户最新指令，已验证的 Makefile/包目录修复及其回归测试、文档和进度记录现获授权按规范本地提交，不推送。
独立命名审查及模块迁移方案不纳入该提交，模块重命名尚未授权。项目全量测试、真实构建/部署及旧安装迁移未纳入本次验证。
