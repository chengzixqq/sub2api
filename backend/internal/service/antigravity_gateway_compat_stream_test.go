package service

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// TestAntigravityCompatStream_MarksUpstreamDeliveredOnMeaningfulEvent 是本轮修复关闭的
// Critical 少算缺口的正向证明：真实的 handleChatCompletionsStreamingFromAntigravity 读循环在
// session.consume 观测到上游第一个「有意义」事件（真实文本/工具调用/message_stop）后，必须在
// 真实的 *gin.Context 上打上 GatewayUpstreamDeliveredKey 标记，供 handler 层的
// forwardDeliveredStreamContent 判定是否计费。
//
// 必须直接跑生产函数，不能像 handler 层测试那样手工 c.Set 模拟——否则删掉
// antigravity_gateway_compat_stream.go 里紧跟 session.consume(event.line) 之后新增的
// if session.hasMeaningfulData() { c.Set(...) } 整块，不会让任何测试失败。
func TestAntigravityCompatStream_MarksUpstreamDeliveredOnMeaningfulEvent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := newAntigravityCompatService(config.GatewayConfig{MaxLineSize: defaultMaxLineSize}, nil)
	c, _ := newAntigravityCompatContext(http.MethodPost, "/v1/chat/completions", nil)
	require.False(t, c.GetBool(GatewayUpstreamDeliveredKey), "读循环开始前不应预置投递标记")

	result, err := svc.handleChatCompletionsStreamingFromAntigravity(
		c, antigravityCompatSuccessResponse(), time.Now(), "gemini-3.1-pro-high", true,
	)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, c.GetBool(GatewayUpstreamDeliveredKey),
		"上游已经真实投递过文本内容（candidates[0].content.parts[0].text=\"ok\"），"+
			"handleAntigravityCompatStream 必须在 session.consume 观测到该 meaningful 事件后 "+
			"c.Set(GatewayUpstreamDeliveredKey, true)；漏掉这一行会让 forwardDeliveredStreamContent "+
			"在 handler 层永远判定为「未投递」，把已经消耗上游 token 的中断请求错误地免单")
}

// TestAntigravityCompatStream_UsageOnlyStreamDoesNotMarkUpstreamDelivered 是上一个测试的反向
// 锚点：上游只回了 message_start 级别的元信息（无 candidates、无文本），不应打上投递标记。用于
// 防止有人为了让上一个测试通过，把 c.Set 提到 session.consume 之外无条件执行——那样会让本该走
// 「未投递」不计费分支的空转请求被误判为已投递，属于比少算更严重的多算回归。
func TestAntigravityCompatStream_UsageOnlyStreamDoesNotMarkUpstreamDelivered(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := newAntigravityCompatService(config.GatewayConfig{MaxLineSize: defaultMaxLineSize}, nil)
	c, _ := newAntigravityCompatContext(http.MethodPost, "/v1/chat/completions", nil)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body: io.NopCloser(strings.NewReader(
			`data: {"response":{"responseId":"resp_3757","usageMetadata":{"promptTokenCount":8}}}` + "\n\n",
		)),
	}

	result, err := svc.handleChatCompletionsStreamingFromAntigravity(c, resp, time.Now(), "gemini-3.1-pro-high", true)

	require.Nil(t, result)
	require.Error(t, err)
	require.False(t, c.GetBool(GatewayUpstreamDeliveredKey), "没有任何有意义事件时不应打上投递标记")
}

// TestAntigravityCompatStream_KeepsUpstreamDeliveredAcrossPlainTimeoutError 直接复现本轮要
// 闭合的缺口链：上游先真实投递一帧文本，随后挂死触发 StreamDataIntervalTimeout。这条路径下
// handleAntigravityCompatStream 返回的是普通 error（"stream data interval timeout"），不是
// *UpstreamFailoverError——这正是修复前漏埋点导致「已投递后中断」永不计费的那条具体路径。
// 断言错误内容 + 投递标记仍为 true，证明本轮已经把它补回计费。
func TestAntigravityCompatStream_KeepsUpstreamDeliveredAcrossPlainTimeoutError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := newAntigravityCompatService(
		config.GatewayConfig{MaxLineSize: defaultMaxLineSize, StreamDataIntervalTimeout: 1},
		nil,
	)
	c, _ := newAntigravityCompatContext(http.MethodPost, "/v1/chat/completions", nil)
	reader, writer := io.Pipe()
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: reader}

	type outcome struct {
		result *antigravityStreamResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := svc.handleChatCompletionsStreamingFromAntigravity(c, resp, time.Now(), "gemini-3.1-pro-high", true)
		done <- outcome{result: result, err: err}
	}()

	_, err := io.WriteString(writer,
		`data: {"response":{"responseId":"resp_3757","candidates":[{"content":{"parts":[{"text":"hi"}]}}],"usageMetadata":{"promptTokenCount":8,"candidatesTokenCount":1}}}`+"\n\n",
	)
	require.NoError(t, err)

	select {
	case got := <-done:
		require.Error(t, got.err)
		require.EqualError(t, got.err, "stream data interval timeout")
		require.NotNil(t, got.result)
		require.True(t, c.GetBool(GatewayUpstreamDeliveredKey),
			"上游已经真实投递过文本内容，随后才因 StreamDataIntervalTimeout 挂死；即使这条路径"+
				"返回的是普通 error 而非 UpstreamFailoverError，也必须保留 "+
				"GatewayUpstreamDeliveredKey=true，否则 handler 层会把这个已消耗上游 token 的"+
				"中断请求错误地免单")
	case <-time.After(3 * time.Second):
		_ = writer.Close()
		_ = reader.Close()
		t.Fatal("compat stream ignored StreamDataIntervalTimeout after delivering meaningful data")
	}
	_ = writer.Close()
	_ = reader.Close()
}
