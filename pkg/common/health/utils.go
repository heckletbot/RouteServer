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
	"errors"
	"fmt"
	"reflect"
	"time"

	"route-server/pkg/common/config"
	"route-server/pkg/common/constant"
	"route-server/pkg/common/logger"
	"route-server/pkg/common/utils"
)

// LeaderCheckInternal is used to pause 100ms between two leader checker of leader_health loops
const LeaderCheckInternal = time.Duration(100) * time.Millisecond

const LinkTypePD = "pd"

func NewRequestEvent(request utils.GenerateRequest, inferMsg *InferMsg, details *utils.InstanceFailureDetails) *utils.RequestEventMsg {
	event := &utils.RequestEventMsg{
		Request:            utils.WrapRequest(request, false),
		TargetInstance:     inferMsg.getTargetInstance(request.GetType()),
		PreInstance:        inferMsg.getPreInstance(request.GetType()),
		FailureDetails:     details,
		InternalTestingReq: inferMsg.IsTesting,
		TimeStamp:          time.Now().UnixMilli(),
	}
	if eventType, exists := constant.ReqFailedEventMap[request.GetType()]; exists {
		event.EventType = eventType
	} else {
		logger.Log().Warnf("Unknown request type: %s", request.GetType())
	}
	return event
}

func ApplyDefaultAndCheckCfg(cfg *config.HealthCheckConfig) (*config.HealthCheckConfig, error) {
	if cfg == nil {
		defaultCfg := &config.HealthCheckConfig{}
		applyDefaultConfig(defaultCfg)
		return defaultCfg, nil
	}

	applyDefaultConfig(cfg)
	if err := checkConfig(cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

func applyDefaultConfig(cfg *config.HealthCheckConfig) {
	statusCodes := [4]int{500, 501, 502, 503}
	DefaultConfig := &config.HealthCheckConfig{
		EventThresholdIntervalMs:   1000,
		ProbeInferRequestTimeoutMs: 10000,
		RestHealthCommonConfig: config.RestHealthCommonConfig{
			Interval:             10000,
			HealthCheckTimeoutMs: 10000,
			MaxConcurrency:       1000,
			BatchSize:            1,
		},
		FallbackHealthCheckConfig: config.FallbackHealthCheckConfig{
			Enable:                false,
			ResumeCheckIntervalMs: 1000,
			ResumeTimeMs:          90000,
		},
		AvailableCheckersCfg: config.AvailableCheckersConfig{
			RestHealthCfg: config.RestHealthConfig{
				Enable:          false,
				ConsecutiveTime: 3,
				FailOn500:       false,
			},
			RequestFailuresCfg: config.RequestFailuresConfig{
				Enable:             false,
				ConsecutiveFailure: false,
				TimeWindow:         90000,
				MaxFailuresTime:    3,
				FakeRequest: config.FakeRequestConfig{
					Enable:           false,
					SendInterval:     12000,
					MaxSendTime:      5,
					FailureThreshold: 3,
				},
				FailureConditionsConfig: config.FailureConditionsConfig{
					RequestTimeout: false,
					StatusCodes:    statusCodes[:],
				},
			},
			LinkDownCfg: config.RequestFailuresConfig{
				Enable:          false,
				TimeWindow:      90000,
				MaxFailuresTime: 3,
			},
			HangCheckerCfg: config.HangCheckerConfig{
				Enable:           false,
				Interval:         1000,
				HangTagValidTime: 10000,
			},
			LinkBlacklistConfig: config.LinkBlacklistConfig{
				Enable:                  false,
				LinkBlockThreshold:      3,
				LinkBlockWindowsMs:      90000,
				LinkBlockTagValidTimeMs: 90000,
				EntryGCIntervalMs:       1000,
				InstanceBlockRatio:      70,
			},
		},
		UnavailableCheckersCfg: config.UnavailableCheckersConfig{
			RestHealthCfg: config.RestHealthConfig{
				Enable:                  false,
				ConsecutiveTime:         9,
				EnableProbeInferRequest: false,
			},
		},
	}
	// autofill use reflect
	applyHealthConfig(reflect.ValueOf(cfg).Elem(), reflect.ValueOf(DefaultConfig).Elem())
	// just one slice in healthcheck config, no need to handle it by reflect
	fillHangThresholds(cfg)
	fillHangCheckerConfig(cfg)
}

func checkConfig(cfg *config.HealthCheckConfig) error {
	if cfg.AvailableCheckersCfg.HangCheckerCfg.Interval < 10 {
		err := fmt.Sprintf("HangChecker leader-check-interval-ms should be greater than 10, now is: %v", cfg.AvailableCheckersCfg.HangCheckerCfg.Interval)
		return errors.New(err)
	}
	if cfg.AvailableCheckersCfg.HangCheckerCfg.HangTagValidTime < 100 {
		err := fmt.Sprintf("HangChecker worker-hang-tag-valid-time-ms should be greater than 100, now is: %v", cfg.AvailableCheckersCfg.HangCheckerCfg.HangTagValidTime)
		return errors.New(err)
	}
	for _, threshold := range cfg.AvailableCheckersCfg.HangCheckerCfg.HangThresholds {
		if !utils.InMap(threshold.Role, constant.NeedHandleReqRoleMap) {
			err := fmt.Sprintf("HangThresholdCfg Role is invalid, now exist role: %v", threshold.Role)
			return errors.New(err)
		}
	}
	requestConfig := cfg.AvailableCheckersCfg.RequestFailuresCfg.FakeRequest
	if requestConfig.MaxSendTime < requestConfig.FailureThreshold {
		err := fmt.Sprintf("FakeRequest max faker request times should not be less than failure threshold. max times: %d, failure threshold:%d", requestConfig.MaxSendTime, requestConfig.FailureThreshold)
		return errors.New(err)
	}
	if cfg.RestHealthCommonConfig.BatchSize <= 0 {
		err := fmt.Sprintf("RestHealthCommonConfig BatchSize should not be less than 0")
		return errors.New(err)
	}
	linkBlackEnable := cfg.AvailableCheckersCfg.LinkBlacklistConfig.Enable
	linkDownEnable := cfg.AvailableCheckersCfg.LinkDownCfg.Enable
	if linkDownEnable && linkBlackEnable {
		err := fmt.Sprintf("link black list module and link down module can not enable together")
		return errors.New(err)
	}
	if cfg.AvailableCheckersCfg.LinkBlacklistConfig.InstanceBlockRatio > 100 || cfg.AvailableCheckersCfg.LinkBlacklistConfig.InstanceBlockRatio < 0 {
		err := fmt.Sprintf("link black list failure percent should be between 0 and 100")
		return errors.New(err)
	}
	if cfg.AvailableCheckersCfg.LinkBlacklistConfig.LinkBlockWindowsMs < 100 {
		err := fmt.Sprintf("blacklist manager link-block-windows-ms should be greater than 100, now is: %v", cfg.AvailableCheckersCfg.LinkBlacklistConfig.LinkBlockWindowsMs)
		return errors.New(err)
	}
	if cfg.AvailableCheckersCfg.LinkBlacklistConfig.LinkBlockTagValidTimeMs < 100 {
		err := fmt.Sprintf("blacklist manager link-block-tag-valid-time-ms should be greater than 100, now is: %v", cfg.AvailableCheckersCfg.LinkBlacklistConfig.LinkBlockTagValidTimeMs)
		return errors.New(err)
	}
	if cfg.AvailableCheckersCfg.LinkBlacklistConfig.EntryGCIntervalMs < 10 {
		err := fmt.Sprintf("blacklist manager entry-gc-interval-ms should be greater than 10, now is: %v", cfg.AvailableCheckersCfg.LinkBlacklistConfig.EntryGCIntervalMs)
		return errors.New(err)
	}
	if cfg.AvailableCheckersCfg.LinkBlacklistConfig.LinkBlockThreshold < 1 {
		err := fmt.Sprintf("blacklist manager link-block-threshold should not be less than 1")
		return errors.New(err)
	}
	return nil
}

func applyHealthConfig(targetValue, defaultsValue reflect.Value) {
	targetType := targetValue.Type()
	numField := targetValue.NumField()
	for i := 0; i < numField; i++ {
		field := targetValue.Field(i)
		fieldType := targetType.Field(i)
		defaultValue := defaultsValue.FieldByName(fieldType.Name)
		// go deeper to the struct
		if field.Kind() == reflect.Struct && defaultValue.Kind() == reflect.Struct {
			applyHealthConfig(field, defaultValue)
			continue
		}
		// fill value
		if field.IsZero() && field.Type() == defaultValue.Type() && field.Type() != reflect.TypeOf(true) {
			field.Set(defaultValue)
		}
	}
}

func fillHangThresholds(cfg *config.HealthCheckConfig) {
	isPrefill := false
	isDecode := false
	for i, threshold := range cfg.AvailableCheckersCfg.HangCheckerCfg.HangThresholds {
		if threshold.Role == constant.Prefill {
			isPrefill = true
		}
		if threshold.Role == constant.Decode {
			isDecode = true
		}
		if threshold.ThresholdTime == 0 {
			cfg.AvailableCheckersCfg.HangCheckerCfg.HangThresholds[i].ThresholdTime = -1
		}
	}
	if !isPrefill {
		cfg.AvailableCheckersCfg.HangCheckerCfg.HangThresholds = append(
			cfg.AvailableCheckersCfg.HangCheckerCfg.HangThresholds,
			config.HangThreshold{
				Role:          constant.Prefill,
				ThresholdTime: -1,
				SkipNonStream: false,
			})
	}
	if !isDecode {
		cfg.AvailableCheckersCfg.HangCheckerCfg.HangThresholds = append(
			cfg.AvailableCheckersCfg.HangCheckerCfg.HangThresholds,
			config.HangThreshold{
				Role:          constant.Decode,
				ThresholdTime: -1,
				SkipNonStream: false,
			})
	}
	if cfg.AvailableCheckersCfg.HangCheckerCfg.HangMessageKeywords != nil &&
		len(cfg.AvailableCheckersCfg.HangCheckerCfg.HangMessageKeywords) > 0 {
		//override the hang message keywords
		logger.Log().Infof("override hang message keywords to [%v]",
			cfg.AvailableCheckersCfg.HangCheckerCfg.HangMessageKeywords)
		utils.HangMessageKeywords = cfg.AvailableCheckersCfg.HangCheckerCfg.HangMessageKeywords
	} else {
		// set config to default vals
		cfg.AvailableCheckersCfg.HangCheckerCfg.HangMessageKeywords = utils.HangMessageKeywords
	}
}

func fillHangCheckerConfig(cfg *config.HealthCheckConfig) {
	if cfg.AvailableCheckersCfg.HangCheckerCfg.MaxConsecutiveInvalidProbes <= 0 {
		cfg.AvailableCheckersCfg.HangCheckerCfg.MaxConsecutiveInvalidProbes = 3
	}
}
