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

package utils

import (
	"route-server/pkg/common/constant"
	"strings"
	"testing"
)

func TestValidRole(t *testing.T) {
	tests := []struct {
		input    string
		expected bool
	}{
		{
			input:    constant.Prefill,
			expected: true,
		},
		{
			input:    constant.Decode,
			expected: true,
		},
		{
			input:    "other",
			expected: false,
		},
	}

	for _, test := range tests {
		result := ValidRole(test.input)
		if result != test.expected {
			t.Errorf("ValidRole(%q) = %v; expected %v", test.input, result, test.expected)
		}
	}
}

func TestCheckResourceNameValid(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		expectError bool
		errorMsg    string
	}{
		// Valid cases
		{
			name:        "Valid simple name",
			input:       "resource-name",
			expectError: false,
		},
		{
			name:        "Valid with dots and underscores",
			input:       "my.resource_123",
			expectError: false,
		},
		{
			name:        "Valid single character",
			input:       "a",
			expectError: false,
		},

		// Invalid cases
		{
			name:        "Empty string",
			input:       "",
			expectError: true,
			errorMsg:    "name is empty",
		},
		{
			name:        "Contains space",
			input:       "resource name",
			expectError: true,
			errorMsg:    "name contains invalid character",
		},
		{
			name:        "Contains special chars",
			input:       "resource@name",
			expectError: true,
			errorMsg:    "name contains invalid character",
		},
		{
			name:        "Too long",
			input:       strings.Repeat("a", 1025),
			expectError: true,
			errorMsg:    "name is too long",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := CheckResourceNameValid(test.input)
			if test.expectError {
				if err == nil {
					t.Errorf("CheckResourceNameValid(%q) expected error but got none", test.input)
				} else if test.errorMsg != "" && !strings.Contains(err.Error(), test.errorMsg) {
					t.Errorf("CheckResourceNameValid(%q) error message mismatch: got %q, want contains %q",
						test.input, err.Error(), test.errorMsg)
				}
			} else {
				if err != nil {
					t.Errorf("CheckResourceNameValid(%q) unexpected error: %v", test.input, err)
				}
			}
		})
	}
}
