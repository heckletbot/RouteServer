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

import re

# for example: prefill_127_0_0_1_9000, decode_127_0_0_1_9001
def generate_instance_name(role, ip, port):
    name_list = [role]
    name_list.extend(ip.split("."))
    name_list.append(str(port))
    return "_".join(name_list)

def valid_port(port):
    try:
        port_int = int(port)
        if port_int <= 0 or port_int > 65535:
            return False
        return True
    except ValueError:
        return False

def check_params(args):
    ip_pattern = r'^((25[0-5]|2[0-4][0-9]|[01]?[0-9][0-9]?)\.){3}(25[0-5]|2[0-4][0-9]|[01]?[0-9][0-9]?)$'
    ips = args.instance_ips.replace(" ", "").split(",")
    for ip in ips:
        if not re.match(ip_pattern, ip):
            raise ValueError(f"param instance-ips ip {ip} is invalid")
    ports = args.api_server_ports.replace(" ", "").split(",")

    if len(ports) != len(ips):
        raise ValueError(f"the array length of api-server-ports and instance-ips are different, "
                         f"api-server-ports: {ports}, instance-ips: {ips}")

    for port in ports:
        if not valid_port(port):
            raise ValueError(f"param api-server-ports port {port} out of range")
        if args.role != "prefill":
            if args.ascend_rt_visible_devices_num is None or args.ascend_rt_visible_devices_num <= 0:
                raise ValueError(f"param ascend-rt-visible-devices-num must be positive, got {args.ascend_rt_visible_devices_num}")

            max_port = int(port) + args.ascend_rt_visible_devices_num - 1
            if not valid_port(max_port):
                raise ValueError(f"api server max port {max_port} out of range, start port {port}, devices_num {args.ascend_rt_visible_devices_num}")

