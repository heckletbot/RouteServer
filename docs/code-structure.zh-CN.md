# 代码库结构

[English](code-structure.md) | [简体中文](code-structure.zh-CN.md)

RouteServer 使用 Go 实现。代码库的结构如下：

```
RouteServer/
├── cmd/                                    # Executable entry points
│   ├── route_server/server/                # RESTful API gateway main entry
│   ├── metrics-collector/                  # Aggregated metrics collector
│   └── config-tool/                        # Configuration generation tool used during deployment
│
├── pkg/                                    # Core code packages
│   ├── common/                             # Common foundation libraries
│   │   ├── client/instance-client/         # Inference instance client
│   │   ├── cluster/                        # Cluster management common components
│   │   ├── config/                         # Configuration definitions
│   │   ├── events/                         # Event processing
│   │   ├── health/                         # Inference instance health check
│   │   ├── instance-metrics/               # Instance metrics
│   │   └── trace/                          # Distributed request lifecycle trace
│   │
│   ├── route-server/                       # Routing service core
│   │   ├── handler/infer/                  # Inference handler (v0 version)
│   │   ├── handler/pipeline/               # Orchestratable inference pipeline implementation
│   │   ├── scheduler/                      # Scheduler implementation
│   │   ├── apc/                            # Cache-aware scheduling
│   │   ├── instances/                      # Local inference instance management
│   │   ├── cluster/etcd/                   # ETCD cluster management implementation (state synchronization)
│   │   ├── cluster/singleton/              # Standalone mode state management implementation
│   │   └── metrics/                        # Metrics definitions
│   │
│   └── metrics-collector/                  # Standalone metrics collection service
│
├── deploy/                                 # Deployment configuration, including deployment configuration templates and orchestrated inference pipelines
└── e2etest/                                # End-to-end test cases
```
