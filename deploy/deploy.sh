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

umask 077
DEPLOY_ROOT_PATH=$(cd "$(dirname "$0")"; pwd)
RS_NAME=""
PORT=9800
MAXPROCS=0
HOST=""
REMOTE_SCHEDULER_TYPE="disable"
REMOTE_SCHEDULER_ENDPOINT=""
DAEMON=""
LOG_FILE_FULL_PATH=""
PACKAGE_DIR=route-server
CANDIDATE_SWITCH="true"
ETCD_ENDPOINTS=${ETCD_ENDPOINTS}
SHOW_STD_OUTPUT="false"
INFER_MODE="PD"
INFER_MODULE_VERSION="pipeline"
READINESS_CFG=""
REUSE_PREFILLED_TOKENS="false"
OPS_PORT=""
OPS_ENABLE_HTTPS="true"
OPS_ENABLE_MTLS="true"
OPS_TLS_CERT_FILE=""
OPS_TLS_KEY_FILE=""
OPS_TLS_CA_CERT_FILE=""
TLS_CERT_FILE=""
TLS_KEY_FILE=""
TLS_CA_CERT_FILE=""
TLS_SKIP_VERIFY="false"
ENABLE_HTTPS="true"
ENABLE_MTLS="true"
# InstanceClient (vLLM) TLS
CLIENT_HTTPS_ENABLE="true"
CLIENT_MTLS_ENABLE="true"
CLIENT_CERT_FILE=""
CLIENT_KEY_FILE=""
CLIENT_CA_CERT_FILE=""
# etcd TLS
ETCD_TLS_ENABLE="true"
ETCD_MTLS_ENABLE="true"
ETCD_CERT_FILE=""
ETCD_KEY_FILE=""
ETCD_CA_CERT_FILE=""

show_help() {
  echo "Usage: $0 [OPTIONS], Script used to deploy route server"
  echo ""
  echo "Options:"
  echo "  --rs_name     name of the route server instance (default: RouteServer_<IP>_<PORT>)"
  echo "  --port        port of the http server of route server (default: 9800)"
  echo "  --host        specify the IP address of this route server instance (default: auto-detect)"
  echo "  --max-procs   env GOMAXPROCS value, default (0, ignored)"
  echo "  --remote-scheduler-type, accept <leader> or <endpoint> or disable, defaults to be disable"
  echo "  --remote-scheduler-endpoint remote endpoint of scheduler, eg. http://x.x.x.x:xxxx . \
will change remote-scheduler-type to endpoint if this option is set"
  echo "  --daemon      whether running as daemon mode, if set to true, then this script will \
wait until route-server process exists"
  echo "  --collect-metrics-unique-ip whether collecting metrics from unique instance per ip. eg. true. \
if it not set, then collecting metrics from all instances."
  echo "  --log-file-path path to route server log file, if not set, then it will use \
RS_INSTALL_DIR/<rs_name>/RouteServer/log/route-server.log, must give full real path"
  echo "  --domain      domain of leader IP , eg. localhost (default: "")"
  echo "  --use_domain_for_leader    whether to use domain to find leader (true/false) (default:false)"
  echo "  --candidate switch to config this route-server instance to candidate, accept true or false, (default: true)"
  echo "  --etcd-endpoints endpoints for etcd, separate with comma, (e.g. localhost:2379,localhost:2480 )"
  echo "  --show-std-output show route-server process stdout and stderr, takes true if shows (default: false)"
  echo "  --mode set the cluster mode of the route server (singleton/etcd) (default: "")"
  echo "  --infer-mode infer deploy mode (PD/PD-AM/EPD/Hybrid/Layerwise-CDCP/Layerwise-CPCD/VA-PD) (default: PD)"
  echo "  --infer-module-version infer module version(v0/pipeline) (default: pipeline)"
  echo "  --readiness-cfg readiness config, set the least number of instances for each role, separate with # (default: prefill:1#decode:1)"
  echo "  --reuse-prefilled-tokens enable this flag to reuse prefilled tokens for layerwise mode (default: false)"
  echo "  --ops-port    port of the ops server for admin operations (default: http-port + 10)"
  echo "  --ops-enable-https enable HTTPS for ops server. true/false (default: true)"
  echo "  --ops-enable-mtls enable mTLS for ops server (requires --ops-tls-ca-cert-file). true/false (default: true)"
  echo "  --ops-tls-cert-file path to ops TLS certificate file (required when --ops-enable-https=true, independent from --tls-cert-file)"
  echo "  --ops-tls-key-file path to ops TLS private key file (required when --ops-enable-https=true, independent from --tls-key-file)"
  echo "  --ops-tls-ca-cert-file path to ops CA certificate file"
  echo "  --tls-cert-file path to TLS certificate file (e.g. /etc/certs/server.crt) [required when enable-https=true]"
  echo "  --tls-key-file path to TLS private key file (e.g. /etc/certs/server.key) [required when enable-https=true]"
  echo "  --tls-ca-cert-file path to CA certificate file for mTLS client verification (e.g. /etc/certs/ca.crt) [required when enable-mtls=true]"
  echo "  --enable-https enable HTTPS for the main server port. true/false (default: true)"
  echo "  --enable-mtls enable mutual TLS (requires --tls-ca-cert-file). true/false (default: true)"
  echo "  --tls-skip-verify skip TLS certificate verification. true/false (default: false)"
  echo "  --client-https-enable enable HTTPS for vLLM instance client. true/false (default: true)"
  echo "  --client-mtls-enable enable mTLS for vLLM instance client. true/false (default: true)"
  echo "  --client-cert-file path to vLLM client TLS certificate file (e.g. /etc/certs/client.crt)"
  echo "  --client-key-file path to vLLM client TLS private key file (e.g. /etc/certs/client.key)"
  echo "  --client-ca-cert-file path to vLLM client CA certificate file (e.g. /etc/certs/ca.crt)"
  echo "  --etcd-tls-enable enable TLS for etcd connection. true/false (default: true)"
  echo "  --etcd-mtls-enable enable mTLS for etcd connection. true/false (default: true)"
  echo "  --etcd-cert-file path to etcd client TLS certificate file (e.g. /etc/certs/etcd-client.crt)"
  echo "  --etcd-key-file path to etcd client TLS private key file (e.g. /etc/certs/etcd-client.key)"
  echo "  --etcd-ca-cert-file path to etcd CA certificate file (e.g. /etc/certs/etcd-ca.crt)"
  echo "  --help        Show this help message"
  echo ""
  echo "Example:"
  echo "  $0 --rs_name route1 --port 30000"
  echo "Env information: "
  echo "Required Env (Only in the mode of route-table is in yaml configs)"
  echo "  PREFILL_SERVERS: prefill server list, format as <ip1:port1>,<ip2:port2>" \
       "or <name1#ip1:port1>,<name2#ip2:port2> or <name1#ip1:port1#tag>,<name2#ip2:port2#tag>" \
       "where <name> is used to set the instance name and <tag> is used to set the slo group"
  echo "  DECODE_SERVERS: decode server list, format as <ip1:port1>,<ip2:port2> "\
       "or <name1#ip1:port1>,<name2#ip2:port2> or <name1#ip1:port1#tag>,<name2#ip2:port2#tag>" \
       "where <name> is used to set the instance name and <tag> is used to set the slo group"
  echo "  MOE_SERVERS: moe server list, format as <ip1:port1>,<ip2:port2>, " \
       "it is required if infer-mode is PD-AM"
  echo "  ENCODE_SERVERS: encode server list, format as <ip1:port1>,<ip2:port2>, " \
       "it is required if infer-mode is EPD"
  echo "Optional Env "
  echo "  RS_INSTALL_DIR: install dir for route server, default: /opt/route_server"
  exit 0
}


CPU_COUNT=$(nproc)

validate_max_procs() {
    local procs=$1
    if [[ ! "$procs" =~ ^[0-9]+$  ]]; then
        echo "max-procs must be number" >&2
        return 1
    fi
    if (( procs < 0 || procs > CPU_COUNT )); then
        echo "max procs must be in [0, ${CPU_COUNT}], (0 is not set)" >&2
        return 1
    fi
    return 0
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --rs_name)
        if [ -n "$2" ] && [[ "$2" != --* ]]; then
          RS_NAME="$2"
          shift 2
        else
          echo "Error: --rs_name requires a name arg"
          exit 1
        fi
        ;;
    --port)
        if [ -n "$2" ] && [[ "$2" != --* ]]; then
            PORT="$2"
            shift 2
        else
            echo "Error: --port port requires a port arg"
            exit 1
        fi
        ;;
    --max-procs)
        if [ -n "$2" ] && [[ "$2" != --* ]]; then
          MAXPROCS="$2"
          if ! validate_max_procs "${MAXPROCS}";then
            exit 1
          fi
          shift 2
        else
          echo "Error: --max-procs requires a arg"
          exit 1
        fi
        ;;
    --remote-scheduler-endpoint)
        if [ -n "$2" ] && [[ "$2" != --* ]]; then
          REMOTE_SCHEDULER_ENDPOINT="$2"
          shift 2
        else
          echo "Error: --remote-scheduler-endpoint requires a arg"
          exit 1
        fi
        ;;
    --remote-scheduler-type)
        if [ -n "$2" ] && [[ "$2" != --* ]]; then
          if [[ "$2" != "endpoint" && "$2" != "leader" ]]; then
            echo "Error: --remote-scheduler-type only accept endpoint or leader, got $2"
            exit 1
          fi
          REMOTE_SCHEDULER_TYPE="$2"
          shift 2
        else
          echo "Error: --remote-scheduler-type requires a arg"
          exit 1
        fi
        ;;
    --collect-metrics-unique-ip)
        if [ -n "$2" ] && [[ "$2" != --* ]]; then
          if [[ "$2" != "true" && "$2" != "false" ]]; then
            echo "Error: --collect-metrics-unique-ip requires a boolean arg (true/false)"
            exit 1
          fi
          COLLECT_METRICS_UNIQUE_IP="$2"
          shift 2
        else
          echo "Error: --collect-metrics-unique-ip requires a arg"
          exit 1
        fi
        ;;
    --daemon)
        if [ -n "$2" ] && [[ "$2" != --* ]]; then
          DAEMON="$2"
          shift 2
        else
          echo "Error: --daemon requires a arg"
          exit 1
        fi
        ;;
    --log-file-path)
        if [ -n "$2" ] && [[ "$2" != --* ]]; then
          LOG_FILE_FULL_PATH="$2"
          shift 2
        else
          echo "Error: --log-file-path requires a valid path to log file"
          exit 1
        fi
        ;;
    --candidate)
        if [ -n "$2" ] && [[ "$2" != --* ]]; then
          CANDIDATE_SWITCH="$2"
          shift 2
        else
          echo "Error: --candidate requires a valid bool val"
          exit 1
        fi
        ;;
    --etcd-endpoints)
        if [ -n "$2" ] && [[ "$2" != --* ]]; then
          ETCD_ENDPOINTS="$2"
          shift 2
        else
          echo "Error: --etcd-endpoints requires a etcd address arg"
          exit 1
        fi
        ;;
    --domain)
        if [ -n "$2" ] && [[ "$2" != --* ]]; then
          DOMAIN="$2"
          shift 2
        else
          echo "Error: --domain requires a string domain name arg"
          exit 1
        fi
        ;;
    --use_domain_for_leader)
        if [ -n "$2" ] && [[ "$2" != --* ]]; then
          if [[ "$2" != "true" && "$2" != "false" ]]; then
            echo "Error: --use_domain_for_leader requires a boolean arg (true/false)"
            exit 1
          fi
          USE_DOMAIN_FOR_LEADER="$2"
          shift 2
        else
          echo "Error: --use_domain_for_leader requires a boolean arg (true/false)"
          exit 1
        fi
        ;;
    --show-std-output)
        if [ -n "$2" ] && [[ "$2" != --* ]]; then
          if [[ "$2" != "true" && "$2" != "false" ]]; then
            echo "Error: --show-std-output requires a boolean arg (true/false)"
            exit 1
          fi
          SHOW_STD_OUTPUT="$2"
          shift 2
        else
          echo "Error: --show-std-output requires a boolean arg (true/false)"
          exit 1
        fi
        ;;
  --mode)
      if [ -n "$2" ] && [[ "$2" != --* ]]; then
          MODE="$2"
          shift 2
          if [[ "$MODE" != "singleton" && "$MODE" != "etcd" ]]; then
              echo "Error: --mode must be either 'singleton' or 'etcd', got '$MODE'"
              exit 1
          fi
      else
          echo "Error: --mode requires a mode arg"
          exit 1
      fi
      ;;
  --infer-mode)
      if [ -n "$2" ] && [[ "$2" != --* ]]; then
          INFER_MODE="$2"
          shift 2
          if [[ "$INFER_MODE" != "PD" && "$INFER_MODE" != "EPD" && "$INFER_MODE" != "PD-AM" && "$INFER_MODE" != "Hybrid" && "$INFER_MODE" != "Layerwise-CDCP" && "$INFER_MODE" != "Layerwise-CPCD" && "$INFER_MODE" != "VA-PD" ]]; then
              echo "Error: --infer-mode must be one of: PD, PD-AM, EPD, Hybrid, Layerwise-CDCP, Layerwise-CPCD, VA-PD, got '$INFER_MODE'"
              exit 1
          fi
      else
          echo "Error: --infer-mode requires an infer-mode arg"
          exit 1
      fi
      ;;
  --infer-module-version)
      if [ -n "$2" ] && [[ "$2" != --* ]]; then
          INFER_MODULE_VERSION="$2"
          shift 2
          if [[ "$INFER_MODULE_VERSION" != "v0" && "$INFER_MODULE_VERSION" != "pipeline" ]]; then
                  echo "Error: --infer-module-version must be either 'v0' or 'pipeline', got '$INFER_MODULE_VERSION'"
                  exit 1
          fi
      else
          echo "Error: --infer-module-version requires an infer-module-version arg"
          exit 1
      fi
      ;;
  --readiness-cfg)
      if [ -n "$2" ] && [[ "$2" != --* ]]; then
          READINESS_CFG="$2"
          shift 2
      else
          echo "Error: --readiness-cfg requires a readiness-cfg arg"
          exit 1
      fi
      ;;
  --reuse-prefilled-tokens)
      REUSE_PREFILLED_TOKENS="true"
      shift
      ;;
  --ops-port)
      if [ -n "$2" ] && [[ "$2" != --* ]]; then
          OPS_PORT="$2"
          shift 2
      else
          echo "Error: --ops-port requires a port arg"
          exit 1
      fi
      ;;
  --ops-enable-https)
      if [ -n "$2" ] && [[ "$2" != --* ]]; then
        if [[ "$2" != "true" && "$2" != "false" ]]; then
          echo "Error: --ops-enable-https requires a boolean arg (true/false)"
          exit 1
        fi
        OPS_ENABLE_HTTPS="$2"
        shift 2
      else
        echo "Error: --ops-enable-https requires a boolean arg (true/false)"
        exit 1
      fi
      ;;
  --ops-enable-mtls)
      if [ -n "$2" ] && [[ "$2" != --* ]]; then
        if [[ "$2" != "true" && "$2" != "false" ]]; then
          echo "Error: --ops-enable-mtls requires a boolean arg (true/false)"
          exit 1
        fi
        OPS_ENABLE_MTLS="$2"
        shift 2
      else
        echo "Error: --ops-enable-mtls requires a boolean arg (true/false)"
        exit 1
      fi
      ;;
  --enable-https)
      if [ -n "$2" ] && [[ "$2" != --* ]]; then
        if [[ "$2" != "true" && "$2" != "false" ]]; then
          echo "Error: --enable-https requires a boolean arg (true/false)"
          exit 1
        fi
        ENABLE_HTTPS="$2"
        shift 2
      else
        echo "Error: --enable-https requires a boolean arg (true/false)"
        exit 1
      fi
      ;;
  --enable-mtls)
      if [ -n "$2" ] && [[ "$2" != --* ]]; then
        if [[ "$2" != "true" && "$2" != "false" ]]; then
          echo "Error: --enable-mtls requires a boolean arg (true/false)"
          exit 1
        fi
        ENABLE_MTLS="$2"
        shift 2
      else
        echo "Error: --enable-mtls requires a boolean arg (true/false)"
        exit 1
      fi
      ;;
  --client-https-enable)
      if [ -n "$2" ] && [[ "$2" != --* ]]; then
        if [[ "$2" != "true" && "$2" != "false" ]]; then
          echo "Error: --client-https-enable requires a boolean arg (true/false)"
          exit 1
        fi
        CLIENT_HTTPS_ENABLE="$2"
        shift 2
      else
        echo "Error: --client-https-enable requires a boolean arg (true/false)"
        exit 1
      fi
      ;;
  --client-mtls-enable)
      if [ -n "$2" ] && [[ "$2" != --* ]]; then
        if [[ "$2" != "true" && "$2" != "false" ]]; then
          echo "Error: --client-mtls-enable requires a boolean arg (true/false)"
          exit 1
        fi
        CLIENT_MTLS_ENABLE="$2"
        shift 2
      else
        echo "Error: --client-mtls-enable requires a boolean arg (true/false)"
        exit 1
      fi
      ;;
  --client-cert-file)
      if [ -n "$2" ] && [[ "$2" != --* ]]; then
        CLIENT_CERT_FILE="$2"
        shift 2
      else
        echo "Error: --client-cert-file requires a file path arg"
        exit 1
      fi
      ;;
  --client-key-file)
      if [ -n "$2" ] && [[ "$2" != --* ]]; then
        CLIENT_KEY_FILE="$2"
        shift 2
      else
        echo "Error: --client-key-file requires a file path arg"
        exit 1
      fi
      ;;
  --client-ca-cert-file)
      if [ -n "$2" ] && [[ "$2" != --* ]]; then
        CLIENT_CA_CERT_FILE="$2"
        shift 2
      else
        echo "Error: --client-ca-cert-file requires a file path arg"
        exit 1
      fi
      ;;
  --etcd-tls-enable)
      if [ -n "$2" ] && [[ "$2" != --* ]]; then
        if [[ "$2" != "true" && "$2" != "false" ]]; then
          echo "Error: --etcd-tls-enable requires a boolean arg (true/false)"
          exit 1
        fi
        ETCD_TLS_ENABLE="$2"
        shift 2
      else
        echo "Error: --etcd-tls-enable requires a boolean arg (true/false)"
        exit 1
      fi
      ;;
  --etcd-mtls-enable)
      if [ -n "$2" ] && [[ "$2" != --* ]]; then
        if [[ "$2" != "true" && "$2" != "false" ]]; then
          echo "Error: --etcd-mtls-enable requires a boolean arg (true/false)"
          exit 1
        fi
        ETCD_MTLS_ENABLE="$2"
        shift 2
      else
        echo "Error: --etcd-mtls-enable requires a boolean arg (true/false)"
        exit 1
      fi
      ;;
  --etcd-cert-file)
      if [ -n "$2" ] && [[ "$2" != --* ]]; then
        ETCD_CERT_FILE="$2"
        shift 2
      else
        echo "Error: --etcd-cert-file requires a file path arg"
        exit 1
      fi
      ;;
  --etcd-key-file)
      if [ -n "$2" ] && [[ "$2" != --* ]]; then
        ETCD_KEY_FILE="$2"
        shift 2
      else
        echo "Error: --etcd-key-file requires a file path arg"
        exit 1
      fi
      ;;
  --etcd-ca-cert-file)
      if [ -n "$2" ] && [[ "$2" != --* ]]; then
        ETCD_CA_CERT_FILE="$2"
        shift 2
      else
        echo "Error: --etcd-ca-cert-file requires a file path arg"
        exit 1
      fi
      ;;
  --ops-tls-cert-file)
      if [ -n "$2" ] && [[ "$2" != --* ]]; then
        OPS_TLS_CERT_FILE="$2"
        shift 2
      else
        echo "Error: --ops-tls-cert-file requires a file path arg"
        exit 1
      fi
      ;;
  --ops-tls-key-file)
      if [ -n "$2" ] && [[ "$2" != --* ]]; then
        OPS_TLS_KEY_FILE="$2"
        shift 2
      else
        echo "Error: --ops-tls-key-file requires a file path arg"
        exit 1
      fi
      ;;
  --ops-tls-ca-cert-file)
      if [ -n "$2" ] && [[ "$2" != --* ]]; then
        OPS_TLS_CA_CERT_FILE="$2"
        shift 2
      else
        echo "Error: --ops-tls-ca-cert-file requires a file path arg"
        exit 1
      fi
      ;;
  --tls-cert-file)
      if [ -n "$2" ] && [[ "$2" != --* ]]; then
        TLS_CERT_FILE="$2"
        shift 2
      else
        echo "Error: --tls-cert-file requires a file path arg"
        exit 1
      fi
      ;;
  --tls-key-file)
      if [ -n "$2" ] && [[ "$2" != --* ]]; then
        TLS_KEY_FILE="$2"
        shift 2
      else
        echo "Error: --tls-key-file requires a file path arg"
        exit 1
      fi
      ;;
  --tls-ca-cert-file)
      if [ -n "$2" ] && [[ "$2" != --* ]]; then
        TLS_CA_CERT_FILE="$2"
        shift 2
      else
        echo "Error: --tls-ca-cert-file requires a file path arg"
        exit 1
      fi
      ;;
  --tls-skip-verify)
      if [ -n "$2" ] && [[ "$2" != --* ]]; then
        if [[ "$2" != "true" && "$2" != "false" ]]; then
          echo "Error: --tls-skip-verify requires a boolean arg (true/false)"
          exit 1
        fi
        TLS_SKIP_VERIFY="$2"
        shift 2
      else
        echo "Error: --tls-skip-verify requires a boolean arg (true/false)"
        exit 1
      fi
      ;;
  --host)
      if [ -n "$2" ] && [[ "$2" != --* ]]; then
        HOST="$2"
        shift 2
      else
        echo "Error: --host requires an IP address arg"
        exit 1
      fi
      ;;
    --help)
        show_help
        ;;
    *)
      echo "Error: Unknown option '$1'"
      show_help
      ;;
  esac
done

get_ip() {
  local ip
  ip=$(hostname -I 2>/dev/null | awk '{print $1}')
  if [ -z "$ip" ]; then
    echo "Failed to get host ip" >&2
    exit 1
  fi

  if ! [[ $ip =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
    echo "Failed to get host ip" >&2
    exit 1
  fi
  echo "$ip"
}

if [ -z "${RS_NAME}" ]; then
  echo Failed: RS_NAME is required
  ip=$(get_ip)
  if [ -z "$ip" ]; then
    echo "Failed: No rs_name is given, failed to get host ip to assign name"
    exit 1
  fi
  RS_NAME=RouteServer_"${ip//./_}"_"${PORT}"
  echo rs_name is not given, assigning default name to "${RS_NAME}"
fi

if [ ! -d "${DEPLOY_ROOT_PATH}/${PACKAGE_DIR}" ]; then
  echo Failed: package dir ${PACKAGE_DIR} not found in deploy dir, check release pkg version
  exit 1
fi

if [ ! -f "${DEPLOY_ROOT_PATH}/route-server-template.yaml" ]; then
  echo Failed: "${DEPLOY_ROOT_PATH}/route-server-template.yaml" not found in deploy dir, check release pkg version
  exit 1
fi

if [ ! -f "${DEPLOY_ROOT_PATH}/prometheus-template.yaml" ]; then
  echo Failed: "${DEPLOY_ROOT_PATH}/prometheus-template.yaml" not found in deploy dir, check release pkg version
  exit 1
fi

if [ ! -f "${DEPLOY_ROOT_PATH}/config_generator" ]; then
  echo Failed: config_generator not found in deploy dir, check release pkg version
  exit 1
fi

if [ -z "${PORT}" ]; then
  PORT=9800
  echo "RouteServer serving port not set, set to default ${PORT}"
fi

if [ -z "${RS_INSTALL_DIR}" ]; then
  RS_INSTALL_DIR="/opt/huawei"
  echo "Env RS_INSTALL_DIR not set, set to default location ${RS_INSTALL_DIR}"
fi

if [ -z "${USE_DOMAIN_FOR_LEADER}" ]; then
  USE_DOMAIN_FOR_LEADER=false
  echo "USE_DOMAIN_FOR_LEADER not set, set to default ${USE_DOMAIN_FOR_LEADER}"
fi

if [ -z "${DOMAIN}" ]; then
  DOMAIN=""
  echo "DOMAIN not set, set to default ${DOMAIN}"
fi

if [ "${INFER_MODE}" = "PD-AM" ] && [ -z "${MOE_SERVERS}" ] ; then
    echo Failed: environment MOE_SERVERS is required when infer-mode is PD-AM
    exit 1
fi

if [ "${INFER_MODE}" = "EPD" ] && [ -z "${ENCODE_SERVERS}" ] ; then
    echo Failed: environment ENCODE_SERVERS is required when infer-mode is EPD
    exit 1
fi

RS_PATH=${RS_INSTALL_DIR}/${RS_NAME}

if [ ! -d "${RS_PATH}" ]; then
  mkdir -p "${RS_PATH}"
fi

echo install RouteServer to "${RS_PATH}"
if [ -d "${RS_PATH}/${PACKAGE_DIR}/bin" ]; then
  chmod u+w "${RS_PATH}/${PACKAGE_DIR}/bin"
fi
cp -rf "${DEPLOY_ROOT_PATH}/${PACKAGE_DIR}" "${RS_PATH}/"

export ENV_ROUTE_SERVER_CONFIG_PATH="${RS_PATH}/${PACKAGE_DIR}/config"
export ENV_ROUTE_SERVER_LOG_PATH="${RS_PATH}/${PACKAGE_DIR}/log"
mkdir -p "${ENV_ROUTE_SERVER_CONFIG_PATH}"
mkdir -p "${ENV_ROUTE_SERVER_LOG_PATH}"

echo kill existing route server if exists
ps -ef | grep "${RS_PATH}/${PACKAGE_DIR}" | grep -v grep | awk -F ' ' '{print $2}' | xargs kill

echo wait for kill to take effect
sleep 1

RS_CONFIG_DIR="${RS_PATH}/${PACKAGE_DIR}"/config
cp "${DEPLOY_ROOT_PATH}"/metrics_config*.yaml "${RS_PATH}"/route-server/config/
rm -rf "${ENV_ROUTE_SERVER_CONFIG_PATH}"/pipelines/*
cp -rf "${DEPLOY_ROOT_PATH}"/pipelines "${ENV_ROUTE_SERVER_CONFIG_PATH}/"

# Always process pipeline configs for prefill token settings
metaserver_cdcp_config="${ENV_ROUTE_SERVER_CONFIG_PATH}/pipelines/metaserver_cdcp.yaml"
layerwise_cdcp_config="${ENV_ROUTE_SERVER_CONFIG_PATH}/pipelines/layerwise_cdcp.yaml"
metaserver_cpcd_config="${ENV_ROUTE_SERVER_CONFIG_PATH}/pipelines/metaserver_cpcd.yaml"
layerwise_cpcd_config="${ENV_ROUTE_SERVER_CONFIG_PATH}/pipelines/layerwise_cpcd.yaml"

if [ "${REUSE_PREFILLED_TOKENS}" = "true" ]; then
  echo "REUSE_PREFILLED_TOKENS is enabled, setting prefill_stream functions..."

  # Process CDCP configs
  if [ -f "$metaserver_cdcp_config" ]; then
    sed -i 's/prefill_prepare_request/prefill_stream_prepare_request/g' "$metaserver_cdcp_config"
    sed -i 's/prefill_post_response/prefill_stream_post_response/g' "$metaserver_cdcp_config"
  fi

  # Process CPCD configs
  if [ -f "$layerwise_cpcd_config" ]; then
    sed -i 's/prefill_prepare_request/prefill_stream_prepare_request/g' "$layerwise_cpcd_config"
    sed -i 's/prefill_post_response/prefill_stream_post_response/g' "$layerwise_cpcd_config"
  fi
else
  echo "REUSE_PREFILLED_TOKENS is disabled, setting non-stream prefill functions..."

  # Process CDCP configs
  if [ -f "$metaserver_cdcp_config" ]; then
    sed -i 's/prefill_stream_prepare_request/prefill_prepare_request/g' "$metaserver_cdcp_config"
    sed -i 's/prefill_stream_post_response/prefill_post_response/g' "$metaserver_cdcp_config"
  fi

  # Process CPCD configs
  if [ -f "$layerwise_cpcd_config" ]; then
    sed -i 's/prefill_stream_prepare_request/prefill_prepare_request/g' "$layerwise_cpcd_config"
    sed -i 's/prefill_stream_post_response/prefill_post_response/g' "$layerwise_cpcd_config"
  fi
fi

pd_config="${ENV_ROUTE_SERVER_CONFIG_PATH}/pipelines/pd_separate.yaml"

if [ "${INFER_MODE}" = "VA-PD" ]; then
  echo "VA-PD mode enabled, applying vllm_ascend pipeline customizations..."
  if [ -f "$pd_config" ]; then
    sed -i '/- prefill_prepare_request$/a\          - vllm_ascend_prepare_request' "$pd_config"
    sed -i '/- prefill_stream_prepare_request$/a\          - vllm_ascend_prepare_request' "$pd_config"
    sed -i 's/type: default_fail/type: vllm_ascend_fail/g' "$pd_config"
  fi
  INFER_MODE="PD"
fi

if [ -z "$COLLECT_METRICS_UNIQUE_IP" ] || [ "$COLLECT_METRICS_UNIQUE_IP" = "false" ]; then
  echo "collect-metrics-unique-ip is null, so collect metrics form all instances."
  sed -i 's/collect_from_uniq_ip: .*/collect_from_uniq_ip: false/' "${RS_PATH}"/route-server/config/metrics_config*.yaml
else
  echo "collect-metrics-unique-ip is true, so collect metrics form one unique instances per node."
  sed -i 's/collect_from_uniq_ip: .*/collect_from_uniq_ip: true/' "${RS_PATH}"/route-server/config/metrics_config*.yaml
fi

# when enable-https=true, cert-file and key-file is required
if [[ "${ENABLE_HTTPS}" == "true" ]]; then
  if [[ -z "${TLS_CERT_FILE}" || -z "${TLS_KEY_FILE}" ]]; then
    echo "Error: --tls-cert-file and --tls-key-file are required when --enable-https=true"
    exit 1
  fi
fi
# Certificate validation: ca-cert-file is required when both enable-https and enable-mtls are true
if [[ "${ENABLE_HTTPS}" == "true" && "${ENABLE_MTLS}" == "true" ]]; then
  if [[ -z "${TLS_CA_CERT_FILE}" ]]; then
    echo "Error: --tls-ca-cert-file is required when --enable-https=true and --enable-mtls=true"
    exit 1
  fi
fi

# when ops-enable-https=true, cert-file and key-file is required
if [[ "${OPS_ENABLE_HTTPS}" == "true" ]]; then
  if [[ -z "${OPS_TLS_CERT_FILE}" || -z "${OPS_TLS_KEY_FILE}" ]]; then
    echo "Error: --ops-tls-cert-file and --ops-tls-key-file are required when --ops-enable-https=true"
    exit 1
  fi
fi
# Certificate validation: ca-cert-file is required when both ops-enable-https and ops-enable-mtls are true
if [[ "${OPS_ENABLE_HTTPS}" == "true" && "${OPS_ENABLE_MTLS}" == "true" ]]; then
  if [[ -z "${OPS_TLS_CA_CERT_FILE}" ]]; then
    echo "Error: --ops-tls-ca-cert-file is required when --ops-enable-https=true and --ops-enable-mtls=true"
    exit 1
  fi
fi

if [ -f "${RS_CONFIG_DIR}"/route-server.yaml ]; then
  echo "route-server.yaml exists in config dir, backup and regenerate"
  mv "${RS_CONFIG_DIR}"/route-server.yaml "${RS_CONFIG_DIR}"/route-server.yaml.bak
fi

chmod +x "${DEPLOY_ROOT_PATH}"/config_generator
"${DEPLOY_ROOT_PATH}"/config_generator \
  --name "${RS_NAME}" \
  --template "${DEPLOY_ROOT_PATH}"/route-server-template.yaml \
  --target "${RS_PATH}/${PACKAGE_DIR}" \
  --port "${PORT}" \
  --host "${HOST}" \
  --encodes "${ENCODE_SERVERS}" \
  --prefills "${PREFILL_SERVERS}" \
  --decodes "${DECODE_SERVERS}" \
  --moes "${MOE_SERVERS}" \
  --static-infos "max_num_seqs#54"\
  --remote-scheduler-type "${REMOTE_SCHEDULER_TYPE}" \
  --remote-scheduler-endpoint "${REMOTE_SCHEDULER_ENDPOINT}" \
  --etcd-endpoints "${ETCD_ENDPOINTS}" \
  --log-file-path "${LOG_FILE_FULL_PATH}" \
  --candidate "${CANDIDATE_SWITCH}" \
  --domain "${DOMAIN}" \
  --use-domain-for-leader "${USE_DOMAIN_FOR_LEADER}" \
  --mode "${MODE}" \
  --infer-mode "${INFER_MODE}" \
  --infer-module-version "${INFER_MODULE_VERSION}" \
  --readiness-cfg "${READINESS_CFG}" \
  ${OPS_PORT:+--ops-port "${OPS_PORT}"} \
  --ops-enable-https "${OPS_ENABLE_HTTPS}" \
  --ops-enable-mtls "${OPS_ENABLE_MTLS}" \
  --ops-tls-cert-file "${OPS_TLS_CERT_FILE}" \
  --ops-tls-key-file "${OPS_TLS_KEY_FILE}" \
  --ops-tls-ca-cert-file "${OPS_TLS_CA_CERT_FILE}" \
  --tls-cert-file "${TLS_CERT_FILE}" \
  --tls-key-file "${TLS_KEY_FILE}" \
  --tls-ca-cert-file "${TLS_CA_CERT_FILE}" \
  --enable-https "${ENABLE_HTTPS}" \
  --enable-mtls "${ENABLE_MTLS}" \
  --tls-skip-verify "${TLS_SKIP_VERIFY}" \
  --client-https-enable "${CLIENT_HTTPS_ENABLE}" \
  --client-mtls-enable "${CLIENT_MTLS_ENABLE}" \
  --client-cert-file "${CLIENT_CERT_FILE}" \
  --client-key-file "${CLIENT_KEY_FILE}" \
  --client-ca-cert-file "${CLIENT_CA_CERT_FILE}" \
  --etcd-tls-enable "${ETCD_TLS_ENABLE}" \
  --etcd-mtls-enable "${ETCD_MTLS_ENABLE}" \
  --etcd-cert-file "${ETCD_CERT_FILE}" \
  --etcd-key-file "${ETCD_KEY_FILE}" \
  --etcd-ca-cert-file "${ETCD_CA_CERT_FILE}"

cp "${DEPLOY_ROOT_PATH}/prometheus-template.yaml" "${RS_PATH}/${PACKAGE_DIR}/config"

export ENV_ROUTE_SERVER_CONFIG_PATH="${RS_PATH}/route-server/config"

echo "config file path is ${ENV_ROUTE_SERVER_CONFIG_PATH}" > "${ENV_ROUTE_SERVER_LOG_PATH}/start.log"

if (( MAXPROCS > 0 ));then
  export GOMAXPROCS="${MAXPROCS}"
  echo "GOMAXPROCS is set to ${MAXPROCS}" >> "${ENV_ROUTE_SERVER_LOG_PATH}/start.log"
else
  echo "GOMAXPROCS is ${GOMAXPROCS}" >> "${ENV_ROUTE_SERVER_LOG_PATH}/start.log"
fi

chmod 500 "${RS_PATH}/${PACKAGE_DIR}"/bin
chmod 500 "${RS_PATH}/${PACKAGE_DIR}"/bin/route-server

if [[ "${SHOW_STD_OUTPUT}" == "true" ]];then
  echo "SHOW_STD_OUTPUT is set to true, output to stdout and stderr"
  "${RS_PATH}/${PACKAGE_DIR}"/bin/route-server &
else
  echo "SHOW_STD_OUTPUT is false, output to dev/null"
  nohup "${RS_PATH}/${PACKAGE_DIR}"/bin/route-server > /dev/null 2>&1 &
fi

PID=$!

echo pid is ${PID}, waiting for 5 sec to check.

sleep 5

if ! ps -p "${PID}" > /dev/null; then
  wait "${PID}"
  status=$?
  echo "Failed: RouteServer process exit, retCode: $status (previous PID: ${PID}), check logs at ${ENV_ROUTE_SERVER_LOG_PATH}"
  exit 1
fi

if [ -z "$DAEMON" ]; then
  echo "RouteServer process is running (PID: ${PID}), --daemon is not set, exit with 0"
  exit 0
else
  echo "--daemon is on, waiting for process ${PID} to exit"
  wait "${PID}"
fi