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

package server

import (
	"fmt"
	"io"
	"net/http"
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"route-server/pkg/common/utils"
)

type Collector struct {
	mapMutex           sync.RWMutex
	registry           *prometheus.Registry
	RunningRequests    *prometheus.GaugeVec
	WaitingRequests    *prometheus.GaugeVec
	TimeToFirstTime    *prometheus.HistogramVec
	TimePerOutputToken *prometheus.HistogramVec
	End2EndTime        *prometheus.HistogramVec
}

func NewMetricsCollector() *Collector {
	registry := prometheus.NewRegistry()
	timeToFirstToken := newTimeToFirstTokenHistogram()
	registry.MustRegister(timeToFirstToken)
	timePerOutputToken := newTimePerOutputTokenHistogram()
	registry.MustRegister(timePerOutputToken)
	endToEndTime := newRequestTotalTimeHistogram()
	registry.MustRegister(endToEndTime)
	runningRequests := newRunningRequestsGauge()
	registry.MustRegister(runningRequests)
	waitingRequests := newWaitingRequestsGauge()
	registry.MustRegister(waitingRequests)

	return &Collector{
		registry:           registry,
		RunningRequests:    runningRequests,
		WaitingRequests:    waitingRequests,
		TimeToFirstTime:    timeToFirstToken,
		TimePerOutputToken: timePerOutputToken,
		End2EndTime:        endToEndTime,
	}
}

func newRunningRequestsGauge() *prometheus.GaugeVec {
	return promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "vllm:num_requests_running",
		Help: "vllm:num_requests_running Number of requests in model execution batches.",
	}, []string{"model_name", "engine"})
}

func newWaitingRequestsGauge() *prometheus.GaugeVec {
	return promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "vllm:num_requests_waiting",
		Help: "vllm:num_requests_waiting Number of requests waiting to be processed.",
	}, []string{"model_name", "engine"})
}

func newTimeToFirstTokenHistogram() *prometheus.HistogramVec {
	return promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "vllm:time_to_first_token_seconds",
		Help:    "vllm:time_to_first_token_seconds Distribution of Time to First Token (TTFT)",
		Buckets: []float64{0.001, 0.005, 0.01, 0.02, 0.04, 0.06, 0.08, 0.1, 0.25, 0.5, 0.75, 1.0, 2.5, 5.0, 7.5, 10.0, 20.0, 40.0, 80.0, 160.0, 640.0, 2560.0}, // seconds, adjust based on actual situation
	}, []string{"model_name"})
}

func newTimePerOutputTokenHistogram() *prometheus.HistogramVec {
	return promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "vllm:time_per_output_token_seconds",
		Help:    "vllm:time_per_output_token_seconds Distribution of time per output token (TPOT)",
		Buckets: []float64{0.01, 0.025, 0.05, 0.075, 0.1, 0.15, 0.2, 0.3, 0.4, 0.5, 0.75, 1.0, 2.5, 5.0, 7.5, 10.0, 20.0, 40.0, 80.0}, // seconds/token
	}, []string{"model_name"})
}

func newRequestTotalTimeHistogram() *prometheus.HistogramVec {
	return promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "vllm:e2e_request_latency_seconds",
		Help:    "vllm:e2e_request_latency_seconds Distribution of request processing times",
		Buckets: []float64{0.3, 0.5, 0.8, 1.0, 1.5, 2.0, 2.5, 5.0, 10.0, 15.0, 20.0, 30.0, 40.0, 50.0, 60.0, 120.0, 240.0, 480.0, 960.0, 1920.0, 7680.0}, // seconds
	}, []string{"model_name"})
}

func (c *Collector) HandleMetricsRequest(ctx *gin.Context) {
	registry := c.registry

	// Use prometheus HTTP handler to output metrics
	handler := promhttp.HandlerFor(registry, promhttp.HandlerOpts{})
	handler.ServeHTTP(ctx.Writer, ctx.Request)
}

type UpdateMetricsRequest struct {
	RunningRequest int `json:"running_request"`
	WaitingRequest int `json:"waiting_request"`
}

func (c *Collector) UpdateMetricsValue(ctx *gin.Context) {
	bodyBytes, err := io.ReadAll(ctx.Request.Body)
	if err != nil {
		fmt.Printf("failed to parse req err = %v, context err = %v\n", err, ctx.Request.Context().Err())
		ctx.JSON(http.StatusBadRequest, err)
		return
	}
	request := &UpdateMetricsRequest{}
	if err = utils.SonicApi.Unmarshal(bodyBytes, request); err != nil {
		fmt.Printf("failed to parse req err = %v, context err = %v\n", err, ctx.Request.Context().Err())
		ctx.JSON(http.StatusBadRequest, err)
		return
	}
	c.RunningRequests.WithLabelValues(Model, "0").Set(float64(request.RunningRequest))
	c.WaitingRequests.WithLabelValues(Model, "0").Set(float64(request.WaitingRequest))
}
