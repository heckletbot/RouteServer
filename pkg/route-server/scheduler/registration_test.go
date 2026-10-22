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

	"infer-dp-scheduler/pkg/common/scheduler"
)

func TestRegister(t *testing.T) {
	err := Register("register_test_1", func() (scheduler.ScheduleAlgorithm, error) {
		return &TestScheduler{}, nil
	})
	if err != nil {
		t.Errorf("failed to register test1, err %v", err)
	}
	algo, err := GetNewSchedulerAlgo("register_test_1")
	if err != nil {
		t.Errorf("get failed")
	}
	if testAlgo, ok := algo.(*TestScheduler); ok {
		if testAlgo.HasInit {
			t.Errorf("registration init the scheduler in get")
		}
	}
}

func TestDuplicateRegister(t *testing.T) {
	err := Register(RoundRobinAlgoName, func() (scheduler.ScheduleAlgorithm, error) {
		return nil, nil
	})
	if err != nil {
		return
	}
	t.Errorf("expect error")
}

func TestInitBuiltinScheduleAlgorithms(t *testing.T) {
	algo, err := GetNewSchedulerAlgo(RoundRobinAlgoName)
	if err != nil {
		t.Errorf("don't expect error")
	}
	if _, ok := algo.(*RoundRobin); !ok {
		t.Errorf("builtin scheduler round-robin doesn't not return RoundRobin algo struct")
	}
}
