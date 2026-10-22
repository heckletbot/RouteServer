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

package constant

var ValidRoleMap = map[string]interface{}{
	Encode:  nil,
	Prefill: nil,
	Decode:  nil,
	MOE:     nil,
}

var NeedHandleReqRoleMap = map[string]interface{}{
	Encode:  nil,
	Prefill: nil,
	Decode:  nil,
}

var NoCollectMetricsRoleMap = map[string]interface{}{
	MOE: nil,
}

var ReqFailedEventMap = map[string]int{
	Prefill: RequestPrefillFailed,
	Decode:  RequestDecodeFailed,
	Encode:  RequestEncodeFailed,
}

var ReqSuccessEventMap = map[string]int{
	Prefill: RequestPrefillSuccess,
	Decode:  RequestDecodeSuccess,
	Encode:  RequestEncodeSuccess,
}

var PDStage = []string{StagePrepare, StagePrefill, StageDecode}
var EPDStage = []string{StagePrepare, StageEncode, StagePrefill, StageDecode}
var HybridStage = []string{StagePrepare, StageDecode}
var EHybridStage = []string{StagePrepare, StageEncode, StageDecode}

var FailureRoleStageMap = map[string]string{
	Prefill: StagePrefill,
	Decode:  StageDecode,
	Encode:  StageEncode,
}

var ValidInferModeMap = map[string]interface{}{
	PDMode:           nil,
	EPDMode:          nil,
	PDAMMode:         nil,
	HybridMode:       nil,
	EHybridMode:      nil,
	LayerwiseMode:    nil,
	LayerwiseMMMode:  nil,
	MetaServerMode:   nil,
	MetaServerMMMode: nil,
}

var StageChain = map[string][]string{
	PDMode:      PDStage,
	EPDMode:     EPDStage,
	PDAMMode:    PDStage,
	HybridMode:  HybridStage,
	EHybridMode: EHybridStage,
}

var StageMap = map[string]map[string]interface{}{
	PDMode:      PDStageMap,
	EPDMode:     EPDStageMap,
	PDAMMode:    PDStageMap,
	HybridMode:  HybridStageMap,
	EHybridMode: EHybridStageMap,
}

var ReqSuccessEvents = mapValToSlice[string, int](ReqSuccessEventMap)
var ReqFailedEvents = mapValToSlice[string, int](ReqFailedEventMap)
var PDStageMap = sliceToKeyMap[string](PDStage)
var EPDStageMap = sliceToKeyMap[string](EPDStage)
var HybridStageMap = sliceToKeyMap[string](HybridStage)
var EHybridStageMap = sliceToKeyMap[string](EHybridStage)

func mapValToSlice[K comparable, V any](givenMap map[K]V) []V {
	slice := make([]V, 0, len(givenMap))
	for _, v := range givenMap {
		slice = append(slice, v)
	}
	return slice
}

func sliceToKeyMap[K comparable](givenSlice []K) map[K]interface{} {
	res := make(map[K]interface{}, len(givenSlice))
	for _, v := range givenSlice {
		res[v] = nil
	}
	return res
}
