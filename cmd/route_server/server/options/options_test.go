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

package options

import (
	"reflect"
	"testing"

	config2 "route-server/pkg/common/config"
	"route-server/pkg/common/logger"
)

func Test_loadFromFile(t *testing.T) {
	var config config2.Config
	err := loadFromFile(&config, "./route-server-test.yaml")
	if err != nil {
		t.Errorf("failed to parse config")
	}
	expectServerCfg := config2.ServerConfig{
		Addr:     "127.0.0.1",
		HttpPort: "49876",
		Name:     "",
	}
	if !reflect.DeepEqual(expectServerCfg, config.ServerConfig) {
		t.Errorf("failed to parse server as expected, expect %v, actual %v", expectServerCfg, config.ServerConfig)
	}

	expectLogCfg := logger.Config{
		FileWriterCfg: logger.FileWriterCfg{
			Enabled:     true,
			Filename:    "/path/to/logs",
			MaxSize:     1024,
			MaxAge:      1,
			MaxBackups:  10,
			LocalTime:   false,
			Compress:    true,
			WriteFormat: "",
		},
		Level: "info",
	}
	if !reflect.DeepEqual(expectLogCfg, config.LogConfig) {
		t.Errorf("failed to parse log as expected, expect %v, actual %v", expectLogCfg, config.LogConfig)
	}

	expectHttpCfg := config2.HttpConfig{
		DialerTimeout:       30,
		DialerKeepAlive:     30,
		RequestTimeout:      5,
		MaxIdleConns:        10000,
		MaxIdleConnsPerHost: 2000,
		IdleConnTimeout:     10,
	}
	if !reflect.DeepEqual(expectHttpCfg, config.HttpConfig) {
		t.Errorf("failed to parse http log as expected, expect %v, actual %v", expectHttpCfg, config.HttpConfig)
	}

	expectInstanceCfg := config2.InstanceManagerCfg{
		InstanceClientType: "vllm",
		RouteTable: config2.RouteTable{
			InstanceRoutes: map[string]config2.Route{
				"instance-01": {
					Ip:   "127.0.0.1",
					Port: 29876,
					Role: "prefill",
					StaticInfos: map[string]string{
						"api-server-count": "1",
					},
				},
				"instance-02": {
					Ip:   "127.0.0.1",
					Port: 29877,
					Role: "decode",
					StaticInfos: map[string]string{
						"api-server-count": "1",
					},
				},
			},
		},
	}
	if !reflect.DeepEqual(expectInstanceCfg, config.InstanceManagerCfg) {
		t.Errorf("failed to parse instances as expected, expect %v, actual %v", expectInstanceCfg, config.InstanceManagerCfg)
	}

	expectSchedulerCfg := config2.SchedulersCfg{
		Schedulers: []config2.SchedulerCfg{
			{
				SchedulerName: "round-robin",
				InstanceRole:  "prefill",
				Params:        nil,
			},
			{
				SchedulerName: "round-robin",
				InstanceRole:  "decode",
				Params: map[string]string{
					"update-interval-ms": "-1",
					"needStats":          "vllm:num_requests_running,vllm:num_requests_waiting",
				},
			},
		},
		SystemPromptLength: 100,
	}
	if !reflect.DeepEqual(expectSchedulerCfg, config.SchedulersCfg) {
		t.Errorf("failed to parse schedulers as expected, expect %v, actual %v", expectSchedulerCfg, config.SchedulersCfg)
	}

	expectMonitorCfg := config2.MonitorCfg{
		MonitorClientType: "none",
	}
	if !reflect.DeepEqual(expectMonitorCfg, config.MonitorCfg) {
		t.Errorf("failed to parse monitor as expected, expect %v, actual %v", expectMonitorCfg, config.MonitorCfg)
	}

	expectMetricsCfg := config2.MetricsCfg{
		Enable:   true,
		Duration: 60,
	}
	if !reflect.DeepEqual(expectMetricsCfg, config.MetricsCfg) {
		t.Errorf("failed to parse metrics as expected, expect %v, actual %v", expectMetricsCfg, config.MetricsCfg)
	}
}
