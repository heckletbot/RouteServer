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
	"context"

	"infer-dp-scheduler/pkg/common/errs"
	utils2 "infer-dp-scheduler/pkg/common/infer"
	"infer-dp-scheduler/pkg/common/trace"
)

type Phase interface {
	PhaseName() string
	PhaseTypeName() string
	Init(phaseCfg *utils2.PhaseConfig) error
	// StageName used in trace log and to determine whether a probe request for certain instance is success.
	StageName() string
	// RelateInstanceType returns related instance type, empty str if don't relate to any instances
	RelateInstanceType() string
	// ExecutePhase execute the phase, if rsError is not nil, execution will be aborted, and FailurePhase will be executed.
	ExecutePhase(ctx *PipelineContext) (rsError *errs.RSError)
	SpanName() string
	InitSpan(context *PipelineContext)
}

type CancelContext struct {
	CancelFunc   context.CancelFunc
	InstanceId   string
	AccessTimeMs int64
}

type BasePhase struct {
	phaseCfg *utils2.PhaseConfig

	typeName  string
	stageName string

	aborted bool

	// cancel ctx used to set during
	CancelContext *CancelContext
}

func NewBasePhase(typeName, stageName string) *BasePhase {
	return &BasePhase{
		typeName:  typeName,
		stageName: stageName,
	}
}

func (p *BasePhase) PhaseName() string {
	if p.phaseCfg == nil {
		return "unknown"
	}
	return p.phaseCfg.Name
}

func (p *BasePhase) SpanName() string {
	if p.phaseCfg == nil {
		return ""
	}
	return p.phaseCfg.SpanName
}

func (p *BasePhase) PhaseTypeName() string {
	return p.typeName
}

func (p *BasePhase) StageName() string {
	return p.stageName
}

func (p *BasePhase) InitSpan(context *PipelineContext) {
	spanName := p.SpanName()
	if context.GetTracer().NewSubCtx(spanName) {
		context.SetSubRequests(spanName)
	}
}

func (p *BasePhase) StartSubSpan(context *PipelineContext, subSpanName string) {
	tracer := context.GetTracer()
	tracer.Tracer.StartSpan(tracer.GetSubCtx(p.SpanName()), subSpanName, p.StageName(), trace.EnableOtelTrace)
}

func (p *BasePhase) GetCancelContext(ctx *PipelineContext, instanceId string, tracer *trace.TracerWithContext) context.Context {
	reqCancelCtx := ctx.GetRequestCancelCtx()
	return reqCancelCtx.WrapContext(tracer.SubCtx[p.SpanName()], instanceId, p.PhaseName())
}

func (p *BasePhase) Init(phaseCfg *utils2.PhaseConfig) error {
	p.phaseCfg = phaseCfg
	if p.stageName == "" {
		p.stageName = phaseCfg.StageName
	}
	return nil
}

func (p *BasePhase) RelateInstanceType() string {
	return ""
}
