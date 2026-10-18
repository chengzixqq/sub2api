package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// stuckProcessing leaves a record in processing whose lease lapsed: the executor died mid-run (process restart,
// crash) and never stored a result. Live executions renew the lease every lease/3, so an expired lease means dead.
func stuckProcessing(t *testing.T, repo *inMemoryIdempotencyRepo, opts IdempotencyExecuteOptions) {
	t.Helper()
	keyHash := HashIdempotencyKey(opts.IdempotencyKey)
	fingerprint, err := BuildIdempotencyFingerprint(opts.Method, opts.Route, opts.ActorScope, opts.Payload)
	require.NoError(t, err)
	past := time.Now().Add(-time.Minute)
	created, err := repo.CreateProcessing(context.Background(), &IdempotencyRecord{
		Scope: opts.Scope, IdempotencyKeyHash: keyHash, RequestFingerprint: fingerprint,
		Status: IdempotencyStatusProcessing, LockedUntil: &past, ExpiresAt: time.Now().Add(time.Hour),
	})
	require.NoError(t, err)
	require.True(t, created)
}

func TestIdempotencyCoordinator_ReclaimsStaleProcessingWhenOptedIn(t *testing.T) {
	repo := newInMemoryIdempotencyRepo()
	coordinator := NewIdempotencyCoordinator(repo, DefaultIdempotencyConfig())
	opts := IdempotencyExecuteOptions{
		Scope: "admin.users.balance.update", Method: "POST", Route: "/balance", ActorScope: "admin:1",
		IdempotencyKey: "stuck-key", RequireKey: true, Payload: map[string]any{"user_id": 42},
		ExecutionTimeout: time.Second, ReclaimStaleProcessing: true,
	}
	stuckProcessing(t, repo, opts)

	executions := 0
	result, err := coordinator.Execute(context.Background(), opts, func(context.Context) (any, error) {
		executions++
		return map[string]int{"balance": 10}, nil
	})
	require.NoError(t, err)
	require.False(t, result.Replayed)
	require.Equal(t, 1, executions, "the business layer dedupes (action_id), so re-running a dead execution is safe")

	replay, err := coordinator.Execute(context.Background(), opts, func(context.Context) (any, error) {
		executions++
		return nil, nil
	})
	require.NoError(t, err)
	require.True(t, replay.Replayed)
	require.Equal(t, 1, executions)
}

func TestIdempotencyCoordinator_StaleProcessingStaysInProgressByDefault(t *testing.T) {
	repo := newInMemoryIdempotencyRepo()
	coordinator := NewIdempotencyCoordinator(repo, DefaultIdempotencyConfig())
	opts := IdempotencyExecuteOptions{
		Scope: "admin.other", Method: "POST", Route: "/x", IdempotencyKey: "stuck-key", RequireKey: true, Payload: 1,
		ExecutionTimeout: time.Second,
	}
	stuckProcessing(t, repo, opts)

	_, err := coordinator.Execute(context.Background(), opts, func(context.Context) (any, error) {
		t.Fatal("must not run without the opt-in")
		return nil, nil
	})
	require.ErrorIs(t, err, ErrIdempotencyInProgress)
}

// Reclaim needs lease renewal (only active with ExecutionTimeout), otherwise a slow live run would look dead.
func TestIdempotencyCoordinator_ReclaimRequiresExecutionTimeout(t *testing.T) {
	repo := newInMemoryIdempotencyRepo()
	coordinator := NewIdempotencyCoordinator(repo, DefaultIdempotencyConfig())
	opts := IdempotencyExecuteOptions{
		Scope: "admin.other", Method: "POST", Route: "/x", IdempotencyKey: "stuck-key", RequireKey: true, Payload: 1,
		ReclaimStaleProcessing: true,
	}
	stuckProcessing(t, repo, opts)

	_, err := coordinator.Execute(context.Background(), opts, func(context.Context) (any, error) {
		t.Fatal("must not run: no lease renewal without ExecutionTimeout")
		return nil, nil
	})
	require.ErrorIs(t, err, ErrIdempotencyInProgress)
}

func TestIdempotencyCoordinator_LiveProcessingIsNotReclaimed(t *testing.T) {
	repo := newInMemoryIdempotencyRepo()
	coordinator := NewIdempotencyCoordinator(repo, DefaultIdempotencyConfig())
	opts := IdempotencyExecuteOptions{
		Scope: "admin.users.balance.update", Method: "POST", Route: "/balance", IdempotencyKey: "live-key", RequireKey: true, Payload: 1,
		ExecutionTimeout: time.Second, ReclaimStaleProcessing: true,
	}
	keyHash := HashIdempotencyKey(opts.IdempotencyKey)
	fingerprint, err := BuildIdempotencyFingerprint(opts.Method, opts.Route, opts.ActorScope, opts.Payload)
	require.NoError(t, err)
	future := time.Now().Add(20 * time.Second)
	_, err = repo.CreateProcessing(context.Background(), &IdempotencyRecord{
		Scope: opts.Scope, IdempotencyKeyHash: keyHash, RequestFingerprint: fingerprint,
		Status: IdempotencyStatusProcessing, LockedUntil: &future, ExpiresAt: time.Now().Add(time.Hour),
	})
	require.NoError(t, err)

	_, err = coordinator.Execute(context.Background(), opts, func(context.Context) (any, error) {
		t.Fatal("a live lease must not be taken over")
		return nil, nil
	})
	require.ErrorIs(t, err, ErrIdempotencyInProgress)
}
