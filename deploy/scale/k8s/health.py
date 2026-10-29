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
import logging
import re
import sys
import requests
from concurrent.futures import ThreadPoolExecutor, as_completed
import utils

def init_logger(args):
    logging.basicConfig(
        filename=args.log_file,
        level=logging.INFO,
        format='%(asctime)s %(levelname)s: %(message)s'
    )

def check_single_api(url):
    try:
        response = requests.get(url, timeout=5)
        if response.status_code == 200:
            return True, url
        else:
            return False, f"{url} (Status: {response.status_code})"
    except Exception as e:
        return False, f"{url} (Error: {str(e)})"


def check_all_api_servers(args):
    instance_ips = args.instance_ips.replace(" ", "").split(",")
    api_server_ports = args.api_server_ports.replace(" ", "").split(",")
    urls_to_check = []
    if args.role == "prefill":
        for index, ip in enumerate(instance_ips):
            port = int(api_server_ports[index])
            url = ip + ":" + str(port)
            urls_to_check.append(f"http://{url}/health")
    else:
        for index, ip in enumerate(instance_ips):
            start_port = int(api_server_ports[index])
            for num in range(args.ascend_rt_visible_devices_num):
                port = start_port + num
                url = ip + ":" + str(port)
                urls_to_check.append(f"http://{url}/health")

    success = True
    failed_details = []
    num_workers = min(len(args.api_server_ports), 1)

    with ThreadPoolExecutor(num_workers) as executor:
        future_to_url = {executor.submit(check_single_api, url): url for url in urls_to_check}

        for future in as_completed(future_to_url):
            is_ok, info = future.result()
            if not is_ok:
                success = False
                failed_details.append(info)

    if success:
        sys.exit(0)
    else:
        logging.error(f"health check failed for: {', '.join(failed_details)}")
        sys.exit(1)


def main():
    parser = argparse.ArgumentParser()
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

    args = parser.parse_args()
    if not args.instance_ips:
        sys.exit(0)
    init_logger(args)

    try:
        utils.check_params(args)
        check_all_api_servers(args)
    except Exception as e:
        logging.error(f"health check failed for {e}")
        sys.exit(1)


if __name__ == "__main__":
    main()
