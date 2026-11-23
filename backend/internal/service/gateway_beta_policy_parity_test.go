//go:build unit

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestContextBetaPolicyForwarding(t *testing.T) {
	const cm = "context-management-2025-06-27"
	const compact = "compact-2026-01-12"
	for _, path := range []string{"/v1/messages", "/v1/messages/count_tokens"} {
		for _, mode := range []string{"automatic", "client", "filter", "strict", "strict-only", "official", "managed-lines", "direct-lines", "empty"} {
			for _, action := range []string{"block", "filter"} {
				for _, beta := range []string{cm, compact} {
					t.Run(fmt.Sprintf("%s/%s/%s/%s", path, mode, action, beta), func(t *testing.T) {
						direct := mode == "automatic" || mode == "direct-lines" || mode == "empty"
						account := fallbackNativeAccountForTest(direct)
						upstream := &anthropicHTTPUpstreamRecorder{resp: nativeAnthropicBufferedResponse()}
						svc := fallbackNativeGatewayForTest(t, upstream, nil)
						repo := &fallbackPolicySettingRepo{}
						svc.settingService = NewSettingService(repo, svc.cfg)
						fields := map[string]any{}
						switch mode {
						case "automatic", "client":
							raw, err := json.Marshal(BetaPolicySettings{Rules: []BetaPolicyRule{{Scope: BetaPolicyScopeAPIKey, BetaToken: beta, Action: action}}})
							require.NoError(t, err)
							require.NoError(t, repo.Set(t.Context(), SettingKeyBetaPolicySettings, string(raw)))
							if !direct {
								fields["beta_policy_mode"] = ClaudeBetaClientPassthrough
							}
						case "filter":
							fields["unknown_beta_action"] = ClaudeUnknownBetaFilter
						case "strict-only":
							fields["beta_policy_mode"] = ClaudeBetaOfficialStrict
						case "strict":
							fields["beta_policy_mode"] = ClaudeBetaOfficialStrict
							fields["unknown_beta_action"] = ClaudeUnknownBetaFilter
						case "official":
							require.NoError(t, repo.Set(t.Context(), SettingKeyClaudeCustomization, `{"preset":"official"}`))
						}
						account.Extra[AccountClaudeCustomizationExtraKey] = fields
						c, _ := gin.CreateTestContext(httptest.NewRecorder())
						c.Request = httptest.NewRequest("POST", path, nil)
						lines := []string{beta}
						if mode == "direct-lines" || mode == "managed-lines" {
							lines = []string{"interleaved-thinking-2025-05-14", beta}
						}
						if mode == "empty" {
							lines = []string{""}
						}
						for _, line := range lines {
							c.Request.Header.Add("Anthropic-Beta", line)
						}
						body := []byte(`{"model":"claude-sonnet-4-5","max_tokens":15,"context_management":{},"messages":[{"role":"user","content":"hi"}]}`)
						parsed, err := ParseGatewayRequest(NewRequestBodyRef(body), PlatformAnthropic)
						require.NoError(t, err)
						if path == "/v1/messages" {
							_, err = svc.Forward(t.Context(), c, account, parsed)
						} else {
							err = svc.ForwardCountTokens(t.Context(), c, account, parsed)
						}
						require.NoError(t, err)
						require.NotNil(t, upstream.lastReq)
						headers := upstream.lastReq.Header["anthropic-beta"]
						kept := gjson.GetBytes(upstream.lastBody, "context_management").Exists()
						t.Logf("wire %q %s", headers, upstream.lastBody)
						if mode == "managed-lines" {
							if beta == compact {
								require.Equal(t, []string{lines[0] + "," + compact}, headers)
							} else {
								require.Equal(t, lines[:1], headers)
							}
							require.Equal(t, beta == compact, kept)
						} else if mode == "filter" || mode == "strict" || mode == "official" {
							require.Equal(t, beta == compact, kept)
							if beta == compact {
								require.Equal(t, lines, headers)
							} else {
								require.Equal(t, "", getHeaderRaw(upstream.lastReq.Header, "anthropic-beta"))
							}
						} else {
							require.Equal(t, lines, headers)
							require.Equal(t, mode != "empty" && (mode != "direct-lines" || beta == compact), kept)
						}
					})
				}
			}
		}
	}
}

func TestCountTokensUnsupportedReadBudget(t *testing.T) {
	for _, account := range []*Account{{Platform: PlatformAntigravity, Type: AccountTypeOAuth}, {Platform: PlatformAnthropic, Type: AccountTypeBedrock}} {
		t.Run(account.Platform+"/"+account.Type, func(t *testing.T) {
			repo := &passthroughReadCounter{}
			svc := fallbackNativeGatewayForTest(t, nil, nil)
			svc.settingService = NewSettingService(repo, svc.cfg)
			if prime, ok := any(svc.settingService).(interface {
				AnyAlignmentReject(context.Context, string) bool
			}); ok {
				prime.AnyAlignmentReject(t.Context(), "unknown_beta_header")
			}
			repo.reads = nil
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest("POST", "/v1/messages/count_tokens", nil)
			parsed, err := ParseGatewayRequest(NewRequestBodyRef([]byte(`{"model":"claude-sonnet-4-5","messages":[]}`)), PlatformAnthropic)
			require.NoError(t, err)
			require.NoError(t, svc.ForwardCountTokens(t.Context(), c, account, parsed))
			require.Equal(t, 404, rec.Code)
			require.Empty(t, repo.reads)
		})
	}
}

type alignmentSchedulableRepo struct{ AccountRepository }

func (alignmentSchedulableRepo) GetByIDScoped(context.Context, int64) (*Account, error) {
	return &Account{ID: 1}, nil
}
func (alignmentSchedulableRepo) SetSchedulable(context.Context, int64, bool) error { return nil }
func TestSchedulableInvalidatesRoutingSnapshot(t *testing.T) {
	settings := NewSettingService(&fallbackPolicySettingRepo{}, nil)
	before := settings.alignmentSnapshot(t.Context())
	admin := &adminServiceImpl{accountRepo: alignmentSchedulableRepo{}, settingService: settings}
	_, err := admin.SetAccountSchedulable(t.Context(), 1, false)
	require.NoError(t, err)
	require.NotSame(t, before, settings.alignmentSnapshot(t.Context()))
}

func TestCountTokensDefaultReadMatrix(t *testing.T) {
	oauth := &Account{ID: 906, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Credentials: map[string]any{"access_token": "test-oauth-token"}}
	setup := *oauth
	setup.Type = AccountTypeSetupToken
	for _, account := range []*Account{
		fallbackNativeAccountForTest(false), fallbackNativeAccountForTest(true), oauth, &setup,
		{Platform: PlatformAntigravity, Type: AccountTypeOAuth}, {Platform: PlatformAnthropic, Type: AccountTypeBedrock},
		{Platform: PlatformOpenAI, Type: AccountTypeOAuth},
		{Platform: PlatformAntigravity, Type: AccountTypeUpstream}, {Platform: PlatformAnthropic, Type: AccountTypeServiceAccount},
	} {
		t.Run(account.Platform+"/"+account.Type, func(t *testing.T) {
			repo := &passthroughReadCounter{}
			svc := fallbackNativeGatewayForTest(t, &anthropicHTTPUpstreamRecorder{resp: nativeAnthropicBufferedResponse()}, nil)
			svc.settingService = NewSettingService(repo, svc.cfg)
			if prime, ok := any(svc.settingService).(interface {
				AnyAlignmentReject(context.Context, string) bool
			}); ok {
				prime.AnyAlignmentReject(t.Context(), "unknown_beta_header")
			}
			repo.reads = nil
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest("POST", "/v1/messages/count_tokens", nil)
			parsed, err := ParseGatewayRequest(NewRequestBodyRef([]byte(`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"hi"}]}`)), PlatformAnthropic)
			require.NoError(t, err)
			err = svc.ForwardCountTokens(t.Context(), c, account, parsed)
			t.Logf("status=%d settings=%q", rec.Code, repo.reads)
			if account.Type == AccountTypeServiceAccount {
				require.Empty(t, repo.reads)
				require.Error(t, err)
				require.Equal(t, 502, rec.Code)
				return
			}
			require.NoError(t, err)
			if account.Platform != PlatformAnthropic || account.IsBedrock() {
				require.Equal(t, 404, rec.Code)
				require.Empty(t, repo.reads)
			} else {
				require.Equal(t, 200, rec.Code)
				budget := 3
				if account.IsAnthropicAPIKeyPassthroughEnabled() {
					budget = 1
				}
				require.LessOrEqual(t, len(repo.reads), budget)
			}
		})
	}
}
