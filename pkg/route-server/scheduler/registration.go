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
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"route-server/pkg/common/logger"
	"route-server/pkg/common/scheduler"
)

const (
	RoundRobinAlgoName     = "round-robin"
	ABC6AlgoName           = "abc6"
	LeastTotalLoadAlgoName = "least-total-load"
	APCSchedulerName       = "apc"
	LeastRequestsAlgoName  = "least-requests"
)

type AlreadyRegisterError struct {
	name string
}

func (e AlreadyRegisterError) Error() string {
	return fmt.Sprintf(
		"failed to register scheduler %s because a scheduler with same name is already registered", e.name)
}

type NewSchedulerAlgoFunc func() (scheduler.ScheduleAlgorithm, error)

var (
	registry  = make(map[string]NewSchedulerAlgoFunc)
	lock      sync.RWMutex
	initiated atomic.Bool
)

func Register(name string, creator NewSchedulerAlgoFunc) error {
	lock.Lock()
	defer lock.Unlock()

	if _, exists := registry[name]; exists {
		logger.Log().Warnf("algorithm with same name %s is already registered", name)
		return &AlreadyRegisterError{
			name: name,
		}
	}
	registry[name] = creator
	logger.Log().Infof("loaded algorithm %s", name)
	return nil
}

func GetNewSchedulerAlgo(name string) (scheduler.ScheduleAlgorithm, error) {
	lock.RLock()
	creator, exists := registry[name]
	lock.RUnlock()

	if !exists {
		return nil, errors.New("scheduler not registered: " + name)
	}
	return creator()
}

func RegisteredSchedulerAlgosNames() []string {
	lock.RLock()
	defer lock.RUnlock()

	names := make([]string, 0, len(registry))
	for name := range registry {
		names = append(names, name)
	}
	return names
}

func InitBuiltinScheduleAlgorithms() error {
	if !initiated.CompareAndSwap(false, true) {
		return nil
	}
	err := Register(RoundRobinAlgoName, func() (scheduler.ScheduleAlgorithm, error) { return &RoundRobin{}, nil })
	if err != nil {
		return fmt.Errorf("failed to init builtin round robin scheduler, err: %v", err)
	}
	err = Register(PrintingRandomSchedulerName, func() (scheduler.ScheduleAlgorithm, error) {
		return &TestScheduler{}, nil
	})
	if err != nil {
		return fmt.Errorf("failed to init builtin StatsPrintingRandom scheduler, err: %v", err)
	}
	err = Register(LeastTotalLoadAlgoName, func() (scheduler.ScheduleAlgorithm, error) {
		return &LeastTotalLoad{}, nil
	})
	if err != nil {
		return fmt.Errorf("failed to init builtin least total load scheduler, err: %v", err)
	}
	err = Register(ABC6AlgoName, func() (scheduler.ScheduleAlgorithm, error) {
		return &ABC6{}, nil
	})
	if err != nil {
		return fmt.Errorf("failed to init builtin abc6 load scheduler, err: %v", err)
	}
	err = Register(APCSchedulerName, NewAPCScheduler)
	if err != nil {
		return fmt.Errorf("failed to init builtin APC scheduler, err: %v", err)
	}
	err = Register(LeastRequestsAlgoName, func() (scheduler.ScheduleAlgorithm, error) {
		return &LeastRequests{}, nil
	})
	if err != nil {
		return fmt.Errorf("failed to init builtin least requests scheduler, err: %v", err)
	}
	return nil
}
