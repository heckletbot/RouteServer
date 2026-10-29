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

package client

import (
	"infer-dp-scheduler/pkg/common/client/instance-client"
	"infer-dp-scheduler/pkg/common/instance-metrics"
)

type Instance struct {
	ID     string
	Stats  instance_metrics.WorkerStats
	Client instance_client.InstanceClient
	IP     string
	Port   int

	LastError                      error
	ConsecutiveHealthFailed        int32
	ConsecutiveHealthUnreachable   int32
	ConsecutiveHealthSuccess       int32
	FaultMessage                   string
	FaultType                      string
	HealthCheckCountAfterUnhealthy int32
}

func NewInstance(id string, client instance_client.InstanceClient, stats instance_metrics.WorkerStats, ip string, port int) *Instance {
	return &Instance{
		ID:                             id,
		Stats:                          stats,
		Client:                         client,
		ConsecutiveHealthFailed:        0,
		ConsecutiveHealthUnreachable:   0,
		ConsecutiveHealthSuccess:       0,
		FaultMessage:                   "",
		HealthCheckCountAfterUnhealthy: 0,
		IP:                             ip,
		Port:                           port,
	}
}
