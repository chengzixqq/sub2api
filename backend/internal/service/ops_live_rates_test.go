package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type liveRatesRepoFake struct {
	OpsRepository
	counts        *OpsLiveRateCounts
	includeErrors bool
	filter        *OpsDashboardFilter
}

func (f *liveRatesRepoFake) GetLiveRateCounts(_ context.Context, filter *OpsDashboardFilter, includeErrors bool) (*OpsLiveRateCounts, error) {
	f.filter = filter
	f.includeErrors = includeErrors
	return f.counts, nil
}

func TestOpsService_GetLiveRates_WithMonitoring(t *testing.T) {
	repo := &liveRatesRepoFake{counts: &OpsLiveRateCounts{
		SuccessCount: 230, TokenConsumed: 1_200_000,
		ErrorCount: 2, ErrorCountSLA: 2, Upstream429Count: 1,
	}}
	svc := NewOpsService(repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	end := time.Date(2026, 10, 18, 1, 41, 23, 0, time.UTC)

	got, err := svc.GetLiveRates(context.Background(), OpsLiveRatesQuery{Window: 5 * time.Minute, End: end})
	require.NoError(t, err)
	require.True(t, repo.includeErrors)
	require.Equal(t, end.Add(-5*time.Minute), repo.filter.StartTime)
	require.Equal(t, 300, got.WindowSeconds)
	require.Equal(t, int64(232), got.RequestCount)
	require.InDelta(t, 46.4, got.RPM, 1e-9)
	require.InDelta(t, 240000.0, got.TPM, 1e-9)
	require.NotNil(t, got.ErrorRate)
	require.InDelta(t, 2.0/232.0, *got.ErrorRate, 1e-9)
	require.Equal(t, int64(1), *got.Upstream429Count)
	require.True(t, got.ErrorsAvailable)
	require.True(t, got.RPMIncludesErrors)
	require.True(t, got.OpsMonitoringEnabled)
}

func TestOpsService_GetLiveRates_WithoutMonitoringOmitsErrors(t *testing.T) {
	repo := &liveRatesRepoFake{counts: &OpsLiveRateCounts{SuccessCount: 60, TokenConsumed: 6000}}
	svc := NewOpsService(repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	svc.runtimeSettings.Store(&opsRuntimeSettingsSnapshot{monitoringEnabled: false})

	got, err := svc.GetLiveRates(context.Background(), OpsLiveRatesQuery{Window: time.Minute, End: time.Now()})
	require.NoError(t, err)
	require.False(t, repo.includeErrors)
	require.Equal(t, int64(60), got.RequestCount)
	require.InDelta(t, 60.0, got.RPM, 1e-9)
	require.InDelta(t, 6000.0, got.TPM, 1e-9)
	require.Nil(t, got.ErrorCount)
	require.Nil(t, got.ErrorCountSLA)
	require.Nil(t, got.ErrorRate)
	require.Nil(t, got.Upstream429Count)
	require.False(t, got.ErrorsAvailable)
	require.False(t, got.RPMIncludesErrors)
	require.False(t, got.OpsMonitoringEnabled)
}

func TestOpsService_GetLiveRates_NoTrafficIsZeroNotNull(t *testing.T) {
	repo := &liveRatesRepoFake{counts: &OpsLiveRateCounts{}}
	svc := NewOpsService(repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	got, err := svc.GetLiveRates(context.Background(), OpsLiveRatesQuery{Window: 15 * time.Minute, End: time.Now()})
	require.NoError(t, err)
	require.Zero(t, got.RPM)
	require.NotNil(t, got.ErrorRate)
	require.Zero(t, *got.ErrorRate)
	require.Zero(t, *got.ErrorCount)
}

func TestParseOpsLiveRatesWindow(t *testing.T) {
	for raw, want := range map[string]time.Duration{"": 5 * time.Minute, "1m": time.Minute, "5m": 5 * time.Minute, "15m": 15 * time.Minute} {
		got, err := ParseOpsLiveRatesWindow(raw)
		require.NoError(t, err, raw)
		require.Equal(t, want, got)
	}
	for _, raw := range []string{"2m", "1h", "abc"} {
		_, err := ParseOpsLiveRatesWindow(raw)
		require.Error(t, err, raw)
	}
}
