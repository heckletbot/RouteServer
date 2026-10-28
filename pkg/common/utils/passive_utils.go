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
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"

	"infer-dp-scheduler/pkg/common/anthropic"
	"infer-dp-scheduler/pkg/common/constant"

	"infer-dp-scheduler/pkg/common/errs"
	"infer-dp-scheduler/pkg/common/openai"
)

const (
	// KVTransferErrType : In Ascend-vllm - kv transfer err is reported with error type pdlinkdownerror in error body
	KVTransferErrType  = "pdlinkdownerror"
	OminiKVTransferErr = "abort for kv loading failure"
)

var HangMessageKeywords = []string{
	"KV cache load failed",
}

type InstanceFailureDetails struct {
	StatusCode      int    `json:"code"`
	IsTimeout       bool   `json:"timeout"`
	IsConnectionErr bool   `json:"connErr"`
	IsKVTransferErr bool   `json:"kvErr"`
	IsCanceledErr   bool   `json:"canceledErr"`
	IsClientErr     bool   `json:"clientErr"`
	ErrorMessage    string `json:"errorMessage,omitempty"`
}

func DetailsFromError(statusCode int, err error) *InstanceFailureDetails {
	if err == nil {
		return &InstanceFailureDetails{
			StatusCode:      statusCode,
			IsTimeout:       false,
			IsConnectionErr: false,
			IsCanceledErr:   false,
		}
	}

	if isClientErr := isClientError(err); isClientErr {
		return &InstanceFailureDetails{
			StatusCode:   statusCode,
			IsClientErr:  isClientErr,
			ErrorMessage: err.Error(),
		}
	}
	return &InstanceFailureDetails{
		StatusCode:      statusCode,
		IsTimeout:       isTimeoutError(err),
		IsConnectionErr: isConnectionError(err),
		IsCanceledErr:   isCanceledErr(err),
		ErrorMessage:    err.Error(),
	}
}

func MaErrorFromError(err error) *errs.MaError {
	if isClientError(err) {
		return errs.ClientConnectionErr.BuildArgs(err.Error())
	}
	if isTimeoutError(err) {
		return errs.GatewayTimeoutErr.BuildArgs(
			fmt.Sprintf("timeout to send requests %v", err))
	}
	if isConnectionError(err) {
		return errs.BadGatewayErr.BuildArgs(
			fmt.Sprintf("fail to send requests %v", err))
	}
	return errs.InternalErr.BuildArgs(
		fmt.Sprintf("unkown fault %v", err))
}

func DetailsFromNonStreamResponse(statusCode int, body []byte, isMessages bool) (*InstanceFailureDetails, string) {
	if body == nil || len(body) == 0 {
		return &InstanceFailureDetails{
			StatusCode:   statusCode,
			ErrorMessage: constant.UnKnownReason,
		}, constant.UnKnownReason
	}

	if isMessages {
		var anthropicErr anthropic.AnthropicError
		if err := SonicApi.Unmarshal(body, &anthropicErr); err == nil {
			if anthropicErr.Type == "error" {
				resolvedStatusCode := anthropic.ResolveAnthropicErrorCode(anthropicErr.Error.Type, anthropicErr.Error.Message)
				details := &InstanceFailureDetails{
					StatusCode:      resolvedStatusCode,
					IsTimeout:       false,
					IsConnectionErr: false,
					IsKVTransferErr: isKvTransferErr(anthropicErr.Error.Type, anthropicErr.Error.Message),
					ErrorMessage:    anthropicErr.Error.Message,
				}
				return details, constant.UnKnownReason
			}
		}
	} else {
		var newFormatError struct {
			Error *openai.VllmErrorBody `json:"error"`
		}
		if err := SonicApi.Unmarshal(body, &newFormatError); err == nil && newFormatError.Error != nil {
			vllmErrBody := newFormatError.Error
			details := &InstanceFailureDetails{
				StatusCode:      statusCode,
				IsTimeout:       false,
				IsConnectionErr: false,
				IsKVTransferErr: isKvTransferErr(vllmErrBody.Type, vllmErrBody.Message),
				ErrorMessage:    vllmErrBody.Message,
			}
			return details, constant.UnKnownReason
		}

		var vllmErrBody openai.VllmErrorBody
		if err := SonicApi.Unmarshal(body, &vllmErrBody); err == nil {
			if vllmErrBody.Type == "" {
				return tryParseErrFromOmini(statusCode, body)
			}
			details := &InstanceFailureDetails{
				StatusCode:      statusCode,
				IsTimeout:       false,
				IsConnectionErr: false,
				IsKVTransferErr: isKvTransferErr(vllmErrBody.Type, vllmErrBody.Message),
				ErrorMessage:    vllmErrBody.Message,
			}
			return details, constant.UnKnownReason
		}
	}
	if statusCode == http.StatusOK {
		return nil, constant.UnKnownReason
	}
	return &InstanceFailureDetails{
		StatusCode:      statusCode,
		IsTimeout:       false,
		IsConnectionErr: false,
		IsKVTransferErr: false,
		ErrorMessage:    string(body),
	}, constant.UnKnownReason
}

func TryParseErrFromStreamResponse(body []byte) (*InstanceFailureDetails, string) {
	if body == nil || len(body) == 0 {
		return nil, constant.UnKnownReason
	}

	var anthropicErr anthropic.AnthropicError
	if err := SonicApi.Unmarshal(body, &anthropicErr); err == nil {
		if anthropicErr.Type == "error" {
			statusCode := anthropic.ResolveAnthropicErrorCode(anthropicErr.Error.Type, anthropicErr.Error.Message)
			details := &InstanceFailureDetails{
				StatusCode:      statusCode,
				IsTimeout:       false,
				IsConnectionErr: false,
				IsKVTransferErr: isKvTransferErr(anthropicErr.Error.Type, anthropicErr.Error.Message),
				ErrorMessage:    anthropicErr.Error.Message,
			}
			return details, constant.UnKnownReason
		}
	}

	var streamErrorBody openai.StreamErrRes
	if err := SonicApi.Unmarshal(body, &streamErrorBody); err == nil {
		if streamErrorBody.ErrBody == nil {
			return tryParseErrFromOmini(http.StatusOK, body)
		}
		errBody := streamErrorBody.ErrBody
		details := &InstanceFailureDetails{
			StatusCode:      errBody.Code,
			IsTimeout:       false,
			IsConnectionErr: false,
			IsKVTransferErr: isKvTransferErr(errBody.Type, errBody.Message),
			ErrorMessage:    errBody.Message,
		}
		return details, constant.UnKnownReason
	}
	return nil, constant.UnKnownReason
}

func tryParseErrFromOmini(statusCode int, body []byte) (*InstanceFailureDetails, string) {
	var omini openai.OminiErrorBody
	if err := SonicApi.Unmarshal(body, &omini); err == nil {
		if len(omini.ChoicesBody) == 0 {
			if statusCode != http.StatusOK {
				return &InstanceFailureDetails{
					StatusCode:      statusCode,
					IsTimeout:       false,
					IsConnectionErr: false,
					IsKVTransferErr: false,
					ErrorMessage:    string(body),
				}, constant.UnKnownReason
			}
			return nil, constant.UnKnownReason
		}
		stopReason := fmt.Sprintf("%v", omini.ChoicesBody[0].StopReason)
		if strings.Contains(strings.ToLower(stopReason), OminiKVTransferErr) {
			details := &InstanceFailureDetails{
				StatusCode:      http.StatusInternalServerError, // change 200 to 500 for consistency.
				IsTimeout:       false,
				IsConnectionErr: false,
				IsKVTransferErr: true,
				ErrorMessage:    stopReason,
			}
			return details, omini.ChoicesBody[0].FinishReason
		}
	}
	if statusCode == http.StatusOK {
		return nil, constant.UnKnownReason
	}
	return &InstanceFailureDetails{
		StatusCode:      statusCode,
		IsTimeout:       false,
		IsConnectionErr: false,
		IsKVTransferErr: false,
		ErrorMessage:    string(body),
	}, constant.UnKnownReason
}

func isClientError(err error) bool {
	if err == nil {
		return false
	}

	return errors.Is(err, errs.ClientError{})
}

func isTimeoutError(err error) bool {
	if err == nil {
		return false
	}

	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}

	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}

	errorMsg := err.Error()
	timeoutKeywords := []string{
		"timeout",
		"timed out",
		"deadline exceeded",
	}

	for _, keyword := range timeoutKeywords {
		if strings.Contains(strings.ToLower(errorMsg), keyword) {
			return true
		}
	}

	return false
}

func isConnectionError(err error) bool {
	if err == nil {
		return false
	}

	var opErr *net.OpError
	if errors.As(err, &opErr) {
		// check if it's net error
		return true
	}

	errorMsg := strings.ToLower(err.Error())
	connectionErrors := []string{
		"connection refused",
		"no such host",
		"network is unreachable",
		"dial tcp",
		"tls handshake",
	}

	for _, connError := range connectionErrors {
		if strings.Contains(errorMsg, connError) {
			return true
		}
	}

	return false
}

// DetailsFromEofError builds failure details for an unexpected EOF error.
func DetailsFromEofError(err error) *InstanceFailureDetails {
	return &InstanceFailureDetails{
		StatusCode:      0,
		IsTimeout:       false,
		IsConnectionErr: true,
		IsCanceledErr:   false,
		ErrorMessage:    err.Error(),
	}
}

// TypeFromDetails maps failure detail flags to the corresponding FailureType.
func (d *InstanceFailureDetails) TypeFromDetails() errs.FailureType {
	if d == nil {
		return errs.FailureTypeUnknown
	}
	if d.IsClientErr {
		return errs.FailureTypeClient
	}
	if d.IsCanceledErr {
		return errs.FailureTypeCanceled
	}
	if d.IsTimeout {
		return errs.FailureTypeTimeout
	}
	if d.IsConnectionErr {
		return errs.FailureTypeConnection
	}
	if d.IsKVTransferErr {
		return errs.FailureTypeKVTransfer
	}
	return errs.FailureTypeEngine
}

func isKvTransferErr(errType string, message string) bool {
	if strings.Contains(strings.ToLower(errType), KVTransferErrType) {
		return true
	}
	for _, keyword := range HangMessageKeywords {
		if strings.Contains(strings.ToLower(message), strings.ToLower(keyword)) {
			return true
		}
	}
	return false
}

func isCanceledErr(err error) bool {
	return errors.Is(err, context.Canceled)
}
