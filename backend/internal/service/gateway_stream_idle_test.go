//go:build unit

package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/synctest"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type anthropicIdleTestHandler func(*GatewayService, *http.Response, *gin.Context, time.Time) (*streamingResult, error)

func anthropicIdleTestHandlers() map[string]anthropicIdleTestHandler {
	return map[string]anthropicIdleTestHandler{
		"normal": func(s *GatewayService, resp *http.Response, c *gin.Context, start time.Time) (*streamingResult, error) {
			return s.handleStreamingResponse(context.Background(), resp, c, &Account{ID: 66}, start, "claude-fable-5", "claude-fable-5", false)
		},
		"passthrough": func(s *GatewayService, resp *http.Response, c *gin.Context, start time.Time) (*streamingResult, error) {
			return s.handleStreamingResponseAnthropicAPIKeyPassthrough(context.Background(), resp, c, &Account{ID: 66}, start, "claude-fable-5")
		},
	}
}

func newAnthropicIdleTestResponse() (*gin.Context, *httptest.ResponseRecorder, *http.Response, *io.PipeWriter) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	reader, writer := io.Pipe()
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       reader,
	}
	return c, recorder, resp, writer
}

func TestAnthropicStreamIdle_ExpiresFromLastReadDespiteKeepalive(t *testing.T) {
	for name, handle := range anthropicIdleTestHandlers() {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s := newMinimalGatewayService()
				s.cfg.Gateway.StreamDataIntervalTimeout = 2
				s.cfg.Gateway.StreamKeepaliveInterval = 1
				c, recorder, resp, writer := newAnthropicIdleTestResponse()
				defer resp.Body.Close()
				defer writer.Close()
				go func() {
					time.Sleep(500 * time.Millisecond)
					_, _ = io.WriteString(writer, "data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":42}}}\n\n"+
						"data: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":9}}\n\n")
				}()

				start := time.Now()
				result, err := handle(s, resp, c, start)
				require.ErrorContains(t, err, "stream data interval timeout")
				require.Equal(t, 2500*time.Millisecond, time.Since(start), "idle timeout must not wait for the next full polling interval")
				require.NotNil(t, result)
				require.Equal(t, 42, result.usage.InputTokens)
				require.Equal(t, 9, result.usage.OutputTokens)
				require.False(t, result.clientDisconnect)
				require.Contains(t, recorder.Body.String(), "event: ping")
			})
		})
	}
}

func TestAnthropicStreamIdle_PartialUpstreamReadsRefreshDeadline(t *testing.T) {
	for name, handle := range anthropicIdleTestHandlers() {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s := newMinimalGatewayService()
				s.cfg.Gateway.StreamDataIntervalTimeout = 2
				c, recorder, resp, writer := newAnthropicIdleTestResponse()
				defer resp.Body.Close()
				defer writer.Close()
				writerDone := make(chan struct{})
				go func() {
					defer close(writerDone)
					defer writer.Close()
					time.Sleep(500 * time.Millisecond)
					for _, part := range []string{
						"data: {\"type\":\"message_start\",",
						"\"message\":{\"usage\":{",
						"\"input_tokens\":42}}}",
					} {
						if _, err := io.WriteString(writer, part); err != nil {
							return
						}
						time.Sleep(1500 * time.Millisecond)
					}
					_, _ = io.WriteString(writer, "\n\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":9}}\n\ndata: {\"type\":\"message_stop\"}\n\n")
				}()

				start := time.Now()
				result, err := handle(s, resp, c, start)
				elapsed := time.Since(start)
				_ = resp.Body.Close()
				<-writerDone
				require.NoError(t, err, "partial SSE reads are upstream activity, even before a complete line arrives")
				require.Equal(t, 5*time.Second, elapsed)
				require.Equal(t, 42, result.usage.InputTokens)
				require.Equal(t, 9, result.usage.OutputTokens)
				require.Contains(t, recorder.Body.String(), "message_stop")
			})
		})
	}
}

func TestAnthropicStreamIdle_DisabledAndTerminalCompletion(t *testing.T) {
	for name, handle := range anthropicIdleTestHandlers() {
		t.Run(name, func(t *testing.T) {
			for setting, interval := range map[string]int{"disabled": 0, "enabled": 30} {
				t.Run(setting, func(t *testing.T) {
					synctest.Test(t, func(t *testing.T) {
						s := newMinimalGatewayService()
						s.cfg.Gateway.StreamDataIntervalTimeout = interval
						c, recorder, resp, writer := newAnthropicIdleTestResponse()
						defer resp.Body.Close()
						go func() {
							defer writer.Close()
							time.Sleep(20 * time.Second)
							_, _ = io.WriteString(writer, "data: {\"type\":\"message_stop\"}\n\n")
						}()

						start := time.Now()
						result, err := handle(s, resp, c, start)
						require.NoError(t, err)
						require.NotNil(t, result)
						require.Equal(t, 20*time.Second, time.Since(start))
						body := recorder.Body.String()
						time.Sleep(time.Minute)
						synctest.Wait()
						require.Equal(t, body, recorder.Body.String(), "completed streams must not receive a late timeout event")
					})
				})
			}
		})
	}
}
