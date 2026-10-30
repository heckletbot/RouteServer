# 扩缩容脚本 / Scaling Scripts

[English](README.md) | [简体中文](README.zh-CN.md)

## 实例注册脚本

`rs_instance_join.py` 用于向 RouteServer 注册新的 P（Prefill）或 D（Decode）实例。

示例：

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

参数说明：

+ `--rs-addr`：RouteServer 地址
+ `--role`：实例角色，`prefill`/`decode`
+ `--instance-ips`：待注册节点的 IP 地址，多个用 `,` 分隔
+ `--api-server-ports`：待注册节点的起始端口，多个用 `,` 分隔，数量必须与 `--instance-ips` 一致
+ `--static-infos`：待注册节点的静态信息（例如 `max_num_seqs`），供 RouteServer 的调度算法使用；格式为 `max_num_seqs#32`，多个用 `,` 分隔
+ `--ascend-rt-visible-devices-num`：节点上可用卡数，默认 16。用于确定从起始端口开始、decode 实际需要注册的 api server 地址列表；`role` 为 `prefill` 时不需要。
  若 `role` 为 `decode`，实际注册的 api server 地址为 `ip:start_port~ip:start_port+card_count`
+ `--log-file`：脚本输出日志文件，默认为 None，即输出到控制台

## 实例注销脚本

`rs_instance_leave.py` 用于注销指定的 P 或 D 实例，包含从 RouteServer 摘除、等待实例上任务完成等步骤。

示例：

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

参数说明：

+ `--rs-addr`：RouteServer 地址
+ `--role`：实例角色，`prefill`/`decode`
+ `--instance-ips`：待注销节点的 IP 地址，多个用 `,` 分隔
+ `--api-server-ports`：待注销节点的起始端口，多个用 `,` 分隔，数量必须与 `--instance-ips` 一致
+ `--ascend-rt-visible-devices-num`：节点上可用卡数，默认 16。用于确定从起始端口开始、decode 实际需要注销的 api server 地址列表；`role` 为 `prefill` 时不需要。
  若 `role` 为 `decode`，实际注销的 api server 地址为 `ip:start_port~ip:start_port+card_count`
+ `--waiting-task-done-timeout`：注销期间等待对应 api server 上运行中/排队队列清空的超时，即等待任务完成的优雅退出超时，默认 300 秒
+ `--waiting-kvcache-pulled-completed-time`：等待 kvcache 拉取完成的时间。对 P 实例而言，等待任务结束**并**额外等待该时间后才算注销完成，默认 120 秒
+ `--log-file`：脚本输出日志文件，默认为 None，即输出到控制台
