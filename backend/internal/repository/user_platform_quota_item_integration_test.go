//go:build integration

package repository

import (
	"context"
	"errors"
	"math"
	"net/http"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/schema/mixins"
	"github.com/Wei-Shaw/sub2api/ent/userplatformquota"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// mustCreateQuotaItemUser 建一个唯一邮箱的用户，测试结束时硬删该用户的配额行与用户本身。
func mustCreateQuotaItemUser(t *testing.T) (*dbent.Client, int64) {
	t.Helper()
	client := testEntClient(t)
	u := mustCreateUser(t, client, &service.User{Email: "quota-item-" + uuid.NewString() + "@example.com"})
	t.Cleanup(func() {
		ctx := context.Background()
		if _, err := client.ExecContext(ctx, "DELETE FROM user_platform_quotas WHERE user_id = $1", u.ID); err != nil {
			t.Logf("cleanup user_platform_quotas for user %d: %v", u.ID, err)
		}
		if _, err := client.ExecContext(ctx, "DELETE FROM users WHERE id = $1", u.ID); err != nil {
			t.Logf("cleanup user %d: %v", u.ID, err)
		}
	})
	return client, u.ID
}

func quotaItemF(v float64) *float64 { return &v }

// quotaItemRows 返回 (user, platform) 的全部行（含软删）。
func quotaItemRows(t *testing.T, client *dbent.Client, userID int64, platform string) []*dbent.UserPlatformQuota {
	t.Helper()
	rows, err := client.UserPlatformQuota.Query().
		Where(userplatformquota.UserIDEQ(userID), userplatformquota.PlatformEQ(platform)).
		All(mixins.SkipSoftDelete(context.Background()))
	require.NoError(t, err)
	return rows
}

func activeQuotaItemRows(rows []*dbent.UserPlatformQuota) int {
	n := 0
	for _, r := range rows {
		if r.DeletedAt == nil {
			n++
		}
	}
	return n
}

var quotaItemUsedAt = time.Date(2026, 5, 22, 10, 0, 0, 0, time.UTC)

func TestUpsertOneForUser_OnlyTouchesThatPlatform(t *testing.T) {
	ctx := context.Background()
	client, userID := mustCreateQuotaItemUser(t)
	repo := NewUserPlatformQuotaRepository(client)

	require.NoError(t, repo.UpsertForUser(ctx, userID, []UserPlatformQuotaRecord{
		{UserID: userID, Platform: "anthropic", DailyLimitUSD: quotaItemF(10)},
		{UserID: userID, Platform: "zhipu", DailyLimitUSD: quotaItemF(5)},
	}))
	require.NoError(t, repo.IncrementUsageWithReset(ctx, userID, "anthropic", 1.25, quotaItemUsedAt))
	require.NoError(t, repo.IncrementUsageWithReset(ctx, userID, "zhipu", 3.5, quotaItemUsedAt))

	current, previous, err := repo.UpsertOneForUser(ctx, userID, UserPlatformQuotaRecord{
		Platform: "anthropic", DailyLimitUSD: quotaItemF(20), WeeklyLimitUSD: quotaItemF(50),
	})
	require.NoError(t, err)
	require.NotNil(t, previous)
	require.InDelta(t, 10, *previous.DailyLimitUSD, 1e-9)
	require.Nil(t, previous.WeeklyLimitUSD)
	require.InDelta(t, 1.25, previous.DailyUsageUSD, 1e-9, "previous carries the usage at the time")
	require.NotNil(t, current)
	require.Equal(t, userID, current.UserID)
	require.Equal(t, "anthropic", current.Platform)
	require.InDelta(t, 20, *current.DailyLimitUSD, 1e-9)
	require.InDelta(t, 50, *current.WeeklyLimitUSD, 1e-9)
	require.Nil(t, current.MonthlyLimitUSD)
	require.InDelta(t, 1.25, current.DailyUsageUSD, 1e-9, "usage is preserved")
	require.NotNil(t, current.DailyWindowStart, "window start is preserved")

	// 缺字段等于 null：这是替换该平台的三档限额，不是合并补丁。
	current, previous, err = repo.UpsertOneForUser(ctx, userID, UserPlatformQuotaRecord{
		Platform: "anthropic", MonthlyLimitUSD: quotaItemF(300),
	})
	require.NoError(t, err)
	require.InDelta(t, 20, *previous.DailyLimitUSD, 1e-9)
	require.InDelta(t, 50, *previous.WeeklyLimitUSD, 1e-9)
	require.Nil(t, current.DailyLimitUSD)
	require.Nil(t, current.WeeklyLimitUSD)
	require.InDelta(t, 300, *current.MonthlyLimitUSD, 1e-9)
	require.InDelta(t, 1.25, current.DailyUsageUSD, 1e-9)

	rows, err := repo.ListByUser(ctx, userID)
	require.NoError(t, err)
	require.Len(t, rows, 2, "the other platform is untouched")
	zhipu, err := repo.GetByUserPlatform(ctx, userID, "zhipu")
	require.NoError(t, err)
	require.NotNil(t, zhipu)
	require.InDelta(t, 5, *zhipu.DailyLimitUSD, 1e-9)
	require.InDelta(t, 3.5, zhipu.DailyUsageUSD, 1e-9)
	require.Len(t, quotaItemRows(t, client, userID, "anthropic"), 1, "updates in place, no history row")
}

func TestUpsertOneForUser_InsertsWhenMissing(t *testing.T) {
	ctx := context.Background()
	client, userID := mustCreateQuotaItemUser(t)
	repo := NewUserPlatformQuotaRepository(client)

	// 留一条带用量的软删历史：单平台写入不复活它，新行从零起算。
	require.NoError(t, repo.UpsertForUser(ctx, userID, []UserPlatformQuotaRecord{
		{UserID: userID, Platform: "gemini", DailyLimitUSD: quotaItemF(9)},
	}))
	require.NoError(t, repo.IncrementUsageWithReset(ctx, userID, "gemini", 4, quotaItemUsedAt))
	require.NoError(t, repo.UpsertForUser(ctx, userID, nil))

	current, previous, err := repo.UpsertOneForUser(ctx, userID, UserPlatformQuotaRecord{
		Platform: "gemini", DailyLimitUSD: quotaItemF(0),
	})
	require.NoError(t, err)
	require.Nil(t, previous)
	require.NotNil(t, current)
	require.NotNil(t, current.DailyLimitUSD)
	require.InDelta(t, 0, *current.DailyLimitUSD, 1e-9, "0 means the platform is blocked and is stored")
	require.InDelta(t, 0, current.DailyUsageUSD, 1e-9)
	require.Nil(t, current.DailyWindowStart)

	rows := quotaItemRows(t, client, userID, "gemini")
	require.Len(t, rows, 2)
	require.Equal(t, 1, activeQuotaItemRows(rows))
}

// 限额列是 decimal(20,10)，只能存绝对值小于 1e10 的数：handler 的 validateQuotaLimitFields
// 按这个上限拦截（maxQuotaLimitUSD），这里钉住两侧边界，列类型变了要同步改校验。
func TestUpsertOneForUser_LimitColumnRange(t *testing.T) {
	ctx := context.Background()
	client, userID := mustCreateQuotaItemUser(t)
	repo := NewUserPlatformQuotaRepository(client)

	largest := math.Nextafter(1e10, 0)
	current, _, err := repo.UpsertOneForUser(ctx, userID, UserPlatformQuotaRecord{
		Platform: "anthropic", DailyLimitUSD: quotaItemF(largest),
	})
	require.NoError(t, err, "the largest accepted limit fits the column")
	require.NotNil(t, current)
	require.InDelta(t, largest, *current.DailyLimitUSD, 1e-5)

	_, _, err = repo.UpsertOneForUser(ctx, userID, UserPlatformQuotaRecord{
		Platform: "anthropic", DailyLimitUSD: quotaItemF(1e10),
	})
	require.Error(t, err, "1e10 overflows decimal(20,10); the handler must reject it first")
	stored, err := repo.GetByUserPlatform(ctx, userID, "anthropic")
	require.NoError(t, err)
	require.NotNil(t, stored)
	require.InDelta(t, largest, *stored.DailyLimitUSD, 1e-5, "the failed write is rolled back")
}

func TestUpsertOneForUser_AllNullSoftDeletes(t *testing.T) {
	ctx := context.Background()
	client, userID := mustCreateQuotaItemUser(t)
	repo := NewUserPlatformQuotaRepository(client)

	require.NoError(t, repo.UpsertForUser(ctx, userID, []UserPlatformQuotaRecord{
		{UserID: userID, Platform: "anthropic", DailyLimitUSD: quotaItemF(10)},
		{UserID: userID, Platform: "openai", DailyLimitUSD: quotaItemF(8)},
	}))

	current, previous, err := repo.UpsertOneForUser(ctx, userID, UserPlatformQuotaRecord{Platform: "anthropic"})
	require.NoError(t, err)
	require.Nil(t, current)
	require.NotNil(t, previous)
	require.InDelta(t, 10, *previous.DailyLimitUSD, 1e-9)

	gone, err := repo.GetByUserPlatform(ctx, userID, "anthropic")
	require.NoError(t, err)
	require.Nil(t, gone)
	openai, err := repo.GetByUserPlatform(ctx, userID, "openai")
	require.NoError(t, err)
	require.NotNil(t, openai, "other platforms are untouched")

	current, previous, err = repo.UpsertOneForUser(ctx, userID, UserPlatformQuotaRecord{Platform: "anthropic"})
	require.NoError(t, err, "clearing an unconfigured platform is a no-op")
	require.Nil(t, current)
	require.Nil(t, previous)
	require.Len(t, quotaItemRows(t, client, userID, "anthropic"), 1)
}

func TestSoftDeleteOne_ReturnsPreviousAndKeepsOthers(t *testing.T) {
	ctx := context.Background()
	client, userID := mustCreateQuotaItemUser(t)
	repo := NewUserPlatformQuotaRepository(client)

	require.NoError(t, repo.UpsertForUser(ctx, userID, []UserPlatformQuotaRecord{
		{UserID: userID, Platform: "anthropic", DailyLimitUSD: quotaItemF(10)},
		{UserID: userID, Platform: "zhipu", DailyLimitUSD: quotaItemF(5), MonthlyLimitUSD: quotaItemF(60)},
	}))
	require.NoError(t, repo.IncrementUsageWithReset(ctx, userID, "zhipu", 3.5, quotaItemUsedAt))

	previous, err := repo.SoftDeleteOne(ctx, userID, "zhipu")
	require.NoError(t, err)
	require.NotNil(t, previous)
	require.Equal(t, "zhipu", previous.Platform)
	require.InDelta(t, 5, *previous.DailyLimitUSD, 1e-9)
	require.InDelta(t, 60, *previous.MonthlyLimitUSD, 1e-9)
	require.InDelta(t, 3.5, previous.DailyUsageUSD, 1e-9)

	rows, err := repo.ListByUser(ctx, userID)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "anthropic", rows[0].Platform)

	history := quotaItemRows(t, client, userID, "zhipu")
	require.Len(t, history, 1, "soft delete keeps the row")
	require.NotNil(t, history[0].DeletedAt)
	require.InDelta(t, 3.5, history[0].DailyUsageUsd, 1e-9)
}

func TestSoftDeleteOne_NotFound(t *testing.T) {
	ctx := context.Background()
	client, userID := mustCreateQuotaItemUser(t)
	repo := NewUserPlatformQuotaRepository(client)

	previous, err := repo.SoftDeleteOne(ctx, userID, "anthropic")
	require.ErrorIs(t, err, ErrUserPlatformQuotaNotFound)
	require.Nil(t, previous)

	for _, platform := range []string{"foo", "composite", ""} {
		_, err = repo.SoftDeleteOne(ctx, userID, platform)
		require.Error(t, err, platform)
		require.NotErrorIs(t, err, ErrUserPlatformQuotaNotFound, "unknown platforms fail validation before any query")
	}
	_, _, err = repo.UpsertOneForUser(ctx, userID, UserPlatformQuotaRecord{Platform: "foo", DailyLimitUSD: quotaItemF(1)})
	require.Error(t, err)
	_, err = repo.RestoreLatestDeleted(ctx, userID, "foo")
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrUserPlatformQuotaNotFound)
}

func TestRestoreLatestDeleted_KeepsUsage(t *testing.T) {
	ctx := context.Background()
	client, userID := mustCreateQuotaItemUser(t)
	repo := NewUserPlatformQuotaRepository(client)

	require.NoError(t, repo.UpsertForUser(ctx, userID, []UserPlatformQuotaRecord{
		{UserID: userID, Platform: "anthropic", DailyLimitUSD: quotaItemF(10)},
	}))
	require.NoError(t, repo.IncrementUsageWithReset(ctx, userID, "anthropic", 7.5, quotaItemUsedAt))
	before, err := repo.GetByUserPlatform(ctx, userID, "anthropic")
	require.NoError(t, err)
	require.NotNil(t, before.DailyWindowStart)

	_, err = repo.SoftDeleteOne(ctx, userID, "anthropic")
	require.NoError(t, err)

	restored, err := repo.RestoreLatestDeleted(ctx, userID, "anthropic")
	require.NoError(t, err)
	require.NotNil(t, restored)
	require.Equal(t, userID, restored.UserID)
	require.InDelta(t, 10, *restored.DailyLimitUSD, 1e-9)
	require.InDelta(t, 7.5, restored.DailyUsageUSD, 1e-9, "restoring keeps the accumulated usage")
	require.InDelta(t, 7.5, restored.WeeklyUsageUSD, 1e-9)
	require.NotNil(t, restored.DailyWindowStart)
	require.True(t, before.DailyWindowStart.Equal(*restored.DailyWindowStart), "window start is unchanged")

	active, err := repo.GetByUserPlatform(ctx, userID, "anthropic")
	require.NoError(t, err)
	require.NotNil(t, active)
	rows := quotaItemRows(t, client, userID, "anthropic")
	require.Len(t, rows, 1, "the soft-deleted row itself comes back")
	require.Equal(t, 1, activeQuotaItemRows(rows))
}

func TestRestoreLatestDeleted_PicksMostRecent(t *testing.T) {
	ctx := context.Background()
	client, userID := mustCreateQuotaItemUser(t)
	repo := NewUserPlatformQuotaRepository(client)

	_, _, err := repo.UpsertOneForUser(ctx, userID, UserPlatformQuotaRecord{Platform: "anthropic", DailyLimitUSD: quotaItemF(10)})
	require.NoError(t, err)
	_, err = repo.SoftDeleteOne(ctx, userID, "anthropic")
	require.NoError(t, err)
	_, _, err = repo.UpsertOneForUser(ctx, userID, UserPlatformQuotaRecord{Platform: "anthropic", DailyLimitUSD: quotaItemF(20)})
	require.NoError(t, err)
	_, err = repo.SoftDeleteOne(ctx, userID, "anthropic")
	require.NoError(t, err)

	restored, err := repo.RestoreLatestDeleted(ctx, userID, "anthropic")
	require.NoError(t, err)
	require.InDelta(t, 20, *restored.DailyLimitUSD, 1e-9, "the most recently deleted row wins")

	rows := quotaItemRows(t, client, userID, "anthropic")
	require.Len(t, rows, 2)
	require.Equal(t, 1, activeQuotaItemRows(rows))
}

func TestRestoreLatestDeleted_ActiveExistsAndNone(t *testing.T) {
	ctx := context.Background()
	client, userID := mustCreateQuotaItemUser(t)
	repo := NewUserPlatformQuotaRepository(client)

	restored, err := repo.RestoreLatestDeleted(ctx, userID, "anthropic")
	require.ErrorIs(t, err, ErrUserPlatformQuotaNotFound, "nothing was ever deleted")
	require.Nil(t, restored)

	_, _, err = repo.UpsertOneForUser(ctx, userID, UserPlatformQuotaRecord{Platform: "anthropic", DailyLimitUSD: quotaItemF(5)})
	require.NoError(t, err)
	_, err = repo.SoftDeleteOne(ctx, userID, "anthropic")
	require.NoError(t, err)
	_, _, err = repo.UpsertOneForUser(ctx, userID, UserPlatformQuotaRecord{Platform: "anthropic", DailyLimitUSD: quotaItemF(10)})
	require.NoError(t, err)

	restored, err = repo.RestoreLatestDeleted(ctx, userID, "anthropic")
	require.ErrorIs(t, err, ErrUserPlatformQuotaActiveExists)
	require.Nil(t, restored)

	active, err := repo.GetByUserPlatform(ctx, userID, "anthropic")
	require.NoError(t, err)
	require.InDelta(t, 10, *active.DailyLimitUSD, 1e-9, "the active row is untouched")
	rows := quotaItemRows(t, client, userID, "anthropic")
	require.Len(t, rows, 2)
	require.Equal(t, 1, activeQuotaItemRows(rows))
}

// TestUserPlatformQuotaServiceAdapter_SingleQuotaSentinels 走生产用的具体 adapter：
// repository sentinel 要包装成带稳定 reason 的 service 错误。
func TestUserPlatformQuotaServiceAdapter_SingleQuotaSentinels(t *testing.T) {
	ctx := context.Background()
	client, userID := mustCreateQuotaItemUser(t)
	adapter := NewUserPlatformQuotaServiceAdapter(NewUserPlatformQuotaRepository(client))

	_, err := adapter.SoftDeleteOne(ctx, userID, "anthropic")
	require.ErrorIs(t, err, service.ErrUserPlatformQuotaNotFound)
	require.Equal(t, "USER_PLATFORM_QUOTA_NOT_FOUND", infraerrors.Reason(err))
	require.Equal(t, http.StatusNotFound, infraerrors.Code(err))

	_, err = adapter.RestoreLatestDeleted(ctx, userID, "anthropic")
	require.ErrorIs(t, err, service.ErrUserPlatformQuotaNotFound)

	current, previous, err := adapter.UpsertOneForUser(ctx, userID, service.UserPlatformQuotaRecord{
		Platform: "anthropic", DailyLimitUSD: quotaItemF(12),
	})
	require.NoError(t, err)
	require.Nil(t, previous)
	require.NotNil(t, current)
	require.InDelta(t, 12, *current.DailyLimitUSD, 1e-9)

	_, err = adapter.RestoreLatestDeleted(ctx, userID, "anthropic")
	require.True(t, errors.Is(err, service.ErrUserPlatformQuotaActiveExists))
	require.Equal(t, "USER_PLATFORM_QUOTA_ACTIVE_EXISTS", infraerrors.Reason(err))
	require.Equal(t, http.StatusConflict, infraerrors.Code(err))

	previous, err = adapter.SoftDeleteOne(ctx, userID, "anthropic")
	require.NoError(t, err)
	require.InDelta(t, 12, *previous.DailyLimitUSD, 1e-9)
	restored, err := adapter.RestoreLatestDeleted(ctx, userID, "anthropic")
	require.NoError(t, err)
	require.InDelta(t, 12, *restored.DailyLimitUSD, 1e-9)
}
