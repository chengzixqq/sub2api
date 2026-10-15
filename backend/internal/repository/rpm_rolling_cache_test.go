package repository

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

func TestRPMCacheRollingWindowSurvivesMinuteBoundary(t *testing.T) {
	server := miniredis.RunT(t)
	now := time.Date(2026, 10, 15, 12, 34, 59, 500000000, time.UTC)
	server.SetTime(now)
	client := redis.NewClient(&redis.Options{Addr: server.Addr(), MaxRetries: -1})
	t.Cleanup(func() { _ = client.Close() })
	minute := NewRPMCache(client)
	rolling, ok := minute.(service.RollingRPMCache)
	require.True(t, ok, "display RPM must use a separate rolling window")
	ctx := context.Background()
	_, err := minute.IncrementRPM(ctx, 101)
	require.NoError(t, err)
	server.SetTime(now.Add(time.Second))
	_, err = minute.IncrementRPM(ctx, 102)
	require.NoError(t, err)
	counts, err := rolling.GetRollingRPMBatch(ctx, []int64{101, 102, 103})
	require.NoError(t, err)
	require.Equal(t, map[int64]int{101: 1, 102: 1, 103: 0}, counts)
	count, err := minute.GetRPM(ctx, 101)
	require.NoError(t, err)
	require.Zero(t, count, "the scheduling minute must still reset")
	total, err := rolling.GetTotalRollingRPM(ctx)
	require.NoError(t, err)
	require.Equal(t, 2, total)
	server.SetTime(now.Add(60*time.Second - time.Microsecond))
	count, err = rolling.GetRollingRPM(ctx, 101)
	require.NoError(t, err)
	require.Equal(t, 1, count)
	server.SetTime(now.Add(60 * time.Second))
	counts, err = rolling.GetRollingRPMBatch(ctx, []int64{101, 102})
	require.NoError(t, err)
	require.Equal(t, map[int64]int{101: 0, 102: 1}, counts)
	total, err = rolling.GetTotalRollingRPM(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, total, "events exactly 60 seconds old expire")
	server.SetTime(now.Add(61 * time.Second))
	total, err = rolling.GetTotalRollingRPM(ctx)
	require.NoError(t, err)
	require.Zero(t, total)
}

func TestRPMCacheRollingSameTimestampConcurrentForwards(t *testing.T) {
	server := miniredis.RunT(t)
	server.SetTime(time.Date(2026, 10, 15, 12, 34, 30, 0, time.UTC))
	client := redis.NewClient(&redis.Options{Addr: server.Addr(), MaxRetries: -1})
	t.Cleanup(func() { _ = client.Close() })
	minute := NewRPMCache(client)
	rolling, ok := minute.(service.RollingRPMCache)
	require.True(t, ok)
	ctx := context.Background()
	var wg sync.WaitGroup
	errors := make(chan error, 80)
	for i := 0; i < 80; i++ {
		wg.Add(1)
		go func(id int64) {
			defer wg.Done()
			_, err := minute.IncrementRPM(ctx, id)
			errors <- err
		}(int64(101 + i%2))
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		require.NoError(t, err)
	}
	counts, err := rolling.GetRollingRPMBatch(ctx, []int64{101, 102})
	require.NoError(t, err)
	require.Equal(t, map[int64]int{101: 40, 102: 40}, counts)
	total, err := rolling.GetTotalRollingRPM(ctx)
	require.NoError(t, err)
	require.Equal(t, 80, total)
}

func TestRPMCacheRollingReadFailureIsUnavailable(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr(), MaxRetries: -1, DialTimeout: 100 * time.Millisecond})
	t.Cleanup(func() { _ = client.Close() })
	rolling, ok := NewRPMCache(client).(service.RollingRPMCache)
	require.True(t, ok)
	server.Close()
	_, err := rolling.GetTotalRollingRPM(context.Background())
	require.Error(t, err)
	counts, err := rolling.GetRollingRPMBatch(context.Background(), []int64{101})
	require.Error(t, err)
	require.Empty(t, counts)
}

func TestRPMCacheRollingWrongTypeCannotPartiallyWriteGlobalOrAccount(t *testing.T) {
	server := miniredis.RunT(t)
	server.SetTime(time.Date(2026, 10, 15, 12, 34, 30, 0, time.UTC))
	client := redis.NewClient(&redis.Options{Addr: server.Addr(), MaxRetries: -1})
	t.Cleanup(func() { _ = client.Close() })
	cache := NewRPMCache(client)
	require.NoError(t, server.Set(rollingRPMGlobalKey, "broken"))
	_, err := cache.IncrementRPM(context.Background(), 101)
	require.Error(t, err)
	require.False(t, server.Exists(rollingRPMAccountKey(101)), "an invalid global key must not leave an account-only event")
	count, err := cache.GetRPM(context.Background(), 101)
	require.NoError(t, err)
	require.Equal(t, 1, count, "display failure must not change the natural-minute limiter")
}

func TestRPMCacheRollingUserAndKeyIsolation(t *testing.T) {
	server := miniredis.RunT(t)
	now := time.Date(2026, 10, 15, 12, 34, 59, 500000000, time.UTC)
	server.SetTime(now)
	client := redis.NewClient(&redis.Options{Addr: server.Addr(), MaxRetries: -1})
	t.Cleanup(func() { _ = client.Close() })
	cache := NewRPMCache(client)
	scoped, ok := cache.(service.ScopedRollingRPMCache)
	require.True(t, ok)
	for _, ids := range [][2]int64{{1, 11}, {1, 12}, {2, 21}} {
		ctx := context.WithValue(context.Background(), ctxkey.UserID, ids[0])
		ctx = context.WithValue(ctx, ctxkey.APIKeyID, ids[1])
		_, err := cache.IncrementRPM(ctx, 101)
		require.NoError(t, err)
	}
	for id, want := range map[int64]int{1: 2, 2: 1, 3: 0} {
		got, err := scoped.GetUserRollingRPM(context.Background(), id)
		require.NoError(t, err)
		require.Equal(t, want, got)
	}
	for id, want := range map[int64]int{11: 1, 12: 1, 21: 1, 99: 0} {
		got, err := scoped.GetAPIKeyRollingRPM(context.Background(), id)
		require.NoError(t, err)
		require.Equal(t, want, got)
	}
	server.SetTime(now.Add(60 * time.Second))
	got, err := scoped.GetUserRollingRPM(context.Background(), 1)
	require.NoError(t, err)
	require.Zero(t, got)
}
