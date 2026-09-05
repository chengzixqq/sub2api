package handler

import (
	"context"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ip"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// openAIFailureSinkParams 是 openAIFailureSink 的入参。OpenAI 家族 6 个 handler 的
// 模型变量名、渠道映射来源、payload hash 口径各不相同（images 的 multipart 分支用
// StickySessionSeed、grok_media 的 lookup 端点用 requestID），因此统一收进结构体，
// 由各 handler 自己把已解析好的值填进来，而不是在 sink 内部重新推导。
type openAIFailureSinkParams struct {
	// APIKey / Subscription 来自请求上下文，失败结算与成功路径用同一份。
	APIKey       *service.APIKey
	Subscription *service.UserSubscription
	// ReqModel 是计费与日志用的模型名（客户端请求的模型，非映射后的上游模型）。
	ReqModel string
	// ChannelUsageFields 渠道归因字段。各 handler 的映射来源不同：多数走
	// clientRequestedUsageFields(c, channelMapping, ...)，grok_media 自建。
	// 失败时上游模型未知，一律按空 upstreamModel 归因。
	ChannelUsageFields service.ChannelUsageFields
	// RequestPayloadBytes 是参与 payload hash 的字节。注意它只用于哈希，
	// 不参与 token 估算——估算是否发生完全由 guardDeps.estimatedPromptTokens 决定。
	RequestPayloadBytes []byte
	// Component 是日志 component 字段，便于按端点区分失败计费来源。
	Component string
}

func cloneOpenAIFailureImageSizeBreakdown(in map[string]int) map[string]int {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]int, len(in))
	for size, count := range in {
		out[size] = count
	}
	return out
}

// openAIFailureSink 把失败计费决策落成 OpenAI 家族的 OpenAIRecordUsageInput，
// 复用与成功路径完全相同的 RecordUsage 计费管线，不新增第二套费用计算。
// 它是 claudeFailureSink 的同构体，四点差异：
//
//  1. 用 OpenAIRecordUsageInput / OpenAIForwardResult（不是 Claude 家族的
//     RecordUsageInput / ForwardResult）；
//  2. ClaudeUsage → OpenAIUsage 按同名字段直映（InputTokens / OutputTokens /
//     CacheReadInputTokens / CacheCreationInputTokens）；
//  3. 提交走 h.submitOpenAIUsageRecordTask（比 Claude 家族的 submitUsageRecordTask
//     多一个 result 形参：result.ImageCount > 0 时改用 mandatory 池，池满同步兜底）；
//  4. 该家族用局部 lastFailoverErr 变量而非 FailoverState，与 guard 无关。
//
// 已知偏差（少算方向，按「宁可少算，不可多算」接受）：本 sink 构造的 result 的
// ImageCount 恒为 0，因此失败的图片请求不会按张计费——图片端点的失败结算只可能
// 落到「上游给了真实 token usage」或「零成本行」，绝不会凭空按张收钱。
func (h *OpenAIGatewayHandler) openAIFailureSink(
	c *gin.Context, p openAIFailureSinkParams,
) failureSettlementSink {
	// 在请求 goroutine 内取快照，worker 池中不得访问 gin.Context。
	apiKey := p.APIKey
	userAgent := c.GetHeader("User-Agent")
	clientIP := ip.GetClientIP(c)
	inboundEndpoint := GetInboundEndpoint(c)
	sessionID := service.ExtractClientSessionID(c)
	requestPayloadHash := service.HashUsageRequestPayload(p.RequestPayloadBytes)
	quotaPlatform := service.QuotaPlatform(c.Request.Context(), apiKey)
	channelUsageFields := p.ChannelUsageFields
	reqModel := p.ReqModel
	component := p.Component
	subscription := p.Subscription

	return func(d service.FailureBillingDecision, account *service.Account) {
		provenance := string(d.Provenance)
		requestID := strings.TrimSpace(d.RequestID)
		if requestID == "" {
			requestID = c.Writer.Header().Get("X-Request-Id")
		}
		result := &service.OpenAIForwardResult{
			RequestID:          requestID,
			Model:              reqModel,
			BillingModel:       d.BillingModel,
			UpstreamModel:      d.UpstreamModel,
			SearchCount:        d.SearchCount,
			ImageCount:         d.ImageCount,
			ImageSize:          d.ImageSize,
			ImageInputSize:     d.ImageInputSize,
			ImageOutputSize:    d.ImageOutputSize,
			ImageOutputSizes:   append([]string(nil), d.ImageOutputSizes...),
			ImageSizeSource:    d.ImageSizeSource,
			ImageSizeBreakdown: cloneOpenAIFailureImageSizeBreakdown(d.ImageSizeBreakdown),
			Usage: service.OpenAIUsage{
				InputTokens:              d.Usage.InputTokens,
				ImageInputTokens:         d.Usage.ImageInputTokens,
				OutputTokens:             d.Usage.OutputTokens,
				CacheReadInputTokens:     d.Usage.CacheReadInputTokens,
				CacheCreationInputTokens: d.Usage.CacheCreationInputTokens,
				ImageOutputTokens:        d.Usage.ImageOutputTokens,
			},
		}
		// Flush() 与本闭包本体都同步跑在请求 goroutine 内（由 defer 触发，
		// 尚未进入下面的 worker 池闭包），此时访问 c 仍然安全。
		upstreamEndpoint := resolveOpenAIUpstreamEndpoint(c, account, nil)
		logger.FromContext(c.Request.Context()).Warn("openai.failure_billed",
			zap.String("component", component),
			zap.String("reason", d.Reason),
			zap.String("provenance", provenance),
			zap.Int("input_tokens", d.Usage.InputTokens),
			zap.Int("output_tokens", d.Usage.OutputTokens),
			zap.Int("search_count", d.SearchCount),
			zap.Int("image_count", d.ImageCount),
		)
		h.submitOpenAIUsageRecordTask(c.Request.Context(), result, func(ctx context.Context) {
			if err := h.gatewayService.RecordUsage(ctx, &service.OpenAIRecordUsageInput{
				Result:             result,
				APIKey:             apiKey,
				User:               apiKey.User,
				Account:            account,
				Subscription:       subscription,
				PricingAt:          service.OpenAIPricingAtFromContext(c.Request.Context()),
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
				logger.FromContext(ctx).Error("openai.failure_usage_record_failed",
					zap.String("component", component),
					zap.Error(err),
				)
			}
		})
	}
}

// openAIBinaryBodyEndpointPromptTokens 是「请求体可能是 multipart / 二进制」的端点
// 专用的估算器替身：恒定返回 0，即根本不调用 EstimateFailurePromptTokens。
//
// 为什么必须整条短路而不是「估算前先判断内容是不是二进制」：EstimateFailurePromptTokens
// 的文本兜底按每个 rune 记 1 token，1 MiB 的二进制上传会估出 1,048,576 token；而这些
// 端点按张计费（BillingModeImage），根本不按 token 计价。这是多算方向，且没有任何别的
// 兜底。Task 4 已实测过字节层判据两个方向都不成立（16 位 PCM 低振幅音频每个字节都落在
// ASCII 区、是合法 UTF-8，非法字节占比 0.0000；而 CJK 请求被截断时 600 个截断点里 391 个
// 变成非法 UTF-8，误伤正是该函数存在的理由），能区分二进制与文本的只有端点本身。
//
// 用具名函数而不是就地写 func() int { return 0 }：让「这个端点刻意不估算」在 grep
// 与 code review 里是显式的，避免日后有人以为是漏接而把估算器补上去。
func openAIBinaryBodyEndpointPromptTokens() int {
	return 0
}
