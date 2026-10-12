package service

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestChannelMonitorV2Config_RejectsInvalidHealthAndIgnoredCategoriesBeforeSave(t *testing.T) {
	for name, change := range map[string]func(*ChannelMonitorV2Config){
		"NaN":                      func(c *ChannelMonitorV2Config) { c.HealthThresholds.WarningErrorRate = math.NaN() },
		"Inf":                      func(c *ChannelMonitorV2Config) { c.HealthThresholds.CacheWeight = math.Inf(1) },
		"negative weight":          func(c *ChannelMonitorV2Config) { c.HealthThresholds.CacheWeight = -.1 },
		"weight total":             func(c *ChannelMonitorV2Config) { c.HealthThresholds.TTFTWeight = .9 },
		"rate above one":           func(c *ChannelMonitorV2Config) { c.HealthThresholds.CriticalErrorRate = 1.1 },
		"rate zero":                func(c *ChannelMonitorV2Config) { c.HealthThresholds.WarningErrorRate = 0 },
		"sample overflow":          func(c *ChannelMonitorV2Config) { c.HealthThresholds.MinimumSample = 10001 },
		"latency order":            func(c *ChannelMonitorV2Config) { c.HealthThresholds.WarningTTFTMs = 1 },
		"latency bound":            func(c *ChannelMonitorV2Config) { c.HealthThresholds.CriticalTTFTMs = 86400001 },
		"cache order":              func(c *ChannelMonitorV2Config) { c.HealthThresholds.CriticalCacheRate = .5 },
		"unknown ignored category": func(c *ChannelMonitorV2Config) { c.IgnoredErrorCategories = []string{"transport_typo"} },
	} {
		t.Run(name, func(t *testing.T) {
			cfg := ChannelMonitorV2Config{RefreshIntervalSeconds: 60, HealthThresholds: DefaultChannelMonitorV2HealthThresholds()}
			change(&cfg)
			_, err := NewChannelMonitorV2Service(nil).UpdateConfig(context.Background(), cfg, 1, 1)
			require.ErrorIs(t, err, ErrChannelMonitorV2InvalidConfig)
		})
	}
	thresholds := DefaultChannelMonitorV2HealthThresholds()
	thresholds.CacheWeight = 0
	thresholds.ErrorWeight = .8
	require.NoError(t, ValidateChannelMonitorV2HealthThresholds(thresholds))
	thresholds.ErrorWeight = -1
	thresholds.WarningErrorRate = math.NaN()
	thresholds.CriticalErrorRate = math.Inf(1)
	normalized := NormalizeChannelMonitorV2HealthThresholds(thresholds)
	require.Zero(t, normalized.ErrorWeight)
	require.Equal(t, .05, normalized.WarningErrorRate)
	require.Equal(t, .2, normalized.CriticalErrorRate)
}

func TestObservationCurrentStatus_ConfiguredFreshnessControlsEvidence(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Minute)
	cfg := DefaultChannelMonitorObservationConfig()
	facts := []ChannelMonitorObservationFact{{Source: "traffic", BucketStart: now.Add(-4 * time.Minute), SuccessRequests: 100}}
	require.Equal(t, "healthy", observationCurrentStatus(facts, cfg, "openai", 7, "gpt-test", now, "healthy").State)
	cfg.TrafficFreshnessSeconds = 120
	require.Equal(t, "stale", observationCurrentStatus(facts, cfg, "openai", 7, "gpt-test", now, "healthy").State)
	cfg.TrafficFreshnessSeconds = 600
	require.Equal(t, "healthy", observationCurrentStatus(facts, cfg, "openai", 7, "gpt-test", now, "healthy").State)
}

func TestObservationLegacyCollector_StaleAllowanceFollowsAggregationInterval(t *testing.T) {
	now := time.Now().UTC()
	require.False(t, observationLegacyCollectorStale(now.Add(-4*time.Minute), now, 300))
	require.True(t, observationLegacyCollectorStale(now.Add(-6*time.Minute-time.Second), now, 300))
	require.False(t, observationLegacyCollectorStale(now.Add(-2*time.Minute), now, 60))
	require.True(t, observationLegacyCollectorStale(now.Add(-2*time.Minute-time.Second), now, 60))
	require.True(t, observationLegacyCollectorStale(time.Time{}, now, 300))
}

func TestObservationProbeEvidence_ConfiguredFailureCountAndFreshness(t *testing.T) {
	now := time.Now().UTC()
	failed := false
	account := int64(1)
	older, latest := now.Add(-29*time.Minute), now.Add(-14*time.Minute)
	policy := DefaultChannelMonitorObservationConfig().ProbePolicy()
	evidence := ChannelMonitorProbeEvidence{MaxAge: policy.EvidenceWindow(), HistoryAge: policy.HistoryWindow(), FailureThreshold: policy.FailureThreshold,
		Targets: []ChannelMonitorProbeTarget{{ID: 1, GroupID: 7, Model: "m", Enabled: true}},
		Runs:    []ChannelMonitorProbeRun{{TargetID: 1, GroupID: 7, Model: "m", AccountID: &account, Success: &failed, CompletedAt: &older}, {TargetID: 1, GroupID: 7, Model: "m", AccountID: &account, Success: &failed, CompletedAt: &latest}}}
	status := observationProbeEvidence(evidence, 7, "m", now)
	require.Equal(t, "critical", status.Status)
	require.Equal(t, 2, status.ConsecutiveFailures)
	evidence.FailureThreshold = 3
	require.Equal(t, "warning", observationProbeEvidence(evidence, 7, "m", now).Status)
	evidence.FailureThreshold = 1
	evidence.Runs = evidence.Runs[1:]
	require.Equal(t, "critical", observationProbeEvidence(evidence, 7, "m", now).Status)
	require.Nil(t, observationProbeEvidence(evidence, 7, "m", now.Add(7*time.Minute)))
}

func TestObservationProbeEvidence_EditedTargetDoesNotReuseOldProtocolOrModel(t *testing.T) {
	now := time.Now().UTC()
	completed := now.Add(-time.Minute)
	failed := false
	account := int64(1)
	evidence := ChannelMonitorProbeEvidence{FailureThreshold: 1, Targets: []ChannelMonitorProbeTarget{{ID: 1, GroupID: 7, Model: "m", Protocol: "openai_responses", Enabled: true}}, Runs: []ChannelMonitorProbeRun{{TargetID: 1, GroupID: 7, Model: "m", Protocol: "openai_chat", AccountID: &account, Success: &failed, CompletedAt: &completed}}}
	require.Nil(t, observationProbeEvidence(evidence, 7, "m", now))
	evidence.Runs[0].Protocol = "openai_responses"
	evidence.Runs[0].Model = "old-model"
	require.Nil(t, observationProbeEvidence(evidence, 7, "", now))
	evidence.Runs[0].Model = "m"
	require.Equal(t, "critical", observationProbeEvidence(evidence, 7, "m", now).Status)
}
