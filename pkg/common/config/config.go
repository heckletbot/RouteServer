/*
 * Copyright 2026 The vLLM-Cloud Authors.
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

package config

import (
	instance_metrics "infer-dp-scheduler/pkg/common/instance-metrics"
	"infer-dp-scheduler/pkg/common/logger"
	"infer-dp-scheduler/pkg/common/utils"
)

const (
	DefaultDialerTimeout       = 5   // in seconds
	DefaultDialerKeepAlive     = -1  // in seconds, disable by default
	DefaultRequestTimeout      = 100 // in seconds
	DefaultMaxConnsPerHost     = 2048
	DefaultMaxIdleConns        = 20480
	DefaultMaxIdleConnsPerHost = 1800
	DefaultIdleConnTimeout     = 10
	TraceTypeLog               = "log"
	TraceTypeNone              = "none"
	TraceTypeOtel              = "otel"
	TraceCompose               = "compose"
	DefaultJaegerServiceName   = "RouteServer"
	DefaultJaegerEndpoint      = ""

	DefaultCircuitBreakerFailureRateThreshold = 0.6
	DefaultCircuitBreakerMinRequestCount      = 10
	DefaultCircuitBreakerCheckWindowSec       = 600 // 10 minutes

	DefaultHealthCheckInterval            = 10000
	DefaultHealthCheckTimeout             = 1000
	DefaultHealthCheckBatchSize           = 5
	DefaultHealthCheckMaxConcurrency      = 10
	DefaultHealthCheckConsecutiveTime     = 3
	DefaultPassiveHealthCheckTimeWindowMs = 10000
	DefaultPassiveHealthCheckMaxFails     = 3
	DefaultSpanUpdateSeconds              = 5
	DefaultTraceSampleRate                = 1.0
)

type HeaderMatchMode struct {
	Header         string `yaml:"header"`
	PrefixMatching bool   `yaml:"prefix-matching"`
}

// RateLimitConfig extends the existing RateLimitCfg
type RateLimitConfig struct {
	// Existing field for backward compatibility
	MaxConcurrentRequests int `yaml:"max-concurrent-requests"`

	NonReferenceAPIs *NonReferenceAPIRateLimit `yaml:"non-reference-apis"`
}

// NonReferenceAPIRateLimit defines rate limiting configuration for non-reference APIs
type NonReferenceAPIRateLimit struct {
	// Enable rate limiting for non-reference APIs
	Enabled bool `yaml:"enabled"`

	// Requests per minute (default: 60)
	RequestsPerMinute int `yaml:"requests-per-minute" default:"60" validate:"min=1"`

	// List of exempt paths (reference APIs)
	ExemptPaths []string `yaml:"exempt-paths"`

	// Cleanup interval for expired client entries (default: 5 minutes)
	CleanupIntervalMinutes int `yaml:"cleanup-interval-minutes" default:"5" validate:"min=1"`
}

// RateLimitCfg maintains backward compatibility
type RateLimitCfg = RateLimitConfig

type TLSCertificateConfig struct {
	CertFile    string `yaml:"cert-file"`
	KeyFile     string `yaml:"key-file"`
	CaFile      string `yaml:"ca-cert-file,omitempty"`
	SkipVerify  bool   `yaml:"skip-verify"`
	EnableHTTPS bool   `yaml:"enable-https"`
	EnableMTLS  bool   `yaml:"enable-mtls"`
}

type ServerConfig struct {
	Addr                       string                `yaml:"addr"`
	HttpPort                   string                `yaml:"http-port"`
	OpsPort                    string                `yaml:"ops-port"`
	Name                       string                `yaml:"name"`
	InferMode                  string                `yaml:"infer-mode"`
	InferenceModuleType        string                `yaml:"inference-module-type"`
	RateLimitCfg               *RateLimitCfg         `yaml:"rate-limit"`
	TraceCfg                   *TraceConfig          `yaml:"trace,omitempty"`
	V1StreamWritingEnable      bool                  `yaml:"v1-stream-writing-enabled"`
	ResponseHeaderBlacklist    []HeaderMatchMode     `yaml:"response-header-blacklist"`
	ClientWriteTimeoutMs       int64                 `yaml:"client-write-timeout-ms"`
	ReWriteRequestID           bool                  `yaml:"rewrite-request-id"`
	TLSConfig                  *TLSCertificateConfig `yaml:"tls,omitempty"`
	OpsTLSConfig               *TLSCertificateConfig `yaml:"ops-tls,omitempty"`
	MaxRequestBodySizeMB       int64                 `yaml:"max-request-body-size-mb"`
	EnableOpsAPIsOnServicePort bool                  `yaml:"enable-ops-apis-on-service-port"`
}

type HttpConfig struct {
	DialerTimeout       int                   `yaml:"dialer-timeout"`
	DialerKeepAlive     int                   `yaml:"dialer-keepalive"`
	RequestTimeout      int                   `yaml:"request-timeout"`
	MaxIdleConns        int                   `yaml:"max-idle-conns"`
	MaxIdleConnsPerHost int                   `yaml:"max-idle-conns-per-host"`
	IdleConnTimeout     int                   `yaml:"idle-conn-timeout"`
	MaxConnsPerHost     int                   `yaml:"max-conns-per-host"`
	TLSConfig           *TLSCertificateConfig `yaml:"tls,omitempty"`
}

type TraceConfig struct {
	EnableTraceTokenLog bool       `yaml:"enable-trace-token-log" default:"false"`
	Type                []string   `yaml:"type,omitempty"`
	OtelConfig          OtelConfig `yaml:"otel-config,omitempty"`
}

type OtelConfig struct {
	JaegerEndpoint    string  `yaml:"jaeger-endpoint"`
	SpanUpdateSeconds int64   `yaml:"span-update-seconds"`
	JaegerServiceName string  `yaml:"jaeger-service-name"`
	TraceSampleRate   float64 `yaml:"trace-sample-rate"`
}

type RouteTable struct {
	// id -> route
	InstanceRoutes map[string]Route `yaml:"instance-routes"`
}

type Route struct {
	Ip          string            `yaml:"ip" json:"ip"`
	Port        int               `yaml:"port" json:"port"`
	Role        string            `yaml:"role" json:"role"`
	StaticInfos map[string]string `yaml:"static-infos,omitempty" json:"static-infos,omitempty"`
	Tag         string            `yaml:"tag" json:"tag"`
}

type MonitorCfg struct {
	MonitorClientType string                             `yaml:"monitor-client-type" default:"none"`
	PrometheusCfg     *instance_metrics.PrometheusConfig `yaml:"prometheus,omitempty"`
}

type MetricsCfg struct {
	Enable                         bool   `yaml:"enable" default:"false"`
	Duration                       int    `yaml:"duration" default:"60"`
	MetricsCollectorConfigFileName string `yaml:"metrics-collector-config-file-name"`
}

type InstanceManagerCfg struct {
	InstanceClientType string             `yaml:"instance-client-type" default:"vllm"`
	RouteTable         RouteTable         `yaml:"route-table"`
	URIWhitelist       []WhitelistEntry   `yaml:"broadcast-request-whitelist"`
	HealthCheckConfig  *HealthCheckConfig `yaml:"health-check-config,omitempty"`
	RequiredRoles      []string           `yaml:"required-roles"`
}

type PassiveHealthCheckCfg struct {
	Enabled           bool                      `yaml:"enabled"`
	TimeWindowMs      int                       `yaml:"time-window-ms" default:"10000" validate:"min=1"`
	MaxFailures       int                       `yaml:"max-failures" default:"3" validate:"min=0"`
	FailureConditions *PassiveFailureConditions `yaml:"failure-conditions,omitempty"`
}

type PassiveFailureConditions struct {
	FailOnRequestTimeout bool  `yaml:"request-timeout"`
	FailureStatusCodes   []int `yaml:"status-codes"`
}

type Config struct {
	LogConfig          logger.Config      `yaml:"logger"`
	ServerConfig       ServerConfig       `yaml:"server"`
	HttpConfig         HttpConfig         `yaml:"http"`
	SchedulersCfg      SchedulersCfg      `yaml:"scheduler-manager"`
	InstanceManagerCfg InstanceManagerCfg `yaml:"instance-manager"`
	APCManagerConfig   APCManagerConfig   `yaml:"apc-manager"`
	MonitorCfg         MonitorCfg         `yaml:"monitor"`
	MetricsCfg         MetricsCfg         `yaml:"metrics"`
	ClusterCfg         ClusterConfig      `yaml:"cluster"`
	ProbeCfg           ProbeCfg           `yaml:"probe"`
	PeerClientCfg      *PeerClientConfig  `yaml:"peer-client,omitempty"`
	OpsPeerClientCfg   *PeerClientConfig  `yaml:"ops-peer-client,omitempty"`
	MetaCleanupCfg     *MetaCleanupConfig `yaml:"meta-cleanup,omitempty"`
}

type MetaCleanupConfig struct {
	MaxInstanceDownTimeSec int `yaml:"max-instances-down-time-sec"`
	MaxWorkerDownTimeSec   int `yaml:"max-workers-down-time-sec"`
}

type PeerClientConfig struct {
	HttpCfg        *HttpConfig `yaml:"http,omitempty"`
	SendWithDomain bool        `yaml:"send-with-domain"`
	Domain         string      `yaml:"domain"`
}

func (p *PeerClientConfig) DeepCopy() *PeerClientConfig {
	if p == nil {
		return nil
	}
	cp := &PeerClientConfig{
		SendWithDomain: p.SendWithDomain,
		Domain:         p.Domain,
	}
	if p.HttpCfg != nil {
		cp.HttpCfg = &HttpConfig{
			DialerTimeout:       p.HttpCfg.DialerTimeout,
			DialerKeepAlive:     p.HttpCfg.DialerKeepAlive,
			RequestTimeout:      p.HttpCfg.RequestTimeout,
			MaxIdleConns:        p.HttpCfg.MaxIdleConns,
			MaxIdleConnsPerHost: p.HttpCfg.MaxIdleConnsPerHost,
			IdleConnTimeout:     p.HttpCfg.IdleConnTimeout,
			MaxConnsPerHost:     p.HttpCfg.MaxConnsPerHost,
		}
		if p.HttpCfg.TLSConfig != nil {
			cp.HttpCfg.TLSConfig = &TLSCertificateConfig{
				CertFile:    p.HttpCfg.TLSConfig.CertFile,
				KeyFile:     p.HttpCfg.TLSConfig.KeyFile,
				CaFile:      p.HttpCfg.TLSConfig.CaFile,
				SkipVerify:  p.HttpCfg.TLSConfig.SkipVerify,
				EnableHTTPS: p.HttpCfg.TLSConfig.EnableHTTPS,
				EnableMTLS:  p.HttpCfg.TLSConfig.EnableMTLS,
			}
		}
	}
	return cp
}

type ProbeCfg struct {
	Readiness      map[string]int        `yaml:"readiness"`
	CircuitBreaker *CircuitBreakerConfig `yaml:"circuit-breaker,omitempty"`
}

type CircuitBreakerConfig struct {
	Enable               bool    `yaml:"enable"`
	FailureRateThreshold float64 `yaml:"failure-rate-threshold"`
	MinRequestCount      int     `yaml:"min-request-count"`
	CheckWindowSec       int64   `yaml:"check-window-sec"`
}

type SchedulersCfg struct {
	// define all the scheduler used for the given request type.
	Schedulers         []SchedulerCfg      `yaml:"schedulers"`
	SystemPromptLength int                 `yaml:"system-prompt-length"`
	RemoteSchedule     *RemoteSchedulerCfg `yaml:"remote-schedule,omitempty"`
	SLOScheduling      *SLOSchedulerCfg    `yaml:"slo-scheduling"`
}

type SLOSchedulerCfg struct {
	SloEnable                bool     `yaml:"slo-enable"`
	SloFallbackEnable        bool     `yaml:"slo-fallback-enable"`
	SloGroupHealthyThreshold int      `yaml:"slo-group-healthy-threshold"`
	SloManager               []SloCfg `yaml:"slo-manager"`
}

type RemoteSchedulerCfg struct {
	Enabled bool `yaml:"enabled"`
	// support "endpoint" and "leader"
	RemoteType string `yaml:"remote-type" validate:"oneof=endpoint leader"`
	// endpoint of remote scheduler under type endpoint
	RemoteEndpoint string `yaml:"remote-endpoint"`

	NeedContent       bool `yaml:"need-content" default:"false"`
	TimeoutMs         int  `yaml:"timeout-ms" default:"1000"`
	CleanUpIntervalMs int  `yaml:"cleanup-interval-ms" default:"60000" validate:"min=0"`
}

type SchedulerCfg struct {
	// The worker types that the Scheduler is interested in.
	//The instance manager will periodically synchronize the status of these types of workers to this Scheduler.
	InstanceRole  string `yaml:"instance-role"`
	SchedulerName string `yaml:"scheduler-name"`
	// additional configs for this scheduler
	Params map[string]string `yaml:"params"`
}

type SloCfg struct {
	Tag          string            `yaml:"tag"`
	SloIntervals utils.SLOInterval `yaml:"slo-intervals"`
}

type ClusterConfig struct {
	ClusterServiceType string `json:"type" yaml:"type"`
	RoleCandidate      bool   `json:"role_candidate" yaml:"role_candidate"`
	Verbose            bool   `json:"verbose" yaml:"verbose"`

	PeerHttpCfg *HttpConfig `json:"peer_http,omitempty" yaml:"peer_http,omitempty"`

	// etcd config
	EtcdCfg *EtcdConfig `json:"etcd,omitempty" yaml:"etcd,omitempty"`

	// EtcdHealthExpireTimeMs is the expiration time for etcd health cache; beyond this time, failure data from etcd is no longer trusted
	// A value of 0 or negative means using the default (2 times FailureSnapshotIntervalMs, with a fallback of 40s)
	EtcdHealthExpireTimeMs int `yaml:"etcd-health-expire-time-ms,omitempty"`
}

type EtcdConfig struct {
	EtcdEndpoints          []string              `json:"etcd-endpoints" yaml:"etcd-endpoints"`
	DialTimeout            int                   `json:"dial-timeout-ms" yaml:"dial-timeout-ms"`
	RetryTime              int                   `json:"retry-time" yaml:"retry-time" default:"10"`
	RetryInterval          int                   `json:"retry-interval-sec" yaml:"retry-interval-sec" default:"5"`
	LeaseTtlSec            int                   `json:"lease-ttl-sec" yaml:"lease-ttl-sec" default:"3" validate:"min=1"`
	LeaseRefreshIntervalMs int                   `json:"lease-refresh-interval-ms" yaml:"lease-refresh-interval-ms" default:"1000"`
	TLSConfig              *TLSCertificateConfig `yaml:"tls,omitempty"`
}

type SeedInfo struct {
	// Name of the node, see server.Name in yaml configs
	Name string `json:"node_name" yaml:"node_name"`
	// HttpEndpoint for non-seed to join cluster
	HttpEndpoint string `json:"http_endpoint" yaml:"http_endpoint"`
}

type WhitelistEntry struct {
	Path    string   `yaml:"path"`
	Methods []string `yaml:"methods" schema:"each=oneof=GET,POST,min=1"`
}

type HealthCheckConfig struct {
	AvailableCheckersCfg       AvailableCheckersConfig   `yaml:"available-checkers"`
	UnavailableCheckersCfg     UnavailableCheckersConfig `yaml:"unavailable-checkers"`
	RestHealthCommonConfig     RestHealthCommonConfig    `yaml:"rest-health-common-config"`
	EventThresholdIntervalMs   int                       `yaml:"event-threshold-interval-ms"`
	ProbeInferRequestTimeoutMs int                       `yaml:"probe-infer-request-timeout-ms"`
	FallbackHealthCheckConfig  FallbackHealthCheckConfig `yaml:"fallback-health-checker"`
	HealthCacheExpireTimeMs    int                       `yaml:"health-cache-expire-time-ms"`
	FailureSnapshotIntervalMs  int                       `yaml:"failure-snapshot-interval-ms,omitempty"`
}

type FallbackHealthCheckConfig struct {
	Enable                bool `yaml:"enable"`
	ResumeCheckIntervalMs int  `yaml:"resume-check-interval-ms" default:"1000" validate:"min=100"`
	ResumeTimeMs          int  `yaml:"resume-time-ms" default:"90000" validate:"min=1000"`
}

type RestHealthCommonConfig struct {
	Interval             int `yaml:"interval-ms"`
	HealthCheckTimeoutMs int `yaml:"health-check-timeout-ms"`
	MaxConcurrency       int `yaml:"max-concurrency"`
	BatchSize            int `yaml:"batch-size"`
}

type AvailableCheckersConfig struct {
	RestHealthCfg       RestHealthConfig      `yaml:"rest-health"`
	RequestFailuresCfg  RequestFailuresConfig `yaml:"request-failures"`
	LinkDownCfg         RequestFailuresConfig `yaml:"link-down-failures"`
	HangCheckerCfg      HangCheckerConfig     `yaml:"hang-checker"`
	LinkBlacklistConfig LinkBlacklistConfig   `yaml:"link-blacklist-config"`
}

type RestHealthConfig struct {
	Enable                  bool `yaml:"enable"`
	ConsecutiveTime         int  `yaml:"consecutive-time"`
	FailOn500               bool `yaml:"fail-on-500-directly"`
	EnableProbeInferRequest bool `yaml:"enable-probe-infer-request"`
}

type RequestFailuresConfig struct {
	Enable                  bool                    `yaml:"enable"`
	ConsecutiveFailure      bool                    `yaml:"consecutive-failure"`
	TimeWindow              int                     `yaml:"time-window-ms"`
	MaxFailuresTime         int                     `yaml:"max-failures"`
	FakeRequest             FakeRequestConfig       `yaml:"fake-request"`
	FailureConditionsConfig FailureConditionsConfig `yaml:"failure-conditions"`
}

type LinkBlacklistConfig struct {
	Enable                  bool  `yaml:"enable"`
	LinkBlockThreshold      int   `yaml:"link-block-threshold"`
	EntryGCIntervalMs       int64 `yaml:"entry-gc-interval-ms"`
	LinkBlockWindowsMs      int64 `yaml:"link-block-windows-ms"`
	LinkBlockTagValidTimeMs int64 `yaml:"link-block-tag-valid-time-ms"`
	InstanceBlockRatio      int   `yaml:"instance-block-ratio"`
}

type FakeRequestConfig struct {
	Enable           bool `yaml:"enable"`
	SendInterval     int  `yaml:"time-interval-ms"`
	MaxSendTime      int  `yaml:"max-fake-request-time"`
	FailureThreshold int  `yaml:"failure-threshold"`
}

type FailureConditionsConfig struct {
	RequestTimeout bool  `yaml:"request-timeout"`
	StatusCodes    []int `yaml:"status-codes"`
}

type HangCheckerConfig struct {
	Enable                      bool            `yaml:"enable"`
	Interval                    int64           `yaml:"leader-check-interval-ms"`
	HangTagValidTime            int64           `yaml:"worker-hang-tag-valid-time-ms"`
	HangThresholds              []HangThreshold `yaml:"hang-thresholds"`
	EnableProbeBeforeHangCheck  bool            `yaml:"enable-probe-before-hang-check"`
	HangProbeIntervalMs         int64           `yaml:"hang-probe-interval-ms"`
	HangProbeTimeoutMs          int64           `yaml:"hang-probe-timeout-ms"`
	MaxConsecutiveInvalidProbes int             `yaml:"max-consecutive-invalid-probes"`
	HangMessageKeywords         []string        `yaml:"hang-message-keywords"`
}

func (c *HangCheckerConfig) GetThresholdTime(role string) int64 {
	for _, v := range c.HangThresholds {
		if v.Role == role {
			return v.ThresholdTime
		}
	}

	return -1
}

type HangThreshold struct {
	Role          string `yaml:"role"`
	ThresholdTime int64  `yaml:"max-non-active-time-ms"`
	SkipNonStream bool   `yaml:"skip-non-stream"`
}

type UnavailableCheckersConfig struct {
	RestHealthCfg RestHealthConfig `yaml:"rest-health"`
}

const SystemPromptFieldName = "system-prompt-length"
