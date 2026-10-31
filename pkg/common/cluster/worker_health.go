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
	"sync"
	"sync/atomic"
	"time"

	"infer-dp-scheduler/cmd/route_server/server/options"
	"infer-dp-scheduler/pkg/common/logger"
	"infer-dp-scheduler/pkg/common/utils"
	"infer-dp-scheduler/pkg/route-server/metrics"
)

const (
	WorkerFailureCheckIntervalSec = 5
)

const (
	LeaveReasonRequested = "requested"
	LeaveReasonFailed    = "failed"
)

type WorkerHealthChecker struct {
	enable           bool
	maxDownTime      time.Duration
	clusterService   ClusterService
	workerPingChan   chan string
	leaveWorkersChan chan []string
	lastPingTimes    map[string]time.Time
	closeChan        chan struct{}
	closed           atomic.Bool
	wg               sync.WaitGroup
}

func NewWorkerHealthChecker(clusterService ClusterService) *WorkerHealthChecker {
	if options.GetConfig().ClusterCfg.ClusterServiceType == TypeK8sConfigMap {
		logger.Log().Infof("skip WorkerHealthChecker in k8s-configmap mode, worker health is managed by K8s pod lifecycle")
		return &WorkerHealthChecker{
			enable:    false,
			closed:    atomic.Bool{},
			closeChan: make(chan struct{}),
		}
	}
	if options.GetConfig().MetaCleanupCfg == nil || options.GetConfig().MetaCleanupCfg.MaxWorkerDownTimeSec <= 0 {
		logger.Log().Warnf("route server worker checker is disabled due to negative max-workers-down-time-sec")
		return &WorkerHealthChecker{
			enable:    false,
			closed:    atomic.Bool{},
			closeChan: make(chan struct{}),
		}
	}

	if options.GetConfig().MetaCleanupCfg.MaxInstanceDownTimeSec < 30 {
		logger.Log().Warnf(
			"route server worker checker max down time is too small [%v] sec, reset to at least 30 sec",
			options.GetConfig().MetaCleanupCfg.MaxInstanceDownTimeSec)
		options.GetConfig().MetaCleanupCfg.MaxInstanceDownTimeSec = 30
	}

	if !options.GetConfig().ClusterCfg.RoleCandidate {
		return &WorkerHealthChecker{
			enable:    false,
			closed:    atomic.Bool{},
			closeChan: make(chan struct{}),
		}
	}

	checker := &WorkerHealthChecker{
		enable:           true,
		closed:           atomic.Bool{},
		clusterService:   clusterService,
		workerPingChan:   make(chan string, 1000),
		leaveWorkersChan: make(chan []string, 1000),
		lastPingTimes:    make(map[string]time.Time),
		closeChan:        make(chan struct{}),
		wg:               sync.WaitGroup{},
		maxDownTime:      time.Duration(options.GetConfig().MetaCleanupCfg.MaxWorkerDownTimeSec) * time.Second,
	}

	go checker.checkNotRespondingWorkersLoop()
	go checker.leavePeerLoop()
	return checker
}

func (ceh *WorkerHealthChecker) HandleWorkerPing(workerId string) {
	if !ceh.enable {
		return
	}
	ceh.workerPingChan <- workerId
}

func (ceh *WorkerHealthChecker) checkNotRespondingWorkersLoop() {
	ceh.wg.Add(1)
	defer ceh.wg.Done()
	checkInterval := time.Duration(WorkerFailureCheckIntervalSec) * time.Second
	ticker := time.NewTicker(checkInterval)
	defer ticker.Stop()
	logger.Log().Infof("starting route server worker checkloop")
	lastSeenLeaderId := ""
	for {
		select {
		case <-ceh.closeChan:
			logger.Log().Infof("stop route server worker checkloop")
			return
		case activeWorkerId, ok := <-ceh.workerPingChan:
			if !ok {
				logger.Log().Warnf("stop route server worker checkloop")
			}
			ceh.lastPingTimes[activeWorkerId] = time.Now()
		case <-ticker.C:
			if !ceh.clusterService.IsLeader() {
				_, leaderName := ceh.clusterService.GetLeader()
				lastSeenLeaderId = leaderName
				continue
			}
			currState := ceh.clusterService.State()
			now := time.Now()
			if ceh.clusterService.LocalNode().Name != lastSeenLeaderId {
				logger.Log().Infof("step up as leader, reset all rs worker active time, last seen leader [%s]",
					lastSeenLeaderId)
				lastSeenLeaderId = ceh.clusterService.LocalNode().Name
				clear(ceh.lastPingTimes)
				for peerName, peer := range currState.Peers {
					if peer.IsCandidate {
						continue
					}
					ceh.lastPingTimes[peerName] = now
				}
				continue
			}

			// clean already removed peer
			for workerId, _ := range ceh.lastPingTimes {
				if _, exists := currState.Peers[workerId]; !exists {
					delete(ceh.lastPingTimes, workerId)
				}
			}

			leavePeers := make([]string, 0)
			workersPingTimeout := 0
			for peerName, peer := range currState.Peers {
				shouldDiscard, pingTimeout := ceh.checkSingleWorker(peerName, peer, now)
				if shouldDiscard {
					leavePeers = append(leavePeers, peerName)
				}
				if pingTimeout {
					workersPingTimeout++
				}
			}
			if len(leavePeers) > 0 {
				logger.Log().Warnf("detect leave peers not sync event at least %s, cleaning up these peers %v",
					ceh.maxDownTime.String(), leavePeers)
				ceh.leaveWorkersChan <- leavePeers
			}
			metrics.RecordUnreachableWorkersNum(workersPingTimeout)
		}
	}
}

func (ceh *WorkerHealthChecker) checkSingleWorker(peerName string, peer *utils.Peer, now time.Time) (shouldDiscard, pingTimeout bool) {
	if peer.IsCandidate {
		return
	}
	activeTime, exists := ceh.lastPingTimes[peerName]
	if !exists {
		ceh.lastPingTimes[peerName] = now
		return
	}

	leaveDuration := now.Sub(activeTime)
	if leaveDuration < ceh.maxDownTime {
		return
	}

	pingTimeout = true
	// worker has been unreachable more than setting threshold, check if it's alive
	if alive, err := ceh.clusterService.IsWorkerAlive(peerName); err == nil && !alive {
		shouldDiscard = true
		return
	} else if err != nil {
		logger.Log().Warnf(
			"fail to check if the unreachable peer [%s] [%s] alive or not, skip this discarding check next time, detail err = %v",
			peerName, leaveDuration.String(), err)
		return
	} else {
		logger.Log().Warnf(
			"The unreachable peer [%s] [%s] is alive, skip this discarding check next time",
			peerName, leaveDuration.String())
		return
	}
}

func (ceh *WorkerHealthChecker) leavePeerLoop() {
	ceh.wg.Add(1)
	defer ceh.wg.Done()
	for {
		select {
		case <-ceh.closeChan:
			logger.Log().Infof("leave peer loop stopped")
			return
		case leaveWorkerIds, ok := <-ceh.leaveWorkersChan:
			if !ok {
				logger.Log().Infof("leave peer loop stopped")
			}
			err := ceh.clusterService.HandleLeavePeer(leaveWorkerIds, LeaveReasonFailed)
			if err != nil {
				logger.Log().Warnf("failed to handle leave failure peers [%v] from worker health: %v",
					leaveWorkerIds, err)
			} else {
				metrics.RecordDiscardUnreachableWorkers()
			}
		}
	}
}

func (ceh *WorkerHealthChecker) Close() {
	close(ceh.closeChan)
	ceh.wg.Wait()
	if ceh.workerPingChan != nil {
		close(ceh.workerPingChan)
	}
	if ceh.leaveWorkersChan != nil {
		close(ceh.leaveWorkersChan)
	}
}
