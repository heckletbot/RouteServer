#!/bin/bash
#
# Copyright 2026 The vLLM-Cloud Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

set -euo pipefail

: "${RS_ETCD_NAME:?Please set RS_ETCD_NAME (e.g.: etcd-1)}"
: "${RS_ETCD_PEERS:?Please set RS_ETCD_PEERS (e.g.: etcd-1=192.168.1.10:32379,etcd-2=192.168.1.11:32379,etcd-3=192.168.1.12:32379)}"

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
    --logger=zap
