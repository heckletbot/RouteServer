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
	"fmt"
	"net/http"
	"strings"

	"infer-dp-scheduler/pkg/common/openai"
)

const ObjectTypeError = "error"
const ErrorPrefix = "RouteServer."

const ModelArtsStatusInternal = "5000"
const ModelArtsStatusEmptyReq = "4202"
const ModelArtsStatusInvalidReq = "4200"
const ModelArtsStatusInvalidJsonBody = "2787"

type ErrorBody struct {
	Message string `json:"error_msg"`
	Code    string `json:"error_code"`
}

type FailureType string

const (
	FailureTypeUnknown    FailureType = "unknown"
	FailureTypeConnection FailureType = "connection"
	FailureTypeTimeout    FailureType = "timeout"
	FailureTypeKVTransfer FailureType = "kv_transfer"
	FailureTypeEngine     FailureType = "engine"
	FailureTypeClient     FailureType = "client"
	FailureTypeCanceled   FailureType = "canceled"
)

type RSError struct {
	MaErr           *MaError
	EngineErr       *EngineError
	EngineStreamErr *EngineError
	ErrorStage      string
	FailureType     FailureType
}

func NewRSMaError(maErr *MaError, stage string, failureType ...FailureType) *RSError {
	failure := FailureTypeUnknown
	if len(failureType) != 0 {
		failure = failureType[0]
	}
	return &RSError{
		FailureType: failure,
		MaErr:       maErr,
		ErrorStage:  stage,
	}
}

func NewRSEngineError(engineErr *EngineError, stage string) *RSError {
	return &RSError{
		FailureType: FailureTypeEngine,
		EngineErr:   engineErr,
		ErrorStage:  stage,
	}
}

func NewRSStreamError(engineStreamErr *EngineError, stage string) *RSError {
	return &RSError{
		FailureType:     FailureTypeEngine,
		EngineStreamErr: engineStreamErr,
		ErrorStage:      stage,
	}
}

type EngineError struct {
	EngineErrBody []byte
	EngineStatus  int
	InstanceID    string
}

type MaError struct {
	Status    int    // http status code
	Code      string // route server internal error code
	MsgEn     string
	MsgCn     string
	Args      []interface{}
	ErrPrefix string
}

func (e RSError) Error() string {
	builder := strings.Builder{}
	builder.WriteString(fmt.Sprintf("stage [%s], ", e.ErrorStage))
	if e.MaErr != nil {
		builder.WriteString(fmt.Sprintf("type [rs], %s | ", e.MaErr.Error()))
	}
	if e.EngineErr != nil {
		builder.WriteString(fmt.Sprintf("type [backend], %s | ", e.EngineErr.Error()))
	}
	if e.EngineStreamErr != nil {
		builder.WriteString(fmt.Sprintf("type [backend_stream], %s | ", e.EngineStreamErr.Error()))
	}
	return builder.String()
}

func (e EngineError) Error() string {
	return fmt.Sprintf("[%s], code=[%v], body=%s", e.InstanceID, e.EngineStatus, string(e.EngineErrBody))
}

func (e MaError) Error() string {
	msg := fmt.Sprintf(e.MsgEn, e.Args...)
	if e.ErrPrefix == "" {
		return fmt.Sprintf("%s%s:%s", ErrorPrefix, e.Code, msg)
	} else {
		return fmt.Sprintf("%s%s:%s", e.ErrPrefix, e.Code, msg)
	}
}

func (e MaError) BuildArgs(args ...interface{}) *MaError {
	return &MaError{
		Code:   e.Code,
		Status: e.Status,
		MsgEn:  e.MsgEn,
		MsgCn:  e.MsgCn,
		Args:   args,
	}
}

func (e MaError) Format(args ...interface{}) error {
	return e.BuildArgs(args...)
}

func (e MaError) Json() ErrorBody {
	if e.ErrPrefix == "" {
		return ErrorBody{
			Code:    ErrorPrefix + e.Code,
			Message: fmt.Sprintf(e.MsgEn, e.Args...),
		}
	} else {
		return ErrorBody{
			Code:    e.ErrPrefix + e.Code,
			Message: fmt.Sprintf(e.MsgEn, e.Args...),
		}
	}

}

func (e MaError) StreamJson() openai.StreamErrRes {
	return openai.StreamErrRes{
		ErrBody: &openai.VllmErrorBody{
			Code:    e.Status,
			Message: fmt.Sprintf(e.MsgEn, e.Args...),
			Type:    http.StatusText(e.Status),
		},
	}
}

type ClientError struct {
	clientIP string
	error
}

func (e ClientError) Error() string {
	return fmt.Sprintf("failed to write to client[%v]: %v", e.clientIP, e.error)
}

func (e ClientError) Unwrap() error {
	return e.error
}

func (e ClientError) Is(target error) bool {
	return errors.Is(target, ClientError{})
}

func NewClientError(clientIP string, err error) error {
	return ClientError{
		clientIP: clientIP,
		error:    err,
	}
}
