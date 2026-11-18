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

package config

// APCStrategyMemoryConfig is the strategy memory configuration
type APCStrategyMemoryConfig struct {
	// Memory management configuration
	MaxEntries    int64 `yaml:"max-entries"`
	MaxMemoryMB   int64 `yaml:"max-memory-mb"`
	CacheTTLSec   int64 `yaml:"cache-ttl-sec"`
	MetricsEnable bool  `yaml:"metrics-enable"`
}

// APCRollingHashConfig is the rolling hash strategy configuration
type APCRollingHashConfig struct {
	// Rolling hash parameters
	BlockSizeBytes int     `yaml:"block-size-bytes"` // Block size
	MaxBlocks      int     `yaml:"max-blocks"`       // Maximum number of blocks
	MatchThreshold float64 `yaml:"match-threshold"`  // Minimum match ratio threshold (0.0-1.0)

	// Progressive block strategy configuration
	ProgressiveBlockEnable        bool `yaml:"progressive-block-enable"`          // Whether to enable progressive block strategy
	ProgressiveBaseBlockSizeBytes int  `yaml:"progressive-base-block-size-bytes"` // Progressive base block size (bytes), default 256

	// Memory management configuration - embedded common config
	APCStrategyMemoryConfig `yaml:",inline"`
}
