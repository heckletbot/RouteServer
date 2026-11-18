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
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestPrintVersionModuleMetadata(t *testing.T) {
	expected := map[string]string{
		"name":          "RouteServer",
		"node_name":     "module-metadata-check",
		"version":       version,
		"tag_or_branch": tagOrBranch,
		"commit_id":     commitId,
		"build_time":    buildTime,
		"port_type":     "service_port",
	}
	// Optional expectations come from the build caller, never from assignments to
	// the production metadata variables. This detects stale linker package paths.
	if raw := os.Getenv("ROUTESERVER_TEST_EXPECT_METADATA"); raw != "" {
		var injected map[string]string
		if err := json.Unmarshal([]byte(raw), &injected); err != nil {
			t.Fatal(err)
		}
		if len(injected) != 4 {
			t.Fatal("expected all four build metadata fields")
		}
		for _, key := range []string{"version", "tag_or_branch", "commit_id", "build_time"} {
			value, ok := injected[key]
			if !ok || value == "" {
				t.Fatalf("missing injected field %q", key)
			}
			expected[key] = value
		}
	}
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	printVersion(ctx, "module-metadata-check", "service_port")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	var actual map[string]string
	if err := json.Unmarshal(recorder.Body.Bytes(), &actual); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("metadata = %#v, want %#v", actual, expected)
	}
}
