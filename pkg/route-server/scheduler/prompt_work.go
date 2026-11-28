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

import "route-server/pkg/common/utils"

// requestWorkTokens is the length this instance still has to compute.
// Without a Conductor hit map it is the request's prompt length.
func requestWorkTokens(request utils.GenerateRequest, instanceID string) int32 {
	if tokens, ok := utils.InstanceWorkTokens(request, instanceID); ok {
		return tokens
	}
	return request.GetPromptTokenCount()
}

// unmatchedPromptTokens is the prefill work added to an instance.
// Conductor already subtracted the cached prefix, so that number is not run
// through the local match ratio a second time.
func unmatchedPromptTokens(request utils.GenerateRequest, instanceID string, matchedRatio float64) float64 {
	if tokens, ok := utils.InstanceWorkTokens(request, instanceID); ok {
		return float64(tokens)
	}
	return float64(request.GetPromptTokenCount()) * (1 - matchedRatio)
}
