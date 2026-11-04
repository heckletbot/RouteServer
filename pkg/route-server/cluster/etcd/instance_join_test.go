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

package etcd

import (
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"

	"route-server/pkg/common/cluster"
	"route-server/pkg/common/config"
)

func TestCheckInstancesExistInState_EmptyInput(t *testing.T) {
	ecs := &EtcdClusterService{}
	err := ecs.checkInstancesExistInState(nil)
	assert.NoError(t, err)

	err = ecs.checkInstancesExistInState(make(map[string]*config.Route))
	assert.NoError(t, err)
}

func TestCheckInstancesExistInState_NilState(t *testing.T) {
	ecs := &EtcdClusterService{
		atomicState: atomic.Value{},
	}
	instances := map[string]*config.Route{
		"instance-1": {Ip: "10.0.0.1", Port: 8000, Role: "prefill"},
	}
	err := ecs.checkInstancesExistInState(instances)
	assert.NoError(t, err)
}

func TestCheckInstancesExistInState_NilInstanceRoutes(t *testing.T) {
	ecs := &EtcdClusterService{
		atomicState: atomic.Value{},
	}
	ecs.atomicState.Store(&cluster.ClusterState{
		InstanceRoutes: nil,
	})
	instances := map[string]*config.Route{
		"instance-1": {Ip: "10.0.0.1", Port: 8000, Role: "prefill"},
	}
	err := ecs.checkInstancesExistInState(instances)
	assert.NoError(t, err)
}
func TestCheckInstancesExistInState_RejectUpdate(t *testing.T) {
	ecs := &EtcdClusterService{
		atomicState: atomic.Value{},
	}
	ecs.atomicState.Store(&cluster.ClusterState{
		InstanceRoutes: map[string]*config.Route{
			"instance-1": {Ip: "10.0.0.1", Port: 8000, Role: "prefill"},
		},
	})

	instances := map[string]*config.Route{
		"instance-1": {Ip: "10.0.0.1", Port: 8000, Role: "prefill"},
	}
	err := ecs.checkInstancesExistInState(instances)
	assert.NoError(t, err)

	instances = map[string]*config.Route{
		"instance-1": {Ip: "10.0.0.2", Port: 8000, Role: "prefill"},
	}
	err = ecs.checkInstancesExistInState(instances)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "instance-1")

	instances = map[string]*config.Route{
		"instance-1": {Ip: "10.0.0.1", Port: 9000, Role: "prefill"},
	}
	err = ecs.checkInstancesExistInState(instances)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "instance-1")

	instances = map[string]*config.Route{
		"instance-1": {Ip: "10.0.0.1", Port: 8000, Role: "decode"},
	}
	err = ecs.checkInstancesExistInState(instances)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "instance-1")
}

func TestCheckInstancesExistInState_MultipleDuplicates(t *testing.T) {
	ecs := &EtcdClusterService{
		atomicState: atomic.Value{},
	}
	ecs.atomicState.Store(&cluster.ClusterState{
		InstanceRoutes: map[string]*config.Route{
			"instance-1": {Ip: "10.0.0.1", Port: 8000, Role: "prefill"},
			"instance-2": {Ip: "10.0.0.2", Port: 8000, Role: "decode"},
		},
	})

	instances := map[string]*config.Route{
		"instance-1": {Ip: "10.0.0.1", Port: 8000, Role: "prefill"},
		"instance-2": {Ip: "10.0.0.2", Port: 8000, Role: "decode"},
	}
	err := ecs.checkInstancesExistInState(instances)
	assert.NoError(t, err)

	instances = map[string]*config.Route{
		"instance-1": {Ip: "10.0.0.1", Port: 8000, Role: "prefill"},
		"instance-3": {Ip: "10.0.0.3", Port: 8000, Role: "encode"},
	}
	err = ecs.checkInstancesExistInState(instances)
	assert.NoError(t, err)
}
