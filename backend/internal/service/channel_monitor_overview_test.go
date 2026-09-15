package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestObservationMetrics_SeparatesReliabilityFromSuccess(t *testing.T) {
	f := ChannelMonitorObservationFact{SuccessRequests: 99, ChannelErrors: 1, ClientErrors: 20, CancelledRequests: 10, UnknownRequests: 5}
	m, h := observationMetrics(f, DefaultChannelMonitorObservationConfig(), "anthropic", 1, "claude", time.Hour, true)
	require.InDelta(t, .99, *m.ReliabilityRate, 1e-9)
	require.InDelta(t, 99.0/130, *m.SuccessRate, 1e-9)
	require.Equal(t, "healthy", h.Reliability)
	require.Equal(t, "sufficient", m.SampleState)
	require.EqualValues(t, 135, m.RequestCount)
	require.InDelta(t, 130.0/60.0, m.RPM, 1e-9)
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
