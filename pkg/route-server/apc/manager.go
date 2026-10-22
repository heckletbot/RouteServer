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

package apc

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"infer-dp-scheduler/pkg/common/cluster"
	"infer-dp-scheduler/pkg/common/config"
	"infer-dp-scheduler/pkg/common/constant"
	"infer-dp-scheduler/pkg/common/logger"
	"infer-dp-scheduler/pkg/common/utils"
	"infer-dp-scheduler/pkg/route-server/metrics"
)

// Manager is the APC manager
type Manager struct {
	config         *config.APCManagerConfig
	running        atomic.Bool
	clusterService atomic.Value // stores cluster.ClusterService

	// Strategy management
	defaultStrategy string
	strategies      map[string]Strategy
	strategiesMutex sync.RWMutex

	// Persistence support
	persistenceProvider PersistenceProvider
	persistenceEnabled  bool
}

func (m *Manager) initializePersistence() error {
	persistCfg := m.config
	if !persistCfg.Persistence.Enable {
		logger.Log().Infof("APC persistence disabled in persistCfg")
		m.persistenceEnabled = false
		return nil
	}

	// Create persistence provider
	provider, err := CreatePersistenceProvider(&persistCfg.Persistence)
	if provider == nil || err != nil {
		m.persistenceEnabled = false
		return fmt.Errorf("unsupported persistence provider: %s, disabling persistence, error: %v", persistCfg.Persistence.Provider, err)
	}

	m.persistenceProvider = provider
	m.persistenceEnabled = true

	logger.Log().Infof("APC persistence initialized with provider: %s", provider.Name())
	return nil
}

// NewManager creates an APC manager
// Responsibilities: static initialization (instance creation, dependency injection, strategy creation, config preparation, persistence initialization)
// Note: dynamic component startup (background goroutines, maintenance tasks) is handled by the Start method
func NewManager(cfg *config.APCManagerConfig, clusterService cluster.ClusterService) (*Manager, error) {
	if clusterService == nil {
		return nil, fmt.Errorf("clusterService cannot be nil")
	}

	manager := &Manager{
		config:     cfg,
		strategies: make(map[string]Strategy),
	}

	manager.clusterService.Store(clusterService)

	// Initialize persistence provider, to be implemented

	// Create strategy instances
	err := manager.createStrategies()
	if err != nil {
		return nil, err
	}

	return manager, nil
}

// createStrategies creates strategy instances
func (m *Manager) createStrategies() error {
	m.strategiesMutex.Lock()
	defer m.strategiesMutex.Unlock()

	if m.config.Strategies == nil {
		logger.Log().Warnf("No strategy configurations found, using default hash config")
		// Create default strategy configuration
		defaultHashConfig := &config.APCRollingHashConfig{
			BlockSizeBytes: 64,
			MaxBlocks:      128,
			APCStrategyMemoryConfig: config.APCStrategyMemoryConfig{
				MaxEntries:  100000,
				MaxMemoryMB: 512,
				CacheTTLSec: 3600,
			},
		}
		hash, err := NewRollingHashStrategy(defaultHashConfig)
		if err != nil {
			return err
		}
		m.strategies[constant.APCStrategyHash] = hash
		m.defaultStrategy = constant.APCStrategyHash
		return nil
	}

	// Create Hash strategy
	if m.config.Strategies.Hash != nil {
		hash, err := NewRollingHashStrategy(m.config.Strategies.Hash)
		if err != nil {
			return err
		}
		m.strategies[constant.APCStrategyHash] = hash
		if m.defaultStrategy == "" {
			m.defaultStrategy = constant.APCStrategyHash
		}
		logger.Log().Infof("Created rolling hash strategy with config: block-size=%d, max-blocks=%d",
			m.config.Strategies.Hash.BlockSizeBytes, m.config.Strategies.Hash.MaxBlocks)
	}

	// If no default strategy is set, use the first strategy
	if m.defaultStrategy == "" && len(m.strategies) > 0 {
		m.defaultStrategy = constant.APCStrategyHash
	}

	logger.Log().Infof("APC manager initialized with %d strategies, default: %s",
		len(m.strategies), m.defaultStrategy)
	return nil
}

// GetConfig returns the configuration
func (m *Manager) GetConfig() *config.APCManagerConfig {
	return m.config
}

// GetDefaultStrategyName returns the default strategy name
func (m *Manager) GetDefaultStrategyName() string {
	if m.defaultStrategy != "" {
		return m.defaultStrategy
	}

	// Check configured strategies
	if m.config.Strategies != nil {
		if m.config.Strategies.Hash != nil {
			return constant.APCStrategyHash
		}
	}

	return constant.APCStrategyHash // Fallback default value
}

// GetDefaultStrategyInstance returns the default strategy instance
func (m *Manager) GetDefaultStrategyInstance() Strategy {
	strategyName := m.GetDefaultStrategyName()
	return m.getStrategy(strategyName)
}

// IsRunning checks whether the manager is running
func (m *Manager) IsRunning() bool {
	return m.running.Load()
}

// Start starts the APC manager
func (m *Manager) Start() error {
	if !m.running.CompareAndSwap(false, true) {
		return fmt.Errorf("APC manager is already running")
	}

	logger.Log().Infof("Starting APC manager...")

	logger.Log().Infof("APC manager started successfully")
	return nil
}

// Stop stops the APC manager
func (m *Manager) Stop() error {
	if m.running.CompareAndSwap(true, false) {
		logger.Log().Infof("Stopped APC manager")
	}
	return nil
}

// Close closes the APC manager (releases resources)
func (m *Manager) Close() error {
	logger.Log().Infof("Closing APC manager...")

	// Stop running
	if err := m.Stop(); err != nil {
		logger.Log().Warnf("Failed to stop APC manager during close: %v", err)
	}

	// Close persistence provider
	if m.persistenceProvider != nil {
		if err := m.persistenceProvider.Close(); err != nil {
			logger.Log().Warnf("Failed to close persistence provider: %v", err)
		}
		m.persistenceProvider = nil
	}

	// Clean up strategies
	m.strategiesMutex.Lock()
	for _, strategy := range m.strategies {
		if cleaner, ok := strategy.(interface{ Cleanup() }); ok {
			cleaner.Cleanup()
		}
	}
	m.strategies = make(map[string]Strategy)
	m.strategiesMutex.Unlock()

	logger.Log().Infof("APC manager closed successfully")
	return nil
}

// GetClusterService returns the cluster service
func (m *Manager) GetClusterService() cluster.ClusterService {
	if cs := m.clusterService.Load(); cs != nil {
		return cs.(cluster.ClusterService)
	}
	return nil
}

// IsMeLeader checks if this node is the leader
func (m *Manager) IsMeLeader() bool {
	cs := m.clusterService.Load()
	if cs == nil {
		return false
	}
	css, ok := cs.(cluster.ClusterService)
	if !ok {
		return false
	}

	return css.IsLeader()
}

// UpdateCache updates the APC cache - based on RequestEventMsg, caller must ensure event validity
func (m *Manager) UpdateCache(event *utils.RequestEventMsg) error {
	if !m.config.Enable || !m.IsRunning() {
		return nil // APC not enabled, return directly
	}

	if event == nil {
		return fmt.Errorf("event cannot be nil")
	}
	requestId := event.Request.GetRequestId()
	strategyType := event.Request.GetApcStrategyType()
	strategy := m.getStrategy(strategyType)
	if strategy == nil {
		return fmt.Errorf("strategy %s not found for request ID %v", strategyType, requestId)
	}

	promptCacheKey := event.Request.GetPromptCacheKey()
	if err := strategy.UpdateCache(promptCacheKey, event.TargetInstance); err != nil {
		logger.Log().Debugf("Failed to update cache for requestId %s: %v", requestId, err)
		return err
	}

	logger.Log().Tracef("Successfully updated cache for requestId %s, instance %s using strategy %s",
		requestId, event.TargetInstance, strategyType)

	// Incremental persistence: write to persistent storage directly in UpdateCache
	if m.persistenceProvider != nil && m.persistenceEnabled {
		ctx := context.Background()

		if err := m.persistenceProvider.UpdateCache(ctx, strategyType, promptCacheKey, event.TargetInstance); err != nil {
			logger.Log().Debugf("Failed to persist incremental cache update: %v", err)
		}
	}

	return nil
}

// GetMatchedInstances gets matched instance candidates (only responsible for cache lookup and matching)
func (m *Manager) GetMatchedInstances(request utils.GenerateRequest, availableInstances map[string]InstanceLoad,
	insBlackMap map[string]interface{}) (map[string]float64, error) {

	if !m.config.Enable {
		return nil, fmt.Errorf("APC disabled")
	}

	if len(availableInstances) == 0 {
		return nil, fmt.Errorf("no available instance")
	}

	start := time.Now()
	var matchedInstances map[string]float64
	defer func() {
		duration := time.Since(start)
		// Prometheus metrics
		metrics.RecordAPCQueryLatency(duration.Seconds())
	}()

	filteredInstances := m.filterAvailableInstances(availableInstances, insBlackMap)
	if len(filteredInstances) == 0 {
		return nil, fmt.Errorf("no available instances after blacklist filtering")
	}

	// Extract necessary info from request
	requestId := request.GetRequestId()
	strategyName := request.GetApcStrategyType()

	// Check leader permission and requestId validity
	if !m.IsMeLeader() || requestId == "" {
		return nil, fmt.Errorf("not leader or empty requestId, skipping APC processing")
	}

	// Use the specified strategy to get matched instances
	strategy := m.getStrategy(strategyName)
	if strategy == nil {
		logger.Log().Debugf("Strategy '%s' not found, using default", strategyName)
		strategy = m.getStrategy(m.defaultStrategy)
		if strategy == nil {
			return nil, fmt.Errorf("no strategies available")
		}
	}

	logger.Log().Tracef("Using strategy '%s' for with %d instances", strategy.Name(), len(filteredInstances))

	var err error
	matchedInstances, _, err = strategy.GetMatchedInstances(request, filteredInstances)
	if err != nil {
		logger.Log().Debugf("Strategy '%s' failed to get matched instances for %v", strategy.Name(), err)
		return nil, fmt.Errorf("failed to get matched instances for %v", err)
	}

	logger.Log().Infof("APC found %d matched instances %v for request %v", len(matchedInstances), matchedInstances, requestId)
	return matchedInstances, nil
}

// filterAvailableInstances filters available instances (removes blacklisted instances)
func (m *Manager) filterAvailableInstances(availableInstances map[string]InstanceLoad,
	insBlackMap map[string]interface{}) map[string]InstanceLoad {

	if insBlackMap == nil || len(insBlackMap) == 0 {
		return availableInstances
	}

	filtered := make(map[string]InstanceLoad)
	for instanceID, load := range availableInstances {
		if _, blacklisted := insBlackMap[instanceID]; !blacklisted {
			filtered[instanceID] = load
		}
	}

	logger.Log().Tracef("Filtered instances: total=%d, blacklisted=%d, remaining=%d",
		len(availableInstances), len(insBlackMap), len(filtered))

	return filtered
}

// getStrategy returns a strategy instance by name
func (m *Manager) getStrategy(name string) Strategy {
	m.strategiesMutex.RLock()
	defer m.strategiesMutex.RUnlock()

	if strategy, exists := m.strategies[name]; exists {
		return strategy
	}
	return nil
}

func (m *Manager) cleanStrategies() {
	for _, strategy := range m.strategies {
		strategy.CleanUp()
	}
}

// RemoveInstanceIdFromCache synchronizes with cluster state
func (m *Manager) RemoveInstanceIdFromCache(instancesToRemove map[string]struct{}) error {
	if !m.running.Load() {
		return fmt.Errorf("APC manager is not running")
	}

	logger.Log().Infof("Syncing APC instancesToRemove with %d instances", len(instancesToRemove))

	// Get all strategies and perform cache cleanup
	var strategies []Strategy
	m.strategiesMutex.RLock()
	for _, strategy := range m.strategies {
		strategies = append(strategies, strategy)
	}
	m.strategiesMutex.RUnlock()

	// Clean up cache for instances that are no longer active
	var errorCount int
	for _, strategy := range strategies {
		if err := strategy.UpdateToRemoveInstances(instancesToRemove); err != nil {
			errorCount++
			logger.Log().Errorf("Failed to update toRemove instances for strategy %s: %v", strategy.Name(), err)
		}
	}

	if errorCount > 0 {
		logger.Log().Warnf("APC instancesToRemove sync completed with %d errors", errorCount)
		return fmt.Errorf("failed to update toRemove instances for %d strategies", errorCount)
	}

	logger.Log().Debugf("APC instancesToRemove sync completed successfully")
	return nil
}

// Global manager support
var (
	// GlobalManager is the global APC manager instance
	GlobalManager atomic.Value // stores *Manager
)

// SetGlobalManager sets the global APC manager
func SetGlobalManager(manager *Manager) {
	GlobalManager.Store(manager)
}

// GetGlobalManager returns the global APC manager
func GetGlobalManager() *Manager {
	if m := GlobalManager.Load(); m != nil {
		return m.(*Manager)
	}
	return nil
}

// NewManagerAndStart creates an APC manager from configuration
// Responsibilities: unified APC manager creation and initialization entry point
func NewManagerAndStart(cfg *config.APCManagerConfig, clusterService cluster.ClusterService) (*Manager, error) {
	if clusterService == nil {
		return nil, fmt.Errorf("new APC Manager failed for nil cluster service")
	}

	if !cfg.Enable {
		logger.Log().Infof("APC Manager disabled")
		return nil, nil
	}

	strategyCount := 0
	if cfg.Strategies != nil {
		if cfg.Strategies.Hash != nil {
			strategyCount++
		}
	}

	logger.Log().Infof("Creating APC manager with config: enable=%v, strategies=%d",
		cfg.Enable, strategyCount)

	// Create APC manager
	apcManager, err := NewManager(cfg, clusterService)
	if apcManager == nil || err != nil {
		return nil, fmt.Errorf("failed to create APC manager: %v", err)
	}
	SetGlobalManager(apcManager)

	if err := apcManager.Start(); err != nil {
		return nil, fmt.Errorf("failed to start APC manager: %v", err)
	}
	logger.Log().Infof("APC manager started successfully")

	return apcManager, nil
}

// StateApplier interface implementation

// GetName returns the name of the APC Manager StateApplier
func (m *Manager) GetName() string {
	return "APCManager"
}

// OnLeaderChanged handles leader change events
func (m *Manager) OnLeaderChanged(newLeader *utils.Peer, isMeLeader bool, leaderRestoreFailures *utils.AllInstanceFailuresSnapshot) {
	logger.Log().Infof("APC Manager leader change: newLeader=%v, isMeLeader=%v", newLeader, isMeLeader)

	if isMeLeader {
		logger.Log().Infof("APC Manager became leader, loading persistence data")
	} else {
		logger.Log().Infof("APC Manager stepped down from leader")
		// Stepped down from leader, need to clean up
		m.cleanStrategies()
	}
}

// OnStateRestored handles cluster state restoration events
func (m *Manager) OnStateRestored(state *cluster.ClusterState) {
	logger.Log().Infof("APC Manager cluster state restored, version: %d", state.Version)
	// Consider two scenarios:
	// 1. Reconnection after etcd disconnection, perform cleanup
	// 2. Normal etcd startup, also perform cleanup
	m.cleanStrategies()
}

// OnAddPeer handles new peer addition events
func (m *Manager) OnAddPeer(peer *utils.Peer) {
	logger.Log().Infof("APC Manager peer added: %v", peer)
	// Add peer addition logic if needed
}

// OnRemovePeer handles peer removal events
func (m *Manager) OnRemovePeer(name string) {
	logger.Log().Infof("APC Manager peer removed: %s", name)
	// Add peer removal logic if needed
}

// OnAddInstance handles instance addition events
func (m *Manager) OnAddInstance(routes map[string]*config.Route, reason string) {
	logger.Log().Infof("APC Manager instance added [%s]: %v", reason, routes)
	// Add instance addition logic if needed
}

// OnRemoveInstance handles instance removal events
func (m *Manager) OnRemoveInstance(names []string, reason string) {
	logger.Log().Infof("APC Manager instance removed [%s]: %v", reason, names)
	// Add instance removal logic if needed
}
