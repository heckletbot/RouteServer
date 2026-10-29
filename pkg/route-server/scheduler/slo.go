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

package scheduler

import (
	"fmt"
	"math"
	"sort"
	"strconv"

	"infer-dp-scheduler/pkg/common/config"
	"infer-dp-scheduler/pkg/common/constant"
	"infer-dp-scheduler/pkg/common/instance-metrics"
	"infer-dp-scheduler/pkg/common/logger"
	"infer-dp-scheduler/pkg/common/utils"
)

type SLOManager struct {
	cfg *config.SchedulersCfg

	// tag key -> slo interval
	tagLookUp map[string]utils.SLOInterval

	// tag key -> instance id
	tagInstanceLookUp map[string]map[string]struct{}

	// instanceRole -> SLOAlgorithm
	roleLookUp map[string]*PromptLenSloAlgorithm
}

func newSLOManager(cfg *config.SchedulersCfg) (*SLOManager, error) {
	sloManager := &SLOManager{
		cfg: cfg,
	}
	if sloManager.cfg.SLOScheduling == nil {
		sloManager.cfg.SLOScheduling = &config.SLOSchedulerCfg{SloEnable: false}
	}
	if sloManager.cfg.SLOScheduling.SloEnable == false {
		return sloManager, nil
	}
	sloManager.tagLookUp = make(map[string]utils.SLOInterval)
	sloManager.roleLookUp = make(map[string]*PromptLenSloAlgorithm)
	// Define the slo groups here
	for _, cfg := range sloManager.cfg.SLOScheduling.SloManager {
		tag := cfg.Tag
		intervals := cfg.SloIntervals
		if _, exists := sloManager.tagLookUp[tag]; exists {
			return nil, fmt.Errorf("scheduler for slo tag %s is assigned to more than 1 scheduler", tag)
		}
		sloManager.tagLookUp[tag] = intervals
		if tag == constant.SLOTagDefault {
			return nil, fmt.Errorf("the slo tag [default] should not be declared in the configuration")
		}
	}
	sloManager.tagLookUp[constant.SLOTagDefault] = utils.SLOInterval{
		PromptStrLengthMax: "0",
	}
	for _, schedulerCfg := range sloManager.cfg.Schedulers {
		if _, exists := sloManager.roleLookUp[schedulerCfg.InstanceRole]; exists {
			return nil, fmt.Errorf("scheduler for InstanceRole %s is assigned to more than 1 scheduler", schedulerCfg.InstanceRole)
		}
		sloAlgorithm, err := newPromptLenSloAlgorithm(sloManager.cfg.SLOScheduling, schedulerCfg.InstanceRole)
		if err != nil {
			return nil, fmt.Errorf("init slo algorithm failed: %v", err)
		}
		sloManager.roleLookUp[schedulerCfg.InstanceRole] = sloAlgorithm
	}
	return sloManager, nil
}

func (slo *SLOManager) ChooseSLOTag(request utils.GenerateRequest, insBlackMap map[string]interface{}) (string, error) {
	if slo.cfg.SLOScheduling.SloEnable == false {
		return constant.SLOTagDefault, nil
	}
	sloa, exists := slo.roleLookUp[request.GetType()]
	if !exists {
		err := &RequestTypeNotFoundError{
			reqId:   request.GetRequestId(),
			reqType: request.GetType(),
		}
		return "", err
	}
	return sloa.ChooseSLOTag(request, insBlackMap)
}

func (slo *SLOManager) UpdateCluster(available map[string]map[string]instance_metrics.WorkerStats) {
	if slo.cfg.SLOScheduling.SloEnable == false {
		return
	}
	for _, sloAlgorithm := range slo.roleLookUp {
		sloAlgorithm.UpdateCluster(available)
	}
}

// check if instance with insId has this sloTag
func (slo *SLOManager) MatchSloTag(insId string, role string, sloTag string) bool {
	roleInfo, ok := slo.roleLookUp[role]
	if !ok || roleInfo.SLOGroups == nil {
		return false
	}

	tag, ok := roleInfo.SLOGroups[sloTag]
	if !ok || tag.instance == nil {
		return false
	}

	_, exists := tag.instance[insId]
	return exists
}

// Used to convert the interval in the config into a form for easier compare
// maybe we can define this in slo manager and replace all the config.SLOInterval
type GroupInterval struct {
	PromptStrLengthMax int
}

type SLOInstanceGroup struct {
	instance map[string]interface{}
	interval *GroupInterval
}

func CompareSLOGroups(sloGroupA, sloGroupB *SLOInstanceGroup) bool {
	return sloGroupA.interval.PromptStrLengthMax < sloGroupB.interval.PromptStrLengthMax
}

type PromptLenSloAlgorithm struct {
	cfg       *config.SLOSchedulerCfg
	role      string
	SLOGroups map[string]*SLOInstanceGroup
	// id -> tags by order
	orderedSLOTags []string
}

func newPromptLenSloAlgorithm(cfg *config.SLOSchedulerCfg, role string) (*PromptLenSloAlgorithm, error) {
	sloa := &PromptLenSloAlgorithm{
		cfg:  cfg,
		role: role,
	}
	if !sloa.cfg.SloEnable {
		return sloa, nil
	}
	sloa.SLOGroups = make(map[string]*SLOInstanceGroup)
	// default group always exists, but will not be ordered
	sloGroup := &SLOInstanceGroup{
		instance: map[string]interface{}{},
		interval: &GroupInterval{},
	}
	sloGroup.interval.PromptStrLengthMax = 0
	sloa.SLOGroups[constant.SLOTagDefault] = sloGroup

	// now decode only has the default group
	if sloa.role == constant.Decode {
		return sloa, nil
	}
	for _, info := range sloa.cfg.SloManager {
		sloGroup := &SLOInstanceGroup{
			instance: map[string]interface{}{},
			interval: &GroupInterval{},
		}
		if &info.SloIntervals == nil {
			return nil, fmt.Errorf("slo tag %v has nil interval", info.Tag)
		}
		promptLen, err := getSLOInfo(&info.SloIntervals)
		if err != nil {
			return nil, fmt.Errorf("invalid PromptStrLengthMax for SLOGroup %v: %v", info.Tag, err)
		}
		sloGroup.interval.PromptStrLengthMax = promptLen
		sloa.SLOGroups[info.Tag] = sloGroup
		sloa.orderedSLOTags = append(sloa.orderedSLOTags, info.Tag)
	}
	sort.Slice(sloa.orderedSLOTags, func(i, j int) bool {
		sloGroupI := sloa.SLOGroups[sloa.orderedSLOTags[i]]
		sloGroupJ := sloa.SLOGroups[sloa.orderedSLOTags[j]]
		return CompareSLOGroups(sloGroupI, sloGroupJ)
	})
	return sloa, nil
}

func (sloa *PromptLenSloAlgorithm) isGroupAvailable(tag string, insBlackMap map[string]interface{}) bool {
	sloGroup := sloa.SLOGroups[tag]
	insCnt := len(sloGroup.instance)
	hasBlacklist := false
	if insBlackMap != nil {
		if len(insBlackMap) != 0 {
			hasBlacklist = true
		}
	}
	if hasBlacklist {
		for insId := range sloGroup.instance {
			_, exist := (insBlackMap)[insId]
			if exist {
				insCnt--
			}
		}
	}
	if insCnt >= sloa.cfg.SloGroupHealthyThreshold {
		return true
	}
	return false
}

func (sloa *PromptLenSloAlgorithm) ChooseSLOTag(request utils.GenerateRequest, insBlackMap map[string]interface{}) (string, error) {
	var promptLength int
	var err error
	if request.GetSLOInterval() == nil {
		logger.Log().Warnf("no slo interval for request %v, return the largest slo tag", request.GetRequestId())
		promptLength = math.MaxInt
	} else {
		promptLength, err = getSLOInfo(request.GetSLOInterval())
		if err != nil {
			return "", fmt.Errorf("invalid PromptStrLengthMax for request %v slo %v", request.GetRequestId(), err)
		}
	}
	index := sort.Search(len(sloa.orderedSLOTags), func(i int) bool {
		return sloa.SLOGroups[sloa.orderedSLOTags[i]].interval.PromptStrLengthMax >= int(promptLength)
	})
	for i := index; i < len(sloa.orderedSLOTags); i++ {
		if sloa.isGroupAvailable(sloa.orderedSLOTags[i], insBlackMap) {
			return sloa.orderedSLOTags[i], nil
		}
	}
	if sloa.cfg.SloFallbackEnable {
		for i := index - 1; i >= 0; i-- {
			if sloa.isGroupAvailable(sloa.orderedSLOTags[i], insBlackMap) {
				return sloa.orderedSLOTags[i], nil
			}
		}
	}
	if sloa.isGroupAvailable(constant.SLOTagDefault, insBlackMap) {
		return constant.SLOTagDefault, nil
	}
	return "", nil
}

// stats <Role: <InstanceID: InstanceStats>>
func (sloa *PromptLenSloAlgorithm) UpdateCluster(available map[string]map[string]instance_metrics.WorkerStats) {
	// tagAvailableInsMap <Tag: <InstanceId: InstanceStats>>
	tagAvailableInsMap := reorganizeSliceBySLOTag(available, sloa.role, sloa.cfg.SloEnable)

	// update SLO Algorithm
	for tag, SLOGroup := range sloa.SLOGroups {
		info, _ := tagAvailableInsMap[tag]
		SLOGroup.instance = make(map[string]interface{})

		for _, id := range info {
			SLOGroup.instance[id] = nil
		}
	}
}

func getSLOInfo(interval *utils.SLOInterval) (int, error) {
	if interval.PromptStrLengthMax == constant.Inf {
		return math.MaxInt, nil
	}
	return strconv.Atoi(interval.PromptStrLengthMax)
}
