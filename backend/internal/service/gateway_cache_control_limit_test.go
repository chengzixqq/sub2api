//go:build unit

package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const cacheControlLimitUpstream400 = `{"type":"error","error":{"type":"invalid_request_error","message":"A maximum of 4 blocks with cache_control may be provided. Found 5."},"request_id":"req_cache_limit"}`

// Five client breakpoints: one in system, one on a tool, three in messages.
const cacheControlFiveClientBreakpoints = `{"model":"claude-sonnet-4-5","max_tokens":1024,` +
	`"system":[{"type":"text","text":"client system","cache_control":{"type":"ephemeral"}}],` +
	`"tools":[{"name":"lookup","description":"d","input_schema":{"type":"object"},"cache_control":{"type":"ephemeral"}}],` +
	`"messages":[` +
	`{"role":"user","content":[{"type":"text","text":"one","cache_control":{"type":"ephemeral"}}]},` +
	`{"role":"assistant","content":[{"type":"text","text":"two","cache_control":{"type":"ephemeral"}}]},` +
	`{"role":"user","content":[{"type":"text","text":"three","cache_control":{"type":"ephemeral"}}]}` +
	`]}`

// Four client breakpoints (one system, three messages) and a tool without a
// breakpoint. The OAuth mimic adds a system anchor and a tools[-1] breakpoint.
const cacheControlFourClientBreakpoints = `{"model":"claude-sonnet-4-5","max_tokens":1024,` +
	`"system":[{"type":"text","text":"client system instructions","cache_control":{"type":"ephemeral"}}],` +
	`"tools":[{"name":"lookup","description":"d","input_schema":{"type":"object"}}],` +
	`"messages":[` +
	`{"role":"user","content":[{"type":"text","text":"one","cache_control":{"type":"ephemeral"}}]},` +
	`{"role":"assistant","content":[{"type":"text","text":"two","cache_control":{"type":"ephemeral"}}]},` +
	`{"role":"user","content":[{"type":"text","text":"three","cache_control":{"type":"ephemeral"}}]}` +
	`]}`

func cacheControlBreakpointCount(body []byte) int {
	_, messagePaths, toolPaths, systemPaths := collectCacheControlPaths(body, cacheControlLimitPlan{})
	return len(messagePaths) + len(toolPaths) + len(systemPaths)
}

// cacheControlBreakpointTexts returns the text of every message block that
// carries a breakpoint, in order.
func cacheControlBreakpointTexts(body []byte) []string {
	var texts []string
	gjson.GetBytes(body, "messages").ForEach(func(_, msg gjson.Result) bool {
		msg.Get("content").ForEach(func(_, block gjson.Result) bool {
			if block.Get("cache_control").Exists() {
				texts = append(texts, block.Get("text").String())
			}
			return true
		})
		return true
	})
	return texts
}

func requireNoCacheControlMarker(t *testing.T, body []byte) {
	t.Helper()
	require.NotContains(t, string(body), cacheControlClientMarkerKey, "the internal client marker must never reach the upstream")
}

// The default mode must be byte-for-byte the historical enforcement, with or
// without a possible injection.
func TestCacheControlLimitPlan_TrimIsLegacyEnforcement(t *testing.T) {
	bodies := map[string]string{
		"five_breakpoints": cacheControlFiveClientBreakpoints,
		"within_limit":     cacheControlFourClientBreakpoints,
		"thinking_and_ttl": `{"system":[{"type":"text","text":"s","cache_control":{"type":"ephemeral"}}],"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"t","signature":"s","cache_control":{"type":"ephemeral"}},{"type":"text","text":"x","cache_control":{"type":"ephemeral","ttl":"1h"}}]}]}`,
		"unicode_escape":   `{"messages":[{"role":"user","content":[{"type":"text","text":"A","cache_control":{"type":"ephemeral"}},{"type":"text","text":"b","cache_control":{"type":"ephemeral"}},{"type":"text","text":"c","cache_control":{"type":"ephemeral"}},{"type":"text","text":"d","cache_control":{"type":"ephemeral"}},{"type":"text","text":"e","cache_control":{"type":"ephemeral"}}]}]}`,
		"no_breakpoints":   `{"messages":[{"role":"user","content":"hi"}]}`,
	}
	for name, raw := range bodies {
		for _, mode := range []string{ClaudeCacheControlLimitTrim, "", "unknown"} {
			for _, mayInject := range []bool{false, true} {
				body := []byte(raw)
				plan, planned, marked := planCacheControlLimit(body, mode, ClaudeAlignStrip, mayInject, false)
				require.False(t, marked, name)
				require.Equal(t, body, planned, name)
				require.Equal(t, enforceCacheControlLimit([]byte(raw)), plan.apply(planned), "%s mode=%q inject=%v", name, mode, mayInject)
			}
		}
	}
	trimmed := enforceCacheControlLimit([]byte(cacheControlFiveClientBreakpoints))
	require.Equal(t, maxCacheControlBlocks, cacheControlBreakpointCount(trimmed))
}

func TestCacheControlLimitPlan_PassthroughForwardsClientOverLimit(t *testing.T) {
	body := []byte(cacheControlFiveClientBreakpoints)
	for _, mayInject := range []bool{false, true} {
		plan, planned, marked := planCacheControlLimit(body, ClaudeCacheControlLimitPassthrough, ClaudeAlignStrip, mayInject, false)
		require.False(t, marked, "a client over the limit is never marked")
		require.Equal(t, body, planned)
		require.Equal(t, body, plan.apply(planned), "five client breakpoints must be forwarded unchanged (inject=%v)", mayInject)
	}

	// Injection after the decision does not change it: nothing is removed.
	plan, planned, _ := planCacheControlLimit(body, ClaudeCacheControlLimitPassthrough, ClaudeAlignStrip, true, false)
	injected := applyToolsLastCacheBreakpoint(addMessageCacheBreakpoints(planned, false))
	out := plan.apply(injected)
	require.GreaterOrEqual(t, cacheControlBreakpointCount(out), 5)
	requireNoCacheControlMarker(t, out)
}

// Thinking cleanup and TTL order repair are independent of the limit mode.
func TestCacheControlLimitPlan_PassthroughKeepsThinkingCleanupAndTTLOrder(t *testing.T) {
	body := []byte(`{"system":[{"type":"text","text":"s","cache_control":{"type":"ephemeral"}}],` +
		`"tools":[{"name":"a","input_schema":{},"cache_control":{"type":"ephemeral"}}],` +
		`"messages":[` +
		`{"role":"user","content":[{"type":"text","text":"a","cache_control":{"type":"ephemeral"}}]},` +
		`{"role":"assistant","content":[{"type":"thinking","thinking":"t","signature":"sig","cache_control":{"type":"ephemeral"}},{"type":"text","text":"b","cache_control":{"type":"ephemeral"}}]},` +
		`{"role":"user","content":[{"type":"text","text":"c","cache_control":{"type":"ephemeral","ttl":"1h"}}]}` +
		`]}`)
	for _, mayInject := range []bool{false, true} {
		plan, planned, _ := planCacheControlLimit(body, ClaudeCacheControlLimitPassthrough, ClaudeAlignStrip, mayInject, false)
		out := plan.apply(planned)
		require.False(t, gjson.GetBytes(out, "messages.1.content.0.cache_control").Exists(), "thinking cache_control is still removed")
		require.Equal(t, 5, cacheControlBreakpointCount(out), "the five valid client breakpoints stay")
		for _, path := range []string{"tools.0", "system.0", "messages.0.content.0", "messages.1.content.1"} {
			require.Equal(t, "1h", gjson.GetBytes(out, path+".cache_control.ttl").String(), "TTL order is still repaired at %s", path)
		}
		requireNoCacheControlMarker(t, out)
	}
}

// Client within the limit plus injection over the limit: only the injected
// breakpoints are removed. The historical trim removes a client breakpoint.
func TestCacheControlLimitPlan_PassthroughTrimsOnlyInjectedBreakpoints(t *testing.T) {
	client := []byte(cacheControlFourClientBreakpoints)
	plan, planned, marked := planCacheControlLimit(client, ClaudeCacheControlLimitPassthrough, ClaudeAlignStrip, true, false)
	require.True(t, marked)
	require.Equal(t, cacheControlBreakpointCount(client), bytes.Count(planned, cacheControlClientMarkerJSONKey))

	mimic := func(body []byte) []byte {
		system := gjson.GetBytes(body, "system").Value()
		body = rewriteSystemForNonClaudeCode(body, system)
		body, _ = normalizeClaudeOAuthRequestBody(body, "claude-sonnet-4-5", claudeOAuthNormalizeOptions{})
		return applyToolsLastCacheBreakpoint(body)
	}

	injected := mimic(planned)
	require.Greater(t, cacheControlBreakpointCount(injected), maxCacheControlBlocks, "the mimic pushes the request over the limit")
	out := plan.apply(injected)
	requireNoCacheControlMarker(t, out)
	require.Equal(t, maxCacheControlBlocks, cacheControlBreakpointCount(out))
	require.Equal(t, []string{"[System Instructions]\nclient system instructions", "one", "two", "three"}, cacheControlBreakpointTexts(out),
		"every client breakpoint survives, including the relocated system breakpoint")
	require.False(t, gjson.GetBytes(out, "tools.0.cache_control").Exists(), "the injected tools[-1] breakpoint is removed")
	_, _, _, systemPaths := collectCacheControlPaths(out, cacheControlLimitPlan{})
	require.Empty(t, systemPaths, "the injected system anchor is removed")

	legacy := enforceCacheControlLimit(mimic(client))
	require.Equal(t, maxCacheControlBlocks, cacheControlBreakpointCount(legacy))
	require.NotContains(t, cacheControlBreakpointTexts(legacy), "[System Instructions]\nclient system instructions",
		"trim keeps the historical order and drops the earliest client message breakpoint")
}

func TestCacheControlLimitPlan_PassthroughWithoutOverflowOnlyRemovesMarkers(t *testing.T) {
	client := []byte(`{"system":[{"type":"text","text":"s","cache_control":{"type":"ephemeral","ttl":"5m"}}],"messages":[{"role":"user","content":[{"type":"text","text":"a","cache_control":{"type":"ephemeral"}}]}]}`)
	plan, planned, marked := planCacheControlLimit(client, ClaudeCacheControlLimitPassthrough, ClaudeAlignStrip, true, false)
	require.True(t, marked)
	require.Equal(t, client, plan.apply(planned), "markers are removed exactly, leaving the client body byte-identical")
}

// Deferred-tool breakpoints are removed before enforcing the passthrough limit.
func TestCacheControlLimitPlan_DeferredToolBreakpointIsNotCounted(t *testing.T) {
	body := []byte(`{"tools":[{"name":"a","input_schema":{},"defer_loading":true,"cache_control":{"type":"ephemeral"}},{"name":"b","input_schema":{}}],` +
		`"messages":[{"role":"user","content":[` +
		`{"type":"text","text":"1","cache_control":{"type":"ephemeral"}},{"type":"text","text":"2","cache_control":{"type":"ephemeral"}},` +
		`{"type":"text","text":"3","cache_control":{"type":"ephemeral"}},{"type":"text","text":"4","cache_control":{"type":"ephemeral"}}]}]}`)
	require.Equal(t, 4, countLegacyCacheControlBreakpoints(body, cacheControlLimitPlan{}))
	plan, planned, marked := planCacheControlLimit(body, ClaudeCacheControlLimitPassthrough, ClaudeAlignStrip, true, false)
	require.True(t, marked)
	require.Equal(t, cacheControlTrimInjectedOnly, plan.scope)

	out := plan.apply(applyToolsLastCacheBreakpoint(planned))
	requireNoCacheControlMarker(t, out)
	require.Equal(t, 4, cacheControlBreakpointCount(out))
	require.Equal(t, []string{"1", "2", "3", "4"}, cacheControlBreakpointTexts(out))
}

// A client-owned marker name survives the mark/unmark round trip verbatim.
func TestCacheControlLimitPlan_ClientMarkerNameRoundTrip(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"a","cache_control":{"type":"ephemeral","` + cacheControlClientMarkerKey + `":true}}]}]}`)
	plan, planned, marked := planCacheControlLimit(body, ClaudeCacheControlLimitPassthrough, ClaudeAlignStrip, true, false)
	require.True(t, marked)
	require.NotEqual(t, cacheControlClientMarkerKey, plan.markerKey)
	require.Equal(t, cacheControlTrimInjectedOnly, plan.scope)
	require.Equal(t, body, plan.apply(planned))
}

func TestStripRemainingCacheControlClientMarkers(t *testing.T) {
	marker := `"` + cacheControlClientMarkerKey + `":true`
	for _, tc := range []struct{ in, want string }{
		{`{"cache_control":{"type":"ephemeral",` + marker + `}}`, `{"cache_control":{"type":"ephemeral"}}`},
		{`{"cache_control":{` + marker + `,"type":"ephemeral"}}`, `{"cache_control":{"type":"ephemeral"}}`},
		{`{"cache_control":{` + marker + `}}`, `{"cache_control":{}}`},
		{`{"x":1}`, `{"x":1}`},
	} {
		require.Equal(t, tc.want, string(stripRemainingCacheControlClientMarkers([]byte(tc.in), cacheControlClientMarkerKey)))
	}
}

func cacheControlLimitPolicy(mode string) ClaudeCustomizationSettings {
	settings := DefaultClaudeCustomizationSettings()
	settings.CacheControlLimitMode = mode
	return settings
}

func cacheControlLimitResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}
}

const cacheControlLimitOKMessage = `{"id":"msg_cc","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":1}}`

func forwardCacheControlLimitTest(t *testing.T, svc *GatewayService, account *Account, body string) *httptest.ResponseRecorder {
	t.Helper()
	parsed, err := ParseGatewayRequest(NewRequestBodyRef([]byte(body)), PlatformAnthropic)
	require.NoError(t, err)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	_, _ = svc.Forward(context.Background(), c, account, parsed)
	return rec
}

// /v1/messages on a managed Anthropic API-key account: the mode, global or
// account override, decides whether five client breakpoints are trimmed.
func TestForward_CacheControlLimitModeOnNativeMessages(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name      string
		global    string
		override  any
		wantCount int
	}{
		{"default_trim", ClaudeCacheControlLimitTrim, nil, 4},
		{"global_passthrough", ClaudeCacheControlLimitPassthrough, nil, 5},
		{"account_passthrough_over_global_trim", ClaudeCacheControlLimitTrim, "passthrough", 5},
		{"account_trim_over_global_passthrough", ClaudeCacheControlLimitPassthrough, "trim", 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, response := http.StatusOK, cacheControlLimitOKMessage
			if tc.wantCount > maxCacheControlBlocks {
				status, response = http.StatusBadRequest, cacheControlLimitUpstream400
			}
			upstream := &fallbackSequenceUpstream{responses: []*http.Response{cacheControlLimitResponse(status, response)}}
			svc := fallbackNativeGatewayForTestWithPolicy(t, upstream, nil, cacheControlLimitPolicy(tc.global))
			account := fallbackNativeAccountForTest(false)
			if tc.override != nil {
				account.Extra[AccountClaudeCustomizationExtraKey] = map[string]any{"cache_control_limit_mode": tc.override}
			}

			rec := forwardCacheControlLimitTest(t, svc, account, cacheControlFiveClientBreakpoints)

			require.Len(t, upstream.bodies, 1)
			require.Equal(t, tc.wantCount, cacheControlBreakpointCount(upstream.bodies[0]))
			requireNoCacheControlMarker(t, upstream.bodies[0])
			if tc.wantCount > maxCacheControlBlocks {
				require.Equal(t, http.StatusBadRequest, rec.Code, "the upstream 400 must reach the client")
				require.JSONEq(t, cacheControlLimitUpstream400, rec.Body.String())
			} else {
				require.Equal(t, http.StatusOK, rec.Code)
			}
		})
	}
}

// The automatic passthrough path (anthropic_passthrough=true) has never
// trimmed breakpoints; both modes leave it unchanged.
func TestForward_AnthropicAPIKeyPassthroughNeverTrims(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, mode := range []string{ClaudeCacheControlLimitTrim, ClaudeCacheControlLimitPassthrough} {
		upstream := &fallbackSequenceUpstream{responses: []*http.Response{cacheControlLimitResponse(http.StatusBadRequest, cacheControlLimitUpstream400)}}
		svc := fallbackNativeGatewayForTestWithPolicy(t, upstream, nil, cacheControlLimitPolicy(mode))
		rec := forwardCacheControlLimitTest(t, svc, fallbackNativeAccountForTest(true), cacheControlFiveClientBreakpoints)
		require.Len(t, upstream.bodies, 1)
		require.Equal(t, 5, cacheControlBreakpointCount(upstream.bodies[0]), mode)
		require.Equal(t, http.StatusBadRequest, rec.Code, mode)
	}
}

func cacheControlLimitMimicAccount() *Account {
	return &Account{
		ID: 906, Name: "cache-limit-setup-token", Platform: PlatformAnthropic, Type: AccountTypeSetupToken,
		Concurrency: 1, Credentials: map[string]any{"access_token": "test-oauth-token"},
		Extra: map[string]any{}, Status: StatusActive, Schedulable: true,
	}
}

// OAuth mimic on /v1/messages: Sub2API injects a system anchor and a tools[-1]
// breakpoint. Under passthrough, injection never pushes a client within the
// limit over it, and a client over the limit still gets the upstream 400.
func TestForward_CacheControlLimitPassthroughWithOAuthMimicInjection(t *testing.T) {
	gin.SetMode(gin.TestMode)
	gatewayForwardingCache.Store(&cachedGatewayForwardingSettings{})
	t.Cleanup(func() { gatewayForwardingCache.Store(&cachedGatewayForwardingSettings{}) })

	t.Run("client_within_limit_keeps_every_client_breakpoint", func(t *testing.T) {
		upstream := &fallbackSequenceUpstream{responses: []*http.Response{cacheControlLimitResponse(http.StatusOK, cacheControlLimitOKMessage)}}
		svc := fallbackNativeGatewayForTestWithPolicy(t, upstream, nil, cacheControlLimitPolicy(ClaudeCacheControlLimitPassthrough))
		rec := forwardCacheControlLimitTest(t, svc, cacheControlLimitMimicAccount(), cacheControlFourClientBreakpoints)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		require.Len(t, upstream.bodies, 1)
		sent := upstream.bodies[0]
		requireNoCacheControlMarker(t, sent)
		require.Equal(t, maxCacheControlBlocks, cacheControlBreakpointCount(sent))
		require.Equal(t, []string{"[System Instructions]\nclient system instructions", "one", "two", "three"}, cacheControlBreakpointTexts(sent))
	})

	t.Run("trim_mode_is_unchanged", func(t *testing.T) {
		upstream := &fallbackSequenceUpstream{responses: []*http.Response{cacheControlLimitResponse(http.StatusOK, cacheControlLimitOKMessage)}}
		svc := fallbackNativeGatewayForTestWithPolicy(t, upstream, nil, cacheControlLimitPolicy(ClaudeCacheControlLimitTrim))
		rec := forwardCacheControlLimitTest(t, svc, cacheControlLimitMimicAccount(), cacheControlFourClientBreakpoints)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		require.Len(t, upstream.bodies, 1)
		require.Equal(t, maxCacheControlBlocks, cacheControlBreakpointCount(upstream.bodies[0]))
		require.NotContains(t, cacheControlBreakpointTexts(upstream.bodies[0]), "[System Instructions]\nclient system instructions")
	})

	t.Run("client_over_limit_reaches_upstream_untrimmed", func(t *testing.T) {
		upstream := &fallbackSequenceUpstream{responses: []*http.Response{cacheControlLimitResponse(http.StatusBadRequest, cacheControlLimitUpstream400)}}
		svc := fallbackNativeGatewayForTestWithPolicy(t, upstream, nil, cacheControlLimitPolicy(ClaudeCacheControlLimitPassthrough))
		rec := forwardCacheControlLimitTest(t, svc, cacheControlLimitMimicAccount(), cacheControlFiveClientBreakpoints)
		require.Len(t, upstream.bodies, 1)
		require.Greater(t, cacheControlBreakpointCount(upstream.bodies[0]), maxCacheControlBlocks)
		requireNoCacheControlMarker(t, upstream.bodies[0])
		require.Equal(t, http.StatusBadRequest, rec.Code)
		require.Contains(t, rec.Body.String(), "A maximum of 4 blocks with cache_control")
	})
}

func countTokensCacheControlLimitTest(t *testing.T, mode, body string, status int, response string) ([]byte, *httptest.ResponseRecorder) {
	t.Helper()
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", nil)
	c.Request.Header.Set("User-Agent", "third-party-client/1.0")
	upstream := &anthropicHTTPUpstreamRecorder{resp: cacheControlLimitResponse(status, response)}
	settings := cacheControlLimitPolicy(mode)
	settings.Preset = ClaudePresetCustom
	raw, err := json.Marshal(settings)
	require.NoError(t, err)
	cfg := &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}}
	svc := &GatewayService{
		cfg:              cfg,
		httpUpstream:     upstream,
		rateLimitService: &RateLimitService{},
		settingService:   NewSettingService(&fallbackPolicySettingRepo{values: map[string]string{SettingKeyClaudeCustomization: string(raw)}}, cfg),
	}
	parsed := &ParsedRequest{Body: NewRequestBodyRef([]byte(body)), Model: "claude-sonnet-4-5"}
	_ = svc.ForwardCountTokens(context.Background(), c, cacheControlLimitMimicAccount(), parsed)
	return upstream.lastBody, rec
}

func TestForwardCountTokens_CacheControlLimitMode(t *testing.T) {
	gin.SetMode(gin.TestMode)
	gatewayForwardingCache.Store(&cachedGatewayForwardingSettings{})
	t.Cleanup(func() { gatewayForwardingCache.Store(&cachedGatewayForwardingSettings{}) })

	// count_tokens does not rewrite system; it injects tools[-1]. Four client
	// breakpoints plus that injection exceed the limit.
	sent, rec := countTokensCacheControlLimitTest(t, ClaudeCacheControlLimitPassthrough, cacheControlFourClientBreakpoints, http.StatusOK, `{"input_tokens":42}`)
	require.Equal(t, http.StatusOK, rec.Code)
	requireNoCacheControlMarker(t, sent)
	require.Equal(t, maxCacheControlBlocks, cacheControlBreakpointCount(sent))
	require.True(t, gjson.GetBytes(sent, "system.0.cache_control").Exists(), "the client system breakpoint survives")
	require.Equal(t, []string{"one", "two", "three"}, cacheControlBreakpointTexts(sent))
	require.False(t, gjson.GetBytes(sent, "tools.0.cache_control").Exists(), "only the injected tools[-1] breakpoint is removed")

	sent, rec = countTokensCacheControlLimitTest(t, ClaudeCacheControlLimitPassthrough, cacheControlFiveClientBreakpoints, http.StatusBadRequest, cacheControlLimitUpstream400)
	require.Equal(t, 5, cacheControlBreakpointCount(sent), "five client breakpoints reach the upstream")
	require.Equal(t, http.StatusBadRequest, rec.Code, "the upstream 400 status reaches the client")

	sent, rec = countTokensCacheControlLimitTest(t, ClaudeCacheControlLimitTrim, cacheControlFiveClientBreakpoints, http.StatusOK, `{"input_tokens":42}`)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, maxCacheControlBlocks, cacheControlBreakpointCount(sent), "trim keeps the historical behavior")
}

// system/tool combinations reproduce the probed upstream's responses from 2026-11-20.
func clientCacheControlTestBody(system, tools int, top bool) string {
	block := map[string]string{"type": "ephemeral"}
	var systems, toolDefs []map[string]any
	for range system {
		systems = append(systems, map[string]any{"type": "text", "text": "system", "cache_control": block})
	}
	for i := range tools {
		toolDefs = append(toolDefs, map[string]any{"name": fmt.Sprintf("tool%d", i), "input_schema": map[string]string{"type": "object"}, "cache_control": block})
	}
	body := map[string]any{"model": "claude-sonnet-4-5", "messages": []map[string]string{{"role": "user", "content": "hi"}}, "max_tokens": 5, "system": systems}
	if tools > 0 {
		body["tools"] = toolDefs
	}
	if top {
		body["cache_control"] = block
	}
	raw, _ := json.Marshal(body)
	return string(raw)
}

func TestCacheControlLimit_RejectGolden(t *testing.T) {
	for _, tc := range []struct {
		name, body, message string
	}{
		{"five system", clientCacheControlTestBody(5, 0, false), "A maximum of 4 blocks with cache_control may be provided. Found 5."},
		{"four system and top", clientCacheControlTestBody(4, 0, true), "A maximum of 4 blocks with cache_control may be provided. Found 5."},
		{"three system and two tools", clientCacheControlTestBody(3, 2, false), "A maximum of 4 blocks with cache_control may be provided. Found 5."},
		{"four system and deferred tool", strings.Replace(clientCacheControlTestBody(4, 1, false), `"name":"tool0"`, `"name":"tool0","defer_loading":true`, 1), "A maximum of 4 blocks with cache_control may be provided. Found 5."},
		{"escaped cache key", strings.Replace(clientCacheControlTestBody(5, 0, false), `"cache_control"`, `"cache_\u0063ontrol"`, 1), "A maximum of 4 blocks with cache_control may be provided. Found 5."},
		{"five system and top", clientCacheControlTestBody(5, 0, true), "A maximum of 4 blocks with cache_control may be provided. Found 6."},
	} {
		for _, path := range []string{"/v1/messages", "/v1/messages/count_tokens"} {
			for _, account := range []*Account{fallbackNativeAccountForTest(false), fallbackNativeAccountForTest(true), cacheControlLimitMimicAccount()} {
				t.Run(tc.name+path+"/"+account.Name, func(t *testing.T) {
					up := &fallbackSequenceUpstream{responses: []*http.Response{cacheControlLimitResponse(http.StatusOK, cacheControlLimitOKMessage)}}
					s := fallbackNativeGatewayForTestWithPolicy(t, up, nil, cacheControlLimitPolicy(ClaudeAlignReject))
					rec := httptest.NewRecorder()
					c, _ := gin.CreateTestContext(rec)
					c.Request = httptest.NewRequest(http.MethodPost, path, nil)
					parsed, err := ParseGatewayRequest(NewRequestBodyRef([]byte(tc.body)), PlatformAnthropic)
					require.NoError(t, err)
					if path == "/v1/messages" {
						_, err = s.Forward(t.Context(), c, account, parsed)
					} else {
						err = s.ForwardCountTokens(t.Context(), c, account, parsed)
					}
					require.Empty(t, up.bodies)
					require.ErrorIs(t, err, ErrLocalAnthropicRejected)
					require.Equal(t, http.StatusBadRequest, rec.Code)
					id := rec.Header().Get("request-id")
					require.Regexp(t, `^req_011C[1-9A-HJ-NP-Za-km-z]{20}$`, id)
					require.Equal(t, `{"type":"error","error":{"type":"invalid_request_error","message":"`+tc.message+`"},"request_id":"`+id+`"}`, rec.Body.String())
				})
			}
		}
	}
}

func TestCacheControlLimit_RejectIgnoresInjected(t *testing.T) {
	up := &fallbackSequenceUpstream{responses: []*http.Response{cacheControlLimitResponse(http.StatusOK, cacheControlLimitOKMessage)}}
	s := fallbackNativeGatewayForTestWithPolicy(t, up, nil, cacheControlLimitPolicy(ClaudeAlignReject))
	rec := forwardCacheControlLimitTest(t, s, cacheControlLimitMimicAccount(), cacheControlFourClientBreakpoints)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Len(t, up.bodies, 1)
	require.Equal(t, 4, cacheControlBreakpointCount(up.bodies[0]))
	require.Equal(t, []string{"[System Instructions]\nclient system instructions", "one", "two", "three"}, cacheControlBreakpointTexts(up.bodies[0]))
	requireNoCacheControlMarker(t, up.bodies[0])
}

func TestCacheControlLimit_RejectIgnoresThinking(t *testing.T) {
	body := []byte(`{"cache_control":{"type":"ephemeral"},"system":[{"type":"text","text":"a","cache_control":{}}],"tools":[{"name":"regular","cache_control":{}},{"name":"deferred","defer_loading":true,"cache_control":{}}],"messages":[{"role":"assistant","content":[{"type":"text","text":"answer","cache_control":{}},{"type":"thinking","cache_control":{}},{"type":"redacted_thinking","cache_control":{}}]}]}`)
	require.Equal(t, 5, countClientCacheControlBreakpoints(body))
	body = bytes.Replace(body, []byte(`"cache_control":{"type":"ephemeral"},`), nil, 1)
	require.Equal(t, 4, countClientCacheControlBreakpoints(body))
}

func TestCacheControlLimit_RejectGoldenNullForwards(t *testing.T) {
	const nullBlock = `{"type":"text","text":"hi","cache_control":null}`
	body := clientCacheControlTestBody(4, 0, false)
	for name, raw := range map[string]string{
		"message blocks": strings.Replace(body, `"content":"hi"`, `"content":[`+nullBlock+`,`+nullBlock+`]`, 1),
		"system block":   strings.Replace(body, `"system":[`, `"system":[`+nullBlock+`,`, 1),
		"top level":      strings.Replace(body, "{", `{"cache_control":null,`, 1),
		"tool":           strings.Replace(body, "{", `{"tools":[{"name":"lookup","input_schema":{"type":"object"},"cache_control":null}],`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			up := &fallbackSequenceUpstream{responses: []*http.Response{cacheControlLimitResponse(http.StatusOK, cacheControlLimitOKMessage)}}
			s := fallbackNativeGatewayForTestWithPolicy(t, up, nil, cacheControlLimitPolicy(ClaudeAlignReject))
			rec := forwardCacheControlLimitTest(t, s, fallbackNativeAccountForTest(false), raw)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			require.Len(t, up.bodies, 1)
			require.Equal(t, 4, countClientCacheControlBreakpoints(up.bodies[0]))
		})
	}
}

func TestCacheControlLimit_RejectDefersToUnknownField(t *testing.T) {
	body := strings.Replace(cacheControlFiveClientBreakpoints, "{", `{"extra_input":true,`, 1)
	up := &fallbackSequenceUpstream{responses: []*http.Response{cacheControlLimitResponse(http.StatusBadRequest, `{"type":"error","error":{"type":"invalid_request_error","message":"extra_input: Extra inputs are not permitted"}}`)}}
	s := fallbackNativeGatewayForTestWithPolicy(t, up, nil, cacheControlLimitPolicy(ClaudeAlignReject))
	rec := forwardCacheControlLimitTest(t, s, fallbackNativeAccountForTest(false), body)
	require.Len(t, up.bodies, 1)
	require.Equal(t, 5, cacheControlBreakpointCount(up.bodies[0]))
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "extra_input: Extra inputs are not permitted")
}

func TestCacheControlLimit_RejectScopeAndPreviousDispatch(t *testing.T) {
	for _, tc := range []struct {
		path       string
		dispatched bool
		wantCount  int
	}{
		{"/v1/chat/completions", false, 4},
		{"/v1/messages", true, 5},
	} {
		t.Run(tc.path, func(t *testing.T) {
			up := &fallbackSequenceUpstream{responses: []*http.Response{cacheControlLimitResponse(http.StatusOK, cacheControlLimitOKMessage)}}
			s := fallbackNativeGatewayForTestWithPolicy(t, up, nil, cacheControlLimitPolicy(ClaudeAlignReject))
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, tc.path, nil)
			if tc.dispatched {
				clock := InstallOpsUpstreamAttemptClock(c)
				MarkOpsUpstreamAttemptStart(c.Request.Context(), 19)
				// The previous account's failure has already consumed its timing event.
				require.Positive(t, clock.claim(19, 0))
			}
			parsed, err := ParseGatewayRequest(NewRequestBodyRef([]byte(cacheControlFiveClientBreakpoints)), PlatformAnthropic)
			require.NoError(t, err)
			_, err = s.Forward(c.Request.Context(), c, fallbackNativeAccountForTest(false), parsed)
			require.NoError(t, err)
			require.Equal(t, http.StatusOK, rec.Code)
			require.Len(t, up.bodies, 1)
			require.Equal(t, tc.wantCount, cacheControlBreakpointCount(up.bodies[0]))
		})
	}
}

func TestMimicPreservesClientCacheSlots(t *testing.T) {
	gatewayForwardingCache.Store(&cachedGatewayForwardingSettings{})
	t.Cleanup(func() { gatewayForwardingCache.Store(&cachedGatewayForwardingSettings{}) })
	nullBlock := `{"type":"text","text":"unused","cache_control":null}`
	cases := []struct {
		name, body   string
		top, hasNull bool
		first        string
	}{
		{"null", strings.Replace(cacheControlFourClientBreakpoints, `"content":[`, `"content":[`+nullBlock+",", 1), false, true, "one"},
		{"top", strings.Replace(strings.Replace(cacheControlFourClientBreakpoints, `"model":`, `"cache_control":{"type":"ephemeral"},"model":`, 1), `"text":"three","cache_control":{"type":"ephemeral"}`, `"text":"three"`, 1), true, false, "one"},
		{"marker_text", strings.Replace(cacheControlFourClientBreakpoints, `"text":"one"`, `"text":"`+cacheControlClientMarkerKey+`"`, 1), false, false, cacheControlClientMarkerKey},
	}
	for _, tc := range cases {
		for _, mode := range []string{ClaudeCacheControlLimitPassthrough, ClaudeAlignReject} {
			for _, accountType := range []string{AccountTypeSetupToken, AccountTypeOAuth} {
				for _, path := range []string{"/v1/messages", "/v1/messages/count_tokens"} {
					t.Run(tc.name+"/"+mode+"/"+accountType+path, func(t *testing.T) {
						response := cacheControlLimitOKMessage
						counting := strings.HasSuffix(path, "count_tokens")
						if counting {
							response = `{"input_tokens":42}`
						}
						up := &fallbackSequenceUpstream{responses: []*http.Response{cacheControlLimitResponse(http.StatusOK, response)}}
						svc := fallbackNativeGatewayForTestWithPolicy(t, up, nil, cacheControlLimitPolicy(mode))
						account := cacheControlLimitMimicAccount()
						account.Type = accountType
						rec := httptest.NewRecorder()
						c, _ := gin.CreateTestContext(rec)
						c.Request = httptest.NewRequest(http.MethodPost, path, nil)
						c.Request.Header.Set("User-Agent", "third-party-client/1.0")
						parsed, err := ParseGatewayRequest(NewRequestBodyRef([]byte(tc.body)), PlatformAnthropic)
						require.NoError(t, err)
						if counting {
							err = svc.ForwardCountTokens(t.Context(), c, account, parsed)
						} else {
							_, err = svc.Forward(t.Context(), c, account, parsed)
						}
						require.NoError(t, err)
						require.Equal(t, http.StatusOK, rec.Code)
						require.Len(t, up.bodies, 1)
						sent := up.bodies[0]
						require.Equal(t, 4, parsedClientCacheControlCount(sent), "%s", sent)
						require.Equal(t, tc.top, gjson.GetBytes(sent, "cache_control").IsObject())
						require.False(t, gjson.GetBytes(sent, "tools.0.cache_control").Exists())
						require.NotContains(t, string(sent), cacheControlClientMarkerKey+`":true`)
						want := []string{tc.first, "two"}
						if !tc.top {
							want = append(want, "three")
						}
						if counting {
							require.True(t, gjson.GetBytes(sent, "system.0.cache_control").IsObject())
						} else {
							want = append([]string{"[System Instructions]\nclient system instructions"}, want...)
						}
						var actual []string
						foundNull := false
						gjson.GetBytes(sent, "messages").ForEach(func(_, m gjson.Result) bool {
							m.Get("content").ForEach(func(_, b gjson.Result) bool {
								cc := b.Get("cache_control")
								if cc.IsObject() {
									actual = append(actual, b.Get("text").String())
								}
								if b.Get("text").String() == "unused" {
									foundNull = cc.Exists() && cc.Type == gjson.Null
								}
								return true
							})
							return true
						})
						require.Equal(t, want, actual)
						require.Equal(t, tc.hasNull, foundNull)
					})
				}
			}
		}
	}
}

func TestCacheControlNullIsNeverMarked(t *testing.T) {
	body := []byte(`{"messages":[{"content":[{"type":"text","cache_control":null},{"type":"text","cache_control":{"type":"ephemeral"}}]}]}`)
	_, planned, marked := planCacheControlLimit(body, ClaudeAlignPassthrough, ClaudeAlignStrip, true, false)
	require.True(t, marked)
	require.Equal(t, "null", gjson.GetBytes(planned, "messages.0.content.0.cache_control").Raw)
}

func TestTrimInjectedRemovesClientMarkerDirectly(t *testing.T) {
	body := []byte(`{"tools":[{"cache_control":{"type":"ephemeral","` + cacheControlClientMarkerKey + `":true}}]}`)
	out := trimInjectedCacheControlBreakpoints(body, cacheControlLimitPlan{markerKey: cacheControlClientMarkerKey})
	require.Equal(t, `{"tools":[{"cache_control":{"type":"ephemeral"}}]}`, string(out))
}

func TestCacheControlPlanCleansRelocatedMarker(t *testing.T) {
	body := []byte(`{"tools":[{"cache_control":{"type":"ephemeral"}}]}`)
	plan, planned, marked := planCacheControlLimit(body, ClaudeAlignPassthrough, ClaudeAlignStrip, true, false)
	require.True(t, marked)
	relocated := bytes.Replace(planned, []byte(`"tools"`), []byte(`"extension"`), 1)
	require.Equal(t, `{"extension":[{"cache_control":{"type":"ephemeral"}}]}`, string(plan.apply(relocated)))
}

func TestForwardCountTokens_CacheControlLimitWireDuplicates(t *testing.T) {
	const regular = `{"type":"text","text":"prefix","cache_control":{"type":"ephemeral"}}`
	const duplicate = `{"type":"text","text":"tail","cache_control": null ,"cache_\u0063ontrol":null,"cache_control":true}`
	const stripped = `{"type":"text","text":"tail" }` // Batch 18 retains the trailing space.
	const top = `,"cache_control":{"type":"ephemeral"}`
	base := `{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"hi"}],"system":[` + regular + "," + regular + "," + regular
	tools := `{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"hi"}],"tools":[` +
		`{"name":"first","input_schema":{"type":"object"},"cache_control":{}},` +
		`{"name":"second","input_schema":{"type":"object"},"cache_control":{}},` +
		`{"name":"third","input_schema":{"type":"object"},"cache_control":{}},`
	const tool = `{"name":"last","input_schema":{"type":"object"}`
	const duplicateMembers = `,"cache_control":null,"cache_control":{"type":"ephemeral"}`
	fourTools := tools + tool + duplicateMembers + `}]}`
	fourTools = strings.TrimSuffix(fourTools, "}") + top + "}"
	for _, tc := range []struct{ name, body, want string }{
		{"tools", fourTools, strings.Replace(fourTools, tool+duplicateMembers, tool, 1)},
		{"system", base + "," + duplicate + "]" + top + "}", base + "," + stripped + "]" + top + "}"},
		{"messages", strings.Replace(base, `"content":"hi"`, `"content":[`+duplicate+`]`, 1) + "]" + top + "}",
			strings.Replace(base, `"content":"hi"`, `"content":[`+stripped+`]`, 1) + "]" + top + "}"},
		{"escaped first", strings.Replace(fourTools, tool+duplicateMembers, tool+`,"cache\u005fcontrol":null,"cache_control":false`, 1),
			strings.Replace(fourTools, tool+duplicateMembers, tool, 1)},
		{"five wire blocks", base + "," + regular + "," + duplicate + "]" + top + "}", ""},
		{"top absent", base + "," + duplicate + "]}", ""},
		{"top null", base + "," + duplicate + `],"cache_control":null}`, ""},
		{"end null", base + `,{"type":"text","text":"null","cache_control":null}]` + top + "}", ""},
		{"all null", base + "," + strings.Replace(duplicate, ":true", ":null", 1) + "]" + top + "}", ""},
		{"nested only", base + `,{"type":"text","cache_control":null,"text":"\"cache_control\":true","extra":{"cache_control":true},"another":[[{}]]}]` + top + "}", ""},
		{"first real", base + "," + strings.Replace(duplicate, ": null ", ":1", 1) + "]" + top + "}", base + "," + strings.Replace(stripped, " }", "}", 1) + "]" + top + "}"},
		{"deferred", strings.Replace(fourTools, tool, tool+`,"defer_loading":true`, 1), strings.Replace(fourTools, tool+duplicateMembers, tool+`,"defer_loading":true,"cache_control":{"type":"ephemeral"}`, 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := forwardThinkingTest(t, "/v1/messages/count_tokens", fallbackNativeAccountForTest(false), tc.body, ClaudeAlignStrip, ClaudeCacheControlLimitTrim, false)
			want := tc.want
			if want == "" {
				want = tc.body
			}
			require.Equal(t, want, string(r.requireSent(t)))
			require.Equal(t, `{"input_tokens":3}`, r.Body.String())
		})
	}

	t.Run("passthrough", func(t *testing.T) {
		r := forwardThinkingTest(t, "/v1/messages/count_tokens", fallbackNativeAccountForTest(true), fourTools, ClaudeAlignStrip, ClaudeCacheControlLimitTrim, false)
		require.Equal(t, fourTools, string(r.requireSent(t)))
	})
	t.Run("client reject counts first value", func(t *testing.T) {
		r := forwardThinkingTest(t, "/v1/messages/count_tokens", fallbackNativeAccountForTest(false), fourTools, ClaudeAlignStrip, ClaudeAlignReject, false)
		require.Equal(t, fourTools, string(r.requireSent(t)))
	})
}

func TestCacheControlLimit_WireDuplicatesUseMessageOrder(t *testing.T) {
	const duplicate = `{"type":"text","text":"first","cache_control":null,"cache_control":{}}`
	const regular = `{"type":"text","text":"other","cache_control":{}}`
	const stripped = `{"type":"text","text":"first"}`
	body := `{"model":"claude-sonnet-4-5","cache_control":{},"messages":[` +
		thinkingTestMessage("user", duplicate, regular, regular, regular) + `]}`
	for _, tc := range []struct {
		path    string
		account *Account
	}{
		{"/v1/messages", fallbackNativeAccountForTest(false)},
		{"/v1/messages/count_tokens", cacheControlLimitMimicAccount()},
	} {
		t.Run(tc.path, func(t *testing.T) {
			r := forwardThinkingTest(t, tc.path, tc.account, body, ClaudeAlignStrip, ClaudeCacheControlLimitTrim, false)
			sent := r.requireSent(t)
			require.Equal(t, stripped, gjson.GetBytes(sent, "messages.0.content.0").Raw)
			for i := 1; i <= 3; i++ {
				require.Equal(t, regular, gjson.GetBytes(sent, fmt.Sprintf("messages.0.content.%d", i)).Raw)
			}
		})
	}
}

func TestCacheControlLimitPlan_RedactedThinkingTextDoesNotAllocate(t *testing.T) {
	plan := cacheControlLimitPlan{nativeManaged: true}
	for _, tc := range []struct{ name, block string }{
		{"text value", `{"type":"text","text":"redacted_thinking"}`},
		{"quoted text", `{"type":"text","text":"a \"redacted_thinking\" block"}`},
		{"escaped quote in key", `{"type":"text","custom\"type":"redacted_thinking"}`},
		{"array element", `{"type":"text","extra":["type","redacted_thinking"]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(`{"cache_control":{"type":"ephemeral"},"messages":[{"content":[` + tc.block + `]}]}`)
			var out []byte
			allocs := testing.AllocsPerRun(100, func() { out = plan.apply(body) })
			require.Equal(t, body, out)
			require.Zero(t, allocs)
		})
	}
}

func TestCacheControlLimitPlan_StripsRedactedThinkingBelowLimit(t *testing.T) {
	const thinking = `{"type":"thinking","thinking":"work"}`
	const redacted = `{"type":"redacted_thinking","data":"opaque"}`
	const breakpoint = `,"cache_control":{"type":"ephemeral"}`
	const text = `{"type":"text","text":"redacted_thinking"}`
	plan := cacheControlLimitPlan{nativeManaged: true}
	for _, tc := range []struct{ name, blocks, want string }{
		{"type first", `{"type" ` + "\t:\r\n " + `"redacted_thinking","data":"opaque"` + breakpoint + `}`,
			`{"type" ` + "\t:\r\n " + `"redacted_thinking","data":"opaque"}`},
		{"cache control first after text", text + `,{"cache_control":{"type":"ephemeral"},"type":"redacted_thinking","data":"opaque"}`, text + "," + redacted},
		{"both thinking types", strings.TrimSuffix(thinking, "}") + breakpoint + `},` + strings.TrimSuffix(redacted, "}") + breakpoint + `}`, thinking + "," + redacted},
		{"escaped type", `{"typ\u0065":"redacted_thinking","data":"opaque"` + breakpoint + `}`, `{"typ\u0065":"redacted_thinking","data":"opaque"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const prefix = `{"messages":[{"content":[`
			const suffix = `]}]}`
			require.Equal(t, prefix+tc.want+suffix, string(plan.apply([]byte(prefix+tc.blocks+suffix))))
		})
	}
}
