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

package constant

var (
	SloSet map[string]interface{}
)

const (
	// DefaultNonReferenceAPIRPM is the default requests per minute for non-reference APIs
	DefaultNonReferenceAPIRPM = 60
	// DefaultRateLimitCleanupMinutes is the default cleanup interval in minutes
	DefaultRateLimitCleanupMinutes = 5
)

// DefaultExemptPaths contains the list of paths exempt from non-reference API rate limiting
var DefaultExemptPaths = []string{
	"/v1/chat/completions",
	"/v1/completions",
	"/v1/metaserver",
}
