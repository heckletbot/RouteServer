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
	"container/list"
	"sync"
	"time"

	"infer-dp-scheduler/cmd/route_server/server/options"
	"infer-dp-scheduler/pkg/common/config"
	"infer-dp-scheduler/pkg/common/constant"
	"infer-dp-scheduler/pkg/common/utils"
)

type reqInsKey struct {
	RequestID      string
	TargetInstance string
}

type requestEntry struct {
	reqID          string
	workerID       string
	targetInstance string
	insertTime     int64
	msg            *utils.RequestEventMsg
}

type RemoteScheduledRequestManager struct {
	cfg *config.RemoteSchedulerCfg
	mu  sync.RWMutex

	// 1. <WorkerID, <RequestID, *list.Element>>
	// Provides O(1) lookup for quick removal upon receiving a response.
	pendingRequests map[string]map[reqInsKey]*list.Element

	// 2. Time-Ordered Linked List: Front is the oldest request, Back is the newest.
	// Enables O(1) timeout check by only examining the list's front.
	timeList *list.List

	// 3. <WorkerID, bootId>
	// Tracks the bootId for each worker to detect worker restarts.
	workerBootIds map[string]string

	stopCh chan struct{}
}

func NewRemoteScheduledRequestManager(cfg *config.RemoteSchedulerCfg) *RemoteScheduledRequestManager {
	mgr := &RemoteScheduledRequestManager{
		mu:              sync.RWMutex{},
		pendingRequests: make(map[string]map[reqInsKey]*list.Element),
		timeList:        list.New(),
		workerBootIds:   make(map[string]string),
		stopCh:          make(chan struct{}),
		cfg:             cfg,
	}
	return mgr
}

func (m *RemoteScheduledRequestManager) OnRequestEvent(workerID string, msg *utils.RequestEventMsg) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.onRequestEventWithNoLock(workerID, msg)
}

func (m *RemoteScheduledRequestManager) onRequestEventWithNoLock(workerID string, msg *utils.RequestEventMsg) {
	if msg.EventType == constant.RequestNew {
		return
	}

	key := reqInsKey{
		RequestID:      msg.Request.GetRequestId(),
		TargetInstance: msg.TargetInstance,
	}
	if reqs, ok := m.pendingRequests[workerID]; ok {
		if elem, found := reqs[key]; found {
			m.timeList.Remove(elem)
			delete(reqs, key)
			if len(reqs) == 0 {
				delete(m.pendingRequests, workerID)
			}
		}
		return
	}
}

// Record event if it's new
func (m *RemoteScheduledRequestManager) OnRequestEventNew(workerID string, workerBootId string, msg *utils.RequestEventMsg) {
	reqID := msg.Request.GetRequestId()
	key := reqInsKey{
		RequestID:      msg.Request.GetRequestId(),
		TargetInstance: msg.TargetInstance,
	}
	entry := &requestEntry{
		reqID:          reqID,
		workerID:       workerID,
		targetInstance: msg.TargetInstance,
		insertTime:     msg.TimeStamp,
		msg:            msg,
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	elem := m.timeList.PushBack(entry)

	if _, exists := m.pendingRequests[workerID]; !exists {
		m.pendingRequests[workerID] = make(map[reqInsKey]*list.Element)
	}

	if _, exists := m.pendingRequests[workerID][key]; exists {
		elemToDelete := m.pendingRequests[workerID][key]
		m.timeList.Remove(elemToDelete)
	}

	m.pendingRequests[workerID][key] = elem
}

func (m *RemoteScheduledRequestManager) checkExpiredEvents() []*utils.RequestEventMsg {
	now := time.Now().UnixMilli()
	reqTimeout := time.Duration(options.GetConfig().HttpConfig.RequestTimeout) * time.Second
	deadline := now - reqTimeout.Milliseconds()

	var expiredEvents []*utils.RequestEventMsg

	m.mu.Lock()
	defer m.mu.Unlock()

	// Iterate from the list front (oldest request)
	for {
		elem := m.timeList.Front()
		if elem == nil {
			break
		}

		entry, _ := elem.Value.(*requestEntry)

		// If the oldest request is not expired, none are, then break
		if entry.insertTime > deadline {
			break
		}

		m.timeList.Remove(elem)
		if reqs, ok := m.pendingRequests[entry.workerID]; ok {
			delete(reqs, reqInsKey{
				RequestID:      entry.reqID,
				TargetInstance: entry.targetInstance,
			})
			if len(reqs) == 0 {
				delete(m.pendingRequests, entry.workerID)
			}
		}
		expiredEvents = append(expiredEvents, entry.msg)
	}

	return expiredEvents
}

func (m *RemoteScheduledRequestManager) GetAllEventsByWorkerID(workerID string) []*utils.RequestEventMsg {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if _, ok := m.pendingRequests[workerID]; !ok {
		return nil
	}

	var events []*utils.RequestEventMsg
	for _, elem := range m.pendingRequests[workerID] {
		events = append(events, elem.Value.(*requestEntry).msg)
	}
	return events
}

func (m *RemoteScheduledRequestManager) CleanUpAllEventsByWorkerID(workerID string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.cleanUpAllEventsByWorkerIDWithNoLock(workerID)
}

// cleanUpAllEventsByWorkerIDWithNoLock removes all pending requests for a given worker.
// The caller must hold m.mu lock.
func (m *RemoteScheduledRequestManager) cleanUpAllEventsByWorkerIDWithNoLock(workerID string) {
	if _, ok := m.pendingRequests[workerID]; !ok {
		return
	}

	delete(m.pendingRequests, workerID)
}

// GetAllEventsByWorkerIDWithLock returns all pending events for a given worker.
// The caller must hold m.mu lock.
func (m *RemoteScheduledRequestManager) GetAllEventsByWorkerIDWithLock(workerID string) []*utils.RequestEventMsg {
	if _, ok := m.pendingRequests[workerID]; !ok {
		return nil
	}

	var events []*utils.RequestEventMsg
	for _, elem := range m.pendingRequests[workerID] {
		events = append(events, elem.Value.(*requestEntry).msg)
	}
	return events
}

// setWorkerBootIdIfChanged checks if the worker's bootId has changed.
// If the worker is new or has the same bootId, returns false.
// If the bootId has changed (worker restarted), returns true.
// The caller must hold m.mu lock.
func (m *RemoteScheduledRequestManager) setWorkerBootIdIfChanged(workerId, bootId string) bool {
	if bootId == "" {
		return false
	}
	if existingBootId, ok := m.workerBootIds[workerId]; ok && existingBootId == bootId {
		return false
	}
	m.workerBootIds[workerId] = bootId
	return true
}

func (m *RemoteScheduledRequestManager) ClearAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pendingRequests = make(map[string]map[reqInsKey]*list.Element)
	m.timeList = list.New()
	m.workerBootIds = make(map[string]string)
}

func (m *RemoteScheduledRequestManager) GetAllRemoteWorkers() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var workers = make([]string, 0)
	for workerId, _ := range m.pendingRequests {
		workers = append(workers, workerId)
	}
	return workers
}

func (m *RemoteScheduledRequestManager) Close() {
	close(m.stopCh)
}
