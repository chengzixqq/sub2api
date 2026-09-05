package handler

import (
	"context"
	"errors"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ip"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// failureSettlementSink 由各 handler 家族提供，负责把决策落成自己家族的
// RecordUsageInput / OpenAIRecordUsageInput 并提交到 worker 池。
type failureSettlementSink func(service.FailureBillingDecision, *service.Account)

type guardDeps struct {
	// estimatedPromptTokens 惰性求值：估算要扫描整个请求体（180KB body 实测
	// 15.5 ms/op、12.1 MB、216106 allocs/op），绝大多数请求是成功的，不该在
	// 热路径上无条件付这个成本。只有真的走到失败兜底计费时才调用。nil 按 0 处理。
	estimatedPromptTokens func() int
	observedEvents        func() int
	sink                  failureSettlementSink
	resetAttemptOutput    func()
	// upstreamUsageOnly is a request-start snapshot. The guard must not read
	// mutable settings during deferred Flush, after the request may have ended.
	upstreamUsageOnly bool
}

// failureBillingUpstreamUsageOnlySnapshot resolves the policy once while a
// request is being wired. OpenAI handlers do not carry a SettingService field;
// they use the service registered by NewGatewayHandler through the existing
// probe-settings registration path. Missing settings fail closed to false.
func failureBillingUpstreamUsageOnlySnapshot(ctx context.Context, settings *service.SettingService) bool {
	if settings == nil {
		settings = defaultProbeSettings.Load()
	}
	if settings == nil {
		return false
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return settings.GetFailureBillingUpstreamUsageOnlyCached(ctx)
}

// billingSettlementGuard 保证每个请求恰好结算一次。
//
// 网关有 73 个失败早退点（handleFailoverExhausted 36 / failoverClientGone 28 /
// *ForwardErrorAlreadyCommunicated 9），分布在 10 个文件、两个 handler 家族。
// 逐点挂钩漏一处就静默漏钱，且编译器发现不了；上游每次新增早退点还会重新漏。
// 因此改为请求内注册一次 defer：无论从哪个 return 退出都必然执行，新增早退点
// 默认被覆盖而不是默认漏钱。
type billingSettlementGuard struct {
	deps    guardDeps
	settled bool
	flushed bool
	account *service.Account
	ferr    *service.UpstreamFailoverError

	// forwardErr 是 Forward 返回的原始错误快照（含非 UpstreamFailoverError）。
	// 计费判定本身只用 ferr + outputStarted，这里留原始错误是为了排查
	// 「这笔失败计费到底因为什么」时不必反推——注意它当前不参与决策。
	forwardErr error
	// outputStarted 在 Forward 返回的那一刻定格，不可惰性求值，理由见
	// ObserveForwardOutcome 的文档。
	outputStarted      bool
	usage              service.ClaudeUsage
	requestID          string
	billingModel       string
	upstreamModel      string
	imageCount         int
	imageSize          string
	imageInputSize     string
	imageOutputSize    string
	imageOutputSizes   []string
	imageSizeSource    string
	imageSizeBreakdown map[string]int
	clientDisconnect   bool

	// Search calls are real upstream work and may be charged on every retry.
	// Token usage intentionally remains attempt-scoped, while search calls are
	// accumulated for the whole downstream request and settled exactly once.
	attemptNo             int
	attemptSearchCount    int
	cumulativeSearchCount int
}

type billingSearchAttemptSnapshot struct {
	Attempt               int
	AttemptSearchCount    int
	CumulativeSearchCount int
}

func newBillingSettlementGuard(deps guardDeps) *billingSettlementGuard {
	return &billingSettlementGuard{deps: deps}
}

// ObserveAttempt 在转发循环内每次选定账号后调用。
func (g *billingSettlementGuard) ObserveAttempt(account *service.Account) {
	if g != nil {
		if g.deps.resetAttemptOutput != nil {
			g.deps.resetAttemptOutput()
		}
		g.account = account
		g.ferr = nil
		g.forwardErr = nil
		g.outputStarted = false
		g.usage = service.ClaudeUsage{}
		g.requestID = ""
		g.billingModel = ""
		g.upstreamModel = ""
		g.imageCount = 0
		g.imageSize = ""
		g.imageInputSize = ""
		g.imageOutputSize = ""
		g.imageOutputSizes = nil
		g.imageSizeSource = ""
		g.imageSizeBreakdown = nil
		g.clientDisconnect = false
		g.attemptNo++
		g.attemptSearchCount = 0
	}
}

func billingAttemptOutputReset(c *gin.Context) func() {
	return func() {
		if c != nil {
			c.Set(service.GatewayUpstreamDeliveredKey, false)
		}
	}
}

// ObserveForwardOutcome 在 Forward 返回后立刻调用，同步快照两件事：
//
//   - 失败原因。必须收下全部错误，而不只是 *UpstreamFailoverError：
//     BetaBlockedError / PromptTooLongError / 「empty request」这类失败
//     ferr 为 nil，若只在 errors.As 分支里记录，Flush 会把它们当成
//     「没有错误」直接落到估算分支计费，而上游根本没收到过这个请求。
//
//   - outputStarted 必须在此刻定格。handler 后续写的错误响应体同样会让
//     c.Writer.Size() 增长，而 Flush 由 defer 触发、必然晚于那次写入；
//     惰性求值会把错误体本身误判成「上游已投递推理内容」，让 429 免计费
//     豁免 100% 失效，并把 completion 从 0 抬到 1。
//
// outputStarted 请用 forwardDeliveredStreamContent(c) 计算，不要直接传
// c.Writer.Size() != writerSizeBeforeForward——handler 层区分不了「上游投递的帧」
// 与「sub2api 自造的 keepalive ping / 错误事件帧」，只有读 service 层显式设置的
// service.GatewayUpstreamDeliveredKey 标记才准确，理由见该函数文档。
func (g *billingSettlementGuard) ObserveForwardOutcome(err error, outputStarted bool) {
	if g == nil {
		return
	}
	g.forwardErr = err
	g.outputStarted = outputStarted
	if errors.Is(err, context.Canceled) {
		g.clientDisconnect = true
	}
	g.ferr = nil
	var ferr *service.UpstreamFailoverError
	if errors.As(err, &ferr) {
		g.ferr = ferr
	}
}

// ObservePartialUsage stages usage observed during the current attempt. A
// subsequent ObserveAttempt replaces it, so only the final exhausted attempt
// can reach failure settlement.
func (g *billingSettlementGuard) ObservePartialUsage(usage service.ClaudeUsage) {
	if g != nil {
		g.usage = usage
	}
}

func (g *billingSettlementGuard) ObserveForwardResult(result *service.ForwardResult) {
	if result != nil {
		g.ObservePartialUsage(result.Usage)
		g.clientDisconnect = g.clientDisconnect || result.ClientDisconnect
	}
}

func (g *billingSettlementGuard) ObserveOpenAIForwardResult(result *service.OpenAIForwardResult) billingSearchAttemptSnapshot {
	if result == nil {
		return g.searchAttemptSnapshot()
	}
	g.ObservePartialUsage(service.ClaudeUsage{
		InputTokens:              result.Usage.InputTokens,
		OutputTokens:             result.Usage.OutputTokens,
		CacheReadInputTokens:     result.Usage.CacheReadInputTokens,
		CacheCreationInputTokens: result.Usage.CacheCreationInputTokens,
		ImageInputTokens:         result.Usage.ImageInputTokens,
		ImageOutputTokens:        result.Usage.ImageOutputTokens,
	})
	g.imageCount = result.ImageCount
	if g.imageCount < 0 {
		g.imageCount = 0
	}
	g.imageSize = result.ImageSize
	g.imageInputSize = result.ImageInputSize
	g.imageOutputSize = result.ImageOutputSize
	g.imageOutputSizes = append([]string(nil), result.ImageOutputSizes...)
	g.imageSizeSource = result.ImageSizeSource
	g.imageSizeBreakdown = cloneBillingImageSizeBreakdown(result.ImageSizeBreakdown)
	g.clientDisconnect = g.clientDisconnect || result.ClientDisconnect
	g.requestID = result.RequestID
	g.billingModel = result.BillingModel
	g.upstreamModel = result.UpstreamModel

	searchCount := service.NormalizeSearchCount(result.SearchCount)
	if result.SearchCount < 0 {
		logger.L().Warn("openai.search_count_negative_clamped",
			zap.Int("search_count", result.SearchCount),
			zap.Int("attempt", g.attemptNo),
		)
	}
	if searchCount > g.attemptSearchCount {
		delta := searchCount - g.attemptSearchCount
		g.attemptSearchCount = searchCount
		var saturated bool
		g.cumulativeSearchCount, saturated = service.SaturatingSearchCountAdd(g.cumulativeSearchCount, delta)
		if saturated {
			logger.L().Warn("openai.search_count_overflow_saturated",
				zap.Int("attempt", g.attemptNo),
				zap.Int("attempt_search_count", searchCount),
			)
		}
	}
	return g.searchAttemptSnapshot()
}

func (g *billingSettlementGuard) searchAttemptSnapshot() billingSearchAttemptSnapshot {
	if g == nil {
		return billingSearchAttemptSnapshot{}
	}
	return billingSearchAttemptSnapshot{
		Attempt:               g.attemptNo,
		AttemptSearchCount:    g.attemptSearchCount,
		CumulativeSearchCount: g.cumulativeSearchCount,
	}
}

func (g *billingSettlementGuard) CumulativeSearchCount() int {
	if g == nil {
		return 0
	}
	return g.cumulativeSearchCount
}

func logBillingSearchAttempt(log *zap.Logger, accountID int64, snapshot billingSearchAttemptSnapshot, forwardErr error) {
	if snapshot.AttemptSearchCount <= 0 {
		return
	}
	if log == nil {
		log = logger.L()
	}
	log.Info("openai.search_attempt_observed",
		zap.Int64("account_id", accountID),
		zap.Int("attempt", snapshot.Attempt),
		zap.Int("attempt_search_count", snapshot.AttemptSearchCount),
		zap.Int("cumulative_search_count", snapshot.CumulativeSearchCount),
		zap.Bool("forward_failed", forwardErr != nil),
	)
}

// forwardDeliveredStreamContent 判定 Forward 期间上游是否真的向客户端投递过推理内容。
//
// 只信任 service 层在观测到「上游第一帧真实内容」时设置的 service.GatewayUpstreamDeliveredKey 标记，
// 不看 writer 字节数——handler 层区分不了「上游投递的帧」与「sub2api 自造的 keepalive ping /
// 错误事件帧」(gateway_upstream_response.go 的 stream_timeout/stream_read_error 等 sendErrorEvent
// 写入点，以及 keepalive ping)，用字节启发式必然把后者误判成投递，导致挂死、零投递的上游请求也被
// 按整个估算 prompt 计费。
// 标记默认 false：任何未打标记的路径都按「未投递」处理，方向是少算，与「宁可少算，不可多算」同侧。
func forwardDeliveredStreamContent(c *gin.Context) bool {
	if c == nil {
		return false
	}
	return c.GetBool(service.GatewayUpstreamDeliveredKey)
}

// MarkSettled 由成功路径调用，表示已提交正常 RecordUsage。
func (g *billingSettlementGuard) MarkSettled() {
	if g != nil {
		g.settled = true
	}
}

// Flush 以 defer 方式调用。幂等。
func (g *billingSettlementGuard) Flush() {
	if g == nil || g.settled || g.flushed {
		return
	}
	g.flushed = true

	// 从未选到账号 = 从未接触上游，上游不会计费。
	if g.account == nil || g.deps.sink == nil {
		return
	}

	// 上游既没返回可识别的失败响应，也没投递任何内容 = 请求没到上游或被本地拒绝，
	// 上游不会计费。BetaBlockedError 这类本地策略拒绝走这里。
	//
	// 反过来不能简化成「ferr == nil 一律不计费」：gateway_forward.go:846
	// 是流式已开始后中断的路径，ferr 为 nil 但上游已经投递过 token，
	// 那是本特性最该计费的场景，由 outputStarted 兜住。
	if g.ferr == nil && !g.outputStarted && !g.clientDisconnect && !hasFailureBillingUsage(g.usage) && g.imageCount == 0 && g.cumulativeSearchCount == 0 {
		return
	}

	in := service.FailureBillingInput{
		RequestID:          g.requestID,
		BillingModel:       g.billingModel,
		UpstreamModel:      g.upstreamModel,
		Usage:              g.usage,
		SearchCount:        g.cumulativeSearchCount,
		ImageCount:         g.imageCount,
		ImageSize:          g.imageSize,
		ImageInputSize:     g.imageInputSize,
		ImageOutputSize:    g.imageOutputSize,
		ImageOutputSizes:   append([]string(nil), g.imageOutputSizes...),
		ImageSizeSource:    g.imageSizeSource,
		ImageSizeBreakdown: cloneBillingImageSizeBreakdown(g.imageSizeBreakdown),
		Err:                g.ferr,
		OutputStarted:      g.outputStarted,
		ClientDisconnect:   g.clientDisconnect,
		UpstreamUsageOnly:  g.deps.upstreamUsageOnly,
	}
	if g.deps.estimatedPromptTokens != nil {
		in.EstimatedPromptTokens = g.deps.estimatedPromptTokens()
	}
	if g.deps.observedEvents != nil {
		in.ObservedResponseEvents = g.deps.observedEvents()
	}

	decision := service.DecideFailureBilling(in)
	if !decision.Billable {
		return
	}

	g.deps.sink(decision, g.account)
}

func cloneBillingImageSizeBreakdown(in map[string]int) map[string]int {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]int, len(in))
	for size, count := range in {
		out[size] = count
	}
	return out
}

func hasFailureBillingUsage(usage service.ClaudeUsage) bool {
	return usage.InputTokens > 0 || usage.OutputTokens > 0 ||
		usage.CacheReadInputTokens > 0 || usage.CacheCreationInputTokens > 0 ||
		usage.CacheCreation5mTokens > 0 || usage.CacheCreation1hTokens > 0 ||
		usage.ImageInputTokens > 0 || usage.ImageOutputTokens > 0
}

// claudeFailureSink 把失败计费决策落成 Claude 家族的 RecordUsageInput，
// 复用与成功路径完全相同的 RecordUsage 计费管线，不新增第二套费用计算。
//
// 已知偏差：Gemini 成功路径走 RecordUsageWithLongContext（200000 阈值、2.0 倍率），
// 失败路径经本 sink 走普通 RecordUsage，因此 300K token 的失败 Gemini 请求按 1× 计。
// 方向是少算，与「宁可少算，不可多算」同侧，暂按已知偏差接受，不在此处补倍率。
func (h *GatewayHandler) claudeFailureSink(
	c *gin.Context, apiKey *service.APIKey, subscription *service.UserSubscription,
	reqModel string, channelMapping service.ChannelMappingResult, body []byte,
) failureSettlementSink {
	// 在请求 goroutine 内取快照，worker 池中不得访问 gin.Context。
	userAgent := c.GetHeader("User-Agent")
	clientIP := ip.GetClientIP(c)
	inboundEndpoint := GetInboundEndpoint(c)
	sessionID := service.ExtractClientSessionID(c)
	requestPayloadHash := service.HashUsageRequestPayload(body)
	quotaPlatform := service.QuotaPlatform(c.Request.Context(), apiKey)
	return func(d service.FailureBillingDecision, account *service.Account) {
		provenance := string(d.Provenance)
		result := &service.ForwardResult{
			RequestID: c.Writer.Header().Get("X-Request-Id"),
			Usage:     d.Usage,
			Model:     reqModel,
		}
		// Flush() 与本闭包本体都同步跑在请求 goroutine 内（由 defer 触发，
		// 尚未进入下面的 worker 池闭包），此时访问 c 仍然安全。
		upstreamEndpoint := GetUpstreamEndpoint(c, account.Platform)
		channelUsageFields := clientRequestedUsageFields(c, channelMapping, reqModel, "")
		logger.FromContext(c.Request.Context()).Warn("gateway.failure_billed",
			zap.String("reason", d.Reason),
			zap.String("provenance", provenance),
			zap.Int("input_tokens", d.Usage.InputTokens),
			zap.Int("output_tokens", d.Usage.OutputTokens),
		)
		h.submitUsageRecordTask(c.Request.Context(), func(ctx context.Context) {
			if err := h.gatewayService.RecordUsage(ctx, &service.RecordUsageInput{
				Result:             result,
				APIKey:             apiKey,
				User:               apiKey.User,
				Account:            account,
				Subscription:       subscription,
				PricingAt:          service.GatewayTokenRequestPricingAtFromContext(c.Request.Context()),
				InboundEndpoint:    inboundEndpoint,
				UpstreamEndpoint:   upstreamEndpoint,
				UserAgent:          userAgent,
				IPAddress:          clientIP,
				SessionID:          sessionID,
				RequestPayloadHash: requestPayloadHash,
				QuotaPlatform:      quotaPlatform,
				APIKeyService:      h.apiKeyService,
				BillingProvenance:  &provenance,
				ChannelUsageFields: channelUsageFields,
			}); err != nil {
				logger.FromContext(ctx).Error("gateway.failure_usage_record_failed", zap.Error(err))
			}
		})
	}
}
