//go:build unit

package handler

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	middleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestMessagesFallbackBillingContext_UsesFallbackPricesForFailureAndSuccess(t *testing.T) {
	amount := func(v float64) *float64 { return &v }
	primary := &service.Group{ID: 1, Platform: service.PlatformAnthropic, RateMultiplier: 9, SubscriptionType: service.SubscriptionTypeSubscription}
	fallback := &service.Group{ID: 2, Platform: service.PlatformAnthropic, RateMultiplier: 2, SubscriptionType: service.SubscriptionTypeStandard,
		ModelPricing: []service.ChannelModelPricing{
			{Models: []string{"fallback-executor"}, InputPrice: amount(.001), OutputPrice: amount(.002)},
			{Models: []string{"fallback-advisor"}, InputPrice: amount(.003), OutputPrice: amount(.004), CacheWritePrice: amount(.005), CacheReadPrice: amount(.0005)},
		}}
	channels := service.NewChannelService(&openAIWSUsageHandlerChannelRepoStub{
		channels: []service.Channel{{ID: 202, Status: service.StatusActive, GroupIDs: []int64{2}, BillingModelSource: service.BillingModelSourceChannelMapped,
			ModelMapping: map[string]map[string]string{service.PlatformAnthropic: {"client-model": "fallback-executor"}}}},
		groupPlatforms: map[int64]string{2: service.PlatformAnthropic},
	}, nil, nil, nil, nil)
	usageRepo := &fakeFailureSinkUsageLogRepo{}
	cfg := &config.Config{}
	cfg.Default.RateMultiplier = 1
	prices := service.NewBillingService(cfg, nil)
	gw := service.NewGatewayService(nil, nil, usageRepo, nil, &fakeFailureSinkUserRepo{}, &fakeFailureSinkSubRepo{}, nil, nil, cfg, nil, nil,
		prices, nil, &service.BillingCacheService{}, nil, nil, &service.DeferredService{}, nil, nil, nil, nil, nil, nil, channels,
		service.NewModelPricingResolver(channels, prices), nil, nil, nil, nil)
	h := &GatewayHandler{gatewayService: gw}
	c := newFailureSinkTestContext("/v1/messages")
	c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), ctxkey.Group, primary))
	key := &service.APIKey{ID: 10, GroupID: &primary.ID, Group: primary, User: &service.User{ID: 20}}
	fallbackKey := cloneAPIKeyWithGroup(key, fallback)
	body := []byte(`{"model":"client-model","tools":[{"type":"advisor_20260301","model":"fallback-advisor"}]}`)
	parsed := &service.ParsedRequest{GroupID: key.GroupID}
	guard := newBillingSettlementGuard(guardDeps{sink: h.claudeFailureSink(c, key, &service.UserSubscription{ID: 30}, "client-model",
		service.ChannelMappingResult{ChannelID: 101, Mapped: true, MappedModel: "primary-executor"}, body)})
	mapping := h.applyMessagesFallbackBillingContext(c, guard, parsed, fallbackKey, "client-model", body)
	currentKey, ok := middleware.GetAPIKeyFromContext(c)
	require.True(t, ok)
	require.Same(t, fallbackKey, currentKey)
	require.Same(t, fallback, c.Request.Context().Value(ctxkey.Group))
	require.Equal(t, fallback.ID, *parsed.GroupID)
	require.Equal(t, int64(202), mapping.ChannelID)
	require.Equal(t, "fallback-executor", mapping.MappedModel)
	require.NoError(t, gw.ValidateAdvisorPricing(c.Request.Context(), currentKey, body))
	usage := service.ClaudeUsage{InputTokens: 10, OutputTokens: 2, IterationUsage: &service.ClaudeIterationUsage{Present: true, Terminal: true, AdvisorSeen: true,
		Iterations: []service.ClaudeUsageIteration{{Type: "message", Usage: service.UsageTokens{InputTokens: 10, OutputTokens: 2}},
			{Type: "advisor_message", Model: "fallback-advisor", Usage: service.UsageTokens{InputTokens: 3, OutputTokens: 4}}}}}
	account := &service.Account{ID: 40, Platform: service.PlatformAnthropic}
	guard.ObserveAttempt(account)
	guard.ObservePartialUsage(usage)
	guard.ObserveForwardOutcome(io.ErrUnexpectedEOF, true)
	guard.Flush()
	guard.Flush()
	calls, failed := usageRepo.snapshot()
	require.Equal(t, 1, calls)
	require.NotNil(t, failed)
	require.Equal(t, fallback.ID, *failed.GroupID)
	require.Nil(t, failed.SubscriptionID)
	require.Equal(t, int64(202), *failed.ChannelID)
	require.InDelta(t, .039, failed.TotalCost, 1e-8)
	require.InDelta(t, .078, failed.ActualCost, 1e-8)
	require.Equal(t, "complete", failed.BillingDetails.Status)
	require.NoError(t, gw.RecordUsage(c.Request.Context(), &service.RecordUsageInput{
		Result: &service.ForwardResult{RequestID: "fallback-success", Model: "client-model", Usage: usage.Clone()}, APIKey: fallbackKey,
		User: fallbackKey.User, Account: account, ChannelUsageFields: mapping.ToUsageFields("client-model", ""),
	}))
	calls, success := usageRepo.snapshot()
	require.Equal(t, 2, calls)
	require.Equal(t, failed.TotalCost, success.TotalCost)
	require.Equal(t, failed.ActualCost, success.ActualCost)
	require.Equal(t, failed.GroupID, success.GroupID)
	require.Equal(t, failed.ChannelID, success.ChannelID)
	require.Nil(t, success.SubscriptionID)
}

type messagesProbeBillingRepository struct {
	service.UsageBillingRepository
	logs []*service.UsageLog
}

func (r *messagesProbeBillingRepository) ApplyWithUsageLog(_ context.Context, _ *service.UsageBillingCommand, log *service.UsageLog) (*service.UsageBillingApplyResult, error) {
	copy := *log
	copy.BillingDetails = log.BillingDetails.Clone()
	r.logs = append(r.logs, &copy)
	return &service.UsageBillingApplyResult{Applied: true, UsageLogPersisted: true}, nil
}

type messagesProbeUpstream struct {
	service.HTTPUpstream
	calls int
}

type messagesProbeCache struct{ *handlerInflightCache }

func (*messagesProbeCache) DeductUserBalance(context.Context, int64, float64) error { return nil }

func (u *messagesProbeUpstream) DoWithTLS(req *http.Request, proxy string, id int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxy, id, concurrency)
}

func (u *messagesProbeUpstream) Do(_ *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	u.calls++
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}},
		Body: io.NopCloser(strings.NewReader(`{"id":"msg_probe","type":"message","role":"assistant","model":"claude-sonnet-4","content":[{"type":"text","text":"12"}],"stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":2,"iterations":[{"type":"message","input_tokens":10,"output_tokens":2},{"type":"advisor_message","model":"claude-sonnet-4","input_tokens":3,"output_tokens":4}]}}`))}, nil
}

func TestGatewayMessages_ProbeLeaderPublishesAfterDurableAdvisorBill(t *testing.T) {
	gin.SetMode(gin.TestMode)
	previousSettings := defaultProbeSettings.Swap(nil)
	t.Cleanup(func() { defaultProbeSettings.Store(previousSettings) })
	group := &service.Group{ID: 51, Hydrated: true, Platform: service.PlatformAnthropic, Status: service.StatusActive, RateMultiplier: 1}
	account := &service.Account{ID: 61, Platform: service.PlatformAnthropic, Type: service.AccountTypeAPIKey, Status: service.StatusActive,
		Schedulable: true, Concurrency: 1, Credentials: map[string]any{"api_key": "fixture"}, AccountGroups: []service.AccountGroup{{AccountID: 61, GroupID: 51}}}
	snapshot := service.NewSchedulerSnapshotService(&fakeSchedulerCache{accounts: []*service.Account{account}}, nil, nil, nil, nil)
	cfg := &config.Config{}
	cfg.Default.RateMultiplier = 1
	billingCache := service.NewBillingCacheService(&messagesProbeCache{newHandlerInflightCache(100)}, nil, nil, nil, nil, nil, cfg, nil)
	t.Cleanup(billingCache.Stop)
	billingRepo := &messagesProbeBillingRepository{}
	usageRepo := &fakeFailureSinkUsageLogRepo{}
	upstream := &messagesProbeUpstream{}
	gw := service.NewGatewayService(nil, &fakeGroupRepo{group: group}, usageRepo, billingRepo, nil, nil, nil, nil, cfg, snapshot, nil,
		service.NewBillingService(cfg, nil), nil, billingCache, nil, upstream, &service.DeferredService{}, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	h := &GatewayHandler{gatewayService: gw, billingCacheService: billingCache, cfg: cfg, maxAccountSwitches: 1,
		concurrencyHelper: NewConcurrencyHelper(service.NewConcurrencyService(&fakeConcurrencyCache{}), SSEPingFormatClaude, 0),
		probeCoalescer:    NewProbeCoalescer(ProbeCoalescerConfig{Mode: ProbeCoalescingActive, Window: time.Minute, LeaderTimeout: time.Second, AttemptBudget: 2})}
	key := &service.APIKey{ID: 71, UserID: 81, GroupID: &group.ID, Group: group, User: &service.User{ID: 81, Balance: 100}}
	require.True(t, gw.CanBillSyntheticProbe(context.Background(), key, "claude-sonnet-4"))
	body := `{"model":"claude-sonnet-4","max_tokens":50,"messages":[{"role":"user","content":"Calculate and respond with ONLY the number, nothing else.\n\nQ: 3 + 5 = ?\nA: 8\n\nQ: 12 - 7 = ?\nA: 5\n\nQ: 19 - 7 = ?\nA:"}]}`
	for attempt := range 2 {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
		c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), ctxkey.Group, group))
		c.Set(string(middleware.ContextKeyAPIKey), key)
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 81})
		h.Messages(c)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		require.Contains(t, rec.Body.String(), `"text":"12"`)
		if attempt == 0 {
			require.Len(t, h.probeCoalescer.entries, 1)
			for _, entry := range h.probeCoalescer.entries {
				require.True(t, entry.billingReady, "leader must finish its durable bill")
				require.NoError(t, entry.billingErr)
				require.True(t, entry.result.success, "leader must publish a valid response with its account")
			}
		}
	}
	require.Equal(t, 1, upstream.calls, "a billed successful leader must be reusable by its follower")
	require.Len(t, billingRepo.logs, 2)
	leader, follower := billingRepo.logs[0], billingRepo.logs[1]
	require.True(t, leader.ProviderCostRecorded)
	require.False(t, follower.ProviderCostRecorded)
	require.True(t, follower.ProbeCoalesced)
	require.Zero(t, follower.AccountCost())
	require.Equal(t, leader.ActualCost, follower.ActualCost)
	require.Len(t, follower.BillingDetails.Components, 2)
	require.Equal(t, "advisor", follower.BillingDetails.Components[1].Role)
	require.Equal(t, 3, follower.BillingDetails.Components[1].InputTokens)
	require.NotEqual(t, leader.RequestID, follower.RequestID)
}
