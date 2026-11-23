//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/tidwall/gjson"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type betaRouteUpstream struct {
	anthropicHTTPUpstreamRecorder
	calls int
}

func (u *betaRouteUpstream) Do(r *http.Request, proxy string, id int64, concurrency int) (*http.Response, error) {
	u.calls++
	return u.anthropicHTTPUpstreamRecorder.Do(r, proxy, id, concurrency)
}

func (u *betaRouteUpstream) DoWithTLS(r *http.Request, proxy string, id int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(r, proxy, id, concurrency)
}

type betaRouteSettings struct {
	fallbackPolicySettingRepo
	reads []string
}

func (s *betaRouteSettings) GetValue(ctx context.Context, key string) (string, error) {
	s.reads = append(s.reads, key)
	return s.fallbackPolicySettingRepo.GetValue(ctx, key)
}

func TestPassthroughAccountBetaRoutes(t *testing.T) {
	const contextAndFast = "context-1m-2025-08-07,fast-mode-2026-02-01"
	const token = "test-policy-token"
	const beta = contextAndFast + "," + token
	for _, path := range []string{"/v1/messages", "/v1/messages/count_tokens", "/v1/chat/completions", "/v1/responses"} {
		for _, policy := range []string{"default", "block", "filter", "client_passthrough"} {
			t.Run(path+"/"+policy, func(t *testing.T) {
				native := path == "/v1/messages" || path == "/v1/messages/count_tokens"
				upstream := &betaRouteUpstream{}
				upstream.resp = nativeAnthropicStreamResponse()
				if native {
					upstream.resp = nativeAnthropicBufferedResponse()
				}
				svc := fallbackNativeGatewayForTest(t, upstream, nil)
				repo := &betaRouteSettings{}
				svc.settingService = NewSettingService(repo, svc.cfg)
				account := fallbackNativeAccountForTest(true)
				if policy != "default" {
					action := policy
					if policy == "client_passthrough" {
						account.Extra[AccountClaudeCustomizationExtraKey] = map[string]any{"beta_policy_mode": ClaudeBetaClientPassthrough}
						action = "block"
					}
					rules, err := json.Marshal(BetaPolicySettings{Rules: []BetaPolicyRule{{Scope: BetaPolicyScopeAPIKey, BetaToken: token, Action: action}}})
					require.NoError(t, err)
					require.NoError(t, repo.Set(t.Context(), SettingKeyBetaPolicySettings, string(rules)))
				}
				// Exclude startup snapshot initialization from the request's read budget.
				svc.settingService.AnyAlignmentReject(t.Context(), "unknown_beta_header")
				repo.reads = nil
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest("POST", path, nil)
				c.Request.Header.Set("Anthropic-Beta", beta)
				body := []byte(`{"model":"claude-sonnet-4-5","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`)
				if path == "/v1/responses" {
					body = []byte(`{"model":"claude-sonnet-4-5","input":"hi"}`)
				}
				parsed, err := ParseGatewayRequest(NewRequestBodyRef(body), PlatformAnthropic)
				require.NoError(t, err)
				switch path {
				case "/v1/chat/completions":
					_, err = svc.ForwardAsChatCompletions(t.Context(), c, account, body, parsed)
				case "/v1/responses":
					_, err = svc.ForwardAsResponses(t.Context(), c, account, body, parsed)
				case "/v1/messages":
					_, err = svc.Forward(t.Context(), c, account, parsed)
				default:
					err = svc.ForwardCountTokens(t.Context(), c, account, parsed)
				}
				var blocked *BetaBlockedError
				header := ""
				if upstream.lastReq != nil {
					header = getHeaderRaw(upstream.lastReq.Header, "anthropic-beta")
				}
				t.Logf("PARITY %s calls=%d blocked=%t beta=%q reads=%q", t.Name(), upstream.calls, errors.As(err, &blocked), header, repo.reads)
				budget := 3
				if native {
					budget = 1
				} else if policy == "client_passthrough" {
					budget = 2
				}
				require.LessOrEqual(t, len(repo.reads), budget)
				if !native && policy == "block" {
					require.ErrorAs(t, err, &blocked)
					require.Zero(t, upstream.calls)
					return
				}
				require.NoError(t, err)
				require.Equal(t, 1, upstream.calls)
				want := beta
				if !native {
					switch policy {
					case "default":
						want = token
					case "filter":
						want = contextAndFast
					}
				}
				require.Equal(t, want, header)
			})
		}
	}
}

func TestClientCompactBetaRoutes(t *testing.T) {
	for _, path := range []string{"/v1/messages", "/v1/messages/count_tokens", "/v1/chat/completions", "/v1/responses"} {
		for _, kind := range []string{"managed", "direct", "oauth"} {
			for _, policy := range []string{"unknown", "strict", "official"} {
				for _, scope := range []string{BetaPolicyScopeAll, BetaPolicyScopeAPIKey} {
					for _, action := range []string{"none", "block", "filter"} {
						for _, two := range []bool{false, true} {
							for _, hasField := range []bool{false, true} {
								t.Run(fmt.Sprintf("%s/%s/%s/%s/%s/two=%t/field=%t", path, kind, policy, scope, action, two, hasField), func(t *testing.T) {
									checkClientCompactBetaRoute(t, path, kind, policy, scope, action, two, hasField)
								})
							}
						}
					}
				}
			}
		}
	}
}

func checkClientCompactBetaRoute(t *testing.T, path, kind, policy, scope, action string, two, hasField bool) {
	t.Helper()
	native := path == "/v1/messages" || path == "/v1/messages/count_tokens"
	upstream := &betaRouteUpstream{}
	upstream.resp = nativeAnthropicStreamResponse()
	if native {
		upstream.resp = nativeAnthropicBufferedResponse()
	}
	svc := fallbackNativeGatewayForTest(t, upstream, nil)
	repo := &betaRouteSettings{}
	svc.settingService = NewSettingService(repo, svc.cfg)
	account := fallbackNativeAccountForTest(kind == "direct")
	if kind == "oauth" {
		account.Type = AccountTypeOAuth
		account.Credentials = map[string]any{"access_token": "test"}
	}
	if action != "none" {
		rules, err := json.Marshal(BetaPolicySettings{Rules: []BetaPolicyRule{{Scope: scope, BetaToken: matrixCompact, Action: action}}})
		require.NoError(t, err)
		require.NoError(t, repo.Set(t.Context(), SettingKeyBetaPolicySettings, string(rules)))
	}
	if policy == "official" {
		require.NoError(t, repo.Set(t.Context(), SettingKeyClaudeCustomization, `{"preset":"official"}`))
	} else {
		fields := map[string]any{"unknown_beta_action": ClaudeUnknownBetaFilter}
		if policy == "strict" {
			fields["beta_policy_mode"] = ClaudeBetaOfficialStrict
		}
		account.Extra[AccountClaudeCustomizationExtraKey] = fields
	}
	// Exclude startup snapshot initialization from the request's read budget.
	svc.settingService.AnyAlignmentReject(t.Context(), "unknown_beta_header")
	repo.reads = nil
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest("POST", path, nil)
	if two {
		c.Request.Header.Add("Anthropic-Beta", "interleaved-thinking-2025-05-14")
	}
	c.Request.Header.Add("Anthropic-Beta", matrixCompact)
	body := []byte(`{"model":"claude-sonnet-4-5","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`)
	if path == "/v1/responses" {
		body = []byte(`{"model":"claude-sonnet-4-5","input":"hi"}`)
	}
	if hasField {
		body = append(body[:len(body)-1], []byte(`,"context_management":{}}`)...)
	}
	parsed, err := ParseGatewayRequest(NewRequestBodyRef(body), PlatformAnthropic)
	require.NoError(t, err)
	switch path {
	case "/v1/chat/completions":
		_, err = svc.ForwardAsChatCompletions(t.Context(), c, account, body, parsed)
	case "/v1/responses":
		_, err = svc.ForwardAsResponses(t.Context(), c, account, body, parsed)
	case "/v1/messages":
		_, err = svc.Forward(t.Context(), c, account, parsed)
	default:
		err = svc.ForwardCountTokens(t.Context(), c, account, parsed)
	}
	var blocked *BetaBlockedError
	header := ""
	if upstream.lastReq != nil {
		header = getHeaderRaw(upstream.lastReq.Header, "anthropic-beta")
	}
	kept := gjson.GetBytes(upstream.lastBody, "context_management").Exists()
	var lines []string
	if upstream.lastReq != nil {
		lines = upstream.lastReq.Header["anthropic-beta"]
	}
	t.Logf("COMPACTROW %s calls=%d blocked=%t status=%d lines=%q kept=%t", t.Name(), upstream.calls, errors.As(err, &blocked), rec.Code, lines, kept)
	if kind == "direct" && native && action != "none" {
		require.False(t, kept)
	}
	if kind == "managed" && two && action != "none" {
		require.NotContains(t, header, matrixCompact)
		require.False(t, kept)
	}
	if !native && two {
		require.NotContains(t, header, matrixCompact)
	}
}
