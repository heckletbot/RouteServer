# RouteServer Security Hardening & TLS Deployment Guide

[English](security.md) | [简体中文](security.zh-CN.md)

This guide is designed to assist operations and security teams in hardening network listening interfaces, environment configurations, and external component communications for the **RouteServer** service. By properly configuring network boundaries, implementing secure environment variables, and enforcing mutual TLS (mTLS) authentication, you can eliminate the risks of cleartext data transmission and unauthorized access.

HTTPS defaults to off on the main, ops, vLLM and etcd planes; mTLS takes effect only after that plane's HTTPS is enabled.
This guide recommends production hardening and explicitly enables the illustrated planes. Certificate paths alone do not
enable encryption; a single-plane example leaves the other planes at their defaults. Replace placeholder addresses,
backend environment variables, certificate paths and passphrases with real values. Use `--option value`, not equals syntax.

---

## 1. Hardening Network Interfaces, Listening Ports, and Key Passwords

Restricting network listening interfaces, utilizing decoupled TLS certificate infrastructures, and securing private key passphrases represents the foundational layer of network security. RouteServer isolates the **Business Port** from the **Ops Port**, and both must be hardened separately.

### 1.1 Network Interface & Port Hardening Principles

* **Avoid Binding Blindly to 0.0.0.0**: Unless the service genuinely needs to accept traffic from all available network interfaces, explicitly define the specific listening IP address using --host.
* **Business Port (Default: 9800)**: If an upstream Load Balancer (LB) or API Gateway is deployed, it is highly recommended to bind RouteServer exclusively to a private internal IP (e.g., 192.168.x.x) or the local loopback address (127.0.0.1).
* **Ops Port**: This port is dedicated to health checks, metric collections (e.g., Prometheus), and management endpoints. **It is critical** to bind this port to a dedicated management network interface or an internal O&M subnet. Never expose it directly to the public internet.

### 1.2 Environment Variables for Private Key Decryption

If your TLS private keys (.key files) are encrypted with passphrases (highly recommended for production), you must inject the matching passwords via environment variables before launching the service.Currently, RouteServer processes the TLS_*_KEY_PASSWORD family of environment variables (including those introduced in subsequent sections) as cleartext.

* TLS_PRIVATE_KEY_PASSWORD: Decrypts the main business port's private key.
* TLS_OPS_KEY_PASSWORD: Decrypts the Ops/O&M port's private key.

---

### 1.3 Standalone Configuration Examples with Strict Mutual TLS / mTLS (Recommended)

Best suited for internal microservice architectures or secure service-to-service networks. Only clients holding a certificate signed by a trusted Certificate Authority (CA) can establish a connection.

```bash
# Export passphrases for both Business and Ops private keys
export TLS_PRIVATE_KEY_PASSWORD="***"
export TLS_OPS_KEY_PASSWORD="***"

# Launch with dual-plane mTLS protection
bash deploy.sh  \
  --host "*.*.*.*" \
  --port 9800 \
  --mode singleton \
  --enable-https true \
  --enable-mtls true \
  --ops-enable-https true \
  --ops-enable-mtls true \
  --tls-cert-file "/etc/certs/standalone/api-server.crt" \
  --tls-key-file "/etc/certs/standalone/api-server.key" \
  --tls-ca-cert-file "/etc/certs/standalone/api-ca.crt" \
  --ops-tls-cert-file "/etc/certs/standalone/ops-server.crt" \
  --ops-tls-key-file "/etc/certs/standalone/ops-server.key" \
  --ops-tls-ca-cert-file "/etc/certs/standalone/ops-ca.crt"

```

> 💡 **Security Note**: Never reuse the same certificate or passphrase for both business and ops traffic. Utilize separate CA chains to prevent any potential exposure of business-facing credentials from compromising the management plane.

---

## 2. Downstream vLLM Instance Communication Hardening (Client mTLS)

When RouteServer acts as a scheduling layer and routes requests downstream to vLLM instances, the traffic carries highly sensitive data, including model inputs (Prompts) and token outputs. Because LLM inference traffic often traverses high-bandwidth internal network clusters, it is susceptible to man-in-the-middle (MITM) attacks or internal sniffing.

By enabling client-side mTLS, RouteServer verifies the authenticity of the backend vLLM instances (preventing node spoofing) and authenticates its own scheduling identity to the vLLM nodes.

### 2.1 Passphrase Injection for Client Key

* TLS_CLIENT_KEY_PASSWORD: Used to decrypt RouteServer's client-side private key when communicating with downstream vLLM services.

### 2.2 Standalone Hardening Configuration Example

When integrating RouteServer with an isolated vLLM instance that has mTLS enabled, use the following configuration payload:

```bash
# Inject the client private key passphrase
export TLS_CLIENT_KEY_PASSWORD="***"

# Run RouteServer with encrypted backend channels
bash deploy.sh \
  --host "*.*.*.*" \
  --port 9800 \
  --mode singleton \
  --client-https-enable true \
  --client-mtls-enable true \
  --client-cert-file "/etc/certs/standalone/vllm-client.crt" \
  --client-key-file "/etc/certs/standalone/vllm-client.key" \
  --client-ca-cert-file "/etc/certs/standalone/vllm-ca.crt"

```

> ⚠️ **Production Tip**: Ensure that the Subject Alternative Name (SAN) fields within the vLLM instance certificates encompass the actual IP addresses or domain names resolved by RouteServer. Otherwise, connections will be terminated due to hostname verification failures.

---

## 3. Service Registry & Discovery Communication Hardening (etcd mTLS)

etcd retains core routing metadata, vLLM instance cluster topology, and global system configurations for RouteServer. Unauthorized access or tampering with etcd can result in complete traffic hijacking, catastrophic outages, or critical data breaches. Securing the connection to etcd via mutual TLS (mTLS) is a **Critical Severity Hardening Item**.

### 3.1 Passphrase Injection for etcd Client Key

* TLS_ETCD_KEY_PASSWORD: Used to decrypt RouteServer's client-side private key specifically dedicated to interacting with the etcd cluster.

### 3.2 Standalone Hardening Configuration Example

When connecting a single RouteServer instance to an etcd node configured with client-cert-auth enabled, perform the following initialization:

```bash
# Inject the dedicated etcd client private key passphrase
export TLS_ETCD_KEY_PASSWORD="***"

# Launch RouteServer with secured metadata communication
bash deploy.sh  \
  --host "*.*.*.*" \
  --port 9800 \
  --mode etcd \
  --etcd-endpoints "etcd.example.internal:2379" \
  --etcd-tls-enable true \
  --etcd-mtls-enable true \
  --etcd-cert-file "/etc/certs/standalone/etcd-client.crt" \
  --etcd-key-file "/etc/certs/standalone/etcd-client.key" \
  --etcd-ca-cert-file "/etc/certs/standalone/etcd-ca.crt"

```

### 3.3 Best Practices for etcd Security Principle of Least Privilege

1. **Certificate Identity Binding**: Ensure that the Common Name (CN) or Identity attributes inside RouteServer's etcd client certificate are bound tightly to etcd RBAC (Role-Based Access Control) profiles.
2. **Path Restriction**: Restrict the RouteServer account's permissions exclusively to its designated key prefix (e.g., /RouteServer/) with read and write capabilities. Never grant the global root administrator role to functional services.

---

## 4. RouteServer High-Availability Cluster Deployment Example

In production high-availability architectures, RouteServer nodes run in a clustered mode backed by an etcd consensus plane. The cluster usually features distributed scheduler **Leader** instances handling intelligent traffic distribution and **Worker** instances focusing on specific LLM routing tasks.

Below is a polished production deployment script demonstrating how to inject private key passwords securely and instantiate both node roles with full TLS isolation.

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
  --candidate false \
  --infer-mode "Layerwise-CPCD" \
  --reuse-prefilled-tokens \
  --infer-module-version "pipeline" \
  --remote-scheduler-type "leader" \
  --enable-https true \
  --enable-mtls true \
  --ops-enable-https true \
  --ops-enable-mtls true \
  --tls-cert-file "${CERT_DIR}/api-server.crt" \
  --tls-key-file "${CERT_DIR}/api-server.key" \
  --tls-ca-cert-file "${CERT_DIR}/api-ca.crt" \
  --ops-tls-cert-file "${CERT_DIR}/ops-server.crt" \
  --ops-tls-key-file "${CERT_DIR}/ops-server.key" \
  --ops-tls-ca-cert-file "${CERT_DIR}/ops-ca.crt" \
  --client-https-enable true \
  --client-mtls-enable true \
  --client-cert-file "${CERT_DIR}/vllm-client.crt" \
  --client-key-file "${CERT_DIR}/vllm-client.key" \
  --client-ca-cert-file "${CERT_DIR}/vllm-ca.crt" \
  --etcd-tls-enable true \
  --etcd-mtls-enable true \
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
  --enable-https true \
  --enable-mtls true \
  --ops-enable-https true \
  --ops-enable-mtls true \
  --tls-cert-file "${CERT_DIR}/api-server.crt" \
  --tls-key-file "${CERT_DIR}/api-server.key" \
  --tls-ca-cert-file "${CERT_DIR}/api-ca.crt" \
  --ops-tls-cert-file "${CERT_DIR}/ops-server.crt" \
  --ops-tls-key-file "${CERT_DIR}/ops-server.key" \
  --ops-tls-ca-cert-file "${CERT_DIR}/ops-ca.crt" \
  --client-https-enable true \
  --client-mtls-enable true \
  --client-cert-file "${CERT_DIR}/vllm-client.crt" \
  --client-key-file "${CERT_DIR}/vllm-client.key" \
  --client-ca-cert-file "${CERT_DIR}/vllm-ca.crt" \
  --etcd-tls-enable true \
  --etcd-mtls-enable true \
  --etcd-cert-file "${CERT_DIR}/etcd-client.crt" \
  --etcd-key-file "${CERT_DIR}/etcd-client.key" \
  --etcd-ca-cert-file "${CERT_DIR}/etcd-ca.crt"

```

---

## 🔒 Security Best Practices Checklist

> ℹ️ **Implementation Note on Ciphertext Passphrases**:
> Currently, RouteServer processes the TLS_*_KEY_PASSWORD environment variables as **cleartext** . If your enterprise compliance requires passphrases to be fetched or stored as encrypted ciphertext (e.g., integrated with a Secret Management Service or KMS), users must manually modify the source code. Specifically, you will need to implement a custom decryption pipeline or KMS SDK wrapper right after the codebase retrieves the raw string using os.Getenv().

| Check Item | Recommended Status | Description                                                                                                                                                                                                                       |
| :--- | :---: |:----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| **Business Port Encryption** (`--enable-https`) | **Enabled (true)** | Prevents cleartext data interception over internal or external networks.                                                                                                                                                          |
| **Business Port mTLS** (`--enable-mtls`) | **As Needed** | Recommended for service-mesh/microservice environments; can be terminated at the Load Balancer if an upstream gateway handles identity verification.                                                                              |
| **Ops Port Certificate Isolation** | **Fully Isolated** | Ops endpoints must use separate key pairs, and ideally distinct internal CA chains completely decoupled from business traffic.                                                                                                    |
| **Production Certificate Chain Validation** (`--tls-skip-verify`) | **Disabled (false)** | Never skip certificate validation in production to eliminate potential Man-in-the-Middle (MITM) vulnerabilities.                                                                                                                  |
| **vLLM Channel Mutual TLS** (`--client-mtls-enable`) | **Enabled (true)** | Guarantees that LLM prompts and inference token streams are fully protected along the downstream internal transport path.                                                                                                         |
| **etcd Plane Mutual TLS** (`--etcd-mtls-enable`) | **Enabled (true)** | Protects crucial state registry, node cluster topology, and routing metadata from unauthorized manipulation.                                                                                                                      |
| **TLS Key Passphrases** (`TLS_PRIVATE_KEY_PASSWORD`, `TLS_OPS_KEY_PASSWORD`, `TLS_CLIENT_KEY_PASSWORD`, `TLS_ETCD_KEY_PASSWORD`) | **Encrypted** *(via source code extension)* | While these four environment variables are processed as cleartext by default, it is highly recommended to extend the source code to decrypt ciphertext via KMS or Secret Management integration for strict enterprise compliance. |
| **Key File Permissions Control** | **chmod 400 / 600** | All `.key` private key files on servers must be strictly limited to read-only access for the specific user executing the RouteServer process.                                                                                       |