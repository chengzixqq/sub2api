//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestQuickTopup_RecentAndLowBalance(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	mk := func(balance float64, status string) *service.User {
		return mustCreateUser(t, client, &service.User{Email: "qt-" + uuid.NewString() + "@example.com", Balance: balance, Status: status})
	}
	recentA := mk(12.4, service.StatusActive)
	recentB := mk(80, service.StatusActive)
	disabled := mk(1, service.StatusDisabled)
	lowGlobal := mk(3.1, service.StatusActive)
	lowOwn := mk(40, service.StatusActive)
	lowPct := mk(9, service.StatusActive)
	healthy := mk(500, service.StatusActive)
	ids := []int64{recentA.ID, recentB.ID, disabled.ID, lowGlobal.ID, lowOwn.ID, lowPct.ID, healthy.ID}
	t.Cleanup(func() {
		for _, id := range ids {
			_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM admin_user_adjustments WHERE user_id = $1", id)
			_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM users WHERE id = $1", id)
		}
	})
	// lowOwn has a personal fixed threshold of 50; lowPct a 10% threshold of 100 recharged.
	_, err := integrationDB.ExecContext(ctx, `UPDATE users SET balance_notify_threshold = 50, balance_notify_threshold_type = 'fixed' WHERE id = $1`, lowOwn.ID)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `UPDATE users SET balance_notify_threshold = 10, balance_notify_threshold_type = 'percentage', total_recharged = 100 WHERE id = $1`, lowPct.ID)
	require.NoError(t, err)

	repo := NewAdminUserAdjustmentRepository(client)
	now := time.Now()
	write := func(userID int64, delta string, kind string, at time.Time) {
		require.NoError(t, repo.CreateBatch(ctx, []service.AdminUserAdjustmentWrite{{
			ActionID: uuid.New(), Kind: kind, Operation: "add", Delta: delta, UserID: userID, Source: "admin", CreatedAt: at,
		}}))
	}
	write(recentA.ID, "5", service.AdjustmentKindBalance, now.Add(-3*time.Hour))
	write(recentA.ID, "20", service.AdjustmentKindBalance, now.Add(-time.Hour)) // latest for A
	write(recentB.ID, "1", service.AdjustmentKindBalance, now.Add(-2*time.Hour))
	write(disabled.ID, "1", service.AdjustmentKindBalance, now.Add(-time.Minute))
	write(healthy.ID, "1", service.AdjustmentKindConcurrency, now.Add(-time.Minute)) // not a balance change
	write(healthy.ID, "1", service.AdjustmentKindBalance, now.AddDate(0, 0, -40))    // too old

	quick, ok := repo.(service.QuickTopupRepository)
	require.True(t, ok)
	// No cap here: the shared integration database keeps zero-balance users from
	// other tests, which would crowd these fixtures out of a short list.
	got, err := quick.QuickTopup(ctx, service.QuickTopupQuery{RecentLimit: 100000, LowLimit: 100000, RecentDays: 30, GlobalEnabled: true, GlobalThreshold: 10})
	require.NoError(t, err)

	recentIDs := []int64{}
	for _, r := range got.Recent {
		if qtContains(ids, r.UserID) {
			recentIDs = append(recentIDs, r.UserID)
		}
	}
	require.Equal(t, []int64{recentA.ID, recentB.ID}, recentIDs, "deduplicated, newest first, disabled and old excluded")
	for _, r := range got.Recent {
		if r.UserID == recentA.ID {
			require.Equal(t, "20.00000000", r.LastDelta)
			require.InDelta(t, 12.4, r.Balance, 1e-9)
		}
	}

	low := map[int64]service.QuickTopupLowBalanceUser{}
	for _, l := range got.LowBalance {
		if qtContains(ids, l.UserID) {
			low[l.UserID] = l
		}
	}
	require.Contains(t, low, lowGlobal.ID)
	require.Equal(t, "global", low[lowGlobal.ID].ThresholdSource)
	require.InDelta(t, 10.0, low[lowGlobal.ID].EffectiveThreshold, 1e-9)
	require.Contains(t, low, lowOwn.ID)
	require.Equal(t, "user", low[lowOwn.ID].ThresholdSource)
	require.InDelta(t, 50.0, low[lowOwn.ID].EffectiveThreshold, 1e-9)
	require.Contains(t, low, lowPct.ID)
	require.Equal(t, "percentage", low[lowPct.ID].ThresholdType)
	require.InDelta(t, 10.0, low[lowPct.ID].EffectiveThreshold, 1e-9)
	require.NotContains(t, low, healthy.ID)
	require.NotContains(t, low, disabled.ID)

	off, err := quick.QuickTopup(ctx, service.QuickTopupQuery{RecentLimit: 0, LowLimit: 100000, RecentDays: 30, GlobalEnabled: false, GlobalThreshold: 10})
	require.NoError(t, err)
	offIDs := map[int64]bool{}
	for _, l := range off.LowBalance {
		offIDs[l.UserID] = true
	}
	require.False(t, offIDs[lowGlobal.ID], "global reminders off: only users with their own threshold count")
	require.True(t, offIDs[lowOwn.ID])
	require.Empty(t, off.Recent)
}

func qtContains(ids []int64, id int64) bool {
	for _, v := range ids {
		if v == id {
			return true
		}
	}
	return false
}
