package handler

import (
	"net/http"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

func beginGatewayRequestTiming(c *gin.Context) func() {
	if c == nil || c.Request == nil {
		return func() {}
	}
	ctx, timing := service.WithGatewayRequestTiming(c.Request.Context())
	c.Request = c.Request.WithContext(ctx)
	original := c.Writer
	writer := &gatewayTimingResponseWriter{ResponseWriter: original, timing: timing}
	c.Writer = writer
	return func() {
		if c.Writer == writer {
			c.Writer = original
		}
		snapshot, ok := service.PublishGatewayTiming(c)
		if !ok {
			return
		}
		fields := []zap.Field{
			zap.Any("timing", snapshot),
			zap.Int("wire_status", c.Writer.Status()),
			zap.Bool("client_canceled", c.Request.Context().Err() != nil),
		}
		if accountID := c.GetInt64(opsAccountIDKey); accountID > 0 {
			fields = append(fields, zap.Int64("account_id", accountID))
		}
		if apiKey := getOpsAPIKey(c); apiKey != nil {
			fields = append(fields, zap.Int64("user_id", apiKey.UserID), zap.Int64("api_key_id", apiKey.ID))
		}
		requestLogger(c, "handler.gateway.messages").Info("gateway.request_timing", fields...)
	}
}

type gatewayTimingResponseWriter struct {
	gin.ResponseWriter
	timing *service.GatewayRequestTiming
}

func (w *gatewayTimingResponseWriter) Write(data []byte) (int, error) {
	started := time.Now()
	n, err := w.ResponseWriter.Write(data)
	w.timing.Record(service.GatewayTimingDownstreamWrite, time.Since(started))
	return n, err
}

func (w *gatewayTimingResponseWriter) WriteString(data string) (int, error) {
	started := time.Now()
	n, err := w.ResponseWriter.WriteString(data)
	w.timing.Record(service.GatewayTimingDownstreamWrite, time.Since(started))
	return n, err
}

func (w *gatewayTimingResponseWriter) Flush() {
	started := time.Now()
	w.ResponseWriter.Flush()
	w.timing.Record(service.GatewayTimingDownstreamWrite, time.Since(started))
}

func (w *gatewayTimingResponseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}
