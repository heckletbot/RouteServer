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
	"testing"

	"route-server/pkg/route-server/apc"
)

type renderedWorkRequest struct {
	mockGenerateRequest
	queried bool
	work    int32
}

func (r renderedWorkRequest) WorkTokens(string) (int32, bool) {
	if !r.queried {
		return r.promptTokens, false
	}
	return r.work, true
}

func TestPredictLoadUsesRenderedRemainingOnce(t *testing.T) {
	algo, err := NewAPCScheduler()
	if err != nil {
		t.Fatalf("new apc scheduler: %v", err)
	}
	schedulerImpl := algo.(*APCScheduler)
	load := apc.InstanceLoad{}

	withHit := &renderedWorkRequest{
		mockGenerateRequest: mockGenerateRequest{reqId: "r1", promptTokens: 100},
		queried:             true,
		work:                10,
	}
	got := schedulerImpl.predictLoadAfterAddRequest(withHit, load, "p1", 0.5)
	// remaining 10 plus prefixLoadBase 1, batch 16: 11 * (1+1) / 2
	if got != 11 {
		t.Fatalf("predicted load with conductor hit = %v, want 11", got)
	}

	withoutHit := &renderedWorkRequest{
		mockGenerateRequest: mockGenerateRequest{reqId: "r2", promptTokens: 100},
	}
	got = schedulerImpl.predictLoadAfterAddRequest(withoutHit, load, "p1", 0.5)
	// 100 * 0.5 + prefixLoadBase 1 = 51, same formula
	if got != 51 {
		t.Fatalf("predicted load without conductor hit = %v, want 51", got)
	}
}
