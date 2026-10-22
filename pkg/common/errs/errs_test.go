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

package errs

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestError_Basic(t *testing.T) {
	e := MaError{
		Code:  "123",
		MsgEn: "Test error",
		Args:  nil,
	}
	expected := "RouteServer.123:Test error"
	assert.Equal(t, expected, e.Error())
}

func TestError_CustomPrefix(t *testing.T) {
	e := MaError{
		Code:      "123",
		MsgEn:     "Test error",
		ErrPrefix: "Custom.",
	}
	expected := "Custom.123:Test error"
	assert.Equal(t, expected, e.Error())
}

func TestError_FormatArgs(t *testing.T) {
	e := MaError{
		Code:  "123",
		MsgEn: "Test error: %s",
		Args:  []interface{}{"test"},
	}
	expected := "RouteServer.123:Test error: test"
	assert.Equal(t, expected, e.Error())
}

func TestError_NoCode(t *testing.T) {
	e := MaError{
		MsgEn: "Test error",
		Args:  nil,
	}
	expected := "RouteServer.:Test error"
	assert.Equal(t, expected, e.Error())
}

func TestJson_Basic(t *testing.T) {
	e := MaError{
		Code:  "123",
		MsgEn: "Test error",
		Args:  nil,
	}
	expected := ErrorBody{
		Code:    "RouteServer.123",
		Message: "Test error",
	}
	assert.Equal(t, expected, e.Json())
}

func TestJson_CustomPrefix(t *testing.T) {
	e := MaError{
		Code:      "123",
		MsgEn:     "Test error",
		ErrPrefix: "Custom.",
	}
	expected := ErrorBody{
		Code:    "Custom.123",
		Message: "Test error",
	}
	assert.Equal(t, expected, e.Json())
}

func TestJson_FormatArgs(t *testing.T) {
	e := MaError{
		Code:  "123",
		MsgEn: "Test error: %s",
		Args:  []interface{}{"test"},
	}
	expected := ErrorBody{
		Code:    "RouteServer.123",
		Message: "Test error: test",
	}
	assert.Equal(t, expected, e.Json())
}

func TestJson_NoCode(t *testing.T) {
	e := MaError{
		MsgEn: "Test error",
		Args:  nil,
	}
	expected := ErrorBody{
		Code:    "RouteServer.",
		Message: "Test error",
	}
	assert.Equal(t, expected, e.Json())
}

func TestError_FormatArgs_MultipleArgs(t *testing.T) {
	e := MaError{
		Code:  "123",
		MsgEn: "Test error: %s, %d, %f",
		Args:  []interface{}{"test", 123, 45.67},
	}
	expected := "RouteServer.123:Test error: test, 123, 45.670000"
	assert.Equal(t, expected, e.Error())
}

func TestError_FormatArgs_NoArgs(t *testing.T) {
	e := MaError{
		Code:  "123",
		MsgEn: "Test error:",
		Args:  nil,
	}
	expected := "RouteServer.123:Test error:"
	assert.Equal(t, expected, e.Error())
}

func TestError_FormatArgs_ExcessArgs(t *testing.T) {
	e := MaError{
		Code:  "123",
		MsgEn: "Test error: %s",
		Args:  []interface{}{"test", 123},
	}
	expected := "RouteServer.123:Test error: test%!(EXTRA int=123)"
	assert.Equal(t, expected, e.Error())
}

func TestError_FormatArgs_NotEnoughArgs(t *testing.T) {
	e := MaError{
		Code:  "123",
		MsgEn: "Test error: %s, %d",
		Args:  []interface{}{"test"},
	}
	expected := "RouteServer.123:Test error: test, %!d(MISSING)"
	assert.Equal(t, expected, e.Error())
}

func TestError_FormatArgs_ZeroArgs(t *testing.T) {
	e := MaError{
		Code:  "123",
		MsgEn: "Test error",
		Args:  nil,
	}
	expected := "RouteServer.123:Test error"
	assert.Equal(t, expected, e.Error())
}

// Test more formatting scenarios for Json() method

func TestJson_FormatArgs_MultipleArgs(t *testing.T) {
	e := MaError{
		Code:  "123",
		MsgEn: "Test error: %s, %d, %f",
		Args:  []interface{}{"test", 123, 45.67},
	}
	expected := ErrorBody{
		Code:    "RouteServer.123",
		Message: "Test error: test, 123, 45.670000",
	}
	assert.Equal(t, expected, e.Json())
}

func TestJson_FormatArgs_NoArgs(t *testing.T) {
	e := MaError{
		Code:  "123",
		MsgEn: "Test error: ",
		Args:  nil,
	}
	expected := ErrorBody{
		Code:    "RouteServer.123",
		Message: "Test error: ",
	}
	assert.Equal(t, expected, e.Json())
}

func TestJson_FormatArgs_ExcessArgs(t *testing.T) {
	e := MaError{
		Code:  "123",
		MsgEn: "Test error: %s",
	}
	expected := ErrorBody{
		Code:    "RouteServer.123",
		Message: "Test error: test",
	}
	assert.Equal(t, expected, e.BuildArgs("test").Json())
}

func TestJson_FormatArgs_NotEnoughArgs(t *testing.T) {
	e := MaError{
		Code:  "123",
		MsgEn: "Test error: %s %d",
	}

	expected := ErrorBody{
		Code:    "RouteServer.123",
		Message: "Test error: test 123",
	}
	assert.Equal(t, expected, e.BuildArgs("test", 123).Json())
}

func TestJson_FormatArgs_ZeroArgs(t *testing.T) {
	e := MaError{
		Code:  "123",
		MsgEn: "Test error",
		Args:  nil,
	}
	expected := ErrorBody{
		Code:    "RouteServer.123",
		Message: "Test error",
	}
	assert.Equal(t, expected, e.Json())
}

func TestClientError_Error(t *testing.T) {
	innerErr := errors.New("inner error")
	clientErr := NewClientError("192.168.1.1", innerErr)

	assert.Contains(t, clientErr.Error(), "192.168.1.1")
	assert.Contains(t, clientErr.Error(), "inner error")
}

func TestClientError_Is(t *testing.T) {
	innerErr := errors.New("inner error")
	clientErr := NewClientError("192.168.1.1", innerErr)

	// Test using errors.Is to check if it's a ClientError
	isClientErr := errors.Is(clientErr, ClientError{})
	assert.True(t, isClientErr, "errors.Is should return true for ClientError")
}

func TestClientError_As(t *testing.T) {
	innerErr := errors.New("inner error")
	clientErr := NewClientError("192.168.1.1", innerErr)

	// Test using errors.As to convert to ClientError
	var ce ClientError
	ok := errors.As(clientErr, &ce)
	assert.True(t, ok, "errors.As should return true for ClientError")
	assert.Equal(t, "192.168.1.1", ce.clientIP)
}

func TestClientError_Unwrap(t *testing.T) {
	innerErr := errors.New("inner error")
	clientErr := NewClientError("192.168.1.1", innerErr)

	// Test that Unwrap returns the original error
	unwrapped := errors.Unwrap(clientErr)
	assert.Equal(t, innerErr, unwrapped)
}
