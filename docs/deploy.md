# Deployment Guide

This guide describes deployment options. All deployment examples in this guide have HTTPS disabled. 

For secure deployment in production environments, refer to [security reference](./security.md)

> **Before you start**: If you are building from source, see the [Build Guide](./build.md) first to compile the project. If you have a pre-built release package (`.zip` from `make package` or `.tar.gz` from the CodeArts build), you can skip straight to [Installation Process](#installation-process).

## Environment Configuration

Configure the inference instance interface through environment variables:

| Variable | Description                                                                                |
|----------|--------------------------------------------------------------------------------------------|
| `ENCODE_SERVERS` | Address of the encode api_server (one for each DP) <br/> Format: `"IP:PORT,IP:PORT,..."`   |
| `PREFILL_SERVERS` | Address of the prefill api_server (one for each DP) <br/> Format: `"IP:PORT,IP:PORT,..."`  |
| `DECODE_SERVERS` | Address of the decode api_server (one for each DP) <br/> Format: `"IP:PORT,IP:PORT,..."`   |

 Optionally, you can specify the SLO tag after the port number using the `#` symbol:
```
"<IP1>:<PORT1>#tag1,<IP2>:<PORT2>#tag2"
```

### Optional Environment Variables

| Variable | Description | Default Value |
|----------|-------------|---------------|
| `RS_INSTALL_DIR` | Installation directory of the deploy.sh script | `/opt/route_server` |

---

## Installation Process

Both release artifacts use the same inner layout: deployment assets (`deploy.sh`, `*.yaml`,
`config_generator`, `scale/`, `pipelines/`) at the archive root and the runnable package under
`DistributedScheduler/`.

1. **Extract the package**
   ```bash
   # zip produced by `make package`
   unzip DistributedScheduler-*.zip -d route-server
   # tar.gz produced by the CodeArts build task
   tar -xzf route-server-*-linux-*.tar.gz -C route-server
   ```

2. **Run the deployment script with explicit options**
   ```bash
   cd route-server
   bash deploy.sh --mode singleton --infer-mode PD --port 9800 --host <YOUR_IP> ...
   ```

   A bare `bash deploy.sh` is **not** enough: HTTPS and mTLS default to `true` on all four
   planes, so the script exits unless you pass certificates or explicitly disable the planes you
   do not want to secure (see [HTTPS and mTLS](#https-and-mtls)). The complete commands for each
   topology are in [Example Usage](#example-usage).

> Use `bash`, not `sh`: the script relies on Bash constructs such as `[[ ]]` and fails under
> `dash`, which is `/bin/sh` on most Linux distributions.

---

## What the Deployment Script Does

The deployment script performs the following operations:

1. **Copies** the software and configuration files to `RS_INSTALL_DIR/<rs_name>/` directory

2. **Generates** the RouteServer configuration file (`route-server.yaml`) based on:
   - Template YAML files
   - Local IP address
   - Parameters provided to the deploy.sh script
   - Saves the configuration to `RS_INSTALL_DIR/<rs_name>/route-server/config/`

3. **Exports** environment variables, including:
   - `ENV_ROUTE_SERVER_CONFIG_PATH` pointing to the generated route-server.yaml
   - Sets `GOMAXPROCS` based on the provided parameters

4. **Starts** the RouteServer process

5. **Verifies** the process status:
   - Returns `0` if the process started successfully
   - Returns `1` if the process failed to start
   - (In daemon mode, the script waits for the RouteServer process to exit before returning)

---

## deploy.sh Optional Parameters

The `deploy.sh` script supports the following optional parameters:

### Basic Configuration

#### `--rs_name`
- **Description**: Name of the route server instance, configured in `route-server.yaml` as `server.name`
- **Default**: `RouteServer_<ip>_<port>`

#### `--host`
- **Description**: Specify the IP address of this RouteServer instance
- **Default**: Auto-detect

#### `--port`
- **Description**: HTTP port for the RouteServer to listen on, configured in `route-server.yaml` as `server.http_port`
- **Default**: `9800`

#### `--ops-port`
- **Description**: Port for the ops server for admin operations
- **Default**: `http-port + 10`

#### `--max-procs`
- **Description**: Configure `GOMAXPROCS` at startup to limit RouteServer CPU usage
- **Default**: No limit

#### `--readiness-cfg`
- **Description**: Readiness configuration for `/internal/worker/readiness` endpoint
- **Format**: `prefill:1#decode:2`
- **Purpose**: Minimum count for each role required when readiness check
- **Default**: `prefill:1#decode:1`

### Logging and Monitoring

#### `--daemon`
- **Description**: Whether the script should stay attached after the startup check
- **Values**: `true`/`false` (any other value is rejected)
- **Behavior**:
  - `true`: the script waits for the RouteServer process to exit and returns its exit code — use this when the script runs as a container entrypoint or under a supervisor
  - `false` or omitted: the script returns `0` as soon as the startup check passes
  - The RouteServer process itself is always started in the background by the script
- **Default**: Not set (returns immediately)

#### `--log-file-path`
- **Description**: Specify the full path to the RouteServer log file
- **Default**: `$RS_INSTALL_DIR/$rs_name/DistributedScheduler/log/route-server.log`

#### `--show-std-output`
- **Description**: Whether to print RouteServer process output directly to stdout
- **Values**: `true`/`false`
- **Default**: `false`

#### `--collect-metrics-unique-ip`
- **Description**: Whether collecting metrics from unique instance per ip
- **Values**: `true`/`false`
- **Default**: `false`

### Distributed Deployment

#### `--mode`
- **Description**: Deployment mode of the RouteServer
- **Values**: `singleton`, `etcd`
- **Effect**: Automatically adjusts event sync interval and remote-scheduler settings
- **Priority**: Lower than `--remote-scheduler-type` and `--remote-scheduler-endpoint`
- **Default**: `singleton`
- 
#### `--remote-scheduler-type`
- **Description**: Type of remote centralized scheduling
- **Values**: `leader`, `endpoint`, or empty string
- **Options**:
  - `leader`: RouteServer worker accesses cluster leader for remote scheduling
  - `endpoint`: RouteServer worker accesses specified endpoint for remote scheduling
  - Empty string: Disable remote scheduling (local scheduling only)
- **Note**: Should not be set for singleton mode deployment

#### `--remote-scheduler-endpoint`
- **Description**: Endpoint URL when `--remote-scheduler-type` is set to `endpoint`
- **Format**: `http://x.x.x.x:xxxx`
- **Note**: Should not be configured when `--remote-scheduler-type` is `leader` or empty

#### `--domain`
- **Description**: Domain name corresponding to the RS listening IP
- **Usage**: Used by workers to access leader via domain name

#### `--use_domain_for_leader`
- **Description**: Whether to enable domain name access to leader
- **Values**: `true` (use domain) / `false` (use IP)
- **Default**: `false`

#### `--candidate`
- **Description**: Whether this RouteServer is deployed in candidate mode
- **Values**: `true`/`false`
- **Usage**:
  - Management plane MO process: set to `true`
  - Request-handling MO worker: set to `false`
- **Note**: Used in distributed deployment mode

#### `--etcd-endpoints`
- **Description**: List of etcd endpoint addresses
- **Format**: Comma-separated addresses (e.g., `"localhost:2379,other_domain:2379"`)
- **Requirement**: Required for cluster mode deployment
- **Support**: Both domain names and IP addresses

### Inference Configuration

#### `--infer-mode`
- **Description**: Inference deployment mode
- **Values**: `Hybrid`, `PD`, `EPD`, `PD-AM`, `Layerwise-CDCP`, `Layerwise-CPCD`
- **Default**: `PD`
- **Modes**:
  - `Hybrid`: Hybrid deployment
  - `PD`: Prefill-Decode separated mode
  - `EPD`: Encode-Prefill-Decode separated mode
  - `PD-AM`: Attention Module separated mode
  - `Layerwise-CDCP`: Layerwise mode with Cross-Data Communication Pipeline
  - `Layerwise-CPCD`: Layerwise mode with Cross-Process Communication Data

#### `--infer-module-version`
- **Description**: Request scheduling mode
- **Values**: `v0`, `pipeline`
- **Options**:
  - `v0`: Basic linear scheduling
  - `pipeline`: Support for configurable scheduling
- **Default**: `pipeline`

#### `--reuse-prefilled-tokens`
- **Description**: Enable reuse of prefilled tokens for layerwise mode
- **Type**: Bare switch — takes **no value**. Presence enables it; `--reuse-prefilled-tokens true` is rejected as an unknown option
- **Effect**: Rewrites the `prefill_*` functions in the `Layerwise-CDCP` / `Layerwise-CPCD` pipeline configs to their streaming variants
- **Default**: Disabled (omit the flag)

### HTTPS and mTLS

All TLS switches default to `true`. Either provide the certificates for every plane you keep
enabled, or explicitly disable the planes you terminate elsewhere. This is why a bare
`bash deploy.sh` fails: the script requires a certificate pair before it generates any config.

| Plane | Switches (default) | Certificate flags |
|-------|--------------------|-------------------|
| Main HTTP port | `--enable-https` (`true`), `--enable-mtls` (`true`) | `--tls-cert-file`, `--tls-key-file`, `--tls-ca-cert-file` |
| Ops port | `--ops-enable-https` (`true`), `--ops-enable-mtls` (`true`) | `--ops-tls-cert-file`, `--ops-tls-key-file`, `--ops-tls-ca-cert-file` |
| Downstream vLLM instance client | `--client-https-enable` (`true`), `--client-mtls-enable` (`true`) | `--client-cert-file`, `--client-key-file`, `--client-ca-cert-file` |
| etcd client | `--etcd-tls-enable` (`true`), `--etcd-mtls-enable` (`true`) | `--etcd-cert-file`, `--etcd-key-file`, `--etcd-ca-cert-file` |

Rules enforced by `deploy.sh`:

- `<plane> https = true` requires that plane's certificate **and** key; otherwise the script exits
  with `Error: ... required when --enable-https=true`.
- `https = true` **and** `mtls = true` additionally require that plane's CA certificate.
- `--tls-skip-verify` (`true`/`false`, default `false`) disables verification of the main server's
  certificate. It is a diagnostic escape hatch only — do not use it in production.
- Disabling HTTPS is enough to skip the certificate requirement; mTLS inherits the plane's HTTPS
  switch and needs no separate disable for the check to pass.

See the [security reference](./security.md) for encrypted private-key passphrases, mTLS examples,
and a full hardened cluster deployment.

---
# Example Usage

## Deploy in Singleton mode

In singleton mode, RouteServer handles both scheduling and request streaming on a single instance.  

This mode is suitable for single-node deployments or testing environments.

Examples are provided for both PD and Hybrid modes. Choose based on your actual scenario.

#### 1. PD Mode Deployment

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

#### 2.Hybrid Mode Deployment

Hybrid mode only requires Decode nodes (no Prefill nodes).

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

#### Verify Deployment

After starting the RouteServer service, execute the following command to check if the RouteServer health check is normal:

```bash
curl -iX GET "http://x.x.x.x:9800/status" -H "Content-Type:application/json"
```

If it returns code 200, the RouteServer deployment is successful.

#### View Logs

To view RouteServer service logs:
```bash
tail -f $RS_INSTALL_DIR/singleton_${HOST}_${PORT}/DistributedScheduler/log/route-server.log
```

---

## Deploy as Cluster (Etcd Mode)

Cluster deployment depends on Etcd cluster. Etcd is provided by the user. This section provides only an example deployment.

**Constraint**: ETCD clusters cannot be shared across multiple MO clusters, otherwise the two MO clusters will merge into one, causing P/D cross-cluster access issues.

### Prerequisites

Before deployment, ensure the following:
- You have created and entered the container where RouteServer and etcd will run
- Etcd binaries are available in the container

#### 1. Create start_etcd.sh Script

Create the `start_etcd.sh` script on each node with the following content:

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

#### 2. Deploy Etcd Cluster

It is recommended to deploy 3 etcd cluster instances with anti-affinity. On each of the 3 machines, execute the corresponding commands below:

**Node 1** :
```bash
# Node 1 x.x.x.x, etcd client port 32379, peer port is client port + 1 = 32380
export MO_ETCD_NAME=etcd-1
export MO_ETCD_PEERS="etcd-1=x.x.x.x:32379,etcd-2=y.y.y.y:32379,etcd-3=z.z.z.z:32379"
nohup ./start_etcd.sh &
tail -f nohup.out # View script output
```

**Node 2** :
```bash
# Node 2, y.y.y.y, etcd client port 32379, peer port is client port + 1 = 32380
export MO_ETCD_NAME=etcd-2
export MO_ETCD_PEERS="etcd-1=x.x.x.x:32379,etcd-2=y.y.y.y:32379,etcd-3=z.z.z.z:32379"
nohup ./start_etcd.sh &
tail -f nohup.out # View script output
```

**Node 3** :
```bash
# Node 3, z.z.z.z, etcd client port 32379, peer port is client port + 1 = 32380
export MO_ETCD_NAME=etcd-3
export MO_ETCD_PEERS="etcd-1=x.x.x.x:32379,etcd-2=y.y.y.y:32379,etcd-3=z.z.z.z:32379"
nohup ./start_etcd.sh &
tail -f nohup.out # View script output
```

#### 3. Verify Cluster Status

After deployment, execute the following command to check if the cluster status is normal:

```bash
etcdctl endpoint status --endpoints=x.x.x.x:32379,y.y.y.y:32379,z.z.z.z:32379 --write-out=table
```

#### 4. Log Rotation for etcd 3.4.x

For etcd 3.4.x, implement log rotation using the system's logrotate. Create a configuration file named `etcd` in the `/etc/logrotate.d/` directory (note: replace `${MO_ETCD_NAME}` with the actual name):

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

To force a log rotation:
```bash
logrotate -vf /etc/logrotate.d/etcd
```

To view logs:
```bash
ls -lh /var/log/etcd*
```

#### 5. Restart etcd (Optional)

When needing to restart etcd, execute inside the etcd container:
```bash
pkill -9 etcd
```
Then follow the previous steps to restart.

### Worker Deployment

When deployed as Worker, RouteServer only handles request streaming and relies on the cluster leader for scheduling and health checks.  
Execute the script to start the service.  

Examples are provided for both PD and Hybrid modes. Choose based on your actual scenario.

The environment variables must be configured, and ports must not be duplicated.

#### 1. PD Mode Deployment

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

#### 2. Hybrid Mode Deployment

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

When you see fields similar to the following, the RouteServer deployment is successful.

```shell
RouteServer process is running (PID: 18942), --daemon is not true, exit with 0
```

**Note**: In production deployment, please configure a stable ClusterIP type Service for the etcd cluster on K8S, and configure ETCD_ENDPOINTS with the etcd cluster's ClusterIP type Service domain:port

**Requirement**: Commands must be executed under root user. Please ensure you have escalated to root user.

#### Verify Deployment

After starting the RouteServer service, execute the following command to check if the RouteServer health check is normal:

```bash
curl -iX GET "http://x.x.x.x:9800/status" -H "Content-Type:application/json"
```

If it returns code 200, the current node's connection to RouteServer is normal.

#### View Logs

To view RouteServer service logs:
```bash
tail -f $RS_INSTALL_DIR/worker_${HOST}_${PORT}/DistributedScheduler/log/route-server.log
```

### Candidate Deployment

Candidates are eligible to become the cluster leader. Multiple candidates can be deployed in a cluster, and one of them will be elected as leader.  

Execute the script to start the service.  

Examples are provided for both PD and Hybrid modes. Choose based on your actual scenario.

The environment variables must be configured, and ports must not be duplicated.

#### 1. PD Mode Deployment

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

#### 2. Hybrid Mode Deployment

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

When you see fields similar to the following, the RouteServer deployment is successful.

```shell
RouteServer process is running (PID: 18942), --daemon is not true, exit with 0
```

**Note**: In production deployment, please configure a stable ClusterIP type Service for the etcd cluster on K8S, and configure ETCD_ENDPOINTS with the etcd cluster's ClusterIP type Service domain:port

**Requirement**: Commands must be executed under root user. Please ensure you have escalated to root user.

#### Verify Deployment

After starting the RouteServer service, execute the following command to check if the RouteServer health check is normal:

```bash
curl -iX GET "http://x.x.x.x:9800/status" -H "Content-Type:application/json"
```

If it returns code 200, the current node's connection to RouteServer is normal.

Confirm that both Candidate and Worker RouteServer instances have the following logs indicating successful startup and connection to ETCD, knowing the latest leader.

#### View Logs

To view RouteServer service logs:
```bash
tail -f $RS_INSTALL_DIR/candidate_${HOST}_${PORT}/DistributedScheduler/log/route-server.log
```
