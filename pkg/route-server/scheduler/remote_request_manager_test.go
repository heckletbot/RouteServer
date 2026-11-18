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
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"route-server/cmd/route_server/server/options"
	commoncfg "route-server/pkg/common/config"
	"route-server/pkg/common/constant"
	"route-server/pkg/common/utils"

	"route-server/pkg/common/health"
	"route-server/pkg/common/scheduler"
)

func PrepareTestScheduler(t *testing.T) (Manager, scheduler.ScheduleAlgorithm, scheduler.ScheduleAlgorithm) {
	cfg := commoncfg.SchedulersCfg{
		Schedulers: []commoncfg.SchedulerCfg{
			{
				SchedulerName: PrintingRandomSchedulerName,
				InstanceRole:  constant.Prefill,
				Params:        nil,
			},
			{
				SchedulerName: PrintingRandomSchedulerName,
				InstanceRole:  constant.Decode,
				Params:        nil,
			},
		},
		SLOScheduling: &commoncfg.SLOSchedulerCfg{
			SloEnable: false,
		},
		SystemPromptLength: 10,
		RemoteSchedule: &commoncfg.RemoteSchedulerCfg{
			TimeoutMs:         1000,
			CleanUpIntervalMs: 6000,
		},
	}
	pdLink := health.NewPDLinkBlacklistManager(&commoncfg.LinkBlacklistConfig{Enable: false})
	remoteReqMgr := NewRemoteScheduledRequestManager(cfg.RemoteSchedule)
	manager, err := NewManager(&commoncfg.Config{SchedulersCfg: cfg}, nil, nil, pdLink, remoteReqMgr)
	if err != nil {
		t.Errorf("failed to create manager")
	}
	pSchedulerPrt := manager.(*SchedulersManager).roleLookUp[constant.Prefill][constant.SLOTagDefault].Algorithm.(*TestScheduler)
	dSchedulerPtr := manager.(*SchedulersManager).roleLookUp[constant.Decode][constant.SLOTagDefault].Algorithm.(*TestScheduler)
	return manager, pSchedulerPrt, dSchedulerPtr
}

func PrepareEvent(eventType int, id int) utils.RequestEventMsg {
	reqId := "req" + strconv.Itoa(id)
	req := &TestLeastTotalLoadReq{
		reqId:     reqId,
		promptLen: 1024,
	}

	event := utils.RequestEventMsg{
		Request:        utils.WrapRequest(req, false),
		TargetInstance: "p1",
		EventType:      eventType,
	}
	return event
}

func TestRemoteScheduledRequestManager(t *testing.T) {
	t.Run("Add new worker node request", func(t *testing.T) {
		mgr, _, _ := PrepareTestScheduler(t)
		defer mgr.Close()
		event := PrepareEvent(constant.RequestPrefillSuccess, 1)
		key := reqInsKey{
			RequestID:      event.Request.GetRequestId(),
			TargetInstance: event.TargetInstance,
		}

		remoteReqMgr := mgr.(*SchedulersManager).remoteReqMgr

		remoteReqMgr.OnRequestEventNew("worker1", "boot-id", &event)
		require.Contains(t, remoteReqMgr.pendingRequests, "worker1")
		require.Contains(t, remoteReqMgr.pendingRequests["worker1"], key)

		remoteReqMgr.onRequestEventWithNoLock("worker1", &event)
		require.NotContains(t, remoteReqMgr.pendingRequests["worker1"], key)
		require.NotContains(t, remoteReqMgr.pendingRequests, "worker1")

	})

	t.Run("Timeout request should trigger algorithm processing", func(t *testing.T) {
		mgr, _, _ := PrepareTestScheduler(t)
		options.GetConfig().HttpConfig.RequestTimeout = 1
		defer mgr.Close()
		event := PrepareEvent(constant.RequestPrefillSuccess, 1)
		key := reqInsKey{
			RequestID:      event.Request.GetRequestId(),
			TargetInstance: event.TargetInstance,
		}
		remoteReqMgr := mgr.(*SchedulersManager).remoteReqMgr

		event.TimeStamp = time.Now().UnixMilli()

		remoteReqMgr.OnRequestEventNew("worker1", "boot-id", &event)
		require.Contains(t, remoteReqMgr.pendingRequests, "worker1")
		require.Contains(t, remoteReqMgr.pendingRequests["worker1"], key)

		expiredMsg := remoteReqMgr.checkExpiredEvents()
		assert.Equal(t, 0, len(expiredMsg))

		time.Sleep(1000 * time.Millisecond)

		expiredMsg = remoteReqMgr.checkExpiredEvents()
		assert.Equal(t, 1, len(expiredMsg))
		require.NotContains(t, remoteReqMgr.pendingRequests, "worker1")
		require.NotContains(t, remoteReqMgr.pendingRequests["worker1"], key)

		assert.Equal(t, "req1", expiredMsg[0].Request.GetRequestId())
	})

	t.Run("Concurrent operations should be thread-safe", func(t *testing.T) {
		mgr, _, _ := PrepareTestScheduler(t)
		defer mgr.Close()
		remoteReqMgr := mgr.(*SchedulersManager).remoteReqMgr

		var wg sync.WaitGroup
		for i := 0; i < 100; i++ {
			wg.Add(1)
			go func(id int) {
				defer wg.Done()
				event := PrepareEvent(constant.RequestPrefillSuccess, i)
				remoteReqMgr.OnRequestEventNew("worker1", "boot-id", &event)
				remoteReqMgr.OnRequestEvent("worker1", &event)
			}(i)
		}
		wg.Wait()

		assert.Empty(t, remoteReqMgr.pendingRequests["worker1"])
		assert.Empty(t, remoteReqMgr.pendingRequests)
	})
}

func TestCheckWorkerBootId(t *testing.T) {
	t.Run("empty bootId should return false and not record", func(t *testing.T) {
		mgr := NewRemoteScheduledRequestManager(&commoncfg.RemoteSchedulerCfg{})
		defer mgr.Close()

		mgr.mu.Lock()
		result := mgr.setWorkerBootIdIfChanged("worker1", "")
		mgr.mu.Unlock()

		assert.False(t, result, "empty bootId should return false")
		mgr.mu.RLock()
		_, exists := mgr.workerBootIds["worker1"]
		mgr.mu.RUnlock()
		assert.False(t, exists, "empty bootId should not be recorded")
	})

	t.Run("new worker first bootId report should return true (bootId changed)", func(t *testing.T) {
		mgr := NewRemoteScheduledRequestManager(&commoncfg.RemoteSchedulerCfg{})
		defer mgr.Close()

		mgr.mu.Lock()
		result := mgr.setWorkerBootIdIfChanged("worker1", "boot-abc-123")
		mgr.mu.Unlock()

		assert.True(t, result, "new worker first bootId report should return true")
		mgr.mu.RLock()
		assert.Equal(t, "boot-abc-123", mgr.workerBootIds["worker1"])
		mgr.mu.RUnlock()
	})

	t.Run("same bootId should return false", func(t *testing.T) {
		mgr := NewRemoteScheduledRequestManager(&commoncfg.RemoteSchedulerCfg{})
		defer mgr.Close()

		mgr.mu.Lock()
		mgr.setWorkerBootIdIfChanged("worker1", "boot-abc-123")
		result := mgr.setWorkerBootIdIfChanged("worker1", "boot-abc-123")
		mgr.mu.Unlock()

		assert.False(t, result, "same bootId should return false")
	})

	t.Run("bootId change should return true indicating worker restart", func(t *testing.T) {
		mgr := NewRemoteScheduledRequestManager(&commoncfg.RemoteSchedulerCfg{})
		defer mgr.Close()

		mgr.mu.Lock()
		mgr.setWorkerBootIdIfChanged("worker1", "boot-abc-123")
		result := mgr.setWorkerBootIdIfChanged("worker1", "boot-xyz-789")
		mgr.mu.Unlock()

		assert.True(t, result, "bootId change should return true")
		mgr.mu.RLock()
		assert.Equal(t, "boot-xyz-789", mgr.workerBootIds["worker1"], "should update to new bootId")
		mgr.mu.RUnlock()
	})

	t.Run("multiple workers track bootId independently", func(t *testing.T) {
		mgr := NewRemoteScheduledRequestManager(&commoncfg.RemoteSchedulerCfg{})
		defer mgr.Close()

		mgr.mu.Lock()
		mgr.setWorkerBootIdIfChanged("worker1", "boot-1")
		mgr.setWorkerBootIdIfChanged("worker2", "boot-2")
		result1 := mgr.setWorkerBootIdIfChanged("worker1", "boot-1-new")
		result2 := mgr.setWorkerBootIdIfChanged("worker2", "boot-2")
		mgr.mu.Unlock()

		assert.True(t, result1, "worker1 bootId change should return true")
		assert.False(t, result2, "worker2 bootId unchanged should return false")
	})
}

func TestOnRequestEventWithBootIdChange(t *testing.T) {
	t.Run("empty bootId should not trigger cleanup, pendingRequests unaffected", func(t *testing.T) {
		mgr, pScheduler, _ := PrepareTestScheduler(t)
		defer mgr.Close()
		s := mgr.(*SchedulersManager)

		ctx := &Context{
			Algorithm: pScheduler,
		}
		s.insIdLookup["p1"] = ctx

		event := PrepareEvent(constant.RequestPrefillSuccess, 1)
		event.TimeStamp = time.Now().UnixMilli()
		remoteReqMgr := s.remoteReqMgr

		remoteReqMgr.OnRequestEventNew("worker1", "boot-abc", &event)
		require.Len(t, remoteReqMgr.pendingRequests["worker1"], 1)

		// Empty bootId: CheckWorkerBootId returns false, clusterService is nil so OnRequestEventWithNoLock is not called
		s.OnRequestEvent("worker1", "", []*utils.RequestEventMsg{&event})

		// pendingRequests still retains previous request records (neither cleaned up nor removed)
		assert.Contains(t, remoteReqMgr.pendingRequests, "worker1")
		assert.Len(t, remoteReqMgr.pendingRequests["worker1"], 1)

		// Clean up test residuals
		remoteReqMgr.CleanUpAllEventsByWorkerID("worker1")
	})

	t.Run("same bootId should not trigger cleanup, pendingRequests unaffected", func(t *testing.T) {
		mgr, pScheduler, _ := PrepareTestScheduler(t)
		defer mgr.Close()
		s := mgr.(*SchedulersManager)

		ctx := &Context{
			Algorithm: pScheduler,
		}
		s.insIdLookup["p1"] = ctx

		event := PrepareEvent(constant.RequestPrefillSuccess, 1)
		event.TimeStamp = time.Now().UnixMilli()
		remoteReqMgr := s.remoteReqMgr

		remoteReqMgr.mu.Lock()
		remoteReqMgr.setWorkerBootIdIfChanged("worker1", "boot-abc")
		remoteReqMgr.mu.Unlock()

		remoteReqMgr.OnRequestEventNew("worker1", "boot-abc", &event)
		require.Len(t, remoteReqMgr.pendingRequests["worker1"], 1)

		// Same bootId: CheckWorkerBootId returns false, clusterService is nil so OnRequestEventWithNoLock is not called
		s.OnRequestEvent("worker1", "boot-abc", []*utils.RequestEventMsg{&event})

		// pendingRequests still retains previous request records
		assert.Contains(t, remoteReqMgr.pendingRequests, "worker1")
		assert.Len(t, remoteReqMgr.pendingRequests["worker1"], 1)

		// Clean up test residuals
		remoteReqMgr.CleanUpAllEventsByWorkerID("worker1")
	})

	t.Run("bootId change should clean up stale requests", func(t *testing.T) {
		mgr, pScheduler, _ := PrepareTestScheduler(t)
		defer mgr.Close()
		s := mgr.(*SchedulersManager)

		ctx := &Context{
			Algorithm: pScheduler,
		}
		s.insIdLookup["p1"] = ctx

		event1 := PrepareEvent(constant.RequestPrefillSuccess, 1)
		event1.TimeStamp = time.Now().UnixMilli()
		event2 := PrepareEvent(constant.RequestPrefillSuccess, 2)
		event2.TimeStamp = time.Now().UnixMilli()
		remoteReqMgr := s.remoteReqMgr

		remoteReqMgr.mu.Lock()
		remoteReqMgr.setWorkerBootIdIfChanged("worker1", "boot-old")
		remoteReqMgr.mu.Unlock()

		remoteReqMgr.OnRequestEventNew("worker1", "boot-old", &event1)
		remoteReqMgr.OnRequestEventNew("worker1", "boot-old", &event2)
		require.Len(t, remoteReqMgr.pendingRequests["worker1"], 2)

		newEvent := PrepareEvent(constant.RequestPrefillSuccess, 3)
		newEvent.TimeStamp = time.Now().UnixMilli()

		s.OnRequestEvent("worker1", "boot-new", []*utils.RequestEventMsg{&newEvent})

		assert.NotContains(t, remoteReqMgr.pendingRequests, "worker1")

		remoteReqMgr.mu.RLock()
		assert.Equal(t, "boot-new", remoteReqMgr.workerBootIds["worker1"])
		remoteReqMgr.mu.RUnlock()
	})

	t.Run("bootId change with no stale requests should not error", func(t *testing.T) {
		mgr, pScheduler, _ := PrepareTestScheduler(t)
		defer mgr.Close()
		s := mgr.(*SchedulersManager)

		ctx := &Context{
			Algorithm: pScheduler,
		}
		s.insIdLookup["p1"] = ctx

		remoteReqMgr := s.remoteReqMgr
		remoteReqMgr.mu.Lock()
		remoteReqMgr.setWorkerBootIdIfChanged("worker1", "boot-old")
		remoteReqMgr.mu.Unlock()

		newEvent := PrepareEvent(constant.RequestPrefillSuccess, 1)
		newEvent.TimeStamp = time.Now().UnixMilli()

		s.OnRequestEvent("worker1", "boot-new", []*utils.RequestEventMsg{&newEvent})

		assert.NotContains(t, remoteReqMgr.pendingRequests, "worker1")
	})

	t.Run("different workers bootId tracked independently", func(t *testing.T) {
		mgr, pScheduler, _ := PrepareTestScheduler(t)
		defer mgr.Close()
		s := mgr.(*SchedulersManager)

		ctx := &Context{
			Algorithm: pScheduler,
		}
		s.insIdLookup["p1"] = ctx

		remoteReqMgr := s.remoteReqMgr

		remoteReqMgr.mu.Lock()
		remoteReqMgr.setWorkerBootIdIfChanged("worker1", "boot-w1-old")
		remoteReqMgr.mu.Unlock()

		event1 := PrepareEvent(constant.RequestPrefillSuccess, 1)
		event1.TimeStamp = time.Now().UnixMilli()
		remoteReqMgr.OnRequestEventNew("worker1", "boot-w1-old", &event1)
		require.Len(t, remoteReqMgr.pendingRequests["worker1"], 1)

		remoteReqMgr.mu.Lock()
		remoteReqMgr.setWorkerBootIdIfChanged("worker2", "boot-w2-old")
		remoteReqMgr.mu.Unlock()

		event2 := PrepareEvent(constant.RequestPrefillSuccess, 2)
		event2.TimeStamp = time.Now().UnixMilli()
		remoteReqMgr.OnRequestEventNew("worker2", "boot-w2-old", &event2)
		require.Len(t, remoteReqMgr.pendingRequests["worker2"], 1)

		newEvent := PrepareEvent(constant.RequestPrefillSuccess, 3)
		newEvent.TimeStamp = time.Now().UnixMilli()
		s.OnRequestEvent("worker1", "boot-w1-new", []*utils.RequestEventMsg{&newEvent})

		assert.NotContains(t, remoteReqMgr.pendingRequests, "worker1")
		assert.Contains(t, remoteReqMgr.pendingRequests, "worker2")
		assert.Len(t, remoteReqMgr.pendingRequests["worker2"], 1)

		remoteReqMgr.CleanUpAllEventsByWorkerID("worker2")
	})

	t.Run("bootId change should clean up correct number of worker requests", func(t *testing.T) {
		mgr, pScheduler, _ := PrepareTestScheduler(t)
		defer mgr.Close()
		s := mgr.(*SchedulersManager)

		ctx := &Context{
			Algorithm: pScheduler,
		}
		s.insIdLookup["p1"] = ctx

		remoteReqMgr := s.remoteReqMgr
		remoteReqMgr.mu.Lock()
		remoteReqMgr.setWorkerBootIdIfChanged("worker1", "boot-old")
		remoteReqMgr.mu.Unlock()

		// Add 3 stale requests
		for i := 1; i <= 3; i++ {
			e := PrepareEvent(constant.RequestPrefillSuccess, i)
			e.TimeStamp = time.Now().UnixMilli()
			remoteReqMgr.OnRequestEventNew("worker1", "boot-old", &e)
		}
		require.Len(t, remoteReqMgr.pendingRequests["worker1"], 3)

		newEvent := PrepareEvent(constant.RequestPrefillSuccess, 4)
		newEvent.TimeStamp = time.Now().UnixMilli()
		s.OnRequestEvent("worker1", "boot-new", []*utils.RequestEventMsg{&newEvent})

		assert.NotContains(t, remoteReqMgr.pendingRequests, "worker1")
	})
}
