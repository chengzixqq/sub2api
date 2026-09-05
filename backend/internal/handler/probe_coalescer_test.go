package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func testProbeCandidate() ProbeCandidate {
	return ProbeCandidate{
		Protocol:     probeProtocolChat,
		Model:        "probe-model",
		Prompt:       "Calculate and respond with ONLY the number, nothing else.",
		Expected:     "8",
		InputTokens:  12,
		OutputTokens: 1,
		Key:          "group-1|openai|openai_chat|probe-model",
	}
}

func TestParseStrictProbeBodyMatchesMonitorTemplate(t *testing.T) {
	body := []byte(`{"model":"probe-model","messages":[{"role":"user","content":"Calculate and respond with ONLY the number, nothing else.\n\nQ: 3 + 5 = ?\nA: 8\n\nQ: 12 - 7 = ?\nA: 5\n\nQ: 19 - 7 = ?\nA:"}],"max_tokens":50,"stream":false}`)
	candidate, ok := parseStrictProbeBody("/v1/chat/completions", body)
	require.True(t, ok)
	require.Equal(t, probeProtocolChat, candidate.Protocol)
	require.Equal(t, "12", candidate.Expected)
	require.Equal(t, service.EstimateFailurePromptTokens(service.PlatformOpenAI, []byte(candidate.Prompt)), candidate.InputTokens)
	require.Equal(t, service.EstimateFailurePromptTokens(service.PlatformOpenAI, []byte(candidate.Expected)), candidate.OutputTokens)

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(body, &decoded))
	decoded["temperature"] = 0
	withExtra, err := json.Marshal(decoded)
	require.NoError(t, err)
	_, ok = parseStrictProbeBody("/v1/chat/completions", withExtra)
	require.False(t, ok, "custom probe bodies must bypass coalescing")

	// The native OpenAI monitor adapter omits stream for its non-streaming
	// request; this is still a canonical probe and must be coalesced.
	withoutStream := []byte(`{"model":"probe-model","messages":[{"role":"user","content":"Calculate and respond with ONLY the number, nothing else.\n\nQ: 3 + 5 = ?\nA: 8\n\nQ: 12 - 7 = ?\nA: 5\n\nQ: 19 - 7 = ?\nA:"}],"max_tokens":50}`)
	_, ok = parseStrictProbeBody("/v1/chat/completions", withoutStream)
	require.True(t, ok, "native OpenAI monitor body omits stream")

	// The built-in low-token template overrides max_tokens to 20.
	lowTokenChat := []byte(`{"model":"probe-model","messages":[{"role":"user","content":"Calculate and respond with ONLY the number, nothing else.\n\nQ: 3 + 5 = ?\nA: 8\n\nQ: 12 - 7 = ?\nA: 5\n\nQ: 19 - 7 = ?\nA:"}],"max_tokens":20,"stream":false}`)
	_, ok = parseStrictProbeBody("/v1/chat/completions", lowTokenChat)
	require.True(t, ok, "low-token chat monitor template must be recognized")

	anthropicWithoutStream := []byte(`{"model":"claude-test","messages":[{"role":"user","content":"Calculate and respond with ONLY the number, nothing else.\n\nQ: 3 + 5 = ?\nA: 8\n\nQ: 12 - 7 = ?\nA: 5\n\nQ: 19 - 7 = ?\nA:"}],"max_tokens":50}`)
	_, ok = parseStrictProbeBody("/v1/messages", anthropicWithoutStream)
	require.True(t, ok, "native Anthropic monitor body omits stream")

	// The seeded Claude Code template adds only its fixed system marker and
	// metadata.user_id; it is still the same arithmetic probe and should be
	// coalesced without allowing arbitrary extra request fields.
	claudeCodeProbe := []byte(`{"model":"claude-test","messages":[{"role":"user","content":"Calculate and respond with ONLY the number, nothing else.\n\nQ: 3 + 5 = ?\nA: 8\n\nQ: 12 - 7 = ?\nA: 5\n\nQ: 19 - 7 = ?\nA:"}],"max_tokens":50,"system":[{"type":"text","text":"You are Claude Code, Anthropic's official CLI for Claude."}],"metadata":{"user_id":"user_0000000000000000000000000000000000000000000000000000000000000000_account_00000000-0000-0000-0000-000000000000_session_00000000-0000-0000-0000-000000000000"}}`)
	_, ok = parseStrictProbeBody("/v1/messages", claudeCodeProbe)
	require.True(t, ok, "the seeded Claude Code monitor template must be recognized")

	claudeCodeWithUnknownField := []byte(`{"model":"claude-test","messages":[{"role":"user","content":"Calculate and respond with ONLY the number, nothing else.\n\nQ: 3 + 5 = ?\nA: 8\n\nQ: 12 - 7 = ?\nA: 5\n\nQ: 19 - 7 = ?\nA:"}],"max_tokens":50,"system":[{"type":"text","text":"You are Claude Code, Anthropic's official CLI for Claude."}],"metadata":{"user_id":"user_0000000000000000000000000000000000000000000000000000000000000000_account_00000000-0000-0000-0000-000000000000_session_00000000-0000-0000-0000-000000000000"},"temperature":0}`)
	_, ok = parseStrictProbeBody("/v1/messages", claudeCodeWithUnknownField)
	require.False(t, ok, "arbitrary Anthropic fields must still bypass coalescing")
}

func TestParseStrictProbeBodyAcceptsLowTokenResponsesTemplate(t *testing.T) {
	body := []byte(`{"model":"probe-model","instructions":"You are a channel health-check endpoint. Answer the arithmetic challenge exactly and briefly.","input":"Calculate and respond with ONLY the number, nothing else.\n\nQ: 3 + 5 = ?\nA: 8\n\nQ: 12 - 7 = ?\nA: 5\n\nQ: 19 - 7 = ?\nA:","max_output_tokens":20,"stream":false}`)
	candidate, ok := parseStrictProbeBody("/v1/responses", body)
	require.True(t, ok, "low-token Responses monitor template must be recognized")
	require.Equal(t, probeProtocolResponse, candidate.Protocol)
}

func TestParseStrictProbeBodyMatchesGeminiMonitorTemplate(t *testing.T) {
	body := []byte(`{"contents":[{"parts":[{"text":"Calculate and respond with ONLY the number, nothing else.\n\nQ: 3 + 5 = ?\nA: 8\n\nQ: 12 - 7 = ?\nA: 5\n\nQ: 19 - 7 = ?\nA:"}]}],"generationConfig":{"maxOutputTokens":50}}`)
	candidate, ok := parseStrictProbeBody("/v1beta/models/gemini-3.7-flash:generateContent", body)
	require.True(t, ok)
	require.Equal(t, probeProtocolGemini, candidate.Protocol)
	require.Equal(t, "gemini-3.7-flash", candidate.Model)
	require.Equal(t, "12", candidate.Expected)
	require.Equal(t, estimateProbeTextTokens(probeProtocolGemini, service.PlatformGemini, candidate.Prompt), candidate.InputTokens)
	require.Equal(t, estimateProbeTextTokens(probeProtocolGemini, service.PlatformGemini, candidate.Expected), candidate.OutputTokens)

	streamBody := []byte(`{"contents":[{"parts":[{"text":"Calculate and respond with ONLY the number, nothing else.\n\nQ: 3 + 5 = ?\nA: 8\n\nQ: 12 - 7 = ?\nA: 5\n\nQ: 19 - 7 = ?\nA:"}]}],"generationConfig":{"maxOutputTokens":50}}`)
	_, ok = parseStrictProbeBody("/v1beta/models/gemini-3.7-flash:streamGenerateContent", streamBody)
	require.False(t, ok)
}

func TestGeminiSyntheticBodyAndValidation(t *testing.T) {
	candidate := testProbeCandidate()
	candidate.Protocol = probeProtocolGemini
	candidate.Model = "gemini-3.7-flash"
	candidate.Expected = "12"
	body, contentType := candidate.syntheticBody()
	require.Equal(t, "application/json", contentType)
	require.True(t, validateProbeResponse(candidate, http.StatusOK, body))
	require.False(t, validateProbeResponse(candidate, http.StatusOK, []byte(`{"candidates":[{"content":{"parts":[{"text":"112"}]}}]}`)))
}

func TestProbeCandidateForRequestSupportsGeminiURLModel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-3.7-flash:generateContent", nil)
	body := []byte(`{"contents":[{"parts":[{"text":"Calculate and respond with ONLY the number, nothing else.\n\nQ: 3 + 5 = ?\nA: 8\n\nQ: 12 - 7 = ?\nA: 5\n\nQ: 19 - 7 = ?\nA:"}]}],"generationConfig":{"maxOutputTokens":50}}`)
	candidate, ok := probeCandidateForRequest(c, body, "gemini-3.7-flash", 27, service.PlatformGemini, 91)
	require.True(t, ok)
	require.Equal(t, probeProtocolGemini, candidate.Protocol)
	require.Contains(t, candidate.Key, "gemini_generate_content")
}

func TestProbeCandidateForRequestRecordsModelMismatch(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	body := []byte(`{"model":"probe-model","messages":[{"role":"user","content":"Calculate and respond with ONLY the number, nothing else.\n\nQ: 3 + 5 = ?\nA: 8\n\nQ: 12 - 7 = ?\nA: 5\n\nQ: 19 - 7 = ?\nA:"}],"max_tokens":50,"stream":false}`)
	before := ProbeParseRejectSnapshot()[probeRejectModelMismatch]
	_, ok := probeCandidateForRequest(c, body, "mapped-model", 27, service.PlatformOpenAI, 91)
	require.False(t, ok)
	require.Equal(t, before+1, ProbeParseRejectSnapshot()[probeRejectModelMismatch])
}

func TestProbeParseRejectDiagnosticsAreBoundedAndSampled(t *testing.T) {
	before := ProbeParseRejectSnapshot()[probeRejectBodyShape]
	body := []byte(`{"model":"probe-model","messages":[{"role":"user","content":"Calculate and respond with ONLY the number, nothing else.\n\nQ: 3 + 5 = ?\nA: 8\n\nQ: 12 - 7 = ?\nA: 5\n\nQ: 19 - 7 = ?\nA:"}],"max_tokens":50,"stream":false,"temperature":0}`)
	for i := 0; i < 3; i++ {
		_, ok := parseStrictProbeBody("/v1/chat/completions", body)
		require.False(t, ok)
	}
	after := ProbeParseRejectSnapshot()[probeRejectBodyShape]
	require.Equal(t, before+3, after)
}

func TestParseStrictProbeBodySkipsOrdinaryBodiesBeforeJSONParse(t *testing.T) {
	// Ordinary requests should not enter the strict parser or diagnostics path
	// merely because they use a gateway endpoint.
	body := []byte(`{"model":"gpt-5.5","messages":[{"role":"user","content":"hello"}],"max_tokens":128}`)
	_, ok := parseStrictProbeBody("/v1/chat/completions", body)
	require.False(t, ok)
}

func TestProbeCoalescerSingleLeaderAndIndependentFollowerResolution(t *testing.T) {
	p := NewProbeCoalescer(ProbeCoalescerConfig{
		Mode:          ProbeCoalescingActive,
		Window:        time.Minute,
		LeaderTimeout: time.Second,
		AttemptBudget: 8,
	})
	candidate := testProbeCandidate()
	leader := p.Begin(context.Background(), candidate, "leader")
	require.True(t, leader.IsLeader())

	followers := make([]*ProbeLease, 49)
	for i := range followers {
		followers[i] = p.Begin(context.Background(), candidate, "follower")
		require.True(t, followers[i].IsFollower())
	}

	account := &service.Account{ID: 42}
	leader.MarkBillingReady(nil)
	leader.Finish(true, "leader", account)
	for _, follower := range followers {
		resolution, err := follower.Resolve(context.Background())
		require.NoError(t, err)
		require.True(t, resolution.Synthetic)
		require.Equal(t, "leader", resolution.LeaderRequestID)
		// The coalescer keeps an immutable billing/audit snapshot rather than
		// sharing the scheduler's mutable account pointer with followers.
		require.Equal(t, account, resolution.LeaderAccount)
		require.NotSame(t, account, resolution.LeaderAccount)
	}
}

func TestProbeCoalescerRequiresBillingBarrierBeforePublishingHealth(t *testing.T) {
	p := NewProbeCoalescer(ProbeCoalescerConfig{
		Mode:          ProbeCoalescingActive,
		Window:        time.Minute,
		LeaderTimeout: time.Second,
		AttemptBudget: 2,
	})
	leader := p.Begin(context.Background(), testProbeCandidate(), "leader")
	follower := p.Begin(context.Background(), testProbeCandidate(), "follower")
	leader.Finish(true, "leader", &service.Account{ID: 1})

	resolution, err := follower.Resolve(context.Background())
	require.NoError(t, err)
	require.True(t, resolution.Leader, "a leader without persisted billing must not publish health")
	require.False(t, resolution.Synthetic)
}

func TestProbeCoalescerTimeoutCancelsExpiredLeaderContext(t *testing.T) {
	p := NewProbeCoalescer(ProbeCoalescerConfig{
		Mode:          ProbeCoalescingActive,
		Window:        time.Minute,
		LeaderTimeout: 20 * time.Millisecond,
		AttemptBudget: 2,
	})
	leader := p.Begin(context.Background(), testProbeCandidate(), "leader")
	follower := p.Begin(context.Background(), testProbeCandidate(), "follower")
	leaderCtx := leader.LeaderContext()

	resolutionCh := make(chan ProbeResolution, 1)
	errCh := make(chan error, 1)
	go func() {
		resolution, err := follower.Resolve(context.Background())
		resolutionCh <- resolution
		errCh <- err
	}()

	select {
	case <-leaderCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("expired leader context was not canceled")
	}
	select {
	case resolution := <-resolutionCh:
		require.True(t, resolution.Leader)
		require.NoError(t, <-errCh)
	case <-time.After(time.Second):
		t.Fatal("follower was not promoted after leader timeout")
	}
}

func TestProbeCoalescerLeaderExpiryCancelsWithoutFollower(t *testing.T) {
	p := NewProbeCoalescer(ProbeCoalescerConfig{
		Mode:          ProbeCoalescingActive,
		Window:        time.Minute,
		LeaderTimeout: 15 * time.Millisecond,
		AttemptBudget: 2,
	})
	leader := p.Begin(context.Background(), testProbeCandidate(), "leader")
	select {
	case <-leader.LeaderContext().Done():
	case <-time.After(time.Second):
		t.Fatal("leader expiry did not cancel the upstream context")
	}
}

func TestRunProbeLeaderUsageTaskIsSynchronousAndPreservesValues(t *testing.T) {
	p := NewProbeCoalescer(ProbeCoalescerConfig{
		Mode:          ProbeCoalescingActive,
		Window:        time.Minute,
		LeaderTimeout: time.Second,
		AttemptBudget: 2,
	})
	lease := p.Begin(context.Background(), testProbeCandidate(), "leader")
	parent := context.WithValue(context.Background(), ctxkey.ProbeRequestID, "probe:leader")
	canceled, cancel := context.WithCancel(parent)
	cancel()
	called := false
	err := runProbeLeaderUsageTask(canceled, lease, func(ctx context.Context) error {
		called = true
		require.Equal(t, "probe:leader", ctx.Value(ctxkey.ProbeRequestID))
		persistenceRequired, ok := ctx.Value(ctxkey.ProbeUsagePersistenceRequired).(bool)
		require.True(t, ok)
		require.True(t, persistenceRequired)
		require.NoError(t, ctx.Err(), "billing must use a detached bounded context")
		return nil
	}, true)
	require.NoError(t, err)
	require.True(t, called)
	follower := p.Begin(context.Background(), testProbeCandidate(), "follower")
	lease.Finish(true, "leader", &service.Account{ID: 1})
	resolution, err := follower.Resolve(context.Background())
	require.NoError(t, err)
	require.True(t, resolution.Synthetic)
}

func TestProbeCoalescerFollowerPromoteReplacesCompletedEntry(t *testing.T) {
	p := NewProbeCoalescer(ProbeCoalescerConfig{
		Mode:          ProbeCoalescingActive,
		Window:        time.Minute,
		LeaderTimeout: time.Second,
		AttemptBudget: 3,
	})
	candidate := testProbeCandidate()
	leader := p.Begin(context.Background(), candidate, "leader")
	followerA := p.Begin(context.Background(), candidate, "follower-a")
	followerB := p.Begin(context.Background(), candidate, "follower-b")
	leader.MarkBillingReady(nil)
	leader.Finish(true, "leader", &service.Account{ID: 7})

	res, err := followerA.Resolve(context.Background())
	require.NoError(t, err)
	require.True(t, res.Synthetic)
	// Simulate a follower billing failure. It must become the only next leader.
	require.True(t, followerA.Promote())
	require.True(t, followerA.IsLeader())

	type resolutionResult struct {
		res ProbeResolution
		err error
	}
	resultCh := make(chan resolutionResult, 1)
	go func() {
		res, err := followerB.Resolve(context.Background())
		resultCh <- resolutionResult{res: res, err: err}
	}()
	time.Sleep(10 * time.Millisecond)
	followerA.MarkBillingReady(nil)
	followerA.Finish(true, "leader-2", &service.Account{ID: 8})
	result := <-resultCh
	require.NoError(t, result.err)
	require.True(t, result.res.Synthetic)
	require.Equal(t, "leader-2", result.res.LeaderRequestID)
}

func TestProbeCoalescerConcurrentWaitersDoNotCreateParallelLeadersAfterFailure(t *testing.T) {
	p := NewProbeCoalescer(ProbeCoalescerConfig{
		Mode:          ProbeCoalescingActive,
		Window:        time.Minute,
		LeaderTimeout: time.Second,
		AttemptBudget: 4,
	})
	candidate := testProbeCandidate()
	leader := p.Begin(context.Background(), candidate, "leader")
	const waiterCount = 50
	leases := make([]*ProbeLease, waiterCount)
	for i := range leases {
		leases[i] = p.Begin(context.Background(), candidate, "waiter")
	}
	leader.Finish(false, "leader", nil)

	var mu sync.Mutex
	leaders := make([]*ProbeLease, 0, 1)
	var wg sync.WaitGroup
	for _, lease := range leases {
		wg.Add(1)
		go func(lease *ProbeLease) {
			defer wg.Done()
			resolution, err := lease.Resolve(context.Background())
			if err != nil {
				return
			}
			if resolution.Leader {
				mu.Lock()
				leaders = append(leaders, resolution.Lease)
				mu.Unlock()
			}
		}(lease)
	}
	// Resolve promotes one waiter and leaves the rest following that entry.
	// Finish it once it is observable so no test goroutine can hang.
	deadline := time.Now().Add(time.Second)
	for {
		mu.Lock()
		count := len(leaders)
		mu.Unlock()
		if count > 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	mu.Lock()
	if len(leaders) == 1 {
		leaders[0].MarkBillingReady(nil)
		leaders[0].Finish(true, "leader-2", &service.Account{ID: 9})
	}
	mu.Unlock()
	wg.Wait()
	mu.Lock()
	require.LessOrEqual(t, len(leaders), 1)
	mu.Unlock()
}

func TestProbeCoalescerStopsAfterAttemptBudget(t *testing.T) {
	p := NewProbeCoalescer(ProbeCoalescerConfig{
		Mode:          ProbeCoalescingActive,
		Window:        time.Minute,
		LeaderTimeout: time.Second,
		AttemptBudget: 1,
	})
	candidate := testProbeCandidate()
	leader := p.Begin(context.Background(), candidate, "leader")
	leader.Finish(false, "leader", nil)
	follower := p.Begin(context.Background(), candidate, "follower")
	require.True(t, follower.IsExhausted())

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	handled, promoted, err := resolveProbeFollower(c, follower, nil)
	require.False(t, promoted)
	require.True(t, handled)
	require.ErrorIs(t, err, ErrProbeAttemptBudgetExhausted)
}

func TestProbeCoalescerCountsSequentialFailuresAgainstBudget(t *testing.T) {
	p := NewProbeCoalescer(ProbeCoalescerConfig{
		Mode:          ProbeCoalescingActive,
		Window:        time.Minute,
		LeaderTimeout: time.Second,
		AttemptBudget: 3,
	})
	candidate := testProbeCandidate()
	for attempt := 1; attempt <= 3; attempt++ {
		lease := p.Begin(context.Background(), candidate, "leader")
		require.True(t, lease.IsLeader(), "attempt %d should be a leader", attempt)
		lease.Finish(false, "leader", nil)
	}
	exhausted := p.Begin(context.Background(), candidate, "after-budget")
	require.True(t, exhausted.IsExhausted())
}

func TestProbeCoalescerCanceledFollowerDoesNotPromoteOrBypass(t *testing.T) {
	p := NewProbeCoalescer(ProbeCoalescerConfig{
		Mode:          ProbeCoalescingActive,
		Window:        time.Minute,
		LeaderTimeout: time.Second,
		AttemptBudget: 2,
	})
	candidate := testProbeCandidate()
	leader := p.Begin(context.Background(), candidate, "leader")
	follower := p.Begin(context.Background(), candidate, "follower")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil).WithContext(ctx)
	handled, promoted, err := resolveProbeFollower(c, follower, nil)
	require.True(t, handled)
	require.False(t, promoted)
	require.NoError(t, err)
	// Keep the leader alive long enough to ensure the canceled follower did
	// not replace it or create another entry.
	require.True(t, leader.IsLeader())
	leader.Finish(false, "leader", nil)
}

func TestProbeCandidateSyntheticIDsAreFresh(t *testing.T) {
	candidate := testProbeCandidate()
	first, contentType := candidate.syntheticBody()
	second, _ := candidate.syntheticBody()
	require.Equal(t, "application/json", contentType)
	require.NotEqual(t, string(first), string(second))
	require.True(t, validateProbeResponse(candidate, 200, first))
}

func TestValidateProbeResponseRequiresAnExactNumberToken(t *testing.T) {
	candidate := testProbeCandidate()
	require.True(t, validateProbeResponse(candidate, 200, []byte(`{"choices":[{"message":{"content":"8"}}]}`)))
	require.False(t, validateProbeResponse(candidate, 200, []byte(`{"choices":[{"message":{"content":"18"}}]}`)))
}

func TestValidateProbeResponseAcceptsResponsesOutputArrayText(t *testing.T) {
	candidate := testProbeCandidate()
	candidate.Protocol = probeProtocolResponse
	candidate.Expected = "8"
	body := []byte(`{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"8"}]}]}`)
	require.True(t, validateProbeResponse(candidate, 200, body))
}

func TestRequestIDForProbeUsesStableServerNamespace(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), ctxkey.RequestID, "server-request-1"))
	c.Request.Header.Set("X-Client-Request-ID", "caller-reused-id")

	first := requestIDForProbe(c)
	second := requestIDForProbe(c)
	require.Equal(t, "probe:server-request-1", first)
	require.Equal(t, first, second)
	require.Equal(t, first, c.Request.Context().Value(ctxkey.ProbeRequestID))
}

func TestRequestIDForProbeDoesNotTrustClientCorrelationHeader(t *testing.T) {
	gin.SetMode(gin.TestMode)
	makeContext := func() *gin.Context {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		c.Request.Header.Set("X-Request-ID", "client-reused-id")
		return c
	}
	first := requestIDForProbe(makeContext())
	second := requestIDForProbe(makeContext())
	require.NotEqual(t, "probe:client-reused-id", first)
	require.NotEqual(t, first, second)
}

func TestRequestIDForProbeKeepsUsageLogKeyWithinDatabaseLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	longRequestID := strings.Repeat("x", 100)
	c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), ctxkey.RequestID, longRequestID))

	id := requestIDForProbe(c)
	require.LessOrEqual(t, len(id), 64)
	require.True(t, strings.HasPrefix(id, "probe:"))
}

func TestPrepareProbeAdmissionDoesNotMutateShadowRequestContext(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), ctxkey.RequestID, "normal-request"))

	shadow := NewProbeCoalescer(ProbeCoalescerConfig{Mode: ProbeCoalescingShadow})
	ctx, id := prepareProbeAdmission(c, shadow)
	require.Empty(t, id)
	require.Equal(t, "normal-request", ctx.Value(ctxkey.RequestID))
	require.Nil(t, c.Request.Context().Value(ctxkey.ProbeRequestID))

	active := NewProbeCoalescer(ProbeCoalescerConfig{Mode: ProbeCoalescingActive})
	ctx, id = prepareProbeAdmission(c, active)
	require.NotEmpty(t, id)
	require.Equal(t, id, ctx.Value(ctxkey.ProbeRequestID))
}

func TestProbeLeaderContextCarriesProbeRequestID(t *testing.T) {
	p := NewProbeCoalescer(ProbeCoalescerConfig{Mode: ProbeCoalescingActive})
	parent := context.WithValue(context.Background(), ctxkey.ProbeRequestID, "probe:leader-1")
	lease := p.Begin(parent, testProbeCandidate(), "probe:leader-1")
	require.True(t, lease.IsLeader())
	require.Equal(t, "probe:leader-1", lease.LeaderContext().Value(ctxkey.ProbeRequestID))
}

func TestProbeCandidateKeyIncludesResolvedChannel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	body := []byte(`{"model":"probe-model","messages":[{"role":"user","content":"Calculate and respond with ONLY the number, nothing else.\n\nQ: 3 + 5 = ?\nA: 8\n\nQ: 12 - 7 = ?\nA: 5\n\nQ: 19 - 7 = ?\nA:"}],"max_tokens":50,"stream":false}`)

	first, ok := probeCandidateForRequest(c, body, "probe-model", 7, "openai", 101)
	require.True(t, ok)
	second, ok := probeCandidateForRequest(c, body, "probe-model", 7, "openai", 202)
	require.True(t, ok)
	same, ok := probeCandidateForRequest(c, body, "probe-model", 7, "openai", 101)
	require.True(t, ok)
	require.NotEqual(t, first.Key, second.Key)
	require.Equal(t, first.Key, same.Key)
}

func TestProbeChannelUsageFieldsPreserveMappedModelAndAuditChain(t *testing.T) {
	fields := probeChannelUsageFields(service.ChannelMappingResult{
		ChannelID:          17,
		Mapped:             true,
		MappedModel:        "mapped-probe-model",
		BillingModelSource: service.BillingModelSourceChannelMapped,
	}, ProbeCandidate{Model: "requested-probe-model"})
	require.Equal(t, int64(17), fields.ChannelID)
	require.Equal(t, "requested-probe-model", fields.OriginalModel)
	require.Equal(t, "mapped-probe-model", fields.ChannelMappedModel)
	require.Equal(t, service.BillingModelSourceChannelMapped, fields.BillingModelSource)
	require.Equal(t, "requested-probe-model→mapped-probe-model", fields.ModelMappingChain)
}

func TestProbeCandidateKeyIncludesRestrictedWorkspaceScope(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	body := []byte(`{"model":"probe-model","messages":[{"role":"user","content":"Calculate and respond with ONLY the number, nothing else.\n\nQ: 3 + 5 = ?\nA: 8\n\nQ: 12 - 7 = ?\nA: 5\n\nQ: 19 - 7 = ?\nA:"}],"max_tokens":50,"stream":false}`)

	c.Request = c.Request.WithContext(service.WithScope(c.Request.Context(), service.VendorScope(7, service.WorkspacePermissions{})))
	first, ok := probeCandidateForRequest(c, body, "probe-model", 7, "openai", 101)
	require.True(t, ok)

	c.Request = c.Request.WithContext(service.WithScope(c.Request.Context(), service.VendorScope(9, service.WorkspacePermissions{})))
	second, ok := probeCandidateForRequest(c, body, "probe-model", 7, "openai", 101)
	require.True(t, ok)
	require.NotEqual(t, first.Key, second.Key)

	// An unrestricted/admin scope intentionally uses the global key namespace.
	c.Request = c.Request.WithContext(service.WithScope(c.Request.Context(), service.AdminScope()))
	admin, ok := probeCandidateForRequest(c, body, "probe-model", 7, "openai", 101)
	require.True(t, ok)
	require.NotEqual(t, first.Key, admin.Key)
}

func TestResolveProbeFollowerOnlyPromotesForPricingMiss(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	newFollower := func(t *testing.T) *ProbeLease {
		t.Helper()
		p := NewProbeCoalescer(ProbeCoalescerConfig{Mode: ProbeCoalescingActive, Window: time.Minute, LeaderTimeout: time.Second, AttemptBudget: 2})
		leader := p.Begin(context.Background(), testProbeCandidate(), "leader")
		follower := p.Begin(context.Background(), testProbeCandidate(), "follower")
		leader.MarkBillingReady(nil)
		leader.Finish(true, "leader", &service.Account{ID: 1})
		return follower
	}

	pricingMiss := newFollower(t)
	handled, promoted, err := resolveProbeFollower(c, pricingMiss, func(context.Context, ProbeCandidate, *service.Account, string) error {
		return service.ErrSyntheticProbePricingUnavailable
	})
	require.False(t, handled)
	require.True(t, promoted)
	require.NoError(t, err)

	billingFailure := newFollower(t)
	handled, promoted, err = resolveProbeFollower(c, billingFailure, func(context.Context, ProbeCandidate, *service.Account, string) error {
		return errors.New("balance unavailable")
	})
	require.True(t, handled)
	require.False(t, promoted)
	require.EqualError(t, err, "balance unavailable")
}
