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

package health

import (
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"infer-dp-scheduler/pkg/common/utils"
	"infer-dp-scheduler/pkg/route-server/metrics"

	"infer-dp-scheduler/pkg/common/client"
	"infer-dp-scheduler/pkg/common/config"
	"infer-dp-scheduler/pkg/common/constant"
	"infer-dp-scheduler/pkg/common/logger"
)

// pd link block detail data
type PDBlock struct {
	// this is short because after first block it will not increase
	// actually we can promise the slice is ordered
	// so if this is too long, use queue here
	linkDownTimestamps []int64
	// record block time after reach the link-block-threshold
	isBlock             bool
	firstBlockTimestamp int64
	lastBlockTimestamp  int64
}

// InstanceBlacklist blacklist for a single instance
type InstanceBlacklist struct {
	blacklist map[string]*PDBlock
	blockNum  int
	role      string
}

type PDLinkBlacklistManager struct {
	linkBlacklist map[string]*InstanceBlacklist
	mutex         sync.RWMutex
	cfg           *config.LinkBlacklistConfig
	checker       *LinkBlacklistCheckerModule
	// channel to close the clear loop
	closeChan chan struct{}
}

func newInstanceBlacklist(role string) *InstanceBlacklist {
	return &InstanceBlacklist{
		blacklist: make(map[string]*PDBlock),
		role:      role,
	}
}

func NewPDLinkBlacklistManager(listConfig *config.LinkBlacklistConfig) *PDLinkBlacklistManager {
	newLinkBlacklistManager := &PDLinkBlacklistManager{
		linkBlacklist: make(map[string]*InstanceBlacklist),
		mutex:         sync.RWMutex{},
		cfg:           listConfig,
		closeChan:     make(chan struct{}),
	}
	return newLinkBlacklistManager
}

func (pm *PDLinkBlacklistManager) setLinkBlacklistChecker(checker *LinkBlacklistCheckerModule) {
	pm.checker = checker
}

// pair struct for prefill and decode pair manage
type pdPair struct {
	insIdA string
	insIdB string
}

func (pm *PDLinkBlacklistManager) deletePDBlock(pair pdPair) {
	delete(pm.linkBlacklist[pair.insIdA].blacklist, pair.insIdB)
	if len(pm.linkBlacklist[pair.insIdA].blacklist) == 0 {
		delete(pm.linkBlacklist, pair.insIdA)
	}
}

func (pm *PDLinkBlacklistManager) clearAgingBlock() {
	pm.mutex.Lock()
	defer pm.mutex.Unlock()
	var agingPair []pdPair
	timestamp := time.Now().UnixMilli()
	for insId, insList := range pm.linkBlacklist {
		for blockInsId, pdBlock := range insList.blacklist {
			newTail := 0
			for _, downTimestamp := range pdBlock.linkDownTimestamps {
				// this will skip aging timestamp
				if timestamp-downTimestamp <= pm.cfg.LinkBlockWindowsMs {
					pdBlock.linkDownTimestamps[newTail] = downTimestamp
					newTail++
				}
			}
			pdBlock.linkDownTimestamps = pdBlock.linkDownTimestamps[:newTail]
			if pdBlock.isBlock {
				if timestamp-pdBlock.lastBlockTimestamp >= pm.cfg.LinkBlockTagValidTimeMs {
					agingSeconds := (timestamp - pdBlock.lastBlockTimestamp) / 1000
					logger.Log().Warnf("instance %s unblocked from instance %s after %d seconds without link down errors", insId, blockInsId, agingSeconds)
					pdBlock.isBlock = false
					pm.linkBlacklist[insId].blockNum--
				}
			}
			if pdBlock.isBlock == false && len(pdBlock.linkDownTimestamps) == 0 {
				agingPair = append(agingPair, pdPair{insId, blockInsId})
			}
		}
	}

	if len(agingPair) != 0 {
		for _, pair := range agingPair {
			pm.deletePDBlock(pair)
		}
		pm.updateMetrics()
	}
}

func (pm *PDLinkBlacklistManager) OnAllInstancesRenewed(available, unavailable map[string]*client.Instance) {
	if !pm.cfg.Enable {
		return
	}
	pm.mutex.Lock()
	defer pm.mutex.Unlock()
	instancesId := make(map[string]bool)
	for _, instance := range available {
		instancesId[instance.ID] = true
	}
	for _, instance := range unavailable {
		instancesId[instance.ID] = true
	}
	// delete instances
	var leavingInsId []string
	for insId, _ := range pm.linkBlacklist {
		_, exist := instancesId[insId]
		if !exist {
			leavingInsId = append(leavingInsId, insId)
		}
	}
	for _, insId := range leavingInsId {
		pm.linkBlacklist[insId].blacklist = make(map[string]*PDBlock)
		delete(pm.linkBlacklist, insId)
	}

	// delete blocks
	var leavingPair []pdPair
	for insId, insList := range pm.linkBlacklist {
		for blockInsId, _ := range insList.blacklist {
			_, exist := instancesId[blockInsId]
			if !exist {
				leavingPair = append(leavingPair, pdPair{insId, blockInsId})
			}
		}
	}

	if len(leavingPair) != 0 {
		for _, pair := range leavingPair {
			pm.deletePDBlock(pair)
		}
		pm.updateMetrics()
	}
}

func (pm *PDLinkBlacklistManager) updatePDBlock(insId string, blockInsId string, timestamp int64) int {
	instanceBlock := pm.linkBlacklist[insId]
	if instanceBlock.blacklist[blockInsId] == nil {
		instanceBlock.blacklist[blockInsId] = &PDBlock{
			linkDownTimestamps:  make([]int64, 0),
			firstBlockTimestamp: 0,
			lastBlockTimestamp:  0,
			// it is not block until it is link down error reach the threshold
			isBlock: false,
		}
	}
	block := instanceBlock.blacklist[blockInsId]
	block.linkDownTimestamps = append(block.linkDownTimestamps, timestamp)
	if len(block.linkDownTimestamps) >= pm.cfg.LinkBlockThreshold {
		block.lastBlockTimestamp = timestamp
		if !block.isBlock {
			logger.Log().Warnf("instance %s has link down [%v] times with instance %s and is blocked now", insId, len(block.linkDownTimestamps), blockInsId)
			block.isBlock = true
			instanceBlock.blockNum++
			block.firstBlockTimestamp = timestamp
		}
	}
	return instanceBlock.blockNum
}

func (pm *PDLinkBlacklistManager) updateMetrics() {
	metrics.ResetLinkBlacklistSize()
	for _, instance := range pm.linkBlacklist {
		metrics.SetLinkBlacklistSize(instance.role, LinkTypePD, instance.blockNum)
	}
}

func (pm *PDLinkBlacklistManager) RefreshBlacklist(insPrefill string, insDecode string) (int, int) {
	if !pm.cfg.Enable {
		return 0, 0
	}
	// record both sides blacklist because we need to check Decode's health situation
	timestamp := time.Now().UnixMilli()
	pm.mutex.Lock()
	defer pm.mutex.Unlock()
	if pm.linkBlacklist[insPrefill] == nil {
		pm.linkBlacklist[insPrefill] = newInstanceBlacklist(constant.Prefill)
	}
	prefillBlacklistNum := pm.updatePDBlock(insPrefill, insDecode, timestamp)

	if pm.linkBlacklist[insDecode] == nil {
		pm.linkBlacklist[insDecode] = newInstanceBlacklist(constant.Decode)
	}
	decodeBlacklistNum := pm.updatePDBlock(insDecode, insPrefill, timestamp)
	pm.updateMetrics()
	return prefillBlacklistNum, decodeBlacklistNum
}

func (pm *PDLinkBlacklistManager) GetBlacklist(insId string) map[string]interface{} {
	if !pm.cfg.Enable {
		return nil
	}
	pm.mutex.RLock()
	defer pm.mutex.RUnlock()
	res := make(map[string]interface{})
	if pm.linkBlacklist[insId] == nil {
		return res
	}
	for blockInsId, block := range pm.linkBlacklist[insId].blacklist {
		if block.isBlock {
			res[blockInsId] = nil
		}
	}
	return res
}

func (pm *PDLinkBlacklistManager) runLinkBlacklistClearLoop() {
	if !pm.cfg.Enable {
		return
	}
	Interval := time.Duration(pm.cfg.EntryGCIntervalMs) * time.Millisecond
	ticker := time.NewTicker(Interval)
	defer ticker.Stop()
	for {
		select {
		case <-pm.closeChan:
			// maybe log something here
			return
		case <-ticker.C:
			pm.clearAgingBlock()
		}
	}
}

func (pm *PDLinkBlacklistManager) Stop() {
	close(pm.closeChan)
}

// handle the black list query request

type BlacklistResponse struct {
	TotalHealthyInstance map[string]int `json:"total_healthy_instance"`
	// role -> list mapping
	InsBlackLists map[string][]InsBlacklist `json:"instance_black_lists"`
}

type InsBlacklist struct {
	InsId         string           `json:"instance_id"`
	BlocksSize    int              `json:"blocks_size"`
	LinkBlackList []InsBlockDetail `json:"link_black_list"`
}

type InsBlockDetail struct {
	InsId           string    `json:"blocked_instance"`
	BlockTime       time.Time `json:"block_time"`
	RemainingTimeMs int64     `json:"remaining_time_ms"`
}

func (pm *PDLinkBlacklistManager) HandleGetAll(ctx *gin.Context) {
	healthyPrefillNum, healthyDecodeNum := pm.checker.leaderHealthChecker.GetHealthyInstanceNum()
	pm.mutex.RLock()
	defer pm.mutex.RUnlock()
	res := BlacklistResponse{
		TotalHealthyInstance: make(map[string]int),
		InsBlackLists:        make(map[string][]InsBlacklist),
	}
	res.TotalHealthyInstance[constant.Prefill] = healthyPrefillNum
	res.TotalHealthyInstance[constant.Decode] = healthyDecodeNum
	res.InsBlackLists[constant.Prefill] = make([]InsBlacklist, 0)
	res.InsBlackLists[constant.Decode] = make([]InsBlacklist, 0)
	for insId, insList := range pm.linkBlacklist {
		var blacklist InsBlacklist
		blacklist.InsId = insId
		timestamp := time.Now().UnixMilli()
		for blockInsId, pdBlock := range insList.blacklist {
			if !pdBlock.isBlock {
				continue
			}
			var blockDetail InsBlockDetail
			blockDetail.InsId = blockInsId
			seconds := pdBlock.firstBlockTimestamp / 1000
			nanoseconds := (pdBlock.firstBlockTimestamp % 1000) * 1000000
			blockDetail.BlockTime = time.Unix(seconds, nanoseconds)
			blockDetail.RemainingTimeMs = pm.cfg.LinkBlockTagValidTimeMs - (timestamp - pdBlock.firstBlockTimestamp)
			blacklist.LinkBlackList = append(blacklist.LinkBlackList, blockDetail)
		}
		blacklist.BlocksSize = len(blacklist.LinkBlackList)
		res.InsBlackLists[insList.role] = append(res.InsBlackLists[insList.role], blacklist)
	}
	ctx.JSON(http.StatusOK, res)
	return
}

type LinkBlacklistCheckerModule struct {
	cfg                  *config.LinkBlacklistConfig
	subCheckerEventsChan chan []*utils.RequestEventMsg
	failureInstanceChan  chan *LeaderSubCheckModuleEvent
	instancesInfo        map[string]*InstanceFailureInfo
	leaderHealthChecker  *LeaderHealthChecker

	mu              sync.RWMutex
	totalPrefillNum int
	totalDecodeNum  int
	closeChan       chan interface{}
	// pdLinkBlacklistManager manage link blacklist
	pdLinkBlacklistManager *PDLinkBlacklistManager
}

func NewLinkBlacklistChecker(checker *LeaderHealthChecker, manager *PDLinkBlacklistManager) *LinkBlacklistCheckerModule {
	linkBlackListConfig := &checker.cfg.AvailableCheckersCfg.LinkBlacklistConfig
	linkDownChecker := &LinkBlacklistCheckerModule{
		leaderHealthChecker:    checker,
		cfg:                    linkBlackListConfig,
		subCheckerEventsChan:   make(chan []*utils.RequestEventMsg, 1000),
		closeChan:              make(chan interface{}),
		pdLinkBlacklistManager: manager,
	}
	manager.setLinkBlacklistChecker(linkDownChecker)
	return linkDownChecker
}

func (lbm *LinkBlacklistCheckerModule) runLinkBlacklistLoop() {
	if !lbm.cfg.Enable {
		return
	}
	go lbm.pdLinkBlacklistManager.runLinkBlacklistClearLoop()
	for {
		if !lbm.leaderHealthChecker.isMeLeaderFunc() {
			time.Sleep(LeaderCheckInternal)
			continue
		}
		select {
		case <-lbm.closeChan:
			logger.Log().Infof("linkdown checker stopped")
			return
		case event, ok := <-lbm.subCheckerEventsChan:
			if !ok {
				return
			}
			for _, msg := range event {
				if msg.FailureDetails != nil && msg.FailureDetails.IsKVTransferErr {
					lbm.handleLinkDownEvent(msg)
				}
			}
		}
	}
}

func (lbm *LinkBlacklistCheckerModule) isBlacklistExceeded(blacklistNum int, healthyInsNum int) bool {
	if healthyInsNum == 0 {
		return false
	}
	if float64(blacklistNum*100)/float64(healthyInsNum) > float64(lbm.cfg.InstanceBlockRatio) {
		return true
	}
	return false
}

func (lbm *LinkBlacklistCheckerModule) handleLinkDownEvent(msg *utils.RequestEventMsg) {
	prefillId := msg.PreInstance
	if prefillId == "" {
		// link down only happens when decode error occurs, prefill instance shouldn't be nil
		return
	}
	prefillInstance, isInstanceHealthy := lbm.leaderHealthChecker.findInstanceWithStatus(prefillId)
	if prefillInstance == nil || !isInstanceHealthy {
		return
	}
	decodeId := msg.TargetInstance
	decodeInstance, isHealthy := lbm.leaderHealthChecker.findInstanceWithStatus(decodeId)
	if decodeInstance == nil || !isHealthy {
		return
	}

	prefillBlackListSize, decodeBlacklistSize := lbm.pdLinkBlacklistManager.RefreshBlacklist(prefillId, decodeId)
	events := make([]*LeaderSubCheckModuleEvent, 0)
	// prefill's blacklist contains decode instances
	isExceeded := lbm.isBlacklistExceeded(prefillBlackListSize, lbm.totalDecodeNum)
	if isExceeded {
		logger.Log().Errorf("instance %s(role:prefill) link black list length is %d, and total decode instance num is %d",
			prefillInstance.Client.GetEndpoint(), prefillBlackListSize, lbm.totalDecodeNum)
		events = append(events, &LeaderSubCheckModuleEvent{
			EventType:  LinkBlacklist,
			EventMsg:   "instance link blacklist percent exceed threshold",
			InstanceId: prefillId,
		})
	}
	// decode's blacklist contains prefill instances
	lbm.mu.RLock()
	defer lbm.mu.RUnlock()
	isExceeded = lbm.isBlacklistExceeded(decodeBlacklistSize, lbm.totalPrefillNum)
	if isExceeded {
		logger.Log().Errorf("instance %s(role:decode) link black list length is %d, and total prefill instance num is %d",
			decodeInstance.Client.GetEndpoint(), decodeBlacklistSize, lbm.totalPrefillNum)
		events = append(events, &LeaderSubCheckModuleEvent{
			EventType:  LinkBlacklist,
			EventMsg:   "instance link blacklist percent exceed threshold",
			InstanceId: decodeId,
		})
	}
	lbm.leaderHealthChecker.OnSubCheckModuleEvent(events)
}

func (lbm *LinkBlacklistCheckerModule) OnAllInstancesRenewed(available, unavailable map[string]*client.Instance) {
	lbm.mu.Lock()
	lbm.totalPrefillNum = 0
	lbm.totalDecodeNum = 0
	for _, instance := range available {
		if instance.Stats.GetRole() == constant.Prefill {
			lbm.totalPrefillNum++
		}
		if instance.Stats.GetRole() == constant.Decode {
			lbm.totalDecodeNum++
		}
	}
	for _, instance := range unavailable {
		if available[instance.ID] != nil {
			continue
		}
		if instance.Stats.GetRole() == constant.Prefill {
			lbm.totalPrefillNum++
		}
		if instance.Stats.GetRole() == constant.Decode {
			lbm.totalDecodeNum++
		}
	}
	lbm.mu.Unlock()
	lbm.pdLinkBlacklistManager.OnAllInstancesRenewed(available, unavailable)
}

func (lbm *LinkBlacklistCheckerModule) OnRequestEvents(workerId string, workerBootId string, requestEvents []*utils.RequestEventMsg) {
	if !lbm.cfg.Enable {
		return
	}
	lbm.subCheckerEventsChan <- requestEvents
}

func (lbm *LinkBlacklistCheckerModule) OnInstanceTokenEvent(workerId string, instanceEvents []*utils.InstanceEventMsg) {
}

// IsInstanceStillBlocked returns true when the blacklist has not aged out yet,
// meaning the KV transfer issue may still exist for the instance.
func (lbm *LinkBlacklistCheckerModule) IsInstanceStillBlocked(instanceId string) bool {
	if !lbm.cfg.Enable {
		return false
	}
	return len(lbm.pdLinkBlacklistManager.GetBlacklist(instanceId)) > 0
}

func (lbm *LinkBlacklistCheckerModule) Stop() {
	lbm.pdLinkBlacklistManager.Stop()
	close(lbm.closeChan)
}
