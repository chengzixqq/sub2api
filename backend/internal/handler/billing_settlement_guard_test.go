package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newGuardWiringTestContext 构造一个带真实 ResponseRecorder 的 gin.Context，
// 使 c.Writer.Size() / Header() 的行为与线上一致（未写入时 Size() 返回 -1）。
func newGuardWiringTestContext() (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	return c, rec
}

func TestBillingSettlementGuard(t *testing.T) {
	t.Run("settled request does not double bill", func(t *testing.T) {
		called := 0
		g := newBillingSettlementGuard(guardDeps{
			sink: func(service.FailureBillingDecision, *service.Account) { called++ },
		})
		// 必须先 ObserveAttempt,让 account 非空,才能确认是 settled 挡住了
		// sink,而不是「从未接触上游」的账号判定顺带挡住了它。
		g.ObserveAttempt(&service.Account{ID: 7})
		g.MarkSettled()
		g.Flush()
		assert.Zero(t, called, "success path already recorded usage")
	})

	t.Run("unsettled failure settles exactly once", func(t *testing.T) {
		called := 0
		g := newBillingSettlementGuard(guardDeps{
			estimatedPromptTokens: func() int { return 900 },
			sink: func(d service.FailureBillingDecision, _ *service.Account) {
				called++
				assert.True(t, d.Billable)
				assert.Equal(t, 900, d.Usage.InputTokens)
			},
		})
		g.ObserveAttempt(&service.Account{ID: 7})
		// outputStarted=true:本用例验证的是「恰好结算一次」的幂等性,需要一个确实
		// 该计费的场景。零投递失败按语义不计费(上游零产出),用它会让断言失去意义。
		g.ObserveForwardOutcome(&service.UpstreamFailoverError{StatusCode: 504, Stage: service.GatewayFailureStageInference}, true)
		g.Flush()
		g.Flush() // 二次 Flush 必须幂等
		assert.Equal(t, 1, called)
	})

	t.Run("zero delivery failure is not billed", func(t *testing.T) {
		called := 0
		g := newBillingSettlementGuard(guardDeps{
			estimatedPromptTokens: func() int { return 900 },
			sink:                  func(service.FailureBillingDecision, *service.Account) { called++ },
		})
		g.ObserveAttempt(&service.Account{ID: 7})
		// 上游空流:502 + 零投递。Scope 为零值,三条豁免都不命中,只有
		// DecideFailureBilling 的零投递判据能挡住它,否则按估算 prompt 凭空扣费。
		g.ObserveForwardOutcome(&service.UpstreamFailoverError{
			StatusCode:             http.StatusBadGateway,
			ResponseBody:           []byte(`{"error":"empty stream response from upstream"}`),
			RetryableOnSameAccount: true,
		}, false)
		g.Flush()
		assert.Zero(t, called, "上游零产出不会向我方计费,下游也不能扣")
	})

	t.Run("credential failure is not billed", func(t *testing.T) {
		called := 0
		g := newBillingSettlementGuard(guardDeps{
			estimatedPromptTokens: func() int { return 900 },
			sink:                  func(service.FailureBillingDecision, *service.Account) { called++ },
		})
		g.ObserveAttempt(&service.Account{ID: 7})
		g.ObserveForwardOutcome(&service.UpstreamFailoverError{StatusCode: 401, Stage: service.GatewayFailureStageAccountAuth}, false)
		g.Flush()
		assert.Zero(t, called)
	})

	t.Run("no attempted account cannot be billed", func(t *testing.T) {
		called := 0
		g := newBillingSettlementGuard(guardDeps{
			estimatedPromptTokens: func() int { return 900 },
			sink:                  func(service.FailureBillingDecision, *service.Account) { called++ },
		})
		g.Flush()
		assert.Zero(t, called, "no upstream was ever contacted")
	})

	t.Run("estimator is not evaluated on the success hot path", func(t *testing.T) {
		estimatorCalls := 0
		g := newBillingSettlementGuard(guardDeps{
			estimatedPromptTokens: func() int {
				estimatorCalls++
				return 900
			},
			sink: func(service.FailureBillingDecision, *service.Account) {},
		})
		g.ObserveAttempt(&service.Account{ID: 7})
		g.MarkSettled()
		g.Flush()
		assert.Zero(t, estimatorCalls,
			"估算要扫描整个请求体（180KB body 实测 15.5ms / 12.1MB / 216106 allocs），"+
				"成功请求不该付这个成本；必须惰性到真的要计费时才求值")
	})

	t.Run("nil estimator is treated as zero prompt tokens", func(t *testing.T) {
		var got service.FailureBillingDecision
		called := 0
		g := newBillingSettlementGuard(guardDeps{
			sink: func(d service.FailureBillingDecision, _ *service.Account) {
				called++
				got = d
			},
		})
		g.ObserveAttempt(&service.Account{ID: 7})
		// outputStarted=true:本用例验证 nil 估算器不 panic 且按 0 处理,需要走到
		// 真正会计费的分支才能观察到 sink 收到的 decision。
		g.ObserveForwardOutcome(&service.UpstreamFailoverError{StatusCode: 500, Stage: service.GatewayFailureStageInference}, true)
		require.NotPanics(t, g.Flush)
		require.Equal(t, 1, called)
		assert.Zero(t, got.Usage.InputTokens)
	})

	t.Run("nil guard is a no-op", func(t *testing.T) {
		var g *billingSettlementGuard
		require.NotPanics(t, func() {
			g.ObserveAttempt(&service.Account{ID: 7})
			g.ObserveForwardOutcome(&service.UpstreamFailoverError{StatusCode: 500, Stage: service.GatewayFailureStageInference}, false)
			g.MarkSettled()
			g.Flush()
			g.Flush()
		})
	})
}

func TestBillingSettlementGuardUpstreamUsageOnly(t *testing.T) {
	t.Run("output without upstream usage is suppressed", func(t *testing.T) {
		called := 0
		g := newBillingSettlementGuard(guardDeps{
			upstreamUsageOnly:     true,
			estimatedPromptTokens: func() int { return 900 },
			sink:                  func(service.FailureBillingDecision, *service.Account) { called++ },
		})
		g.ObserveAttempt(&service.Account{ID: 7})
		g.ObserveForwardOutcome(&service.UpstreamFailoverError{StatusCode: 500, Stage: service.GatewayFailureStageInference}, true)
		g.Flush()
		assert.Zero(t, called, "request-start policy disables failed_estimated settlement")
	})

	t.Run("explicit upstream search remains billable", func(t *testing.T) {
		var got service.FailureBillingDecision
		g := newBillingSettlementGuard(guardDeps{
			upstreamUsageOnly: true,
			sink:              func(d service.FailureBillingDecision, _ *service.Account) { got = d },
		})
		g.ObserveAttempt(&service.Account{ID: 7})
		g.ObserveOpenAIForwardResult(&service.OpenAIForwardResult{SearchCount: 1})
		g.ObserveForwardOutcome(&service.UpstreamFailoverError{StatusCode: 500, Stage: service.GatewayFailureStageInference}, false)
		g.Flush()
		assert.True(t, got.Billable)
		assert.Equal(t, 1, got.SearchCount)
		assert.Equal(t, service.BillingProvenanceFailedUpstream, got.Provenance)
		assert.Equal(t, "upstream_search", got.Reason)
	})

	t.Run("client disconnect without usage is suppressed", func(t *testing.T) {
		called := 0
		g := newBillingSettlementGuard(guardDeps{
			upstreamUsageOnly: true,
			sink:              func(service.FailureBillingDecision, *service.Account) { called++ },
		})
		g.ObserveAttempt(&service.Account{ID: 7})
		g.ObserveForwardResult(&service.ForwardResult{ClientDisconnect: true})
		g.ObserveForwardOutcome(context.Canceled, false)
		g.Flush()
		assert.Zero(t, called)
	})
}

// TestBillingSettlementGuardNonFailoverError 覆盖 C-2：Forward 返回的错误不是
// *UpstreamFailoverError 时的两个方向。
//
// 修复前 ObserveFailure 只挂在 errors.As(err, &failoverErr) 分支内，这类错误使
// g.ferr 保持 nil，于是 DecideFailureBilling 里整块 `if in.Err != nil` 的不计费
// 边界被整体跳过，直接落到估算分支计费——BetaBlockedError 在构造上游请求**之前**
// 就返回，用户会为一个上游根本没收到的请求付整个 prompt 的钱。
//
// 但反过来也不能按「ferr == nil 一律不计费」一刀切，见下面的 outputStarted=true 用例。
func TestBillingSettlementGuardNonFailoverError(t *testing.T) {
	t.Run("local rejection before upstream is not billed", func(t *testing.T) {
		called := 0
		g := newBillingSettlementGuard(guardDeps{
			estimatedPromptTokens: func() int { return 120000 },
			sink:                  func(service.FailureBillingDecision, *service.Account) { called++ },
		})
		g.ObserveAttempt(&service.Account{ID: 7, Platform: service.PlatformAnthropic})
		// BetaBlockedError 在 gateway_forward.go:130 返回，此时上游请求都还没构造，
		// 更没有任何内容投递给客户端。
		g.ObserveForwardOutcome(&service.BetaBlockedError{Message: "beta not allowed"}, false)
		g.Flush()
		assert.Zero(t, called,
			"上游从未收到这个请求，不可能计费；修复前这里会按 120000 estimated prompt token 扣款")
	})

	t.Run("interrupted stream after upstream delivered tokens is billed", func(t *testing.T) {
		called := 0
		var got service.FailureBillingDecision
		g := newBillingSettlementGuard(guardDeps{
			estimatedPromptTokens: func() int { return 1000 },
			sink: func(d service.FailureBillingDecision, _ *service.Account) {
				called++
				got = d
			},
		})
		g.ObserveAttempt(&service.Account{ID: 7, Platform: service.PlatformAnthropic})
		// 对应 gateway_forward.go:846 `return nil, err`：handleStreamingResponse 返回
		// 非 sseStreamErrorEventError 的错误，即流式响应**已经开始、上游已投递 token**
		// 之后才中断。此时 ferr 为 nil，但这正是本特性最该计费的场景。
		// 这个用例是防止 C-2 的修复矫枉过正成「ferr == nil 一律不计费」的护栏——
		// 那样改会让这类请求重新变免费，正是 DecideFailureBilling 文档里
		// 明确警告「不可合并」的那条兜底。
		g.ObserveForwardOutcome(errors.New("stream interrupted"), true)
		g.Flush()
		require.Equal(t, 1, called, "上游已投递 token，必须计费")
		assert.True(t, got.Billable)
		assert.Equal(t, "interrupted_stream", got.Reason)
		assert.Equal(t, 1000, got.Usage.InputTokens)
		assert.GreaterOrEqual(t, got.Usage.OutputTokens, 1,
			"已部分投递的响应不能变成 0 output 的免费请求")
	})
}

// TestBillingSettlementGuardOutputStartedSnapshot 覆盖 C-1：outputStarted 必须在
// Forward 返回的那一刻定格，而不是等到 Flush 时才现算。
//
// 这里刻意走真实的 h.handleFailoverExhausted 去写错误响应体，而不是手工
// c.String()：多计费的成因正是「handler 的错误响应体也会让 c.Writer.Size() 增长」，
// 用真实写入路径才能挡住这个变异。时序是线上的真实时序：
//
//	Forward 失败（writer 还没被写过）
//	  → ObserveForwardOutcome 定格 outputStarted=false
//	  → handleFailoverExhausted 写 JSON 错误体，Size() 从 -1 涨到 >0
//	  → defer guard.Flush()
//
// 若 outputStarted 改成 Flush 时才求值，第 3 步的错误体会让它变成 true，
// 于是 DecideFailureBilling 的 `StatusCode == 429 && !OutputStarted` 免计费豁免
// 100% 失效，并把 completion 从 0 抬到 1——429 从未进入推理，上游不会计费。
func TestBillingSettlementGuardOutputStartedSnapshot(t *testing.T) {
	t.Run("429 before inference stays unbilled after error body is written", func(t *testing.T) {
		c, rec := newGuardWiringTestContext()
		h := &GatewayHandler{}

		called := 0
		g := newBillingSettlementGuard(guardDeps{
			estimatedPromptTokens: func() int { return 5000 },
			sink:                  func(service.FailureBillingDecision, *service.Account) { called++ },
		})
		g.ObserveAttempt(&service.Account{ID: 7, Platform: service.PlatformAnthropic})

		writerSizeBeforeForward := c.Writer.Size()
		require.Equal(t, -1, writerSizeBeforeForward, "gin 在未写入前 Size() 返回 -1")

		failoverErr := &service.UpstreamFailoverError{
			StatusCode: http.StatusTooManyRequests,
			Stage:      service.GatewayFailureStageInference,
			Scope:      service.GatewayFailureScopeAccount,
		}
		g.ObserveForwardOutcome(failoverErr, forwardDeliveredStreamContent(c))

		// 真实早退点：写出 JSON 错误响应体。
		h.handleFailoverExhausted(c, failoverErr, service.PlatformAnthropic, false)
		require.Greater(t, c.Writer.Size(), 0, "错误响应体确实写进了 writer，Size() 已增长")
		require.NotEmpty(t, rec.Body.String())

		g.Flush()

		assert.Zero(t, called,
			"429 且从未进入推理必须免计费；outputStarted 若惰性求值，"+
				"上面那次错误体写入会把它翻成 true，让这条豁免整体失效")
	})

	t.Run("delivered stream stays billed after terminal error frame is written", func(t *testing.T) {
		// 上一个子用例挡住「快照被改成惰性求值」（多算）；这一个挡住反方向的
		// 「快照根本没被写入」（少算）——把 ObserveForwardOutcome 里的
		// g.outputStarted = outputStarted 删掉，outputStarted 恒为 false，
		// 上游已经投递过 token 的流式中断会重新变成免费请求。
		c, _ := newGuardWiringTestContext()
		h := &GatewayHandler{}

		called := 0
		var got service.FailureBillingDecision
		g := newBillingSettlementGuard(guardDeps{
			estimatedPromptTokens: func() int { return 5000 },
			sink: func(d service.FailureBillingDecision, _ *service.Account) {
				called++
				got = d
			},
		})
		g.ObserveAttempt(&service.Account{ID: 7, Platform: service.PlatformAnthropic})

		// Forward 期间上游已开始投递 SSE：Content-Type 先定，再写内容帧，
		// service 层在观测到第一帧真实内容时会同步打上 GatewayUpstreamDeliveredKey
		// 标记（见 gateway_upstream_response.go 等各 leaf 的 c.Set 调用），
		// 这里手工模拟这一步，还原线上真实的信号来源。
		c.Header("Content-Type", "text/event-stream")
		_, err := c.Writer.WriteString("data: {\"type\":\"content_block_delta\"}\n\n")
		require.NoError(t, err)
		c.Set(service.GatewayUpstreamDeliveredKey, true)

		failoverErr := &service.UpstreamFailoverError{
			StatusCode: http.StatusTooManyRequests,
			Stage:      service.GatewayFailureStageInference,
			Scope:      service.GatewayFailureScopeAccount,
		}
		delivered := forwardDeliveredStreamContent(c)
		require.True(t, delivered, "service 层已打上投递标记，判据必须认定为已投递")
		g.ObserveForwardOutcome(failoverErr, delivered)

		// 真实早退点：流已开始，错误只能以 SSE 终止帧就地回传。
		h.handleFailoverExhausted(c, failoverErr, service.PlatformAnthropic, true)

		g.Flush()

		require.Equal(t, 1, called,
			"上游已投递 token，即便状态码是 429 也必须计费；"+
				"若 ObserveForwardOutcome 没把 outputStarted 写进快照，"+
				"429 免计费豁免会错误地吃掉这次真实消耗")
		assert.True(t, got.Billable)
		assert.Equal(t, "interrupted_stream", got.Reason)
		assert.Equal(t, 5000, got.Usage.InputTokens)
		assert.GreaterOrEqual(t, got.Usage.OutputTokens, 1)
	})
}

// TestForwardDeliveredStreamContent 覆盖 forwardDeliveredStreamContent 判据本身。
//
// 判据不能是裸的字节启发式（`c.Writer.Size() != writerSizeBeforeForward`，本轮修复
// 之前的实现）：Forward 内部有一批早退路径会自己写 JSON 错误体再返回错误
// （网络层失败 gateway_forward.go:399 的 c.JSON、Gemini 的 writeGoogleError /
// writeClaudeError 等本地校验拒绝），sub2api 自己在流式读循环挂死时也会写 SSE 帧
// （keepalive ping、sendErrorEvent 写的 stream_timeout / stream_read_error /
// response_too_large 等错误事件帧，见 gateway_upstream_response.go）——字节数量
// 区分不了这些「本地/自造帧」与「上游真实投递的帧」，裸判据会把它们都当成
// 「上游已投递内容」，对零投递的挂死请求多计一次整段估算 prompt 的费用。
//
// 现在的判据只读 service.GatewayUpstreamDeliveredKey 这个 gin.Context 标记，
// 该标记只由各 streaming leaf 在观测到上游第一帧真实内容时才会置位（见
// gateway_upstream_response.go:1046 等处的 c.Set 调用），因此本测试是纯粹针对
// 这个标记读取行为的表驱动测试，不再需要模拟任何字节写入。
func TestForwardDeliveredStreamContent(t *testing.T) {
	tests := []struct {
		name   string
		setup  func(c *gin.Context)
		useNil bool
		want   bool
	}{
		{
			name:  "flag never set is not delivered",
			setup: func(*gin.Context) {},
			want:  false,
		},
		{
			name: "flag explicitly set true is delivered",
			setup: func(c *gin.Context) {
				c.Set(service.GatewayUpstreamDeliveredKey, true)
			},
			want: true,
		},
		{
			name: "flag explicitly set false is not delivered",
			setup: func(c *gin.Context) {
				c.Set(service.GatewayUpstreamDeliveredKey, false)
			},
			want: false,
		},
		{
			name:   "nil context is not delivered and does not panic",
			useNil: true,
			want:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var c *gin.Context
			if !tt.useNil {
				c, _ = newGuardWiringTestContext()
				tt.setup(c)
			}

			var got bool
			require.NotPanics(t, func() {
				got = forwardDeliveredStreamContent(c)
			})
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestBillingSettlementGuardHungStreamWithoutDeliveryFlagIsNotBilled 是本轮修复
// 关闭 Critical over-billing bug 的关闭证明：sub2api 自己在流式读循环挂死时写的
// keepalive ping / stream_timeout 等错误事件帧本身也是合法的 SSE 帧，如果判据只看
// 「writer 是否增长 + Content-Type 是不是 text/event-stream」，会被误判成「上游已经
// 投递过真实内容」，进而对一个上游一个 token 都没吐、纯粹是本地自造帧的挂死请求，
// 按整段估算 prompt 计费。
//
// 现在的判据只认 service.GatewayUpstreamDeliveredKey 标记，这个标记只由各 streaming
// leaf 在观测到上游真实内容时才会置位——本测试不调用任何 leaf，只模拟「sub2api 自己
// 写过 SSE 帧，但从未收到上游真实内容」的挂死场景，因此 sink 永远不应该被调用。
// 这对应 Flush() 里 `if g.ferr == nil && !g.outputStarted { return }` 这条早退：
// 一个普通 error（不是 *UpstreamFailoverError，errors.As 匹配不上，ferr 停留在 nil）
// 加上 outputStarted=false，必须在 DecideFailureBilling 被调用之前就短路掉，不能
// 落到估算分支计费。
//
// 如果 forwardDeliveredStreamContent 被改回旧的字节启发式，本测试里手写的 SSE 帧
// 会被误判为「已投递」，outputStarted 变成 true，Flush 会绕过早退直接调用 sink——
// 本测试必须失败。
func TestBillingSettlementGuardHungStreamWithoutDeliveryFlagIsNotBilled(t *testing.T) {
	c, _ := newGuardWiringTestContext()

	called := 0
	g := newBillingSettlementGuard(guardDeps{
		estimatedPromptTokens: func() int { return 120000 },
		sink:                  func(service.FailureBillingDecision, *service.Account) { called++ },
	})
	g.ObserveAttempt(&service.Account{ID: 7, Platform: service.PlatformAnthropic})

	// 模拟一次完全挂死的上游请求：sub2api 自己写了 SSE 错误事件帧（镜像
	// sendErrorEvent 写的 stream_timeout 帧，或 keepalive ping），但 service 层的
	// 读循环从未观测到上游的任何真实内容，因此从未
	// c.Set(service.GatewayUpstreamDeliveredKey, true)。
	c.Header("Content-Type", "text/event-stream")
	_, err := c.Writer.WriteString("event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"stream_timeout\"}}\n\n")
	require.NoError(t, err)

	// Forward 返回一个普通 error，不是 *UpstreamFailoverError——镜像挂死超时
	// 直接把读循环错误网上抛、不经过 UpstreamFailoverError 包装的路径。
	g.ObserveForwardOutcome(errors.New("stream data interval timeout"), forwardDeliveredStreamContent(c))
	g.Flush()

	assert.Zero(t, called,
		"上游一个 token 都没有真实投递，即便 sub2api 自己写过 SSE 帧（keepalive/"+
			"错误事件），也绝不能按估算 prompt token 计费——这正是本轮修复要关闭的 "+
			"Critical over-billing bug")
}

func TestBillingSettlementGuardPartialUsage(t *testing.T) {
	t.Run("retry resets request-scoped delivery marker", func(t *testing.T) {
		c, _ := newGuardWiringTestContext()
		g := newBillingSettlementGuard(guardDeps{
			resetAttemptOutput: billingAttemptOutputReset(c),
		})

		c.Set(service.GatewayUpstreamDeliveredKey, true)
		g.ObserveAttempt(&service.Account{ID: 2})

		assert.False(t, forwardDeliveredStreamContent(c))
	})

	t.Run("final attempt real usage wins over estimate", func(t *testing.T) {
		var got service.FailureBillingDecision
		g := newBillingSettlementGuard(guardDeps{
			estimatedPromptTokens: func() int { return 9999 },
			sink:                  func(d service.FailureBillingDecision, _ *service.Account) { got = d },
		})
		g.ObserveAttempt(&service.Account{ID: 1})
		g.ObserveForwardOutcome(&service.UpstreamFailoverError{StatusCode: 500}, false)
		g.ObservePartialUsage(service.ClaudeUsage{
			InputTokens: 17, OutputTokens: 3, CacheReadInputTokens: 5,
		})
		g.Flush()

		require.True(t, got.Billable)
		assert.Equal(t, service.BillingProvenanceFailedUpstream, got.Provenance)
		assert.Equal(t, 17, got.Usage.InputTokens)
		assert.Equal(t, 3, got.Usage.OutputTokens)
		assert.Equal(t, 5, got.Usage.CacheReadInputTokens)
	})

	t.Run("retry resets prior attempt outcome and usage", func(t *testing.T) {
		called := 0
		g := newBillingSettlementGuard(guardDeps{
			estimatedPromptTokens: func() int { return 9999 },
			sink:                  func(service.FailureBillingDecision, *service.Account) { called++ },
		})
		g.ObserveAttempt(&service.Account{ID: 1})
		g.ObserveForwardOutcome(&service.UpstreamFailoverError{StatusCode: 500}, true)
		g.ObservePartialUsage(service.ClaudeUsage{InputTokens: 17, OutputTokens: 3})

		g.ObserveAttempt(&service.Account{ID: 2})
		g.ObserveForwardOutcome(errors.New("last attempt failed before output"), false)
		g.Flush()

		assert.Zero(t, called, "prior retry usage must not leak into final settlement")
	})

	t.Run("later non failover error clears stale failover classification", func(t *testing.T) {
		var got service.FailureBillingDecision
		g := newBillingSettlementGuard(guardDeps{
			estimatedPromptTokens: func() int { return 21 },
			sink:                  func(d service.FailureBillingDecision, _ *service.Account) { got = d },
		})
		g.ObserveAttempt(&service.Account{ID: 1})
		g.ObserveForwardOutcome(&service.UpstreamFailoverError{
			StatusCode: 401, Stage: service.GatewayFailureStageAccountAuth,
		}, false)
		g.ObserveForwardOutcome(errors.New("stream interrupted"), true)
		g.Flush()

		require.True(t, got.Billable)
		assert.Equal(t, service.BillingProvenanceFailedEstimated, got.Provenance)
	})

	t.Run("success suppresses staged failure usage", func(t *testing.T) {
		called := 0
		g := newBillingSettlementGuard(guardDeps{
			sink: func(service.FailureBillingDecision, *service.Account) { called++ },
		})
		g.ObserveAttempt(&service.Account{ID: 1})
		g.ObservePartialUsage(service.ClaudeUsage{InputTokens: 17})
		g.MarkSettled()
		g.Flush()
		assert.Zero(t, called)
	})

	t.Run("openai image-only usage is preserved", func(t *testing.T) {
		var got service.FailureBillingDecision
		g := newBillingSettlementGuard(guardDeps{
			sink: func(d service.FailureBillingDecision, _ *service.Account) { got = d },
		})
		g.ObserveAttempt(&service.Account{ID: 1})
		g.ObserveForwardOutcome(&service.UpstreamFailoverError{StatusCode: 500}, false)
		g.ObserveOpenAIForwardResult(&service.OpenAIForwardResult{Usage: service.OpenAIUsage{
			ImageInputTokens:  6,
			ImageOutputTokens: 7,
		}})
		g.Flush()

		require.True(t, got.Billable)
		assert.Equal(t, service.BillingProvenanceFailedUpstream, got.Provenance)
		assert.Equal(t, 6, got.Usage.ImageInputTokens)
		assert.Equal(t, 7, got.Usage.ImageOutputTokens)
	})

	t.Run("partial image error keeps media and cumulative search", func(t *testing.T) {
		var got service.FailureBillingDecision
		g := newBillingSettlementGuard(guardDeps{
			sink: func(d service.FailureBillingDecision, _ *service.Account) { got = d },
		})
		g.ObserveAttempt(&service.Account{ID: 1})
		g.ObserveOpenAIForwardResult(&service.OpenAIForwardResult{SearchCount: 2})
		g.ObserveForwardOutcome(&service.UpstreamFailoverError{StatusCode: 500}, false)
		g.ObserveAttempt(&service.Account{ID: 2})
		g.ObserveOpenAIForwardResult(&service.OpenAIForwardResult{
			RequestID:        "upstream-final",
			BillingModel:     "gpt-image-2",
			UpstreamModel:    "gpt-image-2",
			SearchCount:      1,
			ImageCount:       1,
			ImageSize:        "2K",
			ImageOutputSizes: []string{"2048x2048"},
		})
		g.ObserveForwardOutcome(&service.UpstreamFailoverError{StatusCode: 502}, false)
		g.Flush()

		require.True(t, got.Billable)
		assert.Equal(t, service.BillingProvenanceFailedUpstream, got.Provenance)
		assert.Equal(t, "upstream-final", got.RequestID)
		assert.Equal(t, "gpt-image-2", got.BillingModel)
		assert.Equal(t, 3, got.SearchCount)
		assert.Equal(t, 1, got.ImageCount)
		assert.Equal(t, "2K", got.ImageSize)
		assert.Equal(t, []string{"2048x2048"}, got.ImageOutputSizes)
	})
}

func TestBillingSettlementGuardSearchAcrossAttempts(t *testing.T) {
	t.Run("same attempt observation is idempotent", func(t *testing.T) {
		g := newBillingSettlementGuard(guardDeps{})
		g.ObserveAttempt(&service.Account{ID: 1})
		first := g.ObserveOpenAIForwardResult(&service.OpenAIForwardResult{SearchCount: 2})
		duplicate := g.ObserveOpenAIForwardResult(&service.OpenAIForwardResult{SearchCount: 2})
		increased := g.ObserveOpenAIForwardResult(&service.OpenAIForwardResult{SearchCount: 3})

		assert.Equal(t, 2, first.CumulativeSearchCount)
		assert.Equal(t, 2, duplicate.CumulativeSearchCount)
		assert.Equal(t, 3, increased.CumulativeSearchCount)
	})

	t.Run("retries accumulate search but retain final usage and account", func(t *testing.T) {
		var got service.FailureBillingDecision
		var gotAccountID int64
		g := newBillingSettlementGuard(guardDeps{
			sink: func(d service.FailureBillingDecision, account *service.Account) {
				got = d
				gotAccountID = account.ID
			},
		})

		g.ObserveAttempt(&service.Account{ID: 11})
		g.ObserveOpenAIForwardResult(&service.OpenAIForwardResult{
			SearchCount: 1,
			Usage:       service.OpenAIUsage{InputTokens: 100},
		})
		g.ObserveForwardOutcome(&service.UpstreamFailoverError{StatusCode: 500}, false)

		g.ObserveAttempt(&service.Account{ID: 22})
		g.ObserveOpenAIForwardResult(&service.OpenAIForwardResult{
			SearchCount: 2,
			Usage:       service.OpenAIUsage{InputTokens: 7, OutputTokens: 3},
		})
		g.ObserveForwardOutcome(&service.UpstreamFailoverError{StatusCode: 500}, false)
		g.Flush()

		require.True(t, got.Billable)
		assert.Equal(t, 3, got.SearchCount)
		assert.Equal(t, 7, got.Usage.InputTokens)
		assert.Equal(t, 3, got.Usage.OutputTokens)
		assert.Equal(t, int64(22), gotAccountID)
		assert.Equal(t, service.BillingProvenanceFailedUpstream, got.Provenance)
	})

	t.Run("search only final failure is billable once", func(t *testing.T) {
		calls := 0
		var got service.FailureBillingDecision
		g := newBillingSettlementGuard(guardDeps{
			sink: func(d service.FailureBillingDecision, _ *service.Account) {
				calls++
				got = d
			},
		})
		g.ObserveAttempt(&service.Account{ID: 1})
		g.ObserveOpenAIForwardResult(&service.OpenAIForwardResult{SearchCount: 4})
		g.ObserveForwardOutcome(errors.New("stream failed"), false)
		g.Flush()
		g.Flush()

		assert.Equal(t, 1, calls)
		assert.Equal(t, 4, got.SearchCount)
		assert.Equal(t, service.BillingProvenanceFailedUpstream, got.Provenance)
	})

	t.Run("negative clamps and overflow saturates", func(t *testing.T) {
		g := newBillingSettlementGuard(guardDeps{})
		g.ObserveAttempt(&service.Account{ID: 1})
		negative := g.ObserveOpenAIForwardResult(&service.OpenAIForwardResult{SearchCount: -3})
		assert.Zero(t, negative.CumulativeSearchCount)

		maxInt := int(^uint(0) >> 1)
		g.ObserveOpenAIForwardResult(&service.OpenAIForwardResult{SearchCount: maxInt})
		g.ObserveAttempt(&service.Account{ID: 2})
		saturated := g.ObserveOpenAIForwardResult(&service.OpenAIForwardResult{SearchCount: 1})
		assert.Equal(t, maxInt, saturated.CumulativeSearchCount)
	})
}
