package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

const (
	antigravityImageChunkNoFinish = "data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"inlineData\":{\"mimeType\":\"image/png\",\"data\":\"aGVsbG8=\"}}]}}]}}\n\n"
	antigravityImageChunkFinished = "data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"inlineData\":{\"mimeType\":\"image/png\",\"data\":\"aGVsbG8=\"}}]},\"finishReason\":\"STOP\"}]}}\n\n"
	antigravityTextChunkNoFinish  = "data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"drawing\"}]}}]}}\n\n"
)

// forwardAntigravityGeminiImage 让图片模型经 ForwardGemini 收到一段上游 SSE（随后以 bodyErr 结束），
// 返回转发结果与错误。gatewayCfg 为空时只设置 MaxLineSize。
func forwardAntigravityGeminiImage(t *testing.T, stream bool, body io.ReadCloser, gatewayCfg config.GatewayConfig) (*ForwardResult, error) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	if gatewayCfg.MaxLineSize == 0 {
		gatewayCfg.MaxLineSize = defaultMaxLineSize
	}
	reqBody := []byte(`{"contents":[{"role":"user","parts":[{"text":"draw a cat"}]}],"generationConfig":{"responseModalities":["TEXT","IMAGE"]}}`)
	action := "generateContent"
	if stream {
		action = "streamGenerateContent"
	}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-3.1-flash-image:"+action, bytes.NewReader(reqBody))

	svc := &AntigravityGatewayService{
		settingService: NewSettingService(&antigravitySettingRepoStub{}, &config.Config{Gateway: gatewayCfg}),
		tokenProvider:  &AntigravityTokenProvider{},
		httpUpstream: &queuedHTTPUpstreamStub{responses: []*http.Response{{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       body,
		}}},
	}
	account := &Account{
		ID: 105, Name: "antigravity-image-failure", Platform: PlatformAntigravity, Type: AccountTypeOAuth,
		Status: StatusActive, Concurrency: 1,
		Credentials: map[string]any{"access_token": "test-access-token", "project_id": "test-project"},
	}
	return svc.ForwardGemini(context.Background(), c, account, "gemini-3.1-flash-image", action, stream, reqBody, false)
}

// 上游中断时只能按真实送达客户端的图片结算：合并后失败结算守卫会带上 ImageCount，
// 按模型名猜出来的 1 张会被当成真实上游用量按张扣费。
func TestAntigravityForwardGemini_ImageCountOnInterruptedResponse(t *testing.T) {
	tests := []struct {
		name    string
		stream  bool
		payload string
		want    int
	}{
		{name: "stream read error before any chunk", stream: true, payload: "", want: 0},
		{name: "stream read error after text only", stream: true, payload: antigravityTextChunkNoFinish, want: 0},
		{name: "stream read error after a delivered image", stream: true, payload: antigravityImageChunkNoFinish, want: 1},
		{name: "stream read error after finish chunk", stream: true, payload: antigravityImageChunkFinished, want: 1},
		{name: "non-stream collect error never delivers the image", stream: false, payload: antigravityImageChunkNoFinish, want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := forwardAntigravityGeminiImage(t, tt.stream,
				&streamReadCloser{payload: []byte(tt.payload), err: io.ErrUnexpectedEOF}, config.GatewayConfig{})

			require.Error(t, err)
			require.NotNil(t, result, "中断时仍要把已观察到的用量交给失败结算")
			require.Equal(t, tt.want, result.ImageCount)
		})
	}
}

// 一个分片都没收到就触发数据间隔超时：没有生成也没有送达任何图片。
func TestAntigravityForwardGemini_StreamTimeoutBeforeAnyChunkHasNoImage(t *testing.T) {
	pr, pw := io.Pipe()
	t.Cleanup(func() { _ = pr.Close(); _ = pw.Close() })

	result, err := forwardAntigravityGeminiImage(t, true, pr, config.GatewayConfig{StreamDataIntervalTimeout: 1})

	require.EqualError(t, err, "stream data interval timeout")
	require.NotNil(t, result)
	require.Zero(t, result.ImageCount)
}

// 正常完成的请求保持原有口径：图片模型按 1 张计。
func TestAntigravityForwardGemini_CompletedImageStreamKeepsImageCount(t *testing.T) {
	for _, stream := range []bool{true, false} {
		result, err := forwardAntigravityGeminiImage(t, stream,
			io.NopCloser(bytes.NewReader([]byte(antigravityImageChunkFinished))), config.GatewayConfig{})

		require.NoError(t, err)
		require.NotNil(t, result)
		require.Equal(t, 1, result.ImageCount, "stream=%v", stream)
	}
}
