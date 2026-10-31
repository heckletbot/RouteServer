# 构建指南

[English](build.md) | [简体中文](build.zh-CN.md)

本文档说明如何使用提供的 `Makefile` 从源码构建 RouteServer。

## 前置条件

请确保构建机器上已安装以下工具：

| 工具 | 最低版本 | 用途                          |
|------|-----------------|----------------------------------|
| Go   | 1.25.0          | 编译 Go 源码           |
| Make | 3.82            | 构建编排              |
| Bash | 3.2             | `/bin/bash` 为 Make 测试目标提供 `pipefail` |

> [!NOTE]
> 构建流程支持 Linux、macOS 和 Windows（WSL）。部署包（`deploy.sh`）面向 Linux 环境。

验证安装情况：

```bash
go version
make --version
```

---

## 快速开始

克隆仓库，用一条命令完成完整构建：

```bash
git clone <repo-url> && cd <repo-dir>
make clean && make package
```

这条命令依次执行以下步骤：

1. **清理**所有先前的构建产物（`build/` 和 `output/` 目录）
2. **构建**主服务二进制（`route-server`），源码位于 `cmd/route_server/`
3. **构建**配置生成工具（`config_generator`），源码位于 `cmd/config-tool/`
4. **复制**部署脚本、YAML 模板、自动扩缩容脚本和流水线配置到打包目录
5. **打包**全部内容到 `output/route-server-<version>-<timestamp>.zip`，可直接用于部署

构建完成后，可部署的 zip 位于：

```
output/route-server-<version>-<timestamp>.zip
```

部署方式参见[部署指南](./deploy.zh-CN.md)。

---

## Make 目标

| 目标 | 说明 |
|--------|-------------|
| `build` | 编译 `route-server` 和 `config_generator` |
| `build-binary` | 仅编译 `route-server` |
| `build-config-tool` | 仅编译 `config_generator` |
| `package` | 构建 + 复制部署资源 + 生成 `version.info` + 创建 `.zip` 压缩包 |
| `test` | 运行全部测试，日志写入 `build/test.log` |
| `test-coverage` | 运行全部测试并统计覆盖率，报告写入 `build/coverage.out` |
| `test-unit` | 以 `-short` 运行所有包（当前仍包含 E2E），日志写入 `build/unit-test.log` |
| `clean` | 删除 `build/`、`output/` 以及散落的 `.zip` 文件 |
| `all` | `clean` + `build` |
| `fmt` | `go fmt ./...` |
| `vet` | `go vet ./...` |
| `lint` | `fmt` + `vet` |

> `make test`、`make test-unit` 和 `make test-coverage` 使用 Bash `pipefail`：`go test` 或 `tee` 任一失败
> 都会使 Make 失败，测试输出仍同时发送到终端和日志文件。
> E2E 套件没有检查 `testing.Short()`，所以 `-short` 当前不会排除该套件。

管道回归验证：在仓库根目录使用 Python 3 执行 `python3 .codearts/test_makefile.py`。
检查使用临时目录、受控的 Go/tee 失败，并在安装 Go 时于独立模块内运行真实的通过/失败测试；
禁止下载依赖，不运行项目 E2E。
同一命令还用构建替身检查 Make 与 CodeArts 的归档布局，不代表已验证 RouteServer 编译产物或真实部署。

模块路径或链接目标变更后，执行 `python3 .codearts/test_module_metadata.py`。
它从工作区源码的临时快照构建真实制品，检查 `go version -m` 的模块身份及已有 `GET /` 响应的全部字段，
需要 loopback 监听权限。在非 Linux 主机上，CodeArts 路径显式构建本机目标以验证链接/运行元数据；
Linux 发布制品仍需单独在原生 runner 验证。

### build

编译两个可执行文件：

- **`route-server`** — 主服务二进制，入口 `cmd/route_server/`
- **`config_generator`** — 配置生成工具，入口 `cmd/config-tool/`

```bash
make build
```

输出：

```
output/
├── route-server/
│   └── bin/route-server                 # Server binary
└── config_generator                     # Config tool
```

版本注入：在 git 仓库内构建时，`route-server` 二进制会通过 ldflags 嵌入元数据：

| 字段 | 来源 |
|-------|--------|
| `version` | `git describe --tags --always --dirty` |
| `tagOrBranch` | `git rev-parse --abbrev-ref HEAD` |
| `commitId` | `git rev-parse HEAD` |
| `buildTime` | UTC 时间戳（`YYYYMMDDhhmmss`） |

若 git 不可用，这些字段默认为 `"unknown"`。

### build-binary

仅构建 `route-server`。在服务端开发中可获得更快的反馈。

```bash
make build-binary
```

输出：`output/route-server/bin/route-server`

### build-config-tool

仅构建 `config_generator`。适合调试配置生成逻辑时使用。

```bash
make build-config-tool
```

输出：`output/config_generator`

### package

构建两个二进制、复制部署资源、生成 `version.info`，并创建 `.zip` 压缩包。

```bash
make package
```

输出：`output/route-server-<version>-<timestamp>.zip`

`output/route-server/` 下的 `version.info` 记录构建元数据（构建时从 git 读取）：

```
Build Tag/Branch: <branch>
Build Commit ID: <commit>
Build Time: <timestamp>
```

包内容：

```
<package-root>/
├── config_generator
├── deploy.sh
├── *.yaml                              # All YAML templates from deploy/
├── scale/
├── pipelines/
└── route-server/
    ├── bin/route-server                # Server binary
    └── version.info                    # Build metadata
```

`deploy.sh` 要求严格符合这一布局：它在 `<package-root>/route-server/` 下查找可运行的包，并在自身同级查找 `config_generator`、`*.yaml`、`scale/` 和 `pipelines/`。

### CodeArts 构建

华为云 CodeArts 构建任务会执行 [`.codearts/build.sh`](../.codearts/build.sh)，并发布 `route-server-<version>-<timestamp>-linux-<arch>.tar.gz`。其内部布局与上文 `make package` 的输出一致（资源位于压缩包根目录，可运行的包位于 `route-server/` 下），因此在 `tar -xzf` 解压后同样适用 `deploy.sh` 流程。

---

## 使用

RouteServer 开发与发布的推荐流程：

```bash
# 1. Format and check code quality
make lint

# 2. Test a specific package for quick feedback (example)
go test ./pkg/common/scheduler/... -v

# 3. Run full test suite before committing
go test ./... -v -timeout 60m

# 4. Build and package for deployment
make clean && make package
```

---

## 故障排查

| 问题 | 可能原因 | 解决方式 |
|-------|--------------|----------|
| `go: command not found` | 未安装 Go | 安装 Go 1.25.0 或更高版本 |
| `make: command not found` | 未安装 Make | 通过包管理器安装 make 3.82+ |
| `fatal: not a git repository` | 构建在 git 克隆之外运行 | 先克隆仓库，否则版本字段将默认为 `"unknown"` |
| `fatal: path '...': permission denied` | 文件权限问题 | 检查文件属主；确保对项目目录有读写权限 |
| `Package zip is empty` | 二进制静默构建失败 | 先单独运行 `make build`，检查输出中的错误 |
| `undefined: ...` / `cannot find package` | 依赖缺失或过期 | 运行 `go mod tidy` 同步 `go.sum`，然后重试 `make build` |
| 构建成功但 `route-server` 启动即崩溃 | 配置文件格式不匹配 | 确认配置与二进制版本匹配；检查日志输出中的解析错误 |
| 构建后 `output/` 目录缺失 | 因先前错误导致构建被跳过 | 检查终端输出中的错误信息；运行 `make clean && make build` 以隔离出错的步骤 |
