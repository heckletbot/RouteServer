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

package metrics

import (
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	dto "github.com/prometheus/client_model/go"
	commoncfg "infer-dp-scheduler/pkg/common/config"
	"infer-dp-scheduler/pkg/common/logger"
)

var (
	once    sync.Once
	metrics *Metrics
)

type Metrics struct {
	NumRunningRequests       *prometheus.GaugeVec
	NumFailedRequests        prometheus.Counter     // Failed requests counter
	NumSuccessedRequests     prometheus.Counter     // Success requests counter
	NumScheduledRequests     prometheus.Counter     // Scheduled requests counter
	NumTotalOutputToken      prometheus.Counter     // Total output token counter
	NumRequestsWithErrorCode *prometheus.CounterVec // Number of requests with different error codes, including successful requests

	NumFallbackScheduled             prometheus.Counter // Fallback scheduled requests counter
	NumLeaderRemoteScheduledRequests prometheus.Counter // Remote scheduled requests counter

	NumDiscardedUnreachableRsWorkers prometheus.Counter     // Number of rs workers discarded due to unreachable
	NumDiscardedUnreachableEndpoints *prometheus.CounterVec // Number of inference endpoints discarded due to unreachable (labels by role: e.g. prefill/decode)

	// Request processing time distribution histogram
	RequestTotalTime prometheus.Histogram
	// Scheduling time distribution histogram
	RequestScheduleTime *prometheus.HistogramVec
	// TTFT distribution histogram
	TimeToFirstToken prometheus.Histogram
	// Prompt token count distribution histogram
	PromptTokenNum prometheus.Histogram
	// TPOT distribution histogram
	TimePerOutputToken prometheus.Histogram
	// Output token count distribution histogram
	TokensOutputPerRequest prometheus.Histogram
	// Number of healthy endpoints (by role: prefill/decode)
	HealthyEndpoints *prometheus.GaugeVec
	// Number of unhealthy endpoints (by role: e.g. prefill/decode)
	FaultyEndpoints *prometheus.GaugeVec
	// Link blacklist size (by role: e.g. prefill/decode)
	LinkBlackListSize *prometheus.GaugeVec
	// Event backlog count
	EventsRemaining prometheus.Gauge

	LeaderChangeNum           *prometheus.CounterVec
	NodeRole                  prometheus.Gauge
	UnreachableWorkers        prometheus.Gauge
	ETCDConnectionRetries     prometheus.Counter
	MetricsCollectionFailures prometheus.Counter

	// APC related metrics
	APCQueryLatencySeconds               prometheus.Histogram // APC query latency distribution
	APCQueryMatchRatio                   prometheus.Histogram // APC query match ratio distribution
	PrefillCachedTokens                  prometheus.Histogram // APC cached token count distribution
	NewGenerateRequestImplLatencySeconds prometheus.Histogram // NewGenerateRequestImpl processing time distribution

	// Worker readiness state metrics (unified circuit breaker state and forced mode)
	WorkerReadinessState prometheus.Gauge // Worker readiness state: 0=force_true, 1=force_false, 2=normal, 3=broken
}

func InitMetrics(cfg *commoncfg.MetricsCfg) {
	if cfg == nil {
		cfg = &commoncfg.MetricsCfg{}
	}
	once.Do(func() {
		metrics = &Metrics{
			NumRunningRequests: newRunningRequestsGaugeVec(),
			// Define failed requests counter (categorized by error type)
			NumFailedRequests:                newFailedRequestsCounter(),
			NumRequestsWithErrorCode:         newRequestsWithStatusCodeCounter(),
			NumSuccessedRequests:             newNumSuccessedRequestsCounter(),
			NumScheduledRequests:             newScheduledRequestsCounter(),
			NumFallbackScheduled:             newNumFallbackScheduledCounter(),
			NumLeaderRemoteScheduledRequests: newNumLeaderRemoteScheduledCounter(),

			NumDiscardedUnreachableRsWorkers: newNumDiscardedUnreachableRsWorkers(),
			NumDiscardedUnreachableEndpoints: newNumDiscardedUnreachableEndpoints(), // Labels: role

			NumTotalOutputToken: newNumTotalOutputTokenCounter(),
			// Initialize request total time histogram
			RequestTotalTime: newRequestTotalTimeHistogram(),
			// Initialize labeled scheduling time histogram
			RequestScheduleTime: newRequestScheduleTimeHistogram(),
			// Initialize TTFT histogram
			TimeToFirstToken: newTimeToFirstTokenHistogram(),
			// Initialize prompt token count histogram
			PromptTokenNum: newPromptTokenNumHistogram(),
			// Initialize TPOT histogram
			TimePerOutputToken: newTimePerOutputTokenHistogram(cfg.FineGrainedTpot),
			// Initialize output token count histogram
			TokensOutputPerRequest: newTokensOutputPerRequestHistogram(),
			// Initialize healthy endpoints GaugeVec
			HealthyEndpoints:    newHealthyEndpointsGauge(), // Labels: role
			FaultyEndpoints:     newFaultyEndpointsGauge(),  // Labels: role
			PrefillCachedTokens: newPrefillCachedTokensHistogram(),

			// Initialize APC related metrics
			APCQueryLatencySeconds:               newAPCQueryLatencyHistogram(),
			APCQueryMatchRatio:                   newAPCQueryMatchRatioHistogram(),
			NewGenerateRequestImplLatencySeconds: newNewGenerateRequestImplLatencyHistogram(),

			// Initialize worker readiness state metric (default 0 means circuit breaker not enabled)
			WorkerReadinessState: newWorkerReadinessStateGauge(),

			// Link blacklist GaugeVec
			LinkBlackListSize: newLinkBlacklistSizeGauge(),

			// Event sync failure, event backlog gauge
			EventsRemaining: newEventsRemainingGauge(),

			LeaderChangeNum:           newLeaderChangeCounter(),
			NodeRole:                  newNodeRoleGauge(),
			UnreachableWorkers:        newUnreachableWorkersGauge(),
			ETCDConnectionRetries:     newETCDConnectionRetriesCounter(),
			MetricsCollectionFailures: newMetricsCollectionFailuresCounter(),
		}
	})
}

func newFailedRequestsCounter() prometheus.Counter {
	return promauto.NewCounter(prometheus.CounterOpts{
		Name: "rs:num_failed_requests",
		Help: "Total number of failed requests",
	})
}

func newRequestsWithStatusCodeCounter() *prometheus.CounterVec {
	return promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "rs:infer_requests_failure_status_total",
		Help: "Total number of failed requests in different status code",
	}, []string{"status_code", "url"})
}

func newRunningRequestsGaugeVec() *prometheus.GaugeVec {
	return promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "rs:num_running_requests",
		Help: "Number of running requests",
	}, []string{"type"})
}

func newNumSuccessedRequestsCounter() prometheus.Counter {
	return promauto.NewCounter(prometheus.CounterOpts{
		Name: "rs:num_successed_requests",
		Help: "Total number of successed requests",
	})
}

func newScheduledRequestsCounter() prometheus.Counter {
	return promauto.NewCounter(prometheus.CounterOpts{
		Name: "rs:num_scheduled_requests",
		Help: "Total number of scheduled requests",
	})
}

func newNumFallbackScheduledCounter() prometheus.Counter {
	return promauto.NewCounter(prometheus.CounterOpts{
		Name: "rs:num_fallback_scheduled",
		Help: "Total number of fallback scheduled for this worker, indicate the steadiness of remote scheduler",
	})
}

func newNumLeaderRemoteScheduledCounter() prometheus.Counter {
	return promauto.NewCounter(prometheus.CounterOpts{
		Name: "rs:num_leader_remote_scheduled",
		Help: "Total number of remote schedule request, indicate showing load of the leader scheduler",
	})
}

func newNumTotalOutputTokenCounter() prometheus.Counter {
	return promauto.NewCounter(prometheus.CounterOpts{
		Name: "rs:num_total_output_tokens",
		Help: "Total number of output token",
	})
}

func newNumDiscardedUnreachableRsWorkers() prometheus.Counter {
	return promauto.NewCounter(prometheus.CounterOpts{
		Name: "rs:num_discarded_unreachable_rs_workers",
		Help: "Total number of unreachable rs workers that got discarded by leader",
	})
}

func newETCDConnectionRetriesCounter() prometheus.Counter {
	return promauto.NewCounter(prometheus.CounterOpts{
		Name: "rs:etcd_connection_retries",
		Help: "Total number of ETCD connection retries",
	})
}

func newMetricsCollectionFailuresCounter() prometheus.Counter {
	return promauto.NewCounter(prometheus.CounterOpts{
		Name: "rs:metrics_collection_failures_total",
		Help: "Total number of metrics collection failures",
	})
}

func newNumDiscardedUnreachableEndpoints() *prometheus.CounterVec {
	return promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "rs:num_discarded_unreachable_infer_endpoints",
		Help: "Total number of unreachable infer instance endpoints (i.e. api_server ip:port for each dp) that got discarded by leader",
	}, []string{"role"})
}

func newRequestTotalTimeHistogram() prometheus.Histogram {
	return promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "rs:request_total_time_secs",
		Help:    "Distribution of request processing times",
		Buckets: []float64{0.3, 0.5, 0.8, 1.0, 1.5, 2.0, 2.5, 5.0, 10.0, 15.0, 20.0, 30.0, 40.0, 50.0, 60.0, 120.0, 240.0, 480.0, 960.0, 1920.0, 7680.0}, // seconds
	})
}

func newRequestScheduleTimeHistogram() *prometheus.HistogramVec {
	return promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "rs:request_schedule_time",
		Help:    "Distribution of request scheduling times by stage",
		Buckets: []float64{0.001, 0.005, 0.01, 0.015, 0.02, 0.025, 0.03, 0.035, 0.04, 0.045, 0.05}, // seconds
	}, []string{"stage"})
}

func newTimeToFirstTokenHistogram() prometheus.Histogram {
	return promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "rs:time_to_first_token_sec",
		Help:    "Distribution of Time to First Token (TTFT)",
		Buckets: []float64{0.001, 0.005, 0.01, 0.02, 0.04, 0.06, 0.08, 0.1, 0.25, 0.5, 0.75, 1.0, 2.5, 5.0, 7.5, 10.0, 20.0, 40.0, 80.0, 160.0, 640.0, 2560.0}, // seconds, adjust based on actual situation
	})
}

func newPromptTokenNumHistogram() prometheus.Histogram {
	return promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "rs:prompt_token_num",
		Help:    "Distribution of prompt token counts",
		Buckets: []float64{16, 32, 64, 128, 256, 512, 1024, 2048, 4096, 8192, 16384, 32768},
	})
}

func newTimePerOutputTokenHistogram(fineGrained bool) prometheus.Histogram {
	buckets := []float64{0.01, 0.025, 0.05, 0.075, 0.1, 0.15, 0.2, 0.3, 0.4, 0.5, 0.75, 1.0, 2.5, 5.0, 7.5, 10.0, 20.0, 40.0, 80.0} // seconds/token
	if fineGrained {
		buckets = []float64{0.01, 0.011, 0.012, 0.013, 0.014, 0.015, 0.016, 0.017,
			0.018, 0.019, 0.02, 0.021, 0.022, 0.023, 0.024, 0.025, 0.05, 0.075, 0.1,
			0.15, 0.2, 0.3, 0.4, 0.5, 0.75, 1.0, 2.5, 5.0, 7.5, 10.0, 20.0, 40.0, 80.0}
	}
	return promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "rs:time_per_output_token_ms",
		Help:    "Distribution of time per output token (TPOT)",
		Buckets: buckets,
	})
}

func newTokensOutputPerRequestHistogram() prometheus.Histogram {
	return promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "rs:tokens_output_per_request",
		Help:    "Distribution of output token counts per request",
		Buckets: []float64{1.0, 2.0, 5.0, 10.0, 20.0, 50.0, 100.0, 200.0, 500.0, 1000.0, 2000.0, 5000.0, 10000.0},
	})
}

func newHealthyEndpointsGauge() *prometheus.GaugeVec {
	return promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "rs:number_healthy_endpoints",
		Help: "Number of healthy endpoints (label = <role>, e.g. prefill/decode/encode)",
	}, []string{"role"})
}

func newFaultyEndpointsGauge() *prometheus.GaugeVec {
	return promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "rs:number_faulty_endpoints",
		Help: "Number of faulty endpoints (label = <role>, e.g. prefill/decode/encode)",
	}, []string{"role", "fault_type"})
}

func newEventsRemainingGauge() prometheus.Gauge {
	return promauto.NewGauge(prometheus.GaugeOpts{
		Name: "rs:number_events_remaining",
		Help: "Number of events remaining to sync",
	})
}

func newUnreachableWorkersGauge() prometheus.Gauge {
	return promauto.NewGauge(prometheus.GaugeOpts{
		Name: "rs:num_unreachable_workers",
		Help: "Number of unreachable workers",
	})
}

func newLinkBlacklistSizeGauge() *prometheus.GaugeVec {
	return promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "rs:link_blacklist_size",
		Help: "Size of link blacklist (label = <role>, e.g. prefill/decode/encode)",
	}, []string{"role", "link_type"})
}

func newLeaderChangeCounter() *prometheus.CounterVec {
	return promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "rs:leader_change_total",
		Help: "count of leader change",
	}, []string{"node_name", "success"})
}

func newNodeRoleGauge() prometheus.Gauge {
	return promauto.NewGauge(prometheus.GaugeOpts{
		Name: "rs:node_role",
		Help: "Current role of the RouteServer node (0: worker, 1: leader, 2: candidate).",
	})
}

func newAPCQueryLatencyHistogram() prometheus.Histogram {
	return promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "rs:apc_query_latency_seconds",
		Help:    "Distribution of APC query latencies in seconds",
		Buckets: []float64{0.0001, 0.0005, 0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1.0, 2.5, 5.0}, // seconds
	})
}

func newAPCQueryMatchRatioHistogram() prometheus.Histogram {
	return promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "rs:apc_query_match_ratio",
		Help:    "Distribution of APC query match ratios",
		Buckets: []float64{0.0, 0.1, 0.2, 0.3, 0.4, 0.5, 0.6, 0.7, 0.8, 0.9, 0.95, 0.99, 1.0}, // ratio: 0-1
	})
}

func newPrefillCachedTokensHistogram() prometheus.Histogram {
	return promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "rs:prefill_cached_tokens",
		Help:    "Distribution of prefill cached tokens",
		Buckets: []float64{0, 16, 32, 64, 128, 256, 512, 1024, 2048, 4096, 8192, 16384, 32768, 65536}, // Token count
	})
}

func newNewGenerateRequestImplLatencyHistogram() prometheus.Histogram {
	return promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "rs:new_generate_request_impl_latency_seconds",
		Help:    "Distribution of NewGenerateRequestImpl processing times in seconds",
		Buckets: []float64{0.0001, 0.0005, 0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1.0, 2.5, 5.0}, // seconds
	})
}

func newWorkerReadinessStateGauge() prometheus.Gauge {
	return promauto.NewGauge(prometheus.GaugeOpts{
		Name: "rs:worker_readiness_state",
		Help: "Worker readiness state: 0=disabled (breaker not enabled), 1=force_true, 2=force_false, 3=normal, 4=broken",
	})
}

// RecordFailedRequest records failed requests
func RecordFailedRequest() {
	metrics.NumFailedRequests.Inc()
}

// RecordSuccessedRequest records successful requests
func RecordSuccessedRequest() {
	metrics.NumSuccessedRequests.Inc()
}

func RecordScheduledRequest() {
	metrics.NumScheduledRequests.Inc()
}

func RecordFallBackScheduled() {
	metrics.NumFallbackScheduled.Inc()
}

func RecordRemoteScheduled() {
	metrics.NumLeaderRemoteScheduledRequests.Inc()
}

func RecordFailedRequestStatus(statusCode string, url string) {
	metrics.NumRequestsWithErrorCode.WithLabelValues(statusCode, url).Add(1)
}

func RecordTotalOutputToken(n int) {
	metrics.NumTotalOutputToken.Add(float64(n))
}

// RecordRequestDuration records individual request processing time in seconds
func RecordRequestDuration(duration float64) {
	metrics.RequestTotalTime.Observe(duration)
}

// RecordScheduleDuration records request scheduling time in seconds
func RecordScheduleDuration(stage string, duration float64) {
	metrics.RequestScheduleTime.WithLabelValues(stage).Observe(duration)
}

func RecordDiscardUnreachableWorkers() {
	metrics.NumDiscardedUnreachableRsWorkers.Inc()
}

func RecordDiscardUnreachableEndpoints(role string, count int) {
	metrics.NumDiscardedUnreachableEndpoints.WithLabelValues(role).Add(float64(count))
}

// RecordTTFT records Time to First Token (TTFT)
func RecordTTFT(duration float64) {
	metrics.TimeToFirstToken.Observe(duration)
}

// RecordPromptTokenCount records prompt token count
func RecordPromptTokenCount(count int) {
	metrics.PromptTokenNum.Observe(float64(count))
}

// RecordTPOT records average generation time per output token
func RecordTPOT(durationPerToken float64, token_nums int) {
	if durationPerToken <= 0 {
		return
	}
	for i := 0; i < token_nums; i++ {
		metrics.TimePerOutputToken.Observe(durationPerToken)
	}
}

// RecordOutputTotalTokenCount records total token count per request
func RecordOutputTotalTokenCount(tokenCount int) {
	metrics.TokensOutputPerRequest.Observe(float64(tokenCount))
}

// SetHealthyEndpoint records number of healthy endpoints
func SetHealthyEndpoint(role string, num int) {
	metrics.HealthyEndpoints.WithLabelValues(role).Set(float64(num))
}

func SetEventsRemaining(num int) {
	metrics.EventsRemaining.Set(float64(num))
}

func SetFaultyEndpoint(role string, faultType string, num int) {
	metrics.FaultyEndpoints.WithLabelValues(role, faultType).Set(float64(num))
}

func RecordLeaderChange(nodeName, isLeader string) {
	metrics.LeaderChangeNum.WithLabelValues(nodeName, isLeader).Inc()
}

func SetCurrentNodeRole(roleId int) {
	metrics.NodeRole.Set(float64(roleId))
}

func RecordUnreachableWorkersNum(num int) {
	metrics.UnreachableWorkers.Set(float64(num))
}

func RecordETCDConnectionsRetries() {
	metrics.ETCDConnectionRetries.Inc()
}

func RecordMetricsCollectionFailures() {
	metrics.MetricsCollectionFailures.Inc()
}

func IncFaultyEndpoint(role string, faultType string) {
	metrics.FaultyEndpoints.WithLabelValues(role, faultType).Inc()
}

func ResetFaultyEndpoint() {
	metrics.FaultyEndpoints.Reset()
}

func SetLinkBlacklistSize(role, linkType string, num int) {
	metrics.LinkBlackListSize.WithLabelValues(role, linkType).Set(float64(num))
}

func ResetLinkBlacklistSize() {
	metrics.LinkBlackListSize.Reset()
}

func UpdateRunningRequests(reqType string, val float64) {
	metrics.NumRunningRequests.WithLabelValues(reqType).Add(val)
}

// RecordAPCQueryLatency records APC query latency in seconds
func RecordAPCQueryLatency(duration float64) {
	metrics.APCQueryLatencySeconds.Observe(duration)
}

// RecordAPCQueryMatchRatio records APC query match ratio
func RecordAPCQueryMatchRatio(ratio float64) {
	if ratio < 0.0 || ratio > 1.0 {
		return
	}
	metrics.APCQueryMatchRatio.Observe(ratio)
}

// RecordAPCCachedTokens records APC cached token count
func RecordAPCCachedTokens(cachedTokens int) {
	metrics.PrefillCachedTokens.Observe(float64(cachedTokens))
}

// RecordNewGenerateRequestImplLatency records NewGenerateRequestImpl processing latency in seconds
func RecordNewGenerateRequestImplLatency(duration float64) {
	metrics.NewGenerateRequestImplLatencySeconds.Observe(duration)
}

func StartInternalMetrics(c *gin.Context) {
	showDefaultMetrics := c.Query("only_rs") != "true"

	gatherer := prometheus.GathererFunc(func() ([]*dto.MetricFamily, error) {
		mfs, err := prometheus.DefaultGatherer.Gather()
		if err != nil {
			return nil, err
		}

		if showDefaultMetrics {
			return mfs, nil
		}

		filtered := make([]*dto.MetricFamily, 0, len(mfs))
		for _, mf := range mfs {
			if strings.HasPrefix(mf.GetName(), "rs") {
				filtered = append(filtered, mf)
			}
		}
		return filtered, nil
	})

	promhttp.HandlerFor(
		gatherer,
		promhttp.HandlerOpts{},
	).ServeHTTP(c.Writer, c.Request)
}

// GetNumSuccessedRequests returns the count of successful requests
func GetNumSuccessedRequests() float64 {
	if metrics == nil || metrics.NumSuccessedRequests == nil {
		logger.Log().Warnf("[Metrics] GetNumSuccessedRequests called but metrics not initialized, returning 0")
		return 0
	}
	metric := &dto.Metric{}
	if err := metrics.NumSuccessedRequests.Write(metric); err != nil {
		return 0
	}
	if metric.Counter == nil {
		return 0
	}
	return metric.Counter.GetValue()
}

// GetNumFailedRequests returns the count of failed requests
func GetNumFailedRequests() float64 {
	if metrics == nil || metrics.NumFailedRequests == nil {
		logger.Log().Warnf("[Metrics] GetNumFailedRequests called but metrics not initialized, returning 0")
		return 0
	}
	metric := &dto.Metric{}
	if err := metrics.NumFailedRequests.Write(metric); err != nil {
		return 0
	}
	if metric.Counter == nil {
		return 0
	}
	return metric.Counter.GetValue()
}

// GetNumScheduledRequests returns the count of scheduled requests
func GetNumScheduledRequests() float64 {
	if metrics == nil || metrics.NumScheduledRequests == nil {
		return 0
	}
	metric := &dto.Metric{}
	if err := metrics.NumScheduledRequests.Write(metric); err != nil {
		return 0
	}
	if metric.Counter == nil {
		return 0
	}
	return metric.Counter.GetValue()
}

// GetNumTotalRequestsWithErrorCode Get total request count across all status codes from NumRequestsWithErrorCode
func GetNumTotalRequestsWithErrorCode() float64 {
	if metrics == nil || metrics.NumRequestsWithErrorCode == nil {
		logger.Log().Warnf("[Metrics] GetNumTotalRequestsWithErrorCode called but metrics not initialized, returning 0")
		return 0
	}
	mfs, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		return 0
	}
	var total float64
	for _, mf := range mfs {
		if mf.GetName() == "rs:infer_requests_failure_status_total" {
			for _, m := range mf.GetMetric() {
				if m.Counter != nil {
					total += m.Counter.GetValue()
				}
			}
			break
		}
	}
	return total
}

// GetNumErrorRequestsWithErrorCode Get request count for non-2xx and non-4xx status codes from NumRequestsWithErrorCode
func GetNumErrorRequestsWithErrorCode() float64 {
	if metrics == nil || metrics.NumRequestsWithErrorCode == nil {
		logger.Log().Warnf("[Metrics] GetNumErrorRequestsWithErrorCode called but metrics not initialized, returning 0")
		return 0
	}
	mfs, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		return 0
	}
	var errorCount float64
	for _, mf := range mfs {
		if mf.GetName() == "rs:infer_requests_failure_status_total" {
			for _, m := range mf.GetMetric() {
				if m.Counter == nil {
					continue
				}
				var statusCode string
				for _, label := range m.GetLabel() {
					if label.GetName() == "status_code" {
						statusCode = label.GetValue()
						break
					}
				}
				if !is2xxStatusCode(statusCode) && !is4xxStatusCode(statusCode) {
					errorCount += m.Counter.GetValue()
				}
			}
			break
		}
	}
	return errorCount
}

// is2xxStatusCode Check if status code is 2xx
func is2xxStatusCode(code string) bool {
	return len(code) == 3 && code[0] == '2'
}

// is4xxStatusCode Check if status code is 4xx
func is4xxStatusCode(code string) bool {
	return len(code) == 3 && code[0] == '4'
}

// SetWorkerReadinessState sets the worker readiness state metric
// The state field represents different breaker conditions: 0 means disabled (breaker not enabled), 1 implies force_true to force accept traffic, 2 implies force_false to force reject traffic, 3 signifies normal operations when not tripped, and 4 indicates a broken state due to a high failure rate.
func SetWorkerReadinessState(state float64) {
	if metrics == nil || metrics.WorkerReadinessState == nil {
		return
	}
	metrics.WorkerReadinessState.Set(state)
}
