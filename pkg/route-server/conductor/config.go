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

// Package conductor 实现 RouteServer 向 Mooncake Conductor 注册推理实例的能力。
package conductor

import (
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"route-server/pkg/common/config"
)

const (
	// DefaultBaseURL Conductor 默认地址。
	DefaultBaseURL = "http://127.0.0.1:13333"
	// DefaultRequestTimeout 单次 HTTP 请求默认超时。
	DefaultRequestTimeout = time.Second
	// DefaultRetryInterval 失败任务的默认低频重试周期。
	DefaultRetryInterval = 30 * time.Second
)

// Config 归一化后的 Conductor 配置，字段均已校验。
type Config struct {
	Enabled        bool
	BaseURL        string
	RequestTimeout time.Duration
	// QueryTimeout 单次 /query 的超时。该调用位于请求路径上，默认值刻意远小于
	// RequestTimeout，避免 Conductor 变慢时把延迟带到推理请求上。
	QueryTimeout  time.Duration
	RetryInterval time.Duration
	// BlockSize 前缀命中查询切块用的 KV cache block size。
	// RouteServer 不向 Conductor 注册实例，见 DefaultBlockSize 的说明。
	BlockSize int
	// RegisterRoles 需要注册到 Conductor 的实例角色集合。
	// 空配置解析为 {prefill}，与只支持 prefill 的历史行为一致。
	RegisterRoles map[string]struct{}
	// TokenSource 是聊天请求取 token id 的接口，render 或 tokenize。
	TokenSource string
}

// RolePrefill / RoleDecode 可注册的角色。目前只允许这两个：encode 实例不持有
// 可复用的 KV cache，注册上去只会让 Conductor 维护无用的索引。
const (
	RolePrefill = "prefill"
	RoleDecode  = "decode"

	// TokenSourceRender calls /v1/chat/completions/render with the original chat body.
	TokenSourceRender = "render"
	// TokenSourceTokenize calls POST /tokenize with the tokenization fields.
	TokenSourceTokenize = "tokenize"
)

// Manages 判断该角色的实例是否需要注册到 Conductor。
func (c Config) Manages(role string) bool {
	if len(c.RegisterRoles) == 0 {
		// 归一化后不会为空，这里兜底防止零值 Config 让所有实例都注册。
		return role == RolePrefill
	}
	_, ok := c.RegisterRoles[role]
	return ok
}

// Roles 返回已注册角色的稳定排序列表，用于日志输出。
func (c Config) Roles() []string {
	if len(c.RegisterRoles) == 0 {
		return []string{RolePrefill}
	}
	roles := make([]string, 0, len(c.RegisterRoles))
	for role := range c.RegisterRoles {
		roles = append(roles, role)
	}
	sort.Strings(roles)
	return roles
}

// parseRegisterRoles 解析逗号分隔的角色列表。
// 空串按历史默认返回 {prefill}；出现未知角色直接报错，不静默丢弃 ——
// 配错时 Conductor 查不到实例而 RS 日志一切正常，是最难排查的失效模式。
func parseRegisterRoles(raw string) (map[string]struct{}, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return map[string]struct{}{RolePrefill: {}}, nil
	}

	roles := make(map[string]struct{}, 2)
	for _, part := range strings.Split(trimmed, ",") {
		role := strings.TrimSpace(part)
		if role == "" {
			continue
		}
		if role != RolePrefill && role != RoleDecode {
			return nil, fmt.Errorf("conductor register-roles contains unsupported role %q, "+
				"allowed values are %q and %q", role, RolePrefill, RoleDecode)
		}
		roles[role] = struct{}{}
	}

	if len(roles) == 0 {
		// 输入全是逗号和空格，例如 ", ,"。当成配置错误而不是回退默认值：
		// 用户显然想指定角色但写错了，静默用 prefill 会掩盖问题。
		return nil, fmt.Errorf("conductor register-roles %q lists no valid role", raw)
	}
	return roles, nil
}

// NormalizeConfig 填充默认值并校验语义。disabled 时直接返回，不做任何校验。
// 本函数是 Conductor 配置默认值的唯一来源：仓库不读取 default struct tag。
func NormalizeConfig(raw config.ConductorConfig) (Config, error) {
	if !raw.Enabled {
		return Config{Enabled: false}, nil
	}

	out := Config{Enabled: true}

	baseURL := strings.TrimSpace(raw.BaseURL)
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	normalized, err := validateBaseURL(baseURL)
	if err != nil {
		return Config{}, err
	}
	out.BaseURL = normalized

	// 0 表示未设置，走默认值；只有负数非法，故文案是"不能为负"而非"必须大于 0"。
	if raw.RequestTimeoutMs < 0 {
		return Config{}, fmt.Errorf("conductor request-timeout-ms must not be negative, got %d", raw.RequestTimeoutMs)
	}
	out.RequestTimeout = DefaultRequestTimeout
	if raw.RequestTimeoutMs > 0 {
		out.RequestTimeout = time.Duration(raw.RequestTimeoutMs) * time.Millisecond
	}

	if raw.QueryTimeoutMs < 0 {
		return Config{}, fmt.Errorf("conductor query-timeout-ms must not be negative, got %d", raw.QueryTimeoutMs)
	}
	out.QueryTimeout = DefaultQueryTimeout
	if raw.QueryTimeoutMs > 0 {
		out.QueryTimeout = time.Duration(raw.QueryTimeoutMs) * time.Millisecond
	}

	if raw.RetryIntervalMs < 0 {
		return Config{}, fmt.Errorf("conductor retry-interval-ms must not be negative, got %d", raw.RetryIntervalMs)
	}
	out.RetryInterval = DefaultRetryInterval
	if raw.RetryIntervalMs > 0 {
		out.RetryInterval = time.Duration(raw.RetryIntervalMs) * time.Millisecond
	}

	// 与超时口径一致：0 表示未设置走默认值，负数非法。
	// 不额外要求 2 的幂：vLLM 的合法取值集合由推理侧决定（混合注意力模型的实际值
	// 是 vllm-ascend 按 mamba state shape 推导后覆盖的），这里多加一层约束只会在
	// 换模型时误挡合法配置。填错的表现由启动日志里打印的生效值兜底。
	if raw.BlockSize < 0 {
		return Config{}, fmt.Errorf("conductor block-size must not be negative, got %d", raw.BlockSize)
	}
	out.BlockSize = DefaultBlockSize
	if raw.BlockSize > 0 {
		out.BlockSize = raw.BlockSize
	}

	roles, err := parseRegisterRoles(raw.RegisterRoles)
	if err != nil {
		return Config{}, err
	}
	out.RegisterRoles = roles

	tokenSource := strings.TrimSpace(raw.TokenSource)
	if tokenSource == "" {
		tokenSource = TokenSourceRender
	}
	if tokenSource != TokenSourceRender && tokenSource != TokenSourceTokenize {
		return Config{}, fmt.Errorf("conductor token-source must be %q or %q, got %q",
			TokenSourceRender, TokenSourceTokenize, raw.TokenSource)
	}
	out.TokenSource = tokenSource

	return out, nil
}

// validateBaseURL 要求绝对 http/https URL，允许网关 path prefix，拒绝 userinfo/query/fragment。
func validateBaseURL(raw string) (string, error) {
	// 这里不做归因：能走到这个分支的输入五花八门（缺 scheme 的 host:port、非法转义、
	// 畸形 IPv6、非法端口），统一说「不是合法 URL」并用 %w 透出真实原因，
	// 「必须是绝对 URL」的判定交给下面的 IsAbs。
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("conductor base-url is not a valid URL: %w", err)
	}
	// IsAbs 必须先判：http:// 这类输入 IsAbs()==true 但 Host==""，
	// 顺序颠倒会把「缺 scheme」误报成「缺 host」。
	if !u.IsAbs() {
		return "", fmt.Errorf("conductor base-url must be an absolute http or https URL, got %q", raw)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("conductor base-url scheme must be http or https, got %q", u.Scheme)
	}
	if u.Host == "" {
		return "", fmt.Errorf("conductor base-url must contain a host, got %q", raw)
	}
	if u.User != nil {
		return "", fmt.Errorf("conductor base-url must not contain userinfo, got %q", raw)
	}
	if u.RawQuery != "" || u.ForceQuery {
		return "", fmt.Errorf("conductor base-url must not contain query, got %q", raw)
	}
	if u.Fragment != "" {
		return "", fmt.Errorf("conductor base-url must not contain fragment, got %q", raw)
	}
	u.Path = strings.TrimRight(u.Path, "/")
	return u.String(), nil
}
