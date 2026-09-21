//go:build unit

package repository

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestSchedulerCache_RollingUpgradeReadsLegacyMetadata(t *testing.T) {
	ctx := context.Background()
	cache := newSchedulerCacheUnit(t)
	bucket := service.SchedulerBucket{GroupID: 8, Platform: service.PlatformAnthropic, Mode: service.SchedulerModeSingle}
	account := service.Account{
		ID: 31, Platform: service.PlatformAnthropic, Status: service.StatusActive, Schedulable: true,
		Credentials: map[string]any{"account_scheduling_threshold": 60, "access_token": "test-only"},
		Extra:       map[string]any{"passive_usage_7d_utilization": .75, "unrelated_payload": "private"},
	}
	token, err := cache.CaptureBucketWriteToken(ctx, bucket)
	require.NoError(t, err)
	require.NoError(t, cache.SetSnapshot(ctx, bucket, token, []service.Account{account}))
	legacy := buildSchedulerMetadataAccount(account)
	delete(legacy.Credentials, "account_scheduling_threshold")
	delete(legacy.Extra, "passive_usage_7d_utilization")
	payload, err := json.Marshal(legacy)
	require.NoError(t, err)
	require.NoError(t, cache.rdb.Set(ctx, schedulerAccountMetaKey("31"), payload, 0).Err())
	got, hit, err := cache.GetSnapshot(ctx, bucket)
	require.NoError(t, err)
	require.True(t, hit)
	require.Len(t, got, 1)
	require.EqualValues(t, 60, got[0].Credentials["account_scheduling_threshold"])
	require.Equal(t, .75, got[0].Extra["passive_usage_7d_utilization"])
	require.NotContains(t, got[0].Credentials, "access_token")
	require.NotContains(t, got[0].Extra, "unrelated_payload")
	// Missing full data must trigger the existing database fallback, not admission
	// with an incomplete threshold projection.
	require.NoError(t, cache.rdb.Del(ctx, schedulerAccountKey("31")).Err())
	_, hit, err = cache.GetSnapshot(ctx, bucket)
	require.NoError(t, err)
	require.False(t, hit)
}

func TestSchedulerCache_RollingUpgradeRejectsIncompleteOrFuturePayload(t *testing.T) {
	for _, tc := range []struct{ name, full, meta string }{
		{"missing full", "", `{"ID":31}`},
		{"mismatched full", `{"ID":32}`, `{"ID":31}`},
		{"future schema", `{"ID":31}`, `{"ID":31,"scheduler_metadata_version":999}`},
		{"mismatched meta", `{"ID":31}`, `{"ID":32,"scheduler_metadata_version":1}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			cache := newSchedulerCacheUnit(t)
			bucket := service.SchedulerBucket{GroupID: 8, Platform: service.PlatformAnthropic, Mode: service.SchedulerModeSingle}
			token, err := cache.CaptureBucketWriteToken(ctx, bucket)
			require.NoError(t, err)
			require.NoError(t, cache.SetSnapshot(ctx, bucket, token, []service.Account{{ID: 31}}))
			require.NoError(t, cache.rdb.Set(ctx, schedulerAccountMetaKey("31"), tc.meta, 0).Err())
			if tc.full == "" {
				require.NoError(t, cache.rdb.Del(ctx, schedulerAccountKey("31")).Err())
			} else {
				require.NoError(t, cache.rdb.Set(ctx, schedulerAccountKey("31"), tc.full, 0).Err())
			}
			_, hit, err := cache.GetSnapshot(ctx, bucket)
			require.NoError(t, err)
			require.False(t, hit)
		})
	}
}

func TestSchedulerCache_CurrentMetadataDoesNotRequireFullPayload(t *testing.T) {
	ctx := context.Background()
	cache := newSchedulerCacheUnit(t)
	bucket := service.SchedulerBucket{GroupID: 8, Platform: service.PlatformAnthropic, Mode: service.SchedulerModeSingle}
	token, err := cache.CaptureBucketWriteToken(ctx, bucket)
	require.NoError(t, err)
	require.NoError(t, cache.SetSnapshot(ctx, bucket, token, []service.Account{{ID: 31}}))
	require.NoError(t, cache.rdb.Del(ctx, schedulerAccountKey("31")).Err())
	_, hit, err := cache.GetSnapshot(ctx, bucket)
	require.NoError(t, err)
	require.True(t, hit)
}
