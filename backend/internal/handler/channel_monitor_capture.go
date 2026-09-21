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
		requestID := uuid.NewString()
		protocol := channelMonitorProtocol(c.Request.Method, c.Request.URL.Path)
		if protocol == "unknown" {
			// Unsupported endpoints (including WebSocket handshakes and async
			// task polling) must not be mistaken for a completed generation.
			c.Next()
			return
		}
		var userID, groupID, keyID int64
		platform := "unknown"
		// Fallback routing can mutate the API key's group; attribution belongs
		// to the authenticated request, not the last selected fallback group.
		if apiKey, ok := middleware.GetAPIKeyFromContext(c); ok && apiKey != nil {
			userID, keyID = apiKey.UserID, apiKey.ID
			if apiKey.GroupID != nil {
				groupID = *apiKey.GroupID
			}
			if apiKey.Group != nil && apiKey.Group.Platform != "" {
				platform = apiKey.Group.Platform
			}
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
			if protocol == "images" {
				if image, ok := monitorImageResult(c); ok {
					result = image
				}
			}
			// Composite routing resolves the concrete provider during c.Next. Use
			// that value after the handler completes so facts line up with usage and
			// error aggregates instead of being stranded under "composite".
			if resolved, ok := service.ResolvedTargetPlatformFromContext(c.Request.Context()); ok {
				platform = resolved
			}
			requestedModel, _ := c.Request.Context().Value(ctxkey.RequestedPublicModel).(string)
			if requestedModel == "" {
				requestedModel, _ = c.Request.Context().Value(ctxkey.Model).(string)
			}
			var attempts []service.ChannelMonitorAttempt
			var attemptsTruncated bool
			if recorder != nil {
				attempts, attemptsTruncated = recorder.Snapshot()
			}
			if c.Request.Context().Err() != nil && !result.TerminalSeen {
				result.Outcome, result.ErrorCategory = "cancelled", "client_cancelled"
			}
			result = channelMonitorFinalizeAttempts(result, attempts, attemptsTruncated, time.Now().UTC())
			// Requests rejected before any upstream routing must not create an
			// unbounded aggregate dimension from arbitrary client model strings.
			if len(attempts) == 0 && result.Outcome != "success" {
				requestedModel = "unknown"
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

func channelMonitorFinalizeAttempts(result channelMonitorParseResult, attempts []service.ChannelMonitorAttempt, truncated bool, completed time.Time) channelMonitorParseResult {
	if truncated || len(attempts) == 0 {
		return result
	}
	last := &attempts[len(attempts)-1]
	// Only the final recorded attempt can supply the downstream result. Earlier
	// failed attempts must not overwrite a later stream failure or cancellation.
	if last.HTTPStatus >= 400 && (result.Outcome == "channel_error" || result.Outcome == "client_error") {
		result.Outcome, result.ErrorCategory = last.Outcome, last.ErrorCategory
	}
	if last.HTTPStatus >= 200 && last.HTTPStatus < 300 && last.Outcome == "unknown" {
		if result.Outcome != "success" || result.TerminalSeen {
			last.Outcome, last.ErrorCategory = result.Outcome, result.ErrorCategory
			last.CompletedAt = completed
			if !last.StartedAt.IsZero() {
				last.DurationMs = completed.Sub(last.StartedAt).Milliseconds()
			}
		}
	}
	return result
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
	if w.protocol == "images" {
		return
	}
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
	case strings.HasSuffix(p, "/images/generations"), strings.HasSuffix(p, "/images/edits"):
		return "images"
	case strings.HasSuffix(p, ":generatecontent") || strings.HasSuffix(p, ":streamgeneratecontent"):
		return "gemini"
	default:
		return "unknown"
	}
}
