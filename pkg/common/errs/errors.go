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

package errs

import (
	"net/http"

	"infer-dp-scheduler/pkg/common/constant"
)

var (
	UnknownErr = MaError{
		Status: http.StatusInternalServerError,
		Code:   "2950",
		MsgEn:  "Unknown",
		MsgCn:  "未知错误",
	}

	EmptyTokenHeader = MaError{
		Status: http.StatusUnauthorized,
		Code:   "0201",
		MsgEn:  "Token doesn't exist.",
		MsgCn:  "Token为空",
	}

	InvalidTokenForExpired = MaError{
		Status: http.StatusUnauthorized,
		Code:   "0202",
		MsgEn:  "Invalid token header because the token has expired.",
		MsgCn:  "无效Token，Token已过期",
	}

	IamAuthFailed = MaError{
		Status: http.StatusUnauthorized,
		Code:   "0203",
		MsgEn:  "Invalid token. %v",
		MsgCn:  "无效的token. %v",
	}

	TokenParseFailed = MaError{
		Status: http.StatusUnauthorized,
		Code:   "0204",
		MsgEn:  "Failed to parse the token.",
		MsgCn:  "解析Token失败",
	}

	ServiceStatusUnavailable = MaError{
		Status: http.StatusBadRequest,
		Code:   "4203",
		MsgEn:  "Service %v's is unavailable status: %v",
		MsgCn:  "服务%v不可用，当前状态是:%v",
	}

	RequestBodyOverSizeERROR = MaError{
		Status: http.StatusRequestEntityTooLarge,
		Code:   "4104",
		MsgEn:  "Invalid request body because the size %d MB exceeds the maximum size %d MB allowed.",
		MsgCn:  "请求body体长度%d超过了允许的最大长度%d",
	}

	TooManyRequestError = MaError{
		Status: http.StatusTooManyRequests,
		Code:   "4206",
		MsgEn:  "Excessive number of requests, %s.",
		MsgCn:  "请求数量达到限制，%s",
	}

	MethodNotAllowed = MaError{
		Status: http.StatusMethodNotAllowed,
		Code:   "2803",
		MsgEn:  "Method not allowed",
		MsgCn:  "不允许的方法",
	}

	PathNotFound = MaError{
		Status: http.StatusNotFound,
		Code:   "2836",
		MsgEn:  "Path not found",
		MsgCn:  "未找到路径",
	}
)

// Custom error codes, range 8900-8999
var (
	UnsupportedAuthType = MaError{
		Status: http.StatusUnauthorized,
		Code:   "8900",
		MsgEn:  "Service %v's auth type: %v is unsupported now",
		MsgCn:  "服务%v暂不支持%v的鉴权类型",
	}

	InvalidApiKey = MaError{
		Status: http.StatusUnauthorized,
		Code:   "8901",
		MsgEn:  "Invalid api key, %v",
		MsgCn:  "无效的api key, %v",
	}

	ApiKeyAuthFailed = MaError{
		Status: http.StatusUnauthorized,
		Code:   "8902",
		MsgEn:  "Api key auth failed, %v",
		MsgCn:  "Api key校验失败, %v",
	}

	GatewayInternalError = MaError{
		Status: http.StatusBadRequest,
		Code:   "8903",
		MsgEn:  "Service %s backend is not available, please retry later.",
		MsgCn:  "服务%s没有可用的后端节点，请稍后重试",
	}

	ServiceNotFound = MaError{
		Status: http.StatusNotFound,
		Code:   "8904",
		MsgEn:  "Service %v not found",
		MsgCn:  "服务%v不存在",
	}

	ServiceConfigNotFound = MaError{
		Status: http.StatusNotFound,
		Code:   "8905",
		MsgEn:  "Service %v's config not found, please retry later.",
		MsgCn:  "服务%v的配置信息不存在，请稍后重试",
	}

	ServiceDiscoveryNotFound = MaError{
		Status: http.StatusNotFound,
		Code:   "8906",
		MsgEn:  "Service %v's instance groups not found, please retry later.",
		MsgCn:  "服务%v的实例分组不存在，请稍后重试",
	}

	ServiceConfigError = MaError{
		Status: http.StatusInternalServerError,
		Code:   "8907",
		MsgEn:  "Get service %v's config failed, please retry later.",
		MsgCn:  "服务%v的配置信息查询失败，请稍后重试",
	}

	ServiceDiscoveryError = MaError{
		Status: http.StatusInternalServerError,
		Code:   "8908",
		MsgEn:  "Get service %v's instance groups failed, please retry later.",
		MsgCn:  "服务%v的实例分组查询失败，请稍后重试",
	}

	RequestFailedERROR = MaError{
		Status: http.StatusInternalServerError,
		Code:   "8909",
		MsgEn:  "Request failed: %s",
		MsgCn:  "请求失败: %s",
	}

	RequestProtocolERROR = MaError{
		Status: http.StatusBadRequest,
		Code:   "8910",
		MsgEn:  "Request protocol: %s does not match the service protocol:%s.",
		MsgCn:  "请求的协议是%s与服务的协议%s不匹配",
	}

	RequestDegradeERROR = MaError{
		Status: http.StatusForbidden,
		Code:   "8913",
		MsgEn:  "Service is fused, %s.",
		MsgCn:  "服务请求被熔断，%s",
	}

	RequestAdmissionERROR = MaError{
		Status: http.StatusForbidden,
		Code:   "8914",
		MsgEn:  "Permission deny, %s.",
		MsgCn:  "拒绝访问，%s",
	}

	RequestServiceIdInvalid = MaError{
		Status: http.StatusBadRequest,
		Code:   "8915",
		MsgEn:  "Service id is invalid.",
		MsgCn:  "服务id不合法。",
	}

	RequestConnectLimitERROR = MaError{
		Status: http.StatusInternalServerError,
		Code:   "8916",
		MsgEn:  "Request connect numbers is over system limit.",
		MsgCn:  "请求连接数超过系统阈值",
	}

	WebsocketUpgradeError = MaError{
		Status: http.StatusBadRequest,
		Code:   "8917",
		MsgEn:  "websocket upgrade error: %s",
		MsgCn:  "长连接升级错误：%s",
	}

	DispatcherReconnectError = MaError{
		Status: http.StatusBadRequest,
		Code:   "8918",
		MsgEn:  "Unavailable now, please reconnect later",
		MsgCn:  "当前不可用，请稍后重连",
	}

	IntranetAccessForbiddenError = MaError{
		Status: http.StatusForbidden,
		Code:   "8919",
		MsgEn:  "The source IP address is not allowed to access the service(%s) through the intranet.",
		MsgCn:  "源地址禁止通过内网访问服务: %s",
	}

	InternetAccessForbiddenError = MaError{
		Status: http.StatusForbidden,
		Code:   "8920",
		MsgEn:  "The service(%s) cannot be accessed through the public network.",
		MsgCn:  "服务（%s）禁止通过公网访问服务",
	}

	UnsupportedAccessType = MaError{
		Status: http.StatusForbidden,
		Code:   "8921",
		MsgEn:  "unsupported access type: %s",
		MsgCn:  "未知的访问类型: %s",
	}

	ConversationIdOverSizeERROR = MaError{
		Status: http.StatusBadRequest,
		Code:   "8922",
		MsgEn:  "Invalid x-conversation-id because the length %d exceeds the maximum length %d allowed.",
		MsgCn:  "请求会话id长度%d超过了允许的最大长度%d",
	}

	PluginERROR = MaError{
		Status: http.StatusInternalServerError,
		Code:   "8923",
		MsgEn:  "plugin error",
		MsgCn:  "插件处理失败",
	}

	RequestForcedCancelledErr = MaError{
		Status: http.StatusServiceUnavailable,
		Code:   "8924",
		MsgEn:  "Request is cancelled by manager abort requests.",
		MsgCn:  "请求被管理员中止",
	}

	RequestCancelledByUserErr = MaError{
		Status: constant.StatusClientError,
		Code:   "8499",
		MsgEn:  "Request is cancelled by user abort requests.",
		MsgCn:  "请求被用户中止",
	}
)

// Proxy custom error codes, range 8800-8899
var (
	ProxyServiceNotFound = MaError{
		Status: http.StatusNotFound,
		Code:   "8800",
		MsgEn:  "Service %v not found",
		MsgCn:  "服务%v不存在",
	}

	ProxyServiceConfigNotFound = MaError{
		Status: http.StatusNotFound,
		Code:   "8801",
		MsgEn:  "Service %v's config not found, please retry later.",
		MsgCn:  "服务%v的配置信息不存在，请稍后重试",
	}

	ProxyBackendNotFound = MaError{
		Status: http.StatusNotFound,
		Code:   "8802",
		MsgEn:  "%v",
		MsgCn:  "%v",
	}

	ProxyRequestFailedERROR = MaError{
		Status: http.StatusInternalServerError,
		Code:   "8803",
		MsgEn:  "Request failed: %s",
		MsgCn:  "请求失败: %s",
	}

	InternalErr = MaError{
		Status: http.StatusInternalServerError,
		Code:   "5000",
		MsgEn:  "Internal server error: %s",
		MsgCn:  "内部服务器错误: %s",
	}

	GatewayTimeoutErr = MaError{
		Status: http.StatusGatewayTimeout,
		Code:   "5001",
		MsgEn:  "Gateway timeout: %s",
		MsgCn:  "Gateway 超时: %s",
	}

	BadGatewayErr = MaError{
		Status: http.StatusBadGateway,
		Code:   "5002",
		MsgEn:  "Bad gateway: %s",
		MsgCn:  "后端错误: %s",
	}

	ClientConnectionErr = MaError{
		Status: constant.StatusClientError,
		Code:   "4099",
		MsgEn:  "client connection error: %s",
		MsgCn:  "客户端连接错误: %s",
	}

	ServiceUnavailableErr = MaError{
		Status: http.StatusServiceUnavailable,
		Code:   "5003",
		MsgEn:  "no available %s instance found in router : %s",
		MsgCn:  "RouteServer中当前无可调用的%s实例: %s",
	}

	EmptyReqErr = MaError{
		Status: http.StatusBadRequest,
		Code:   "4202",
		MsgEn:  "request is null",
		MsgCn:  "请求为空",
	}

	BadReqErr = MaError{
		MsgEn:  "Invalid request: %s",
		MsgCn:  "非法请求: %s",
		Status: http.StatusBadRequest,
		Code:   "4200",
	}
)
