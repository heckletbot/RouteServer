# RouteServer Configuration Reference

[English](config-spec.md) | [简体中文](config-spec.zh-CN.md)

This document describes all configuration parameters for the RouteServer (`route-server.yaml`).

## Configuration Setup

1. Set the environment variable `ENV_ROUTE_SERVER_CONFIG_PATH` to point to the configuration directory.
2. Place a file named `route-server.yaml` in that directory.

## Deployment Notes
Parameters marked with "Template default" in the Constraints column refer to the default value provided in the `route-server.yaml` shipped with the release package. When deploying via `deploy.sh`, this template file is used to generate the final deployed configuration.

This document references two types of defaults:

- Template default: The value pre-filled in the deploy/route-server-template.yaml deployment template.
- Default: The default value hardcoded in the Go source code. It takes effect when a configuration field is set to its zero value (e.g., 0, false, empty string "") or is omitted entirely from the YAML file.

If a Template default is left empty or the field is absent from the YAML, the Default will be used at runtime. The Default is the ultimate fallback. Where no explicit Default exists, the Go zero value for the type applies (e.g., 0 for integers, false for booleans, "" for strings), which may result in the feature being disabled or a specific behavior as described in the Constraints column.

---

## server

The `server` section defines the core RouteServer identity and service settings, including listening address, inference mode, tracing, rate limiting, TLS/mTLS, and response header filtering.

```yaml
server:
  addr: <ip>
  http-port: <port>
  name: <name>
  infer-mode: PD
  inference-module-type: v0
  v1-stream-writing-enabled: true
  client-write-timeout-ms: 0
  rewrite-request-id: true
  trace:
    enable-trace-token-log: true
    type:
      - log
    otel-config:
      jaeger-endpoint:
      jaeger-service-name: RouteServer
      span-update-seconds: 5
      trace-sample-rate: 1.0
  response-header-blacklist:
    - header: "Date"
      prefix-matching: false
    - header: "Server"
      prefix-matching: false
    - header: "X-Pad"
      prefix-matching: false
    - header: "X-Accel-"
      prefix-matching: true
  rate-limit:
    max-concurrent-requests: 10000
    non-reference-apis:
      enabled: true
      requests-per-minute: 60
      exempt-paths:
        - "/v1/chat/completions"
        - "/v1/completions"
        - "/v1/metaserver"
      cleanup-interval-minutes: 5
  tls:
    enable-https: true
    cert-file:
    key-file:
    ca-cert-file:
    skip-verify: false
    enable-mtls: true
  ops-tls:
    enable-https: true
    cert-file:
    key-file:
    ca-cert-file:
```

| Parameter                                                       | Type   | Description                                                                                                                                                                              | Constraints                                                                                                                                                                                                |
|-----------------------------------------------------------------| ------ | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `server.name`                                                   | string | Unique name identifying this RouteServer node within the cluster                                                                                                                           | Must be unique across the cluster; duplicates will overwrite existing state and may cause data inconsistency                                                                                               |
| `server.addr`                                                   | string | RouteServer listening IP address                                                                                                                                                           | Must be a valid IP address                                                                                                                                                                                 |
| `server.http-port`                                              | int    | RouteServer listening HTTP port                                                                                                                                                            | Must be a valid, available port                                                                                                                                                                            |
| `server.infer-mode`                                             | string | Inference deployment mode that affects request processing flow                                                                                                                           | Default:`PD`. Must be one of: `PD`, `EPD`, `PD-AM`, `Hybrid`, `E-Hybrid`, `Layerwise-CDCP`, `Layerwise-CPCD`, `MetaServer-CDCP`, `MetaServer-CPCD`<br />Check details in deploy doc. |
| `server.inference-module-type`                                  | string | Pipeline scheduling mode;`v0` for basic linear scheduling, `pipeline` for configurable pipeline scheduling                                                                               | Default:`v0`. Must be `v0` or `pipeline`                                                                                                                                                                   |
| `server.v1-stream-writing-enabled`                              | bool   | Enable v1 stream writing mode                                                                                                                                                            | Template default:`true`                                                                                                                                                                                    |
| `server.client-write-timeout-ms`                                | int    | Client write timeout in milliseconds (0 = no timeout)                                                                                                                                    | Template default:`0`                                                                                                                                                                                       |
| `server.rewrite-request-id`                                     | bool   | Enable rewriting of the X-Request-Id header                                                                                                                                              | Template default:`true`                                                                                                                                                                                    |
| `server.trace.enable-trace-token-log`                           | bool   | Enable summary trace logging (TTFT, TPOT, prompt tokens, etc.)                                                                                                                           | Default:`false`; Template default: `true`                                                                                                                                                             |
| `server.trace.type`                                             | List   | Trace capability types:`log`, `otel`, `none`, `compose`. `log` prints reqId, span_id, and execution phases; `otel` captures the full request lifecycle via Jaeger; `none` disables tracing | Template default:`["log"]`                                                                                                                                                                                 |
| `server.trace.otel-config.jaeger-endpoint`                      | string | Jaeger/OTLP endpoint for OpenTelemetry traces in`IP:Port` format (e.g., `127.0.0.1:4317`)                                                                                                | Template default: empty                                                                                                                                                                                    |
| `server.trace.otel-config.jaeger-service-name`                  | string | Jaeger service name                                                                                                                                                                      | Default:`RouteServer`                                                                                                                                                                                      |
| `server.trace.otel-config.span-update-seconds`                  | int    | Interval for sending span data to Jaeger (seconds)                                                                                                                                       | Default:`5`. A complete request produces approximately 7 spans; setting this too high may accumulate excessive local span data under high concurrency                                                      |
| `server.trace.otel-config.trace-sample-rate`                    | float  | Trace sampling rate. If the upstream parent has already decided to trace, that decision takes precedence; otherwise the sample rate applies                                              | Must be in range`[0.0, 1.0]`                                                                                                                                                                               |
| `server.response-header-blacklist[].header`                     | string | Header name to strip from responses forwarded to the client                                                                                                                              | Template default:`Date`, `Server`, `X-Pad`, `X-Accel-`                                                                                                                                                     |
| `server.response-header-blacklist[].prefix-matching`            | bool   | Whether to match the header name by prefix                                                                                                                                               | Template default:`false` for exact-match headers, `true` for `X-Accel-`                                                                                                                                    |
| `server.rate-limit.max-concurrent-requests`                     | int    | Maximum number of concurrent active requests the RouteServer can handle                                                                                                                    | Default:`5000`; Template default: `10000`                                                                                                                                                             |
| `server.rate-limit.non-reference-apis.enabled`                  | bool   | Enable rate limiting for non-reference API endpoints; the token bucket is shared per API path                                                                                                                          | Template default:`true`                                                                                                                                                                                    |
| `server.rate-limit.non-reference-apis.requests-per-minute`      | int    | Maximum requests per minute per API path for non-reference APIs                                                                                                                            | Template default:`60`                                                                                                                                                                                      |
| `server.rate-limit.non-reference-apis.exempt-paths`             | List   | API paths exempt from rate limiting                                                                                                                                                      | Template default:`/v1/chat/completions`, `/v1/completions`, `/v1/metaserver`                                                                                                                               |
| `server.rate-limit.non-reference-apis.cleanup-interval-minutes` | int    | Interval for cleaning up expired rate-limit entries                                                                                                                                      | Template default:`5`                                                                                                                                                                                       |
| `server.tls.enable-https`                                       | bool   | Enable HTTPS for the main server port                                                                                                                                                    | Template default:`true`                                                                                                                                                                                    |
| `server.tls.cert-file`                                          | string | Path to the TLS certificate file                                                                                                                                                         | Template default: empty                                                                                                                                                                                    |
| `server.tls.key-file`                                           | string | Path to the TLS private key file                                                                                                                                                         | Template default: empty                                                                                                                                                                                    |
| `server.tls.ca-cert-file`                                       | string | Path to the CA certificate file for mTLS                                                                                                                                                 | Template default: empty                                                                                                                                                                                    |
| `server.tls.skip-verify`                                        | bool   | Skip TLS certificate verification (not recommended for production)                                                                                                                       | Template default:`false`                                                                                                                                                                                   |
| `server.tls.enable-mtls`                                        | bool   | Enable mutual TLS (mTLS) for the main server port                                                                                                                                        | Template default:`true`                                                                                                                                                                                    |
| `server.ops-tls.enable-https`                                   | bool   | Enable HTTPS for the OPS (management) port                                                                                                                                               | Template default:`true`                                                                                                                                                                                    |
| `server.ops-tls.cert-file`                                      | string | Path to the OPS TLS certificate file                                                                                                                                                     | Template default: empty                                                                                                                                                                                    |
| `server.ops-tls.key-file`                                       | string | Path to the OPS TLS private key file                                                                                                                                                     | Template default: empty                                                                                                                                                                                    |
| `server.ops-tls.ca-cert-file`                                   | string | Path to the OPS CA certificate file for mTLS                                                                                                                                             | Template default: empty                                                                                                                                                                                    |
| `server.ops-port`                                               | /      | Not listed in yaml, use`deploy.sh --ops-port` to setup, check details in deploy doc.                                                                                                     |                                                                                                                                                                                                            |

---

## meta-cleanup

The `meta-cleanup` section controls automatic cleanup of stale instance and worker metadata from the cluster state. This is an **optional** section and is not present in the default template for singleton deployments.

```yaml
meta-cleanup:
  max-instances-down-time-sec: -1
  max-workers-down-time-sec: -1
```

| Parameter                                  | Type | Description                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                            | Constraints                                                                       |
| ------------------------------------------ | ---- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------- |
| `meta-cleanup.max-instances-down-time-sec` | int  | When the leader detects an instance failing the GET /health check for longer than this duration, the instance address is considered unrecoverable and is removed from the cluster state. Values <= 0 disable cleanup. During operation, this timeout is converted to a failure count based on`rest-health-common-config.interval-ms` (e.g., 900s with a 10s interval yields 90 consecutive failures, rounded up; minimum 6). Due to distributed leader failover, health check progress may not be fully synchronized, potentially extending the effective cleanup time | Template default:`-1` (singleton mode, cleanup disabled); `900` (etcd mode, 900s) |
| `meta-cleanup.max-workers-down-time-sec`   | int  | Max seconds a worker can be down before the worker is proactively cleaned up                                                                                                                                                                                                                                                                                                                                                                                                                                                                                           | Values <= 0 disable cleanup. Default:`300` (5 min). Minimum: `30` (30s)           |

---

## logger

The `logger` section configures file-based logging with rotation, compression, and retention policies.

```yaml
logger:
  enabled: true
  filename: /path/to/route-server.log
  max-size: 1024
  max-age: 15
  max-backups: 10
  local-time: true
  compress: true
  level: info
  write-format: text
```

| Parameter             | Type   | Description                                                                                            | Constraints                                                                                   |
| --------------------- | ------ | ------------------------------------------------------------------------------------------------------ | --------------------------------------------------------------------------------------------- |
| `logger.enabled`      | bool   | Enable file logging. When`false`, logs are written to stderr                                           | Default:`false`; Template default: `true`                                                     |
| `logger.filename`     | string | Path to the log file. If empty, a file named`scheduler.logger` is created in the system temp directory | Default: empty (uses temp directory)                                                          |
| `logger.max-size`     | int    | Maximum size of a single log file in MB before rotation                                                | Default:`20` MB; Template default: `1024` MB                                             |
| `logger.max-age`      | int    | Maximum number of days to retain rotated log files                                                     | Default:`30` days; Template default: `15` days                                           |
| `logger.max-backups`  | int    | Maximum number of rotated log files to retain. Values <= 0 mean unlimited                              | Default:`50`; Template default: `10`                                                     |
| `logger.local-time`   | bool   | Use the computer's local time for backup file timestamps. When`false`, UTC time is used                | Default:`false`; Template default: `true`                                                |
| `logger.compress`     | bool   | Compress rotated log files using gzip                                                                  | Default:`false`; Template default: `true`                                                |
| `logger.level`        | string | Log output level                                                                                       | Default:`info`. Supported values: `trace`, `debug`, `info`, `warn`, `error`, `fatal`, `panic` |
| `logger.write-format` | string | Log output format                                                                                      | Options:`json`, `text`. Template default: `text`                                              |

---

## http

The `http` section configures the global HTTP client connection pool used for outbound requests to inference instances and peer RouteServer nodes.

```yaml
http:
  global-client-en: true
  request-timeout: 54000
  max-conns-per-host: 2048
  max-idle-conns: 40960
  max-idle-conns-per-host: 1800
  idle-conn-timeout: 110
  dialer-timeout: 600
  dialer-keepalive: 5
  tls:
    enable-https: true
    enable-mtls: true
    cert-file:
    key-file:
    ca-cert-file:
    skip-verify: false
```

| Parameter                      | Type   | Description                                                                                                                                      | Constraints                                              |
| ------------------------------ | ------ | ------------------------------------------------------------------------------------------------------------------------------------------------ | -------------------------------------------------------- |
| `http.request-timeout`         | int    | HTTP request timeout in seconds. Setting this too low may cause inference request timeouts                                                       | Default:`100`; Template default: `54000` (15 hours) |
| `http.dialer-timeout`          | int    | TCP dialer timeout in seconds. 0 means unlimited                                                                                                 | Default:`5`; Template default: `600`                |
| `http.dialer-keepalive`        | int    | TCP keep-alive probe interval in seconds. Negative values disable keep-alive                                                                     | Default:`-1`; Template default: `5`                 |
| `http.max-idle-conns`          | int    | Maximum total idle keep-alive connections across all peer hosts. Only meaningful when`dialer-keepalive` > 0                                      | Default:`20480`; Template default: `40960`          |
| `http.max-idle-conns-per-host` | int    | Maximum idle keep-alive connections per peer host. Setting to 0 resets to the transport default of 2. Only meaningful when`dialer-keepalive` > 0 | Default:`1800`; Template default: `1800`            |
| `http.idle-conn-timeout`       | int    | Idle keep-alive connection timeout in seconds. Idle connections exceeding this duration are destroyed                                            | Default:`10`; Template default: `110`               |
| `http.max-conns-per-host`      | int    | Maximum total connections (including non-idle) per host                                                                                          | Template default:`2048`                                  |
| `http.tls.enable-https`        | bool   | Enable HTTPS for HTTP client connections                                                                                                         | Template default:`true`                                  |
| `http.tls.enable-mtls`         | bool   | Enable mTLS for HTTP client connections                                                                                                          | Template default:`true`                                  |
| `http.tls.cert-file`           | string | TLS certificate file path for HTTP client connections                                                                                            | Template default: empty                                  |
| `http.tls.key-file`            | string | TLS private key file path for HTTP client connections                                                                                            | Template default: empty                                  |
| `http.tls.ca-cert-file`        | string | CA certificate file path for mTLS                                                                                                                | Template default: empty                                  |
| `http.tls.skip-verify`         | bool   | Skip TLS certificate verification for HTTP client connections                                                                                    | Template default:`false`                                 |

---

## instance-manager

The `instance-manager` section is the most complex configuration block. It manages inference instances, including health checking, failure detection, route tables, and broadcast request routing.

```yaml
instance-manager:
  required-roles:
    - prefill
    - decode
  instance-client-type: vllm
  health-check-config:
    event-threshold-interval-ms: 1000
    probe-infer-request-timeout-ms: 10000
    rest-health-common-config:
      interval-ms: 10000
      health-check-timeout-ms: 10000
      max-concurrency: 1000
      batch-size: 1
    fallback-health-checker:
      enable: true
      resume-check-interval-ms: 1000
      resume-time-ms: 90000
    available-checkers:
      rest-health:
        enable: true
        fail-on-500-directly: true
        consecutive-time: 3
      request-failures:
        enable: true
        consecutive-failure: false
        time-window-ms: 90000
        max-failures: 3
        failure-conditions:
          request-timeout: true
          status-codes:
            - 500
            - 501
            - 502
            - 503
        fake-request:
          enable: true
          time-interval-ms: 12000
          max-fake-request-time: 5
          failure-threshold: 3
      link-down-failures:
        enable: false
        time-window-ms: 90000
        max-failures: 3
      link-blacklist-config:
        enable: true
        link-block-threshold: 3
        entry-gc-interval-ms: 1000
        link-block-windows-ms: 180000
        link-block-tag-valid-time-ms: 600000
        instance-block-ratio: 70
      hang-checker:
        enable: true
        leader-check-interval-ms: 1000
        worker-hang-tag-valid-time-ms: 10000
        enable-probe-before-hang-check: true
        hang-probe-interval-ms: 60000
        hang-probe-timeout-ms: 10000
        max-consecutive-invalid-probes: 3
        hang-message-keywords:
          - "KV cache load failed"
        hang-thresholds:
          - role: encode
            max-non-active-time-ms: -1
            skip-non-stream: false
          - role: prefill
            max-non-active-time-ms: -1
            skip-non-stream: false
          - role: decode
            max-non-active-time-ms: 60000
            skip-non-stream: true
    unavailable-checkers:
      rest-health:
        enable: true
        consecutive-time: 9
        enable-probe-infer-request: true
  broadcast-request-whitelist:
    - path: "/version"
      methods: ["GET"]
    - path: "/v1/models"
      methods: ["GET"]
    # ... (full list in template)
```

| Parameter                                      | Type      | Description                                                                                                                                                     | Constraints                                                                                                                                                                                                                                                                                                                                                                                                                                 |
| ---------------------------------------------- | --------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `instance-manager.required-roles`              | List      | Required worker roles that must be present in the cluster                                                                                                       | Template default:`["prefill", "decode"]`                                                                                                                                                                                                                                                                                                                                                                                                    |
| `instance-manager.instance-client-type`        | string    | The inference engine type that the RouteServer connects to                                                                                                        | Currently only supports`vllm`                                                                                                                                                                                                                                                                                                                                                                                                               |
| `instance-manager.broadcast-request-whitelist` | List      | Whitelist of requests to broadcast to all vLLM instances. Each entry specifies a`path` (URL path) and `methods` (HTTP methods). Modification is not recommended | Template default includes: GET`/version`, `/v1/models`, `/server_info`, `/load`, `/ping`, `/is_sleeping`; POST `/start_profile`, `/stop_profile`, `/ping`, `/tokenize`, `/detokenize`, `/v1/embeddings`, `/pooling`, `/classify`, `/score`, `/v1/score`, `/v1/audio/transcriptions`, `/rerank`, `/v1/rerank`, `/v2/rerank`, `/reset_prefix_cache`, `/sleep`, `/wake_up`, `/invocations`, `/v1/load_lora_adapter`, `/v1/unload_lora_adapter` |
| `instance-manager.health-check-config`         | HealthCfg | Configures health check behavior on the RouteServer leader                                                                                                        | See nested sections below                                                                                                                                                                                                                                                                                                                                                                                                                   |

### Health Check Configuration (HealthCfg)

The health check system determines instance health status through multiple checkers. For a detailed explanation, refer to the "RouteServer Health Check Mechanism" section in the FAQ.

| Parameter                        | Type                  | Description                                                                                                                                                                          | Constraints                                                  |
| -------------------------------- | --------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | ------------------------------------------------------------ |
| `event-threshold-interval-ms`    | int                   | Interval for synchronizing health check events from workers to the leader in multi-node mode                                                                                         | Default:`1000` (1s). Minimum: `10`. Template default: `1000` |
| `rest-health-common-config`      | RestHealthCommonCfg   | Common HTTP health check configuration used by`available-checkers.rest-health` and `unavailable-checkers.rest-health`                                                                | See sub-section below                                        |
| `probe-infer-request-timeout-ms` | int                   | Timeout for probe inference requests sent during health checks                                                                                                                       | Default:`10000` (10s). Template default: `10000`             |
| `fallback-health-checker`        | FallbackHealthChecker | Fallback local health check mechanism configuration                                                                                                                                  | See sub-section below                                        |
| `available-checkers`             | AvailCheckersConfig   | Set of checker mechanisms that determine when a healthy instance should be marked as unhealthy. Any single checker meeting its failure condition will mark the instance as unhealthy | See sub-sections below                                       |
| `unavailable-checkers`           | UnavailCheckersConfig | Set of checker mechanisms that determine when an unhealthy instance should be restored to healthy status                                                                             | See sub-sections below                                       |

#### REST Health Common Configuration (RestHealthCommonCfg)

This sub-section configures how the RouteServer leader calls the GET /health endpoint on prefill/decode instances.

| Parameter                 | Type | Description                                                                                                                              | Constraints                                                            |
| ------------------------- | ---- | ---------------------------------------------------------------------------------------------------------------------------------------- |------------------------------------------------------------------------|
| `interval-ms`             | int  | Interval between consecutive GET /health requests to each instance                                                                       | Default:`10000` (10s). Minimum: `100`. Template default: `10000` (10s) |
| `health-check-timeout-ms` | int  | Timeout for each GET /health request                                                                                                     | Default:`10000` (10s). Minimum: `1`. Template default: `10000` (10s)   |
| `max-concurrency`         | int  | Maximum concurrency for batched health check requests                                                                                    | Default:`1000`. Minimum: `1`. Template default: `1000`                 |
| `batch-size`              | int  | Number of instances to check per batch. Setting this > 1 may cause health check delays due to connection timeouts. Recommended value:`1` | Default:`1`. Minimum: `1`. Template default: `1`                       |

#### Fallback Health Checker (FallbackHealthChecker)

A local fallback health check mechanism that isolates instances when health check data is unreliable.

| Parameter                                          | Type | Description                                                                                          | Constraints                                                         |
| -------------------------------------------------- | ---- | ---------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------- |
| `fallback-health-checker.enable`                   | bool | Enable the fallback local health mechanism                                                           | Default:`false`; Template default: `true`                      |
| `fallback-health-checker.resume-check-interval-ms` | int  | Interval for checking whether a fallback-isolated instance should be removed from the isolation list | Template default:`1000` (1s)                                        |
| `fallback-health-checker.resume-time-ms`           | int  | Duration (ms) an instance remains in the fallback isolation list before being removed                | Default:`90000` (90s). Minimum: `1`. Template default: `90000` |

#### Available Checkers (AvailCheckersConfig)

##### 1. REST Health Checker (rest-health)

Periodically calls the GET /health endpoint on each instance. If the endpoint fails to return HTTP 200 for a consecutive number of times, the instance is marked as unhealthy.

| Parameter                                             | Type | Description                                                                                                                                                                   | Constraints                                           |
| ----------------------------------------------------- | ---- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------- |
| `available-checkers.rest-health.enable`               | bool | Whether to mark a healthy instance as unhealthy based on GET /health failures                                                                                                 | Default:`false`; Template default: `true`        |
| `available-checkers.rest-health.fail-on-500-directly` | bool | When the GET /health endpoint explicitly returns HTTP 500, whether to fail immediately (`true`) or wait for the configured consecutive failure count (`false`)                | Default:`false`; Template default: `true`        |
| `available-checkers.rest-health.consecutive-time`     | int  | Number of consecutive failed health checks required to transition an instance from healthy to unhealthy (or vice versa). This prevents flapping under poor network conditions | Default:`3`. Minimum: `1`. Template default: `3` |

##### 2. Request Failures Checker (request-failures)

Monitors inference request errors within a sliding time window. If the error count exceeds the threshold within the window, the instance is marked as unhealthy.

| Parameter                                                                | Type | Description                                                                                                                                                                                                                  | Constraints                                                                   |
| ------------------------------------------------------------------------ | ---- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------- |
| `available-checkers.request-failures.enable`                             | bool | Whether to analyze inference request errors as a health indicator                                                                                                                                                            | Default:`false`; Template default: `true`                                |
| `available-checkers.request-failures.time-window-ms`                     | int  | Sliding time window in milliseconds. Errors outside this window are ignored                                                                                                                                                  | Default:`10000` (10s). Minimum: `1`. Template default: `90000` (90s)     |
| `available-checkers.request-failures.consecutive-failure`                | bool | When`true`, a successful request within the window resets the failure count. When `false`, failures accumulate within the window regardless of intervening successes                                                         | Default:`false`; Template default: `false`                               |
| `available-checkers.request-failures.max-failures`                       | int  | Number of failures within the time window that triggers the failure detection mechanism                                                                                                                                      | Default:`3`. Minimum: `1`. Template default: `3`                         |
| `available-checkers.request-failures.failure-conditions.request-timeout` | bool | Whether a request timeout counts as a failure                                                                                                                                                                                | Default:`false`; Template default: `true`                                |
| `available-checkers.request-failures.failure-conditions.status-codes`    | List | HTTP status codes that count as failures                                                                                                                                                                                     | Default:`[500, 501, 502, 503]`; Template default: `[500, 501, 502, 503]` |
| `available-checkers.request-failures.fake-request.enable`                | bool | When all failures within the window are HTTP 500 responses, send a probe request to confirm whether the instance is truly failing. When`false`, any error reaching the threshold immediately marks the instance as unhealthy | Default:`false`; Template default: `true`                                |
| `available-checkers.request-failures.fake-request.time-interval-ms`      | int  | Interval between probe requests                                                                                                                                                                                              | Default:`60000` (60s). Minimum: `1`. Template default: `12000` (12s)     |
| `available-checkers.request-failures.fake-request.max-fake-request-time` | int  | Maximum number of probe request retries. If the probe continues failing after this many attempts, the system logs a warning and aborts (the cluster should already be degraded due to prefill issues)                        | Default:`5`. Must be >= `failure-threshold`. Template default: `5`       |
| `available-checkers.request-failures.fake-request.failure-threshold`     | int  | Number of probe request failures (connection failure or 5xx) before the instance is confirmed as problematic                                                                                                                 | Default:`3`. Minimum: `1`. Template default: `3`                         |

##### 3. Hang Checker (hang-checker)

Detects whether an instance of a specific role has become stuck (hung). An instance is considered hung if it has not processed any active requests beyond the configured threshold.

> **Warning**: The hang threshold must be set based on the actual business requirements and the maximum acceptable decode response latency. Once an instance exceeds this threshold, it is marked as failed (status: `Hang`) and the cluster requires at least 80 seconds to recover it. During this period, the RouteServer health endpoint returns HTTP 500.

| Parameter                                                                  | Type   | Description                                                                                                                                                                                                                              | Constraints                                                                                     |
| -------------------------------------------------------------------------- | ------ | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------- |
| `available-checkers.hang-checker.enable`                                   | bool   | Whether to consider instance hangs as a health check criterion                                                                                                                                                                           | Default:`false`; Template default: `true`                                                  |
| `available-checkers.hang-checker.leader-check-interval-ms`                 | int    | Leader's hang check loop interval                                                                                                                                                                                                        | Default:`1000` (1s). Minimum: `10`. Template default: `1000`                               |
| `available-checkers.hang-checker.worker-hang-tag-valid-time-ms`            | int    | In multi-RouteServer deployments, workers synchronize hang instance lists to the leader. If no hang update is received for a given instance within this period, the instance is considered recovered. Not applicable in single-node mode | Default:`10000` (10s). Minimum: `100`. Template default: `10000`                           |
| `available-checkers.hang-checker.enable-probe-before-hang-check`           | bool   | Send a probe request to the instance before marking it as hung, to confirm the hang state                                                                                                                                                | Template default:`true`                                                                         |
| `available-checkers.hang-checker.hang-probe-interval-ms`                   | int    | Interval between hang probe requests                                                                                                                                                                                                     | Template default:`60000` (60s)                                                                  |
| `available-checkers.hang-checker.hang-probe-timeout-ms`                    | int    | Timeout for each hang probe request                                                                                                                                                                                                      | Template default:`10000` (10s)                                                                  |
| `available-checkers.hang-checker.max-consecutive-invalid-probes`           | int    | Maximum number of consecutive invalid probe results before taking action                                                                                                                                                                 | Template default:`3`                                                                            |
| `available-checkers.hang-checker.hang-message-keywords`                    | List   | Keywords in worker messages that indicate a hang condition                                                                                                                                                                               | Template default:`["KV cache load failed"]`                                                     |
| `available-checkers.hang-checker.hang-thresholds[].role`                   | string | The instance role this threshold applies to                                                                                                                                                                                              | Must be`encode`, `prefill`, or `decode`                                                         |
| `available-checkers.hang-checker.hang-thresholds[].max-non-active-time-ms` | int    | Maximum non-active time threshold in milliseconds. Values <= 0 skip hang checking for this role                                                                                                                                          | Template default:`encode`: `-1` (skip), `prefill`: `-1` (skip), `decode`: `60000` (60s)         |
| `available-checkers.hang-checker.hang-thresholds[].skip-non-stream`        | bool   | Whether to skip non-streaming requests for hang detection. When`true`, non-streaming requests on this role are not considered for hang detection                                                                                         | Default:`false`. Template default: `encode`: `false`, `prefill`: `false`, `decode`: `true` |

##### 4. Link Down Failures Checker (link-down-failures)

For each prefill-decode (PD) link pair, if link-down errors occur within a Y-second window for X times, the prefill instance serving that request is marked as unhealthy.

| Parameter                                              | Type | Description                                                                        | Constraints                                                                                                               |
| ------------------------------------------------------ | ---- | ---------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------- |
| `available-checkers.link-down-failures.enable`         | bool | Whether to analyze link-down errors as a health indicator                          | **Cannot be `true` simultaneously with `link-blacklist-config.enable`**. Default: `false`; Template default: `false` |
| `available-checkers.link-down-failures.time-window-ms` | int  | Sliding time window in milliseconds for counting link-down errors                  | Default:`10000` (10s). Minimum: `1`. Template default: `90000` (90s)                                                 |
| `available-checkers.link-down-failures.max-failures`   | int  | Number of link-down failures within the window that triggers the failure detection | Default:`3`. Minimum: `1`. Template default: `3`                                                                     |

##### 5. Link Blacklist Configuration (link-blacklist-config)

For each prefill-decode (PD) link pair, if Z link-down errors occur within a Y-second window, the link is considered faulty and undergoes link-level isolation. If a sufficient proportion of an instance's links are blocked, the instance itself is isolated.

| Parameter                                                               | Type | Description                                                                                                  | Constraints                                                                                                           |
| ----------------------------------------------------------------------- | ---- | ------------------------------------------------------------------------------------------------------------ | --------------------------------------------------------------------------------------------------------------------- |
| `available-checkers.link-blacklist-config.enable`                       | bool | Enable the link blacklist management module                                                                  | **Cannot be `true` simultaneously with `link-down-failures.enable`**. Default: `false`; Template default: `true` |
| `available-checkers.link-blacklist-config.link-block-threshold`         | int  | Number of PD link-down errors within the window that triggers blacklisting the PD link pair                  | Must be > 0. Default:`3`; Template default: `3`                                                                  |
| `available-checkers.link-blacklist-config.entry-gc-interval-ms`         | int  | Garbage collection interval for cleaning up expired blacklist entries                                        | Must be > 10. Default:`1000` (1s); Template default: `1000`                                                      |
| `available-checkers.link-blacklist-config.link-block-windows-ms`        | int  | Time window for recording PD link-down errors. Records older than this are discarded                         | Must be > 100. Default:`90000`; Template default: `180000` (180s)                                                |
| `available-checkers.link-blacklist-config.link-block-tag-valid-time-ms` | int  | Validity period of a PD link blacklist entry                                                                 | Must be > 100. Default:`90000`; Template default: `600000` (10 min)                                              |
| `available-checkers.link-blacklist-config.instance-block-ratio`         | int  | Threshold ratio (percentage) of blocked links to total links above which an instance is considered unhealthy | Must be in`[0, 100]`. Default: `70`; Template default: `70`                                                      |

#### Unavailable Checkers (UnAvailCheckersConfig)

Currently provides a single mechanism: if the GET /health endpoint succeeds consecutively for N times, the unhealthy instance is restored to healthy status and removed from scheduling isolation.

| Parameter                                                     | Type | Description                                                                                           | Constraints                                                                                                                           |
| ------------------------------------------------------------- | ---- | ----------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------- |
| `unavailable-checkers.rest-health.enable`                     | bool | Whether to restore an unhealthy instance based on consecutive successful GET /health responses        | Default:`false`; Template default: `true`. **Should not be set to `false`**, otherwise instances marked as failed cannot recover |
| `unavailable-checkers.rest-health.consecutive-time`           | int  | Number of consecutive successful health checks required to restore an instance to healthy status      | Default:`3`. Minimum: `1`. Template default: `9` (at 10s intervals, 90s of consecutive success)                                  |
| `unavailable-checkers.rest-health.enable-probe-infer-request` | bool | Whether to perform a probe inference request before restoring an unhealthy instance to healthy status | Default:`false`; Template default: `true`                                                                                        |

---

## apc-manager and apc-scheduler

The `apc-manager` section configures Automatic Prefix Caching (APC), which caches and reuses KV cache computations for repeated prompt prefixes. The `apc` scheduler algorithm (configured in `scheduler-manager`) works in conjunction with this module.

```yaml
apc-manager:
  enable: true
  strategies:
    hash:
      block-size-bytes: 8000
      max-blocks: 32
      match-threshold: 0.0
      max-entries: 32768
      max-memory-mb: 512
      cache-ttl-sec: 3600
```

| Parameter                                      | Type  | Description                                                                                                             | Constraints                      |
| ---------------------------------------------- | ----- | ----------------------------------------------------------------------------------------------------------------------- | -------------------------------- |
| `apc-manager.enable`                           | bool  | Enable the APC manager                                                                                                  | Template default:`true`          |
| `apc-manager.strategies.hash.block-size-bytes` | int   | Block size in bytes for the rolling hash strategy                                                                       | Template default:`8000`          |
| `apc-manager.strategies.hash.max-blocks`       | int   | Maximum number of blocks per request                                                                                    | Template default:`32`            |
| `apc-manager.strategies.hash.match-threshold`  | float | Minimum prefix match ratio threshold (0.0-1.0). APC scheduling is triggered when the match ratio exceeds this threshold | Template default:`0.0`           |
| `apc-manager.strategies.hash.max-entries`      | int   | Maximum number of cache entries                                                                                         | Template default:`32768`         |
| `apc-manager.strategies.hash.max-memory-mb`    | int   | Maximum memory usage for the APC cache in MB                                                                            | Template default:`512`           |
| `apc-manager.strategies.hash.cache-ttl-sec`    | int   | Cache entry TTL in seconds                                                                                              | Template default:`3600` (1 hour) |

---

## scheduler-manager

The `scheduler-manager` section configures request scheduling strategies for different instance roles, including SLO-based scheduling and remote scheduling.

```yaml
scheduler-manager:
  system-prompt-length: 100
  slo-scheduling:
    slo-enable: false
    slo-fallback-enable: true
    slo-group-healthy-threshold: 1
  schedulers:
    - instance-role: encode
      scheduler-name: least-total-load
      params:
        verbose: false
    - instance-role: prefill
      scheduler-name: apc
      params:
        default-batch-size: 64
        prefix-load-base: 1
    - instance-role: decode
      scheduler-name: abc6
      params:
        metrics-overdue-interval-ms: 100
        update-interval-ms: -1
        needStats: "vllm:num_requests_running,vllm:num_requests_waiting"
  remote-schedule:
    enabled: true
    remote-type: endpoint
    remote-endpoint: <remote_endpoint>
    need-content: false
    timeout-ms: 1000
    cleanup-interval-ms: 60000
```

| Parameter                                               | Type   | Description                                                                                                                              | Constraints                                |
| ------------------------------------------------------- | ------ | ---------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------ |
| `scheduler-manager.system-prompt-length`                | int    | System prompt length used for scheduling decisions                                                                                       | Template default:`100`                     |
| `scheduler-manager.schedulers`                          | List   | Scheduler configurations per instance role. One scheduler per role must be defined; configuring more than one per role produces an error | See ScheduleCfg below                      |
| `scheduler-manager.remote-schedule.enabled`             | bool   | Enable remote scheduling (delegating scheduling decisions to an external endpoint)                                                       | Template default:`true`                    |
| `scheduler-manager.remote-schedule.remote-type`         | string | Remote scheduler type                                                                                                                    | Options:`endpoint` or `leader`             |
| `scheduler-manager.remote-schedule.remote-endpoint`     | string | Remote scheduler endpoint URL                                                                                                            | Required when remote scheduling is enabled |
| `scheduler-manager.remote-schedule.need-content`        | bool   | Include request content in the remote scheduling request                                                                                 | Default:`false`                            |
| `scheduler-manager.remote-schedule.timeout-ms`          | int    | Remote scheduling request timeout in milliseconds                                                                                        | Default:`1000`                             |
| `scheduler-manager.remote-schedule.cleanup-interval-ms` | int    | Interval for cleaning up stale remote schedule entries                                                                                   | Default:`60000` (60s)                      |

### ScheduleCfg

| Parameter        | Type   | Description                                                | Constraints                                                                                                                                                                                                                                                                                                                                                                                 |
| ---------------- | ------ | ---------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `instance-role`  | string | The instance role this scheduler handles                   | Valid values:`encode`, `prefill`, `decode`                                                                                                                                                                                                                                                                                                                                                  |
| `scheduler-name` | string | Name of the scheduling algorithm                           | Supported algorithms:`round-robin` (round-robin forwarding), `least-total-load` (load balancing by request length, suitable for prefill), `apc` (APC-aware scheduling for prefill), `abc6` (advanced scheduling for decode), `StatsPrintingRandom` (random distribution with logging, for testing only). Template default: `encode`: `least-total-load`, `prefill`: `apc`, `decode`: `abc6` |
| `params`         | map    | Scheduler-specific parameters as arbitrary key-value pairs | See below for per-algorithm parameters                                                                                                                                                                                                                                                                                                                                                      |

### SLO Scheduling Configuration

| Parameter                                                      | Type | Description                                                                                                                                                                           | Constraints                       |
| -------------------------------------------------------------- | ---- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | --------------------------------- |
| `scheduler-manager.slo-scheduling.slo-enable`                  | bool | Enable SLO-based scheduling                                                                                                                                                           | Template default:`false`          |
| `scheduler-manager.slo-scheduling.slo-fallback-enable`         | bool | When`true`, if no instance group satisfies the SLO requirements, fall back to any available group. When `false`, scheduling fails if no qualifying group has enough healthy instances | Template default:`true`           |
| `scheduler-manager.slo-scheduling.slo-group-healthy-threshold` | int  | Minimum number of healthy instances required in an SLO group for it to be considered available                                                                                        | Must be > 0. Template default:`1` |

### Per-Algorithm Parameters

**StatsPrintingRandom** (testing only):

- `needStats`: Comma-separated list of metric names to collect and log. Metrics are synchronized every 100ms and printed at trace level on each update. Default: empty.

**apc** (prefill scheduler):

- `default-batch-size`: Default batch size used when the prefill instance does not report its effective batch size. Default: `64`.
- `prefix-load-base`: Base load added when predicting the impact of a new request. When set to `0`, a fully prefix-matched request adds no extra load. Default: `1`.

**abc6** (decode scheduler):

- `metrics-overdue-interval-ms`: Metric staleness interval. Metrics older than this are discarded during synchronization. Default: `100`.
- `update-interval-ms`: Metric update interval. Set to a positive value when Prometheus metrics are enabled; `-1` disables metric updates. Default: `-1`.
- `needStats`: Comma-separated list of Prometheus metric names to collect (e.g., `vllm:num_requests_running,vllm:num_requests_waiting`).

---

## monitor

The `monitor` section configures the monitoring client. Currently, the standard configuration uses `none` to disable monitoring.

```yaml
monitor:
  monitor-client-type: none
```

| Parameter                     | Type   | Description         | Constraints                                                              |
| ----------------------------- | ------ | ------------------- | ------------------------------------------------------------------------ |
| `monitor.monitor-client-type` | string | Monitor client type | Supported values:`none`, `redis`, `prometheus`. Template default: `none` |

---

## metrics

The `metrics` section configures performance metrics collection.

```yaml
metrics:
  enable: true
  duration: 60
  metrics-collector-config-file-name: metrics_config_vllm_0.11.0.yaml
```

| Parameter                                    | Type   | Description                                      | Constraints                                        |
| -------------------------------------------- | ------ | ------------------------------------------------ | -------------------------------------------------- |
| `metrics.enable`                             | bool   | Enable metrics collection                        | Default:`false`; Template default: `true`     |
| `metrics.duration`                           | int    | Metrics collection interval in seconds           | Template default:`60`                              |
| `metrics.metrics-collector-config-file-name` | string | File name of the metrics collector configuration | Template default:`metrics_config_vllm_0.11.0.yaml` |

---

## cluster

The `cluster` section configures cluster membership, coordination, and leader election. For singleton (single-node) deployments, the deploy.sh script auto-generates these values; modification is generally not recommended.

```yaml
cluster:
  type: singleton
  verbose: true
  role_candidate: true
  peer_http:
    dialer-timeout: 600
    dialer-keepalive: 5
    request-timeout: 14400
    max-idle-conns: 40960
    max-idle-conns-per-host: 1800
    idle-conn-timeout: 110
    max-conns-per-host: 2048
  etcd-health-expire-time-ms: 0
  etcd:
    etcd-endpoints:
      - <etcd_endpoint1>
      - <etcd_endpoint2>
    dial-timeout-ms: 5000
    retry-time: 10
    retry-interval-sec: 5
    lease-ttl-sec: 3
    lease-refresh-interval-ms: 1000
    tls:
      enable-https: true
      enable-mtls: true
      cert-file:
      key-file:
      ca-cert-file:
      skip-verify: false
```

| Parameter                            | Type       | Description                                                                                                                                                                                                                                                                        | Constraints                                                                                             |
| ------------------------------------ | ---------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |---------------------------------------------------------------------------------------------------------|
| `cluster.type`                       | string     | Cluster management mode.`singleton`: standalone mode with no state synchronization (only `verbose` is used; other cluster settings are ignored). `etcd`: uses an external etcd service for leader election and state synchronization (`peer_http` and `etcd` configurations apply) | Options:`singleton`, `etcd`. Default: `singleton`                                                       |
| `cluster.verbose`                    | bool       | Enable additional debug logging for cluster operations                                                                                                                                                                                                                             | Default:`false`; Template default: `true`                                                               |
| `cluster.role_candidate`             | bool       | Whether this RouteServer node is eligible to become a leader                                                                                                                                                                                                                         | Default:`false`. When `cluster.type` is `singleton`, this is forced to `true`. Template default: `true` |
| `cluster.peer_http`                  | HttpConfig | HTTP client configuration for peer-to-peer communication. Used for sending join requests to seed nodes and forwarding requests from non-leader to leader nodes. When undefined, the top-level`http` configuration is used                                                          | Template default: see YAML above                                                                        |
| `cluster.etcd-health-expire-time-ms` | int        | Expiration time for etcd health cache. When the cache expires, failure data from etcd is no longer trusted                                                                                                                                                                         | Default:`0` (when 0 or unset, uses 2x `FailureSnapshotIntervalMs`; fallback ~40s)                       |

### Etcd Configuration (EtcdConfig)

| Parameter                                | Type   | Description                                                                                                                                                                     | Constraints                                                     |
| ---------------------------------------- | ------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------- |
| `cluster.etcd.etcd-endpoints`            | List   | etcd server endpoints                                                                                                                                                           | Required; must not be empty                                     |
| `cluster.etcd.dial-timeout-ms`           | int    | Timeout for the initial etcd client dial                                                                                                                                        | Default:`10000` (10s); Template default: `5000` (5s)            |
| `cluster.etcd.retry-time`                | int    | Number of retry attempts for etcd initialization, reconnection, state recovery, and service registration                                                                        | Default:`10`. Must be >= 0. Template default: `10`              |
| `cluster.etcd.retry-interval-sec`        | int    | Wait interval between retry attempts                                                                                                                                            | Default:`5`. Range: `[0, 60]`. Template default: `5`            |
| `cluster.etcd.lease-ttl-sec`             | int    | TTL for etcd sessions used in leader election and service registration. If network isolation exceeds this duration, the session expires and the node is evicted from leadership | Default:`3`. Range: `[1, 600]`. Template default: `3` (seconds) |
| `cluster.etcd.lease-refresh-interval-ms` | int    | Interval for refreshing the etcd lease                                                                                                                                          | Default:`1000`. Range: `[50, 60000]`. Template default: `1000`  |
| `cluster.etcd.tls.enable-https`          | bool   | Enable HTTPS for etcd connections                                                                                                                                               | Template default:`true`                                         |
| `cluster.etcd.tls.enable-mtls`           | bool   | Enable mTLS for etcd connections                                                                                                                                                | Template default:`true`                                         |
| `cluster.etcd.tls.cert-file`             | string | etcd client TLS certificate file path                                                                                                                                           | Template default: empty                                         |
| `cluster.etcd.tls.key-file`              | string | etcd client TLS private key file path                                                                                                                                           | Template default: empty                                         |
| `cluster.etcd.tls.ca-cert-file`          | string | etcd CA certificate file path for mTLS                                                                                                                                          | Template default: empty                                         |
| `cluster.etcd.tls.skip-verify`           | bool   | Skip TLS certificate verification for etcd connections                                                                                                                          | Template default:`false`                                        |

---

## probe

The `probe` section configures Kubernetes-style readiness probes and circuit breaker settings for instance health monitoring.

```yaml
probe:
  readiness:
    prefill: 1
    decode: 1
  circuit-breaker:
    enable: true
    failure-rate-threshold: 0.8
    min-request-count: 10
    check-window-sec: 600
```

| Parameter                                      | Type  | Description                                                                                                         | Constraints                                                     |
| ---------------------------------------------- | ----- | ------------------------------------------------------------------------------------------------------------------- |-----------------------------------------------------------------|
| `probe.readiness.prefill`                      | int   | Minimum number of healthy prefill instances required for the RouteServer to report readiness                          | Must be > 0. Default:`1`                                        |
| `probe.readiness.decode`                       | int   | Minimum number of healthy decode instances required for the RouteServer to report readiness                           | Must be > 0. Default:`1`                                        |
| `probe.circuit-breaker.enable`                 | bool  | Enable the circuit breaker functionality                                                                            | Template default:`true`                                         |
| `probe.circuit-breaker.failure-rate-threshold` | float | Failure rate threshold. When the failure rate exceeds this value within the check window, the circuit breaker trips | Must be in`(0.0, 1.0]`. Default: `0.6`. Template default: `0.8` |
| `probe.circuit-breaker.min-request-count`      | int   | Minimum number of requests within the check window required before evaluating whether to trip the breaker           | Must be > 0. Default:`10`                                       |
| `probe.circuit-breaker.check-window-sec`       | int   | Circuit breaker check window duration in seconds                                                                    | Must be > 0. Default:`600` (10 minutes)                         |

---

## peer-client

The `peer-client` section configures the HTTP client used for peer-to-peer communication between RouteServer nodes.

```yaml
peer-client:
  http:
    request-timeout: 14400
    max-conns-per-host: 2048
    max-idle-conns: 40960
    max-idle-conns-per-host: 1800
    idle-conn-timeout: 110
    dialer-timeout: 600
    dialer-keepalive: 5
  send-with-domain: false
  domain: leader_domain
```

| Parameter                                  | Type   | Description                                                                                                                                 | Constraints                                   |
| ------------------------------------------ | ------ | ------------------------------------------------------------------------------------------------------------------------------------------- | --------------------------------------------- |
| `peer-client.http.request-timeout`         | int    | HTTP request timeout in seconds. Setting this too low may cause request timeouts                                                            | Template default:`14400`                      |
| `peer-client.http.dialer-timeout`          | int    | TCP dialer timeout in seconds. 0 means unlimited                                                                                            | Template default:`600`                        |
| `peer-client.http.dialer-keepalive`        | int    | TCP keep-alive probe interval in seconds. Negative values disable keep-alive                                                                | Template default:`5`                          |
| `peer-client.http.max-conns-per-host`      | int    | Maximum total connections per host                                                                                                          | Template default:`2048`                       |
| `peer-client.http.max-idle-conns`          | int    | Maximum total idle keep-alive connections across all peer hosts. Only meaningful when`dialer-keepalive` > 0                                 | Template default:`40960`                      |
| `peer-client.http.max-idle-conns-per-host` | int    | Maximum idle keep-alive connections per host. Setting to 0 resets to the transport default of 2. Only meaningful when`dialer-keepalive` > 0 | Template default:`1800`                       |
| `peer-client.http.idle-conn-timeout`       | int    | Idle keep-alive connection timeout in seconds                                                                                               | Template default:`110`                        |
| `peer-client.send-with-domain`             | bool   | When`true`, access the leader using a domain name instead of an IP address                                                                  | Template default:`false`                      |
| `peer-client.domain`                       | string | Domain name used when`send-with-domain` is `true`. Must not be empty when that setting is enabled                                           | Template default:`leader_domain`              |

---
