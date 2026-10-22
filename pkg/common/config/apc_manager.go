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

package config

// APCManagerConfig is the APC manager configuration
type APCManagerConfig struct {
	// Basic configuration
	Enable bool `yaml:"enable"`

	// Persistence configuration
	Persistence APCPersistenceConfig `yaml:"persistence"`

	// Strategy configuration - using concrete struct instead of map[string]interface{}
	Strategies *StrategyConfigs `yaml:"strategies"`
}

// APCPersistenceConfig is the APC persistence configuration
type APCPersistenceConfig struct {
	Enable   bool   `yaml:"enable"`   // Whether to enable persistence
	Provider string `yaml:"provider"` // Persistence provider: "redis"
}

// StrategyConfigs is the strategy configuration collection
type StrategyConfigs struct {
	// Rolling hash strategy configuration
	Hash *APCRollingHashConfig `yaml:"hash,omitempty"`
}
