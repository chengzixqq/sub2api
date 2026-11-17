//go:build integration

package repository

import (
	"context"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

func atomicLaneUsageLog(f batchBillingFixture, cmd *service.UsageBillingCommand) *service.UsageLog {
	leader := "leader-" + uuid.NewString()
	return &service.UsageLog{
		UserID: f.userID, APIKeyID: cmd.APIKeyID, AccountID: cmd.AccountID,
		RequestID: cmd.RequestID, Model: "claude-sonnet", RequestedModel: "claude-sonnet",
		InputTokens: 10, TotalCost: cmd.BalanceCost, ActualCost: cmd.BalanceCost,
		RateMultiplier: 1, ProbeCoalesced: true, ProbeLeaderRequestID: &leader,
		ProviderCostRecorded: false,
	}
}

func TestUsageBillingLaneAtomic_UsageInsertFailureRollsBackAndRetryDeduplicates(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	f := newBatchBillingFixture(t, client, 10)
	t.Cleanup(func() {
		// This fixture commits its writes; logs must not leak into later dashboard tests.
		_, err := integrationDB.ExecContext(context.Background(), "DELETE FROM usage_logs WHERE user_id = $1", f.userID)
		require.NoError(t, err)
	})
	repo := NewUsageBillingRepository(client, integrationDB)
	lanes := service.NewUsageBillingLaneCombiner(repo)
	defer lanes.Stop()
	require.True(t, lanes.SupportsAtomicUsageBilling())
	cmd := f.commands("atomic-rollback-"+uuid.NewString(), []batchBillingStep{{key: 'A', account: 'P', balance: 1.25, keyQuota: 1.25, keyRate: 1.25}})[0]
	log := atomicLaneUsageLog(f, cmd)
	log.Model = strings.Repeat("x", 101) // The usage row alone violates VARCHAR(100).
	before := readBatchBillingState(t, ctx, f)
	result, err := lanes.ApplyWithUsageLog(ctx, cmd, log)
	require.Error(t, err)
	var postgresError *pq.Error
	require.ErrorAs(t, err, &postgresError)
	require.Equal(t, pq.ErrorCode("22001"), postgresError.Code)
	require.Nil(t, result)
	require.Equal(t, before, readBatchBillingState(t, ctx, f), "usage insert failure must roll back balance, key quotas and the dedup claim")
	var count int
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM usage_logs WHERE request_id = $1 AND api_key_id = $2", cmd.RequestID, cmd.APIKeyID).Scan(&count))
	require.Zero(t, count)

	log.Model = "claude-sonnet"
	result, err = lanes.ApplyWithUsageLog(ctx, cmd, log)
	require.NoError(t, err)
	require.True(t, result.Applied)
	require.True(t, result.UsageLogPersisted)
	after := readBatchBillingState(t, ctx, f)
	require.Equal(t, "8.75000000", after.balance)
	require.Equal(t, "1.25000000", after.keyAUsed)
	require.Equal(t, 1, after.dedupRows)
	require.Equal(t, "0", after.pUsed, "coalesced followers must not consume provider account quota")
	var coalesced, providerRecorded bool
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT probe_coalesced, provider_cost_recorded FROM usage_logs WHERE request_id = $1 AND api_key_id = $2", cmd.RequestID, cmd.APIKeyID).Scan(&coalesced, &providerRecorded))
	require.True(t, coalesced)
	require.False(t, providerRecorded)

	result, err = lanes.ApplyWithUsageLog(ctx, cmd, log)
	require.NoError(t, err)
	require.False(t, result.Applied)
	require.True(t, result.UsageLogPersisted)
	require.Equal(t, after, readBatchBillingState(t, ctx, f), "retry must retain the persistence barrier without charging again")
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM usage_logs WHERE request_id = $1 AND api_key_id = $2", cmd.RequestID, cmd.APIKeyID).Scan(&count))
	require.Equal(t, 1, count)
}

func TestUsageBillingLaneAtomic_RepairsExistingChargeWithoutSecondDebit(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	f := newBatchBillingFixture(t, client, 10)
	t.Cleanup(func() {
		_, err := integrationDB.ExecContext(context.Background(), "DELETE FROM usage_logs WHERE user_id = $1", f.userID)
		require.NoError(t, err)
	})
	repo := NewUsageBillingRepository(client, integrationDB)
	lanes := service.NewUsageBillingLaneCombiner(repo)
	defer lanes.Stop()
	cmd := f.commands("atomic-repair-"+uuid.NewString(), []batchBillingStep{{key: 'A', account: 'P', balance: 1.25, keyQuota: 1.25}})[0]
	result, err := lanes.Apply(ctx, cmd)
	require.NoError(t, err)
	require.True(t, result.Applied)
	beforeRepair := readBatchBillingState(t, ctx, f)

	result, err = lanes.ApplyWithUsageLog(ctx, cmd, atomicLaneUsageLog(f, cmd))
	require.NoError(t, err)
	require.False(t, result.Applied)
	require.True(t, result.UsageLogPersisted)
	require.Equal(t, beforeRepair, readBatchBillingState(t, ctx, f))
	var count int
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM usage_logs WHERE request_id = $1 AND api_key_id = $2 AND probe_coalesced AND NOT provider_cost_recorded", cmd.RequestID, cmd.APIKeyID).Scan(&count))
	require.Equal(t, 1, count, "a legacy charge with no usage row must recover the follower row atomically")
}

func TestUsageBillingBatchUpgrade_QuantizesEachCommandBeforeAggregation(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewUsageBillingRepository(client, integrationDB)
	sequential := newBatchBillingFixture(t, client, 10)
	batched := newBatchBillingFixture(t, client, 10)
	// Twice the unrounded amount is 0.00015625; rounding each command first
	// must produce 0.00015626, the same total as sequential billing.
	steps := []batchBillingStep{
		{key: 'A', account: 'P', balance: 0.000078125, keyQuota: 0.000078125, keyRate: 0.000078125},
		{key: 'A', account: 'P', balance: 0.000078125, keyQuota: 0.000078125, keyRate: 0.000078125},
	}
	for _, cmd := range sequential.commands("round-seq-"+uuid.NewString(), steps) {
		_, err := repo.Apply(ctx, cmd)
		require.NoError(t, err)
	}
	commands := batched.commands("round-batch-"+uuid.NewString(), steps)
	outcomes, err := repo.ApplyBatch(ctx, commands)
	require.NoError(t, err)
	require.Len(t, outcomes, 2)
	for i, outcome := range outcomes {
		require.NoError(t, outcome.Err)
		require.True(t, outcome.Result.Applied)
		require.Equal(t, 0.00007813, commands[i].BalanceCost)
	}
	seqState := readBatchBillingState(t, ctx, sequential)
	batchState := readBatchBillingState(t, ctx, batched)
	require.Equal(t, seqState, batchState)
	require.Equal(t, "9.99984374", batchState.balance)
	require.Equal(t, "0.00015626", batchState.keyAUsed)
	outcomes, err = repo.ApplyBatch(ctx, commands)
	require.NoError(t, err)
	for _, outcome := range outcomes {
		require.NoError(t, outcome.Err)
		require.False(t, outcome.Result.Applied)
	}
	require.Equal(t, batchState, readBatchBillingState(t, ctx, batched))
}
