package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type observedAnthropicStreamBody struct {
	io.ReadCloser
	active     atomic.Int32
	closeCount atomic.Int32
	once       sync.Once
}

func (b *observedAnthropicStreamBody) Read(p []byte) (int, error) {
	b.active.Add(1)
	defer b.active.Add(-1)
	return b.ReadCloser.Read(p)
}
func (b *observedAnthropicStreamBody) Close() error {
	var err error
	b.once.Do(func() { b.closeCount.Add(1); err = b.ReadCloser.Close() })
	return err
}

func TestAnthropicStreamTerminalFrameClosesBodyAndJoinsScanner(t *testing.T) {
	for _, path := range []string{"managed", "passthrough"} {
		for _, host := range []string{"api.anthropic.com", "compatible.test"} {
			t.Run(path+"/"+host, func(t *testing.T) {
				pr, pw := io.Pipe()
				defer func() { _ = pr.Close() }()
				defer func() { _ = pw.Close() }()
				body := &observedAnthropicStreamBody{ReadCloser: pr}
				upstreamReq, err := http.NewRequest(http.MethodPost, "https://"+host+"/v1/messages", nil)
				require.NoError(t, err)
				resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: body, Request: upstreamReq}
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
				svc := &GatewayService{cfg: &config.Config{}, rateLimitService: &RateLimitService{}}
				account := &Account{ID: 1, Platform: PlatformAnthropic}
				type result struct {
					stream *streamingResult
					err    error
				}
				done := make(chan result, 1)
				go func() {
					var stream *streamingResult
					var err error
					if path == "managed" {
						stream, err = svc.handleStreamingResponse(context.Background(), resp, c, account, time.Now(), "model", "model", false)
					} else {
						stream, err = svc.handleStreamingResponseAnthropicAPIKeyPassthrough(context.Background(), resp, c, account, time.Now(), "model")
					}
					done <- result{stream, err}
				}()
				_, err = io.WriteString(pw, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":11}}}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":5}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
				require.NoError(t, err)
				// Leave the producer open: completion must not depend on network EOF.
				select {
				case got := <-done:
					require.NoError(t, got.err)
					require.Equal(t, 11, got.stream.usage.InputTokens)
					require.Equal(t, 5, got.stream.usage.OutputTokens)
					require.EqualValues(t, 1, body.closeCount.Load())
					require.Zero(t, body.active.Load(), "handler returned before the scanner left Read")
				case <-time.After(time.Second):
					t.Fatal("terminal frame did not finish the response")
				}
			})
		}
	}
}

func TestAnthropicStreamIdleTimeoutClosesBodyAndJoinsScanner(t *testing.T) {
	for _, path := range []string{"managed", "passthrough"} {
		t.Run(path, func(t *testing.T) {
			pr, pw := io.Pipe()
			defer func() { _ = pr.Close() }()
			defer func() { _ = pw.Close() }()
			body := &observedAnthropicStreamBody{ReadCloser: pr}
			resp := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: body}
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			svc := &GatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{StreamDataIntervalTimeout: 1}}, rateLimitService: &RateLimitService{}}
			var err error
			if path == "managed" {
				_, err = svc.handleStreamingResponse(context.Background(), resp, c, &Account{ID: 1}, time.Now(), "model", "model", false)
			} else {
				_, err = svc.handleStreamingResponseAnthropicAPIKeyPassthrough(context.Background(), resp, c, &Account{ID: 1}, time.Now(), "model")
			}
			require.ErrorIs(t, err, ErrUpstreamIdleTimeout)
			require.EqualValues(t, 1, body.closeCount.Load())
			require.Zero(t, body.active.Load())
		})
	}
}
