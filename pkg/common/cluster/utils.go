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

package cluster

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	commonCfg "infer-dp-scheduler/pkg/common/config"
	"infer-dp-scheduler/pkg/common/logger"
	"infer-dp-scheduler/pkg/common/utils"

	"github.com/gin-gonic/gin"
	"github.com/goccy/go-yaml"
)

const (
	DefaultRetryTime              = 10
	DefaultRetryIntervalSec       = 5
	DefaultLeaseRefreshIntervalMS = 1000
	DefaultLeaseTtlSec            = 3
	DefaultTransportTimeout       = 10000
	LeaderElectionKey             = "rs-leader-election"
)

func ParsePeerReq(ctx *gin.Context) (*utils.Peer, error) {
	peer := &utils.Peer{}
	if err := json.NewDecoder(ctx.Request.Body).Decode(peer); err != nil {
		return nil, fmt.Errorf("failed to parse req body, expect peer, err = %v", err)
	}
	if err := utils.CheckResourceNameValid(peer.Name); err != nil {
		return nil, fmt.Errorf("invalid peer name: %v", err)
	}
	return peer, nil
}

func ParseLeaveRequest(ctx *gin.Context) (string, error) {
	leaveReq := &LeaveRequest{}
	if err := json.NewDecoder(ctx.Request.Body).Decode(leaveReq); err != nil {
		return "", fmt.Errorf("failed to parse leave req body, expect LeaveRequest, err = %v", err)
	}
	if err := utils.CheckResourceNameValid(leaveReq.LeavePeerName); err != nil {
		return "", fmt.Errorf("invalid peer name: %v", err)
	}
	return leaveReq.LeavePeerName, nil
}

func InitClusterConfig(cfg *commonCfg.Config) error {
	clusterCfg := &cfg.ClusterCfg
	switch clusterCfg.ClusterServiceType {
	case TypeSingleton, "":
		if !clusterCfg.RoleCandidate {
			logger.Log().Warnf("start in singleton mode, change role_candidate to true")
			clusterCfg.RoleCandidate = true
		}
		return nil
	case TypeEtcd:
		if clusterCfg.EtcdCfg == nil {
			return fmt.Errorf("etcd config is required when type is [etcd], got nil")
		}
		applyDefaultEtcdCfg(clusterCfg.EtcdCfg)
		applyDefaultEtcdHealthExpireTimeMs(clusterCfg, cfg.InstanceManagerCfg.HealthCheckConfig)
		return checkEtcdCfg(clusterCfg.EtcdCfg)
	case TypeK8sConfigMap:
		cmCfg, err := loadConfigMapConfig(clusterCfg.ConfigMapConfigFile)
		if err != nil {
			return fmt.Errorf("failed to load configmap config: %v", err)
		}
		applyDefaultConfigMapCfg(cmCfg)
		if err := checkConfigMapCfg(cmCfg); err != nil {
			return err
		}
		clusterCfg.ConfigMapCfg = cmCfg
	default:
		logger.Log().Warnf("unsupported cluster service type[%v]", clusterCfg.ClusterServiceType)
	}
	return nil
}

// loadConfigMapConfig reads and parses the k8s-configmap.yaml file referenced
// by ClusterConfig.ConfigMapConfigFile. The path may be absolute or relative.
// Relative paths are resolved against ENV_ROUTE_SERVER_CONFIG_PATH (the same
// directory route-server.yaml lives in), falling back to the process working
// directory if that env is unset.
func loadConfigMapConfig(configFile string) (*commonCfg.ConfigMapConfig, error) {
	if strings.TrimSpace(configFile) == "" {
		return nil, fmt.Errorf("configmap-config-file is required when type is [k8s-configmap]")
	}
	resolved := configFile
	if !filepath.IsAbs(configFile) {
		if base := os.Getenv("ENV_ROUTE_SERVER_CONFIG_PATH"); base != "" {
			resolved = filepath.Join(base, configFile)
		}
	}
	data, err := os.ReadFile(resolved)
	if err != nil {
		// Fall back to the literal path so the error message matches the config.
		return nil, fmt.Errorf("failed to read configmap config file %s: %v", configFile, err)
	}
	var cmCfg commonCfg.ConfigMapConfig
	if err := yaml.Unmarshal(data, &cmCfg); err != nil {
		return nil, fmt.Errorf("failed to unmarshal configmap config file %s: %v", configFile, err)
	}
	return &cmCfg, nil
}

func applyDefaultConfigMapCfg(cfg *commonCfg.ConfigMapConfig) {
	if cfg.LeaderElectionLeaseDurationSec <= 0 {
		cfg.LeaderElectionLeaseDurationSec = 15
	}
	if cfg.LeaderElectionRenewDeadlineSec <= 0 {
		cfg.LeaderElectionRenewDeadlineSec = 10
	}
	if cfg.LeaderElectionRetryPeriodSec <= 0 {
		cfg.LeaderElectionRetryPeriodSec = 2
	}
	if cfg.MinFailurePersistIntervalSec <= 0 {
		cfg.MinFailurePersistIntervalSec = 3
	}
	if cfg.LeaderElectionKey == "" {
		cfg.LeaderElectionKey = LeaderElectionKey
	}
}

func checkConfigMapCfg(cfg *commonCfg.ConfigMapConfig) error {
	if cfg.ConfigMapName == "" {
		return fmt.Errorf("configmap configmap-name is required")
	}
	if cfg.TopologyKey == "" {
		return fmt.Errorf("configmap topology-key is required")
	}
	if cfg.Namespace == "" {
		logger.Log().Infof("configmap namespace not configured, will use POD_NAMESPACE env or default")
	}
	if cfg.LeaderElectionLeaseDurationSec <= cfg.LeaderElectionRenewDeadlineSec {
		return fmt.Errorf("configmap leader election lease duration (%d) must be greater than renew deadline (%d)",
			cfg.LeaderElectionLeaseDurationSec, cfg.LeaderElectionRenewDeadlineSec)
	}
	if cfg.LeaderElectionRenewDeadlineSec <= cfg.LeaderElectionRetryPeriodSec {
		return fmt.Errorf("configmap leader election renew deadline (%d) must be greater than retry period (%d)",
			cfg.LeaderElectionRenewDeadlineSec, cfg.LeaderElectionRetryPeriodSec)
	}
	if cfg.MinFailurePersistIntervalSec < 0 {
		return fmt.Errorf("configmap min-failure-persist-interval-sec must not be negative, got %d",
			cfg.MinFailurePersistIntervalSec)
	}
	if cfg.OwnerReference != nil {
		if cfg.OwnerReference.APIVersion == "" {
			return fmt.Errorf("configmap owner-reference.api-version is required when owner-reference is configured")
		}
		if cfg.OwnerReference.Kind == "" {
			return fmt.Errorf("configmap owner-reference.kind is required when owner-reference is configured")
		}
		if cfg.OwnerReference.Name == "" {
			return fmt.Errorf("configmap owner-reference.name is required when owner-reference is configured")
		}
	}
	for i, ig := range cfg.InstanceGroups {
		if ig.GroupType == "" {
			return fmt.Errorf("configmap instance-groups[%d].group-type is empty", i)
		}
		if ig.GroupType == "master" {
			return fmt.Errorf("configmap instance-groups[%d].group-type must not be master (master is reserved for peers)", i)
		}
		if ig.EndpointCount <= 0 {
			return fmt.Errorf("configmap instance-groups[%d].endpoint-count must be positive, got %d", i, ig.EndpointCount)
		}
		if ig.StartingPort <= 0 || ig.StartingPort > 65535 {
			return fmt.Errorf("configmap instance-groups[%d].starting-port out of range [1,65535], got %d", i, ig.StartingPort)
		}
		if ig.StartingPort+ig.EndpointCount-1 > 65535 {
			return fmt.Errorf("configmap instance-groups[%d] port range [%d,%d] exceeds 65535",
				i, ig.StartingPort, ig.StartingPort+ig.EndpointCount-1)
		}
	}
	return nil
}

// applyDefaultEtcdHealthExpireTimeMs sets the default value for EtcdHealthExpireTimeMs
// Priority: config value > FailureSnapshotIntervalMs * 2 > RestHealthCommonConfig.Interval * 4
func applyDefaultEtcdHealthExpireTimeMs(clusterCfg *commonCfg.ClusterConfig, healthCfg *commonCfg.HealthCheckConfig) {
	if clusterCfg.EtcdHealthExpireTimeMs > 0 {
		return
	}

	failureSnapshotIntervalMs := 0
	restInterval := 10000 // RestHealthCommonConfig.Interval defaults to 10s
	if healthCfg != nil {
		failureSnapshotIntervalMs = healthCfg.FailureSnapshotIntervalMs
		if healthCfg.RestHealthCommonConfig.Interval > 0 {
			restInterval = healthCfg.RestHealthCommonConfig.Interval
		}
	}

	if failureSnapshotIntervalMs <= 0 {
		failureSnapshotIntervalMs = restInterval * 2
	}
	clusterCfg.EtcdHealthExpireTimeMs = failureSnapshotIntervalMs * 2
}

func applyDefaultEtcdCfg(config *commonCfg.EtcdConfig) {
	if config.DialTimeout == 0 {
		config.DialTimeout = DefaultTransportTimeout
	}
	if config.RetryTime == 0 {
		config.RetryTime = DefaultRetryTime
	}
	if config.RetryInterval == 0 {
		config.RetryInterval = DefaultRetryIntervalSec
	}
	if config.LeaseRefreshIntervalMs == 0 {
		config.LeaseRefreshIntervalMs = DefaultLeaseRefreshIntervalMS
	}
	if config.LeaseTtlSec == 0 {
		config.LeaseTtlSec = DefaultLeaseTtlSec
	}
}

func checkEtcdCfg(config *commonCfg.EtcdConfig) error {
	if len(config.EtcdEndpoints) == 0 {
		return fmt.Errorf("etcd endpoints is empty")
	}
	if config.LeaseTtlSec < 1 || config.LeaseTtlSec > 600 {
		return fmt.Errorf("etcd lease ttl sec is out of range [1,600], got %v", config.LeaseTtlSec)
	}
	if config.RetryTime < 0 {
		return fmt.Errorf("etcd retry time is negative, got %v", config.RetryTime)
	}
	if config.RetryInterval < 0 || config.RetryInterval > 60 {
		return fmt.Errorf("etcd retry interval is out of range [0,60], got %v", config.RetryInterval)
	}
	if config.LeaseRefreshIntervalMs < 50 || config.LeaseRefreshIntervalMs > 60000 {
		return fmt.Errorf("etcd lease refresh interval is out of range [50, 60000], got %v", config.LeaseRefreshIntervalMs)
	}
	if config.LeaseRefreshIntervalMs >= (config.LeaseTtlSec * 1000) {
		return fmt.Errorf("etcd lease refresh interval must be smaller than lease refresh interval %v ms, got %v",
			config.LeaseTtlSec*1000, config.LeaseRefreshIntervalMs)
	}
	if config.AutoSyncIntervalSec < 0 {
		return fmt.Errorf("etcd auto sync interval sec is negative, got %v", config.AutoSyncIntervalSec)
	}
	return nil
}

func CheckPeerValid(peer *utils.Peer) error {
	if peer == nil {
		return fmt.Errorf("peer must not be nil")
	}
	if peer.Name == "" {
		return fmt.Errorf("peer name is empty")
	}
	if err := utils.CheckResourceNameValid(peer.Name); err != nil {
		return fmt.Errorf("peer name is invalid: %v", err)
	}
	if peer.Addr.Host == "" {
		return fmt.Errorf("peer %s address host is empty", peer.Name)
	}
	if peer.Addr.HttpPort < 0 || peer.Addr.HttpPort > 65535 {
		return fmt.Errorf("peer %s http port %v is out of range", peer.Name, peer.Addr.HttpPort)
	}
	if peer.Addr.OpsPort < 0 || peer.Addr.OpsPort > 65535 {
		return fmt.Errorf("peer %s ops port %v is out of range", peer.Name, peer.Addr.OpsPort)
	}
	return nil
}
