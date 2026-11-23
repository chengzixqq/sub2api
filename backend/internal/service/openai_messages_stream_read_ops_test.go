//go:build unit

package service

import (
	"bufio"
	"context"
	"io"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestMessagesPreWriteReadErrorMark(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"read error", io.ErrUnexpectedEOF, true},
		{"cancelled", context.Canceled, false}, {"timeout", context.DeadlineExceeded, false},
		{"oversize", bufio.ErrTooLong, false}, {"clean EOF", io.EOF, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest("POST", "/v1/messages", nil)
			writeOpenAIMessagesStreamReadError(c, tc.err, false, false)
			require.Equal(t, tc.want, c.GetBool("ops_openai_messages_pre_write_read_error"))
			require.False(t, c.Writer.Written())
		})
	}
}

func TestMessagesReadErrorOpsKeepsPriorCommitAndCancellation(t *testing.T) {
	for _, mode := range []string{"cancelled request", "client disconnected", "tracked ping", "committed SSE", "plain response", "other endpoint"} {
		t.Run(mode, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest("POST", "/v1/messages", nil)
			switch mode {
			case "cancelled request":
				ctx, cancel := context.WithCancel(c.Request.Context())
				cancel()
				c.Request = c.Request.WithContext(ctx)
			case "tracked ping", "committed SSE":
				c.Header("Content-Type", "text/event-stream")
				n, err := c.Writer.WriteString("event: ping\ndata: {}\n\n")
				require.NoError(t, err)
				if mode == "tracked ping" {
					recordOpenAIStreamKeepaliveBytes(c, n)
				}
				c.Writer.Flush()
			case "plain response":
				c.String(200, "ready")
			case "other endpoint":
				c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
			}
			writeOpenAIMessagesStreamReadError(c, io.ErrUnexpectedEOF, mode == "client disconnected", false)
			require.False(t, c.GetBool(OpsOpenAIMessagesPreWriteReadErrorKey))
			if mode == "committed SSE" {
				require.Contains(t, rec.Body.String(), "event: error")
			}
		})
	}
}

func TestMessagesPreWriteReadDeadlinePreservesOps(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/messages", nil)
	writeOpenAIMessagesStreamReadError(c, os.ErrDeadlineExceeded, false, false)
	require.False(t, c.GetBool(OpsOpenAIMessagesPreWriteReadErrorKey))
}
