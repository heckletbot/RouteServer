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

package conductor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/ugorji/go/codec"
	"io"
	"mime"
	"net"
	"net/http"
	"route-server/pkg/common/logger"
	"strings"
	"time"
)

// 连接池参数。Conductor 是单个后端地址，只需少量长连接，取值与请求频率
// （注册/反注册为低频事件，重试由 Manager 控制）匹配即可。
const (
	maxIdleConns        = 4
	maxIdleConnsPerHost = 4
	idleConnTimeout     = 90 * time.Second
	// dialTimeout 建连上限。RequestTimeout 通常更短（默认 1s）并由 post 的 context
	// 约束建连，故此值只在 RequestTimeout 被配到 5s 以上时才成为实际上限。
	dialTimeout = 5 * time.Second
	// bodySummaryMaxBytes 进日志的响应体上限。读取上限是 maxResponseBodyBytes（64 KiB），
	// 但错误信息不能把它全塞进日志，故摘要另设更小的界。
	bodySummaryMaxBytes = 512
)

// 成功响应必须命中的字段名。Conductor 在 200 响应里回填这些字段，缺失说明
// 对端不是预期的服务（例如网关返回了自己的 200 页面）。
const (
	fieldInstanceID       = "instance_id"
	fieldRemovedInstances = "removed_instances"
)

// 错误响应体的字段名。Conductor 的参数校验错误带 error/reason/可选 field，
// 运行时错误至少带 error。
// 不含 index：它只出现在批量接口的错误体，本 client 不发批量请求。
const (
	fieldReason = "reason"
	fieldField  = "field"
	fieldError  = "error"
)

// APIError.Reason 的取值。reasonInvalidRegistration 由 Conductor 在错误体里给出，
// 其余两个由本 client 在响应无法解析时合成（Conductor 也可能给出同名 reason，
// 语义一致：请求或响应不符合协议）。
const (
	reasonInvalidRegistration    = "invalid_registration"
	reasonUnsupportedContentType = "unsupported_content_type"
	reasonInvalidMsgpack         = "invalid_msgpack"
	reasonMissingSuccessField    = "missing_success_field"
)

// Client 向 Conductor 注册/反注册推理实例。抽成接口便于上层 Manager 注入 fake。
type Client interface {
	Register(ctx context.Context, req RegisterRequest) error
	Unregister(ctx context.Context, req UnregisterRequest) error
}

// HTTPClient 基于 MessagePack over HTTP 的 Client 实现。
// 内部不做重试：退避策略由 Manager 统一控制，两层重试会叠加成指数级请求量。
// 构造后所有字段只读，可被多 goroutine 并发调用；新增字段必须维持这一点。
type HTTPClient struct {
	baseURL    string
	timeout    time.Duration
	httpClient *http.Client
	// handle 构造时取一次并长期只读复用，见 newMsgpackHandle 的契约说明。
	handle *codec.MsgpackHandle
}

// NewHTTPClient 按归一化后的配置构造 client。
func NewHTTPClient(cfg Config) (*HTTPClient, error) {
	// BaseURL 和 RequestTimeout 的合法性都由 NormalizeConfig 保证（绝对 http/https、
	// 有 host、无末尾斜杠、timeout 非零）。这里只兜底空 BaseURL，拦住绕过
	// NormalizeConfig 直接构造 Config 的调用方；不重复填充默认值——NormalizeConfig
	// 是 Conductor 配置默认值的唯一来源，本文件再填一遍会让不变量失效。
	if strings.TrimSpace(cfg.BaseURL) == "" {
		return nil, fmt.Errorf("conductor base-url must not be empty")
	}

	// 不复用 pkg/common/http.NewClient：它强制设置 http.Client.Timeout，与本文件
	// 「超时只由 post 的 context 控制」的决策冲突；且它要求 config.HttpConfig，
	// 而 ConductorConfig 不含传输层字段。
	transport := &http.Transport{
		DialContext: (&net.Dialer{Timeout: dialTimeout}).DialContext,
		// 连接池随 client 长期存活，避免每次注册重新握手。
		MaxIdleConns:        maxIdleConns,
		MaxIdleConnsPerHost: maxIdleConnsPerHost,
		IdleConnTimeout:     idleConnTimeout,
	}

	return &HTTPClient{
		baseURL: cfg.BaseURL,
		timeout: cfg.RequestTimeout,
		// 不设 http.Client.Timeout：超时统一由 post 里的 context 控制，
		// 两处都设会让超时归因变得不确定。
		httpClient: &http.Client{Transport: transport},
		handle:     newMsgpackHandle(),
	}, nil
}

// Register 注册一个 Prefill 推理实例。
func (c *HTTPClient) Register(ctx context.Context, req RegisterRequest) error {
	return c.post(ctx, pathRegister, req, fieldInstanceID)
}

// Unregister 反注册一个推理实例。Conductor 视实现返回 instance_id 或
// removed_instances，任一命中即视为成功。
func (c *HTTPClient) Unregister(ctx context.Context, req UnregisterRequest) error {
	err := c.post(ctx, pathUnregister, req, fieldInstanceID, fieldRemovedInstances)
	// 404 且响应体是合法 msgpack error map 时，表示实例已不在 Conductor 侧，
	// 反注册的目标状态已达成，按成功处理。
	// 排除两个合成 reason 即等价于「响应体确实是合法 msgpack map」：网关或代理返回的
	// 404（HTML 错误页、非法 msgpack）说明路径配错了，若当成功会删掉 tombstone，
	// 导致实例永远不被注销，而初版没有 /services 对账能发现这类残留。
	// 拦在这里而不给 post/parseResponse 加参数：这是 Unregister 的业务规则，
	// 不是传输层关注点。Register 的 404 不同——那意味着 endpoint 不存在，必须报错。
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound &&
		apiErr.Reason != reasonUnsupportedContentType && apiErr.Reason != reasonInvalidMsgpack {
		return nil
	}
	return err
}

// post 编码 payload 发往 path，并按 successKeys 判定响应是否成功。
func (c *HTTPClient) post(ctx context.Context, path string, payload interface{}, successKeys ...string) error {
	var body []byte
	if err := codec.NewEncoderBytes(&body, c.handle).Encode(payload); err != nil {
		return fmt.Errorf("encode %s request to msgpack failed: %w", path, err)
	}

	// BaseURL 已由 NormalizeConfig 去掉末尾斜杠并保留 path prefix，直接拼接。
	url := c.baseURL + path

	reqCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(reqCtx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build %s request failed: %w", path, err)
	}
	httpReq.Header.Set("Content-Type", ContentTypeMsgpack)
	httpReq.Header.Set("Accept", ContentTypeMsgpack)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		// 用 %w 包装：ClassOf 依赖 errors.Is 识别 context.Canceled。
		return fmt.Errorf("post %s failed: %w", url, err)
	}
	defer func() {
		// 排空剩余 body 后再关闭，否则连接无法回到池中复用。
		_, err = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseBodyBytes))
		if err != nil {
			logger.Log().Errorf("failed to copy response body, err = %v", err)
		}
		err = resp.Body.Close()
		if err != nil {
			logger.Log().Errorf("fail to close response body, err = %v", err)
		}
	}()

	return c.parseResponse(resp, path, successKeys)
}

// parseResponse 判定响应成败。成功要求 200 且解码出的 map 命中至少一个 successKey。
func (c *HTTPClient) parseResponse(resp *http.Response, path string, successKeys []string) error {
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBodyBytes))
	if err != nil {
		return fmt.Errorf("read %s response body failed: %w", path, err)
	}

	// 状态码判定必须排在 Content-Type 校验之前：网关的 500 + text/html 错误页是
	// 状态码问题（transient，快速重试），不是协议问题；响应体超 maxResponseBodyBytes
	// 被截断后解码必然失败、reason 会变成 invalid_msgpack，同样不能盖掉 5xx。
	// 顺序反了的症状是「Conductor 挂了但 RouteServer 半分钟才重试一次」。
	if resp.StatusCode != http.StatusOK {
		return c.newStatusError(resp, respBody)
	}

	if err := checkContentType(resp); err != nil {
		return &APIError{
			StatusCode:  resp.StatusCode,
			Reason:      reasonUnsupportedContentType,
			Message:     err.Error(),
			BodySummary: summarizeBody(respBody),
			Class:       ClassProtocol,
		}
	}

	var decoded map[string]interface{}
	if err := codec.NewDecoderBytes(respBody, c.handle).Decode(&decoded); err != nil {
		return &APIError{
			StatusCode:  resp.StatusCode,
			Reason:      reasonInvalidMsgpack,
			Message:     err.Error(),
			BodySummary: summarizeBody(respBody),
			Class:       ClassProtocol,
		}
	}

	for _, key := range successKeys {
		if _, ok := decoded[key]; ok {
			return nil
		}
	}
	// Field 字段在错误体里专用于「被拒绝的请求字段」（如 endpoint），语义与这里的
	// 「缺失的响应字段集合」不同，故写进 Message。
	return &APIError{
		StatusCode:  resp.StatusCode,
		Reason:      reasonMissingSuccessField,
		Message:     "none of " + strings.Join(successKeys, ", ") + " present in response",
		BodySummary: summarizeBody(respBody),
		Class:       ClassProtocol,
	}
}

// newStatusError 把非 200 响应转成 APIError。能解码时从错误体取 reason/field/error，
// 否则合成一个描述解析失败原因的 reason；最终分类由 ClassifyStatus 决定。
func (c *HTTPClient) newStatusError(resp *http.Response, body []byte) error {
	apiErr := &APIError{
		StatusCode:  resp.StatusCode,
		BodySummary: summarizeBody(body),
	}

	var decoded map[string]interface{}
	if err := checkContentType(resp); err != nil {
		// 非 msgpack 响应体不尝试解析：Conductor 无 JSON 兼容路径，
		// 这种响应来自网关或代理，内容不遵循 Conductor 的错误体约定。
		apiErr.Reason = reasonUnsupportedContentType
		apiErr.Message = err.Error()
	} else if err := codec.NewDecoderBytes(body, c.handle).Decode(&decoded); err != nil {
		apiErr.Reason = reasonInvalidMsgpack
		apiErr.Message = err.Error()
	} else {
		// 字段缺失或类型不符时断言取到零值，按缺失处理。
		// Field 只放「被 Conductor 拒绝的单个请求字段名」（如 endpoint），
		// 不得塞入其他含义的内容。
		if reason, exists := decoded[fieldReason]; exists {
			if reasonStr, ok := reason.(string); ok {
				apiErr.Reason = reasonStr
			}
		}
		if field, exists := decoded[fieldField]; exists {
			if fieldStr, ok := field.(string); ok {
				apiErr.Field = fieldStr
			}
		}
		if msg, exists := decoded[fieldError]; exists {
			if msgStr, ok := msg.(string); ok {
				apiErr.Message = msgStr
			}
		}
	}

	apiErr.Class = ClassifyStatus(resp.StatusCode, apiErr.Reason)
	return apiErr
}

// ClassifyStatus 判定非 200 响应的重试策略。
// 5xx 必须先判：服务端故障时响应体常常无法解码或不是 msgpack，那时 reason 是本
// client 合成的，先看 reason 会把服务端故障误判成 protocol 而转低频重试。
// 导出以供 conductor-cache-aware 的 query_client 复用，消除两包各维护一份分类口径的漂移风险。
func ClassifyStatus(status int, reason string) ErrorClass {
	if status >= http.StatusInternalServerError {
		return ClassTransient
	}
	switch reason {
	case reasonUnsupportedContentType, reasonInvalidMsgpack:
		return ClassProtocol
	case reasonInvalidRegistration:
		// 服务端已有冲突状态，需先反注册再重试。
		return ClassConflict
	}
	switch status {
	case http.StatusBadRequest:
		// 请求内容被 Conductor 拒绝，重发同样的请求不会好转，需人工修正配置。
		return ClassSemantic
	case http.StatusMethodNotAllowed, http.StatusUnsupportedMediaType, http.StatusNotFound:
		// 方法/媒体类型/路径不匹配：对端不是预期的 Conductor 接口。
		return ClassProtocol
	}
	if status < http.StatusBadRequest {
		// 非 200 的 2xx/3xx：Conductor 只用 200 表示成功，其余是对端未按自身协议
		// 应答（设计文档 §16.1「非预期 2xx」归 protocol）。与 semantic 的重试节奏
		// 相同，但标签决定排查方向——protocol 指向对端不守协议，semantic 指向本地配置错。
		return ClassProtocol
	}
	return ClassSemantic
}

// checkContentType 校验响应是 MessagePack。Conductor 只用 MessagePack，
// 任何其他类型（含 JSON）都不得进入解析路径。
// mime.ParseMediaType 会剥离参数并把媒体类型小写化，故直接比较即可。
func checkContentType(resp *http.Response) error {
	raw := resp.Header.Get("Content-Type")
	mediaType, _, err := mime.ParseMediaType(raw)
	if err != nil {
		return fmt.Errorf("parse response Content-Type %q failed: %w", raw, err)
	}
	if mediaType != ContentTypeMsgpack {
		return fmt.Errorf("response Content-Type is %q, want %s", mediaType, ContentTypeMsgpack)
	}
	return nil
}

// summarizeBody 截断响应体供错误信息使用。响应是 MessagePack 二进制，
// 用 %q 转义不可打印字节，避免把控制字符直接写进日志。
func summarizeBody(body []byte) string {
	if len(body) > bodySummaryMaxBytes {
		return fmt.Sprintf("%q...(truncated from %d bytes)", body[:bodySummaryMaxBytes], len(body))
	}
	return fmt.Sprintf("%q", body)
}
