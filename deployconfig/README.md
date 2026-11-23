# deployconfig — PD 分离本地部署配置

本目录包含两套可直接运行的 PD 分离(Prefill/Decode 分离)部署配置, 用于
`agentic-balancer` 与基线的对比验证:

| 目录 | 代码基线 | prefill 调度器 | decode 调度器 | 端口 (http/ops) |
|------|----------|----------------|---------------|-----------------|
| `pd-baseline/` | `dev` 分支(当前代码) | `apc`(模板默认) | `abc6` | 8000 / 8090 |
| `pd-agentic-balancer/` | `origin/agentic-balancer` 分支 | `agentic-balancer` | `abc6` | 8001 / 8091 |
| `pd-coord/` | `person_dev` 分支(coord 弹性路由) | `apc` | `abc6` | 8002 / 8092 |

两套配置除调度器外保持一致(同一 4P+4D 后端拓扑、同一健康检查、同一 pipeline),
便于公平对比; 端口错开, 可在同机并行运行 A/B。

> 注意: 两套配置必须用**对应分支构建的二进制**启动 ——
> `agentic-balancer` 调度器只在 `origin/agentic-balancer` 分支注册,
> `dev` 分支的代码启动 `pd-agentic-balancer/route-server.yaml` 会报
> `scheduler not registered: agentic-balancer`。

## 目录内容(每个子目录为一个完整的配置目录)

```
deployconfig/
├── README.md
├── pd-baseline/                       # PD 分离基线(dev 分支)
│   ├── route-server.yaml              # 主配置(加载文件名固定为 route-server.yaml)
│   ├── prometheus.yaml                # monitor 采集的指标清单(abc6/apc 所需)
│   ├── metrics_config_vllm_0.11.0.yaml # 指标采集器配置(复制自 deploy/)
│   └── pipelines/
│       └── pd_separate.yaml           # PD 分离 pipeline(复制自 deploy/pipelines/)
└── pd-agentic-balancer/               # agentic-balancer(该分支代码)
    ├── route-server.yaml
    ├── prometheus.yaml                # 额外包含 vllm:kv_cache_usage_perc
    ├── metrics_config_vllm_0.11.0.yaml
    └── pipelines/
        └── pd_separate.yaml
└── pd-coord/                          # PD 弹性(本地混部)路由(person_dev)
    ├── route-server.yaml              # 含 coord-routing 节
    ├── prometheus.yaml
    ├── metrics_config_vllm_0.11.0.yaml
    └── pipelines/
        └── pd_coord.yaml              # 含 CoordRoute 阶段与 coord_* 钩子
```

## 启动方式

```bash
# 基线(dev 分支二进制)
cd deployconfig/pd-baseline
ENV_ROUTE_SERVER_CONFIG_PATH=$PWD ./route-server

# agentic-balancer(该分支二进制)
cd deployconfig/pd-agentic-balancer
ENV_ROUTE_SERVER_CONFIG_PATH=$PWD ./route-server
```

要点:

- 主配置文件名固定为 `route-server.yaml`, 由 `ENV_ROUTE_SERVER_CONFIG_PATH`
  指向其所在目录;
- **进程工作目录(CWD)建议设为对应子目录**:`prometheus-config-path` 与日志
  文件名按相对路径解析(也可改成绝对路径);
- `peer-client` / `ops-peer-client` 两节不可省略:当前代码在
  `ops-peer-client` 缺省时空指针解引用, 启动即 panic;
- 后端拓扑(静态路由表):4 prefill(7.242.105.78:8010-8013) +
  4 decode(7.242.105.78:8020-8023), 两套配置共用同一后端, 便于 A/B;
  按实际环境修改 `instance-manager.route-table` 即可。

## PD 弹性(coord)路由说明(pd-coord)

`pd-coord/` 在 PD 分离基线上多一个 `CoordRoute` 阶段: 每个请求先判一次
"能否由单个空闲 worker 独立服务整条请求"(本地混部), 判不了就走原有 PD 分离。
判定只依赖实例上报的 worker 统计, 不改动任何调度器, 也不依赖全局 StateManager。

pipeline 组成(`pipelines/pd_coord.yaml`):

| 阶段 | 作用 |
|------|------|
| `coord_route` | 唯一决策点; 结果写入 `CoordDecision`, 命中本地时预选实例并占一个在飞槽位。**本身不配置 `request_load`**: prompt token 直接沿用 `prefill_schedule` 的取值口径(启动期把该阶段作为取值来源), 不存在第二份需要同步的 token 配置 |
| `prefill_schedule` | 原有逻辑不变(`dependsOn: coord_route`) |
| `prefill_execute` | 钩子换成 `coord_prepare_request` / `coord_post_response` / `coord_post_phase`: PD 走基线路径, 本地走 decode 路径 |
| `decode_schedule` | `skip_policy: skip_if_coord_local` — 本地请求不再调度 decode |
| `decode_execute` | 原有逻辑不变(本地请求已被跳过) |

判定顺序(任一不满足即回落 PD):

1. 请求本身带 `kv_transfer_params`(PD 分段续传)→ 不本地;
2. `prompt tokens < local-min-input-tokens` → 不本地(小请求不值得混部);
3. 某一角色(且仅一个角色)存在 idle worker 且 `prompt tokens` 不超过该角色
   `p-hybrid-max-input-tokens` / `d-hybrid-max-input-tokens` → 本地;
4. 否则 PD。

代价说明: `coord_route` 在 `prefill_schedule` 之前就要知道 prompt 大小, 因此会执行一次
同样的取值逻辑(一次 O(请求体长度) 的解析), `prefill_schedule` 之后仍会按原逻辑再取一次;
这是"先决策后调度"的固有代价, 换来的是两个阶段读到的 token 数天然一致。

参数(`server.coord-routing`):

| 参数 | 默认 | 含义 |
|------|------|------|
| `strategy` | imbalance | v1 仅 `imbalance`(单角色空闲下沉) |
| `metrics-stale-seconds` | 3.0 | 统计超过该时长视为过期, 不参与判定 |
| `idle-max-waiting` | 0 | idle 允许的排队请求数上限 |
| `idle-max-running-p` / `-d` | 0 | idle 允许的在飞请求数上限(按角色) |
| `idle-max-kv-usage` | 0.50 | idle 允许的 KV 占用上限 |
| `p-hybrid-max-inflight` / `d-...` | 2 | 本地路由在单 worker 上的并发上限(预留槽位) |
| `local-min-input-tokens` | 32768 | 触发本地路由的最小输入长度 |
| `p-hybrid-max-input-tokens` | 204800 | prefill 侧本地承接的最大输入长度(含) |
| `d-hybrid-max-input-tokens` | 1024 | decode 侧本地承接的最大输入长度(含) |

启动期校验(没有“开关”字段: **本节与 pipeline 里的 `CoordRoute` 阶段同时存在才算开启**;
只有其一, 启动直接失败, 不做静默降级):

- pipeline 里有 `CoordRoute` 阶段, 但 `route-server.yaml` 没有 `coord-routing` 节;
- `route-server.yaml` 有 `coord-routing` 节, 但 pipeline 里没有 `CoordRoute` 阶段;
- `strategy` 不是已注册的名字。

上线与回滚都只在配置目录里增删这两处(同一次重启, 无需换二进制, 端口/调度器/拓扑不动):

- 上线: 用 `pd-coord/` 这套(`coord-routing` 节 + `pipelines/pd_coord.yaml`);
- 回滚: 把 `pipelines/pd_coord.yaml` 换回基线的 `pd_separate.yaml`, 并删掉
  `route-server.yaml` 里的 `coord-routing` 节 —— 请求链路回到基线的 PD 分离。

## 基线说明(pd-baseline)

基于 `deploy/route-server-template.yaml`(当前代码的官方模板)构建:
调度器取模板默认组合 prefill=`apc`(前缀缓存亲和, 依赖 `apc-manager` 节)+
decode=`abc6`。当前 dev 分支可用的其他 prefill 基线选择(改
`scheduler-name` 并删除 `apc-manager` 节即可):

- `round-robin` — 轮询(参考 local-config-pd-rr 的最简基线)
- `least-total-load` / `least-requests` — 负载类基线
- `abc6` — 与 decode 相同的负载均衡基线

## agentic-balancer 参数说明(pd-agentic-balancer)

参数取自参考配置 `local-config-pd-ab`(见 `origin/agentic-balancer` 分支的
`pkg/route-server/scheduler/agentic_balancer/scheduler.go`):

| 参数 | 默认 | 含义 |
|------|------|------|
| `target-role` | prefill | 候选 worker 角色过滤(Hybrid 模式可设 decode) |
| `affinity-weight` (lambda) | 0.5 | 亲和收益相对负载成本的权重 |
| `overload-util` (U) | 0.90 | 单 worker 准入阈值(0<U<=1) |
| `affinity-half-life` (H) | 30s | acting 时间的亲和衰减半衰期 |
| `aging-rate` (eta) | ~0.167 | 每秒排队等待获得的优先级 |
| `dispatch-timeout` | 30s | 排队等待超时 |
| `max-queue-size` | 1000 | 优先队列容量 |
| `max-worker-history` | 4 | 每会话记录的 worker 历史长度 |
| `session-ttl` | 30m | 会话表 TTL |
| `update-interval-ms` | 1000 | worker 统计刷新间隔 |

约束:`agentic-balancer` 按 `vllm:kv_cache_usage_perc` 计算 worker 负载,
要求后端 vLLM >= 0.11(0.9/0.10 只有 `gpu_cache_usage_perc`, 会读成 0 负载)。

## 其他差异

- 基线配置含 `apc-manager` 节(apc 调度器 Init 强制要求);agentic 配置无此节。
- 旧配置中的 `init-instance-from-config`、`logger-anonymization-switch`、
  abc6 的 `update_interval_ms`(下划线键名)在本仓库代码中不存在/不生效,
  两套配置均已修正为当前代码实际的键名(`update-interval-ms` 连字符)。
