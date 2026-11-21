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
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"reflect"
	"sync"
	"time"

	"github.com/ugorji/go/codec"

	"route-server/pkg/common/logger"
)

// HTTP transport parameters. The Conductor address is single, but /query runs once per
// request, so it needs more headroom than the registration client, which only sees low-rate
// cluster events.
const (
	queryMaxIdleConns        = 32
	queryMaxIdleConnsPerHost = 32
	queryIdleConnTimeout     = 90 * time.Second
	queryDialTimeout         = 5 * time.Second
	// DefaultQueryTimeout bounds one /query call. Short on purpose: the call sits on the
	// request path ahead of prefill scheduling, so a slow Conductor must not add latency.
	DefaultQueryTimeout = 100 * time.Millisecond
	// DefaultMaxQueryResponseBytes caps the decoded response size.
	DefaultMaxQueryResponseBytes = 4 << 20
)

// /query protocol field names. The tier names follow the Mooncake wire format: "device" is the
// inference engine's device memory, then cpu_local, cpu_share and disk.
const (
	fieldInstances   = "instances"
	fieldRankMatches = "rank_matches"
	// rankZero is only used by tests and by the compatibility path: instances are keyed by their
	// own dp rank, so in production the instance-level totals are what get read.
	rankZero      = "0"
	fieldDevice   = "device"
	fieldCPULocal = "cpu_local"
	fieldCPUShare = "cpu_share"
	fieldDisk     = "disk"

	fieldModel     = "model"
	fieldBlockSize = "block_size"
	fieldTokenIDs  = "token_ids"
	fieldTenantID  = "tenant_id"
	fieldLoraName  = "lora_name"
	fieldCacheSalt = "cache_salt"

	pathQuery = "/query"

	// tenantID and loraName are fixed in this version: no multi-tenancy, no LoRA support.
	tenantID = DefaultTenantID
	loraName = ""
)

// RankMatch is one instance's cumulative prefix-cache hit, aggregated over its dp ranks.
//
// The four values are cumulative rather than per-tier: Device <= CPULocal <= CPUShare <= Disk.
// Each tier includes every faster tier, so Device counts the same tokens as the head of CPULocal.
// The conversion to per-tier exclusive hits happens in ComputeRequestUnits -- weighting the
// cumulative values directly would count the same prefix up to four times.
//
// The ordering constraint is checked by the parser. It is not just protocol validation: a Disk
// value above the prompt length would drive requestUnits negative, and a decreasing value would
// make some tier's exclusive hit count negative.
type RankMatch struct {
	// Device is what the inference engine holds in device memory (GPU).
	Device int64
	// CPULocal extends Device with the local CPU tier.
	CPULocal int64
	// CPUShare extends CPULocal with the shared CPU tier.
	CPUShare int64
	// Disk extends CPUShare with the disk tier.
	Disk int64
}

// HitQuerier queries how much of a token prefix is already cached on each instance.
//
// Safe for concurrent use; one instance is shared process-wide.
type HitQuerier interface {
	// Query returns instance_id -> cumulative hit. A prefix shorter than one block returns
	// (nil, nil) without issuing a request, because Conductor cannot match a partial block.
	Query(ctx context.Context, model string, tokenIDs []int32, cacheSalt *string) (map[string]RankMatch, error)
}

// HitQuerierConfig carries the tunables of the query client.
type HitQuerierConfig struct {
	// Timeout bounds one call. Zero uses DefaultQueryTimeout.
	Timeout time.Duration
	// MaxResponseBytes caps the response body. Zero uses DefaultMaxQueryResponseBytes.
	MaxResponseBytes int64
}

type hitQuerier struct {
	baseURL          string
	timeout          time.Duration
	maxResponseBytes int64
	httpClient       *http.Client
	handle           *codec.MsgpackHandle
	// blockSize is the prefix granularity, which must match what was reported at registration.
	blockSize int
}

// NewHitQuerier builds a query client.
//
// baseURL comes from NormalizeConfig (trailing slash already trimmed, path prefix preserved).
// blockSize must be the same value used for registration: the two sides disagreeing shows up
// as "registration and query both succeed but every hit is 0", with no error on either side.
func NewHitQuerier(cfg HitQuerierConfig, baseURL string, blockSize int) (HitQuerier, error) {
	if blockSize <= 0 {
		return nil, fmt.Errorf("conductor block size must be positive, got %d", blockSize)
	}

	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = DefaultQueryTimeout
	}
	maxBytes := cfg.MaxResponseBytes
	if maxBytes <= 0 {
		maxBytes = DefaultMaxQueryResponseBytes
	}

	transport := &http.Transport{
		DialContext:         (&net.Dialer{Timeout: queryDialTimeout}).DialContext,
		MaxIdleConns:        queryMaxIdleConns,
		MaxIdleConnsPerHost: queryMaxIdleConnsPerHost,
		IdleConnTimeout:     queryIdleConnTimeout,
	}
	return &hitQuerier{
		baseURL:          baseURL,
		timeout:          timeout,
		maxResponseBytes: maxBytes,
		httpClient:       &http.Client{Transport: transport},
		handle:           newQueryMsgpackHandle(),
		blockSize:        blockSize,
	}, nil
}

// newQueryMsgpackHandle returns the codec handle used for /query.
//
// One difference from the registration handle matters: WriteExt must be true. Without it a
// []byte is encoded as a msgpack str (0xa4...) instead of bin (0xc4 0x04...). Go cannot tell
// the two apart on the wire, only the bytes can. Registration does not need it because its
// request has no []byte field.
//
// MapType and RawToString follow the registration conclusion: without RawToString a str
// decodes to []uint8, and without MapType a nested map decodes to map[interface{}]interface{},
// both of which break response parsing. Do not add PositiveIntUnsigned -- it changes the wire
// format of every integer.
func newQueryMsgpackHandle() *codec.MsgpackHandle {
	handle := &codec.MsgpackHandle{}
	handle.MapType = reflect.TypeOf(map[string]interface{}(nil))
	handle.RawToString = true
	handle.WriteExt = true
	return handle
}

// Query asks Conductor how much of the prefix each registered instance already holds.
//
// cacheSalt nil means "no salt" and the field is omitted (nil, "" and absent are equivalent on
// the Conductor side). It must come from the client request, not from the renderer response.
//
// The returned map is not intersected with the legal candidates: that is the caller's job, as
// only the caller holds the candidate set and blacklist.
//
// No internal retry: query is a single logical dependency and retrying amplifies traffic when
// Conductor is congested. Callers treat an error as zero hits.
func (c *hitQuerier) Query(ctx context.Context, model string, tokenIDs []int32,
	cacheSalt *string) (map[string]RankMatch, error) {
	// A partial block can never match, so skip the call entirely.
	completeTokens := len(tokenIDs) / c.blockSize * c.blockSize
	if completeTokens == 0 {
		return nil, nil
	}

	payload := map[string]interface{}{
		fieldModel:     model,
		fieldBlockSize: c.blockSize,
		fieldTokenIDs:  encodeTokenIDs(tokenIDs),
		fieldTenantID:  tenantID,
		fieldLoraName:  loraName,
	}
	if cacheSalt != nil {
		payload[fieldCacheSalt] = *cacheSalt
	}

	var body []byte
	if err := codec.NewEncoderBytes(&body, c.handle).Encode(payload); err != nil {
		return nil, fmt.Errorf("encode query request to msgpack failed: %w", err)
	}

	url := c.baseURL + pathQuery
	reqCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build query request failed: %w", err)
	}
	req.Header.Set("Content-Type", ContentTypeMsgpack)
	req.Header.Set("Accept", ContentTypeMsgpack)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		// Wrap with %w so ClassOf can recognise context.Canceled through errors.Is.
		return nil, fmt.Errorf("post %s failed: %w", url, err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, c.maxResponseBytes))
		if closeErr := resp.Body.Close(); closeErr != nil {
			logger.Log().Debugf("conductor query: close response body failed: %v", closeErr)
		}
	}()

	decoded, err := c.readResponse(resp, url)
	if err != nil {
		return nil, err
	}
	return parseInstances(decoded, int64(completeTokens))
}

// encodeTokenIDs packs the IDs as a little-endian int32 byte string. Conductor expects msgpack
// bin: four fixed bytes per token, which is far smaller than an integer array for long prompts
// and lets the server skip element-wise parsing.
func encodeTokenIDs(tokenIDs []int32) []byte {
	out := make([]byte, len(tokenIDs)*4)
	for i, id := range tokenIDs {
		binary.LittleEndian.PutUint32(out[i*4:], uint32(id))
	}
	return out
}

// readResponse validates the status and content type, then decodes the msgpack map.
//
// The status check comes before the content type for the same reason as in the registration
// client: a gateway 500 with an HTML error page is a transient status problem, not a protocol
// problem, and reversing the order would report "Conductor is down" as a protocol error.
func (c *hitQuerier) readResponse(resp *http.Response, url string) (map[string]interface{}, error) {
	// Read one extra byte to detect truncation.
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, c.maxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read query response failed: %w", err)
	}
	if int64(len(respBody)) > c.maxResponseBytes {
		return nil, fmt.Errorf("query response from %s exceeds %d bytes", url, c.maxResponseBytes)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, c.newStatusError(resp, respBody)
	}

	raw := resp.Header.Get("Content-Type")
	mediaType, _, err := mime.ParseMediaType(raw)
	if err != nil {
		return nil, fmt.Errorf("parse query response Content-Type %q failed: %w", raw, err)
	}
	// No JSON fallback: Conductor is msgpack-only, and accepting JSON would hide a version
	// mismatch between RouteServer and Conductor.
	if mediaType != ContentTypeMsgpack {
		return nil, fmt.Errorf("query response Content-Type is %q, want %s", mediaType, ContentTypeMsgpack)
	}

	var decoded map[string]interface{}
	if err := codec.NewDecoderBytes(respBody, c.handle).Decode(&decoded); err != nil {
		return nil, fmt.Errorf("decode query response from %s failed: %w", url, err)
	}
	return decoded, nil
}

// newStatusError converts a non-200 response into an APIError, reusing the registration error
// type so ClassOf and the circuit breaker can treat both dependencies uniformly.
func (c *hitQuerier) newStatusError(resp *http.Response, body []byte) error {
	apiErr := &APIError{
		StatusCode:  resp.StatusCode,
		BodySummary: summarizeBody(body),
	}

	var decoded map[string]interface{}
	if err := codec.NewDecoderBytes(body, c.handle).Decode(&decoded); err == nil {
		if reason, exists := decoded["reason"]; exists {
			if reasonStr, ok := reason.(string); ok {
				apiErr.Reason = reasonStr
			}
		}
		if field, exists := decoded["field"]; exists {
			if fieldStr, ok := field.(string); ok {
				apiErr.Field = fieldStr
			}
		}
		if msg, exists := decoded["error"]; exists {
			if msgStr, ok := msg.(string); ok {
				apiErr.Message = msgStr
			}
		}
	}
	if apiErr.Message == "" {
		apiErr.Message = fmt.Sprintf("query returned status %d", resp.StatusCode)
	}
	apiErr.Class = ClassifyStatus(resp.StatusCode, apiErr.Reason)
	return apiErr
}

// parseInstances extracts each instance's cumulative hit.
//
// Conductor publishes two views per instance: rank_matches keyed by the instance's own dp ranks,
// and the instance-level device/cpu_local/cpu_share/disk that it already aggregates across them
// (max over ranks in prefix_indexer.cpp, both packed by event_manager.cpp). The scoring formula
// wants the instance-level view, so that is what is read.
//
// Reading rank_matches["0"] instead -- what this code used to do -- only works for an instance
// whose single dp rank is literally 0. Every engine here registers with its own dp_rank, so
// prefill-1 publishes rank "1": all but one instance per role were dropped and lost their cache
// credit, which is why every non-zero hit was attributed to prefill-0 / decode-0.
//
// Kept checks: the four tiers exist and 0 <= device <= cpu_local <= cpu_share <= disk <=
// completeTokens. They are not protocol validation but guards against garbage entering the
// scoring formula -- a disk value above the prompt drives requestUnits negative, and gpu > cpu
// makes "CPU-only hits" negative.
//
// An instance that fails a check is dropped on its own, together with a log line; the remaining
// instances are still returned. One instance publishing malformed data must not zero out the
// cache credit of the whole cluster, which is what returning an error here would do: the caller
// treats a failed query as "nothing is cached anywhere".
//
// Deliberately not checked: rank_matches agreeing with the instance-level totals, longest_matched
// == disk, and block-size multiples. Those are internal consistency assertions about Conductor's
// own output; only the instance-level totals feed the computation.
func parseInstances(decoded map[string]interface{}, completeTokens int64) (map[string]RankMatch, error) {
	rawInstances, exists := decoded[fieldInstances]
	if !exists {
		return nil, fmt.Errorf("query response has no %q field", fieldInstances)
	}
	instances, ok := rawInstances.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("query response %q has unexpected type %T", fieldInstances, rawInstances)
	}

	out := make(map[string]RankMatch, len(instances))
	for id, rawEntry := range instances {
		entry, ok := rawEntry.(map[string]interface{})
		if !ok {
			warnSkippedInstance(id, fmt.Errorf("entry has unexpected type %T", rawEntry))
			continue
		}
		match, err := parseInstanceHit(id, entry, completeTokens)
		if err != nil {
			warnSkippedInstance(id, err)
			continue
		}
		out[id] = match
	}
	return out, nil
}

// warnedBadInstances remembers which instances were already reported, so a permanently
// malformed instance does not emit one warning per request. Bounded by the instance count.
var warnedBadInstances sync.Map

// warnSkippedInstance reports a skipped instance once at Warn and at Debug afterwards.
func warnSkippedInstance(id string, err error) {
	if _, loaded := warnedBadInstances.LoadOrStore(id, struct{}{}); loaded {
		logger.Log().Debugf("query: skipping instance %s for prefix hits: %v", id, err)
		return
	}
	logger.Log().Warnf("query: skipping instance %s for prefix hits, its data is unusable and "+
		"only this instance loses cache credit: %v", id, err)
}

// parseInstanceHit reads one instance's cumulative hit, preferring the instance-level totals.
func parseInstanceHit(id string, entry map[string]interface{}, completeTokens int64) (RankMatch, error) {
	match, found, err := readTiers(id, entry)
	if err != nil {
		return RankMatch{}, err
	}
	switch found {
	case 4:
		if err := checkTierOrder(id, match, completeTokens); err != nil {
			return RankMatch{}, err
		}
		return match, nil
	case 0:
		// Compatibility path: a Conductor that publishes only rank_matches.
		return parseSingleRankMatch(id, entry, completeTokens)
	default:
		return RankMatch{}, fmt.Errorf(
			"instance %s publishes only %d of the 4 instance-level tiers and no usable %q",
			id, found, fieldRankMatches)
	}
}

// parseSingleRankMatch reads the hit of an instance that reports exactly one dp rank.
//
// Several ranks without instance-level totals are refused: the aggregate would be ambiguous, and
// silently taking rank "0" is the defect this whole path exists to avoid.
func parseSingleRankMatch(id string, entry map[string]interface{}, completeTokens int64) (RankMatch, error) {
	rawMatches, exists := entry[fieldRankMatches]
	if !exists {
		return RankMatch{}, fmt.Errorf("instance %s has no %q", id, fieldRankMatches)
	}
	matches, ok := rawMatches.(map[string]interface{})
	if !ok {
		return RankMatch{}, fmt.Errorf("instance %s %q has unexpected type %T", id, fieldRankMatches, rawMatches)
	}
	if len(matches) != 1 {
		return RankMatch{}, fmt.Errorf(
			"instance %s reports %d dp ranks in %q but no instance-level totals, cannot pick one",
			id, len(matches), fieldRankMatches)
	}
	for rank, rawMatch := range matches {
		matchMap, ok := rawMatch.(map[string]interface{})
		if !ok {
			return RankMatch{}, fmt.Errorf("instance %s rank %q has unexpected type %T", id, rank, rawMatch)
		}
		match, found, err := readTiers(id, matchMap)
		if err != nil {
			return RankMatch{}, err
		}
		if found != 4 {
			return RankMatch{}, fmt.Errorf(
				"instance %s rank %q publishes only %d of the 4 tiers", id, rank, found)
		}
		if err := checkTierOrder(id, match, completeTokens); err != nil {
			return RankMatch{}, err
		}
		return match, nil
	}
	return RankMatch{}, fmt.Errorf("instance %s has no dp ranks to read", id)
}

// readTiers reads the four cumulative tiers from one map and reports how many were present.
//
// The map is either an instance entry or one rank entry; both use the same four field names.
func readTiers(id string, m map[string]interface{}) (RankMatch, int, error) {
	var out RankMatch
	fields := []struct {
		name string
		dst  *int64
	}{
		{fieldDevice, &out.Device},
		{fieldCPULocal, &out.CPULocal},
		{fieldCPUShare, &out.CPUShare},
		{fieldDisk, &out.Disk},
	}
	found := 0
	for _, field := range fields {
		raw, exists := m[field.name]
		if !exists {
			continue
		}
		value, ok := asHitInt64(raw)
		if !ok {
			return RankMatch{}, found,
				fmt.Errorf("instance %s field %q has unexpected type %T", id, field.name, raw)
		}
		*field.dst = value
		found++
	}
	return out, found, nil
}

// checkTierOrder rejects values that would make the scoring formula negative.
func checkTierOrder(id string, match RankMatch, completeTokens int64) error {
	if match.Device < 0 || match.Device > match.CPULocal || match.CPULocal > match.CPUShare ||
		match.CPUShare > match.Disk || match.Disk > completeTokens {
		return fmt.Errorf(
			"instance %s violates 0 <= device <= cpu_local <= cpu_share <= disk <= %d, "+
				"got device=%d cpu_local=%d cpu_share=%d disk=%d",
			id, completeTokens, match.Device, match.CPULocal, match.CPUShare, match.Disk)
	}
	return nil
}

// asHitInt64 reads an integer field from the decoded map.
//
// The handle does not set PositiveIntUnsigned, so positive integers decode to int64, but the
// msgpack integer family is wide and a producer using a different library may emit uint64, so
// both are accepted. float64 is rejected: these fields are token counts, and a float means the
// protocol does not match -- accepting it would let a real format change pass silently.
func asHitInt64(raw interface{}) (int64, bool) {
	switch v := raw.(type) {
	case int64:
		return v, true
	case uint64:
		return int64(v), true
	default:
		return 0, false
	}
}
