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
	"net/http"

	"github.com/gin-gonic/gin"

	"route-server/pkg/common/logger"
	"route-server/pkg/route-server/conductor"
)

// maxConductorQueryTokens caps the token list accepted by the diagnostics endpoint.
// The limit exists to bound request memory, not because Conductor has a protocol
// limit; real prompts are far below it.
const maxConductorQueryTokens = 1 << 20

type conductorQueryRequest struct {
	Model     string  `json:"model"`
	TokenIDs  []int32 `json:"token_ids"`
	CacheSalt *string `json:"cache_salt,omitempty"`
}

type conductorTierHits struct {
	Device   int64 `json:"device"`
	CPULocal int64 `json:"cpu_local"`
	CPUShare int64 `json:"cpu_share"`
	Disk     int64 `json:"disk"`
}

type conductorQueryResponse struct {
	Model          string                       `json:"model"`
	BlockSize      int                          `json:"block_size"`
	CompleteTokens int                          `json:"complete_tokens"`
	Instances      map[string]conductorTierHits `json:"instances"`
}

// ConductorQueryHandler exposes the prefix-hit query as an internal diagnostics API.
// It is registered on the ops port only; it never takes part in scheduling.
type ConductorQueryHandler struct {
	querier   conductor.HitQuerier
	blockSize int
}

// NewConductorQueryHandler builds the handler. A nil querier means the integration is
// disabled; the route is then not registered at all.
func NewConductorQueryHandler(querier conductor.HitQuerier, blockSize int) *ConductorQueryHandler {
	return &ConductorQueryHandler{querier: querier, blockSize: blockSize}
}

// Handle answers POST /internal/conductor/query with the per-instance cumulative hits.
func (h *ConductorQueryHandler) Handle(c *gin.Context) {
	if h.querier == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "conductor integration is disabled"})
		return
	}

	var req conductorQueryRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body: " + err.Error()})
		return
	}
	if req.Model == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "model must not be empty"})
		return
	}
	if len(req.TokenIDs) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "token_ids must not be empty"})
		return
	}
	if len(req.TokenIDs) > maxConductorQueryTokens {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{
			"error": "token_ids exceeds the limit",
		})
		return
	}
	for _, id := range req.TokenIDs {
		if id < 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "token_ids must not contain negative values"})
			return
		}
	}

	hits, err := h.querier.Query(c.Request.Context(), req.Model, req.TokenIDs, req.CacheSalt)
	if err != nil {
		// A failed query leaves callers with no cache credit; do not turn it into a
		// misleading answer that only reports zero hits.
		logger.Log().Warnf("conductor query from internal API failed, model=%s, tokens=%d: %v",
			req.Model, len(req.TokenIDs), err)
		c.JSON(http.StatusBadGateway, gin.H{"error": "conductor query failed: " + err.Error()})
		return
	}

	instances := make(map[string]conductorTierHits, len(hits))
	for id, match := range hits {
		instances[id] = conductorTierHits{
			Device:   match.Device,
			CPULocal: match.CPULocal,
			CPUShare: match.CPUShare,
			Disk:     match.Disk,
		}
	}

	completeTokens := 0
	if h.blockSize > 0 {
		completeTokens = len(req.TokenIDs) / h.blockSize * h.blockSize
	}
	c.JSON(http.StatusOK, conductorQueryResponse{
		Model:          req.Model,
		BlockSize:      h.blockSize,
		CompleteTokens: completeTokens,
		Instances:      instances,
	})
}
