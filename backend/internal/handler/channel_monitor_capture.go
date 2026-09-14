package handler

import (
	"bytes"
	"encoding/json"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"io"
	"net/http"
	"strings"
	"time"
)

// ChannelMonitorCaptureMiddleware accounts for in-flight requests without reading
// request bodies or credentials. Protocol adapters submit terminal events when
// their lifecycle is complete; unsupported paths remain visible as in-flight.
func ChannelMonitorCaptureMiddleware(collector *service.ChannelMonitorCollector) gin.HandlerFunc {
	return func(c *gin.Context) {
		if collector == nil {
			c.Next()
			return
		}
		started := time.Now().UTC()
		var reqModel string
		if c.Request.Body != nil {
			body, err := io.ReadAll(io.LimitReader(c.Request.Body, channelMonitorCaptureLimit))
			if err == nil {
				c.Request.Body.Close()
				c.Request.Body = io.NopCloser(bytes.NewReader(body))
				var v struct {
					Model string `json:"model"`
				}
				_ = json.Unmarshal(body, &v)
				reqModel = monitorIdentifier(v.Model)
			}
		}
		writer := &channelMonitorCaptureWriter{ResponseWriter: c.Writer}
		c.Writer = writer
		end := collector.Begin()
		c.Next()
		end()
		status := writer.status
		if status == 0 {
			status = c.Writer.Status()
		}
		protocol := channelMonitorProtocol(c.Request.URL.Path)
		stream := strings.Contains(strings.ToLower(writer.Header().Get("Content-Type")), "text/event-stream")
		parser := newChannelMonitorParser(protocol, stream)
		parser.feed(writer.body.Bytes())
		result := parser.result(status)
		var userID, groupID, keyID int64
		platform := "unknown"
		if apiKey, ok := middleware.GetAPIKeyFromContext(c); ok && apiKey != nil {
			userID, keyID = apiKey.UserID, apiKey.ID
			if apiKey.GroupID != nil {
				groupID = *apiKey.GroupID
			}
			if apiKey.Group != nil && apiKey.Group.Platform != "" {
				platform = apiKey.Group.Platform
			}
		}
		requestID, _ := c.Request.Context().Value(ctxkey.ClientRequestID).(string)
		if _, err := uuid.Parse(requestID); err != nil {
			requestID = uuid.NewString()
		}
		collector.Submit(service.ChannelMonitorEvent{RequestID: requestID, StartedAt: started, CompletedAt: time.Now().UTC(), Platform: platform, GroupID: groupID, UserID: userID, APIKeyID: keyID, RequestedModel: reqModel, ResponseModel: result.Model, Protocol: protocol, Outcome: result.Outcome, ErrorCategory: result.ErrorCategory, HTTPStatus: status, Stream: stream, OutputSeen: result.OutputSeen, TerminalSeen: result.TerminalSeen, DurationMs: time.Since(started).Milliseconds(), InputTokens: result.InputTokens, OutputTokens: result.OutputTokens, CacheReadTokens: result.CacheReadTokens, CacheCreationTokens: result.CacheWriteTokens})
	}
}

type channelMonitorCaptureWriter struct {
	gin.ResponseWriter
	body   bytes.Buffer
	status int
}

func (w *channelMonitorCaptureWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}
func (w *channelMonitorCaptureWriter) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	if w.body.Len() < channelMonitorCaptureLimit {
		sample := data
		n := channelMonitorCaptureLimit - w.body.Len()
		if len(sample) > n {
			sample = sample[:n]
		}
		_, _ = w.body.Write(sample)
	}
	return w.ResponseWriter.Write(data)
}
func (w *channelMonitorCaptureWriter) WriteString(s string) (int, error) { return w.Write([]byte(s)) }
func channelMonitorProtocol(path string) string {
	p := strings.ToLower(path)
	switch {
	case strings.Contains(p, "messages"):
		return "anthropic"
	case strings.Contains(p, "chat/completions"):
		return "openai_chat"
	case strings.Contains(p, "responses"):
		return "openai_responses"
	case strings.Contains(p, "generatecontent"):
		return "gemini"
	default:
		return "unknown"
	}
}
