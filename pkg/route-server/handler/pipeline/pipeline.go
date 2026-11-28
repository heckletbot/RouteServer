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
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/goccy/go-yaml"
	"route-server/pkg/common/constant"
	"route-server/pkg/common/errs"
	utils2 "route-server/pkg/common/infer"
	"route-server/pkg/common/logger"
	"route-server/pkg/common/trace"
	"route-server/pkg/common/utils"
	"route-server/pkg/route-server/handler/request"
)

const PipelineConfigDir = "pipelines"

var pipelineConfigs = make(map[string]*utils2.PipelineConfig)
var configsLock = sync.RWMutex{}
var configInit = false

func InferInit() error {
	if err := initBuiltinPhases(); err != nil {
		return err
	}

	if err := initPipelineCfgs(); err != nil {
		return err
	}
	return nil
}

func initPipelineCfgs() error {
	configsLock.Lock()
	defer configsLock.Unlock()
	if !configInit {
		configPath := filepath.Join(os.Getenv(constant.EnvServerConfigPath), PipelineConfigDir)
		if err := loadPipelineConfigs(configPath); err != nil {
			return err
		}
		configInit = true
	}
	return nil
}

func loadPipelineConfigs(configDir string) error {
	info, err := os.Stat(configDir)
	if err != nil {
		return fmt.Errorf("config directory error: %v", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("config path is not a directory: %s", configDir)
	}

	err = filepath.WalkDir(configDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("error accessing path %s: %v", path, err)
		}
		if d.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		if ext != ".yaml" && ext != ".yml" {
			return nil
		}
		config, err := loadSingleConfig(path)
		if err != nil {
			return fmt.Errorf("failed to load config from %s: %v", path, err)
		}

		if len(config.InferModes) == 0 {
			return fmt.Errorf("no infer_modes found in %s", path)
		}

		for _, inferMode := range config.InferModes {
			if inferMode == "" {
				continue
			}
			if err = loadPipelineForInferMode(inferMode, path, config); err != nil {
				return err
			}
		}

		return nil
	})

	if err != nil {
		return err
	}

	return nil
}

func loadPipelineForInferMode(inferMode string, path string, config *utils2.PipelineFileConfig) error {
	if !utils.InMap(inferMode, constant.ValidInferModeMap) {
		return fmt.Errorf("infer_mode [%s] is invalid, must be one of %v",
			inferMode, utils.MapKeysStr(constant.ValidInferModeMap))
	}

	if _, exists := pipelineConfigs[inferMode]; exists {
		return fmt.Errorf("duplicate infer_mode [%s] found in %s", inferMode, path)
	}

	if err := validatePipelineConfig(&config.PipelineCfg); err != nil {
		return fmt.Errorf("invalid pipeline config in %s: %v", path, err)
	}

	pipelineConfigs[inferMode] = &config.PipelineCfg
	logger.Log().Infof("Loaded pipeline config: %s (%s)", inferMode, filepath.Base(path))
	return nil
}

func loadSingleConfig(filePath string) (*utils2.PipelineFileConfig, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read file: %v", err)
	}

	var config utils2.PipelineFileConfig
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to unmarshal YAML: %v", err)
	}
	for i := range config.PipelineCfg.PhaseConfigs {
		phase := &config.PipelineCfg.PhaseConfigs[i]
		if phase.Name == "" {
			phase.Name = fmt.Sprintf("phase_%d", i+1)
		}
	}

	return &config, nil
}

func validatePipelineConfig(config *utils2.PipelineConfig) error {
	if config == nil {
		return fmt.Errorf("given pipeline config is nil")
	}

	if len(config.PhaseConfigs) == 0 {
		return fmt.Errorf("pipeline must contain at least one phase")
	}

	registeredTypes := GetRegisteredPhaseTypes()
	typeSet := make(map[string]bool)
	for _, typ := range registeredTypes {
		typeSet[typ] = true
	}

	for _, phaseConfig := range config.PhaseConfigs {
		if !typeSet[phaseConfig.Type] {
			return fmt.Errorf("phase type [%s] is not registered. Available types: %v",
				phaseConfig.Type, registeredTypes)
		}

		for _, dep := range phaseConfig.DependsOn {
			found := false
			for _, otherPhase := range config.PhaseConfigs {
				if otherPhase.Name == dep {
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("phase [%s] depends on undefined phase [%s]",
					phaseConfig.Name, dep)
			}
		}
	}

	return nil
}

func GetPipelineConfig(inferMode string) (*utils2.PipelineConfig, error) {
	configsLock.RLock()
	defer configsLock.RUnlock()

	config, exists := pipelineConfigs[inferMode]
	if !exists {
		return nil, fmt.Errorf("pipeline config not found for infer_type: %s", inferMode)
	}

	return config.Clone(), nil
}

type Pipeline struct {
	Name            string
	phases          []Phase
	stageNameChain  []string
	stageNameLookup map[string]interface{}
	config          *utils2.PipelineConfig
	isMain          bool
	preparePhase    Phase
	errorPhase      Phase
	successPhase    Phase
	// slices for topology, store the position of phases
	inDegree   []int
	nextPhases [][]int
	spanCnt    map[string]int
}

func NewPipeline(inferType string, isMain bool) (*Pipeline, error) {
	config, err := GetPipelineConfig(inferType)
	if err != nil {
		return nil, err
	}

	p, err := newPipelineFromConfig(config)
	if err != nil {
		return nil, err
	}

	p.Name = inferType
	p.isMain = isMain
	return p, nil
}

func newPipelineFromConfig(config *utils2.PipelineConfig) (*Pipeline, error) {
	if err := validatePipelineConfig(config); err != nil {
		return nil, err
	}

	pipeline := &Pipeline{
		config: config,
	}

	if err := pipeline.createPhases(); err != nil {
		return nil, err
	}

	if err := pipeline.sortPhases(); err != nil {
		return nil, err
	}

	sortedStageChain := make([]string, len(pipeline.phases))
	stageLookup := make(map[string]interface{})
	spanCnt := make(map[string]int)

	for _, phase := range pipeline.phases {
		spanCnt[phase.SpanName()]++
		sortedStageChain = append(sortedStageChain, phase.StageName())
		stageLookup[phase.StageName()] = nil
	}
	pipeline.stageNameChain = sortedStageChain
	pipeline.stageNameLookup = stageLookup
	pipeline.spanCnt = spanCnt

	return pipeline, nil
}

func (p *Pipeline) createPhases() error {
	for _, phaseConfig := range p.config.PhaseConfigs {
		phase, err := p.createPhaseFromConfig(&phaseConfig)
		if err != nil {
			return err
		}

		p.phases = append(p.phases, phase)
	}

	if p.config.PreparePhaseConfig == nil {
		p.preparePhase = NewPreparePhase()
	} else {
		preparePhase, err := p.createPhaseFromConfig(p.config.PreparePhaseConfig)
		if err != nil {
			return err
		}
		p.preparePhase = preparePhase
	}

	if p.config.ErrorPhaseConfig == nil {
		p.errorPhase = newInferFailurePhase()
	} else {
		errPhase, err := p.createPhaseFromConfig(p.config.ErrorPhaseConfig)
		if err != nil {
			return err
		}
		p.errorPhase = errPhase
	}

	if p.config.SuccessPhaseConfig == nil {
		p.successPhase = newInferSuccessPhase()
	} else {
		successPhase, err := p.createPhaseFromConfig(p.config.SuccessPhaseConfig)
		if err != nil {
			return err
		}
		p.successPhase = successPhase
	}

	return nil
}

func (p *Pipeline) createPhaseFromConfig(phaseConfig *utils2.PhaseConfig) (Phase, error) {
	phase, err := CreatePhase(phaseConfig.Type)
	if err != nil {
		return nil, fmt.Errorf("failed to create phase [%s]: %v", phaseConfig.Name, err)
	}

	if err := phase.Init(phaseConfig); err != nil {
		return nil, fmt.Errorf("failed to init phase [%s]: %v", phaseConfig.Name, err)
	}
	return phase, nil
}

func (p *Pipeline) sortPhases() error {
	phaseMap := make(map[string]Phase)
	nameToIndex := make(map[string]int)

	for i, phaseConfig := range p.config.PhaseConfigs {
		phaseMap[phaseConfig.Name] = p.phases[i]
	}

	visited := make(map[string]bool)
	temp := make(map[string]bool)
	result := make([]Phase, 0, len(p.phases))
	inDegree := make([]int, len(p.phases))
	nextPhases := make([][]int, len(p.phases))

	var visit func(name string) error
	visit = func(name string) error {
		if temp[name] {
			return fmt.Errorf("cyclic dependency detected involving phase [%s]", name)
		}
		if visited[name] {
			return nil
		}

		temp[name] = true

		var phaseConfig utils2.PhaseConfig
		for _, pc := range p.config.PhaseConfigs {
			if pc.Name == name {
				phaseConfig = pc
				break
			}
		}

		for _, dep := range phaseConfig.DependsOn {
			if err := visit(dep); err != nil {
				return err
			}
		}

		temp[name] = false
		visited[name] = true
		result = append(result, phaseMap[name])
		nameToIndex[name] = len(result) - 1
		return nil
	}

	for _, phaseConfig := range p.config.PhaseConfigs {
		if !visited[phaseConfig.Name] {
			if err := visit(phaseConfig.Name); err != nil {
				return err
			}
		}
		// record dependence edge
		curPhaseId := nameToIndex[phaseConfig.Name]
		for _, dep := range phaseConfig.DependsOn {
			prePhaseId := nameToIndex[dep]
			nextPhases[prePhaseId] = append(nextPhases[prePhaseId], curPhaseId)
			inDegree[curPhaseId]++
		}
	}
	p.inDegree = inDegree
	p.nextPhases = nextPhases
	p.phases = result
	return nil
}

type phaseResult struct {
	index   int
	rsError *errs.RSError
}

func (p *Pipeline) preparePipeline(ctx *PipelineContext) *errs.RSError {
	reqCtx := ctx.GetRequestCtx()
	// only the main pipeline will recompute req id
	if p.isMain {
		parsedReq, err := request.NewBasicRequestImplForPipeline(ctx.ClientGinCtx.Request)
		// Generate tracer first, then process error
		reqCtx.ParsedRequest = parsedReq
		reqId := ctx.RequestId()
		tracer := trace.NewTracer(ctx.ClientGinCtx, ctx.InferManager.Cfg.TraceCfg, reqId, ctx.ClientGinCtx.Request.Header)
		ctx.SetTracer(&tracer)
		if err != nil {
			return errs.NewRSMaError(errs.BadReqErr.BuildArgs(
				fmt.Sprintf("fail to create generate request: %v", err)), constant.StagePrepare)
		}
		reqCtx.ParsedRequest.SetRequestId(request.GetInferRequestId(&ctx.ClientGinCtx.Request.Header, ctx.IsChat()))
		extraInfo := request.RewriteRequestIDDetail(ctx.ClientGinCtx)
		tracer.E2ECtx = tracer.Tracer.StartSpan(ctx.ClientGinCtx.Request.Context(), trace.RequestSpanE2E, extraInfo, trace.GtLogPermission)

		cancelCtx, err := ctx.InferManager.RunningRequestManager.NewReqCancelCtx(reqId, tracer.Tracer)
		if err != nil {
			err := errs.NewRSMaError(errs.TooManyRequestError.BuildArgs(
				fmt.Sprintf("fail to create request context: %v", err)), constant.StagePrepare)
			ctx.SetError(err, nil)
			return err
		}
		cancelCtx.PipelineReqCtx = ctx
		ctx.SetRequestCancelCtx(cancelCtx)
		ctx.GetRequestCancelCtx().RecordNewPhase(p.preparePhase.PhaseName())
		defer ctx.GetRequestCancelCtx().RecordEndPhase(p.preparePhase.PhaseName())
	}

	if errRS := p.preparePhase.ExecutePhase(ctx); errRS != nil {
		return errRS
	}
	if p.isMain {
		resolveRenderedPrompt(ctx)
	}
	return nil
}

// phaseByType returns the phase of the given type, or nil when the pipeline
// config does not contain one.
func (p *Pipeline) phaseByType(typeName string) Phase {
	for _, phase := range p.phases {
		if phase.PhaseTypeName() == typeName {
			return phase
		}
	}
	return nil
}

// schedulePhaseFor returns the schedule phase of one instance role, or nil when
// the pipeline does not schedule that role.
func (p *Pipeline) schedulePhaseFor(instanceType string) *SchedulePhase {
	for _, phase := range p.phases {
		schedulePhase, ok := phase.(*SchedulePhase)
		if ok && schedulePhase.targetInstanceType == instanceType {
			return schedulePhase
		}
	}
	return nil
}

func (p *Pipeline) ExecutePipeline(ctx *PipelineContext) {
	// prepare for pipeline
	if errRs := p.preparePipeline(ctx); errRs != nil {
		ctx.SetError(errRs, nil)
		if err := p.errorPhase.ExecutePhase(ctx); err != nil {
			logger.Log().Errorf("failed to prepare pipeline for request [%s]. details err: %v",
				ctx.RequestId(), err.Error())
		}
		return
	}

	runningResultChannel := make(chan phaseResult, len(p.phases))
	runningInDegree := make([]int, len(p.inDegree))
	copy(runningInDegree, p.inDegree)
	remainPhasesCnt := len(p.phases)
	runningSpanCnt := make(map[string]int)
	for k, v := range p.spanCnt {
		runningSpanCnt[k] = v
	}
	var wg sync.WaitGroup

	submitExecute := func(phaseId int, phase Phase) {
		wg.Add(1)
		ctx.GetRequestCancelCtx().RecordNewPhase(phase.PhaseName())
		go func() {
			defer wg.Done()
			if _, exist := ctx.GetRequestCtx().SubRequests.Load(phase.SpanName()); !exist {
				phase.InitSpan(ctx)
			}
			rsErr := phase.ExecutePhase(ctx)
			result := phaseResult{
				index:   phaseId,
				rsError: rsErr,
			}
			runningResultChannel <- result
		}()
	}

	// submit all phase without dependencies
	for index, phase := range p.phases {
		if runningInDegree[index] == 0 {
			submitExecute(index, phase)
		}
	}

	defer func() {
		// close all the channels when the pipeline stop
		ctx.SetKVTransferParams(map[string]interface{}{})
		ctx.SetDone()
		ctx.SetFirstTokenSend()
		wg.Wait()
		close(runningResultChannel)
	}()

	// handle the result
	for remainPhasesCnt > 0 {
		result, ok := <-runningResultChannel
		if !ok {
			return
		}
		curPhase := p.phases[result.index]
		remainPhasesCnt--
		ctx.GetRequestCancelCtx().RecordEndPhase(curPhase.PhaseName())
		if result.rsError != nil {
			ctx.SetError(result.rsError, nil)
			ctx.InferManager.RunningRequestManager.AbortByPipeline(ctx.RequestId(), p.Name, p.config.SubPipelines)
			if err := p.errorPhase.ExecutePhase(ctx); err != nil {
				logger.Log().Errorf("failed to process phase [%s] for request [%s]. details err: %v",
					p.errorPhase.PhaseName(), ctx.RequestId(), err.Error())
			}
			ctx.GetTracer().Tracer.ErrorSpan(curPhase.SpanName(), result.rsError, trace.EnableOtelTrace)
			return
		}
		for _, nextPhaseId := range p.nextPhases[result.index] {
			runningInDegree[nextPhaseId]--
			if runningInDegree[nextPhaseId] == 0 {
				submitExecute(nextPhaseId, p.phases[nextPhaseId])
			}
		}

		if spanName := curPhase.SpanName(); spanName != "" {
			runningSpanCnt[spanName]--
			if runningSpanCnt[spanName] == 0 {
				ctx.GetTracer().Tracer.EndSpan(curPhase.SpanName(), fmt.Sprintf("success to exec [%s] stage", curPhase.StageName()), trace.EnableOtelTrace)
			}
		}
	}

	if err := p.successPhase.ExecutePhase(ctx); err != nil {
		logger.Log().Errorf("failed to process finalize phase [%s] for request [%s], details err: %v",
			p.successPhase.PhaseName(), ctx.RequestId(), err.Error())
	}
}

func (p *Pipeline) GetStageChain() []string {
	return p.stageNameChain
}

func (p *Pipeline) IsStageInPipeline(stageName string) bool {
	_, exists := p.stageNameLookup[stageName]
	return exists
}
