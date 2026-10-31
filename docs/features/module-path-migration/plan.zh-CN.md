# Go 模块路径迁移方案

[English](plan.md) | [简体中文](plan.zh-CN.md)

状态：**已实施，2026-10-31 获准本地提交，不推送**。基线：`f7e8185`。
已验证的 Makefile/包目录修复已在该基线本地提交，未推送。

## 1. 目标与边界

将主模块名由 `infer-dp-scheduler` 迁移为用户指定的 `route-server`，在一次原子改动中同步内部 import、
字符串形式的构建入口及链接注入目标。

保留 `cmd/route_server`、`pkg/route-server` 等源码目录和 `package route_server` 等包声明。
保留已修正为 `route-server` 的二进制/包目录。HTTP 路由、JSON/YAML 字段、环境变量、指标、错误码、OTEL 服务身份、
产品名及版权头不属于本次迁移；旧安装目录升级另行处理。

`route-server` 仍是非 URL 的自包含模块路径，不会因此成为可通过网络 `go get` 的地址。
Go 官方规范区分此类路径与可下载依赖路径，后者的首段需要包含点号。
[Go 模块路径规范](https://go.dev/ref/mod#module-path)

## 2. 已核实影响范围

基于 `f7e8185` 的已跟踪源码扫描，共发现 **162 个不同文件、694 处旧名称**。
其中 678 处是 import 声明，其余 16 处见下表；不计未跟踪的审查/方案文档。
若分支在实施前继续变化，需要重新统计。

| 范围 | 当前数量 | 必须同步的变更 |
| --- | --- | --- |
| Go import | 157 个文件、678 条声明：`cmd/` 15 个、`pkg/` 122 个、`e2etest/` 20 个；生产文件 101 个、测试文件 56 个 | 仅替换 import 的旧模块前缀，保留别名、声明和业务实现 |
| [go.mod](../../../go.mod) | 1 条模块声明 | 改为 `module route-server`，保留 Go 版本及依赖版本 |
| [E2E 套件入口](../../../e2etest/route-server/rs_suit_test.go) | 3 个 `gexec.Build` 参数，该文件已包含在上述 157 个文件中 | 改为 `route-server/cmd/route_server`、`route-server/cmd/mock_server`、`route-server/cmd/config-tool` |
| [Makefile](../../../Makefile) | 4 个 `-X` 符号 | 修改 `version`、`tagOrBranch`、`commitId`、`buildTime` 的包前缀 |
| [CodeArts 构建](../../../.codearts/build.sh) | 同样的 4 个 `-X` 符号 | 同步修改包前缀 |
| [贡献指南](../../../CONTRIBUTING.md)及[中文版](../../../CONTRIBUTING.zh-CN.md) | 模块及 import 示例共 4 处 | 更新当前使用的模块路径 |

仓库有 1 个已跟踪的 `go.mod`，没有已跟踪的 `go.work` 或 vendor 目录，也没有现成的模块别名 `replace`。
新增验证代码与发版说明不包含在上述存量文件数量中。

链接目标将变为 `route-server/cmd/route_server/server.<变量>`。Go 的 `-X` 按 import 路径及变量名指定字符串变量，
因此必须与迁移后的代码一致。[Go 链接器文档](https://pkg.go.dev/cmd/link)

## 3. 实施前的兼容决策

- 对使用旧前缀 import 的消费者，应按源码级破坏性变更处理。核实内网仓、fork、本地 `replace`/workspace 使用方及平台构建任务，
  记录确认人和结果；仅凭当前 checkout 无法确认它们存在或不存在。
- 默认建议：仓内一次迁移，协调下游同步 import，不增加兼容模块；不通过自动添加指向自身的替换规则来保留旧 import。
  若存在必须继续使用旧路径的下游，应在实施前停止并单独批准兼容设计。
- `replace` 是主模块中的解析控制，不会统一改写源码 import，也不是所有下游都会继承的路径别名。
  依赖自身的替换规则不会自动配置其消费者。[Go 替换规则](https://go.dev/ref/mod#go-mod-file-replace)
- 已有二进制不会被改写；新构建的栈、符号和 profile 可能出现新包路径，需检查按路径过滤的工具。
  公开运行协议和配置身份预期保持稳定，仍需明确验证。
- 安排现有 Linux amd64/arm64 构建 runner 及依赖缓存；本方案不表示平台验证或下游迁移已经完成。

## 4. 建议实施顺序

1. **基线与批准门禁。** 明确获准实施并确认上述下游处理方式；记录准确提交、Go/Make/Bash 版本、包图及基线测试结果。
   将声明的 Go 1.25 工具链与现行 CI 工具链作为分别记录的环境，不顺便提高模块 Go 版本要求。
2. **原子迁移源码。** 后续实施时先执行 `go mod edit -module route-server`，使用 Go AST 只修改
   `ImportSpec.Value` 中等于 `infer-dp-scheduler` 或以 `infer-dp-scheduler/` 开头的路径，再格式化改动的 Go 文件。
   同一工作改动中更新 3 个 E2E 构建字符串和 8 个链接符号目标，不改目录或包标识符。
3. **文档与回归覆盖。** 同步双语贡献指南，为确认的下一版本补充双语发版说明，写清下游 import 迁移方式与破坏性变更。
   补充实际链接目标和元数据验证；现有模拟打包测试不能证明版本注入有效。保留 Make 失败回归检查。
4. **验证门禁。** 执行下方矩阵并与基线比较。先检查 `go mod tidy -diff`，不自动接受依赖变化；任何依赖或校验和变化均需调查。
   遇到意外协议/配置变化、新旧包路径混用或未解决的下游兼容问题时停止。
5. **评审与交付。** 在进度记录中列出实际 diff、命令、结果及剩余风险。验证后仍等待用户单独授权提交，不自动推送。
   后续提交标题可用 `fix(module)!: align module path with route-server`，并在 `BREAKING CHANGE:` footer 中说明 import 迁移。
   按现有两人及领域 owner 要求评审。

以上是已批准的实施顺序。仓内迁移现已获授权；实际结果及剩余外部门禁见 [progress.md](progress.md)。
用户随后已授权本地提交，仍不授权推送。

## 5. 验证矩阵与标准

以下命令定义验证要求，已执行的命令和结果以进度记录为准；此矩阵不代表每项检查都已通过。
涉及清理构建输出的操作仅在可丢弃的验证 checkout 中执行。

| 门禁 | 拟执行检查 | 验收标准 |
| --- | --- | --- |
| 模块身份 | `go list -m -f '{{.Path}}'`，检查 `go.mod` | 精确为 `route-server`，Go/依赖版本未变 |
| 活跃路径完整性 | AST 检查 Go import，检查 E2E 构建字符串及两套链接目标 | 无旧活跃前缀；历史审查及迁移解释允许保留旧名 |
| 包与依赖图 | `go list -mod=readonly ./...`、`go list -mod=readonly -deps -test ./...` | 前缀归一化后的仓内包集合不变，无新旧身份混用或意外依赖变化 |
| 格式与编译链接 | 改动文件的 `gofmt -l`、`git diff --check`、`go test -mod=readonly ./... -run '^$' -count=1` | 格式无误，所有包及测试二进制均可加载、链接 |
| Go 回归 | `go test -mod=readonly ./cmd/... ./pkg/... -count=1 -timeout 60m`；`go vet -mod=readonly ./...` | 在记录的基线工具链和设置下通过 |
| 竞态覆盖 | `go test -mod=readonly -race ./cmd/... ./pkg/... -count=1 -timeout 60m` | 在支持的原生 runner 上通过，记录架构/工具链及既有失败 |
| E2E 启动及行为 | `ROUTER_TEST_ENABLE_SU=false go test -mod=readonly ./e2etest/route-server/ -v -count=1 -timeout 60m` | 三个新 `gexec.Build` 入口正常，普通非 root 套件通过，特权 iptables 用例如实记录为跳过 |
| 既有构建回归 | `python3 .codearts/test_makefile.py` | Go/tee 失败、参数及归档布局回归仍通过 |
| 真实生产流程 | 全新 `make package`，Linux amd64/arm64 原生 CodeArts 构建 | 真实二进制与归档保留既有 `route-server` 布局，不以模拟构建替代 |
| 运行与版本元数据 | 下述真实制品流程 | 四项注入字段与构建输入一致，稳定身份/协议字段未变 |
| 依赖稳定性 | `go mod tidy -diff`，检查 `go.mod`/`go.sum` | 无未解释的依赖图/校验和变化，不引入指向自身的别名 |

`-short` 当前不会排除项目 E2E，因此单测和竞态阶段显式选择 `./cmd/... ./pkg/...`。
测试使用 gomonkey，需先确认选定编译器/架构下的基线行为；若基线已失败或需要特殊编译参数，应记录准确失败/参数，
必要时另行批准修复，不以静默跳过测试将阻塞检查描述为通过。

### 真实制品版本检查

- 在可丢弃 checkout 的空输出目录中构建。Make 使用不同的哨兵值覆盖 `VERSION`、`BRANCH`、`COMMIT` 和 `BUILD_TIME`，
  通过实际构建目标生成二进制。
- CodeArts 在原生 Linux runner 构建，记录其 checkout/时间产生的输入，与 `version.info`、制品版本和构建日志对照。
- 使用现有本地 E2E fixture 在 loopback 启动各真实制品，请求现有 `GET /`，断言 `version`、`tag_or_branch`、
  `commit_id`、`build_time` 与实际输入逐项相等；不接受默认 `Unknown` 或仅二进制/ZIP 存在作为证据。
- 必要时为已有 `printVersion` 响应补充定向测试，必须验证注入值，不能在测试里直接给生产变量赋预期值。
  不新增仅供测试的生产端点或 CLI 开关。
- 用 `go version -m` 检查真实二进制的新模块身份；验证结束后关闭临时 server 并释放端口。
  与基线 fixture 比较 API/配置键、错误前缀、指标名和追踪身份。

## 6. 验收、回退与当前状态

所有关联路径同步修改、验证矩阵通过或明确记录的基线阻塞得到解决，并确认下游处理方式后，才能验收迁移。
运行身份仍为 `RouteServer`，包/二进制仍为 `route-server`，`route_server` 等 Go 标识符语法保持不变。

未来实施若在提交前失败，先保留无关工作，再只撤销隔离的迁移改动；若之后获准提交而需要回退，revert 该独立提交并重跑门禁，
不 reset 共享历史，也不恢复旧部署目录名。模块前缀回退不能解决旧安装迁移问题。

当前已在工作区迁移模块、import、E2E 入口和链接前缀，依赖版本及运行协议未变。
基线测试失败、竞态报告及 Linux 原生验证限制见 [progress.md](progress.md)，不代表发布门禁已全绿。
此前[命名审查](../documentation-review/implementation-audit.md)保留历史基线；用户已授权将本次实施及配套记录独立本地提交，
不授权推送或发布。
