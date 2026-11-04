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

package scheduler

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"route-server/pkg/common/health"

	"github.com/agiledragon/gomonkey/v2"
	commoncfg "route-server/pkg/common/config"
	"route-server/pkg/common/logger"
	"route-server/pkg/common/scheduler"
	"route-server/pkg/common/utils"
	"route-server/pkg/route-server/metrics"

	"route-server/pkg/common/constant"
	stats2 "route-server/pkg/common/instance-metrics"
)

// scheduler contains 3 roles [constant.Prefill, constant.Decode, "X"], algorithms may contain 3 roles [constant.Prefill, constant.Decode, "unknown"]
func TestNewManager(t *testing.T) {
	cfg := commoncfg.SchedulersCfg{
		Schedulers: []commoncfg.SchedulerCfg{
			{
				SchedulerName: RoundRobinAlgoName,
				InstanceRole:  constant.Prefill,
				Params:        nil,
			},
			{
				SchedulerName: RoundRobinAlgoName,
				InstanceRole:  constant.Decode,
				Params:        nil,
			},
			{
				SchedulerName: RoundRobinAlgoName,
				InstanceRole:  "X",
				Params:        nil,
			},
		},
		SystemPromptLength: 10,
		SLOScheduling: &commoncfg.SLOSchedulerCfg{
			SloEnable: false,
		},
		RemoteSchedule: &commoncfg.RemoteSchedulerCfg{
			CleanUpIntervalMs: 60000,
		},
	}
	metricsCfg := commoncfg.MetricsCfg{
		Enable:   true,
		Duration: 30,
	}
	metrics.InitMetrics(&metricsCfg)

	pdLink := health.NewPDLinkBlacklistManager(&commoncfg.LinkBlacklistConfig{Enable: false})
	remoteReqMgr := NewRemoteScheduledRequestManager(cfg.RemoteSchedule)
	manager, err := NewManager(&commoncfg.Config{SchedulersCfg: cfg}, nil, nil, pdLink, remoteReqMgr)
	if err != nil {
		t.Error(err)
	}
	defer manager.Close()
	minStatsInterval := manager.MinStatsSyncInterval()
	if minStatsInterval != (&RoundRobin{}).GetUpdateInterval() {
		t.Errorf("min interval for all roundrobin should be round-robin")
	}

	stats := map[string]map[string]stats2.WorkerStats{
		constant.Prefill: {
			"p_node1": &stats2.ZeroCopyStats{
				Id:     "p_node1",
				Role:   constant.Prefill,
				Status: stats2.Available,
				StaticInfo: map[string]string{
					"info1": "val1",
				},
			},
			"p_node2": &stats2.ZeroCopyStats{
				Id:     "p_node2",
				Role:   constant.Prefill,
				Status: stats2.Available,
				StaticInfo: map[string]string{
					"info1": "val1",
				},
			},
		},
		constant.Decode: {
			"d_node1": &stats2.ZeroCopyStats{
				Id:     "d_node1",
				Role:   constant.Decode,
				Status: stats2.Available,
				StaticInfo: map[string]string{
					"info1": "val1",
				},
			},
			"d_node2": &stats2.ZeroCopyStats{
				Id:     "d_node2",
				Role:   constant.Decode,
				Status: stats2.Available,
				StaticInfo: map[string]string{
					"info1": "val1",
				},
			},
		},
	}
	manager.UpdateStats(stats, nil)

	schedulersManager := manager.(*SchedulersManager)

	pCtx := schedulersManager.GetContext(constant.Prefill, constant.SLOTagDefault)
	pAlgo := pCtx.Algorithm.(*RoundRobin)

	dCtx := schedulersManager.GetContext(constant.Decode, constant.SLOTagDefault)
	dAlgo := dCtx.Algorithm.(*RoundRobin)
	assert.Equal(t, 2, len(pAlgo.instances))
	assert.Equal(t, 2, len(dAlgo.instances))

	res, err := manager.DispatchRequest(context.Background(), &TestReq{
		reqId:   "1",
		reqType: constant.Prefill,
	}, make(map[string]string), nil, "", RemoteRequestInfo{IsLocal: true})
	if err != nil {
		t.Errorf("dispatch P failed")
	}
	if res.Result == "" || res.Result != pAlgo.instances[0] {
		t.Errorf("expect route P request to p node 1")
	}

	res, err = manager.DispatchRequest(context.Background(), &TestReq{
		reqId:   "2",
		reqType: constant.Prefill,
	}, make(map[string]string), nil, "", RemoteRequestInfo{IsLocal: true})
	if err != nil {
		t.Errorf("dispatch P failed")
	}
	if res.Result == "" || res.Result != pAlgo.instances[1] {
		t.Errorf("expect route P request to p node 2")
	}

	res, err = manager.DispatchRequest(context.Background(), &TestReq{
		reqId:   "3",
		reqType: constant.Decode,
	}, make(map[string]string), nil, "", RemoteRequestInfo{IsLocal: true})
	if err != nil {
		t.Errorf("dispatch D failed")
	}
	if res.Result == "" || res.Result != dAlgo.instances[0] {
		t.Errorf("expect route D request to d node 1")
	}

	// stats only contains prefill, unknown. Decode res should be nil, prefill res should be as expected
	roleKeys := make([]string, 0)
	for k, _ := range schedulersManager.roleLookUp {
		roleKeys = append(roleKeys, k)
	}
	assert.ElementsMatch(t, []string{constant.Prefill, constant.Decode, "X"}, roleKeys)
	logger.Log().Infof("===== stats only contains prefill")
	stats = map[string]map[string]stats2.WorkerStats{
		constant.Prefill: {
			"p_node1": &stats2.ZeroCopyStats{
				Id:     "p_node1",
				Role:   constant.Prefill,
				Status: stats2.Available,
				StaticInfo: map[string]string{
					"info1": "val1",
				},
			},
		},
		"unknown": {
			"unknown": &stats2.ZeroCopyStats{
				Id:     "unknown",
				Role:   "unknown",
				Status: stats2.Available,
				StaticInfo: map[string]string{
					"info1": "unknown",
				},
			},
		},
	}
	manager.UpdateStats(stats, nil)
	res, err = manager.DispatchRequest(context.Background(), &TestReq{
		reqId:   "1",
		reqType: constant.Prefill,
	}, make(map[string]string), nil, "", RemoteRequestInfo{IsLocal: true})
	assert.Equal(t, "p_node1", res.Result)
	res, err = manager.DispatchRequest(context.Background(), &TestReq{
		reqId:   "1",
		reqType: constant.Decode,
	}, make(map[string]string), nil, "", RemoteRequestInfo{IsLocal: true})
	assert.Equal(t, "", res.Result)
	assert.Equal(t, 1, len(pAlgo.instances))
	assert.Equal(t, 0, len(dAlgo.instances))

	// stats only contains decode, unknown. Prefill res should be nil, decode res should be as expected
	logger.Log().Infof("===== stats only contains decode")
	stats = map[string]map[string]stats2.WorkerStats{
		constant.Decode: {
			"d_node1": &stats2.ZeroCopyStats{
				Id:     "d_node1",
				Role:   constant.Decode,
				Status: stats2.Available,
				StaticInfo: map[string]string{
					"info1": "val1",
				},
			},
		},
	}
	manager.UpdateStats(stats, nil)
	res, err = manager.DispatchRequest(context.Background(), &TestReq{
		reqId:   "1",
		reqType: constant.Prefill,
	}, make(map[string]string), nil, "", RemoteRequestInfo{IsLocal: true})
	assert.Equal(t, "", res.Result)
	res, err = manager.DispatchRequest(context.Background(), &TestReq{
		reqId:   "1",
		reqType: constant.Decode,
	}, make(map[string]string), nil, "", RemoteRequestInfo{IsLocal: true})
	assert.Equal(t, "d_node1", res.Result)
	assert.Equal(t, 0, len(pAlgo.instances))
	assert.Equal(t, 1, len(dAlgo.instances))

	// unavailable contains prefill, unknown
	stats = map[string]map[string]stats2.WorkerStats{
		constant.Decode: {
			"d_node1": &stats2.ZeroCopyStats{
				Id:     "d_node1",
				Role:   constant.Decode,
				Status: stats2.Available,
				StaticInfo: map[string]string{
					"info1": "val1",
				},
			},
		},
	}
	unavailable := map[string]map[string]stats2.WorkerStats{
		constant.Prefill: {
			"p_node1": &stats2.ZeroCopyStats{
				Id:     "p_node1",
				Role:   constant.Prefill,
				Status: stats2.Faulty,
				StaticInfo: map[string]string{
					"info1": "val1",
				},
			},
		},
		"unknown": {
			"unknown": &stats2.ZeroCopyStats{
				Id: "unknown",
			},
		},
	}
	manager.UpdateStats(stats, unavailable)
	res, err = manager.DispatchRequest(context.Background(), &TestReq{
		reqId:   "1",
		reqType: constant.Prefill,
	}, make(map[string]string), nil, "", RemoteRequestInfo{IsLocal: true})
	assert.Equal(t, "", res.Result)
	res, err = manager.DispatchRequest(context.Background(), &TestReq{
		reqId:   "1",
		reqType: constant.Decode,
	}, make(map[string]string), nil, "", RemoteRequestInfo{IsLocal: true})
	assert.Equal(t, "d_node1", res.Result)
	assert.Equal(t, 0, len(pAlgo.instances))
	assert.Equal(t, 1, len(dAlgo.instances))

	// stats, unavailable both nil. Prefill and Decode res should be both nil.
	manager.UpdateStats(nil, nil)
	res, err = manager.DispatchRequest(context.Background(), &TestReq{
		reqId:   "1",
		reqType: constant.Prefill,
	}, make(map[string]string), nil, "", RemoteRequestInfo{IsLocal: true})
	assert.Equal(t, "", res.Result)
	res, err = manager.DispatchRequest(context.Background(), &TestReq{
		reqId:   "1",
		reqType: constant.Decode,
	}, make(map[string]string), nil, "", RemoteRequestInfo{IsLocal: true})
	assert.Equal(t, "", res.Result)
	assert.Equal(t, 0, len(pAlgo.instances))
	assert.Equal(t, 0, len(dAlgo.instances))
}

func TestFallbackSchedule(t *testing.T) {
	cfg := commoncfg.SchedulersCfg{
		Schedulers: []commoncfg.SchedulerCfg{
			{
				SchedulerName: PrintingRandomSchedulerName,
				InstanceRole:  constant.Prefill,
				Params:        nil,
			},
			{
				SchedulerName: PrintingRandomSchedulerName,
				InstanceRole:  constant.Decode,
				Params:        nil,
			},
		},
		SLOScheduling: &commoncfg.SLOSchedulerCfg{
			SloEnable: false,
		},
		SystemPromptLength: 10,
	}
	metricsCfg := commoncfg.MetricsCfg{
		Enable:   true,
		Duration: 30,
	}
	metrics.InitMetrics(&metricsCfg)
	pdLink := health.NewPDLinkBlacklistManager(&commoncfg.LinkBlacklistConfig{Enable: false})
	remoteReqMgr := NewRemoteScheduledRequestManager(cfg.RemoteSchedule)
	manager, err := NewManager(&commoncfg.Config{SchedulersCfg: cfg}, nil, nil, pdLink, remoteReqMgr)
	if err != nil {
		t.Error(err)
	}
	defer manager.Close()

	stats := map[string]map[string]stats2.WorkerStats{
		constant.Prefill: {
			"p_node1": &stats2.ZeroCopyStats{
				Id:     "p_node1",
				Role:   constant.Prefill,
				Status: stats2.Available,
				StaticInfo: map[string]string{
					"info1": "val1",
				},
			},
		},
		constant.Decode: {
			"d_node1": &stats2.ZeroCopyStats{
				Id:     "d_node1",
				Role:   constant.Decode,
				Status: stats2.Available,
				StaticInfo: map[string]string{
					"info1": "val1",
				},
			},
		},
	}
	schedulersManager := manager.(*SchedulersManager)
	// update correct algorithm
	schedulersManager.UpdateStats(stats, nil)
	// update fallback algorithm
	schedulersManager.UpdateFallbackAlgoStats(stats)

	healthCheckConfig := &commoncfg.HealthCheckConfig{
		FallbackHealthCheckConfig: commoncfg.FallbackHealthCheckConfig{
			Enable:                true,
			ResumeCheckIntervalMs: 100,
			ResumeTimeMs:          100,
		},
	}
	schedulersManager.fallbackHealthChecker = health.NewFallbackHealthChecker(healthCheckConfig)

	pAlgo := schedulersManager.roleLookUp[constant.Prefill][constant.SLOTagDefault].Algorithm.(*TestScheduler)
	dAlgo := schedulersManager.roleLookUp[constant.Decode][constant.SLOTagDefault].Algorithm.(*TestScheduler)

	pFallbackAlgo := schedulersManager.fallbackAlgo[constant.Prefill][constant.SLOTagDefault].(*RoundRobin)
	dFallbackAlgo := schedulersManager.fallbackAlgo[constant.Decode][constant.SLOTagDefault].(*RoundRobin)

	patches := gomonkey.NewPatches()
	defer patches.Reset()

	patches.ApplyMethod(reflect.TypeOf(&TestScheduler{}), "DispatchRequest", func() (string, bool, error) {
		logger.Log().Infof("TestScheduler.DispatchRequest called, return error to call fallback")
		return "", false, errors.New("test error")
	})

	res, err := manager.DispatchRequest(context.Background(), &TestReq{
		reqId:   "1",
		reqType: constant.Prefill,
	}, make(map[string]string), nil, "", RemoteRequestInfo{IsLocal: true})
	if err != nil {
		t.Errorf("dispatch P failed")
	}
	if res.Result == "" || res.Result != pAlgo.instances[0] || res.Result != pFallbackAlgo.instances[0] {
		t.Errorf("expect route P request to p node 1")
	}

	res, err = manager.DispatchRequest(context.Background(), &TestReq{
		reqId:   "2",
		reqType: constant.Decode,
	}, make(map[string]string), nil, "", RemoteRequestInfo{IsLocal: true})
	if err != nil {
		t.Errorf("dispatch D failed")
	}
	if res.Result == "" || res.Result != dAlgo.instances[0] || res.Result != dFallbackAlgo.instances[0] {
		t.Errorf("expect route D request to d node 1")
	}
}

func GetSchedulerCfg() commoncfg.SchedulersCfg {
	// SLO config: Silver (0-100), Gold (101-1000)
	cfg := commoncfg.SchedulersCfg{
		Schedulers: []commoncfg.SchedulerCfg{
			{
				SchedulerName: PrintingRandomSchedulerName,
				InstanceRole:  constant.Prefill,
				Params:        nil,
			},
		},
		SystemPromptLength: 0,
		SLOScheduling: &commoncfg.SLOSchedulerCfg{
			SloEnable:                true,
			SloFallbackEnable:        true,
			SloGroupHealthyThreshold: 1,
			SloManager: []commoncfg.SloCfg{
				{
					Tag: "Silver",
					SloIntervals: utils.SLOInterval{
						PromptStrLengthMax: "100",
					},
				},
				{
					Tag: "Gold",
					SloIntervals: utils.SLOInterval{
						PromptStrLengthMax: "1000",
					},
				},
			},
		},
	}
	return cfg
}

func TestFallbackDispatchWithSLOFallback(t *testing.T) {
	cfg := GetSchedulerCfg()

	metricsCfg := commoncfg.MetricsCfg{Enable: false}
	metrics.InitMetrics(&metricsCfg)
	pdLink := health.NewPDLinkBlacklistManager(&commoncfg.LinkBlacklistConfig{Enable: false})
	remoteReqMgr := NewRemoteScheduledRequestManager(cfg.RemoteSchedule)
	// Initialize Manager
	mgr, err := NewManager(&commoncfg.Config{SchedulersCfg: cfg}, nil, nil, pdLink, remoteReqMgr)
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Close()

	schedulersManager := mgr.(*SchedulersManager)

	// node_silver -> Tag: Silver
	// node_gold   -> Tag: Gold
	stats := map[string]map[string]stats2.WorkerStats{
		constant.Prefill: {
			"default1": &stats2.ZeroCopyStats{ // <- Default
				Id:     "default1",
				Role:   constant.Prefill,
				Status: stats2.Available,
			},
			"default2": &stats2.ZeroCopyStats{ // <- Default
				Id:     "default2",
				Role:   constant.Prefill,
				Status: stats2.Available,
			},
			"node_silver": &stats2.ZeroCopyStats{
				Id:     "node_silver",
				Role:   constant.Prefill,
				Status: stats2.Available,
				SLOTag: "Silver",
			},
			"node_gold": &stats2.ZeroCopyStats{
				Id:     "node_gold",
				Role:   constant.Prefill,
				Status: stats2.Available,
				SLOTag: "Gold",
			},
		},
	}

	schedulersManager.UpdateStats(stats, nil)
	schedulersManager.UpdateFallbackAlgoStats(stats)

	prefillFallbacks := schedulersManager.fallbackAlgo[constant.Prefill]
	if prefillFallbacks == nil {
		t.Fatal("Fallback map for Prefill not initialized")
	}

	silverAlgo, okSilver := prefillFallbacks["Silver"]
	goldAlgo, okGold := prefillFallbacks["Gold"]

	assert.True(t, okSilver, "Silver fallback algo missing")
	assert.True(t, okGold, "Gold fallback algo missing")

	silverRR := silverAlgo.(*RoundRobin)
	goldRR := goldAlgo.(*RoundRobin)

	assert.Equal(t, 1, len(silverRR.instances), "Silver algo should have 1 instance")
	assert.Equal(t, "node_silver", silverRR.instances[0], "Silver algo has wrong instance")

	assert.Equal(t, 1, len(goldRR.instances), "Gold algo should have 1 instance")
	assert.Equal(t, "node_gold", goldRR.instances[0], "Gold algo has wrong instance")

	patches := gomonkey.NewPatches()
	defer patches.Reset()

	patches.ApplyMethod(reflect.TypeOf(&TestScheduler{}), "DispatchRequest", func() (string, bool, error) {
		logger.Log().Infof("TestScheduler.DispatchRequest called, return error to call fallback")
		return "", false, errors.New("test error")
	})

	// Case 1: Send short Prompt (50 token) -> should hit Silver -> fallback to node_silver
	reqSilver := &TestReq{
		reqId:   "req_silver",
		reqType: constant.Prefill,
		sloInterval: &utils.SLOInterval{
			PromptStrLengthMax: "50",
		},
	}

	resp, err := mgr.DispatchRequest(context.Background(), reqSilver, nil, nil, "", RemoteRequestInfo{IsLocal: true})
	assert.Nil(t, err)
	assert.Equal(t, "node_silver", resp.Result, "Should fallback to Silver node")
	assert.Equal(t, "Silver", resp.SLOTag, "Response should carry correct SLO tag even in fallback")

	// Case 2: Send long Prompt (500 token) -> should hit Gold -> fallback to node_gold
	reqGold := &TestReq{
		reqId:   "req_gold",
		reqType: constant.Prefill,
		sloInterval: &utils.SLOInterval{
			PromptStrLengthMax: "500",
		},
	}

	resp, err = mgr.DispatchRequest(context.Background(), reqGold, nil, nil, "", RemoteRequestInfo{IsLocal: true})
	assert.Nil(t, err)
	assert.Equal(t, "node_gold", resp.Result, "Should fallback to Gold node")
	assert.Equal(t, "Gold", resp.SLOTag)

	// Case 3: Empty SLO interval -> should hit strictest tag Gold -> fallback to node_gold
	reqDefault := &TestReq{
		reqId:   "req_default",
		reqType: constant.Prefill,
	}

	resp, err = mgr.DispatchRequest(context.Background(), reqDefault, nil, nil, "", RemoteRequestInfo{IsLocal: true})
	assert.Nil(t, err)
	assert.Contains(t, "node_gold", resp.Result, "Should fallback to Gold node")
	assert.Equal(t, "Gold", resp.SLOTag)

	resp, err = mgr.DispatchRequest(context.Background(), reqDefault, nil, nil, "", RemoteRequestInfo{IsLocal: true})
	assert.Nil(t, err)
	assert.Contains(t, "node_gold", resp.Result, "Should fallback to Gold node")
	assert.Equal(t, "Gold", resp.SLOTag)

	// Case 4: unknown reqType
	reqUnknown := &TestReq{
		reqId:   "req_unknown",
		reqType: constant.UnKnownReason,
	}
	resp, err = mgr.DispatchRequest(context.Background(), reqUnknown, nil, nil, "", RemoteRequestInfo{IsLocal: true})
	logger.Log().Errorf("%v", err)
	assert.Equal(t, err, &RequestTypeNotFoundError{
		reqId:   "req_unknown",
		reqType: constant.UnKnownReason,
	})

	// only default but gold request
	stats = map[string]map[string]stats2.WorkerStats{
		constant.Prefill: { // <- Default
			"default": &stats2.ZeroCopyStats{
				Id:     "default",
				Role:   constant.Prefill,
				Status: stats2.Available,
			},
		},
	}
	reqGold = &TestReq{
		reqId:   "req_gold",
		reqType: constant.Prefill,
		sloInterval: &utils.SLOInterval{
			PromptStrLengthMax: "500",
		},
	}
	schedulersManager.UpdateStats(stats, nil)
	schedulersManager.UpdateFallbackAlgoStats(stats)
	resp, err = mgr.DispatchRequest(context.Background(), reqGold, nil, nil, "", RemoteRequestInfo{IsLocal: true})
	assert.Nil(t, err)
	assert.Equal(t, "default", resp.Result, "Should fallback to default node")
	assert.Equal(t, "default", resp.SLOTag)

	// only silver but no default slo instances, but request claims gold slo, it will fallback to Silver SLO instance
	stats = map[string]map[string]stats2.WorkerStats{
		constant.Prefill: { // <- Default
			"node_silver": &stats2.ZeroCopyStats{
				Id:     "node_silver",
				Role:   constant.Prefill,
				Status: stats2.Available,
				SLOTag: "Silver",
			},
		},
	}
	reqGold = &TestReq{
		reqId:   "req_gold",
		reqType: constant.Prefill,
		sloInterval: &utils.SLOInterval{
			PromptStrLengthMax: "500",
		},
	}
	schedulersManager.UpdateStats(stats, nil)
	schedulersManager.UpdateFallbackAlgoStats(stats)
	resp, err = mgr.DispatchRequest(context.Background(), reqGold, nil, nil, "", RemoteRequestInfo{IsLocal: true})
	assert.Nil(t, err)
	assert.Equal(t, "node_silver", resp.Result, "Should fallback to default node")
	assert.Equal(t, "Silver", resp.SLOTag)

	// nil prefill
	stats = map[string]map[string]stats2.WorkerStats{}
	schedulersManager.UpdateStats(stats, nil)
	schedulersManager.UpdateFallbackAlgoStats(stats)

	resp, err = mgr.DispatchRequest(context.Background(), reqGold, nil, nil, "", RemoteRequestInfo{IsLocal: true})
	assert.Equal(t, resp.Result, "")
}

func TestFallbackDispatchWithoutSLOFallback(t *testing.T) {
	cfg := GetSchedulerCfg()
	cfg.SLOScheduling.SloFallbackEnable = false

	metricsCfg := commoncfg.MetricsCfg{Enable: false}
	metrics.InitMetrics(&metricsCfg)
	pdLink := health.NewPDLinkBlacklistManager(&commoncfg.LinkBlacklistConfig{Enable: false})
	remoteReqMgr := NewRemoteScheduledRequestManager(cfg.RemoteSchedule)
	// Initialize Manager
	mgr, err := NewManager(&commoncfg.Config{SchedulersCfg: cfg}, nil, nil, pdLink, remoteReqMgr)
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Close()

	schedulersManager := mgr.(*SchedulersManager)

	// node_silver -> Tag: Silver
	// node_gold   -> Tag: Gold
	stats := map[string]map[string]stats2.WorkerStats{
		constant.Prefill: {
			"node_silver": &stats2.ZeroCopyStats{
				Id:     "node_silver",
				Role:   constant.Prefill,
				Status: stats2.Available,
				SLOTag: "Silver",
			},
			"node_gold": &stats2.ZeroCopyStats{
				Id:     "node_gold",
				Role:   constant.Prefill,
				Status: stats2.Available,
				SLOTag: "Gold",
			},
		},
	}

	schedulersManager.UpdateStats(stats, nil)
	schedulersManager.UpdateFallbackAlgoStats(stats)

	// only silver but no default slo instances, but request claims gold slo, it will fallback to Silver SLO instance
	stats = map[string]map[string]stats2.WorkerStats{
		constant.Prefill: { // <- Default
			"node_silver": &stats2.ZeroCopyStats{
				Id:     "node_silver",
				Role:   constant.Prefill,
				Status: stats2.Available,
				SLOTag: "Silver",
			},
		},
	}
	reqGold := &TestReq{
		reqId:   "req_gold",
		reqType: constant.Prefill,
		sloInterval: &utils.SLOInterval{
			PromptStrLengthMax: "500",
		},
	}
	schedulersManager.UpdateStats(stats, nil)
	schedulersManager.UpdateFallbackAlgoStats(stats)
	// cannot find Gold slo instance and slo don't allow fallback to silver, return empty result
	resp, err := mgr.DispatchRequest(context.Background(), reqGold, nil, nil, "", RemoteRequestInfo{IsLocal: true})
	assert.Equal(t, resp.Result, "")
}

func TestUpdateConfig(t *testing.T) {
	err := Register("manager_test1", func() (scheduler.ScheduleAlgorithm, error) {
		return &TestScheduler{}, nil
	})
	if err != nil {
		t.Errorf("failed to register test1")
	}
	cfg := commoncfg.SchedulersCfg{
		Schedulers: []commoncfg.SchedulerCfg{
			{
				SchedulerName: "manager_test1",
				InstanceRole:  constant.Prefill,
				Params:        nil,
			},
			{
				SchedulerName: "manager_test1",
				InstanceRole:  constant.Decode,
				Params:        nil,
			},
		},
		SLOScheduling: &commoncfg.SLOSchedulerCfg{
			SloEnable: false,
		},
		SystemPromptLength: 10,
	}
	pdLink := health.NewPDLinkBlacklistManager(&commoncfg.LinkBlacklistConfig{Enable: false})
	remoteReqMgr := NewRemoteScheduledRequestManager(cfg.RemoteSchedule)
	manager, err := NewManager(&commoncfg.Config{SchedulersCfg: cfg}, nil, nil, pdLink, remoteReqMgr)
	if err != nil {
		t.Errorf("failed to create manager")
	}
	newConf := map[string]string{
		"newCfg": "cfg_val",
	}
	pSchedulerPrt := manager.(*SchedulersManager).roleLookUp[constant.Prefill][constant.SLOTagDefault].Algorithm.(*TestScheduler)
	dSchedulerPtr := manager.(*SchedulersManager).roleLookUp[constant.Decode][constant.SLOTagDefault].Algorithm.(*TestScheduler)
	expectInitCfg := map[string]string{
		commoncfg.SystemPromptFieldName: "10",
	}
	if !reflect.DeepEqual(pSchedulerPrt.Config, expectInitCfg) {
		t.Errorf("prefill scheduler config not as expected, got %v", pSchedulerPrt.Config)
	}
	if !reflect.DeepEqual(dSchedulerPtr.Config, expectInitCfg) {
		t.Error("decode scheduler config not as expected")
	}

	err = manager.UpdateConfig("not_exists", newConf)
	if err != nil {
		t.Error("should ignore the unknown request type config update")
	}

	err = manager.UpdateConfig(constant.Prefill, newConf)
	if err != nil {
		t.Errorf("failed to register test1")
	}
	expectInitCfgAfterChange := map[string]string{
		commoncfg.SystemPromptFieldName: "10",
		"newCfg":                        "cfg_val",
	}
	if !reflect.DeepEqual(pSchedulerPrt.Config, expectInitCfgAfterChange) {
		t.Error("prefill scheduler config change not as expected")
	}
	if !reflect.DeepEqual(dSchedulerPtr.Config, expectInitCfg) {
		t.Error("decode scheduler config not changed after prefill config changes")
	}

	newConf2 := map[string]string{
		"newCfg2": "cfg_val2",
	}
	err = manager.UpdateConfig(constant.Decode, newConf2)
	if err != nil {
		t.Errorf("failed to register test1")
	}
	expectInitCfgAfterChange = map[string]string{
		commoncfg.SystemPromptFieldName: "10",
		"newCfg2":                       "cfg_val2",
	}
	if !reflect.DeepEqual(dSchedulerPtr.Config, expectInitCfgAfterChange) {
		t.Errorf("decode scheduler config not changed as expected, got %v", dSchedulerPtr.Config)
	}
}

type TestReq struct {
	reqId            string
	reqType          string
	sloTag           string
	sloInterval      *utils.SLOInterval
	promptTokenCount int32
}

func (r *TestReq) GetApcStrategyType() string {
	return ""
}

func (r *TestReq) GetPromptCacheKey() string {
	return ""
}

func (r *TestReq) GetModel() string {
	return "test-model"
}

func (r *TestReq) IsStream() bool {
	return true
}

func (r *TestReq) GetChat() []utils.ChatContent {
	return make([]utils.ChatContent, 0)
}

func (r *TestReq) GetPromptTokenCount() int32 {
	if r.promptTokenCount == 0 {
		r.promptTokenCount = 200
	}
	return r.promptTokenCount
}

func (r *TestReq) GetSLOTag() string {
	if r.sloTag == "" {
		return constant.SLOTagDefault
	}
	return r.sloTag
}

func (r *TestReq) GetSLOInterval() *utils.SLOInterval {
	return r.sloInterval
}

func (r *TestReq) GetPrompt() []utils.ChatContent {
	return make([]utils.ChatContent, 0)
}

func (r *TestReq) GetMaxToken() int32 {
	return 5000
}

func (r *TestReq) GetRequestId() string {
	return r.reqId
}

func (r *TestReq) GetHttpRequest() *http.Request {
	return nil
}

func (r *TestReq) GetType() string {
	return r.reqType
}

func (r *TestReq) GetTokenCount() int32 {
	return 10
}
