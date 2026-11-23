package service

import (
	"bytes"
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/tidwall/gjson"
)

// cacheControlClientMarkerKey tags every client cache_control object for the
// short time Sub2API rewrites a native Claude request. The marker lives inside
// the cache_control object, so it follows a breakpoint that the OAuth mimic
// relocates (for example the client system breakpoint moved into the
// "[System Instructions]" message). A breakpoint without the marker at
// enforcement time was added by Sub2API. The marker is always removed before
// the body leaves cacheControlLimitPlan.apply.
const cacheControlClientMarkerKey = "__sub2api_client_cache_control__"

var cacheControlClientMarkerJSONKey = []byte(`"` + cacheControlClientMarkerKey + `"`)

// cacheControlLimitPlan carries the per-request decision for the four
// breakpoint limit from the point before Sub2API injects breakpoints to the
// point where the limit is enforced.
type cacheControlLimitPlan struct {
	scope            cacheControlTrimScope
	markerKey        string
	preserveThinking bool
}

// planCacheControlLimit decides how a native Claude /v1/messages or
// count_tokens request handles the four breakpoint limit. It must run before
// Sub2API injects any breakpoint, so body holds only client breakpoints and
// the decision is based on the client's own breakpoint count.
//
//   - trim (default): every breakpoint may be removed, unchanged behavior.
//   - passthrough, client sent more than four: nothing is removed. The
//     request is forwarded as is and the upstream answers with its own 400.
//   - passthrough, client sent four or fewer, no injection possible: nothing
//     is removed (the request is already within the limit).
//   - passthrough, client sent four or fewer, injection possible (OAuth
//     mimic): client breakpoints are marked and only unmarked breakpoints,
//     the ones Sub2API added, are trimmed, so injection never causes a 400.
//     If the markers cannot be placed reliably the historical trim is used,
//     which also never causes a 400.
//   - reject: after the local overflow check lets a request through, apply
//     the passthrough rules using the upstream's client breakpoint count.
//
// The returned body is the input body unless marked is true.
func planCacheControlLimit(body []byte, mode, thinkingMode string, mayInject bool) (plan cacheControlLimitPlan, out []byte, marked bool) {
	plan.preserveThinking = thinkingMode == ClaudeAlignPassthrough || thinkingMode == ClaudeAlignReject
	if mode != ClaudeCacheControlLimitPassthrough && mode != ClaudeAlignReject {
		return plan, body, false
	}
	if !mayInject {
		plan.scope = cacheControlTrimNone
		return plan, body, false
	}
	var count int
	if mode == ClaudeAlignReject {
		count = countClientCacheControlBreakpoints(body)
	} else {
		count = countLegacyCacheControlBreakpoints(body, plan.preserveThinking)
	}
	if count > maxCacheControlBlocks {
		plan.scope = cacheControlTrimNone
		return plan, body, false
	}
	plan.markerKey = cacheControlClientMarkerKey
	// A client string or an escaped JSON name can survive a mimic rewrite.
	// Keep cleanup unambiguous when the client could already own this name.
	if bytes.Contains(body, cacheControlClientMarkerJSONKey) || bytes.Contains(body, jsonUnicodeEscapeBytes) {
		plan.markerKey += uuid.NewString()
	}
	out, marked, ok := markClientCacheControlBreakpoints(body, plan.preserveThinking, plan.markerKey)
	if !ok {
		return plan, body, false
	}
	plan.scope = cacheControlTrimInjectedOnly
	return plan, out, marked
}

// Passthrough is based on block-level breakpoints. In strip mode it also
// retains the historical treatment of redacted thinking.
func countLegacyCacheControlBreakpoints(body []byte, preserveThinking bool) int {
	if len(body) == 0 {
		return 0
	}
	if !bytes.Contains(body, cacheControlKeyBytes) && !bytes.Contains(body, jsonUnicodeEscapeBytes) {
		return 0
	}
	_, messagePaths, toolPaths, systemPaths := scanCacheControlPaths(stripDeferredToolCacheControl(body), preserveThinking, false)
	return len(messagePaths) + len(toolPaths) + len(systemPaths)
}

// countClientCacheControlBreakpoints follows the upstream's four-slot limit
// on the original request, before mimicry moves or injects breakpoints. When
// the raw key count proves the body is within the limit, zero suffices.
func countClientCacheControlBreakpoints(body []byte) int {
	candidates := scanCacheControl(body, missingJSONRange())
	// BASE returns the actual count even below the limit when the input
	// contains a Unicode escape. Preserve that result for count callers.
	if !candidates.mayOverflow() && (candidates.literalKeys == 0 || !bytes.Contains(body, jsonUnicodeEscapeBytes)) {
		return 0
	}
	return parsedClientCacheControlCount(body)
}

func (c cacheControlCandidates) mayOverflow() bool {
	return c.escapedName || c.literalKeys > maxCacheControlBlocks
}

func parsedClientCacheControlCount(body []byte) int {
	root := gjson.Parse(openAIWSPayloadStringView(body))
	count := 0
	if cc := root.Get("cache_control"); cc.Exists() && cc.Type != gjson.Null {
		count++
	}
	countBlocks := func(blocks gjson.Result) {
		if blocks.IsArray() {
			blocks.ForEach(func(_, block gjson.Result) bool {
				switch block.Get("type").String() {
				case "thinking", "redacted_thinking":
				default:
					if cc := block.Get("cache_control"); cc.Exists() && cc.Type != gjson.Null {
						count++
					}
				}
				return true
			})
		}
	}
	countBlocks(root.Get("system"))
	if messages := root.Get("messages"); messages.IsArray() {
		messages.ForEach(func(_, message gjson.Result) bool {
			countBlocks(message.Get("content"))
			return true
		})
	}
	if tools := root.Get("tools"); tools.IsArray() {
		tools.ForEach(func(_, tool gjson.Result) bool {
			if cc := tool.Get("cache_control"); cc.Exists() && cc.Type != gjson.Null {
				count++
			}
			return true
		})
	}
	return count
}

func clientCacheControlOverflow(body []byte, header http.Header) int {
	if !scanCacheControl(body, missingJSONRange()).mayOverflow() {
		return 0
	}
	return parsedClientCacheControlOverflow(body, header)
}

func parsedClientCacheControlOverflow(body []byte, header http.Header) int {
	count := parsedClientCacheControlCount(body)
	if count > maxCacheControlBlocks && len(claude.UnknownTopLevelRequestFields(body, anthropicBetaHeader(header))) == 0 {
		return count
	}
	return 0
}

// markClientCacheControlBreakpoints adds the client marker to every
// cache_control object counted by collectCacheControlPaths. Non-object values
// cannot carry a marker and are treated like injected breakpoints, which is
// the historical trim behavior for them.
//
// ok is false when the markers cannot be placed reliably: a body that already
// contains the marker key in a cache_control object, or a cache_control key written with JSON escapes
// that sjson would duplicate instead of update. The input body is returned.
func markClientCacheControlBreakpoints(body []byte, preserveThinking bool, markerKey string) (out []byte, marked bool, ok bool) {
	if !bytes.Contains(body, cacheControlKeyBytes) && !bytes.Contains(body, jsonUnicodeEscapeBytes) {
		return body, false, true
	}
	batch := readCacheControlBatch(body, "")
	expected := 0
	for section := cacheTools; section < cacheTop; section++ {
		blocks := batch[section]
		for i := range blocks {
			b := &blocks[i]
			if !b.cc.IsObject() || !b.participates(section, preserveThinking) {
				continue
			}
			if b.cc.Get(markerKey).Exists() || !b.setField(body, markerKey, true) {
				return body, false, false
			}
			expected++
		}
	}
	out = batch.apply(body)
	if bytes.Count(out, cacheControlKeyBytes) != bytes.Count(body, cacheControlKeyBytes) ||
		bytes.Count(out, []byte(`"`+markerKey+`"`)) != expected {
		return body, false, false
	}
	return out, expected > 0, true
}

// trimInjectedCacheControlBreakpoints removes the client markers and, while
// the request exceeds the limit, removes only unmarked breakpoints in the
// historical order (tools from the end, messages from the start, system from
// the end). Client breakpoints are never removed here.
//
// A top-level cache_control (automatic caching) is always a client value and
// is reserved one of the four slots, so injected breakpoints cannot push such
// a request over the limit either. If the upstream does not count it, one
// injected breakpoint more than needed is removed, which cannot cause a 400.
func trimInjectedCacheControlBreakpoints(body []byte, plan cacheControlLimitPlan) []byte {
	batch := readCacheControlBatch(body, plan.markerKey)
	remaining := -maxCacheControlBlocks
	if top := batch[cacheTop]; len(top) > 0 && top[0].cc.Type != gjson.Null {
		remaining++
	}
	for section := cacheTools; section < cacheTop; section++ {
		blocks := batch[section]
		for i := range blocks {
			b := &blocks[i]
			if b.cc.Type == gjson.Null {
				continue
			}
			if b.participates(section, plan.preserveThinking) {
				remaining++
				continue
			}
			if section == cacheSystem {
				logger.LegacyPrintf("service.gateway", "[Warning] Removed illegal cache_control from thinking block in system")
				b.deleteField(body, "cache_control")
			} else if !plan.preserveThinking {
				logger.LegacyPrintf("service.gateway", "[Warning] Removed illegal cache_control from thinking block in messages[%d].content[%d]", b.index, b.content)
				b.deleteField(body, "cache_control")
			}
		}
	}
	// Removed thinking and preserved thinking are both outside the trim pool.
	for _, section := range [...]cacheControlSection{cacheTools, cacheMessages, cacheSystem} {
		blocks := batch[section]
		for j := range blocks {
			i := j
			if section != cacheMessages {
				i = len(blocks) - 1 - j
			}
			b := &blocks[i]
			if b.cc.Type == gjson.Null || !b.participates(section, plan.preserveThinking) {
				continue
			}
			if b.leaf.Exists() {
				b.block = b.cc
				b.deleteField(body, plan.markerKey)
			} else if remaining > 0 {
				b.deleteField(body, "cache_control")
				remaining--
			}
		}
	}
	return batch.apply(body)
}

// stripRemainingCacheControlClientMarkers is a safety net for a marker that a
// future body rewrite moved outside system, messages[*].content[*] or tools.
// Markers are written as the raw member "<key>":true, either by sjson (no
// whitespace) or by encoding/json after a map round trip (no whitespace), so a
// byte-level removal keeps the key order of the rest of the body. Inside a
// JSON string the quotes would be escaped, so client text cannot match.
func stripRemainingCacheControlClientMarkers(body []byte, markerKey string) []byte {
	key := []byte(`"` + markerKey + `"`)
	if !bytes.Contains(body, key) {
		return body
	}
	member := append(key, []byte(":true")...)
	out := bytes.ReplaceAll(body, append([]byte{','}, member...), nil)
	out = bytes.ReplaceAll(out, append(append([]byte{}, member...), ','), nil)
	out = bytes.ReplaceAll(out, member, nil)
	if !bytes.Equal(out, body) {
		logger.LegacyPrintf("service.gateway", "[Warning] Removed cache_control client marker outside the scanned breakpoint paths")
	}
	return out
}

// apply enforces the planned limit, fixes the TTL order and removes client
// markers. Escaped keys and marked bodies need the full path scan.
func (p cacheControlLimitPlan) apply(body []byte) []byte {
	if !bytes.Contains(body, jsonUnicodeEscapeBytes) {
		if !bytes.Contains(body, cacheControlKeyBytes) {
			return body
		}
		noTrim := p.scope == cacheControlTrimNone || (p.scope == cacheControlTrimAny && bytes.Count(body, cacheControlKeyBytes) <= maxCacheControlBlocks)
		if noTrim && !bytes.Contains(body, thinkingBlockTypeBytes) {
			if !bytes.Contains(body, cacheTTL1hJSONBytes) {
				return body
			}
			return normalizeCacheControlTTLOrder(body, p.preserveThinking)
		}
	}
	out := normalizeCacheControlTTLOrder(enforceCacheControlBlockLimitScoped(body, p), p.preserveThinking)
	if p.scope == cacheControlTrimInjectedOnly {
		out = stripRemainingCacheControlClientMarkers(out, p.markerKey)
	}
	return out
}

// cacheControlLimitMode defaults missing and unrecognized policies to trim.
func cacheControlLimitMode(policy ClaudeCustomizationSettings) string {
	switch policy.CacheControlLimitMode {
	case ClaudeCacheControlLimitPassthrough, ClaudeAlignReject:
		return policy.CacheControlLimitMode
	default:
		return ClaudeCacheControlLimitTrim
	}
}

func requestCacheControlLimitMode(c *gin.Context, account *Account, policy ClaudeCustomizationSettings) string {
	mode := cacheControlLimitMode(policy)
	if mode == ClaudeAlignReject && (!IsAnthropicMessagesRequest(c) || !isNativeCacheControlAccount(account)) {
		return ClaudeCacheControlLimitTrim
	}
	return mode
}

func isNativeCacheControlAccount(account *Account) bool {
	return account != nil && account.Platform == PlatformAnthropic && (account.Type == AccountTypeAPIKey || account.IsOAuth())
}
