package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestObservationMetrics_SeparatesReliabilityFromSuccess(t *testing.T) {
	f := ChannelMonitorObservationFact{SuccessRequests: 99, ChannelErrors: 1, ClientErrors: 20, CancelledRequests: 10, UnknownRequests: 5}
	m, h := observationMetrics(f, DefaultChannelMonitorObservationConfig(), "anthropic", 1, "claude", time.Hour, true)
	require.InDelta(t, 99.0/105, *m.ReliabilityRate, 1e-9)
	require.InDelta(t, 99.0/135, *m.SuccessRate, 1e-9)
	require.Equal(t, "critical", h.Reliability)
	require.Equal(t, "sufficient", m.SampleState)
	require.EqualValues(t, 6, m.ChannelErrors)
	require.Zero(t, m.UnknownRequests)
	require.EqualValues(t, 135, m.RequestCount)
	require.InDelta(t, 135.0/60.0, m.RPM, 1e-9)
}

func TestObservationMetrics_UnknownOnlyRequestsAreFailures(t *testing.T) {
	cfg := DefaultChannelMonitorObservationConfig()
	for _, tc := range []struct {
		name        string
		count       int64
		wantSamples string
		wantHealth  string
	}{
		{name: "below minimum", count: cfg.MinimumSample - 1, wantSamples: "insufficient", wantHealth: "unknown"},
		{name: "at minimum", count: cfg.MinimumSample, wantSamples: "sufficient", wantHealth: "critical"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fact := ChannelMonitorObservationFact{UnknownRequests: tc.count, ErrorCategories: map[string]int64{"stream_incomplete": tc.count}}
			admin, health := observationMetrics(fact, cfg, "anthropic", 7, "claude", time.Minute, true)
			require.NotNil(t, admin.ReliabilityRate)
			require.Zero(t, *admin.ReliabilityRate)
			require.NotNil(t, admin.SuccessRate)
			require.Zero(t, *admin.SuccessRate)
			require.Equal(t, tc.wantSamples, admin.SampleState)
			require.Equal(t, tc.wantHealth, health.Reliability)
			require.Equal(t, tc.count, admin.ChannelErrors)
			require.Equal(t, tc.count, admin.RequestCount)
			require.Zero(t, admin.UnknownRequests)
			require.Equal(t, float64(tc.count), admin.RPM)
			require.Equal(t, fact.ErrorCategories, admin.ErrorCategories)
			require.Equal(t, tc.count, fact.UnknownRequests, "the stored observation keeps its diagnostic outcome")

			public, publicHealth := observationMetrics(fact, cfg, "anthropic", 7, "claude", time.Minute, false)
			require.Equal(t, admin.ReliabilityRate, public.ReliabilityRate)
			require.Equal(t, admin.SuccessRate, public.SuccessRate)
			require.Equal(t, admin.SampleState, public.SampleState)
			require.Equal(t, health, publicHealth)
			require.Zero(t, public.ChannelErrors)
			require.Zero(t, public.UnknownRequests)
			require.Zero(t, public.RequestCount)
			require.Zero(t, public.RPM)
			require.Nil(t, public.ErrorCategories)
		})
	}
}

func TestObservationMetrics_RedactionDoesNotDestroySampleState(t *testing.T) {
	f := ChannelMonitorObservationFact{SuccessRequests: 95, ChannelErrors: 5, CacheReadTokens: 10, InputTokens: 10,
		TTFTCount: 100, TTFTSumMs: 1100000, TTFTHistogram: map[int64]int64{11000: 100}, PhaseSumMs: map[string]int64{"queue": 42}, PhaseCounts: map[string]int64{"queue": 1}}
	m, h := observationMetrics(f, DefaultChannelMonitorObservationConfig(), "anthropic", 1, "claude", time.Hour, false)
	require.Zero(t, m.RequestCount)
	require.Zero(t, m.TTFT.SampleCount)
	require.Zero(t, m.RPM)
	require.Nil(t, m.PhaseAvgMs)
	require.Equal(t, "sufficient", m.SampleState)
	require.Equal(t, "warning", h.Reliability)
	require.Equal(t, "critical", h.Latency)
	require.Equal(t, .5, *m.CacheRate)
}

func TestObservationMetrics_EmptyDoesNotBecomeHealthy(t *testing.T) {
	m, h := observationMetrics(ChannelMonitorObservationFact{}, DefaultChannelMonitorObservationConfig(), "openai", 1, "", time.Hour, false)
	require.Nil(t, m.ReliabilityRate)
	require.Nil(t, m.SuccessRate)
	require.Nil(t, m.CacheRate)
	require.Equal(t, "no_samples", m.SampleState)
	require.Equal(t, "unknown", h.Reliability)
}
