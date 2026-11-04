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

package handler

import (
	"bytes"
	"strings"

	"route-server/pkg/common/constant"
	"route-server/pkg/common/openai"
	"route-server/pkg/common/utils"
	"route-server/pkg/route-server/metrics"
)

type StreamDataProcessor struct {
	buffer         string
	retainLineNums int
	lastLines      []string
}

// ProcessStream processes new streaming data chunks, returns complete lines and updates the last two lines
func (sp *StreamDataProcessor) ProcessStream(chunk string) {
	sp.buffer += chunk

	for {
		startIdx := strings.Index(sp.buffer, "data:")
		if startIdx < 0 {
			// no new line found
			break
		}

		dataPart := sp.buffer[startIdx:]
		endIdx := strings.Index(dataPart, "\n\n")
		if endIdx < 0 {
			break
		}

		fullLine := dataPart[:endIdx+2]

		content := strings.TrimPrefix(fullLine, "data:")
		content = strings.TrimSuffix(content, "\n\n")

		sp.lastLines = append(sp.lastLines, content)
		if len(sp.lastLines) > sp.retainLineNums {
			sp.lastLines = sp.lastLines[1:] // Remove the oldest line
		}

		sp.buffer = sp.buffer[startIdx+len(fullLine):]
	}
}

func (sp *StreamDataProcessor) ParseAndRecordTokens() *openai.Usage {
	if len(sp.lastLines) < 2 { // it means that this is an error response
		return nil
	}
	//lastLines[0] for choice, lastLines[1] for usage, lastLines[2] for done in stream mode
	buf := []byte(sp.lastLines[len(sp.lastLines)-2])
	body, err := ParseResponseBody(buf)
	if err != nil {
		return nil
	}
	if body.Usage == nil || body.Usage.PromptTokens == 0 {
		return nil
	}
	metrics.RecordPromptTokenCount(body.Usage.PromptTokens)
	metrics.RecordOutputTotalTokenCount(body.Usage.CompletionTokens)
	metrics.RecordTotalOutputToken(body.Usage.CompletionTokens)
	return body.Usage
}

func (sp *StreamDataProcessor) GetRawResBody() []byte {
	if len(sp.lastLines) < 2 { // it means that this is an error response
		return nil
	}
	return []byte(sp.lastLines[len(sp.lastLines)-2])
}

func (sp *StreamDataProcessor) ParseFinishReason() string {
	if len(sp.lastLines) < 3 {
		return constant.UnKnownReason
	}
	// Try lastLines[0] first
	body, err := ParseResponseBody([]byte(sp.lastLines[0]))
	if err == nil && len(body.Choices) > 0 {
		return body.Choices[0].FinishReason
	}
	// Fallback to lastLines[len-2]
	body, err = ParseResponseBody([]byte(sp.lastLines[len(sp.lastLines)-2]))
	if err == nil && len(body.Choices) > 0 {
		return body.Choices[0].FinishReason
	}
	return constant.UnKnownReason
}

func (sp *StreamDataProcessor) ParseAndRecordError() (*utils.InstanceFailureDetails, string, []byte, bool) {
	lineLength := len(sp.lastLines)
	hasDoneWritten := false
	if lineLength > 0 {
		lastLine := sp.lastLines[lineLength-1]
		if strings.Contains(lastLine, "[DONE]") {
			hasDoneWritten = true
		}
	}
	if lineLength < 2 {
		return nil, constant.UnKnownReason, nil, hasDoneWritten
	}
	rawBytes := []byte(sp.lastLines[lineLength-2])
	details, finishReason := utils.TryParseErrFromStreamResponse(rawBytes)
	if details == nil && lineLength >= 3 {
		rawBytes = []byte(sp.lastLines[lineLength-3])
		details, finishReason = utils.TryParseErrFromStreamResponse(rawBytes)
	}

	return details, finishReason, rawBytes, hasDoneWritten
}

func SkipOriginalToken(passTokenTime int, isWrite *bool, endOffset int, buf *[]byte) (remainPass int, newEndOffset int) {
	// Skip initial tokens
	for ; passTokenTime > 0; passTokenTime-- {
		pos := bytes.IndexByte((*buf)[:endOffset], '\n')
		if pos < 0 {
			*isWrite = false
			break
		}
		tmp := (*buf)[pos+2:]
		copy(*buf, tmp)
		endOffset -= pos + 2
	}
	remainPass = passTokenTime
	newEndOffset = endOffset
	return
}
