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

package agentic_balancer

import (
	"fmt"
	"net/http"
	"strings"

	"route-server/pkg/common/logger"
)

// SessionContext holds the session identity parsed from HTTP headers or the body.
type SessionContext struct {
	SessionID       string
	ParentSessionID string
}

// Session source headers (names are case-insensitive), grouped by platform:
// Dynamo/Codex/OpenCode carry their own session and parent ids; Claude Code uses
// Session-ID as the root session and Agent-ID as the sub-agent session;
// X-Correlation-ID is only a session_id fallback when the config enables it.
const (
	hdrDynamoSessionID       = "X-Dynamo-Session-ID"
	hdrDynamoParentSessionID = "X-Dynamo-Parent-Session-ID"

	hdrCodexThreadID       = "thread-id"
	hdrCodexParentThreadID = "X-Codex-Parent-Thread-ID"

	hdrOpenCodeSessionID       = "X-OpenCode-Session-ID"
	hdrOpenCodeParentSessionID = "X-OpenCode-Parent-Session-ID"

	hdrClaudeCodeSessionID   = "X-Claude-Code-Session-ID"
	hdrClaudeCodeAgentID     = "X-Claude-Code-Agent-ID"
	hdrClaudeCodeParentAgent = "X-Claude-Code-Parent-Agent-ID"

	hdrCorrelationID = "X-Correlation-ID"
)

// sessionCandidate is a session_id taken from one source.
type sessionCandidate struct {
	value  string
	source string
}

// maxSessionIDLen bounds session_id / parent_session_id so a malicious oversized
// header cannot inflate memory.
const maxSessionIDLen = 256

// getHeader returns the trimmed header value; an empty result means absent.
func getHeader(h http.Header, key string) string {
	return strings.TrimSpace(h.Get(key))
}

// validateSessionIDLen rejects values that are too long or contain control
// characters.
func validateSessionIDLen(value, source string) error {
	if len(value) > maxSessionIDLen {
		logger.Log().Warnf("agentic-balancer: session id too long from source %s, len=%d", source, len(value))
		return fmt.Errorf("session_id from source %s exceeds max length %d", source, maxSessionIDLen)
	}
	for i := 0; i < len(value); i++ {
		if value[i] < 0x20 || value[i] == 0x7f {
			logger.Log().Warnf("agentic-balancer: session id contains control character from source %s", source)
			return fmt.Errorf("session_id from source %s contains control characters", source)
		}
	}
	return nil
}

// collectSessionCandidates gathers session_id candidates in priority order.
// Claude Code Agent-ID and Session-ID are mutually exclusive: when Agent-ID is
// present Session-ID does not compete and is treated as the parent instead.
// X-Correlation-ID covers clients without a dedicated session header that reuse
// one correlation id for the whole conversation; it is off by default.
func collectSessionCandidates(
	h http.Header,
	bodySessionID string,
	enableCorrelationID bool,
) []sessionCandidate {
	var candidates []sessionCandidate

	if v := getHeader(h, hdrDynamoSessionID); v != "" {
		candidates = append(candidates, sessionCandidate{v, hdrDynamoSessionID})
	}

	if v := getHeader(h, hdrCodexThreadID); v != "" {
		candidates = append(candidates, sessionCandidate{v, hdrCodexThreadID})
	}

	agentID := getHeader(h, hdrClaudeCodeAgentID)
	rootSessionID := getHeader(h, hdrClaudeCodeSessionID)
	if agentID != "" {
		candidates = append(candidates, sessionCandidate{agentID, hdrClaudeCodeAgentID})
	} else if rootSessionID != "" {
		candidates = append(candidates, sessionCandidate{rootSessionID, hdrClaudeCodeSessionID})
	}

	if v := getHeader(h, hdrOpenCodeSessionID); v != "" {
		candidates = append(candidates, sessionCandidate{v, hdrOpenCodeSessionID})
	}

	if enableCorrelationID {
		if v := getHeader(h, hdrCorrelationID); v != "" {
			candidates = append(candidates, sessionCandidate{v, hdrCorrelationID})
		}
	}

	bodySID := strings.TrimSpace(bodySessionID)
	if bodySID != "" {
		candidates = append(candidates, sessionCandidate{bodySID, "body"})
	}

	return candidates
}

// ParseSessionContext resolves session_id and parent_session_id. Conflicting
// values from different sources are an error so affinity cannot silently fail;
// parent_session_id is checked the same way.
func ParseSessionContext(
	h http.Header,
	bodySessionID string,
	bodyParentSessionID string,
	enableCorrelationID bool,
) (SessionContext, error) {
	candidates := collectSessionCandidates(h, bodySessionID, enableCorrelationID)

	if len(candidates) == 0 {
		return SessionContext{}, nil
	}

	for _, c := range candidates {
		if err := validateSessionIDLen(c.value, c.source); err != nil {
			return SessionContext{}, err
		}
	}

	first := candidates[0]
	for _, c := range candidates[1:] {
		if c.value != first.value {
			logger.Log().Warnf("agentic-balancer: session id conflict, source %s=%q vs source %s=%q",
				first.source, maskSessionID(first.value), c.source, maskSessionID(c.value))
			return SessionContext{}, fmt.Errorf(
				"conflicting session_id from sources %s and %s",
				first.source, c.source)
		}
	}

	sid := first.value

	rootSessionID := getHeader(h, hdrClaudeCodeSessionID)
	var parentCandidates []sessionCandidate
	for _, sc := range candidates {
		p := resolveParent(h, sc.source, rootSessionID, bodyParentSessionID)
		if p != "" {
			parentCandidates = append(parentCandidates, sessionCandidate{p, sc.source + ".parent"})
		}
	}

	var parentSid string
	if len(parentCandidates) > 0 {
		pFirst := parentCandidates[0]
		for _, c := range parentCandidates[1:] {
			if c.value != pFirst.value {
				logger.Log().Warnf("agentic-balancer: parent session id conflict, source %s=%q vs source %s=%q",
					pFirst.source, maskSessionID(pFirst.value), c.source, maskSessionID(c.value))
				return SessionContext{}, fmt.Errorf(
					"conflicting parent_session_id from sources %s and %s",
					pFirst.source, c.source)
			}
		}
		parentSid = pFirst.value
	}

	if parentSid != "" {
		if err := validateSessionIDLen(parentSid, "parent_session_id"); err != nil {
			return SessionContext{}, err
		}
	}

	return SessionContext{
		SessionID:       sid,
		ParentSessionID: parentSid,
	}, nil
}

// resolveParent returns the parent_session_id that belongs to the session source.
// A Claude Code Agent-ID prefers Parent-Agent-ID and falls back to the root
// Session-ID.
func resolveParent(h http.Header, source, rootSessionID, bodyParentSessionID string) string {
	switch source {
	case hdrDynamoSessionID:
		return getHeader(h, hdrDynamoParentSessionID)
	case hdrCodexThreadID:
		return getHeader(h, hdrCodexParentThreadID)
	case hdrClaudeCodeAgentID:
		p := getHeader(h, hdrClaudeCodeParentAgent)
		if p != "" {
			return p
		}
		return rootSessionID
	case hdrClaudeCodeSessionID:
		return ""
	case hdrOpenCodeSessionID:
		return getHeader(h, hdrOpenCodeParentSessionID)
	case hdrCorrelationID:
		return ""
	case "body":
		return strings.TrimSpace(bodyParentSessionID)
	}
	return ""
}

// maskSessionID returns a log-safe form: short ids are masked entirely, long ids
// keep their first four characters.
func maskSessionID(id string) string {
	if len(id) <= 4 {
		return strings.Repeat("*", len(id))
	}
	return id[:4] + strings.Repeat("*", len(id)-4)
}
