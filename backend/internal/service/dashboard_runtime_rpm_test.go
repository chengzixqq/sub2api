package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/stretchr/testify/require"
)

type dashboardRollingRPMStub struct {
	total      int
	counts     map[int64]int
	err        error
	totalCalls int
	batchCalls int
}

func (s *dashboardRollingRPMStub) GetRollingRPM(context.Context, int64) (int, error) {
	panic("dashboard must not perform per-account reads")
}

func (s *dashboardRollingRPMStub) GetRollingRPMBatch(context.Context, []int64) (map[int64]int, error) {
	s.batchCalls++
	return s.counts, s.err
}

func (s *dashboardRollingRPMStub) GetTotalRollingRPM(context.Context) (int, error) {
	s.totalCalls++
	return s.total, s.err
}

func TestDashboardService_RuntimeRPMBypassesStatsCache(t *testing.T) {
	stored := &usagestats.DashboardStats{Rpm: 2000, Tpm: 100000, TotalAccountCost: 12.34}
	data, err := json.Marshal(dashboardStatsCacheEntry{Stats: stored, UpdatedAt: time.Now().Unix()})
	require.NoError(t, err)
	cache := &dashboardCacheStub{get: func(context.Context) (string, error) { return string(data), nil }}
	svc := NewDashboardService(&usageRepoStub{}, nil, cache, nil)
	runtime := &dashboardRollingRPMStub{total: 1000}
	svc.SetRollingRPMCache(runtime)
	first, err := svc.GetDashboardStats(context.Background())
	require.NoError(t, err)
	require.EqualValues(t, 1000, first.Rpm)
	require.True(t, *first.RPMAvailable)
	runtime.total = 1500
	second, err := svc.GetDashboardStats(context.Background())
	require.NoError(t, err)
	require.EqualValues(t, 1500, second.Rpm)
	require.EqualValues(t, 100000, second.Tpm)
	require.Equal(t, 12.34, second.TotalAccountCost)
	require.EqualValues(t, 1000, first.Rpm)
	require.EqualValues(t, 2000, stored.Rpm)
	require.Nil(t, stored.RPMAvailable)
	require.Equal(t, 2, runtime.totalCalls)
	require.Zero(t, cache.setCalls)
}

func TestDashboardService_RuntimeRPMUnavailableAndScope(t *testing.T) {
	svc := NewDashboardService(nil, nil, nil, nil)
	stored := &usagestats.DashboardStats{Rpm: 2000}
	got := svc.WithRuntimeRPM(context.Background(), stored)
	require.False(t, *got.RPMAvailable)
	require.Zero(t, got.Rpm)
	runtime := &dashboardRollingRPMStub{total: 4000, counts: map[int64]int{11: 600, 12: 400}}
	svc.SetRollingRPMCache(runtime)
	got = svc.WithRuntimeRPM(WithUsageAccountScope(context.Background(), []int64{11, 12, 11}), stored)
	require.True(t, *got.RPMAvailable)
	require.EqualValues(t, 1000, got.Rpm)
	require.Zero(t, runtime.totalCalls, "restricted dashboards must not query global RPM")
	got = svc.WithRuntimeRPM(WithUsageAccountScope(context.Background(), nil), stored)
	require.True(t, *got.RPMAvailable)
	require.Zero(t, got.Rpm)
	require.Equal(t, 1, runtime.batchCalls)
	delete(runtime.counts, 12)
	got = svc.WithRuntimeRPM(WithUsageAccountScope(context.Background(), []int64{11, 12}), stored)
	require.False(t, *got.RPMAvailable, "partial counts must not pretend to be a complete total")
	require.Zero(t, got.Rpm)
	runtime.err = errors.New("redis unavailable")
	got = svc.WithRuntimeRPM(context.Background(), stored)
	require.False(t, *got.RPMAvailable)
	require.Zero(t, got.Rpm)
	require.EqualValues(t, 2000, stored.Rpm)
}

func TestDashboardService_FreshStatsUseRuntimeRPM(t *testing.T) {
	svc := NewDashboardService(&usageRepoStub{stats: &usagestats.DashboardStats{Rpm: 2000}}, nil, nil, nil)
	svc.SetRollingRPMCache(&dashboardRollingRPMStub{total: 1000})
	got, err := svc.GetDashboardStatsFresh(context.Background())
	require.NoError(t, err)
	require.EqualValues(t, 1000, got.Rpm)
	require.True(t, *got.RPMAvailable)
}
