package service

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestChannelMonitorObservation_ConfigDefaultsAndOverride(t *testing.T) {
	cfg := DefaultChannelMonitorObservationConfig()
	require.Equal(t, "shadow", cfg.Mode)
	require.Equal(t, 72, cfg.DetailRetentionHours)
	cfg.Overrides = []ChannelMonitorObservationOverride{
		{Platform: "anthropic", WarningTTFTMs: 4000, CriticalTTFTMs: 12000},
		{GroupID: 7, WarningTTFTMs: 5000, CriticalTTFTMs: 15000},
		{Model: "claude", WarningTTFTMs: 6000, CriticalTTFTMs: 18000},
	}
	require.NoError(t, ValidateChannelMonitorObservationConfig(cfg))
	warn, critical := cfg.LatencyThresholds("anthropic", 7, "claude")
	require.EqualValues(t, 6000, warn)
	require.EqualValues(t, 18000, critical)
	cfg.Overrides = append(cfg.Overrides, cfg.Overrides[0])
	require.Error(t, ValidateChannelMonitorObservationConfig(cfg))
}

func TestChannelMonitorObservation_EventValidationAndCopy(t *testing.T) {
	now := time.Now().UTC()
	e := ChannelMonitorEvent{RequestID: uuid.NewString(), StartedAt: now.Add(-time.Second), CompletedAt: now,
		Platform: "anthropic", GroupID: 7, RequestedModel: "claude-sonnet", Protocol: "anthropic_sse",
		Outcome: "success", TerminalSeen: true, OutputSeen: true, PhaseMs: map[string]int64{"queue": 3, "https://secret.example/key": 1}}
	copy, err := NormalizeChannelMonitorEvent(e, now)
	require.NoError(t, err)
	require.NotContains(t, copy.PhaseMs, "https://secret.example/key")
	e.PhaseMs["queue"] = 100
	require.EqualValues(t, 3, copy.PhaseMs["queue"])
	e.RequestedModel = "https://user:password@secret.example/v1"
	_, err = NormalizeChannelMonitorEvent(e, now)
	require.Error(t, err)
}

func TestChannelMonitorObservation_FactCountsFreeSuccessAndRecoveredRetry(t *testing.T) {
	now := time.Now().UTC()
	e := ChannelMonitorEvent{RequestID: uuid.NewString(), StartedAt: now.Add(-time.Second), CompletedAt: now,
		Platform: "anthropic", GroupID: 7, Outcome: "success", DurationMs: 1000, FirstOutputMs: int64Pointer(0),
		Attempts: []ChannelMonitorAttempt{{Sequence: 1, Outcome: "channel_error"}, {Sequence: 2, Outcome: "success"}}}
	fact := ChannelMonitorObservationFactFromEvent(e, time.Minute)
	require.EqualValues(t, 1, fact.SuccessRequests)
	require.Zero(t, fact.ChannelErrors)
	require.EqualValues(t, 1, fact.RetryRecoveredRequests)
	require.EqualValues(t, 2, fact.AttemptCount)
	require.EqualValues(t, 1, fact.TTFTHistogram[0])
	combined := ChannelMonitorObservationFact{}
	combined.Add(fact)
	combined.Add(fact)
	require.EqualValues(t, 2, combined.SuccessRequests)
	require.EqualValues(t, 2, combined.TTFTHistogram[0])
}

func int64Pointer(n int64) *int64 { return &n }
