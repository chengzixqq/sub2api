package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

type quickTopupRepoFake struct {
	AdminUserAdjustmentRepository
	params QuickTopupQuery
}

func (f *quickTopupRepoFake) QuickTopup(_ context.Context, q QuickTopupQuery) (*QuickTopupResult, error) {
	f.params = q
	return &QuickTopupResult{Recent: []QuickTopupRecentUser{}, LowBalance: []QuickTopupLowBalanceUser{}}, nil
}

type quickTopupSettingsFake struct{ values map[string]string }

func (f quickTopupSettingsFake) GetMultiple(context.Context, []string) (map[string]string, error) {
	return f.values, nil
}

func TestAdminUserAdjustmentService_QuickTopup(t *testing.T) {
	repo := &quickTopupRepoFake{}
	svc := NewAdminUserAdjustmentService(repo)
	svc.SetBalanceNotifySettings(quickTopupSettingsFake{values: map[string]string{
		SettingKeyBalanceLowNotifyEnabled:   "true",
		SettingKeyBalanceLowNotifyThreshold: "10",
	}})

	got, err := svc.QuickTopup(context.Background(), QuickTopupQuery{RecentLimit: 8, LowLimit: 5, RecentDays: 30})
	require.NoError(t, err)
	require.True(t, got.LowBalanceEnabled)
	require.True(t, repo.params.GlobalEnabled)
	require.InDelta(t, 10.0, repo.params.GlobalThreshold, 1e-9)
	require.Equal(t, 8, repo.params.RecentLimit)
	require.Equal(t, 5, repo.params.LowLimit)
	require.Equal(t, 30, repo.params.RecentDays)
}

func TestAdminUserAdjustmentService_QuickTopupGlobalOff(t *testing.T) {
	repo := &quickTopupRepoFake{}
	svc := NewAdminUserAdjustmentService(repo)
	svc.SetBalanceNotifySettings(quickTopupSettingsFake{values: map[string]string{SettingKeyBalanceLowNotifyEnabled: "false", SettingKeyBalanceLowNotifyThreshold: "10"}})

	got, err := svc.QuickTopup(context.Background(), QuickTopupQuery{RecentLimit: 8, LowLimit: 8, RecentDays: 30})
	require.NoError(t, err)
	require.False(t, got.LowBalanceEnabled)
	require.False(t, repo.params.GlobalEnabled)
}

func TestValidateQuickTopupQuery(t *testing.T) {
	require.NoError(t, ValidateQuickTopupQuery(QuickTopupQuery{RecentLimit: 0, LowLimit: 20, RecentDays: 1}))
	for _, q := range []QuickTopupQuery{
		{RecentLimit: -1, LowLimit: 8, RecentDays: 30},
		{RecentLimit: 21, LowLimit: 8, RecentDays: 30},
		{RecentLimit: 8, LowLimit: 21, RecentDays: 30},
		{RecentLimit: 8, LowLimit: 8, RecentDays: 0},
		{RecentLimit: 8, LowLimit: 8, RecentDays: 366},
	} {
		require.Error(t, ValidateQuickTopupQuery(q), q)
	}
}
