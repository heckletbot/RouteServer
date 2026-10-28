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
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
)

// RequestBodyLimit creates a middleware that limits the size of request body.
// It uses http.MaxBytesReader which returns HTTP 413 Payload Too Large error
// if the body exceeds the limit.
// A maxBytes value <= 0 disables the limit (no body size restriction).
func RequestBodyLimit(maxBytes int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		// maxBytes <= 0 means unlimited, skip body size restriction entirely
		if maxBytes <= 0 {
			c.Next()
			return
		}

		// First check Content-Length header if present
		if contentLength := c.Request.Header.Get("Content-Length"); contentLength != "" {
			if length, err := strconv.ParseInt(contentLength, 10, 64); err == nil && length > maxBytes {
				c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "request body too large"})
				c.Abort()
				return
			}
		}

		// Use MaxBytesReader which will trigger 413 if body exceeds limit during read
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBytes)
		c.Next()
	}
}
