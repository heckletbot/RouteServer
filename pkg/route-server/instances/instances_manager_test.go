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

package instances

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	peer_client "infer-dp-scheduler/pkg/common/client/peer-client"
	"infer-dp-scheduler/pkg/route-server/metrics"

	"github.com/gin-gonic/gin"
	"infer-dp-scheduler/pkg/common/cluster"
	"infer-dp-scheduler/pkg/common/errs"
	"infer-dp-scheduler/pkg/common/health"
	"infer-dp-scheduler/pkg/common/utils"

	"github.com/stretchr/testify/assert"
	"infer-dp-scheduler/pkg/common/logger"
	"infer-dp-scheduler/pkg/common/openai"

	"github.com/agiledragon/gomonkey/v2"
	dto "github.com/prometheus/client_model/go"
	"infer-dp-scheduler/pkg/common/client"
	instanceclient "infer-dp-scheduler/pkg/common/client/instance-client"
	monitor_client "infer-dp-scheduler/pkg/common/client/monitor-client"
	"infer-dp-scheduler/pkg/common/config"
	"infer-dp-scheduler/pkg/common/constant"
	instance_metrics "infer-dp-scheduler/pkg/common/instance-metrics"
	scheduler2 "infer-dp-scheduler/pkg/common/scheduler"
	"infer-dp-scheduler/pkg/route-server/scheduler"
)

func Test_getRoleInstanceSize(t *testing.T) {
	type args struct {
		routes map[string]config.Route
	}
	tests := []struct {
		name string
		args args
		want map[string]int
	}{
		{
			name: "0p0d",
			args: args{
				make(map[string]config.Route),
			},
			want: map[string]int{},
		},
		{
			name: "1p2d",
			args: args{
				map[string]config.Route{
					"p1": {
						Ip:          "1.1.1.1",
						Port:        0,
						Role:        "P",
						StaticInfos: nil,
					},
					"d1": {
						Ip:          "2.2.2.2",
						Port:        0,
						Role:        "D",
						StaticInfos: nil,
					},
					"d2": {
						Ip:          "3.3.3.3",
						Port:        0,
						Role:        "D",
						StaticInfos: nil,
					},
				},
			},
			want: map[string]int{
				"P": 1,
				"D": 2,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := getRoleInstanceSize(tt.args.routes); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("getRoleInstanceSize() = %v, want %v", got, tt.want)
			}
		})
	}
}

func testConfig(insConfig *config.InstanceManagerCfg, isCandidate bool) *config.Config {
	return &config.Config{
		InstanceManagerCfg: *insConfig,
		HttpConfig: config.HttpConfig{
			DialerTimeout:       100,
			DialerKeepAlive:     100,
			RequestTimeout:      10,
			MaxIdleConns:        10,
			MaxIdleConnsPerHost: 10,
			IdleConnTimeout:     100,
		},
		ClusterCfg: config.ClusterConfig{
			RoleCandidate: isCandidate,
		},
	}
}

func TestNewManager(t *testing.T) {
	cfg := config.InstanceManagerCfg{
		InstanceClientType: "vllm",
		RequiredRoles:      []string{constant.Prefill, constant.Decode},
		RouteTable: config.RouteTable{
			InstanceRoutes: map[string]config.Route{
				"p1": {
					Ip:          "1.1.1.1",
					Port:        8080,
					Role:        constant.Prefill,
					StaticInfos: nil,
				},
				"d1": {
					Ip:          "2.2.2.2",
					Port:        8080,
					Role:        constant.Decode,
					StaticInfos: nil,
				},
			},
		},
		HealthCheckConfig: &config.HealthCheckConfig{
			AvailableCheckersCfg: config.AvailableCheckersConfig{
				RestHealthCfg: config.RestHealthConfig{
					Enable:          true,
					ConsecutiveTime: 3,
				},
			},
			UnavailableCheckersCfg:   config.UnavailableCheckersConfig{},
			EventThresholdIntervalMs: 1000,
			RestHealthCommonConfig: config.RestHealthCommonConfig{
				Interval:             100,
				HealthCheckTimeoutMs: 1000,
				MaxConcurrency:       1000,
				BatchSize:            1,
			},
		},
	}

	_, err := NewManager(testConfig(&cfg, true), roundRobinSchedulerManager(t), nil, utils.NewRegularMessageCenter())
	if err == nil {
		t.Errorf("expect manager is nil due to creation failed")
	}

	_, err = NewManager(testConfig(&cfg, true), roundRobinSchedulerManager(t), &TestMonitorClient{
		isInitErr: true,
	}, utils.NewRegularMessageCenter())
	if err == nil {
		t.Errorf("expect manager is nil due to creation failed")
	}

	manager, err := NewManager(testConfig(&cfg, true), roundRobinSchedulerManager(t), &TestMonitorClient{}, utils.NewRegularMessageCenter())
	if err != nil || manager == nil {
		t.Errorf("failed to create manager, error: %v", err)
		return
	}

	if manager.GetCurrentStage() != "RUNNING" {
		t.Errorf("manager is not running after creation")
	}

	// can be stopped without deadlock
	manager.Stop()
}

func TestMonitorRegistrationInit(t *testing.T) {
	schedulerManager := testSchedulerManager(t, "100", "", "")
	expectStats := map[string][]string{
		"prefill": {},
		"decode":  {},
	}
	actualStatsReg := schedulerManager.GetStatsRegistrations()
	if !reflect.DeepEqual(expectStats, actualStatsReg) {
		t.Errorf("expect empty stats, but got %v", actualStatsReg)
	}

	schedulerManager = testSchedulerManager(t, "100", "stat1,stat2", "")
	expectStats = map[string][]string{
		"prefill": {"stat1", "stat2"},
		"decode":  {},
	}
	actualStatsReg = schedulerManager.GetStatsRegistrations()
	if !reflect.DeepEqual(expectStats, actualStatsReg) {
		t.Errorf("expect test stats, but got %v", actualStatsReg)
	}

	schedulerManager = testSchedulerManager(t, "100", "stat1,stat2", "stat3,stat4,stat5")
	expectStats = map[string][]string{
		"prefill": {"stat1", "stat2"},
		"decode":  {"stat3", "stat4", "stat5"},
	}
	actualStatsReg = schedulerManager.GetStatsRegistrations()
	if !reflect.DeepEqual(expectStats, actualStatsReg) {
		t.Errorf("expect test stats, but got %v", actualStatsReg)
	}

	cfg := config.InstanceManagerCfg{
		InstanceClientType: "vllm",
		RequiredRoles:      []string{constant.Prefill, constant.Decode},
		RouteTable: config.RouteTable{
			InstanceRoutes: map[string]config.Route{
				"p1": {
					Ip:          "1.1.1.1",
					Port:        8080,
					Role:        constant.Prefill,
					StaticInfos: nil,
				},
				"d1": {
					Ip:          "2.2.2.2",
					Port:        8080,
					Role:        constant.Decode,
					StaticInfos: nil,
				},
			},
		},
		HealthCheckConfig: &config.HealthCheckConfig{
			AvailableCheckersCfg: config.AvailableCheckersConfig{
				RestHealthCfg: config.RestHealthConfig{
					Enable:          true,
					ConsecutiveTime: 3,
				},
			},
			UnavailableCheckersCfg:   config.UnavailableCheckersConfig{},
			EventThresholdIntervalMs: 1000,
		},
	}

	patches := gomonkey.NewPatches()
	defer patches.Reset()

	checkCount := 0
	patches.ApplyMethod(reflect.TypeOf(&instanceclient.VllmClient{}), "HealthCheck", func() (bool, int, error) {
		checkCount++
		return true, 200, nil
	})
	patches.ApplyMethod(reflect.TypeOf(&instanceclient.VllmClient{}), "ModelName", func() (string, error) {
		return "test_model", nil
	})

	monitorClient := &TestMonitorClient{}
	_, err := NewManager(testConfig(&cfg, true), schedulerManager, monitorClient, utils.NewRegularMessageCenter())
	if err != nil {
		t.Errorf("fail to create manager, error: %v", err)
	}
	if !reflect.DeepEqual(expectStats, monitorClient.metricsRegisters) {
		t.Errorf("monitor stats should be registered by instance-manager, expect %v but got %v",
			expectStats, monitorClient.metricsRegisters)
	}
}

func PrepareCfg() *config.InstanceManagerCfg {
	cfg := config.InstanceManagerCfg{
		InstanceClientType: "vllm",
		RequiredRoles:      []string{constant.Prefill, constant.Decode},
		RouteTable: config.RouteTable{
			InstanceRoutes: map[string]config.Route{
				"p1": {
					Ip:          "1.1.1.1",
					Port:        8080,
					Role:        constant.Prefill,
					StaticInfos: nil,
				},
				"d1": {
					Ip:          "2.2.2.2",
					Port:        8080,
					Role:        constant.Decode,
					StaticInfos: nil,
				},
			},
		},
		HealthCheckConfig: &config.HealthCheckConfig{
			AvailableCheckersCfg: config.AvailableCheckersConfig{
				RestHealthCfg: config.RestHealthConfig{
					Enable:          true,
					ConsecutiveTime: 3,
				},
			},
			UnavailableCheckersCfg: config.UnavailableCheckersConfig{
				RestHealthCfg: config.RestHealthConfig{
					Enable:          true,
					ConsecutiveTime: 3,
				},
			},
			RestHealthCommonConfig: config.RestHealthCommonConfig{
				Interval:             100,
				HealthCheckTimeoutMs: 1000,
				MaxConcurrency:       1000,
				BatchSize:            1,
			},
			FallbackHealthCheckConfig: config.FallbackHealthCheckConfig{
				Enable:                true,
				ResumeCheckIntervalMs: 100,
				ResumeTimeMs:          1000,
			},
			EventThresholdIntervalMs: 1000,
		},
	}
	return &cfg
}

func PrepareInstanceManager(t *testing.T) (*config.InstanceManagerCfg, *scheduler.SchedulersManager, *TestMonitorClient) {
	cfg := PrepareCfg()

	scheduleManager := testSchedulerManager(t, "100", "", "").(*scheduler.SchedulersManager)

	monitorClient := &TestMonitorClient{}
	monitorClient.testStats = make(map[string]map[string]*monitor_client.MonitorStats)
	monitorClient.testStats[constant.Prefill] = make(map[string]*monitor_client.MonitorStats)
	monitorClient.testStats[constant.Decode] = make(map[string]*monitor_client.MonitorStats)
	monitorClient.testStats[constant.Prefill]["p1"] = &monitor_client.MonitorStats{
		Metrics: &map[string][]*instance_metrics.MetricInfo{
			"1": {
				{
					MetricType: dto.MetricType_COUNTER,
					MetricName: "1",
					Value:      1.1,
				},
			},
			"2": {
				{
					MetricType: dto.MetricType_COUNTER,
					MetricName: "2",
					Value:      2.2,
				},
			},
		},
	}
	monitorClient.testStats[constant.Decode]["d1"] = &monitor_client.MonitorStats{
		Metrics: &map[string][]*instance_metrics.MetricInfo{
			"3": {
				{
					MetricType: dto.MetricType_COUNTER,
					MetricName: "3",
					Value:      3.3,
				},
			},
			"4": {
				{
					MetricType: dto.MetricType_COUNTER,
					MetricName: "4",
					Value:      4.4,
				},
			},
		},
	}

	return cfg, scheduleManager, monitorClient
}

func Test_SwitchBetweenAvailAndUnavailable(t *testing.T) {
	cfg, scheduleManager, monitorClient := PrepareInstanceManager(t)
	manager, err := NewManager(testConfig(cfg, true), scheduleManager, monitorClient, utils.NewRegularMessageCenter())
	if err != nil || manager == nil {
		t.Errorf("fail to create manager")
		return
	}
	cfg.HealthCheckConfig.AvailableCheckersCfg.RestHealthCfg.Enable = false
	cfg.HealthCheckConfig.UnavailableCheckersCfg.RestHealthCfg.Enable = false
	instanceManager := manager.(*InstanceManager)
	clusterService := initMockClusterService(&cfg.RouteTable)
	startTestManager(t, instanceManager, clusterService)

	assert.Equal(t, 2, len(instanceManager.allInstanceMap))
	assert.Equal(t, 2, len(instanceManager.unavailableMap))
	assert.Equal(t, 0, len(instanceManager.availableMap))

	instanceManager.switchToUnavailable(instanceManager.allInstanceMap["p1"])
	assert.Equal(t, 2, len(instanceManager.allInstanceMap))
	assert.Equal(t, 2, len(instanceManager.unavailableMap))
	assert.Equal(t, 0, len(instanceManager.availableMap))

	instanceManager.switchToAvailable(instanceManager.allInstanceMap["p1"])
	assert.Equal(t, 2, len(instanceManager.allInstanceMap))
	assert.Equal(t, 1, len(instanceManager.unavailableMap))
	assert.Equal(t, 1, len(instanceManager.availableMap))
	assert.NotNil(t, instanceManager.availableMap["p1"])

	instanceManager.switchToAvailable(instanceManager.allInstanceMap["d1"])
	assert.Equal(t, 2, len(instanceManager.allInstanceMap))
	assert.Equal(t, 0, len(instanceManager.unavailableMap))
	assert.Equal(t, 2, len(instanceManager.availableMap))
	assert.NotNil(t, instanceManager.availableMap["d1"])

	addRoutes := map[string]*config.Route{
		"newP": {
			Ip:          "3.3.3.3",
			Port:        3333,
			Role:        constant.Prefill,
			StaticInfos: nil,
		},
		"newD": {
			Ip:          "3.3.3.3",
			Port:        3333,
			Role:        constant.Prefill,
			StaticInfos: nil,
		},
		"nilRoute": nil,
	}
	instanceManager.OnAddInstance(addRoutes, "add")
	// should ignore nilRoute
	assert.Equal(t, 4, len(instanceManager.allInstanceMap))
	// add to unavailable at first
	assert.Equal(t, 2, len(instanceManager.unavailableMap))
	assert.Equal(t, 2, len(instanceManager.availableMap))
	assert.NotNil(t, instanceManager.unavailableMap["newP"])
	assert.NotNil(t, instanceManager.unavailableMap["newD"])

	newInstanceRouteMap := make(map[string]*config.Route)
	for k, v := range cfg.RouteTable.InstanceRoutes {
		newInstanceRouteMap[k] = &v
	}
	for k, v := range addRoutes {
		newInstanceRouteMap[k] = v
	}
	instanceManager.OnStateRestored(&cluster.ClusterState{
		InstanceRoutes: newInstanceRouteMap,
	})
	instanceManager.OnLeaderChanged(clusterService.localPeer, true, &utils.AllInstanceFailuresSnapshot{
		LeaderId:     "xxx",
		LocalRound:   1,
		SnapshotTime: "",
		Failures: map[string]*utils.InstanceFailureRecord{
			"p1": {
				Reason: "mock as failure",
			},
			"newD": {
				Reason: "mock as failure",
			},
			"notExists": {
				Reason: "mock as failure, should ignored",
			},
		},
	})
	assert.Equal(t, 4, len(instanceManager.allInstanceMap))
	assert.Equal(t, 2, len(instanceManager.unavailableMap))
	assert.Equal(t, 2, len(instanceManager.availableMap))
	assert.NotNil(t, instanceManager.unavailableMap["p1"])
	assert.NotNil(t, instanceManager.unavailableMap["newD"])
}

func TestPeriodSync_CopyOnWrite(t *testing.T) {
	PeriodSyncTest(t)
}

func PeriodSyncTest(t *testing.T) {
	patches := gomonkey.NewPatches()
	defer patches.Reset()
	patches.ApplyMethod(reflect.TypeOf(&instanceclient.VllmClient{}), "HealthCheck", func() (bool, int, error) {
		return true, 200, nil
	})
	patches.ApplyMethod(reflect.TypeOf(&instanceclient.VllmClient{}), "ModelName", func() (string, error) {
		return "test_model", nil
	})

	cfg, scheduleManager, monitorClient := PrepareInstanceManager(t)

	pContext := scheduleManager.GetContext(constant.Prefill, constant.SLOTagDefault)
	pScheduler := pContext.Algorithm.(*scheduler.TestScheduler)
	dContext := scheduleManager.GetContext(constant.Decode, constant.SLOTagDefault)
	dScheduler := dContext.Algorithm.(*scheduler.TestScheduler)

	manager, err := NewManager(testConfig(cfg, true), scheduleManager, monitorClient, utils.NewRegularMessageCenter())
	if err != nil || manager == nil {
		t.Errorf("fail to create manager")
		return
	}
	clusterService := initMockClusterService(&cfg.RouteTable)
	instanceManager, returnOnErr := startTestManager(t, manager, clusterService)
	if returnOnErr {
		return
	}
	assert.Equal(t, "InstanceManager", instanceManager.GetName())
	assert.Empty(t, instanceManager.GetBroadCastURIWhitelist())
	instanceClient, err := instanceManager.GetInstanceClient("not_exists")
	assert.Nil(t, instanceClient)
	assert.ErrorContains(t, err, "missing")
	err = instanceManager.leaderDealWithHealthChanges(make([]*client.Instance, 0), make([]*client.Instance, 0))
	assert.ErrorContains(t, err, "not leader right now")

	clusterService.ChangeLeader(true)
	routes := instanceManager.GetAllInstance()
	assert.Equal(t, 2, len(routes))
	assert.Equal(t, true, instanceManager.isMeLeader.Load())
	assert.Equal(t, 2, len(instanceManager.leaderHealthChecker.GetAllInstance()))

	defer manager.Stop()

	if manager.GetCurrentStage() != "RUNNING" {
		t.Errorf("manager is not running after creation")
	}
	time.Sleep(time.Duration(2) * time.Second)

	if pScheduler.StatsUpdateCount.Load() < 10 {
		t.Errorf("manager should sync at lease 10 time in 2 sec when interval is set to 100ms, but got %v",
			pScheduler.StatsUpdateCount.Load())
	}

	if monitorClient.callTime.Load() < 10 {
		t.Errorf("manager should call monitor at lease 10 time in 2 sec when interval is set to 100ms, but got %v",
			monitorClient.callTime.Load())
	}

	if pScheduler.GetStats()["p1"].GetNumericStat("1")[0].Value != 1.1 || pScheduler.GetStats()["p1"].GetNumericStat("2")[0].Value != 2.2 {
		t.Errorf("p scheduler should get monitor stats")
	}
	if dScheduler.GetStats()["d1"].GetNumericStat("3")[0].Value != 3.3 || dScheduler.GetStats()["d1"].GetNumericStat("4")[0].Value != 4.4 {
		t.Errorf("d scheduler should get monitor stats")
	}
	monitorClient.testStats[constant.Decode]["d1"] = &monitor_client.MonitorStats{
		Metrics: &map[string][]*instance_metrics.MetricInfo{
			"5": {{
				MetricType: dto.MetricType_COUNTER,
				MetricName: "3",
				Value:      5.3,
			},
			},
			"4": {{
				MetricType: dto.MetricType_COUNTER,
				MetricName: "4",
				Value:      5.5,
			}},
		},
	}
	time.Sleep(time.Duration(1) * time.Second)
	if dScheduler.GetStats()["d1"].GetNumericStat("5")[0].Value != 5.3 ||
		dScheduler.GetStats()["d1"].GetNumericStat("4")[0].Value != 5.5 ||
		dScheduler.GetStats()["d1"].GetNumericStat("3") != nil {
		t.Errorf("d scheduler should get monitor stats")
	}
}

func startTestManager(t *testing.T, manager Manager, clusterService *MockClusterService) (*InstanceManager, bool) {
	pdLinkBlackListManager := health.NewPDLinkBlacklistManager(&config.LinkBlacklistConfig{Enable: false})
	instanceManager := manager.(*InstanceManager)
	clusterService.RegisterStateApplier(instanceManager)
	manager.InitHealthyModule(nil, clusterService, pdLinkBlackListManager)
	if err := clusterService.Start(); err != nil {
		t.Errorf("fail to start clusterService")
		return nil, true
	}
	return instanceManager, false
}

func TestLeaderChangesSync(t *testing.T) {
	checkCount := 0
	patches := gomonkey.NewPatches()
	defer patches.Reset()
	patches.ApplyMethod(reflect.TypeOf(&instanceclient.VllmClient{}), "HealthCheck", func() (bool, int, error) {
		checkCount++
		return true, 200, nil
	})
	patches.ApplyMethod(reflect.TypeOf(&instanceclient.VllmClient{}), "ModelName", func() (string, error) {
		return "test_model", nil
	})
	cfg, scheduleManager, monitorClient := PrepareInstanceManager(t)
	manager, err := NewManager(testConfig(cfg, true), scheduleManager, monitorClient, utils.NewRegularMessageCenter())
	if err != nil || manager == nil {
		t.Errorf("fail to create manager")
		return
	}
	clusterService := initMockClusterService(&cfg.RouteTable)
	_, returnOnErr := startTestManager(t, manager, clusterService)
	if returnOnErr {
		return
	}
	defer manager.Stop()

	if manager.GetCurrentStage() != "RUNNING" {
		t.Errorf("manager is not running after creation")
	}
	clusterService.ChangeLeader(false)
	pContext := scheduleManager.GetContext(constant.Prefill, constant.SLOTagDefault)
	pScheduler := pContext.Algorithm.(*scheduler.TestScheduler)

	printCount := func() {
		for {
			time.Sleep(time.Duration(100) * time.Millisecond)
			logger.Log().Infof("check count: %v", checkCount)
			logger.Log().Infof("stats update count: %v", pScheduler.StatsUpdateCount.Load())
			if checkCount > 150 {
				break
			}
		}
	}
	go printCount()

	time.Sleep(time.Duration(2) * time.Second)
	// instance manager is not leader
	assert.Equal(t, false, manager.(*InstanceManager).isMeLeader.Load())
	assert.GreaterOrEqual(t, checkCount, 0)
	// manager should only update stats several times from beginning because it is not leader
	assert.LessOrEqual(t, pScheduler.StatsUpdateCount.Load(), int32(1))
	updateCounts1 := pScheduler.StatsUpdateCount.Load()
	logger.Log().Infof("check count should be 0, stats update count should be 1 as non-leader.")

	// instance manager becomes leader
	logger.Log().Infof("becoming leader")
	clusterService.ChangeLeader(true)

	assert.Equal(t, 0, scheduleManager.GetContext(constant.Prefill, constant.SLOTagDefault).Algorithm.(*scheduler.TestScheduler).LoadClearTime, "prefill algo should not clear states when become leader")
	assert.Equal(t, 0, scheduleManager.GetContext(constant.Decode, constant.SLOTagDefault).Algorithm.(*scheduler.TestScheduler).LoadClearTime, "decode algo should not clear states when become leader")
	assert.Equal(t, true, manager.(*InstanceManager).isMeLeader.Load())
	time.Sleep(time.Duration(2) * time.Second)
	// manager should check more than 10 times in 1 sec, check interval is 100 ms
	assert.GreaterOrEqual(t, checkCount, 10)
	// manager should update stats 10 times in 1 sec because it is leader now
	updateCounts2 := pScheduler.StatsUpdateCount.Load()
	assert.GreaterOrEqual(t, updateCounts2-updateCounts1, int32(10))

	// instance manager becomes non-leader
	logger.Log().Infof("step down as non leader")
	clusterService.ChangeLeader(false)
	assert.Equal(t, 1, scheduleManager.GetContext(constant.Prefill, constant.SLOTagDefault).Algorithm.(*scheduler.TestScheduler).LoadClearTime, "prefill algo should clear states when step down")
	assert.Equal(t, 1, scheduleManager.GetContext(constant.Decode, constant.SLOTagDefault).Algorithm.(*scheduler.TestScheduler).LoadClearTime, "decode algo should clear states when step down")

	assert.Equal(t, false, manager.(*InstanceManager).isMeLeader.Load())
	// stats should not update from now on
	time.Sleep(time.Duration(500) * time.Millisecond)
	nowUpdateCount := pScheduler.StatsUpdateCount.Load()
	time.Sleep(time.Duration(500) * time.Millisecond)
	assert.Equal(t, nowUpdateCount, pScheduler.StatsUpdateCount.Load())
}

func testSchedulerManager(t *testing.T, updateIntervalCfg string,
	prefillNeedStatsLine string, decodeNeedStats string) scheduler.Manager {
	cfg := config.SchedulersCfg{
		Schedulers: []config.SchedulerCfg{
			{
				SchedulerName: "instance_test1",
				InstanceRole:  constant.Prefill,
				Params: map[string]string{
					scheduler.UpdateIntervalKey: updateIntervalCfg,
					scheduler.NeedStatsKey:      prefillNeedStatsLine,
				},
			},
			{
				SchedulerName: "instance_test1",
				InstanceRole:  constant.Decode,
				Params: map[string]string{
					scheduler.UpdateIntervalKey: updateIntervalCfg,
					scheduler.NeedStatsKey:      decodeNeedStats,
				},
			},
		},
		SLOScheduling: &config.SLOSchedulerCfg{
			SloEnable: false,
		},
		SystemPromptLength: 10,
	}

	remoteRequestManager := scheduler.NewRemoteScheduledRequestManager(&config.RemoteSchedulerCfg{
		Enabled: false,
	})

	manager, err := scheduler.NewManager(&config.Config{SchedulersCfg: cfg}, nil, nil, nil, remoteRequestManager)
	if err != nil {
		t.Error(err.Error())
	}
	return manager
}

func TestDuplicateReqType(t *testing.T) {
	cfg := config.SchedulersCfg{
		Schedulers: []config.SchedulerCfg{
			{
				SchedulerName: "instance_test1",
				InstanceRole:  constant.Prefill,
			},
			{
				SchedulerName: "instance_test1",
				InstanceRole:  constant.Prefill,
			},
		},
		SLOScheduling: &config.SLOSchedulerCfg{
			SloEnable: false,
		},
		SystemPromptLength: 10,
	}
	pdLinkBlackListManager := health.NewPDLinkBlacklistManager(&config.LinkBlacklistConfig{Enable: false})
	_, err := scheduler.NewManager(&config.Config{SchedulersCfg: cfg}, nil, nil, pdLinkBlackListManager, nil)
	if err == nil {
		t.Errorf("duplicate req type should return error")
	}
}

func roundRobinSchedulerManager(t *testing.T) scheduler.Manager {
	cfg := config.SchedulersCfg{
		Schedulers: []config.SchedulerCfg{
			{
				SchedulerName: scheduler.RoundRobinAlgoName,
				InstanceRole:  constant.Prefill,
				Params:        nil,
			},
			{
				SchedulerName: scheduler.RoundRobinAlgoName,
				InstanceRole:  constant.Decode,
				Params:        nil,
			},
		},
		SLOScheduling: &config.SLOSchedulerCfg{
			SloEnable: false,
		},
		SystemPromptLength: 10,
	}
	pdLink := health.NewPDLinkBlacklistManager(&config.LinkBlacklistConfig{Enable: false})
	manager, err := scheduler.NewManager(&config.Config{SchedulersCfg: cfg}, nil, nil, pdLink, nil)
	if err != nil {
		t.Error(err.Error())
	}
	return manager
}

func TestMain(m *testing.M) {
	err := scheduler.InitBuiltinScheduleAlgorithms()
	if err != nil {
		return
	}
	_ = scheduler.Register("instance_test1", func() (scheduler2.ScheduleAlgorithm, error) {
		return &scheduler.TestScheduler{}, nil
	})
	code := m.Run()
	os.Exit(code)
}

type TestMonitorClient struct {
	callTime             atomic.Int32
	testStats            map[string]map[string]*monitor_client.MonitorStats
	metricsRegisters     map[string][]string
	isInitErr            bool
	isGetMonitorStatsErr bool
}

func (t *TestMonitorClient) Init(roleSubs map[string][]string) error {
	if t.isInitErr {
		return fmt.Errorf("failed to init for test")
	}
	t.metricsRegisters = roleSubs
	return nil
}

func (t *TestMonitorClient) GetMonitorStats(prefix string, roles []string,
	clients map[string]map[string]*client.Instance) (map[string]map[string]*monitor_client.MonitorStats, error) {
	if t.isGetMonitorStatsErr {
		return nil, fmt.Errorf("failed to get monitor stats for test")
	}
	t.callTime.Add(1)
	return t.testStats, nil
}

func GetTestInstanceManager(t *testing.T) *InstanceManager {
	instCfg, _, monitorClient := PrepareInstanceManager(t)

	httpCfg := config.HttpConfig{
		DialerTimeout:       100,
		DialerKeepAlive:     100,
		RequestTimeout:      10,
		MaxIdleConns:        10,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     100,
	}

	cfg := &config.Config{
		HttpConfig:         httpCfg,
		InstanceManagerCfg: *instCfg,
		ClusterCfg: config.ClusterConfig{
			RoleCandidate: true,
		},
	}
	testInstanceManager, err := NewManager(cfg, roundRobinSchedulerManager(t), monitorClient, utils.NewRegularMessageCenter())
	if err != nil {
		t.Error(err.Error())
		return nil
	}
	pdLinkBlackListManager := health.NewPDLinkBlacklistManager(&config.LinkBlacklistConfig{Enable: false})
	testInstanceManager.InitHealthyModule(nil, initMockClusterService(&instCfg.RouteTable), pdLinkBlackListManager)
	return testInstanceManager.(*InstanceManager)
}

// Test OnAddInstance
func TestOnAddInstance(t *testing.T) {
	tests := []struct {
		name      string
		routes    map[string]*config.Route
		initState func() *InstanceManager
		expected  func(t *testing.T, m *InstanceManager)
	}{
		{
			name: "Normal add instance",
			routes: map[string]*config.Route{
				"p1": {
					Ip:          "1.1.1.1",
					Port:        8080,
					Role:        constant.Prefill,
					StaticInfos: nil,
				},
				"d1": {
					Ip:          "2.2.2.2",
					Port:        8080,
					Role:        constant.Decode,
					StaticInfos: nil,
				},
			},
			initState: func() *InstanceManager {
				return GetTestInstanceManager(t)
			},
			expected: func(t *testing.T, m *InstanceManager) {
				if len(m.unavailable["prefill"]) != 1 || len(m.unavailable["decode"]) != 1 {
					t.Errorf("expected unavailable contains one instance each for prefill and decode, actual: %v", m.unavailable)
				}
				if m.roleLookUp["p1"] != "prefill" || m.roleLookUp["d1"] != "decode" {
					t.Errorf("expected roleLookUp contains p1->prefill, d1->decode, actual: %v", m.roleLookUp)
				}
				if m.roleInstanceSize["prefill"] != 1 || m.roleInstanceSize["decode"] != 1 {
					t.Errorf("expected roleInstanceSize contains prefill:1, decode:1, actual: %v", m.roleInstanceSize)
				}
				if len(m.allInstances) != 2 {
					t.Errorf("expected allInstances contains 2 instances, actual: %d", len(m.allInstances))
				}
				if len(m.allInstanceMap) != 2 {
					t.Errorf("expected allInstances contains 2 instances, actual: %d", len(m.allInstances))
				}
				if _, err := m.GetInstanceClient("p1"); err != nil {
					t.Errorf("instance client not found")
				}

				if _, err := m.GetInstanceClient("d1"); err != nil {
					t.Errorf("instance client not found")
				}
				sort.Strings(m.seenRoles)
				if !reflect.DeepEqual(m.seenRoles, []string{"decode", "prefill"}) {
					t.Errorf("expected seenRoles contains [prefill, decode], actual: %v", m.seenRoles)
				}
			},
		},
		{
			name:   "Empty routes",
			routes: nil,
			initState: func() *InstanceManager {
				return GetTestInstanceManager(t)
			},
			expected: func(t *testing.T, m *InstanceManager) {
				if len(m.unavailable) != 0 || len(m.allInstances) != 0 || len(m.seenRoles) != 0 {
					t.Errorf("expected state unchanged, actual: unavailable=%v, allInstances=%v, seenRoles=%v", m.unavailable, m.allInstances, m.seenRoles)
				}
			},
		},
		{
			name: "Invalid id and route",
			routes: map[string]*config.Route{
				"":      {Role: "prefill"}, // empty id
				"inst1": {Role: ""},        // nil route
				"inst2": {Role: ""},        // empty role
			},
			initState: func() *InstanceManager {
				return GetTestInstanceManager(t)
			},
			expected: func(t *testing.T, m *InstanceManager) {
				if len(m.unavailable) != 0 || len(m.allInstances) != 0 || len(m.seenRoles) != 0 {
					t.Errorf("expected state unchanged, actual: unavailable=%v, allInstances=%v, seenRoles=%v", m.unavailable, m.allInstances, m.seenRoles)
				}
			},
		},
		{
			name: "Duplicate id",
			routes: map[string]*config.Route{
				"inst1": {Role: "prefill"},
			},
			initState: func() *InstanceManager {
				m := GetTestInstanceManager(t)
				m.roleLookUp["inst1"] = "prefill"
				return m
			},
			expected: func(t *testing.T, m *InstanceManager) {
				if len(m.unavailable) != 0 || len(m.allInstances) != 0 {
					t.Errorf("expected state unchanged, actual: unavailable=%v, allInstances=%v", m.unavailable, m.allInstances)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := tt.initState()
			m.OnAddInstance(tt.routes, "request")
			tt.expected(t, m)
		})
	}
}

// Test OnRemoveInstance
func TestOnRemoveInstance(t *testing.T) {
	tests := []struct {
		name      string
		names     []string
		initState func() *InstanceManager
		expected  func(t *testing.T, m *InstanceManager)
	}{
		{
			name:  "Normal remove instance",
			names: []string{"inst1", "inst2"},
			initState: func() *InstanceManager {
				m := GetTestInstanceManager(t)
				m.unavailable["prefill"] = map[string]*client.Instance{
					"inst1": {ID: "inst1"},
				}
				m.unavailable["decode"] = map[string]*client.Instance{
					"inst2": {ID: "inst2"},
				}
				m.roleLookUp["inst1"] = "prefill"
				m.roleLookUp["inst2"] = "decode"
				m.roleInstanceSize["prefill"] = 1
				m.roleInstanceSize["decode"] = 1
				m.allInstances = []*client.Instance{{ID: "inst1"}, {ID: "inst2"}}
				return m
			},
			expected: func(t *testing.T, m *InstanceManager) {
				if len(m.unavailable) != 0 || len(m.available) != 0 {
					t.Errorf("expected unavailable and available to be empty, actual: %v, %v", m.unavailable, m.available)
				}
				if len(m.roleLookUp) != 0 {
					t.Errorf("expected roleLookUp to be empty, actual: %v", m.roleLookUp)
				}
				if len(m.roleInstanceSize) != 0 {
					t.Errorf("expected roleInstanceSize to be empty, actual: %v", m.roleInstanceSize)
				}
				if len(m.allInstances) != 0 {
					t.Errorf("expected allInstances to be empty, actual: %v", m.allInstances)
				}
				if len(m.seenRoles) != 0 {
					t.Errorf("expected seenRoles to be empty, actual: %v", m.seenRoles)
				}
			},
		},
		{
			name:  "nil names",
			names: nil,
			initState: func() *InstanceManager {
				return GetTestInstanceManager(t)
			},
			expected: func(t *testing.T, m *InstanceManager) {
				if len(m.unavailable) != 0 || len(m.allInstances) != 0 {
					t.Errorf("expected state unchanged, actual: unavailable=%v, allInstances=%v", m.unavailable, m.allInstances)
				}
			},
		},
		{
			name:  "Empty names",
			names: []string{},
			initState: func() *InstanceManager {
				return GetTestInstanceManager(t)
			},
			expected: func(t *testing.T, m *InstanceManager) {
				if len(m.unavailable) != 0 || len(m.allInstances) != 0 {
					t.Errorf("expected state unchanged, actual: unavailable=%v, allInstances=%v", m.unavailable, m.allInstances)
				}
			},
		},
		{
			name:  "Non-existent id",
			names: []string{"inst3"},
			initState: func() *InstanceManager {
				m := GetTestInstanceManager(t)
				m.unavailable["prefill"] = map[string]*client.Instance{
					"inst1": {ID: "inst1"},
				}
				m.roleLookUp["inst1"] = "prefill"
				m.roleInstanceSize["prefill"] = 1
				m.allInstances = []*client.Instance{{ID: "inst1", Stats: &instance_metrics.ZeroCopyStats{Role: "prefill"}}}
				return m
			},
			expected: func(t *testing.T, m *InstanceManager) {
				if len(m.unavailable["prefill"]) != 1 {
					t.Errorf("expected prefill role still has one instance, actual: %v", m.unavailable["prefill"])
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := tt.initState()
			m.OnRemoveInstance(tt.names, constant.InstanceChangeReasonSynced)
			tt.expected(t, m)
		})
	}
}

// Concurrent test
func TestConcurrentOnAddAndRemoveInstance(t *testing.T) {
	m := GetTestInstanceManager(t)
	var wg sync.WaitGroup
	numGoroutines := 100

	// Concurrent add instances
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			print(fmt.Sprintf("add routine %v\n", i))
			routes := map[string]*config.Route{
				fmt.Sprintf("inst%d", i): {Role: fmt.Sprintf("role%d", i%5)},
			}
			m.OnAddInstance(routes, constant.InstanceChangeReasonSynced)
			print(fmt.Sprintf("done add routine %v\n", i))
		}(i)
	}

	// Concurrent remove instances
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			names := []string{fmt.Sprintf("inst%d", i)}
			print(fmt.Sprintf("remove routine %v\n", i))
			m.OnRemoveInstance(names, constant.InstanceChangeReasonSynced)
			print(fmt.Sprintf("done remove routine %v\n", i))
		}(i)
	}

	print(fmt.Sprintf("waiting on group\n"))
	wg.Wait()
	print(fmt.Sprintf("done waiting on group\n"))

	// Verify state
	m.routeMapMutex.Lock()
	defer m.routeMapMutex.Unlock()
	m.syncMutex.Lock()
	defer m.syncMutex.Unlock()

	// Due to concurrent add and remove, state may not be fully predictable, but check consistency
	for role, size := range m.roleInstanceSize {
		if len(m.unavailable[role])+len(m.available[role]) != size {
			t.Errorf("role %s roleInstanceSize is inconsistent, expected %d, actual unavailable=%d, available=%d",
				role, size, len(m.unavailable[role]), len(m.available[role]))
		}
	}
	for id, role := range m.roleLookUp {
		if m.unavailable[role][id] == nil && m.available[role][id] == nil {
			t.Errorf("id %s in roleLookUp has no corresponding instance", id)
		}
	}
	if len(m.allInstances) != len(m.roleLookUp) {
		t.Errorf("allInstances and roleLookUp length mismatch, allInstances=%d, roleLookUp=%d",
			len(m.allInstances), len(m.roleLookUp))
	}
	if len(m.allInstanceMap) != len(m.allInstances) {
		t.Errorf("allInstances and allInstanceMap length mismatch")
	}
}

func TestConstructSuccessInstanceResp(t *testing.T) {
	customHeader := map[string][]string{
		"X-Custom-ID":     {"req-123"},
		"X-Response-Time": {"15ms"},
	}

	response := openai.InstanceResponse{
		InstanceName: "instance-1",
		StatusCode:   200,
		Header:       customHeader,
		Result:       "",
	}

	// Verify basic fields
	assert.Equal(t, "instance-1", response.InstanceName)
	assert.Equal(t, 200, response.StatusCode)
	assert.Empty(t, response.Error)

	// Verify custom Header
	assert.Equal(t, customHeader, response.Header)

	// Verify result mapping
	assert.Equal(t, "", response.Result)
	assert.Equal(t, 200, response.StatusCode)
}

func TestConstructFailedInstanceResp(t *testing.T) {
	customHeader := map[string][]string{
		"X-Custom-ID":     {"req-123"},
		"X-Response-Time": {"15ms"},
	}
	id := "instance-2"
	response := openai.InstanceResponse{
		InstanceName: id,
		StatusCode:   500,
		Header:       customHeader,
		Error:        fmt.Sprintf("fail to get instance client for %v", id),
	}

	// Verify basic fields
	assert.Equal(t, "instance-2", response.InstanceName)
	assert.Equal(t, 500, response.StatusCode)
	assert.Equal(t, fmt.Sprintf("fail to get instance client for %v", id), response.Error)

	// Verify custom Header
	assert.Equal(t, customHeader, response.Header)

	// Verify result mapping
	assert.Equal(t, "fail to get instance client for instance-2", response.Error)
	assert.Equal(t, 500, response.StatusCode)
}

func TestMergeBroadRespSuccess(t *testing.T) {
	// Create test InstanceManager
	manager := &InstanceManager{}

	instanceStatus := []*openai.InstanceResponse{
		{InstanceName: "instance-1", StatusCode: http.StatusOK},
		{InstanceName: "instance-2", StatusCode: http.StatusOK},
	}

	response := manager.mergeBroadResp(instanceStatus, 2, 0)

	assert.Equal(t, "success", response.Status)
	assert.Equal(t, int64(2), response.Total.Success)
	assert.Equal(t, int64(0), response.Total.Failed)
	assert.Len(t, response.Responses, 2)
}

func TestMergeBroadRespPartial(t *testing.T) {
	// Create test InstanceManager
	manager := &InstanceManager{}

	instanceStatus := []*openai.InstanceResponse{
		{InstanceName: "instance-1", StatusCode: http.StatusOK},
		{InstanceName: "instance-2", StatusCode: http.StatusInternalServerError},
		{InstanceName: "instance-3", StatusCode: http.StatusInternalServerError},
	}

	response := manager.mergeBroadResp(instanceStatus, 1, 2)

	assert.Equal(t, "partial", response.Status)
	assert.Equal(t, int64(1), response.Total.Success)
	assert.Equal(t, int64(2), response.Total.Failed)
	assert.Len(t, response.Responses, 3)
}

func TestMergeBroadRespFailed(t *testing.T) {
	// Create test InstanceManager
	manager := &InstanceManager{}

	instanceStatus := []*openai.InstanceResponse{
		{InstanceName: "instance-1", StatusCode: http.StatusInternalServerError},
		{InstanceName: "instance-2", StatusCode: http.StatusInternalServerError},
	}

	response := manager.mergeBroadResp(instanceStatus, 0, 2)

	assert.Equal(t, "failed", response.Status)
	assert.Equal(t, int64(0), response.Total.Success)
	assert.Equal(t, int64(2), response.Total.Failed)
	assert.Len(t, response.Responses, 2)
}

func TestMergeBroadRespEmpty(t *testing.T) {
	// Create test InstanceManager
	manager := &InstanceManager{}

	response := manager.mergeBroadResp([]*openai.InstanceResponse{}, 0, 0)

	assert.Equal(t, "failed", response.Status)
	assert.Equal(t, int64(0), response.Total.Success)
	assert.Equal(t, int64(0), response.Total.Failed)
	assert.Empty(t, response.Responses)

}

func TestMergeBroadRespMixedStatusCodes(t *testing.T) {
	// Create test InstanceManager
	manager := &InstanceManager{}

	instanceStatus := []*openai.InstanceResponse{
		{InstanceName: "instance-1", StatusCode: http.StatusOK},
		{InstanceName: "instance-2", StatusCode: http.StatusBadGateway},
		{InstanceName: "instance-3", StatusCode: http.StatusServiceUnavailable},
	}

	response := manager.mergeBroadResp(instanceStatus, 1, 2)

	assert.Equal(t, "partial", response.Status)
	assert.Equal(t, int64(1), response.Total.Success)
	assert.Equal(t, int64(2), response.Total.Failed)
	assert.Len(t, response.Responses, 3)
}

// Test isMethodAllowed function
func TestIsMethodAllowed(t *testing.T) {
	tests := []struct {
		name           string
		reqMethod      string
		allowedMethods []string
		want           bool
	}{
		{
			name:           "empty allowed methods (allow all)",
			reqMethod:      "POST",
			allowedMethods: []string{},
			want:           false,
		},
		{
			name:           "exact match (case insensitive)",
			reqMethod:      "post",
			allowedMethods: []string{"POST"},
			want:           true,
		},
		{
			name:           "method not allowed",
			reqMethod:      "PUT",
			allowedMethods: []string{"GET", "POST"},
			want:           false,
		},
		{
			name:           "multiple allowed methods",
			reqMethod:      "DELETE",
			allowedMethods: []string{"GET", "POST", "DELETE"},
			want:           true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isMethodAllowed(tt.reqMethod, tt.allowedMethods); got != tt.want {
				t.Errorf("isMethodAllowed() = %v, want %v", got, tt.want)
			}
		})
	}
}

// Test exact path matching
func TestWhitelistMiddleware_ExactPathMatch(t *testing.T) {
	testConfig := []config.WhitelistEntry{
		{Path: "/public/api", Methods: []string{"GET", "HEAD"}},
		{Path: "/health", Methods: []string{"GET"}},
	}

	im := &InstanceManager{}
	im.cfg = &config.InstanceManagerCfg{}
	im.cfg.URIWhitelist = testConfig

	tests := []*openai.BroadcastRequest{}
	tests = append(tests, &openai.BroadcastRequest{
		Targets: "test01",
		URI:     "/health",
		Method:  "GET",
	})

	for _, req := range tests {
		t.Run(req.Targets, func(t *testing.T) {
			if got := im.whitelistMiddleware(req); got != true {
				t.Errorf("WhitelistMiddleware() = %v, want %v", got, true)
			}
		})
	}
}

// Test empty method list
func TestWhitelistMiddleware_EmptyMethods(t *testing.T) {
	testConfig := []config.WhitelistEntry{
		{Path: "/public/api", Methods: []string{"GET", "HEAD"}},
		{Path: "/health", Methods: []string{"GET"}},
	}

	im := &InstanceManager{}
	im.cfg = &config.InstanceManagerCfg{}
	im.cfg.URIWhitelist = testConfig

	tests := []*openai.BroadcastRequest{}
	tests = append(tests, &openai.BroadcastRequest{
		Targets: "test01",
		URI:     "/healthz",
		Method:  "GET",
	})
	tests = append(tests, &openai.BroadcastRequest{
		Targets: "test02",
		URI:     "/test",
		Method:  "POST",
	})

	for _, req := range tests {
		t.Run(req.Targets, func(t *testing.T) {
			if got := im.whitelistMiddleware(req); got != false {
				t.Errorf("whitelistMiddleware() = %v, want %v", got, false)
			}
		})
	}
}

// Test mixed scenarios
func TestWhitelistMiddleware_MixedScenarios(t *testing.T) {
	testConfig := []config.WhitelistEntry{
		{Path: "/public/api", Methods: []string{"GET", "HEAD"}},
		{Path: "/health", Methods: []string{"GET"}},
	}

	im := &InstanceManager{}
	im.cfg = &config.InstanceManagerCfg{}
	im.cfg.URIWhitelist = testConfig

	tests := []*openai.BroadcastRequest{}
	tests = append(tests, &openai.BroadcastRequest{
		Targets: "test01",
		URI:     "/health",
		Method:  "GET",
	})
	tests = append(tests, &openai.BroadcastRequest{
		Targets: "test02",
		URI:     "/public/api",
		Method:  "GET",
	})
	tests = append(tests, &openai.BroadcastRequest{
		Targets: "test02",
		URI:     "/public/api",
		Method:  "POST",
		Body:    nil,
	})
	res := []bool{}
	res = append(res, true)
	res = append(res, true)
	res = append(res, false)
	for index, req := range tests {
		t.Run(req.Targets, func(t *testing.T) {
			if got := im.whitelistMiddleware(req); got != res[index] {
				t.Errorf("WhitelistMiddleware() = %v, want %v", got, res[index])
			}
		})
	}
}

func TestStateRecovered(t *testing.T) {
	cfg, scheduleManager, monitorClient := PrepareInstanceManager(t)
	manager, err := NewManager(testConfig(cfg, true), scheduleManager, monitorClient, utils.NewRegularMessageCenter())

	if err != nil || manager == nil {
		t.Errorf("fail to craete manager")
		return
	}
	instanceManager := manager.(*InstanceManager)
	clusterService := initMockClusterService(&cfg.RouteTable)
	pdLinkBlackListManager := health.NewPDLinkBlacklistManager(&config.LinkBlacklistConfig{Enable: false})
	manager.InitHealthyModule(func(ctx *gin.Context, path string, isChat bool, instance *client.Instance) (successToCheck bool, testSuccess bool, errDetails *errs.RSError) {
		return true, true, nil
	}, clusterService, pdLinkBlackListManager)

	routeMap := map[string]*config.Route{
		"p1": {
			Ip:          "x.x.x.x",
			Port:        9800,
			Role:        constant.Prefill,
			StaticInfos: nil,
		},
		"p2": {
			Ip:          "x.x.x.x",
			Port:        9801,
			Role:        constant.Prefill,
			StaticInfos: nil,
		},
		"p3": {
			Ip:          "x.x.x.x",
			Port:        9802,
			Role:        constant.Prefill,
			StaticInfos: nil,
		},
		"d1": {
			Ip:          "x.x.x.x",
			Port:        9900,
			Role:        constant.Decode,
			StaticInfos: nil,
		},
		"d2": {
			Ip:          "x.x.x.x",
			Port:        9901,
			Role:        constant.Decode,
			StaticInfos: nil,
		},
	}

	instanceManager.OnAddInstance(routeMap, "testing")

	checkTotalInstances(t, instanceManager, 3, 2, 0, 0)
	for key, _ := range instanceManager.allInstanceMap {
		if _, exists := routeMap[key]; !exists {
			t.Errorf("fail to find instance after add %s", key)
		}
	}

	instanceManager.OnRemoveInstance([]string{"p1", "d1"}, "testing")
	checkTotalInstances(t, instanceManager, 2, 1, 0, 0)
	delete(routeMap, "p1")
	delete(routeMap, "d1")
	for key, _ := range instanceManager.allInstanceMap {
		if _, exists := routeMap[key]; !exists {
			t.Errorf("fail to find instance after add %s", key)
		}
	}

	recoveredRouteMap := map[string]*config.Route{
		"p100": {
			Ip:          "x.x.x.x",
			Port:        9800,
			Role:        constant.Prefill,
			StaticInfos: nil,
		},
		"p200": {
			Ip:          "x.x.x.x",
			Port:        9801,
			Role:        constant.Prefill,
			StaticInfos: nil,
		},
		"d100": {
			Ip:          "x.x.x.x",
			Port:        9900,
			Role:        constant.Decode,
			StaticInfos: nil,
		},
		"d200": {
			Ip:          "x.x.x.x",
			Port:        9901,
			Role:        constant.Decode,
			StaticInfos: nil,
		},
		"d300": {
			Ip:          "x.x.x.x",
			Port:        9902,
			Role:        constant.Decode,
			StaticInfos: nil,
		},
	}
	state := cluster.DeepCopyState(clusterService.State())
	state.InstanceRoutes = recoveredRouteMap
	instanceManager.OnStateRestored(state)
	checkTotalInstances(t, instanceManager, 2, 3, 0, 0)
	for key, _ := range instanceManager.allInstanceMap {
		if _, exists := recoveredRouteMap[key]; !exists {
			t.Errorf("fail to find instance after add %s", key)
		}
	}
}

func checkTotalInstances(t *testing.T, manager *InstanceManager, pCount, dCount, pAvail, dAvail int) {
	assert.Equal(t, pCount+dCount, len(manager.allInstances))
	assert.Equal(t, pCount+dCount, len(manager.allInstanceMap))
	assert.Equal(t, pCount+dCount, len(manager.roleLookUp))

	pRoleAvailStats, ok := manager.availableStats[constant.Prefill].Load().(*map[string]instance_metrics.WorkerStats)
	assert.True(t, ok)
	assert.Equal(t, pAvail, len(*pRoleAvailStats))

	dRoleAvailStats, ok := manager.availableStats[constant.Decode].Load().(*map[string]instance_metrics.WorkerStats)
	assert.True(t, ok)
	assert.Equal(t, dAvail, len(*dRoleAvailStats))

	assert.Equal(t, pAvail, len(manager.available[constant.Prefill]))
	assert.Equal(t, dAvail, len(manager.available[constant.Decode]))
	assert.Equal(t, pCount-pAvail, len(manager.unavailable[constant.Prefill]))
	assert.Equal(t, dCount-dAvail, len(manager.unavailable[constant.Decode]))
	assert.Equal(t, pCount, manager.roleInstanceSize[constant.Prefill])
	assert.Equal(t, dCount, manager.roleInstanceSize[constant.Decode])
}

func initMockClusterService(initRoute *config.RouteTable) *MockClusterService {
	service := &MockClusterService{}
	localPeer := &utils.Peer{
		Name: "local peer",
		Addr: utils.PeerAddr{
			Host:     "localhost",
			HttpPort: 8080,
			OpsPort:  8180,
		},
		IsCandidate: true,
	}
	service.Init(config.ClusterConfig{}, initRoute, localPeer, nil)
	return service
}

type MockClusterService struct {
	LeaderFlag atomic.Bool

	appliers   []cluster.StateApplier
	localPeer  *utils.Peer
	localState *cluster.ClusterState
	failures   map[string]*utils.InstanceFailureRecord
}

func (m *MockClusterService) GetHealthStats() (cluster.EtcdHealthStatsCache, error) {
	return cluster.EtcdHealthStatsCache{}, nil
}

func (m *MockClusterService) UpdateHealthStats(cache cluster.EtcdHealthStatsCache) error {
	logger.Log().Infof("mock UpdateHealthStats successfully")
	return nil
}

func (m *MockClusterService) IsWorkerAlive(peerName string) (bool, error) {
	return true, nil
}

func (m *MockClusterService) GetInstanceCleanHandler() utils.Subscriber {
	return nil
}

func (m *MockClusterService) IsSingleton() bool {
	return true
}

func (m *MockClusterService) Init(cfg config.ClusterConfig, initRoute *config.RouteTable, localPeer *utils.Peer, peerClient *peer_client.PeerClient) {
	routes := make(map[string]*config.Route)
	if initRoute != nil {
		for k, v := range initRoute.InstanceRoutes {
			routes[k] = &v
		}
	}
	m.LeaderFlag = atomic.Bool{}
	m.localPeer = localPeer
	endPoint, err := utils.PeerHttpEndpoint(localPeer, "", false)
	if err != nil {
		endPoint = ""
	}
	m.localState = &cluster.ClusterState{
		Initialized: true,
		SeedPeers: []config.SeedInfo{
			{
				Name:         localPeer.Name,
				HttpEndpoint: endPoint,
			},
		},
		Peers: map[string]*utils.Peer{
			localPeer.Name: localPeer,
		},
		InstanceRoutes: routes,
		Version:        0,
	}
	m.appliers = make([]cluster.StateApplier, 0)
}

func (m *MockClusterService) Start() error {
	for _, applier := range m.appliers {
		applier.OnStateRestored(m.localState)
	}
	return nil
}

func (m *MockClusterService) ChangeLeader(up bool) {
	if up {
		m.LeaderFlag.Store(true)
		instanceFailures := &utils.AllInstanceFailuresSnapshot{
			LeaderId:     m.localPeer.Name,
			LocalRound:   1,
			SnapshotTime: "",
			Failures:     m.failures,
		}
		for _, applier := range m.appliers {
			applier.OnLeaderChanged(m.localPeer, true, instanceFailures)
		}
		return
	}
	m.LeaderFlag.Store(false)
	for _, applier := range m.appliers {
		applier.OnLeaderChanged(nil, false, nil)
	}
}

func (m *MockClusterService) IsLeader() bool {
	return m.LeaderFlag.Load()
}

func (m *MockClusterService) GetLeader() (*utils.Peer, string) {
	if m.LeaderFlag.Load() && m.localPeer != nil {
		return m.localPeer, m.localPeer.Name
	}
	return nil, ""
}

func (m *MockClusterService) LocalNode() *utils.Peer {
	return m.localPeer
}

func (m *MockClusterService) State() *cluster.ClusterState {
	return m.localState
}

func (m *MockClusterService) Stats() map[string]string {
	return make(map[string]string)
}

func (m *MockClusterService) HandleNewPeer(peer *utils.Peer) error {
	return nil
}

func (m *MockClusterService) HandleLeavePeer(names []string, reason string) error {
	return nil
}

func (m *MockClusterService) HandleNewInstance(req *cluster.InstanceJoinRequest) error {
	if req == nil {
		return nil
	}
	for k, v := range req.Instances {
		m.localState.InstanceRoutes[k] = v
	}
	for _, applier := range m.appliers {
		applier.OnAddInstance(req.Instances, "add")
	}
	return nil
}

func (m *MockClusterService) HandleLeaveInstance(req *cluster.InstanceLeaveRequest, leaveReason string) error {
	if req == nil {
		return nil
	}
	for _, name := range req.InstanceNames {
		delete(m.localState.InstanceRoutes, name)
	}
	for _, applier := range m.appliers {
		applier.OnRemoveInstance(req.InstanceNames, "remove")
	}
	return nil
}

func (m *MockClusterService) PersistInstanceFailures(failures map[string]*utils.InstanceFailureRecord) error {
	m.failures = failures
	return nil
}

func (m *MockClusterService) RegisterStateApplier(applier cluster.StateApplier) {
	m.appliers = append(m.appliers, applier)
}

func (m *MockClusterService) GetApplierNames() []string {
	return nil
}

func (m *MockClusterService) Close() {

}

func Test_resetFailedInstanceFromRecord(t *testing.T) {
	type args struct {
		failureDetail            *utils.InstanceFailureRecord
		instance                 *client.Instance
		expectStatus             instance_metrics.InstanceStatus
		expectMessageSubStr      string
		expectConsecutiveSuccess int32
		expectLasErrorNil        bool
	}
	tests := []struct {
		name string
		args args
	}{
		{
			name: "restore healthy to faulty",
			args: args{
				failureDetail: &utils.InstanceFailureRecord{
					Status: "faulty",
					Reason: "111",
				},
				instance: &client.Instance{
					ID: "11",
					Stats: &instance_metrics.ZeroCopyStats{
						Status: instance_metrics.Available,
					},
					LastError:                nil,
					ConsecutiveHealthFailed:  6,
					ConsecutiveHealthSuccess: 5,
					FaultMessage:             "",
				},
				expectStatus:             instance_metrics.Faulty,
				expectMessageSubStr:      "111",
				expectConsecutiveSuccess: 0,
				expectLasErrorNil:        false,
			},
		},
		{
			name: "restore healthy to deploying",
			args: args{
				failureDetail: &utils.InstanceFailureRecord{
					Status: "deploying",
					Reason: "deploying",
				},
				instance: &client.Instance{
					ID: "11",
					Stats: &instance_metrics.ZeroCopyStats{
						Status: instance_metrics.Faulty,
					},
					LastError:                nil,
					ConsecutiveHealthFailed:  6,
					ConsecutiveHealthSuccess: 5,
					FaultMessage:             "",
				},
				expectStatus:             instance_metrics.Init,
				expectMessageSubStr:      "deploying",
				expectConsecutiveSuccess: 0,
				expectLasErrorNil:        true,
			},
		},
		{
			name: "restore to faulty when failures instance status is available",
			args: args{
				failureDetail: &utils.InstanceFailureRecord{
					Status: "running",
					Reason: "running",
				},
				instance: &client.Instance{
					ID: "11",
					Stats: &instance_metrics.ZeroCopyStats{
						Status: instance_metrics.Available,
					},
					LastError:                nil,
					ConsecutiveHealthFailed:  6,
					ConsecutiveHealthSuccess: 5,
					FaultMessage:             "",
				},
				expectStatus:             instance_metrics.Faulty,
				expectMessageSubStr:      "resumed as faulty",
				expectConsecutiveSuccess: 0,
				expectLasErrorNil:        false,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resetFailedInstanceFromRecord(tt.args.failureDetail, tt.args.instance)
			assert.Equal(t, tt.args.expectStatus, tt.args.instance.Stats.GetStatus())
			assert.Contains(t, tt.args.instance.FaultMessage, tt.args.expectMessageSubStr)
			assert.Equal(t, tt.args.expectConsecutiveSuccess, tt.args.instance.ConsecutiveHealthSuccess)
			if tt.args.expectLasErrorNil {
				assert.Nil(t, tt.args.instance.LastError)
			} else {
				assert.NotNil(t, tt.args.instance.LastError)
			}
		})
	}
}

func TestHandleProxyToSingleInstanceWithEmptyInstance(t *testing.T) {
	cfg, scheduleManager, monitorClient := PrepareInstanceManager(t)
	manager, err := NewManager(testConfig(cfg, true), scheduleManager, monitorClient, utils.NewRegularMessageCenter())
	if err != nil {
		logger.Log().Errorf("new instance manager failed, err = %v\n", err)
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request, _ = http.NewRequest(http.MethodGet, "/v1/models", bytes.NewBufferString(""))
	manager.HandleProxyToSingleInstance(c)

	// Verify response
	assert.Equal(t, http.StatusInternalServerError, w.Code)
	assert.Equal(t, `{"reason":"no available instance","status":"failed"}`, w.Body.String())
}

func TestHandleProxyToSingleInstanceWithInvalidInstance(t *testing.T) {
	cfg, scheduleManager, monitorClient := PrepareInstanceManager(t)
	manager, err := NewManagerWithInstance(testConfig(cfg, true), scheduleManager, monitorClient)
	if err != nil {
		logger.Log().Errorf("new instance manager failed, err = %v\n", err)
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request, _ = http.NewRequest(http.MethodGet, "/v1/models", bytes.NewBufferString(""))
	manager.HandleProxyToSingleInstance(c)

	// Verify response
	assert.Equal(t, http.StatusBadGateway, w.Code)
	assert.Equal(t, `{"detail":"instance error: status code 502, body = ","error":"all instances failed"}`, w.Body.String())
}

func NewManagerWithInstance(rootCfg *config.Config, schedulerManager scheduler.Manager,
	monitorClient monitor_client.MonitorClient) (Manager, error) {
	if rootCfg == nil {
		return nil, fmt.Errorf("root config is nil")
	}
	if schedulerManager == nil || monitorClient == nil {
		return nil, fmt.Errorf("failed to create instance-manager, rootCfg or scheduler or monitor is nil")
	}

	cfg := rootCfg.InstanceManagerCfg
	httpCfg := rootCfg.HttpConfig
	allInstanceSize := len(cfg.RouteTable.InstanceRoutes)
	instanceManager := newInstanceManager(cfg, httpCfg, allInstanceSize, schedulerManager)
	instanceManager.isPureWorker = !rootCfg.ClusterCfg.RoleCandidate
	metricsCfg := config.MetricsCfg{
		Enable:   true,
		Duration: 30,
	}
	metrics.InitMetrics(&metricsCfg)
	err := initMetricsRegistration(schedulerManager, monitorClient)
	if err != nil {
		return nil, err
	}

	// construct all instances list
	instanceManager.seenRoles = make([]string, 0, len(instanceManager.unavailable))
	var allInstances = make([]*client.Instance, 0, allInstanceSize)
	routPrefill := config.Route{
		Ip:          "127.0.0.1",
		Port:        63000,
		Role:        "prefill",
		StaticInfos: nil,
	}
	insPrefill, err := newInstance("prefill01", routPrefill, &cfg, &httpCfg)
	if err != nil {
		logger.Log().Errorf("fail to new instance, err = %v\n", err)
	}
	routDecode := config.Route{
		Ip:          "127.0.0.1",
		Port:        63006,
		Role:        "decode",
		StaticInfos: nil,
	}
	insDecode, err := newInstance("decode01", routDecode, &cfg, &httpCfg)
	if err != nil {
		logger.Log().Errorf("fail to new instance, err = %v\n", err)
	}
	allInstances = append(allInstances, insPrefill)
	allInstances = append(allInstances, insDecode)
	var allInstanceMap = make(map[string]*client.Instance, allInstanceSize)
	instanceManager.allInstances = allInstances
	instanceManager.allInstanceMap = allInstanceMap
	instanceManager.monitorClient = monitorClient

	instanceManager.exitingWaitGroup.Add(3)
	go instanceManager.statsSyncLoop()
	go instanceManager.instanceWarningLoop()
	go instanceManager.failureSnapshotLoop()

	instanceManager.currentStage = "RUNNING"
	logger.Log().Info("instance manager start successfully")
	return instanceManager, nil
}

func TestHandleProxyToSingleInstanceWithErrReq(t *testing.T) {
	cfg, scheduleManager, monitorClient := PrepareInstanceManager(t)
	manager, err := NewManagerWithInstance(testConfig(cfg, true), scheduleManager, monitorClient)
	if err != nil {
		logger.Log().Errorf("new instance manager failed, err = %v\n", err)
	}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	expectedErr := errors.New("mock read body error")
	c.Request = &http.Request{
		Body: &ErrorReadCloser{ReadErr: expectedErr},
	}
	manager.HandleProxyToSingleInstance(c)
}

// ErrorReadCloser custom ReadCloser that returns error
type ErrorReadCloser struct {
	// Custom error to return
	ReadErr error
}

// Read implements io.Reader interface, forcefully returns error
func (e *ErrorReadCloser) Read(p []byte) (n int, err error) {
	return 0, e.ReadErr
}

// Close implements io.Closer interface (empty implementation is fine)
func (e *ErrorReadCloser) Close() error {
	return nil
}

// ===== InstanceManager new methods tests =====

// TestInstanceManager_GetAvailable test GetAvailable
func TestInstanceManager_GetAvailable(t *testing.T) {
	m := &InstanceManager{
		available: make(map[string]map[string]*client.Instance),
	}
	m.available[constant.Prefill] = map[string]*client.Instance{
		"inst01": {ID: "inst01"},
	}

	avail := m.GetAvailable()
	assert.Equal(t, 1, len(avail[constant.Prefill]))
}

// TestInstanceManager_GetHealthStatsUpdater test GetHealthStatsUpdater
func TestInstanceManager_GetHealthStatsUpdater(t *testing.T) {
	healthCache := NewInstanceHealthCache(nil, newTestHealthCheckConfig())
	m := &InstanceManager{
		healthCache: healthCache,
	}

	updater := m.GetHealthStatsUpdater()
	assert.NotNil(t, updater)
}

// TestHandleForceReadiness_CircuitBreakerDisabled test circuit breaker not enabled
func TestHandleForceReadiness_CircuitBreakerDisabled(t *testing.T) {
	m := &InstanceManager{}

	w := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(w)
	m.HandleForceReadiness(ctx)

	var rsp map[string]string
	utils.SonicApi.Unmarshal(w.Body.Bytes(), &rsp)
	assert.Equal(t, "disabled", rsp["status"])
}

// TestHandleForceReadiness_InvalidMode test invalid mode
func TestHandleForceReadiness_InvalidMode(t *testing.T) {
	cb := NewCircuitBreaker(&config.CircuitBreakerConfig{
		Enable:               true,
		CheckWindowSec:       10,
		FailureRateThreshold: 0.5,
		MinRequestCount:      5,
	})
	defer cb.Close()

	m := &InstanceManager{
		circuitBreaker: cb,
	}

	w := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(w)
	ctx.Request = httptest.NewRequest("POST", "/", nil)
	ctx.Request.Header.Set("Content-Type", "application/json")
	ctx.Request.Body = nil // no body, default to auto
	m.HandleForceReadiness(ctx)

	var rsp map[string]string
	utils.SonicApi.Unmarshal(w.Body.Bytes(), &rsp)
	assert.Equal(t, "success", rsp["status"])
}

// TestSnapshotUnhealthyInstances_RoleField test snapshot contains Role field
func TestSnapshotUnhealthyInstances_RoleField(t *testing.T) {
	m := &InstanceManager{
		unavailableMap:       make(map[string]*client.Instance),
		routeMapMutex:        sync.RWMutex{},
		failureSnapshotMutex: sync.RWMutex{},
	}
	m.isMeLeader.Store(true)
	m.failureSnapshotFunc = func(records map[string]*utils.InstanceFailureRecord) error {
		return nil
	}

	stats := CreateStats("inst01", "prefill", "10", 0, 1, 100, 50)
	m.unavailableMap["inst01"] = &client.Instance{
		ID: "inst01", Stats: stats,
		FaultMessage: "test fault",
	}

	var capturedRecords map[string]*utils.InstanceFailureRecord
	m.failureSnapshotFunc = func(records map[string]*utils.InstanceFailureRecord) error {
		capturedRecords = records
		return nil
	}

	// First calling shouldSnapshotFailuresNoLock will modify lastSnapshotTimestamp, so manually set to trigger snapshot
	m.lastSnapshotTimestamp = time.Time{} // zero value, ensure large time difference

	m.SnapshotUnhealthyInstances()

	assert.NotNil(t, capturedRecords)
	assert.Equal(t, "prefill", capturedRecords["inst01"].Role)
	assert.Equal(t, "test fault", capturedRecords["inst01"].Reason)
}

// TestShouldSnapshotFailuresNoLock test snapshot decision logic
func TestShouldSnapshotFailuresNoLock(t *testing.T) {
	m := &InstanceManager{
		cfg: &config.InstanceManagerCfg{
			HealthCheckConfig: &config.HealthCheckConfig{
				FailureSnapshotIntervalMs: 100,
				RestHealthCommonConfig: config.RestHealthCommonConfig{
					Interval: 50,
				},
			},
		},
		unavailableMap:      make(map[string]*client.Instance),
		lastSnapshotRecords: make(map[string]*utils.InstanceFailureRecord),
		routeMapMutex:       sync.RWMutex{},
	}

	// No unavailable instances -> do not trigger time snapshot
	should, reason := m.shouldSnapshotFailuresNoLock()
	assert.False(t, should)
	assert.Equal(t, SnapshotReasonNoChanges, reason)

	// First set lastSnapshotRecords consistent with unavailableMap, ensure HasKeyDifference is false
	stats := CreateStats("inst01", "prefill", "10", 0, 1, 100, 50)
	m.unavailableMap["inst01"] = &client.Instance{
		ID: "inst01", Stats: stats,
	}
	m.lastSnapshotRecords["inst01"] = &utils.InstanceFailureRecord{
		Role:   "prefill",
		Status: "available",
	}

	// lastSnapshotTimestamp is time.Time{} (zero value), time difference is large, should trigger time snapshot
	should, reason = m.shouldSnapshotFailuresNoLock()
	assert.True(t, should)
	assert.Equal(t, SnapshotReasonTimeRenewed, reason)

	// Call again, time difference is small, should not trigger
	should, reason = m.shouldSnapshotFailuresNoLock()
	assert.False(t, should)
	assert.Equal(t, SnapshotReasonNoChanges, reason)

	// Key changes should trigger
	m.unavailableMap["inst02"] = &client.Instance{
		ID: "inst02", Stats: stats,
	}
	should, reason = m.shouldSnapshotFailuresNoLock()
	assert.True(t, should)
	assert.Equal(t, SnapshotReasonFailureChanged, reason)
}

// TestShouldSnapshotFailuresNoLock_DefaultInterval test default value when FailureSnapshotIntervalMs is 0
func TestShouldSnapshotFailuresNoLock_DefaultInterval(t *testing.T) {
	m := &InstanceManager{
		cfg: &config.InstanceManagerCfg{
			HealthCheckConfig: &config.HealthCheckConfig{
				FailureSnapshotIntervalMs: 0,
				RestHealthCommonConfig: config.RestHealthCommonConfig{
					Interval: 50,
				},
			},
		},
		unavailableMap:      make(map[string]*client.Instance),
		lastSnapshotRecords: make(map[string]*utils.InstanceFailureRecord),
		routeMapMutex:       sync.RWMutex{},
	}
	stats := CreateStats("inst01", "prefill", "10", 0, 1, 100, 50)
	m.unavailableMap["inst01"] = &client.Instance{
		ID: "inst01", Stats: stats,
	}
	m.lastSnapshotRecords["inst01"] = &utils.InstanceFailureRecord{
		Role:   "prefill",
		Status: "available",
	}

	should, reason := m.shouldSnapshotFailuresNoLock()
	assert.True(t, should)
	assert.Equal(t, SnapshotReasonTimeRenewed, reason)
}
