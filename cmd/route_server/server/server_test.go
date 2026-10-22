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

package server

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCalculateClientWriteTimeout(t *testing.T) {
	tests := []struct {
		name                 string
		hangThreshold        int64
		clientWriteTimeoutMs int64
		expected             int64
	}{
		// Case 1: client-write-timeout-ms not configured or is 0
		{
			name:                 "clientWriteTimeoutMs=0, hangThreshold>0",
			hangThreshold:        10000,
			clientWriteTimeoutMs: 0,
			expected:             5000, // hangThreshold / 2
		},
		{
			name:                 "clientWriteTimeoutMs=0, hangThreshold>0 (larger)",
			hangThreshold:        30000,
			clientWriteTimeoutMs: 0,
			expected:             15000,
		},
		{
			name:                 "clientWriteTimeoutMs=0, hangThreshold<=0",
			hangThreshold:        -1,
			clientWriteTimeoutMs: 0,
			expected:             -1, // no timeout
		},
		{
			name:                 "clientWriteTimeoutMs=0, hangThreshold=0",
			hangThreshold:        0,
			clientWriteTimeoutMs: 0,
			expected:             -1, // no timeout
		},

		// Case 2: client-write-timeout-ms < 0, no timeout
		{
			name:                 "clientWriteTimeoutMs<0, hangThreshold>0",
			hangThreshold:        10000,
			clientWriteTimeoutMs: -1,
			expected:             -1, // no timeout
		},
		{
			name:                 "clientWriteTimeoutMs<0, hangThreshold<=0",
			hangThreshold:        -1,
			clientWriteTimeoutMs: -5,
			expected:             -1, // no timeout
		},

		// Case 3: client-write-timeout-ms > 0
		{
			name:                 "clientWriteTimeoutMs>0, hangThreshold>0, clientWriteTimeoutMs>=hangThreshold",
			hangThreshold:        10000,
			clientWriteTimeoutMs: 10000,
			expected:             5000, // hangThreshold / 2
		},
		{
			name:                 "clientWriteTimeoutMs>0, hangThreshold>0, clientWriteTimeoutMs>hangThreshold",
			hangThreshold:        10000,
			clientWriteTimeoutMs: 20000,
			expected:             5000, // hangThreshold / 2
		},
		{
			name:                 "clientWriteTimeoutMs>0, hangThreshold>0, clientWriteTimeoutMs<hangThreshold",
			hangThreshold:        10000,
			clientWriteTimeoutMs: 5000,
			expected:             5000, // use configured value
		},
		{
			name:                 "clientWriteTimeoutMs>0, hangThreshold<=0",
			hangThreshold:        -1,
			clientWriteTimeoutMs: 3000,
			expected:             3000, // use configured value
		},
		{
			name:                 "clientWriteTimeoutMs>0, hangThreshold=0",
			hangThreshold:        0,
			clientWriteTimeoutMs: 3000,
			expected:             3000, // use configured value
		},
		{
			name:                 "clientWriteTimeoutMs>0, hangThreshold>0, clientWriteTimeoutMs=hangThreshold-1",
			hangThreshold:        10000,
			clientWriteTimeoutMs: 9999,
			expected:             9999, // use configured value
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := calculateClientWriteTimeout(tt.hangThreshold, tt.clientWriteTimeoutMs)
			assert.Equal(t, tt.expected, result)
		})
	}
}
