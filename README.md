# RouteServer

[English](README.md) | [简体中文](README.zh-CN.md)

# About

RouteServer is a distributed OpenAI inference orchestration frontend, designed for Large Language Model (LLM) service scenarios. It supports inference workflows across various vLLM deployment patterns. Aims to provide distributed high-availability inference request execution workflows, it achieves efficient, scalable, and reliable inference load-balancing services through centralized request scheduling, inference engine fault awareness and isolation, and cluster state coordination.

- Supports two deployment modes
  - Standalone deployment: Simple single-machine deployment, suitable for scenarios where inference instance addresses are fixed in advance
  - Distributed deployment: Supports distributed request processing with horizontal scaling to prevent RouteServer from becoming an inference performance bottleneck. Through leader election, it provides centralized and unified inference request scheduling to avoid scheduling conflict performance issues caused by distributed routers scheduling independently
- Supports multiple vLLM deployment modes:
  - [PD hybrid](docs/orchestrations/pd-hybrid.md): prefill and decode stage runs in same vLLM instance.
  - [PD separation](docs/orchestrations/pd-disaggregate.md): prefill and decode stages disaggregated into separate vLLM instances for independent scaling.
  - [EPD separation](docs/orchestrations/epd-disaggregate.md): encode, prefill, and decode stages fully disaggregated into separate vLLM instances.
  - [Layerwise CDCP](docs/orchestrations/layerwise-cdcp.md): vLLM PD disaggregated deployed, where prefill will actively push prompt kv cache to decode in a layerwise manner during prefilling. see [vllm-ascend issue](https://github.com/vllm-project/vllm-ascend/issues/4842)
- Supports multiple load balancing and scheduling algorithms designed for LLM inference. see [load-balancing](docs/load-balancing.md)
- Multi-granularity fault detection, isolation, and automatic recovery: In both standalone and distributed systems, collects request processing information and perceives inference instance faults through mechanisms such as request error type identification, instance output token performance, and proactive probe requests, performing real-time isolation.
- Provides observable interfaces to retrieve various runtime states within the cluster.

see our [architecture docs](docs/architecture.md) and [code-base structure](docs/code-structure.md) for more details

# Getting Started

## Quickstart

### Prerequisites

- Go 1.26.0 or higher
- Make 3.82 or higher

### Build and Package

```bash
# Clean previous builds and create the deploy package
make clean && make package
```

This will produce a zip package at `output/route-server-<version>-<timestamp>.zip`.

For detailed build instructions and available Makefile targets, see the [Build Guide](docs/build.md).

### Deploy with PD Separation Example

1. Navigate to the package directory (the `output/` directory created by `make package`):
```bash
cd output
```

2. Set environment variables for your inference framework instances (OpenAI format compatible):
```bash
export PREFILL_SERVERS="<YOUR_PREFILL_IP>:<PORT>"
export DECODE_SERVERS="<YOUR_DECODE_IP>:<PORT>"
```

3. Deploy a single RouteServer in local:
```bash
bash deploy.sh \
    --port 9800 \
    --mode singleton \
    --host <YOUR_IP>  \
    --enable-https false \
    --ops-enable-https false \
    --client-https-enable false \
    --etcd-tls-enable false
```

4. Check the status of cluster:
```bash
curl -X GET http://<YOUR_IP>:9800/status
```

For more deployment options and configurations, see our [deploying script reference](docs/deploy.md)

## Security

We strongly advise enable https and mTLS in production, see our [security guide](docs/security.md). HTTPS/TLS is disabled by default (`--enable-https`, `--ops-enable-https`, `--client-https-enable` and `--etcd-tls-enable` all default to `false`), so the example above needs no certificates; enable them explicitly for production.

## API reference

visit our [RESTful API reference](docs/restful-api.md) for users

visit our [Config reference](docs/config-spec.md) for developers

## Contributing

We welcome contributions! The full guide is in [CONTRIBUTING.md](CONTRIBUTING.md) —
中文版见 [CONTRIBUTING.zh-CN.md](CONTRIBUTING.zh-CN.md)（含分支模型、提交规范与评审流程）。

The short version:

1. **Fork** the repository
2. **Create a topic branch** from `main` (`git checkout -b feat/my-feature`)
3. **Make your changes** and ensure code quality:
   ```bash
   make lint   # go fmt + go vet
   go test ./... -v -timeout 60m
   ```
   For Make test targets, the Bash requirement and E2E coverage, see
   [the testing notes](CONTRIBUTING.md#3-build-and-test).
4. **Commit** with a Conventional Commits message and sign off (`git commit -s`)
5. **Submit a Pull Request** against `main`

For major changes, please open an issue first to discuss what you would like to change. Do not open pull
requests against `intranet-sync` — it is written by a machine and changes there are overwritten.

- Branch model: [docs/branching.md](docs/branching.md) · [中文](docs/branching.zh-CN.md)
- Roles and decision making: [GOVERNANCE.md](GOVERNANCE.md) · [中文](GOVERNANCE.zh-CN.md)
- Security reports: [SECURITY.md](SECURITY.md) · [中文](SECURITY.zh-CN.md)

## License

RouteServer is licensed under Apache License 2.0
