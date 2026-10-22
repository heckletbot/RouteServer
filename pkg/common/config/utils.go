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

import (
	"fmt"

	"infer-dp-scheduler/pkg/common/constant"
	"infer-dp-scheduler/pkg/common/logger"
	"infer-dp-scheduler/pkg/common/utils"
)

func ApplyDefaultHttpCfg(cfg *HttpConfig) {
	if cfg.DialerTimeout == 0 {
		cfg.DialerTimeout = DefaultDialerTimeout
	}
	if cfg.DialerKeepAlive == 0 {
		cfg.DialerKeepAlive = DefaultDialerKeepAlive
	}
	if cfg.RequestTimeout == 0 {
		cfg.RequestTimeout = DefaultRequestTimeout
	}
	if cfg.MaxIdleConns == 0 {
		cfg.MaxIdleConns = DefaultMaxIdleConns
	}
	if cfg.MaxConnsPerHost == 0 {
		cfg.MaxConnsPerHost = DefaultMaxConnsPerHost
	}
	if cfg.MaxIdleConnsPerHost == 0 {
		cfg.MaxIdleConnsPerHost = DefaultMaxIdleConnsPerHost
	}
	if cfg.IdleConnTimeout == 0 {
		cfg.IdleConnTimeout = DefaultIdleConnTimeout
	}
}

func CheckAndApplyDefaultOtelConfig(cfg *OtelConfig) {
	if cfg.SpanUpdateSeconds == 0 {
		cfg.SpanUpdateSeconds = DefaultSpanUpdateSeconds
	}
	if cfg.JaegerServiceName == "" {
		cfg.JaegerServiceName = DefaultJaegerServiceName
	}
	if cfg.TraceSampleRate == 0 {
		cfg.TraceSampleRate = DefaultTraceSampleRate
	}
	if cfg.JaegerEndpoint == "" {
		cfg.JaegerEndpoint = DefaultJaegerEndpoint
	}
}

func CheckInstanceValid(route *Route) error {
	if route == nil {
		return fmt.Errorf("instance route must not be nil")
	}
	if route.Ip == "" {
		return fmt.Errorf("instance ip is empty")
	}
	if route.Port < 0 || route.Port > 65535 {
		return fmt.Errorf("instance port must be in range [0, 65535], but got %v", route.Port)
	}
	if _, exist := constant.ValidRoleMap[route.Role]; !exist {
		return fmt.Errorf("instance role is invalid, must be one of %v", utils.MapKeysStr(constant.ValidRoleMap))
	}
	return nil
}

func ApplyDefaultInstanceManagerCfg(cfg *InstanceManagerCfg) {
	if cfg == nil {
		return
	}
	if cfg.InstanceClientType == "" {
		cfg.InstanceClientType = constant.ClientTypeVllm
	}
}

func NewResHeaderBlackSet(resHeaderBlackList []HeaderMatchMode) map[string]bool {
	resHeaderBlackSet := make(map[string]bool)
	for _, header := range resHeaderBlackList {
		resHeaderBlackSet[header.Header] = header.PrefixMatching
	}
	return resHeaderBlackSet
}

func ApplyDefaultProbeCfg(cfg *ProbeCfg) {
	if cfg == nil {
		return
	}
	if cfg.CircuitBreaker == nil {
		return
	}
	if cfg.CircuitBreaker.FailureRateThreshold <= 0 || cfg.CircuitBreaker.FailureRateThreshold > 1 {
		logger.Log().Warnf("CircuitBreaker FailureRateThreshold %f is invalid, use default %f",
			cfg.CircuitBreaker.FailureRateThreshold, DefaultCircuitBreakerFailureRateThreshold)
		cfg.CircuitBreaker.FailureRateThreshold = DefaultCircuitBreakerFailureRateThreshold
	}
	if cfg.CircuitBreaker.MinRequestCount <= 0 {
		logger.Log().Warnf("CircuitBreaker MinRequestCount %d is invalid, use default %d",
			cfg.CircuitBreaker.MinRequestCount, DefaultCircuitBreakerMinRequestCount)
		cfg.CircuitBreaker.MinRequestCount = DefaultCircuitBreakerMinRequestCount
	}
	if cfg.CircuitBreaker.CheckWindowSec <= 0 {
		logger.Log().Warnf("CircuitBreaker CheckWindowSec %d is invalid, use default %d",
			cfg.CircuitBreaker.CheckWindowSec, DefaultCircuitBreakerCheckWindowSec)
		cfg.CircuitBreaker.CheckWindowSec = DefaultCircuitBreakerCheckWindowSec
	}
}
