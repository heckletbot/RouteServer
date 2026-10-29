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

package utils

import (
	"fmt"
	"reflect"
	"sync"

	"infer-dp-scheduler/pkg/common/logger"
)

/*
for a new producer:
1. define a constant topic
2. init this topic when init the message center
3. register and use channel to send message

for a new consumer:
implement Subscriber interface and subscribe
*/

type Subscriber interface {
	HandleMessage(message interface{})
}

type MessageCenter interface {
	InitTopic(topic string) *SingleTopicHandler
	Register(topic string, message interface{}) (chan<- interface{}, error)
	Subscribe(topic string, handler Subscriber) error
	Close()
}

type SingleTopicHandler struct {
	msgChannel chan interface{}
	msgType    reflect.Type
	// each topic has its own mutex, topic will not remove when running so it works
	mutex       sync.RWMutex
	subscribers []Subscriber
}

// a simple message center that manage several single topic
// each topic has a certain type of message
type RegularMessageCenter struct {
	singleTopicMsgCenter map[string]*SingleTopicHandler
	topicMutex           sync.RWMutex
	closeChan            chan struct{}
}

func NewRegularMessageCenter() MessageCenter {
	msgCenter := &RegularMessageCenter{
		singleTopicMsgCenter: make(map[string]*SingleTopicHandler),
		topicMutex:           sync.RWMutex{},
		closeChan:            make(chan struct{}),
	}
	return msgCenter
}

// use this to init topic immediately after initializing the message center
// it allows for a more flexible order of registration and subscription
func (r *RegularMessageCenter) InitTopic(topic string) *SingleTopicHandler {
	r.topicMutex.Lock()
	defer r.topicMutex.Unlock()
	singleMsgCenter := &SingleTopicHandler{
		// Buffer size configurable?
		msgChannel:  make(chan interface{}, 100000),
		subscribers: make([]Subscriber, 0),
		msgType:     nil,
		mutex:       sync.RWMutex{},
	}
	r.singleTopicMsgCenter[topic] = singleMsgCenter
	go r.singleTopicLoop(topic, singleMsgCenter)
	return singleMsgCenter
}

func (r *RegularMessageCenter) getOrInitTopic(topic string) *SingleTopicHandler {
	r.topicMutex.RLock()
	singleTopicMsgCenter, exist := r.singleTopicMsgCenter[topic]
	if exist {
		r.topicMutex.RUnlock()
		return singleTopicMsgCenter
	}
	r.topicMutex.RUnlock()
	return r.InitTopic(topic)
}

// register should input a sampleMessage to decide the topic type
func (r *RegularMessageCenter) Register(topic string, sampleMessage any) (chan<- interface{}, error) {
	singleMsgCenter := r.getOrInitTopic(topic)
	singleMsgCenter.mutex.Lock()
	defer singleMsgCenter.mutex.Unlock()
	sampleType := reflect.TypeOf(sampleMessage)
	if singleMsgCenter.msgType == nil {
		singleMsgCenter.msgType = sampleType
	} else if singleMsgCenter.msgType != sampleType {
		return nil, fmt.Errorf("register failed, topic %v expected type %v, but the sample type is %v", topic, singleMsgCenter.msgType, sampleType)
	}
	return singleMsgCenter.msgChannel, nil
}

// Subscribe module should implement the subscriber interface, the message will send by the HandleMessage
func (r *RegularMessageCenter) Subscribe(topic string, handler Subscriber) error {
	singleMsgCenter := r.getOrInitTopic(topic)
	singleMsgCenter.mutex.Lock()
	defer singleMsgCenter.mutex.Unlock()
	singleMsgCenter.subscribers = append(singleMsgCenter.subscribers, handler)
	return nil
}

func (r *RegularMessageCenter) singleTopicLoop(topic string, singleMsgCenter *SingleTopicHandler) {
	for {
		select {
		case <-r.closeChan:
			return
		case message, ok := <-singleMsgCenter.msgChannel:
			if !ok {
				return
			}
			singleMsgCenter.mutex.RLock()
			if reflect.TypeOf(message) != singleMsgCenter.msgType {
				logger.Log().Warnf("invalid type for topic %v, expect %v, receive %v", topic, singleMsgCenter.msgType, reflect.TypeOf(message))
				continue
			}
			for _, subscriber := range singleMsgCenter.subscribers {
				go subscriber.HandleMessage(message)
			}
			singleMsgCenter.mutex.RUnlock()
		}
	}
}

func (r *RegularMessageCenter) Close() {
	logger.Log().Infof("message center stopping, closing all topic message channels")
	close(r.closeChan)
	r.topicMutex.Lock()
	defer r.topicMutex.Unlock()
	for _, singleMsgCenter := range r.singleTopicMsgCenter {
		singleMsgCenter.mutex.Lock()
		close(singleMsgCenter.msgChannel)
		singleMsgCenter.subscribers = nil
		singleMsgCenter.mutex.Unlock()
	}
	logger.Log().Infof("message center stopped")
}
