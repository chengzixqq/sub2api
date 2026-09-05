package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// fakeFailureSinkUsageLogRepo 只捕获 Create 收到的 *service.UsageLog，其余方法
// 通过内嵌的 nil service.UsageLogRepository 透传（本测试路径不会调用到）。
type fakeFailureSinkUsageLogRepo struct {
	service.UsageLogRepository

	calls   int
	lastLog *service.UsageLog
}

func (s *fakeFailureSinkUsageLogRepo) Create(ctx context.Context, log *service.UsageLog) (bool, error) {
	s.calls++
	s.lastLog = log
	return true, nil
}

type fakeFailureSinkUserRepo struct {
	service.UserRepository
}

func (s *fakeFailureSinkUserRepo) DeductBalance(ctx context.Context, id int64, amount float64) error {
	return nil
}

type fakeFailureSinkSubRepo struct {
	service.UserSubscriptionRepository
}

func (s *fakeFailureSinkSubRepo) IncrementUsage(ctx context.Context, id int64, costUSD float64) error {
	return nil
}

// newFailureSinkTestGatewayService 构造一个足以跑通 RecordUsage 计费管线的最小
// GatewayService，镜像 internal/service/gateway_record_usage_test.go 里的
// newGatewayRecordUsageServiceForTest（该文件带 //go:build unit，本包无法直接复用）。
func newFailureSinkTestGatewayService(usageRepo service.UsageLogRepository) *service.GatewayService {
	cfg := &config.Config{}
	cfg.Default.RateMultiplier = 1
	return service.NewGatewayService(
		nil, nil, usageRepo, nil,
		&fakeFailureSinkUserRepo{}, &fakeFailureSinkSubRepo{},
		nil, nil, cfg, nil, nil,
		service.NewBillingService(cfg, nil), nil, &service.BillingCacheService{}, nil, nil,
		&service.DeferredService{}, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
		nil, // workspaceService
	)
}

// newFailureSinkTestContext 构造一个最小可用的 gin.Context，携带确定性的
// User-Agent / Session-Id 请求头，供快照捕获断言使用。
func newFailureSinkTestContext(path string) *gin.Context {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	req := httptest.NewRequest(http.MethodPost, path, nil)
	req.Header.Set("User-Agent", "test-agent/1.0")
	req.Header.Set("X-Session-Id", "sess-123")
	c.Request = req
	return c
}

// TestClaudeFailureSink_RecordsFailureUsageWithProvenanceAndChannelFields 验证
// claudeFailureSink 把 FailureBillingDecision 正确落成 RecordUsageInput：
// BillingProvenance 与 ChannelUsageFields 必须一路传到持久化的 UsageLog（brief 原始
// sink 实现遗漏了后者，参见 task-6-brief.md 的编排者修正 #1）。
func TestClaudeFailureSink_RecordsFailureUsageWithProvenanceAndChannelFields(t *testing.T) {
	c := newFailureSinkTestContext("/v1/messages")

	usageRepo := &fakeFailureSinkUsageLogRepo{}
	h := &GatewayHandler{gatewayService: newFailureSinkTestGatewayService(usageRepo)}

	// Quota 留空（0）：h.apiKeyService 在本测试中是 nil，若 Quota>0 会触发
	// postUsageBilling.shouldDeductAPIKeyQuota() 对着装箱后的 nil *APIKeyService
	// 发起调用而 panic（非 nil 接口包了 nil 指针），该 panic 会被
	// submitUsageRecordTask 的 recover() 静默吞掉，导致本测试的落库断言看起来像
	// “没调用”而不是报错。这与 claudeFailureSink 本身的正确性无关。
	apiKey := &service.APIKey{ID: 501, User: &service.User{ID: 601}}
	reqModel := "claude-sonnet-4-5"
	channelMapping := service.ChannelMappingResult{
		ChannelID:          7,
		Mapped:             true,
		MappedModel:        "claude-sonnet-4-5-20250929",
		BillingModelSource: service.BillingModelSourceChannelMapped,
	}
	body := []byte(`{"model":"claude-sonnet-4-5"}`)

	sink := h.claudeFailureSink(c, apiKey, nil, reqModel, channelMapping, body)
	require.NotNil(t, sink)

	account := &service.Account{ID: 42, Platform: service.PlatformAnthropic}
	decision := service.FailureBillingDecision{
		Billable:   true,
		Usage:      service.ClaudeUsage{InputTokens: 123, OutputTokens: 45},
		Provenance: service.BillingProvenanceFailedEstimated,
		Reason:     "test_reason",
	}

	sink(decision, account)

	require.Equal(t, 1, usageRepo.calls)
	require.NotNil(t, usageRepo.lastLog)
	log := usageRepo.lastLog

	require.Equal(t, account.ID, log.AccountID)
	require.Equal(t, apiKey.ID, log.APIKeyID)
	require.Equal(t, 123, log.InputTokens)
	require.Equal(t, 45, log.OutputTokens)

	require.NotNil(t, log.BillingProvenance, "失败请求必须打上 BillingProvenance，不能落成看起来像成功的 NULL 行")
	require.Equal(t, string(service.BillingProvenanceFailedEstimated), *log.BillingProvenance)

	require.NotNil(t, log.ChannelID, "渠道映射信息必须传到失败计费路径，不能只在成功路径生效")
	require.Equal(t, channelMapping.ChannelID, *log.ChannelID)

	wantFields := clientRequestedUsageFields(c, channelMapping, reqModel, "")
	require.NotNil(t, log.ModelMappingChain)
	require.Equal(t, wantFields.ModelMappingChain, *log.ModelMappingChain)

	require.NotNil(t, log.UpstreamEndpoint)
	require.Equal(t, GetUpstreamEndpoint(c, account.Platform), *log.UpstreamEndpoint)
}

// TestClaudeFailureSink_UpstreamEndpointReflectsInvocationTimeAccount 验证
// UpstreamEndpoint 是在 sink 闭包被调用（即账号已选定）时才计算，而不是在
// claudeFailureSink 创建时（此时转发循环尚未开始，根本没有账号）。用两个平台不同的
// 账号各调用一次同一个 sink，断言两次落库的 UpstreamEndpoint 随账号平台变化。
func TestClaudeFailureSink_UpstreamEndpointReflectsInvocationTimeAccount(t *testing.T) {
	c := newFailureSinkTestContext("/v1/messages")

	usageRepo := &fakeFailureSinkUsageLogRepo{}
	h := &GatewayHandler{gatewayService: newFailureSinkTestGatewayService(usageRepo)}

	// Quota 留空，理由同上一测试。
	apiKey := &service.APIKey{ID: 501, User: &service.User{ID: 601}}
	sink := h.claudeFailureSink(c, apiKey, nil, "claude-sonnet-4-5", service.ChannelMappingResult{}, nil)

	decision := service.FailureBillingDecision{
		Billable:   true,
		Usage:      service.ClaudeUsage{InputTokens: 10, OutputTokens: 1},
		Provenance: service.BillingProvenanceFailedEstimated,
		Reason:     "test_reason",
	}

	anthropicAccount := &service.Account{ID: 1, Platform: service.PlatformAnthropic}
	sink(decision, anthropicAccount)
	require.Equal(t, 1, usageRepo.calls)
	require.NotNil(t, usageRepo.lastLog.UpstreamEndpoint)
	anthropicEndpoint := *usageRepo.lastLog.UpstreamEndpoint

	geminiAccount := &service.Account{ID: 2, Platform: service.PlatformGemini}
	sink(decision, geminiAccount)
	require.Equal(t, 2, usageRepo.calls)
	require.NotNil(t, usageRepo.lastLog.UpstreamEndpoint)
	geminiEndpoint := *usageRepo.lastLog.UpstreamEndpoint

	require.NotEqual(t, anthropicEndpoint, geminiEndpoint,
		"UpstreamEndpoint 必须按调用时传入的 account.Platform 重新推导，而不是在 sink 创建时固化")
	require.Equal(t, GetUpstreamEndpoint(c, service.PlatformAnthropic), anthropicEndpoint)
	require.Equal(t, GetUpstreamEndpoint(c, service.PlatformGemini), geminiEndpoint)
}

// TestClaudeFailureSink_NotBillableDecisionNeverReachesSink 验证守卫的“不计费”判定
// （如凭证失败）在到达 claudeFailureSink 之前就短路，sink 本身完全不会被调用——这是
// billingSettlementGuard.Flush 的既有职责，这里只确认 claudeFailureSink 接入后没有
// 绕开该短路。
func TestClaudeFailureSink_NotBillableDecisionNeverReachesSink(t *testing.T) {
	c := newFailureSinkTestContext("/v1/messages")

	usageRepo := &fakeFailureSinkUsageLogRepo{}
	h := &GatewayHandler{gatewayService: newFailureSinkTestGatewayService(usageRepo)}

	apiKey := &service.APIKey{ID: 501, User: &service.User{ID: 601}, Quota: 100}
	sink := h.claudeFailureSink(c, apiKey, nil, "claude-sonnet-4-5", service.ChannelMappingResult{}, nil)

	guard := newBillingSettlementGuard(guardDeps{
		estimatedPromptTokens: func() int { return 100 },
		sink:                  sink,
	})
	guard.ObserveAttempt(&service.Account{ID: 9, Platform: service.PlatformAnthropic})
	guard.ObserveForwardOutcome(&service.UpstreamFailoverError{Scope: service.GatewayFailureScopeRequest}, false)

	guard.Flush()

	require.Equal(t, 0, usageRepo.calls, "request-scope 失败（未进入推理）不应计费")
}
