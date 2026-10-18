package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The client learns how long a key stays replayable, so it knows when only the ledger can answer "did it happen".
func TestIdempotencyCoordinator_ReportsExpiresAt(t *testing.T) {
	repo := newInMemoryIdempotencyRepo()
	cfg := DefaultIdempotencyConfig()
	coordinator := NewIdempotencyCoordinator(repo, cfg)
	opts := IdempotencyExecuteOptions{Scope: "admin.users.balance.update", Method: "POST", Route: "/b", IdempotencyKey: "k-exp", RequireKey: true, Payload: 1, TTL: time.Hour}

	before := time.Now()
	first, err := coordinator.Execute(context.Background(), opts, func(context.Context) (any, error) { return 1, nil })
	require.NoError(t, err)
	require.False(t, first.ExpiresAt.IsZero())
	require.WithinDuration(t, before.Add(time.Hour), first.ExpiresAt, 5*time.Second)

	replay, err := coordinator.Execute(context.Background(), opts, func(context.Context) (any, error) { return 2, nil })
	require.NoError(t, err)
	require.True(t, replay.Replayed)
	require.True(t, first.ExpiresAt.Equal(replay.ExpiresAt), "a replay reports the stored record's expiry")

	noKey, err := coordinator.Execute(context.Background(), IdempotencyExecuteOptions{Scope: "x", Method: "POST", Route: "/b", Payload: 1}, func(context.Context) (any, error) { return 3, nil })
	require.NoError(t, err)
	require.True(t, noKey.ExpiresAt.IsZero(), "without a key nothing is stored")
}
