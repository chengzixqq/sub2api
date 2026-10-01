//go:build unit

package service

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type anthropicBackpressureWriter struct {
	gin.ResponseWriter
	once sync.Once
}

func (w *anthropicBackpressureWriter) delay() { w.once.Do(func() { time.Sleep(2 * time.Second) }) }
func (w *anthropicBackpressureWriter) Write(p []byte) (int, error) {
	w.delay()
	return w.ResponseWriter.Write(p)
}
func (w *anthropicBackpressureWriter) WriteString(s string) (int, error) {
	w.delay()
	return w.ResponseWriter.WriteString(s)
}

func TestAnthropicStreamSlowClientDoesNotBecomeUpstreamIdle(t *testing.T) {
	for _, path := range []string{"managed", "passthrough"} {
		t.Run(path, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var payload strings.Builder
				_, _ = payload.WriteString("data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":11}}}\n\n")
				for i := 0; i < 100; i++ {
					_, _ = fmt.Fprint(&payload, "data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"hello\"}}\n\n")
				}
				_, _ = payload.WriteString("data: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":5}}\n\ndata: {\"type\":\"message_stop\"}\n\n")
				resp := &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(payload.String()))}
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
				c.Writer = &anthropicBackpressureWriter{ResponseWriter: c.Writer}
				svc := &GatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{StreamDataIntervalTimeout: 1}}, rateLimitService: &RateLimitService{}}
				account := &Account{ID: 1}
				var result *streamingResult
				var err error
				if path == "managed" {
					result, err = svc.handleStreamingResponse(context.Background(), resp, c, account, time.Now(), "model", "model", false)
				} else {
					result, err = svc.handleStreamingResponseAnthropicAPIKeyPassthrough(context.Background(), resp, c, account, time.Now(), "model")
				}
				require.NoError(t, err, "all upstream data arrived immediately; only the client write was slow")
				require.Equal(t, 5, result.usage.OutputTokens)
			})
		})
	}
}
