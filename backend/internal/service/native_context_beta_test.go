//go:build unit

package service

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const relayContextError = `{"type":"error","error":{"type":"invalid_request_error","message":"context_management: Extra inputs are not permitted"}}`

type contextBetaRelay struct {
	httpUpstreamRecorder
	strict        bool
	rejectCompact bool
}

func (r *contextBetaRelay) Do(req *http.Request, proxyURL string, accountID int64, concurrency int) (*http.Response, error) {
	r.resp = nativeAnthropicBufferedResponse()
	resp, err := r.httpUpstreamRecorder.Do(req, proxyURL, accountID, concurrency)
	cm := gjson.GetBytes(r.lastBody, "context_management")
	missingContextBeta := r.strict && cm.Exists() && !strings.Contains(getHeaderRaw(req.Header, "anthropic-beta"), "context-management-2025-06-27")
	if err == nil && (missingContextBeta || (r.rejectCompact && cm.Get("edits.#(type==compact_20260112)").Exists())) {
		_ = resp.Body.Close()
		resp.StatusCode = http.StatusBadRequest
		resp.Body = io.NopCloser(strings.NewReader(relayContextError))
	}
	return resp, err
}

type contextBetaAccountRepo struct {
	rateLimitAccountRepoStub
	modelCooldowns int
}

func (r *contextBetaAccountRepo) SetModelRateLimit(context.Context, int64, string, time.Time, ...string) error {
	r.modelCooldowns++
	return nil
}

func TestThirdPartyNativeContextBeta(t *testing.T) {
	const compact = "compact-2026-01-12"
	const contextBeta = "context-management-2025-06-27"
	const body = `{"model":"claude-sonnet-4-5","max_tokens":16,"messages":[{"role":"user","content":"hello"}],"context_management":{"edits":[{"type":"compact_20260112"}]}}`
	for _, platform := range []string{PlatformKimi, PlatformCommandCode} {
		for _, beta := range []string{"", contextBeta, compact, contextBeta + "," + compact} {
			for _, strict := range []bool{false, true} {
				for _, count := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/%s/strict=%t/count=%t", platform, beta, strict, count), func(t *testing.T) {
						account := nativeAnthropicTestAccount()
						account.Platform = platform
						account.Credentials["temp_unschedulable_enabled"] = true
						account.Credentials["temp_unschedulable_rules"] = []any{map[string]any{"error_code": float64(400), "keywords": []any{"context_management"}, "duration_minutes": float64(10)}}
						repo := &contextBetaAccountRepo{}
						relay := &contextBetaRelay{strict: strict}
						svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: relay, rateLimitService: &RateLimitService{accountRepo: repo}}
						rec := httptest.NewRecorder()
						c, _ := gin.CreateTestContext(rec)
						path := "/v1/messages"
						if count {
							path += "/count_tokens"
						}
						c.Request = httptest.NewRequest(http.MethodPost, path, nil)
						if beta != "" {
							c.Request.Header.Set("Anthropic-Beta", beta)
						}
						var err error
						if count {
							err = svc.ForwardCountTokensAsAnthropic(t.Context(), c, account, []byte(body), "")
						} else {
							_, err = svc.ForwardAsAnthropic(t.Context(), c, account, []byte(body), "", "")
						}
						var wireBeta string
						if relay.lastReq != nil {
							wireBeta = getHeaderRaw(relay.lastReq.Header, "anthropic-beta")
						}
						t.Logf("WIRE %s %q %q %d %q cooldown=%d", t.Name(), wireBeta, relay.lastBody, rec.Code, rec.Body.String(), repo.tempCalls)
						require.NoError(t, err)
						require.Equal(t, http.StatusOK, rec.Code)
						require.Zero(t, repo.modelCooldowns)
						require.Zero(t, repo.tempCalls)
						require.Zero(t, repo.setErrorCalls)
						require.Zero(t, repo.rateLimitedCalls)
						if count {
							require.Empty(t, relay.requests)
							require.Positive(t, gjson.Get(rec.Body.String(), "input_tokens").Int())
						} else {
							require.Len(t, relay.requests, 1)
							require.Equal(t, beta, wireBeta)
							require.Equal(t, strings.Contains(beta, contextBeta), gjson.GetBytes(relay.lastBody, "context_management").Exists())
						}
					})
				}
			}
		}
	}
}

func TestAntigravityStrictCompactRelay(t *testing.T) {
	cases := []struct {
		name, beta, context string
		wantDebit           float64
	}{
		{"compact_only", "compact-2026-01-12", `,"context_management":{"edits":[{"type":"compact_20260112"}]}`, .25},
		{"dual_compact", "context-management-2025-06-27,compact-2026-01-12", `,"context_management":{"edits":[{"type":"compact_20260112"}]}`, .25},
		{"dual_clear", "context-management-2025-06-27,compact-2026-01-12", `,"context_management":{"edits":[{"type":"clear_tool_uses_20250919"}]}`, .25},
		{"compact_no_field", "compact-2026-01-12", "", .25},
		{"context_empty", "context-management-2025-06-27", `,"context_management":{}`, .25},
		{"neither", "", "", .25},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			relay := &httpUpstreamRecorder{resp: &http.Response{StatusCode: http.StatusBadRequest, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(relayContextError))}}
			repo := &contextBetaAccountRepo{}
			svc := &AntigravityGatewayService{httpUpstream: relay, rateLimitService: &RateLimitService{accountRepo: repo}}
			account := &Account{Platform: PlatformAntigravity, Type: AccountTypeUpstream, Credentials: map[string]any{"base_url": "https://relay.example", "api_key": "test", "temp_unschedulable_enabled": true, "temp_unschedulable_rules": []any{map[string]any{"error_code": float64(400), "keywords": []any{"context_management"}, "duration_minutes": float64(10)}}}}
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			c.Request.Header.Set("Anthropic-Beta", tc.beta)
			result, err := svc.ForwardUpstream(t.Context(), c, account, []byte(`{"model":"claude-sonnet-4-5","messages":[]`+tc.context+`}`))
			require.Len(t, relay.requests, 1)
			require.Equal(t, http.StatusBadRequest, rec.Code)
			require.Equal(t, relayContextError, rec.Body.String())
			require.Zero(t, repo.modelCooldowns)
			require.Zero(t, repo.tempCalls)
			require.Zero(t, repo.setErrorCalls)
			require.Zero(t, repo.rateLimitedCalls)
			var debit float64
			require.NoError(t, err)

			prices, key := advisorTestService()
			key.Group.RateMultiplier = 1
			key.Group.ModelPricing = []ChannelModelPricing{{Models: []string{result.Model}, BillingMode: BillingModePerRequest, PerRequestPrice: billingAmount(.25)}}
			ledger := &openAIRecordUsageBillingRepoStub{result: &UsageBillingApplyResult{Applied: true}}
			usage := newGatewayRecordUsageServiceWithBillingRepoForTest(&openAIRecordUsageLogRepoStub{inserted: true}, ledger, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{})
			usage.billingService, usage.resolver = prices.billingService, prices.resolver
			result.RequestID = "relay-compact-rejected"
			require.NoError(t, usage.RecordUsage(t.Context(), &RecordUsageInput{APIKey: key, User: &User{ID: 2}, Account: account, Result: result}))
			if ledger.lastCmd != nil {
				debit = ledger.lastCmd.BalanceCost
			}
			t.Logf("RELAY %s debit=%.2f status=%d response=%q wire=%q beta=%q cooldown=%d/%d/%d/%d ops_status=%d ops_message=%q ops_detail=%q", tc.name, debit, rec.Code, rec.Body.String(), relay.lastBody, relay.lastReq.Header.Get("Anthropic-Beta"), repo.modelCooldowns, repo.tempCalls, repo.setErrorCalls, repo.rateLimitedCalls, c.GetInt(OpsUpstreamStatusCodeKey), c.GetString(OpsUpstreamErrorMessageKey), c.GetString(OpsUpstreamErrorDetailKey))
			require.InDelta(t, tc.wantDebit, debit, 1e-9)
		})
	}
}
