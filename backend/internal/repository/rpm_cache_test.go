package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestRPMCacheCurrentMinuteAbsentAndObservedCounts(t *testing.T) {
	server := miniredis.RunT(t)
	now := time.Date(2026, 10, 15, 12, 34, 0, 0, time.UTC)
	server.SetTime(now)
	client := redis.NewClient(&redis.Options{Addr: server.Addr(), MaxRetries: -1})
	t.Cleanup(func() { _ = client.Close() })
	cache := NewRPMCache(client)
	ctx := context.Background()

	count, err := cache.GetRPM(ctx, 101)
	require.NoError(t, err)
	require.Zero(t, count)
	count, err = cache.IncrementRPM(ctx, 102)
	require.NoError(t, err)
	require.Equal(t, 1, count)
	counts, err := cache.GetRPMBatch(ctx, []int64{101, 102})
	require.NoError(t, err)
	require.Equal(t, map[int64]int{101: 0, 102: 1}, counts)
	// A previous minute's traffic is not substituted for the current minute.
	server.SetTime(now.Add(time.Minute))
	counts, err = cache.GetRPMBatch(ctx, []int64{101, 102})
	require.NoError(t, err)
	require.Equal(t, map[int64]int{101: 0, 102: 0}, counts)
}

func TestRPMCacheBatchReadErrorOmitsOnlyFailedCounter(t *testing.T) {
	for _, corruptType := range []string{"not-an-integer", "wrong-redis-type"} {
		t.Run(corruptType, func(t *testing.T) {
			server := miniredis.RunT(t)
			now := time.Date(2026, 10, 15, 12, 34, 0, 0, time.UTC)
			server.SetTime(now)
			client := redis.NewClient(&redis.Options{Addr: server.Addr(), MaxRetries: -1})
			t.Cleanup(func() { _ = client.Close() })
			cache := NewRPMCache(client)
			key := func(id int64) string { return fmt.Sprintf("rpm:%d:%d", id, now.Unix()/60) }
			require.NoError(t, server.Set(key(102), "7"))
			if corruptType == "not-an-integer" {
				require.NoError(t, server.Set(key(103), "broken"))
			} else {
				server.HSet(key(103), "field", "value")
			}
			// The missing first key makes Pipeline.Exec return redis.Nil. A later
			// corrupt key must still be reported as failed, not zero.
			counts, err := cache.GetRPMBatch(context.Background(), []int64{101, 102, 103})
			require.Error(t, err)
			require.Equal(t, map[int64]int{101: 0, 102: 7}, counts)
			count, err := cache.GetRPM(context.Background(), 103)
			require.Error(t, err)
			require.Zero(t, count)
		})
	}
}

func TestRPMCacheReadFailureDoesNotProduceZeroObservations(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr(), MaxRetries: -1, DialTimeout: 100 * time.Millisecond})
	t.Cleanup(func() { _ = client.Close() })
	cache := NewRPMCache(client)
	server.Close()

	count, err := cache.GetRPM(context.Background(), 101)
	require.Error(t, err)
	require.Zero(t, count)
	counts, err := cache.GetRPMBatch(context.Background(), []int64{101, 102})
	require.Error(t, err)
	require.Empty(t, counts)
}
