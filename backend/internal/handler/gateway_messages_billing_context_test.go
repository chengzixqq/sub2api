//go:build unit

package handler

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	middleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
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
	logs    []*service.UsageLog
	charges []*service.UsageBillingCommand
}

func (r *messagesProbeBillingRepository) Apply(_ context.Context, cmd *service.UsageBillingCommand) (*service.UsageBillingApplyResult, error) {
	r.charges = append(r.charges, cmd)
	return &service.UsageBillingApplyResult{Applied: true}, nil
}

func (r *messagesProbeBillingRepository) ApplyWithUsageLog(_ context.Context, _ *service.UsageBillingCommand, log *service.UsageLog) (*service.UsageBillingApplyResult, error) {
	copy := *log
	copy.BillingDetails = log.BillingDetails.Clone()
	r.logs = append(r.logs, &copy)
	return &service.UsageBillingApplyResult{Applied: true, UsageLogPersisted: true}, nil
}

type messagesProbeUpstream struct {
	service.HTTPUpstream
	calls   int
	respond func(accountID int64, body []byte) *http.Response
}

type messagesProbeCache struct{ *handlerInflightCache }

func (*messagesProbeCache) DeductUserBalance(context.Context, int64, float64) error { return nil }

func (u *messagesProbeUpstream) DoWithTLS(req *http.Request, proxy string, id int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxy, id, concurrency)
}

func (u *messagesProbeUpstream) Do(req *http.Request, _ string, accountID int64, _ int) (*http.Response, error) {
	u.calls++
	service.MarkOpsUpstreamAttemptStart(req.Context(), accountID)
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	if u.respond != nil {
		return u.respond(accountID, body), nil
	}
	if gjson.GetBytes(body, "stream").Bool() {
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}},
			Body: io.NopCloser(strings.NewReader("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"model\":\"claude-sonnet-4\",\"usage\":{\"input_tokens\":3}}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"))}, nil
	}
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}},
		Body: io.NopCloser(strings.NewReader(`{"id":"msg_probe","type":"message","role":"assistant","model":"claude-sonnet-4","content":[{"type":"text","text":"12"}],"stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":2,"iterations":[{"type":"message","input_tokens":10,"output_tokens":2},{"type":"advisor_message","model":"claude-sonnet-4","input_tokens":3,"output_tokens":4}]}}`))}, nil
}

type messagesAlignmentFixture struct {
	additionalAllowed []string
	memberExtra       map[string]any
	failoverExtra     map[string]any
	grantedExtra      map[string]any
	queryErr          error
	globalReject      bool
	ccFallback        bool
	clientBeta        string
	geminiMixed       bool
}

type messagesAlignmentAccounts struct {
	*gatewayModelsDispatchRepoStub
	accounts []*service.Account
	queryErr error
}

func (r *messagesAlignmentAccounts) HasGroupAlignmentCandidate(_ context.Context, accountTypes []string, ids []int64, groupID *int64, _ bool, platform string, mixedOnly bool) (bool, error) {
	if r.queryErr != nil {
		return false, r.queryErr
	}
	for _, account := range r.byGroup[*groupID] {
		if account.Platform == platform && (!mixedOnly || account.IsMixedSchedulingEnabled()) && slices.Contains(ids, account.ID) && slices.Contains(accountTypes, account.Type) {
			return true, nil
		}
	}
	return false, nil
}

func (r *messagesAlignmentAccounts) GetByIDs(_ context.Context, ids []int64) ([]*service.Account, error) {
	if r.queryErr != nil {
		return nil, r.queryErr
	}
	var result []*service.Account
	for _, id := range ids {
		for _, account := range r.accounts {
			if account.ID == id {
				result = append(result, account)
			}
		}
	}
	return result, nil
}

func (r *messagesAlignmentAccounts) GetByID(_ context.Context, id int64) (*service.Account, error) {
	for _, account := range r.accounts {
		if account.ID == id {
			return account, nil
		}
	}
	return nil, service.ErrAccountNotFound
}

type messagesAlignmentSettings struct {
	toggleSettingRepo
	accounts []*service.Account
	queryErr error
}

func (r *messagesAlignmentSettings) ListClaudeAlignmentOverrides(context.Context, []string) ([]service.ClaudeAlignmentOverrides, error) {
	var result []service.ClaudeAlignmentOverrides
	for _, account := range r.accounts {
		if fields, ok := account.Extra[service.AccountClaudeCustomizationExtraKey].(map[string]any); ok {
			result = append(result, service.ClaudeAlignmentOverrides{AccountID: account.ID, AccountType: account.Type, Platform: account.Platform, Fields: fields})
		}
	}
	return result, r.queryErr
}

func newMessagesBillingTestHandler(t *testing.T, fixture messagesAlignmentFixture) (*GatewayHandler, *service.APIKey, *messagesProbeBillingRepository, *fakeFailureSinkUsageLogRepo, *messagesProbeUpstream) {
	t.Helper()
	group := &service.Group{ID: 51, Hydrated: true, Platform: service.PlatformAnthropic, Status: service.StatusActive, RateMultiplier: 1}
	account := &service.Account{ID: 61, Platform: service.PlatformAnthropic, Type: service.AccountTypeAPIKey, Status: service.StatusActive,
		Schedulable: true, Concurrency: 1, Credentials: map[string]any{"api_key": "fixture"}, AccountGroups: []service.AccountGroup{{AccountID: 61, GroupID: 51}}}
	account.Extra = fixture.memberExtra
	if fixture.geminiMixed {
		group.Platform = service.PlatformGemini
		account.Platform, account.Type = service.PlatformAntigravity, service.AccountTypeOAuth
		account.Credentials = map[string]any{"model_mapping": map[string]any{"claude-sonnet-4": "claude-sonnet-4"}}
	}
	accountRepo := &messagesAlignmentAccounts{gatewayModelsDispatchRepoStub: &gatewayModelsDispatchRepoStub{
		gatewayModelsAccountRepoStub: &gatewayModelsAccountRepoStub{byGroup: map[int64][]service.Account{group.ID: {*account}}},
	}, accounts: []*service.Account{account}, queryErr: fixture.queryErr}
	if fixture.failoverExtra != nil {
		next := *account
		next.ID, next.Priority, next.Extra = 63, account.Priority+1, fixture.failoverExtra
		next.AccountGroups = []service.AccountGroup{{AccountID: next.ID, GroupID: group.ID}}
		accountRepo.accounts = append(accountRepo.accounts, &next)
		accountRepo.byGroup[group.ID] = append(accountRepo.byGroup[group.ID], next)
	}
	if fixture.grantedExtra != nil {
		extra := *account
		extra.ID, extra.AccountGroups, extra.GroupIDs, extra.Extra = 62, nil, []int64{54}, fixture.grantedExtra
		extra.DispatchScope = service.DispatchScopeAuthorizedOnly
		accountRepo.accounts = append(accountRepo.accounts, &extra)
		accountRepo.policy = &service.AccountDispatchPolicy{Mode: service.AccountDispatchModeReplace,
			Accounts: []service.AccountDispatchGrant{{AccountID: extra.ID, Origin: service.AccountDispatchOriginExtra}}}
	}
	snapshot := service.NewSchedulerSnapshotService(&groupScopedSchedulerCache{fakeSchedulerCache: &fakeSchedulerCache{accounts: accountRepo.accounts}}, nil, nil, nil, nil)
	cfg := &config.Config{}
	cfg.Security.ResponseHeaders = config.ResponseHeaderConfig{Enabled: len(fixture.additionalAllowed) > 0, AdditionalAllowed: fixture.additionalAllowed}
	cfg.Default.RateMultiplier = 1
	billingCache := service.NewBillingCacheService(&messagesProbeCache{newHandlerInflightCache(100)}, nil, nil, nil, nil, nil, cfg, nil)
	t.Cleanup(billingCache.Stop)
	billingRepo := &messagesProbeBillingRepository{}
	usageRepo := &fakeFailureSinkUsageLogRepo{}
	upstream := &messagesProbeUpstream{}
	settings := &messagesAlignmentSettings{accounts: accountRepo.accounts, queryErr: fixture.queryErr}
	if fixture.globalReject {
		settings.values = map[string]string{service.SettingKeyClaudeCustomization: `{"context_management_missing_beta":"reject"}`}
	}
	settingService := service.NewSettingService(settings, cfg)
	gw := service.NewGatewayService(accountRepo, &fakeGroupRepo{group: group}, usageRepo, billingRepo, nil, nil, nil, nil, cfg, snapshot, nil,
		service.NewBillingService(cfg, nil), &service.RateLimitService{}, billingCache, nil, upstream, &service.DeferredService{}, nil, nil, nil, nil, settingService, nil, nil, nil, nil, nil, nil, nil)
	h := &GatewayHandler{gatewayService: gw, billingCacheService: billingCache, cfg: cfg, maxAccountSwitches: 1,
		concurrencyHelper: NewConcurrencyHelper(service.NewConcurrencyService(&fakeConcurrencyCache{}), SSEPingFormatClaude, 0),
		probeCoalescer:    NewProbeCoalescer(ProbeCoalescerConfig{Mode: ProbeCoalescingActive, Window: time.Minute, LeaderTimeout: time.Second, AttemptBudget: 2})}
	if fixture.geminiMixed {
		h.antigravityGatewayService = service.NewAntigravityGatewayService(accountRepo, nil, snapshot, nil, nil, upstream, settingService, nil)
	}
	keyGroup := group
	if fixture.ccFallback {
		keyGroup = &service.Group{ID: 50, Hydrated: true, Platform: service.PlatformAnthropic, Status: service.StatusActive, RateMultiplier: 1,
			ClaudeCodeOnly: true, FallbackGroupID: &group.ID}
	}
	key := &service.APIKey{ID: 71, UserID: 81, GroupID: &keyGroup.ID, Group: keyGroup, User: &service.User{ID: 81, Balance: 100}}
	return h, key, billingRepo, usageRepo, upstream
}

func TestQueuePing_ContextManagementStripDefault(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{{"default", nil}, {"repository error", errors.New("account list unavailable")}} {
		t.Run(tc.name, func(t *testing.T) { testQueuePingStrip(t, tc.err) })
	}
}

func TestOfficialCodedError(t *testing.T) {
	for _, id := range []string{"", "req_selected"} {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest("POST", "/v1/messages", nil)
		if id != "" {
			c.Set("anthropic_filled_request_id", id)
			c.Header("Request-Id", id)
		}
		(&GatewayHandler{}).errorResponseWithCode(c, 400, "invalid_request_error", "bad_field", "invalid field")
		want := `{"type":"error","error":{"type":"invalid_request_error","code":"bad_field","message":"invalid field"}}`
		if id != "" {
			want = `{"type":"error","error":{"type":"invalid_request_error","code":"bad_field","message":"invalid field"},"request_id":"req_selected"}`
		}
		require.Equal(t, want, rec.Body.String())
		require.Equal(t, id, rec.Result().Header.Get("Request-Id"))
	}
}

func TestRequestIDFill_QueuedStreamAndFailoverError(t *testing.T) {
	for _, tc := range []struct {
		name, body, contentType string
		status, clientStatus    int
	}{
		{"stream", "event: ping\ndata: {\"type\":\"ping\"}\n\nevent: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"api_error\",\"message\":\"unavailable\"}}\n\n", "text/event-stream", 200, 200},
		{"final HTTP error", `{"error":{"message":"unavailable"},"request_id":"req_body"}`, "application/json", 503, 502},
	} {
		t.Run(tc.name, func(t *testing.T) {
			extra := map[string]any{service.AccountClaudeCustomizationExtraKey: map[string]any{
				service.ClaudeFieldRequestIDMode: "fill", service.ClaudeFieldUpstreamHeaderPassthrough: "on",
			}}
			h, key, _, _, up := newMessagesBillingTestHandler(t, messagesAlignmentFixture{memberExtra: extra})
			up.respond = func(int64, []byte) *http.Response {
				return &http.Response{StatusCode: tc.status, Header: http.Header{"Content-Type": {tc.contentType}, "Request-Id": {"req_final"}, "Anthropic-Ratelimit-Tokens-Limit": {"12"}}, Body: io.NopCloser(strings.NewReader(tc.body))}
			}
			cache := &helperConcurrencyCacheStub{userSeq: []bool{false, true}, accountSeq: []bool{true}, waitAllowed: true}
			h.concurrencyHelper = NewConcurrencyHelper(service.NewConcurrencyService(cache), SSEPingFormatClaude, time.Millisecond)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(queuedContextManagementBody))
			c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), ctxkey.Group, key.Group))
			c.Set(string(middleware.ContextKeyAPIKey), key)
			c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: key.UserID, Concurrency: 1})
			h.Messages(c)
			require.Equal(t, tc.clientStatus, rec.Code, rec.Body.String())
			require.Equal(t, "req_final", rec.Result().Header.Get("Request-Id"), rec.Body.String())
			require.Equal(t, "12", rec.Result().Header.Get("Anthropic-Ratelimit-Tokens-Limit"))
			require.Contains(t, rec.Body.String(), `,"request_id":"req_final"}`)
			require.NotContains(t, rec.Body.String(), `"request_id":"req_body"`)
		})
	}
}

func TestHeaderPassthrough_EarlyStreamFailoverKeepsWinningAccountHeaders(t *testing.T) {
	extra := map[string]any{service.AccountClaudeCustomizationExtraKey: map[string]any{
		service.ClaudeFieldRequestIDMode: "fill", service.ClaudeFieldUpstreamHeaderPassthrough: "on",
	}}
	h, key, _, _, up := newMessagesBillingTestHandler(t, messagesAlignmentFixture{memberExtra: extra, failoverExtra: map[string]any{}})
	up.respond = func(id int64, _ []byte) *http.Response {
		header := http.Header{"Content-Type": {"text/event-stream"}, "Request-Id": {"req_loser"}, "Anthropic-Ratelimit-Tokens-Limit": {"1"}}
		body := "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"api_error\",\"message\":\"unavailable\"}}\n\n"
		if id != 61 {
			header.Set("Request-Id", "req_winner")
			body = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"model\":\"claude-sonnet-4\",\"usage\":{\"input_tokens\":3}}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
		}
		return &http.Response{StatusCode: 200, Header: header, Body: io.NopCloser(strings.NewReader(body))}
	}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(queuedContextManagementBody))
	c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), ctxkey.Group, key.Group))
	c.Set(string(middleware.ContextKeyAPIKey), key)
	c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: key.UserID, Concurrency: 1})
	h.Messages(c)
	require.Equal(t, 2, up.calls)
	require.Contains(t, rec.Body.String(), "event: message_stop")
	require.Empty(t, rec.Result().Header.Get("Request-Id"))
	require.Empty(t, rec.Result().Header.Get("Anthropic-Ratelimit-Tokens-Limit"))
}

func testQueuePingStrip(t *testing.T, queryErr error) {
	testQueuePingAllowed(t, messagesAlignmentFixture{queryErr: queryErr}, queuedContextManagementBody)
}

func runOfficialStreamRequest(h *GatewayHandler, key *service.APIKey) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(queuedContextManagementBody))
	c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), ctxkey.Group, key.Group))
	c.Set(string(middleware.ContextKeyAPIKey), key)
	c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: key.UserID, Concurrency: 1})
	h.Messages(c)
	return rec
}

func TestHeaderPassthrough_ClearsFailedAttemptHeaders(t *testing.T) {
	const rateHeader = "Anthropic-Ratelimit-Tokens-Limit"
	fixture := messagesAlignmentFixture{
		additionalAllowed: []string{"request-id", rateHeader},
		failoverExtra: map[string]any{service.AccountClaudeCustomizationExtraKey: map[string]any{
			service.ClaudeFieldUpstreamHeaderPassthrough: service.ClaudeHeaderPassthroughOn,
		}},
	}
	h, key, _, _, up := newMessagesBillingTestHandler(t, fixture)
	up.respond = func(id int64, _ []byte) *http.Response {
		header := http.Header{"Content-Type": {"text/event-stream"}}
		body := "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
		if id == 61 {
			header.Set("Request-Id", "req_A_failed")
			header.Set(rateHeader, "17")
			header.Set("Retry-After", "8")
			body = "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"api_error\",\"message\":\"unavailable\"}}\n\n"
		}
		return &http.Response{StatusCode: 200, Header: header, Body: io.NopCloser(strings.NewReader(body))}
	}
	rec := runOfficialStreamRequest(h, key)
	require.Equal(t, 2, up.calls)
	require.Equal(t, 200, rec.Code)
	require.Contains(t, rec.Body.String(), "event: message_stop")
	for _, name := range []string{"Request-Id", rateHeader, "Retry-After"} {
		t.Run(name, func(t *testing.T) { require.NotContains(t, rec.Result().Header, name) })
	}
}

func TestRequestID_PassthroughStreamErrorBeforeWrite(t *testing.T) {
	for _, mode := range []string{"passthrough", "fill"} {
		for _, tc := range []struct {
			name       string
			body       io.Reader
			upstreamID string
		}{
			{"oversized line", strings.NewReader(strings.Repeat("x", 70000)), "req_line"},
			{"read error", iotest.ErrReader(io.ErrUnexpectedEOF), ""},
			{"rejected frame", strings.NewReader("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"), "req_frame"},
		} {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				extra := map[string]any{"anthropic_passthrough": true, service.AccountClaudeCustomizationExtraKey: map[string]any{service.ClaudeFieldRequestIDMode: mode}}
				h, key, _, _, up := newMessagesBillingTestHandler(t, messagesAlignmentFixture{memberExtra: extra})
				h.cfg.Gateway.MaxLineSize = 65536
				up.respond = func(int64, []byte) *http.Response {
					header := http.Header{"Content-Type": {"text/event-stream"}, "Retry-After": {"8"}}
					header.Set("Request-Id", tc.upstreamID)
					return &http.Response{StatusCode: 200, Header: header, Body: io.NopCloser(tc.body),
						Request: httptest.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages", nil)}
				}
				rec := runOfficialStreamRequest(h, key)
				require.Equal(t, 1, up.calls)
				require.Equal(t, 502, rec.Code, rec.Body.String())
				header := rec.Result().Header
				id := header.Get("Request-Id")
				if mode == "fill" {
					if tc.upstreamID == "" {
						require.Regexp(t, "^req_011C[1-9A-HJ-NP-Za-km-z]{20}$", id)
					} else {
						require.Equal(t, tc.upstreamID, id)
					}
					require.Equal(t, id, gjson.Get(rec.Body.String(), "request_id").String())
					require.Empty(t, header.Get("Retry-After"))
				} else {
					require.Empty(t, id)
					require.Equal(t, `{"type":"error","error":{"type":"api_error","message":"Upstream request failed"}}`, rec.Body.String())
				}
			})
		}
	}
}

const queuedContextManagementBody = `{"model":"claude-sonnet-4","stream":true,"context_management":null,"max_tokens":50,"messages":[{"role":"user","content":"Hello"}]}`

func testQueuePingAllowed(t *testing.T, fixture messagesAlignmentFixture, body string) {
	t.Helper()
	h, key, _, _, upstream := newMessagesBillingTestHandler(t, fixture)
	cache := &helperConcurrencyCacheStub{userSeq: []bool{false, true}, accountSeq: []bool{true}, waitAllowed: true}
	h.concurrencyHelper = NewConcurrencyHelper(service.NewConcurrencyService(cache), SSEPingFormatClaude, time.Millisecond)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), ctxkey.Group, key.Group))
	c.Set(string(middleware.ContextKeyAPIKey), key)
	c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: key.UserID, Concurrency: 1})
	h.Messages(c)
	require.Equal(t, 2, cache.userAcquireCalls)
	require.Equal(t, 1, upstream.calls)
	require.Equal(t, http.StatusOK, rec.Code)
	require.True(t, strings.HasPrefix(rec.Body.String(), string(SSEPingFormatClaude)), rec.Body.String())
	require.Contains(t, rec.Body.String(), "event: message_stop")
}

func TestQueuePing_ContextManagementRejectSkipsBillingAndFailover(t *testing.T) {
	reject := map[string]any{
		service.AccountClaudeCustomizationExtraKey: map[string]any{"context_management_missing_beta": service.ClaudeAlignReject},
	}
	t.Run("member", func(t *testing.T) { testQueuePingReject(t, messagesAlignmentFixture{memberExtra: reject}) })
	t.Run("extra grant outside group", func(t *testing.T) { testQueuePingReject(t, messagesAlignmentFixture{grantedExtra: reject}) })
	t.Run("global reject with unavailable overrides", func(t *testing.T) {
		testQueuePingReject(t, messagesAlignmentFixture{globalReject: true, queryErr: errors.New("override list unavailable")})
	})
	t.Run("non Claude Code client in fallback group", func(t *testing.T) {
		testQueuePingReject(t, messagesAlignmentFixture{memberExtra: reject, ccFallback: true})
	})
}

func testQueuePingReject(t *testing.T, fixture messagesAlignmentFixture) {
	testQueuedAlignmentReject(t, fixture, queuedContextManagementBody, "context_management: Extra inputs are not permitted")
}

func TestContextManagementRejectAfterFailoverSettlesFailedRequest(t *testing.T) {
	reject := map[string]any{service.AccountClaudeCustomizationExtraKey: map[string]any{service.ClaudeFieldContextManagementMissingBeta: service.ClaudeAlignReject}}
	h, key, billing, usage, upstream := newMessagesBillingTestHandler(t, messagesAlignmentFixture{failoverExtra: reject})
	var ids []int64
	upstream.respond = func(id int64, body []byte) *http.Response {
		ids = append(ids, id)
		if id == 61 {
			require.False(t, gjson.GetBytes(body, "context_management").Exists())
			return &http.Response{StatusCode: http.StatusServiceUnavailable, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"error":{"type":"api_error","message":"unavailable"}}`))}
		}
		require.True(t, gjson.GetBytes(body, "context_management").Exists())
		// EOF after message_start leaves real input usage for the failure settlement.
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}},
			Body: io.NopCloser(strings.NewReader("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_failover\",\"model\":\"claude-sonnet-4\",\"usage\":{\"input_tokens\":13}}}\n\n"))}
	}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(queuedContextManagementBody))
	c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), ctxkey.Group, key.Group))
	c.Set(string(middleware.ContextKeyAPIKey), key)
	c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: key.UserID, Concurrency: 1})
	service.InstallOpsUpstreamAttemptClock(c)
	h.Messages(c)
	require.Equal(t, []int64{61, 63}, ids)
	require.False(t, service.IsOpsRequestScopedError(c))
	require.Len(t, billing.charges, 1)
	require.Positive(t, billing.charges[0].BalanceCost)
	calls, log := usage.snapshot()
	require.Equal(t, 1, calls)
	require.Equal(t, int64(63), log.AccountID)
	require.Equal(t, 13, log.InputTokens)
	require.Equal(t, string(service.BillingProvenanceFailedUpstream), *log.BillingProvenance)
}

func TestQueuePing_CacheControlLimit(t *testing.T) {
	const block = `{"type":"text","text":"Hello","cache_control":{"type":"ephemeral"}}`
	body := `{"model":"claude-sonnet-4","stream":true,"max_tokens":50,"cache_control":{"type":"ephemeral"},"messages":[{"role":"user","content":[` + strings.Repeat(block+",", 3) + block + `]}]}`
	t.Run("default trim pings", func(t *testing.T) {
		testQueuePingAllowed(t, messagesAlignmentFixture{}, body)
	})
	t.Run("account override returns JSON before ping", func(t *testing.T) {
		reject := map[string]any{service.AccountClaudeCustomizationExtraKey: map[string]any{service.ClaudeFieldCacheControlLimitMode: service.ClaudeAlignReject}}
		testQueuedAlignmentReject(t, messagesAlignmentFixture{memberExtra: reject}, body, "A maximum of 4 blocks with cache_control may be provided. Found 5.")
	})
}

func TestQueuePing_UnknownBetaRejectSkipsBillingAndFailover(t *testing.T) {
	reject := map[string]any{service.AccountClaudeCustomizationExtraKey: map[string]any{service.ClaudeFieldUnknownBetaHeader: service.ClaudeAlignReject}}
	const message = "Unexpected value(s) `not-a-real-beta-20260101` for the `anthropic-beta` header. Please consult our documentation at platform.claude.com/docs or try again without the header."
	t.Run("Anthropic", func(t *testing.T) {
		testQueuedAlignmentReject(t, messagesAlignmentFixture{memberExtra: reject, clientBeta: "not-a-real-beta-20260101"}, queuedContextManagementBody, message)
	})
	t.Run("Gemini with mixed Antigravity member", func(t *testing.T) {
		extra := map[string]any{"mixed_scheduling": true, service.AccountClaudeCustomizationExtraKey: reject[service.AccountClaudeCustomizationExtraKey]}
		testQueuedAlignmentReject(t, messagesAlignmentFixture{memberExtra: extra, geminiMixed: true, clientBeta: "not-a-real-beta-20260101"}, queuedContextManagementBody, message)
	})
}

func testQueuedAlignmentReject(t *testing.T, fixture messagesAlignmentFixture, body, message string) {
	t.Helper()
	h, key, billing, usage, upstream := newMessagesBillingTestHandler(t, fixture)
	cache := &helperConcurrencyCacheStub{userSeq: []bool{false, true}, accountSeq: []bool{true}, waitAllowed: true}
	h.concurrencyHelper = NewConcurrencyHelper(service.NewConcurrencyService(cache), SSEPingFormatClaude, time.Millisecond)
	rec := httptest.NewRecorder()
	setupOpsErrorLogTestQueue(t, 1)
	ops := service.NewOpsService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	router := gin.New()
	router.POST("/v1/messages", OpsErrorLoggerMiddleware(ops), func(c *gin.Context) {
		ctx := context.WithValue(c.Request.Context(), ctxkey.Group, key.Group)
		ctx = context.WithValue(ctx, ctxkey.UserID, key.UserID)
		c.Request = c.Request.WithContext(context.WithValue(ctx, ctxkey.AccountDispatchGroupID, *key.GroupID))
		c.Set(string(middleware.ContextKeyAPIKey), key)
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: key.UserID, Concurrency: 1})
		h.Messages(c)
		require.True(t, service.IsOpsRequestScopedError(c))
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	if fixture.clientBeta != "" {
		req.Header.Set("Anthropic-Beta", fixture.clientBeta)
	}
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	id := rec.Header().Get("request-id")
	require.NotEmpty(t, id)
	require.Contains(t, rec.Header().Get("Content-Type"), "application/json")
	require.Equal(t, `{"type":"error","error":{"type":"invalid_request_error","message":"`+message+`"},"request_id":"`+id+`"}`, rec.Body.String())
	require.Len(t, opsErrorLogQueue, 1)
	entry := (<-opsErrorLogQueue).entry
	require.Equal(t, "request", entry.ErrorPhase)
	require.Equal(t, "client", entry.ErrorOwner)
	require.Equal(t, "client_request", entry.ErrorSource)
	require.Equal(t, 2, cache.userAcquireCalls)
	require.Zero(t, upstream.calls)
	require.Empty(t, billing.logs)
	require.Empty(t, billing.charges)
	calls, _ := usage.snapshot()
	require.Zero(t, calls)
}

func TestGatewayMessages_ProbeLeaderPublishesAfterDurableAdvisorBill(t *testing.T) {
	gin.SetMode(gin.TestMode)
	previousSettings := defaultProbeSettings.Swap(nil)
	t.Cleanup(func() { defaultProbeSettings.Store(previousSettings) })
	h, key, billingRepo, _, upstream := newMessagesBillingTestHandler(t, messagesAlignmentFixture{})
	gw, group := h.gatewayService, key.Group
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
