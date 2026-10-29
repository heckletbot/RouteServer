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
	"infer-dp-scheduler/pkg/common/cluster"
	"infer-dp-scheduler/pkg/common/config"
	"infer-dp-scheduler/pkg/common/logger"
	"infer-dp-scheduler/pkg/common/utils"
)

type StateChangeReport struct {
}

func (s StateChangeReport) GetName() string {
	return "StateChangeReport"
}

func (s StateChangeReport) OnStateRestored(state *cluster.ClusterState) {
	if state == nil {
		logger.Log().Errorf("ClusterState Changes: cluster state is nil.")
		return
	}
	marshal, err := utils.SonicApi.Marshal(state)
	if err != nil {
		logger.Log().Errorf("ClusterState Changes: failed to marshal state: %v", err)
	} else {
		logger.Log().Infof("ClusterState Changes: state restored, %s", string(marshal))
	}
}

func (s StateChangeReport) OnLeaderChanged(newLeader *utils.Peer, isMeLeader bool, leaderRestoreFailures *utils.AllInstanceFailuresSnapshot) {
	logger.Log().Infof("ClusterState Changes: leader change in cluster, new leader %v, isMe: %v",
		newLeader, isMeLeader)

	if leaderRestoreFailures != nil {
		marshal, err := utils.SonicApi.Marshal(leaderRestoreFailures)
		if err != nil {
			logger.Log().Errorf("ClusterState Changes: failed to marshal leaderRestoreFailures: %v", err)
		} else {
			logger.Log().Infof("ClusterState Changes: leader instance failures restored, %s", string(marshal))
		}
	}
}

func (s StateChangeReport) OnAddPeer(peer *utils.Peer) {
	logger.Log().Infof("ClusterState Changes: add peer %v", peer)
}

func (s StateChangeReport) OnRemovePeer(name string) {
	logger.Log().Infof("ClusterState Changes: remove peer %s", name)
}

func (s StateChangeReport) OnAddInstance(routes map[string]*config.Route, reason string) {
	for instanceId, instance := range routes {
		logger.Log().Infof("ClusterState Changes [%s]: add instance name=%s. route=%v", reason, instanceId, instance)
	}
}

func (s StateChangeReport) OnRemoveInstance(names []string, reason string) {
	for _, instanceId := range names {
		logger.Log().Infof("ClusterState Changes [%s]: remove instance name=%s", reason, instanceId)
	}
}
