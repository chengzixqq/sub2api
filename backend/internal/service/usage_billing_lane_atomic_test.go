//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type atomicLaneTestRepo struct {
	*laneTestRepo
	logs chan *UsageLog
}

func (r *atomicLaneTestRepo) ApplyWithUsageLog(ctx context.Context, cmd *UsageBillingCommand, log *UsageLog) (*UsageBillingApplyResult, error) {
	result, err := r.Apply(ctx, cmd)
	if err == nil {
		r.logs <- log
		result.UsageLogPersisted = true
	}
	return result, err
}

func TestUsageBillingLane_AtomicProbePreservesOrderAndStopBarrier(t *testing.T) {
	repo := &atomicLaneTestRepo{laneTestRepo: newLaneTestRepo(), logs: make(chan *UsageLog, 1)}
	repo.balances[7] = 10
	firstGate := repo.block("first")
	probeGate := repo.block("probe")
	c := newLaneTestCombiner(repo, 8)
	require.True(t, hasAtomicUsageBillingRepository(c))
	results := make(chan laneTestResult, 3)
	applyAsync(c, laneTestCommand(7, "first", 1), results)
	waitStarted(t, repo.laneTestRepo, "first")
	log := &UsageLog{RequestID: "probe", TotalCost: 2}
	go func() {
		res, err := c.ApplyWithUsageLog(context.Background(), laneTestCommand(7, "probe", 2), log)
		results <- laneTestResult{requestID: "probe", res: res, err: err}
	}()
	waitPending(t, c, 7, 1)
	applyAsync(c, laneTestCommand(7, "after", 3), results)
	waitPending(t, c, 7, 2)
	close(firstGate)
	waitStarted(t, repo.laneTestRepo, "probe")
	stopped := make(chan struct{})
	go func() { c.Stop(); close(stopped) }()
	select {
	case <-stopped:
		t.Fatal("Stop returned before the atomic probe transaction completed")
	case <-time.After(20 * time.Millisecond):
	}
	close(probeGate)
	waitStarted(t, repo.laneTestRepo, "after")
	got := collectResults(t, results, 3)
	for _, result := range got {
		require.NoError(t, result.err)
	}
	require.True(t, got["probe"].res.UsageLogPersisted)
	require.Same(t, log, <-repo.logs)
	require.Equal(t, []string{"first", "probe", "after"}, repo.applyCalls)
	require.Empty(t, repo.batchCalls)
	require.Equal(t, 1, repo.maxInflight)
	require.Equal(t, 4.0, repo.balances[7])
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not complete")
	}
}

func TestUsageBillingLane_AtomicCapabilityFailsClosedForLegacyRepository(t *testing.T) {
	repo := newLaneTestRepo()
	c := newLaneTestCombiner(repo, 8)
	require.False(t, hasAtomicUsageBillingRepository(c))
	result, err := c.ApplyWithUsageLog(context.Background(), laneTestCommand(7, "probe", 1), &UsageLog{})
	require.ErrorIs(t, err, ErrSyntheticProbeBillingUnavailable)
	require.Nil(t, result)
	require.Empty(t, repo.applyCalls)
}

func TestUsageBillingLane_AtomicProbeSeparatesOrdinaryBatches(t *testing.T) {
	repo := &atomicLaneTestRepo{laneTestRepo: newLaneTestRepo(), logs: make(chan *UsageLog, 1)}
	repo.balances[7] = 20
	firstGate := repo.block("first")
	c := newLaneTestCombiner(repo, 8)
	t.Cleanup(c.Stop)
	results := make(chan laneTestResult, 6)
	applyAsync(c, laneTestCommand(7, "first", 1), results)
	waitStarted(t, repo.laneTestRepo, "first")
	for i, id := range []string{"before-a", "before-b"} {
		applyAsync(c, laneTestCommand(7, id, 1), results)
		waitPending(t, c, 7, i+1)
	}
	log := &UsageLog{RequestID: "probe", TotalCost: 2}
	go func() {
		res, err := c.ApplyWithUsageLog(context.Background(), laneTestCommand(7, "probe", 2), log)
		results <- laneTestResult{requestID: "probe", res: res, err: err}
	}()
	waitPending(t, c, 7, 3)
	for i, id := range []string{"after-a", "after-b"} {
		applyAsync(c, laneTestCommand(7, id, 1), results)
		waitPending(t, c, 7, i+4)
	}
	close(firstGate)
	got := collectResults(t, results, 6)
	for id, result := range got {
		require.NoError(t, result.err, id)
		require.True(t, result.res.Applied, id)
	}
	require.Equal(t, [][]string{{"before-a", "before-b"}, {"after-a", "after-b"}}, repo.batchCalls)
	require.Equal(t, []string{"first", "probe"}, repo.applyCalls)
	require.True(t, got["probe"].res.UsageLogPersisted)
	require.Same(t, log, <-repo.logs)
	require.Equal(t, 1, repo.maxInflight)
	require.Equal(t, 13.0, repo.balances[7])
	require.Equal(t, 17.0, *got["before-b"].res.NewBalance)
	require.Equal(t, 15.0, *got["probe"].res.NewBalance)
	require.Equal(t, 13.0, *got["after-b"].res.NewBalance)
}

type atomicOutcomeLaneTestRepo struct {
	*laneTestRepo
	outcome UsageBillingBatchOutcome
}

func (r *atomicOutcomeLaneTestRepo) ApplyWithUsageLog(ctx context.Context, cmd *UsageBillingCommand, _ *UsageLog) (*UsageBillingApplyResult, error) {
	r.enter(cmd.UserID)
	defer r.leave(cmd.UserID)
	r.started <- cmd.RequestID
	r.wait(cmd.RequestID)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	r.applyCalls = append(r.applyCalls, cmd.RequestID)
	r.mu.Unlock()
	return r.outcome.Result, r.outcome.Err
}

func TestUsageBillingLane_AtomicOutcomeKeepsFollowingCommandsAndStopWorking(t *testing.T) {
	failure := errors.New("atomic usage insert failed")
	for _, tc := range []struct {
		name    string
		outcome UsageBillingBatchOutcome
	}{
		{"idempotent", UsageBillingBatchOutcome{Result: &UsageBillingApplyResult{Applied: false, UsageLogPersisted: true}}},
		{"failed", UsageBillingBatchOutcome{Err: failure}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &atomicOutcomeLaneTestRepo{laneTestRepo: newLaneTestRepo(), outcome: tc.outcome}
			repo.balances[7] = 10
			firstGate := repo.block("first")
			probeGate := repo.block("probe")
			c := newLaneTestCombiner(repo, 8)
			results := make(chan laneTestResult, 3)
			applyAsync(c, laneTestCommand(7, "first", 1), results)
			waitStarted(t, repo.laneTestRepo, "first")
			go func() {
				res, err := c.ApplyWithUsageLog(context.Background(), laneTestCommand(7, "probe", 2), &UsageLog{RequestID: "probe"})
				results <- laneTestResult{requestID: "probe", res: res, err: err}
			}()
			waitPending(t, c, 7, 1)
			applyAsync(c, laneTestCommand(7, "after", 3), results)
			waitPending(t, c, 7, 2)
			close(firstGate)
			waitStarted(t, repo.laneTestRepo, "probe")
			stopped := make(chan struct{})
			go func() { c.Stop(); close(stopped) }()
			select {
			case <-stopped:
				t.Fatal("Stop returned before the queued atomic result")
			case <-time.After(20 * time.Millisecond):
			}
			close(probeGate)
			got := collectResults(t, results, 3)
			require.NoError(t, got["first"].err)
			require.NoError(t, got["after"].err)
			if tc.outcome.Err != nil {
				require.ErrorIs(t, got["probe"].err, failure)
				require.Nil(t, got["probe"].res)
			} else {
				require.NoError(t, got["probe"].err)
				require.False(t, got["probe"].res.Applied)
				require.True(t, got["probe"].res.UsageLogPersisted)
			}
			require.Equal(t, []string{"first", "probe", "after"}, repo.applyCalls)
			require.Empty(t, repo.batchCalls)
			require.Equal(t, 1, repo.maxInflight)
			require.Equal(t, 6.0, repo.balances[7], "failed and duplicate atomic calls must not consume a second charge")
			select {
			case <-stopped:
			case <-time.After(5 * time.Second):
				t.Fatal("Stop did not drain the following command")
			}
		})
	}
}
