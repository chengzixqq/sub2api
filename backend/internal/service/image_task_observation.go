package service

import (
	"context"
	"encoding/json"
	"hash/fnv"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// ImageTaskObservationScope is captured at submission, never reconstructed from
// an API key whose group may have changed while an image was being generated.
type ImageTaskObservationScope struct {
	GroupID  int64  `json:"group_id"`
	Platform string `json:"platform"`
	Model    string `json:"model"`
}

type imageTaskObservationState struct {
	sink  ChannelMonitorEventSink
	locks [64]sync.Mutex
}

type imageTaskObservationContextKey struct{}

// SetObservationSink is called during dependency wiring, before requests start.
func (s *ImageTaskService) SetObservationSink(sink ChannelMonitorEventSink) {
	if s != nil {
		s.observation.sink = sink
	}
}

// BeginObservation includes background work in collector shutdown accounting.
func (s *ImageTaskService) BeginObservation() func() {
	if s != nil {
		if lifecycle, ok := s.observation.sink.(interface{ Begin() func() }); ok {
			return lifecycle.Begin()
		}
	}
	return func() {}
}

// WithImageTaskObservation carries protocol facts from the background executor.
// Identity, group, platform and request timestamps always come from the task.
func WithImageTaskObservation(ctx context.Context, facts ChannelMonitorEvent) context.Context {
	return context.WithValue(ctx, imageTaskObservationContextKey{}, facts)
}

func (s *ImageTaskService) lockTerminal(id string) func() {
	h := fnv.New32a()
	_, _ = h.Write([]byte(id))
	lock := &s.observation.locks[h.Sum32()%uint32(len(s.observation.locks))]
	lock.Lock()
	return lock.Unlock
}

func (s *ImageTaskService) observeTerminal(ctx context.Context, task *ImageTaskRecord, completed time.Time) {
	if s.observation.sink == nil || task.Observation == nil || task.ObservationStartedAt.IsZero() {
		return
	}
	scope := task.Observation
	if scope.GroupID <= 0 || (scope.Platform != PlatformOpenAI && scope.Platform != PlatformGrok) {
		return
	}
	e, _ := ctx.Value(imageTaskObservationContextKey{}).(ChannelMonitorEvent)
	hasProtocolFacts := e.Outcome != ""
	e.Source = "traffic"
	e.RequestID = uuid.NewSHA1(uuid.NameSpaceOID, []byte("sub2api:async-image:"+task.ID)).String()
	e.SessionID = ""
	e.GroupID, e.UserID, e.APIKeyID = scope.GroupID, task.UserID, task.APIKeyID
	e.Platform, e.Protocol = scope.Platform, "images_async"
	e.StartedAt, e.CompletedAt = task.ObservationStartedAt, completed
	e.DurationMs = completed.Sub(e.StartedAt).Milliseconds()
	e.HTTPStatus, e.Stream = task.HTTPStatus, false
	if !hasProtocolFacts || task.Status != ImageTaskStatusCompleted {
		e.TerminalSeen = true
	}
	e.RequestedModel = scope.Model
	if !validObservationIdentifier(e.RequestedModel) || e.RequestedModel == "" {
		e.RequestedModel = "unknown"
	}
	if !validObservationIdentifier(e.ResponseModel) {
		e.ResponseModel = ""
	}
	if task.Status == ImageTaskStatusCompleted {
		if !hasProtocolFacts {
			e.OutputSeen = imageTaskHasOutput(task.Result)
			e.Outcome, e.ErrorCategory = "unknown", "empty_output"
			if e.OutputSeen {
				e.Outcome, e.ErrorCategory = "success", ""
			}
		}
	} else {
		if !hasProtocolFacts || e.Outcome == "success" {
			e.Outcome, e.ErrorCategory = "channel_error", "image_task_failed"
			if task.HTTPStatus >= 400 && task.HTTPStatus < 500 {
				e.Outcome, e.ErrorCategory = "client_error", "client_request"
			}
		}
		if task.HTTPStatus == http.StatusGatewayTimeout && e.Outcome != "cancelled" {
			e.ErrorCategory = "timeout"
		}
		if !e.AttemptsTruncated && len(e.Attempts) > 0 && (e.Outcome == "channel_error" || e.Outcome == "client_error") {
			last := e.Attempts[len(e.Attempts)-1]
			if last.HTTPStatus >= 400 && (last.Outcome == "channel_error" || last.Outcome == "client_error") {
				e.Outcome, e.ErrorCategory = last.Outcome, last.ErrorCategory
			}
		}
	}
	if len(e.Attempts) == 0 && e.Outcome != "success" {
		e.RequestedModel = "unknown"
	}
	s.observation.sink.Submit(e)
}

func imageTaskHasOutput(result json.RawMessage) bool {
	var response struct {
		Data []struct {
			URL    string `json:"url"`
			Base64 string `json:"b64_json"`
		} `json:"data"`
	}
	if json.Unmarshal(result, &response) != nil {
		return false
	}
	for _, output := range response.Data {
		if strings.TrimSpace(output.URL) != "" || strings.TrimSpace(output.Base64) != "" {
			return true
		}
	}
	return false
}
