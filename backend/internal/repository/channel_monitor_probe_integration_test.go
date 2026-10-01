//go:build integration

package repository

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func probeIntegrationTarget(t *testing.T, now time.Time) *service.ChannelMonitorProbeTarget {
	t.Helper()
	ctx := context.Background()
	var groupID int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO groups(name,platform) VALUES($1,'openai') RETURNING id`, fmt.Sprintf("probe-%d", time.Now().UnixNano())).Scan(&groupID))
	repo := NewChannelMonitorProbeRepository(integrationDB)
	target := &service.ChannelMonitorProbeTarget{GroupID: groupID, Model: "gpt-test", Protocol: "openai_chat", Enabled: true}
	require.NoError(t, repo.SaveTarget(ctx, target))
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM channel_monitor_probe_runs WHERE target_id=$1`, target.ID)
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM channel_monitor_probe_budgets WHERE target_id=$1 OR (target_id=0 AND utc_day=$2)`, target.ID, now.UTC().Format("2006-01-02"))
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM channel_monitor_probe_targets WHERE id=$1`, target.ID)
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM groups WHERE id=$1`, groupID)
	})
	return target
}

func TestChannelMonitorProbeRepository_ConcurrentIdempotency(t *testing.T) {
	now := time.Date(2031, 1, 1, 23, 59, 0, 0, time.UTC)
	target := probeIntegrationTarget(t, now)
	repo := NewChannelMonitorProbeRepository(integrationDB)
	ctx := context.Background()
	var wg sync.WaitGroup
	var reserved atomic.Int32
	runs := make(chan *service.ChannelMonitorProbeRun, 24)
	errs := make(chan error, 24)
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			run, owner, err := repo.Reserve(ctx, target.ID, "one-idempotent-operation", true, now)
			if err != nil {
				errs <- err
				return
			}
			if owner {
				reserved.Add(1)
			}
			runs <- run
		}()
	}
	wg.Wait()
	close(errs)
	close(runs)
	for err := range errs {
		require.NoError(t, err)
	}
	require.Equal(t, int32(1), reserved.Load())
	var first *service.ChannelMonitorProbeRun
	for run := range runs {
		if first == nil {
			first = run
		}
		require.Equal(t, first.ID, run.ID)
	}
	budget, err := repo.Budget(ctx, now)
	require.NoError(t, err)
	require.Equal(t, 1, budget.GlobalUsed)
	require.Equal(t, 1, budget.Targets[0].Used)
	finished := now.Add(time.Second)
	first.CompletedAt = &finished
	first.Status = "healthy"
	require.NoError(t, repo.Complete(ctx, first))
	// A client retry after commit/response loss reads the committed result, not another generation.
	replayed, owner, err := repo.Reserve(ctx, target.ID, "one-idempotent-operation", true, now.Add(time.Minute))
	require.NoError(t, err)
	require.False(t, owner)
	require.Equal(t, "healthy", replayed.Status)
}

func TestChannelMonitorProbeRepository_GlobalBudgetConcurrentTargets(t *testing.T) {
	now := time.Date(2031, 1, 2, 12, 0, 0, 0, time.UTC)
	a := probeIntegrationTarget(t, now)
	b := probeIntegrationTarget(t, now)
	repo := NewChannelMonitorProbeRepository(integrationDB)
	ctx := context.Background()
	_, err := integrationDB.ExecContext(ctx, `INSERT INTO channel_monitor_probe_budgets(utc_day,target_id,used) VALUES($1,0,999)`, now.Format("2006-01-02"))
	require.NoError(t, err)
	var wg sync.WaitGroup
	var owners atomic.Int32
	for _, id := range []int64{a.ID, b.ID} {
		wg.Add(1)
		go func(id int64) {
			defer wg.Done()
			_, owned, err := repo.Reserve(ctx, id, fmt.Sprintf("global-boundary-%d", id), true, now)
			if owned {
				owners.Add(1)
			} else {
				require.ErrorIs(t, err, service.ErrChannelMonitorProbeBudget)
			}
		}(id)
	}
	wg.Wait()
	require.Equal(t, int32(1), owners.Load())
	budget, err := repo.Budget(ctx, now)
	require.NoError(t, err)
	require.Equal(t, 1000, budget.GlobalUsed)
}

func TestChannelMonitorProbeRepository_TargetBudgetAndUTCReset(t *testing.T) {
	now := time.Date(2031, 1, 3, 23, 59, 0, 0, time.UTC)
	target := probeIntegrationTarget(t, now)
	repo := NewChannelMonitorProbeRepository(integrationDB)
	ctx := context.Background()
	_, err := integrationDB.ExecContext(ctx, `INSERT INTO channel_monitor_probe_budgets(utc_day,target_id,used) VALUES($1,$2,96)`, now.Format("2006-01-02"), target.ID)
	require.NoError(t, err)
	_, _, err = repo.Reserve(ctx, target.ID, "limit-before-midnight", true, now)
	require.ErrorIs(t, err, service.ErrChannelMonitorProbeBudget)
	run, owner, err := repo.Reserve(ctx, target.ID, "limit-after-midnight", true, now.Add(time.Minute))
	require.NoError(t, err)
	require.True(t, owner)
	require.NotNil(t, run)
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM channel_monitor_probe_budgets WHERE target_id=0 AND utc_day=$1`, now.Add(time.Minute).Format("2006-01-02"))
	})
}

func TestChannelMonitorProbeRepository_VersionAndLease(t *testing.T) {
	now := time.Now().UTC()
	target := probeIntegrationTarget(t, now)
	repo := NewChannelMonitorProbeRepository(integrationDB)
	ctx := context.Background()
	stale := *target
	target.Enabled = false
	require.NoError(t, repo.SaveTarget(ctx, target))
	require.ErrorIs(t, repo.SaveTarget(ctx, &stale), service.ErrChannelMonitorProbeConflict)
	_, _, err := repo.Reserve(ctx, target.ID, "disabled-probe-target", true, now)
	require.ErrorIs(t, err, service.ErrChannelMonitorProbeDisabled)
	target.Enabled = true
	require.NoError(t, repo.SaveTarget(ctx, target))
	_, owner, err := repo.Reserve(ctx, target.ID, "lease-first-operation", true, now)
	require.NoError(t, err)
	require.True(t, owner)
	_, _, err = repo.Reserve(ctx, target.ID, "lease-second-operation", true, now)
	require.ErrorIs(t, err, service.ErrChannelMonitorProbeBusy)
	require.ErrorIs(t, repo.SaveTarget(ctx, target), service.ErrChannelMonitorProbeConflict)
}

func TestChannelMonitorProbeRepository_QuotaClaimAndSafeSummary(t *testing.T) {
	now := time.Now().UTC()
	target := probeIntegrationTarget(t, now)
	repo := NewChannelMonitorProbeRepository(integrationDB)
	ctx := context.Background()
	var accountID int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO accounts(name,platform,type) VALUES($1,'openai','apikey') RETURNING id`, fmt.Sprintf("probe-quota-%d", time.Now().UnixNano())).Scan(&accountID))
	_, err := integrationDB.ExecContext(ctx, `INSERT INTO account_groups(account_id,group_id) VALUES($1,$2)`, accountID, target.GroupID)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM account_groups WHERE account_id=$1`, accountID)
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM accounts WHERE id=$1`, accountID)
	})
	ids, err := repo.QuotaAccounts(ctx)
	require.NoError(t, err)
	require.Contains(t, ids, accountID)
	claimed, err := repo.ClaimQuota(ctx, accountID, now)
	require.NoError(t, err)
	require.True(t, claimed)
	claimed, err = repo.ClaimQuota(ctx, accountID, now.Add(4*time.Minute))
	require.NoError(t, err)
	require.False(t, claimed)
	require.NoError(t, repo.StoreQuota(ctx, accountID, &domain.MonitorQuotaSnapshot{Success: true, Source: "observed", FetchedAt: now}))
	states, err := repo.QuotaSummaries(ctx, []int64{target.GroupID}, now)
	require.NoError(t, err)
	require.Equal(t, "available", states[target.GroupID].State)
	states, err = repo.QuotaSummaries(ctx, []int64{target.GroupID}, now.Add(11*time.Minute))
	require.NoError(t, err)
	require.Equal(t, "stale", states[target.GroupID].State)
	states, err = repo.QuotaSummaries(ctx, nil, now)
	require.NoError(t, err)
	require.Empty(t, states)
}

func TestChannelMonitorProbeRepository_InterruptedRunNotHealthEvidence(t *testing.T) {
	now := time.Now().UTC()
	target := probeIntegrationTarget(t, now)
	repo := NewChannelMonitorProbeRepository(integrationDB)
	ctx := context.Background()
	run, owner, err := repo.Reserve(ctx, target.ID, "interrupted-operation", true, now.Add(-2*time.Minute))
	require.NoError(t, err)
	require.True(t, owner)
	probeRepo, ok := repo.(*channelMonitorProbeRepository)
	require.True(t, ok)
	require.NoError(t, probeRepo.MaintainProbe(ctx, now))
	replayed, owner, err := repo.Reserve(ctx, target.ID, "interrupted-operation", true, now)
	require.NoError(t, err)
	require.False(t, owner)
	require.Equal(t, run.ID, replayed.ID)
	require.Equal(t, "execution_interrupted", replayed.ErrorClass)
	require.Nil(t, replayed.Success)
}

func TestChannelMonitorProbeRepository_DuplicateIdentityUpdateIsConflict(t *testing.T) {
	now := time.Now().UTC()
	target := probeIntegrationTarget(t, now)
	repo := NewChannelMonitorProbeRepository(integrationDB)
	ctx := context.Background()
	second := &service.ChannelMonitorProbeTarget{GroupID: target.GroupID, Model: "gpt-other", Protocol: target.Protocol, Enabled: true}
	require.NoError(t, repo.SaveTarget(ctx, second))
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), `DELETE FROM channel_monitor_probe_targets WHERE id=$1`, second.ID)
	})
	second.Model = target.Model
	require.ErrorIs(t, repo.SaveTarget(ctx, second), service.ErrChannelMonitorProbeConflict)
}
