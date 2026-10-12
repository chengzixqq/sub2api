package service

import (
	"encoding/json"
	"math"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestChannelMonitorObservation_ConfigRuntimeLimitsAndLegacyJSON(t *testing.T) {
	cfg := DefaultChannelMonitorObservationConfig()
	require.NoError(t, json.Unmarshal([]byte(`{"mode":"live","refresh_interval_seconds":120}`), &cfg))
	require.Equal(t, 900, cfg.ProbeIntervalSeconds)
	require.Equal(t, 20, cfg.ProbeTimeoutSeconds)
	require.Equal(t, 96, cfg.ProbeTargetDailyLimit)
	require.Equal(t, 1000, cfg.ProbeGlobalDailyLimit)
	require.False(t, cfg.ProbeEnabled)
	require.NoError(t, ValidateChannelMonitorObservationConfig(cfg))
	for name, change := range map[string]func(*ChannelMonitorObservationConfig){
		"refresh below":           func(c *ChannelMonitorObservationConfig) { c.RefreshIntervalSeconds = 29 },
		"refresh above":           func(c *ChannelMonitorObservationConfig) { c.RefreshIntervalSeconds = 301 },
		"retention":               func(c *ChannelMonitorObservationConfig) { c.DetailRetentionHours = 48 },
		"interval zero":           func(c *ChannelMonitorObservationConfig) { c.ProbeIntervalSeconds = 0 },
		"interval above":          func(c *ChannelMonitorObservationConfig) { c.ProbeIntervalSeconds = 3601 },
		"timeout below":           func(c *ChannelMonitorObservationConfig) { c.ProbeTimeoutSeconds = 4 },
		"timeout above":           func(c *ChannelMonitorObservationConfig) { c.ProbeTimeoutSeconds = 61 },
		"target budget zero":      func(c *ChannelMonitorObservationConfig) { c.ProbeTargetDailyLimit = 0 },
		"target budget above":     func(c *ChannelMonitorObservationConfig) { c.ProbeTargetDailyLimit = 1441 },
		"global budget zero":      func(c *ChannelMonitorObservationConfig) { c.ProbeGlobalDailyLimit = 0 },
		"global budget above":     func(c *ChannelMonitorObservationConfig) { c.ProbeGlobalDailyLimit = 100001 },
		"NaN healthy":             func(c *ChannelMonitorObservationConfig) { c.HealthyReliability = math.NaN() },
		"Inf warning":             func(c *ChannelMonitorObservationConfig) { c.WarningReliability = math.Inf(1) },
		"traffic freshness below": func(c *ChannelMonitorObservationConfig) { c.TrafficFreshnessSeconds = 59 },
		"traffic freshness above": func(c *ChannelMonitorObservationConfig) { c.TrafficFreshnessSeconds = 3601 },
		"failure threshold below": func(c *ChannelMonitorObservationConfig) { c.ProbeFailureThreshold = 0 },
		"failure threshold above": func(c *ChannelMonitorObservationConfig) { c.ProbeFailureThreshold = 11 },
		"quota stale below":       func(c *ChannelMonitorObservationConfig) { c.QuotaStaleSeconds = 299 },
		"quota stale above":       func(c *ChannelMonitorObservationConfig) { c.QuotaStaleSeconds = 3601 },
	} {
		t.Run(name, func(t *testing.T) { c := cfg; change(&c); require.Error(t, ValidateChannelMonitorObservationConfig(c)) })
	}
	for _, edge := range []int{30, 300} {
		cfg.RefreshIntervalSeconds = edge
		require.NoError(t, ValidateChannelMonitorObservationConfig(cfg))
	}
}

func TestChannelMonitorObservation_OverrideSpecificityIsOrderIndependent(t *testing.T) {
	cfg := DefaultChannelMonitorObservationConfig()
	cfg.Overrides = []ChannelMonitorObservationOverride{
		{Platform: "openai", GroupID: 7, Model: "gpt-test", WarningTTFTMs: 8000, CriticalTTFTMs: 9000},
		{Model: "gpt-test", WarningTTFTMs: 6000, CriticalTTFTMs: 7000},
		{GroupID: 7, WarningTTFTMs: 4000, CriticalTTFTMs: 5000},
		{Platform: "openai", WarningTTFTMs: 2000, CriticalTTFTMs: 3000},
	}
	require.NoError(t, ValidateChannelMonitorObservationConfig(cfg))
	for i := 0; i < 2; i++ {
		warning, critical := cfg.LatencyThresholds("openai", 7, "gpt-test")
		require.EqualValues(t, 8000, warning)
		require.EqualValues(t, 9000, critical)
		warning, _ = cfg.LatencyThresholds("openai", 8, "gpt-test")
		require.EqualValues(t, 6000, warning)
		warning, _ = cfg.LatencyThresholds("openai", 7, "other")
		require.EqualValues(t, 4000, warning)
		warning, _ = cfg.LatencyThresholds("anthropic", 8, "other")
		require.EqualValues(t, 3000, warning)
		for a, b := 0, len(cfg.Overrides)-1; a < b; a, b = a+1, b-1 {
			cfg.Overrides[a], cfg.Overrides[b] = cfg.Overrides[b], cfg.Overrides[a]
		}
	}
}

func TestChannelMonitorObservation_ConfigDefaultsAndOverride(t *testing.T) {
	cfg := DefaultChannelMonitorObservationConfig()
	require.Equal(t, "shadow", cfg.Mode)
	require.Equal(t, 24, cfg.DetailRetentionHours)
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

func TestChannelMonitorObservation_RejectsEventsOutsideDedupHorizon(t *testing.T) {
	now := time.Now().UTC()
	e := ChannelMonitorEvent{RequestID: uuid.NewString(), StartedAt: now.Add(-25 * time.Hour), CompletedAt: now.Add(-24*time.Hour - time.Second), Platform: "openai", Protocol: "openai_chat", Outcome: "success"}
	_, err := NormalizeChannelMonitorEvent(e, now)
	require.Error(t, err)
}
