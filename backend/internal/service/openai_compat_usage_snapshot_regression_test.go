package service

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestOpenAICompatUsageSnapshot_PreservesRawBillingDetails(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const initial = `{"input_tokens":200,"output_tokens":1,"input_tokens_details":{"cache_write_tokens":100,"cache_write_tokens_details":{"ttl_30m_tokens":20,"ttl_12h_tokens":80}}}`
	for _, tc := range []struct {
		name                        string
		terminal                    string
		wantTotal, want30m, want12h int
		wantOutput, wantImageInput  int
		wantImageOutput             int
	}{
		{name: "terminal details", terminal: `{"input_tokens":200,"output_tokens":2,"input_tokens_details":{"cache_write_tokens":100,"cache_write_tokens_details":{"ttl_30m_tokens":70,"ttl_12h_tokens":30}}}`, wantTotal: 100, want30m: 70, want12h: 30, wantOutput: 2},
		{name: "missing details same total", terminal: `{"input_tokens":200,"output_tokens":2,"input_tokens_details":{"cache_write_tokens":100}}`, wantTotal: 100, want30m: 20, want12h: 80, wantOutput: 2},
		{name: "changed aggregate", terminal: `{"input_tokens":200,"output_tokens":2,"input_tokens_details":{"cache_write_tokens":120}}`, wantTotal: 120, wantOutput: 2},
		{name: "explicit invalid split", terminal: `{"input_tokens":200,"output_tokens":2,"input_tokens_details":{"cache_write_tokens":100,"cache_write_tokens_details":{"ttl_30m_tokens":20,"ttl_12h_tokens":70}}}`, wantTotal: 100, wantOutput: 2},
		{name: "explicit null split", terminal: `{"input_tokens":200,"output_tokens":2,"input_tokens_details":{"cache_write_tokens":100,"cache_write_tokens_details":null}}`, wantTotal: 100, wantOutput: 2},
		{name: "explicit canonical zero", terminal: `{"input_tokens":200,"output_tokens":2,"cache_creation_input_tokens":100,"input_tokens_details":{"cache_write_tokens":0}}`, wantOutput: 2},
		{name: "empty terminal retains observed usage", terminal: `{}`, wantTotal: 100, want30m: 20, want12h: 80, wantOutput: 1},
		{name: "image token details", terminal: `{"input_tokens":200,"output_tokens":10,"input_tokens_details":{"image_tokens":7},"output_tokens_details":{"image_tokens":4}}`, wantOutput: 10, wantImageInput: 7, wantImageOutput: 4},
		{name: "independent reasoning tokens", terminal: `{"input_tokens":200,"output_tokens":5,"total_tokens":225,"output_tokens_details":{"reasoning_tokens":20}}`, wantOutput: 25},
	} {
		for _, path := range []string{"messages-stream", "chat-stream", "messages-buffered", "chat-buffered"} {
			t.Run(tc.name+"/"+path, func(t *testing.T) {
				stream := fmt.Sprintf("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_usage\",\"model\":\"gpt-5\",\"status\":\"in_progress\",\"usage\":%s}}\n\n", initial) +
					"data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"content_index\":0,\"delta\":\"answer\"}\n\n" +
					fmt.Sprintf("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_usage\",\"model\":\"gpt-5\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"answer\"}]}],\"usage\":%s}}\n\n", tc.terminal)
				resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(stream))}
				defer func() { _ = resp.Body.Close() }()
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
				svc := &OpenAIGatewayService{}
				account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
				var result *OpenAIForwardResult
				var err error
				switch path {
				case "messages-stream":
					result, err = svc.handleAnthropicStreamingResponse(resp, c, account, "gpt-5", "gpt-5", "gpt-5", time.Now())
				case "chat-stream":
					result, err = svc.handleChatStreamingResponse(resp, c, account, "gpt-5", "gpt-5", "gpt-5", time.Now(), 0)
				case "messages-buffered":
					result, err = svc.handleAnthropicBufferedStreamingResponse(resp, c, account, "gpt-5", "gpt-5", "gpt-5", time.Now())
				case "chat-buffered":
					result, err = svc.handleChatBufferedStreamingResponse(resp, c, account, "gpt-5", "gpt-5", "gpt-5", time.Now())
				}
				require.NoError(t, err)
				require.NotNil(t, result)
				require.Equal(t, 200, result.Usage.InputTokens)
				require.Equal(t, tc.wantOutput, result.Usage.OutputTokens)
				require.Equal(t, tc.wantTotal, result.Usage.CacheCreationInputTokens)
				require.Equal(t, tc.want30m, result.Usage.CacheCreation30mInputTokens)
				require.Equal(t, tc.want12h, result.Usage.CacheCreation12hInputTokens)
				require.Equal(t, tc.wantImageInput, result.Usage.ImageInputTokens)
				require.Equal(t, tc.wantImageOutput, result.Usage.ImageOutputTokens)
			})
		}
	}
}

func TestOpenAICompatBufferedFailure_PreservesObservedUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const observed = `{"input_tokens":200,"output_tokens":3,"input_tokens_details":{"cache_write_tokens":100,"cache_write_tokens_details":{"ttl_30m_tokens":20,"ttl_12h_tokens":80}}}`
	for _, tc := range []struct {
		name      string
		initial   string
		terminal  string
		readError bool
		wantUsage bool
	}{
		{name: "missing terminal", initial: observed, wantUsage: true},
		{name: "read failure", initial: observed, readError: true, wantUsage: true},
		{name: "failed terminal", initial: observed, terminal: `data: {"type":"response.failed","response":{"id":"resp_partial","model":"gpt-5","status":"failed","error":{"code":"server_error","message":"An error occurred while processing your request."}}}` + "\n\n", wantUsage: true},
		{name: "no fabricated usage", initial: `{}`},
	} {
		for _, path := range []string{"messages", "chat"} {
			t.Run(tc.name+"/"+path, func(t *testing.T) {
				stream := fmt.Sprintf("data: {\"type\":\"response.in_progress\",\"response\":{\"id\":\"resp_partial\",\"model\":\"gpt-5\",\"status\":\"in_progress\",\"usage\":%s}}\n\n", tc.initial) + tc.terminal
				var reader io.Reader = strings.NewReader(stream)
				if tc.readError {
					reader = io.MultiReader(reader, openAICompatUsageErrorReader{})
				}
				resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}, "X-Request-Id": []string{"partial-request"}}, Body: io.NopCloser(reader)}
				defer func() { _ = resp.Body.Close() }()
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
				svc := &OpenAIGatewayService{}
				account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
				handle := svc.handleAnthropicBufferedStreamingResponse
				if path == "chat" {
					handle = svc.handleChatBufferedStreamingResponse
				}
				result, err := handle(resp, c, account, "gpt-5", "billing-model", "upstream-model", time.Now())
				require.Error(t, err)
				if tc.terminal != "" {
					var failover *UpstreamFailoverError
					require.True(t, errors.As(err, &failover), "preserving usage must not suppress failover")
				}
				if !tc.wantUsage {
					require.Nil(t, result, "an upstream failure without usage must not manufacture usage")
					return
				}
				require.NotNil(t, result)
				require.Equal(t, "partial-request", result.RequestID)
				require.Equal(t, "billing-model", result.BillingModel)
				require.Equal(t, "upstream-model", result.UpstreamModel)
				require.False(t, result.Stream)
				require.Equal(t, 200, result.Usage.InputTokens)
				require.Equal(t, 3, result.Usage.OutputTokens)
				require.Equal(t, 100, result.Usage.CacheCreationInputTokens)
				require.Equal(t, 20, result.Usage.CacheCreation30mInputTokens)
				require.Equal(t, 80, result.Usage.CacheCreation12hInputTokens)
			})
		}
	}
}

type openAICompatUsageErrorReader struct{}

func (openAICompatUsageErrorReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
