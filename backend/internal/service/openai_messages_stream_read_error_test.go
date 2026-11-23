package service

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestOpenAIMessagesStreamReadError(t *testing.T) {
	const start = "data: " + `{"type":"response.created","response":{"id":"resp_1","model":"gpt-5","usage":{"input_tokens":21,"output_tokens":3}}}` + "\n\n"
	const text = "data: " + `{"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"hi"}` + "\n\n"
	const ping = ":\n\n"
	const frame = "event: error\ndata: " + `{"type":"error","error":{"type":"api_error","message":"upstream stream disconnected: unexpected EOF"}}` + "\n\n"
	for _, keepalive := range []int{0, 60} {
		for _, tc := range []struct {
			name, upstream          string
			ping, tracked, canceled bool
			wantFrame, delivered    bool
		}{
			{name: "text", upstream: start + text, wantFrame: true, delivered: true},
			{name: "queue ping", ping: true, wantFrame: true},
			{name: "tracked keepalive handled by fallback", ping: true, tracked: true},
			{name: "before output"},
			{name: "canceled after ping", ping: true, canceled: true},
		} {
			t.Run(fmt.Sprintf("%s/keepalive=%d", tc.name, keepalive), func(t *testing.T) {
				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
				if tc.ping {
					c.Header("Content-Type", "text/event-stream")
					n, err := c.Writer.WriteString(ping)
					require.NoError(t, err)
					if tc.tracked {
						recordOpenAIStreamKeepaliveBytes(c, n)
					}
					c.Writer.Flush()
				}
				fault := io.ErrUnexpectedEOF
				if tc.canceled {
					ctx, cancel := context.WithCancel(c.Request.Context())
					cancel()
					c.Request = c.Request.WithContext(ctx)
					fault = context.Canceled
				}
				cfg := &config.Config{}
				cfg.Gateway.StreamKeepaliveInterval = keepalive
				resp := &http.Response{Header: http.Header{}, Body: io.NopCloser(io.MultiReader(strings.NewReader(tc.upstream), iotest.ErrReader(fault)))}
				svc := &OpenAIGatewayService{cfg: cfg}
				account := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
				result, err := svc.handleAnthropicStreamingResponse(resp, c, account, "gpt-5", "gpt-5", "gpt-5", time.Now())
				require.ErrorIs(t, err, fault)
				require.NotNil(t, result)
				require.Equal(t, tc.delivered, c.GetBool(GatewayUpstreamDeliveredKey))
				if tc.delivered {
					require.Equal(t, 21, result.Usage.InputTokens)
					require.Equal(t, 3, result.Usage.OutputTokens)
				} else {
					require.Zero(t, result.Usage)
				}
				body := rec.Body.String()
				if tc.wantFrame {
					require.True(t, strings.HasSuffix(body, frame), "%q", body)
					require.Equal(t, 1, strings.Count(body, "event: error\n"))
					require.True(t, IsResponseCommitted(c))
					mark, ok := GetOpsStreamError(c)
					require.True(t, ok)
					require.Equal(t, "upstream_error", mark.ErrType)
					require.Equal(t, http.StatusBadGateway, mark.IntendedStatus)
					require.Equal(t, "stream_read_error", c.GetString(OpsTerminalStreamReasonKey))
					if tc.ping {
						require.Equal(t, ping+frame, body)
					}
				} else {
					require.NotContains(t, body, "event: error")
					require.False(t, IsResponseCommitted(c))
				}
			})
		}
	}
}

func TestChatCompletionsAsAnthropicStreamReadError(t *testing.T) {
	const text = "data: " + `{"choices":[{"index":0,"delta":{"content":"hi"}}]}` + "\n\n"
	const usage = "data: " + `{"choices":[],"usage":{"prompt_tokens":21,"completion_tokens":3}}` + "\n\n"
	const frame = "event: error\ndata: " + `{"type":"error","error":{"type":"api_error","message":"upstream stream disconnected: unexpected EOF"}}` + "\n\n"
	for _, tc := range []struct {
		name, upstream            string
		ping, canceled, wantFrame bool
	}{
		{name: "before output"},
		{name: "queue ping", ping: true, wantFrame: true},
		{name: "text", upstream: text, wantFrame: true},
		{name: "metered text", upstream: text + usage, wantFrame: true},
		{name: "request canceled during read", upstream: text, canceled: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			c.Request = httptest.NewRequestWithContext(ctx, http.MethodPost, "/v1/messages", nil)
			if tc.ping {
				c.Header("Content-Type", "text/event-stream")
				_, err := c.Writer.WriteString(":\n\n")
				require.NoError(t, err)
				c.Writer.Flush()
			}
			if tc.canceled {
				cancel()
			}
			resp := &http.Response{Header: http.Header{}, Body: io.NopCloser(io.MultiReader(strings.NewReader(tc.upstream), iotest.ErrReader(io.ErrUnexpectedEOF)))}
			svc := &OpenAIGatewayService{}
			result, err := svc.streamChatCompletionsAsAnthropic(c, resp, "gpt-5", "gpt-5", "gpt-5", nil, nil, time.Now())
			require.ErrorIs(t, err, io.ErrUnexpectedEOF)
			if tc.upstream == text+usage {
				require.Equal(t, 21, result.Usage.InputTokens)
				require.Equal(t, 3, result.Usage.OutputTokens)
			}
			body := rec.Body.String()
			require.Equal(t, tc.wantFrame, IsResponseCommitted(c))
			mark, marked := GetOpsStreamError(c)
			require.Equal(t, tc.wantFrame, marked)
			if tc.wantFrame {
				require.Equal(t, 1, strings.Count(body, "event: error\n"))
				require.True(t, strings.HasSuffix(body, frame), "%q", body)
				require.Equal(t, "upstream_error", mark.ErrType)
				require.Equal(t, http.StatusBadGateway, mark.IntendedStatus)
				require.Equal(t, "stream_read_error", c.GetString(OpsTerminalStreamReasonKey))
				if tc.ping {
					require.Equal(t, ":\n\n"+frame, body)
				}
			} else {
				require.NotContains(t, body, "event: error")
			}
		})
	}
}
