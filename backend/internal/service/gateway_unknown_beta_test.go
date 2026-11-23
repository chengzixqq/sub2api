//go:build unit

package service

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestUnknownBeta_FormatGolden(t *testing.T) {
	require.Equal(t, "Unexpected value(s) `not-a-real-beta-20260101` for the `anthropic-beta` header. Please consult our documentation at platform.claude.com/docs or try again without the header.", formatUnknownBetaMessage([]string{"not-a-real-beta-20260101"}))
	require.Equal(t, "Unexpected value(s) `aaa-bad-1`, `bbb-bad-2` for the `anthropic-beta` header. Please consult our documentation at platform.claude.com/docs or try again without the header.", formatUnknownBetaMessage([]string{"aaa-bad-1", "bbb-bad-2"}))
}

func TestUnknownBeta_DedupAndOrder(t *testing.T) {
	require.Equal(t, []string{"aaa-bad-1", "bbb-bad-2"}, unknownBetaTokens(" interleaved-thinking-2025-05-14,aaa-bad-1,,aaa-bad-1, bbb-bad-2,", nil))
	require.Empty(t, unknownBetaTokens(" , ,", nil))
}

func TestUnknownBeta_PolicyRuleMakesKnown(t *testing.T) {
	rules := []BetaPolicyRule{{BetaToken: "future", Action: BetaPolicyActionPass, Scope: BetaPolicyScopeOAuth}, {BetaToken: "blocked", Action: BetaPolicyActionBlock}, {BetaToken: "filtered", Action: BetaPolicyActionFilter}}
	require.Equal(t, []string{"FUTURE"}, unknownBetaTokens("future,blocked,filtered,FUTURE", rules))
}

func TestKnownBeta_Bedrock(t *testing.T) {
	for token, allowed := range bedrockSupportedBetaTokens {
		if allowed {
			require.True(t, claude.IsKnownBeta(token), token)
		}
	}
	for token := range bedrockBetaTokenTransforms {
		require.True(t, claude.IsKnownBeta(token), token)
	}
}

func TestUnknownBeta_RawBeforeBedrockCompat(t *testing.T) {
	up := &fallbackSequenceUpstream{responses: []*http.Response{cacheControlLimitResponse(http.StatusOK, cacheControlLimitOKMessage)}}
	policy := DefaultClaudeCustomizationSettings()
	policy.UnknownBetaHeader = ClaudeAlignReject
	s := fallbackNativeGatewayForTestWithPolicy(t, up, nil, policy)
	group := int64(1)
	s.channelService = &ChannelService{}
	s.channelService.cache.Store(&channelCache{loadedAt: time.Now(), channelByGroupID: map[int64]*Channel{group: {Status: StatusActive, FeaturesConfig: map[string]any{featureKeyBedrockCCCompat: true}}}})
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	c.Request.Header.Set("Anthropic-Beta", "one-bad")
	account := fallbackNativeAccountForTest(true)
	s.ApplyBedrockCCCompat(c, []byte(`{}`), "claude-opus-4-6", account, &group)
	require.Empty(t, c.Request.Header.Get("Anthropic-Beta"))
	require.ErrorIs(t, forwardUnknownBeta(t, s, c, account), ErrLocalAnthropicRejected)
	require.Empty(t, up.bodies)
}

func TestQueuePing_UnknownBetaAccountOverride(t *testing.T) {
	group := int64(4)
	account := &Account{ID: 8, Type: AccountTypeOAuth, Platform: PlatformAntigravity, GroupIDs: []int64{group}, Extra: map[string]any{AccountClaudeCustomizationExtraKey: map[string]any{ClaudeFieldUnknownBetaHeader: ClaudeAlignReject}}}
	repo := &queueAlignmentSettings{accounts: []*Account{account}}
	groups := &mockGroupRepoForGateway{groups: map[int64]*Group{group: {ID: group, Platform: PlatformAntigravity}}}
	s := &GatewayService{
		settingService: NewSettingService(repo, nil),
		accountRepo:    &queueAlignmentAccounts{accountDispatchTestRepo: accountDispatchTestRepo{accounts: []*Account{account}}},
		groupRepo:      groups,
	}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/antigravity/v1/messages", nil)
	c.Request.Header.Set("Anthropic-Beta", "computer-use-2025-01-24")
	require.False(t, s.AlignmentMayRejectBeforeDispatch(c, nil, c.Request.Header, &group, nil))
	c.Request.Header.Set("Anthropic-Beta", "future-beta")
	require.True(t, s.AlignmentMayRejectBeforeDispatch(c, nil, c.Request.Header, &group, nil))
	groups.groups[group].Platform = PlatformGemini
	require.False(t, s.AlignmentMayRejectBeforeDispatch(c, nil, c.Request.Header, &group, nil), "a Gemini pool requires mixed scheduling")
	account.Extra["mixed_scheduling"] = true
	require.True(t, s.AlignmentMayRejectBeforeDispatch(c, nil, c.Request.Header, &group, nil))
	require.NoError(t, s.settingService.SetBetaPolicySettings(t.Context(), &BetaPolicySettings{Rules: []BetaPolicyRule{
		{BetaToken: "future-beta", Scope: BetaPolicyScopeOAuth, Action: BetaPolicyActionPass},
	}}))
	require.False(t, s.AlignmentMayRejectBeforeDispatch(c, nil, c.Request.Header, &group, nil), "newly recognized token permits queue pings")
}

func forwardUnknownBeta(t *testing.T, s *GatewayService, c *gin.Context, account *Account) error {
	t.Helper()
	body := []byte(`{"model":"claude-opus-4-6","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`)
	parsed, err := ParseGatewayRequest(NewRequestBodyRef(body), PlatformAnthropic)
	require.NoError(t, err)
	if strings.HasSuffix(c.Request.URL.Path, "/count_tokens") {
		return s.ForwardCountTokens(c.Request.Context(), c, account, parsed)
	}
	if account.Platform == PlatformAntigravity {
		ag := AntigravityGatewayService{settingService: s.settingService, httpUpstream: s.httpUpstream}
		_, err = ag.Forward(c.Request.Context(), c, account, body, false)
	} else {
		_, err = s.Forward(c.Request.Context(), c, account, parsed)
	}
	return err
}

func TestUnknownBeta_RejectNoUpstream(t *testing.T) {
	for _, path := range []string{"/v1/messages", "/v1/messages/count_tokens", "/antigravity/v1/messages", "/antigravity/v1/messages/count_tokens"} {
		oauth := cacheControlLimitMimicAccount()
		oauth.Type = AccountTypeOAuth
		for _, account := range []*Account{fallbackNativeAccountForTest(false), fallbackNativeAccountForTest(true), cacheControlLimitMimicAccount(), oauth} {
			t.Run(path+"/"+account.Type, func(t *testing.T) {
				up := &fallbackSequenceUpstream{responses: []*http.Response{cacheControlLimitResponse(http.StatusOK, cacheControlLimitOKMessage)}}
				s := fallbackNativeGatewayForTest(t, up, nil)
				if strings.HasPrefix(path, "/antigravity/") {
					account.Platform = PlatformAntigravity
					// A model mapping failure would expose validation running too late.
					account.Credentials["model_mapping"] = map[string]any{"other-model": "other-model"}
				}
				if account.Extra == nil {
					account.Extra = map[string]any{}
				}
				account.Extra[AccountClaudeCustomizationExtraKey] = map[string]any{ClaudeFieldUnknownBetaHeader: ClaudeAlignReject}
				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				c.Request = httptest.NewRequest(http.MethodPost, path, nil)
				c.Request.Header.Add("Anthropic-Beta", "interleaved-thinking-2025-05-14, aa`a<>& ,")
				c.Request.Header.Add("Anthropic-Beta", "aa`a<>&, INTERLEAVED-THINKING-2025-05-14,BBB-BAD-2,")
				err := forwardUnknownBeta(t, s, c, account)
				require.ErrorIs(t, err, ErrLocalAnthropicRejected)
				require.Empty(t, up.bodies)
				require.Equal(t, http.StatusBadRequest, rec.Code)
				id := rec.Header().Get("request-id")
				require.Regexp(t, "^req_011C[1-9A-HJ-NP-Za-km-z]{20}$", id)
				const message = "Unexpected value(s) `aa`a<>&`, `INTERLEAVED-THINKING-2025-05-14`, `BBB-BAD-2` for the `anthropic-beta` header. Please consult our documentation at platform.claude.com/docs or try again without the header."
				require.Equal(t, `{"type":"error","error":{"type":"invalid_request_error","message":"`+message+`"},"request_id":"`+id+`"}`, rec.Body.String())
			})
		}
	}
}

func TestUnknownBeta_DefaultPassthrough(t *testing.T) {
	for _, tc := range []struct {
		name, path, mode, override string
		dispatched, rule           bool
	}{
		{name: "default", path: "/v1/messages"},
		{name: "account permits", path: "/v1/messages", mode: "reject", override: "passthrough"},
		{name: "previous dispatch", path: "/v1/messages", mode: "reject", dispatched: true},
		{name: "OpenAI", path: "/v1/chat/completions", mode: "reject"},
		{name: "configured token", path: "/v1/messages", mode: "reject", rule: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			up := &fallbackSequenceUpstream{responses: []*http.Response{cacheControlLimitResponse(http.StatusOK, cacheControlLimitOKMessage)}}
			policy := DefaultClaudeCustomizationSettings()
			if tc.mode != "" {
				policy.UnknownBetaHeader = tc.mode
			}
			s := fallbackNativeGatewayForTestWithPolicy(t, up, nil, policy)
			account := fallbackNativeAccountForTest(false)
			if tc.override != "" {
				account.Extra[AccountClaudeCustomizationExtraKey] = map[string]any{ClaudeFieldUnknownBetaHeader: tc.override}
			}
			if tc.rule {
				require.NoError(t, s.settingService.SetBetaPolicySettings(t.Context(), &BetaPolicySettings{Rules: []BetaPolicyRule{{BetaToken: "new-beta", Action: BetaPolicyActionPass, Scope: BetaPolicyScopeOAuth}}}))
			}
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, tc.path, nil)
			c.Request.Header.Set("Anthropic-Beta", "new-beta")
			if tc.dispatched {
				clock := InstallOpsUpstreamAttemptClock(c)
				MarkOpsUpstreamAttemptStart(c.Request.Context(), 19)
				require.Positive(t, clock.claim(19, 0))
			}
			require.NoError(t, forwardUnknownBeta(t, s, c, account))
			require.Equal(t, http.StatusOK, rec.Code)
			require.Len(t, up.bodies, 1)
		})
	}
}

type delayedAlignmentOverrides struct {
	queueAlignmentSettings
	release     chan struct{}
	panicOnRead bool
}

func (r *delayedAlignmentOverrides) ListClaudeAlignmentOverrides(ctx context.Context, types []string) ([]ClaudeAlignmentOverrides, error) {
	if r.panicOnRead {
		r.reads++
		panic("override refresh panic")
	}
	if r.release != nil {
		select {
		case <-r.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return r.queueAlignmentSettings.ListClaudeAlignmentOverrides(ctx, types)
}

func TestAlignmentSnapshot_ServesStaleDuringOneDetachedRefresh(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		repo := &delayedAlignmentOverrides{}
		s := NewSettingService(repo, nil)
		stale := s.alignmentSnapshot(t.Context())
		time.Sleep(11 * time.Second)
		repo.release = make(chan struct{})
		ctx, cancel := context.WithCancel(t.Context())
		results := make(chan *alignmentRejectSnapshot, 16)
		go func() { results <- s.alignmentSnapshot(ctx) }()
		synctest.Wait()
		if len(results) == 0 {
			cancel()
			close(repo.release)
			t.Fatal("the stale caller waited for the database read")
		}
		for range cap(results) - 1 {
			go func() { results <- s.alignmentSnapshot(ctx) }()
		}
		synctest.Wait()
		served := len(results)
		cancel()
		close(repo.release)
		synctest.Wait()
		require.Equal(t, cap(results), served, "all callers must return before the database read completes")
		for range cap(results) {
			require.Same(t, stale, <-results)
		}
		require.NotSame(t, stale, s.alignmentRejectCache.Load(), "publish despite request cancellation")
		require.Equal(t, 2, repo.reads, "one cold load and one refresh")
	})
}

func TestAlignmentSnapshot_CancelledColdCallerPublishes(t *testing.T) {
	s := NewSettingService(&queueAlignmentSettings{}, nil)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	require.Same(t, s.alignmentSnapshot(ctx), s.alignmentRejectCache.Load())
}

func TestAlignmentSnapshot_ColdTimeoutSharesDefault(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		repo := &delayedAlignmentOverrides{release: make(chan struct{})}
		repo.value = `{"unknown_beta_header":"reject"}`
		s := NewSettingService(repo, nil)
		fallback := s.alignmentSnapshot(t.Context())
		require.Equal(t, DefaultClaudeCustomizationSettings(), fallback.global)
		require.Empty(t, fallback.reject)
		require.Equal(t, time.Second, time.Until(fallback.expiresAt))
		start := time.Now()
		require.Same(t, fallback, s.alignmentSnapshot(t.Context()))
		require.Zero(t, time.Since(start), "the next request must not wait for another timeout")
		close(repo.release)
		time.Sleep(time.Second)
		s.alignmentSnapshot(t.Context())
		synctest.Wait()
		require.True(t, s.AnyAlignmentReject(t.Context(), ClaudeFieldUnknownBetaHeader))
	})
}

func TestAlignmentSnapshot_RecoversBackgroundRefreshPanic(t *testing.T) {
	var logs strings.Builder
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	synctest.Test(t, func(t *testing.T) {
		repo := &delayedAlignmentOverrides{}
		s := NewSettingService(repo, nil)
		repo.value = `{"thinking_cache_control":"reject"}`
		stale := s.alignmentSnapshot(t.Context())
		time.Sleep(11 * time.Second)
		repo.panicOnRead = true
		require.Same(t, stale, s.alignmentSnapshot(t.Context()))
		synctest.Wait()
		for range 9 {
			time.Sleep(100 * time.Millisecond)
			require.True(t, s.AnyAlignmentReject(t.Context(), ClaudeFieldThinkingCacheControl))
			synctest.Wait()
		}
		require.Equal(t, 2, repo.reads, "one cold read and one panicking refresh")
		require.Equal(t, 1, strings.Count(logs.String(), "level=ERROR"))
		require.Contains(t, logs.String(), "override refresh panic")
		require.Contains(t, logs.String(), "(*delayedAlignmentOverrides).ListClaudeAlignmentOverrides(")
		require.Equal(t, 100*time.Millisecond, time.Until(s.alignmentRejectCache.Load().expiresAt))
		require.True(t, stale.expiresAt.Before(time.Now()), "readers keep their immutable snapshot")
		time.Sleep(100 * time.Millisecond)
		s.alignmentSnapshot(t.Context())
		synctest.Wait()
		require.Equal(t, 3, repo.reads, "retry after one second")
		require.Equal(t, 1, strings.Count(logs.String(), "level=ERROR"), "the next panic is rate limited")
		time.Sleep(time.Minute)
		s.alignmentSnapshot(t.Context())
		synctest.Wait()
		require.Equal(t, 2, strings.Count(logs.String(), "level=ERROR"))
		repo.panicOnRead = false
		time.Sleep(time.Second)
		s.alignmentSnapshot(t.Context())
		synctest.Wait()
		require.Equal(t, 10*time.Second, time.Until(s.alignmentRejectCache.Load().expiresAt))
	})
}

func TestAlignmentSnapshot_ColdAuthRefreshPanicPublishesDefaults(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := NewSettingService(&delayedAlignmentOverrides{panicOnRead: true}, nil)
		require.False(t, s.globalAuthRequestIDFill(t.Context()))
		synctest.Wait()
		snapshot := s.alignmentSnapshot(t.Context())
		require.Equal(t, DefaultClaudeCustomizationSettings(), snapshot.global)
		require.Equal(t, time.Second, time.Until(snapshot.expiresAt))
	})
}

type betaReadSettings struct {
	queueAlignmentSettings
	betaReads       int
	betaErr         error
	releaseBetaRead chan struct{}
}

func (r *betaReadSettings) GetValue(ctx context.Context, key string) (string, error) {
	if key == SettingKeyBetaPolicySettings {
		r.betaReads++
		value := r.setVal
		if r.releaseBetaRead != nil {
			select {
			case <-r.releaseBetaRead:
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}
		return value, r.betaErr
	}
	return r.queueAlignmentSettings.GetValue(ctx, key)
}

func TestAlignmentSnapshot_DefaultSaveDuringRefreshToReject(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		repo := &betaReadSettings{}
		s := NewSettingService(repo, nil)
		stale := s.alignmentSnapshot(t.Context())
		repo.releaseBetaRead = make(chan struct{})
		repo.accounts = []*Account{{ID: 1, Type: AccountTypeOAuth, Platform: PlatformAnthropic,
			Extra: map[string]any{AccountClaudeCustomizationExtraKey: map[string]any{ClaudeFieldUnknownBetaHeader: ClaudeAlignReject}}}}
		time.Sleep(11 * time.Second)
		require.Same(t, stale, s.alignmentSnapshot(t.Context()))
		synctest.Wait()
		require.Equal(t, 2, repo.betaReads, "cold load and refresh have read the old rules")
		// The refresh has already copied the old rules. Release its read
		// concurrently with the save, without advancing the synthetic clock.
		go func() { close(repo.releaseBetaRead) }()
		rules := []BetaPolicyRule{{BetaToken: "future-beta", Action: BetaPolicyActionPass, Scope: BetaPolicyScopeAll}}
		require.NoError(t, s.SetBetaPolicySettings(t.Context(), &BetaPolicySettings{Rules: rules}))
		synctest.Wait()
		require.Equal(t, rules, s.alignmentSnapshot(t.Context()).betaRules)
	})
}

func TestUnknownBeta_DefaultSharesRuleReadsAndInvalidatesOnSave(t *testing.T) {
	repo := &betaReadSettings{}
	s := NewSettingService(repo, nil)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	c.Request.Header.Set("Anthropic-Beta", "unknown")
	snapshot := s.alignmentSnapshot(t.Context())
	require.Equal(t, 1, repo.betaReads)
	require.NoError(t, s.checkUnknownBeta(t.Context(), c, fallbackNativeAccountForTest(false), snapshot.global))
	require.NoError(t, s.SetBetaPolicySettings(t.Context(), &BetaPolicySettings{}))
	require.NotSame(t, snapshot, s.alignmentSnapshot(t.Context()))
	require.Equal(t, 2, repo.betaReads)
}

func TestUnknownBeta_RuleReadFailureWarnsOncePerInterval(t *testing.T) {
	var logs strings.Builder
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	synctest.Test(t, func(t *testing.T) {
		s := NewSettingService(&betaReadSettings{betaErr: errors.New("beta repository unavailable")}, nil)
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		c.Request.Header.Set("Anthropic-Beta", "future")
		policy := DefaultClaudeCustomizationSettings()
		policy.UnknownBetaHeader = ClaudeAlignReject
		for want := 1; want <= 2; want++ {
			for range 3 {
				require.NoError(t, s.checkUnknownBeta(t.Context(), c, fallbackNativeAccountForTest(false), policy))
				synctest.Wait()
			}
			require.Equal(t, want, strings.Count(logs.String(), "level=WARN"))
			require.Contains(t, logs.String(), "beta repository unavailable")
			require.Contains(t, logs.String(), "unknown beta rule lookup failed; allowing request")
			require.Contains(t, logs.String(), "source=alignment_snapshot")
			time.Sleep(time.Minute)
		}
	})
}

func TestQueuePing_UnknownBetaDeduplicatesCandidateQueries(t *testing.T) {
	for _, tc := range []struct {
		name             string
		fallback         int64
		fallbackPlatform string
		mode             string
		wantCalls        int
	}{
		{"same group", 1, PlatformAnthropic, "", 2},
		{"different groups", 2, PlatformAnthropic, "", 4},
		{"simple ignores group", 2, PlatformAnthropic, config.RunModeSimple, 2},
		{"simple keeps direct and mixed distinct", 2, PlatformAntigravity, config.RunModeSimple, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			primary := int64(1)
			settings := &queueAlignmentSettings{accounts: []*Account{{ID: 8, Type: AccountTypeOAuth,
				Extra: map[string]any{AccountClaudeCustomizationExtraKey: map[string]any{ClaudeFieldUnknownBetaHeader: ClaudeAlignReject}},
			}}}
			accounts := &queueAlignmentAccounts{}
			groups := &mockGroupRepoForGateway{groups: map[int64]*Group{
				primary: {ID: primary, Platform: PlatformAnthropic}, tc.fallback: {ID: tc.fallback, Platform: tc.fallbackPlatform},
			}}
			s := &GatewayService{cfg: &config.Config{RunMode: tc.mode}, groupRepo: groups, accountRepo: accounts, settingService: NewSettingService(settings, nil)}
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			c.Request.Header.Set("Anthropic-Beta", "future")
			require.False(t, s.AlignmentMayRejectBeforeDispatch(c, nil, c.Request.Header, &primary, &tc.fallback))
			require.Equal(t, tc.wantCalls, accounts.calls)
		})
	}
}
