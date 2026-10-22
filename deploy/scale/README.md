# Scaling Scripts

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
    --ascend-rt-visible-devices-num=16 \
    --log-file=rs_instance_join.log
```

Parameter Description:
+ --rs-addr: Router server address
+ --role: Instance role, prefill/decode
+ --instance-ips: IP addresses of nodes to join, separated by "," for multiple
+ --api-server-ports: Starting port of nodes to join, separated by "," for multiple, count must match instance-ips
+ --static-infos: Static information of joining nodes, such as max_num_seqs, used by scheduling algorithm in router server, format is max_num_seqs#32, separated by "," for multiple
+ --ascend-rt-visible-devices-num: Number of available cards on the node. Default value is 16, used to determine the actual api server address list that needs to be registered for decode starting from the starting port. Not required if role is prefill.
  If role is decode, the actual registered api server addresses are ip:start_port~ip:start_port+card_count
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
    --ascend-rt-visible-devices-num=16 \
    --waiting-task-done-timeout=300 \
    --waiting-kvcache-pulled-completed-time=120 \
    --log-file=rs_instance_leave.log
```

Parameter Description:
+ --rs-addr: router server address
+ --role: Instance role, prefill/decode
+ --instance-ips: IP addresses of nodes to deregister, separated by "," for multiple
+ --api-server-ports: Starting port of nodes to deregister, separated by "," for multiple, count must match instance-ips
+ --ascend-rt-visible-devices-num: Number of available cards on the node. Default value is 16, used to determine the actual api server address list that needs to be deregistered for decode starting from the starting port. Not required if role is prefill.
  If role is decode, the actual deregistered api server addresses are ip:start_port~ip:start_port+card_count
+ --waiting-task-done-timeout: Timeout for waiting for running/waiting queue on corresponding api server to become empty during deregistration, i.e., graceful exit timeout for waiting for tasks to complete, default is 300s
+ --waiting-kvcache-pulled-completed-time: Time to wait for kvcache pull to complete. For P instances, deregistration is not complete until after waiting for tasks to finish AND this additional time, default is 120s
+ --log-file: Log file for script output, default is None, meaning output to console
