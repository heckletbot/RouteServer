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

import "fmt"

type PipelineFileConfig struct {
	InferModes  []string       `yaml:"infer_modes"`
	PipelineCfg PipelineConfig `yaml:"pipeline_cfg"`
	Description string         `yaml:"description,omitempty"`
}

type PipelineConfig struct {
	PhaseConfigs       []PhaseConfig `yaml:"phases"`
	SuccessPhaseConfig *PhaseConfig  `yaml:"success_phase,omitempty"`
	ErrorPhaseConfig   *PhaseConfig  `yaml:"error_phase,omitempty"`
	PreparePhaseConfig *PhaseConfig  `yaml:"prepare_phase,omitempty"`
	SubPipelines       []string      `yaml:"sub_pipelines,omitempty"`
	ApiEndpoints       []ApiEndpoint `yaml:"api_endpoints,omitempty"`
}

type ApiEndpoint struct {
	Path    string   `yaml:"path"`
	Methods []string `yaml:"methods"`
	Handler string   `yaml:"handler"`
}

type PhaseConfig struct {
	// Name for phase in log
	Name string `yaml:"name"`
	// Type of phase to create from registration
	Type           string                 `yaml:"type"`
	SpanName       string                 `yaml:"span_name,omitempty"`
	StageName      string                 `yaml:"stage_name,omitempty"`
	Config         map[string]interface{} `yaml:"config,omitempty"`
	ExecuteParams  ExecuteConfig          `yaml:"execute_params,omitempty"`
	ScheduleParams ScheduleConfig         `yaml:"schedule_params,omitempty"`
	DependsOn      []string               `yaml:"dependsOn,omitempty"`
}

type ExecuteConfig struct {
	InstanceType           string   `yaml:"instance_type"`
	PreProcessRequestFuncs []string `yaml:"pre_process_request_funcs"`
	PostProcessFuncs       []string `yaml:"post_process_funcs"`
	PostPhaseFuncs         []string `yaml:"post_phase_funcs"`
}

func (cfg *ExecuteConfig) Clone() ExecuteConfig {
	return ExecuteConfig{
		InstanceType:           cfg.InstanceType,
		PreProcessRequestFuncs: append([]string{}, cfg.PreProcessRequestFuncs...),
		PostProcessFuncs:       append([]string{}, cfg.PostProcessFuncs...),
		PostPhaseFuncs:         append([]string{}, cfg.PostPhaseFuncs...),
	}
}

type ScheduleConfig struct {
	InstanceType string                 `yaml:"instance_type"`
	SkipPolicy   string                 `yaml:"skip_policy"`
	RequestLoad  []string               `yaml:"request_load"`
	Params       map[string]interface{} `yaml:"params,omitempty"`
}

func (cfg *ScheduleConfig) Clone() ScheduleConfig {
	paramsClone := make(map[string]interface{}, len(cfg.Params))
	for k, v := range cfg.Params {
		paramsClone[k] = v
	}
	return ScheduleConfig{
		InstanceType: cfg.InstanceType,
		SkipPolicy:   cfg.SkipPolicy,
		RequestLoad:  append([]string{}, cfg.RequestLoad...),
		Params:       paramsClone,
	}
}

func (cfg *ScheduleConfig) GetStringParam(key string) string {
	if cfg.Params == nil {
		return ""
	}
	val, ok := cfg.Params[key]
	if !ok {
		return ""
	}
	return fmt.Sprintf("%v", val)
}

func (phaseCfg *PhaseConfig) Clone() *PhaseConfig {
	cfg := &PhaseConfig{
		Name:           phaseCfg.Name,
		Type:           phaseCfg.Type,
		SpanName:       phaseCfg.SpanName,
		StageName:      phaseCfg.StageName,
		Config:         make(map[string]interface{}),
		ExecuteParams:  phaseCfg.ExecuteParams.Clone(),
		ScheduleParams: phaseCfg.ScheduleParams.Clone(),
		DependsOn:      make([]string, len(phaseCfg.DependsOn)),
	}

	copy(cfg.DependsOn, phaseCfg.DependsOn)

	for k, v := range phaseCfg.Config {
		cfg.Config[k] = v
	}
	return cfg
}

func (pc *PipelineConfig) Clone() *PipelineConfig {
	if pc == nil {
		return nil
	}

	clone := &PipelineConfig{
		PhaseConfigs: make([]PhaseConfig, len(pc.PhaseConfigs)),
		SubPipelines: append([]string{}, pc.SubPipelines...),
		ApiEndpoints: append([]ApiEndpoint{}, pc.ApiEndpoints...),
	}

	for i, phase := range pc.PhaseConfigs {
		clonePhaseCfg := phase.Clone()
		if clonePhaseCfg != nil {
			clone.PhaseConfigs[i] = *clonePhaseCfg
		}
	}
	if pc.ErrorPhaseConfig != nil {
		clone.ErrorPhaseConfig = pc.ErrorPhaseConfig.Clone()
	}
	if pc.SuccessPhaseConfig != nil {
		clone.SuccessPhaseConfig = pc.SuccessPhaseConfig.Clone()
	}
	if pc.PreparePhaseConfig != nil {
		clone.PreparePhaseConfig = pc.PreparePhaseConfig.Clone()
	}

	return clone
}
