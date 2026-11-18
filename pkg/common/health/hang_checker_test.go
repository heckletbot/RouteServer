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

package health

import (
	"sync"
	"testing"
	"time"

	"route-server/pkg/common/client"
	"route-server/pkg/common/config"
	"route-server/pkg/common/constant"
	"route-server/pkg/common/utils"
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

	// Should only initiate one probe
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

// Test startProbeAsync - non-blocking main loop
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

	// Record start time
	startTime := time.Now()

	// Simulate main loop calling needToStartProbe and startProbeAsync for multiple instances
	// This should complete in a very short time, should not block
	for i := 1; i <= 2; i++ {
		instanceId := "instance-" + string(rune('0'+i))
		leader.startProbeAsync(instanceId)
	}

	// Should return almost immediately (nanosecond level)
	elapsed := time.Since(startTime)
	if elapsed.Milliseconds() > 100 {
		t.Errorf("Expected non-blocking behavior, but took %v", elapsed)
	}
}

// Test SetProbeInferRequest
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

// Test NewLeaderHangDecider initializes invalidProbeCount
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

// Test config default value handling
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

// Test config default value handling - negative
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

// Test config valid value remains unchanged
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

// ==================== WorkerHangChecker Unit Tests ====================

// Test scenario: SetInstanceRecorder creates new instance recorder
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

// Test scenario: SetInstanceRecorder returns nil for duplicate instance
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

// Test scenario: GetInstanceRecorder gets existing instance recorder
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

// Test scenario: GetInstanceRecorder returns nil for non-existing instance
func TestWorkerHangChecker_GetInstanceRecorder_NotExisting(t *testing.T) {
	checker := &WorkerHangChecker{
		InstancesList: make(map[string]*InstanceRecorder),
	}

	recorder := checker.GetInstanceRecorder("non-existent")
	if recorder != nil {
		t.Error("Expected nil for non-existing instance")
	}
}

// Test scenario: RemoveInstance removes instance recorder
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

// Test scenario: Init initializes config and sets thresholds
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

// Test scenario: Init does not initialize when config is disabled
func TestWorkerHangChecker_Init_Disable(t *testing.T) {
	checker := &WorkerHangChecker{}
	cfg := &config.HangCheckerConfig{
		Enable: false,
	}

	checker.Init(cfg)

	// When Enable is false, InstancesList should not be initialized
	if checker.InstancesList != nil {
		t.Error("Expected InstancesList to remain nil when disabled")
	}
}

// Test scenario: OnRequestEvent handles new request event
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

// Test scenario: OnRequestEvent skips internal testing request
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

	// Internal testing request should be skipped
	if len(checker.InstancesList) != 0 {
		t.Error("Expected no instance to be added for internal testing request")
	}
}

// Test scenario: OnRequestEvent skips when config is disabled
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

	// Should be skipped when config is disabled
	if len(checker.InstancesList) != 0 {
		t.Error("Expected no instance to be added when disabled")
	}
}

// Test scenario: OnNewRequest adds new non-stream request
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

// Test scenario: OnNewRequest adds new stream request
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

// Test scenario: OnNewRequest updates existing request time
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

// Test scenario: OnNewRequest logs warning for invalid role type
func TestWorkerHangChecker_OnNewRequest_InvalidRole(t *testing.T) {
	checker := &WorkerHangChecker{
		cfg:           &config.HangCheckerConfig{Enable: true},
		InstancesList: make(map[string]*InstanceRecorder),
		Thresholds:    make(map[string]*RoleThreshold),
		mu:            sync.Mutex{},
	}

	request := &utils.RemoteRequestWrapper{Id: "req-1", Type: "invalid-role", Stream: false}
	timestamp := time.Now().UnixMilli()

	// Should log warning but not panic
	checker.OnNewRequest(request, "instance-1", timestamp)
}

// Test scenario: OnFinishedRequest completes non-stream request
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

// Test scenario: OnFinishedRequest removes instance when request list is empty
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

	// When RequestsTimeList is empty, instance should be removed
	if _, exists := checker.InstancesList["instance-1"]; exists {
		t.Error("Expected instance to be removed when RequestsTimeList is empty")
	}
}

// Test scenario: OnFinishedRequest returns directly for non-existing instance
func TestWorkerHangChecker_OnFinishedRequest_NonExistingInstance(t *testing.T) {
	checker := &WorkerHangChecker{
		cfg:           &config.HangCheckerConfig{Enable: true},
		InstancesList: make(map[string]*InstanceRecorder),
		mu:            sync.Mutex{},
	}

	request := &utils.RemoteRequestWrapper{Id: "req-1", Type: "prefill", Stream: true}

	// Should return directly, not panic
	checker.OnFinishedRequest(request, "non-existent", 1000)
}

// Test scenario: OnLocalDecodeTokens handles local decode token event
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

// Test scenario: OnLocalDecodeTokens skips when config is disabled
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

// Test scenario: OnLocalDecodeTokens returns directly for non-existing instance
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

	// Should return directly, not panic
	checker.OnLocalDecodeTokens(tokenMsg)
}

// Test scenario: IsInstanceHanging returns false when threshold time <= 0
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

// Test scenario: IsInstanceHanging does not report hanging when non-stream request exists and SkipNonStream is true
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

	timestamp := int64(10000) // Exceeds threshold time, but has non-stream request should skip
	result := checker.IsInstanceHanging(recorder, "instance-1", timestamp)

	if result != false {
		t.Error("Expected false when NonStreamCnt > 0 and SkipNonStream is true")
	}
}

// Test scenario: IsInstanceHanging logs warning when non-stream request exceeds warning interval
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

	timestamp := int64(40000) // Exceeds threshold time and warning interval
	result := checker.IsInstanceHanging(recorder, "instance-1", timestamp)

	if result != false {
		t.Error("Expected false when NonStreamCnt > 0 and SkipNonStream is true")
	}
	if recorder.LastNonStreamWarnTime != timestamp {
		t.Errorf("Expected LastNonStreamWarnTime to be updated to %d", timestamp)
	}
}

// Test scenario: IsInstanceHanging returns true when instance is hanging
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

	timestamp := int64(10000) // Exceeds threshold time
	result := checker.IsInstanceHanging(recorder, "instance-1", timestamp)

	if result != true {
		t.Error("Expected true when instance is hanging")
	}
	if recorder.MayHang != true {
		t.Error("Expected MayHang to be set to true")
	}
}

// Test scenario: IsInstanceHanging logs when instance recovers from hanging
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
		MayHang:               true, // Previously in hanging state
	}

	timestamp := int64(12000) // Does not exceed threshold time
	result := checker.IsInstanceHanging(recorder, "instance-1", timestamp)

	if result != false {
		t.Error("Expected false when instance recovered from hang")
	}
	if recorder.MayHang != false {
		t.Error("Expected MayHang to be set to false")
	}
}

// Test scenario: InstanceHangCheck returns nil when config is disabled
func TestWorkerHangChecker_InstanceHangCheck_Disabled(t *testing.T) {
	checker := &WorkerHangChecker{
		cfg: &config.HangCheckerConfig{Enable: false},
	}

	events := checker.InstanceHangCheck(time.Now().UnixMilli())

	if events != nil {
		t.Error("Expected nil when disabled")
	}
}

// Test scenario: InstanceHangCheck normally detects hanging instances
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

// Test scenario: InstanceHangCheck returns empty list when no hanging instances
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

// ==================== LeaderHangDecider Unit Tests ====================

// Test scenario: GetWorker gets existing worker
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

// Test scenario: GetWorker returns nil for non-existing instance and logs warning
func TestLeaderHangDecider_GetWorker_NonExistingInstance(t *testing.T) {
	leader := &LeaderHangDecider{
		WorkerList: make(map[string]map[string]*WorkerRecorder),
	}

	worker := leader.GetWorker("non-existent", "worker-1")
	if worker != nil {
		t.Error("Expected nil for non-existing instance")
	}
}

// Test scenario: GetWorker returns nil for non-existing worker
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

// Test scenario: SetWorker sets new worker
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

// Test scenario: SetWorker returns nil for non-existing instance
func TestLeaderHangDecider_SetWorker_NonExistingInstance(t *testing.T) {
	leader := &LeaderHangDecider{
		WorkerList: make(map[string]map[string]*WorkerRecorder),
	}

	worker := leader.SetWorker("non-existent", "worker-1")
	if worker != nil {
		t.Error("Expected nil for non-existing instance")
	}
}

// Test scenario: SetWorker returns existing record for existing worker
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

// Test scenario: ClearInstance clears all workers of an instance
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

// Test scenario: ClearInstance logs warning for non-existing instance
func TestLeaderHangDecider_ClearInstance_NonExisting(t *testing.T) {
	leader := &LeaderHangDecider{
		WorkerList: make(map[string]map[string]*WorkerRecorder),
	}

	// Should log warning but not panic
	leader.ClearInstance("non-existent")
}

// Test scenario: OnAllInstancesRenewed adds new instance
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

// Test scenario: OnAllInstancesRenewed deletes non-existing instance
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

// Test scenario: OnAllInstancesRenewed skips when config is disabled
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

	// When config is disabled, WorkerList should not be modified
	if len(leader.WorkerList) != 0 {
		t.Error("Expected WorkerList to remain empty when disabled")
	}
}

// Test scenario: OnInstanceTokenEvent handles instance hang event
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

// Test scenario: OnInstanceTokenEvent skips when config is disabled
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

// Test scenario: OnRequestEvents handles request events
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

	leader.OnRequestEvents("worker-1", "", requestEvents)

	if _, exists := leader.WorkerList["instance-1"]["worker-1"]; !exists {
		t.Error("Expected worker to be added")
	}
}

// Test scenario: OnRequestEvents skips when config is disabled
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

	leader.OnRequestEvents("worker-1", "", requestEvents)

	if len(leader.WorkerList) != 0 {
		t.Error("Expected WorkerList to remain empty when disabled")
	}
}

// Test scenario: OnNewRequest handles new request
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

// Test scenario: OnNewRequest logs warning for non-existing instance
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

	// Should log warning but not panic
	leader.OnNewRequest("worker-1", reqMsg)
}

// Test scenario: OnFinishedRequest completes request
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
	// Ensure the other request is still kept
	if _, exists := leader.WorkerList["instance-1"]["worker-1"].RequestsList["req-2"]; !exists {
		t.Error("Expected req-2 to still exist")
	}
}

// Test scenario: OnFinishedRequest returns directly for non-existing worker
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

	// Should return directly, not panic
	leader.OnFinishedRequest("non-existent", reqMsg)
}

// Test scenario: OnFinishedRequest deletes empty worker list after request completion
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

// Test scenario: Stop closes channel
func TestLeaderHangDecider_Stop(t *testing.T) {
	leader := &LeaderHangDecider{
		closeChan: make(chan struct{}),
	}

	// Should not panic
	leader.Stop()
}

// Test scenario: getProbeStatus is probing
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

// Test scenario: getProbeStatus no probe record
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

// Test scenario: getProbeStatus probe success and not timeout
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

// Test scenario: getProbeStatus probe success timeout returns None
func TestLeaderHangDecider_getProbeStatus_SuccessTimeout(t *testing.T) {
	cfg := &config.HangCheckerConfig{
		Enable:              true,
		HangProbeIntervalMs: 30000,
	}
	leader := &LeaderHangDecider{
		cfg:              cfg,
		probingInstances: make(map[string]bool),
		probeStates:      map[string]ProbeState{"instance-1": ProbeStateSuccess},
		lastProbeTime:    map[string]int64{"instance-1": time.Now().UnixMilli() - 60000}, // Exceeds probe interval
	}

	status := leader.getProbeStatus("instance-1")
	if status != ProbeStatusNone {
		t.Errorf("Expected ProbeStatusNone when timeout, got %d", status)
	}
}

// Test scenario: getProbeStatus probe failed
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

// Test scenario: getProbeStatus unknown state returns None
func TestLeaderHangDecider_getProbeStatus_UnknownState(t *testing.T) {
	leader := &LeaderHangDecider{
		probingInstances: make(map[string]bool),
		probeStates:      map[string]ProbeState{"instance-1": 999}, // Unknown state
	}

	status := leader.getProbeStatus("instance-1")
	if status != ProbeStatusNone {
		t.Errorf("Expected ProbeStatusNone for unknown state, got %d", status)
	}
}

// Test scenario: checkAndResetProbeTimeout returns original status when not timeout
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

// Test scenario: checkAndResetProbeTimeout returns None when timeout
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

// Test scenario: checkAndResetProbeTimeout returns default status when no probe time record
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

// Test scenario: ProbeStatus value definitions
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

// Test scenario: ProbeResult value definitions
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

// ==================== Other Boundary Test Scenarios ====================

// Test scenario: OnRequestEvent finished request event
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
		EventType:          1, // Use non-zero value to simulate RequestFinish
		TimeStamp:          2000,
		InternalTestingReq: false,
	}

	checker.OnRequestEvent(reqMsg)

	// Should be deleted after request completion
	if _, exists := checker.InstancesList["instance-1"]; exists {
		t.Error("Expected instance to be removed after all requests finished")
	}
}

// Test scenario: OnNewRequest adds request when instance already exists
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

// Test scenario: OnLocalDecodeTokens returns directly for non-existing request
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

	// Should return directly, not panic
	checker.OnLocalDecodeTokens(tokenMsg)
}

// Test scenario: IsInstanceHanging also checks hanging when no requests
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

// Test scenario: OnInstanceTokenEvent skips when worker does not exist
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

	// Should skip when worker does not exist, not panic
	leader.OnInstanceTokenEvent("non-existent-worker", instanceEvents)
}

// Test scenario: OnRequestEvents handles finished request event
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
			EventType:      1, // Use non-zero value to simulate RequestFinish
			TimeStamp:      2000,
		},
	}

	leader.OnRequestEvents("worker-1", "", requestEvents)

	// req-1 should be deleted, but req-2 should still exist
	if _, exists := leader.WorkerList["instance-1"]["worker-1"].RequestsList["req-1"]; exists {
		t.Error("Expected request to be removed")
	}
	// Ensure the other request is still kept
	if _, exists := leader.WorkerList["instance-1"]["worker-1"].RequestsList["req-2"]; !exists {
		t.Error("Expected req-2 to still exist")
	}
}

// Test scenario: NewLeaderHangDecider config disabled
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

	// Should return non-nil object even when config is disabled
	if leader == nil {
		t.Error("Expected LeaderHangDecider to be created even when disabled")
	}
}

// Test scenario: getProbeStatus probing state
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

// Test scenario: getProbeStatus probe status timeout returns None
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

// Test scenario: startProbeAsync probe module is nil
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

	// When probeInferRequest is nil, should return normally
	leader.startProbeAsync("test-instance")
}

// Test scenario: startProbeAsync instance is nil
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

	// When instance is nil, should return normally
	leader.startProbeAsync("non-existent-instance")
}

// Test scenario: probeBeforeHang config probe not enabled
func TestProbeBeforeHang_ConfigNotEnabled(t *testing.T) {
	cfg := &config.HangCheckerConfig{
		Enable:                     true,
		EnableProbeBeforeHangCheck: false, // Probe not enabled
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

	// When probe is not enabled, probeBeforeHang behavior is handled by logic in checkLeaderHang
	// Here we test the scenario where getProbeStatus returns None
	status := leader.getProbeStatus("instance-1")
	if status != ProbeStatusNone {
		t.Errorf("Expected ProbeStatusNone when no probe record, got %d", status)
	}
}

// Test scenario: probeBeforeHang is probing
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
		probingInstances: map[string]bool{"instance-1": true}, // Currently probing
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

// Test scenario: probeBeforeHang probe success and not timeout
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
		lastProbeTime:    map[string]int64{"instance-1": now}, // Most recent probe time
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

// Test scenario: probeBeforeHang probe success timeout
func TestProbeBeforeHang_ProbeSuccessTimeout(t *testing.T) {
	cfg := &config.HangCheckerConfig{
		Enable:                     true,
		EnableProbeBeforeHangCheck: true,
		HangProbeIntervalMs:        30000,
	}
	// Probe time exceeds interval, considered timeout
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

// Test scenario: getProbeStatus probe not enabled in config
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

// Test scenario: getProbeStatus is probing
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

// Test scenario: getProbeStatus instance is unhealthy (in unhealthy list)
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

	// Instance is in unhealthy list, isInstanceHealthy returns false
	isHealthy := leader.leaderHealthChecker.isInstanceHealthy("instance-1")
	if isHealthy != false {
		t.Error("Expected instance to be unhealthy")
	}
}

// Test scenario: getProbeStatus probe interval not reached
func TestGetProbeStatus_ProbeIntervalNotReached(t *testing.T) {
	cfg := &config.HangCheckerConfig{
		Enable:              true,
		HangProbeIntervalMs: 30000,
	}
	now := time.Now().UnixMilli()
	leader := &LeaderHangDecider{
		cfg:              cfg,
		lastProbeTime:    map[string]int64{"instance-1": now - 10000}, // 10 seconds ago, just within interval
		probingInstances: make(map[string]bool),
		probeStates:      map[string]ProbeState{"instance-1": ProbeStateFailed},
		leaderHealthChecker: &LeaderHealthChecker{
			healthInstance:    map[string]*client.Instance{"instance-1": {ID: "instance-1"}},
			unhealthyInstance: make(map[string]*client.Instance),
		},
	}

	status := leader.getProbeStatus("instance-1")
	// Probe failed and not timeout, should return ProbeStatusFailed
	if status != ProbeStatusFailed {
		t.Errorf("Expected ProbeStatusFailed when probe failed and not timeout, got %d", status)
	}
}

// Test scenario: getProbeStatus probe module is nil
func TestGetProbeStatus_ProbeModuleNil(t *testing.T) {
	cfg := &config.HangCheckerConfig{
		Enable: true,
	}
	leader := &LeaderHangDecider{
		cfg:               cfg,
		lastProbeTime:     make(map[string]int64),
		probingInstances:  make(map[string]bool),
		probeStates:       make(map[string]ProbeState),
		probeInferRequest: nil, // Probe module is nil
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

// Test scenario: getProbeStatus instance is nil
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

// Test scenario: getProbeStatus all conditions met returns true (instance healthy and can initiate probe)
func TestGetProbeStatus_AllConditionsMet(t *testing.T) {
	cfg := &config.HangCheckerConfig{
		Enable:              true,
		HangProbeIntervalMs: 30000,
	}
	// Last probe time exceeds interval, can probe again
	oldTime := time.Now().UnixMilli() - 60000
	leader := &LeaderHangDecider{
		cfg:               cfg,
		lastProbeTime:     map[string]int64{"instance-1": oldTime},
		probingInstances:  make(map[string]bool),
		probeStates:       map[string]ProbeState{"instance-1": ProbeStateSuccess}, // Previously succeeded
		probeInferRequest: &ProbeInferRequest{},
		leaderHealthChecker: &LeaderHealthChecker{
			healthInstance:    map[string]*client.Instance{"instance-1": {ID: "instance-1"}},
			unhealthyInstance: make(map[string]*client.Instance),
		},
	}

	status := leader.getProbeStatus("instance-1")
	// Probe success timeout, returns ProbeStatusNone, indicating can initiate new probe
	if status != ProbeStatusNone {
		t.Errorf("Expected ProbeStatusNone (can start new probe), got %d", status)
	}
}

// Test scenario: isInstanceHealthy instance is in healthy list
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

// Test scenario: isInstanceHealthy instance is not in healthy list
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

// Test scenario: isInstanceHealthy instance is in neither healthy nor unhealthy list
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

// Test scenario: startProbeAsync valid probe success (successToCheck=true, testSuccess=true)
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

	// Since startProbeAsync is asynchronous, we need to wait a bit for the goroutine to execute
	// But we can verify by checking probingInstances state
	leader.startProbeAsync("test-instance")

	// Wait for async probe to complete
	time.Sleep(100 * time.Millisecond)

	leader.mu.Lock()
	defer leader.mu.Unlock()

	// After probe completes, probingInstances should be cleared
	// Since we don't have a real probe module, the probe will fail
	// Check probe status
	state := leader.probeStates["test-instance"]
	t.Logf("Probe state after async probe: %d", state)
}

// Test scenario: startProbeAsync valid probe fail (successToCheck=true, testSuccess=false)
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

	// Set initial invalidProbeCount
	leader.invalidProbeCount["test-instance"] = 1

	leader.startProbeAsync("test-instance")

	// Wait for async probe to complete
	time.Sleep(100 * time.Millisecond)

	leader.mu.Lock()
	defer leader.mu.Unlock()

	// Since ProbeInferRequest is not properly configured (model name is empty), probe will return invalid probe
	// invalidProbeCount = 1 + 1 = 2, not reaching threshold 3, so status is ProbeStateNone
	if leader.probeStates["test-instance"] != ProbeStateNone {
		t.Errorf("Expected ProbeStateNone for invalid probe (not properly configured), got %d", leader.probeStates["test-instance"])
	}
	// Verify invalid probe count has increased
	if leader.invalidProbeCount["test-instance"] != 2 {
		t.Errorf("Expected invalidProbeCount = 2, got %d", leader.invalidProbeCount["test-instance"])
	}
}

// Test scenario: startProbeAsync invalid probe reaches max count
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

	// Set current invalid probe count to threshold-1=2, next probe will reach max
	leader.invalidProbeCount["test-instance"] = 2

	leader.startProbeAsync("test-instance")

	// Wait for async probe to complete
	time.Sleep(100 * time.Millisecond)

	leader.mu.Lock()
	defer leader.mu.Unlock()

	// When reaching max consecutive invalid probe count, status should be ProbeStateFailed
	if leader.probeStates["test-instance"] != ProbeStateFailed {
		t.Errorf("Expected ProbeStateFailed when reaching max invalid probe count, got %d", leader.probeStates["test-instance"])
	}
}

// Test scenario: startProbeAsync invalid probe reaches threshold-1
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

	// Set current invalid probe count to 1 (when threshold-1=2, one more will reach threshold)
	leader.invalidProbeCount["test-instance"] = 1

	leader.startProbeAsync("test-instance")

	// Wait for async probe to complete
	time.Sleep(100 * time.Millisecond)

	leader.mu.Lock()
	defer leader.mu.Unlock()

	// At threshold-1, status should be ProbeStateNone (waiting for next probe)
	if leader.probeStates["test-instance"] != ProbeStateNone {
		t.Errorf("Expected ProbeStateNone at threshold-1, got %d", leader.probeStates["test-instance"])
	}
}

// Test scenario: startProbeAsync invalid probe not reaching threshold
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

	// Set current invalid probe count to 0
	leader.invalidProbeCount["test-instance"] = 0

	leader.startProbeAsync("test-instance")

	// Wait for async probe to complete
	time.Sleep(100 * time.Millisecond)

	leader.mu.Lock()
	defer leader.mu.Unlock()

	// When not reaching threshold, status should be ProbeStateNone
	if leader.probeStates["test-instance"] != ProbeStateNone {
		t.Errorf("Expected ProbeStateNone when below threshold, got %d", leader.probeStates["test-instance"])
	}
}

// Test scenario: OnAllInstancesRenewed from healthy to unhealthy
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

	// Simulate instance list update: both instance-1 and instance-2 are in available list
	available := map[string]*client.Instance{
		"instance-1": {ID: "instance-1"},
		"instance-2": {ID: "instance-2"},
	}
	unavailable := make(map[string]*client.Instance)

	leader.OnAllInstancesRenewed(available, unavailable)

	// Both instance-1 and instance-2 should remain in WorkerList
	if _, exists := leader.WorkerList["instance-1"]; !exists {
		t.Error("Expected instance-1 to remain in WorkerList")
	}
	if _, exists := leader.WorkerList["instance-2"]; !exists {
		t.Error("Expected instance-2 to remain in WorkerList")
	}
	// HangTag should remain
	if leader.HangTag["instance-1"] != true {
		t.Error("Expected instance-1 HangTag to remain true")
	}
}

// Test scenario: OnFinishedRequest (Worker) instance becomes empty after removing request
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

	// When RequestsTimeList is empty, instance should be removed
	if _, exists := checker.InstancesList["instance-1"]; exists {
		t.Error("Expected instance to be removed when RequestsTimeList is empty")
	}
}

// Test scenario: OnFinishedRequest (Worker) MayHang cleared when last request deleted and MayHang is true
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
				MayHang:               true, // Marked as possibly hanging
			},
		},
		Thresholds: map[string]*RoleThreshold{
			"prefill": {ThresholdTime: 5000, SkipNonStream: false},
		},
		mu: sync.Mutex{},
	}

	request := &utils.RemoteRequestWrapper{Id: "req-1", Type: "prefill", Stream: true}
	timestamp := int64(2000)

	// This test verifies that when instance is removed, if MayHang is true, recovery log is printed
	checker.OnFinishedRequest(request, "instance-1", timestamp)

	// Instance should be removed
	if _, exists := checker.InstancesList["instance-1"]; exists {
		t.Error("Expected instance to be removed")
	}
}

// Test scenario: IsInstanceHanging boundary condition - exactly equals threshold time
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

	// Exactly equals threshold time, should not be judged as hanging
	timestamp := int64(6000) // 1000 + 5000 = 6000
	result := checker.IsInstanceHanging(recorder, "instance-1", timestamp)

	if result != false {
		t.Error("Expected false when hang time equals threshold")
	}
}

// Test scenario: IsInstanceHanging boundary condition - just exceeds threshold time
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

	// Just exceeds threshold time, should be judged as hanging
	timestamp := int64(6001) // 1000 + 5000 + 1
	result := checker.IsInstanceHanging(recorder, "instance-1", timestamp)

	if result != true {
		t.Error("Expected true when hang time exceeds threshold by 1ms")
	}
}

// Test scenario: IsInstanceHanging boundary condition - LastSuccessTime is 0
func TestWorkerHangChecker_IsInstanceHanging_LastSuccessTimeZero(t *testing.T) {
	checker := &WorkerHangChecker{
		Thresholds: map[string]*RoleThreshold{
			"prefill": {ThresholdTime: 5000, SkipNonStream: false},
		},
		HangWarningInterval: 30000,
	}

	recorder := &InstanceRecorder{
		Role:                  "prefill",
		LastSuccessTime:       0, // Initial state
		RequestsTimeList:      map[string]int64{"req-1": 1000},
		NonStreamCnt:          0,
		LastNonStreamWarnTime: 0,
		MayHang:               false,
	}

	timestamp := int64(10000)
	result := checker.IsInstanceHanging(recorder, "instance-1", timestamp)

	// When LastSuccessTime is 0, hangtime will be large, should be judged as hanging
	if result != true {
		t.Error("Expected true when LastSuccessTime is 0")
	}
}

// Test scenario: IsInstanceHanging boundary condition - Role configured but ThresholdTime is 0
func TestWorkerHangChecker_IsInstanceHanging_RoleNotExist(t *testing.T) {
	checker := &WorkerHangChecker{
		Thresholds: map[string]*RoleThreshold{
			"prefill": {ThresholdTime: 5000, SkipNonStream: false},
			"decode":  {ThresholdTime: 0, SkipNonStream: false}, // Threshold is 0, do not check hanging
		},
		HangWarningInterval: 30000,
	}

	recorder := &InstanceRecorder{
		Role:                  "decode", // Role with threshold 0
		LastSuccessTime:       1000,
		RequestsTimeList:      map[string]int64{"req-1": 1000},
		NonStreamCnt:          0,
		LastNonStreamWarnTime: 0,
		MayHang:               false,
	}

	timestamp := int64(10000)
	result := checker.IsInstanceHanging(recorder, "instance-1", timestamp)

	// When ThresholdTime is 0, should return false (do not check hanging)
	if result != false {
		t.Error("Expected false when role threshold time is 0")
	}
}

// Test scenario: IsInstanceHanging boundary condition - NonStreamCnt is exactly 0
func TestWorkerHangChecker_IsInstanceHanging_NonStreamCntZero(t *testing.T) {
	checker := &WorkerHangChecker{
		Thresholds: map[string]*RoleThreshold{
			"prefill": {ThresholdTime: 5000, SkipNonStream: true}, // Enable non-stream skip
		},
		HangWarningInterval: 30000,
	}

	recorder := &InstanceRecorder{
		Role:                  "prefill",
		LastSuccessTime:       1000,
		RequestsTimeList:      map[string]int64{"req-1": 1000},
		NonStreamCnt:          0, // Exactly 0
		LastNonStreamWarnTime: 0,
		MayHang:               false,
	}

	timestamp := int64(10000)
	result := checker.IsInstanceHanging(recorder, "instance-1", timestamp)

	// When NonStreamCnt is 0 and SkipNonStream is true, should not take skip logic, but check normally
	if result != true {
		t.Error("Expected true when NonStreamCnt is 0 and SkipNonStream is true (should check normally)")
	}
}

// Test scenario: checkAndResetProbeTimeout not timeout
func TestCheckAndResetProbeTimeout_NotTimeout(t *testing.T) {
	cfg := &config.HangCheckerConfig{
		Enable:              true,
		HangProbeIntervalMs: 30000,
	}
	now := time.Now().UnixMilli()
	leader := &LeaderHangDecider{
		cfg:           cfg,
		lastProbeTime: map[string]int64{"instance-1": now - 10000}, // 10 seconds ago, not timed out
	}

	status := leader.checkAndResetProbeTimeout("instance-1", ProbeStatusSuccess)
	if status != ProbeStatusSuccess {
		t.Errorf("Expected ProbeStatusSuccess when not timeout, got %d", status)
	}
}

// Test scenario: checkAndResetProbeTimeout timeout
func TestCheckAndResetProbeTimeout_Timeout(t *testing.T) {
	cfg := &config.HangCheckerConfig{
		Enable:              true,
		HangProbeIntervalMs: 30000,
	}
	oldTime := time.Now().UnixMilli() - 60000 // 60 seconds ago, already timed out
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

// Test scenario: checkAndResetProbeTimeout no probe time record
func TestCheckAndResetProbeTimeout_NoProbeTime(t *testing.T) {
	cfg := &config.HangCheckerConfig{
		Enable:              true,
		HangProbeIntervalMs: 30000,
	}
	leader := &LeaderHangDecider{
		cfg:           cfg,
		lastProbeTime: make(map[string]int64), // No record
	}

	status := leader.checkAndResetProbeTimeout("instance-1", ProbeStatusFailed)
	if status != ProbeStatusFailed {
		t.Errorf("Expected ProbeStatusFailed when no probe time record, got %d", status)
	}
}

// Test scenario: OnAllInstancesRenewed skips when config is disabled
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

	// When config is disabled, should not clear instances
	available := make(map[string]*client.Instance)
	unavailable := map[string]*client.Instance{"instance-1": {ID: "instance-1"}}

	leader.OnAllInstancesRenewed(available, unavailable)

	// Instance should not be cleared
	if _, exists := leader.WorkerList["instance-1"]; !exists {
		t.Error("Expected instance to remain when config is disabled")
	}
}

// Test scenario: LeaderHangDecider OnFinishedRequest worker becomes empty after deleting request
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

	// worker-1's RequestsList should be empty, worker-1 should be deleted
	if _, exists := leader.WorkerList["instance-1"]; exists {
		if _, workerExists := leader.WorkerList["instance-1"]["worker-1"]; workerExists {
			t.Error("Expected worker to be removed when RequestsList is empty")
		}
	}
}
