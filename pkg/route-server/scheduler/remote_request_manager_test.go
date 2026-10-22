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
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"infer-dp-scheduler/cmd/route_server/server/options"
	commoncfg "infer-dp-scheduler/pkg/common/config"
	"infer-dp-scheduler/pkg/common/constant"
	"infer-dp-scheduler/pkg/common/utils"

	"infer-dp-scheduler/pkg/common/health"
	"infer-dp-scheduler/pkg/common/scheduler"
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

		remoteReqMgr.OnRequestEventNew("worker1", &event)
		require.Contains(t, remoteReqMgr.pendingRequests, "worker1")
		require.Contains(t, remoteReqMgr.pendingRequests["worker1"], key)

		remoteReqMgr.OnRequestEventWithNoLock("worker1", &event)
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

		remoteReqMgr.OnRequestEventNew("worker1", &event)
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
				remoteReqMgr.OnRequestEventNew("worker1", &event)
				remoteReqMgr.OnRequestEvent("worker1", &event)
			}(i)
		}
		wg.Wait()

		assert.Empty(t, remoteReqMgr.pendingRequests["worker1"])
		assert.Empty(t, remoteReqMgr.pendingRequests)
	})
}
