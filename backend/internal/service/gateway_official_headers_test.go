//go:build unit

package service

import (
	"bytes"
	"fmt"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const officialTestError = `{"type":"error","error":{"type":"invalid_request_error","message":"bad field"}}`
const officialIDPattern = "^req_011C[1-9A-HJ-NP-Za-km-z]{20}$"

func forwardOfficialResponse(t *testing.T, path string, account *Account, policy ClaudeCustomizationSettings, cfg *config.Config, resp *http.Response) *httptest.ResponseRecorder {
	t.Helper()
	up := &fallbackSequenceUpstream{responses: []*http.Response{resp}}
	s := fallbackNativeGatewayForTestWithPolicy(t, up, cfg, policy)
	if path == "/v1/messages/count_tokens" {
		s.rateLimitService = nil
	}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, path, nil)
	body := thinkingTestBody(plainTestMessage)
	if resp.Header.Get("Content-Type") == "text/event-stream" {
		body = strings.Replace(body, "{", `{"stream":true,`, 1)
	}
	parsed, err := ParseGatewayRequest(NewRequestBodyRef([]byte(body)), PlatformAnthropic)
	require.NoError(t, err)
	if path == "/v1/messages/count_tokens" {
		err = s.ForwardCountTokens(c.Request.Context(), c, account, parsed)
	} else {
		_, err = s.Forward(c.Request.Context(), c, account, parsed)
	}
	if resp.StatusCode < 400 && !parsed.Stream {
		require.NoError(t, err)
	}
	require.Equal(t, resp.StatusCode, rec.Code)
	return rec
}

func TestRequestIDFill_200(t *testing.T) {
	for _, path := range []string{"/v1/messages", "/v1/messages/count_tokens"} {
		for _, pass := range []bool{true, false} {
			for _, upstreamID := range []string{"req_official", ""} {
				policy := DefaultClaudeCustomizationSettings()
				account := fallbackNativeAccountForTest(pass)
				account.Extra[AccountClaudeCustomizationExtraKey] = map[string]any{ClaudeFieldRequestIDMode: ClaudeRequestIDFill}
				body := cacheControlLimitOKMessage
				if path == "/v1/messages/count_tokens" {
					body = `{ "input_tokens" : 3 }`
				}
				resp := cacheControlLimitResponse(200, body)
				resp.Header.Set("Request-Id", upstreamID)
				rec := forwardOfficialResponse(t, path, account, policy, nil, resp)
				require.Equal(t, body, rec.Body.String())
				id := rec.Result().Header.Get("request-id")
				if upstreamID == "" {
					require.Regexp(t, officialIDPattern, id)
				} else {
					require.Equal(t, upstreamID, id)
				}
			}
		}
	}
}

func TestRequestIDFill_ErrorBodyHasID(t *testing.T) {
	policy := DefaultClaudeCustomizationSettings()
	policy.RequestIDMode = ClaudeRequestIDFill
	for _, tc := range []struct{ header, want string }{{"", "req_body"}, {"req_header", "req_header"}} {
		resp := cacheControlLimitResponse(400, strings.TrimSuffix(officialTestError, "}")+`,"request_id":"req_body"}`)
		resp.Header.Set("Request-Id", tc.header)
		rec := forwardOfficialResponse(t, "/v1/messages", fallbackNativeAccountForTest(false), policy, nil, resp)
		require.Equal(t, tc.want, rec.Result().Header.Get("request-id"))
		require.Equal(t, strings.TrimSuffix(officialTestError, "}")+`,"request_id":"`+tc.want+`"}`, rec.Body.String())
	}
}

func TestRequestIDFill_ErrorBodyMissing(t *testing.T) {
	policy := DefaultClaudeCustomizationSettings()
	policy.RequestIDMode = ClaudeRequestIDFill
	rec := forwardOfficialResponse(t, "/v1/messages", fallbackNativeAccountForTest(true), policy, nil, cacheControlLimitResponse(400, officialTestError))
	id := rec.Result().Header.Get("request-id")
	require.Regexp(t, officialIDPattern, id)
	require.Equal(t, strings.TrimSuffix(officialTestError, "}")+`,"request_id":"`+id+`"}`, rec.Body.String())
	for _, pass := range []bool{true, false} {
		resp := cacheControlLimitResponse(401, `{"type":"error","request_id":"req_upstream"}`)
		rec := forwardOfficialResponse(t, "/v1/messages/count_tokens", fallbackNativeAccountForTest(pass), policy, nil, resp)
		id := rec.Result().Header.Get("request-id")
		require.Regexp(t, officialIDPattern, id)
		require.Equal(t, id, gjson.Get(rec.Body.String(), "request_id").String())
	}
}

func TestRequestIDDefault_Unchanged(t *testing.T) {
	body := "  " + officialTestError + "\n"
	resp := cacheControlLimitResponse(400, body)
	resp.Header.Set("Request-Id", "req_official")
	resp.Header.Set("Anthropic-Ratelimit-Tokens-Reset", "1")
	rec := forwardOfficialResponse(t, "/v1/messages", fallbackNativeAccountForTest(false), DefaultClaudeCustomizationSettings(), nil, resp)
	require.Equal(t, http.Header{"Content-Type": {"application/json"}}, rec.Result().Header)
	require.Equal(t, body, rec.Body.String())
}

func TestRequestIDDefault_SuccessBytes(t *testing.T) {
	for _, pass := range []bool{false, true} {
		body := "\n" + cacheControlLimitOKMessage + "\n"
		resp := cacheControlLimitResponse(200, body)
		resp.Header.Set("X-Request-Id", "gateway")
		resp.Header.Set("Request-Id", "req_official")
		resp.Header.Set("Anthropic-Ratelimit-Tokens-Reset", "123")
		rec := forwardOfficialResponse(t, "/v1/messages", fallbackNativeAccountForTest(pass), DefaultClaudeCustomizationSettings(), nil, resp)
		require.Equal(t, http.Header{"Content-Type": {"application/json"}, "X-Request-Id": {"gateway"}}, rec.Result().Header)
		require.Equal(t, body, rec.Body.String())
	}
}

func TestRequestIDDefault_NoAllocations(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	policy := DefaultClaudeCustomizationSettings()
	c.Set(claudeCustomizationRequestContextKey, claudeCustomizationRequestPolicy{policy: policy})
	body := []byte(officialTestError)
	header := http.Header{"Request-Id": {"req_upstream"}}
	settings := NewSettingService(&fallbackPolicySettingRepo{}, &config.Config{})
	settings.alignmentSnapshot(t.Context())
	allocations := testing.AllocsPerRun(100, func() {
		if settings.FillAnthropicAuthRequestID(c) != "" {
			t.Fatal("warm default auth filled an ID")
		}
		pendingOfficialStreamHeaders(c, header)()
		applyOfficialResponseHeaders(c, policy, header)
		filled := fillRequestID(c, policy, header, body)
		if &filled[0] != &body[0] {
			t.Fatal("default path replaced error bytes")
		}
	})
	require.Zero(t, allocations)
}

func TestOfficialAntigravityLocalErrors(t *testing.T) {
	account := newAntigravityCompatAccount(AccountTypeOAuth)
	account.Extra = map[string]any{AccountClaudeCustomizationExtraKey: map[string]any{ClaudeFieldRequestIDMode: ClaudeRequestIDFill}}
	for _, body := range []string{"{}", `{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"hi"}]}`} {
		c, rec := newAntigravityCompatContext("POST", "/v1/messages", nil)
		resp := cacheControlLimitResponse(400, `{"error":{"message":"Prompt is too long"},"request_id":"req_body"}`)
		resp.Header.Set("Request-Id", "req_prompt")
		s := newAntigravityCompatService(config.GatewayConfig{}, &fallbackSequenceUpstream{responses: []*http.Response{resp}})
		_, err := s.Forward(c.Request.Context(), c, account, []byte(body), false)
		require.Error(t, err)
		if body != "{}" {
			var prompt *PromptTooLongError
			require.ErrorAs(t, err, &prompt)
			require.Empty(t, c.Writer.Header().Get("Request-Id"))
			require.Empty(t, FilledAnthropicRequestID(c))
			require.Error(t, s.WriteMappedClaudeError(c, account, prompt.StatusCode, prompt.RequestID, prompt.Body, prompt.ResponseHeaders))
			require.Equal(t, "req_prompt", rec.Result().Header.Get("Request-Id"))
		}
		id := rec.Result().Header.Get("Request-Id")
		require.NotEmpty(t, id)
		require.Equal(t, id, gjson.Get(rec.Body.String(), "request_id").String())
	}
}

func TestHeaderPassthrough_RequestIDMatrix(t *testing.T) {
	for _, mode := range []string{"passthrough", "fill"} {
		for _, headers := range []string{"off", "on"} {
			t.Run(mode+"/"+headers, func(t *testing.T) {
				policy := DefaultClaudeCustomizationSettings()
				policy.UpstreamHeaderPassthrough = headers
				account := fallbackNativeAccountForTest(false)
				account.Extra[AccountClaudeCustomizationExtraKey] = map[string]any{ClaudeFieldRequestIDMode: mode}
				resp := cacheControlLimitResponse(400, officialTestError)
				resp.Header.Set("Request-Id", "req_header")
				resp.Header.Set("Anthropic-Ratelimit-Tokens-Remaining", "17")
				rec := forwardOfficialResponse(t, "/v1/messages", account, policy, nil, resp)
				h := rec.Result().Header
				require.Equal(t, mode == "fill" || headers == "on", h.Get("request-id") == "req_header")
				require.Equal(t, headers == "on", h.Get("Anthropic-Ratelimit-Tokens-Remaining") == "17")
				want := officialTestError
				if mode == "fill" {
					want = strings.TrimSuffix(want, "}") + `,"request_id":"req_header"}`
				}
				require.Equal(t, want, rec.Body.String())
			})
		}
	}
}

func TestHeaderPassthrough_RatelimitAndNoOrgID(t *testing.T) {
	cfg := &config.Config{}
	cfg.Security.ResponseHeaders = config.ResponseHeaderConfig{Enabled: true, AdditionalAllowed: []string{"anthropic-organization-id", "anthropic-ratelimit-input-tokens-limit"}}
	for _, path := range []string{"/v1/messages", "/v1/messages/count_tokens", "/v1/chat/completions"} {
		for _, headers := range []string{"off", "on"} {
			policy := DefaultClaudeCustomizationSettings()
			policy.UpstreamHeaderPassthrough = headers
			policy.RequestIDMode = ClaudeRequestIDFill
			resp := cacheControlLimitResponse(200, cacheControlLimitOKMessage)
			resp.Header.Set("Anthropic-Organization-Id", "org_private")
			resp.Header.Set("Anthropic-Ratelimit-Other-Limit", "private")
			resp.Header.Set("Retry-After", "3")
			resp.Header.Set("X-Request-Id", "gateway")
			for _, kind := range []string{"requests", "tokens", "input-tokens", "output-tokens"} {
				for _, field := range []string{"limit", "remaining", "reset"} {
					resp.Header.Add("anthropic-ratelimit-"+kind+"-"+field, "123")
				}
			}
			rec := forwardOfficialResponse(t, path, fallbackNativeAccountForTest(true), policy, cfg, resp)
			h := rec.Result().Header
			require.Empty(t, h.Get("Anthropic-Ratelimit-Other-Limit"))
			require.Equal(t, "3", h.Get("Retry-After"))
			require.Equal(t, "gateway", h.Get("X-Request-Id"))
			if path == "/v1/chat/completions" {
				require.Empty(t, h.Get("Request-Id"))
				require.Empty(t, h.Get("Anthropic-Ratelimit-Requests-Limit"))
				continue
			}
			if headers == "off" {
				require.Equal(t, "org_private", h.Get("Anthropic-Organization-Id"))
			} else {
				require.Empty(t, h.Get("Anthropic-Organization-Id"))
			}
			for key := range resp.Header {
				if strings.HasPrefix(key, "Anthropic-Ratelimit-") && !strings.Contains(key, "Other") {
					if headers == "on" || key == "Anthropic-Ratelimit-Input-Tokens-Limit" {
						require.Equal(t, []string{"123"}, h.Values(key), key)
					} else {
						require.Empty(t, h.Values(key), key)
					}
				}
			}
		}
	}
}

func TestRequestIDFill_SSEError(t *testing.T) {
	const ping = "event: ping\ndata: {\"type\":\"ping\"}\n\n"
	policy := DefaultClaudeCustomizationSettings()
	policy.RequestIDMode = ClaudeRequestIDFill
	resp := cacheControlLimitResponse(200, ping+"event: error\ndata: "+officialTestError+"\n\n")
	resp.Header.Set("Content-Type", "text/event-stream")
	rec := forwardOfficialResponse(t, "/v1/messages", fallbackNativeAccountForTest(true), policy, nil, resp)
	id := rec.Result().Header.Get("Request-Id")
	require.Regexp(t, officialIDPattern, id)
	require.Equal(t, ping+"event: error\ndata: "+strings.TrimSuffix(officialTestError, "}")+`,"request_id":"`+id+`"}`+"\n\n", rec.Body.String())
}

func TestHeaderPassthrough_ForceRemove(t *testing.T) {
	cfg := &config.Config{}
	cfg.Security.ResponseHeaders = config.ResponseHeaderConfig{Enabled: true, ForceRemove: []string{"retry-after", "anthropic-ratelimit-tokens-limit"}}
	policy := DefaultClaudeCustomizationSettings()
	policy.UpstreamHeaderPassthrough = ClaudeHeaderPassthroughOn
	resp := cacheControlLimitResponse(200, cacheControlLimitOKMessage)
	resp.Header.Set("Retry-After", "1")
	resp.Header.Set("Anthropic-Ratelimit-Tokens-Limit", "2")
	resp.Header.Set("Anthropic-Ratelimit-Tokens-Remaining", "1")
	rec := forwardOfficialResponse(t, "/v1/messages", fallbackNativeAccountForTest(true), policy, cfg, resp)
	require.Empty(t, rec.Result().Header.Get("Retry-After"))
	require.Empty(t, rec.Result().Header.Get("Anthropic-Ratelimit-Tokens-Limit"))
	require.Equal(t, "1", rec.Result().Header.Get("Anthropic-Ratelimit-Tokens-Remaining"))
}

const officialNativeStop = "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
const officialRateHeader = "Anthropic-Ratelimit-Tokens-Limit"

func antigravityOfficialAttempt(t *testing.T, c *gin.Context, stream bool, resp *http.Response, fields map[string]any) error {
	t.Helper()
	s := newAntigravityCompatService(config.GatewayConfig{MaxLineSize: 65536}, &fallbackSequenceUpstream{responses: []*http.Response{resp}})
	account := newAntigravityCompatAccount(AccountTypeOAuth)
	account.Extra = map[string]any{AccountClaudeCustomizationExtraKey: fields}
	body := fmt.Appendf(nil, `{"model":"claude-sonnet-4-5","max_tokens":16,"stream":%t,"messages":[{"role":"user","content":"hi"}]}`, stream)
	_, err := s.Forward(c.Request.Context(), c, account, body, false)
	return err
}

func TestOfficialFailoverAntigravity(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, exhausted := range []bool{false, true} {
			t.Run(fmt.Sprintf("stream=%t/exhausted=%t", stream, exhausted), func(t *testing.T) {
				c, rec := newAntigravityCompatContext("POST", "/v1/messages", nil)
				fields := map[string]any{ClaudeFieldRequestIDMode: ClaudeRequestIDFill, ClaudeFieldUpstreamHeaderPassthrough: ClaudeHeaderPassthroughOn}
				fail := func(id, limit string) *UpstreamFailoverError {
					resp := cacheControlLimitResponse(200, "")
					resp.Header.Set("Request-Id", id)
					resp.Header.Set(officialRateHeader, limit)
					var failure *UpstreamFailoverError
					require.ErrorAs(t, antigravityOfficialAttempt(t, c, stream, resp, fields), &failure)
					require.False(t, c.Writer.Written())
					require.Empty(t, c.Writer.Header().Get("Request-Id"))
					require.Empty(t, c.Writer.Header().Get(officialRateHeader))
					require.Empty(t, FilledAnthropicRequestID(c))
					return failure
				}
				fail("req_first", "1")
				if exhausted {
					last := fail("req_last", "2")
					last.ApplyOfficialResponseHeaders(c)
					writeGatewayErrorResponse(c, last.StatusCode, "upstream_error", "unavailable")
					require.Equal(t, "req_last", rec.Result().Header.Get("Request-Id"))
					require.Equal(t, "2", rec.Result().Header.Get(officialRateHeader))
					require.Equal(t, "req_last", gjson.Get(rec.Body.String(), "request_id").String())
					return
				}
				body := cacheControlLimitOKMessage
				if stream {
					body = officialNativeStop
				}
				native := fallbackNativeGatewayForTest(t, &fallbackSequenceUpstream{responses: []*http.Response{cacheControlLimitResponse(200, body)}}, nil)
				req := fmt.Appendf(nil, `{"model":"claude-sonnet-4-5","max_tokens":16,"stream":%t,"messages":[{"role":"user","content":"hi"}]}`, stream)
				parsed, err := ParseGatewayRequest(NewRequestBodyRef(req), PlatformAnthropic)
				require.NoError(t, err)
				_, err = native.Forward(c.Request.Context(), c, fallbackNativeAccountForTest(true), parsed)
				require.NoError(t, err)
				require.Equal(t, body, rec.Body.String())
				require.Empty(t, rec.Result().Header.Get("Request-Id"))
				require.Empty(t, rec.Result().Header.Get(officialRateHeader))
				require.Empty(t, FilledAnthropicRequestID(c))
			})
		}
	}
}

func TestOfficialFailoverNativeFilteredHeaders(t *testing.T) {
	c, _ := newAntigravityCompatContext("POST", "/v1/messages", nil)
	resp := cacheControlLimitResponse(200, "event: error\ndata: "+officialTestError+"\n\n")
	resp.Header.Set("Request-Id", "req_failed")
	resp.Header.Set(officialRateHeader, "1")
	resp.Header.Set("Retry-After", "7")
	cfg := &config.Config{}
	cfg.Security.ResponseHeaders = config.ResponseHeaderConfig{Enabled: true, AdditionalAllowed: []string{"request-id", officialRateHeader}}
	s := fallbackNativeGatewayForTest(t, &fallbackSequenceUpstream{responses: []*http.Response{resp}}, cfg)
	acc := fallbackNativeAccountForTest(false)
	acc.Extra[AccountClaudeCustomizationExtraKey] = map[string]any{ClaudeFieldRequestIDMode: ClaudeRequestIDFill, ClaudeFieldUpstreamHeaderPassthrough: ClaudeHeaderPassthroughOn}
	parsed, err := ParseGatewayRequest(NewRequestBodyRef([]byte(`{"model":"claude-sonnet-4-5","stream":true,"max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`)), PlatformAnthropic)
	require.NoError(t, err)
	_, err = s.Forward(c.Request.Context(), c, acc, parsed)
	var failure *UpstreamFailoverError
	require.ErrorAs(t, err, &failure)
	require.False(t, c.Writer.Written())
	require.Empty(t, FilledAnthropicRequestID(c))
	for _, name := range []string{"Request-Id", officialRateHeader, "Retry-After"} {
		require.Empty(t, c.Writer.Header().Get(name), name)
	}
}

func TestOfficialAntigravityErrorFrame(t *testing.T) {
	const firstFrame = "data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"hi\"}]}}]}}\n\n"
	for _, prefix := range []string{"", firstFrame} {
		for _, mode := range []string{"passthrough", "fill"} {
			c, rec := newAntigravityCompatContext("POST", "/v1/messages", nil)
			resp := cacheControlLimitResponse(200, prefix+strings.Repeat("x", 70000)+"\n")
			resp.Header.Set("Request-Id", "req_stream")
			err := antigravityOfficialAttempt(t, c, true, resp, map[string]any{ClaudeFieldRequestIDMode: mode})
			require.Error(t, err)
			if prefix != "" {
				require.Contains(t, rec.Body.String(), "event: message_start")
			}
			_, frame, found := strings.Cut(rec.Body.String(), "event: error\n")
			require.True(t, found)
			payload := `{"type":"error","error":{"type":"api_error","message":"Upstream response exceeded the size limit"}`
			if mode == "fill" {
				require.Equal(t, "req_stream", rec.Result().Header.Get("Request-Id"))
				payload += `,"request_id":"req_stream"`
			} else {
				require.Empty(t, rec.Result().Header.Get("Request-Id"))
			}
			require.Equal(t, "data: "+payload+"}\n\n", frame)
		}
	}
}

func TestOfficialNativeErrorFrame(t *testing.T) {
	policy := DefaultClaudeCustomizationSettings()
	policy.RequestIDMode = ClaudeRequestIDFill
	resp := cacheControlLimitResponse(200, "event: ping\ndata: {\"type\":\"ping\"}\n\n"+strings.Repeat("x", 70000)+"\n")
	resp.Header.Set("Content-Type", "text/event-stream")
	resp.Header.Set("Request-Id", "req_native")
	rec := forwardOfficialResponse(t, "/v1/messages", fallbackNativeAccountForTest(false), policy, &config.Config{Gateway: config.GatewayConfig{MaxLineSize: 65536}}, resp)
	require.Equal(t, "req_native", rec.Result().Header.Get("Request-Id"))
	_, payload, found := strings.Cut(rec.Body.String(), "event: error\ndata: ")
	require.True(t, found)
	require.Equal(t, "req_native", gjson.Get(payload, "request_id").String())
}

func TestOfficialAntigravityUpstreamErrors(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, mode := range []string{"passthrough", "fill"} {
			c, rec := newAntigravityCompatContext("POST", "/v1/messages", nil)
			status, upstreamBody := 400, officialTestError
			if stream {
				status, upstreamBody = 200, "event: error\ndata: "+officialTestError+"\n\n"
			}
			resp := cacheControlLimitResponse(status, upstreamBody)
			resp.Header.Set("Request-Id", "req_upstream")
			s := newAntigravityCompatService(config.GatewayConfig{}, &fallbackSequenceUpstream{responses: []*http.Response{resp}})
			account := newAntigravityCompatAccount(AccountTypeUpstream)
			account.Credentials["base_url"], account.Credentials["api_key"] = "https://example.test", "upstream"
			s.settingService = NewSettingService(&fallbackPolicySettingRepo{values: map[string]string{SettingKeyClaudeCustomization: `{"request_id_mode":"` + mode + `"}`}}, nil)
			_, err := s.Forward(c.Request.Context(), c, account, fmt.Appendf(nil, `{"model":"claude-sonnet-4-5","stream":%t}`, stream), false)
			require.NoError(t, err)
			want := upstreamBody
			if mode == "fill" {
				require.Equal(t, "req_upstream", rec.Result().Header.Get("Request-Id"))
				want = strings.Replace(want, officialTestError, strings.TrimSuffix(officialTestError, "}")+`,"request_id":"req_upstream"}`, 1)
			} else {
				require.Empty(t, rec.Result().Header.Get("Request-Id"))
			}
			require.Equal(t, want, rec.Body.String())
		}
	}
}

func TestOfficialAntigravityUpstreamEmptyStream(t *testing.T) {
	for _, mode := range []string{"passthrough", "fill"} {
		for _, upstreamID := range []string{"req_empty", ""} {
			t.Run(mode+"/"+upstreamID, func(t *testing.T) {
				c, rec := newAntigravityCompatContext("POST", "/v1/messages", nil)
				resp := cacheControlLimitResponse(200, "")
				resp.Header.Set("Request-Id", upstreamID)
				s := newAntigravityCompatService(config.GatewayConfig{}, &fallbackSequenceUpstream{responses: []*http.Response{resp}})
				account := newAntigravityCompatAccount(AccountTypeUpstream)
				account.Credentials["base_url"], account.Credentials["api_key"] = "https://example.test", "fixture"
				s.settingService = NewSettingService(&fallbackPolicySettingRepo{values: map[string]string{SettingKeyClaudeCustomization: `{"request_id_mode":"` + mode + `"}`}}, nil)
				_, err := s.Forward(c.Request.Context(), c, account, []byte(`{"model":"claude-sonnet-4-5","stream":true}`), false)
				require.NoError(t, err)
				c.Writer.WriteHeaderNow() // Gin commits the pending 200 after the handler returns.
				require.Equal(t, 200, rec.Code)
				require.Empty(t, rec.Body.Bytes())
				id := rec.Result().Header.Get("Request-Id")
				switch {
				case mode == "passthrough":
					require.Empty(t, id)
				case upstreamID != "":
					require.Equal(t, upstreamID, id)
				default:
					require.Regexp(t, officialIDPattern, id)
				}
			})
		}
	}
}

func TestFilledIDValidation(t *testing.T) {
	p := DefaultClaudeCustomizationSettings()
	p.RequestIDMode = ClaudeRequestIDFill
	for _, id := range []string{"https://private.invalid/?key=test-secret", "req_" + strings.Repeat("a", 65)} {
		for _, fromHeader := range []bool{true, false} {
			resp := cacheControlLimitResponse(400, strings.TrimSuffix(officialTestError, "}")+`,"request_id":"`+id+`"}`)
			if fromHeader {
				resp.Header.Set("Request-Id", id)
			}
			rec := forwardOfficialResponse(t, "/v1/messages", fallbackNativeAccountForTest(false), p, nil, resp)
			got := rec.Result().Header.Get("Request-Id")
			require.Regexp(t, officialIDPattern, got)
			require.Equal(t, got, gjson.Get(rec.Body.String(), "request_id").String())
			require.NotContains(t, rec.Body.String(), id)
		}
	}
}

func TestFinalTransportHeaders(t *testing.T) {
	c := alignmentTestContext("/v1/messages")
	p := DefaultClaudeCustomizationSettings()
	p.RequestIDMode = ClaudeRequestIDFill
	p.UpstreamHeaderPassthrough = ClaudeHeaderPassthroughOn
	c.Set(claudeCustomizationRequestContextKey, claudeCustomizationRequestPolicy{policy: p})
	c.Header("Request-Id", "req_previous")
	c.Header("Retry-After", "42")
	svc := &GatewayService{}
	err := svc.handleUpstreamTransportError(t.Context(), c, fallbackNativeAccountForTest(true), io.ErrUnexpectedEOF, OpsUpstreamErrorEvent{})
	var fail *UpstreamFailoverError
	require.ErrorAs(t, err, &fail)
	fail.ApplyOfficialResponseHeaders(c)
	require.Regexp(t, officialIDPattern, c.Writer.Header().Get("Request-Id"))
	require.Empty(t, c.Writer.Header().Get("Retry-After"))
	require.Equal(t, FilledAnthropicRequestID(c), c.Writer.Header().Get("Request-Id"))
}
func TestBedrockOfficialSuccess(t *testing.T) {
	for _, stream := range []bool{true, false} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			c := alignmentTestContext("/v1/messages")
			p := DefaultClaudeCustomizationSettings()
			p.RequestIDMode = ClaudeRequestIDFill
			p.UpstreamHeaderPassthrough = ClaudeHeaderPassthroughOn
			c.Set(claudeCustomizationRequestContextKey, claudeCustomizationRequestPolicy{policy: p})
			resp := cacheControlLimitResponse(200, cacheControlLimitOKMessage)
			resp.Header.Set("Anthropic-Ratelimit-Requests-Limit", "34")
			svc := &GatewayService{}
			a := &Account{Type: AccountTypeBedrock, Platform: PlatformAnthropic}
			if stream {
				resp.Body = io.NopCloser(bytes.NewReader(bedrockChunkFrame(`{"type":"message_stop"}`)))
				_, err := svc.handleBedrockStreamingResponse(t.Context(), resp, c, a, time.Now(), "model")
				require.NoError(t, err)
			} else {
				_, err := svc.handleBedrockNonStreamingResponse(t.Context(), resp, c, a)
				require.NoError(t, err)
			}
			require.Regexp(t, officialIDPattern, c.Writer.Header().Get("Request-Id"))
			require.Equal(t, "34", c.Writer.Header().Get("Anthropic-Ratelimit-Requests-Limit"))
		})
	}
}
func TestAuthFillWithoutInfoLog(t *testing.T) {
	core, logs := observer.New(zap.InfoLevel)
	settings := NewSettingService(&queueAlignmentSettings{claudeCustomizationRepoStub: claudeCustomizationRepoStub{value: `{"request_id_mode":"fill"}`}}, nil)
	settings.alignmentSnapshot(t.Context())
	c := alignmentTestContext("/v1/messages")
	c.Request = c.Request.WithContext(logger.IntoContext(c.Request.Context(), zap.New(core)))
	require.NotEmpty(t, settings.FillAnthropicAuthRequestID(c))
	require.Zero(t, logs.Len())
}

func TestMappedRequestIDsDoNotExposeUpstreamBody(t *testing.T) {
	const privateID = "https://relay-internal.corp:8443/v1/messages?key=sk-live-123"
	p := DefaultClaudeCustomizationSettings()
	p.RequestIDMode = ClaudeRequestIDFill
	for _, status := range []int{401, 404, 503} {
		for _, source := range []string{privateID, "req_hidden"} {
			t.Run(fmt.Sprintf("%d/%t", status, source == privateID), func(t *testing.T) {
				resp := cacheControlLimitResponse(status, `{"type":"error","request_id":"`+source+`"}`)
				rec := forwardOfficialResponse(t, "/v1/messages/count_tokens", fallbackNativeAccountForTest(true), p, nil, resp)
				id := rec.Result().Header.Get("Request-Id")
				require.Regexp(t, officialIDPattern, id)
				require.Equal(t, id, gjson.Get(rec.Body.String(), "request_id").String())
				require.NotContains(t, rec.Body.String(), source)
				c := alignmentTestContext("/v1/messages")
				c.Set(claudeCustomizationRequestContextKey, claudeCustomizationRequestPolicy{policy: p})
				fail := (&UpstreamFailoverError{StatusCode: status, ResponseBody: []byte(`{"request_id":"` + source + `"}`)}).withOfficialHeaders(c, nil)
				fail.ApplyOfficialResponseHeaders(c)
				require.Regexp(t, officialIDPattern, c.Writer.Header().Get("Request-Id"))
			})
		}
	}
}

func TestSelectedLocalErrorFill(t *testing.T) {
	for _, path := range []string{"/v1/messages", "/v1/messages/count_tokens"} {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest("POST", path, nil)
		c.Set(claudeCustomizationRequestContextKey, claudeCustomizationRequestPolicy{policy: ClaudeCustomizationSettings{RequestIDMode: ClaudeRequestIDFill}})
		if path == "/v1/messages" {
			anthropicTooLargeError(c)
		} else {
			(&GatewayService{}).countTokensError(c, 502, "upstream_error", "Request failed")
		}
		id := rec.Header().Get("Request-Id")
		require.NotEmpty(t, id)
		require.Equal(t, id, gjson.Get(rec.Body.String(), "request_id").Str)
	}
}
