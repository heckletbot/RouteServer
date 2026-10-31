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
	"time"

	"route-server/pkg/common/constant"
	"route-server/pkg/common/errs"
	common_infer "route-server/pkg/common/infer"
	"route-server/pkg/common/utils"
)

type DummyPhase struct {
	*BasePhase
	ReqTimeoutMS uint
}

func (d *DummyPhase) Init(phaseCfg *common_infer.PhaseConfig) error {
	if err := d.BasePhase.Init(phaseCfg); err != nil {
		return err
	}
	if _, exist := phaseCfg.Config[RequestTimeout]; exist {
		timeoutMs, ok := utils.ParseUint(phaseCfg.Config[RequestTimeout])
		if !ok {
			return fmt.Errorf("invalid req_timeout_ms config")
		}
		d.ReqTimeoutMS = timeoutMs
	} else {
		d.ReqTimeoutMS = 60000
	}
	return nil
}

func (d *DummyPhase) ExecutePhase(ctx *PipelineContext) *errs.RSError {
	requestCtx := ctx.GetRequestCtx()

	select {
	case <-requestCtx.DoneChannel:
		if requestCtx.CancelCtx != nil && requestCtx.CancelCtx.IsCancel.Load() {
			return errs.NewRSMaError(
				errs.RequestForcedCancelledErr.BuildArgs(fmt.Sprintf("DummyPhase [%s] aborted due to other phase error for request [%s]", d.PhaseName(), ctx.RequestId())),
				"MetaServer",
				errs.FailureTypeCanceled,
			)
		}
		return nil
	case <-time.After(time.Duration(d.ReqTimeoutMS) * time.Millisecond):
		return errs.NewRSMaError(
			errs.GatewayTimeoutErr.BuildArgs(fmt.Sprintf("DummyPhase [%s] wait timeout after %d ms for request [%s]", d.PhaseName(), d.ReqTimeoutMS, ctx.RequestId())),
			"MetaServer",
			errs.FailureTypeTimeout,
		)
	}
}

func newDummyPhase() Phase {
	return &DummyPhase{
		BasePhase: NewBasePhase(constant.DummyPhase, ""),
	}
}
