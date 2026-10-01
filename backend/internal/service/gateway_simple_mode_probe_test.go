//go:build unit

package service

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type simpleModeAtomicProbeBillingRepo struct {
	UsageBillingRepository
	failNext   bool
	seen       map[string]bool
	calls      int
	applied    int
	windowCost float64
	lastCmd    *UsageBillingCommand
	lastCtxErr error
}

func (r *simpleModeAtomicProbeBillingRepo) ApplyWithUsageLog(ctx context.Context, cmd *UsageBillingCommand, usageLog *UsageLog) (*UsageBillingApplyResult, error) {
	r.calls++
	r.lastCmd = cmd
	r.lastCtxErr = ctx.Err()
	if r.failNext {
		r.failNext = false
		return nil, errors.New("atomic transaction unavailable")
	}
	key := fmt.Sprintf("%s/%d", cmd.RequestID, cmd.APIKeyID)
	if r.seen[key] {
		return &UsageBillingApplyResult{UsageLogPersisted: true}, nil
	}
	if r.seen == nil {
		r.seen = make(map[string]bool)
	}
	r.seen[key] = true
	r.applied++
	r.windowCost += cmd.APIKeyRateLimitCost
	return &UsageBillingApplyResult{Applied: true, UsageLogPersisted: true}, nil
}

func TestSimpleModeStrictProbeUsesAtomicDeduplication(t *testing.T) {
	for _, openAI := range []bool{false, true} {
		for _, providerCostRecorded := range []bool{false, true} {
			for _, failFirst := range []bool{false, true} {
				t.Run(fmt.Sprintf("openai=%v/provider=%v/retry=%v", openAI, providerCostRecorded, failFirst), func(t *testing.T) {
					logs := &probeFailingUsageLogRepoStub{}
					billing := &simpleModeAtomicProbeBillingRepo{failNext: failFirst}
					key := &APIKey{ID: 1, Quota: 100, RateLimit5h: 30, Group: &Group{RateMultiplier: 1}}
					user := &User{ID: 2, Balance: 100}
					account := &Account{ID: 3, Type: AccountTypeAPIKey}
					ctx, cancel := context.WithCancel(context.Background())
					cancel()
					var record func() error
					var deferred *DeferredService
					if openAI {
						svc := newOpenAIRecordUsageServiceWithBillingRepoForTest(logs, billing, nil, nil, nil)
						svc.cfg.RunMode = config.RunModeSimple
						svc.cfg.SimpleModeKeyRateLimitEnabled = true
						svc.resolver = NewModelPricingResolver(nil, svc.billingService)
						deferred = svc.deferredService
						record = func() error {
							return svc.RecordUsage(ctx, &OpenAIRecordUsageInput{
								Result: &OpenAIForwardResult{RequestID: "strict-simple-probe", Model: "gpt-5.1", Usage: OpenAIUsage{InputTokens: 100, OutputTokens: 20}},
								APIKey: key, User: user, Account: account,
								ProbeCoalesced: true, ProviderCostRecorded: providerCostRecorded, RequireUsageLogPersistence: true,
							})
						}
					} else {
						svc := newGatewayRecordUsageServiceWithBillingRepoForTest(logs, billing, nil, nil)
						svc.cfg.RunMode = config.RunModeSimple
						svc.cfg.SimpleModeKeyRateLimitEnabled = true
						svc.resolver = NewModelPricingResolver(nil, svc.billingService)
						deferred = svc.deferredService
						record = func() error {
							return svc.recordUsageCore(ctx, &recordUsageCoreInput{
								Result: &ForwardResult{RequestID: "strict-simple-probe", Model: "claude-sonnet-4", Usage: ClaudeUsage{InputTokens: 100, OutputTokens: 20}},
								APIKey: key, User: user, Account: account,
								ProbeCoalesced: true, ProviderCostRecorded: providerCostRecorded, RequireUsageLogPersistence: true,
							}, &recordUsageOpts{})
						}
					}
					if failFirst {
						require.Error(t, record())
						require.Zero(t, billing.applied)
						require.Zero(t, logs.createCalls)
						require.Zero(t, logs.bestEffortCalls)
					}
					require.NoError(t, record())
					require.NoError(t, record(), "retry must reuse the idempotent atomic path")
					require.Equal(t, 1, billing.applied)
					require.Positive(t, billing.windowCost)
					require.Equal(t, billing.lastCmd.APIKeyRateLimitCost, billing.windowCost)
					require.NoError(t, billing.lastCtxErr, "client cancellation must not cancel settlement")
					require.Zero(t, billing.lastCmd.BalanceCost)
					require.Zero(t, billing.lastCmd.SubscriptionCost)
					require.Zero(t, billing.lastCmd.APIKeyQuotaCost)
					require.Zero(t, billing.lastCmd.AccountQuotaCost)
					require.Zero(t, logs.createCalls, "atomic usage persistence must not be followed by another insert")
					require.Zero(t, logs.bestEffortCalls)
					_, scheduled := deferred.lastUsedUpdates.Load(account.ID)
					require.Equal(t, providerCostRecorded, scheduled, "followers must not update provider activity")
				})
			}
		}
	}
}
