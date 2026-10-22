/*
 * Copyright 2026 The vLLM-Cloud Authors.
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

package middleware

import (
	"bytes"
	"net/http"
	"strconv"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func TestRequestBodyLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name           string
		maxSize        int64
		bodySize       int64
		expectedStatus int
	}{
		{
			name:           "Small body should pass",
			maxSize:        50 * 1024 * 1024, // 50MB
			bodySize:       1024,             // 1KB
			expectedStatus: http.StatusOK,
		},
		{
			name:           "Large body should return 413",
			maxSize:        50 * 1024 * 1024, // 50MB
			bodySize:       60 * 1024 * 1024, // 60MB
			expectedStatus: http.StatusRequestEntityTooLarge,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			router := gin.New()
			router.Use(RequestBodyLimit(tt.maxSize))

			router.POST("/test", func(c *gin.Context) {
				c.Status(http.StatusOK)
			})

			// Create request body
			body := bytes.Repeat([]byte("a"), int(tt.bodySize))

			req, _ := http.NewRequest("POST", "/test", bytes.NewReader(body))
			req.Header.Set("Content-Length", strconv.FormatInt(tt.bodySize, 10))
			w := &testResponseWriter{
				header: make(http.Header),
				body:   &bytes.Buffer{},
			}

			router.ServeHTTP(w, req)

			assert.Equal(t, tt.expectedStatus, w.status)
		})
	}
}

// testResponseWriter implements http.ResponseWriter for testing
type testResponseWriter struct {
	status int
	header http.Header
	body   *bytes.Buffer
}

func (w *testResponseWriter) Header() http.Header {
	return w.header
}

func (w *testResponseWriter) Write(data []byte) (int, error) {
	return w.body.Write(data)
}

func (w *testResponseWriter) WriteHeader(statusCode int) {
	w.status = statusCode
}
