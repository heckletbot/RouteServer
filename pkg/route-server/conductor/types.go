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
	"context"
	"errors"
	"fmt"
	"reflect"
	"strconv"

	"github.com/ugorji/go/codec"
)

// Conductor 协议常量。这些字面值是与外部服务的约定，改动即为 breaking change。
const (
	// InstanceTypeVLLM 推理实例类型标识，大小写由 Conductor 侧校验，必须精确。
	InstanceTypeVLLM = "vLLM"
	// DefaultBlockSize vLLM 的 KV cache block size 默认值，对应 vLLM 命令行
	// --block-size 的常见取值。**必须与推理实例的实际值一致，否则前缀切分错位，
	// 表现是注册和查询都成功但命中恒为 0，两侧都不报错。**
	//
	// 实际值不一定等于命令行参数：混合注意力（Mamba/GDN + Attention）模型下
	// vllm-ascend 会按 mamba state shape 重算并覆盖它（`patch_mamba_config.py`：
	// attn_block_size = 128*cdiv(ssm_page, 128*attn_k_page)，且硬 assert 要求
	// attention 与 ssm 的 page size 精确相等，所以压不回 128），Qwen3.6-35B-A3B
	// 上实测是 1024。该值随模型结构变化，因此前缀查询用的切块大小由 YAML 的
	// conductor.block-size 配置，本常量只是未配置时的回退。注册时的 block size
	// 不读这里，而以 GET /services 公布的值为准。
	DefaultBlockSize = 128
	// DefaultDpRank 单实例部署下的 data parallel rank。
	DefaultDpRank = 0
	// DefaultTenantID 未启用多租户时的租户标识。
	DefaultTenantID = "default"
	// ContentTypeMsgpack Conductor 只接受 MessagePack，不提供 JSON 兼容路径。
	ContentTypeMsgpack = "application/msgpack"
)

// hash profile 各字段的取值，描述 RouteServer 与 vLLM 之间 prefix hash 的计算口径。
const (
	hashStrategyVLLMV1     = "vllm_v1"
	hashAlgorithmSHA256    = "sha256"
	hashPythonSeedDisabled = "0"
	hashIndexProjLow64BE   = "low64_be"
)

// HTTP 层参数，供 client 使用。
const (
	pathRegister         = "/register"
	pathUnregister       = "/unregister"
	pathServices         = "/services"
	maxResponseBodyBytes = 64 * 1024
	// maxServicesBodyBytes bounds GET /services. A cluster publishes one record
	// per instance, which does not fit in the register response cap.
	maxServicesBodyBytes = 4 << 20
)

// HashProfile 声明 prefix hash 的计算口径，Conductor 用它判断能否复用本实例的 cache。
type HashProfile struct {
	Strategy        string `codec:"strategy"`
	Algorithm       string `codec:"algorithm"`
	PythonHashSeed  string `codec:"python_hash_seed"`
	IndexProjection string `codec:"index_projection"`
}

// NewDefaultHashProfile 返回当前 vLLM 版本对应的 hash 口径。
func NewDefaultHashProfile() HashProfile {
	return HashProfile{
		Strategy:        hashStrategyVLLMV1,
		Algorithm:       hashAlgorithmSHA256,
		PythonHashSeed:  hashPythonSeedDisabled,
		IndexProjection: hashIndexProjLow64BE,
	}
}

// RegisterRequest POST /register 的请求体。
// 字段与 Conductor 协议一一对应，不含 root_digest（由 Conductor 派生）、
// replay_endpoint 和 cache_group（功能未启用）。
type RegisterRequest struct {
	Endpoint    string      `codec:"endpoint"`
	Type        string      `codec:"type"`
	ModelName   string      `codec:"modelname"`
	InstanceID  string      `codec:"instance_id"`
	BlockSize   int         `codec:"block_size"`
	DpRank      int         `codec:"dp_rank"`
	TenantID    string      `codec:"tenant_id"`
	LoraName    string      `codec:"lora_name"`
	HashProfile HashProfile `codec:"hash_profile"`
}

// UnregisterRequest POST /unregister 的请求体，只需定位实例。
type UnregisterRequest struct {
	InstanceID string `codec:"instance_id"`
	DpRank     int    `codec:"dp_rank"`
	TenantID   string `codec:"tenant_id"`
}

// ServiceKey 唯一标识一个已注册实例。可比较，用作注册状态表的 map key。
type ServiceKey struct {
	InstanceID string
	TenantID   string
	DpRank     int
}

// NewServiceKey 按当前单租户、单 rank 的部署形态构造 key。
func NewServiceKey(instanceID string) ServiceKey {
	return ServiceKey{
		InstanceID: instanceID,
		TenantID:   DefaultTenantID,
		DpRank:     DefaultDpRank,
	}
}

func (k ServiceKey) String() string {
	return k.InstanceID + "/" + k.TenantID + "/" + strconv.Itoa(k.DpRank)
}

// ErrorClass 注册失败的可重试性分类，决定调用方是快速重试、低频重试还是放弃。
type ErrorClass int

const (
	// ClassTransient 传输层或服务端临时故障，应快速重试。
	ClassTransient ErrorClass = iota
	// ClassProtocol 响应无法解析，重试同样的请求不会好转，转低频重试。
	ClassProtocol
	// ClassSemantic 请求内容被拒绝，需人工修正配置。
	ClassSemantic
	// ClassConflict 服务端已有冲突状态，需先反注册再重试。
	ClassConflict
	// ClassCanceled 调用方主动取消，不计为失败。
	ClassCanceled
)

func (c ErrorClass) String() string {
	switch c {
	case ClassTransient:
		return "transient"
	case ClassProtocol:
		return "protocol"
	case ClassSemantic:
		return "semantic"
	case ClassConflict:
		return "conflict"
	case ClassCanceled:
		return "canceled"
	default:
		return "unknown"
	}
}

// APIError Conductor 返回的非 2xx 响应。BodySummary 是截断后的原始响应体，便于排查协议不一致。
type APIError struct {
	StatusCode  int
	Reason      string
	Field       string
	Message     string
	BodySummary string
	Class       ErrorClass
}

func (e *APIError) Error() string {
	return fmt.Sprintf("conductor API error: status=%d class=%s reason=%q field=%q message=%q body=%s",
		e.StatusCode, e.Class, e.Reason, e.Field, e.Message, e.BodySummary)
}

// ClassOf 提取错误的重试分类。context 取消单独识别：它是传输层错误而非
// APIError，但语义上不是失败，不应重试。
// 其余非 APIError（含 nil）一律按 transient 处理：
// 传输层错误重试有意义，而未知错误保守重试优于直接放弃注册。
func ClassOf(err error) ErrorClass {
	var apiErr *APIError
	// 用 errors.As 而非类型断言：client 会用 %w 包装后再往上传。
	if errors.As(err, &apiErr) {
		return apiErr.Class
	}
	// DeadlineExceeded 不归入此列：超时是真的临时故障，应当重试。
	if errors.Is(err, context.Canceled) {
		return ClassCanceled
	}
	return ClassTransient
}

// PublishedService is one record from GET /services.
// Conductor publishes the KV event endpoint, block size, and dp rank that the
// engine actually registered. RouteServer does not invent those values.
type PublishedService struct {
	InstanceID string
	Endpoint   string
	BlockSize  int
	DPRank     int
	TenantID   string
}

// newMsgpackHandle 返回收发 Conductor 报文用的 codec handle。
//
// 调用方应在构造时取一次并长期持有：handle 配置完成后并发只读安全
// （见 codec doc.go「Multiple Encoders or Decoders can now use the Handle
// concurrently」），且内部按类型缓存编解码函数，逐请求新建会重复类型解析
// （实测 1.84x 耗时、5x 分配）。取到后不得再改字段。
//
// MapType 和 RawToString 两项都只影响解码，编码侧无论是否设置都恒把 Go string 写成
// msgpack str（已对四种 handle 配置实测比对 wire bytes 确认），因此不需要 WriteExt
// 或 StringToRaw。但解码侧缺一不可：不设 RawToString 时 str 解成 []uint8，
// 不设 MapType 时嵌套 map 解成 map[interface{}]interface{}。删任一项都会让
// 响应解析拿到错误的动态类型。
func newMsgpackHandle() *codec.MsgpackHandle {
	handle := &codec.MsgpackHandle{}
	handle.MapType = reflect.TypeOf(map[string]interface{}(nil))
	handle.RawToString = true
	return handle
}
