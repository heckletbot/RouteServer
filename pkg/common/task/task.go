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

package task

import (
	"sync"
	"time"

	"infer-dp-scheduler/pkg/common/logger"
)

type Task interface {
	Run()
	Stop()
}

type task struct {
	initOnce sync.Once
	name     string
	delay    bool
	ticker   *time.Ticker
	handler  func()
}

// NewTask creates a new task
// name is the task name
// period is the execution period of the task
// delay indicates whether to execute the first task after one period
// handler is the function to be executed
func NewTask(name string, period time.Duration, delay bool, handler func()) Task {
	return &task{
		name:    name,
		ticker:  time.NewTicker(period),
		delay:   delay,
		handler: handler,
	}
}

func (t *task) Run() {
	t.initOnce.Do(func() {
		logger.Log().Infof("task: %s start...", t.name)
		go func() {
			if !t.delay {
				t.handler()
			}

			for range t.ticker.C {
				t.handler()
			}
		}()
	})
}

func (t *task) Stop() {
	t.ticker.Stop()
	logger.Log().Infof("task: %s stop...", t.name)
}
