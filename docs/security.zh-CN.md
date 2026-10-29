# RouteServer 安全加固与 TLS 部署指南

[English](security.md) | [简体中文](security.zh-CN.md)

本指南旨在帮助运维与安全团队加固 **RouteServer** 服务的网络监听接口、环境配置以及外部组件通信。通过正确配置网络边界、使用安全的环境变量并强制启用双向 TLS（mTLS）认证，可以消除明文传输数据与未授权访问的风险。

---

## 1. 加固网络接口、监听端口与密钥口令

限制网络监听接口、采用解耦的 TLS 证书基础设施并保护私钥口令，是网络安全的基础层。RouteServer 将**业务端口**与**运维端口**隔离，两者必须分别加固。

### 1.1 网络接口与端口加固原则

* **避免盲目绑定 0.0.0.0**：除非服务确实需要接收所有可用网络接口的流量，否则应通过 --host 显式指定具体的监听 IP 地址。
* **业务端口（默认：9800）**：如果部署了上游负载均衡器（LB）或 API 网关，强烈建议将 RouteServer 仅绑定到内部私有 IP（例如 192.168.x.x）或本地回环地址（127.0.0.1）。
* **运维端口**：该端口专用于健康检查、指标采集（例如 Prometheus）和管理端点。**务必**将其绑定到专用管理网卡或内部运维子网。切勿将其直接暴露在公网。

### 1.2 私钥解密的环境变量

如果 TLS 私钥（.key 文件）使用口令加密（生产环境强烈建议如此），则必须在启动服务前通过环境变量注入匹配的密码。目前，RouteServer 将 TLS_*_KEY_PASSWORD 系列环境变量（包括后续章节中引入的变量）按明文处理。

* TLS_PRIVATE_KEY_PASSWORD：解密主业务端口的私钥。
* TLS_OPS_KEY_PASSWORD：解密运维（O&M）端口的私钥。

---

### 1.3 启用严格双向 TLS / mTLS 的单机配置示例（推荐）

最适合内部微服务架构或安全的服务间网络。只有持有受信任证书颁发机构（CA）签发证书的客户端才能建立连接。

```bash
# Export passphrases for both Business and Ops private keys
export TLS_PRIVATE_KEY_PASSWORD="***"
export TLS_OPS_KEY_PASSWORD="***"

# Launch with dual-plane mTLS protection
bash deploy.sh  \
  --host "*.*.*.*" \
  --port 9800 \
  --mode singleton \
  --tls-cert-file "/etc/certs/standalone/api-server.crt" \
  --tls-key-file "/etc/certs/standalone/api-server.key" \
  --tls-ca-cert-file "/etc/certs/standalone/api-ca.crt" \
  --ops-tls-cert-file "/etc/certs/standalone/ops-server.crt" \
  --ops-tls-key-file "/etc/certs/standalone/ops-server.key" \
  --ops-tls-ca-cert-file "/etc/certs/standalone/ops-ca.crt"

```

> [!IMPORTANT]
> **安全提示**：切勿为业务流量和运维流量复用同一份证书或口令。请使用独立的 CA 链，以免业务侧凭据一旦泄露就危及管理平面。

---

## 2. 下游 vLLM 实例通信加固（客户端 mTLS）

当 RouteServer 作为调度层，将请求向下游路由到 vLLM 实例时，这些流量携带高度敏感的数据，包括模型输入（Prompt）和 token 输出。由于 LLM 推理流量常常穿越高带宽的内部网络集群，它容易受到中间人（MITM）攻击或内部嗅探。

通过启用客户端 mTLS，RouteServer 可以验证后端 vLLM 实例的真实性（防止节点伪造），并向 vLLM 节点证明自身的调度身份。

### 2.1 客户端密钥的口令注入

* TLS_CLIENT_KEY_PASSWORD：用于在与下游 vLLM 服务通信时解密 RouteServer 的客户端私钥。

### 2.2 单机加固配置示例

将 RouteServer 与已启用 mTLS 的隔离 vLLM 实例集成时，请使用以下配置：

```bash
# Inject the client private key passphrase
export TLS_CLIENT_KEY_PASSWORD="***"

# Run RouteServer with encrypted backend channels
bash deploy.sh \
  --host "*.*.*.*" \
  --port 9800 \
  --mode singleton \
  --client-https-enable=true \
  --client-mtls-enable=true \
  --client-cert-file "/etc/certs/standalone/vllm-client.crt" \
  --client-key-file "/etc/certs/standalone/vllm-client.key" \
  --client-ca-cert-file "/etc/certs/standalone/vllm-ca.crt"

```

> [!WARNING]
> **生产提示**：请确保 vLLM 实例证书中的 Subject Alternative Name（SAN）字段涵盖 RouteServer 实际解析到的 IP 地址或域名。否则，连接将因主机名验证失败而被终止。

---

## 3. 服务注册与发现通信加固（etcd mTLS）

etcd 保存着 RouteServer 的核心路由元数据、vLLM 实例集群拓扑以及全局系统配置。对 etcd 的未授权访问或篡改可能导致流量被完全劫持、灾难性故障或严重数据泄露。通过双向 TLS（mTLS）保护与 etcd 的连接是一项**严重级别的加固事项**。

### 3.1 etcd 客户端密钥的口令注入

* TLS_ETCD_KEY_PASSWORD：用于解密 RouteServer 专用于与 etcd 集群交互的客户端私钥。

### 3.2 单机加固配置示例

将单个 RouteServer 实例连接到已启用 client-cert-auth 的 etcd 节点时，请执行以下初始化：

```bash
# Inject the dedicated etcd client private key passphrase
export TLS_ETCD_KEY_PASSWORD="***"

# Launch RouteServer with secured metadata communication
bash deploy.sh  \
  --host "*.*.*.*" \
  --port 9800 \
  --mode singleton \
  --etcd-tls-enable=true \
  --etcd-mtls-enable=true \
  --etcd-cert-file "/etc/certs/standalone/etcd-client.crt" \
  --etcd-key-file "/etc/certs/standalone/etcd-client.key" \
  --etcd-ca-cert-file "/etc/certs/standalone/etcd-ca.crt"

```

### 3.3 etcd 安全最小权限原则最佳实践

1. **证书身份绑定**：确保 RouteServer 的 etcd 客户端证书中的 Common Name（CN）或身份属性与 etcd RBAC（基于角色的访问控制）配置紧密绑定。
2. **路径限制**：将 RouteServer 账号的权限严格限制在其指定的 key 前缀（例如 /RouteServer/）上，仅授予读写能力。切勿将全局 root 管理员角色授予功能性服务。

---

## 4. RouteServer 高可用集群部署示例

在生产高可用架构中，RouteServer 节点以集群模式运行，由 etcd 共识平面支撑。集群通常包含负责智能流量分发的分布式调度器 **Leader** 实例，以及专注于特定 LLM 路由任务的 **Worker** 实例。

下面是一份完善的生产部署脚本，演示如何安全地注入私钥口令，并以完全 TLS 隔离的方式启动两种节点角色。

```bash
#!/usr/bin/env bash
set -euo pipefail

# ==============================================================================
# 1. SECURITY ENVIRONMENT CONFIGURATION
# ==============================================================================
# Define the absolute path for cryptographic assets
export CERT_DIR="/etc/certs/production"

# Inject production passphrases dynamically (Avoid hardcoding inside script files)
export TLS_PRIVATE_KEY_PASSWORD="***"
export TLS_OPS_KEY_PASSWORD="***"
export TLS_CLIENT_KEY_PASSWORD="***"
export TLS_ETCD_KEY_PASSWORD="***"

# Common Infrastructure Variables
export ETCD_CLUSTER_ENDPOINTS="*.*.*.*:2379"
export BIND_HOST_IP="*.*.*.*"

# ==============================================================================
# 2. DEPLOYMENT: WORKER NODE (worker-01)
# ==============================================================================
echo "Deploying RouteServer [Worker Node] with mTLS Hardening..."

bash deploy.sh \
  --rs_name "routeserver-worker-01" \
  --host "${BIND_HOST_IP}" \
  --port 9800 \
  --mode "etcd" \
  --etcd-endpoints "${ETCD_CLUSTER_ENDPOINTS}" \
  --candidate true \
  --infer-mode "Layerwise-CPCD" \
  --reuse-prefilled-tokens \
  --infer-module-version "pipeline" \
  --remote-scheduler-type "leader" \
  --tls-cert-file "${CERT_DIR}/api-server.crt" \
  --tls-key-file "${CERT_DIR}/api-server.key" \
  --tls-ca-cert-file "${CERT_DIR}/api-ca.crt" \
  --ops-tls-cert-file "${CERT_DIR}/ops-server.crt" \
  --ops-tls-key-file "${CERT_DIR}/ops-server.key" \
  --ops-tls-ca-cert-file "${CERT_DIR}/ops-ca.crt" \
  --client-cert-file "${CERT_DIR}/vllm-client.crt" \
  --client-key-file "${CERT_DIR}/vllm-client.key" \
  --client-ca-cert-file "${CERT_DIR}/vllm-ca.crt" \
  --etcd-cert-file "${CERT_DIR}/etcd-client.crt" \
  --etcd-key-file "${CERT_DIR}/etcd-client.key" \
  --etcd-ca-cert-file "${CERT_DIR}/etcd-ca.crt"

# ==============================================================================
# 3. DEPLOYMENT: LEADER / SCHEDULER NODE (leader-01)
# ==============================================================================
echo "Deploying RouteServer [Leader Scheduler Node] with Management Isolation..."

bash deploy.sh \
  --rs_name "routeserver-leader-01" \
  --host "${BIND_HOST_IP}" \
  --port 9801 \
  --ops-port 9210 \
  --mode "etcd" \
  --etcd-endpoints "${ETCD_CLUSTER_ENDPOINTS}" \
  --candidate true \
  --infer-mode "Layerwise-CPCD" \
  --reuse-prefilled-tokens \
  --infer-module-version "pipeline" \
  --remote-scheduler-type "leader" \
  --tls-cert-file "${CERT_DIR}/api-server.crt" \
  --tls-key-file "${CERT_DIR}/api-server.key" \
  --tls-ca-cert-file "${CERT_DIR}/api-ca.crt" \
  --ops-tls-cert-file "${CERT_DIR}/ops-server.crt" \
  --ops-tls-key-file "${CERT_DIR}/ops-server.key" \
  --ops-tls-ca-cert-file "${CERT_DIR}/ops-ca.crt" \
  --client-cert-file "${CERT_DIR}/vllm-client.crt" \
  --client-key-file "${CERT_DIR}/vllm-client.key" \
  --client-ca-cert-file "${CERT_DIR}/vllm-ca.crt" \
  --etcd-cert-file "${CERT_DIR}/etcd-client.crt" \
  --etcd-key-file "${CERT_DIR}/etcd-client.key" \
  --etcd-ca-cert-file "${CERT_DIR}/etcd-ca.crt"

```

---

## 🔒 安全最佳实践检查清单

> [!NOTE]
> **关于密文口令的实现说明**：
> 目前，RouteServer 将 TLS_*_KEY_PASSWORD 环境变量按**明文**处理。如果企业合规要求以加密密文的形式获取或存储口令（例如集成密钥管理服务或 KMS），用户必须手动修改源码。具体而言，需要在代码通过 os.Getenv() 取得原始字符串之后，紧接着实现自定义的解密流水线或 KMS SDK 封装。

| 检查项 | 建议状态 | 说明                                                                                                                                                                                                                              |
| :--- | :---: |:----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| **业务端口加密**（`--enable-https`） | **已启用（true）** | 防止内部或外部网络上的明文数据被截获。                                                                                                                                                          |
| **业务端口 mTLS**（`--enable-mtls`） | **按需启用** | 建议用于服务网格/微服务环境；如果上游网关负责身份验证，可在负载均衡器处终止。                                                                              |
| **运维端口证书隔离** | **完全隔离** | 运维端点必须使用独立的密钥对，最好使用与业务流量完全解耦的独立内部 CA 链。                                                                                                    |
| **生产环境证书链校验**（`--tls-skip-verify`） | **已禁用（false）** | 生产环境切勿跳过证书校验，以消除潜在的中间人（MITM）漏洞。                                                                                                                  |
| **vLLM 通道双向 TLS**（`--client-mtls-enable`） | **已启用（true）** | 保证 LLM Prompt 与推理 token 流在下游内部传输路径上受到完整保护。                                                                                                         |
| **etcd 平面双向 TLS**（`--etcd-mtls-enable`） | **已启用（true）** | 保护关键状态注册表、节点集群拓扑与路由元数据免遭未授权篡改。                                                                                                                      |
| **TLS 密钥口令**（`TLS_PRIVATE_KEY_PASSWORD`、`TLS_OPS_KEY_PASSWORD`、`TLS_CLIENT_KEY_PASSWORD`、`TLS_ETCD_KEY_PASSWORD`） | **加密** *（通过源码扩展实现）* | 这四个环境变量默认按明文处理，但为满足严格的企业合规要求，强烈建议扩展源码，通过 KMS 或密钥管理集成来解密密文。 |
| **密钥文件权限控制** | **chmod 400 / 600** | 服务器上所有 `.key` 私钥文件必须严格限制为仅允许执行 RouteServer 进程的特定用户只读访问。                                                                                       |
