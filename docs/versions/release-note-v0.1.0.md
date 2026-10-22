# Release Note - RouteServer v0.1.0

## About

RouteServer is a distributed OpenAI inference orchestration frontend, designed for Large Language Model (LLM) service scenarios. It supports inference workflows across various vLLM deployment patterns. Aims to provide distributed high-availability inference request execution workflows, it achieves efficient, scalable, and reliable inference load-balancing services through centralized request scheduling, inference engine fault awareness and isolation, and cluster state coordination.

## Features

### Deployment Modes

- **Standalone Deployment**: Simple single-machine deployment, suitable for scenarios where inference instance addresses are fixed in advance. ([Deployment Guide](../deploy.md))
- **Distributed Deployment**: Supports distributed request processing with horizontal scaling to prevent RouteServer from becoming an inference performance bottleneck. Through leader election, it provides centralized and unified inference request scheduling to avoid scheduling conflict performance issues caused by distributed routers scheduling independently. ([Deployment Guide](../deploy.md))

### Supported vLLM Deployment Modes

- **PD Hybrid**: Prefill and decode stages run in the same vLLM instance. ([PD Hybrid Orchestration](../orchestrations/pd-hybrid.md))
- **PD Separation (Disaggregated)**: Prefill and decode stages disaggregated into separate vLLM instances for independent scaling. ([PD Disaggregated Orchestration](../orchestrations/pd-disaggregate.md))
- **EPD Separation (Disaggregated)**: Encode, prefill, and decode stages fully disaggregated into separate vLLM instances. ([EPD Disaggregated Orchestration](../orchestrations/epd-disaggregate.md))
- **Layerwise CDCP**: vLLM PD disaggregated deployed, where prefill actively pushes prompt KV cache to decode in a layerwise manner during prefilling. ([Layerwise CDCP Orchestration](../orchestrations/layerwise-cdcp.md))

### Load Balancing & Scheduling

- Supports multiple load balancing and scheduling algorithms designed specifically for LLM inference scenarios. ([Load Balancing Algorithms](../load-balancing.md))

### Fault Detection & Recovery

- Multi-granularity fault detection, isolation, and automatic recovery:
  - Request error type identification
  - Hang detection mechanism
  - Proactive probe requests
  - PD link-level blacklist mechanism (not applicable in PD hybrid scenario)
- Real-time isolation of faulty instances in both standalone and distributed systems. ([Inference Instance Health Management](../infer-instance-health.md))

### Observability

Provides rich observable interfaces to retrieve various runtime states within the cluster:

- **Cluster Health & Readiness**: `/health` (inference instance health check, verifies all required role instances are available, returns 200/500), `/status` (overall cluster status: running/concerning/deploying/fault), `/worker/readiness` (Worker readiness, checks if instance count meets minimum requirements)
- **Cluster Management & Topology**: `/cluster/*` (Ops port, including node/instance join/leave, leader info, local node state, consistency status, etc.)
- **Load & Scheduling**: `/scheduler/load` (per-SLO-group instance load details, supports role filtering and including unavailable instances)
- **Fault Isolation Status**: `/link_black_instance` (PD link blacklist snapshot, showing blocked peer instances and remaining block time)
- **Running Request Details**: `/cluster/running_details` (all running requests across the cluster with per-phase execution status)
- **Performance Metrics**: `/internal/metrics` (Prometheus-format metrics, supports `only_rs=true` to filter Route Server custom metrics)

For details, see ([Observability Documentation](../observability.md)) and ([RESTful API Reference](../restful-api.md)).

## Known Issues

- N/A (initial release)

## Notes

- This is the first open-source release of RouteServer.
- For production use, it is strongly recommended to enable HTTPS and mTLS. See the security guide for details.
- For complete API documentation, refer to the RESTful API reference and Config reference.

## References

- [Architecture Documentation](../architecture.md)
- [Code Structure](../code-structure.md)
- [Load Balancing Algorithms](../load-balancing.md)
- [Security Guide](../security.md)
- [Deployment Guide](../deploy.md)
- [Observability Documentation](../observability.md)
- [RESTful API Reference](../restful-api.md)
- [Config Specification](../config-spec.md)

## License

RouteServer is licensed under Apache License 2.0.
