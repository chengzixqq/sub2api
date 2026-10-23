//go:build unit

package repository

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSchedulerCache_MetadataMemoLegacyWorkspaceAndRevision(t *testing.T) {
	ctx := context.Background()
	cache, _, _ := newSchedulerCacheMemoUnit(t, defaultSchedulerSnapshotWriteChunkSize)
	bucket := memoTestBucket(201)
	account := memoTestAccount(301, "legacy-full")
	account.WorkspaceID = 41
	memoPublish(t, cache, bucket, account)

	legacy := buildSchedulerMetadataAccount(account)
	legacy.WorkspaceID = 0 // Older writers omitted the workspace projection.
	legacy.Name = "legacy-slim"
	payload, err := json.Marshal(legacy)
	require.NoError(t, err)
	require.NoError(t, cache.rdb.Set(ctx, schedulerAccountMetaKey("301"), payload, 0).Err())

	first := memoRead(t, cache, bucket)[0]
	require.Equal(t, int64(41), first.WorkspaceID)
	require.Equal(t, "legacy-full", first.Name)
	first.WorkspaceID = 999
	second := memoRead(t, cache, bucket)[0]
	require.Equal(t, int64(41), second.WorkspaceID, "memo must return independent top-level copies")
	stored, err := cache.rdb.Get(ctx, schedulerAccountMetaKey("301")).Bytes()
	require.NoError(t, err)
	require.Equal(t, payload, stored, "legacy projection must never be written back")

	account.WorkspaceID = 42
	account.Name = "new-writer"
	require.NoError(t, cache.SetAccount(ctx, &account))
	updated := memoRead(t, cache, bucket)[0]
	require.Equal(t, int64(42), updated.WorkspaceID)
	require.Equal(t, "new-writer", updated.Name, "a revision must invalidate legacy memo immediately")
	stored, err = cache.rdb.Get(ctx, schedulerAccountMetaKey("301")).Bytes()
	require.NoError(t, err)
	metadata, err := decodeCachedSchedulerMetadata(stored)
	require.NoError(t, err)
	require.Equal(t, schedulerMetadataVersion, metadata.Version)
}

func TestSchedulerCache_MetadataMemoRejectsInvalidReloadAndRecovers(t *testing.T) {
	for _, tc := range []struct {
		name     string
		metadata string
		full     string
	}{
		{"future version", `{"ID":302,"scheduler_metadata_version":999}`, ""},
		{"mismatched metadata", `{"ID":303,"scheduler_metadata_version":1}`, ""},
		{"mismatched legacy full", `{"ID":302}`, `{"ID":303}`},
		{"missing legacy full", `{"ID":302}`, "DELETE"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			cache, _, _ := newSchedulerCacheMemoUnit(t, defaultSchedulerSnapshotWriteChunkSize)
			clock := &memoTestClock{now: time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC)}
			cache.now = clock.Now
			bucket := memoTestBucket(202)
			account := memoTestAccount(302, "valid")
			account.WorkspaceID = 71
			memoPublish(t, cache, bucket, account)
			require.Equal(t, int64(71), memoRead(t, cache, bucket)[0].WorkspaceID)
			require.NoError(t, cache.rdb.Set(ctx, schedulerAccountMetaKey("302"), tc.metadata, 0).Err())
			if tc.full == "DELETE" {
				require.NoError(t, cache.rdb.Del(ctx, schedulerAccountKey("302")).Err())
			} else if tc.full != "" {
				require.NoError(t, cache.rdb.Set(ctx, schedulerAccountKey("302"), tc.full, 0).Err())
			}
			// Old writers do not bump revisions; cached state is bounded to ten seconds.
			clock.Advance(schedulerSnapshotCacheMaxAge - time.Millisecond)
			require.Equal(t, int64(71), memoRead(t, cache, bucket)[0].WorkspaceID)
			clock.Advance(time.Millisecond)
			accounts, hit, err := cache.GetSnapshot(ctx, bucket)
			require.NoError(t, err)
			require.False(t, hit)
			require.Nil(t, accounts)
			require.Nil(t, cache.lookupSnapshotEntry(bucket.String()), "invalid reload must not keep a master")
			require.NoError(t, cache.SetAccount(ctx, &account))
			require.Equal(t, int64(71), memoRead(t, cache, bucket)[0].WorkspaceID)
		})
	}
}
