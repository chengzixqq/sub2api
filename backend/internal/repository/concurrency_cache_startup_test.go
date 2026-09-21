package repository

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestConcurrencyStartupCleanupPreservesLivePeers(t *testing.T) {
	for _, legacyMarkerExists := range []bool{false, true} {
		name := "without_legacy_marker"
		if legacyMarkerExists {
			name = "with_legacy_marker"
		}
		t.Run(name, func(t *testing.T) {
			server := miniredis.RunT(t)
			client := redis.NewClient(&redis.Options{Addr: server.Addr()})
			t.Cleanup(func() { require.NoError(t, client.Close()) })
			ctx := context.Background()
			blue := NewConcurrencyCache(client, 15, 900)
			green := NewConcurrencyCache(client, 15, 900)
			if legacyMarkerExists {
				require.NoError(t, client.Set(ctx, legacyWaitSweepMarkerKey, "1", 0).Err())
			}
			for _, requestID := range []string{"blue-1", "green-1"} {
				acquired, err := blue.AcquireAccountSlot(ctx, 10, 2, requestID)
				require.NoError(t, err)
				require.True(t, acquired)
				acquired, err = blue.AcquireUserSlot(ctx, 20, 2, requestID)
				require.NoError(t, err)
				require.True(t, acquired)
			}
			waiting, err := blue.IncrementAccountWaitCount(ctx, 10, 1)
			require.NoError(t, err)
			require.True(t, waiting)
			waiting, err = blue.IncrementWaitCount(ctx, 20, 1)
			require.NoError(t, err)
			require.True(t, waiting)
			require.NoError(t, client.Set(ctx, accountWaitKey(99), 3, time.Minute).Err())
			require.NoError(t, client.Set(ctx, waitQueueKey(99), 4, time.Minute).Err())
			accountWaitTTL := server.TTL(accountWaitKey(10))
			userWaitTTL := server.TTL(waitQueueKey(20))

			require.NoError(t, green.CleanupStaleProcessSlots(ctx, "green-"))
			require.NoError(t, blue.CleanupStaleProcessSlots(ctx, "blue-"))

			for _, key := range []string{accountSlotKey(10), userSlotKey(20)} {
				members, err := client.ZRange(ctx, key, 0, -1).Result()
				require.NoError(t, err)
				require.ElementsMatch(t, []string{"blue-1", "green-1"}, members)
			}
			for key, expected := range map[string]int{
				accountWaitKey(10): 1, waitQueueKey(20): 1,
				accountWaitKey(99): 3, waitQueueKey(99): 4,
			} {
				count, err := client.Get(ctx, key).Int()
				require.NoError(t, err)
				require.Equal(t, expected, count)
			}
			require.Equal(t, accountWaitTTL, server.TTL(accountWaitKey(10)))
			require.Equal(t, userWaitTTL, server.TTL(waitQueueKey(20)))
			acquired, err := green.AcquireAccountSlot(ctx, 10, 2, "green-over-limit")
			require.NoError(t, err)
			require.False(t, acquired, "starting a peer must not free occupied account capacity")
			acquired, err = green.AcquireUserSlot(ctx, 20, 2, "green-over-limit")
			require.NoError(t, err)
			require.False(t, acquired, "starting a peer must not free occupied user capacity")
			waiting, err = green.IncrementAccountWaitCount(ctx, 10, 1)
			require.NoError(t, err)
			require.False(t, waiting)
			waiting, err = green.IncrementWaitCount(ctx, 20, 1)
			require.NoError(t, err)
			require.False(t, waiting)
		})
	}
}

func TestConcurrencyStartupCleanupPrunesOnlyExpiredSlots(t *testing.T) {
	server := miniredis.RunT(t)
	redisNow := time.Date(2040, time.January, 2, 3, 4, 5, 0, time.UTC)
	server.SetTime(redisNow)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	ctx := context.Background()
	cache := NewConcurrencyCache(client, 1, 120)
	for _, spec := range []slotIndexSpec{accountSlotIndex, userSlotIndex} {
		require.NoError(t, client.ZAdd(ctx, spec.slotKey(10),
			redis.Z{Score: float64(redisNow.Unix() - 61), Member: "blue-expired"},
			redis.Z{Score: float64(redisNow.Unix() - 60), Member: "green-expired-boundary"},
			redis.Z{Score: float64(redisNow.Unix() - 59), Member: "blue-live"},
			redis.Z{Score: float64(redisNow.Unix()), Member: "green-live"},
		).Err())
		for _, id := range []int64{20, 30} {
			require.NoError(t, client.ZAdd(ctx, spec.slotKey(id), redis.Z{
				Score: float64(redisNow.Unix() - 60), Member: "green-expired",
			}).Err())
		}
		require.NoError(t, client.Set(ctx, spec.waitKey(20), 2, 120*time.Second).Err())
		require.NoError(t, client.ZAdd(ctx, spec.indexKey,
			redis.Z{Score: float64(redisNow.Unix() - 100), Member: "10"},
			redis.Z{Score: float64(redisNow.Unix() - 100), Member: "20"},
			redis.Z{Score: float64(redisNow.Unix() - 100), Member: "30"},
			redis.Z{Score: float64(redisNow.Unix() - 100), Member: "invalid"},
		).Err())
	}

	require.NoError(t, cache.CleanupStaleProcessSlots(ctx, "green-"))

	for _, spec := range []slotIndexSpec{accountSlotIndex, userSlotIndex} {
		members, err := client.ZRange(ctx, spec.slotKey(10), 0, -1).Result()
		require.NoError(t, err)
		require.ElementsMatch(t, []string{"blue-live", "green-live"}, members)
		require.False(t, server.Exists(spec.slotKey(20)))
		require.False(t, server.Exists(spec.slotKey(30)))
		count, err := client.Get(ctx, spec.waitKey(20)).Int()
		require.NoError(t, err)
		require.Equal(t, 2, count)
		indexMembers, err := client.ZRange(ctx, spec.indexKey, 0, -1).Result()
		require.NoError(t, err)
		require.ElementsMatch(t, []string{"10", "20"}, indexMembers)
		waitScore, err := client.ZScore(ctx, spec.indexKey, "20").Result()
		require.NoError(t, err)
		require.Equal(t, float64(redisNow.Unix()+120), waitScore)
	}
}
