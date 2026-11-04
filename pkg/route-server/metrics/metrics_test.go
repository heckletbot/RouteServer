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
	"math/rand"
	"sync"
	"testing"
	"time"

	commoncfg "route-server/pkg/common/config"

	"github.com/stretchr/testify/assert"
)

func TestInitMetrics_SingleInitialization(t *testing.T) {
	InitMetrics(&commoncfg.MetricsCfg{Enable: true})
	InitPerfMetrics(&commoncfg.MetricsCfg{Enable: true})

	initialPerfMetrics := perfMetrics
	initialMetrics := metrics

	InitMetrics(&commoncfg.MetricsCfg{Enable: true})

	assert.Same(t, initialPerfMetrics, perfMetrics)
	assert.Same(t, initialMetrics, metrics)
}

// TestMetricsComponentInitialization tests metrics component initialization
func TestInitMetrics_ComponentInitialization(t *testing.T) {
	InitMetrics(&commoncfg.MetricsCfg{Enable: true, Duration: 2})
	// Verify core components
	assert.NotNil(t, metrics.NumFailedRequests)
	assert.NotNil(t, metrics.RequestTotalTime)
	assert.NotNil(t, metrics.TokensOutputPerRequest)
	assert.NotNil(t, metrics.HealthyEndpoints)
}

func TestMetrics(t *testing.T) {
	InitPerfMetrics(&commoncfg.MetricsCfg{Enable: true, Duration: 2})
	RegisterMetric("infer", "ttft")
	RegisterMetric("infer", "tpot")
	RegisterMetric("infer", "schedule")
	err := StartMetric()
	if err != nil {
		t.Errorf("start metric failed. %v", err)
	}
	defer StopMetric()

	for range 20 {
		time.Sleep(100 * time.Millisecond)
		MetricRecord("infer", "ttft", rand.Intn(2000))
		MetricRecord("infer", "tpot", rand.Intn(50))
		MetricRecord("infer", "schedule", rand.Intn(50))
	}

	time.Sleep(500 * time.Millisecond)
	if len(perfMetrics.pastPerfMetrics) <= 0 {
		t.Errorf("metrics calculate failed.")
	}
}

// TestCounterMetrics tests basic counter functionality
func TestCounterMetrics(t *testing.T) {
	InitMetrics(&commoncfg.MetricsCfg{Enable: true})

	// Test failed request count
	RecordFailedRequest()

	// Test successful request count
	RecordSuccessedRequest()

	// Test concurrent safety
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			RecordSuccessedRequest()
		}()
	}
	wg.Wait()

}

// TestEndpointCounting tests healthy node counting
func TestEndpointCounting(t *testing.T) {
	InitMetrics(&commoncfg.MetricsCfg{Enable: true})
	SetHealthyEndpoint("prefill", 1)
	SetHealthyEndpoint("decode", 2)
}
