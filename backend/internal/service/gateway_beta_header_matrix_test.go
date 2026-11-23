//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type betaMatrixRepo struct {
	fallbackPolicySettingRepo
	reads int
}

func (r *betaMatrixRepo) GetValue(ctx context.Context, key string) (string, error) {
	r.reads++
	return r.fallbackPolicySettingRepo.GetValue(ctx, key)
}

const (
	matrixCM      = "context-management-2025-06-27"
	matrixCompact = "compact-2026-01-12"
	matrixBoth    = matrixCM + "," + matrixCompact
)

func TestBetaHeaderPolicyMatrix(t *testing.T) {
	for _, path := range []string{"/v1/messages", "/v1/messages/count_tokens"} {
		for _, kind := range []string{"managed", "direct", "oauth"} {
			for oi, override := range []string{"", matrixCM, matrixCompact, matrixBoth} {
				for ci, client := range [][]string{nil, {matrixCM}, {matrixCompact}, {matrixBoth}, {"interleaved-thinking-2025-05-14", matrixCompact}} {
					for pi, policy := range []string{"default", "unknown", "strict", "official", "cm-block", "cm-filter", "compact-block", "compact-filter"} {
						checkBetaHeaderMatrixRow(t, path, kind, override, client, policy, oi, ci, pi)
					}
				}
			}
		}
	}
}
func checkBetaHeaderMatrixRow(t *testing.T, path, kind, override string, client []string, policy string, oi, ci, pi int) {
	t.Helper()
	const cm, compact = matrixCM, matrixCompact
	const body = `{"model":"claude-sonnet-4-5","max_tokens":15,"context_management":{},"messages":[{"role":"user","content":"hi"}]}`
	repo := &betaMatrixRepo{}
	upstream := &anthropicHTTPUpstreamRecorder{resp: nativeAnthropicBufferedResponse()}
	svc := fallbackNativeGatewayForTest(t, upstream, nil)
	svc.settingService = NewSettingService(repo, svc.cfg)
	account := fallbackNativeAccountForTest(kind == "direct")
	if kind == "oauth" {
		account.Type = AccountTypeOAuth
		account.Credentials = map[string]any{"access_token": "test-access-token"}
	}
	if override != "" {
		account.Credentials[credKeyHeaderOverrideEnabled] = true
		account.Credentials[credKeyHeaderOverrides] = map[string]any{"anthropic-beta": override}
	}
	fields := map[string]any{}
	switch policy {
	case "unknown", "strict":
		fields["unknown_beta_action"] = ClaudeUnknownBetaFilter
		if policy == "strict" {
			fields["beta_policy_mode"] = ClaudeBetaOfficialStrict
		}
	case "official":
		require.NoError(t, repo.Set(t.Context(), SettingKeyClaudeCustomization, `{"preset":"official"}`))
	case "cm-block", "cm-filter", "compact-block", "compact-filter":
		token, action, _ := strings.Cut(policy, "-")
		if token == "cm" {
			token = cm
		} else {
			token = compact
		}
		raw, err := json.Marshal(BetaPolicySettings{Rules: []BetaPolicyRule{{Scope: BetaPolicyScopeAll, BetaToken: token, Action: action}}})
		require.NoError(t, err)
		require.NoError(t, repo.Set(t.Context(), SettingKeyBetaPolicySettings, string(raw)))
	}
	account.Extra[AccountClaudeCustomizationExtraKey] = fields
	if prime, ok := any(svc.settingService).(interface {
		AnyAlignmentReject(context.Context, string) bool
	}); ok {
		prime.AnyAlignmentReject(t.Context(), "unknown_beta_header")
	}
	repo.reads = 0
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest("POST", path, nil)
	for _, line := range client {
		c.Request.Header.Add("Anthropic-Beta", line)
	}
	parsed, err := ParseGatewayRequest(NewRequestBodyRef([]byte(body)), PlatformAnthropic)
	require.NoError(t, err)
	if path == "/v1/messages" {
		_, err = svc.Forward(t.Context(), c, account, parsed)
	} else {
		err = svc.ForwardCountTokens(t.Context(), c, account, parsed)
	}
	var headers []string
	if upstream.lastReq != nil {
		headers = upstream.lastReq.Header["anthropic-beta"]
	}
	kept := gjson.GetBytes(upstream.lastBody, "context_management").Exists()
	var blocked *BetaBlockedError
	blockedClient := errors.As(err, &blocked)
	row, marshalErr := json.Marshal([]any{path, kind, oi, ci, pi, headers, kept, rec.Code, rec.Body.String(), blockedClient, repo.reads})
	require.NoError(t, marshalErr)
	t.Logf("BETAROW %s", row)
	if kind == "managed" && ci == 4 && policy == "compact-block" {
		require.NoError(t, err, "second-line compact cannot trigger a block")
		require.Equal(t, override != "", kept)
	}
	if kind == "managed" && override != "" {
		first := ""
		if len(client) > 0 {
			first = client[0]
		}
		wantBlock := policy == "cm-block" && containsBetaToken(first, cm) || policy == "compact-block" && containsBetaToken(first, compact)
		if wantBlock {
			if path == "/v1/messages" {
				require.True(t, blockedClient)
			} else {
				require.Equal(t, 500, rec.Code)
				require.Equal(t, `{"error":{"message":"Failed to build request","type":"api_error"},"type":"error"}`, rec.Body.String())
			}
		} else {
			if err != nil || len(headers) != 1 || headers[0] != override || !kept {
				t.Errorf("%s/%s override=%q client=%q header=%q keep=%t err=%v", path, policy, override, client, headers, kept, err)
			}
		}
	}
}

func TestContextBetaBlockRetainsOptInErrorFormat(t *testing.T) {
	for _, fill := range []bool{false, true} {
		token := matrixCompact
		if !fill {
			token = "other-feature"
		}
		repo := &fallbackPolicySettingRepo{}
		rules, err := json.Marshal(BetaPolicySettings{Rules: []BetaPolicyRule{{Scope: BetaPolicyScopeAll, BetaToken: token, Action: BetaPolicyActionBlock}}})
		require.NoError(t, err)
		require.NoError(t, repo.Set(t.Context(), SettingKeyBetaPolicySettings, string(rules)))
		account := fallbackNativeAccountForTest(false)
		if fill {
			account.Extra[AccountClaudeCustomizationExtraKey] = map[string]any{"request_id_mode": "fill"}
		}
		svc := fallbackNativeGatewayForTest(t, nil, nil)
		svc.settingService = NewSettingService(repo, svc.cfg)
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest("POST", "/v1/messages/count_tokens", nil)
		c.Request.Header.Set("Anthropic-Beta", token)
		parsed, err := ParseGatewayRequest(NewRequestBodyRef([]byte(`{"model":"claude-sonnet-4-5","messages":[]}`)), PlatformAnthropic)
		require.NoError(t, err)
		require.Error(t, svc.ForwardCountTokens(t.Context(), c, account, parsed))
		require.Equal(t, 500, rec.Code)
		require.True(t, strings.HasPrefix(rec.Body.String(), `{"type":"error","error":{"type":"api_error"`))
		require.Equal(t, fill, gjson.Get(rec.Body.String(), "request_id").Exists())
	}
}

func setBetaPolicyFilterSet(c *gin.Context, account *Account, model string, filters map[string]struct{}) {
	_, compactFiltered := filters[matrixCompact]
	cacheBetaPolicy(c, account, model, betaPolicyResult{filterSet: filters, compactFiltered: compactFiltered})
}

// An explicit block on compact must leave an unrelated request available
// to the separately configured local validation for missing context betas.
func TestUnusedCompactBlockPreservesMissingBetaRejection(t *testing.T) {
	for _, path := range []string{"/v1/messages", "/v1/messages/count_tokens"} {
		t.Run(path, func(t *testing.T) {
			upstream := &anthropicHTTPUpstreamRecorder{resp: nativeAnthropicBufferedResponse()}
			svc := fallbackNativeGatewayForTest(t, upstream, nil)
			repo := &betaMatrixRepo{}
			svc.settingService = NewSettingService(repo, svc.cfg)
			rules, err := json.Marshal(BetaPolicySettings{Rules: []BetaPolicyRule{{Scope: BetaPolicyScopeAll, BetaToken: matrixCompact, Action: BetaPolicyActionBlock}}})
			require.NoError(t, err)
			require.NoError(t, repo.Set(t.Context(), SettingKeyBetaPolicySettings, string(rules)))
			account := fallbackNativeAccountForTest(false)
			account.Extra[AccountClaudeCustomizationExtraKey] = map[string]any{ClaudeFieldContextManagementMissingBeta: ClaudeAlignReject}
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest("POST", path, nil)
			body := []byte(`{"model":"claude-sonnet-4-5","max_tokens":16,"messages":[{"role":"user","content":"hi"}],"context_management":{}}`)
			parsed, err := ParseGatewayRequest(NewRequestBodyRef(body), PlatformAnthropic)
			require.NoError(t, err)
			if path == "/v1/messages" {
				_, err = svc.Forward(t.Context(), c, account, parsed)
			} else {
				err = svc.ForwardCountTokens(t.Context(), c, account, parsed)
			}
			require.ErrorIs(t, err, ErrLocalAnthropicRejected)
			require.Equal(t, 400, rec.Code)
			require.Nil(t, upstream.lastReq)
		})
	}
}
