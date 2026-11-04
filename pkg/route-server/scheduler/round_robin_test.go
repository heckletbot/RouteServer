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
	"math/rand"
	"reflect"
	"testing"
	"time"

	stats2 "route-server/pkg/common/instance-metrics"
)

var seed = time.Now().UnixNano()
var random = rand.New(rand.NewSource(seed))

func TestRoundRobin_SingleNode(t *testing.T) {
	r := &RoundRobin{}
	err := r.Init(make(map[string]string, 0))
	if err != nil {
		t.Errorf("init error %v", err)
	}
	stats := map[string]stats2.WorkerStats{
		"node1": &stats2.ZeroCopyStats{
			Id:     "node1",
			Role:   "P",
			Status: stats2.Available,
			StaticInfo: map[string]string{
				"info1": "val1",
			},
		},
	}
	err = r.UpdateCluster(stats, nil)
	if err != nil {
		t.Errorf("update error %v", err)
	}
	if r.GetUpdateInterval() > 0 {
		t.Errorf("round-robin return positive interval %v", r.GetUpdateInterval())
	}
	if r.currIndex != 0 {
		t.Errorf("init is not 0, but %v", r.currIndex)
	}

	for i := 0; i < 10; i++ {
		expectRetNum := uint32(random.Intn(5)) + 1
		res, _, err := r.DispatchRequest(nil, nil, nil)
		if err != nil {
			t.Errorf("dispatch failed %v", err)
		}
		expectRes :=
			"node1"

		if !reflect.DeepEqual(res, expectRes) {
			t.Errorf("dispatch return unexpected res, res = %v, expectRes = %v， expectRet = %v", res, expectRes, expectRetNum)
		}
	}
}

func TestRoundRobin_MultipleNode(t *testing.T) {
	r := &RoundRobin{}
	err := r.Init(make(map[string]string, 0))
	if err != nil {
		t.Errorf("init error %v", err)
	}
	stats := map[string]stats2.WorkerStats{
		"node1": &stats2.ZeroCopyStats{
			Id:     "node1",
			Role:   "P",
			Status: stats2.Available,
			StaticInfo: map[string]string{
				"info1": "val1",
			},
		},
		"node2": &stats2.ZeroCopyStats{
			Id:     "node2",
			Role:   "P",
			Status: stats2.Available,
			StaticInfo: map[string]string{
				"info1": "val1",
			},
		},
		"node3": &stats2.ZeroCopyStats{
			Id:     "node3",
			Role:   "P",
			Status: stats2.Available,
			StaticInfo: map[string]string{
				"info1": "val1",
			},
		},
	}
	err = r.UpdateCluster(stats, nil)
	if err != nil {
		t.Errorf("update error %v", err)
	}
	if r.GetUpdateInterval() > 0 {
		t.Errorf("round-robin return positive interval %v", r.GetUpdateInterval())
	}
	if r.currIndex != 0 {
		t.Errorf("init is not 0, but %v", r.currIndex)
	}

	expectRes := []string{
		r.instances[0],
		r.instances[1],
		r.instances[2],
	}

	for i := 0; i < 10; i++ {
		res, _, err := r.DispatchRequest(nil, nil, nil)
		if err != nil {
			t.Errorf("dispatch failed %v", err)
		}
		expectResThisRound := expectRes[i%len(expectRes)]
		if !reflect.DeepEqual(res, expectResThisRound) {
			t.Errorf("dispatch return unexpected res, res = %v, expectRes = %v", res, expectResThisRound)
		}
	}
}

func TestRoundRobin_MultipleNodeAtOnce(t *testing.T) {
	r := &RoundRobin{}
	err := r.Init(make(map[string]string, 0))
	if err != nil {
		t.Errorf("init error %v", err)
	}
	stats := map[string]stats2.WorkerStats{
		"node1": &stats2.ZeroCopyStats{
			Id:     "node1",
			Role:   "P",
			Status: stats2.Available,
			StaticInfo: map[string]string{
				"info1": "val1",
			},
		},
		"node2": &stats2.ZeroCopyStats{
			Id:     "node2",
			Role:   "P",
			Status: stats2.Available,
			StaticInfo: map[string]string{
				"info1": "val1",
			},
		},
		"node3": &stats2.ZeroCopyStats{
			Id:     "node3",
			Role:   "P",
			Status: stats2.Available,
			StaticInfo: map[string]string{
				"info1": "val1",
			},
		},
	}
	err = r.UpdateCluster(stats, nil)
	if err != nil {
		t.Errorf("update error %v", err)
	}
	if r.GetUpdateInterval() > 0 {
		t.Errorf("round-robin return positive interval %v", r.GetUpdateInterval())
	}
	if r.currIndex != 0 {
		t.Errorf("init is not 0, but %v", r.currIndex)
	}

	expectRes := []string{
		r.instances[0], // 2
		r.instances[1], // 2
		r.instances[2], // 3
		r.instances[0], // 1
		r.instances[1], // 4
	}

	for i := 0; i < len(expectRes); i++ {
		res, _, err := r.DispatchRequest(nil, nil, nil)
		if err != nil {
			t.Errorf("dispatch failed %v", err)
		}
		expectResThisRound := expectRes[i%len(expectRes)]
		if !reflect.DeepEqual(res, expectResThisRound) {
			t.Errorf("dispatch return unexpected res, res = %v, expectRes = %v", res, expectResThisRound)
		}
	}
}

func TestRoundRobin_UpdateAddNodes(t *testing.T) {
	r := &RoundRobin{}
	err := r.Init(make(map[string]string, 0))
	if err != nil {
		t.Errorf("init error %v", err)
	}
	initStats := map[string]stats2.WorkerStats{
		"node1": &stats2.ZeroCopyStats{
			Id:     "node1",
			Role:   "P",
			Status: stats2.Available,
			StaticInfo: map[string]string{
				"info1": "val1",
			},
		},
		"node2": &stats2.ZeroCopyStats{
			Id:     "node2",
			Role:   "P",
			Status: stats2.Available,
			StaticInfo: map[string]string{
				"info1": "val1",
			},
		},
	}
	err = r.UpdateCluster(initStats, nil)
	if err != nil {
		t.Errorf("update error %v", err)
	}

	res, _, err := r.DispatchRequest(nil, nil, nil)
	if err != nil {
		t.Errorf("dispatch request error, %v", err)
	}

	expectRes := r.instances[0]
	if !reflect.DeepEqual(res, expectRes) {
		t.Errorf("dispatch return unexpected res, res = %v, expectRes = %v", res, expectRes)
	}

	addNode3Stats := map[string]stats2.WorkerStats{
		"node1": &stats2.ZeroCopyStats{
			Id:     "node1",
			Role:   "P",
			Status: stats2.Available,
			StaticInfo: map[string]string{
				"info1": "val1",
			},
		},
		"node2": &stats2.ZeroCopyStats{
			Id:     "node2",
			Role:   "P",
			Status: stats2.Available,
			StaticInfo: map[string]string{
				"info1": "val1",
			},
		},
		"node3": &stats2.ZeroCopyStats{
			Id:     "node3",
			Role:   "P",
			Status: stats2.Available,
			StaticInfo: map[string]string{
				"info1": "val1",
			},
		},
	}
	err = r.UpdateCluster(addNode3Stats, nil)
	if err != nil {
		t.Errorf("update error %v", err)
	}
	if len(r.instances) != 3 {
		t.Errorf("should be 3 nodes after update")
	}
	expectResAfterNodesChange := []string{
		r.instances[1],
		r.instances[2],
		r.instances[0],
		r.instances[1],
		r.instances[2],
	}

	for i := 0; i < len(expectResAfterNodesChange); i++ {
		res, _, err := r.DispatchRequest(nil, nil, nil)
		if err != nil {
			t.Errorf("dispatch failed %v", err)
		}
		expectResThisRound := expectResAfterNodesChange[i%len(expectResAfterNodesChange)]
		if !reflect.DeepEqual(res, expectResThisRound) {
			t.Errorf("dispatch return unexpected res, res = %v, expectRes = %v", res, expectResThisRound)
		}
	}
}

func TestRoundRobin_UpdateReduceNodes(t *testing.T) {
	r := &RoundRobin{}
	err := r.Init(make(map[string]string, 0))
	if err != nil {
		t.Errorf("init error %v", err)
	}
	initStats := map[string]stats2.WorkerStats{
		"node1": &stats2.ZeroCopyStats{
			Id:     "node1",
			Role:   "P",
			Status: stats2.Available,
			StaticInfo: map[string]string{
				"info1": "val1",
			},
		},
		"node2": &stats2.ZeroCopyStats{
			Id:     "node2",
			Role:   "P",
			Status: stats2.Available,
			StaticInfo: map[string]string{
				"info1": "val1",
			},
		},
	}
	err = r.UpdateCluster(initStats, nil)
	if err != nil {
		t.Errorf("update error %v", err)
	}

	res, _, err := r.DispatchRequest(nil, nil, nil)
	if err != nil {
		t.Errorf("dispatch request failed, %v", err)
	}

	expectRes := r.instances[0]
	if !reflect.DeepEqual(res, expectRes) {
		t.Errorf("dispatch return unexpected res, res = %v, expectRes = %v", res, expectRes)
	}

	newStats := map[string]stats2.WorkerStats{
		"node3": &stats2.ZeroCopyStats{
			Id:     "node3",
			Role:   "P",
			Status: stats2.Available,
			StaticInfo: map[string]string{
				"info1": "val1",
			},
		},
	}

	err = r.UpdateCluster(newStats, nil)
	if err != nil {
		t.Errorf("update error %v", err)
	}

	if len(r.instances) != 1 {
		t.Errorf("instance number is invalid.")
	}

	res, _, err = r.DispatchRequest(nil, nil, nil)
	if err != nil {
		t.Errorf("dispatch request failed, %v", err)
	}

	expectRes = "node3"
	if !reflect.DeepEqual(res, expectRes) {
		t.Errorf("dispatch return unexpected res, res = %v, expectRes = %v", res, expectRes)
	}
}
