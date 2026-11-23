//go:build unit

package handler

import (
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"net/http/httptest"
	"testing"
)

func TestClaudeErrorWireHandlers(t *testing.T) {
	for _, tc := range []struct {
		name, typ, code, msg string
		status               int
		write                func(*gin.Context, bool)
	}{
		{"openQueue", "rate_limit_error", "gateway_queue_full", "Too many pending requests, please retry later", 429, func(c *gin.Context, stream bool) {
			(&OpenAIGatewayHandler{}).handleStreamingAwareErrorWithCode(c, 429, "rate_limit_error", "gateway_queue_full", "Too many pending requests, please retry later", stream, false)
		}},
		{"openConcurrency", "rate_limit_error", "gateway_concurrency_limit", "Concurrency limit exceeded for user, please retry later", 429, func(c *gin.Context, stream bool) {
			(&OpenAIGatewayHandler{}).handleStreamingAwareErrorWithCode(c, 429, "rate_limit_error", "gateway_concurrency_limit", "Concurrency limit exceeded for user, please retry later", stream, false)
		}},
		{"nativeQueue", "rate_limit_error", "gateway_queue_full", "Too many pending requests, please retry later", 429, func(c *gin.Context, stream bool) {
			(&GatewayHandler{}).handleStreamingAwareErrorWithCode(c, 429, "rate_limit_error", "gateway_queue_full", "Too many pending requests, please retry later", stream)
		}},
		{"open503", "api_error", "", "No available accounts", 503, func(c *gin.Context, stream bool) {
			(&OpenAIGatewayHandler{}).errorResponse(c, 503, "api_error", "No available accounts")
		}},
		{"open500", "api_error", "", "Internal server error", 500, func(c *gin.Context, stream bool) {
			(&OpenAIGatewayHandler{}).errorResponse(c, 500, "api_error", "Internal server error")
		}},
		{"native500", "api_error", "", "Internal server error", 500, func(c *gin.Context, stream bool) {
			(&GatewayHandler{}).errorResponse(c, 500, "api_error", "Internal server error")
		}},
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
						prefix = ":\n\n"
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
