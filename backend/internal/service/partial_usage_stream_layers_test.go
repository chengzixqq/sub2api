package service

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func partialUsageTestContext(path string) *gin.Context {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, path, nil)
	return c
}

func partialUsageReadErrorResponse(payload string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body: &streamReadCloser{
			payload: []byte(payload),
			err:     io.ErrUnexpectedEOF,
		},
	}
}

func TestAntigravityStreamingReadErrorPreservesPartialUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := newAntigravityTestService(&config.Config{
		Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize},
	})

	t.Run("gemini passthrough", func(t *testing.T) {
		c := partialUsageTestContext("/v1beta/models/gemini:streamGenerateContent")
		resp := partialUsageReadErrorResponse("data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"partial\"}]}}],\"usageMetadata\":{\"promptTokenCount\":11,\"candidatesTokenCount\":3,\"cachedContentTokenCount\":2}}\n\n")

		result, err := svc.handleGeminiStreamingResponse(c, resp, time.Now())

		require.Error(t, err)
		require.NotNil(t, result)
		require.NotNil(t, result.usage)
		require.Equal(t, 9, result.usage.InputTokens)
		require.Equal(t, 3, result.usage.OutputTokens)
		require.Equal(t, 2, result.usage.CacheReadInputTokens)
		require.True(t, c.GetBool(GatewayUpstreamDeliveredKey))
	})

	t.Run("claude conversion", func(t *testing.T) {
		c := partialUsageTestContext("/v1/messages")
		resp := partialUsageReadErrorResponse("data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"partial\"}]}}],\"usageMetadata\":{\"promptTokenCount\":7,\"candidatesTokenCount\":2}}}\n\n")

		result, err := svc.handleClaudeStreamingResponse(c, resp, time.Now(), "claude-sonnet-4-5")

		require.Error(t, err)
		require.NotNil(t, result)
		require.NotNil(t, result.usage)
		require.Equal(t, 7, result.usage.InputTokens)
		require.Equal(t, 2, result.usage.OutputTokens)
		require.True(t, c.GetBool(GatewayUpstreamDeliveredKey))
	})
}

func TestAntigravityNonStreamingCollectorPreservesPartialUsageWithoutDelivery(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := newAntigravityTestService(&config.Config{
		Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize},
	})

	t.Run("gemini passthrough", func(t *testing.T) {
		c := partialUsageTestContext("/v1beta/models/gemini:generateContent")
		resp := partialUsageReadErrorResponse("data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"partial\"}]}}],\"usageMetadata\":{\"promptTokenCount\":11,\"candidatesTokenCount\":3,\"cachedContentTokenCount\":2}}}\n\n")

		result, err := svc.handleGeminiStreamToNonStreaming(c, resp, time.Now())

		require.Error(t, err)
		require.NotNil(t, result)
		require.NotNil(t, result.usage)
		require.Equal(t, 9, result.usage.InputTokens)
		require.Equal(t, 3, result.usage.OutputTokens)
		require.False(t, c.GetBool(GatewayUpstreamDeliveredKey), "buffered non-stream collection has not delivered upstream content")
	})

	t.Run("claude conversion", func(t *testing.T) {
		c := partialUsageTestContext("/v1/messages")
		resp := partialUsageReadErrorResponse("data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"partial\"}]}}],\"usageMetadata\":{\"promptTokenCount\":7,\"candidatesTokenCount\":2}}}\n\n")

		result, err := svc.handleClaudeStreamToNonStreaming(c, resp, time.Now(), "claude-sonnet-4-5")

		require.Error(t, err)
		require.NotNil(t, result)
		require.NotNil(t, result.usage)
		require.Equal(t, 7, result.usage.InputTokens)
		require.Equal(t, 2, result.usage.OutputTokens)
		require.False(t, c.GetBool(GatewayUpstreamDeliveredKey), "buffered non-stream collection has not delivered upstream content")
	})
}

func TestGeminiCompatibilityStreamingReadErrorPreservesPartialUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := &GeminiMessagesCompatService{}
	geminiPayload := `{"candidates":[{"content":{"parts":[{"text":"partial"}]}}],"usageMetadata":{"promptTokenCount":13,"candidatesTokenCount":4,"cachedContentTokenCount":3}}`

	t.Run("messages conversion", func(t *testing.T) {
		c := partialUsageTestContext("/v1/messages")
		resp := partialUsageReadErrorResponse("data: {\"response\":" + geminiPayload + "}\n\n")

		result, err := svc.handleStreamingResponse(c, resp, time.Now(), "claude-sonnet-4-5")

		require.Error(t, err)
		require.NotNil(t, result)
		require.NotNil(t, result.usage)
		require.Equal(t, 10, result.usage.InputTokens)
		require.Equal(t, 4, result.usage.OutputTokens)
		require.Equal(t, 3, result.usage.CacheReadInputTokens)
	})

	t.Run("native passthrough", func(t *testing.T) {
		c := partialUsageTestContext("/v1beta/models/gemini:streamGenerateContent")
		resp := partialUsageReadErrorResponse("data: " + geminiPayload + "\n\n")

		result, err := svc.handleNativeStreamingResponse(c, resp, time.Now(), false)

		require.Error(t, err)
		require.NotNil(t, result)
		require.NotNil(t, result.usage)
		require.Equal(t, 10, result.usage.InputTokens)
		require.Equal(t, 4, result.usage.OutputTokens)
		require.Equal(t, 3, result.usage.CacheReadInputTokens)
	})

	t.Run("chat completions conversion", func(t *testing.T) {
		c := partialUsageTestContext("/v1/chat/completions")
		resp := partialUsageReadErrorResponse("data: " + geminiPayload + "\n\n")

		result, err := svc.handleChatCompletionsStreamingResponseFromGemini(c, resp, time.Now(), "gemini-2.5-pro", false, true)

		require.Error(t, err)
		require.NotNil(t, result)
		require.NotNil(t, result.usage)
		require.Equal(t, 10, result.usage.InputTokens)
		require.Equal(t, 4, result.usage.OutputTokens)
		require.Equal(t, 3, result.usage.CacheReadInputTokens)
	})
}

func TestCollectGeminiSSEReadErrorPreservesPartialUsage(t *testing.T) {
	body := &streamReadCloser{
		payload: []byte("data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"partial\"}]}}],\"usageMetadata\":{\"promptTokenCount\":13,\"candidatesTokenCount\":4,\"cachedContentTokenCount\":3}}\n\n"),
		err:     io.ErrUnexpectedEOF,
	}

	collected, usage, err := collectGeminiSSE(body, false)

	require.Error(t, err)
	require.NotEmpty(t, collected)
	require.NotNil(t, usage)
	require.Equal(t, 10, usage.InputTokens)
	require.Equal(t, 4, usage.OutputTokens)
	require.Equal(t, 3, usage.CacheReadInputTokens)
}

func TestAntigravityCompatibilityNonStreamingReadErrorPreservesPartialUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := newAntigravityCompatService(config.GatewayConfig{MaxLineSize: defaultMaxLineSize}, nil)
	payload := "data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"partial\"}]}}],\"usageMetadata\":{\"promptTokenCount\":8,\"candidatesTokenCount\":2}}}\n\n"

	tests := []struct {
		name string
		path string
		call func(*gin.Context, *http.Response) (*antigravityStreamResult, error)
	}{
		{
			name: "responses",
			path: "/v1/responses",
			call: func(c *gin.Context, resp *http.Response) (*antigravityStreamResult, error) {
				return svc.handleResponsesNonStreamingFromAntigravity(c, resp, time.Now(), "gemini-3.1-pro-high")
			},
		},
		{
			name: "chat completions",
			path: "/v1/chat/completions",
			call: func(c *gin.Context, resp *http.Response) (*antigravityStreamResult, error) {
				return svc.handleChatCompletionsNonStreamingFromAntigravity(c, resp, time.Now(), "gemini-3.1-pro-high")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := partialUsageTestContext(tt.path)
			result, err := tt.call(c, partialUsageReadErrorResponse(payload))

			require.Error(t, err)
			require.NotNil(t, result)
			require.NotNil(t, result.usage)
			require.Equal(t, 8, result.usage.InputTokens)
			require.Equal(t, 2, result.usage.OutputTokens)
			require.False(t, c.GetBool(GatewayUpstreamDeliveredKey))
		})
	}
}
