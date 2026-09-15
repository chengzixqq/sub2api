package handler

import (
	"context"
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
		protocol := channelMonitorProtocol(c.Request.Method, c.Request.URL.Path)
		if protocol == "unknown" {
			// Unsupported endpoints (including WebSocket handshakes and async
			// task polling) must not be mistaken for a completed generation.
			c.Next()
			return
		}
		var recorder *service.ChannelMonitorAttemptRecorder
		var requestContext context.Context
		requestContext, recorder = service.WithChannelMonitorAttempts(c.Request.Context())
		c.Request = c.Request.WithContext(requestContext)
		writer := &channelMonitorCaptureWriter{ResponseWriter: c.Writer, protocol: protocol}
		c.Writer = writer
		end := collector.Begin()
		submitted := false
		finalize := func(panicked bool) {
			if submitted {
				return
			}
			submitted = true
			status := writer.status
			if status == 0 {
				status = c.Writer.Status()
			}
			result, stream := channelMonitorWriterResult(writer, protocol, status)
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
			// Composite routing resolves the concrete provider during c.Next. Use
			// that value after the handler completes so facts line up with usage and
			// error aggregates instead of being stranded under "composite".
			if resolved, ok := service.ResolvedTargetPlatformFromContext(c.Request.Context()); ok {
				platform = resolved
			}
			requestID := uuid.NewString()
			requestedModel, _ := c.Request.Context().Value(ctxkey.RequestedPublicModel).(string)
			if requestedModel == "" {
				requestedModel, _ = c.Request.Context().Value(ctxkey.Model).(string)
			}
			var attempts []service.ChannelMonitorAttempt
			var attemptsTruncated bool
			if recorder != nil {
				attempts, attemptsTruncated = recorder.Snapshot()
			}
			// Prefer the last observed upstream status when the gateway wrapped an
			// upstream error (for example upstream 413 becoming downstream 502).
			// Without an attempt record a 4xx remains a client-side reject.
			if result.Outcome != "success" {
				for i := len(attempts) - 1; i >= 0; i-- {
					if attempts[i].HTTPStatus >= 400 {
						result.Outcome = "channel_error"
						if attempts[i].ErrorCategory != "" {
							result.ErrorCategory = attempts[i].ErrorCategory
						}
						break
					}
				}
			}
			if panicked && result.Outcome == "unknown" {
				result.Outcome = "unknown"
				result.ErrorCategory = "handler_panic"
			}
			phaseMs, firstOutputMs := channelMonitorTimingFacts(c.Request.Context())
			collector.Submit(service.ChannelMonitorEvent{RequestID: requestID, StartedAt: started, CompletedAt: time.Now().UTC(), Platform: platform, GroupID: groupID, UserID: userID, APIKeyID: keyID, RequestedModel: monitorIdentifier(requestedModel), ResponseModel: result.Model, Protocol: protocol, Outcome: result.Outcome, ErrorCategory: result.ErrorCategory, HTTPStatus: status, Stream: stream, OutputSeen: result.OutputSeen, TerminalSeen: result.TerminalSeen, PhaseMs: phaseMs, FirstOutputMs: firstOutputMs, DurationMs: time.Since(started).Milliseconds(), InputTokens: result.InputTokens, OutputTokens: result.OutputTokens, CacheReadTokens: result.CacheReadTokens, CacheCreationTokens: result.CacheWriteTokens, Attempts: attempts, AttemptsTruncated: attemptsTruncated})
		}
		defer func() {
			if recovered := recover(); recovered != nil {
				finalize(true)
				end()
				panic(recovered)
			}
			finalize(false)
			end()
		}()
		c.Next()
	}
}

func channelMonitorTimingFacts(ctx context.Context) (map[string]int64, *int64) {
	timing := service.GatewayRequestTimingFromContext(ctx)
	if timing == nil {
		return nil, nil
	}
	snapshot := timing.Snapshot()
	phases := make(map[string]int64, len(snapshot.PhaseMS)+1)
	for name, value := range snapshot.PhaseMS {
		if value >= 0 {
			phases[name] = value
		}
	}
	if snapshot.TotalMS >= 0 {
		phases["total"] = snapshot.TotalMS
	}
	var first *int64
	if snapshot.FirstUpstreamByteMS != nil && *snapshot.FirstUpstreamByteMS >= 0 {
		value := *snapshot.FirstUpstreamByteMS
		first = &value
	}
	if len(phases) == 0 {
		phases = nil
	}
	return phases, first
}

func channelMonitorWriterResult(writer *channelMonitorCaptureWriter, protocol string, status int) (channelMonitorParseResult, bool) {
	if writer == nil {
		parser := newChannelMonitorParser(protocol, false)
		return parser.result(status), false
	}
	parser := writer.parser
	stream := strings.Contains(strings.ToLower(writer.Header().Get("Content-Type")), "text/event-stream")
	if parser != nil {
		stream = parser.stream
	}
	if parser == nil {
		parser = newChannelMonitorParser(protocol, stream)
	}
	return parser.result(status), stream
}

type channelMonitorCaptureWriter struct {
	gin.ResponseWriter
	protocol string
	parser   *channelMonitorParser
	status   int
}

func (w *channelMonitorCaptureWriter) WriteHeader(code int) {
	if w.status != 0 {
		return
	}
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}
func (w *channelMonitorCaptureWriter) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	w.observe(data)
	return w.ResponseWriter.Write(data)
}
func (w *channelMonitorCaptureWriter) WriteString(s string) (int, error) { return w.Write([]byte(s)) }
func (w *channelMonitorCaptureWriter) Flush() {
	if w.parser == nil {
		w.observe(nil)
	}
	w.ResponseWriter.Flush()
}
func (w *channelMonitorCaptureWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *channelMonitorCaptureWriter) ReadFrom(r io.Reader) (int64, error) {
	return io.Copy(struct{ io.Writer }{w}, r)
}
func (w *channelMonitorCaptureWriter) observe(data []byte) {
	if w.parser == nil {
		stream := strings.Contains(strings.ToLower(w.Header().Get("Content-Type")), "text/event-stream")
		w.parser = newChannelMonitorParser(w.protocol, stream)
	}
	if len(data) > 0 {
		w.parser.feed(data)
	}
}
func channelMonitorProtocol(method, path string) string {
	if !strings.EqualFold(method, http.MethodPost) {
		return "unknown"
	}
	p := strings.ToLower(strings.TrimRight(path, "/"))
	switch {
	case strings.HasSuffix(p, "/messages") && !strings.HasSuffix(p, "/count_tokens"):
		return "anthropic"
	case strings.HasSuffix(p, "/chat/completions"):
		return "openai_chat"
	case strings.HasSuffix(p, "/responses"):
		return "openai_responses"
	case strings.HasSuffix(p, ":generatecontent") || strings.HasSuffix(p, ":streamgeneratecontent"):
		return "gemini"
	default:
		return "unknown"
	}
}
