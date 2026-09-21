package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type imageTaskObservationSink struct {
	mu     sync.Mutex
	events []ChannelMonitorEvent
}

func (s *imageTaskObservationSink) Submit(event ChannelMonitorEvent) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, event)
	return true
}

func TestImageTaskObservation_TerminalOnlyAndImmutableScope(t *testing.T) {
	store := &imageTaskMemoryStore{}
	svc := NewImageTaskService(store)
	sink := &imageTaskObservationSink{}
	svc.SetObservationSink(sink)
	scope := &ImageTaskObservationScope{GroupID: 3, Platform: PlatformOpenAI, Model: "gpt-image-1"}
	owner := ImageTaskOwner{UserID: 7, APIKeyID: 9, Observation: scope}
	task, err := svc.Create(context.Background(), owner)
	require.NoError(t, err)
	scope.GroupID = 99
	_, err = svc.Get(context.Background(), owner, task.ID)
	require.NoError(t, err)
	require.Empty(t, sink.events)
	ctx := WithImageTaskObservation(context.Background(), ChannelMonitorEvent{ResponseModel: "gpt-image-1", InputTokens: 30, Attempts: []ChannelMonitorAttempt{{AccountID: 12, Outcome: "success"}}})
	require.NoError(t, svc.Complete(ctx, task.ID, http.StatusOK, json.RawMessage(`{"data":[{"url":"https://example.test/image.png"}]}`)))
	require.Len(t, sink.events, 1)
	event := sink.events[0]
	require.NoError(t, uuid.Validate(event.RequestID))
	require.Equal(t, int64(3), event.GroupID)
	require.Equal(t, "gpt-image-1", event.RequestedModel)
	require.Equal(t, "images_async", event.Protocol)
	require.Equal(t, "success", event.Outcome)
	require.True(t, event.OutputSeen)
	require.True(t, event.TerminalSeen)
	require.Equal(t, int64(30), event.InputTokens)
	require.Len(t, event.Attempts, 1)
	_, err = NormalizeChannelMonitorEvent(event, time.Now())
	require.NoError(t, err)
	require.NoError(t, svc.Fail(context.Background(), task.ID, http.StatusBadGateway, json.RawMessage(`{"type":"api_error"}`)))
	require.Len(t, sink.events, 1)
	require.Equal(t, ImageTaskStatusCompleted, store.task.Status)
	public, err := svc.Get(context.Background(), owner, task.ID)
	require.NoError(t, err)
	raw, err := json.Marshal(public)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "group_id")
	require.NotContains(t, string(raw), "observation")
}

func TestImageTaskObservation_EmptyOutputAndPersistenceFailure(t *testing.T) {
	store := &imageTaskMemoryStore{}
	svc := NewImageTaskService(store)
	sink := &imageTaskObservationSink{}
	svc.SetObservationSink(sink)
	task, err := svc.Create(context.Background(), ImageTaskOwner{UserID: 7, APIKeyID: 9, Observation: &ImageTaskObservationScope{GroupID: 3, Platform: PlatformOpenAI, Model: "gpt-image-1"}})
	require.NoError(t, err)
	store.saveErr = errors.New("redis unavailable")
	require.Error(t, svc.Complete(context.Background(), task.ID, http.StatusOK, json.RawMessage(`{"data":[]}`)))
	require.Empty(t, sink.events)
	store.saveErr = nil
	require.NoError(t, svc.Complete(context.Background(), task.ID, http.StatusOK, json.RawMessage(`{"data":[]}`)))
	require.Len(t, sink.events, 1)
	require.Equal(t, "unknown", sink.events[0].Outcome)
	require.Equal(t, "empty_output", sink.events[0].ErrorCategory)
	require.Equal(t, "unknown", sink.events[0].RequestedModel)
}

func TestImageTaskObservation_ConcurrentTerminalIsRecordedOnce(t *testing.T) {
	store := &imageTaskMemoryStore{}
	svc := NewImageTaskService(store)
	sink := &imageTaskObservationSink{}
	svc.SetObservationSink(sink)
	task, err := svc.Create(context.Background(), ImageTaskOwner{UserID: 7, APIKeyID: 9, Observation: &ImageTaskObservationScope{GroupID: 3, Platform: PlatformOpenAI, Model: "gpt-image-1"}})
	require.NoError(t, err)
	var wg sync.WaitGroup
	errors := make(chan error, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errors <- svc.Complete(context.Background(), task.ID, http.StatusOK, json.RawMessage(`{"data":[{"b64_json":"aW1hZ2U="}]}`))
		}()
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		require.NoError(t, err)
	}
	require.Len(t, sink.events, 1)
	require.Equal(t, "success", sink.events[0].Outcome)
}

func TestImageTaskObservation_PartialAdapterResultIsNotSuccess(t *testing.T) {
	store := &imageTaskMemoryStore{}
	svc := NewImageTaskService(store)
	sink := &imageTaskObservationSink{}
	svc.SetObservationSink(sink)
	task, err := svc.Create(context.Background(), ImageTaskOwner{UserID: 7, APIKeyID: 9, Observation: &ImageTaskObservationScope{GroupID: 3, Platform: PlatformOpenAI, Model: "gpt-image-1"}})
	require.NoError(t, err)
	ctx := WithImageTaskObservation(context.Background(), ChannelMonitorEvent{Outcome: "unknown", ErrorCategory: "incomplete_terminal", OutputSeen: true, TerminalSeen: false})
	require.NoError(t, svc.Complete(ctx, task.ID, http.StatusOK, json.RawMessage(`{"data":[{"url":"https://example.test/partial.png"}]}`)))
	require.Len(t, sink.events, 1)
	require.Equal(t, "unknown", sink.events[0].Outcome)
	require.Equal(t, "incomplete_terminal", sink.events[0].ErrorCategory)
	require.False(t, sink.events[0].TerminalSeen)
}

func TestImageTaskObservation_EarlierRetryDoesNotOverrideCancellation(t *testing.T) {
	store := &imageTaskMemoryStore{}
	svc := NewImageTaskService(store)
	sink := &imageTaskObservationSink{}
	svc.SetObservationSink(sink)
	task, err := svc.Create(context.Background(), ImageTaskOwner{UserID: 7, APIKeyID: 9, Observation: &ImageTaskObservationScope{GroupID: 3, Platform: PlatformOpenAI, Model: "gpt-image-1"}})
	require.NoError(t, err)
	ctx := WithImageTaskObservation(context.Background(), ChannelMonitorEvent{Outcome: "cancelled", ErrorCategory: "client_cancelled", Attempts: []ChannelMonitorAttempt{{Sequence: 1, Outcome: "channel_error", HTTPStatus: 429, ErrorCategory: "upstream_capacity"}, {Sequence: 2, Outcome: "cancelled", HTTPStatus: 200, ErrorCategory: "client_cancelled"}}})
	require.NoError(t, svc.Fail(ctx, task.ID, http.StatusGatewayTimeout, json.RawMessage(`{"type":"timeout_error"}`)))
	require.Len(t, sink.events, 1)
	require.Equal(t, "cancelled", sink.events[0].Outcome)
	require.Equal(t, "client_cancelled", sink.events[0].ErrorCategory)
}

func TestImageTaskObservation_FailureCategoriesAndLegacyTasks(t *testing.T) {
	for _, tc := range []struct {
		name     string
		status   int
		attempts []ChannelMonitorAttempt
		outcome  string
		category string
	}{
		{"timeout", 504, nil, "channel_error", "timeout"},
		{"upstream", 502, []ChannelMonitorAttempt{{Sequence: 1, AccountID: 12, Outcome: "channel_error", HTTPStatus: 429, ErrorCategory: "upstream_capacity"}}, "channel_error", "upstream_capacity"},
		{"rejected", 400, nil, "client_error", "client_request"},
		{"upstream_validation", 502, []ChannelMonitorAttempt{{Sequence: 1, AccountID: 12, Outcome: "client_error", HTTPStatus: 400, ErrorCategory: "client_request"}}, "client_error", "client_request"},
		{"upstream_size", 502, []ChannelMonitorAttempt{{Sequence: 1, AccountID: 12, Outcome: "client_error", HTTPStatus: 413, ErrorCategory: "request_too_large"}}, "client_error", "request_too_large"},
		{"last_retry_validation", 502, []ChannelMonitorAttempt{{Sequence: 1, AccountID: 12, Outcome: "channel_error", HTTPStatus: 429, ErrorCategory: "upstream_capacity"}, {Sequence: 2, AccountID: 13, Outcome: "client_error", HTTPStatus: 422, ErrorCategory: "client_request"}}, "client_error", "client_request"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &imageTaskMemoryStore{}
			svc := NewImageTaskService(store)
			sink := &imageTaskObservationSink{}
			svc.SetObservationSink(sink)
			task, err := svc.Create(context.Background(), ImageTaskOwner{UserID: 7, APIKeyID: 9, Observation: &ImageTaskObservationScope{GroupID: 3, Platform: PlatformGrok, Model: "grok-imagine-image"}})
			require.NoError(t, err)
			ctx := WithImageTaskObservation(context.Background(), ChannelMonitorEvent{Attempts: tc.attempts})
			require.NoError(t, svc.Fail(ctx, task.ID, tc.status, json.RawMessage(`{"type":"api_error"}`)))
			require.Len(t, sink.events, 1)
			require.Equal(t, tc.outcome, sink.events[0].Outcome)
			require.Equal(t, tc.category, sink.events[0].ErrorCategory)
		})
	}
	store := &imageTaskMemoryStore{}
	svc := NewImageTaskService(store)
	sink := &imageTaskObservationSink{}
	svc.SetObservationSink(sink)
	task, err := svc.Create(context.Background(), ImageTaskOwner{UserID: 7, APIKeyID: 9})
	require.NoError(t, err)
	require.NoError(t, svc.Fail(context.Background(), task.ID, 500, json.RawMessage(`{"type":"api_error"}`)))
	require.Empty(t, sink.events, "old tasks must not guess their group from a current API key")
}

type imageTaskMemoryStore struct {
	task    *ImageTaskRecord
	ttl     time.Duration
	saveErr error
	getErr  error
}

func (s *imageTaskMemoryStore) Save(_ context.Context, task *ImageTaskRecord, ttl time.Duration) error {
	if s.saveErr != nil {
		return s.saveErr
	}
	copy := *task
	s.task = &copy
	s.ttl = ttl
	return nil
}

func (s *imageTaskMemoryStore) Get(_ context.Context, _ string) (*ImageTaskRecord, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	if s.task == nil {
		return nil, ErrImageTaskNotFound
	}
	copy := *s.task
	return &copy, nil
}

func TestImageTaskServiceLifecycleAndOwnership(t *testing.T) {
	store := &imageTaskMemoryStore{}
	svc := NewImageTaskServiceWithOptions(store, time.Hour, 10*time.Minute)
	owner := ImageTaskOwner{UserID: 7, APIKeyID: 9}

	created, err := svc.Create(context.Background(), owner)
	require.NoError(t, err)
	require.Equal(t, ImageTaskStatusProcessing, created.Status)
	require.Equal(t, created.ID, created.TaskID)
	require.Equal(t, "image.generation.task", created.Object)
	require.Equal(t, time.Hour, store.ttl)
	require.Equal(t, owner.UserID, store.task.UserID)
	require.Equal(t, owner.APIKeyID, store.task.APIKeyID)

	_, err = svc.Get(context.Background(), ImageTaskOwner{UserID: 7, APIKeyID: 10}, created.ID)
	require.ErrorIs(t, err, ErrImageTaskNotFound)

	result := json.RawMessage(`{"created":123,"data":[{"url":"https://example.test/image.png"}]}`)
	require.NoError(t, svc.Complete(context.Background(), created.ID, http.StatusOK, result))

	completed, err := svc.Get(context.Background(), owner, created.ID)
	require.NoError(t, err)
	require.Equal(t, ImageTaskStatusCompleted, completed.Status)
	require.Equal(t, http.StatusOK, completed.HTTPStatus)
	require.Equal(t, "https://example.test/image.png", completed.ImageURL)
	require.JSONEq(t, string(result), string(completed.Result))
	require.NotNil(t, completed.CompletedAt)
}

func TestImageTaskServiceInvalidResultBecomesFailed(t *testing.T) {
	store := &imageTaskMemoryStore{}
	svc := NewImageTaskServiceWithOptions(store, time.Hour, time.Minute)
	created, err := svc.Create(context.Background(), ImageTaskOwner{UserID: 1, APIKeyID: 2})
	require.NoError(t, err)

	require.NoError(t, svc.Complete(context.Background(), created.ID, http.StatusOK, json.RawMessage(`not-json`)))
	got, err := svc.Get(context.Background(), ImageTaskOwner{UserID: 1, APIKeyID: 2}, created.ID)
	require.NoError(t, err)
	require.Equal(t, ImageTaskStatusFailed, got.Status)
	require.Equal(t, http.StatusBadGateway, got.HTTPStatus)
	require.Contains(t, string(got.Error), "non-JSON")
}

func TestImageTaskServiceMapsStoreFailures(t *testing.T) {
	store := &imageTaskMemoryStore{saveErr: errors.New("redis down")}
	svc := NewImageTaskService(store)

	_, err := svc.Create(context.Background(), ImageTaskOwner{UserID: 1, APIKeyID: 2})
	require.ErrorIs(t, err, ErrImageTaskUnavailable)
}
