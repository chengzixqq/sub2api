//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestBlueGreenConcurrencyStartupPreservesLivePeersIntegration(t *testing.T) {
	for _, markerExists := range []bool{false, true} {
		t.Run(strconv.FormatBool(markerExists), func(t *testing.T) {
			ctx := context.Background()
			rdb := testRedis(t)
			blue := NewConcurrencyCache(rdb, 15, 120)
			green := NewConcurrencyCache(rdb, 15, 120)
			if markerExists {
				require.NoError(t, rdb.Set(ctx, legacyWaitSweepMarkerKey, "1", 0).Err())
			}
			for prefix, cache := range map[string]service.ConcurrencyCache{"blue-": blue, "green-": green} {
				acquired, err := cache.AcquireAccountSlot(ctx, 10, 2, prefix+"1")
				require.NoError(t, err)
				require.True(t, acquired)
				acquired, err = cache.AcquireUserSlot(ctx, 20, 2, prefix+"1")
				require.NoError(t, err)
				require.True(t, acquired)
			}
			waiting, err := blue.IncrementAccountWaitCount(ctx, 10, 1)
			require.NoError(t, err)
			require.True(t, waiting)
			waiting, err = blue.IncrementWaitCount(ctx, 20, 1)
			require.NoError(t, err)
			require.True(t, waiting)
			require.NoError(t, rdb.Set(ctx, accountWaitKey(99), 3, 30*time.Second).Err())
			require.NoError(t, rdb.Set(ctx, waitQueueKey(99), 4, 30*time.Second).Err())
			waitCounts := map[string]int{accountWaitKey(10): 1, waitQueueKey(20): 1, accountWaitKey(99): 3, waitQueueKey(99): 4}
			waitTTLs := make(map[string]time.Duration, len(waitCounts))
			for key := range waitCounts {
				ttl, err := rdb.PTTL(ctx, key).Result()
				require.NoError(t, err)
				waitTTLs[key] = ttl
			}
			started := time.Now()

			require.NoError(t, green.CleanupStaleProcessSlots(ctx, "green-"))
			require.NoError(t, blue.CleanupStaleProcessSlots(ctx, "blue-"))

			for _, key := range []string{accountSlotKey(10), userSlotKey(20)} {
				members, err := rdb.ZRange(ctx, key, 0, -1).Result()
				require.NoError(t, err)
				require.ElementsMatch(t, []string{"blue-1", "green-1"}, members)
			}
			for key, expected := range waitCounts {
				count, err := rdb.Get(ctx, key).Int()
				require.NoError(t, err)
				require.Equal(t, expected, count)
				ttl, err := rdb.PTTL(ctx, key).Result()
				require.NoError(t, err)
				require.Positive(t, ttl)
				require.LessOrEqual(t, ttl, waitTTLs[key], "startup must not renew wait TTL")
				require.GreaterOrEqual(t, ttl, waitTTLs[key]-time.Since(started)-time.Second)
			}
			acquired, err := green.AcquireAccountSlot(ctx, 10, 2, "green-over-limit")
			require.NoError(t, err)
			require.False(t, acquired)
			acquired, err = green.AcquireUserSlot(ctx, 20, 2, "green-over-limit")
			require.NoError(t, err)
			require.False(t, acquired)
			waiting, err = green.IncrementAccountWaitCount(ctx, 10, 1)
			require.NoError(t, err)
			require.False(t, waiting)
			waiting, err = green.IncrementWaitCount(ctx, 20, 1)
			require.NoError(t, err)
			require.False(t, waiting)
		})
	}
}

func TestBlueGreenConcurrencyStartupPrunesExpiredSlotsIntegration(t *testing.T) {
	ctx := context.Background()
	rdb := testRedis(t)
	cache := NewConcurrencyCache(rdb, 1, 120)
	now, err := rdb.Time(ctx).Result()
	require.NoError(t, err)
	for _, spec := range []slotIndexSpec{accountSlotIndex, userSlotIndex} {
		require.NoError(t, rdb.ZAdd(ctx, spec.slotKey(10),
			redis.Z{Score: float64(now.Unix() - 61), Member: "blue-expired"},
			redis.Z{Score: float64(now.Unix() - 60), Member: "green-expired"},
			redis.Z{Score: float64(now.Unix()), Member: "blue-live"},
			redis.Z{Score: float64(now.Unix()), Member: "green-live"},
		).Err())
		require.NoError(t, rdb.ZAdd(ctx, spec.slotKey(20), redis.Z{Score: float64(now.Unix() - 61), Member: "green-expired"}).Err())
		require.NoError(t, rdb.Set(ctx, spec.waitKey(20), 2, 120*time.Second).Err())
		require.NoError(t, rdb.ZAdd(ctx, spec.indexKey,
			redis.Z{Score: float64(now.Unix() - 100), Member: "10"},
			redis.Z{Score: float64(now.Unix() - 100), Member: "20"},
		).Err())
	}

	require.NoError(t, cache.CleanupStaleProcessSlots(ctx, "green-"))

	for _, spec := range []slotIndexSpec{accountSlotIndex, userSlotIndex} {
		members, err := rdb.ZRange(ctx, spec.slotKey(10), 0, -1).Result()
		require.NoError(t, err)
		require.ElementsMatch(t, []string{"blue-live", "green-live"}, members)
		exists, err := rdb.Exists(ctx, spec.slotKey(20)).Result()
		require.NoError(t, err)
		require.Zero(t, exists)
		waitCount, err := rdb.Get(ctx, spec.waitKey(20)).Int()
		require.NoError(t, err)
		require.Equal(t, 2, waitCount)
		indexMembers, err := rdb.ZRange(ctx, spec.indexKey, 0, -1).Result()
		require.NoError(t, err)
		require.ElementsMatch(t, []string{"10", "20"}, indexMembers)
	}
}

func TestBlueGreenSchedulerMixedMetadataIntegration(t *testing.T) {
	ctx := context.Background()
	rdb := testRedis(t)
	cache := newSchedulerCacheWithChunkSizes(rdb, 1, 2)
	bucket := service.SchedulerBucket{GroupID: 8, Platform: service.PlatformAnthropic, Mode: service.SchedulerModeSingle}
	oldLastUsed := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	newLastUsed := oldLastUsed.Add(time.Hour)
	accounts := []service.Account{
		{ID: 31, Platform: service.PlatformAnthropic, Type: service.AccountTypeOAuth, Status: service.StatusActive, Schedulable: true, Priority: 1,
			LastUsedAt: &oldLastUsed, Credentials: map[string]any{"account_scheduling_threshold": 60, "access_token": "test-only-access", "refresh_token": "test-only-refresh"},
			Extra: map[string]any{"passive_usage_7d_utilization": .75, "unrelated_payload": "private"}},
		{ID: 32, Platform: service.PlatformAnthropic, Type: service.AccountTypeOAuth, Status: service.StatusActive, Schedulable: true, Priority: 2,
			LastUsedAt: &oldLastUsed, Credentials: map[string]any{"account_scheduling_threshold": 80, "access_token": "test-only-current-access"}},
		{ID: 33, Platform: service.PlatformAnthropic, Type: service.AccountTypeOAuth, Status: service.StatusActive, Schedulable: true, Priority: 3,
			LastUsedAt: &oldLastUsed, Credentials: map[string]any{"account_scheduling_threshold": 70, "refresh_token": "test-only-other-refresh"},
			Extra: map[string]any{"passive_usage_7d_utilization": .65, "unrelated_payload": "private"}},
	}
	token, err := cache.CaptureBucketWriteToken(ctx, bucket)
	require.NoError(t, err)
	require.NoError(t, cache.SetSnapshot(ctx, bucket, token, accounts))
	metadataBefore := make(map[string]string, len(accounts))
	for _, account := range accounts {
		id := strconv.FormatInt(account.ID, 10)
		if account.ID != 32 {
			legacy := buildSchedulerMetadataAccount(account)
			delete(legacy.Credentials, "account_scheduling_threshold")
			delete(legacy.Extra, "passive_usage_7d_utilization")
			payload, err := json.Marshal(legacy)
			require.NoError(t, err)
			require.NoError(t, rdb.Set(ctx, schedulerAccountMetaKey(id), payload, 0).Err())
		}
		metadata, err := rdb.Get(ctx, schedulerAccountMetaKey(id)).Result()
		require.NoError(t, err)
		metadataBefore[id] = metadata
		require.NoError(t, rdb.Set(ctx, schedulerLastUsedKey(id), newLastUsed.UnixMilli(), 0).Err())
	}
	// A current projection must not depend on the legacy full-payload fallback.
	require.NoError(t, rdb.Del(ctx, schedulerAccountKey("32")).Err())

	got, hit, err := cache.GetSnapshot(ctx, bucket)
	require.NoError(t, err)
	require.True(t, hit)
	require.Len(t, got, 3)
	byID := make(map[int64]*service.Account, len(got))
	for _, account := range got {
		byID[account.ID] = account
		require.NotContains(t, account.Credentials, "access_token")
		require.NotContains(t, account.Credentials, "refresh_token")
		require.NotContains(t, account.Extra, "unrelated_payload")
		require.NotNil(t, account.LastUsedAt)
		require.True(t, account.LastUsedAt.Equal(newLastUsed))
		id := strconv.FormatInt(account.ID, 10)
		metadataAfter, err := rdb.Get(ctx, schedulerAccountMetaKey(id)).Result()
		require.NoError(t, err)
		require.Equal(t, metadataBefore[id], metadataAfter, "legacy reader must not overwrite a concurrent writer's metadata")
	}
	for id, threshold := range map[int64]int{31: 60, 32: 80, 33: 70} {
		require.Contains(t, byID, id)
		require.EqualValues(t, threshold, byID[id].Credentials["account_scheduling_threshold"])
	}
	require.Equal(t, .75, byID[31].Extra["passive_usage_7d_utilization"])
	require.Equal(t, .65, byID[33].Extra["passive_usage_7d_utilization"])
}

func TestBlueGreenSchedulerIncompleteLegacyMetadataIntegration(t *testing.T) {
	for _, tc := range []struct{ name, full, metadata string }{
		{"missing_full", "", `{"ID":31}`},
		{"mismatched_full", `{"ID":32}`, `{"ID":31}`},
		{"mismatched_metadata", `{"ID":31}`, `{"ID":32}`},
		{"future_metadata", `{"ID":31}`, `{"ID":31,"scheduler_metadata_version":999}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			rdb := testRedis(t)
			cache := NewSchedulerCache(rdb)
			bucket := service.SchedulerBucket{GroupID: 8, Platform: service.PlatformAnthropic, Mode: service.SchedulerModeSingle}
			token, err := cache.CaptureBucketWriteToken(ctx, bucket)
			require.NoError(t, err)
			require.NoError(t, cache.SetSnapshot(ctx, bucket, token, []service.Account{{ID: 31}}))
			require.NoError(t, rdb.Set(ctx, schedulerAccountMetaKey("31"), tc.metadata, 0).Err())
			if tc.full == "" {
				require.NoError(t, rdb.Del(ctx, schedulerAccountKey("31")).Err())
			} else {
				require.NoError(t, rdb.Set(ctx, schedulerAccountKey("31"), tc.full, 0).Err())
			}

			got, hit, err := cache.GetSnapshot(ctx, bucket)
			require.NoError(t, err)
			require.False(t, hit, "incomplete legacy data must fall back to the repository")
			require.Nil(t, got)
		})
	}
}
