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

package utils

import (
	"testing"
)

func TestHasKeyDifference(t *testing.T) {
	tests := []struct {
		name     string
		map1     map[string]int
		map2     map[string]string
		expected bool
	}{
		{
			name:     "完全相同的键",
			map1:     map[string]int{"a": 1, "b": 2},
			map2:     map[string]string{"a": "x", "b": "y"},
			expected: false,
		},
		{
			name:     "键数量不同",
			map1:     map[string]int{"a": 1, "b": 2, "c": 3},
			map2:     map[string]string{"a": "x", "b": "y"},
			expected: true,
		},
		{
			name:     "一个键不同",
			map1:     map[string]int{"a": 1, "b": 2},
			map2:     map[string]string{"a": "x", "c": "y"},
			expected: true,
		},
		{
			name:     "一个映射为空",
			map1:     map[string]int{},
			map2:     map[string]string{"a": "x"},
			expected: true,
		},
		{
			name:     "两个映射都为空",
			map1:     map[string]int{},
			map2:     map[string]string{},
			expected: false,
		},
		{
			name:     "键存在差异",
			map1:     map[string]int{"a": 1, "b": 2},
			map2:     map[string]string{"b": "y", "c": "z"},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := HasKeyDifference(tt.map1, tt.map2)
			if result != tt.expected {
				t.Errorf("HasKeyDifference(%v, %v) = %v, expected %v", tt.map1, tt.map2, result, tt.expected)
			}
		})
	}
}
