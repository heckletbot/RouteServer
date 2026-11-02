# Deployment Guide

This guide describes deployment options. 

All deployment examples in this guide have HTTPS disabled. 

For secure deployment in production environments, refer to [security reference](./security.md)

> **Before you start**: If you are building from source, see the [Build Guide](./build.md) first to compile the project. If you have a pre-built release package (`.zip` or `.rar`), you can skip straight to [Installation Process](#installation-process).

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

1. **Extract the package**
   ```bash
   unzip DistributedScheduler-*.zip
   ```

2. **Run the deployment script**
   ```bash
   sh deploy.sh
   ```

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
- **Description**: Whether the script should run in daemon mode
- **Values**: `true`/`false`
- **Behavior**: When `true`, the script waits for the RouteServer process to exit before returning
- **Default**: `false`

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
- **Values**: `singleton`, `etcd`, `k8s-configmap`
- **Effect**: Automatically adjusts event sync interval and remote-scheduler settings
- **Priority**: Lower than `--remote-scheduler-type` and `--remote-scheduler-endpoint`
- **Default**: `singleton`

#### `--cluster-namespace`
- **Description**: Kubernetes namespace for cluster resources when `--mode k8s-configmap`
- **Default**: empty, falls back to the `POD_NAMESPACE` environment variable
- **Note**: Ignored in `singleton` and `etcd` modes

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
  - Management plane RS process: set to `true`
  - Request-handling RS worker: set to `false`
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
- **Default**: `v0`

#### `--reuse-prefilled-tokens`
- **Description**: Enable reuse of prefilled tokens for layerwise mode
- **Values**: `true`/`false`
- **Default**: `false`

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

**Constraint**: ETCD clusters cannot be shared across multiple RS clusters, otherwise the two RS clusters will merge into one, causing P/D cross-cluster access issues.

### Prerequisites

Before deployment, ensure the following:
- Create and enter the container (refer to section 4.1 - Prerequisites)
- Etcd binaries are available in the container

#### 1. Create start_etcd.sh Script

Create the `start_etcd.sh` script on each node with the following content:

```bash
#!/bin/bash
set -euo pipefail

: "${RS_ETCD_NAME:?Please set RS_ETCD_NAME (e.g.: etcd-1)}"
: "${RS_ETCD_PEERS:?Please set RS_ETCD_PEERS (e.g.: etcd-1=x.x.x.x:32379,etcd-2=y.y.y.y:32379,etcd-3=z.z.z.z:32379)}"

RS_ETCD_TOKEN="${RS_ETCD_TOKEN:-etcd-cluster}"
RS_ETCD_DATA_DIR="${RS_ETCD_DATA_DIR:-/var/lib/etcd}/${RS_ETCD_NAME}"
RS_ETCD_LOG="${RS_ETCD_LOG:-/var/log/etcd/${RS_ETCD_NAME}.log}"

ETCD_IP=$(hostname -I | awk '{print $1}')
mkdir -p "${RS_ETCD_DATA_DIR}" "$(dirname "${RS_ETCD_LOG}")"
chmod 700 "${RS_ETCD_DATA_DIR}" "$(dirname "${RS_ETCD_LOG}")"

CLUSTER=""
MY_CLIENT_PORT=""
MY_PEER_PORT=""
for peer in $(echo "${RS_ETCD_PEERS}" | tr ',' ' '); do
    name="${peer%%=*}"
    addr="${peer##*=}"
    ip="${addr%%:*}"
    port="${addr##*:}"
    CLUSTER+="${name}=http://${ip}:$((port + 1)),"
    if [[ "${name}" == "${RS_ETCD_NAME}" ]]; then
        MY_CLIENT_PORT="${port}"
        MY_PEER_PORT=$((port + 1))
    fi
done
CLUSTER="${CLUSTER%,}"

: "${MY_CLIENT_PORT:?node name not found in peers. check RS_ETCD_PEERS and RS_ETCD_NAME}"

echo "Starting etcd: name=${RS_ETCD_NAME}, ip=${ETCD_IP}, client_port=${MY_CLIENT_PORT}, peer_port=${MY_PEER_PORT}"

etcd --name "${RS_ETCD_NAME}" \
    --initial-advertise-peer-urls "http://${ETCD_IP}:${MY_PEER_PORT}" \
    --listen-peer-urls "http://${ETCD_IP}:${MY_PEER_PORT}" \
    --listen-client-urls "http://${ETCD_IP}:${MY_CLIENT_PORT},http://127.0.0.1:${MY_CLIENT_PORT}" \
    --advertise-client-urls "http://${ETCD_IP}:${MY_CLIENT_PORT}" \
    --initial-cluster-token "${RS_ETCD_TOKEN}" \
    --initial-cluster "${CLUSTER}" \
    --data-dir "${RS_ETCD_DATA_DIR}" \
    --initial-cluster-state new \
    --log-outputs="${RS_ETCD_LOG}" \
    --logger=zap > /dev/null 2>&1
```

#### 2. Deploy Etcd Cluster

It is recommended to deploy 3 etcd cluster instances with anti-affinity. On each of the 3 machines, execute the corresponding commands below:

**Node 1** :
```bash
# Node 1 x.x.x.x, etcd client port 32379, peer port is client port + 1 = 32380
export RS_ETCD_NAME=etcd-1
export RS_ETCD_PEERS="etcd-1=x.x.x.x:32379,etcd-2=y.y.y.y:32379,etcd-3=z.z.z.z:32379"
nohup ./start_etcd.sh &
tail -f nohup.out # View script output
```

**Node 2** :
```bash
# Node 2, y.y.y.y, etcd client port 32379, peer port is client port + 1 = 32380
export RS_ETCD_NAME=etcd-2
export RS_ETCD_PEERS="etcd-1=x.x.x.x:32379,etcd-2=y.y.y.y:32379,etcd-3=z.z.z.z:32379"
nohup ./start_etcd.sh &
tail -f nohup.out # View script output
```

**Node 3** :
```bash
# Node 3, z.z.z.z, etcd client port 32379, peer port is client port + 1 = 32380
export RS_ETCD_NAME=etcd-3
export RS_ETCD_PEERS="etcd-1=x.x.x.x:32379,etcd-2=y.y.y.y:32379,etcd-3=z.z.z.z:32379"
nohup ./start_etcd.sh &
tail -f nohup.out # View script output
```

#### 3. Verify Cluster Status

After deployment, execute the following command to check if the cluster status is normal:

```bash
etcdctl endpoint status --endpoints=x.x.x.x:32379,y.y.y.y:32379,z.z.z.z:32379 --write-out=table
```

#### 4. Log Rotation for etcd 3.4.x

For etcd 3.4.x, implement log rotation using the system's logrotate. Create a configuration file named `etcd` in the `/etc/logrotate.d/` directory (note: replace `${RS_ETCD_NAME}` with the actual name):

```
/var/log/${RS_ETCD_NAME}.log {
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
RouteServer process is running (PID: 18942), --daemon is not set, exit with 0
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
RouteServer process is running (PID: 18942), --daemon is not set, exit with 0
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

---

## Deploy as Cluster (ConfigMap Mode)

ConfigMap mode derives cluster topology from a Kubernetes ConfigMap instead of etcd. It requires no etcd cluster and no CRD: RouteServer watches one topology ConfigMap, elects a leader through a Kubernetes Lease, and optionally persists failure snapshots to a separate `rs-status` ConfigMap.

**When to use**: you already run Kubernetes, want to avoid operating etcd, and can have the management plane publish the topology as a ConfigMap.

**Constraint**: the topology ConfigMap is the single source of truth for peers and instances. Every RouteServer in the cluster must point at the same ConfigMap; two ConfigMaps means two independent clusters.

### Prerequisites

- RouteServer Pods run inside the cluster with a ServiceAccount (in-cluster config is used by default).
- The ServiceAccount has the permissions listed in `deploy/k8s-configmap/rbac.yaml`: `configmaps` get/list/watch/create/update, `leases` get/create/update/watch, and `pods` list when Pod List reconciliation is enabled.
- **If you enable failure persistence (`owner-reference` is set), the ServiceAccount additionally needs `get` on the owner resource.** RouteServer resolves the owner UID at runtime through a dynamic client `Get`; without it, `ensureStatusConfigMap` fails and failure snapshots are silently dropped (the log shows `configmap get owner resource ... failed`).
- **On clusters with the `OwnerReferencesPermissionEnforcement` admission plugin enabled, the ServiceAccount also needs `update` on the owner's `finalizers` subresource.** RouteServer sets `blockOwnerDeletion: true` on the `rs-status` ConfigMap; that admission plugin rejects the create unless the caller can update the owner's finalizers. See the fan-out table below.
- The topology ConfigMap exists before RouteServer starts.

#### Owner-reference permission matrix

How much RBAC you need depends on how `rs-status` is created:

| Scenario | `configmaps` verbs | owner resource | Observed result |
|---|---|---|---|
| RouteServer creates `rs-status` itself | `get`, `list`, `watch`, `create`, `update` | `get` on owner | Works when the cluster does **not** enable `OwnerReferencesPermissionEnforcement` |
| Same, on a cluster that **does** enable it | same | `get` **and** `<owner>/finalizers: update` | Without the finalizers rule the create is rejected with `cannot set blockOwnerDeletion if an ownerReference refers to a resource you can't set finalizers on` |
| An operator pre-creates `rs-status` | `get`, `list`, `watch`, `update` (no `create`) | `get` on owner | Works; RouteServer takes the "already exists" branch. The operator must set `ownerReferences` itself |

`kubectl auth can-i` needs `--subresource` for the finalizers check — a slash is parsed as `TYPE/NAME`, not a subresource:

```bash
# Correct
kubectl auth can-i update podgroups --subresource=finalizers --as=system:serviceaccount:rs-system:route-server -n rs-system

# Wrong: silently reports "no" because it asks about a resource *named* finalizers
kubectl auth can-i update podgroups/finalizers --as=system:serviceaccount:rs-system:route-server -n rs-system
```

### 1. Apply RBAC

```bash
kubectl apply -f deploy/k8s-configmap/rbac.yaml
```

Adjust `metadata.namespace` and the ServiceAccount name to match your deployment before applying. **The file ships with a `<namespace>` placeholder and will not apply verbatim.**

The bundled file grants only the base permissions. Add the two owner rules yourself when you enable failure persistence:

```yaml
  - apiGroups: ["<owner-group>"]          # e.g. apps
    resources: ["<owner-plural>"]         # e.g. deployments
    verbs: ["get"]
  - apiGroups: ["<owner-group>"]
    resources: ["<owner-plural>/finalizers"]
    verbs: ["update"]
```

### 2. Create the topology ConfigMap

The ConfigMap holds the topology JSON under the key configured by `topology-key`. **`topology-key` is required — RouteServer refuses to start when it is empty** (`configmap topology-key is required`). The template ships with `topology-key: "topology"`, but nothing enforces that value; it only has to match the data key below.

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: rs-topology
  namespace: rs-system
data:
  topology: |
    {
      "version": "1.0",
      "status": "completed",
      "server_group_count": "2",
      "server_group_list": [
        {
          "group_id": "0",
          "group_type": "master",
          "server_count": "3",
          "server_list": [
            {"server_id": "0", "server_ip": "10.244.0.21", "pod_name": "rs-0"},
            {"server_id": "1", "server_ip": "10.244.0.22", "pod_name": "rs-1"},
            {"server_id": "2", "server_ip": "10.244.0.23", "pod_name": "rs-2"}
          ]
        },
        {
          "group_id": "1",
          "group_type": "prefill",
          "server_count": "1",
          "server_list": [
            {"server_id": "0", "server_ip": "10.244.0.31", "pod_name": "prefill-0"}
          ]
        },
        {
          "group_id": "2",
          "group_type": "decode",
          "server_count": "1",
          "server_list": [
            {"server_id": "0", "server_ip": "10.244.0.41", "pod_name": "decode-0"}
          ]
        }
      ]
    }
```

**Field reference**

| Field | Meaning |
|---|---|
| `version` | Topology format version. Not interpreted by RouteServer today; keep it for traceability. |
| `status` | Free-form status string. RouteServer does not gate on it. |
| `server_group_count` | Number of entries in `server_group_list`. Informational. |
| `server_group_list[].group_id` | Free-form group identifier. |
| `server_group_list[].group_type` | Role of the group. The value matching `router-group-type` (default `master`) becomes RouteServer peers; every other value must appear in `instance-groups`. |
| `server_group_list[].server_count` | Number of entries in `server_list`. Informational. |
| `server_group_list[].server_list[].server_id` | Free-form server identifier. |
| `server_group_list[].server_list[].server_ip` | IP address of the inference instance. Route generation uses this value directly. |
| `server_group_list[].server_list[].pod_name` | Pod name; used for stable ordering and for peer naming. |
| `server_group_list[].server_list[].device[]` | Optional accelerator details (`device_id`, `device_ip`, `device_logical_id`, `rank_id`). Carried through but not interpreted by the scheduler. |

**Rules**

- A server entry with an empty `server_ip` is skipped.
- `group_type` values listed in `instance-groups` generate instance routes: `endpoint-count` routes per server, starting at `starting-port`.
- The `router-group-type` group generates peers instead of instances and must not also appear in `instance-groups`.
- Servers are sorted by `pod_name` before diffing, so republishing the same set in a different order produces no state change.

### 3. Write k8s-configmap.yaml

Place `k8s-configmap.yaml` next to `route-server.yaml` in the config directory and fill in the placeholders:

```yaml
kubeconfig-path: ""                 # empty: use in-cluster config
namespace: rs-system
configmap-name: rs-topology
topology-key: "topology"
failures-key: "failures"            # empty: disable failure persistence

owner-reference:
  api-version: apps/v1              # owner of the rs-status ConfigMap
  kind: Deployment
  name: route-server

instance-groups:
  - group-type: prefill
    starting-port: 17000
    endpoint-count: 2
  - group-type: decode
    starting-port: 18000
    endpoint-count: 2

router-group-type: "master"
leader-election-key: "rs-leader-election"
leader-election-lease-duration-sec: 15
leader-election-renew-deadline-sec: 10
leader-election-retry-period-sec: 2
min-failure-persist-interval-sec: 3

fallback-pod-discovery-enabled: false
fallback-pod-list-interval-sec: 60
infer-group-name: ""
infer-group-label-key: ""
infer-group-role-type-label-key: ""
```

**Required fields**

| Field | Notes |
|---|---|
| `configmap-name` | Must match the ConfigMap created in step 2. |
| `topology-key` | Must match the data key holding the topology JSON. |
| `instance-groups` | At least one entry for every non-router `group_type` you publish. Groups absent here are ignored. |
| `owner-reference` | Optional. When all three fields are set, RouteServer creates `<name>-rs-status` and owns it, enabling failure persistence. When empty, failures are not persisted. |
| `infer-group-name`, `infer-group-label-key`, `infer-group-role-type-label-key` | Required only when `fallback-pod-discovery-enabled: true`. There are no built-in defaults; RouteServer refuses to start if any of them is empty while reconciliation is enabled. |

### 4. Point the cluster config at ConfigMap mode

In `route-server.yaml`, the `cluster` section must select `k8s-configmap` and reference the file from step 3:

```yaml
cluster:
  type: k8s-configmap
  role_candidate: true
  configmap-config-file: k8s-configmap.yaml
```

`configmap-config-file` may be absolute or relative. A relative path is resolved against `ENV_ROUTE_SERVER_CONFIG_PATH` (the directory holding `route-server.yaml`).

`deploy.sh --mode k8s-configmap` generates this automatically and copies `k8s-configmap.yaml` into the config directory.

### 5. Start RouteServer

```bash
./deploy.sh \
  --rs_name rs-0 \
  --host 10.244.0.21 \
  --port 9800 \
  --mode k8s-configmap \
  --cluster-namespace rs-system \
  --candidate true \
  --infer-mode PD \
  --infer-module-version pipeline \
  --enable-https false \
  --ops-enable-https false \
  --client-https-enable false
```

`--ops-port` defaults to `http-port + 10`. All peers in the cluster must use the same http and ops ports, because peer ports are derived from the local configuration rather than published in the topology.

### 6. Optional: Pod List reconciliation

When the management plane cannot update the ConfigMap promptly, the leader can rebuild the topology from Pod labels as a fallback. Enable it by setting `fallback-pod-discovery-enabled: true` and naming the labels your inference Pods carry:

```yaml
fallback-pod-discovery-enabled: true
fallback-pod-list-interval-sec: 60
infer-group-name: my-inference-group
infer-group-label-key: example.com/infer-group
infer-group-role-type-label-key: example.com/group-type
```

With this configuration, Pods labelled `example.com/infer-group=my-inference-group` are listed every 60 seconds when the ConfigMap has not been updated within that interval. The value of `example.com/group-type` on each Pod becomes its `group_type` (`master`, `prefill`, `decode`, ...). Only `Running` Pods with a non-empty `status.podIP` are used.

The rebuilt topology updates in-memory routes only; the ConfigMap is never written back. Grant the `pods` `list` permission (already present in `deploy/k8s-configmap/rbac.yaml`).

### 7. Minimal end-to-end example

The smallest setup that exercises peer discovery, instance routing and leader election: three candidates plus a small prefill/decode set. Numbers are illustrative; substitute your own Pod IPs.

```bash
NS=rs-system
kubectl create namespace $NS
kubectl apply -f deploy/k8s-configmap/rbac.yaml     # after editing <namespace>
```

Publish the topology once the inference Pods have IPs:

```bash
kubectl get pods -n $NS -o wide      # note each Pod's IP
```

Then start one candidate per Pod. `--host` must be the Pod IP because RouteServer uses it both to bind and to derive its peer name:

```bash
./deploy.sh \
  --rs_name rs-candidate-0 --host 10.244.0.21 --port 9800 \
  --mode k8s-configmap --cluster-namespace $NS \
  --candidate true --infer-mode PD \
  --enable-https false --enable-mtls false \
  --ops-enable-https false --ops-enable-mtls false \
  --client-https-enable false --client-mtls-enable false \
  --etcd-tls-enable false --etcd-mtls-enable false
```

`--host` and the TLS flags matter in practice: without `--host` the address comes from `hostname -I`, and the TLS defaults are `true` on every hop, which fails outright when no certificates are configured.

Expected steady state:

```bash
kubectl get lease -n $NS -o jsonpath='{.spec.holderIdentity}'; echo
# candidate_10_244_0_21_9800

curl -s http://10.244.0.21:9800/cluster/local/state | jq '.state | {peers: (.peers|keys), instances: (.InstanceRoutes|keys)}'
# peers: 3, instances: one per prefill/decode endpoint
```

`InstanceRoutes` is capitalised in the JSON payload — `jq '.state.InstanceRoutes'`, not `.instanceRoutes`.

### 8. Verify Deployment

```bash
curl -iX GET "http://10.244.0.21:9800/status"
curl -iX GET "http://10.244.0.21:9800/cluster/leader"
```

The first call returns 200 when the instance list is readable. The second returns the current leader, resolved from the Lease.

**Which port serves `/cluster/leader`**: it is registered on the ops port *and* — unless `enable-ops-apis-on-service-port` is `true` — also on the main http port. So the call above works against `9800` by default. If you set `enable-ops-apis-on-service-port: true`, the leader/local-state routes move to the ops port group and you must query `9810` instead. Both ports answer on a default deployment; verify whichever one your monitoring actually probes.

Leader identity is the `<role>_<ip_with_underscores>_<port>` form produced by `EndpointName`, for example `candidate_10_244_0_21_9800`. The legacy `IP:Port` form is still accepted when parsing, but RouteServer does not write it.

Check that exactly one RouteServer reports itself as leader and that the others can reach it:

```bash
kubectl get lease rs-leader-election -n rs-system -o jsonpath='{.spec.holderIdentity}'
kubectl logs -n rs-system rs-1 | grep -c "send WorkerEventsRequest to leader failed"
```

The counter should stay at zero on a healthy cluster.

### 9. View Logs

```bash
tail -f $RS_INSTALL_DIR/rs-0/route-server/log/route-server.log
```

Key log lines to look for:

| Log | Meaning |
|---|---|
| `configmap informer started and synced` | Topology ConfigMap watch established. |
| `configmap leader election: became leader` | This instance won the Lease. |
| `configmap topology applied, reason=...` | A topology change was applied to the in-memory state. |
| `configmap created status ConfigMap: ...` | The leader created `rs-status` for failure persistence. |

### Operating notes

**`server.addr` is derived from the Pod IP, not `0.0.0.0`.** `deploy.sh` runs `hostname -I | awk '{print $1}'` to pick the bind address, and that value lands in `server.addr`. Consequences:

- RouteServer listens **only on its Pod IP**, not on `127.0.0.1`. `kubectl port-forward pod/<rs>` therefore fails with `dial tcp4 127.0.0.1:9800: connect: connection refused`.
- On a multi-homed host or with `hostNetwork: true`, `hostname -I` may return an interface you did not intend. Pass `--host <pod-ip>` explicitly instead of relying on auto-detection.

To expose the leader outside the cluster, use a `ClusterIP` Service over all candidates rather than a per-Pod port-forward. Leader-only routes (`/status`, `/cluster/*`, `/metrics`, join/leave) are transparently proxied to the leader by `adminLeaderProxyMiddleware`, so a Service that load-balances across candidates still returns leader data:

```yaml
apiVersion: v1
kind: Service
metadata:
  name: rs-service
  namespace: rs-system
spec:
  selector:
    app: route-server          # must match your candidate Pod labels
  ports:
    - name: http
      port: 9800
      targetPort: 9800
```

**Pod IPs change when a Pod is recreated, and the topology does not follow.** `server_ip` in the topology ConfigMap is whatever the management plane wrote. A recreated Pod gets a new IP, so the peer is still named after the old address and routes point at a dead endpoint. Either:

- re-publish the ConfigMap with the new `server_ip` after any Pod restart, or
- enable `fallback-pod-discovery-enabled: true` so the leader rebuilds topology from Pod labels.

**Pod CIDR affects nothing inside RouteServer, but it affects your test scripts.** RouteServer treats `server_ip` as an opaque address. Scripts that hard-code the API server Service IP (for disconnect testing) must use the cluster's actual `kubernetes` Service IP:

```bash
kubectl get svc kubernetes -o jsonpath='{.spec.clusterIP}'   # e.g. 10.43.0.1 on k3s, 10.96.0.1 on kubeadm
```

Likewise `server_ip` values in the topology must be reachable Pod IPs from that cluster — a topology copied from another cluster will silently produce unreachable routes.

**All peers must share the same http and ops ports.** Peer port numbers are taken from the local configuration, not published per-server in the topology. Mixed ports across candidates break event sync and leader proxying.

### Troubleshooting

| Symptom | Likely cause |
|---|---|
| `configmap configmap-name is required` at startup | `configmap-name` is empty in `k8s-configmap.yaml`. |
| `configmap-config-file is required when type is [k8s-configmap]` | `cluster.configmap-config-file` is not set in `route-server.yaml`. |
| `failed to read configmap config file` | The path does not resolve; check `ENV_ROUTE_SERVER_CONFIG_PATH` for relative paths. |
| `configmap topology-key is required` | `topology-key` is empty; there is no built-in default. |
| `fallback-pod-discovery-enabled is true but infer-group-name is empty` | Reconciliation is on but the group name is not set. |
| `fallback-pod-discovery-enabled is true but infer-group-label-key is empty` | Reconciliation is on but the group label key is not set (no built-in default). |
| `fallback-pod-discovery-enabled is true but infer-group-role-type-label-key is empty` | Same, for the role-type label key. |
| Peers never appear | The topology `group_type` does not match `router-group-type`, or `server_ip` is empty. |
| `owner-reference not configured, cannot persist instance failures` | Failure persistence is disabled by design; set the three `owner-reference` fields to enable it. |
| `configmap get owner resource <Kind>/<name> failed` | `owner-reference` points at a missing object, **or at a cluster-scoped CRD while RouteServer looks it up with a namespace**. Owner resources must be namespaced. |
| `cannot set blockOwnerDeletion if an ownerReference refers to a resource you can't set finalizers on` | The cluster enables `OwnerReferencesPermissionEnforcement` and the ServiceAccount lacks `update` on `<owner>/finalizers`. |
| `dial tcp <ip>:0: connect: connection refused` on `request_event` | Peer `OpsPort` is 0. Peers built from the topology carry only the http port; `request_event` is an ops-port route, so event sync to the leader cannot resolve a port. Workaround: ensure `enable-ops-apis-on-service-port: true` so the route is also served on the http port, or run RouteServer builds that populate `OpsPort` on topology-derived peers. |
| `kubectl port-forward` to a RouteServer Pod fails with `connect: connection refused` | RouteServer binds its Pod IP, not loopback. Use a Service or an in-cluster proxy. |
| `configmap failed to parse topology key <key>` | The ConfigMap value is not valid JSON, or `topology-key` does not match the data key. |
