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

package pipeline

import (
	"fmt"
	"reflect"
	"sync"
)

type PhaseFactoryFunc func() Phase

var (
	phaseRegistry = make(map[string]PhaseFactoryFunc)
	registryLock  sync.RWMutex
	builtinPhases = []PhaseFactoryFunc{
		NewPreparePhase,
		newSchedulePhase,
		newExecutePhase,
		newInferFailurePhase,
		newInferSuccessPhase,
		NewMetaServicePrepare,
		newDummyPhase,
		newLayerWiseMetaServerErrorPhase,
		newLayerWiseMetaServerSuccessPhase,
		newVllmAscendFailPhase,
	}
)

func initBuiltinPhases() error {
	for _, builtin := range builtinPhases {
		if err := RegisterPhase(builtin); err != nil {
			return err
		}
	}
	return nil
}

func RegisterPhase(phaseFunc PhaseFactoryFunc) error {
	registryLock.Lock()
	defer registryLock.Unlock()

	if phaseFunc == nil {
		return fmt.Errorf("input phase is nil")
	}

	testPhase := phaseFunc()

	if testPhase == nil {
		return fmt.Errorf("invalid phase function, got nil phase")
	}

	phaseType := reflect.TypeOf(testPhase)

	if testPhase.PhaseTypeName() == "" {
		return fmt.Errorf("invalid phase [%s], got empty phase type", phaseType.Name())
	}

	if phaseRegistry[testPhase.PhaseTypeName()] != nil {
		return fmt.Errorf("phase %s already registered", testPhase.PhaseTypeName())
	}

	phaseRegistry[testPhase.PhaseTypeName()] = phaseFunc
	return nil
}

func CreatePhase(typeName string) (Phase, error) {
	registryLock.RLock()
	phaseFactoryFunc, exists := phaseRegistry[typeName]
	registryLock.RUnlock()

	if !exists {
		return nil, fmt.Errorf("phase type [%s] not registered", typeName)
	}

	phase := phaseFactoryFunc()
	if phase == nil {
		return nil, fmt.Errorf("failed to create phase for type [%s], got nil", typeName)
	}

	return phase, nil
}

func GetRegisteredPhaseTypes() []string {
	registryLock.RLock()
	defer registryLock.RUnlock()

	types := make([]string, 0, len(phaseRegistry))
	for name := range phaseRegistry {
		types = append(types, name)
	}
	return types
}
