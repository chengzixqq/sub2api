//go:build unit

package service

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const (
	thinkingTestBlock = `{"type":"thinking","thinking":"h","cache_control":{"type":"ephemeral"}}`
	redactedTestBlock = `{"type":"redacted_thinking","data":"x","cache_control":{"type":"ephemeral"}}`
	plainTestMessage  = `{"role":"user","content":"hello"}`
)

func thinkingTestBody(messages ...string) string {
	return `{"model":"claude-sonnet-4-5","max_tokens":15,"messages":[` + strings.Join(messages, ",") + `]}`
}

func thinkingTestMessage(role string, blocks ...string) string {
	return `{"role":"` + role + `","content":[` + strings.Join(blocks, ",") + `]}`
}

type thinkingForwardResult struct {
	*httptest.ResponseRecorder
	bodies [][]byte
	err    error
}

func forwardThinkingTest(t *testing.T, path string, account *Account, body, thinkingMode, limitMode string, dispatched bool) thinkingForwardResult {
	t.Helper()
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, path, nil)
	if dispatched {
		clock := InstallOpsUpstreamAttemptClock(c)
		MarkOpsUpstreamAttemptStart(c.Request.Context(), 19)
		require.Positive(t, clock.claim(19, 0))
	}
	policy := cacheControlLimitPolicy(limitMode)
	policy.ThinkingCacheControl = thinkingMode
	response := cacheControlLimitOKMessage
	if path == "/v1/messages/count_tokens" {
		response = `{"input_tokens":3}`
	}
	up := &fallbackSequenceUpstream{responses: []*http.Response{cacheControlLimitResponse(http.StatusOK, response)}}
	s := fallbackNativeGatewayForTestWithPolicy(t, up, nil, policy)
	parsed, err := ParseGatewayRequest(NewRequestBodyRef([]byte(body)), PlatformAnthropic)
	require.NoError(t, err)
	if path == "/v1/messages/count_tokens" {
		err = s.ForwardCountTokens(c.Request.Context(), c, account, parsed)
	} else {
		_, err = s.Forward(c.Request.Context(), c, account, parsed)
	}
	return thinkingForwardResult{rec, up.bodies, err}
}

func (r thinkingForwardResult) requireRejected(t *testing.T, message string) {
	t.Helper()
	require.ErrorIs(t, r.err, ErrLocalAnthropicRejected)
	require.Empty(t, r.bodies)
	require.Equal(t, http.StatusBadRequest, r.Code)
	id := r.Header().Get("request-id")
	require.Regexp(t, `^req_011C[1-9A-HJ-NP-Za-km-z]{20}$`, id)
	require.Equal(t, `{"type":"error","error":{"type":"invalid_request_error","message":"`+message+`"},"request_id":"`+id+`"}`, r.Body.String())
}

func (r thinkingForwardResult) requireSent(t *testing.T) []byte {
	t.Helper()
	require.NoError(t, r.err)
	require.Len(t, r.bodies, 1)
	require.Equal(t, http.StatusOK, r.Code)
	requireNoCacheControlMarker(t, r.bodies[0])
	return r.bodies[0]
}

func TestThinkingCacheControl_RejectGolden(t *testing.T) {
	const text = `{"type":"text","text":"hi"}`
	for _, tc := range []struct{ name, body, message string }{
		{"thinking", thinkingTestBody(plainTestMessage, thinkingTestMessage("assistant", thinkingTestBlock)), "messages.1.content.0.thinking.cache_control: Extra inputs are not permitted"},
		{"redacted", thinkingTestBody(plainTestMessage, thinkingTestMessage("assistant", redactedTestBlock)), "messages.1.content.0.redacted_thinking.cache_control: Extra inputs are not permitted"},
		{"redacted first block", thinkingTestBody(thinkingTestMessage("assistant", redactedTestBlock, thinkingTestBlock)), "messages.0.content.0.redacted_thinking.cache_control: Extra inputs are not permitted"},
		{"redacted first message", thinkingTestBody(thinkingTestMessage("assistant", redactedTestBlock), thinkingTestMessage("user", thinkingTestBlock)), "messages.0.content.0.redacted_thinking.cache_control: Extra inputs are not permitted"},
		{"first", thinkingTestBody(plainTestMessage, thinkingTestMessage("assistant", text, thinkingTestBlock, redactedTestBlock), thinkingTestMessage("user", thinkingTestBlock)), "messages.1.content.1.thinking.cache_control: Extra inputs are not permitted"},
		{"user", thinkingTestBody(thinkingTestMessage("user", thinkingTestBlock)), "messages.0.content.0.thinking.cache_control: Extra inputs are not permitted"},
		{"absolute", thinkingTestBody(plainTestMessage, thinkingTestMessage("assistant", text), plainTestMessage, thinkingTestMessage("assistant", text, redactedTestBlock)), "messages.3.content.1.redacted_thinking.cache_control: Extra inputs are not permitted"},
		{"ttl", thinkingTestBody(thinkingTestMessage("user", strings.Replace(thinkingTestBlock, `"ephemeral"`, `"ephemeral","ttl":"1h"`, 1))), "messages.0.content.0.thinking.cache_control: Extra inputs are not permitted"},
		{"escaped", thinkingTestBody(thinkingTestMessage("user", strings.Replace(redactedTestBlock, `cache_control`, `cache_\u0063ontrol`, 1))), "messages.0.content.0.redacted_thinking.cache_control: Extra inputs are not permitted"},
	} {
		for _, path := range []string{"/v1/messages", "/v1/messages/count_tokens"} {
			t.Run(tc.name+path, func(t *testing.T) {
				r := forwardThinkingTest(t, path, fallbackNativeAccountForTest(false), tc.body, ClaudeAlignReject, ClaudeCacheControlLimitTrim, false)
				r.requireRejected(t, tc.message)
			})
		}
	}
}

func TestThinkingCacheControl_IndependentOfLimitMode(t *testing.T) {
	// Five real breakpoints, plus one message and one system thinking block.
	const text = `{"type":"text","text":"a","cache_control":{"type":"ephemeral"}}`
	body := thinkingTestBody(thinkingTestMessage("user", thinkingTestBlock, text, text, text, text, text))
	body = strings.Replace(body, `"messages"`, `"system":[`+thinkingTestBlock+`],"messages"`, 1)
	t.Run("count_tokens/reject/reject", func(t *testing.T) {
		r := forwardThinkingTest(t, "/v1/messages/count_tokens", fallbackNativeAccountForTest(false), body, ClaudeAlignReject, ClaudeAlignReject, false)
		r.requireRejected(t, "messages.0.content.0.thinking.cache_control: Extra inputs are not permitted")
	})
	for _, thinking := range []string{ClaudeAlignStrip, ClaudeAlignPassthrough, ClaudeAlignReject} {
		for _, limit := range []string{ClaudeCacheControlLimitTrim, ClaudeAlignPassthrough, ClaudeAlignReject} {
			t.Run(thinking+"/"+limit, func(t *testing.T) {
				r := forwardThinkingTest(t, "/v1/messages", fallbackNativeAccountForTest(false), body, thinking, limit, false)
				switch {
				case thinking == ClaudeAlignReject:
					r.requireRejected(t, "messages.0.content.0.thinking.cache_control: Extra inputs are not permitted")
				case limit == ClaudeAlignReject:
					r.requireRejected(t, "A maximum of 4 blocks with cache_control may be provided. Found 5.")
				default:
					const stripped = `{"type":"thinking","thinking":"h"}`
					want := strings.Replace(body, thinkingTestBlock, stripped, 1) // system
					if thinking == ClaudeAlignStrip {
						want = strings.Replace(want, thinkingTestBlock, stripped, 1)
					}
					if limit == ClaudeCacheControlLimitTrim {
						want = strings.Replace(want, text, `{"type":"text","text":"a"}`, 1)
					}
					require.Equal(t, want, string(r.requireSent(t)))
				}
			})
		}
	}
}

func TestThinkingCacheControl_StripBaselineBytes(t *testing.T) {
	body := thinkingTestBody(thinkingTestMessage("assistant", redactedTestBlock, thinkingTestBlock))
	want := strings.Replace(body, thinkingTestBlock, `{"type":"thinking","thinking":"h"}`, 1)
	want = strings.Replace(want, redactedTestBlock, `{"type":"redacted_thinking","data":"x"}`, 1)
	for _, limit := range []string{ClaudeCacheControlLimitTrim, ClaudeAlignPassthrough, ClaudeAlignReject} {
		r := forwardThinkingTest(t, "/v1/messages", fallbackNativeAccountForTest(false), body, ClaudeAlignStrip, limit, false)
		require.Equal(t, want, string(r.requireSent(t)))
	}
}

func TestThinkingCacheControl_UnknownFieldDefers(t *testing.T) {
	body := clientCacheControlTestBody(5, 0, false)
	body = strings.Replace(body, `"content":"hi"`, `"content":[`+thinkingTestBlock+`]`, 1)
	body = strings.Replace(body, "{", `{"bogus_field_xyz":true,`, 1)
	r := forwardThinkingTest(t, "/v1/messages", fallbackNativeAccountForTest(false), body, ClaudeAlignReject, ClaudeAlignReject, false)
	require.Equal(t, body, string(r.requireSent(t)))
}

func TestThinkingCacheControl_PassthroughKeepsField(t *testing.T) {
	const text = `{"type":"text","text":"t","cache_control":{}}`
	body := thinkingTestBody(thinkingTestMessage("assistant", redactedTestBlock, thinkingTestBlock, text, text, text, text, text))
	want := strings.Replace(body, text, `{"type":"text","text":"t"}`, 1)
	r := forwardThinkingTest(t, "/v1/messages", fallbackNativeAccountForTest(false), body, ClaudeAlignPassthrough, ClaudeCacheControlLimitTrim, false)
	require.Equal(t, want, string(r.requireSent(t)))
}

func TestThinkingCacheControl_NullAndPriorDispatch(t *testing.T) {
	body := thinkingTestBody(thinkingTestMessage("user", thinkingTestBlock))
	for _, tc := range []struct {
		name, body, path string
		dispatched       bool
		wantCC           bool
	}{
		{"null", strings.Replace(body, `{"type":"ephemeral"}`, "null", 1), "/v1/messages", false, true},
		{"dispatched", body, "/v1/messages", true, true},
		{"other protocol", body, "/v1/chat/completions", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := forwardThinkingTest(t, tc.path, fallbackNativeAccountForTest(false), tc.body, ClaudeAlignReject, ClaudeCacheControlLimitTrim, tc.dispatched)
			require.Equal(t, tc.wantCC, gjson.GetBytes(r.requireSent(t), "messages.0.content.0.cache_control").Exists())
		})
	}
}

func TestThinkingCacheControl_OAuthMessageRewrite(t *testing.T) {
	// Some installations replace all message breakpoints and anchor the last
	// block. A native request must still send the client's thinking fields.
	gatewayForwardingCache.Store(&cachedGatewayForwardingSettings{
		rewriteMessageCacheControl: true, expiresAt: time.Now().Add(time.Minute).UnixNano(),
	})
	t.Cleanup(func() { gatewayForwardingCache.Store(&cachedGatewayForwardingSettings{}) })
	body := thinkingTestBody(thinkingTestMessage("assistant", thinkingTestBlock, redactedTestBlock, `{"type":"thinking","thinking":"last"}`))
	for _, path := range []string{"/v1/messages", "/v1/messages/count_tokens"} {
		acc := cacheControlLimitMimicAccount()
		acc.Extra[AccountClaudeCustomizationExtraKey] = map[string]any{ClaudeFieldThinkingCacheControl: ClaudeAlignPassthrough}
		r := forwardThinkingTest(t, path, acc, body, ClaudeAlignReject, ClaudeAlignPassthrough, false)
		sent := r.requireSent(t)
		require.Equal(t, `{"type":"ephemeral"}`, gjson.GetBytes(sent, "messages.0.content.0.cache_control").Raw)
		require.Equal(t, `{"type":"ephemeral"}`, gjson.GetBytes(sent, "messages.0.content.1.cache_control").Raw)
		require.False(t, gjson.GetBytes(sent, "messages.0.content.2.cache_control").Exists())
	}
}

func TestThinkingCacheControl_RejectNativeAccountPaths(t *testing.T) {
	body := thinkingTestBody(thinkingTestMessage("assistant", thinkingTestBlock))
	oauth := cacheControlLimitMimicAccount()
	oauth.Type = AccountTypeOAuth
	for _, acc := range []*Account{fallbackNativeAccountForTest(true), oauth, cacheControlLimitMimicAccount()} {
		acc.Extra[AccountClaudeCustomizationExtraKey] = map[string]any{ClaudeFieldThinkingCacheControl: ClaudeAlignReject}
		for _, path := range []string{"/v1/messages", "/v1/messages/count_tokens"} {
			r := forwardThinkingTest(t, path, acc, body, ClaudeAlignStrip, ClaudeCacheControlLimitTrim, false)
			r.requireRejected(t, "messages.0.content.0.thinking.cache_control: Extra inputs are not permitted")
		}
	}
}

func TestThinkingCacheControl_QueuePing(t *testing.T) {
	original := alignmentQueueConditions
	t.Cleanup(func() { alignmentQueueConditions = original })
	alignmentQueueConditions = append([]alignmentQueueCondition(nil), original...)
	var matches int
	for i, condition := range alignmentQueueConditions {
		if condition.key == ClaudeFieldThinkingCacheControl {
			alignmentQueueConditions[i].match = func(body []byte, header http.Header, rules []BetaPolicyRule) func(ClaudeCustomizationSettings) bool {
				matches++
				return condition.match(body, header, rules)
			}
		}
	}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	settings := &queueAlignmentSettings{}
	s := GatewayService{settingService: NewSettingService(settings, nil), accountRepo: &queueAlignmentAccounts{}}
	check := func(body string) bool {
		return s.AlignmentMayRejectBeforeDispatch(c, []byte(body), c.Request.Header, nil, nil)
	}
	body := thinkingTestBody(thinkingTestMessage("user", redactedTestBlock))
	require.False(t, check(body))
	require.Zero(t, matches)
	settings.value = `{"thinking_cache_control":"reject"}`
	s.settingService = NewSettingService(settings, nil)
	require.True(t, check(body))
	require.Equal(t, 1, matches)
	require.False(t, check(strings.Replace(body, `{"type":"ephemeral"}`, "null", 1)))
	require.False(t, check(strings.Replace(body, "{", `{"unknown":true,`, 1)))
	require.False(t, check(`{"system":[`+thinkingTestBlock+`]}`))
}

func TestThinkingCacheControl_StripRedactedScope(t *testing.T) {
	body := thinkingTestBody(plainTestMessage, thinkingTestMessage("assistant", redactedTestBlock), plainTestMessage)
	for _, tc := range []struct {
		name, path string
		account    *Account
		strip      bool
	}{
		{"managed messages", "/v1/messages", fallbackNativeAccountForTest(false), true},
		{"mimic count tokens", "/v1/messages/count_tokens", cacheControlLimitMimicAccount(), true},
		{"non-mimic count tokens", "/v1/messages/count_tokens", fallbackNativeAccountForTest(false), false},
		{"passthrough messages", "/v1/messages", fallbackNativeAccountForTest(true), false},
		{"passthrough count tokens", "/v1/messages/count_tokens", fallbackNativeAccountForTest(true), false},
		{"other protocol", "/v1/chat/completions", fallbackNativeAccountForTest(false), false},
	} {
		for _, limit := range []string{ClaudeCacheControlLimitTrim, ClaudeAlignPassthrough, ClaudeAlignReject} {
			t.Run(tc.name+"/"+limit, func(t *testing.T) {
				r := forwardThinkingTest(t, tc.path, tc.account, body, ClaudeAlignStrip, limit, false)
				want := redactedTestBlock
				if tc.strip {
					want = `{"type":"redacted_thinking","data":"x"}`
				}
				require.Equal(t, want, gjson.GetBytes(r.requireSent(t), "messages.1.content.0").Raw)
			})
		}
	}
}

func TestThinkingCacheControl_StripRedactedSlotsAndTTL(t *testing.T) {
	const ordinary = `{"type":"text","text":"ordinary","cache_control":{"type":"ephemeral"}}`
	const redacted = `{"type":"redacted_thinking","data":"x","cache_control":{"type":"ephemeral","ttl":"1h"}}`
	body := thinkingTestBody(thinkingTestMessage("assistant", ordinary, ordinary, ordinary, ordinary, redacted))
	body = strings.Replace(body, "{", `{"cache_control":{"type":"ephemeral"},`, 1)
	want := strings.Replace(body, redacted, `{"type":"redacted_thinking","data":"x"}`, 1)
	for _, limit := range []string{ClaudeCacheControlLimitTrim, ClaudeAlignPassthrough} {
		t.Run(limit, func(t *testing.T) {
			expected := want
			if limit == ClaudeCacheControlLimitTrim {
				expected = strings.Replace(expected, ordinary, `{"type":"text","text":"ordinary"}`, 1)
			}
			r := forwardThinkingTest(t, "/v1/messages", fallbackNativeAccountForTest(false), body, ClaudeAlignStrip, limit, false)
			require.Equal(t, expected, string(r.requireSent(t)))
		})
	}
}

func TestThinkingCacheControl_StripDuplicateMembers(t *testing.T) {
	const redacted = `{"type":"redacted_thin\u006bing","data":"cipher\u0074ext", "cache_control":{},"cache_\u0063ontrol":{},"cache_control":null}`
	const stripped = `{"type":"redacted_thin\u006bing","data":"cipher\u0074ext"}`
	body := thinkingTestBody(plainTestMessage, thinkingTestMessage("assistant", redacted), plainTestMessage)
	for _, limit := range []string{ClaudeCacheControlLimitTrim, ClaudeAlignPassthrough, ClaudeAlignReject} {
		t.Run(limit, func(t *testing.T) {
			r := forwardThinkingTest(t, "/v1/messages", fallbackNativeAccountForTest(false), body, ClaudeAlignStrip, limit, false)
			require.Equal(t, strings.Replace(body, redacted, stripped, 1), string(r.requireSent(t)))
			r = forwardThinkingTest(t, "/v1/messages/count_tokens", cacheControlLimitMimicAccount(), body, ClaudeAlignStrip, limit, false)
			require.Equal(t, stripped, gjson.GetBytes(r.requireSent(t), "messages.1.content.0").Raw)
		})
	}

	// The injection-only pass retains the historical first-null gate.
	nullFirst := strings.Replace(redacted, `"cache_control":{}`, `"cache_control":null`, 1)
	r := forwardThinkingTest(t, "/v1/messages/count_tokens", cacheControlLimitMimicAccount(), strings.Replace(body, redacted, nullFirst, 1), ClaudeAlignStrip, ClaudeAlignPassthrough, false)
	require.Equal(t, nullFirst, gjson.GetBytes(r.requireSent(t), "messages.1.content.0").Raw)
}

func TestThinkingCacheControl_StripMimicReinjection(t *testing.T) {
	gatewayForwardingCache.Store(&cachedGatewayForwardingSettings{
		rewriteMessageCacheControl: true, expiresAt: time.Now().Add(time.Minute).UnixNano(),
	})
	t.Cleanup(func() { gatewayForwardingCache.Store(&cachedGatewayForwardingSettings{}) })
	body := thinkingTestBody(thinkingTestMessage("assistant", `{"type":"redacted_thinking","data":"x"}`))
	for _, path := range []string{"/v1/messages", "/v1/messages/count_tokens"} {
		for _, limit := range []string{ClaudeCacheControlLimitTrim, ClaudeAlignPassthrough, ClaudeAlignReject} {
			t.Run(path+"/"+limit, func(t *testing.T) {
				r := forwardThinkingTest(t, path, cacheControlLimitMimicAccount(), body, ClaudeAlignStrip, limit, false)
				require.Equal(t, `{"type":"redacted_thinking","data":"x"}`, gjson.GetBytes(r.requireSent(t), "messages.0.content.0").Raw)
			})
		}
	}
}
