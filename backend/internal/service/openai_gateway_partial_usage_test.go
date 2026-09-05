package service

import (
	"bytes"
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

func openAIPartialUsageFailureSSE() string {
	return strings.Join([]string{
		"event: response.output_text.delta",
		`data: {"type":"response.output_text.delta","delta":"partial"}`,
		"",
		"event: response.failed",
		`data: {"type":"response.failed","response":{"id":"resp_partial","status":"failed","usage":{"input_tokens":23,"output_tokens":4,"input_tokens_details":{"cached_tokens":7}},"error":{"code":"server_error","message":"failed after partial output"}}}`,
		"",
	}, "\n")
}

func openAIPartialImageFailureSSE() string {
	return strings.Join([]string{
		"event: response.output_item.done",
		`data: {"type":"response.output_item.done","item":{"id":"img_partial","type":"image_generation_call","status":"completed","result":"cGFydGlhbC1pbWFnZQ==","size":"1024x1024"}}`,
		"",
		"event: response.failed",
		`data: {"type":"response.failed","response":{"id":"resp_partial_image","status":"failed","usage":{"input_tokens":0,"output_tokens":0},"error":{"code":"server_error","message":"failed after image output"}}}`,
		"",
	}, "\n")
}

func newOpenAIPartialUsageContext(body []byte) *gin.Context {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	return c
}

func newOpenAIPartialUsageService(resp *http.Response) *OpenAIGatewayService {
	cfg := &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}}
	return &OpenAIGatewayService{cfg: cfg, httpUpstream: &httpUpstreamRecorder{resp: resp}}
}

func newOpenAIPartialUsageAccount(passthrough bool) *Account {
	return &Account{
		ID: 707, Name: "partial-usage", Platform: PlatformOpenAI,
		Type: AccountTypeOAuth, Concurrency: 1,
		Credentials: map[string]any{"access_token": "oauth-token", "chatgpt_account_id": "chatgpt-account"},
		Extra: map[string]any{
			"openai_passthrough":                        passthrough,
			"openai_oauth_responses_websockets_v2_mode": OpenAIWSIngressModeOff,
		},
		Status: StatusActive, Schedulable: true,
	}
}

func newGrokPartialUsageAccount() *Account {
	return &Account{
		ID: 708, Name: "grok-partial-usage", Platform: PlatformGrok,
		Type: AccountTypeAPIKey, Concurrency: 1,
		Credentials: map[string]any{
			"api_key":  "xai-partial-token",
			"base_url": "https://api.x.ai/v1",
		},
		Status: StatusActive, Schedulable: true,
	}
}

func TestHasOpenAIPartialUsageIncludesImageTokens(t *testing.T) {
	require.True(t, hasOpenAIPartialUsage(&OpenAIUsage{ImageInputTokens: 3}))
	require.True(t, hasOpenAIPartialUsage(&OpenAIUsage{ImageOutputTokens: 5}))
	require.False(t, hasOpenAIPartialUsage(&OpenAIUsage{}))
	require.False(t, hasOpenAIPartialUsage(nil))
}

func TestOpenAIGatewayForwardPreservesStreamingPartialUsageOnError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"gpt-5.4","stream":true,"input":"hello"}`)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "X-Request-Id": []string{"rid-partial"}},
		Body:       io.NopCloser(strings.NewReader(openAIPartialUsageFailureSSE())),
	}
	svc := newOpenAIPartialUsageService(resp)

	result, err := svc.Forward(context.Background(), newOpenAIPartialUsageContext(body), newOpenAIPartialUsageAccount(false), body)
	require.Error(t, err)
	require.NotNil(t, result)
	require.Equal(t, 23, result.Usage.InputTokens)
	require.Equal(t, 4, result.Usage.OutputTokens)
	require.Equal(t, 7, result.Usage.CacheReadInputTokens)
}

func TestOpenAIGatewayPassthroughPreservesStreamingPartialUsageOnError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"gpt-5.4","stream":true,"input":"hello"}`)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "X-Request-Id": []string{"rid-pass-partial"}},
		Body:       io.NopCloser(strings.NewReader(openAIPartialUsageFailureSSE())),
	}
	svc := newOpenAIPartialUsageService(resp)

	result, err := svc.Forward(context.Background(), newOpenAIPartialUsageContext(body), newOpenAIPartialUsageAccount(true), body)
	require.Error(t, err)
	require.NotNil(t, result)
	require.Equal(t, 23, result.Usage.InputTokens)
	require.Equal(t, 4, result.Usage.OutputTokens)
	require.Equal(t, 7, result.Usage.CacheReadInputTokens)
}

func TestForwardGrokResponsesPreservesStreamingPartialResultOnError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"grok-4.5","stream":true,"input":"hello"}`)

	tests := []struct {
		name           string
		stream         string
		wantInput      int
		wantOutput     int
		wantCacheRead  int
		wantImageCount int
		wantImageSizes []string
	}{
		{
			name:          "usage",
			stream:        openAIPartialUsageFailureSSE(),
			wantInput:     23,
			wantOutput:    4,
			wantCacheRead: 7,
		},
		{
			name:           "image output without token usage",
			stream:         openAIPartialImageFailureSSE(),
			wantImageCount: 1,
			wantImageSizes: []string{"1024x1024"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := &http.Response{
				StatusCode: http.StatusOK,
				Header: http.Header{
					"Content-Type":   []string{"text/event-stream"},
					"Xai-Request-Id": []string{"rid-grok-partial"},
				},
				Body: io.NopCloser(strings.NewReader(tt.stream)),
			}
			svc := newOpenAIPartialUsageService(resp)

			result, err := svc.forwardGrokResponses(
				context.Background(), newOpenAIPartialUsageContext(body),
				newGrokPartialUsageAccount(), body, "grok-4.5", true, time.Now(),
			)

			require.Error(t, err)
			require.NotNil(t, result)
			require.True(t, result.Stream)
			require.Equal(t, tt.wantInput, result.Usage.InputTokens)
			require.Equal(t, tt.wantOutput, result.Usage.OutputTokens)
			require.Equal(t, tt.wantCacheRead, result.Usage.CacheReadInputTokens)
			require.Equal(t, tt.wantImageCount, result.ImageCount)
			require.Equal(t, tt.wantImageSizes, result.ImageOutputSizes)
		})
	}
}
