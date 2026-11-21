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

package request

import (
	"math"
	"net/http"

	"route-server/pkg/common/openai"
	"route-server/pkg/common/utils"
	"route-server/pkg/route-server/scheduler/agentic_balancer"
)

// parseSchedulingFields resolves session_id / parent_session_id / priority /
// outputLength for both GenerateRequestImpl and GenerateRequestImplV1.
func parseSchedulingFields(
	body *utils.JSONModifier,
	header http.Header,
	enableCorrelationID bool,
	originMaxTokens int32,
) (sessionId, parentSessionId string, priority, outputLength int32, err error) {
	bodySID, _ := body.GetString(openai.SessionIdKey)
	bodyParentSID, _ := body.GetString(openai.ParentSessionIdKey)
	sctx, err := agentic_balancer.ParseSessionContext(header, bodySID, bodyParentSID, enableCorrelationID)
	if err != nil {
		return "", "", 0, 0, err
	}
	sessionId = sctx.SessionID
	parentSessionId = sctx.ParentSessionID

	// Out-of-range priorities and output lengths are treated as unspecified
	// rather than rejected: they are hints, not part of the request contract.
	if p, exists := body.GetInt(openai.PriorityKey); exists {
		if p < 0 || p > math.MaxInt32 {
			priority = 0
		} else {
			priority = int32(p)
		}
	}

	if ol, exists := body.GetInt(openai.OutputLengthKey); exists {
		if ol < 0 || ol > math.MaxInt32 {
			outputLength = 0
		} else {
			outputLength = int32(ol)
		}
	} else if originMaxTokens > 0 {
		// Fall back to max_tokens: it is the best available upper bound on the
		// generated length when the caller does not predict one.
		outputLength = originMaxTokens
	}

	return sessionId, parentSessionId, priority, outputLength, nil
}
