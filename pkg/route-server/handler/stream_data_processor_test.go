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

package handler

import (
	"testing"

	"github.com/stretchr/testify/assert"
	commoncfg "infer-dp-scheduler/pkg/common/config"
	"infer-dp-scheduler/pkg/common/openai"
	"infer-dp-scheduler/pkg/common/utils"
	"infer-dp-scheduler/pkg/route-server/metrics"
)

func TestProcessDataCompleteDataPacket(t *testing.T) {
	dp := &StreamDataProcessor{retainLineNums: 3}
	input := "data: {\"id\":\"123\",\"content\":\"test\"}\n\n"
	expect := " {\"id\":\"123\",\"content\":\"test\"}"
	dp.ProcessStream(input)

	assert.Equal(t, expect, dp.lastLines[0])
}

func TestProcessDataDataFragment(t *testing.T) {
	dp := &StreamDataProcessor{buffer: "partial", retainLineNums: 3}
	input := " data"

	dp.ProcessStream(input)

	expected := "partial data"
	if dp.buffer != expected {
		t.Errorf("Expected currentData '%s', got '%s'", expected, dp.lastLines[0])
	}
}

func TestProcessDataMultipleDataPackets(t *testing.T) {
	dp := &StreamDataProcessor{retainLineNums: 3}
	packet1 := "data: {\"id\":\"123\"}\n\n"
	packet2 := "data: {\"id\":\"456\"}\n\n"
	packet3 := "data: {\"id\":\"678\"}\n\n"

	// Process first data packet
	dp.ProcessStream(packet1)
	assert.Equal(t, " {\"id\":\"123\"}", dp.lastLines[0])

	// Process second data packet
	dp.ProcessStream(packet2)
	assert.Equal(t, " {\"id\":\"123\"}", dp.lastLines[0])

	dp.ProcessStream(packet3)
	assert.Equal(t, " {\"id\":\"456\"}", dp.lastLines[1])
}

func TestProcessDataMixedData(t *testing.T) {
	dp := &StreamDataProcessor{retainLineNums: 3}

	// Receive fragment first
	dp.ProcessStream("hello")
	assert.Equal(t, "hello", dp.buffer)
	assert.Empty(t, dp.lastLines)

	// Then receive complete data packet
	full := "data: {\"msg\":\"world\"}\n\n"
	dp.ProcessStream(full)
	assert.Equal(t, " {\"msg\":\"world\"}", dp.lastLines[0])

	// Finally receive another fragment
	dp.ProcessStream("!")
	assert.Equal(t, " {\"msg\":\"world\"}", dp.lastLines[0])
}

func TestProcessDataEmptyString(t *testing.T) {
	dp := &StreamDataProcessor{buffer: "existing", retainLineNums: 3}

	dp.ProcessStream("")
	assert.Equal(t, "existing", dp.buffer)
	assert.Empty(t, dp.lastLines)
}

func TestProcessDataDataWithWhitespace(t *testing.T) {
	dp := &StreamDataProcessor{retainLineNums: 3}
	input := "  data: {\"value\":42}  \n\n  "

	dp.ProcessStream(input)
	assert.Equal(t, " {\"value\":42}  ", dp.lastLines[0])
}

func TestParseAndRecordTokens_NormalCase(t *testing.T) {
	metrics.InitMetrics(&commoncfg.MetricsCfg{Enable: true})
	// Normal case
	sp := &StreamDataProcessor{
		lastLines: []string{
			"first line",
			`{"usage":{"prompt_tokens":10,"completion_tokens":20,"total_tokens":30}}`,
			"another line",
		},
	}
	usage := sp.ParseAndRecordTokens()
	assert.Equal(t, int(10), usage.PromptTokens)
	assert.Equal(t, int(20), usage.CompletionTokens)
	assert.Equal(t, int(30), usage.TotalTokens)
}

func TestParseAndRecordTokens_EmptyUsageCaseWithChoice(t *testing.T) {
	metrics.InitMetrics(&commoncfg.MetricsCfg{Enable: true})
	// Normal case
	sp := &StreamDataProcessor{
		lastLines: []string{
			"first line",
			`{"model":"deepseek","choices":[{"index":0,"delta":{"content":"Hello","tool_calls":[]},"finish_reason":"abort","stop_reason":"Abort for KV loading failure"}]}`,
			"another line",
		},
	}
	usage := sp.ParseAndRecordTokens()
	var expected *openai.Usage
	assert.Equal(t, expected, usage)
}

func TestParseFinishReason_AbnormalCase(t *testing.T) {
	metrics.InitMetrics(&commoncfg.MetricsCfg{Enable: true})
	// Normal case
	sp := &StreamDataProcessor{
		lastLines: []string{
			`{"error": {"object": "error", "message": "pd link is down!", "type": "PDLinkDownError", "param": null, "code": 500}}`,
			"another line",
		},
	}
	reason := sp.ParseFinishReason()
	assert.Equal(t, "unknown", reason)
}

func TestParseFinishReason_NormalCase(t *testing.T) {
	metrics.InitMetrics(&commoncfg.MetricsCfg{Enable: true})
	sp := &StreamDataProcessor{
		lastLines: []string{
			`{"id":"test","object":"test","created":1,"model":"deepseek-r1","choices":[{"index":0,"delta":{"content":"token_31"}, "finish_reason":"length"}],"usage":{"prompt_tokens":2000,"completion_tokens":32,"total_tokens":2032,"prompt_tokens_details":null,"completion_tokens_details":null}}`,
			"second line",
			"third line",
		},
	}
	reason := sp.ParseFinishReason()
	assert.Equal(t, "length", reason)
}

func TestParseFinishReason_EmptyCase(t *testing.T) {
	metrics.InitMetrics(&commoncfg.MetricsCfg{Enable: true})
	sp := &StreamDataProcessor{
		lastLines: []string{
			"",
			"second line",
			"third line",
		},
	}
	reason := sp.ParseFinishReason()
	assert.Equal(t, "unknown", reason)
}

func TestParseAndRecordError_EmptyCase(t *testing.T) {
	metrics.InitMetrics(&commoncfg.MetricsCfg{Enable: true})
	// Normal case
	sp := &StreamDataProcessor{
		lastLines: []string{
			"another line",
			"",
		},
	}
	var expected *utils.InstanceFailureDetails
	detail, _, _, _ := sp.ParseAndRecordError()
	assert.Equal(t, expected, detail)
}

func TestParseAndRecordTokens_OnlyOneLine(t *testing.T) {
	// Only one line
	sp := &StreamDataProcessor{
		lastLines: []string{
			`{"usage":{"prompt_tokens":10,"completion_tokens":20,"total_tokens":30}}`,
		},
	}
	usage := sp.ParseAndRecordTokens()
	assert.Nil(t, usage)
}

func TestParseAndRecordTokens_EmptylastThreeLine(t *testing.T) {
	// lastLines is empty
	sp := &StreamDataProcessor{
		lastLines: []string{},
	}
	usage := sp.ParseAndRecordTokens()
	assert.Nil(t, usage)
}

func TestParseAndRecordTokens_InvalidJSON(t *testing.T) {
	// Invalid JSON
	sp := &StreamDataProcessor{
		lastLines: []string{
			"invalid json",
			"another line",
		},
	}
	usage := sp.ParseAndRecordTokens()
	assert.Nil(t, usage)
}

func TestParseAndRecordTokens_MissingFields(t *testing.T) {
	// Missing fields
	sp := &StreamDataProcessor{
		lastLines: []string{
			`{"usage":{}}`,
			"another line",
		},
	}
	usage := sp.ParseAndRecordTokens()
	assert.Nil(t, usage)
}

func TestParseAndRecordError_VllmNormalCase(t *testing.T) {
	metrics.InitMetrics(&commoncfg.MetricsCfg{Enable: true})
	// Normal case
	sp := &StreamDataProcessor{
		lastLines: []string{
			`{"error": {"object": "error", "message": "pd link is down!", "type": "PDLinkDownError", "param": null, "code": 500}}`,
			"another line",
		},
	}
	_, finishReason, _, _ := sp.ParseAndRecordError()
	assert.Equal(t, "unknown", finishReason)
}

func TestParseAndRecordError_VllmAbnormalCase(t *testing.T) {
	metrics.InitMetrics(&commoncfg.MetricsCfg{Enable: true})
	// Normal case
	sp := &StreamDataProcessor{
		lastLines: []string{
			"another line",
			`{"error": {"object": "error", "message": "pd link is down!", "type": "PDLinkDownError", "param": null, "code": 500}}`,
			"another line",
		},
	}
	_, finishReason, _, _ := sp.ParseAndRecordError()
	assert.Equal(t, "unknown", finishReason)
}

func TestParseAndRecordError_OmniNormalCase(t *testing.T) {
	metrics.InitMetrics(&commoncfg.MetricsCfg{Enable: true})
	// Normal case
	sp := &StreamDataProcessor{
		lastLines: []string{
			`{"model":"deepseek","choices":[{"index":0,"delta":{"content":"Hello","tool_calls":[]},"finish_reason":"abort","stop_reason":"Abort for KV loading failure"}]}`,
			"another line",
			"another line",
		},
	}
	_, finishReason, _, _ := sp.ParseAndRecordError()
	assert.Equal(t, "abort", finishReason)
}
