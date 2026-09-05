package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 本文件覆盖 Task 7a 在 OpenAI/Grok 家族埋下的「上游已投递」标记。
//
// 选点理由：11 个叶子共两类结构，各挑一个代表即可咬住回归——
//   - SSE 类（handleStreamingResponse，openai_gateway_response_handling.go）：11 个叶子里唯一
//     使用「累积到帧边界再一次性 dispatch」结构的一类，也是唯一需要区分「真实内容帧」与
//     「response.failed 错误帧」的一类，误判方向是多算，风险最高。
//   - WS 类（proxyOpenAIWSHTTPBridgeTurn，openai_ws_http_bridge.go）：WS 是本轮唯一有
//     happens-before 疑虑的一类，必须坐实标记确实写回了 handler 持有的同一个 *gin.Context。
//
// 三个叶子共用 firstTokenMs / isOpenAIWSTokenEvent 门控，行为同构，不再重复铺测试。
// 所有断言都驱动真实生产函数，不手工 c.Set 自导自演——否则删掉埋点行不会让任何测试失败。

// newOpenAIUpstreamDeliveredSSEContext 复用本包既有的 flush recorder 夹具构造真实 *gin.Context。
func newOpenAIUpstreamDeliveredSSEContext() (*gin.Context, *openAIResponseFlushRecorder) {
	gin.SetMode(gin.TestMode)
	recorder := newOpenAIResponseFlushRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	return c, recorder
}

func newOpenAIUpstreamDeliveredService() *OpenAIGatewayService {
	return &OpenAIGatewayService{
		cfg:           &config.Config{Gateway: config.GatewayConfig{}},
		toolCorrector: NewCodexToolCorrector(),
	}
}

// runOpenAIUpstreamDeliveredStream 驱动 handleStreamingResponse。
// guardFirstOutput 决定走哪条埋点分支：handleStreamingResponse 内有两处标记，
// 由 firstOutputTimeout 是否 >0 分流（见 guardFirstOutput）——
//   - false：直写分支，标记在 SSE 行处理处（response_handling.go:555 附近）
//   - true ：pre-output failover 暂存分支，标记在帧 dispatch 处（:266 附近）
//
// 两条分支必须各自被测到，否则删掉其中一处埋点另一处仍能让测试通过（已实测到该漏网）。
func runOpenAIUpstreamDeliveredStream(c *gin.Context, sse string, guardFirstOutput bool) (*openaiStreamingResult, error) {
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(sse)),
	}
	svc := newOpenAIUpstreamDeliveredService()
	if guardFirstOutput {
		svc.cfg.Gateway.OpenAIFirstOutputTimeoutSeconds = 60
	}
	return svc.handleStreamingResponse(
		context.Background(), resp, c, &Account{ID: 1, Platform: PlatformOpenAI},
		time.Now(), "gpt-5", "gpt-5",
	)
}

// TestOpenAIStreamingResponse_MarksUpstreamDeliveredOnRealContent 正向证明：上游真实投递了
// output_text.delta 文本后，handleStreamingResponse 必须在真实 *gin.Context 上打标记，供
// handler 层 forwardDeliveredStreamContent 判定计费。漏掉这一行 = 已消耗上游 token 的中断
// 请求被错误免单（少算）。
func TestOpenAIStreamingResponse_MarksUpstreamDeliveredOnRealContent(t *testing.T) {
	for _, guard := range []bool{false, true} {
		t.Run(openAIDeliveredBranchName(guard), func(t *testing.T) {
			c, _ := newOpenAIUpstreamDeliveredSSEContext()
			require.False(t, c.GetBool(GatewayUpstreamDeliveredKey), "读循环开始前不应预置投递标记")

			_, err := runOpenAIUpstreamDeliveredStream(c,
				"event: response.output_text.delta\n"+
					`data: {"type":"response.output_text.delta","delta":"ok"}`+"\n\n"+
					"event: response.completed\n"+
					`data: {"type":"response.completed","response":{"id":"resp_1","usage":{"input_tokens":7,"output_tokens":5}}}`+"\n\n",
				guard,
			)

			require.NoError(t, err)
			require.True(t, c.GetBool(GatewayUpstreamDeliveredKey),
				"上游已真实投递文本内容（response.output_text.delta delta=\"ok\"），"+
					"handleStreamingResponse 必须 c.Set(GatewayUpstreamDeliveredKey, true)；"+
					"漏掉会让 handler 层永远判定为「未投递」，把已消耗上游 token 的中断请求错误免单")
		})
	}
}

// openAIDeliveredBranchName 给两条埋点分支起可读的子测试名。
func openAIDeliveredBranchName(guard bool) string {
	if guard {
		return "guarded_staging_branch"
	}
	return "direct_write_branch"
}

// TestOpenAIStreamingResponse_EmptyStreamDoesNotMarkUpstreamDelivered 反向锚点：上游直接 EOF，
// 一帧真实内容都没有。若有人为让正向测试通过而把 c.Set 提到条件块外无条件执行，本测试会 FAIL。
// 这条咬的是多算方向——比少算更严重。
func TestOpenAIStreamingResponse_EmptyStreamDoesNotMarkUpstreamDelivered(t *testing.T) {
	for _, guard := range []bool{false, true} {
		t.Run(openAIDeliveredBranchName(guard), func(t *testing.T) {
			c, _ := newOpenAIUpstreamDeliveredSSEContext()

			_, _ = runOpenAIUpstreamDeliveredStream(c, "", guard)

			require.False(t, c.GetBool(GatewayUpstreamDeliveredKey),
				"上游没有投递任何内容帧，标记必须保持 false；否则零投递请求会被计费 = 多算")
		})
	}
}

func TestOpenAIStreamingResponse_EmptyDeltaDoesNotMarkUpstreamDelivered(t *testing.T) {
	for _, guard := range []bool{false, true} {
		t.Run(openAIDeliveredBranchName(guard), func(t *testing.T) {
			c, _ := newOpenAIUpstreamDeliveredSSEContext()

			_, _ = runOpenAIUpstreamDeliveredStream(c,
				"event: response.output_text.delta\n"+
					`data: {"type":"response.output_text.delta","delta":""}`+"\n\n"+
					"event: response.failed\n"+
					`data: {"type":"response.failed","response":{"status":"failed","error":{"code":"content_policy_violation"}}}`+"\n\n",
				guard,
			)

			require.False(t, c.GetBool(GatewayUpstreamDeliveredKey),
				"empty delta followed by failure must not count as delivered content")
		})
	}
}

// TestOpenAIStreamingResponse_FailedEventOnlyDoesNotMarkUpstreamDelivered 是本轮发现并关闭的
// 多算陷阱的专项锚点：上游只回了一个 response.failed 错误帧。该帧会让既有的
// lineStartsClientOutput / startsClientOutput 门控为真（forceFlushFailedEvent 分支），但它
// 不含任何推理内容。若把标记挂在那个更宽的门控上，零投递的失败请求会被计费 = 多算。
func TestOpenAIStreamingResponse_FailedEventOnlyDoesNotMarkUpstreamDelivered(t *testing.T) {
	for _, guard := range []bool{false, true} {
		t.Run(openAIDeliveredBranchName(guard), func(t *testing.T) {
			c, _ := newOpenAIUpstreamDeliveredSSEContext()

			// 必须用「不可重试」的 failed（invalid_request/content_policy 一类）：
			// 可重试的 failed 会走 openAIStreamFailedEventShouldFailover 提前 return，
			// 根本到不了帧 dispatch 处，那样这条反向锚点就咬不到宽门控的多算变异。
			_, _ = runOpenAIUpstreamDeliveredStream(c,
				"event: response.failed\n"+
					`data: {"type":"response.failed","response":{"id":"resp_2","status":"failed",`+
					`"error":{"code":"invalid_request_error","message":"content_policy violation"}}}`+"\n\n",
				guard,
			)

			require.False(t, c.GetBool(GatewayUpstreamDeliveredKey),
				"上游只回了 response.failed 错误帧、没有任何推理内容，标记必须保持 false；"+
					"若挂在 forceFlushFailedEvent 也能触达的宽门控上，会把零投递失败请求计费 = 多算")
		})
	}
}

// runOpenAIWSBridgeDeliveredTurn 复用 openai_ws_http_bridge_test.go 既有的 httpUpstreamRecorder
// 夹具驱动真实的 proxyOpenAIWSHTTPBridgeTurn，返回 handler 侧持有的同一个 *gin.Context。
func runOpenAIWSBridgeDeliveredTurn(sse string) *gin.Context {
	gin.SetMode(gin.TestMode)
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(sse)),
	}}
	svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
	account := &Account{ID: 11, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Concurrency: 1}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	payload := []byte(`{"type":"response.create","model":"gpt-5","input":"hi"}`)

	_, _ = svc.proxyOpenAIWSHTTPBridgeTurn(
		context.Background(), c, account, "sk-test", payload, len(payload),
		"gpt-5", "", "", "", "", 2,
		func([]byte) error { return nil },
	)
	return c
}

// TestOpenAIWSHTTPBridge_MarksUpstreamDeliveredOnTokenEvent 是 WS 类的正向证明，同时坐实
// happens-before：标记由 proxyOpenAIWSHTTPBridgeTurn 内的 SSE 读循环写入，且确实写回了
// handler 传进来的同一个 *gin.Context（无中间 goroutine 逃逸），调用返回后可被读到。
func TestOpenAIWSHTTPBridge_MarksUpstreamDeliveredOnTokenEvent(t *testing.T) {
	c := runOpenAIWSBridgeDeliveredTurn(
		`data: {"type":"response.output_text.delta","delta":"ok"}` + "\n\n" +
			`data: {"type":"response.completed","response":{"id":"resp_ws","usage":{"input_tokens":6,"output_tokens":3}}}` + "\n\n",
	)

	require.True(t, c.GetBool(GatewayUpstreamDeliveredKey),
		"上游已真实投递 token 事件（response.output_text.delta），proxyOpenAIWSHTTPBridgeTurn 必须在 "+
			"isOpenAIWSTokenEvent 命中处 c.Set(GatewayUpstreamDeliveredKey, true)，且必须写在 handler "+
			"持有的同一个 *gin.Context 上；否则 WS 桥接的中断请求会被错误免单")
}

// ===========================================================================
// Task 7a-fix：把 5 个偏宽门控收窄成真实内容帧白名单后补的正反向锚点。
//
// 每个叶子都驱动真实生产函数，反向锚点覆盖 response.failed 与裸 error 两种错误帧形状。
// 夹具纪律：response.failed 必须用「不可重试」的错误（content_policy 一类）——可重试的
// failed 会被 openAIStreamFailedEventShouldFailover 提前 failover return，控制流到不了
// 帧 dispatch，反向锚点会假绿（Task 7a 已踩过一次）。
// ===========================================================================

// openAIDeliveredNonRetryableFailedSSE 是不可重试的 response.failed 夹具。
func openAIDeliveredNonRetryableFailedSSE() string {
	return `data: {"type":"response.failed","response":{"id":"resp_failed","status":"failed",` +
		`"error":{"code":"invalid_request_error","message":"content_policy violation"}}}` + "\n\n"
}

// openAIDeliveredBareErrorSSE 是裸 error 帧夹具（type:"error"，不是 response.failed）。
// 黑名单门控正是漏在这一形状上。
func openAIDeliveredBareErrorSSE() string {
	return `data: {"type":"error","error":{"type":"invalid_request_error",` +
		`"code":"content_policy_violation","message":"content_policy violation"}}` + "\n\n"
}

// openAIDeliveredErrorOnlyFixtures 给两种错误帧形状起可读子测试名。
func openAIDeliveredErrorOnlyFixtures() map[string]string {
	return map[string]string{
		"response_failed_only": openAIDeliveredNonRetryableFailedSSE(),
		"bare_error_only":      openAIDeliveredBareErrorSSE(),
	}
}

// runOpenAIDeliveredChatStream 驱动 handleChatStreamingResponse（Responses→CC 转换叶子）。
func runOpenAIDeliveredChatStream(sse string) *gin.Context {
	c, _ := newOpenAIUpstreamDeliveredSSEContext()
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(sse)),
	}
	svc := &OpenAIGatewayService{cfg: &config.Config{}}
	_, _ = svc.handleChatStreamingResponse(
		resp, c, &Account{ID: 21, Platform: PlatformOpenAI},
		"gpt-5", "gpt-5", "gpt-5", time.Now(), 0,
	)
	return c
}

func TestOpenAIChatStream_MarksUpstreamDeliveredOnRealContent(t *testing.T) {
	c := runOpenAIDeliveredChatStream(
		`data: {"type":"response.output_text.delta","delta":"ok"}` + "\n\n" +
			`data: {"type":"response.completed","response":{"id":"resp_cc","usage":{"input_tokens":5,"output_tokens":2}}}` + "\n\n",
	)

	require.True(t, c.GetBool(GatewayUpstreamDeliveredKey),
		"上游已投递 response.output_text.delta 真实内容，handleChatStreamingResponse 必须置位投递标记；"+
			"漏掉会把已消耗上游 token 的中断请求错误免单（少算）")
}

func TestOpenAIChatStream_ErrorFrameOnlyDoesNotMarkUpstreamDelivered(t *testing.T) {
	for name, sse := range openAIDeliveredErrorOnlyFixtures() {
		t.Run(name, func(t *testing.T) {
			c := runOpenAIDeliveredChatStream(sse)

			require.False(t, c.GetBool(GatewayUpstreamDeliveredKey),
				"上游零投递、只回错误帧，投递标记必须保持 false；原门控是裸 firstChunk 判断，"+
					"会把这种请求标记为已投递并全额估算 prompt 入账 = 多算")
		})
	}
}

func TestOpenAIChatStream_PreambleOnlyDoesNotMarkUpstreamDelivered(t *testing.T) {
	c := runOpenAIDeliveredChatStream(
		`data: {"type":"response.created","response":{"id":"resp_pre"}}` + "\n\n" +
			`data: {"type":"response.in_progress","response":{"id":"resp_pre"}}` + "\n\n",
	)

	require.False(t, c.GetBool(GatewayUpstreamDeliveredKey),
		"上游只回前导帧（response.created / in_progress）、未产出任何内容，标记必须保持 false")
}

// runOpenAIDeliveredAnthropicStream 驱动 handleAnthropicStreamingResponse（Responses→Anthropic 叶子）。
func runOpenAIDeliveredAnthropicStream(sse string) *gin.Context {
	c, _ := newOpenAIUpstreamDeliveredSSEContext()
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(sse)),
	}
	svc := &OpenAIGatewayService{cfg: &config.Config{}}
	_, _ = svc.handleAnthropicStreamingResponse(
		resp, c, &Account{ID: 22, Platform: PlatformOpenAI},
		"gpt-5", "gpt-5", "gpt-5", time.Now(),
	)
	return c
}

func TestOpenAIAnthropicStream_MarksUpstreamDeliveredOnRealContent(t *testing.T) {
	c := runOpenAIDeliveredAnthropicStream(
		`data: {"type":"response.output_text.delta","delta":"ok"}` + "\n\n" +
			`data: {"type":"response.completed","response":{"id":"resp_msg","usage":{"input_tokens":5,"output_tokens":2}}}` + "\n\n",
	)

	require.True(t, c.GetBool(GatewayUpstreamDeliveredKey),
		"上游已投递真实内容，handleAnthropicStreamingResponse 必须置位投递标记，否则中断请求被错误免单")
}

func TestOpenAIAnthropicStream_ErrorFrameOnlyDoesNotMarkUpstreamDelivered(t *testing.T) {
	for name, sse := range openAIDeliveredErrorOnlyFixtures() {
		t.Run(name, func(t *testing.T) {
			c := runOpenAIDeliveredAnthropicStream(sse)

			require.False(t, c.GetBool(GatewayUpstreamDeliveredKey),
				"上游零投递、只回错误帧，投递标记必须保持 false；原门控是裸 firstChunk 判断 = 多算")
		})
	}
}

// runOpenAIDeliveredRawChatStream 驱动 streamRawChatCompletions（原生 CC chunk 直转叶子）。
func runOpenAIDeliveredRawChatStream(sse string) *gin.Context {
	c, _ := newOpenAIUpstreamDeliveredSSEContext()
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(sse)),
	}
	// 不复用 openai_gateway_chat_completions_raw_test.go 的夹具：那个文件带 //go:build unit，
	// 本文件按 brief 要求无 build tag，跨 tag 引用会导致默认构建失败。
	svc := &OpenAIGatewayService{cfg: &config.Config{}}
	account := &Account{
		ID:          23,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{"api_key": "sk-test"},
	}
	_, _ = svc.streamRawChatCompletions(
		c, resp, account,
		"gpt-5.5", "gpt-5.5", "gpt-5.5", nil, nil, time.Now(), 0,
	)
	return c
}

func TestOpenAIRawChatStream_MarksUpstreamDeliveredOnRealContent(t *testing.T) {
	c := runOpenAIDeliveredRawChatStream(
		`data: {"id":"c1","choices":[{"index":0,"delta":{"role":"assistant","content":"ok"}}]}` + "\n\n" +
			"data: [DONE]\n\n",
	)

	require.True(t, c.GetBool(GatewayUpstreamDeliveredKey),
		"上游已投递 choices[].delta.content，streamRawChatCompletions 必须置位投递标记")
}

func TestOpenAIRawChatStream_ErrorChunkOnlyDoesNotMarkUpstreamDelivered(t *testing.T) {
	cases := map[string]string{
		// 原生 CC 形状的上游错误：顶层 error 对象。
		"top_level_error": `data: {"error":{"type":"invalid_request_error","code":"content_policy_violation",` +
			`"message":"content_policy violation"}}` + "\n\n" + "data: [DONE]\n\n",
		// 骨架 chunk：只有 role / finish_reason，上游一个 token 都没产出。
		"role_and_finish_only": `data: {"id":"c1","choices":[{"index":0,"delta":{"role":"assistant"}}]}` + "\n\n" +
			`data: {"id":"c1","choices":[{"index":0,"delta":{},"finish_reason":"content_filter"}]}` + "\n\n" +
			"data: [DONE]\n\n",
	}
	for name, sse := range cases {
		t.Run(name, func(t *testing.T) {
			c := runOpenAIDeliveredRawChatStream(sse)

			require.False(t, c.GetBool(GatewayUpstreamDeliveredKey),
				"上游没有产出任何 delta 内容，投递标记必须保持 false；原门控只排除 usage-only 尾块，"+
					"错误帧与骨架 chunk 都会置位 = 多算")
		})
	}
}

// runOpenAIDeliveredImagesStream 驱动 handleOpenAIImagesStreamingResponse（API Key 图片叶子）。
func runOpenAIDeliveredImagesStream(sse string) *gin.Context {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(sse)),
	}
	svc := &OpenAIGatewayService{cfg: &config.Config{}}
	_, _, _, _, _ = svc.handleOpenAIImagesStreamingResponse(resp, c, time.Now())
	return c
}

func TestOpenAIImagesStream_MarksUpstreamDeliveredOnImageOutput(t *testing.T) {
	c := runOpenAIDeliveredImagesStream(
		`data: {"type":"image_generation.completed","b64_json":"ZmluYWw=","output_format":"png"}` + "\n\n" +
			"data: [DONE]\n\n",
	)

	require.True(t, c.GetBool(GatewayUpstreamDeliveredKey),
		"上游已投递真实图片内容（image_generation.completed 带 b64_json），必须置位投递标记")
}

func TestOpenAIImagesStream_ErrorFrameOnlyDoesNotMarkUpstreamDelivered(t *testing.T) {
	for name, sse := range openAIDeliveredErrorOnlyFixtures() {
		t.Run(name, func(t *testing.T) {
			c := runOpenAIDeliveredImagesStream(sse + "data: [DONE]\n\n")

			require.False(t, c.GetBool(GatewayUpstreamDeliveredKey),
				"上游零图片产出、只回错误帧，投递标记必须保持 false；原门控对任何非空行置位 = 多算")
		})
	}
}

// runOpenAIDeliveredImagesOAuthStream 驱动 handleOpenAIImagesOAuthStreamingResponse
//
//	（OAuth Responses 形状图片叶子）。
func runOpenAIDeliveredImagesOAuthStream(sse string) *gin.Context {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(sse)),
	}
	svc := &OpenAIGatewayService{cfg: &config.Config{}}
	_, _, _, _, _ = svc.handleOpenAIImagesOAuthStreamingResponse(
		resp, c, time.Now(), "b64_json", "image_generation", "gpt-image-1",
	)
	return c
}

func TestOpenAIImagesOAuthStream_MarksUpstreamDeliveredOnImageOutput(t *testing.T) {
	c := runOpenAIDeliveredImagesOAuthStream(
		`data: {"type":"response.image_generation_call.partial_image","partial_image_b64":"cGFydGlhbA==","output_format":"png"}` + "\n\n",
	)

	require.True(t, c.GetBool(GatewayUpstreamDeliveredKey),
		"上游已投递真实图片字节（partial_image），必须置位投递标记，否则中断的图片请求被错误免单")
}

func TestOpenAIImagesOAuthStream_ErrorFrameOnlyDoesNotMarkUpstreamDelivered(t *testing.T) {
	for name, sse := range openAIDeliveredErrorOnlyFixtures() {
		t.Run(name, func(t *testing.T) {
			c := runOpenAIDeliveredImagesOAuthStream(sse)

			require.False(t, c.GetBool(GatewayUpstreamDeliveredKey),
				"上游零图片产出、只回错误帧（正是 case \"error\", \"response.failed\" 分支处理的帧），"+
					"投递标记必须保持 false；原门控对任何有效 data 帧置位 = 多算")
		})
	}
}

// TestOpenAIStreamingResponse_IncompleteAndBareErrorDoNotMarkUpstreamDelivered 补齐 brief 的
// Important 项：黑名单 openAIStreamDataStartsClientOutput 只排除 response.failed 与前导帧，
// 裸 error 与 response.incomplete 会穿透。收窄该函数会改变客户端 flush/写出行为（它同时是
// startsClientOutput 的来源），故改为在标记置位处独立用白名单排除，本测试咬住这一点。
func TestOpenAIStreamingResponse_IncompleteAndBareErrorDoNotMarkUpstreamDelivered(t *testing.T) {
	cases := map[string]string{
		"bare_error_only": openAIDeliveredBareErrorSSE(),
		"incomplete_only": `data: {"type":"response.incomplete","response":{"id":"resp_inc",` +
			`"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"}}}` + "\n\n",
	}
	for name, sse := range cases {
		for _, guard := range []bool{false, true} {
			t.Run(name+"/"+openAIDeliveredBranchName(guard), func(t *testing.T) {
				c, _ := newOpenAIUpstreamDeliveredSSEContext()

				_, _ = runOpenAIUpstreamDeliveredStream(c, sse, guard)

				require.False(t, c.GetBool(GatewayUpstreamDeliveredKey),
					"裸 error 帧与 response.incomplete 都不含上游推理内容，投递标记必须保持 false；"+
						"它们能穿透 openAIStreamDataStartsClientOutput 黑名单，必须由白名单判定拦住 = 防多算")
			})
		}
	}
}

// TestOpenAIWSHTTPBridge_TerminalOnlyStreamDoesNotMarkUpstreamDelivered 是 WS 类反向锚点：
// 上游只回了 response.completed 终止事件，没有任何 delta/output token 事件。
// isOpenAIWSTokenEvent 明确把终止事件排除在外，若有人把 c.Set 提到 isTokenEvent 判断之外
// 无条件执行，本测试会 FAIL——咬的是多算方向。
func TestOpenAIWSHTTPBridge_TerminalOnlyStreamDoesNotMarkUpstreamDelivered(t *testing.T) {
	c := runOpenAIWSBridgeDeliveredTurn(
		`data: {"type":"response.completed","response":{"id":"resp_ws2","usage":{"input_tokens":6,"output_tokens":0}}}` + "\n\n",
	)

	require.False(t, c.GetBool(GatewayUpstreamDeliveredKey),
		"上游只回了终止事件、没有任何 token 事件，标记必须保持 false；否则零投递请求被计费 = 多算")
}
