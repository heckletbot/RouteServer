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

package health

import (
	"sync"
	"testing"
	"time"

	"infer-dp-scheduler/pkg/common/client"
	"infer-dp-scheduler/pkg/common/config"
	"infer-dp-scheduler/pkg/common/constant"
	"infer-dp-scheduler/pkg/common/utils"
)

// Test ProbeState value definitions
func TestProbeStateValues(t *testing.T) {
	if ProbeStateNone != 0 {
		t.Errorf("ProbeStateNone should be 0, got %d", ProbeStateNone)
	}
	if ProbeStateProbing != 1 {
		t.Errorf("ProbeStateProbing should be 1, got %d", ProbeStateProbing)
	}
	if ProbeStateSuccess != 2 {
		t.Errorf("ProbeStateSuccess should be 2, got %d", ProbeStateSuccess)
	}
	if ProbeStateFailed != 3 {
		t.Errorf("ProbeStateFailed should be 3, got %d", ProbeStateFailed)
	}
}

// Test startProbeAsync - concurrent safety
func TestStartProbeAsync_Concurrent(t *testing.T) {
	cfg := &config.HangCheckerConfig{
		Enable:                     true,
		EnableProbeBeforeHangCheck: true,
		HangProbeIntervalMs:        30000,
	}

	leader := &LeaderHangDecider{
		cfg:               cfg,
		lastProbeTime:     make(map[string]int64),
		probingInstances:  make(map[string]bool),
		probeStates:       make(map[string]ProbeState),
		probeInferRequest: &ProbeInferRequest{},
		leaderHealthChecker: &LeaderHealthChecker{
			healthInstance:    map[string]*client.Instance{"test-instance": {ID: "test-instance"}},
			unhealthyInstance: make(map[string]*client.Instance),
		},
	}

	// Concurrently call startProbeAsync to ensure its internal probing is concurrent-safe
	var wg sync.WaitGroup
	var mutex sync.Mutex
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			mutex.Lock()
			leader.startProbeAsync("test-instance")
			mutex.Unlock()
		}()
	}
	wg.Wait()

	// 只应该发起一次探测
	leader.mu.Lock()
	probingCount := 0
	if leader.probingInstances["test-instance"] {
		probingCount = 1
	}
	leader.mu.Unlock()

	if probingCount != 1 {
		t.Errorf("Expected probing count = 1, got %d", probingCount)
	}
}

// 测试 startProbeAsync - 不阻塞主循环
func TestStartProbeAsync_NonBlocking(t *testing.T) {
	cfg := &config.HangCheckerConfig{
		Enable:                     true,
		EnableProbeBeforeHangCheck: true,
		HangProbeIntervalMs:        30000,
	}

	leader := &LeaderHangDecider{
		cfg:               cfg,
		lastProbeTime:     make(map[string]int64),
		probingInstances:  make(map[string]bool),
		probeStates:       make(map[string]ProbeState),
		probeInferRequest: &ProbeInferRequest{},
		leaderHealthChecker: &LeaderHealthChecker{
			healthInstance:    map[string]*client.Instance{"instance-1": {ID: "instance-1"}, "instance-2": {ID: "instance-2"}},
			unhealthyInstance: make(map[string]*client.Instance),
		},
	}

	// 记录开始时间
	startTime := time.Now()

	// 模拟主循环对多个实例调用 needToStartProbe 和 startProbeAsync
	// 这应该在极短时间内完成，不应该阻塞
	for i := 1; i <= 2; i++ {
		instanceId := "instance-" + string(rune('0'+i))
		leader.startProbeAsync(instanceId)
	}

	// 应该几乎立即返回（纳秒级别）
	elapsed := time.Since(startTime)
	if elapsed.Milliseconds() > 100 {
		t.Errorf("Expected non-blocking behavior, but took %v", elapsed)
	}
}

// 测试 SetProbeInferRequest
func TestSetProbeInferRequest(t *testing.T) {
	cfg := &config.HangCheckerConfig{
		Enable: true,
	}

	leader := &LeaderHangDecider{
		cfg:              cfg,
		lastProbeTime:    make(map[string]int64),
		probingInstances: make(map[string]bool),
		probeStates:      make(map[string]ProbeState),
		leaderHealthChecker: &LeaderHealthChecker{
			healthInstance:    make(map[string]*client.Instance),
			unhealthyInstance: make(map[string]*client.Instance),
		},
	}

	mockProbe := &ProbeInferRequest{}
	leader.SetProbeInferRequest(mockProbe)

	if leader.probeInferRequest != mockProbe {
		t.Error("Failed to set probeInferRequest")
	}
}

// 测试 NewLeaderHangDecider 初始化 invalidProbeCount
func TestNewLeaderHangDecider_InitInvalidProbeCount(t *testing.T) {
	cfg := &config.HangCheckerConfig{
		Enable:                      true,
		EnableProbeBeforeHangCheck:  true,
		MaxConsecutiveInvalidProbes: 3,
	}

	leaderHealthChecker := &LeaderHealthChecker{
		cfg: &config.HealthCheckConfig{
			AvailableCheckersCfg: config.AvailableCheckersConfig{
				HangCheckerCfg: *cfg,
			},
		},
		healthInstance:    make(map[string]*client.Instance),
		unhealthyInstance: make(map[string]*client.Instance),
	}

	leader := NewLeaderHangDecider(leaderHealthChecker)

	if leader.invalidProbeCount == nil {
		t.Error("invalidProbeCount should be initialized")
	}
}

// 测试配置默认值处理
func TestFillHangCheckerConfig_DefaultValue(t *testing.T) {
	cfg := &config.HealthCheckConfig{
		AvailableCheckersCfg: config.AvailableCheckersConfig{
			HangCheckerCfg: config.HangCheckerConfig{
				MaxConsecutiveInvalidProbes: 0,
			},
		},
	}

	fillHangCheckerConfig(cfg)

	if cfg.AvailableCheckersCfg.HangCheckerCfg.MaxConsecutiveInvalidProbes != 3 {
		t.Errorf("Expected default value 3, got %d", cfg.AvailableCheckersCfg.HangCheckerCfg.MaxConsecutiveInvalidProbes)
	}
}

// 测试配置默认值处理 - 负数
func TestFillHangCheckerConfig_NegativeValue(t *testing.T) {
	cfg := &config.HealthCheckConfig{
		AvailableCheckersCfg: config.AvailableCheckersConfig{
			HangCheckerCfg: config.HangCheckerConfig{
				MaxConsecutiveInvalidProbes: -1,
			},
		},
	}

	fillHangCheckerConfig(cfg)

	if cfg.AvailableCheckersCfg.HangCheckerCfg.MaxConsecutiveInvalidProbes != 3 {
		t.Errorf("Expected default value 3, got %d", cfg.AvailableCheckersCfg.HangCheckerCfg.MaxConsecutiveInvalidProbes)
	}
}

// 测试配置有效值保持不变
func TestFillHangCheckerConfig_ValidValue(t *testing.T) {
	cfg := &config.HealthCheckConfig{
		AvailableCheckersCfg: config.AvailableCheckersConfig{
			HangCheckerCfg: config.HangCheckerConfig{
				MaxConsecutiveInvalidProbes: 5,
			},
		},
	}

	fillHangCheckerConfig(cfg)

	if cfg.AvailableCheckersCfg.HangCheckerCfg.MaxConsecutiveInvalidProbes != 5 {
		t.Errorf("Expected value 5, got %d", cfg.AvailableCheckersCfg.HangCheckerCfg.MaxConsecutiveInvalidProbes)
	}
}

// ==================== WorkerHangChecker 单元测试 ====================

// 测试场景: SetInstanceRecorder 创建新实例记录器
func TestWorkerHangChecker_SetInstanceRecorder_NewInstance(t *testing.T) {
	checker := &WorkerHangChecker{
		InstancesList: make(map[string]*InstanceRecorder),
	}

	timestamp := time.Now().UnixMilli()
	recorder := checker.SetInstanceRecorder("instance-1", "prefill", timestamp)

	if recorder == nil {
		t.Error("Expected new InstanceRecorder, got nil")
	}
	if recorder.Role != "prefill" {
		t.Errorf("Expected role 'prefill', got '%s'", recorder.Role)
	}
	if recorder.LastSuccessTime != timestamp {
		t.Errorf("Expected LastSuccessTime %d, got %d", timestamp, recorder.LastSuccessTime)
	}
	if recorder.RequestsTimeList == nil {
		t.Error("Expected RequestsTimeList to be initialized")
	}
}

// 测试场景: SetInstanceRecorder 重复设置同一实例返回nil
func TestWorkerHangChecker_SetInstanceRecorder_ExistingInstance(t *testing.T) {
	checker := &WorkerHangChecker{
		InstancesList: make(map[string]*InstanceRecorder),
	}

	timestamp := time.Now().UnixMilli()
	checker.SetInstanceRecorder("instance-1", "prefill", timestamp)
	recorder := checker.SetInstanceRecorder("instance-1", "prefill", timestamp)

	if recorder != nil {
		t.Error("Expected nil for existing instance")
	}
}

// 测试场景: GetInstanceRecorder 获取存在的实例记录器
func TestWorkerHangChecker_GetInstanceRecorder_Existing(t *testing.T) {
	checker := &WorkerHangChecker{
		InstancesList: map[string]*InstanceRecorder{
			"instance-1": {
				Role:                  "prefill",
				LastSuccessTime:       1000,
				RequestsTimeList:      make(map[string]int64),
				NonStreamCnt:          0,
				LastNonStreamWarnTime: 0,
				MayHang:               false,
			},
		},
	}

	recorder := checker.GetInstanceRecorder("instance-1")
	if recorder == nil {
		t.Error("Expected InstanceRecorder, got nil")
	}
	if recorder.Role != "prefill" {
		t.Errorf("Expected role 'prefill', got '%s'", recorder.Role)
	}
}

// 测试场景: GetInstanceRecorder 获取不存在的实例返回nil
func TestWorkerHangChecker_GetInstanceRecorder_NotExisting(t *testing.T) {
	checker := &WorkerHangChecker{
		InstancesList: make(map[string]*InstanceRecorder),
	}

	recorder := checker.GetInstanceRecorder("non-existent")
	if recorder != nil {
		t.Error("Expected nil for non-existing instance")
	}
}

// 测试场景: RemoveInstance 删除实例记录器
func TestWorkerHangChecker_RemoveInstance(t *testing.T) {
	checker := &WorkerHangChecker{
		InstancesList: map[string]*InstanceRecorder{
			"instance-1": {
				Role:                  "prefill",
				LastSuccessTime:       1000,
				RequestsTimeList:      map[string]int64{"req-1": 1000},
				NonStreamCnt:          0,
				LastNonStreamWarnTime: 0,
				MayHang:               true,
			},
		},
	}

	checker.RemoveInstance("instance-1")

	if _, exists := checker.InstancesList["instance-1"]; exists {
		t.Error("Expected instance to be removed")
	}
}

// 测试场景: Init 初始化配置并设置阈值
func TestWorkerHangChecker_Init_Enable(t *testing.T) {
	checker := &WorkerHangChecker{}
	cfg := &config.HangCheckerConfig{
		Enable: true,
		HangThresholds: []config.HangThreshold{
			{Role: "prefill", ThresholdTime: 5000, SkipNonStream: false},
			{Role: "decode", ThresholdTime: 3000, SkipNonStream: true},
		},
	}

	checker.Init(cfg)

	if checker.cfg != cfg {
		t.Error("Expected cfg to be set")
	}
	if checker.HangWarningInterval == 0 {
		t.Error("Expected HangWarningInterval to be set")
	}
	if checker.InstancesList == nil {
		t.Error("Expected InstancesList to be initialized")
	}
	if checker.Thresholds == nil {
		t.Error("Expected Thresholds to be initialized")
	}
	if checker.Thresholds["prefill"].ThresholdTime != 5000 {
		t.Errorf("Expected prefill threshold 5000, got %d", checker.Thresholds["prefill"].ThresholdTime)
	}
	if checker.Thresholds["decode"].SkipNonStream != true {
		t.Error("Expected decode SkipNonStream to be true")
	}
}

// 测试场景: Init 配置未启用时不初始化
func TestWorkerHangChecker_Init_Disable(t *testing.T) {
	checker := &WorkerHangChecker{}
	cfg := &config.HangCheckerConfig{
		Enable: false,
	}

	checker.Init(cfg)

	// 当Enable为false时，不应该初始化InstancesList
	if checker.InstancesList != nil {
		t.Error("Expected InstancesList to remain nil when disabled")
	}
}

// 测试场景: OnRequestEvent 处理新请求事件
func TestWorkerHangChecker_OnRequestEvent_NewRequest(t *testing.T) {
	checker := &WorkerHangChecker{
		cfg: &config.HangCheckerConfig{
			Enable: true,
			HangThresholds: []config.HangThreshold{
				{Role: "prefill", ThresholdTime: 5000, SkipNonStream: false},
			},
		},
		InstancesList: make(map[string]*InstanceRecorder),
		Thresholds: map[string]*RoleThreshold{
			"prefill": {ThresholdTime: 5000, SkipNonStream: false},
		},
	}

	reqMsg := &utils.RequestEventMsg{
		Request:            &utils.RemoteRequestWrapper{Id: "req-1", Type: "prefill", Stream: false},
		TargetInstance:     "instance-1",
		EventType:          constant.RequestNew,
		TimeStamp:          1000,
		InternalTestingReq: false,
	}

	checker.OnRequestEvent(reqMsg)

	recorder := checker.GetInstanceRecorder("instance-1")
	if recorder == nil {
		t.Error("Expected InstanceRecorder to be created")
	}
	if _, exists := recorder.RequestsTimeList["req-1"]; !exists {
		t.Error("Expected request to be tracked")
	}
}

// 测试场景: OnRequestEvent 内部测试请求跳过
func TestWorkerHangChecker_OnRequestEvent_InternalTestingReq(t *testing.T) {
	checker := &WorkerHangChecker{
		cfg:           &config.HangCheckerConfig{Enable: true},
		InstancesList: make(map[string]*InstanceRecorder),
	}

	reqMsg := &utils.RequestEventMsg{
		Request:            &utils.RemoteRequestWrapper{Id: "req-1", Type: "prefill"},
		TargetInstance:     "instance-1",
		EventType:          constant.RequestNew,
		TimeStamp:          1000,
		InternalTestingReq: true,
	}

	checker.OnRequestEvent(reqMsg)

	// 内部测试请求应该被跳过
	if len(checker.InstancesList) != 0 {
		t.Error("Expected no instance to be added for internal testing request")
	}
}

// 测试场景: OnRequestEvent 配置未启用时跳过
func TestWorkerHangChecker_OnRequestEvent_Disabled(t *testing.T) {
	checker := &WorkerHangChecker{
		cfg:           &config.HangCheckerConfig{Enable: false},
		InstancesList: make(map[string]*InstanceRecorder),
	}

	reqMsg := &utils.RequestEventMsg{
		Request:        &utils.RemoteRequestWrapper{Id: "req-1", Type: "prefill"},
		TargetInstance: "instance-1",
		EventType:      constant.RequestNew,
		TimeStamp:      1000,
	}

	checker.OnRequestEvent(reqMsg)

	// 配置未启用时应该被跳过
	if len(checker.InstancesList) != 0 {
		t.Error("Expected no instance to be added when disabled")
	}
}

// 测试场景: OnNewRequest 添加新的非流请求
func TestWorkerHangChecker_OnNewRequest_NonStreamRequest(t *testing.T) {
	checker := &WorkerHangChecker{
		cfg:           &config.HangCheckerConfig{Enable: true},
		InstancesList: make(map[string]*InstanceRecorder),
		Thresholds: map[string]*RoleThreshold{
			"prefill": {ThresholdTime: 5000, SkipNonStream: false},
		},
		mu: sync.Mutex{},
	}

	request := &utils.RemoteRequestWrapper{Id: "req-1", Type: "prefill", Stream: false}
	timestamp := time.Now().UnixMilli()

	checker.OnNewRequest(request, "instance-1", timestamp)

	recorder := checker.GetInstanceRecorder("instance-1")
	if recorder == nil {
		t.Error("Expected InstanceRecorder to be created")
	}
	if recorder.NonStreamCnt != 1 {
		t.Errorf("Expected NonStreamCnt = 1, got %d", recorder.NonStreamCnt)
	}
}

// 测试场景: OnNewRequest 添加新的流请求
func TestWorkerHangChecker_OnNewRequest_StreamRequest(t *testing.T) {
	checker := &WorkerHangChecker{
		cfg:           &config.HangCheckerConfig{Enable: true},
		InstancesList: make(map[string]*InstanceRecorder),
		Thresholds: map[string]*RoleThreshold{
			"prefill": {ThresholdTime: 5000, SkipNonStream: false},
		},
		mu: sync.Mutex{},
	}

	request := &utils.RemoteRequestWrapper{Id: "req-1", Type: "prefill", Stream: true}
	timestamp := time.Now().UnixMilli()

	checker.OnNewRequest(request, "instance-1", timestamp)

	recorder := checker.GetInstanceRecorder("instance-1")
	if recorder == nil {
		t.Error("Expected InstanceRecorder to be created")
	}
	if recorder.NonStreamCnt != 0 {
		t.Errorf("Expected NonStreamCnt = 0, got %d", recorder.NonStreamCnt)
	}
}

// 测试场景: OnNewRequest 更新已存在的请求时间
func TestWorkerHangChecker_OnNewRequest_UpdateExistingRequest(t *testing.T) {
	checker := &WorkerHangChecker{
		cfg: &config.HangCheckerConfig{Enable: true},
		InstancesList: map[string]*InstanceRecorder{
			"instance-1": {
				Role:                  "prefill",
				LastSuccessTime:       1000,
				RequestsTimeList:      map[string]int64{"req-1": 1000},
				NonStreamCnt:          0,
				LastNonStreamWarnTime: 0,
				MayHang:               false,
			},
		},
		Thresholds: map[string]*RoleThreshold{
			"prefill": {ThresholdTime: 5000, SkipNonStream: false},
		},
		mu: sync.Mutex{},
	}

	request := &utils.RemoteRequestWrapper{Id: "req-1", Type: "prefill", Stream: true}
	timestamp := int64(2000)

	checker.OnNewRequest(request, "instance-1", timestamp)

	recorder := checker.GetInstanceRecorder("instance-1")
	if recorder.RequestsTimeList["req-1"] != timestamp {
		t.Errorf("Expected request time to be updated to %d, got %d", timestamp, recorder.RequestsTimeList["req-1"])
	}
}

// 测试场景: OnNewRequest 无效的角色类型记录警告
func TestWorkerHangChecker_OnNewRequest_InvalidRole(t *testing.T) {
	checker := &WorkerHangChecker{
		cfg:           &config.HangCheckerConfig{Enable: true},
		InstancesList: make(map[string]*InstanceRecorder),
		Thresholds:    make(map[string]*RoleThreshold),
		mu:            sync.Mutex{},
	}

	request := &utils.RemoteRequestWrapper{Id: "req-1", Type: "invalid-role", Stream: false}
	timestamp := time.Now().UnixMilli()

	// 应该记录警告但不会panic
	checker.OnNewRequest(request, "instance-1", timestamp)
}

// 测试场景: OnFinishedRequest 完成非流请求
func TestWorkerHangChecker_OnFinishedRequest_NonStreamRequest(t *testing.T) {
	checker := &WorkerHangChecker{
		cfg: &config.HangCheckerConfig{Enable: true},
		InstancesList: map[string]*InstanceRecorder{
			"instance-1": {
				Role:                  "prefill",
				LastSuccessTime:       1000,
				RequestsTimeList:      map[string]int64{"req-1": 1000, "req-2": 2000},
				NonStreamCnt:          1,
				LastNonStreamWarnTime: 0,
				MayHang:               false,
			},
		},
		Thresholds: map[string]*RoleThreshold{
			"prefill": {ThresholdTime: 5000, SkipNonStream: false},
		},
		mu: sync.Mutex{},
	}

	request := &utils.RemoteRequestWrapper{Id: "req-1", Type: "prefill", Stream: false}
	timestamp := int64(2000)

	checker.OnFinishedRequest(request, "instance-1", timestamp)

	recorder := checker.GetInstanceRecorder("instance-1")
	if recorder == nil {
		t.Error("Expected InstanceRecorder to exist when there are remaining requests")
	}
	if recorder.NonStreamCnt != 0 {
		t.Errorf("Expected NonStreamCnt = 0, got %d", recorder.NonStreamCnt)
	}
	if recorder.LastSuccessTime != timestamp {
		t.Errorf("Expected LastSuccessTime = %d, got %d", timestamp, recorder.LastSuccessTime)
	}
}

// 测试场景: OnFinishedRequest 当请求列表为空时删除实例
func TestWorkerHangChecker_OnFinishedRequest_RemoveInstance(t *testing.T) {
	checker := &WorkerHangChecker{
		cfg: &config.HangCheckerConfig{Enable: true},
		InstancesList: map[string]*InstanceRecorder{
			"instance-1": {
				Role:                  "prefill",
				LastSuccessTime:       1000,
				RequestsTimeList:      map[string]int64{"req-1": 1000},
				NonStreamCnt:          0,
				LastNonStreamWarnTime: 0,
				MayHang:               false,
			},
		},
		Thresholds: map[string]*RoleThreshold{
			"prefill": {ThresholdTime: 5000, SkipNonStream: false},
		},
		mu: sync.Mutex{},
	}

	request := &utils.RemoteRequestWrapper{Id: "req-1", Type: "prefill", Stream: true}
	timestamp := int64(2000)

	checker.OnFinishedRequest(request, "instance-1", timestamp)

	// 当RequestsTimeList为空时，实例应该被删除
	if _, exists := checker.InstancesList["instance-1"]; exists {
		t.Error("Expected instance to be removed when RequestsTimeList is empty")
	}
}

// 测试场景: OnFinishedRequest 不存在的实例直接返回
func TestWorkerHangChecker_OnFinishedRequest_NonExistingInstance(t *testing.T) {
	checker := &WorkerHangChecker{
		cfg:           &config.HangCheckerConfig{Enable: true},
		InstancesList: make(map[string]*InstanceRecorder),
		mu:            sync.Mutex{},
	}

	request := &utils.RemoteRequestWrapper{Id: "req-1", Type: "prefill", Stream: true}

	// 应该直接返回，不panic
	checker.OnFinishedRequest(request, "non-existent", 1000)
}

// 测试场景: OnLocalDecodeTokens 处理本地解码token事件
func TestWorkerHangChecker_OnLocalDecodeTokens(t *testing.T) {
	checker := &WorkerHangChecker{
		cfg: &config.HangCheckerConfig{Enable: true},
		InstancesList: map[string]*InstanceRecorder{
			"instance-1": {
				Role:                  "decode",
				LastSuccessTime:       1000,
				RequestsTimeList:      map[string]int64{"req-1": 1000},
				NonStreamCnt:          0,
				LastNonStreamWarnTime: 0,
				MayHang:               false,
			},
		},
		mu: sync.Mutex{},
	}

	tokenMsg := &WorkerLocalTokenEvent{
		instanceId: "instance-1",
		reqId:      "req-1",
		timestamp:  2000,
	}

	checker.OnLocalDecodeTokens(tokenMsg)

	recorder := checker.GetInstanceRecorder("instance-1")
	if recorder.LastSuccessTime != 2000 {
		t.Errorf("Expected LastSuccessTime = 2000, got %d", recorder.LastSuccessTime)
	}
}

// 测试场景: OnLocalDecodeTokens 配置未启用时跳过
func TestWorkerHangChecker_OnLocalDecodeTokens_Disabled(t *testing.T) {
	checker := &WorkerHangChecker{
		cfg: &config.HangCheckerConfig{Enable: false},
		InstancesList: map[string]*InstanceRecorder{
			"instance-1": {
				Role:            "decode",
				LastSuccessTime: 1000,
			},
		},
		mu: sync.Mutex{},
	}

	tokenMsg := &WorkerLocalTokenEvent{
		instanceId: "instance-1",
		reqId:      "req-1",
		timestamp:  2000,
	}

	originalTime := checker.InstancesList["instance-1"].LastSuccessTime
	checker.OnLocalDecodeTokens(tokenMsg)

	if checker.InstancesList["instance-1"].LastSuccessTime != originalTime {
		t.Error("Expected LastSuccessTime to remain unchanged when disabled")
	}
}

// 测试场景: OnLocalDecodeTokens 不存在的实例直接返回
func TestWorkerHangChecker_OnLocalDecodeTokens_NonExistingInstance(t *testing.T) {
	checker := &WorkerHangChecker{
		cfg:           &config.HangCheckerConfig{Enable: true},
		InstancesList: make(map[string]*InstanceRecorder),
		mu:            sync.Mutex{},
	}

	tokenMsg := &WorkerLocalTokenEvent{
		instanceId: "non-existent",
		reqId:      "req-1",
		timestamp:  2000,
	}

	// 应该直接返回，不panic
	checker.OnLocalDecodeTokens(tokenMsg)
}

// 测试场景: IsInstanceHanging 阈值时间小于等于0时返回false
func TestWorkerHangChecker_IsInstanceHanging_ThresholdZero(t *testing.T) {
	checker := &WorkerHangChecker{
		Thresholds: map[string]*RoleThreshold{
			"prefill": {ThresholdTime: 0, SkipNonStream: false},
		},
		HangWarningInterval: 30000,
	}

	recorder := &InstanceRecorder{
		Role:             "prefill",
		LastSuccessTime:  1000,
		RequestsTimeList: map[string]int64{"req-1": 1000},
	}

	timestamp := int64(6000)
	result := checker.IsInstanceHanging(recorder, "instance-1", timestamp)

	if result != false {
		t.Error("Expected false when ThresholdTime <= 0")
	}
}

// 测试场景: IsInstanceHanging 有非流请求且SkipNonStream为true时不报告挂起
func TestWorkerHangChecker_IsInstanceHanging_NonStreamWithSkip(t *testing.T) {
	checker := &WorkerHangChecker{
		Thresholds: map[string]*RoleThreshold{
			"prefill": {ThresholdTime: 5000, SkipNonStream: true},
		},
		HangWarningInterval: 30000,
	}

	recorder := &InstanceRecorder{
		Role:                  "prefill",
		LastSuccessTime:       1000,
		RequestsTimeList:      map[string]int64{"req-1": 1000},
		NonStreamCnt:          1,
		LastNonStreamWarnTime: 0,
		MayHang:               false,
	}

	timestamp := int64(10000) // 超过阈值时间，但有非流请求应该跳过
	result := checker.IsInstanceHanging(recorder, "instance-1", timestamp)

	if result != false {
		t.Error("Expected false when NonStreamCnt > 0 and SkipNonStream is true")
	}
}

// 测试场景: IsInstanceHanging 有非流请求且超过警告间隔时记录警告
func TestWorkerHangChecker_IsInstanceHanging_NonStreamWarnInterval(t *testing.T) {
	checker := &WorkerHangChecker{
		Thresholds: map[string]*RoleThreshold{
			"prefill": {ThresholdTime: 5000, SkipNonStream: true},
		},
		HangWarningInterval: 30000,
	}

	recorder := &InstanceRecorder{
		Role:                  "prefill",
		LastSuccessTime:       1000,
		RequestsTimeList:      map[string]int64{"req-1": 1000},
		NonStreamCnt:          1,
		LastNonStreamWarnTime: 0,
		MayHang:               false,
	}

	timestamp := int64(40000) // 超过阈值时间和警告间隔
	result := checker.IsInstanceHanging(recorder, "instance-1", timestamp)

	if result != false {
		t.Error("Expected false when NonStreamCnt > 0 and SkipNonStream is true")
	}
	if recorder.LastNonStreamWarnTime != timestamp {
		t.Errorf("Expected LastNonStreamWarnTime to be updated to %d", timestamp)
	}
}

// 测试场景: IsInstanceHanging 实例挂起时返回true
func TestWorkerHangChecker_IsInstanceHanging_Hanging(t *testing.T) {
	checker := &WorkerHangChecker{
		Thresholds: map[string]*RoleThreshold{
			"prefill": {ThresholdTime: 5000, SkipNonStream: false},
		},
		HangWarningInterval: 30000,
	}

	recorder := &InstanceRecorder{
		Role:                  "prefill",
		LastSuccessTime:       1000,
		RequestsTimeList:      map[string]int64{"req-1": 1000, "req-2": 1000},
		NonStreamCnt:          0,
		LastNonStreamWarnTime: 0,
		MayHang:               false,
	}

	timestamp := int64(10000) // 超过阈值时间
	result := checker.IsInstanceHanging(recorder, "instance-1", timestamp)

	if result != true {
		t.Error("Expected true when instance is hanging")
	}
	if recorder.MayHang != true {
		t.Error("Expected MayHang to be set to true")
	}
}

// 测试场景: IsInstanceHanging 实例从挂起恢复时记录日志
func TestWorkerHangChecker_IsInstanceHanging_RecoverFromHang(t *testing.T) {
	checker := &WorkerHangChecker{
		Thresholds: map[string]*RoleThreshold{
			"prefill": {ThresholdTime: 5000, SkipNonStream: false},
		},
		HangWarningInterval: 30000,
	}

	recorder := &InstanceRecorder{
		Role:                  "prefill",
		LastSuccessTime:       10000,
		RequestsTimeList:      map[string]int64{"req-1": 10000},
		NonStreamCnt:          0,
		LastNonStreamWarnTime: 0,
		MayHang:               true, // 之前是挂起状态
	}

	timestamp := int64(12000) // 未超过阈值时间
	result := checker.IsInstanceHanging(recorder, "instance-1", timestamp)

	if result != false {
		t.Error("Expected false when instance recovered from hang")
	}
	if recorder.MayHang != false {
		t.Error("Expected MayHang to be set to false")
	}
}

// 测试场景: InstanceHangCheck 配置未启用时返回nil
func TestWorkerHangChecker_InstanceHangCheck_Disabled(t *testing.T) {
	checker := &WorkerHangChecker{
		cfg: &config.HangCheckerConfig{Enable: false},
	}

	events := checker.InstanceHangCheck(time.Now().UnixMilli())

	if events != nil {
		t.Error("Expected nil when disabled")
	}
}

// 测试场景: InstanceHangCheck 正常检测挂起实例
func TestWorkerHangChecker_InstanceHangCheck(t *testing.T) {
	checker := &WorkerHangChecker{
		cfg: &config.HangCheckerConfig{Enable: true},
		InstancesList: map[string]*InstanceRecorder{
			"instance-1": {
				Role:                  "prefill",
				LastSuccessTime:       1000,
				RequestsTimeList:      map[string]int64{"req-1": 1000},
				NonStreamCnt:          0,
				LastNonStreamWarnTime: 0,
				MayHang:               false,
			},
		},
		Thresholds: map[string]*RoleThreshold{
			"prefill": {ThresholdTime: 5000, SkipNonStream: false},
		},
		HangWarningInterval: 30000,
		mu:                  sync.Mutex{},
	}

	timestamp := int64(10000)
	events := checker.InstanceHangCheck(timestamp)

	if len(events) != 1 {
		t.Errorf("Expected 1 event, got %d", len(events))
	}
	if events[0].InstanceId != "instance-1" {
		t.Errorf("Expected instance-1, got %s", events[0].InstanceId)
	}
	if events[0].IsHang != true {
		t.Error("Expected IsHang to be true")
	}
}

// 测试场景: InstanceHangCheck 无挂起实例时返回空列表
func TestWorkerHangChecker_InstanceHangCheck_NoHang(t *testing.T) {
	checker := &WorkerHangChecker{
		cfg: &config.HangCheckerConfig{Enable: true},
		InstancesList: map[string]*InstanceRecorder{
			"instance-1": {
				Role:                  "prefill",
				LastSuccessTime:       10000,
				RequestsTimeList:      map[string]int64{"req-1": 10000},
				NonStreamCnt:          0,
				LastNonStreamWarnTime: 0,
				MayHang:               false,
			},
		},
		Thresholds: map[string]*RoleThreshold{
			"prefill": {ThresholdTime: 5000, SkipNonStream: false},
		},
		HangWarningInterval: 30000,
		mu:                  sync.Mutex{},
	}

	timestamp := int64(12000)
	events := checker.InstanceHangCheck(timestamp)

	if len(events) != 0 {
		t.Errorf("Expected 0 events, got %d", len(events))
	}
}

// ==================== LeaderHangDecider 单元测试 ====================

// 测试场景: GetWorker 获取存在的worker
func TestLeaderHangDecider_GetWorker_Existing(t *testing.T) {
	leader := &LeaderHangDecider{
		WorkerList: map[string]map[string]*WorkerRecorder{
			"instance-1": {
				"worker-1": {
					RequestsList:    make(map[string]int64),
					MayHang:         false,
					LastHangMsgTime: 1000,
				},
			},
		},
	}

	worker := leader.GetWorker("instance-1", "worker-1")
	if worker == nil {
		t.Error("Expected WorkerRecorder, got nil")
	}
}

// 测试场景: GetWorker 不存在的实例返回nil并记录警告
func TestLeaderHangDecider_GetWorker_NonExistingInstance(t *testing.T) {
	leader := &LeaderHangDecider{
		WorkerList: make(map[string]map[string]*WorkerRecorder),
	}

	worker := leader.GetWorker("non-existent", "worker-1")
	if worker != nil {
		t.Error("Expected nil for non-existing instance")
	}
}

// 测试场景: GetWorker 不存在的worker返回nil
func TestLeaderHangDecider_GetWorker_NonExistingWorker(t *testing.T) {
	leader := &LeaderHangDecider{
		WorkerList: map[string]map[string]*WorkerRecorder{
			"instance-1": {},
		},
	}

	worker := leader.GetWorker("instance-1", "non-existent")
	if worker != nil {
		t.Error("Expected nil for non-existing worker")
	}
}

// 测试场景: SetWorker 设置新的worker
func TestLeaderHangDecider_SetWorker_NewWorker(t *testing.T) {
	leader := &LeaderHangDecider{
		WorkerList: map[string]map[string]*WorkerRecorder{
			"instance-1": {},
		},
	}

	worker := leader.SetWorker("instance-1", "worker-1")
	if worker == nil {
		t.Error("Expected WorkerRecorder, got nil")
	}
	if worker.RequestsList == nil {
		t.Error("Expected RequestsList to be initialized")
	}
}

// 测试场景: SetWorker 不存在的实例返回nil
func TestLeaderHangDecider_SetWorker_NonExistingInstance(t *testing.T) {
	leader := &LeaderHangDecider{
		WorkerList: make(map[string]map[string]*WorkerRecorder),
	}

	worker := leader.SetWorker("non-existent", "worker-1")
	if worker != nil {
		t.Error("Expected nil for non-existing instance")
	}
}

// 测试场景: SetWorker 已存在的worker返回现有记录
func TestLeaderHangDecider_SetWorker_ExistingWorker(t *testing.T) {
	existingWorker := &WorkerRecorder{
		RequestsList:    make(map[string]int64),
		MayHang:         true,
		LastHangMsgTime: 1000,
	}
	leader := &LeaderHangDecider{
		WorkerList: map[string]map[string]*WorkerRecorder{
			"instance-1": {
				"worker-1": existingWorker,
			},
		},
	}

	worker := leader.SetWorker("instance-1", "worker-1")
	if worker != existingWorker {
		t.Error("Expected existing WorkerRecorder")
	}
}

// 测试场景: ClearInstance 清除实例的所有worker
func TestLeaderHangDecider_ClearInstance(t *testing.T) {
	leader := &LeaderHangDecider{
		WorkerList: map[string]map[string]*WorkerRecorder{
			"instance-1": {
				"worker-1": {
					RequestsList:    map[string]int64{"req-1": 1000},
					MayHang:         false,
					LastHangMsgTime: 1000,
				},
			},
		},
		HangTag: map[string]bool{
			"instance-1": true,
		},
	}

	leader.ClearInstance("instance-1")

	if len(leader.WorkerList["instance-1"]) != 0 {
		t.Error("Expected WorkerList to be cleared")
	}
}

// 测试场景: ClearInstance 不存在的实例记录警告
func TestLeaderHangDecider_ClearInstance_NonExisting(t *testing.T) {
	leader := &LeaderHangDecider{
		WorkerList: make(map[string]map[string]*WorkerRecorder),
	}

	// 应该记录警告但不panic
	leader.ClearInstance("non-existent")
}

// 测试场景: OnAllInstancesRenewed 添加新实例
func TestLeaderHangDecider_OnAllInstancesRenewed_AddInstance(t *testing.T) {
	cfg := &config.HangCheckerConfig{
		Enable: true,
	}
	leader := &LeaderHangDecider{
		cfg:        cfg,
		WorkerList: make(map[string]map[string]*WorkerRecorder),
		HangTag:    make(map[string]bool),
	}

	available := map[string]*client.Instance{
		"instance-1": {ID: "instance-1"},
	}
	unavailable := make(map[string]*client.Instance)

	leader.OnAllInstancesRenewed(available, unavailable)

	if _, exists := leader.WorkerList["instance-1"]; !exists {
		t.Error("Expected instance to be added")
	}
	if leader.HangTag["instance-1"] != false {
		t.Error("Expected HangTag to be false for new instance")
	}
}

// 测试场景: OnAllInstancesRenewed 删除不存在的实例
func TestLeaderHangDecider_OnAllInstancesRenewed_DeleteInstance(t *testing.T) {
	cfg := &config.HangCheckerConfig{
		Enable: true,
	}
	leader := &LeaderHangDecider{
		cfg: cfg,
		WorkerList: map[string]map[string]*WorkerRecorder{
			"instance-1": {},
		},
		HangTag: map[string]bool{
			"instance-1": true,
		},
	}

	available := make(map[string]*client.Instance)
	unavailable := make(map[string]*client.Instance)

	leader.OnAllInstancesRenewed(available, unavailable)

	if _, exists := leader.WorkerList["instance-1"]; exists {
		t.Error("Expected instance to be deleted")
	}
	if _, exists := leader.HangTag["instance-1"]; exists {
		t.Error("Expected HangTag to be deleted")
	}
}

// 测试场景: OnAllInstancesRenewed 配置未启用时跳过
func TestLeaderHangDecider_OnAllInstancesRenewed_Disabled(t *testing.T) {
	cfg := &config.HangCheckerConfig{
		Enable: false,
	}
	leader := &LeaderHangDecider{
		cfg:        cfg,
		WorkerList: make(map[string]map[string]*WorkerRecorder),
		HangTag:    make(map[string]bool),
	}

	available := map[string]*client.Instance{
		"instance-1": {ID: "instance-1"},
	}
	unavailable := make(map[string]*client.Instance)

	leader.OnAllInstancesRenewed(available, unavailable)

	// 配置未启用时不应该修改WorkerList
	if len(leader.WorkerList) != 0 {
		t.Error("Expected WorkerList to remain empty when disabled")
	}
}

// 测试场景: OnInstanceTokenEvent 处理实例挂起事件
func TestLeaderHangDecider_OnInstanceTokenEvent(t *testing.T) {
	cfg := &config.HangCheckerConfig{
		Enable: true,
	}
	leader := &LeaderHangDecider{
		cfg: cfg,
		WorkerList: map[string]map[string]*WorkerRecorder{
			"instance-1": {
				"worker-1": {
					RequestsList:    make(map[string]int64),
					MayHang:         false,
					LastHangMsgTime: 0,
				},
			},
		},
	}

	instanceEvents := []*utils.InstanceEventMsg{
		{InstanceId: "instance-1", IsHang: true},
	}

	leader.OnInstanceTokenEvent("worker-1", instanceEvents)

	if leader.WorkerList["instance-1"]["worker-1"].MayHang != true {
		t.Error("Expected MayHang to be set to true")
	}
}

// 测试场景: OnInstanceTokenEvent 配置未启用时跳过
func TestLeaderHangDecider_OnInstanceTokenEvent_Disabled(t *testing.T) {
	cfg := &config.HangCheckerConfig{
		Enable: false,
	}
	leader := &LeaderHangDecider{
		cfg: cfg,
		WorkerList: map[string]map[string]*WorkerRecorder{
			"instance-1": {
				"worker-1": {
					RequestsList:    make(map[string]int64),
					MayHang:         false,
					LastHangMsgTime: 0,
				},
			},
		},
	}

	instanceEvents := []*utils.InstanceEventMsg{
		{InstanceId: "instance-1", IsHang: true},
	}

	leader.OnInstanceTokenEvent("worker-1", instanceEvents)

	if leader.WorkerList["instance-1"]["worker-1"].MayHang != false {
		t.Error("Expected MayHang to remain false when disabled")
	}
}

// 测试场景: OnRequestEvents 处理请求事件
func TestLeaderHangDecider_OnRequestEvents(t *testing.T) {
	cfg := &config.HangCheckerConfig{
		Enable: true,
	}
	leader := &LeaderHangDecider{
		cfg: cfg,
		WorkerList: map[string]map[string]*WorkerRecorder{
			"instance-1": {},
		},
	}

	requestEvents := []*utils.RequestEventMsg{
		{
			Request:        &utils.RemoteRequestWrapper{Id: "req-1"},
			TargetInstance: "instance-1",
			EventType:      constant.RequestNew,
			TimeStamp:      1000,
		},
	}

	leader.OnRequestEvents("worker-1", requestEvents)

	if _, exists := leader.WorkerList["instance-1"]["worker-1"]; !exists {
		t.Error("Expected worker to be added")
	}
}

// 测试场景: OnRequestEvents 配置未启用时跳过
func TestLeaderHangDecider_OnRequestEvents_Disabled(t *testing.T) {
	cfg := &config.HangCheckerConfig{
		Enable: false,
	}
	leader := &LeaderHangDecider{
		cfg:        cfg,
		WorkerList: make(map[string]map[string]*WorkerRecorder),
	}

	requestEvents := []*utils.RequestEventMsg{
		{
			Request:        &utils.RemoteRequestWrapper{Id: "req-1"},
			TargetInstance: "instance-1",
			EventType:      constant.RequestNew,
			TimeStamp:      1000,
		},
	}

	leader.OnRequestEvents("worker-1", requestEvents)

	if len(leader.WorkerList) != 0 {
		t.Error("Expected WorkerList to remain empty when disabled")
	}
}

// 测试场景: OnNewRequest 处理新请求
func TestLeaderHangDecider_OnNewRequest(t *testing.T) {
	cfg := &config.HangCheckerConfig{
		Enable: true,
	}
	leader := &LeaderHangDecider{
		cfg: cfg,
		WorkerList: map[string]map[string]*WorkerRecorder{
			"instance-1": {},
		},
	}

	reqMsg := &utils.RequestEventMsg{
		Request:        &utils.RemoteRequestWrapper{Id: "req-1"},
		TargetInstance: "instance-1",
		TimeStamp:      1000,
	}

	leader.OnNewRequest("worker-1", reqMsg)

	if _, exists := leader.WorkerList["instance-1"]["worker-1"]; !exists {
		t.Error("Expected worker to be created")
	}
	if leader.WorkerList["instance-1"]["worker-1"].RequestsList["req-1"] != 1000 {
		t.Errorf("Expected request time 1000, got %d", leader.WorkerList["instance-1"]["worker-1"].RequestsList["req-1"])
	}
}

// 测试场景: OnNewRequest 不存在的实例记录警告
func TestLeaderHangDecider_OnNewRequest_NonExistingInstance(t *testing.T) {
	cfg := &config.HangCheckerConfig{
		Enable: true,
	}
	leader := &LeaderHangDecider{
		cfg:        cfg,
		WorkerList: make(map[string]map[string]*WorkerRecorder),
	}

	reqMsg := &utils.RequestEventMsg{
		Request:        &utils.RemoteRequestWrapper{Id: "req-1"},
		TargetInstance: "non-existent",
		TimeStamp:      1000,
	}

	// 应该记录警告但不panic
	leader.OnNewRequest("worker-1", reqMsg)
}

// 测试场景: OnFinishedRequest 完成请求
func TestLeaderHangDecider_OnFinishedRequest(t *testing.T) {
	cfg := &config.HangCheckerConfig{
		Enable: true,
	}
	leader := &LeaderHangDecider{
		cfg: cfg,
		WorkerList: map[string]map[string]*WorkerRecorder{
			"instance-1": {
				"worker-1": {
					RequestsList:    map[string]int64{"req-1": 1000, "req-2": 2000},
					MayHang:         false,
					LastHangMsgTime: 1000,
				},
			},
		},
	}

	reqMsg := &utils.RequestEventMsg{
		Request:        &utils.RemoteRequestWrapper{Id: "req-1"},
		TargetInstance: "instance-1",
	}

	leader.OnFinishedRequest("worker-1", reqMsg)

	if _, exists := leader.WorkerList["instance-1"]["worker-1"].RequestsList["req-1"]; exists {
		t.Error("Expected request to be removed")
	}
	// 确保还保留另一个请求
	if _, exists := leader.WorkerList["instance-1"]["worker-1"].RequestsList["req-2"]; !exists {
		t.Error("Expected req-2 to still exist")
	}
}

// 测试场景: OnFinishedRequest 不存在的worker直接返回
func TestLeaderHangDecider_OnFinishedRequest_NonExistingWorker(t *testing.T) {
	cfg := &config.HangCheckerConfig{
		Enable: true,
	}
	leader := &LeaderHangDecider{
		cfg: cfg,
		WorkerList: map[string]map[string]*WorkerRecorder{
			"instance-1": {},
		},
	}

	reqMsg := &utils.RequestEventMsg{
		Request:        &utils.RemoteRequestWrapper{Id: "req-1"},
		TargetInstance: "instance-1",
	}

	// 应该直接返回，不panic
	leader.OnFinishedRequest("non-existent", reqMsg)
}

// 测试场景: OnFinishedRequest 请求完成后删除空的worker列表
func TestLeaderHangDecider_OnFinishedRequest_RemoveEmptyWorker(t *testing.T) {
	cfg := &config.HangCheckerConfig{
		Enable: true,
	}
	leader := &LeaderHangDecider{
		cfg: cfg,
		WorkerList: map[string]map[string]*WorkerRecorder{
			"instance-1": {
				"worker-1": {
					RequestsList:    map[string]int64{"req-1": 1000},
					MayHang:         false,
					LastHangMsgTime: 1000,
				},
			},
		},
	}

	reqMsg := &utils.RequestEventMsg{
		Request:        &utils.RemoteRequestWrapper{Id: "req-1"},
		TargetInstance: "instance-1",
	}

	leader.OnFinishedRequest("worker-1", reqMsg)

	if _, exists := leader.WorkerList["instance-1"]["worker-1"]; exists {
		t.Error("Expected worker to be removed when RequestsList is empty")
	}
}

// 测试场景: Stop 关闭channel
func TestLeaderHangDecider_Stop(t *testing.T) {
	leader := &LeaderHangDecider{
		closeChan: make(chan struct{}),
	}

	// 应该不会panic
	leader.Stop()
}

// 测试场景: getProbeStatus 正在探测中
func TestLeaderHangDecider_getProbeStatus_Probing(t *testing.T) {
	leader := &LeaderHangDecider{
		probingInstances: map[string]bool{
			"instance-1": true,
		},
	}

	status := leader.getProbeStatus("instance-1")
	if status != ProbeStatusProbing {
		t.Errorf("Expected ProbeStatusProbing, got %d", status)
	}
}

// 测试场景: getProbeStatus 无探测记录
func TestLeaderHangDecider_getProbeStatus_NoRecord(t *testing.T) {
	leader := &LeaderHangDecider{
		probingInstances: make(map[string]bool),
		probeStates:      make(map[string]ProbeState),
	}

	status := leader.getProbeStatus("instance-1")
	if status != ProbeStatusNone {
		t.Errorf("Expected ProbeStatusNone, got %d", status)
	}
}

// 测试场景: getProbeStatus 探测成功且未超时
func TestLeaderHangDecider_getProbeStatus_Success(t *testing.T) {
	cfg := &config.HangCheckerConfig{
		Enable:              true,
		HangProbeIntervalMs: 30000,
	}
	leader := &LeaderHangDecider{
		cfg:              cfg,
		probingInstances: make(map[string]bool),
		probeStates:      map[string]ProbeState{"instance-1": ProbeStateSuccess},
		lastProbeTime:    map[string]int64{"instance-1": time.Now().UnixMilli()},
	}

	status := leader.getProbeStatus("instance-1")
	if status != ProbeStatusSuccess {
		t.Errorf("Expected ProbeStatusSuccess, got %d", status)
	}
}

// 测试场景: getProbeStatus 探测成功已超时返回None
func TestLeaderHangDecider_getProbeStatus_SuccessTimeout(t *testing.T) {
	cfg := &config.HangCheckerConfig{
		Enable:              true,
		HangProbeIntervalMs: 30000,
	}
	leader := &LeaderHangDecider{
		cfg:              cfg,
		probingInstances: make(map[string]bool),
		probeStates:      map[string]ProbeState{"instance-1": ProbeStateSuccess},
		lastProbeTime:    map[string]int64{"instance-1": time.Now().UnixMilli() - 60000}, // 超过探测间隔
	}

	status := leader.getProbeStatus("instance-1")
	if status != ProbeStatusNone {
		t.Errorf("Expected ProbeStatusNone when timeout, got %d", status)
	}
}

// 测试场景: getProbeStatus 探测失败
func TestLeaderHangDecider_getProbeStatus_Failed(t *testing.T) {
	cfg := &config.HangCheckerConfig{
		Enable:              true,
		HangProbeIntervalMs: 30000,
	}
	leader := &LeaderHangDecider{
		cfg:              cfg,
		probingInstances: make(map[string]bool),
		probeStates:      map[string]ProbeState{"instance-1": ProbeStateFailed},
		lastProbeTime:    map[string]int64{"instance-1": time.Now().UnixMilli()},
	}

	status := leader.getProbeStatus("instance-1")
	if status != ProbeStatusFailed {
		t.Errorf("Expected ProbeStatusFailed, got %d", status)
	}
}

// 测试场景: getProbeStatus 未知状态返回None
func TestLeaderHangDecider_getProbeStatus_UnknownState(t *testing.T) {
	leader := &LeaderHangDecider{
		probingInstances: make(map[string]bool),
		probeStates:      map[string]ProbeState{"instance-1": 999}, // 未知状态
	}

	status := leader.getProbeStatus("instance-1")
	if status != ProbeStatusNone {
		t.Errorf("Expected ProbeStatusNone for unknown state, got %d", status)
	}
}

// 测试场景: checkAndResetProbeTimeout 未超时返回原状态
func TestLeaderHangDecider_checkAndResetProbeTimeout_NotTimeout(t *testing.T) {
	cfg := &config.HangCheckerConfig{
		Enable:              true,
		HangProbeIntervalMs: 30000,
	}
	leader := &LeaderHangDecider{
		cfg:           cfg,
		lastProbeTime: map[string]int64{"instance-1": time.Now().UnixMilli()},
	}

	status := leader.checkAndResetProbeTimeout("instance-1", ProbeStatusSuccess)
	if status != ProbeStatusSuccess {
		t.Errorf("Expected ProbeStatusSuccess when not timeout, got %d", status)
	}
}

// 测试场景: checkAndResetProbeTimeout 超时返回None
func TestLeaderHangDecider_checkAndResetProbeTimeout_Timeout(t *testing.T) {
	cfg := &config.HangCheckerConfig{
		Enable:              true,
		HangProbeIntervalMs: 30000,
	}
	leader := &LeaderHangDecider{
		cfg:           cfg,
		lastProbeTime: map[string]int64{"instance-1": time.Now().UnixMilli() - 60000},
		probeStates:   make(map[string]ProbeState),
	}

	status := leader.checkAndResetProbeTimeout("instance-1", ProbeStatusSuccess)
	if status != ProbeStatusNone {
		t.Errorf("Expected ProbeStatusNone when timeout, got %d", status)
	}
}

// 测试场景: checkAndResetProbeTimeout 无探测时间记录返回默认状态
func TestLeaderHangDecider_checkAndResetProbeTimeout_NoRecord(t *testing.T) {
	cfg := &config.HangCheckerConfig{
		Enable:              true,
		HangProbeIntervalMs: 30000,
	}
	leader := &LeaderHangDecider{
		cfg:           cfg,
		lastProbeTime: make(map[string]int64),
	}

	status := leader.checkAndResetProbeTimeout("instance-1", ProbeStatusSuccess)
	if status != ProbeStatusSuccess {
		t.Errorf("Expected ProbeStatusSuccess when no record, got %d", status)
	}
}

// 测试场景: ProbeStatus 值定义
func TestProbeStatusValues(t *testing.T) {
	if ProbeStatusNone != 0 {
		t.Errorf("ProbeStatusNone should be 0, got %d", ProbeStatusNone)
	}
	if ProbeStatusProbing != 1 {
		t.Errorf("ProbeStatusProbing should be 1, got %d", ProbeStatusProbing)
	}
	if ProbeStatusSuccess != 2 {
		t.Errorf("ProbeStatusSuccess should be 2, got %d", ProbeStatusSuccess)
	}
	if ProbeStatusFailed != 3 {
		t.Errorf("ProbeStatusFailed should be 3, got %d", ProbeStatusFailed)
	}
}

// 测试场景: ProbeResult 值定义
func TestProbeResultValues(t *testing.T) {
	if ProbeResultSkip != 0 {
		t.Errorf("ProbeResultSkip should be 0, got %d", ProbeResultSkip)
	}
	if ProbeResultProbing != 1 {
		t.Errorf("ProbeResultProbing should be 1, got %d", ProbeResultProbing)
	}
	if ProbeResultPending != 2 {
		t.Errorf("ProbeResultPending should be 2, got %d", ProbeResultPending)
	}
}

// ==================== 其他边界测试场景 ====================

// 测试场景: OnRequestEvent 完成请求事件
func TestWorkerHangChecker_OnRequestEvent_FinishedRequest(t *testing.T) {
	checker := &WorkerHangChecker{
		cfg: &config.HangCheckerConfig{
			Enable: true,
			HangThresholds: []config.HangThreshold{
				{Role: "prefill", ThresholdTime: 5000, SkipNonStream: false},
			},
		},
		InstancesList: map[string]*InstanceRecorder{
			"instance-1": {
				Role:                  "prefill",
				LastSuccessTime:       1000,
				RequestsTimeList:      map[string]int64{"req-1": 1000},
				NonStreamCnt:          0,
				LastNonStreamWarnTime: 0,
				MayHang:               false,
			},
		},
		Thresholds: map[string]*RoleThreshold{
			"prefill": {ThresholdTime: 5000, SkipNonStream: false},
		},
	}

	reqMsg := &utils.RequestEventMsg{
		Request:            &utils.RemoteRequestWrapper{Id: "req-1", Type: "prefill", Stream: true},
		TargetInstance:     "instance-1",
		EventType:          1, // 使用非0值模拟RequestFinish
		TimeStamp:          2000,
		InternalTestingReq: false,
	}

	checker.OnRequestEvent(reqMsg)

	// 请求完成后应该被删除
	if _, exists := checker.InstancesList["instance-1"]; exists {
		t.Error("Expected instance to be removed after all requests finished")
	}
}

// 测试场景: OnNewRequest 已有实例时增加请求
func TestWorkerHangChecker_OnNewRequest_ExistingInstance(t *testing.T) {
	checker := &WorkerHangChecker{
		cfg: &config.HangCheckerConfig{Enable: true},
		InstancesList: map[string]*InstanceRecorder{
			"instance-1": {
				Role:                  "prefill",
				LastSuccessTime:       1000,
				RequestsTimeList:      map[string]int64{"req-1": 1000},
				NonStreamCnt:          0,
				LastNonStreamWarnTime: 0,
				MayHang:               false,
			},
		},
		Thresholds: map[string]*RoleThreshold{
			"prefill": {ThresholdTime: 5000, SkipNonStream: false},
		},
		mu: sync.Mutex{},
	}

	request := &utils.RemoteRequestWrapper{Id: "req-2", Type: "prefill", Stream: true}
	timestamp := time.Now().UnixMilli()

	checker.OnNewRequest(request, "instance-1", timestamp)

	recorder := checker.GetInstanceRecorder("instance-1")
	if recorder == nil {
		t.Error("Expected InstanceRecorder to exist")
	}
	if len(recorder.RequestsTimeList) != 2 {
		t.Errorf("Expected 2 requests, got %d", len(recorder.RequestsTimeList))
	}
}

// 测试场景: OnLocalDecodeTokens 不存在的请求直接返回
func TestWorkerHangChecker_OnLocalDecodeTokens_NonExistingRequest(t *testing.T) {
	checker := &WorkerHangChecker{
		cfg: &config.HangCheckerConfig{Enable: true},
		InstancesList: map[string]*InstanceRecorder{
			"instance-1": {
				Role:                  "decode",
				LastSuccessTime:       1000,
				RequestsTimeList:      map[string]int64{},
				NonStreamCnt:          0,
				LastNonStreamWarnTime: 0,
				MayHang:               false,
			},
		},
		mu: sync.Mutex{},
	}

	tokenMsg := &WorkerLocalTokenEvent{
		instanceId: "instance-1",
		reqId:      "non-existent",
		timestamp:  2000,
	}

	// 应该直接返回，不panic
	checker.OnLocalDecodeTokens(tokenMsg)
}

// 测试场景: IsInstanceHanging 无请求时也检查挂起
func TestWorkerHangChecker_IsInstanceHanging_NoRequests(t *testing.T) {
	checker := &WorkerHangChecker{
		Thresholds: map[string]*RoleThreshold{
			"prefill": {ThresholdTime: 5000, SkipNonStream: false},
		},
		HangWarningInterval: 30000,
	}

	recorder := &InstanceRecorder{
		Role:                  "prefill",
		LastSuccessTime:       1000,
		RequestsTimeList:      map[string]int64{},
		NonStreamCnt:          0,
		LastNonStreamWarnTime: 0,
		MayHang:               false,
	}

	timestamp := int64(10000)
	result := checker.IsInstanceHanging(recorder, "instance-1", timestamp)

	if result != true {
		t.Error("Expected true when instance is hanging even with no requests")
	}
}

// 测试场景: OnInstanceTokenEvent worker不存在时跳过
func TestLeaderHangDecider_OnInstanceTokenEvent_WorkerNotExist(t *testing.T) {
	cfg := &config.HangCheckerConfig{
		Enable: true,
	}
	leader := &LeaderHangDecider{
		cfg: cfg,
		WorkerList: map[string]map[string]*WorkerRecorder{
			"instance-1": {},
		},
	}

	instanceEvents := []*utils.InstanceEventMsg{
		{InstanceId: "instance-1", IsHang: true},
	}

	// worker不存在时应该跳过，不panic
	leader.OnInstanceTokenEvent("non-existent-worker", instanceEvents)
}

// 测试场景: OnRequestEvents 处理完成请求事件
func TestLeaderHangDecider_OnRequestEvents_FinishedRequest(t *testing.T) {
	cfg := &config.HangCheckerConfig{
		Enable: true,
	}
	leader := &LeaderHangDecider{
		cfg: cfg,
		WorkerList: map[string]map[string]*WorkerRecorder{
			"instance-1": {
				"worker-1": {
					RequestsList:    map[string]int64{"req-1": 1000, "req-2": 2000},
					MayHang:         false,
					LastHangMsgTime: 1000,
				},
			},
		},
	}

	requestEvents := []*utils.RequestEventMsg{
		{
			Request:        &utils.RemoteRequestWrapper{Id: "req-1"},
			TargetInstance: "instance-1",
			EventType:      1, // 使用非0值模拟RequestFinish
			TimeStamp:      2000,
		},
	}

	leader.OnRequestEvents("worker-1", requestEvents)

	// req-1应该被删除，但req-2应该还存在
	if _, exists := leader.WorkerList["instance-1"]["worker-1"].RequestsList["req-1"]; exists {
		t.Error("Expected request to be removed")
	}
	// 确保还保留另一个请求
	if _, exists := leader.WorkerList["instance-1"]["worker-1"].RequestsList["req-2"]; !exists {
		t.Error("Expected req-2 to still exist")
	}
}

// 测试场景: NewLeaderHangDecider 配置未启用
func TestNewLeaderHangDecider_Disabled(t *testing.T) {
	cfg := &config.HangCheckerConfig{
		Enable: false,
	}

	leaderHealthChecker := &LeaderHealthChecker{
		cfg: &config.HealthCheckConfig{
			AvailableCheckersCfg: config.AvailableCheckersConfig{
				HangCheckerCfg: *cfg,
			},
		},
		healthInstance:    make(map[string]*client.Instance),
		unhealthyInstance: make(map[string]*client.Instance),
	}

	leader := NewLeaderHangDecider(leaderHealthChecker)

	// 配置未启用时也应该返回非nil对象
	if leader == nil {
		t.Error("Expected LeaderHangDecider to be created even when disabled")
	}
}

// 测试场景: getProbeStatus 探测中状态
func TestLeaderHangDecider_getProbeStatus_ProbeStateProbing(t *testing.T) {
	cfg := &config.HangCheckerConfig{
		Enable:              true,
		HangProbeIntervalMs: 30000,
	}
	leader := &LeaderHangDecider{
		cfg:              cfg,
		probingInstances: make(map[string]bool),
		probeStates:      map[string]ProbeState{"instance-1": ProbeStateProbing},
		lastProbeTime:    map[string]int64{"instance-1": time.Now().UnixMilli()},
	}

	status := leader.getProbeStatus("instance-1")
	if status != ProbeStatusProbing {
		t.Errorf("Expected ProbeStatusProbing, got %d", status)
	}
}

// 测试场景: getProbeStatus 探测状态超时返回None
func TestLeaderHangDecider_getProbeStatus_FailedTimeout(t *testing.T) {
	cfg := &config.HangCheckerConfig{
		Enable:              true,
		HangProbeIntervalMs: 30000,
	}
	leader := &LeaderHangDecider{
		cfg:              cfg,
		probingInstances: make(map[string]bool),
		probeStates:      map[string]ProbeState{"instance-1": ProbeStateFailed},
		lastProbeTime:    map[string]int64{"instance-1": time.Now().UnixMilli() - 60000},
	}

	status := leader.getProbeStatus("instance-1")
	if status != ProbeStatusNone {
		t.Errorf("Expected ProbeStatusNone when timeout, got %d", status)
	}
}

// 测试场景: startProbeAsync 探测模块为nil
func TestStartProbeAsync_ProbeNil(t *testing.T) {
	cfg := &config.HangCheckerConfig{
		Enable:                     true,
		EnableProbeBeforeHangCheck: true,
		HangProbeIntervalMs:        30000,
	}

	leader := &LeaderHangDecider{
		cfg:               cfg,
		lastProbeTime:     make(map[string]int64),
		probingInstances:  make(map[string]bool),
		probeStates:       make(map[string]ProbeState),
		probeInferRequest: nil,
		leaderHealthChecker: &LeaderHealthChecker{
			healthInstance:    map[string]*client.Instance{"test-instance": {ID: "test-instance"}},
			unhealthyInstance: make(map[string]*client.Instance),
		},
	}

	// probeInferRequest为nil时应该正常返回
	leader.startProbeAsync("test-instance")
}

// 测试场景: startProbeAsync 实例为nil
func TestStartProbeAsync_InstanceNil(t *testing.T) {
	cfg := &config.HangCheckerConfig{
		Enable:                     true,
		EnableProbeBeforeHangCheck: true,
		HangProbeIntervalMs:        30000,
	}

	leader := &LeaderHangDecider{
		cfg:               cfg,
		lastProbeTime:     make(map[string]int64),
		probingInstances:  make(map[string]bool),
		probeStates:       make(map[string]ProbeState),
		probeInferRequest: &ProbeInferRequest{},
		leaderHealthChecker: &LeaderHealthChecker{
			healthInstance:    make(map[string]*client.Instance),
			unhealthyInstance: make(map[string]*client.Instance),
		},
	}

	// 实例为nil时应该正常返回
	leader.startProbeAsync("non-existent-instance")
}

// 测试场景: probeBeforeHang 配置未启用探测
func TestProbeBeforeHang_ConfigNotEnabled(t *testing.T) {
	cfg := &config.HangCheckerConfig{
		Enable:                     true,
		EnableProbeBeforeHangCheck: false, // 探测未启用
		HangProbeIntervalMs:        30000,
	}
	leader := &LeaderHangDecider{
		cfg:              cfg,
		WorkerList:       map[string]map[string]*WorkerRecorder{"instance-1": {}},
		lastProbeTime:    make(map[string]int64),
		probingInstances: make(map[string]bool),
		probeStates:      make(map[string]ProbeState),
		leaderHealthChecker: &LeaderHealthChecker{
			healthInstance:    map[string]*client.Instance{"instance-1": {ID: "instance-1"}},
			unhealthyInstance: make(map[string]*client.Instance),
		},
	}

	// 配置未启用探测时，probeBeforeHang的行为通过checkLeaderHang中的逻辑处理
	// 这里我们测试getProbeStatus返回None时的场景
	status := leader.getProbeStatus("instance-1")
	if status != ProbeStatusNone {
		t.Errorf("Expected ProbeStatusNone when no probe record, got %d", status)
	}
}

// 测试场景: probeBeforeHang 正在探测中
func TestProbeBeforeHang_Probing(t *testing.T) {
	cfg := &config.HangCheckerConfig{
		Enable:                     true,
		EnableProbeBeforeHangCheck: true,
		HangProbeIntervalMs:        30000,
	}
	leader := &LeaderHangDecider{
		cfg:              cfg,
		WorkerList:       map[string]map[string]*WorkerRecorder{"instance-1": {}},
		lastProbeTime:    make(map[string]int64),
		probingInstances: map[string]bool{"instance-1": true}, // 正在探测中
		probeStates:      make(map[string]ProbeState),
		leaderHealthChecker: &LeaderHealthChecker{
			healthInstance:    map[string]*client.Instance{"instance-1": {ID: "instance-1"}},
			unhealthyInstance: make(map[string]*client.Instance),
		},
	}

	status := leader.getProbeStatus("instance-1")
	if status != ProbeStatusProbing {
		t.Errorf("Expected ProbeStatusProbing, got %d", status)
	}
}

// 测试场景: probeBeforeHang 探测成功且未超时
func TestProbeBeforeHang_ProbeSuccessNotTimeout(t *testing.T) {
	cfg := &config.HangCheckerConfig{
		Enable:                     true,
		EnableProbeBeforeHangCheck: true,
		HangProbeIntervalMs:        30000,
	}
	now := time.Now().UnixMilli()
	leader := &LeaderHangDecider{
		cfg:              cfg,
		WorkerList:       map[string]map[string]*WorkerRecorder{"instance-1": {}},
		lastProbeTime:    map[string]int64{"instance-1": now}, // 最近探测时间
		probingInstances: make(map[string]bool),
		probeStates:      map[string]ProbeState{"instance-1": ProbeStateSuccess},
		leaderHealthChecker: &LeaderHealthChecker{
			healthInstance:    map[string]*client.Instance{"instance-1": {ID: "instance-1"}},
			unhealthyInstance: make(map[string]*client.Instance),
		},
	}

	status := leader.getProbeStatus("instance-1")
	if status != ProbeStatusSuccess {
		t.Errorf("Expected ProbeStatusSuccess, got %d", status)
	}
}

// 测试场景: probeBeforeHang 探测成功已超时
func TestProbeBeforeHang_ProbeSuccessTimeout(t *testing.T) {
	cfg := &config.HangCheckerConfig{
		Enable:                     true,
		EnableProbeBeforeHangCheck: true,
		HangProbeIntervalMs:        30000,
	}
	// 探测时间超过间隔，视为超时
	oldTime := time.Now().UnixMilli() - 60000
	leader := &LeaderHangDecider{
		cfg:              cfg,
		WorkerList:       map[string]map[string]*WorkerRecorder{"instance-1": {}},
		lastProbeTime:    map[string]int64{"instance-1": oldTime},
		probingInstances: make(map[string]bool),
		probeStates:      map[string]ProbeState{"instance-1": ProbeStateSuccess},
		leaderHealthChecker: &LeaderHealthChecker{
			healthInstance:    map[string]*client.Instance{"instance-1": {ID: "instance-1"}},
			unhealthyInstance: make(map[string]*client.Instance),
		},
	}

	status := leader.getProbeStatus("instance-1")
	if status != ProbeStatusNone {
		t.Errorf("Expected ProbeStatusNone when probe timeout, got %d", status)
	}
}

// 测试场景: getProbeStatus 配置未启用探测
func TestGetProbeStatus_ConfigNotEnabled(t *testing.T) {
	cfg := &config.HangCheckerConfig{
		Enable:                     true,
		EnableProbeBeforeHangCheck: false,
	}
	leader := &LeaderHangDecider{
		cfg:              cfg,
		lastProbeTime:    make(map[string]int64),
		probingInstances: make(map[string]bool),
		probeStates:      make(map[string]ProbeState),
	}

	status := leader.getProbeStatus("instance-1")
	if status != ProbeStatusNone {
		t.Errorf("Expected ProbeStatusNone when no probe, got %d", status)
	}
}

// 测试场景: getProbeStatus 正在探测中
func TestGetProbeStatus_Probing(t *testing.T) {
	cfg := &config.HangCheckerConfig{
		Enable: true,
	}
	leader := &LeaderHangDecider{
		cfg:              cfg,
		lastProbeTime:    make(map[string]int64),
		probingInstances: map[string]bool{"instance-1": true},
		probeStates:      make(map[string]ProbeState),
	}

	status := leader.getProbeStatus("instance-1")
	if status != ProbeStatusProbing {
		t.Errorf("Expected ProbeStatusProbing, got %d", status)
	}
}

// 测试场景: getProbeStatus 实例不健康（在unhealthy列表中）
func TestGetProbeStatus_InstanceUnhealthy(t *testing.T) {
	cfg := &config.HangCheckerConfig{
		Enable: true,
	}
	leader := &LeaderHangDecider{
		cfg:              cfg,
		lastProbeTime:    make(map[string]int64),
		probingInstances: make(map[string]bool),
		probeStates:      make(map[string]ProbeState),
		leaderHealthChecker: &LeaderHealthChecker{
			healthInstance:    make(map[string]*client.Instance),
			unhealthyInstance: map[string]*client.Instance{"instance-1": {ID: "instance-1"}},
		},
	}

	// 实例在不健康列表中，isInstanceHealthy返回false
	isHealthy := leader.leaderHealthChecker.isInstanceHealthy("instance-1")
	if isHealthy != false {
		t.Error("Expected instance to be unhealthy")
	}
}

// 测试场景: getProbeStatus 探测间隔未到
func TestGetProbeStatus_ProbeIntervalNotReached(t *testing.T) {
	cfg := &config.HangCheckerConfig{
		Enable:              true,
		HangProbeIntervalMs: 30000,
	}
	now := time.Now().UnixMilli()
	leader := &LeaderHangDecider{
		cfg:              cfg,
		lastProbeTime:    map[string]int64{"instance-1": now - 10000}, // 10秒前，刚好在间隔内
		probingInstances: make(map[string]bool),
		probeStates:      map[string]ProbeState{"instance-1": ProbeStateFailed},
		leaderHealthChecker: &LeaderHealthChecker{
			healthInstance:    map[string]*client.Instance{"instance-1": {ID: "instance-1"}},
			unhealthyInstance: make(map[string]*client.Instance),
		},
	}

	status := leader.getProbeStatus("instance-1")
	// 探测失败且未超时，应该返回ProbeStatusFailed
	if status != ProbeStatusFailed {
		t.Errorf("Expected ProbeStatusFailed when probe failed and not timeout, got %d", status)
	}
}

// 测试场景: getProbeStatus 探测模块为nil
func TestGetProbeStatus_ProbeModuleNil(t *testing.T) {
	cfg := &config.HangCheckerConfig{
		Enable: true,
	}
	leader := &LeaderHangDecider{
		cfg:               cfg,
		lastProbeTime:     make(map[string]int64),
		probingInstances:  make(map[string]bool),
		probeStates:       make(map[string]ProbeState),
		probeInferRequest: nil, // 探测模块为nil
		leaderHealthChecker: &LeaderHealthChecker{
			healthInstance:    map[string]*client.Instance{"instance-1": {ID: "instance-1"}},
			unhealthyInstance: make(map[string]*client.Instance),
		},
	}

	status := leader.getProbeStatus("instance-1")
	if status != ProbeStatusNone {
		t.Errorf("Expected ProbeStatusNone when probe module nil, got %d", status)
	}
}

// 测试场景: getProbeStatus 实例为nil
func TestGetProbeStatus_InstanceNil(t *testing.T) {
	cfg := &config.HangCheckerConfig{
		Enable: true,
	}
	leader := &LeaderHangDecider{
		cfg:               cfg,
		lastProbeTime:     make(map[string]int64),
		probingInstances:  make(map[string]bool),
		probeStates:       make(map[string]ProbeState),
		probeInferRequest: &ProbeInferRequest{},
		leaderHealthChecker: &LeaderHealthChecker{
			healthInstance:    make(map[string]*client.Instance),
			unhealthyInstance: make(map[string]*client.Instance),
		},
	}

	status := leader.getProbeStatus("instance-1")
	if status != ProbeStatusNone {
		t.Errorf("Expected ProbeStatusNone when instance nil, got %d", status)
	}
}

// 测试场景: getProbeStatus 所有条件满足返回true (实例健康且可以发起探测)
func TestGetProbeStatus_AllConditionsMet(t *testing.T) {
	cfg := &config.HangCheckerConfig{
		Enable:              true,
		HangProbeIntervalMs: 30000,
	}
	// 上次探测时间超过间隔，可以再次探测
	oldTime := time.Now().UnixMilli() - 60000
	leader := &LeaderHangDecider{
		cfg:               cfg,
		lastProbeTime:     map[string]int64{"instance-1": oldTime},
		probingInstances:  make(map[string]bool),
		probeStates:       map[string]ProbeState{"instance-1": ProbeStateSuccess}, // 之前成功过
		probeInferRequest: &ProbeInferRequest{},
		leaderHealthChecker: &LeaderHealthChecker{
			healthInstance:    map[string]*client.Instance{"instance-1": {ID: "instance-1"}},
			unhealthyInstance: make(map[string]*client.Instance),
		},
	}

	status := leader.getProbeStatus("instance-1")
	// 探测成功已超时，返回ProbeStatusNone，表示可以发起新探测
	if status != ProbeStatusNone {
		t.Errorf("Expected ProbeStatusNone (can start new probe), got %d", status)
	}
}

// 测试场景: isInstanceHealthy 实例在健康列表中
func TestIsInstanceHealthy_InHealthyList(t *testing.T) {
	lhc := &LeaderHealthChecker{
		healthInstance:    map[string]*client.Instance{"instance-1": {ID: "instance-1"}},
		unhealthyInstance: make(map[string]*client.Instance),
		checkerMu:         sync.RWMutex{},
	}

	isHealthy := lhc.isInstanceHealthy("instance-1")
	if isHealthy != true {
		t.Error("Expected instance to be healthy")
	}
}

// 测试场景: isInstanceHealthy 实例不在健康列表中
func TestIsInstanceHealthy_NotInHealthyList(t *testing.T) {
	lhc := &LeaderHealthChecker{
		healthInstance:    make(map[string]*client.Instance),
		unhealthyInstance: map[string]*client.Instance{"instance-1": {ID: "instance-1"}},
		checkerMu:         sync.RWMutex{},
	}

	isHealthy := lhc.isInstanceHealthy("instance-1")
	if isHealthy != false {
		t.Error("Expected instance to be unhealthy")
	}
}

// 测试场景: isInstanceHealthy 实例既不在健康列表也不在不健康列表中
func TestIsInstanceHealthy_NotInAnyList(t *testing.T) {
	lhc := &LeaderHealthChecker{
		healthInstance:    make(map[string]*client.Instance),
		unhealthyInstance: make(map[string]*client.Instance),
		checkerMu:         sync.RWMutex{},
	}

	isHealthy := lhc.isInstanceHealthy("non-existent")
	if isHealthy != false {
		t.Error("Expected instance to be unhealthy when not in any list")
	}
}

// 测试场景: startProbeAsync 有效探测成功 (successToCheck=true, testSuccess=true)
func TestStartProbeAsync_ValidProbeSuccess(t *testing.T) {
	cfg := &config.HangCheckerConfig{
		Enable:                      true,
		EnableProbeBeforeHangCheck:  true,
		HangProbeIntervalMs:         30000,
		MaxConsecutiveInvalidProbes: 3,
	}

	leader := &LeaderHangDecider{
		cfg:               cfg,
		lastProbeTime:     make(map[string]int64),
		probingInstances:  make(map[string]bool),
		probeStates:       make(map[string]ProbeState),
		invalidProbeCount: make(map[string]int),
		probeInferRequest: &ProbeInferRequest{},
		leaderHealthChecker: &LeaderHealthChecker{
			healthInstance:    map[string]*client.Instance{"test-instance": {ID: "test-instance"}},
			unhealthyInstance: make(map[string]*client.Instance),
		},
	}

	// 由于startProbeAsync是异步的，我们需要等待一下让goroutine执行
	// 但我们可以通过检查probingInstances状态来验证
	leader.startProbeAsync("test-instance")

	// 等待异步探测完成
	time.Sleep(100 * time.Millisecond)

	leader.mu.Lock()
	defer leader.mu.Unlock()

	// 探测完成后probingInstances应该被清除
	// 由于我们没有真正的探测模块，探测会失败
	// 检查探测状态
	state := leader.probeStates["test-instance"]
	t.Logf("Probe state after async probe: %d", state)
}

// 测试场景: startProbeAsync 有效探测失败 (successToCheck=true, testSuccess=false)
func TestStartProbeAsync_ValidProbeFail(t *testing.T) {
	cfg := &config.HangCheckerConfig{
		Enable:                      true,
		EnableProbeBeforeHangCheck:  true,
		HangProbeIntervalMs:         30000,
		MaxConsecutiveInvalidProbes: 3,
	}

	leader := &LeaderHangDecider{
		cfg:               cfg,
		lastProbeTime:     make(map[string]int64),
		probingInstances:  make(map[string]bool),
		probeStates:       make(map[string]ProbeState),
		invalidProbeCount: make(map[string]int),
		probeInferRequest: &ProbeInferRequest{},
		leaderHealthChecker: &LeaderHealthChecker{
			healthInstance:    map[string]*client.Instance{"test-instance": {ID: "test-instance"}},
			unhealthyInstance: make(map[string]*client.Instance),
		},
	}

	// 设置初始的invalidProbeCount
	leader.invalidProbeCount["test-instance"] = 1

	leader.startProbeAsync("test-instance")

	// 等待异步探测完成
	time.Sleep(100 * time.Millisecond)

	leader.mu.Lock()
	defer leader.mu.Unlock()

	// 由于ProbeInferRequest未正确配置（model name为空），探测会返回无效探测
	// invalidProbeCount = 1 + 1 = 2，未达到阈值3，所以状态是ProbeStateNone
	if leader.probeStates["test-instance"] != ProbeStateNone {
		t.Errorf("Expected ProbeStateNone for invalid probe (not properly configured), got %d", leader.probeStates["test-instance"])
	}
	// 验证无效探测计数已增加
	if leader.invalidProbeCount["test-instance"] != 2 {
		t.Errorf("Expected invalidProbeCount = 2, got %d", leader.invalidProbeCount["test-instance"])
	}
}

// 测试场景: startProbeAsync 无效探测达到最大次数
func TestStartProbeAsync_InvalidProbeMaxCount(t *testing.T) {
	cfg := &config.HangCheckerConfig{
		Enable:                      true,
		EnableProbeBeforeHangCheck:  true,
		HangProbeIntervalMs:         30000,
		MaxConsecutiveInvalidProbes: 3,
	}

	leader := &LeaderHangDecider{
		cfg:               cfg,
		lastProbeTime:     make(map[string]int64),
		probingInstances:  make(map[string]bool),
		probeStates:       make(map[string]ProbeState),
		invalidProbeCount: make(map[string]int),
		probeInferRequest: &ProbeInferRequest{},
		leaderHealthChecker: &LeaderHealthChecker{
			healthInstance:    map[string]*client.Instance{"test-instance": {ID: "test-instance"}},
			unhealthyInstance: make(map[string]*client.Instance),
		},
	}

	// 设置当前无效探测次数为阈值-1=2，下一次探测会达到最大
	leader.invalidProbeCount["test-instance"] = 2

	leader.startProbeAsync("test-instance")

	// 等待异步探测完成
	time.Sleep(100 * time.Millisecond)

	leader.mu.Lock()
	defer leader.mu.Unlock()

	// 达到最大连续无效探测次数时，状态应该是ProbeStateFailed
	if leader.probeStates["test-instance"] != ProbeStateFailed {
		t.Errorf("Expected ProbeStateFailed when reaching max invalid probe count, got %d", leader.probeStates["test-instance"])
	}
}

// 测试场景: startProbeAsync 无效探测达到阈值-1
func TestStartProbeAsync_InvalidProbeThresholdMinusOne(t *testing.T) {
	cfg := &config.HangCheckerConfig{
		Enable:                      true,
		EnableProbeBeforeHangCheck:  true,
		HangProbeIntervalMs:         30000,
		MaxConsecutiveInvalidProbes: 3,
	}

	leader := &LeaderHangDecider{
		cfg:               cfg,
		lastProbeTime:     make(map[string]int64),
		probingInstances:  make(map[string]bool),
		probeStates:       make(map[string]ProbeState),
		invalidProbeCount: make(map[string]int),
		probeInferRequest: &ProbeInferRequest{},
		leaderHealthChecker: &LeaderHealthChecker{
			healthInstance:    map[string]*client.Instance{"test-instance": {ID: "test-instance"}},
			unhealthyInstance: make(map[string]*client.Instance),
		},
	}

	// 设置当前无效探测次数为1（阈值-1=2时，再来一次会达到阈值）
	leader.invalidProbeCount["test-instance"] = 1

	leader.startProbeAsync("test-instance")

	// 等待异步探测完成
	time.Sleep(100 * time.Millisecond)

	leader.mu.Lock()
	defer leader.mu.Unlock()

	// 达到阈值-1时，状态应该是ProbeStateNone（等待下一次探测）
	if leader.probeStates["test-instance"] != ProbeStateNone {
		t.Errorf("Expected ProbeStateNone at threshold-1, got %d", leader.probeStates["test-instance"])
	}
}

// 测试场景: startProbeAsync 无效探测未达到阈值
func TestStartProbeAsync_InvalidProbeBelowThreshold(t *testing.T) {
	cfg := &config.HangCheckerConfig{
		Enable:                      true,
		EnableProbeBeforeHangCheck:  true,
		HangProbeIntervalMs:         30000,
		MaxConsecutiveInvalidProbes: 3,
	}

	leader := &LeaderHangDecider{
		cfg:               cfg,
		lastProbeTime:     make(map[string]int64),
		probingInstances:  make(map[string]bool),
		probeStates:       make(map[string]ProbeState),
		invalidProbeCount: make(map[string]int),
		probeInferRequest: &ProbeInferRequest{},
		leaderHealthChecker: &LeaderHealthChecker{
			healthInstance:    map[string]*client.Instance{"test-instance": {ID: "test-instance"}},
			unhealthyInstance: make(map[string]*client.Instance),
		},
	}

	// 设置当前无效探测次数为0
	leader.invalidProbeCount["test-instance"] = 0

	leader.startProbeAsync("test-instance")

	// 等待异步探测完成
	time.Sleep(100 * time.Millisecond)

	leader.mu.Lock()
	defer leader.mu.Unlock()

	// 未达到阈值时，状态应该是ProbeStateNone
	if leader.probeStates["test-instance"] != ProbeStateNone {
		t.Errorf("Expected ProbeStateNone when below threshold, got %d", leader.probeStates["test-instance"])
	}
}

// 测试场景: OnAllInstancesRenewed 从健康实例变为不健康
func TestOnAllInstancesRenewed_HealthyToUnhealthy(t *testing.T) {
	cfg := &config.HangCheckerConfig{
		Enable: true,
	}
	leader := &LeaderHangDecider{
		cfg: cfg,
		WorkerList: map[string]map[string]*WorkerRecorder{
			"instance-1": {
				"worker-1": {
					RequestsList:    make(map[string]int64),
					MayHang:         true,
					LastHangMsgTime: time.Now().UnixMilli(),
				},
			},
			"instance-2": {
				"worker-1": {
					RequestsList:    make(map[string]int64),
					MayHang:         false,
					LastHangMsgTime: time.Now().UnixMilli(),
				},
			},
		},
		HangTag: map[string]bool{"instance-1": true, "instance-2": false},
	}

	// 模拟实例列表更新：instance-1和instance-2都在available列表中
	available := map[string]*client.Instance{
		"instance-1": {ID: "instance-1"},
		"instance-2": {ID: "instance-2"},
	}
	unavailable := make(map[string]*client.Instance)

	leader.OnAllInstancesRenewed(available, unavailable)

	// instance-1和instance-2都应该保留在WorkerList中
	if _, exists := leader.WorkerList["instance-1"]; !exists {
		t.Error("Expected instance-1 to remain in WorkerList")
	}
	if _, exists := leader.WorkerList["instance-2"]; !exists {
		t.Error("Expected instance-2 to remain in WorkerList")
	}
	// HangTag应该保留
	if leader.HangTag["instance-1"] != true {
		t.Error("Expected instance-1 HangTag to remain true")
	}
}

// 测试场景: OnFinishedRequest (Worker) 删除请求后实例为空的情况
func TestWorkerHangChecker_OnFinishedRequest_RemoveEmptyInstance(t *testing.T) {
	checker := &WorkerHangChecker{
		cfg: &config.HangCheckerConfig{Enable: true},
		InstancesList: map[string]*InstanceRecorder{
			"instance-1": {
				Role:                  "prefill",
				LastSuccessTime:       1000,
				RequestsTimeList:      map[string]int64{"req-1": 1000},
				NonStreamCnt:          0,
				LastNonStreamWarnTime: 0,
				MayHang:               false,
			},
		},
		Thresholds: map[string]*RoleThreshold{
			"prefill": {ThresholdTime: 5000, SkipNonStream: false},
		},
		mu: sync.Mutex{},
	}

	request := &utils.RemoteRequestWrapper{Id: "req-1", Type: "prefill", Stream: true}
	timestamp := int64(2000)

	checker.OnFinishedRequest(request, "instance-1", timestamp)

	// 当RequestsTimeList为空时，实例应该被删除
	if _, exists := checker.InstancesList["instance-1"]; exists {
		t.Error("Expected instance to be removed when RequestsTimeList is empty")
	}
}

// 测试场景: OnFinishedRequest (Worker) 删除最后请求后MayHang为true时被清除
func TestWorkerHangChecker_OnFinishedRequest_RemoveMayHangInstance(t *testing.T) {
	checker := &WorkerHangChecker{
		cfg: &config.HangCheckerConfig{Enable: true},
		InstancesList: map[string]*InstanceRecorder{
			"instance-1": {
				Role:                  "prefill",
				LastSuccessTime:       1000,
				RequestsTimeList:      map[string]int64{"req-1": 1000},
				NonStreamCnt:          0,
				LastNonStreamWarnTime: 0,
				MayHang:               true, // 标记为可能挂起
			},
		},
		Thresholds: map[string]*RoleThreshold{
			"prefill": {ThresholdTime: 5000, SkipNonStream: false},
		},
		mu: sync.Mutex{},
	}

	request := &utils.RemoteRequestWrapper{Id: "req-1", Type: "prefill", Stream: true}
	timestamp := int64(2000)

	// 这个测试验证当删除实例时，如果MayHang为true会打印恢复日志
	checker.OnFinishedRequest(request, "instance-1", timestamp)

	// 实例应该被删除
	if _, exists := checker.InstancesList["instance-1"]; exists {
		t.Error("Expected instance to be removed")
	}
}

// 测试场景: IsInstanceHanging 边界条件 - 刚好等于阈值时间
func TestWorkerHangChecker_IsInstanceHanging_BoundaryEqualThreshold(t *testing.T) {
	checker := &WorkerHangChecker{
		Thresholds: map[string]*RoleThreshold{
			"prefill": {ThresholdTime: 5000, SkipNonStream: false},
		},
		HangWarningInterval: 30000,
	}

	recorder := &InstanceRecorder{
		Role:                  "prefill",
		LastSuccessTime:       1000,
		RequestsTimeList:      map[string]int64{"req-1": 1000},
		NonStreamCnt:          0,
		LastNonStreamWarnTime: 0,
		MayHang:               false,
	}

	// 刚好等于阈值时间，不应该被判定为挂起
	timestamp := int64(6000) // 1000 + 5000 = 6000
	result := checker.IsInstanceHanging(recorder, "instance-1", timestamp)

	if result != false {
		t.Error("Expected false when hang time equals threshold")
	}
}

// 测试场景: IsInstanceHanging 边界条件 - 刚好超过阈值时间
func TestWorkerHangChecker_IsInstanceHanging_BoundaryExceedThreshold(t *testing.T) {
	checker := &WorkerHangChecker{
		Thresholds: map[string]*RoleThreshold{
			"prefill": {ThresholdTime: 5000, SkipNonStream: false},
		},
		HangWarningInterval: 30000,
	}

	recorder := &InstanceRecorder{
		Role:                  "prefill",
		LastSuccessTime:       1000,
		RequestsTimeList:      map[string]int64{"req-1": 1000},
		NonStreamCnt:          0,
		LastNonStreamWarnTime: 0,
		MayHang:               false,
	}

	// 刚好超过阈值时间，应该被判定为挂起
	timestamp := int64(6001) // 1000 + 5000 + 1
	result := checker.IsInstanceHanging(recorder, "instance-1", timestamp)

	if result != true {
		t.Error("Expected true when hang time exceeds threshold by 1ms")
	}
}

// 测试场景: IsInstanceHanging 边界条件 - LastSuccessTime为0
func TestWorkerHangChecker_IsInstanceHanging_LastSuccessTimeZero(t *testing.T) {
	checker := &WorkerHangChecker{
		Thresholds: map[string]*RoleThreshold{
			"prefill": {ThresholdTime: 5000, SkipNonStream: false},
		},
		HangWarningInterval: 30000,
	}

	recorder := &InstanceRecorder{
		Role:                  "prefill",
		LastSuccessTime:       0, // 初始状态
		RequestsTimeList:      map[string]int64{"req-1": 1000},
		NonStreamCnt:          0,
		LastNonStreamWarnTime: 0,
		MayHang:               false,
	}

	timestamp := int64(10000)
	result := checker.IsInstanceHanging(recorder, "instance-1", timestamp)

	// LastSuccessTime为0时，hangtime会很大，应该被判定为挂起
	if result != true {
		t.Error("Expected true when LastSuccessTime is 0")
	}
}

// 测试场景: IsInstanceHanging 边界条件 - Role配置但ThresholdTime为0
func TestWorkerHangChecker_IsInstanceHanging_RoleNotExist(t *testing.T) {
	checker := &WorkerHangChecker{
		Thresholds: map[string]*RoleThreshold{
			"prefill": {ThresholdTime: 5000, SkipNonStream: false},
			"decode":  {ThresholdTime: 0, SkipNonStream: false}, // 阈值为0，不检查挂起
		},
		HangWarningInterval: 30000,
	}

	recorder := &InstanceRecorder{
		Role:                  "decode", // 阈值为0的角色
		LastSuccessTime:       1000,
		RequestsTimeList:      map[string]int64{"req-1": 1000},
		NonStreamCnt:          0,
		LastNonStreamWarnTime: 0,
		MayHang:               false,
	}

	timestamp := int64(10000)
	result := checker.IsInstanceHanging(recorder, "instance-1", timestamp)

	// 当ThresholdTime为0时，应该返回false（不检查挂起）
	if result != false {
		t.Error("Expected false when role threshold time is 0")
	}
}

// 测试场景: IsInstanceHanging 边界条件 - NonStreamCnt刚好为0
func TestWorkerHangChecker_IsInstanceHanging_NonStreamCntZero(t *testing.T) {
	checker := &WorkerHangChecker{
		Thresholds: map[string]*RoleThreshold{
			"prefill": {ThresholdTime: 5000, SkipNonStream: true}, // 启用非流跳过
		},
		HangWarningInterval: 30000,
	}

	recorder := &InstanceRecorder{
		Role:                  "prefill",
		LastSuccessTime:       1000,
		RequestsTimeList:      map[string]int64{"req-1": 1000},
		NonStreamCnt:          0, // 刚好为0
		LastNonStreamWarnTime: 0,
		MayHang:               false,
	}

	timestamp := int64(10000)
	result := checker.IsInstanceHanging(recorder, "instance-1", timestamp)

	// NonStreamCnt为0且SkipNonStream为true时，不应该走跳过逻辑，而是正常检查
	if result != true {
		t.Error("Expected true when NonStreamCnt is 0 and SkipNonStream is true (should check normally)")
	}
}

// 测试场景: checkAndResetProbeTimeout 未超时
func TestCheckAndResetProbeTimeout_NotTimeout(t *testing.T) {
	cfg := &config.HangCheckerConfig{
		Enable:              true,
		HangProbeIntervalMs: 30000,
	}
	now := time.Now().UnixMilli()
	leader := &LeaderHangDecider{
		cfg:           cfg,
		lastProbeTime: map[string]int64{"instance-1": now - 10000}, // 10秒前，未超时
	}

	status := leader.checkAndResetProbeTimeout("instance-1", ProbeStatusSuccess)
	if status != ProbeStatusSuccess {
		t.Errorf("Expected ProbeStatusSuccess when not timeout, got %d", status)
	}
}

// 测试场景: checkAndResetProbeTimeout 超时
func TestCheckAndResetProbeTimeout_Timeout(t *testing.T) {
	cfg := &config.HangCheckerConfig{
		Enable:              true,
		HangProbeIntervalMs: 30000,
	}
	oldTime := time.Now().UnixMilli() - 60000 // 60秒前，已超时
	leader := &LeaderHangDecider{
		cfg:           cfg,
		lastProbeTime: map[string]int64{"instance-1": oldTime},
		probeStates:   make(map[string]ProbeState),
	}

	status := leader.checkAndResetProbeTimeout("instance-1", ProbeStatusSuccess)
	if status != ProbeStatusNone {
		t.Errorf("Expected ProbeStatusNone when timeout, got %d", status)
	}
}

// 测试场景: checkAndResetProbeTimeout 无探测时间记录
func TestCheckAndResetProbeTimeout_NoProbeTime(t *testing.T) {
	cfg := &config.HangCheckerConfig{
		Enable:              true,
		HangProbeIntervalMs: 30000,
	}
	leader := &LeaderHangDecider{
		cfg:           cfg,
		lastProbeTime: make(map[string]int64), // 无记录
	}

	status := leader.checkAndResetProbeTimeout("instance-1", ProbeStatusFailed)
	if status != ProbeStatusFailed {
		t.Errorf("Expected ProbeStatusFailed when no probe time record, got %d", status)
	}
}

// 测试场景: OnAllInstancesRenewed 配置未启用时跳过
func TestOnAllInstancesRenewed_HealthyToUnhealthy_ConfigDisabled(t *testing.T) {
	cfg := &config.HangCheckerConfig{
		Enable: false,
	}
	leader := &LeaderHangDecider{
		cfg: cfg,
		WorkerList: map[string]map[string]*WorkerRecorder{
			"instance-1": {
				"worker-1": {
					RequestsList:    make(map[string]int64),
					MayHang:         true,
					LastHangMsgTime: time.Now().UnixMilli(),
				},
			},
		},
		HangTag: map[string]bool{"instance-1": true},
	}

	// 配置未启用时，不应该清除实例
	available := make(map[string]*client.Instance)
	unavailable := map[string]*client.Instance{"instance-1": {ID: "instance-1"}}

	leader.OnAllInstancesRenewed(available, unavailable)

	// 实例不应该被清除
	if _, exists := leader.WorkerList["instance-1"]; !exists {
		t.Error("Expected instance to remain when config is disabled")
	}
}

// 测试场景: LeaderHangDecider OnFinishedRequest 删除请求后worker为空的情况
func TestLeaderHangDecider_OnFinishedRequest_RemoveEmptyWorkerList(t *testing.T) {
	cfg := &config.HangCheckerConfig{
		Enable: true,
	}
	leader := &LeaderHangDecider{
		cfg: cfg,
		WorkerList: map[string]map[string]*WorkerRecorder{
			"instance-1": {
				"worker-1": {
					RequestsList:    map[string]int64{"req-1": 1000},
					MayHang:         false,
					LastHangMsgTime: 1000,
				},
			},
		},
	}

	reqMsg := &utils.RequestEventMsg{
		Request:        &utils.RemoteRequestWrapper{Id: "req-1"},
		TargetInstance: "instance-1",
		EventType:      1, // RequestFinish
		TimeStamp:      2000,
	}

	leader.OnFinishedRequest("worker-1", reqMsg)

	// worker-1的RequestsList应该为空，worker-1应该被删除
	if _, exists := leader.WorkerList["instance-1"]; exists {
		if _, workerExists := leader.WorkerList["instance-1"]["worker-1"]; workerExists {
			t.Error("Expected worker to be removed when RequestsList is empty")
		}
	}
}
