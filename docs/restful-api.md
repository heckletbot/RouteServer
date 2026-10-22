# RouteServer API Documentation

## Port Description

The **Listen Port** in each API description indicates which port the API listens on:

- **Service Port**: Corresponds to the `--port` option of `deploy.sh`, used for business inference requests.
- **Operations Port**: Corresponds to the `--ops-port` option of `deploy.sh`, used for management-related requests (e.g., force readiness setting).
- If `--ops-port` is not specified during deployment, the Operations Port defaults to **Service Port + 10**.

## Table of Contents

- [1. Status Query API](#1-status-query-api)
- [2. Health Check API](#2-health-check-api)
- [3. Inference Request API](#3-inference-request-api)
- [4. Scheduling Request API](#4-scheduling-request-api)
- [5. Instance Management API](#5-instance-management-api)
- [6. Request Management API](#6-request-management-api)
- [7. Cluster Management API](#7-cluster-management-api)
- [8. Monitoring Metrics API](#8-monitoring-metrics-api)
- [9. Health Probe API](#9-health-probe-api)
- [10. Version Information API](#10-version-information-api)
- [11. Event Sync Request API](#11-event-sync-request-api)
- [12. Blacklist Management API](#12-blacklist-management-api)
- [13. Dynamic Proxy API](#13-dynamic-proxy-api)

## 1. Status Query API

### GET /status

**Listen Port**: Service Port

**Functionality**: Check the health status of all inference instances under the requested RouteServer process, including both Prefill and Decode instances. Returns status information for all instances and outputs the overall cluster status.

**Handler**: The requested RouteServer receives and forwards to the cluster Leader for processing.

**Caller**: Operations Platform

**Constraints/Conditions**:
- Access to RouteServer in the cluster is only successful when the leader is alive
- When Leader is unavailable, this endpoint is not available and will return a non-200 status code

**Parameters**: This endpoint requires no request parameters

**Core Business Logic**:
- System obtains the list of all instances
- Checks the health status of each instance individually, returning the instance's access URI (IP address and port number), health status (running or failed), error message (set to "healthy" if status is healthy), role type (prefill or decode), and alert information
- Cluster status rules are as follows:
    - deploying: When at least 1p1d running does not exist, and all non-running instances are in deploying state (no Fault state exists)
    - fault: When at least 1p1d running does not exist, and Fault state instances exist
    - concerning: When at least 1p1d running exists, and fault instances exist
    - running: All instances are running

**Response Example**:
```json
{
    "status": "running",
    "prefills": [
        {
            "status": "running",
            "url": "http://*.*.*.*:***",
            "role": "prefill",
            "pod_name": "",
            "fault message": "healthy",
            "slo_tag": "default",
            "alert": ""
        }
    ],
    "decodes": [
        {
            "status": "running",
            "url": "http://*.*.*.*:***",
            "role": "decode",
            "pod_name": "",
            "slo_tag": "default",
            "fault message": "healthy",
            "alert": ""
        }
    ]
}
```

## 2. Health Check API

### GET /health

**Listen Port**: Service Port

**Functionality**: Check whether all inference instances under the requested RouteServer process are healthy, covering both Prefill and Decode instance types. Returns the corresponding HTTP status code based on the check result.

**Handler**: The requested RouteServer receives and forwards to the cluster Leader for processing.

**Caller**: Monitoring System, Operations Platform

**Constraints/Conditions**:
- Requires available cluster Leader through adminLeaderProxyMiddleware
- When Leader is unavailable, this endpoint is not available and will return a non-200 status code

**Parameters**: No request parameters

**Core Business Logic**:
- System obtains the list of all instances
- Checks the availability status of each instance one by one
- Returns 200 if all instances are available
- Returns 500 if any instance is unavailable

**Response**: This endpoint returns a bare HTTP status code with no JSON body.

| Scenario | Result |
|----------|--------|
| All instances available | HTTP 200 OK |
| Any instance unavailable | HTTP 500 Internal Server Error |
| All instances unavailable | HTTP 500 Internal Server Error |
| No instances configured in config file | HTTP 500 Internal Server Error |

## 3. Inference Request API

The inference endpoints support the OpenAI-standard API format. RouteServer proxies requests to the appropriate inference instances based on scheduling strategy.

**Error Codes**:

| Status Code | Scenario | Description |
|-------------|----------|-------------|
| 400 | Invalid body (empty/non-JSON) | Response contains "failed to create generate request" |
| 400 | Backend engine 400 passthrough | Body content is passthrough from inference engine |
| 429 | Concurrent requests exceed single RouteServer limit | Rate limit exceeded |
| 500 | vLLM backend error | Direct passthrough of vLLM error content |
| 500 | Other RouteServer internal errors | See response body for details |
| 504 | Streaming timeout | Gateway timeout during streaming output |
| 502 | No available Prefill instances | Bad gateway |
| 502 | No available Decode instances | Bad gateway |

**APC Cache Support**: When vLLM has APC (Automatic Prefix Caching) enabled, the response `usage` field includes `prompt_tokens_details` with `cached_tokens` showing the number of cache-hit tokens. First requests show null; subsequent requests with the same prefix show cached tokens.

**Streaming**: Add `"stream": true` to the request body to enable Server-Sent Events (SSE) streaming responses. Each chunk follows the OpenAI SSE format.

### POST /v1/chat/completions

**Listen Port**: Service Port

**Functionality**: Process chat conversation inference requests, supporting conversational AI inference services.

**Handler**: RouteServer forwards to appropriate inference instances based on load balancing strategy.

**Caller**: Client Applications, AI Service Callers

**Constraints/Conditions**:
- Must have available inference instances
- RouteServer service is normal
- Network connection is normal

**Parameters**:
```json
{
    "model": "model_name",
    "messages": [
        {
            "role": "user",
            "content": "Hello"
        }
    ],
    "max_tokens": 1000,
    "temperature": 0.7
}
```

**Core Business Logic**:
- Receive chat inference request from client
- Select appropriate inference instance based on scheduling algorithm (based on load, model matching, etc.)
- Forward request to selected inference instance
- Instance executes Prefill and/or Decode inference
- Return inference result to client

**Response Example**:
```json
{
    "id": "chatcmpl-xxxxx",
    "object": "chat.completion",
    "created": 1716161616,
    "model": "model_name",
    "choices": [
        {
            "index": 0,
            "message": {
                "role": "assistant",
                "content": "Hello! How can I help you today?"
            },
            "finish_reason": "stop"
        }
    ],
    "usage": {
        "prompt_tokens": 10,
        "completion_tokens": 9,
        "total_tokens": 19
    }
}
```

### POST /v1/completions

**Listen Port**: Service Port

**Functionality**: Process text completion inference requests, supporting text generation AI services.

**Handler**: RouteServer forwards to appropriate inference instances based on load balancing strategy.

**Caller**: Client Applications, AI Service Callers

**Constraints/Conditions**:
- Must have available inference instances
- RouteServer service is normal
- Request prompt length does not exceed model limit

**Parameters**:
```json
{
    "model": "model_name",
    "prompt": "Once upon a time",
    "max_tokens": 1000,
    "temperature": 0.7
}
```

**Core Business Logic**:
- Receive text completion request from client
- Validate request parameters and prompt length
- Select appropriate inference instance based on scheduling algorithm
- Forward request to selected inference instance
- Instance executes text generation inference
- Return generated text result

**Response Example**:
```json
{
    "id": "cmpl-xxxxx",
    "object": "text_completion",
    "created": 1716161616,
    "model": "model_name",
    "choices": [
        {
            "text": "Once upon a time, in a faraway kingdom lived a princess...",
            "index": 0,
            "logprobs": null,
            "finish_reason": "length"
        }
    ],
    "usage": {
        "prompt_tokens": 4,
        "completion_tokens": 20,
        "total_tokens": 24
    }
}
```

### POST /v1/metaserver

**Listen Port**: Service Port

**Functionality**: Callback interface for layerwise PD Disaggregation inference mode, used by inference instances to transmit KV Cache transfer metadata.

**Handler**: RouteServer's metadata manager handles the request.

**Caller**: Inference Instances

**Constraints/Conditions**:
- Only used in layerwise PD Disaggregation inference mode
- Requester must be a registered inference instance
- Need to verify request legitimacy

**Parameters**:
```json
{
    "kv_transfer_params": {
        "request_id": "req_xxxxx",
        "do_remote_decode": true,
        "do_remote_prefill": false,
        "metaserver": "http://metaserver.address:port"
    }
}
```

**Core Business Logic**:
- Verify inference instance identity and permissions
- Receive and parse KV Cache transfer parameters
- Return HTTP 200 on success, 503 on service unavailable

**Response**: Returns simple HTTP status code (200 on success, 503 if service unavailable) with no structured JSON body.

## 4. Scheduling Request API

### POST /schedule

**Listen Port**: Operations Port

**Functionality**: Distributed remote scheduling interface, processes inference task scheduling requests, and allocates inference resources based on cluster status and load conditions.

**Handler**: RouteServer's scheduler processor handles the request.

**Caller**: Internal Services, Inference Instances

**Constraints/Conditions**:
- RouteServer is in running state
- At least one available instance in the cluster

**Parameters**:
```json
{
    "req": {
        "id": "chatcmpl-xxx",
        "type": "prefill",
        "slo": "default",
        "slo-intervals": null,
        "max-tokens": 1000,
        "prompt-tokens": 128,
        "stream": false,
        "prompt-cache-key": "",
        "apc-strategy-type": ""
    },
    "size": 1,
    "pre_id": "",
    "params": {},
    "worker_id": "route1",
    "prompt_key": ""
}
```

**Core Business Logic**:
- Parse scheduling request to obtain task requirements
- Check cluster resource status and instance load
- Select optimal instance based on scheduling strategy (considering load, latency, cost, etc.)
- Allocate resources for request and generate scheduling token
- Record scheduling information for subsequent tracking
- Return scheduling result and target instance information

**Response Example**:
```json
{
    "res": "instance_id",
    "slo": "default",
    "skipped": false
}
```

| Field | Type | Description |
|-------|------|-------------|
| res | string | Selected instance ID |
| slo | string | Service level tag |
| skipped | bool | Whether some instances were skipped during scheduling |

### GET /scheduler/load

**Listen Port**: Service Port

**Functionality**: Get load information of each node in the cluster for load balancing and scheduling decisions.

**Handler**: The requested RouteServer receives and forwards to the cluster Leader for processing.

**Caller**: Operations Platform, Monitoring System

**Constraints/Conditions**:
- Requires available cluster Leader through adminLeaderProxyMiddleware
- When Leader is unavailable, this endpoint is not available and will return a non-200 status code

**Query Parameters**:

| Parameter | Type | Description | Default |
|-----------|------|-------------|---------|
| include_requests | bool | Whether to include per-request load info | false |
| include_unavailable | bool | Whether to include isolated instance load info | false |
| role | string | Target instance role filter (e.g., "prefill,decode") | empty (all roles) |

**Core Business Logic**:
- Get scheduler state from the cluster Leader
- Collect load information per role and per instance
- Return load data for scheduling analysis

**Response Example**:
```json
{
    "rs_name": "route1",
    "loads": {
        "prefill": {
            "slo_group_loads": {
                "default": {
                    "algorithm": "default",
                    "instance_loads": [
                        {
                            "id": "prefill_01",
                            "load": 0.5,
                            "desc": "requests:5,total_prompt_tokens:2560,batch_size:5,max_num_seqs:256",
                            "requests": {
                                "chatcmpl-1": {
                                    "load": 512,
                                    "desc": "prompt_tokens:512"
                                }
                            }
                        }
                    ]
                }
            }
        },
        "decode": {
            "slo_group_loads": {
                "default": {
                    "algorithm": "default",
                    "instance_loads": [
                        {
                            "id": "decode_01",
                            "load": 0.3,
                            "desc": "requests:3,total_prompt_tokens:1280,batch_size:3,max_num_seqs:256",
                            "requests": {}
                        }
                    ]
                }
            }
        }
    }
}
```

| Response Field | Description                                                 |
|----------------|-------------------------------------------------------------|
| rs_name | RouteServer name                                       |
| loads | Map of role -> SLOLoadInfo                                  |
| slo_group_loads | Map of SLO group tag -> group load info                     |
| algorithm | Scheduling algorithm used                                   |
| instance_loads | List of per-instance load info                              |
| id | Instance ID                                                 |
| load | Calculated load value                                       |
| desc | Load description (requests count, token counts, batch size) |
| requests | Per-request load details (only when include_requests=true)  |

## 5. Instance Management API

### POST /instance_requests

**Listen Port**: Service Port

**Functionality**: Process instance broadcast requests for inter-instance communication and coordination.

**Handler**: RouteServer's instance manager handles the request.

**Caller**: Other instances or services within the cluster

**Constraints/Conditions**:
- Requester must be in the cluster whitelist
- Request path must be configured in BroadcastURIWhitelist

**Parameters**:
```json
{
    "targets": "instance_name_or_all",
    "uri": "/health",
    "method": "GET",
    "headers": {},
    "body": null
}
```

| Field | Type | Description |
|-------|------|-------------|
| targets | string | Target instance(s). "all" for broadcast, or specific instance name |
| uri | string | URI path to request on target instance(s) |
| method | string | HTTP method (GET, POST, etc.) |
| headers | object | HTTP headers to include in the forwarded request |
| body | raw JSON | Request body for the forwarded request |

**Core Business Logic**:
- Verify request source and permissions
- Forward request to target instance(s) based on targets field
- Collect responses from all target instances
- Return aggregated results

**Response Example**:
```json
{
    "status": "success",
    "total": {
        "total": 2,
        "success": 2,
        "failed": 0
    },
    "responses": [
        {
            "instance_id": "prefill_01",
            "status": 200,
            "body": {},
            "error": ""
        }
    ]
}
```

| Field | Type | Description |
|-------|------|-------------|
| status | string | Overall status: "success", "partial", or "failed" |
| total | object | Statistics of instance responses |
| responses | array | List of per-instance response details |

### POST /internal/worker/force-readiness

**Listen Port**: Operations Port

**Functionality**: Force-set the readiness state of the RouteServer Worker node (emergency escape channel). When the circuit breaker auto-judgment behaves abnormally, this interface can be used to manually intervene in the Worker's readiness state, controlling whether k8s distributes traffic to this Worker.

**Handler**: Instance manager (circuit breaker) handles locally — NOT forwarded to Leader.

**Caller**: Operations Platform

**Constraints/Conditions**:
- Only effective when the circuit breaker is enabled (`circuit-breaker.enable: true`)
- When circuit breaker is not enabled, returns `disabled` status
- This interface is only bound to the Operations Port
- The circuit breaker maintains the forced mode until changed again or RouteServer restarts

**Parameters**:

| Parameter | Location | Type | Required | Description |
|-----------|----------|------|----------|-------------|
| mode | Body (JSON) | string | No | Force mode. Values: `true` / `false` / `auto`. Default: `auto` |

**Request Body**:
```json
{
    "mode": "true"
}
```

**Mode Parameter Description**:

| Value | Aliases | Behavior |
|-------|---------|----------|
| `true` | `1`, `yes` | Force ready — readiness probe returns 200, k8s assigns traffic |
| `false` | `0`, `no` | Force not ready — readiness probe returns 500, k8s stops traffic |
| `auto` | `2` | Auto mode — circuit breaker judges readiness based on failure rate |

**Core Business Logic**:
1. Check if circuit breaker is enabled; if not, return `disabled`
2. Parse `mode` from request body (default `auto` if missing)
3. Set circuit breaker force mode accordingly
4. Log the operation and return result

**Response Examples**:

```json
// mode=true (force ready)
{
    "status": "success",
    "mode": "true",
    "ready": true,
    "message": "force ready (accepting traffic)"
}

// mode=false (force not ready)
{
    "status": "success",
    "mode": "false",
    "ready": false,
    "message": "force not ready (rejecting traffic)"
}

// mode=auto (circuit breaker auto-judgment)
{
    "status": "success",
    "mode": "auto",
    "ready": true,
    "message": "auto mode (internal logic)"
}

// Circuit breaker not enabled
{
    "status": "disabled",
    "message": "circuit breaker is not enabled"
}

// Invalid mode parameter (HTTP 400)
{
    "status": "invalid",
    "message": "invalid mode: xxx, use: true/false/auto"
}

// Setting failed (HTTP 500)
{
    "status": "failed",
    "message": "failed to set circuit breaker mode"
}
```

## 6. Request Management API

### POST /internal/abort

**Listen Port**: Operations Port

**Functionality**: Abort running inference requests, used for request cancellation and resource release.

**Handler**: Running request manager handles the request.

**Caller**: Operations Platform, Monitoring System

**Constraints/Conditions**:
- Request must be in running state (tracked by RouteServer's request context manager)

**Parameters**:
```json
{
    "req_ids": [
        "chatcmpl-1",
        "chatcmpl-2",
        "chatcmpl-3"
    ]
}
```

| Field | Type | Description |
|-------|------|-------------|
| req_ids | array of string | List of request IDs to abort |

**Core Business Logic**:
1. RouteServer Worker tracks each request using context before forwarding to P or D instances
2. On receiving abort request, traverse the request list, cancel matching request contexts, triggering HTTP request auto-termination
3. Record successfully aborted req_ids and return the result

**Response Example**:
```json
{
    "aborted_req_ids": [
        "chatcmpl-1"
    ],
    "not_found_req_ids": [
        "chatcmpl-2",
        "chatcmpl-3"
    ]
}
```

| Field | Type | Description |
|-------|------|-------------|
| aborted_req_ids | array of string | Request IDs that were successfully aborted |
| not_found_req_ids | array of string | Request IDs not found or already completed |

**Error Scenarios**:

| Status Code | Scenario | Response |
|-------------|----------|----------|
| 400 | Missing request body | `{"error": "failed to read body, err = %s"}` |
| 400 | Malformed JSON | `{"error": "failed to unmarshal request body, err = %s"}` |
| 400 | Empty req_ids | `{"error": "request body is empty or contains no req_ids"}` |

### GET /internal/running_details

**Listen Port**: Operations Port

**Functionality**: View active request information for each instance under the specified RouteServer process.

**Handler**: Running request manager handles locally — NOT forwarded to Leader.

**Caller**: Operations Platform, Monitoring System

**Constraints/Conditions**:
- Requires management permissions

**Parameters**: No request parameters

**Core Business Logic**:
1. RouteServer Worker tracks each request using context before forwarding to P or D instances
2. On receiving request, traverse the request list and collect status of active requests on each P and D instance
3. Return each instance's name, role, and detailed status of each request

**Response Example**:
```json
{
    "server_name": "route1",
    "request_lists": {
        "chatcmpl-1": {
            "finished_phases": {
                "prefill": "instance:prefill_01,start:1745568000000,end:1745568020000"
            },
            "running_phases": {
                "decode": "instance:decode_01,start:1745568020000,end:0"
            },
            "waiting_phases": []
        },
        "chatcmpl-2": {
            "finished_phases": {},
            "running_phases": {
                "prefill": "instance:prefill_01,start:1745568030000,end:0"
            },
            "waiting_phases": ["decode"]
        }
    }
}
```

| Field | Type | Description |
|-------|------|-------------|
| server_name | string | RouteServer name |
| request_lists | map | Map of request_id -> request details |
| request_lists.{id}.finished_phases | map | Completed phases with instance and timing info |
| request_lists.{id}.running_phases | map | Currently running phases with instance and timing info |
| request_lists.{id}.waiting_phases | array | Phases waiting to be executed |

## 7. Cluster Management API

### POST /cluster/peer/join

**Listen Port**: Operations Port

**Functionality**: Handle node joining cluster requests.

**Handler**: The requested RouteServer receives and forwards to the cluster Leader for processing.

**Caller**: Node to be joined

**Constraints/Conditions**:
- Requires available cluster Leader through adminLeaderProxyMiddleware
- When Leader is unavailable, this endpoint is not available and will return a non-200 status code
- New node must be network-reachable with the cluster

**Parameters**:
```json
{
    "node_name": "node_name",
    "peer_addr": {
        "host": "ip_address",
        "service_port": 9800,
        "ops_port": 9810,
        "domain": "domain_name"
    },
    "is_candidate": true
}
```

| Field | Type | Description |
|-------|------|-------------|
| node_name | string | Node name |
| peer_addr.host | string | Node IP address |
| peer_addr.service_port | int | Service port (HTTP) |
| peer_addr.ops_port | int | Operations port |
| peer_addr.domain | string | Domain name (optional) |
| is_candidate | bool | Whether this node participates in Leader election |

**Core Business Logic**:
- Leader verifies the legitimacy of the new node
- Check network connectivity and version compatibility
- Add new node to cluster member list
- Update cluster state persistence
- Notify all cluster members of new node joining
- If it's a candidate node, join Leader election

**Response**: `{"result": "accepted"}` on success, or error JSON on failure.

### POST /cluster/peer/leave

**Listen Port**: Operations Port

**Functionality**: Handle node leaving cluster requests.

**Handler**: The requested RouteServer receives and forwards to the cluster Leader for processing.

**Caller**: Node to leave

**Constraints/Conditions**:
- Requires available cluster Leader through adminLeaderProxyMiddleware
- When Leader is unavailable, this endpoint is not available and will return a non-200 status code
- Leaving node cannot be the current Leader (need to transfer Leader role first)

**Parameters**:
```json
{
    "name": "node_name"
}
```

| Field | Type | Description |
|-------|------|-------------|
| name | string | Name of the node to leave the cluster |

**Core Business Logic**:
- Verify request legitimacy
- If node is current Leader, trigger Leader election
- Remove node from cluster member list
- Migrate requests on that node to other nodes
- Update cluster state persistence
- Notify all cluster members of node leaving

**Response**: `{"result": "accepted"}` on success, or error JSON on failure.

### POST /cluster/instance/join

**Listen Port**: Operations Port

**Functionality**: Handle instance joining cluster requests.

**Handler**: The requested RouteServer receives and forwards to the cluster Leader for processing.

**Caller**: Inference Instance

**Constraints/Conditions**:
- Requires available cluster Leader through adminLeaderProxyMiddleware
- When Leader is unavailable, this endpoint is not available and will return a non-200 status code
- Instance network must be reachable

**Parameters**:
```json
{
    "instances": {
        "instance1": {
            "ip": "*.*.*.*",
            "port": 1234,
            "role": "prefill",
            "tag": "short",
            "static-infos": {
                "max_num_seqs": "16"
            }
        },
        "instance2": {
            "ip": "*.*.*.*",
            "port": 1235,
            "role": "decode",
            "tag": "",
            "static-infos": {
                "max_num_seqs": "32"
            }
        }
    }
}
```

| Field | Type | Description |
|-------|------|-------------|
| instances | map | Map of instance name -> instance config. Instance name must be globally unique. |
| instances.{name}.ip | string | Instance IP address |
| instances.{name}.port | int | Instance port for generate/health requests |
| instances.{name}.role | string | Instance role: "prefill" or "decode" |
| instances.{name}.tag | string | SLO instance group identifier (optional) |
| instances.{name}.static-infos | object | Static config for scheduling algorithm (e.g., max_num_seqs) |

**Core Business Logic**:
- Add multiple instances to cluster state transactionally (all-or-nothing)
- If any instance fails validation, none are added
- Register instance to cluster routing table
- Update scheduler's available instance list

**Response**: `{"result": "accepted"}` on success.

**Error Response Example**:
```json
{
    "error": "no leader elected, can't process cluster admin requests"
}
```

### POST /cluster/instance/leave

**Listen Port**: Operations Port

**Functionality**: Handle instance leaving cluster requests.

**Handler**: The requested RouteServer receives and forwards to the cluster Leader for processing.

**Caller**: Inference Instance

**Constraints/Conditions**:
- Requires available cluster Leader through adminLeaderProxyMiddleware
- When Leader is unavailable, this endpoint is not available and will return a non-200 status code
- Instances processing requests need to exit gracefully

**Parameters**:
```json
{
    "instance_names": [
        "instance1",
        "instance4"
    ]
}
```

| Field | Type | Description |
|-------|------|-------------|
| instance_names | array of string | List of instance names to remove. Non-existent names are silently ignored. Empty strings are rejected. |

**Core Business Logic**:
- Remove specified instances from cluster state
- Stop distributing new requests to those instances
- Update cluster state persistence
- Non-existent instance names are ignored

**Response**: `{"result": "accepted"}` on success.

**Error Response Example**:
```json
{
    "error": "no leader elected, can't process cluster admin requests"
}
```

### POST /cluster/abort

**Listen Port**: Operations Port

**Functionality**: Abort specified requests across the entire RouteServer cluster.

**Handler**: The requested RouteServer processes locally — NOT forwarded to Leader via middleware. Contains internal `IsLeader()` check that returns 503 if not leader.

**Caller**: Operations Platform

**Constraints/Conditions**:
- Only the cluster Leader processes the request; non-leader nodes return 503
- Request must be in running state

**Parameters**:
```json
{
    "req_ids": [
        "chatcmpl-1",
        "chatcmpl-2",
        "chatcmpl-3"
    ]
}
```

| Field | Type | Description |
|-------|------|-------------|
| req_ids | array of string | List of request IDs to abort across the cluster |

**Core Business Logic**:
1. Receiving RouteServer checks if it is the Leader; if not, returns 503
2. Leader forwards the abort request to all Worker nodes via `/internal/abort`
3. Leader collects all responses and returns the aggregated result

**Response Example**:
```json
{
    "aborted_req_ids": [
        "chatcmpl-1"
    ],
    "not_found_req_ids": [
        "chatcmpl-2",
        "chatcmpl-3"
    ]
}
```

| Field | Type | Description |
|-------|------|-------------|
| aborted_req_ids | array of string | Request IDs successfully aborted across the cluster |
| not_found_req_ids | array of string | Request IDs not found or already completed |

**Error Scenarios**:

| Status Code | Scenario | Response |
|-------------|----------|----------|
| 400 | Missing/empty req_ids | `{"error": "request body is empty or contains no req_ids"}` |
| 400 | Malformed JSON | `{"error": "failed to unmarshal request body, err = %s"}` |
| 503 | No leader/cluster not started | `{"error": "no state found"}` |

**Warning Scenarios** (Worker communication failures are logged but do not cause request failure):
- Worker address invalid: `WARN worker [%s] address is invalid`
- Request creation failure: `WARN failed to create request for worker [%s]`
- Request send failure: `WARN failed to send request for worker [%s]`
- Response parse failure: `WARN failed to unmarshal JSON response from worker [%s]`
- Non-success status code: `WARN received non-successful status code from worker [%s], status = %d`

### GET /cluster/leader

**Listen Port**: Service Port & Operations Port

**Functionality**: Get current cluster Leader information.

**Handler**: The requested RouteServer receives and forwards to the cluster Leader for processing.

**Caller**: Cluster nodes, Monitoring System

**Constraints/Conditions**:
- Path is NOT in LeaderOperationSet — processes locally, does NOT forward to Leader
- Returns 503 if no leader is elected

**Parameters**: No request parameters

**Core Business Logic**:
- Query current cluster Leader from local cluster state
- Return Leader's ID and peer information
- If no Leader, return appropriate error

**Response Example**:
```json
{
    "id": "rs-node-01",
    "peer_info": {
        "node_name": "rs-node-01",
        "peer_addr": {
            "host": "*.*.*.*",
            "service_port": 9800,
            "ops_port": 9810,
            "domain": ""
        },
        "is_candidate": true
    }
}
```

| Field | Type | Description |
|-------|------|-------------|
| id | string | Leader node name |
| peer_info.node_name | string | Leader peer name |
| peer_info.peer_addr.host | string | Leader host address |
| peer_info.peer_addr.service_port | int | Leader service port |
| peer_info.peer_addr.ops_port | int | Leader operations port |
| peer_info.is_candidate | bool | Whether the leader is a candidate node |

**No Leader Response**: HTTP 503 with `{"error": "No elected leader is found for this endpoint"}`

### GET /cluster/local/state

**Listen Port**: Service Port & Operations Port

**Functionality**: Get local node status information.

**Handler**: The requested RouteServer receives and forwards to the cluster Leader for processing.

**Caller**: Monitoring System, Operations Platform

**Constraints/Conditions**:
- Path is NOT in LeaderOperationSet — processes locally, does NOT forward to Leader
- Returns 503 if cluster state is not yet initialized

**Parameters**: No request parameters

**Core Business Logic**:
- Get local cluster state
- Return the full cluster state including peers, instance routes, and version

**Response Example**:
```json
{
    "state": {
        "initialized": true,
        "seed_peers": [],
        "peers": {
            "rs-node-01": {
                "node_name": "rs-node-01",
                "peer_addr": {
                    "host": "*.*.*.*",
                    "service_port": 9800,
                    "ops_port": 9810,
                    "domain": ""
                },
                "is_candidate": true
            },
            "rs-node-02": {
                "node_name": "rs-node-02",
                "peer_addr": {
                    "host": "*.*.*.*",
                    "service_port": 9800,
                    "ops_port": 9810,
                    "domain": ""
                },
                "is_candidate": false
            }
        },
        "instance-routes": {
            "prefill_01": {
                "ip": "*.*.*.*",
                "port": 9000,
                "role": "prefill",
                "static-infos": {
                    "max_num_seqs": "16"
                },
                "tag": ""
            }
        },
        "version": 15
    }
}
```

| Field | Type | Description |
|-------|------|-------------|
| state.initialized | bool | Whether cluster state is initialized |
| state.seed_peers | array | Seed peer configuration |
| state.peers | map | Map of peer name -> Peer info (all cluster nodes) |
| state.instance-routes | map | Map of instance name -> Route config (all registered instances) |
| state.version | int | Cluster state version number |

### GET /cluster/consistent_status

**Listen Port**: Service Port

**Functionality**: Get cluster consistency status information.

**Handler**: The requested RouteServer receives and forwards to the cluster Leader for processing.

**Caller**: Monitoring System, Operations Platform

**Constraints/Conditions**:
- Path is NOT in LeaderOperationSet — processes locally, does NOT forward to Leader

**Parameters**: No request parameters

**Core Business Logic**:
- Get cluster consistency statistics from local cluster service
- Return map of stats key-value pairs

**Response Example**:
```json
{
    "stats": {
        "peers_version": "15",
        "instances_version": "12",
        "last_updated": "2025-05-20T10:30:00Z"
    }
}
```

| Field | Type | Description |
|-------|------|-------------|
| stats | map[string]string | Map of consistency statistics key-value pairs |

### GET /cluster/running_details

**Listen Port**: Operations Port

**Functionality**: Get details of running tasks in the cluster.

**Handler**: The requested RouteServer receives and forwards to the cluster Leader for processing.

**Caller**: Monitoring System, Operations Platform

**Constraints/Conditions**:
- Path is NOT in LeaderOperationSet — processes locally, does NOT forward to Leader
- Contains internal `IsLeader()` check — non-leader returns 503

**Parameters**: No request parameters

**Core Business Logic**:
1. Receiving RouteServer checks if it is the Leader; if not, returns 503
2. Leader sends `/internal/running_details` request to all Worker nodes
3. Leader collects all Worker responses and returns the aggregated result

**Response Example**:
```json
{
    "server_lists": [
        {
            "server_name": "route1",
            "request_lists": {
                "chatcmpl-1": {
                    "finished_phases": {
                        "prefill": "instance:prefill_01,start:1745568000000,end:1745568020000"
                    },
                    "running_phases": {
                        "decode": "instance:decode_01,start:1745568020000,end:0"
                    },
                    "waiting_phases": []
                },
                "chatcmpl-2": {
                    "finished_phases": {},
                    "running_phases": {
                        "prefill": "instance:prefill_01,start:1745568030000,end:0"
                    },
                    "waiting_phases": ["decode"]
                }
            }
        },
        {
            "server_name": "route2",
            "request_lists": {
                "chatcmpl-3": {
                    "finished_phases": {},
                    "running_phases": {
                        "decode": "instance:decode_02,start:1745568040000,end:0"
                    },
                    "waiting_phases": ["prefill"]
                }
            }
        }
    ]
}
```

| Field | Type | Description |
|-------|------|-------------|
| server_lists | array | List of per-server running details |
| server_name | string | RouteServer name |
| request_lists | map | Map of request_id -> phase-level request details |

## 8. Monitoring Metrics API

### GET /metrics

**Listen Port**: Service Port

**Functionality**: Get Prometheus format monitoring metrics for RouteServer and cluster.

**Handler**: The requested RouteServer receives and forwards to the cluster Leader for processing.

**Caller**: Prometheus Monitoring System, Operations Platform

**Constraints/Conditions**:
- Requires available cluster Leader through adminLeaderProxyMiddleware
- When Leader is unavailable, this endpoint is not available and will return a non-200 status code

**Parameters**: No request parameters

**Core Business Logic**:
- Collect RouteServer's own operational metrics
- Aggregate metrics data from all instances in the cluster
- Format Prometheus metrics format text
- Include request volume, response time, error rate, resource usage, etc.
- Support label filtering and aggregate queries

**Response Example**:
```
# HELP route_server_requests_total Total number of requests
# TYPE route_server_requests_total counter
route_server_requests_total{method="POST",endpoint="/v1/chat/completions",status="200"} 1500

# HELP route_server_request_duration_seconds Request duration in seconds
# TYPE route_server_request_duration_seconds histogram
route_server_request_duration_seconds_bucket{le="0.1"} 100
route_server_request_duration_seconds_bucket{le="0.5"} 800
route_server_request_duration_seconds_bucket{le="1.0"} 1450
route_server_request_duration_seconds_bucket{le="+Inf"} 1500

# HELP cluster_instances_total Total number of instances in cluster
# TYPE cluster_instances_total gauge
cluster_instances_total{role="prefill"} 3
cluster_instances_total{role="decode"} 3

# HELP cluster_instance_load_ratio Instance load ratio
# TYPE cluster_instance_load_ratio gauge
cluster_instance_load_ratio{instance="prefill_01"} 0.65
cluster_instance_load_ratio{instance="decode_01"} 0.72
```

### GET /internal/metrics

**Listen Port**: Service Port

**Functionality**: Get the RouteServer process's Prometheus monitoring metrics. Registered directly on the engine (no leader proxy).

**Handler**: Internal metrics collector handles locally — NOT forwarded to Leader.

**Caller**: Monitoring System

**Constraints/Conditions**: No special permissions required

**Query Parameters**:

| Parameter | Type | Description | Default |
|-----------|------|-------------|---------|
| only_rs | string | If "true", only returns rs-prefixed metrics (excludes Go runtime metrics) | empty |

**Core Business Logic**:
- Collect RouteServer process's operational metrics
- Return Prometheus text format metrics

**Response Format**: Prometheus text format (not JSON)

**Performance Metrics**:

| Metric | Type | Description | Internal Name |
|--------|------|-------------|---------------|
| num_failed_requests | Counter | Failed request count | rs:num_failed_requests |
| num_total_requests | Counter | Successful request count | rs:num_successed_requests |
| num_schedule_requests | Counter | Scheduled request count | rs:num_scheduled_requests |
| request_total_time_sec | Histogram | Request total time distribution | rs:request_total_time_secs |
| request_schedule_time_ms | Histogram | Schedule time distribution (by Prefill/Decode) | rs:request_schedule_time |
| time_to_first_token_sec | Histogram | TTFT distribution | rs:time_to_first_token_sec |
| prompt_token_num | Histogram | Prompt token count per request | rs:prompt_token_num |
| time_per_output_token_ms | Histogram | TPOT distribution | rs:time_per_output_token_ms |
| tokens_output_per_request | Histogram | Output tokens per request | rs:tokens_output_per_request |
| total_output_tokens | Counter | Total output tokens | rs:num_total_output_tokens |
| number_healthy_endpoints | Gauge | Healthy endpoints by role | rs:number_healthy_endpoints{role="decode\|prefill"} |
| infer_requests_failure_status_total | Counter | Inference request status codes | rs:infer_requests_failure_status_total |

## 9. Health Probe API

### GET /internal/liveness

**Listen Port**: Service Port

**Functionality**: Kubernetes Liveness Probe — check if service process is alive (not stuck).

**Handler**: Health probe handles locally — NOT forwarded to Leader.

**Caller**: Kubernetes, Health Check System

**Constraints/Conditions**: No special permissions required

**Parameters**: No request parameters

**Core Business Logic**:
- Simple process liveness check
- Returns HTTP 200 with plain text "ok"

**Response**: Plain text `ok` with HTTP 200 status code.

### GET /internal/leader/readiness

**Listen Port**: Service Port

**Functionality**: Check Leader role readiness status. Only checks if the current node is the cluster Leader.

**Handler**: Health probe handles locally — NOT forwarded to Leader.

**Caller**: Kubernetes, Health Check System

**Constraints/Conditions**: No special permissions required

**Parameters**: No request parameters

**Core Business Logic**:
- Check if current node is the cluster Leader
- Returns JSON with is_leader boolean

**Response Example**:
```json
{
    "is_leader": true
}
```

### GET /internal/worker/readiness

**Listen Port**: Service Port

**Functionality**: Check Worker node readiness status for k8s readiness probe. Determines whether the current Worker should receive traffic. Based on a three-layer judgment logic: first checks if force mode is enabled > then checks health cache > finally checks circuit breaker state.

**Handler**: Health probe handles locally — NOT forwarded to Leader.

**Caller**: Kubernetes, Health Check System

**Constraints/Conditions**: No special permissions required

**Parameters**: No request parameters

**Core Business Logic (Three-Layer Judgment)**:

1. **Layer 1: Force mode check** (whether circuit breaker has set force mode)
   - `ForceModeTrue` → return ready (HTTP 200)
   - `ForceModeFalse` → return not ready (HTTP 500)
   - `ForceModeNone` → continue to next layer

2. **Layer 2: Health cache check** (`InstanceHealthCache`)
   - Cache valid → check if instance count per role meets minimum requirements
     - Satisfied → ready, state=3 (normal)
     - Insufficient → not ready, state=3 (normal)
   - Cache invalid → continue to next layer

3. **Layer 3: Circuit breaker check** (when circuit breaker is enabled)
   - Circuit breaker open (StateOpen) → not ready, state=4 (broken)
   - Circuit breaker closed (StateClosed) → ready, state=3 (normal)

**Response Fields**:

| Field | Type | Description |
|-------|------|-------------|
| is_ready | bool | Whether the worker is ready |
| message | string | Status description message |
| state | int | Worker readiness state code (only when circuit breaker enabled) |
| state_name | string | State name (only when circuit breaker enabled) |

**WorkerReadinessState Enum**:

| state | state_name | Description |
|-------|------------|-------------|
| 0 | disabled | Circuit breaker not enabled (not returned in API response) |
| 1 | force_true | Force ready — manually set to accept traffic |
| 2 | force_false | Force not ready — manually set to reject traffic |
| 3 | normal | Normal mode — auto-determined by health cache or circuit breaker |
| 4 | broken | Circuit broken — failure rate exceeded threshold |

**Response Examples**:

```json
// Layer 1: Force ready (circuit breaker force mode=true)
HTTP 200
{
    "is_ready": true,
    "message": "ready",
    "state": 1,
    "state_name": "force_true"
}

// Layer 1: Force not ready (circuit breaker force mode=false)
HTTP 500
{
    "is_ready": false,
    "message": "force not ready",
    "state": 2,
    "state_name": "force_false"
}

// Layer 2: Health check passed, minimum instance count satisfied
HTTP 200
{
    "is_ready": true,
    "message": "ready",
    "state": 3,
    "state_name": "normal"
}

// Layer 2: Health check failed, insufficient instances
HTTP 500
{
    "is_ready": false,
    "message": "the cluster isn't ready, 1 prefill required at least, but got 0 prefill available",
    "state": 3,
    "state_name": "normal"
}

// Layer 3: Circuit breaker open (high failure rate)
HTTP 500
{
    "is_ready": false,
    "message": "circuit breaker is open, service degraded due to high failure rate",
    "state": 4,
    "state_name": "broken"
}

// Layer 3: Circuit breaker closed (degraded service mode)
HTTP 200
{
    "is_ready": true,
    "message": "degraded service (circuit breaker closed)",
    "state": 3,
    "state_name": "normal"
}

// Fallback: Circuit breaker not enabled and no valid cache
HTTP 500
{
    "is_ready": false,
    "message": "no valid instance info and circuit breaker not enabled"
}
```

**Status Code Description**:

| HTTP Status | Meaning |
|-------------|---------|
| 200 OK | Worker ready, k8s can assign traffic |
| 500 Internal Server Error | Worker not ready, k8s should stop assigning traffic |

### GET /worker/readiness

**Listen Port**: Service Port

**Functionality**: Worker readiness status check (same handler as `/internal/worker/readiness`). Externally accessible alias.

**Handler**: Health probe handles locally — NOT forwarded to Leader.

**Caller**: External Health Check System

**Constraints/Conditions**: No special permissions required

**Parameters**: No request parameters

**Core Business Logic**: Same as `/internal/worker/readiness` — same handler with identical three-layer judgment logic.

**Response**: Same response format as `/internal/worker/readiness`.

## 10. Version Information API

### GET /

**Listen Port**: Service Port & Operations Port

**Functionality**: Get RouteServer version information.

**Handler**: Root path handler.

**Caller**: Any caller needing version information

**Constraints/Conditions**: No special permissions required

**Parameters**: No request parameters

**Core Business Logic**:
- Read version information injected at compile time
- Get current node name
- Assemble version information JSON response
- Used for version management and troubleshooting

**Response Example**:
```json
{
    "name": "xxxx",
    "node_name": "node-01",
    "version": "v1.2.3",
    "tag_or_branch": "release/v1.2.3",
    "commit_id": "xxxx1234",
    "build_time": "2025-05-20T08:00:00Z",
    "port_type": "service_port"
}
```

| Field | Type | Description |
|-------|------|-------------|
| name | string | Software name |
| node_name | string | Current node name |
| version | string | Software version |
| tag_or_branch | string | Git tag or branch name |
| commit_id | string | Git commit hash |
| build_time | string | Build timestamp |
| port_type | string | Port type: "service_port" or "operations_port" |

## 11. Event Sync Request API

### POST /request_event

**Listen Port**: Operations Port

**Functionality**: Distributed request event synchronization interface, used for request lifecycle management. Workers send request/instance events to the Leader for tracking and health monitoring.

**Handler**: Registered directly on opsEngine (NOT behind adminLeaderProxyMiddleware). Contains internal `IsLeader()` check that returns 503 if not leader.

**Caller**: Worker nodes, Inference instances

**Constraints/Conditions**:
- Only the Leader processes the request; non-leader returns 503
- Registered directly on opsEngine, NOT through adminLeaderProxyMiddleware

**Parameters**:
```json
{
    "id": "route1",
    "reqs": [
        {
            "request": {
                "id": "chatcmpl-1",
                "type": "prefill",
                "slo": "default",
                "max-tokens": 1000,
                "prompt-tokens": 128,
                "stream": false
            },
            "instance": "prefill_01",
            "preInstance": "",
            "type": 1,
            "failure-details": null,
            "time": 1745568000000,
            "test": false
        }
    ],
    "inst": [
        {
            "instance": "prefill_01",
            "type": 寿
        }
    ]
}
```

| Field | Type | Description |
|-------|------|-------------|
| id | string | Worker node ID |
| reqs | array | Request event list |
| reqs[].request | object | Request details (id, type, slo, etc.) |
| reqs[].instance | string | Target instance name |
| reqs[].preInstance | string | Previous instance name (for PD link tracking) |
| reqs[].type | int | Event type |
| reqs[].failure-details | object | Failure details if applicable |
| reqs[].time | int64 | Event timestamp (Unix ms) |
| inst | array | Instance event list |

**Core Business Logic**:
- Verify the sender is the cluster Leader (internal IsLeader check)
- Process request events for lifecycle tracking
- Process instance events for health monitoring
- Update worker health check timestamp (ping)
- Return available instance count per role

**Response Example**:
```json
{
    "prefill": 2,
    "decode": 3
}
```

**Response**: Returns `map[string]int` — role to available instance count mapping. On error, returns 503 or 500 with `{"error": "..."}`.

## 12. Blacklist Management API

### GET /link_black_instance

**Listen Port**: Service Port

**Functionality**: Get instance information in the link blacklist.

**Handler**: The requested RouteServer receives and forwards to the cluster Leader for processing.

**Caller**: Operations Platform, Monitoring System

**Constraints/Conditions**:
- Requires available cluster Leader through adminLeaderProxyMiddleware
- When Leader is unavailable, this endpoint is not available and will return a non-200 status code

**Parameters**: No request parameters

**Core Business Logic**:
- Get current blacklist list
- Collect detailed information for each blacklisted instance
- Include reason and time for being added to blacklist
- Statistics blacklist instance role distribution
- Provide blacklist management operation history

**Response Example**:
```json
{
    "total_healthy_instance": {
        "decode": 3,
        "prefill": 2
    },
    "instance_black_lists": {
        "decode": [
            {
                "instance_id": "decode_01",
                "blocks_size": 1,
                "link_black_list": [
                    {
                        "blocked_instance": "prefill_01",
                        "block_time": "2025-11-10T11:11:11.537+08:00",
                        "remaining_time_ms": 70050
                    }
                ]
            }
        ],
        "prefill": [
            {
                "instance_id": "prefill_01",
                "blocks_size": 1,
                "link_black_list": [
                    {
                        "blocked_instance": "decode_01",
                        "block_time": "2025-11-10T11:11:11.537+08:00",
                        "remaining_time_ms": 70050
                    }
                ]
            }
        ]
    }
}
```

| Field | Type | Description |
|-------|------|-------------|
| total_healthy_instance | map[string]int | Number of healthy instances by role (prefill/decode) |
| instance_black_lists | map[string]array | Instance blacklist details grouped by role |
| instance_id | string | Instance name with blocked links |
| blocks_size | int | Number of instances blocked by this instance |
| link_black_list | array | List of blocked instance details |
| blocked_instance | string | Name of the blocked instance |
| block_time | string | Timestamp when the block first occurred |
| remaining_time_ms | int64 | Milliseconds remaining until block is lifted |

## 13. Dynamic Proxy API

Dynamic proxy paths are dynamically registered according to configured whitelist, supporting GET and POST methods.

**Listen Port**: Service Port

**Functionality**: Proxy requests to specified inference instances, used to support custom API endpoints.

**Handler**: Instance manager's proxy handler.

**Caller**: Client Applications

**Constraints/Conditions**:
- Path must be configured in BroadcastURIWhitelist
- Cannot conflict with internally registered routes
- Requester needs permission to access target instance

**Parameters**: Depends on specific proxied API

**Core Business Logic**:
- Verify if request path is in whitelist
- Select target instance based on load balancing algorithm
- Forward request to selected instance
- Wait and return instance's response
- Record proxy logs for tracking and statistics
- Handle instance failures and retry logic

**Response Example** (using proxy /v1/embeddings as example):
```json
{
    "object": "list",
    "data": [
        {
            "object": "embedding",
            "embedding": [0.001, -0.002, 0.003, ...],
            "index": 0
        }
    ],
    "model": "text-embedding-ada-002",
    "usage": {
        "prompt_tokens": 8,
        "total_tokens": 8
    }
}
```

**Error Example**:
```json
{
    "error": {
        "message": "Proxy path not found or not authorized",
        "type": "proxy_error",
        "code": "path_not_found"
    }
}
```

## Notes

1. **Port Description**:
  - Service Port: Used for processing inference requests and most management APIs
  - Operations Port: Dedicated for operations tasks, such as force readiness setting

2. **Leader Proxy Description**:
  - The `adminLeaderProxyMiddleware` selectively forwards requests based on path:
    - Paths in `LeaderOperationSet` are forwarded to the Leader if the current node is not the Leader
    - Paths NOT in `LeaderOperationSet` are always processed locally (no forwarding, no leader check)
  - `LeaderOperationSet` includes: `/cluster/peer/join`, `/cluster/peer/leave`, `/cluster/instance/join`, `/cluster/instance/leave`, `/metrics`, `/status`, `/health`, `/link_black_instance`, `/scheduler/load`
  - Endpoints like `/cluster/leader`, `/cluster/local/state`, `/cluster/consistent_status`, `/cluster/running_details`, `/cluster/abort` are NOT in `LeaderOperationSet` and process locally
  - When Leader is unavailable, forwarded endpoints return non-200 status codes

3. **Pipeline Mode Description**:
  - When configured in Pipeline mode, inference API endpoints are dynamically registered based on pipeline configuration
  - Default mode supports /v1/chat/completions and /v1/completions endpoints

4. **Instance Join/Leave Scripts**:
  - For batch instance registration, use `rs_instance_join.py` and `rs_instance_leave.py` scripts located in the `scale/` directory
  - These scripts call `/cluster/instance/join` and `/cluster/instance/leave` endpoints respectively
  - The scripts support cross-machine deployment by accepting multiple IPs and starting ports