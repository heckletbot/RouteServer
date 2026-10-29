#!/usr/bin/env python
# -*- coding: utf-8 -*-
#
# Copyright 2026 The RouteServer Authors.
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

import argparse
import json
import logging
import sys
import requests
import base64
from typing import List, Dict

import utils

# ETCD key constants - matching Go code exactly
RS_CLUSTER_STATE_ALL_INSTANCES_DIR = "/rs/cluster/state/instances/all/"
RS_INSTANCE_FAILURES_KEY = "/rs/instances/failures"


def init_logger(args):
    logging.basicConfig(
        filename=args.log_file,
        level=logging.INFO,
        format='%(asctime)s %(levelname)s: %(message)s'
    )


def generate_all_etcd_instance_keys(args) -> List[str]:
    instance_ips = args.instance_ips.replace(" ", "").split(",")
    api_server_ports = args.api_server_ports.replace(" ", "").split(",")

    instance_keys = []

    if len(instance_ips) != len(api_server_ports):
        raise ValueError(f"instance_ips and api_server_ports length mismatch: "
                        f"ips={len(instance_ips)}, ports={len(api_server_ports)}")

    if args.role == "prefill":
        for index, ip in enumerate(instance_ips):
            if index >= len(api_server_ports):
                raise ValueError(f"Index {index} out of range for api_server_ports with length {len(api_server_ports)}")
            port = int(api_server_ports[index])
            instance_name = utils.generate_instance_name(args.role, ip, port)
            instance_keys.append(instance_name)
    else:
        if args.ascend_rt_visible_devices_num is None or args.ascend_rt_visible_devices_num <= 0:
            raise ValueError(f"ascend_rt_visible_devices_num must be positive for role '{args.role}', "
                            f"got {args.ascend_rt_visible_devices_num}")

        for index, ip in enumerate(instance_ips):
            if index >= len(api_server_ports):
                raise ValueError(f"Index {index} out of range for api_server_ports with length {len(api_server_ports)}")
            start_port = int(api_server_ports[index])
            for num in range(args.ascend_rt_visible_devices_num):
                port = start_port + num
                instance_name = utils.generate_instance_name(args.role, ip, port)
                instance_keys.append(instance_name)

    return instance_keys


class EtcdInstanceChecker:
    def __init__(self, endpoints: List[str]):
        self.endpoints = endpoints
        self.session = requests.Session()
        self._connect()

    def __enter__(self):
        return self

    def __exit__(self, exc_type, exc_val, exc_tb):
        self.close()

    def close(self):
        """Close the session and clean up resources"""
        if hasattr(self, 'session') and self.session:
            self.session.close()

    def _try_all_endpoints(self, path: str, method: str = "get", data: dict = None, timeout: int = 5):
        """Try endpoints in order with fallback, return the first successful response.
        Both connection failures and HTTP error responses trigger fallback to the next endpoint.
        Raises if none of the endpoints respond successfully."""
        for endpoint in self.endpoints:
            url = f"http://{endpoint}{path}"
            try:
                if method == "get":
                    response = self.session.get(url, timeout=timeout)
                else:
                    response = self.session.post(url, json=data, timeout=timeout)
                response.raise_for_status()
                return response
            except requests.exceptions.RequestException as e:
                logging.warning(f"Failed to reach etcd endpoint {endpoint}: {e}")
                continue
        raise requests.exceptions.RequestException(f"All etcd endpoints unreachable: {self.endpoints}")

    def _connect(self):
        try:
            # Use GET request to check health status
            # Normal response example: {"health": "true"}
            response = self._try_all_endpoints("/health", method="get", timeout=5)

            if response.status_code == 200:
                health_data = response.json()
                if health_data.get("health") != "true":
                    logging.warning(f"etcd reported unhealthy status: {health_data}")
                    exit(0)
            else:
                logging.warning(f"HTTP {response.status_code}: {response.text}")
                exit(0)

        except requests.exceptions.RequestException:
            logging.warning(f"Failed to connect to etcd, skip checking")
            exit(0)

    def _get_prefix(self, prefix: str) -> Dict[str, Dict]:
        try:
            data = {
                "key": base64.b64encode(prefix.encode('utf-8')).decode('utf-8'),
                "range_end": base64.b64encode(self._get_range_end(prefix).encode('utf-8')).decode('utf-8')
            }

            response = self._try_all_endpoints("/v3/kv/range", method="post", data=data, timeout=10)

            result = response.json()

            all_instances = {}

            if 'kvs' in result:
                for kv in result['kvs']:
                    key = base64.b64decode(kv['key']).decode('utf-8')
                    value = base64.b64decode(kv['value']).decode('utf-8')

                    instance_name = key.split('/')[-1]

                    instance_info = json.loads(value)

                    all_instances[instance_name] = instance_info

            return all_instances

        except Exception as e:
            logging.error(f"[ERROR] Failed to fetch instances from etcd: {e}")
            exit(0)

    def _get_range_end(self, prefix: str) -> str:
        prefix_bytes = list(prefix.encode('utf-8'))

        if not prefix_bytes:
            return "\x00"

        for i in range(len(prefix_bytes) - 1, -1, -1):
            if prefix_bytes[i] < 0xff:
                prefix_bytes[i] += 1
                return bytes(prefix_bytes[:i + 1]).decode('utf-8', errors='ignore')

        return "\x00"

    def _get_single(self, key: str) -> Dict:
        try:
            data = {
                "key": base64.b64encode(key.encode('utf-8')).decode('utf-8')
            }

            response = self._try_all_endpoints("/v3/kv/range", method="post", data=data, timeout=10)

            result = response.json()

            if 'kvs' in result and len(result['kvs']) > 0:
                value = base64.b64decode(result['kvs'][0]['value']).decode('utf-8')
                return json.loads(value)

            return {}

        except Exception as e:
            logging.error(f"[ERROR] Failed to fetch key {key} from etcd: {e}")
            exit(0)

    def get_all_instance_keys(self):
        return self._get_prefix(RS_CLUSTER_STATE_ALL_INSTANCES_DIR)

    def get_instance_failures(self) -> Dict[str, Dict]:
        failures_data = self._get_single(RS_INSTANCE_FAILURES_KEY)
        return failures_data.get('failures', {})

    def check_instance_failure_threshold(self, max_threshold: int, available_instances: List[str]) -> bool:
        failures_data = self.get_instance_failures()
        failed_instances = []

        for instance_name in available_instances:
            if instance_name in failures_data:
                failure_info = failures_data[instance_name]
                health_check_count = failure_info.get('health_check_count', 0)

                if health_check_count > max_threshold:
                    failed_instances.append(instance_name)

        all_failed = len(failed_instances) == len(available_instances) and len(failed_instances) > 0

        return all_failed

    def is_instance_started(self, args, expected_instance_keys: List[str]) -> bool:
        available_instances = []
        instances_in_etcd = self.get_all_instance_keys()

        for instance_name in expected_instance_keys:
            if instance_name in instances_in_etcd:
                available_instances.append(instance_name)

        # all expected instances should be found in etcd
        if len(available_instances) != len(expected_instance_keys):
            logging.error("all instances should be found in ETCD")
            return False

        return True

    def is_instance_alive(self, args, expected_instance_keys: List[str]) -> bool:
        available_instances = []
        instances_in_etcd = self.get_all_instance_keys()

        for instance_name in expected_instance_keys:
            if instance_name in instances_in_etcd:
                available_instances.append(instance_name)

        # alive when at least one instance in etcd
        if len(available_instances) == 0:
            logging.error("no instance found in ETCD")
            return False

        all_failed = self.check_instance_failure_threshold(args.max_health_check_threshold, available_instances)

        if all_failed:
            logging.error("all instances irresponsible")
            return False

        return True



def main():
    parser = argparse.ArgumentParser(description='router server Etcd Instance Status Checker (No etcd3 library)')
    parser.add_argument('--instance-ips', type=str, required=True,
                        help='instance ips need to be checked for health status, multiple ips are separated by '
                             'commas')
    parser.add_argument('--role', type=str, required=True,
                        help='instance role')
    parser.add_argument('--api-server-ports', type=str, required=True,
                        help='api server port of prefill or api server start port of decode, encode, moe, multiple ports are '
                             'separated by commas, it needs to correspond to the param instance-ips')
    parser.add_argument('--ascend-rt-visible-devices-num', type=int, default=16,
                        help='visible devices num of one node')
    parser.add_argument('--log-file', type=str, default=None,
                        help='log file')
    parser.add_argument('--probe-type', type=str, choices=["startup", "liveness"], default="liveness",
                        help='probe type, startup or liveness')
    parser.add_argument('--etcd-endpoints', type=str, default='127.0.0.1:62379',
                        help='etcd endpoints, multiple endpoints separated by commas (default: 127.0.0.1:62379)')
    parser.add_argument('--max-health-check-threshold', type=int, default=180,
                        help='maximum health check count after marking as failure (default: 180)')

    args = parser.parse_args()

    if not args.instance_ips:
        sys.exit(0)

    init_logger(args)

    try:
        utils.check_params(args)
        # Generate all expected etcd instance keys
        expected_instance_keys = generate_all_etcd_instance_keys(args)

        etcd_endpoints = [e for e in args.etcd_endpoints.replace(" ", "").split(",") if e]
        if not etcd_endpoints:
            logging.error("No etcd endpoints provided")
            sys.exit(1)
        for endpoint in etcd_endpoints:
            parts = endpoint.split(":")
            if len(parts) != 2 or not utils.valid_port(parts[1]):
                logging.error(f"Invalid etcd endpoint format: {endpoint}, expected 'ip:port'")
                sys.exit(1)
        # Use context manager to ensure session is properly closed
        with EtcdInstanceChecker(etcd_endpoints) as checker:
            if args.probe_type == "startup":
                check_pass = checker.is_instance_started(args, expected_instance_keys)
            elif args.probe_type == "liveness":
                check_pass = checker.is_instance_alive(args, expected_instance_keys)
            else:
                check_pass = True

        if not check_pass:
            sys.exit(1)

        logging.info(f"instances work properly")

    except Exception as e:
        logging.error(f"check failed: {e}")
        sys.exit(1)


if __name__ == '__main__':
    main()