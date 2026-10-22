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

package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"infer-dp-scheduler/cmd/route_server/server"
	"infer-dp-scheduler/pkg/common/logger"
)

var (
	sigs = make(chan os.Signal, 1)
)

func main() {
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT)

	// Starting service
	var app = server.App{}
	go func() {
		for {
			sigNum := <-sigs
			switch sigNum {
			case syscall.SIGTERM:
				fallthrough
			case syscall.SIGINT:
				logger.Log().Errorf("Process exit by sig %v", sigNum)
				app.Stop()
				return
			default:
				logger.Log().Infof("recv sig %v", sigNum)
			}
		}
	}()

	err := app.Run()
	if err != nil {
		if logger.Log() != nil {
			logger.Log().Errorf("process run failed, %v", err)
		} else {
			fmt.Printf("process run failed, %v\n", err)
		}
		os.Exit(1)
	}
}
