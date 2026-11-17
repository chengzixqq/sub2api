//go:build unit

package repository

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestSchedulerCacheUpgrade_LegacyReloadReadsFullAccountAgain(t *testing.T) {
	ctx := context.Background()
	cache, _, _ := newSchedulerCacheMemoUnit(t, defaultSchedulerSnapshotWriteChunkSize)
	clock := &memoTestClock{now: time.Date(2026, 11, 17, 0, 0, 0, 0, time.UTC)}
	cache.now = clock.Now
	bucket := memoTestBucket(401)
	account := memoTestAccount(501, "old-full")
	account.WorkspaceID = 31
	account.DispatchScope = service.DispatchScopeAuthorizedOnly
	memoPublish(t, cache, bucket, account)
	legacy, err := json.Marshal(service.Account{ID: account.ID, Name: "unchanged-legacy"})
	require.NoError(t, err)
	require.NoError(t, cache.rdb.Set(ctx, schedulerAccountMetaKey("501"), legacy, 0).Err())
	first := memoRead(t, cache, bucket)[0]
	require.Equal(t, int64(31), first.WorkspaceID)
	require.Equal(t, service.DispatchScopeAuthorizedOnly, first.DispatchScope)
	require.NotContains(t, first.Credentials, "access_token")

	account.Name = "new-full"
	account.WorkspaceID = 32
	full, err := json.Marshal(account)
	require.NoError(t, err)
	require.NoError(t, cache.rdb.Set(ctx, schedulerAccountKey("501"), full, 0).Err())
	clock.Advance(schedulerSnapshotCacheMaxAge)
	second := memoRead(t, cache, bucket)[0]
	require.Equal(t, "new-full", second.Name, "identical legacy metadata must not hide changes in the full account")
	require.Equal(t, int64(32), second.WorkspaceID)
	require.Equal(t, "old-full", first.Name)
	stored, err := cache.rdb.Get(ctx, schedulerAccountMetaKey("501")).Bytes()
	require.NoError(t, err)
	require.Equal(t, legacy, stored, "read-side compatibility must not overwrite another writer")
}

func TestSchedulerCacheUpgrade_VersionedProjectionPreservesPolicyAndDropsSecrets(t *testing.T) {
	ctx := context.Background()
	cache, _, _ := newSchedulerCacheMemoUnit(t, defaultSchedulerSnapshotWriteChunkSize)
	bucket := memoTestBucket(402)
	account := memoTestAccount(502, "projected")
	account.DispatchScope = service.DispatchScopeAuthorizedOnly
	account.Credentials["openai_capabilities"] = []string{"responses", "seedance"}
	account.Credentials["refresh_token"] = "must-not-reach-candidates"
	account.Extra["openai_passthrough_model_filter"] = true
	account.Extra["model_rate_limits"] = map[string]any{"model": map[string]any{"rate_limit_reset_at": time.Now().Add(time.Hour).Format(time.RFC3339)}}
	account.Extra["unrelated_secret"] = "must-not-reach-candidates"
	memoPublish(t, cache, bucket, account)
	payload, err := json.Marshal(schedulerMetadataPayload{Account: account, Version: schedulerMetadataVersion})
	require.NoError(t, err)
	require.NoError(t, cache.rdb.Set(ctx, schedulerAccountMetaKey("502"), payload, 0).Err())
	got := memoRead(t, cache, bucket)[0]
	require.Equal(t, service.DispatchScopeAuthorizedOnly, got.DispatchScope)
	require.Equal(t, []any{"responses", "seedance"}, got.Credentials["openai_capabilities"])
	require.Equal(t, true, got.Extra["openai_passthrough_model_filter"])
	require.Contains(t, got.Extra, "model_rate_limits")
	require.NotContains(t, got.Credentials, "access_token")
	require.NotContains(t, got.Credentials, "refresh_token")
	require.NotContains(t, got.Extra, "unrelated_secret")
}

func TestSchedulerCacheUpgrade_LastUsedPointersAreRequestLocal(t *testing.T) {
	for _, sideKey := range []bool{false, true} {
		t.Run(map[bool]string{false: "embedded", true: "side-key"}[sideKey], func(t *testing.T) {
			cache, _, _ := newSchedulerCacheMemoUnit(t, defaultSchedulerSnapshotWriteChunkSize)
			bucket := memoTestBucket(403)
			account := memoTestAccount(503, "last-used")
			baseline := time.Now().UTC().Truncate(time.Millisecond).Add(-time.Hour)
			account.LastUsedAt = &baseline
			memoPublish(t, cache, bucket, account)
			want := baseline
			if sideKey {
				want = baseline.Add(time.Minute)
				require.NoError(t, cache.UpdateLastUsed(context.Background(), map[int64]time.Time{account.ID: want}))
			}
			first := memoRead(t, cache, bucket)[0]
			second := memoRead(t, cache, bucket)[0]
			require.NotSame(t, first.LastUsedAt, second.LastUsedAt)
			*first.LastUsedAt = first.LastUsedAt.Add(24 * time.Hour)
			require.Equal(t, want, *second.LastUsedAt)
			require.Equal(t, want, *memoRead(t, cache, bucket)[0].LastUsedAt)
		})
	}
}

func TestSchedulerCacheUpgrade_MetadataCompatibilityCheckExpiresAtMaxAge(t *testing.T) {
	ctx := context.Background()
	cache, _, recorder := newSchedulerCacheMemoUnit(t, defaultSchedulerSnapshotWriteChunkSize)
	clock := &memoTestClock{now: time.Date(2026, 11, 17, 0, 0, 0, 0, time.UTC)}
	cache.now = clock.Now
	bucket := memoTestBucket(404)
	memoPublish(t, cache, bucket, memoTestAccount(504, "current"))
	memoRead(t, cache, bucket)
	entry := cache.lookupSnapshotEntry(bucket.String())
	clock.Advance(schedulerSnapshotCacheMaxAge)
	recorder.reset()
	unchanged, err := cache.snapshotMetadataUnchanged(ctx, entry)
	require.NoError(t, err)
	require.True(t, unchanged)
	require.Equal(t, 1, recorder.countPrefix("mget "+schedulerAccountMetaPrefix))
	require.NoError(t, cache.rdb.Set(ctx, schedulerAccountMetaKey("504"), `{"ID":504,"scheduler_metadata_version":999}`, 0).Err())
	clock.Advance(schedulerSnapshotCacheMaxAge - time.Millisecond)
	unchanged, err = cache.snapshotMetadataUnchanged(ctx, entry)
	require.NoError(t, err)
	require.True(t, unchanged)
	require.Equal(t, 1, recorder.countPrefix("mget "+schedulerAccountMetaPrefix))
	clock.Advance(time.Millisecond)
	unchanged, err = cache.snapshotMetadataUnchanged(ctx, entry)
	require.NoError(t, err)
	require.False(t, unchanged, "stale grace cannot extend legacy format validation beyond ten seconds")
	require.Equal(t, 2, recorder.countPrefix("mget "+schedulerAccountMetaPrefix))
}
