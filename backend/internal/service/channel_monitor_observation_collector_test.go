package service

import (
	"context"
	"sync"
	"testing"
	"time"

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
