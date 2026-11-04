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

package events

import (
	"route-server/pkg/common/health"
	"route-server/pkg/common/logger"
	"route-server/pkg/common/utils"
)

type ServingEventsHandler struct {
	listeners []health.LeaderEventListener
}

func NewServingEventsHandler() *ServingEventsHandler {
	return &ServingEventsHandler{
		listeners: make([]health.LeaderEventListener, 0),
	}
}

func (seh *ServingEventsHandler) RegisterListener(listener health.LeaderEventListener) {
	if listener != nil {
		seh.listeners = append(seh.listeners, listener)
	}
}

func (seh *ServingEventsHandler) DoHandleRequestEvents(events *utils.WorkerEventsRequest) {
	if events == nil {
		logger.Log().Warnf("got nil serving events")
		return
	}

	for _, listener := range seh.listeners {
		if len(events.RequestEvents) > 0 {
			listener.OnRequestEvents(events.WorkerId, events.WorkerBootId, events.RequestEvents)
		}
		if len(events.InstanceEvents) > 0 {
			listener.OnInstanceTokenEvent(events.WorkerId, events.InstanceEvents)
		}
	}
}

func (seh *ServingEventsHandler) HandleMessage(message interface{}) {
	event, ok := message.(*utils.WorkerEventsRequest)
	if !ok {
		logger.Log().Errorf("invalid message type, expected *utils.WorkerEventsRequest, got %T", message)
		return
	}
	if len(event.RequestEvents) != 0 || len(event.InstanceEvents) != 0 {
		logger.Log().Tracef("receive worker event from message center,including %d request events and %d instance events", len(event.RequestEvents), len(event.InstanceEvents))
	}
	seh.DoHandleRequestEvents(event)
}
