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

package health

import (
	"sync"

	"infer-dp-scheduler/pkg/common/constant"
)

type InferMsg struct {
	mutex     sync.RWMutex
	Instances map[string]string `json:"instances"`
	// instance_role -> skip_reason, phase of the instance_role will be skipped when skip_reason not empty
	SkipFlags map[string]string `json:"skip_flags"`
	IsTesting bool              `json:"isFake"`
}

func NewInferMsg(isFake bool, encodeID string, prefillID string, decodeID string) *InferMsg {
	InstanceMap := make(map[string]string)
	InstanceMap[constant.Encode] = encodeID
	InstanceMap[constant.Prefill] = prefillID
	InstanceMap[constant.Decode] = decodeID
	return &InferMsg{
		IsTesting: isFake,
		Instances: map[string]string{
			"encode":  encodeID,
			"prefill": prefillID,
			"decode":  decodeID,
		},
		SkipFlags: map[string]string{},
		mutex:     sync.RWMutex{},
	}
}

func (iM *InferMsg) SkipInferRole(role, msg string) {
	iM.mutex.Lock()
	defer iM.mutex.Unlock()
	iM.SkipFlags[role] = msg
}

func (iM *InferMsg) IsSkipped(role string) (string, bool) {
	iM.mutex.RLock()
	defer iM.mutex.RUnlock()
	msg, isSkipped := iM.SkipFlags[role]
	return msg, isSkipped
}

func (iM *InferMsg) SetInstance(role string, targetInstance string) {
	iM.mutex.Lock()
	defer iM.mutex.Unlock()
	iM.Instances[role] = targetInstance
}

func (iM *InferMsg) GetInstance(role string) string {
	iM.mutex.RLock()
	defer iM.mutex.RUnlock()
	return iM.Instances[role]
}

func (iM *InferMsg) getTargetInstance(requestType string) string {
	iM.mutex.RLock()
	defer iM.mutex.RUnlock()
	return iM.Instances[requestType]
}

func (iM *InferMsg) getPreInstance(requestType string) string {
	iM.mutex.RLock()
	defer iM.mutex.RUnlock()
	if requestType == constant.Encode {
		return ""
	} else if requestType == constant.Prefill {
		return iM.Instances[constant.Encode]
	} else {
		return iM.Instances[constant.Prefill]
	}
}
