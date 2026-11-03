# Rebase 后验证进度

[English](progress.md) | [简体中文](progress.zh-CN.md)

## 2026-11-03：授权范围与环境

用户要求审查并实际运行 Make/deploy.sh/mock_vllm，以及全量 E2E；随后明确在本机 macOS 验证并排除 ConfigMap 功能。
基线为 `1333071` / 上游 `6e10898`。不修复生产实现、不实现 skill、不提交、不推送，保护既有工作文件。

环境为 macOS arm64、缓存的官方 Go 1.26.8、Make 3.81、Bash 3.2.57。采用 300 个已跟踪工作区文件的快照，
设置 GOTOOLCHAIN=local、GOWORK=off、GOPROXY=off、GOSUMDB=off，隔离缓存和临时目录。
无需下载依赖/工具链或安装 Linux 运行时。完整 E2E 非 root，ROUTER_TEST_ENABLE_SU=false，本机不具备特权 iptables 用例环境。

## 实际变更

仅新增本双语进度及[审查](implementation-audit.zh-CN.md)。不编辑既有源码、部署脚本/配置、正式指南及未提交 skill 文档。
临时复现/检查工具和本轮进程的安装位于仓库外。

## 已执行检查与结果

| 检查 | 结果 |
| --- | --- |
| 隔离快照内 `make clean` | 通过，0.2s；未清理用户原有产物 |
| 传入原仓 Git 元数据的真实 `make package` | 通过，7.68s；实际解压 ZIP 并检查布局/资源 |
| `go build -mod=readonly -o <validation-dir>/mock_vllm ./cmd/mock_server` | 通过，0.5s |
| 文档 deploy.sh PD/Hybrid 路径 | 均退出 0，版本、就绪、状态及流式/非流式 mock chat 通过；保留 macOS nproc/sed 错误，不计为 Linux 部署验收 |
| Mock 后端日志 | Prefill、Decode、Hybrid 各收到两次 chat，四个客户端请求返回预期 JSON/SSE |
| 缺失证书 / 错误主机反例 | 均退出 1，确认拒绝前已写安装/pipeline，生成器失败后仍尝试启动 |
| 将本轮 Decode mock 标为不健康后的 `/status` 与就绪 | 复现 HTTP 200、`status: fault`，就绪 HTTP 500 / `is_ready: false`，指南仅看 HTTP 码的成功判据不足 |
| `bash deploy.sh --help` / 等号选项形式 | Help 退出 0；`--client-https-enable=true` 作为未知参数退出 1，复现文档命令不匹配 |
| `go vet -mod=readonly ./...` | 21 条锁拷贝诊断失败，同一 Go 1.26.8 在 rebase 前 b2c2841 完全复现，不计为通过 |
| 完整 `go test -mod=readonly ./e2etest/route-server/ -v -count=1 -timeout 60m` | 总耗时 1,608.01s、退出 0；套件 123 通过、0 失败、0 待执行、3 个特权 iptables 用例跳过，三个构建入口及清理阶段通过 |
| 非 root E2E 清理限制 | BeforeEach 仍尝试 sudo iptables 清理并忽略错误，不声称成功修改防火墙或覆盖特权网络用例 |
| 文档/范围/进程清理检查 | 81 份 Markdown / 689 个本地链接无缺失目标，两组新双语对齐；既有部署指南标题差异仍保留为发现。340 个初始文件哈希、HEAD、暂存区不变；gexec 制品目录移除，无本轮残留进程或冒烟监听端口 |

初次冒烟断言错误来自误以为非流式 mock 使用流式文本，仅修正临时检查器后在新目录重跑，两种模式通过。
收窄范围前完成的局部 ConfigMap 包/配置探测保留临时日志，排除在功能验收之外，后续不再验证 ConfigMap。

## 证据与剩余限制

本地 `/private/tmp/routeserver-post-rebase-validation.json` 指向隔离源码、日志、实际 ZIP、临时安装及 JSON 结果。
不声称全仓单测/竞态、Linux 原生、真实模型、生产 TLS 或 Kubernetes/ConfigMap 验收通过。
已检查并保留既有无关文件及 Git/暂存状态。V-01 至 V-08 作为另行获准修正里程碑的发现，不在本任务悄然修复。

## 完成状态

已完成获准的 macOS 审查、实际 Make/deploy/mock 检查及完整非 root E2E。
本轮 singleton PD/Hybrid 路径和 E2E 在所述边界内通过；代码/文档差异、部署失败顺序、平台/清理错误被忽略及基线 vet 问题仍开放，
不等于全仓全绿或生产认证。仅新增四个未跟踪报告 Markdown，本任务未编辑既有文件，未暂存、提交或推送。

## 2026-11-03：获准的文档与部署修复

### 决策与范围

用户批准修正文档不一致与 deploy.sh 错误处理、补充回归并同步中英文。HTTPS 默认关闭，
只在文档中建议生产显式开启；ConfigMap 功能、既有 vet 问题及 V-08 特权 E2E 清理留待后续。
本里程碑不实现 skill、不部署生产、不暂存、不提交、不推送。保留前述审查和完整 E2E 作为修复前证据。

### 实际修改与验收项

- V-01：README 及构建/贡献指南中的最低 Go 版本对齐 `go.mod` 的 1.26.0；
  CodeArts 最低版本判断及下载回退同步到 1.26，并增加拒绝旧工具链的回归。
- V-02：deploy.sh 的四个 HTTPS 默认值保持 false；原始发布模板的业务、运维、vLLM、etcd、peer
  HTTPS 也统一为 false，避免直接生成配置时仍启用 TLS。mTLS 开关保留，仅在对应 HTTPS 开启后生效。
  配置生成器仅修正四项帮助说明，TLS 实现不变；双语配置参考与指南同步默认值及生产建议。
- V-03、V-06：使用私有临时目录准备资源并调用原配置生成器；复制、sed、生成及进程枚举失败立即退出。
  服务端必需证书及启用平面所填文件的可读性先检查；生成成功却没有新配置也拒绝，不使用包内旧配置兜底。
  准备阶段失败不写安装、不停旧实例、不启动新实例，临时目录自动清理。
- 生成完成后保留旧配置 `.bak`；按最终可执行路径匹配旧实例，拒绝空 PID 调用，停止失败或 10 秒内未退出则终止安装，
  不强杀。CPU 读取改用 getconf，兼容 GNU/BSD sed，保持最终日志路径及 daemon/子进程退出码语义。
- V-04、V-05：双语安全示例改用独立参数值、worker candidate=false；生产示例显式开启相应 TLS，
  etcd 示例使用 etcd 模式和端点；仅指定证书不等于启用 HTTPS。
- V-07：双语部署验收要求检查 `/status` 内容、工作节点就绪及有超时的真实模型请求，
  不再把 HTTP 200 或启动 PID 消息视为推理验收通过。

### 本轮回归与结果

使用相同 macOS / Go 1.26.8，本轮源码快照在仓库外构建与安装；无真实模型或集群连接。

| 命令/检查 | 结果 |
| --- | --- |
| `bash -n deploy/deploy.sh`、`bash -n .codearts/build.sh` | 通过 |
| `python3 .codearts/test_deploy.py -v` | 14 项通过：准备失败不替换、缺配置、证书、复制/sed、CPU、进程枚举、停止失败/超时、精确进程选择、pipeline/指标改写、备份、默认 HTTP、启动失败及 daemon 退出码 |
| `python3 .codearts/test_makefile.py -v` | 8 项通过：原退出码/日志/打包测试及新增 Go 1.25 拒绝与 1.26 回退 URL 检查；下载由 fixture 截断，无网络安装 |
| `go test -mod=readonly ./cmd/config-tool -run 'Test_(shippedTemplateHTTPSDefaults\|applyTLSConfig\|applyOpsTLSConfig\|applyClientTLSConfig\|applyEtcdTLSConfig\|deployDefaultBehavior)$' -count=1 -v` | 6 个顶层测试及其子例通过；实际发布模板无 TLS 参数生成结果的五处 HTTPS 均为 false，不运行 ConfigMap 测试 |
| 隔离快照内 `make package VERSION=v0.1.0-alpha.1-16-g1333071-dirty BRANCH=dev COMMIT=13330714065d2e4be5e291e2b5eec636f1ddf4a1` | 通过，重新打包最终模板/脚本/生成器，实际解压验证 |
| 最终包 `bash deploy.sh` singleton PD/Hybrid + mock_vllm，不传 HTTPS 开关 | 均退出 0；版本、就绪、后端状态、流式/非流式四个推理请求通过，无 nproc/sed/空 kill 错误，日志指向最终安装目录 |
| 真实缺失业务证书 / 错误 host 反例 | 均非零退出，不创建实例安装目录，不复制二进制/pipeline，不尝试启动 |
| 本轮临时 PD 实例重复部署 | 通过：旧实例被识别并正常停止，`.bak` 保留，新实例就绪且两种请求通过；该检查使用模板对齐前、脚本相同的首次修复包 |
| 文档/范围/清理检查 | 81 份 Markdown / 697 个本地链接有效，安全示例双语代码块一致，审查/进度标题及发现编号对齐；322 个原有非本轮文件、HEAD 和暂存区保持不变，本轮服务与 mock 全部清理 |

新增模板生成测试最初漏建生成器要求的目标 config 目录，修正测试夹具后通过；未放宽生产校验。
完整 E2E 的 123 通过 / 3 跳过是本页前一轮修复前结果，本轮没有重跑整套约 27 分钟的 E2E；
本轮以部署脚本回归、TLS 配置测试和真实打包/重部署/推理覆盖变更，不把历史结果冒充修复后全量运行。

### 完成与剩余边界

V-01 至 V-07 已按本轮范围修正并完成上述回归；V-08 及既有 21 条 vet 锁拷贝诊断仍开放，
未修改或重新宣称通过。ConfigMap 实现、清单及专属复制分支保持原样，不部署或验收 Kubernetes/ConfigMap。
Linux 原生、真实模型和生产 TLS 握手未验收。准备成功后停止旧实例及安装阶段仍非原子事务，不提供自动回滚；
证书可读性不代表证书链、身份和口令已通过运行时校验。

本轮日志和结果由本地 `/private/tmp/routeserver-deploy-fix.json` 定位，保留最终构建、回归、冒烟及反例记录。
所有修改均留在工作区，HEAD 和暂存区不变，未提交、未推送。

## 2026-11-03：提交前全量 E2E 复验

用户追加授权：对修复后的当前源码重新运行完整 E2E，通过后按项目规范提交本轮修复，仅本地提交、不推送。
ConfigMap 功能与既有 vet 问题继续排除；上一节的“不提交”及“未重跑全量 E2E”是前一阶段状态，由本节更新。

### 执行与结果

使用当前工作区 301 个源码/回归文件的新临时快照，保留 macOS arm64、官方 Go 1.26.8、
离线依赖/工具链配置及 `ROUTER_TEST_ENABLE_SU=false`。未设置 focus/skip 筛选，选中全部 126 个 E2E 用例。
套件没有 ConfigMap 用例，不启动 Kubernetes 或验证 ConfigMap。

实际命令（JSON 报告写入本轮临时目录）：

```bash
go test -mod=readonly ./e2etest/route-server/ -v -count=1 -timeout 60m \
  -ginkgo.json-report=<validation-dir>/ginkgo-report.json
```

| 检查 | 结果 |
| --- | --- |
| 完整修复后 E2E | 退出 0，总耗时 1,621.20 秒；套件耗时 1,613.334 秒，随机种子 1793704385 |
| 用例结果 | 123 通过、0 失败、0 待执行、3 跳过；跳过项均为既有 iptables 特权网络用例 |
| 构建与收尾 | route-server、mock_vllm、config-tool 三个入口重新构建成功；SynchronizedAfterSuite 与 JSON 报告生成成功 |
| 清理与范围 | 无本轮残留进程，检查的 96 个测试端口均无监听；本轮独立临时运行目录已清理，日志与报告保留 |
| 测试对工作区的影响 | 测试期间 345 个初始文件、HEAD 和暂存区均未变化；仅随后更新本双语进度记录 |

V-08 既有非 root 清理仍会尝试 sudo iptables 并忽略错误，本轮不修改该行为，不把跳过或清理错误当作防火墙覆盖。
既有 21 条 vet 锁拷贝诊断未修改、未重跑，也不宣称通过。本次不是 Linux 原生、生产 TLS 或真实模型验收。

### 提交范围与证据

本轮提交限定 23 个文件：部署错误处理、默认关闭的通用 HTTPS 模板及帮助说明、构建/部署回归、
Go 版本对齐、双语公开指南和本双语审查/进度。打包测试夹具适配现有上游资源布局，不修改 ConfigMap 实现或清单。
其他既有修改（含 `.gitignore`、模块迁移旧进度）以及 skill 设计、内部草稿、图片和编辑器状态不纳入提交。

采用 Conventional Commits 标题 `fix(deploy): fail fast and align deployment guidance`，并使用当前仓库身份添加 DCO
`Signed-off-by`。本地 `/private/tmp/routeserver-e2e-precommit.json` 索引完整日志、JSON 报告、源码保全检查与清理结果。
本阶段只授权本地提交，未授权 push；不改写历史、不推送。
