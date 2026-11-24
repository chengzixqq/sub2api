package service

import (
	"math"
	"strconv"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

// NormalizeSearchCount prevents malformed upstream counters from producing a
// negative surcharge.
func NormalizeSearchCount(count int) int {
	if count < 0 {
		return 0
	}
	return count
}

// SaturatingSearchCountAdd adds counters without allowing integer wraparound.
func SaturatingSearchCountAdd(current, delta int) (int, bool) {
	current = NormalizeSearchCount(current)
	delta = NormalizeSearchCount(delta)
	maxInt := int(^uint(0) >> 1)
	if delta > maxInt-current {
		return maxInt, true
	}
	return current + delta, false
}

// AccumulateSearchCount is the common boundary for counters collected while
// parsing an upstream response. Upstream data is untrusted: clamp negative
// values and saturate instead of allowing an int wraparound to become a free
// or negative search surcharge.
func AccumulateSearchCount(current, delta int, component string) int {
	if current < 0 || delta < 0 {
		logger.L().Warn("openai.search_count_negative_clamped",
			zap.String("component", component),
			zap.Int("current", current),
			zap.Int("delta", delta),
		)
	}
	next, saturated := SaturatingSearchCountAdd(current, delta)
	if saturated {
		logger.L().Warn("openai.search_count_overflow_saturated",
			zap.String("component", component),
			zap.Int("current", current),
			zap.Int("delta", delta),
		)
	}
	return next
}

// countGrokNativeSearchCallsFromJSONBytes counts completed native search tool
// calls in a Responses-style JSON body (output array or nested response.output).
// Counts: web_search_call, x_search_call, tool_search_call, and function_call
// named tool_search / web_search / x_search, and custom_tool_call items the xAI
// server runs itself (apicompat.IsServerSideSearchCall: call_id xs_ / as_).
//
// When usage.server_side_tool_usage_details is present, xAI's reported success
// counts replace the item count (see grokSearchCountFromUsage).
func countGrokNativeSearchCallsFromJSONBytes(body []byte) int {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return 0
	}
	if n, ok := grokReportedSearchCount(body); ok {
		return n
	}
	// Responses envelopes normally expose either top-level output (JSON mode)
	// or response.output (terminal SSE payload). Compatibility layers can retain
	// both copies; counting both would bill the same search twice. Prefer the
	// canonical nested response when present and fall back to top-level output.
	if nested := gjson.GetBytes(body, "response.output"); nested.IsArray() {
		return countGrokNativeSearchCallsInOutputArray(nested)
	}
	return countGrokNativeSearchCallsInOutputArray(gjson.GetBytes(body, "output"))
}

func countGrokNativeSearchCallsFromSSEBody(body string) int {
	var searches grokSearchStreamCounter
	forEachOpenAISSEFrame(body, func(eventType string, data []byte) {
		searches.observe(data, eventType)
	})
	return searches.count
}

// One upstream attempt may repeat an item in its terminal output. An explicit
// terminal usage report replaces all such evidence, even when the report is zero.
type grokSearchStreamCounter struct {
	seen     map[string]struct{}
	count    int
	reported bool
}

func (s *grokSearchStreamCounter) observe(data []byte, eventType string) {
	eventType = effectiveOpenAISSEEventType(data, eventType)
	if openAIStreamEventTypeIsTerminal(eventType) {
		if n, ok := grokReportedSearchCount(data); ok && gjson.ValidBytes(data) {
			s.count, s.reported = n, true
			return
		}
	}
	if s.reported {
		return
	}
	switch eventType {
	case "response.output_item.done", "response.completed", "response.done", "response.incomplete":
	default:
		return
	}
	if s.seen == nil {
		s.seen = make(map[string]struct{})
	}
	if !gjson.GetBytes(data, "type").Exists() {
		data = []byte(openAICompatPayloadWithEventType(string(data), eventType))
	}
	s.count = AccumulateSearchCount(s.count, countGrokNativeSearchCallsInSSEDataDedup(data, s.seen), "grok_search_stream")
}

func grokReportedSearchCount(data []byte) (int, bool) {
	for _, path := range []string{"response.usage", "usage"} {
		if n, ok := grokSearchCountFromUsage(gjson.GetBytes(data, path)); ok {
			return n, true
		}
	}
	return 0, false
}

// grokSearchCountFromUsage reads xAI's per-class success counts from a usage
// object. xAI documents these counts as what determines billing for per-call
// tools (https://docs.x.ai/developers/tools/tool-usage-details#server-side-tool-calls-vs-tool-usage).
// Only the classes the item counter also counts are read: web_search_calls
// (web_search_call items), x_search_calls (xs_ custom_tool_call items) and
// document_search_calls (as_ custom_tool_call items). Other classes and the
// X Search item counts (x_posts_fetched, x_users_fetched) are not read. ok is
// false when server_side_tool_usage_details is absent, so callers keep the
// item count.
func grokSearchCountFromUsage(usage gjson.Result) (int, bool) {
	details := usage.Get("server_side_tool_usage_details")
	if !details.IsObject() {
		return 0, false
	}
	total := 0
	for _, field := range []string{"web_search_calls", "x_search_calls", "document_search_calls"} {
		value := details.Get(field)
		if value.Type == gjson.String && gjson.Valid(value.Str) {
			value = gjson.Parse(value.Str)
		}
		n, ok := boundedJSONNonNegativeInt(value)
		if !ok && value.Type == gjson.Number && value.Num >= float64(math.MaxInt) {
			n = math.MaxInt
		}
		total = AccumulateSearchCount(total, n, "grok_search_usage")
	}
	return total, true
}

// countGrokNativeSearchCallsInSSEData counts search tool calls in one SSE
// payload without cross-event dedup. Prefer countGrokNativeSearchCallsInSSEDataDedup
// for live streams so item.done + response.completed do not double-bill.
func countGrokNativeSearchCallsInSSEData(data []byte) int {
	n, _ := countGrokNativeSearchCallsInSSEDataWithKeys(data)
	return n
}

// countGrokNativeSearchCallsInSSEDataDedup increments only unseen call ids.
// Callers must reuse the same seen map for the full stream lifetime.
//
// When call_id/id is missing, a synthetic key is built from item type + name so
// item.done + response.completed for the same tool still count once (never fall
// back to raw multi-event n, which ~2× overbills).
func countGrokNativeSearchCallsInSSEDataDedup(data []byte, seen map[string]struct{}) int {
	if seen == nil {
		return countGrokNativeSearchCallsInSSEData(data)
	}
	n, keys := countGrokNativeSearchCallsInSSEDataWithKeys(data)
	if n <= 0 {
		return 0
	}
	// Prefer stable ids; fill gaps with synthetic keys so we never raw-add n.
	if len(keys) < n {
		// Rebuild keys for every item so unkeyed items still get a fingerprint.
		keys = collectGrokNativeSearchCallKeys(data)
	}
	if len(keys) == 0 {
		// True empty — should not happen when n>0; fail-closed to 0 extra bill.
		return 0
	}
	added := 0
	local := make(map[string]struct{}, len(keys))
	isItemDone := strings.TrimSpace(gjson.GetBytes(data, "type").String()) == "response.output_item.done"
	for _, k := range keys {
		if k == "" {
			continue
		}
		if _, ok := local[k]; ok {
			continue
		}
		local[k] = struct{}{}
		if _, ok := seen[k]; ok {
			if !isItemDone || !strings.HasPrefix(k, "synth:") {
				continue
			}
			// Each id-less item.done is a distinct completed invocation. Advance
			// its ordinal so interrupted streams remain accurately billable.
			separator := strings.LastIndexByte(k, ':')
			if separator < 0 {
				continue
			}
			base := k[:separator]
			for ordinal := 2; ; ordinal++ {
				candidate := base + ":" + strconv.Itoa(ordinal)
				if _, exists := seen[candidate]; !exists {
					k = candidate
					break
				}
			}
		}
		seen[k] = struct{}{}
		added++
	}
	return added
}

func collectGrokNativeSearchCallKeys(data []byte) []string {
	if len(data) == 0 || !gjson.ValidBytes(data) {
		return nil
	}
	// An empty type means a bare item object without an SSE envelope; anything
	// else that is not a completion event carries no billable call.
	switch strings.TrimSpace(gjson.GetBytes(data, "type").String()) {
	case "response.output_item.done", "response.completed", "response.done", "response.incomplete", "":
	default:
		return nil
	}
	var keys []string
	syntheticOrdinals := make(map[string]int)
	consider := func(item gjson.Result) {
		if !isGrokNativeSearchOutputItem(item) {
			return
		}
		key := firstNonEmpty(
			strings.TrimSpace(item.Get("call_id").String()),
			strings.TrimSpace(item.Get("id").String()),
			strings.TrimSpace(item.Get("item.call_id").String()),
			strings.TrimSpace(item.Get("item.id").String()),
		)
		if key == "" {
			// Include the ordinal among same-kind calls. A plain type:name key
			// collapses two id-less web searches in one completed response into
			// one charge. The ordinal remains stable between ordered item.done
			// events and response.completed output.
			base := "synth:" + strings.ToLower(strings.TrimSpace(item.Get("type").String())) +
				":" + strings.ToLower(strings.TrimSpace(item.Get("name").String()))
			syntheticOrdinals[base]++
			key = base + ":" + strconv.Itoa(syntheticOrdinals[base])
		}
		keys = append(keys, key)
	}
	if item := gjson.GetBytes(data, "item"); item.Exists() {
		consider(item)
	}
	output := gjson.GetBytes(data, "response.output")
	if !output.IsArray() {
		output = gjson.GetBytes(data, "output")
	}
	output.ForEach(func(_, item gjson.Result) bool {
		consider(item)
		return true
	})
	if len(keys) == 0 && isGrokNativeSearchOutputItem(gjson.ParseBytes(data)) {
		consider(gjson.ParseBytes(data))
	}
	return keys
}

func countGrokNativeSearchCallsInSSEDataWithKeys(data []byte) (int, []string) {
	if len(data) == 0 || !gjson.ValidBytes(data) {
		return 0, nil
	}
	// Count once on item completion / completed response, not on every delta.
	// An empty type is a bare item object without an SSE envelope.
	switch strings.TrimSpace(gjson.GetBytes(data, "type").String()) {
	case "response.output_item.done", "response.completed", "response.done", "response.incomplete", "":
	default:
		return 0, nil
	}
	var keys []string
	n := 0
	consider := func(item gjson.Result) {
		if !isGrokNativeSearchOutputItem(item) {
			return
		}
		n++
		key := firstNonEmpty(
			strings.TrimSpace(item.Get("call_id").String()),
			strings.TrimSpace(item.Get("id").String()),
			strings.TrimSpace(item.Get("item.call_id").String()),
			strings.TrimSpace(item.Get("item.id").String()),
		)
		if key != "" {
			keys = append(keys, key)
		}
	}
	if item := gjson.GetBytes(data, "item"); item.Exists() {
		consider(item)
	}
	output := gjson.GetBytes(data, "response.output")
	if !output.IsArray() {
		output = gjson.GetBytes(data, "output")
	}
	output.ForEach(func(_, item gjson.Result) bool {
		consider(item)
		return true
	})
	// Bare output item event without nested item key.
	if n == 0 && isGrokNativeSearchOutputItem(gjson.ParseBytes(data)) {
		consider(gjson.ParseBytes(data))
	}
	return n, keys
}

func countGrokNativeSearchCallsInOutputArray(output gjson.Result) int {
	if !output.IsArray() {
		return 0
	}
	seen := make(map[string]struct{})
	syntheticOrdinal := 0
	count := 0
	output.ForEach(func(_, item gjson.Result) bool {
		if !isGrokNativeSearchOutputItem(item) {
			return true
		}
		key := firstNonEmpty(
			strings.TrimSpace(item.Get("call_id").String()),
			strings.TrimSpace(item.Get("id").String()),
			strings.TrimSpace(item.Get("item.call_id").String()),
			strings.TrimSpace(item.Get("item.id").String()),
		)
		if key == "" {
			// Distinct id-less calls still each count; only reuse a key when an
			// upstream-provided identifier makes a duplicate provable.
			syntheticOrdinal++
			key = "json-synth:" + strconv.Itoa(syntheticOrdinal)
		}
		if _, exists := seen[key]; exists {
			return true
		}
		seen[key] = struct{}{}
		count++
		return true
	})
	return count
}

func isGrokNativeSearchOutputItem(item gjson.Result) bool {
	if !item.Exists() {
		return false
	}
	itemType := strings.ToLower(strings.TrimSpace(item.Get("type").String()))
	switch itemType {
	case "web_search_call", "x_search_call", "tool_search_call":
		return true
	case "function_call", "custom_tool_call":
		if apicompat.IsServerSideSearchCall(itemType, item.Get("call_id").String()) {
			return true
		}
		name := strings.ToLower(strings.TrimSpace(item.Get("name").String()))
		return name == "web_search" || name == "x_search" || name == "tool_search"
	default:
		return false
	}
}
