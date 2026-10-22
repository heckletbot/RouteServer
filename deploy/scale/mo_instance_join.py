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
import json
import logging
import re
import sys
import time

import requests

RETRY_TIME = 10


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


def valid_port(port):
    if int(port) <= 0 or int(port) > 65535:
        return False
    return True


def check_params(args):
    ip_pattern = r'^((25[0-5]|2[0-4][0-9]|[01]?[0-9][0-9]?)\.){3}(25[0-5]|2[0-4][0-9]|[01]?[0-9][0-9]?)$'
    if ":" not in args.mo_addr:
        raise ValueError("param mo-addr format error")
    ip, port = args.mo_addr.strip().split(":")
    if not valid_port(port):
        raise ValueError(f"param mo-addr port {port} out of range")
    # validate instance-ips
    ips = args.instance_ips.replace(" ", "").split(",")
    for ip in ips:
        if not re.match(ip_pattern, ip):
            raise ValueError(f"param instance-ips ip {ip} is invalid")
    # 校验api-server-ports
    ports = args.api_server_ports.replace(" ", "").split(",")
    for port in ports:
        if not valid_port(port):
            raise ValueError(f"param api-server-ports port {port} out of range")
    if len(ports) != len(ips):
        raise ValueError(f"the array length of api-server-ports and instance-ips are different, "
                         f"api-server-ports: {ports}, instance-ips: {ips}")
    # 校验num-api-servers
    if args.num_api_servers is not None:
        num_api_servers = args.num_api_servers.replace(" ", "").split(",")
        if len(num_api_servers) != len(ips):
            raise ValueError(f"the array length of num-api-servers and instance-ips are different, "
                             f"num-api-servers: {num_api_servers}, instance-ips: {ips}")
        for num in num_api_servers:
            if int(num) < 0:
                raise ValueError(f"param num-api-servers value {num} must be >= 0")
        for idx, num in enumerate(num_api_servers):
            if int(num) > 0:
                max_port = int(ports[idx]) + int(num) - 1
                if not valid_port(max_port):
                    raise ValueError(f"api server max port {max_port} out of range, start port {ports[idx]}")
    # 校验static-infos
    parse_static_infos_param(args.static_infos)


def parse_static_infos_param(static_infos):
    res = {}
    try:
        for param in static_infos.split(","):
            k, v = param.split("#")
            res[k] = v
    except Exception:
        raise ValueError(f"param static-infos format error")
    return res


def generate_instance_name(role, ip, port):
    name_list = [role]
    name_list.extend(ip.split("."))
    name_list.append(str(port))
    return "_".join(name_list)


def instance_join(args):
    base_url = "http://{}/".format(args.mo_addr)
    url = base_url + "/cluster/instance/join"
    static_infos = parse_static_infos_param(args.static_infos)
    instance_ips = args.instance_ips.replace(" ", "").split(",")
    api_server_ports = args.api_server_ports.replace(" ", "").split(",")
    instances = {}
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
            instance_info = {
                "ip": ip,
                "port": port,
                "role": args.role,
                "static-infos": static_infos
            }
            instances[instance_name] = instance_info
            instances_addr.append(ip + ":" + str(port))
    # 生成body
    body = {
        "instances": instances
    }
    logging.info(f"begin join instances {instances_addr} to model orch")
    for i in range(RETRY_TIME):
        try:
            res = request_post(url, body)
            check_result(res)
            break
        except Exception as e:
            if i == RETRY_TIME - 1:
                raise Exception(f"instances {instances_addr} join to model orch error after retrying {RETRY_TIME} times: {str(e)}")
            logging.warning(f"instances join to model orch error, try again..")
            time.sleep(3)
    logging.info(f"instances {instances_addr} join to model orch successfully")


def check_result(res):
    if 'result' in res and res['result'] == 'accepted':
        return
    else:
        if 'error' in res:
            err_msg = res['error']
        else:
            err_msg = str(res)
        raise Exception(f"instances join to model orch error: {err_msg}")


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument('--mo-addr', type=str, required=True,
                        help='model orch address, the format is {ip}:{port}')
    parser.add_argument('--role', type=str, required=True, choices=['prefill', 'decode'],
                        help='instance role that need to join to the model orch')
    parser.add_argument('--instance-ips', type=str, required=True,
                        help='instance ips that need to be joined to the model orch, multiple ips are separated by '
                             'commas')
    parser.add_argument('--static-infos', type=str, default="max_num_seqs#54",
                        help='instance static infos, format is ${key1}#${val1}, multiple infos are separated by commas,'
                             'eg: max_num_seqs#54,api_server_count#1')
    parser.add_argument('--api-server-ports', type=str, required=True,
                        help='api server port of prefill or api server start port of decode, multiple ports are '
                             'separated by commas, it needs to correspond to the param instance-ips')
    parser.add_argument('--num-api-servers', type=str, default=None,
                        help='number of api servers on each instance ip, separated by commas, '
                             'corresponds to instance-ips one-to-one')
    parser.add_argument('--ascend-rt-visible-devices-num', type=int, default=16,
                            help='visible devices num of one node')
    parser.add_argument('--log-file', type=str, default=None,
                        help='log file')

    args = parser.parse_args()
    if not args.instance_ips:
        sys.exit(0)
    init_logger(args)
    try:
        check_params(args)
        instance_join(args)
    except Exception as e:
        logging.exception("instances join to model orch error")
        sys.exit(1)
