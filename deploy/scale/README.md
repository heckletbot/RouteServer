# Scaling Scripts

[English](README.md) | [简体中文](README.zh-CN.md)

## Instance Registration Script
rs_instance_join.py is used to add new P (prefill) or D (decode) instances to router server.

Example:
```
python rs_instance_join.py \
    --rs-addr=<RS_IP>:<PORT> \
    --role=decode \
    --instance-ips=<INSTANCE_IP_1>,<INSTANCE_IP_2> \
    --api-server-ports=9000,9000 \
    --static-infos=max_num_seqs#32 \
    --num-api-servers=16,16 \
    --log-file=rs_instance_join.log
```

Parameter Description:
+ --rs-addr: Router server address
+ --role: Instance role, prefill/decode
+ --instance-ips: IP addresses of nodes to join, separated by "," for multiple
+ --api-server-ports: Starting port of nodes to join, separated by "," for multiple, count must match instance-ips
+ --static-infos: Static information of joining nodes, such as max_num_seqs, used by scheduling algorithm in router server, format is max_num_seqs#32, separated by "," for multiple
+ --num-api-servers: Number of API servers to register on each IP, separated by commas; count must match instance-ips. If omitted, one server is registered per IP, for either role. For a value N > 0, ports run from start_port through start_port + N - 1, inclusive; N = 0 skips that IP. The example registers ports 9000–9015 on each of the two IPs (32 servers total).
+ --ascend-rt-visible-devices-num: Accepted with a default of 16, but not used by the current registration logic. It does not expand the port list; use --num-api-servers to specify the server count.
+ --log-file: Log file for script output, default is None, meaning output to console

## Instance Deregistration Script
rs_instance_leave.py is used to deregister specified P or D instances, including steps such as deregistering from router server and waiting for tasks on them to complete.

Example:
```
python rs_instance_leave.py \
    --rs-addr=<RS_IP>:<PORT> \
    --role=decode \
    --instance-ips=<INSTANCE_IP_1>,<INSTANCE_IP_2> \
    --api-server-ports=9000,9000 \
    --num-api-servers=16,16 \
    --waiting-task-done-timeout=300 \
    --waiting-kvcache-pulled-completed-time=120 \
    --log-file=rs_instance_leave.log
```

Parameter Description:
+ --rs-addr: router server address
+ --role: Instance role, prefill/decode
+ --instance-ips: IP addresses of nodes to deregister, separated by "," for multiple
+ --api-server-ports: Starting port of nodes to deregister, separated by "," for multiple, count must match instance-ips
+ --num-api-servers: Number of API servers to deregister on each IP, separated by commas; count must match instance-ips. If omitted, one server is deregistered per IP, for either role. For a value N > 0, ports run from start_port through start_port + N - 1, inclusive; N = 0 skips that IP. Use the same IPs, starting ports and server counts as registration; the example deregisters ports 9000–9015 on each IP (32 servers total).
+ --ascend-rt-visible-devices-num: Accepted with a default of 16, but not used by the current deregistration logic. It does not expand the port list; use --num-api-servers to specify the server count.
+ --waiting-task-done-timeout: Timeout for waiting for running/waiting queue on corresponding api server to become empty during deregistration, i.e., graceful exit timeout for waiting for tasks to complete, default is 300s
+ --waiting-kvcache-pulled-completed-time: Time to wait for kvcache pull to complete. For P instances, deregistration is not complete until after waiting for tasks to finish AND this additional time, default is 120s
+ --log-file: Log file for script output, default is None, meaning output to console
