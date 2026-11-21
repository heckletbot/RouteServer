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

package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"route-server/pkg/route-server/conductor"
)

type fakeHitQuerier struct {
	hits    map[string]conductor.RankMatch
	err     error
	lastReq struct {
		model     string
		tokenIDs  []int32
		cacheSalt *string
	}
}

func (q *fakeHitQuerier) Query(_ context.Context, model string, tokenIDs []int32,
	cacheSalt *string) (map[string]conductor.RankMatch, error) {
	q.lastReq.model = model
	q.lastReq.tokenIDs = append([]int32(nil), tokenIDs...)
	q.lastReq.cacheSalt = cacheSalt
	return q.hits, q.err
}

func performConductorQuery(t *testing.T, handler *ConductorQueryHandler, body string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/internal/conductor/query", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	handler.Handle(c)
	return w
}

func TestConductorQueryHandlerSuccess(t *testing.T) {
	querier := &fakeHitQuerier{hits: map[string]conductor.RankMatch{
		"prefill-0": {Device: 4, CPULocal: 4, CPUShare: 8, Disk: 8},
	}}
	handler := NewConductorQueryHandler(querier, 4)

	w := performConductorQuery(t, handler,
		`{"model":"qwen3-0.6b","token_ids":[1,2,3,4,5],"cache_salt":"salt"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if querier.lastReq.model != "qwen3-0.6b" || len(querier.lastReq.tokenIDs) != 5 {
		t.Errorf("querier request = %+v", querier.lastReq)
	}
	if querier.lastReq.cacheSalt == nil || *querier.lastReq.cacheSalt != "salt" {
		t.Errorf("cache salt = %v, want salt", querier.lastReq.cacheSalt)
	}

	var resp conductorQueryResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.CompleteTokens != 4 {
		t.Errorf("complete_tokens = %d, want 4", resp.CompleteTokens)
	}
	if resp.BlockSize != 4 {
		t.Errorf("block_size = %d, want 4", resp.BlockSize)
	}
	if resp.Instances["prefill-0"].Disk != 8 {
		t.Errorf("prefill-0 = %+v, want disk=8", resp.Instances["prefill-0"])
	}
}

func TestConductorQueryHandlerValidation(t *testing.T) {
	handler := NewConductorQueryHandler(&fakeHitQuerier{}, 4)
	cases := []string{
		`{}`,
		`{"model":"m"}`,
		`{"model":"","token_ids":[1]}`,
		`{"model":"m","token_ids":[]}`,
		`{"model":"m","token_ids":[-1]}`,
	}
	for _, body := range cases {
		w := performConductorQuery(t, handler, body)
		if w.Code != http.StatusBadRequest {
			t.Errorf("body %s: status = %d, want 400", body, w.Code)
		}
	}
}

func TestConductorQueryHandlerUpstreamError(t *testing.T) {
	handler := NewConductorQueryHandler(&fakeHitQuerier{err: errors.New("boom")}, 4)
	w := performConductorQuery(t, handler, `{"model":"m","token_ids":[1,2,3,4]}`)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", w.Code)
	}
}

func TestConductorQueryHandlerDisabled(t *testing.T) {
	handler := NewConductorQueryHandler(nil, 0)
	w := performConductorQuery(t, handler, `{"model":"m","token_ids":[1]}`)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", w.Code)
	}
}
