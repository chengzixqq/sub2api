package service

import (
	"context"
	"errors"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/stretchr/testify/require"
)

type usageScopedRPMStub struct {
	users map[int64]int
	keys  map[int64]int
	err   error
}

func (s *usageScopedRPMStub) GetUserRollingRPM(_ context.Context, id int64) (int, error) {
	return s.users[id], s.err
}
func (s *usageScopedRPMStub) GetAPIKeyRollingRPM(_ context.Context, id int64) (int, error) {
	return s.keys[id], s.err
}

type usageRuntimeSummaryRepo struct {
	UsageLogRepository
	stats *usagestats.UserDashboardStats
}

func (r *usageRuntimeSummaryRepo) GetUserDashboardStats(context.Context, int64) (*usagestats.UserDashboardStats, error) {
	return r.stats, nil
}
func (r *usageRuntimeSummaryRepo) GetAPIKeyDashboardStats(context.Context, int64) (*usagestats.UserDashboardStats, error) {
	return r.stats, nil
}

func TestUsageService_RuntimeRPMUsesUserAndAPIKeyScopes(t *testing.T) {
	original := &usagestats.UserDashboardStats{Rpm: 2000, Tpm: 100000, TotalActualCost: 12.34, ByPlatform: []usagestats.PlatformDashboardStats{{Platform: "openai"}}}
	svc := NewUsageService(&usageRuntimeSummaryRepo{stats: original}, nil, nil, nil)
	runtime := &usageScopedRPMStub{users: map[int64]int{11: 800, 12: 100}, keys: map[int64]int{21: 500, 22: 10}}
	svc.SetScopedRollingRPMCache(runtime)
	user, err := svc.GetUserDashboardStats(context.Background(), 11)
	require.NoError(t, err)
	require.EqualValues(t, 800, user.Rpm)
	require.True(t, *user.RPMAvailable)
	key, err := svc.GetAPIKeyDashboardStats(context.Background(), 21)
	require.NoError(t, err)
	require.EqualValues(t, 500, key.Rpm)
	require.True(t, *key.RPMAvailable)
	other := svc.WithUserRuntimeRPM(context.Background(), user, 12)
	require.EqualValues(t, 100, other.Rpm)
	require.EqualValues(t, 800, user.Rpm)
	require.EqualValues(t, 2000, original.Rpm)
	require.Nil(t, original.RPMAvailable)
	user.ByPlatform[0].Platform = "changed"
	require.Equal(t, "openai", original.ByPlatform[0].Platform)
	require.Equal(t, 12.34, key.TotalActualCost)
	require.EqualValues(t, 100000, key.Tpm)
	runtime.err = errors.New("redis unavailable")
	failed := svc.WithAPIKeyRuntimeRPM(context.Background(), key, 21)
	require.Zero(t, failed.Rpm)
	require.False(t, *failed.RPMAvailable)
	svc.SetScopedRollingRPMCache(nil)
	missing := svc.WithUserRuntimeRPM(context.Background(), original, 11)
	require.Zero(t, missing.Rpm)
	require.False(t, *missing.RPMAvailable)
}
