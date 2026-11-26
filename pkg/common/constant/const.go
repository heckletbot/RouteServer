/*
 * Copyright 2026 The RouteServer Authors.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package constant

const (
	SoftwareName = "RouteServer"

	// common key enum
	Encode                string = "encode"
	Prefill               string = "prefill"
	Decode                string = "decode"
	MOE                   string = "moe"
	RequestSrcUser        string = "user"
	MetricsKeyPrefix      string = "dynamic"
	XRequestId            string = "X-Request-Id"
	XRouterId             string = "X-Router-Id"
	HeaderContentLength   string = "Content-Length"
	HeaderXSpanId         string = "x-span-id"
	HeaderXUserAlias      string = "x-user-alias"
	MonitorTypePrometheus string = "prometheus"
	MonitorTypeNone       string = "none"
	DeployingStatus       string = "deploying"
	FailedStatus          string = "failed"
	RunningStatus         string = "running"
	ConcerningStatus      string = "concerning"
	FaultStatus           string = "fault"
	PartialStatus         string = "partial"
	SuccessStatus         string = "success"

	// SLO const
	Inf           string = "Inf"
	SLOTagDefault string = "default"

	ClusterRole = "cluster_role"
	Msg         = "message"
	IsLeader    = "is_leader"
	IsReady     = "is_ready"
	PrefillNum  = "prefill_num"
	DecodeNum   = "decode_num"

	// cluster role enum
	ClusterRoleLeader    string = "leader"
	ClusterRoleWorker    string = "worker"
	ClusterRoleCandidate string = "candidate"
	ClusterRoleUnknown   string = "unknown"

	ClientTypeVllm string = "vllm"

	// scheduler api path
	ScheduleRequestPath = "schedule"
	RequestEventPath    = "request_event"

	EndpointType      = "endpoint"
	ClusterLeaderType = "leader"

	// EventType Enum
	RequestNew            int = 0
	RequestPrefillSuccess int = 1
	RequestPrefillFailed  int = 2
	RequestDecodeSuccess  int = 3
	RequestDecodeFailed   int = 4
	RequestEncodeSuccess  int = 5
	RequestEncodeFailed   int = 6
	OtherFailure          int = 7
	RequestExpired        int = 8

	MaxNumSeqs               string = "max_num_seqs" // Corresponds to the max_num_seqs parameter at instance startup, needs to be configured in StaticInfo, required by prefill scheduling
	UpdateIntervalMs         string = "update-interval-ms"
	MetricsOverdueIntervalMs string = "metrics-overdue-interval-ms"

	// vLLM metric names are defined once here so scheduling algorithms, the worker
	// state tables and the monitor client share one definition instead of keeping
	// their own literals. A metric is scraped only when a scheduler declares it in
	// NeedStatsList and the prometheus config lists it as well.
	// kv_cache_usage_perc is the vLLM 0.11+ name; 0.9/0.10 only export
	// vllm:gpu_cache_usage_perc, so an agentic scheduler needs vLLM >= 0.11.
	MetricNumRequestsRunning    string = "vllm:num_requests_running"
	MetricNumRequestsWaiting    string = "vllm:num_requests_waiting"
	MetricKVCacheUsagePerc      string = "vllm:kv_cache_usage_perc"
	MetricPromptTokensTotal     string = "vllm:prompt_tokens_total"
	MetricGenerationTokensTotal string = "vllm:generation_tokens_total"

	FirstTokenLatencyMs   = "FirstTokenLatencyMs"
	FirstDTokenLatencyMs  = "FirstDTokenLatencyMs"
	SecondDTokenLatencyMs = "SecondDTokenLatencyMs"

	HeadersContentType string = "Content-Type"
	HeadersXRequestID  string = "X-Request-Id"
	HeadersXSpanId     string = "x-span-id"

	MaxRequestEventsNum   int = 100000
	MaxConcurrentRequests int = 5000

	DefaultNoTimeout = -1

	NonStreamHangIntervalMs int64 = 30000

	InstanceChangeReasonSynced   = "state synced"
	InstanceChangeReasonRestored = "restored"

	UnKnownReason   = "unknown"
	ClientErrReason = "client"

	// message center topics
	LocalWorkerEventsTopic = "WorkerEvents"
	InstanceDownTopic      = "InstanceDown"

	// Health event synchronization interval in singleton and etcd cluster modes
	// Since use the local message centers, a smaller interval can be set in the singleton case.
	SingletonHealthEventIntervalMs = 50
	ClusterHealthEventIntervalMs   = 1000

	// StatusClientError is a non-standard HTTP error code, referencing nginx error codes,
	// indicating client unexpectedly disconnected or write to client timed out, etc.
	StatusClientError = 499

	// Execution phases
	StageEncode   = "Encode"
	StagePrepare  = "Prepare"
	StagePrefill  = "Prefill"
	StageDecode   = "Decode"
	StageFinalize = "Finalize"
	StageMOE      = "MOE"

	PDMode           = "PD"
	PDStreamMode     = "PD-Stream"
	EPDMode          = "EPD"
	EPDStreamMode    = "EPD-Stream"
	PDAMMode         = "PD-AM"
	EHybridMode      = "E-Hybrid"
	HybridMode       = "Hybrid"
	LayerwiseMode    = "Layerwise-CDCP"
	LayerwiseMMMode  = "Layerwise-CPCD"
	MetaServerMode   = "MetaServer-CDCP"
	MetaServerMMMode = "MetaServer-CPCD"

	TextType = "text"

	HybridModeMetricsYml = "metrics_config_vllm_0.12.0_hybrid.yaml"

	EnvServerConfigPath = "ENV_ROUTE_SERVER_CONFIG_PATH"

	// gin context key
	RewriteRequestID = "rewrite-request-id"

	// time
	MinuteToSec = 60
)

type InferStage string

const (
	InferStageEncode   InferStage = "Encode"
	InferStagePrepare  InferStage = "Prepare"
	InferStagePrefill  InferStage = "Prefill"
	InferStageDecode   InferStage = "Decode"
	InferStageFinalize InferStage = "Finalize"
	InferStageMOE      InferStage = "MOE"

	ExecutePhase  = "Execute"
	SchedulePhase = "Schedule"
	DummyPhase    = "Dummy"
)

// APCStrategyHash apc strategy
const APCStrategyHash = "hash"

// TLS private key decryption password environment variables (differentiated by usage)
const (
	// ClientKeyPasswordEnv is the environment variable name for the llm client private key decryption password
	ClientKeyPasswordEnv = "TLS_CLIENT_KEY_PASSWORD"

	// TLSPrivateKeyPasswordEnv Server private key decryption password (server.tls)
	TLSPrivateKeyPasswordEnv = "TLS_PRIVATE_KEY_PASSWORD"
	// TLSOpsKeyPasswordEnv Ops port private key decryption password (ops-tls)
	TLSOpsKeyPasswordEnv = "TLS_OPS_KEY_PASSWORD"

	// TLSEtcdKeyPasswordEnv is the environment variable name for the etcd client private key decryption password
	TLSEtcdKeyPasswordEnv = "TLS_ETCD_KEY_PASSWORD"
)

var ServerName string
