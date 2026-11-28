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

package server

import (
	"fmt"

	"route-server/pkg/common/config"
	"route-server/pkg/common/logger"
	"route-server/pkg/route-server/conductor"
	"route-server/pkg/route-server/instances"
)

// conductorModelNameResolver narrows instances.Manager down to the single method the
// conductor package needs. Keeping the dependency small lets conductor stay free of
// instances/instance-client imports.
type conductorModelNameResolver struct {
	manager instances.Manager
}

// ResolveModelName asks the instance's own /v1/models endpoint which model it serves.
func (r conductorModelNameResolver) ResolveModelName(instanceID string) (string, error) {
	if r.manager == nil {
		return "", fmt.Errorf("instance manager is nil")
	}
	instanceClient, err := r.manager.GetInstanceClient(instanceID)
	if err != nil {
		return "", err
	}
	return instanceClient.ModelName()
}

// buildConductorManager normalizes the conductor config and constructs the hit querier.
// Both return values are nil when the integration is disabled.
//
// The registration manager is kept for token-source and block size. It is not started
// and must not be registered as a cluster applier: RouteServer does not register or
// unregister inference instances with Conductor.
func buildConductorManager(cfg config.ConductorConfig, instanceManager instances.Manager) (
	*conductor.RegistrationManager, conductor.HitQuerier, error) {
	normalized, err := conductor.NormalizeConfig(cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid conductor config: %w", err)
	}
	if !normalized.Enabled {
		return nil, nil, nil
	}

	client, err := conductor.NewHTTPClient(normalized)
	if err != nil {
		return nil, nil, fmt.Errorf("build conductor client failed: %w", err)
	}

	registrationManager, err := conductor.NewRegistrationManager(
		normalized, client, conductorModelNameResolver{manager: instanceManager})
	if err != nil {
		return nil, nil, fmt.Errorf("build conductor registration manager failed: %w", err)
	}

	hitQuerier, err := conductor.NewHitQuerier(
		conductor.HitQuerierConfig{Timeout: normalized.QueryTimeout},
		normalized.BaseURL, normalized.BlockSize)
	if err != nil {
		return nil, nil, fmt.Errorf("build conductor hit querier failed: %w", err)
	}

	logger.Log().Infof("Conductor prefix query enabled: base_url=%s, block_size=%d, token_source=%s, query_timeout=%s",
		normalized.BaseURL, normalized.BlockSize, normalized.TokenSource, normalized.QueryTimeout)
	return registrationManager, hitQuerier, nil
}
