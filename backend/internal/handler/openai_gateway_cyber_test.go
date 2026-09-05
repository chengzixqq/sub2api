package handler

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type cyberUsageCaptureRepo struct {
	service.UsageLogRepository
	logs chan *service.UsageLog
}

func (r *cyberUsageCaptureRepo) Create(_ context.Context, log *service.UsageLog) (bool, error) {
	r.logs <- log
	return true, nil
}

// newTestGinContext builds a bare gin.Context backed by an httptest recorder.
func newTestGinContext() *gin.Context {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	return c
}

// TestRecordCyberPolicyIfMarked_NoMark verifies that when no cyber mark is set,
// the function returns immediately and does NOT set the recorded flag.
func TestRecordCyberPolicyIfMarked_NoMark(t *testing.T) {
	c := newTestGinContext()
	h := &OpenAIGatewayHandler{}

	h.recordCyberPolicyIfMarked(c, nil, nil, nil, "gpt-5", true, nil, service.ChannelUsageFields{}, "")

	// Flag must NOT be set when there was no mark.
	require.False(t, c.GetBool(cyberPolicyRecordedKey),
		"cyberPolicyRecordedKey must remain false when no cyber mark is present")
}

// TestRecordCyberPolicyIfMarked_WithMark verifies that:
//  1. When a cyber mark is present, the recorded flag is set (guard activated).
//  2. A second call is a no-op (idempotent guard).
//  3. Nil services do not panic.
func TestRecordCyberPolicyIfMarked_WithMark(t *testing.T) {
	c := newTestGinContext()
	service.MarkOpsCyberPolicy(c, service.CyberPolicyMark{
		Message:        "flagged",
		Body:           `{"error":{"code":"cyber_policy"}}`,
		UpstreamStatus: 400,
	})

	h := &OpenAIGatewayHandler{} // nil services — must not panic

	// First call: should set the flag.
	require.NotPanics(t, func() {
		h.recordCyberPolicyIfMarked(c, nil, nil, nil, "gpt-5", true, nil, service.ChannelUsageFields{}, "")
	})
	require.True(t, c.GetBool(cyberPolicyRecordedKey),
		"cyberPolicyRecordedKey must be true after first call with a mark")

	// Second call: flag already set — must be a no-op (idempotent).
	require.NotPanics(t, func() {
		h.recordCyberPolicyIfMarked(c, nil, nil, nil, "gpt-5", false, nil, service.ChannelUsageFields{}, "")
	})
	// Flag should still be true (not toggled or cleared).
	require.True(t, c.GetBool(cyberPolicyRecordedKey),
		"cyberPolicyRecordedKey must remain true after second call (guard)")
}

// TestRecordCyberPolicyIfMarked_ForwardSuccessSkipsUsageLog verifies the semantic:
// when forwardErrored=false the function still sets the guard flag (mark present),
// but the cyber usage row is NOT requested (only RecordCyberPolicyEvent fires).
// Since services are nil here we only verify the guard flag and no panic.
func TestRecordCyberPolicyIfMarked_ForwardSuccessSkipsUsageLog(t *testing.T) {
	c := newTestGinContext()
	service.MarkOpsCyberPolicy(c, service.CyberPolicyMark{
		Message:        "flagged",
		UpstreamStatus: 200,
	})

	h := &OpenAIGatewayHandler{}

	require.NotPanics(t, func() {
		h.recordCyberPolicyIfMarked(c, nil, nil, nil, "gpt-5", false /* forwardErrored=false */, nil, service.ChannelUsageFields{}, "")
	})
	require.True(t, c.GetBool(cyberPolicyRecordedKey))
}

// TestRecordCyberPolicyIfMarked_ReportsWhetherCyberUsageRowWasSubmitted 锁定返回值
// 语义：调用方（chat/completions、/v1/responses、/v1/messages）用它决定
// billingSettlementGuard.MarkSettled()。返回 true 必须严格等价于「真的提交了 cyber 计费
// 行」——放宽会让本该兜底的普通失败被标成已结算（少算），收紧会在 cyber 那笔之外再兜底记一
// 笔（多算，即首轮复审报出的 Critical）。
func TestRecordCyberPolicyIfMarked_ReportsWhetherCyberUsageRowWasSubmitted(t *testing.T) {
	usageRepo := &fakeFailureSinkUsageLogRepo{}
	newHandler := func() *OpenAIGatewayHandler {
		return &OpenAIGatewayHandler{gatewayService: newCyberUsageTestGatewayService(usageRepo)}
	}
	apiKey := &service.APIKey{ID: 7, User: &service.User{ID: 9}}
	account := &service.Account{ID: 3, Platform: service.PlatformOpenAI}

	t.Run("no mark", func(t *testing.T) {
		c := newTestGinContext()
		require.False(t, newHandler().recordCyberPolicyIfMarked(c, apiKey, account, nil, "gpt-5", true, "", service.ChannelUsageFields{}, ""),
			"没有 cyber 标记就没有 cyber 计费行，必须返回 false，否则兜底被错误跳过（少算）")
	})

	t.Run("forward succeeded", func(t *testing.T) {
		c := newTestGinContext()
		service.MarkOpsCyberPolicy(c, service.CyberPolicyMark{Message: "flagged", UpstreamStatus: 200})
		require.False(t, newHandler().recordCyberPolicyIfMarked(c, apiKey, account, nil, "gpt-5", false, "", service.ChannelUsageFields{}, ""),
			"forward 成功时 cyber 不写计费行（由正常 RecordUsage 负责），必须返回 false")
	})

	t.Run("second call is deduped", func(t *testing.T) {
		c := newTestGinContext()
		service.MarkOpsCyberPolicy(c, service.CyberPolicyMark{Message: "flagged", UpstreamStatus: 200})
		h := newHandler()
		require.True(t, h.recordCyberPolicyIfMarked(c, apiKey, account, nil, "gpt-5", true, "", service.ChannelUsageFields{}, ""))
		require.False(t, h.recordCyberPolicyIfMarked(c, apiKey, account, nil, "gpt-5", true, "", service.ChannelUsageFields{}, ""),
			"重复调用被 cyberPolicyRecordedKey 短路、没有第二条计费行，必须返回 false")
	})

	t.Run("incomplete input records nothing", func(t *testing.T) {
		c := newTestGinContext()
		service.MarkOpsCyberPolicy(c, service.CyberPolicyMark{Message: "flagged", UpstreamStatus: 200})
		require.False(t, newHandler().recordCyberPolicyIfMarked(c, nil, nil, nil, "gpt-5", true, "", service.ChannelUsageFields{}, ""),
			"入参不足以落账（apiKey/account 为 nil）时 RecordCyberPolicyUsageLog 会 return，必须返回 false")
	})

	t.Run("cyber usage row submitted", func(t *testing.T) {
		c := newTestGinContext()
		service.MarkOpsCyberPolicy(c, service.CyberPolicyMark{
			Message: "flagged", UpstreamStatus: 200, UpstreamInTok: 120, UpstreamOutTok: 8,
		})
		require.True(t, newHandler().recordCyberPolicyIfMarked(c, apiKey, account, nil, "gpt-5", true, "", service.ChannelUsageFields{}, ""),
			"cyber 按上游真实 usage 记了账，必须返回 true 让请求级兜底让路，否则同一请求计费两次")
	})
}

func TestRecordCyberPolicyIfMarked_PreservesPartialImageInCyberRow(t *testing.T) {
	repo := &cyberUsageCaptureRepo{logs: make(chan *service.UsageLog, 1)}
	h := &OpenAIGatewayHandler{gatewayService: newCyberUsageTestGatewayService(repo)}
	c := newTestGinContext()
	c.Request = httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{}`))
	service.MarkOpsCyberPolicy(c, service.CyberPolicyMark{
		Message:        "flagged",
		UpstreamStatus: 400,
		UpstreamInTok:  12,
		UpstreamOutTok: 3,
	})
	apiKey := &service.APIKey{ID: 7, User: &service.User{ID: 9}}
	account := &service.Account{ID: 3, Platform: service.PlatformOpenAI}
	result := &service.OpenAIForwardResult{
		RequestID:     "cyber-image-request",
		Model:         "draw-alias",
		BillingModel:  "gpt-image-1",
		UpstreamModel: "gpt-image-1",
		Usage: service.OpenAIUsage{
			ImageInputTokens:  5,
			ImageOutputTokens: 7,
		},
		ImageCount:         2,
		ImageSize:          "1024x1024",
		ImageOutputSize:    "1024x1024",
		ImageOutputSizes:   []string{"1024x1024", "1024x1024"},
		ImageSizeSource:    "output",
		ImageSizeBreakdown: map[string]int{"1024x1024": 2},
	}

	require.True(t, h.recordCyberPolicyIfMarked(
		c,
		apiKey,
		account,
		nil,
		"draw-alias",
		true,
		"",
		service.ChannelUsageFields{},
		service.HashUsageRequestPayload([]byte(`{}`)),
		cyberUsageObservation{SearchCount: 4, Result: result},
	))

	select {
	case log := <-repo.logs:
		require.Equal(t, "cyber-image-request", log.RequestID)
		require.Equal(t, service.RequestTypeCyberBlocked, log.RequestType)
		require.Equal(t, 2, log.ImageCount)
		require.Equal(t, 5, log.ImageInputTokens)
		require.Equal(t, 7, log.ImageOutputTokens)
		require.NotNil(t, log.ImageSize)
		require.Equal(t, "1K", *log.ImageSize)
		require.Equal(t, map[string]int{"1K": 2}, log.ImageSizeBreakdown)
		require.NotNil(t, log.UpstreamModel)
		require.Equal(t, "gpt-image-1", *log.UpstreamModel)
		require.NotNil(t, log.BillingProvenance)
		require.Equal(t, string(service.BillingProvenanceFailedUpstream), *log.BillingProvenance)
		require.Greater(t, log.TotalCost, 0.0)
	case <-time.After(time.Second):
		t.Fatal("cyber partial-image usage row was not recorded")
	}
}

// newCyberUsageTestGatewayService 构造一个足以让 RecordCyberPolicyUsageLog 的入参校验
// 通过的最小 OpenAIGatewayService（本测试只断言返回值语义，不断言落库内容）。
func newCyberUsageTestGatewayService(usageRepo service.UsageLogRepository) *service.OpenAIGatewayService {
	cfg := &config.Config{}
	cfg.Default.RateMultiplier = 1
	return service.NewOpenAIGatewayService(
		nil, usageRepo, nil, &fakeFailureSinkUserRepo{}, &fakeFailureSinkSubRepo{},
		nil, nil, cfg, nil, nil, service.NewBillingService(cfg, nil), nil,
		&service.BillingCacheService{}, nil, &service.DeferredService{},
		nil, nil, nil, nil, nil, nil, nil,
		nil, // workspaceService
	)
}

// TestClearCyberPolicyTurnState verifies F1 at the handler level: after a turn
// is finalized, both the mark and the recorded guard are reset so the next WS
// turn detects/records independently.
func TestClearCyberPolicyTurnState(t *testing.T) {
	c := newTestGinContext()
	h := &OpenAIGatewayHandler{}

	service.MarkOpsCyberPolicy(c, service.CyberPolicyMark{Message: "turn1", UpstreamStatus: 200})
	h.recordCyberPolicyIfMarked(c, nil, nil, nil, "gpt-5", false, nil, service.ChannelUsageFields{}, "")
	require.True(t, c.GetBool(cyberPolicyRecordedKey))

	clearCyberPolicyTurnState(c)
	require.Nil(t, service.GetOpsCyberPolicy(c))
	require.False(t, c.GetBool(cyberPolicyRecordedKey))

	// turn2: a fresh cyber hit must be recordable again.
	service.MarkOpsCyberPolicy(c, service.CyberPolicyMark{Message: "turn2", UpstreamStatus: 200})
	h.recordCyberPolicyIfMarked(c, nil, nil, nil, "gpt-5", false, nil, service.ChannelUsageFields{}, "")
	require.True(t, c.GetBool(cyberPolicyRecordedKey))
	require.Equal(t, "turn2", service.GetOpsCyberPolicy(c).Message)
}

// TestBuildCyberSessionBlockedOpsEntry verifies the locally-rejected request is
// auditable: 403 / phase=request / type=cyber_policy_session_blocked — distinct
// from upstream cyber_policy hits, and it must NOT touch moderation/violation.
func TestBuildCyberSessionBlockedOpsEntry(t *testing.T) {
	entry := buildCyberSessionBlockedOpsEntry(cyberPolicyOpsErrorMeta{
		RequestID: "req-9", Model: "gpt-5", RequestPath: "/openai/v1/responses",
	})
	require.Equal(t, 403, entry.StatusCode)
	require.Equal(t, "cyber_policy_session_blocked", entry.ErrorType)
	require.Equal(t, "request", entry.ErrorPhase)
	require.True(t, entry.IsBusinessLimited)
	require.Equal(t, "gateway_local", entry.ErrorSource)
	require.Equal(t, "platform", entry.ErrorOwner)
	require.Empty(t, entry.ErrorBody, "no session block key → ErrorBody must be empty")

	entryWithKey := buildCyberSessionBlockedOpsEntry(cyberPolicyOpsErrorMeta{
		RequestID: "req-9", Model: "gpt-5", RequestPath: "/openai/v1/responses",
		SessionBlockKey: "abc123",
	})
	require.Equal(t, "session_block_key=abc123", entryWithKey.ErrorBody)
}

// TestRejectIfCyberSessionBlocked_FailOpen verifies fail-open paths: nil handler
// services, no explicit session signal, and (implicitly) disabled switch all
// pass the request through.
func TestRejectIfCyberSessionBlocked_FailOpen(t *testing.T) {
	c := newTestGinContext()
	c.Request = httptest.NewRequest("POST", "/openai/v1/responses", strings.NewReader(`{}`))

	h := &OpenAIGatewayHandler{}
	require.False(t, h.rejectIfCyberSessionBlocked(c, nil, []byte(`{}`), "gpt-5", cyberBlockFormatResponses), "nil apiKey → pass")

	h2 := &OpenAIGatewayHandler{gatewayService: nil}
	key := &service.APIKey{ID: 1}
	require.False(t, h2.rejectIfCyberSessionBlocked(c, key, []byte(`{}`), "gpt-5", cyberBlockFormatResponses), "nil gateway service → pass")
}

func TestBuildCyberSessionBlockWritePlanCombinesExplicitAndTranscriptKeys(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"setup"},{"role":"assistant","content":"ready"},{"role":"user","content":"trigger"}]}`)
	c := newTestGinContext()
	c.Request = httptest.NewRequest("POST", "/openai/v1/responses", strings.NewReader(string(body)))
	c.Request.RemoteAddr = "203.0.113.44:12345"
	c.Request.Header.Set("User-Agent", "client/1.2.3")

	plan := buildCyberSessionBlockWritePlan(7, c, body)
	require.Len(t, plan.keys, 2)
	require.NotEmpty(t, plan.scopeKey)

	c.Request.Header.Set("session_id", "sess-explicit")
	plan = buildCyberSessionBlockWritePlan(7, c, body)
	require.Len(t, plan.keys, 3)
	require.NotEmpty(t, plan.scopeKey)
}

// TestRecordCyberPolicyIfMarked_BlockKeyPlumbed verifies the 6th param is
// accepted and a non-empty key with nil gateway service does not panic
// (write-side guards live in the service layer).
func TestRecordCyberPolicyIfMarked_BlockKeyPlumbed(t *testing.T) {
	c := newTestGinContext()
	service.MarkOpsCyberPolicy(c, service.CyberPolicyMark{Message: "x", UpstreamStatus: 400})
	h := &OpenAIGatewayHandler{}
	require.NotPanics(t, func() {
		h.recordCyberPolicyIfMarked(c, nil, nil, nil, "gpt-5", true, []byte(`{"input":"deadbeef"}`), service.ChannelUsageFields{}, "")
	})
}

// TestBuildCyberPolicyOpsErrorEntry_StatusCode verifies F6: the ops error log
// records the status the codex client actually received (400 non-stream / 200 stream),
// not a hardcoded 403.
func TestBuildCyberPolicyOpsErrorEntry_StatusCode(t *testing.T) {
	for _, tc := range []struct {
		name           string
		upstreamStatus int
	}{
		{"non_stream_400", 400},
		{"stream_200", 200},
		{"zero_value", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mark := &service.CyberPolicyMark{
				Code:           "cyber_policy",
				Message:        "blocked",
				UpstreamStatus: tc.upstreamStatus,
			}
			entry := buildCyberPolicyOpsErrorEntry(cyberPolicyOpsErrorMeta{
				RequestID: "req-1", Model: "gpt-5", RequestPath: "/openai/v1/responses",
			}, mark)
			require.Equal(t, tc.upstreamStatus, entry.StatusCode)
			require.Equal(t, "cyber_policy", entry.ErrorType)
			require.Equal(t, "request", entry.ErrorPhase)
		})
	}
}
