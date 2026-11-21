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

package conductor

import (
	"math"

	"route-server/pkg/common/logger"
)

// bestHit is how much of this request's prefix the worker already holds: Disk, the last tier.
//
// The four counters are cumulative, and Conductor produces them from a single cursor that only
// advances (Mooncake's prefix_indexer.cpp: "// The allowed sets are nested; keep one cursor for
// all four tiers", then `result.longest_match_tokens = result.disk`). So the tiers are a nested
// chain, not four disjoint measurements:
//
//	disk >= cpu_share >= cpu_local >= device
//
// and Disk is already the total, not "only what sits on disk". Adding the other three would count
// the same prefix up to four times.
//
// Only Disk is read, and only Disk is clamped. Clamping still matters: the caller's prompt length
// comes from the tokenizer while the counters come from Conductor, and the two need not agree
// (protocol version skew, different block alignment), so an unclamped value could claim the whole
// prompt is cached without either side reporting an error.
func bestHit(m RankMatch, promptTokens int64) int64 {
	return clampHit(m.Disk, promptTokens)
}

// clampHit clamps a single hit count to [0, promptTokens].
//
// Debug rather than Warn: one malformed response does not deserve a per-request warning, and if
// it becomes widespread the symptom is visible as universally high cache benefit and low load in
// ReportLoads.
func clampHit(hit, promptTokens int64) int64 {
	if hit < 0 {
		logger.Log().Debugf("conductor: got negative cache hit %d, clamped to 0", hit)
		return 0
	}
	if hit > promptTokens {
		logger.Log().Debugf("conductor: got cache hit %d exceeding prompt tokens %d, clamped",
			hit, promptTokens)
		return promptTokens
	}
	return hit
}

// ComputeRequestUnits returns how many tokens this request still needs to prefill on this worker.
//
//	request_units = max(0, promptTokens - bestHit)
//
// Every cached token counts the same whichever tier holds it: the worker does not have to
// recompute it, and "how much prefill is left" is the only question this number answers. The
// tiers are still visible in the hit data and in the decision log, but they no longer change the
// arithmetic -- a version that discounted the slower tiers existed and was removed because the
// discount answered a latency question this number is not asked.
//
// promptTokens and RankMatch are not necessarily the same unit. On the cache-aware path the prompt
// length is the tokenizer's count, so one cached token cancels one prompt token exactly; the
// legacy path carries a byte count instead, which makes the credit conservative (a fully cached
// prompt still leaves (1 - 1/k) of the load). Being conservative is the safe direction for the
// only property ordering needs: more hits never score worse.
//
// A zero RankMatch yields promptTokens unchanged, i.e. "the whole prompt has to be computed" --
// which is exactly the meaning of "no cache found", so a failed or timed-out query can pass a
// zero value instead of taking a separate code path.
func ComputeRequestUnits(m RankMatch, promptTokens int64) int64 {
	return requestUnits(promptTokens, bestHit(m, promptTokens))
}

// requestUnits is the load of one request, in tokens, given what the worker already has cached.
//
// The zero lower bound still matters even though bestHit is clamped to the prompt: a caller that
// credits more than it asked for would otherwise produce a negative load, and a negative load wins
// every comparison -- that instance would take every subsequent request and never shed the
// deficit. One extra comparison is cheaper than that failure mode.
func requestUnits(promptTokens, cache int64) int64 {
	if cache <= 0 {
		return promptTokens
	}
	if cache >= promptTokens {
		return 0
	}
	return promptTokens - cache
}

// ScoreUnits is the final candidate score: current accumulated load plus this request's load.
//
// Only these two terms. Hotspot penalties, historical hit decay and timed decay are explicitly
// out of scope for this version: adding them would make "the score" and "the value subtracted
// back on completion" different quantities, and the load would never return to zero.
func ScoreUnits(activeUnits, requestUnits int64) int64 {
	return addSat(activeUnits, requestUnits)
}

// addSat is saturating addition: overflow yields math.MaxInt64 instead of wrapping.
//
// Wrapping turns the load negative, which makes the instance win every subsequent request.
// Saturating confines a malformed count to one instance being temporarily avoided.
func addSat(a, b int64) int64 {
	if a > 0 && b > math.MaxInt64-a {
		logger.Log().Debugf("conductor: load computation saturated: %d + %d exceeds int64", a, b)
		return math.MaxInt64
	}
	return a + b
}
