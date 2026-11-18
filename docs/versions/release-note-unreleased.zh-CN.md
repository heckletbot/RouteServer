# 未发布变更

[English](release-note-unreleased.md) | [简体中文](release-note-unreleased.zh-CN.md)

本文记录下一版本的源码变更，不代表已有版本发布。

## 破坏性源码变更：Go 模块路径

Go 模块路径由 `infer-dp-scheduler` 改为 `route-server`。引用项目包的消费者必须同步修改 Go import 的模块前缀，
并协调本地 `require`/`replace` 或 workspace 配置，例如：

```go
// Before
import "infer-dp-scheduler/pkg/common/config"
// After
import "route-server/pkg/common/config"
```

新名称仍是非 URL 的自包含模块路径，不是公开网络下载地址；没有提供旧路径兼容模块或指向自身的替换别名。
仓外下游消费者尚未完成核实。

源码目录、Go 包声明、HTTP 路由、配置键、环境变量、指标、错误前缀及 OTEL 服务身份保持不变。
包/可执行文件目录仍为 `route-server`，旧安装目录升级属于独立操作。

两套构建流程同步更新全部四项链接版本目标，E2E 入口同步更新三个 Go 构建路径；
真实制品元数据冒烟检查覆盖现有 `GET /` 响应。

## 验证状态

实际命令、基线失败及平台限制见[模块迁移进度](../features/module-path-migration/progress.md)。
迁移已获准本地提交，不授权推送或发布；本说明不代表所有发布平台门禁均已通过。
