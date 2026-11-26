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

	// The prompt-token gate must read the same size the prefill schedule reads,
	// so it borrows that phase instead of carrying a second request_load list.
	loadSource := pipeline.schedulePhaseFor(constant.Prefill)
	if loadSource == nil {
		return fmt.Errorf("pipeline [%s] has a coord route phase but no prefill schedule phase to take request_load from", pipeline.Name)
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
