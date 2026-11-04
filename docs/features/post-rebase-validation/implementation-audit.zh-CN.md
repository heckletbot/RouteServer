# Rebase 后代码与文档审查

[English](implementation-audit.md) | [简体中文](implementation-audit.zh-CN.md)

基线：`dev` 的 `1333071`，已包含 `origin/intranet-sync` 的 `6e10898`。核查日期：2026-11-03。
本轮属于审查/验证，不包含生产实现、正式指南修正、提交或推送；既有未提交工作不纳入本次修改。

后续状态：以下为修复前的基线审查，保留当时的复现证据。用户随后批准修正文档与部署错误处理，
并确认 HTTPS 默认关闭；实施范围、回归结果和仍开放的问题见[进度](progress.zh-CN.md)。

## 1. 范围与结论

用户要求检查代码/文档一致性、实际 Make 打包、按文档用 deploy.sh 启动并接入 mock_vllm，以及全量 E2E。
随后明确使用当前 macOS 主机并排除 ConfigMap 功能。不准备 Linux 环境，不声称 Kubernetes 部署或 ConfigMap 验收。
收窄范围之前已完成的 ConfigMap 局部检查仅保留临时证据，不作为本报告验收结果。

当前代码与正式文档**不完全一致**。文档中的 singleton PD/Hybrid 命令路径能启动实际 Make 制品并完成模拟后端推理，
但平台命令错误及失败顺序缺陷仍存在，不能称 deploy.sh 已具备快速失败，也不能据此声明 Linux/生产部署通过。
完整 E2E 结果另见[进度](progress.zh-CN.md)。

## 2. 发现

| ID / 优先级 | 证据 | 影响与建议后续 |
| --- | --- | --- |
| V-01 / P1 | [go.mod](../../../go.mod) 要求 Go 1.26.0，[构建指南](../../build.md)、[中文版](../../build.zh-CN.md)及两份贡献指南仍写 1.25.0；[CodeArts 引导](../../../.codearts/build.sh)接受 >=1.25，回退下载 1.25.0 | 文档/脚本准备的本地工具链低于模块要求，联网自动切换可能掩盖问题，本地/离线工具链不能依赖它；应对齐指南和经评审的引导要求，本轮使用缓存 Go 1.26.8 |
| V-02 / P1 | [部署指南](../../deploy.md)/[中文版](../../deploy.zh-CN.md)称四平面 HTTPS/mTLS 默认 true；[deploy.sh](../../../deploy/deploy.sh)的四个 HTTPS 默认 false，mTLS 默认 true | 用户获得错误的有效传输默认值，并被告知仅缺证书就会阻塞默认路径；需说明 HTTPS/mTLS 关系及显式安全/演示选择，不在审查中悄然撤销上游政策 |
| V-03 / P1 | 实际反例中，缺少主证书被拒绝前已复制二进制/pipeline；无效 `--host` 导致 config_generator 失败后，deploy.sh 仍尝试启动，最后退出 1 | 无效输入时可能已修改安装，配置生成失败未立即停止；需在另行获准修复中前移前置校验、显式处理生成/复制/启动失败；本次反例最终均非零退出，不能误报为假成功退出 |
| V-04 / P2 | [安全指南](../../security.md)/[中文版](../../security.zh-CN.md)使用 `--client-https-enable=true` / `--etcd-tls-enable=true`，实际 deploy.sh 以 `Unknown option` 拒绝等号形式，解析器要求参数和值分开 | 发布示例无法原样执行；同步双语为真实格式或另行批准修改解析器，不能混淆 Go flag 和 deploy.sh 的参数解析 |
| V-05 / P2 | 安全指南标为 worker 的示例传入 `--candidate true`，部署指南区分 worker=false 与 candidate=true | 命令允许参与选主，与说明的 worker 角色不符；需明确意图并同步双语示例，本轮不作角色政策选择或真实集群验证 |
| V-06 / P2 | 本机部署日志出现缺少 `nproc` 及 BSD/GNU `sed -i` 错误，脚本仍进入 PID 检查并返回 0 | 本机在准备命令失败后仍能处理本轮默认 mock 请求，说明命令失败未检查，不证明改写生效或所有模式支持 macOS；宣传可移植性前需明确前置条件/错误传播修复 |
| V-07 / P2 | Singleton 部署指南把 `/status` HTTP 200 当成成功；将本轮 Decode mock 标为不健康后，实际服务仍返回 200、响应体为 `status: fault`，就绪则返回 500 / `is_ready: false` | 传输状态不等于后端就绪或推理成功；应在双语验证步骤要求预期角色状态/就绪及有界请求，不提议改变状态码/协议 |
| V-08 / P2 | [worker-kill BeforeEach](../../../e2etest/route-server/worker_kill_test.go) 无条件调用 [UnblockTrafficToPort](../../../e2etest/route-server/distributed_utils_test.go)，即使 ROUTER_TEST_ENABLE_SU=false 也忽略错误，本机日志出现 sudo 密码/终端错误 | 非特权套件仍尝试特权清理；后续测试修复应加开关并跟踪本轮防火墙规则，跳过用例及忽略的清理错误不能算防火墙覆盖 |

P1 表示建议在宣传可靠部署引导前解决，P2 表示可执行的示例/健壮性问题。这些是审查建议，不构成新合并政策或部署行为修改授权。

## 3. 实际 Make 与部署验证

验证前把 300 个已跟踪工作区文件复制到可丢弃快照，在其中构建/测试，不清理用户 checkout 的既有产物。
`make clean` 及真实 `make package` 均成功。快照无 .git，版本/分支/提交变量从原仓读取后显式传给 Make，制品报告预期 `1333071` 提交。

实际 ZIP 解压到另一临时目录，包含二进制、配置生成器、YAML/pipeline 及新增 ConfigMap/RBAC 资源。
资源存在检查不验证 ConfigMap 功能。解压后的 deploy.sh 与源文件逐字节一致，未打补丁。

三个本机 mock_vllm 进程分别代表 Prefill、Decode、Hybrid，监听 loopback 的 61001/61002/61003。
按文档 singleton PD/Hybrid 命令，替换真实地址、临时 RS_INSTALL_DIR、显式业务/运维端口、四个 HTTPS=false 并开启 stdout。
未使用系统安装目录、真实模型端点、集群凭据或既有服务。

两种模式部署均退出 0，版本响应对应制品提交，就绪返回 `is_ready: true`，`/status` 后端为 running，
流式/非流式 chat 均返回 200 及预期 mock 文本。流式以 `[DONE]` 结束，非流式 usage 记录 4 个 completion token。
Prefill、Decode、Hybrid 日志分别记录两次对应 chat 请求，证明 PD 两阶段及 Hybrid 路由，而不只检查进程存活。

另在全新 PD 安装中，仅将本轮 Decode mock 标为不健康，复现 V-07：`/status` 仍是 HTTP 200、响应体 fault，
`/internal/worker/readiness` 正确返回 HTTP 500 / 未就绪。附加正常请求及故障转换单独记录于
`status-negative-final-result.json`，未覆盖主要 PD/Hybrid 结果。

初版临时检查器误以为 mock 非流式文本也带流式 token 后缀，按实际 mock 契约修正为 `test` 与 `test_0...` 后，
在新目录重跑，保留初次结果/日志。未为让测试通过而修改生产实现。
所有冒烟/反例进程均按本轮创建的进程组终止并等待回收。

## 4. 补充检查与限制

`go vet -mod=readonly ./...` 有 21 条锁值拷贝诊断；同一 Go 1.26.8 在归档的 rebase 前 `b2c2841` 上得到逐行相同的 21 条。
Vet 仍为失败，不作为新增 rebase 缺陷，不修复或豁免；不声称全仓单测/竞态通过。

原始 deploy.sh 的 `--help` 当前退出 0。此前 skill 审查的 help 不终止属于 `b2c2841` 旧基线，不能沿用为当前缺陷。
未提交的 skill 设计记录保持原样，本轮未改写。

文档扫描覆盖 81 份已跟踪/设计/报告 Markdown 及 689 个本地链接/锚点，无缺失目标。
既有 deploy.md/deploy.zh-CN.md 因英文多出部署内容而存在标题结构差异，识别翻译差异不代表验证被排除的 ConfigMap 功能。
新增报告双语结构对应。

完整非 root 套件还暴露 V-08：记录 `sudo iptables -D` 清理失败后仍继续。本机没有成功变更防火墙的证据，
不能把三个特权跳过或忽略清理错误解释为网络隔离模拟成功。

当前 Make 3.81 低于文档 3.82，Bash 为 3.2.57，主机 darwin/arm64。
构建成功是本轮证据，不改变支持矩阵。macOS 是用户指定环境；Linux、真实引擎/加速器、生产 TLS、特权 iptables 以及
ConfigMap/Kubernetes 集成不纳入本轮验收。

## 5. 证据与剩余工作

临时证据由 `/private/tmp/routeserver-post-rebase-validation.json` 索引，其验证目录包含 `run.json`、`logs/make-package.log`、
`doc-deploy-smoke-final-result.json`、`negative-deploy-result.json`、`cli-check-result.json`、`logs/vet.log`、
`logs/baseline-vet.log` 及完整 E2E 日志/结果。临时工具不是新增的受支持仓库命令。

V-01 至 V-08 需要另行批准修正并同步双语。Mock/E2E 通过不能消除文档不一致和部署失败顺序问题，
本轮验证不批准任何 skill 决策。最终测试总数、未执行覆盖及仓库状态检查见[进度](progress.zh-CN.md)。
