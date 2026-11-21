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
	"net/http"
	"strings"
	"testing"
)

func setHeaders(h http.Header, kv ...string) {
	for i := 0; i+1 < len(kv); i += 2 {
		h.Set(kv[i], kv[i+1])
	}
}

func newHeaders(m map[string]string) http.Header {
	h := http.Header{}
	for k, v := range m {
		setHeaders(h, k, v)
	}
	return h
}

// TestParseSessionContext_Sources covers each source providing session_id alone.
func TestParseSessionContext_Sources(t *testing.T) {
	tests := []struct {
		name       string
		headers    map[string]string
		bodySID    string
		enableCorr bool
		wantSid    string
		wantPid    string
	}{
		{"Dynamo", map[string]string{"X-Dynamo-Session-ID": "sess-1"}, "", false, "sess-1", ""},
		{"Codex", map[string]string{"thread-id": "thread-1"}, "", false, "thread-1", ""},
		{"OpenCode", map[string]string{"X-OpenCode-Session-ID": "oc-1"}, "", false, "oc-1", ""},
		{"OpenCodeWithParent", map[string]string{
			"X-OpenCode-Session-ID": "oc-sess", "X-OpenCode-Parent-Session-ID": "oc-parent",
		}, "", false, "oc-sess", "oc-parent"},
		{"ClaudeCodeRoot", map[string]string{"X-Claude-Code-Session-ID": "cc-root"}, "", false, "cc-root", ""},
		{"CorrelationDisabled", map[string]string{"X-Correlation-ID": "corr-1"}, "body-sess", false, "body-sess", ""},
		{"CorrelationSoleSource", map[string]string{"X-Correlation-ID": "corr-only"}, "", true, "corr-only", ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseSessionContext(newHeaders(tc.headers), tc.bodySID, "", tc.enableCorr)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.SessionID != tc.wantSid {
				t.Errorf("expected %q, got %q", tc.wantSid, got.SessionID)
			}
			if got.ParentSessionID != tc.wantPid {
				t.Errorf("expected parent %q, got %q", tc.wantPid, got.ParentSessionID)
			}
		})
	}
}

// TestParseSessionContext_ClaudeCode covers Claude Code sub-agent session/parent resolution.
func TestParseSessionContext_ClaudeCode(t *testing.T) {
	tests := []struct {
		name    string
		headers map[string]string
		wantSid string
		wantPid string
	}{
		{"AgentAndRootNotConflict", map[string]string{
			"X-Claude-Code-Session-ID": "cc-root", "X-Claude-Code-Agent-ID": "cc-sub",
		}, "cc-sub", "cc-root"},
		{"ParentAgentPreferred", map[string]string{
			"X-Claude-Code-Session-ID":      "cc-root",
			"X-Claude-Code-Agent-ID":        "cc-sub",
			"X-Claude-Code-Parent-Agent-ID": "cc-parent-agent",
		}, "cc-sub", "cc-parent-agent"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseSessionContext(newHeaders(tc.headers), "", "", false)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.SessionID != tc.wantSid || got.ParentSessionID != tc.wantPid {
				t.Errorf("expected %q/%q, got %q/%q", tc.wantSid, tc.wantPid, got.SessionID, got.ParentSessionID)
			}
		})
	}
}

// TestParseSessionContext_Conflicts covers conflict detection for session_id and
// parent_session_id, and checks that error messages leak no id fragment.
func TestParseSessionContext_Conflicts(t *testing.T) {
	tests := []struct {
		name       string
		headers    map[string]string
		bodySID    string
		bodyParent string
		enableCorr bool
		wantErr    string
	}{
		{
			name: "DifferentSessionIDs",
			headers: map[string]string{
				"X-Dynamo-Session-ID": "dynamo-secret-session-1234",
				"thread-id":           "codex-secret-session-5678",
			},
			wantErr: "conflicting session_id",
		},
		{
			name:    "HeaderAndBodyConflict",
			headers: map[string]string{"X-Dynamo-Session-ID": "header-sess"},
			bodySID: "body-sess",
			wantErr: "conflicting session_id",
		},
		{
			name:       "CorrelationAndBodyConflict",
			headers:    map[string]string{"X-Correlation-ID": "corr-1"},
			bodySID:    "body-sess",
			enableCorr: true,
			wantErr:    "conflicting session_id",
		},
		{
			name: "ParentConflict",
			headers: map[string]string{
				"X-Dynamo-Session-ID":        "shared",
				"X-Dynamo-Parent-Session-ID": "dp",
				"thread-id":                  "shared",
				"X-Codex-Parent-Thread-ID":   "cp",
			},
			bodySID: "shared",
			wantErr: "conflicting parent_session_id",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseSessionContext(newHeaders(tc.headers), tc.bodySID, tc.bodyParent, tc.enableCorr)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("expected error containing %q, got %q", tc.wantErr, err)
			}
		})
	}

	// NoLeakToClient: the error message must not leak any session_id fragment.
	t.Run("NoLeakToClient", func(t *testing.T) {
		const dynamoSess = "dynamo-secret-session-1234"
		const codexSess = "codex-secret-session-5678"
		h := newHeaders(map[string]string{
			"X-Dynamo-Session-ID": dynamoSess,
			"thread-id":           codexSess,
		})
		_, err := ParseSessionContext(h, "", "", false)
		if err == nil {
			t.Fatal("expected conflict error, got nil")
		}
		msg := err.Error()
		for _, fragment := range []string{dynamoSess, codexSess, "dynamo-secret", "codex-secret", "dyna", "code"} {
			if strings.Contains(msg, fragment) {
				t.Errorf("error message leaks session_id fragment %q: %s", fragment, msg)
			}
		}
		if !strings.Contains(msg, "X-Dynamo-Session-ID") || !strings.Contains(msg, "thread-id") {
			t.Errorf("error should mention source names, got: %s", msg)
		}
	})
}

// TestParseSessionContext_Parents covers parent_session_id resolution.
func TestParseSessionContext_Parents(t *testing.T) {
	tests := []struct {
		name       string
		headers    map[string]string
		bodySID    string
		bodyParent string
		wantSid    string
		wantPid    string
		wantErr    string
	}{
		{
			name:       "DynamoParent",
			headers:    map[string]string{"X-Dynamo-Session-ID": "sess", "X-Dynamo-Parent-Session-ID": "parent"},
			bodySID:    "sess",
			bodyParent: "parent",
			wantSid:    "sess",
			wantPid:    "parent",
		},
		{
			name:       "BodyParentUsedWhenSessionFromBoth",
			headers:    map[string]string{"X-Dynamo-Session-ID": "header-sess"},
			bodySID:    "header-sess",
			bodyParent: "body-parent",
			wantSid:    "header-sess",
			wantPid:    "body-parent",
		},
		{
			name:    "ParentOnlyWithoutSessionIgnored",
			headers: map[string]string{"X-Dynamo-Parent-Session-ID": "parent"},
			wantSid: "",
			wantPid: "",
		},
		{
			name:       "BodySessionAndParentWithTrimSpace",
			bodySID:    "  body-sess  ",
			bodyParent: "body-parent",
			wantSid:    "body-sess",
			wantPid:    "body-parent",
		},
		{
			name:    "SameValueNoConflict",
			headers: map[string]string{"X-Dynamo-Session-ID": "shared-sess", "thread-id": "shared-sess", "X-OpenCode-Session-ID": "shared-sess"},
			bodySID: "shared-sess",
			wantSid: "shared-sess",
			wantPid: "",
		},
		{
			name:    "EmptyValuesSkipped",
			headers: map[string]string{"X-Dynamo-Session-ID": "   ", "X-OpenCode-Session-ID": "oc-sess"},
			wantSid: "oc-sess",
			wantPid: "",
		},
		{
			name:    "NoSessionReturnsEmpty",
			wantSid: "",
			wantPid: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseSessionContext(newHeaders(tc.headers), tc.bodySID, tc.bodyParent, false)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("expected error containing %q, got %q", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.SessionID != tc.wantSid || got.ParentSessionID != tc.wantPid {
				t.Errorf("expected %q/%q, got %q/%q", tc.wantSid, tc.wantPid, got.SessionID, got.ParentSessionID)
			}
		})
	}
}

// TestParseSessionContext_Validation covers the length limit and control characters.
func TestParseSessionContext_Validation(t *testing.T) {
	tests := []struct {
		name    string
		headers map[string]string
		wantErr string
	}{
		{"SessionIDExceedsMaxLen", map[string]string{"X-Dynamo-Session-ID": strings.Repeat("a", maxSessionIDLen+1)}, "exceeds max length"},
		{"ParentSessionIDExceedsMaxLen", map[string]string{"X-Dynamo-Session-ID": "sess", "X-Dynamo-Parent-Session-ID": strings.Repeat("p", maxSessionIDLen+1)}, "exceeds max length"},
		{"SessionIDWithControlChar", map[string]string{"X-Dynamo-Session-ID": "bad\nsession"}, "control characters"},
		{"ParentSessionIDWithControlChar", map[string]string{"X-Dynamo-Session-ID": "sess", "X-Dynamo-Parent-Session-ID": "bad\nparent"}, "control characters"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseSessionContext(newHeaders(tc.headers), "", "", false)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("expected error containing %q, got %q", tc.wantErr, err)
			}
		})
	}

	// Boundary: a value exactly at the limit is accepted.
	t.Run("MaxLenBoundaryOK", func(t *testing.T) {
		okID := strings.Repeat("a", maxSessionIDLen)
		h := newHeaders(map[string]string{"X-Dynamo-Session-ID": okID})
		got, err := ParseSessionContext(h, "", "", false)
		if err != nil {
			t.Fatalf("expected no error at max boundary, got: %v", err)
		}
		if got.SessionID != okID {
			t.Errorf("expected session id of max length, got len=%d", len(got.SessionID))
		}
	})
}

// TestMaskSessionID covers the masking behavior.
func TestMaskSessionID(t *testing.T) {
	tests := []struct {
		name string
		id   string
		want string
	}{
		{"ShortID", "abc", "***"},
		{"LongID", "abcdef123456", "abcd********"},
		{"ControlCharInPrefix", "ab\ncd", "ab\nc*"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := maskSessionID(tc.id); got != tc.want {
				t.Errorf("expected %q, got %q", tc.want, got)
			}
		})
	}
}
