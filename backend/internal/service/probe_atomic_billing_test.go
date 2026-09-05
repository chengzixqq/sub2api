package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

type probeAtomicBillingRepoStub struct {
	UsageBillingRepository
	atomicCalls int
}

func (r *probeAtomicBillingRepoStub) Apply(context.Context, *UsageBillingCommand) (*UsageBillingApplyResult, error) {
	return &UsageBillingApplyResult{Applied: true}, nil
}

func (r *probeAtomicBillingRepoStub) ApplyWithUsageLog(context.Context, *UsageBillingCommand, *UsageLog) (*UsageBillingApplyResult, error) {
	r.atomicCalls++
	return &UsageBillingApplyResult{Applied: true, UsageLogPersisted: true}, nil
}

type probeFailingUsageLogRepoStub struct {
	UsageLogRepository
	bestEffortCalls int
	createCalls     int
}

func (r *probeFailingUsageLogRepoStub) CreateBestEffort(context.Context, *UsageLog) error {
	r.bestEffortCalls++
	return errors.New("unexpected second usage-log write")
}

func (r *probeFailingUsageLogRepoStub) Create(context.Context, *UsageLog) (bool, error) {
	r.createCalls++
	return false, errors.New("unexpected second usage-log write")
}

func TestApplyUsageBillingAtomicInsertIsTheStrictPersistenceBarrier(t *testing.T) {
	billingRepo := &probeAtomicBillingRepoStub{}
	usageRepo := &probeFailingUsageLogRepoStub{}
	params := &postUsageBillingParams{
		Cost: &CostBreakdown{
			BillingMode: string(BillingModeToken),
		},
		User:                       &User{ID: 1},
		APIKey:                     &APIKey{ID: 2},
		Account:                    &Account{ID: 3},
		ProbeCoalesced:             true,
		ProviderCostRecorded:       false,
		RequireUsageLogPersistence: true,
	}
	usageLog := &UsageLog{RequestID: "probe:atomic", APIKeyID: 2}

	applied, err := applyUsageBilling(context.Background(), usageLog.RequestID, usageLog, params, &billingDeps{}, billingRepo)
	require.NoError(t, err)
	require.True(t, applied)
	require.Equal(t, 1, billingRepo.atomicCalls)
	require.True(t, params.usageLogPersisted)
	require.Zero(t, usageRepo.bestEffortCalls)
	require.Zero(t, usageRepo.createCalls)
}
