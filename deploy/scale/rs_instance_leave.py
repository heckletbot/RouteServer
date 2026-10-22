#!/usr/bin/env python
# -*- coding: utf-8 -*-
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

import argparse
import copy
import json
import logging
import re
import sys
import time

import requests

RETRY_TIME = 3


def init_logger(args):
    logging.basicConfig(
        filename=args.log_file,
        level=logging.INFO,
        format='%(asctime)s %(levelname)s: %(message)s'
    )


def request_post(url, body):
    headers = {"content-type": "application/json"}
    try:
        res = json.loads(requests.post(url, json=body, headers=headers, timeout=15).text)
    except Exception as e:
        raise Exception(f"can not access to the url {url}: {str(e)}")
    return res


def request_get(url):
    try:
        res = requests.get(url, timeout=15).content.decode('utf-8')
    except Exception as e:
        raise Exception(f"can not access to the url {url}: {str(e)}")
    return res


def valid_port(port):
    if int(port) <= 0 or int(port) > 65535:
        return False
    return True


def check_params(args):
    ip_pattern = r'^((25[0-5]|2[0-4][0-9]|[01]?[0-9][0-9]?)\.){3}(25[0-5]|2[0-4][0-9]|[01]?[0-9][0-9]?)$'
    if ":" not in args.rs_addr:
        raise ValueError("param rs-addr format error")
    # router server address may be a domain name, only validate port
    port = args.rs_addr.split(":")[1]
    if not valid_port(port):
        raise ValueError(f"param rs-addr port {port} out of range")
    # validate instance-ips
    ips = args.instance_ips.replace(" ", "").split(",")
    for ip in ips:
        if not re.match(ip_pattern, ip):
            raise ValueError(f"param instance-ips ip {ip} is invalid")
    # Validate api-server-ports
    ports = args.api_server_ports.replace(" ", "").split(",")
    for port in ports:
        if not valid_port(port):
            raise ValueError(f"param api-server-ports port {port} out of range")
    if len(ports) != len(ips):
        raise ValueError(f"the array length of api-server-ports and instance-ips are different, "
                         f"api-server-ports: {ports}, instance-ips: {ips}")
    # Validate num-api-servers
    if args.num_api_servers is not None:
        num_api_servers = args.num_api_servers.replace(" ", "").split(",")
        if len(num_api_servers) != len(ips):
            raise ValueError(f"the array length of num-api-servers and instance-ips are different, "
                             f"num-api-servers: {num_api_servers}, instance-ips: {ips}")
        for num in num_api_servers:
            if int(num) < 0:
                raise ValueError(f"param num-api-servers value {num} must be >= 0")


def generate_instance_name(role, ip, port):
    name_list = [role]
    name_list.extend(ip.split("."))
    name_list.append(str(port))
    return "_".join(name_list)


def wait_task_done(instance_addrs, args):
    unfinished_addrs = copy.deepcopy(instance_addrs)
    start_time = time.time()
    while True:
        exec_time = time.time() - start_time
        if exec_time > args.waiting_task_done_timeout:
            logging.warning(f"waiting instance task done timeout, unfinished instances: {unfinished_addrs}")
            return unfinished_addrs
        for inst_addr in unfinished_addrs:
            logging.info(f"getting metrics from {inst_addr}")
            url = "http://{}/metrics".format(inst_addr)
            try:
                res = request_get(url)
            except Exception:
                # Node access failure means the node has no task executing, can be removed
                unfinished_addrs.remove(inst_addr)
                continue
            running_values = get_metric_values(res, 'vllm:num_requests_running')
            waiting_values = get_metric_values(res, 'vllm:num_requests_waiting')
            running_num = sum(int(item) for item in running_values)
            waiting_num = sum(int(item) for item in waiting_values)
            if running_num + waiting_num == 0:
                unfinished_addrs.remove(inst_addr)
        if not unfinished_addrs:
            return []
        time.sleep(2)


def parse_prometheus_line(line: str):
    """Parse single line Prometheus format data"""
    # Ignore comments and empty lines
    if line.startswith('#') or not line.strip():
        return None

    # Match Prometheus metric format
    pattern = r'^([\w:]+)(\{.*?\})?\s+([\d\.]+)\s*(\d+)?$'
    match = re.match(pattern, line.strip())
    if not match:
        return None
    metric_name = match.group(1)
    labels_str = match.group(2)
    value = float(match.group(3))

    # Parse labels
    labels = {}
    if labels_str:
        labels_str = labels_str[1:-1]
        label_pairs = re.findall(r'(\w+)="([^"]+)"', labels_str)
        for key, val in label_pairs:
            labels[key] = val
    return {
        'metric': metric_name,
        'labels': labels,
        'value': value
    }


def parse_metrics_text(text: str):
    """Parse multi-line Prometheus format text"""
    metrics = []
    for line in text.split('\n'):
        parsed = parse_prometheus_line(line)
        if parsed:
            metrics.append(parsed)
    return metrics


def get_metric_values(text, metric_name: str):
    values = []
    metrics = parse_metrics_text(text)
    for metric in metrics:
        if metric['metric'] != metric_name:
            continue
        values.append(metric['value'])
    return values


def instance_leave(args):
    base_url = "http://{}/".format(args.rs_addr)
    url = base_url + "/cluster/instance/leave"
    instance_ips = args.instance_ips.replace(" ", "").split(",")
    api_server_ports = args.api_server_ports.replace(" ", "").split(",")
    instance_names = []
    instances_addr = []
    num_api_servers_list = None
    if args.num_api_servers is not None:
        num_api_servers_list = [int(x) for x in args.num_api_servers.replace(" ", "").split(",")]
    for index, ip in enumerate(instance_ips):
        start_port = int(api_server_ports[index])
        if num_api_servers_list is not None:
            server_count = num_api_servers_list[index]
        else:
            server_count = 1
        for num in range(server_count):
            port = start_port + num
            instance_name = generate_instance_name(args.role, ip, port)
            instance_names.append(instance_name)
            instances_addr.append(ip + ":" + str(port))
    # Generate body
    body = {
        "instance_names": instance_names
    }
    logging.info(f"begin remove instances {instances_addr} from router server")
    for i in range(RETRY_TIME):
        try:
            res = request_post(url, body)
            check_instance_leave_rs_result(res)
            break
        except Exception as e:
            if i == RETRY_TIME - 1:
                raise Exception(f"instances remove from router server error after retrying {RETRY_TIME} times: {str(e)}")

    # after instance is removed from router server, check if there are still running tasks
    logging.info(f"begin wait instance task done")
    unfinished_addrs = wait_task_done(instances_addr, args)
    logging.info(f"end wait instance task done, unfinished_addrs: {unfinished_addrs}")

    # for prefill instance, after task done, need to wait for specified time to ensure kvcache is pulled
    if args.role == "prefill":
        logging.info(f"begin wait {args.waiting_kvcache_pulled_completed_time} seconds to ensure the kvcache to be pulled completed")
        time.sleep(args.waiting_kvcache_pulled_completed_time)
    if unfinished_addrs:
        logging.warning(f"instances {instances_addr} remove from router server successfully, but there were unfinished "
                        f"task on instances after {args.waiting_task_done_timeout} seconds timeout: {unfinished_addrs}")
    else:
        logging.info(f"instances {instances_addr} remove from router server successfully")


def check_instance_leave_rs_result(res):
    if 'result' in res and res['result'] == 'accepted':
        return
    else:
        if 'error' in res:
            err_msg = res['error']
        else:
            err_msg = str(res)
        raise Exception(f"instances remove from router server error: {err_msg}")


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument('--rs-addr', type=str, required=True,
                        help='router server address, the format is {ip}:{port}')
    parser.add_argument('--role', type=str, required=True, choices=['prefill', 'decode'],
                        help='instance role that need to join to the router server')
    parser.add_argument('--instance-ips', type=str, required=True,
                        help='instance ips that need to be removed from the router server, multiple ips are separated '
                             'by commas')
    parser.add_argument('--api-server-ports', type=str, required=True,
                        help='api server port of prefill or api server start port of decode, multiple ports are '
                             'separated by commas, it needs to correspond to the param instance-ips')
    parser.add_argument('--num-api-servers', type=str, default=None,
                        help='number of api servers on each instance ip, separated by commas, '
                             'corresponds to instance-ips one-to-one')
    parser.add_argument('--ascend-rt-visible-devices-num', type=int, default=16,
                        help='visible devices num of one node')
    parser.add_argument('--waiting-task-done-timeout', type=int, default=300,
                        help='timeout waiting for instance task done')
    parser.add_argument('--waiting-kvcache-pulled-completed-time', type=int, default=120,
                        help='time waiting for kvcache pulled completed')
    parser.add_argument('--log-file', type=str, default=None,
                        help='log file')

    args = parser.parse_args()
    if not args.instance_ips:
        sys.exit(0)
    init_logger(args)
    try:
        check_params(args)
        instance_leave(args)
    except Exception as e:
        logging.exception("instances remove from router server error")
        sys.exit(1)
