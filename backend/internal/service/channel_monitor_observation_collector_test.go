package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type collectorObservationRepo struct {
	mu        sync.Mutex
	sessions  []ChannelMonitorObservationSession
	gaps      []ChannelMonitorObservationGap
	storeRuns int
}

func (r *collectorObservationRepo) GetConfig(context.Context) (*ChannelMonitorObservationConfig, error) {
	cfg := DefaultChannelMonitorObservationConfig()
	return &cfg, nil
}
func (r *collectorObservationRepo) UpdateConfig(context.Context, ChannelMonitorObservationConfig, int) (*ChannelMonitorObservationConfig, error) {
	cfg := DefaultChannelMonitorObservationConfig()
	return &cfg, nil
}
func (r *collectorObservationRepo) StoreBatch(context.Context, []ChannelMonitorEvent) error {
	r.mu.Lock()
	r.storeRuns++
	r.mu.Unlock()
	return nil
}
func (r *collectorObservationRepo) RecordGaps(_ context.Context, _ string, gaps []ChannelMonitorObservationGap) error {
	r.mu.Lock()
	r.gaps = append(r.gaps, gaps...)
	r.mu.Unlock()
	return nil
}
func (r *collectorObservationRepo) Heartbeat(_ context.Context, session ChannelMonitorObservationSession) error {
	r.mu.Lock()
	r.sessions = append(r.sessions, session)
	r.mu.Unlock()
	return nil
}
func (r *collectorObservationRepo) Maintain(context.Context, time.Time) error { return nil }
func (r *collectorObservationRepo) Query(context.Context, ChannelMonitorV2Filter) (*ChannelMonitorObservationSnapshot, error) {
	return &ChannelMonitorObservationSnapshot{}, nil
}

func TestChannelMonitorCollectorStopClosesSessionWithInFlightGap(t *testing.T) {
	repo := &collectorObservationRepo{}
	collector := NewChannelMonitorCollector(repo, ChannelMonitorCollectorOptions{FlushInterval: time.Millisecond, HeartbeatInterval: time.Hour})
	collector.Start()
	endRequest := collector.Begin()
	require.NoError(t, collector.Stop(context.Background()))
	endRequest()

	repo.mu.Lock()
	sessions := append([]ChannelMonitorObservationSession(nil), repo.sessions...)
	gaps := append([]ChannelMonitorObservationGap(nil), repo.gaps...)
	repo.mu.Unlock()
	require.NotEmpty(t, sessions)
	last := sessions[len(sessions)-1]
	require.NotNil(t, last.EndedAt)
	require.Zero(t, last.InFlight)
	var found bool
	for _, gap := range gaps {
		if gap.Reason == "shutdown_loss" && gap.LostEvents == 1 {
			found = true
		}
	}
	require.True(t, found)
}

type failingCollectorRepo struct{ collectorObservationRepo }

func (*failingCollectorRepo) StoreBatch(context.Context, []ChannelMonitorEvent) error {
	return errors.New("test write failure")
}

func TestChannelMonitorCollector_WriteFailurePreservesOrderedGapAndState(t *testing.T) {
	repo := &failingCollectorRepo{}
	c := NewChannelMonitorCollector(repo, ChannelMonitorCollectorOptions{FlushInterval: time.Hour, HeartbeatInterval: time.Hour})
	c.Start()
	now := time.Now().UTC()
	for _, end := range []time.Time{now, now.Add(-time.Second)} {
		require.True(t, c.Submit(ChannelMonitorEvent{RequestID: uuid.NewString(), StartedAt: end.Add(-time.Second), CompletedAt: end, Platform: "openai", Protocol: "openai_chat", Outcome: "success"}))
	}
	require.NoError(t, c.Stop(context.Background()))
	repo.mu.Lock()
	defer repo.mu.Unlock()
	require.Len(t, repo.gaps, 1)
	require.False(t, repo.gaps[0].EndedAt.Before(repo.gaps[0].StartedAt))
	require.Equal(t, "write_failed", repo.sessions[len(repo.sessions)-1].LastWriteError)
	require.Nil(t, repo.sessions[len(repo.sessions)-1].LastIngestedAt)
}

func TestChannelMonitorCollector_ProgressTracksFlushedPrefixWithPendingEvents(t *testing.T) {
	c := NewChannelMonitorCollector(&collectorObservationRepo{}, ChannelMonitorCollectorOptions{})
	now := time.Now().UTC()
	c.updateProgress(now, 0)
	require.NotNil(t, c.session.DataThrough)
	require.Equal(t, now, *c.session.DataThrough)
	c.queue <- ChannelMonitorEvent{}
	c.updateProgress(now.Add(time.Minute), 1)
	require.Equal(t, int64(2), c.session.PendingEvents)
	require.Equal(t, now, *c.session.DataThrough)
	c.advanceDataThrough(now.Add(30 * time.Second))
	require.Equal(t, now.Add(30*time.Second), *c.session.DataThrough)
	<-c.queue
	c.session.LastWriteError = "write_failed"
	c.updateProgress(now.Add(2*time.Minute), 0)
	require.Zero(t, c.session.PendingEvents)
	require.Equal(t, now.Add(30*time.Second), *c.session.DataThrough)
	c.session.LastWriteError = ""
	c.updateProgress(now.Add(3*time.Minute), 0)
	require.Equal(t, now.Add(3*time.Minute), *c.session.DataThrough)
}

type gatedCollectorHeartbeatRepo struct {
	collectorObservationRepo
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (r *gatedCollectorHeartbeatRepo) Heartbeat(ctx context.Context, session ChannelMonitorObservationSession) error {
	r.once.Do(func() { close(r.entered); <-r.release })
	return r.collectorObservationRepo.Heartbeat(ctx, session)
}

func TestChannelMonitorCollector_QueueOverflowIsAnExplicitGap(t *testing.T) {
	repo := &gatedCollectorHeartbeatRepo{entered: make(chan struct{}), release: make(chan struct{})}
	c := NewChannelMonitorCollector(repo, ChannelMonitorCollectorOptions{QueueSize: 1, FlushInterval: time.Hour, HeartbeatInterval: time.Hour})
	c.Start()
	<-repo.entered
	now := time.Now().UTC()
	event := ChannelMonitorEvent{RequestID: uuid.NewString(), StartedAt: now.Add(-time.Second), CompletedAt: now, Platform: "openai", Protocol: "openai_chat", Outcome: "success"}
	first, second := c.Submit(event), c.Submit(event)
	close(repo.release)
	require.NoError(t, c.Stop(context.Background()))
	require.True(t, first)
	require.False(t, second)
	repo.mu.Lock()
	defer repo.mu.Unlock()
	require.Len(t, repo.gaps, 1)
	require.Equal(t, "queue_loss", repo.gaps[0].Reason)
	require.Equal(t, int64(1), repo.gaps[0].LostEvents)
	require.Equal(t, int64(1), repo.sessions[len(repo.sessions)-1].DroppedEvents)
	require.Zero(t, repo.sessions[len(repo.sessions)-1].PendingEvents)
}
