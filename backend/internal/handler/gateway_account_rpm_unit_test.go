//go:build unit

package handler

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	middleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type accountRPMSuccessUpstream struct{ service.HTTPUpstream }

func (u *accountRPMSuccessUpstream) DoWithTLS(req *http.Request, proxy string, id int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxy, id, concurrency)
}

func (*accountRPMSuccessUpstream) Do(_ *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"id":"msg_rpm","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)),
	}, nil
}

func TestAccountRPM_AnthropicAPIKeyAndExistingOAuthCountOnce(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, accountType := range []string{service.AccountTypeAPIKey, service.AccountTypeOAuth} {
		t.Run(accountType, func(t *testing.T) {
			groupID := int64(5101)
			group := &service.Group{ID: groupID, Hydrated: true, Platform: service.PlatformAnthropic, Status: service.StatusActive}
			account := &service.Account{
				ID: 6101, Platform: service.PlatformAnthropic, Type: accountType,
				Status: service.StatusActive, Schedulable: true, Concurrency: 1,
				Credentials:   map[string]any{"api_key": "fixture", "access_token": "fixture"},
				Extra:         map[string]any{"base_rpm": 60},
				AccountGroups: []service.AccountGroup{{AccountID: 6101, GroupID: groupID}},
			}
			rpm := &accountRPMHandlerCache{}
			snapshot := service.NewSchedulerSnapshotService(&fakeSchedulerCache{accounts: []*service.Account{account}}, nil, nil, nil, nil)
			cfg := &config.Config{RunMode: config.RunModeSimple}
			h, cleanup := newTestGatewayHandler(t, group, []*service.Account{account})
			t.Cleanup(cleanup)
			h.gatewayService = service.NewGatewayService(
				nil, &fakeGroupRepo{group: group}, nil, nil, nil, nil, nil, nil, cfg,
				snapshot, nil, nil, nil, nil, nil, &accountRPMSuccessUpstream{}, nil, nil, nil, rpm,
				nil, nil, nil, nil, nil, nil, nil, nil, nil,
			)
			h.apiKeyService = service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, cfg)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"claude-sonnet-4-5","max_tokens":32,"messages":[{"role":"user","content":"hello"}]}`))
			c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), ctxkey.Group, group))
			c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{ID: 7101, UserID: 8101, GroupID: &groupID, Group: group, User: &service.User{ID: 8101, Balance: 100}})
			c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 8101})
			h.Messages(c)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			require.Equal(t, []int64{6101}, rpm.calls())
		})
	}
}

type compactRejectedUpstream struct {
	service.HTTPUpstream
	calls          int
	response, sent string
}

const compactRejectionBody = `{"type":"error","error":{"type":"invalid_request_error","message":"context_management: Extra inputs are not permitted"}}`

func (u *compactRejectedUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	u.calls++
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	u.sent = string(body)
	return &http.Response{StatusCode: http.StatusBadRequest, Header: http.Header{"Content-Type": {"application/json"}},
		Body: io.NopCloser(strings.NewReader(u.response))}, nil
}

func TestMessagesRelay400Settlement(t *testing.T) {
	const compact = "compact-2026-01-12"
	const cm = "context-management-2025-06-27"
	const edit = `,"context_management":{"edits":[{"type":"compact_20260112"}]}`
	cases := []struct{ name, beta, field, message string }{
		{"compact_only", compact, edit, ""},
		{"dual_compact", cm + "," + compact, edit, ""},
		{"dual_clear", cm + "," + compact, `,"context_management":{"edits":[{"type":"clear_tool_uses_20250919"}]}`, ""},
		{"compact_no_field", compact, "", ""},
		{"context_empty", cm, `,"context_management":{}`, ""},
		{"neither", "", "", ""},
		{"prompt_length", compact, edit, "prompt is too long"},
		{"missing_field", compact, edit, "max_tokens: Field required"},
	}
	for _, tc := range cases {
		for _, perRequest := range []bool{false, true} {
			for _, platform := range []string{service.PlatformAnthropic, service.PlatformGemini} {
				t.Run(fmt.Sprintf("%s/%s/perRequest=%t", tc.name, platform, perRequest), func(t *testing.T) {
					logs, restore := captureHandlerStructuredLog(t)
					defer restore()
					response := compactRejectionBody
					if tc.message != "" {
						response = strings.Replace(response, "context_management: Extra inputs are not permitted", tc.message, 1)
					}
					groupID := int64(5101)
					group := &service.Group{ID: groupID, Hydrated: true, Platform: platform, Status: service.StatusActive, RateMultiplier: 1}
					account := &service.Account{ID: 6101, Platform: service.PlatformAntigravity, Type: service.AccountTypeUpstream,
						Status: service.StatusActive, Schedulable: true, Concurrency: 1, Extra: map[string]any{"mixed_scheduling": true},
						Credentials:   map[string]any{"base_url": "https://relay.example", "api_key": "test"},
						AccountGroups: []service.AccountGroup{{AccountID: 6101, GroupID: groupID}}}
					rpm := &accountRPMHandlerCache{}
					upstream := &compactRejectedUpstream{response: response}
					usage := &fakeFailureSinkUsageLogRepo{}
					snapshot := service.NewSchedulerSnapshotService(&fakeSchedulerCache{accounts: []*service.Account{account}}, nil, nil, nil, nil)
					cfg := &config.Config{}
					cfg.Default.RateMultiplier = 1
					prices := service.NewBillingService(cfg, nil)
					ledger := &messagesProbeBillingRepository{}
					billCache := service.NewBillingCacheService(&messagesProbeCache{newHandlerInflightCache(100)}, nil, nil, nil, nil, nil, cfg, nil)
					t.Cleanup(billCache.Stop)
					price := .25
					if perRequest {
						group.ModelPricing = []service.ChannelModelPricing{{Models: []string{"claude-sonnet-4-5"}, BillingMode: service.BillingModePerRequest, PerRequestPrice: &price}}
					}
					h, cleanup := newTestGatewayHandler(t, group, []*service.Account{account})
					t.Cleanup(cleanup)
					h.gatewayService = service.NewGatewayService(nil, &fakeGroupRepo{group: group}, usage, ledger, nil, nil, nil, nil, cfg, snapshot, nil, prices, nil, billCache, nil, nil, &service.DeferredService{}, nil, nil, rpm, nil, nil, nil, nil, service.NewModelPricingResolver(nil, prices), nil, nil, nil, nil)
					h.antigravityGatewayService = service.NewAntigravityGatewayService(nil, nil, snapshot, nil, nil, upstream, nil, nil)
					h.apiKeyService = service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, cfg)
					h.usageRecordWorkerPool = service.NewUsageRecordWorkerPool(cfg)
					rec := httptest.NewRecorder()
					c, _ := gin.CreateTestContext(rec)
					c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"claude-sonnet-4-5","max_tokens":32,"messages":[{"role":"user","content":"hello"}]`+tc.field+`}`))
					c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), ctxkey.Group, group))
					c.Request.Header.Set("Anthropic-Beta", tc.beta)
					c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{ID: 7101, UserID: 8101, GroupID: &groupID, Group: group, User: &service.User{ID: 8101, Balance: 100}})
					c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 8101})
					h.Messages(c)
					h.usageRecordWorkerPool.Stop()
					require.Equal(t, 400, rec.Code, rec.Body.String())
					require.Equal(t, response, rec.Body.String())
					require.Equal(t, 1, upstream.calls)
					require.Equal(t, []int64{6101}, rpm.calls())
					n, row := usage.snapshot()
					require.Equal(t, 1, n)
					require.NotNil(t, row)
					want := 0.0
					if perRequest {
						want = .25
					}
					require.Equal(t, want, row.ActualCost)
					require.Len(t, ledger.charges, 1)
					require.Equal(t, want, ledger.charges[0].BalanceCost)
					require.False(t, logs.ContainsMessageAtLevel("gateway.compact_rejected_no_charge", "info"))
					require.False(t, logs.ContainsMessageAtLevel("gateway.forward_failed", "error"))
					t.Logf("RELAYROW %s debit=%g usage=%d rpm=%v status=%d body=%q sent=%q ops=%d/%q/%q", t.Name(), want, n, rpm.calls(), rec.Code, rec.Body.String(), upstream.sent, c.GetInt(service.OpsUpstreamStatusCodeKey), c.GetString(service.OpsUpstreamErrorMessageKey), c.GetString(service.OpsUpstreamErrorDetailKey))
				})
			}
		}
	}
}
