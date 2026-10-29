# 部署指南

[English](deploy.md) | [简体中文](deploy.zh-CN.md)

本文档介绍各种部署方式。本文档中的所有部署示例均关闭了 HTTPS。

生产环境的安全部署请参考[安全参考](./security.zh-CN.md)。

> [!NOTE]
> **开始之前**：如果从源码构建，请先阅读[构建指南](./build.zh-CN.md)完成编译。若已有构建好的发布包（`make package` 产出的 `.zip`，或 CodeArts 构建产出的 `.tar.gz`），可直接跳到[安装流程](#安装流程)。

## 环境配置

通过环境变量配置推理实例的接口：

| 变量 | 说明                                                                                |
|----------|--------------------------------------------------------------------------------------------|
| `ENCODE_SERVERS` | encode api_server 的地址（每个 DP 一个）<br/> 格式：`"IP:PORT,IP:PORT,..."`   |
| `PREFILL_SERVERS` | prefill api_server 的地址（每个 DP 一个）<br/> 格式：`"IP:PORT,IP:PORT,..."`  |
| `DECODE_SERVERS` | decode api_server 的地址（每个 DP 一个）<br/> 格式：`"IP:PORT,IP:PORT,..."`   |

 可选地，可以用 `#` 在端口号之后指定 SLO 标签：
```
"<IP1>:<PORT1>#tag1,<IP2>:<PORT2>#tag2"
```

### 可选环境变量

| 变量 | 说明 | 默认值 |
|----------|-------------|---------------|
| `RS_INSTALL_DIR` | deploy.sh 脚本的安装目录 | `/opt/route_server` |

---

## 安装流程

两种发布产物的内部布局相同：部署资源（`deploy.sh`、`*.yaml`、
`config_generator`、`scale/`、`pipelines/`）位于压缩包根目录，可运行的程序包位于
`DistributedScheduler/` 下。

1. **解压程序包**
   ```bash
   # zip produced by `make package`
   unzip DistributedScheduler-*.zip -d route-server
   # tar.gz produced by the CodeArts build task
   tar -xzf route-server-*-linux-*.tar.gz -C route-server
   ```

2. **带上显式选项运行部署脚本**
   ```bash
   cd route-server
   bash deploy.sh --mode singleton --infer-mode PD --port 9800 --host <YOUR_IP> ...
   ```

   只执行 `bash deploy.sh` 是**不够**的：四个平面的 HTTPS 与 mTLS 默认都是 `true`，
   因此除非传入证书，或显式关闭不需要加固的平面，脚本会直接退出
   （见 [HTTPS 与 mTLS](#https-与-mtls)）。各拓扑的完整命令见[示例用法](#示例用法)。

> [!WARNING]
> 请用 `bash` 而非 `sh`：脚本依赖 `[[ ]]` 等 Bash 语法，在 `dash` 下会失败，
> 而多数 Linux 发行版的 `/bin/sh` 正是 `dash`。

---

## 部署脚本做了什么

部署脚本执行以下操作：

1. **复制**软件与配置文件到 `RS_INSTALL_DIR/<rs_name>/` 目录

2. **生成**RouteServer 配置文件（`route-server.yaml`），依据：
   - 模板 YAML 文件
   - 本机 IP 地址
   - 传给 deploy.sh 脚本的参数
   - 将配置保存到 `RS_INSTALL_DIR/<rs_name>/route-server/config/`

3. **导出**环境变量，包括：
   - `ENV_ROUTE_SERVER_CONFIG_PATH`，指向生成的 route-server.yaml
   - 根据传入参数设置 `GOMAXPROCS`

4. **启动**RouteServer 进程

5. **校验**进程状态：
   - 进程启动成功返回 `0`
   - 进程启动失败返回 `1`
   - （守护模式下，脚本会等 RouteServer 进程退出后再返回）

---

## deploy.sh 可选参数

`deploy.sh` 脚本支持以下可选参数：

### 基础配置

#### `--rs_name`
- **描述**：路由服务器实例的名称，在 `route-server.yaml` 中配置为 `server.name`
- **默认值**：`RouteServer_<ip>_<port>`

#### `--host`
- **描述**：指定该 RouteServer 实例的 IP 地址
- **默认值**：自动探测

#### `--port`
- **描述**：RouteServer 监听的 HTTP 端口，在 `route-server.yaml` 中配置为 `server.http_port`
- **默认值**：`9800`

#### `--ops-port`
- **描述**：运维服务器（ops server）用于管理操作的端口
- **默认值**：`http-port + 10`

#### `--max-procs`
- **描述**：启动时配置 `GOMAXPROCS`，限制 RouteServer 的 CPU 占用
- **默认值**：不限制

#### `--readiness-cfg`
- **描述**：`/internal/worker/readiness` 端点的就绪配置
- **格式**：`prefill:1#decode:2`
- **用途**：就绪检查时每个角色所需的最小数量
- **默认值**：`prefill:1#decode:1`

### 日志与监控

#### `--daemon`
- **描述**：启动检查通过后脚本是否保持附着
- **取值**：`true`/`false`（其他值会被拒绝）
- **行为**：
  - `true`：脚本等待 RouteServer 进程退出并返回其退出码 —— 当脚本作为容器入口或在 supervisor 下运行时使用
  - `false` 或省略：启动检查通过后脚本立即返回 `0`
  - RouteServer 进程本身始终由脚本在后台启动
- **默认值**：未设置（立即返回）

#### `--log-file-path`
- **描述**：指定 RouteServer 日志文件的完整路径
- **默认值**：`$RS_INSTALL_DIR/$rs_name/DistributedScheduler/log/route-server.log`

#### `--show-std-output`
- **描述**：是否将 RouteServer 进程输出直接打印到 stdout
- **取值**：`true`/`false`
- **默认值**：`false`

#### `--collect-metrics-unique-ip`
- **描述**：是否按 IP 采集每个唯一实例的指标
- **取值**：`true`/`false`
- **默认值**：`false`

### 分布式部署

#### `--mode`
- **描述**：RouteServer 的部署模式
- **取值**：`singleton`、`etcd`
- **作用**：自动调整事件同步间隔与远端调度器设置
- **优先级**：低于 `--remote-scheduler-type` 与 `--remote-scheduler-endpoint`
- **默认值**：`singleton`

#### `--remote-scheduler-type`
- **描述**：远端集中调度的类型
- **取值**：`leader`、`endpoint` 或空字符串
- **选项**：
  - `leader`：RouteServer worker 访问集群 leader 进行远端调度
  - `endpoint`：RouteServer worker 访问指定端点进行远端调度
  - 空字符串：关闭远端调度（仅本地调度）
- **说明**：singleton 模式部署时不应设置

#### `--remote-scheduler-endpoint`
- **描述**：当 `--remote-scheduler-type` 设为 `endpoint` 时使用的端点 URL
- **格式**：`http://x.x.x.x:xxxx`
- **说明**：当 `--remote-scheduler-type` 为 `leader` 或为空时不应配置

#### `--domain`
- **描述**：RS 监听 IP 对应的域名
- **用法**：供 worker 通过域名访问 leader

#### `--use_domain_for_leader`
- **描述**：是否启用通过域名访问 leader
- **取值**：`true`（使用域名）/ `false`（使用 IP）
- **默认值**：`false`

#### `--candidate`
- **描述**：该 RouteServer 是否以 candidate 模式部署
- **取值**：`true`/`false`
- **用法**：
  - 管理面 MO 进程：设为 `true`
  - 处理请求的 MO worker：设为 `false`
- **说明**：用于分布式部署模式

#### `--etcd-endpoints`
- **描述**：etcd 端点地址列表
- **格式**：逗号分隔的地址（例如 `"localhost:2379,other_domain:2379"`）
- **要求**：集群模式部署必填
- **支持**：域名与 IP 地址均可

### 推理配置

#### `--infer-mode`
- **描述**：推理部署模式
- **取值**：`Hybrid`、`PD`、`EPD`、`PD-AM`、`Layerwise-CDCP`、`Layerwise-CPCD`
- **默认值**：`PD`
- **模式**：
  - `Hybrid`：混合部署
  - `PD`：Prefill-Decode 分离模式
  - `EPD`：Encode-Prefill-Decode 分离模式
  - `PD-AM`：Attention Module 分离模式
  - `Layerwise-CDCP`：带 Cross-Data Communication Pipeline 的 Layerwise 模式
  - `Layerwise-CPCD`：带 Cross-Process Communication Data 的 Layerwise 模式

#### `--infer-module-version`
- **描述**：请求调度模式
- **取值**：`v0`、`pipeline`
- **选项**：
  - `v0`：基础线性调度
  - `pipeline`：支持可配置调度
- **默认值**：`pipeline`

#### `--reuse-prefilled-tokens`
- **描述**：为 layerwise 模式启用已 prefill token 的复用
- **类型**：裸开关 —— **不取值**。出现即启用；`--reuse-prefilled-tokens true` 会被当作未知选项拒绝
- **作用**：将 `Layerwise-CDCP` / `Layerwise-CPCD` pipeline 配置中的 `prefill_*` 函数改写为流式版本
- **默认值**：关闭（省略该选项）

### HTTPS 与 mTLS

所有 TLS 开关默认都是 `true`。要么为每个保持启用的平面提供证书，要么显式关闭
在别处终止 TLS 的平面。这也是直接执行 `bash deploy.sh` 会失败的原因：脚本在生成任何
配置之前就要求提供证书对。

| 平面 | 开关（默认值） | 证书参数 |
|-------|--------------------|-------------------|
| 主 HTTP 端口 | `--enable-https`（`true`）、`--enable-mtls`（`true`） | `--tls-cert-file`、`--tls-key-file`、`--tls-ca-cert-file` |
| 运维端口 | `--ops-enable-https`（`true`）、`--ops-enable-mtls`（`true`） | `--ops-tls-cert-file`、`--ops-tls-key-file`、`--ops-tls-ca-cert-file` |
| 下游 vLLM 实例客户端 | `--client-https-enable`（`true`）、`--client-mtls-enable`（`true`） | `--client-cert-file`、`--client-key-file`、`--client-ca-cert-file` |
| etcd 客户端 | `--etcd-tls-enable`（`true`）、`--etcd-mtls-enable`（`true`） | `--etcd-cert-file`、`--etcd-key-file`、`--etcd-ca-cert-file` |

`deploy.sh` 强制执行的规则：

- `<plane> https = true` 要求该平面的证书**和**私钥；否则脚本以
  `Error: ... required when --enable-https=true` 退出。
- `https = true` **且** `mtls = true` 还要求该平面的 CA 证书。
- `--tls-skip-verify`（`true`/`false`，默认 `false`）关闭对主服务器证书的
  校验。它只是诊断用的应急开关 —— 不要在生产环境使用。
- 关闭 HTTPS 即可跳过证书要求；mTLS 继承所在平面的 HTTPS 开关，无需单独关闭也能
  通过检查。

加密私钥口令、mTLS 示例以及完整的集群加固部署，见[安全参考](./security.zh-CN.md)。

---
# 示例用法

## Singleton 模式部署

在 singleton 模式下，RouteServer 在单个实例上同时负责调度与请求流转。

该模式适用于单节点部署或测试环境。

下面分别给出 PD 与 Hybrid 模式的示例，请按实际场景选择。

#### 1. PD 模式部署

```bash
# RouteServer IP and port settings
export HOST="x.x.x.x"
export PORT=9800

# Configure inference servers
export PREFILL_SERVERS="x.x.x.x:8000"
export DECODE_SERVERS="x.x.x.x:8001"

cd /path/to/route-server

bash deploy.sh \
    --rs_name singleton_${HOST}_${PORT} \
    --port $PORT \
    --host $HOST \
    --mode singleton \
    --infer-mode PD \
    --enable-https false \
    --ops-enable-https false \
    --client-https-enable false \
    --etcd-tls-enable false
```

#### 2. Hybrid 模式部署

Hybrid 模式只需要 Decode 节点（不需要 Prefill 节点）。

```bash
# RouteServer IP and port settings
export HOST="x.x.x.x"
export PORT=9800

# Hybrid mode only requires Decode servers
export DECODE_SERVERS="x.x.x.x:8001"

cd /path/to/route-server

bash deploy.sh \
    --rs_name singleton_${HOST}_${PORT} \
    --port $PORT \
    --host $HOST \
    --mode singleton \
    --infer-mode Hybrid \
    --enable-https false \
    --ops-enable-https false \
    --client-https-enable false \
    --etcd-tls-enable false 
```

#### 校验部署

启动 RouteServer 服务后，执行以下命令检查 RouteServer 健康检查是否正常：

```bash
curl -iX GET "http://x.x.x.x:9800/status" -H "Content-Type:application/json"
```

若返回码为 200，说明 RouteServer 部署成功。

#### 查看日志

查看 RouteServer 服务日志：
```bash
tail -f $RS_INSTALL_DIR/singleton_${HOST}_${PORT}/DistributedScheduler/log/route-server.log
```

---

## 集群部署（Etcd 模式）

集群部署依赖 Etcd 集群。Etcd 由用户自行提供。本节仅给出一个示例部署。

**约束**：同一个 ETCD 集群不能在多个 MO 集群之间共享，否则两个 MO 集群会合并为一个，导致 P/D 跨集群访问问题。

### 前置条件

部署之前，请确认以下事项：
- 已创建并进入将运行 RouteServer 与 etcd 的容器
- 容器中已具备 etcd 二进制

#### 1. 创建 start_etcd.sh 脚本

在每个节点上创建 `start_etcd.sh` 脚本，内容如下：

```bash
#!/bin/bash
set -euo pipefail

: "${MO_ETCD_NAME:?Please set MO_ETCD_NAME (e.g.: etcd-1)}"
: "${MO_ETCD_PEERS:?Please set MO_ETCD_PEERS (e.g.: etcd-1=x.x.x.x:32379,etcd-2=y.y.y.y:32379,etcd-3=z.z.z.z:32379)}"

MO_ETCD_TOKEN="${MO_ETCD_TOKEN:-etcd-cluster}"
MO_ETCD_DATA_DIR="${MO_ETCD_DATA_DIR:-/var/lib/etcd}/${MO_ETCD_NAME}"
MO_ETCD_LOG="${MO_ETCD_LOG:-/var/log/etcd/${MO_ETCD_NAME}.log}"

ETCD_IP=$(hostname -I | awk '{print $1}')
mkdir -p "${MO_ETCD_DATA_DIR}" "$(dirname "${MO_ETCD_LOG}")"
chmod 700 "${MO_ETCD_DATA_DIR}" "$(dirname "${MO_ETCD_LOG}")"

CLUSTER=""
MY_CLIENT_PORT=""
MY_PEER_PORT=""
for peer in $(echo "${MO_ETCD_PEERS}" | tr ',' ' '); do
    name="${peer%%=*}"
    addr="${peer##*=}"
    ip="${addr%%:*}"
    port="${addr##*:}"
    CLUSTER+="${name}=http://${ip}:$((port + 1)),"
    if [[ "${name}" == "${MO_ETCD_NAME}" ]]; then
        MY_CLIENT_PORT="${port}"
        MY_PEER_PORT=$((port + 1))
    fi
done
CLUSTER="${CLUSTER%,}"

: "${MY_CLIENT_PORT:?node name not found in peers. check MO_ETCD_PEERS and MO_ETCD_NAME}"

echo "Starting etcd: name=${MO_ETCD_NAME}, ip=${ETCD_IP}, client_port=${MY_CLIENT_PORT}, peer_port=${MY_PEER_PORT}"

etcd --name "${MO_ETCD_NAME}" \
    --initial-advertise-peer-urls "http://${ETCD_IP}:${MY_PEER_PORT}" \
    --listen-peer-urls "http://${ETCD_IP}:${MY_PEER_PORT}" \
    --listen-client-urls "http://${ETCD_IP}:${MY_CLIENT_PORT},http://127.0.0.1:${MY_CLIENT_PORT}" \
    --advertise-client-urls "http://${ETCD_IP}:${MY_CLIENT_PORT}" \
    --initial-cluster-token "${MO_ETCD_TOKEN}" \
    --initial-cluster "${CLUSTER}" \
    --data-dir "${MO_ETCD_DATA_DIR}" \
    --initial-cluster-state new \
    --log-outputs="${MO_ETCD_LOG}" \
    --logger=zap > /dev/null 2>&1
```

#### 2. 部署 Etcd 集群

建议以反亲和方式部署 3 个 etcd 集群实例。在 3 台机器上分别执行下面对应的命令：

**节点 1**：
```bash
# Node 1 x.x.x.x, etcd client port 32379, peer port is client port + 1 = 32380
export MO_ETCD_NAME=etcd-1
export MO_ETCD_PEERS="etcd-1=x.x.x.x:32379,etcd-2=y.y.y.y:32379,etcd-3=z.z.z.z:32379"
nohup ./start_etcd.sh &
tail -f nohup.out # View script output
```

**节点 2**：
```bash
# Node 2, y.y.y.y, etcd client port 32379, peer port is client port + 1 = 32380
export MO_ETCD_NAME=etcd-2
export MO_ETCD_PEERS="etcd-1=x.x.x.x:32379,etcd-2=y.y.y.y:32379,etcd-3=z.z.z.z:32379"
nohup ./start_etcd.sh &
tail -f nohup.out # View script output
```

**节点 3**：
```bash
# Node 3, z.z.z.z, etcd client port 32379, peer port is client port + 1 = 32380
export MO_ETCD_NAME=etcd-3
export MO_ETCD_PEERS="etcd-1=x.x.x.x:32379,etcd-2=y.y.y.y:32379,etcd-3=z.z.z.z:32379"
nohup ./start_etcd.sh &
tail -f nohup.out # View script output
```

#### 3. 校验集群状态

部署完成后，执行以下命令检查集群状态是否正常：

```bash
etcdctl endpoint status --endpoints=x.x.x.x:32379,y.y.y.y:32379,z.z.z.z:32379 --write-out=table
```

#### 4. etcd 3.4.x 的日志轮转

etcd 3.4.x 使用系统的 logrotate 实现日志轮转。在 `/etc/logrotate.d/` 目录下创建名为 `etcd` 的配置文件（注意：将 `${MO_ETCD_NAME}` 替换为实际名称）：

```
/var/log/${MO_ETCD_NAME}.log {
    rotate 7        # Keep the last 7 backup files
    size 100M       # logrotate checks daily; rotate if file reaches 100MB
    compress        # Compress old logs to save space
    delaycompress   # Delay compression until the next rotation
    missingok       # Do not report errors if log file is missing
    notifempty      # Do not rotate if log file is empty
    copytruncate    # Copy current log file then truncate original, for programs that keep writing to the same fd
}
```

强制触发一次日志轮转：
```bash
logrotate -vf /etc/logrotate.d/etcd
```

查看日志：
```bash
ls -lh /var/log/etcd*
```

#### 5. 重启 etcd（可选）

需要重启 etcd 时，在 etcd 容器内执行：
```bash
pkill -9 etcd
```
然后按前面的步骤重新启动。

### Worker 部署

以 Worker 部署时，RouteServer 只负责请求流转，调度与健康检查依赖集群 leader。

执行脚本启动服务。

下面分别给出 PD 与 Hybrid 模式的示例，请按实际场景选择。

必须配置好环境变量，且各端口不能重复。

#### 1. PD 模式部署

```bash
# RouteServer IP and port settings
export HOST="x.x.x.x"
export PORT=9800

# Configure inference servers
export PREFILL_SERVERS="x.x.x.x:8000"
export DECODE_SERVERS="x.x.x.x:8001"

# Configure etcd service address, etcd node list, comma-separated
export ETCD_ENDPOINTS="x.x.x.x:2379,y.y.y.y:2379,z.z.z.z:2379"

cd /path/to/route-server

bash deploy.sh \
    --rs_name worker_${HOST}_${PORT} \
    --port $PORT \
    --host $HOST \
    --mode etcd \
    --infer-mode PD \
    --etcd-endpoints $ETCD_ENDPOINTS \
    --candidate false \
    --max-procs 16 \
    --remote-scheduler-type leader \
    --enable-https false \
    --ops-enable-https false \
    --client-https-enable false \
    --etcd-tls-enable false
```

#### 2. Hybrid 模式部署

```bash
# RouteServer IP and port settings
export HOST="x.x.x.x"
export PORT=9800

# Hybrid mode only requires Decode servers
export DECODE_SERVERS="x.x.x.x:8001"

# Configure etcd service address, etcd node list, comma-separated
export ETCD_ENDPOINTS="x.x.x.x:2379,y.y.y.y:2379,z.z.z.z:2379"

cd /path/to/route-server

bash deploy.sh \
    --rs_name worker_${HOST}_${PORT} \
    --port $PORT \
    --host $HOST \
    --mode etcd \
    --infer-mode Hybrid \
    --etcd-endpoints $ETCD_ENDPOINTS \
    --candidate false \
    --max-procs 16 \
    --remote-scheduler-type leader \
    --enable-https false \
    --ops-enable-https false \
    --client-https-enable false \
    --etcd-tls-enable false
```

看到类似下面的输出，说明 RouteServer 部署成功。

```shell
RouteServer process is running (PID: 18942), --daemon is not true, exit with 0
```

**说明**：生产部署时，请为 K8S 上的 etcd 集群配置稳定的 ClusterIP 类型 Service，并将 `ETCD_ENDPOINTS` 配置为该 etcd 集群 ClusterIP 类型 Service 的 `domain:port`

**要求**：命令必须以 root 用户执行。请确认已提权到 root 用户。

#### 校验部署

启动 RouteServer 服务后，执行以下命令检查 RouteServer 健康检查是否正常：

```bash
curl -iX GET "http://x.x.x.x:9800/status" -H "Content-Type:application/json"
```

若返回码为 200，说明当前节点到 RouteServer 的连接正常。

#### 查看日志

查看 RouteServer 服务日志：
```bash
tail -f $RS_INSTALL_DIR/worker_${HOST}_${PORT}/DistributedScheduler/log/route-server.log
```

### Candidate 部署

candidate 有资格成为集群 leader。一个集群中可以部署多个 candidate，其中一个会被选举为 leader。

执行脚本启动服务。

下面分别给出 PD 与 Hybrid 模式的示例，请按实际场景选择。

必须配置好环境变量，且各端口不能重复。

#### 1. PD 模式部署

```bash
# RouteServer IP and port settings
export HOST="x.x.x.x"
export PORT=9800

# Configure inference servers
export PREFILL_SERVERS="x.x.x.x:8000"
export DECODE_SERVERS="x.x.x.x:8001"

# Configure etcd service address, etcd node list, comma-separated
export ETCD_ENDPOINTS="x.x.x.x:2379,y.y.y.y:2379,z.z.z.z:2379"

cd /path/to/route-server

bash deploy.sh \
    --rs_name candidate_${HOST}_${PORT} \
    --port $PORT \
    --host $HOST \
    --infer-mode PD \
    --mode etcd \
    --etcd-endpoints $ETCD_ENDPOINTS \
    --candidate true \
    --max-procs 16 \
    --remote-scheduler-type leader \
    --enable-https false \
    --ops-enable-https false \
    --client-https-enable false \
    --etcd-tls-enable false
```

#### 2. Hybrid 模式部署

```bash
# RouteServer IP and port settings
export HOST="x.x.x.x"
export PORT=9800

# Hybrid mode only requires Decode servers
export DECODE_SERVERS="x.x.x.x:8001"

# Configure etcd service address, etcd node list, comma-separated
export ETCD_ENDPOINTS="x.x.x.x:2379,y.y.y.y:2379,z.z.z.z:2379"

cd /path/to/route-server

bash deploy.sh \
    --rs_name candidate_${HOST}_${PORT} \
    --port $PORT \
    --host $HOST \
    --infer-mode Hybrid \
    --mode etcd \
    --etcd-endpoints $ETCD_ENDPOINTS \
    --candidate true \
    --max-procs 16 \
    --remote-scheduler-type leader \
    --enable-https false \
    --ops-enable-https false \
    --client-https-enable false \
    --etcd-tls-enable false
```

看到类似下面的输出，说明 RouteServer 部署成功。

```shell
RouteServer process is running (PID: 18942), --daemon is not true, exit with 0
```

**说明**：生产部署时，请为 K8S 上的 etcd 集群配置稳定的 ClusterIP 类型 Service，并将 `ETCD_ENDPOINTS` 配置为该 etcd 集群 ClusterIP 类型 Service 的 `domain:port`

**要求**：命令必须以 root 用户执行。请确认已提权到 root 用户。

#### 校验部署

启动 RouteServer 服务后，执行以下命令检查 RouteServer 健康检查是否正常：

```bash
curl -iX GET "http://x.x.x.x:9800/status" -H "Content-Type:application/json"
```

若返回码为 200，说明当前节点到 RouteServer 的连接正常。

确认 Candidate 与 Worker 两类 RouteServer 实例的日志中都有成功启动并连上 ETCD、获知最新 leader 的记录。

#### 查看日志

查看 RouteServer 服务日志：
```bash
tail -f $RS_INSTALL_DIR/candidate_${HOST}_${PORT}/DistributedScheduler/log/route-server.log
```
