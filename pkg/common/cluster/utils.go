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

package cluster

import (
	"encoding/json"
	"fmt"

	commonCfg "infer-dp-scheduler/pkg/common/config"
	"infer-dp-scheduler/pkg/common/logger"
	"infer-dp-scheduler/pkg/common/utils"

	"github.com/gin-gonic/gin"
)

const (
	DefaultRetryTime              = 10
	DefaultRetryIntervalSec       = 5
	DefaultLeaseRefreshIntervalMS = 1000
	DefaultLeaseTtlSec            = 3
	DefaultTransportTimeout       = 10000
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
	default:
		logger.Log().Warnf("unsupported cluster service type[%v]", clusterCfg.ClusterServiceType)
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
