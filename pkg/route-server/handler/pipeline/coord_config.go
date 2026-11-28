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

	commoncfg "route-server/pkg/common/config"
	"route-server/pkg/common/constant"
	common_infer "route-server/pkg/common/infer"
	"route-server/pkg/common/logger"
	"route-server/pkg/route-server/scheduler/coord_disaggregation"
)

// configureCoordRouting validates the coord routing wiring of a freshly built
// pipeline and injects the decision inputs into its CoordRoute phase.
//
// The feature is switched on by the config section and the pipeline phase being
// present together, the same way every other pipeline capability is switched on
// by its phase config. Either alone is a deployment mistake — a phase without
// config would panic on the first request, config without a phase would leave
// the switch silently inert — so both are refused at startup.
func configureCoordRouting(pipeline *Pipeline, cfg *commoncfg.ServerConfig) error {
	phase := pipeline.phaseByType(constant.CoordRoutePhase)

	if cfg.CoordRouting == nil {
		if phase != nil {
			return fmt.Errorf("pipeline [%s] has a coord route phase but no coord-routing config", pipeline.Name)
		}
		return nil
	}

	if phase == nil {
		return fmt.Errorf("coord-routing is configured but pipeline [%s] has no coord route phase", pipeline.Name)
	}

	coordPhase, ok := phase.(*CoordRoutePhase)
	if !ok {
		return fmt.Errorf("phase [%s] is not a coord route phase", phase.PhaseName())
	}

	// Local routing writes the chosen worker into the prefill schedule slot.
	loadSource := pipeline.schedulePhaseFor(constant.Prefill)
	if loadSource == nil {
		return fmt.Errorf("pipeline [%s] has a coord route phase but no prefill schedule phase", pipeline.Name)
	}

	if cfg.CoordRouting.LocalMinInputTokens != nil {
		return fmt.Errorf("coord-routing: local-min-input-tokens has been renamed to p-local-min-input-tokens / d-local-min-input-tokens")
	}

	params := coordRoutingParams(cfg.CoordRouting)
	if err := params.Validate(); err != nil {
		return fmt.Errorf("pipeline [%s] has an invalid coord-routing config: %w", pipeline.Name, err)
	}
	strategy, err := coord_disaggregation.NewStrategy(cfg.CoordRouting.Strategy, params)
	if err != nil {
		return err
	}
	if err := validateCoordExecution(pipeline, coordPhase); err != nil {
		return err
	}

	coordPhase.Configure(params, strategy, loadSource)
	logger.Log().Infof("coord-routing enabled: strategy=%s p-window=[%d,%d] d-window=[%d,%d] stale=%s idle(kv<=%.2f,wait<=%d,runs p<=%d/d<=%d) inflight(p=%d,d=%d)",
		strategy.Name(),
		params.PMinTokens, params.PInputTokenGate,
		params.DMinTokens, params.DInputTokenGate,
		params.StaleDuration,
		params.IdleMaxKVUsage, params.IdleMaxWaiting, params.IdleMaxRunningP, params.IdleMaxRunningD,
		params.PMaxInflight, params.DMaxInflight)
	return nil
}

// validateCoordExecution checks the pipeline edges the local route actually uses.
// A missing edge is a startup error. The request path does not substitute another one.
func validateCoordExecution(pipeline *Pipeline, coord Phase) error {
	coordName := coord.PhaseName()
	prefillExecute := pipeline.executePhaseFor(constant.Prefill)
	decodeSchedule := pipeline.schedulePhaseFor(constant.Decode)
	decodeExecute := pipeline.executePhaseFor(constant.Decode)
	prefillSchedule := pipeline.schedulePhaseFor(constant.Prefill)
	if prefillExecute == nil || decodeSchedule == nil || decodeExecute == nil {
		return fmt.Errorf("pipeline [%s] coord route requires a prefill execute phase and decode schedule and execute phases", pipeline.Name)
	}

	byName := map[string]Phase{}
	for _, phase := range pipeline.phases {
		byName[phase.PhaseName()] = phase
	}
	for _, phase := range []Phase{prefillSchedule, prefillExecute, decodeSchedule, decodeExecute} {
		if !dependsOnPhase(phase, coordName, byName) {
			return fmt.Errorf("pipeline [%s] phase [%s] must depend on coord route phase [%s]", pipeline.Name, phase.PhaseName(), coordName)
		}
	}

	executeCfg := prefillExecute.phaseCfg.ExecuteParams
	if !containsName(executeCfg.PreProcessRequestFuncs, CoordPrepareRequest) ||
		containsAnyName(executeCfg.PreProcessRequestFuncs, PrefillPrepareRequest, PrefillStreamPrepareRequest) {
		return fmt.Errorf("pipeline [%s] prefill execute must prepare the body with %s and without %s or %s",
			pipeline.Name, CoordPrepareRequest, PrefillPrepareRequest, PrefillStreamPrepareRequest)
	}
	if !containsName(executeCfg.PostProcessFuncs, CoordProcessResponse) ||
		containsAnyName(executeCfg.PostProcessFuncs, PrefillProcessResponse, PrefillStreamProcessResponse, CheckKVTransferParamsKey) {
		return fmt.Errorf("pipeline [%s] prefill execute must handle the response with %s and without %s, %s, or %s",
			pipeline.Name, CoordProcessResponse, PrefillProcessResponse, PrefillStreamProcessResponse, CheckKVTransferParamsKey)
	}
	if !containsName(executeCfg.PostPhaseFuncs, CoordPostPhase) {
		return fmt.Errorf("pipeline [%s] prefill execute must finish the phase with %s", pipeline.Name, CoordPostPhase)
	}

	if decodeSchedule.phaseCfg.ScheduleParams.SkipPolicy != SkipPolicySkipCoordLocal {
		return fmt.Errorf("pipeline [%s] decode schedule skip_policy must be %s", pipeline.Name, SkipPolicySkipCoordLocal)
	}
	if decodeSchedule.StageName() == "" || decodeSchedule.StageName() != decodeExecute.StageName() {
		return fmt.Errorf("pipeline [%s] decode schedule and decode execute must share one stage name", pipeline.Name)
	}
	return nil
}

func dependsOnPhase(phase Phase, target string, byName map[string]Phase) bool {
	seen := map[string]bool{}
	var walk func(name string) bool
	walk = func(name string) bool {
		if name == target {
			return true
		}
		if seen[name] {
			return false
		}
		seen[name] = true
		next, ok := byName[name]
		if !ok {
			return false
		}
		for _, dep := range phaseDepends(next) {
			if walk(dep) {
				return true
			}
		}
		return false
	}
	for _, dep := range phaseDepends(phase) {
		if walk(dep) {
			return true
		}
	}
	return false
}

func phaseDepends(phase Phase) []string {
	cfg := phaseConfig(phase)
	if cfg == nil {
		return nil
	}
	return cfg.DependsOn
}

func phaseConfig(phase Phase) *common_infer.PhaseConfig {
	switch typed := phase.(type) {
	case *SchedulePhase:
		return typed.phaseCfg
	case *MonoRequestPhase:
		return typed.phaseCfg
	case *CoordRoutePhase:
		return typed.phaseCfg
	}
	return nil
}

func containsName(names []string, want string) bool {
	for _, name := range names {
		if name == want {
			return true
		}
	}
	return false
}

func containsAnyName(names []string, blocked ...string) bool {
	for _, name := range names {
		if containsName(blocked, name) {
			return true
		}
	}
	return false
}
