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
	"strings"
	"testing"

	utils2 "route-server/pkg/common/infer"
	"route-server/pkg/common/openai"
	"route-server/pkg/common/utils"
	"route-server/pkg/route-server/conductor"
	"route-server/pkg/route-server/handler/request"
)

func TestApplyPrefixHitsUsesTotalHit(t *testing.T) {
	ids := make([]int32, 100)
	hits := map[string]conductor.RankMatch{
		"p1": {Device: 10, CPULocal: 20, CPUShare: 40, Disk: 70},
		"d1": {Device: 5, CPULocal: 5, CPUShare: 5, Disk: 5},
	}

	remaining, line := applyPrefixHits(ids, hits)
	if remaining["p1"] != 30 || remaining["d1"] != 95 {
		t.Fatalf("remaining = %v, want p1=30 d1=95", remaining)
	}
	if !strings.Contains(line, "d1:total_hit=5,unmatched=95") || !strings.Contains(line, "p1:total_hit=70,unmatched=30") {
		t.Fatalf("replacement log = %q", line)
	}
}

func TestTokenizeRequestBodyKeepsTokenFieldsOnly(t *testing.T) {
	raw, err := utils.NewJSONModifier([]byte(`{"model":"qwen","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function"}],"max_tokens":128,"stream":true,"temperature":0.2}`))
	if err != nil {
		t.Fatalf("parse body: %v", err)
	}
	body, err := tokenizeRequestBody(&request.GenerateRequestImplV1{RawBody: raw})
	if err != nil {
		t.Fatalf("tokenizeRequestBody() error = %v", err)
	}
	parsed, err := utils.NewJSONModifier(body)
	if err != nil {
		t.Fatalf("parse tokenize body: %v", err)
	}
	if model, ok := parsed.GetString("model"); !ok || model != "qwen" {
		t.Fatalf("model = %q, %v", model, ok)
	}
	if _, ok := parsed.GetArray("messages"); !ok {
		t.Fatal("tokenize body dropped messages")
	}
	if _, ok := parsed.GetArray("tools"); !ok {
		t.Fatal("tokenize body dropped tools")
	}
	if _, ok := parsed.GetInt("max_tokens"); ok {
		t.Fatal("tokenize body kept max_tokens")
	}
	if _, ok := parsed.GetBool("stream"); ok {
		t.Fatal("tokenize body kept stream")
	}
}

func TestParseRenderTokenIDs(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		want    []int32
		wantErr bool
	}{
		{name: "top level token_ids", body: `{"token_ids":[1,2,3]}`, want: []int32{1, 2, 3}},
		{name: "nested prompt_token_ids", body: `{"request":{"prompt_token_ids":[4,5]}}`, want: []int32{4, 5}},
		{name: "array of renders", body: `[{"token_ids":[7]}]`, want: []int32{7}},
		{name: "tokenize tokens", body: `{"count":3,"max_model_len":2048,"tokens":[9,8,7]}`, want: []int32{9, 8, 7}},
		{name: "empty", body: `{"token_ids":[]}`, wantErr: true},
		{name: "negative", body: `{"token_ids":[1,-2]}`, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseRenderTokenIDs([]byte(tc.body))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseRenderTokenIDs() = %v, want error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseRenderTokenIDs() error = %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("parseRenderTokenIDs() = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("parseRenderTokenIDs() = %v, want %v", got, tc.want)
				}
			}
		})
	}
}

func chatBody(t *testing.T) *utils.JSONModifier {
	t.Helper()
	body, err := utils.NewJSONModifier([]byte(`{"model":"qwen3.6","messages":[{"role":"user","content":"什么是 KV Cache？"}],"max_tokens":128,"temperature":0.2,"stream":true}`))
	if err != nil {
		t.Fatalf("parse body: %v", err)
	}
	return body
}

func renderedContext(ids []int32, kv map[string]interface{}) *PipelineContext {
	return &PipelineContext{
		RequestContext: &RequestContext{
			ParsedRequest: &request.GenerateRequestImplV1{KvTransferParams: kv},
			RenderedPrompt: &RenderedPrompt{
				TokenIDs: ids,
			},
		},
	}
}

func TestPrefillBodyCarriesRenderedIDs(t *testing.T) {
	ctx := renderedContext([]int32{11, 22, 33}, nil)
	body := PreparePrefillRequestBody(ctx, chatBody(t))

	maxTokens, ok := body.GetInt(openai.MaxTokenKey)
	if !ok || maxTokens != 1 {
		t.Fatalf("max_tokens = %d, %v", maxTokens, ok)
	}
	stream, ok := body.GetBool(openai.StreamKey)
	if !ok || stream {
		t.Fatalf("stream = %v, %v", stream, ok)
	}
	minTokens, ok := body.GetInt(openai.MinTokensKey)
	if !ok || minTokens != 1 {
		t.Fatalf("min_tokens = %d, %v", minTokens, ok)
	}
	params, ok := body.GetObject(openai.KvTransferParamsKey)
	if !ok {
		t.Fatal("missing kv_transfer_params")
	}
	if params[DoRemoteDecodeKey] != true {
		t.Fatalf("do_remote_decode = %v", params[DoRemoteDecodeKey])
	}
	if !sameIDs(t, params[promptTokenIDsKey], []int32{11, 22, 33}) {
		t.Fatalf("prompt_token_ids = %#v", params[promptTokenIDsKey])
	}
}

func TestDecodeBodyKeepsOriginalLimitsAndPrefillHandoff(t *testing.T) {
	ctx := renderedContext([]int32{11, 22, 33}, map[string]interface{}{
		"remote_host": "10.0.0.8",
		"remote_port": 9000,
	})
	body := PrepareDecodeRequestBody(ctx, chatBody(t))

	maxTokens, _ := body.GetInt(openai.MaxTokenKey)
	if maxTokens != 128 {
		t.Fatalf("max_tokens = %d, want 128", maxTokens)
	}
	stream, _ := body.GetBool(openai.StreamKey)
	if !stream {
		t.Fatal("stream = false, want true")
	}
	if _, ok := body.GetInt(openai.MinTokensKey); ok {
		t.Fatal("decode body inherited min_tokens")
	}
	params, ok := body.GetObject(openai.KvTransferParamsKey)
	if !ok {
		t.Fatal("missing kv_transfer_params")
	}
	if _, exists := params[DoRemoteDecodeKey]; exists {
		t.Fatal("decode body set do_remote_decode")
	}
	if params["remote_host"] != "10.0.0.8" {
		t.Fatalf("remote_host = %v", params["remote_host"])
	}
	if !sameIDs(t, params[promptTokenIDsKey], []int32{11, 22, 33}) {
		t.Fatalf("prompt_token_ids = %#v", params[promptTokenIDsKey])
	}
}

func TestPrefillBodyUnchangedWithoutRender(t *testing.T) {
	ctx := &PipelineContext{RequestContext: &RequestContext{
		ParsedRequest: &request.GenerateRequestImplV1{},
	}}
	original, err := utils.NewJSONModifier([]byte(`{"max_tokens":128,"stream":true,"min_tokens":8,"kv_transfer_params":{"remote_host":"x"}}`))
	if err != nil {
		t.Fatalf("parse body: %v", err)
	}
	body := PreparePrefillRequestBody(ctx, original)
	if _, ok := body.GetObject(openai.KvTransferParamsKey); ok {
		t.Fatal("prefill kept kv_transfer_params without a render")
	}
	if _, ok := body.GetInt(openai.MinTokensKey); ok {
		t.Fatal("prefill kept min_tokens without a render")
	}
}

func TestInitRequestLoadUsesRenderedLength(t *testing.T) {
	raw, err := utils.NewJSONModifier([]byte(`{"messages":[{"role":"user","content":"hello hello hello"}]}`))
	if err != nil {
		t.Fatalf("parse body: %v", err)
	}
	ctx := &PipelineContext{RequestContext: &RequestContext{
		ParsedRequest: &request.GenerateRequestImplV1{RawBody: raw},
		RenderedPrompt: &RenderedPrompt{
			TokenIDs:  []int32{1, 2, 3, 4, 5},
			Queried:   true,
			Remaining: map[string]int32{"p1": 2},
		},
	}}
	ctx.SetSubRequests("prefill")
	phase := &SchedulePhase{BasePhase: NewBasePhase("schedule", "")}
	phase.phaseCfg = &utils2.PhaseConfig{
		ScheduleParams: utils2.ScheduleConfig{RequestLoad: []string{LoadTypeText}},
	}

	phase.InitRequestLoad(ctx, "prefill")
	sub := ctx.GetSubRequests("prefill")
	if sub.GetPromptTokenCount() != 5 {
		t.Fatalf("prompt tokens = %d, want 5", sub.GetPromptTokenCount())
	}
	work, ok := sub.WorkTokens("p1")
	if !ok || work != 2 {
		t.Fatalf("WorkTokens(p1) = %d, %v, want 2, true", work, ok)
	}
	full, ok := sub.WorkTokens("unknown")
	if !ok || full != 5 {
		t.Fatalf("WorkTokens(unknown) = %d, %v, want 5, true", full, ok)
	}
}

func sameIDs(t *testing.T, raw interface{}, want []int32) bool {
	t.Helper()
	switch ids := raw.(type) {
	case []int32:
		if len(ids) != len(want) {
			return false
		}
		for i := range ids {
			if ids[i] != want[i] {
				return false
			}
		}
		return true
	case []interface{}:
		if len(ids) != len(want) {
			return false
		}
		for i := range ids {
			got, ok := asTokenID(ids[i])
			if !ok || got != want[i] {
				return false
			}
		}
		return true
	default:
		return false
	}
}
