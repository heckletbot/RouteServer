# 扩缩容脚本
## 实例注册脚本
mo_instance_join.py 用于将新增的p或d实例加入ModelOrch

示例：
```
python mo_instance_join.py \
    --mo-addr=<MO_IP>:<PORT> \
    --role=decode \
    --instance-ips=<INSTANCE_IP_1>,<INSTANCE_IP_2> \
    --api-server-ports=9000,9000 \
    --static-infos=max_num_seqs#32 \
    --ascend-rt-visible-devices-num=16 \
    --log-file=mo_instance_join.log
```

参数含义：
+ --mo-addr：ModelOrch的地址
+ --role：实例角色，prefill/decode
+ --instance-ips：需要加入的节点ip，多个使用“，”分割
+ --api-server-ports：需要加入的节点的起始port，多个使用“，”分割，数量与instance-ips对应
+ --static-infos：加入节点的静态信息，如指定max_num_seqs，供ModelOrch中调度算法使用，格式为max_num_seqs#32，多个使用”，”分割
+ --ascend-rt-visible-devices-num：节点可用卡数。默认值为16，用于确定decode从起始port开始需要实际注册的api server地址列表。如果role为prefill则不需要配置，
  如果role为decode时，则实际注册的api server地址为 ip:起始port~ip:起始port+卡数
+ --log-file：脚本输出的日志文件，默认值为None，即输出到控制台

## 实例注销脚本
mo_instance_leave.py 用于注销指定的p或d实例，包括从ModelOrch中注销、等待其上任务完成等步骤

示例：
```
python mo_instance_leave.py \
    --mo-addr=<MO_IP>:<PORT> \
    --role=decode \
    --instance-ips=<INSTANCE_IP_1>,<INSTANCE_IP_2> \
    --api-server-ports=9000,9000 \
    --ascend-rt-visible-devices-num=16 \
    --waiting-task-done-timeout=300 \
    --waiting-kvcache-pulled-completed-time=120 \
    --log-file=mo_instance_leave.log
```

参数含义：
+ --mo-addr：ModelOrch的地址
+ --role：实例角色，prefill/decode
+ --instance-ips：需要注销的节点ip，多个使用“，”分割
+ --api-server-ports：需要注销的节点的起始port，多个使用“，”分割，数量与instance-ips对应
+ --ascend-rt-visible-devices-num：节点可用卡数。默认值为16，用于确定decode从起始port开始需要实际注销的api server地址列表。如果role为prefill则不需要配置，
  如果role为decode时，则实际注销的api server地址为 ip:起始port~ip:起始port+卡数
+ --waiting-task-done-timeout：注销时等待对应api server上running/waiting queue队列为空的超时时间，即优雅退出等待其上任务完成的超时时间，默认为300s
+ --waiting-kvcache-pulled-completed-time：等待kvcache拉取完成的时间，如果是p实例，等待任务完成后还需等待该时间才算注销完成，默认为120s
+ --log-file：脚本输出的日志文件，默认值为None，即输出到控制台

## kubeinfer方式全流程脚本
example_1p1d.yml 根据环境自行修改yml中参数，如容器使用镜像、模型路径等

部署命令：

```
kubectl apply -f example_1p1d.yml
```

扩缩容时修改对应role（prefill、decode）的replicas。例如将prefill的replicas从1->2，然后执行apply命令，则为扩1个p节点，将prefill的replicas从2->1，然后执行apply命令，则为缩1个p节点
