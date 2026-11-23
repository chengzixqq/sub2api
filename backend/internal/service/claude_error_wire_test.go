//go:build unit

package service

import (
	"errors"
	"fmt"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/iotest"
	"time"
)

func TestClaudeErrorWireServices(t *testing.T) {
	for _, tc := range []struct {
		name, typ, code, msg string
		status               int
		write                func(*gin.Context, bool)
	}{
		{"mappedAntigravity", "invalid_request_error", "", "Invalid request", 400, func(c *gin.Context, _ bool) {
			_ = (&AntigravityGatewayService{}).writeMappedClaudeError(c, &Account{Platform: PlatformAntigravity}, 400, "", []byte(`{"error":{"message":"bad <tag>&"}}`))
		}},
		{"mappedGemini", "api_error", "", "Upstream service temporarily unavailable", 502, func(c *gin.Context, _ bool) {
			_ = (&GeminiMessagesCompatService{}).writeGeminiMappedError(c, &Account{Platform: PlatformGemini}, 502, "", nil)
		}},
		{"retries", "api_error", "", "Upstream request failed after retries", 502, func(c *gin.Context, _ bool) {
			_ = (&AntigravityGatewayService{}).writeClaudeError(c, nil, 502, "upstream_error", "Upstream request failed after retries")
		}},
		{"provider", "api_error", "", "Antigravity token provider not configured", 502, func(c *gin.Context, _ bool) {
			_ = (&AntigravityGatewayService{}).writeClaudeError(c, nil, 502, "api_error", "Antigravity token provider not configured")
		}},
		{"build", "api_error", "", "gemini api_key not configured", 502, func(c *gin.Context, _ bool) {
			_ = (&GeminiMessagesCompatService{}).writeClaudeError(c, 502, "upstream_error", "gemini api_key not configured")
		}},
		{"html", "api_error", "", "<tag>&", 502, func(c *gin.Context, _ bool) { writeGatewayErrorResponse(c, 502, "upstream_error", "<tag>&") }},
		{"tooLarge", "api_error", "", "Upstream response too large", 502, func(c *gin.Context, _ bool) { anthropicTooLargeError(c) }},
		{"bridgedTooLarge", "api_error", "", "Upstream response too large", 502, func(c *gin.Context, _ bool) { openAITooLargeError(c) }},
	} {
		for _, stream := range []bool{false, true} {
			for _, fill := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/stream=%t/fill=%t", tc.name, stream, fill), func(t *testing.T) {
					rec := httptest.NewRecorder()
					c, _ := gin.CreateTestContext(rec)
					c.Request = httptest.NewRequest("POST", "/v1/messages", nil)
					id := "req_011C123456789ABCDEFGHJKL"
					if fill {
						c.Set("anthropic_filled_request_id", id)
						c.Header("Request-Id", id)
					}
					prefix := ""
					if stream {
						prefix = "data: {\"type\": \"ping\"}\n\n"
						c.Header("Content-Type", "text/event-stream")
						_, err := c.Writer.WriteString(prefix)
						require.NoError(t, err)
						c.Writer.Flush()
					}
					tc.write(c, stream)
					want := `{"type":"error","error":{"type":"` + tc.typ + `"`
					if tc.code != "" {
						want += `,"code":"` + tc.code + `"`
					}
					want += `,"message":"` + tc.msg + `"}`
					if fill {
						want += `,"request_id":"` + id + `"`
					}
					want += "}"
					if stream {
						want = prefix + "event: error\ndata: " + want + "\n\n"
					}
					t.Logf("bytes %q %d %q", rec.Body.String(), rec.Code, rec.Result().Header.Get("Content-Type"))
					require.Equal(t, want, rec.Body.String())
					if !stream {
						require.Equal(t, tc.status, rec.Code)
						require.Equal(t, "application/json", rec.Header().Get("Content-Type"))
					}
				})
			}
		}
	}
}

func TestAntigravityStreamErrorWire(t *testing.T) {
	for _, tc := range []struct{ reason, msg, typ, category string }{
		{"stream_read_error", "Failed to read upstream stream", "api_error", "transport_or_stream"},
		{"response_too_large", "Upstream response exceeded the size limit", "api_error", "upstream_5xx"},
		{"stream_timeout", "Upstream response stream timed out", "timeout_error", "timeout"},
	} {
		for _, fill := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/fill=%t", tc.reason, fill), func(t *testing.T) {
				cfg := &config.Config{}
				cfg.Gateway.MaxLineSize = 65536
				var reader io.Reader = iotest.ErrReader(errors.New("broken upstream"))
				if tc.reason == "response_too_large" {
					reader = strings.NewReader(strings.Repeat("a", 131072))
				}
				if tc.reason == "stream_timeout" {
					pr, pw := io.Pipe()
					defer pw.Close()
					defer pr.Close()
					reader = pr
					cfg.Gateway.StreamDataIntervalTimeout = 1
				}
				svc := &AntigravityGatewayService{settingService: &SettingService{cfg: cfg}}
				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				c.Request = httptest.NewRequest("POST", "/v1/messages", nil)
				const id = "req_011C123456789ABCDEFGHJKL"
				if fill {
					c.Set("anthropic_filled_request_id", id)
					c.Header("Request-Id", id)
				}
				_, err := svc.handleClaudeStreamingResponse(c, &http.Response{Header: http.Header{}, Body: io.NopCloser(reader)}, time.Now(), "claude-sonnet-4")
				require.Error(t, err)
				body := `{"type":"error","error":{"type":"` + tc.typ + `","message":"` + tc.msg + `"}`
				if fill {
					body += `,"request_id":"` + id + `"`
				}
				t.Logf("bytes %q", rec.Body.String())
				require.Equal(t, "event: error\ndata: "+body+"}\n\n", rec.Body.String())
				require.True(t, IsResponseCommitted(c))
				mark, ok := GetOpsStreamError(c)
				require.True(t, ok)
				require.Equal(t, "upstream_error", mark.ErrType)
				require.Equal(t, 502, mark.IntendedStatus)
				detail := c.GetString(OpsUpstreamErrorDetailKey)
				require.Equal(t, tc.reason, detail)
				require.Equal(t, tc.category, ClassifyChannelMonitorV2Error(ChannelMonitorV2ErrorInput{ErrorType: mark.ErrType, UpstreamStatusCode: 502, Message: detail}))
			})
		}
	}
}

func TestOpenAIChatResponseTooLargeWire(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
	openAITooLargeError(c)
	t.Logf("bytes %q %d %q", rec.Body.String(), rec.Code, rec.Header().Get("Content-Type"))
	require.Equal(t, 502, rec.Code)
	require.Equal(t, `{"error":{"message":"Upstream response too large","type":"upstream_error"}}`, rec.Body.String())
}

func TestTerminalStreamReasonPreservesUpstreamDetail(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	SetOpsUpstreamError(c, 502, "Request could not be processed", "content_policy_violation")
	markOpsAnthropicTerminalError(c, "stream_read_error", gin.H{"error": "stream_read_error"})
	require.Equal(t, "content_policy_violation", c.GetString(OpsUpstreamErrorDetailKey))
	require.Equal(t, "stream_read_error", c.GetString(OpsTerminalStreamReasonKey))
}
