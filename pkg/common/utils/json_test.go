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
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNewJSONModifier(t *testing.T) {
	// Test normal case
	jsonStr := []byte(`{"key": "value"}`)
	modifier, err := NewJSONModifier(jsonStr)
	if err != nil {
		t.Errorf("Expected no error, got %v", err)
	}
	if modifier == nil {
		t.Errorf("Expected a non-nil JSONModifier, got nil")
	}
	if modifier.data["key"] != "value" {
		t.Errorf("Expected value for key 'key' to be 'value', got %v", modifier.data["key"])
	}

	// Test error case
	jsonStr = []byte(`{"key": "value"`)
	_, err = NewJSONModifier(jsonStr)
	if err == nil {
		t.Errorf("Expected an error, got nil")
	}
}

func TestShadowCopy(t *testing.T) {
	// Test empty map
	j := &JSONModifier{
		data: map[string]interface{}{},
	}
	copy := j.ShadowCopy()
	if !reflect.DeepEqual(j.data, copy.data) {
		t.Errorf("Expected %v, got %v", j.data, copy.data)
	}

	// Test non-empty map
	j = &JSONModifier{
		data: map[string]interface{}{
			"key1": "value1",
			"key2": "value2",
		},
	}
	copy = j.ShadowCopy()
	if !reflect.DeepEqual(j.data, copy.data) {
		t.Errorf("Expected %v, got %v", j.data, copy.data)
	}

	// Test that modifying the original map does not affect the copied map
	j.data["key1"] = "new value"
	if reflect.DeepEqual(j.data, copy.data) {
		t.Errorf("Expected %v, got %v", j.data, copy.data)
	}
}

func TestGetString(t *testing.T) {
	// Test case 1: key exists and its value is a string
	j := &JSONModifier{
		data: map[string]interface{}{
			"key1": "value1",
		},
	}
	val, exists := j.GetString("key1")
	if !exists || val != "value1" {
		t.Errorf("Expected 'value1' and true, got '%s' and %t", val, exists)
	}

	// Test case 2: key exists but its value is not a string
	j = &JSONModifier{
		data: map[string]interface{}{
			"key2": 123,
		},
	}
	val, exists = j.GetString("key2")
	if exists {
		t.Errorf("Expected false, got %t", exists)
	}

	// Test case 3: key does not exist
	j = &JSONModifier{
		data: map[string]interface{}{},
	}
	val, exists = j.GetString("key3")
	if exists {
		t.Errorf("Expected false, got %t", exists)
	}
}

func TestGetNumber(t *testing.T) {
	// Test data
	tests := []struct {
		name     string
		data     map[string]interface{}
		key      string
		expected float64
		ok       bool
	}{
		{
			"Test with existing key and float64 value",
			map[string]interface{}{"key1": 1.23},
			"key1",
			1.23,
			true,
		},
		{
			"Test with existing key and non-float64 value",
			map[string]interface{}{"key2": "value2"},
			"key2",
			0,
			false,
		},
		{
			"Test with non-existing key",
			map[string]interface{}{"key3": 3.21},
			"key4",
			0,
			false,
		},
	}

	// Iterate over test data
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			j := &JSONModifier{data: tt.data}
			result, ok := j.GetNumber(tt.key)
			if ok != tt.ok || result != tt.expected {
				t.Errorf("Want: ok: %v, result: %v; Got: ok: %v, result: %v", tt.ok, tt.expected, ok, result)
			}
		})
	}
}

func TestGetInt(t *testing.T) {
	j := &JSONModifier{
		data: map[string]interface{}{
			"intKey":   123,
			"floatKey": 123.45,
		},
	}

	if val, exists := j.GetInt("intKey"); !exists || val != 123 {
		t.Errorf("Expected 123, got %d", val)
	}

	if val, exists := j.GetInt("floatKey"); !exists || val != 123 {
		t.Errorf("Expected 123, got %d", val)
	}

	if _, exists := j.GetInt("nonExistentKey"); exists {
		t.Errorf("Expected false, got true")
	}
}

func TestGetBool(t *testing.T) {
	j := &JSONModifier{
		data: map[string]interface{}{
			"boolKey": true,
		},
	}

	if val, exists := j.GetBool("boolKey"); !exists || !val {
		t.Errorf("Expected true, got %t", val)
	}

	if _, exists := j.GetBool("nonExistentKey"); exists {
		t.Errorf("Expected false, got true")
	}
}

func TestGetObject(t *testing.T) {
	j := &JSONModifier{
		data: map[string]interface{}{
			"objectKey": map[string]interface{}{
				"nestedKey": "nestedValue",
			},
		},
	}

	if val, exists := j.GetObject("objectKey"); !exists || !reflect.DeepEqual(val, map[string]interface{}{"nestedKey": "nestedValue"}) {
		t.Errorf("Expected map[string]interface{}{\"nestedKey\": \"nestedValue\"}, got %v", val)
	}

	if _, exists := j.GetObject("nonExistentKey"); exists {
		t.Errorf("Expected false, got true")
	}
}

func TestGetArray(t *testing.T) {
	j := &JSONModifier{
		data: map[string]interface{}{
			"arrayKey": []interface{}{1, 2, 3},
		},
	}

	if val, exists := j.GetArray("arrayKey"); !exists || !reflect.DeepEqual(val, []interface{}{1, 2, 3}) {
		t.Errorf("Expected []interface{}{1, 2, 3}, got %v", val)
	}

	if _, exists := j.GetArray("nonExistentKey"); exists {
		t.Errorf("Expected false, got true")
	}
}

func TestReparseArray(t *testing.T) {
	// Test basic type conversion
	t.Run("basic types", func(t *testing.T) {
		dataArr := []interface{}{1, 2, 3}
		var v []int

		if err := ReparseArray(dataArr, &v); err != nil {
			t.Errorf("Expected nil, got %v", err)
		}

		if !reflect.DeepEqual(v, []int{1, 2, 3}) {
			t.Errorf("Expected []int{1, 2, 3}, got %v", v)
		}
	})

	// Test string type conversion
	t.Run("string types", func(t *testing.T) {
		dataArr := []interface{}{"hello", "world", "test"}
		var v []string

		if err := ReparseArray(dataArr, &v); err != nil {
			t.Errorf("Expected nil, got %v", err)
		}

		if !reflect.DeepEqual(v, []string{"hello", "world", "test"}) {
			t.Errorf("Expected []string{hello, world, test}, got %v", v)
		}
	})

	// Test mixed numeric type conversion
	t.Run("mixed numeric types", func(t *testing.T) {
		dataArr := []interface{}{1, 2.5, 3}
		var v []float64

		if err := ReparseArray(dataArr, &v); err != nil {
			t.Errorf("Expected nil, got %v", err)
		}

		expected := []float64{1, 2.5, 3}
		if !reflect.DeepEqual(v, expected) {
			t.Errorf("Expected %v, got %v", expected, v)
		}
	})

	// Test empty array
	t.Run("empty array", func(t *testing.T) {
		dataArr := []interface{}{}
		var v []int

		if err := ReparseArray(dataArr, &v); err != nil {
			t.Errorf("Expected nil, got %v", err)
		}

		if !reflect.DeepEqual(v, []int{}) {
			t.Errorf("Expected empty slice, got %v", v)
		}
	})

	// Test error case: non-pointer
	t.Run("non-pointer target", func(t *testing.T) {
		dataArr := []interface{}{1, 2, 3}
		var v []int

		if err := ReparseArray(dataArr, v); err == nil {
			t.Error("Expected error for non-pointer target")
		}
	})

	// Test error case: non-slice
	t.Run("non-slice target", func(t *testing.T) {
		dataArr := []interface{}{1, 2, 3}
		var v int

		if err := ReparseArray(dataArr, &v); err == nil {
			t.Error("Expected error for non-slice target")
		}
	})

	// Test nil pointer
	t.Run("nil pointer", func(t *testing.T) {
		dataArr := []interface{}{1, 2, 3}
		var v *[]int

		if err := ReparseArray(dataArr, v); err == nil {
			t.Error("Expected error for nil pointer")
		}
	})
}

// ChatMessage is a test message struct
type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// TestReparseArrayWithStruct tests conversion of complex structs
func TestReparseArrayWithStruct(t *testing.T) {
	// Create source data
	dataArr := []interface{}{
		map[string]interface{}{
			"role":    "user",
			"content": "Hello, world!",
		},
		map[string]interface{}{
			"role":    "assistant",
			"content": "Hi there!",
		},
	}

	// Target slice
	var messages []ChatMessage

	// Execute conversion
	if err := ReparseArray(dataArr, &messages); err != nil {
		t.Fatalf("Expected nil, got %v", err)
	}

	// Verify result
	expected := []ChatMessage{
		{Role: "user", Content: "Hello, world!"},
		{Role: "assistant", Content: "Hi there!"},
	}

	if !reflect.DeepEqual(messages, expected) {
		t.Errorf("Expected %v, got %v", expected, messages)
	}
}

func TestJSONModifier(t *testing.T) {
	j := &JSONModifier{
		data: make(map[string]interface{}),
	}

	// Test Set method
	j.Set("key1", "value1")
	if j.data["key1"] != "value1" {
		t.Errorf("Expected value1, got %v", j.data["key1"])
	}

	// Test Remove method
	j.Remove("key1")
	if _, ok := j.data["key1"]; ok {
		t.Errorf("Expected key1 to be removed")
	}

	// Test ToJSON method
	j.Set("key2", "value2")
	bytes, err := j.ToJSON()
	if err != nil {
		t.Errorf("Expected no error, got %v", err)
	}
	var data map[string]interface{}
	if err := SonicApi.Unmarshal(bytes, &data); err != nil {
		t.Errorf("Expected no error, got %v", err)
	}
	if data["key2"] != "value2" {
		t.Errorf("Expected value2, got %v", data["key2"])
	}

	// Test ToIndentedJSON method
	indented, err := j.ToIndentedJSON()
	if err != nil {
		t.Errorf("Expected no error, got %v", err)
	}
	if err := SonicApi.Unmarshal([]byte(indented), &data); err != nil {
		t.Errorf("Expected no error, got %v", err)
	}
	if data["key2"] != "value2" {
		t.Errorf("Expected value2, got %v", data["key2"])
	}
}

func TestJSONModifier_GetBool(t *testing.T) {
	type fields struct {
		body        string
		key         string
		expectTrue  bool
		expectExist bool
	}
	tests := []struct {
		name   string
		fields fields
	}{
		{
			name: "Str bool T",
			fields: fields{
				body:        "{\"strBoolKey\":\"T\"}",
				key:         "strBoolKey",
				expectTrue:  true,
				expectExist: true,
			},
		},
		{
			name: "Str bool True",
			fields: fields{
				body:        "{\"strBoolKey\":\"True\"}",
				key:         "strBoolKey",
				expectTrue:  true,
				expectExist: true,
			},
		},
		{
			name: "Str bool true",
			fields: fields{
				body:        "{\"strBoolKey\":\"true\"}",
				key:         "strBoolKey",
				expectTrue:  true,
				expectExist: true,
			},
		},
		{
			name: "Str bool 1",
			fields: fields{
				body:        "{\"strBoolKey\":\"1\"}",
				key:         "strBoolKey",
				expectTrue:  true,
				expectExist: true,
			},
		},
		{
			name: "Str bool when is number 1",
			fields: fields{
				body:        "{\"strBoolKey\":1}",
				key:         "strBoolKey",
				expectTrue:  true,
				expectExist: true,
			},
		},
		{
			name: "Str bool invalid when is number float",
			fields: fields{
				body:        "{\"strBoolKey\":3.1}",
				key:         "strBoolKey",
				expectExist: false,
			},
		},
		{
			name: "Str bool invalid when is number 2",
			fields: fields{
				body:        "{\"strBoolKey\":2}",
				key:         "strBoolKey",
				expectExist: false,
			},
		},
		{
			name: "Str bool F",
			fields: fields{
				body:        "{\"strBoolKey\":\"F\"}",
				key:         "strBoolKey",
				expectTrue:  false,
				expectExist: true,
			},
		},
		{
			name: "Str bool False",
			fields: fields{
				body:        "{\"strBoolKey\":\"False\"}",
				key:         "strBoolKey",
				expectTrue:  false,
				expectExist: true,
			},
		},
		{
			name: "Str bool false",
			fields: fields{
				body:        "{\"strBoolKey\":\"false\"}",
				key:         "strBoolKey",
				expectTrue:  false,
				expectExist: true,
			},
		},
		{
			name: "Str bool 0",
			fields: fields{
				body:        "{\"strBoolKey\":\"0\"}",
				key:         "strBoolKey",
				expectTrue:  false,
				expectExist: true,
			},
		},
		{
			name: "Str bool when is number 0",
			fields: fields{
				body:        "{\"strBoolKey\":0}",
				key:         "strBoolKey",
				expectTrue:  false,
				expectExist: true,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			j, err := NewJSONModifier([]byte(tt.fields.body))
			if err != nil {
				t.Errorf("Expected no error, got %v", err)
			}
			val, exist := j.GetBool(tt.fields.key)
			if exist != tt.fields.expectExist {
				t.Errorf("expect exist %v, got %v", tt.fields.expectExist, exist)
			}
			if exist {
				if val != tt.fields.expectTrue {
					t.Errorf("expect val to be %v, got %v", tt.fields.expectTrue, val)
				}
			}
		})
	}
}

func TestJsonModifier_GetVeryLongInt(t *testing.T) {
	type fields struct {
		in string
	}

	tests := []struct {
		name   string
		fields fields
	}{
		{
			name: "very long int at first level",
			fields: fields{
				in: "{\"a\":{\"c\":[2],\"d\":[3885624150,546640030883315728]},\"b\":[11111111111111111111111111111111,22222222222222222222]}",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			j, err := NewJSONModifier([]byte(tt.fields.in))
			if err != nil {
				t.Errorf("Expected no error, got %v", err)
			}
			actualOut, err := j.ToJSON()
			if err != nil {
				t.Errorf("Expected no error on to json, got %v", err)
			}
			assert.Equal(t, tt.fields.in, string(actualOut))
		})
	}
}

func TestJSONModifier_GetInt(t *testing.T) {
	type fields struct {
		body        string
		key         string
		expectVal   int
		expectExist bool
	}
	tests := []struct {
		name   string
		fields fields
	}{
		{
			name: "str num invalid val",
			fields: fields{
				body:        "{\"strIntKey\":\"false\"}",
				key:         "strIntKey",
				expectVal:   0,
				expectExist: false,
			},
		},
		{
			name: "str num 1",
			fields: fields{
				body:        "{\"strIntKey\":\"1\"}",
				key:         "strIntKey",
				expectVal:   1,
				expectExist: true,
			},
		},
		{
			name: "num 1",
			fields: fields{
				body:        "{\"strIntKey\":100}",
				key:         "strIntKey",
				expectVal:   100,
				expectExist: true,
			},
		},
		{
			name: "num 1 from float 1.3",
			fields: fields{
				body:        "{\"strIntKey\":1.3}",
				key:         "strIntKey",
				expectVal:   1,
				expectExist: true,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			j, err := NewJSONModifier([]byte(tt.fields.body))
			if err != nil {
				t.Errorf("Expected no error, got %v", err)
			}
			val, exist := j.GetInt(tt.fields.key)
			if exist != tt.fields.expectExist {
				t.Errorf("expect exist %v, got %v", tt.fields.expectExist, exist)
			}
			if exist {
				if val != tt.fields.expectVal {
					t.Errorf("expect val to be %v, got %v", tt.fields.expectVal, val)
				}
			}
		})
	}
}
