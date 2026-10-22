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

: "${MA_ETCD_NAME:?Please set MA_ETCD_NAME (e.g.: etcd-1)}"
: "${MA_ETCD_PEERS:?Please set MA_ETCD_PEERS (e.g.: etcd-1=192.168.1.10:32379,etcd-2=192.168.1.11:32379,etcd-3=192.168.1.12:32379)}"

MA_ETCD_TOKEN="${MA_ETCD_TOKEN:-etcd-cluster}"
MA_ETCD_DATA_DIR="${MA_ETCD_DATA_DIR:-/var/lib/etcd}/${MA_ETCD_NAME}"
MA_ETCD_LOG="${MA_ETCD_LOG:-/var/log/etcd/${MA_ETCD_NAME}.log}"

ETCD_IP=$(hostname -I | awk '{print $1}')
mkdir -p "${MA_ETCD_DATA_DIR}" "$(dirname "${MA_ETCD_LOG}")"
chmod 700 "${MA_ETCD_DATA_DIR}" "$(dirname "${MA_ETCD_LOG}")"

CLUSTER=""
MY_CLIENT_PORT=""
MY_PEER_PORT=""
for peer in $(echo "${MA_ETCD_PEERS}" | tr ',' ' '); do
    name="${peer%%=*}"
    addr="${peer##*=}"
    ip="${addr%%:*}"
    port="${addr##*:}"
    CLUSTER+="${name}=http://${ip}:$((port + 1)),"
    if [[ "${name}" == "${MA_ETCD_NAME}" ]]; then
        MY_CLIENT_PORT="${port}"
        MY_PEER_PORT=$((port + 1))
    fi
done
CLUSTER="${CLUSTER%,}"

: "${MY_CLIENT_PORT:?node name not found in peers. check MA_ETCD_PEERS and MA_ETCD_NAME}"

echo "Starting etcd: name=${MA_ETCD_NAME}, ip=${ETCD_IP}, client_port=${MY_CLIENT_PORT}, peer_port=${MY_PEER_PORT}"

etcd --name "${MA_ETCD_NAME}" \
    --initial-advertise-peer-urls "http://${ETCD_IP}:${MY_PEER_PORT}" \
    --listen-peer-urls "http://${ETCD_IP}:${MY_PEER_PORT}" \
    --listen-client-urls "http://${ETCD_IP}:${MY_CLIENT_PORT},http://127.0.0.1:${MY_CLIENT_PORT}" \
    --advertise-client-urls "http://${ETCD_IP}:${MY_CLIENT_PORT}" \
    --initial-cluster-token "${MA_ETCD_TOKEN}" \
    --initial-cluster "${CLUSTER}" \
    --data-dir "${MA_ETCD_DATA_DIR}" \
    --initial-cluster-state new \
    --log-outputs="${MA_ETCD_LOG}" \
    --logger=zap
