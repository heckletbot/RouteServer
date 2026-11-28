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

package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"

	"route-server/pkg/common/constant"
	instance_metrics "route-server/pkg/common/instance-metrics"
	"route-server/pkg/common/logger"
	"route-server/pkg/common/openai"
	"route-server/pkg/common/utils"
	"route-server/pkg/route-server/conductor"
	"route-server/pkg/route-server/handler/request"
	"route-server/pkg/route-server/instances"
)

const (
	promptTokenIDsKey = "prompt_token_ids"
	renderPathSuffix  = "/render"
	// renderTimeout bounds tokenization. The user request continues without token
	// ids when this expires, so a stuck renderer cannot stall scheduling.
	renderTimeout = 10 * time.Second
	maxRenderBody = 16 << 20
)

// RenderedPrompt is one successful call to /v1/chat/completions/render.
// The same token ids are what Conductor matches and what the backends receive.
type RenderedPrompt struct {
	TokenIDs []int32
	// Queried is true when Conductor /query returned a hit map. A skipped or
	// failed query leaves this false, and the prompt length stays len(TokenIDs).
	Queried bool
	// Remaining is instance id -> tokens that worker still has to compute.
	Remaining map[string]int32
}

// TokenCount is the rendered prompt length.
func (p *RenderedPrompt) TokenCount() int32 {
	if p == nil {
		return 0
	}
	n := len(p.TokenIDs)
	if n > math.MaxInt32 {
		return math.MaxInt32
	}
	return int32(n)
}

// WorkFor is how many tokens instanceID still has to compute.
// A worker missing from a successful query is treated as a full prompt.
func (p *RenderedPrompt) WorkFor(instanceID string) int32 {
	if p == nil {
		return 0
	}
	if p.Queried {
		if n, ok := p.Remaining[instanceID]; ok {
			return n
		}
	}
	return p.TokenCount()
}

// resolveRenderedPrompt tokenizes the original chat body once. Failure leaves
// the request on the existing character-length path and does not inject ids.
func resolveRenderedPrompt(ctx *PipelineContext) {
	if ctx == nil || ctx.InferManager == nil || ctx.InferManager.prefixQuerier == nil {
		return
	}
	if ctx.ClientGinCtx == nil || ctx.ClientGinCtx.Request == nil || !ctx.IsChat() {
		return
	}
	if ctx.RequestContext.InferMsg != nil && ctx.RequestContext.InferMsg.IsTesting {
		return
	}
	parsed := ctx.RequestContext.ParsedRequest
	if parsed == nil || len(parsed.OriginBodyBytes) == 0 {
		return
	}

	instanceID := pickRenderInstance(ctx.InferManager.InstanceMgr)
	if instanceID == "" {
		logger.Log().Warnf("render: request %s has no instance to tokenize, keep the text length", ctx.RequestId())
		return
	}
	instanceClient, err := ctx.InferManager.InstanceMgr.GetInstanceClient(instanceID)
	if err != nil {
		logger.Log().Warnf("render: request %s instance %s client: %v", ctx.RequestId(), instanceID, err)
		return
	}

	renderCtx, cancel := context.WithTimeout(ctx.ClientGinCtx.Request.Context(), renderTimeout)
	defer cancel()
	res, rsErr, _ := instanceClient.Generate(
		renderCtx, parsed.GetHeader(), ctx.RequestPath()+renderPathSuffix,
		bytes.NewReader(parsed.OriginBodyBytes), instanceID, constant.StagePrepare, ctx.RequestId(), false)
	if rsErr != nil {
		logger.Log().Warnf("render: request %s instance %s failed: %v", ctx.RequestId(), instanceID, rsErr)
		return
	}
	if res == nil || res.Body == nil {
		logger.Log().Warnf("render: request %s instance %s returned an empty response", ctx.RequestId(), instanceID)
		return
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(res.Body, 512))
		logger.Log().Warnf("render: request %s instance %s status %d body %s",
			ctx.RequestId(), instanceID, res.StatusCode, snippet)
		return
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, maxRenderBody+1))
	if err != nil {
		logger.Log().Warnf("render: request %s read body: %v", ctx.RequestId(), err)
		return
	}
	if len(body) > maxRenderBody {
		logger.Log().Warnf("render: request %s response exceeds %d bytes", ctx.RequestId(), maxRenderBody)
		return
	}
	ids, err := parseRenderTokenIDs(body)
	if err != nil {
		logger.Log().Warnf("render: request %s parse token ids: %v", ctx.RequestId(), err)
		return
	}

	prompt := &RenderedPrompt{TokenIDs: ids}
	if model := parsed.GetModel(); model != "" {
		hits, queryErr := ctx.InferManager.prefixQuerier.Query(renderCtx, model, ids, cacheSaltFromRequest(parsed))
		if queryErr != nil {
			logger.Log().Warnf("conductor query: request %s model %s tokens %d: %v",
				ctx.RequestId(), model, len(ids), queryErr)
		} else if hits != nil {
			prompt.Queried = true
			remaining, replacement := applyPrefixHits(ids, hits)
			prompt.Remaining = remaining
			logger.Log().Infof("conductor replace: request %s tokens=%d %s",
				ctx.RequestId(), len(ids), replacement)
		}
	}
	ctx.RequestContext.RenderedPrompt = prompt
	logger.Log().Infof("render: request %s tokens=%d queried=%v instances=%d via %s",
		ctx.RequestId(), len(ids), prompt.Queried, len(prompt.Remaining), instanceID)
}

// applyPrefixHits replaces the prompt length with the tokens each worker still
// has to compute. The hit subtracted is that worker's total prefix hit: Disk
// already includes the faster tiers, so the tiers are not added together.
func applyPrefixHits(ids []int32, hits map[string]conductor.RankMatch) (map[string]int32, string) {
	full := int64(len(ids))
	remaining := make(map[string]int32, len(hits))
	parts := make([]string, 0, len(hits))
	for id, match := range hits {
		units := conductor.ComputeRequestUnits(match, full)
		if units > math.MaxInt32 {
			units = math.MaxInt32
		}
		remaining[id] = int32(units)
		parts = append(parts, fmt.Sprintf("%s:total_hit=%d,unmatched=%d", id, full-units, units))
	}
	sort.Strings(parts)
	return remaining, strings.Join(parts, " ")
}

// cacheSaltFromRequest reads the salt from the client body. The renderer must
// not be asked to invent one.
func cacheSaltFromRequest(parsed *request.GenerateRequestImplV1) *string {
	if parsed == nil || parsed.RawBody == nil {
		return nil
	}
	salt, ok := parsed.RawBody.GetString("cache_salt")
	if !ok || salt == "" {
		return nil
	}
	return &salt
}

func pickRenderInstance(mgr instances.Manager) string {
	if mgr == nil {
		return ""
	}
	stats := mgr.GetAllInstanceStats()
	if id := firstAvailableInstance(mgr, stats[constant.Prefill]); id != "" {
		return id
	}
	for role, roleStats := range stats {
		if role == constant.Prefill {
			continue
		}
		if id := firstAvailableInstance(mgr, roleStats); id != "" {
			return id
		}
	}
	for id := range stats[constant.Prefill] {
		return id
	}
	return ""
}

func firstAvailableInstance(mgr instances.Manager, roleStats map[string]instance_metrics.WorkerStats) string {
	for id := range roleStats {
		if mgr.IsInstanceAvailable(id) {
			return id
		}
	}
	return ""
}

// parseRenderTokenIDs reads token_ids from a render response. The body is only
// used to obtain ids; the GenerateRequest itself is not forwarded.
func parseRenderTokenIDs(body []byte) ([]int32, error) {
	var raw interface{}
	if err := utils.SonicApi.Unmarshal(body, &raw); err != nil {
		return nil, err
	}
	ids, ok := tokenIDsFrom(raw, 0)
	if !ok || len(ids) == 0 {
		return nil, fmt.Errorf("render response has no token_ids")
	}
	return ids, nil
}

func tokenIDsFrom(raw interface{}, depth int) ([]int32, bool) {
	if depth > 2 || raw == nil {
		return nil, false
	}
	switch value := raw.(type) {
	case []interface{}:
		if len(value) == 0 {
			return nil, false
		}
		return tokenIDsFrom(value[0], depth+1)
	case map[string]interface{}:
		if ids, ok := readTokenIDs(value, "token_ids"); ok {
			return ids, true
		}
		if ids, ok := readTokenIDs(value, promptTokenIDsKey); ok {
			return ids, true
		}
		if nested, exists := value["request"]; exists {
			return tokenIDsFrom(nested, depth+1)
		}
		return nil, false
	default:
		return nil, false
	}
}

func readTokenIDs(obj map[string]interface{}, key string) ([]int32, bool) {
	raw, ok := obj[key]
	if !ok || raw == nil {
		return nil, false
	}
	items, ok := raw.([]interface{})
	if !ok || len(items) == 0 {
		return nil, false
	}
	ids := make([]int32, len(items))
	for i, item := range items {
		id, ok := asTokenID(item)
		if !ok {
			return nil, false
		}
		ids[i] = id
	}
	return ids, true
}

func asTokenID(value interface{}) (int32, bool) {
	switch n := value.(type) {
	case json.Number:
		parsed, err := n.Int64()
		if err != nil || parsed < 0 || parsed > math.MaxInt32 {
			return 0, false
		}
		return int32(parsed), true
	case float64:
		if n < 0 || n > math.MaxInt32 || n != math.Trunc(n) {
			return 0, false
		}
		return int32(n), true
	case int:
		if n < 0 || n > math.MaxInt32 {
			return 0, false
		}
		return int32(n), true
	case int32:
		if n < 0 {
			return 0, false
		}
		return n, true
	case int64:
		if n < 0 || n > math.MaxInt32 {
			return 0, false
		}
		return int32(n), true
	default:
		return 0, false
	}
}

func renderedTokenIDs(ctx *PipelineContext) []int32 {
	if ctx == nil || ctx.RequestContext == nil || ctx.RequestContext.RenderedPrompt == nil {
		return nil
	}
	return ctx.RequestContext.RenderedPrompt.TokenIDs
}

// attachPrefillPromptIDs adds the rendered ids to the prefill chat body.
// vLLM's chat frontend reads prompt_token_ids from kv_transfer_params and skips
// a second template render. do_remote_decode tells the prefill worker to stop
// after the KV cache is ready.
func attachPrefillPromptIDs(ctx *PipelineContext, body *utils.JSONModifier) {
	ids := renderedTokenIDs(ctx)
	if len(ids) == 0 || body == nil {
		return
	}
	body.Set(openai.MinTokensKey, 1)
	body.Set(openai.KvTransferParamsKey, map[string]interface{}{
		DoRemoteDecodeKey: true,
		promptTokenIDsKey: ids,
	})
}

// attachDecodePromptIDs rebuilds kv_transfer_params from the prefill response
// and the same rendered ids. It does not copy prefill sampling limits.
func attachDecodePromptIDs(ctx *PipelineContext, body *utils.JSONModifier) {
	if ctx == nil || ctx.RequestContext == nil || body == nil {
		return
	}
	params := map[string]interface{}{}
	if parsed := ctx.RequestContext.ParsedRequest; parsed != nil && parsed.KvTransferParams != nil {
		for key, value := range parsed.KvTransferParams {
			params[key] = value
		}
	}
	if ids := renderedTokenIDs(ctx); len(ids) > 0 {
		params[promptTokenIDsKey] = ids
	}
	if len(params) > 0 {
		body.Set(openai.KvTransferParamsKey, params)
	}
}
