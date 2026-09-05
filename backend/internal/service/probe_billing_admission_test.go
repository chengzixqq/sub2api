package service

import (
	"context"
	"errors"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type probeRequiredLogRepoStub struct {
	UsageLogRepository
	bestEffortErr   error
	createErr       error
	bestEffortCalls int
	createCalls     int
}

func (s *probeRequiredLogRepoStub) CreateBestEffort(context.Context, *UsageLog) error {
	s.bestEffortCalls++
	return s.bestEffortErr
}

func (s *probeRequiredLogRepoStub) Create(context.Context, *UsageLog) (bool, error) {
	s.createCalls++
	return false, s.createErr
}

func TestCanBillSyntheticProbeRejectsNonTokenChannelPricing(t *testing.T) {
	billing := &BillingService{}
	resolver := NewModelPricingResolver(nil, billing)
	usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
	billingRepo := &probeAtomicBillingRepoStub{}
	svc := &GatewayService{billingService: billing, resolver: resolver, usageLogRepo: usageRepo, usageBillingRepo: billingRepo, billingCacheService: &BillingCacheService{}, deferredService: &DeferredService{}}
	inputPrice := 0.000001
	outputPrice := 0.000002
	apiKey := &APIKey{Group: &Group{
		ID: 1,
		ModelPricing: []ChannelModelPricing{{
			Models:      []string{"probe-model"},
			BillingMode: BillingModePerRequest,
		}},
	}}

	require.False(t, svc.CanBillSyntheticProbe(context.Background(), apiKey, "probe-model"))

	apiKey.Group.ModelPricing[0].BillingMode = BillingModeToken
	apiKey.Group.ModelPricing[0].InputPrice = &inputPrice
	apiKey.Group.ModelPricing[0].OutputPrice = &outputPrice
	require.True(t, svc.CanBillSyntheticProbe(context.Background(), apiKey, "probe-model"))
}

func TestCanBillSyntheticProbeDoesNotFallThroughNonTokenCard(t *testing.T) {
	billing := NewBillingService(&config.Config{}, nil)
	resolver := NewModelPricingResolver(nil, billing)
	svc := &GatewayService{
		billingService:      billing,
		resolver:            resolver,
		usageLogRepo:        &openAIRecordUsageLogRepoStub{inserted: true},
		usageBillingRepo:    &probeAtomicBillingRepoStub{},
		billingCacheService: &BillingCacheService{},
		deferredService:     &DeferredService{},
	}
	apiKey := &APIKey{Group: &Group{ID: 1, ModelPricing: []ChannelModelPricing{{
		Models: []string{"claude-sonnet-4"}, BillingMode: BillingModePerRequest,
	}}}}
	require.False(t, svc.CanBillSyntheticProbe(context.Background(), apiKey, "claude-sonnet-4"))
}

func TestCanBillSyntheticProbeRejectsUnpricedTokenCard(t *testing.T) {
	billing := &BillingService{}
	resolver := NewModelPricingResolver(nil, billing)
	svc := &GatewayService{
		billingService:      billing,
		resolver:            resolver,
		usageLogRepo:        &openAIRecordUsageLogRepoStub{inserted: true},
		usageBillingRepo:    &probeAtomicBillingRepoStub{},
		billingCacheService: &BillingCacheService{},
		deferredService:     &DeferredService{},
	}
	apiKey := &APIKey{Group: &Group{ID: 1, ModelPricing: []ChannelModelPricing{{
		Models: []string{"unknown-probe-model"}, BillingMode: BillingModeToken,
	}}}}
	require.False(t, svc.CanBillSyntheticProbe(context.Background(), apiKey, "unknown-probe-model"))
}

func TestCanBillSyntheticProbeRequiresUnifiedBillingAndStandardMode(t *testing.T) {
	billing := &BillingService{}
	resolver := NewModelPricingResolver(nil, billing)
	inputPrice := 0.000001
	outputPrice := 0.000002
	apiKey := &APIKey{Group: &Group{ID: 1, ModelPricing: []ChannelModelPricing{{
		Models: []string{"probe-model"}, BillingMode: BillingModeToken,
		InputPrice: &inputPrice, OutputPrice: &outputPrice,
	}}}}
	base := &GatewayService{billingService: billing, resolver: resolver, usageLogRepo: &openAIRecordUsageLogRepoStub{inserted: true}, billingCacheService: &BillingCacheService{}, deferredService: &DeferredService{}}
	require.False(t, base.CanBillSyntheticProbe(context.Background(), apiKey, "probe-model"))
	base.usageBillingRepo = &probeAtomicBillingRepoStub{}
	base.cfg = &config.Config{RunMode: config.RunModeSimple}
	require.False(t, base.CanBillSyntheticProbe(context.Background(), apiKey, "probe-model"))
}

func TestRecordSyntheticProbeUsageRequiresUsageLogRepository(t *testing.T) {
	in := &SyntheticProbeUsageInput{
		APIKey:    &APIKey{},
		User:      &User{},
		Account:   &Account{},
		RequestID: "probe-request",
		Model:     "probe-model",
	}

	err := (&GatewayService{}).RecordSyntheticProbeUsage(context.Background(), in)
	require.ErrorIs(t, err, ErrSyntheticProbeBillingUnavailable)

	err = (&OpenAIGatewayService{}).RecordSyntheticProbeUsage(context.Background(), in)
	require.ErrorIs(t, err, ErrSyntheticProbeBillingUnavailable)
}

func TestWriteUsageLogRequiredReturnsPersistenceFailure(t *testing.T) {
	repo := &probeRequiredLogRepoStub{
		bestEffortErr: errors.New("batch unavailable"),
		createErr:     errors.New("database unavailable"),
	}
	err := writeUsageLogRequired(context.Background(), repo, &UsageLog{RequestID: "probe-log"})
	require.ErrorIs(t, err, ErrSyntheticProbeUsagePersistence)
	require.Equal(t, 1, repo.bestEffortCalls)
	require.Equal(t, 1, repo.createCalls)
}

func TestSyntheticCostCoversUsageRejectsUnmatchedIntervalPlaceholder(t *testing.T) {
	// The legacy resolver can return a token-mode zero breakdown when an
	// interval-only card has no matching range. Synthetic responses must take
	// the real path instead of silently becoming free.
	require.False(t, syntheticCostCoversUsage(&CostBreakdown{
		BillingMode: string(BillingModeToken),
		OutputCost:  0.001,
	}, 10, 1, 0, 0))
	require.True(t, syntheticCostCoversUsage(&CostBreakdown{
		BillingMode: string(BillingModeToken),
		InputCost:   0.001,
		OutputCost:  0.001,
	}, 10, 1, 0, 0))
}
