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

package scheduler

import (
	"testing"

	"infer-dp-scheduler/pkg/common/config"
	"infer-dp-scheduler/pkg/common/constant"
	"infer-dp-scheduler/pkg/common/instance-metrics"
	"infer-dp-scheduler/pkg/common/utils"
)

func initSLOManagerNormal() (*SLOManager, error) {
	cfg := config.SchedulersCfg{
		SLOScheduling: &config.SLOSchedulerCfg{
			SloEnable:                true,
			SloFallbackEnable:        false,
			SloGroupHealthyThreshold: 1,
			SloManager: []config.SloCfg{
				{
					Tag: "tag1",
					SloIntervals: utils.SLOInterval{
						PromptStrLengthMax: "100",
					},
				},
				{
					Tag: "tag2",
					SloIntervals: utils.SLOInterval{
						PromptStrLengthMax: "10000",
					},
				},
			},
		},
		Schedulers: []config.SchedulerCfg{
			{
				InstanceRole: constant.Prefill,
			},
			{
				InstanceRole: constant.Decode,
			},
		},
	}
	sloManager, err := newSLOManager(&cfg)
	if err != nil {
		return nil, err
	}

	availableInsMap := map[string]map[string]instance_metrics.WorkerStats{
		constant.Prefill: {
			"p_node1": &instance_metrics.ZeroCopyStats{
				Id:     "p_node1",
				Role:   constant.Prefill,
				Status: instance_metrics.Available,
				SLOTag: "tag1",
				StaticInfo: map[string]string{
					"info1": "val1",
				},
			},
			"p_node2": &instance_metrics.ZeroCopyStats{
				Id:     "p_node2",
				Role:   constant.Prefill,
				Status: instance_metrics.Available,
				SLOTag: "tag2",
				StaticInfo: map[string]string{
					"info2": "val2",
				},
			},
			"p_node3": &instance_metrics.ZeroCopyStats{
				Id:     "p_node3",
				Role:   constant.Prefill,
				Status: instance_metrics.Available,
				StaticInfo: map[string]string{
					"info3": "val3",
				},
			},
		},
		constant.Decode: {
			"d_node1": &instance_metrics.ZeroCopyStats{
				Id:     "d_node1",
				Role:   constant.Decode,
				Status: instance_metrics.Available,
				SLOTag: constant.SLOTagDefault,
				StaticInfo: map[string]string{
					"info1": "val1",
				},
			},
			"d_node2": &instance_metrics.ZeroCopyStats{
				Id:     "d_node2",
				Role:   constant.Decode,
				Status: instance_metrics.Available,
				SLOTag: constant.SLOTagDefault,
				StaticInfo: map[string]string{
					"info2": "val2",
				},
			},
		},
	}
	sloManager.UpdateCluster(availableInsMap)

	return sloManager, nil
}

func TestChooseSLOTagNormal(t *testing.T) {
	sloManager, err := initSLOManagerNormal()
	if err != nil {
		t.Errorf("Expected no error, but got: %v", err)
	}

	if len(sloManager.roleLookUp[constant.Prefill].SLOGroups) != 3 {
		t.Errorf("Expected sloGroup len is 3, but got: %v", len(sloManager.roleLookUp[constant.Prefill].SLOGroups))
	}

	mockRequest := &TestReq{
		reqId:       "testRequest",
		reqType:     constant.Prefill,
		sloInterval: &utils.SLOInterval{},
	}
	var chosenTag string

	mockRequest.sloInterval.PromptStrLengthMax = "50"
	chosenTag, err = sloManager.ChooseSLOTag(mockRequest, nil)
	if err != nil {
		t.Errorf("Expected no error, but got: %v", err)
	}
	if chosenTag != "tag1" {
		t.Errorf("Expected chosenTag to be 'tag1', but got '%s'", chosenTag)
	}

	mockRequest.sloInterval.PromptStrLengthMax = "1000"
	chosenTag, err = sloManager.ChooseSLOTag(mockRequest, nil)
	if err != nil {
		t.Errorf("Expected no error, but got: %v", err)
	}
	if chosenTag != "tag2" {
		t.Errorf("Expected chosenTag to be 'tag2', but got '%s'", chosenTag)
	}

	mockRequest.sloInterval.PromptStrLengthMax = constant.Inf
	chosenTag, err = sloManager.ChooseSLOTag(mockRequest, nil)
	if err != nil {
		t.Errorf("Expected no error, but got: %v", err)
	}
	if chosenTag != "default" {
		t.Errorf("Expected chosenTag to be 'default', but got '%s'", chosenTag)
	}
}
