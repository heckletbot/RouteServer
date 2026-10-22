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
	"fmt"
	"strings"
	"sync"

	"infer-dp-scheduler/pkg/common/constant"
)

const (
	ResourceNameMaxLen = 1024
)

func ValidRole(role string) bool {
	return InMap(role, constant.ValidRoleMap)
}

func InSlice[T comparable](value T, slice []T) bool {
	for _, v := range slice {
		if v == value {
			return true
		}
	}
	return false
}

func InMap[V any](value string, set map[string]V) bool {
	_, exists := set[value]
	return exists
}

func SyncMapToString(m *sync.Map) string {
	var builder strings.Builder
	builder.WriteString("{")
	first := true
	m.Range(func(key, value interface{}) bool {
		if !first {
			builder.WriteString(", ")
		}
		first = false
		// Use %v to format key and value, similar to map's default printing
		fmt.Fprintf(&builder, "%v: %v", key, value)
		return true
	})
	builder.WriteString("}")
	return builder.String()
}

func AbstractLargeBytes(data []byte) string {
	if len(data) <= 100 {
		return string(data)
	}

	front := string(data[:50])
	back := string(data[len(data)-50:])
	return front + "..." + back
}

func MapKeysStr[V any](givenMap map[string]V) string {
	builder := strings.Builder{}
	for name := range givenMap {
		builder.WriteString(name)
		builder.WriteString(",")
	}
	return builder.String()
}

func CheckResourceNameValid(name string) error {
	if name == "" {
		return fmt.Errorf("name is empty")
	}

	if len(name) > ResourceNameMaxLen {
		return fmt.Errorf("name is too long, max [%v], got %v", ResourceNameMaxLen, len(name))
	}

	for _, r := range name {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-') {
			return fmt.Errorf("name contains invalid character , only letters, numbers, dot, underscore and hyphen are allowed")
		}
	}
	return nil
}

func CheckSLOTagValid(insId string, tag string) error {
	if constant.SloSet == nil || tag == "" {
		return nil
	}
	_, exists := constant.SloSet[tag]
	if !exists {
		return fmt.Errorf("instance [%s] has invalid slo tag [%s]", insId, tag)
	}
	return nil
}

func ParseUint(in any) (uint, bool) {
	if in == nil {
		return 0, false
	}

	switch v := in.(type) {
	case uint:
		return v, true
	case int:
		if v < 0 {
			return 0, false
		} // uint can not be negative
		return uint(v), true
	case int64:
		if v < 0 {
			return 0, false
		}
		return uint(v), true
	case uint64:
		return uint(v), true
	case float64:
		if v < 0 {
			return 0, false
		}
		return uint(v), true
	default:
		return 0, false
	}
}

func Pointer[T any](v T) *T {
	return &v
}
