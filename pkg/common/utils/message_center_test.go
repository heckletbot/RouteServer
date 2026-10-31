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
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"route-server/pkg/common/logger"
)

const testTopic = "test-topic"

type TestMessage struct {
	msgA int
	msgB map[string]struct{}
}

type MockConsumer struct {
	receivedMsg []TestMessage
	done        chan bool
}

func (m *MockConsumer) HandleMessage(message interface{}) {
	logger.Log().Infof("receive message, type is %v, message is: %+v", reflect.TypeOf(message), message.(TestMessage))
	m.receivedMsg = append(m.receivedMsg, message.(TestMessage))
	m.done <- true
	return
}

type MockProducer struct {
	msgChan chan<- any
}

func (p *MockProducer) sendMsg() {
	newMessage := TestMessage{
		msgA: 1,
		msgB: make(map[string]struct{}),
	}
	p.msgChan <- newMessage
}

func TestRegularMessageCenter(t *testing.T) {
	msgCenter := NewRegularMessageCenter()

	msgCenter.InitTopic(testTopic)
	msgChan, err := msgCenter.Register(testTopic, TestMessage{})
	if err != nil {
		t.Fatalf("Failed to register topic: %v", err)
	}
	p := MockProducer{msgChan: msgChan}

	c := MockConsumer{
		done: make(chan bool),
	}
	if err := msgCenter.Subscribe(testTopic, &c); err != nil {
		t.Fatalf("Failed to subscribe to topic: %v", err)
	}

	p.sendMsg()

	select {
	case <-c.done:
		// 1 sec is long enough
	case <-time.After(1 * time.Second):
		t.Fatal("Timeout waiting for message to be processed")
	}

	assert.Equal(t, 1, len(c.receivedMsg))
	// clear
	close(c.done)
	msgCenter.Close()
}

func TestRegularMessageCenter_RegisterWithoutInit(t *testing.T) {
	msgCenter := NewRegularMessageCenter()

	_, err := msgCenter.Register(testTopic, TestMessage{})
	assert.NoErrorf(t, err, "Expected no error when registering without initializing the topic")
	msgCenter.Close()
}

func TestRegularMessageCenter_SubscribeWithoutInit(t *testing.T) {
	msgCenter := NewRegularMessageCenter()

	c := MockConsumer{}
	err := msgCenter.Subscribe(testTopic, &c)
	assert.NoErrorf(t, err, "Expected an error when subscribing without initializing the topic")
	msgCenter.Close()
}

func TestRegularMessageCenter_RegisterWithWrongType(t *testing.T) {
	msgCenter := NewRegularMessageCenter()

	msgCenter.InitTopic(testTopic)

	_, err := msgCenter.Register(testTopic, TestMessage{})

	_, err = msgCenter.Register(testTopic, "wrong type")
	assert.Error(t, err, "Expected an error when registering with a wrong type")

	msgCenter.Close()
}
