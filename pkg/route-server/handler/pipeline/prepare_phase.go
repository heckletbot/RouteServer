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

package pipeline

import (
	"time"

	"infer-dp-scheduler/pkg/common/constant"
	"infer-dp-scheduler/pkg/common/errs"
	"infer-dp-scheduler/pkg/common/health"
	"infer-dp-scheduler/pkg/route-server/handler"
)

type PreparePhase struct {
	*BasePhase
}

func NewPreparePhase() Phase {
	return &PreparePhase{
		BasePhase: NewBasePhase(constant.StagePrepare, constant.StagePrepare),
	}
}

func (p *PreparePhase) ExecutePhase(context *PipelineContext) (rsError *errs.RSError) {
	context.FinishReason = constant.UnKnownReason
	reqCtx := context.GetRequestCtx()
	reqCtx.PerfMetrics = &handler.PerfMetrics{
		StartTime: time.Now(),
	}

	if reqCtx.InferMsg == nil {
		reqCtx.InferMsg = &health.InferMsg{
			IsTesting: false,
			Instances: make(map[string]string),
			SkipFlags: make(map[string]string),
		}
	}

	if reqCtx.CancelCtx != nil && reqCtx.CancelCtx.IsCancel.Load() {
		return errs.NewRSMaError(&errs.RequestForcedCancelledErr, p.StageName(), errs.FailureTypeCanceled)
	}

	return nil
}
