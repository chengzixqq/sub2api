//go:build unit

package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func advisorTestService() (*GatewayService, *APIKey) {
	bs := newTestBillingServiceForResolver()
	bs.fallbackPrices["advisor-test"] = &ModelPricing{InputPricePerToken: .01, OutputPricePerToken: .02, CacheCreationPricePerToken: .03, CacheReadPricePerToken: .004,
		CacheCreation5mPrice: .03, CacheCreation1hPrice: .06, SupportsCacheBreakdown: true}
	r := NewModelPricingResolver(nil, bs)
	gid := int64(1)
	return &GatewayService{billingService: bs, resolver: r}, &APIKey{GroupID: &gid, Group: &Group{ID: gid, LongContextPricingEnabled: true}}
}

func advisorTestUsage(advisorModel string) ClaudeUsage {
	body := fmt.Sprintf(`{"type":"message","stop_reason":"end_turn","usage":{"input_tokens":100,"output_tokens":10,"iterations":[{"type":"message","input_tokens":50,"output_tokens":4},{"type":"advisor_message","model":%q,"input_tokens":10,"output_tokens":20},{"type":"message","input_tokens":50,"output_tokens":6}]}}`, advisorModel)
	return *parseClaudeUsageFromResponseBody([]byte(body))
}

func TestAdvisorBilling_IndependentModelAndMultiplier(t *testing.T) {
	s, key := advisorTestService()
	u := advisorTestUsage("advisor-test")
	cost := s.calculateTokenCost(context.Background(), &ForwardResult{Usage: u}, key, "claude-sonnet-4", 2, time.Now())
	require.InDelta(t, .50045, cost.TotalCost, 1e-12)
	require.InDelta(t, 1.0009, cost.ActualCost, 1e-12)
	require.Equal(t, "complete", cost.BillingDetails.Status)
	require.Len(t, cost.BillingDetails.Components, 3)
	require.Equal(t, 1, *cost.BillingDetails.Components[1].IterationIndex)
	require.Equal(t, 100, u.InputTokens)
	require.Equal(t, 10, u.OutputTokens)
}

func TestAdvisorBilling_PerRequestOnceAndAdvisorStandardTier(t *testing.T) {
	s, key := advisorTestService()
	key.Group.ModelPricing = []ChannelModelPricing{{Models: []string{"claude-sonnet-4"}, BillingMode: BillingModePerRequest, PerRequestPrice: billingAmount(.25)},
		{Models: []string{"advisor-test"}, BillingMode: BillingModePerRequest, PerRequestPrice: billingAmount(99)}}
	priority := "priority"
	cost := s.calculateTokenCost(context.Background(), &ForwardResult{Usage: advisorTestUsage("advisor-test"), ServiceTier: &priority}, key, "claude-sonnet-4", 2, time.Now())
	require.InDelta(t, .75, cost.TotalCost, 1e-12)
	require.InDelta(t, 1.5, cost.ActualCost, 1e-12)
	require.Len(t, cost.BillingDetails.Components, 2)
	require.Equal(t, "per_request", cost.BillingDetails.Components[0].BillingMode)
	require.Equal(t, "token", cost.BillingDetails.Components[1].BillingMode)
}

func TestAdvisorBilling_UnpricedActualModelIsNull(t *testing.T) {
	s, key := advisorTestService()
	cost := s.calculateTokenCost(context.Background(), &ForwardResult{Usage: advisorTestUsage("claude-invented-opus-unknown")}, key, "claude-sonnet-4", 1, time.Now())
	require.InDelta(t, .00045, cost.TotalCost, 1e-12)
	require.Equal(t, "partial_unpriced", cost.BillingDetails.Status)
	component := cost.BillingDetails.Components[1]
	require.Nil(t, component.TotalCost)
	require.Nil(t, component.ActualCost)
	require.Equal(t, "advisor_pricing_unavailable", component.UnpricedReason)
	require.Equal(t, 10, component.InputTokens)
}

func TestAdvisorBilling_ExecutorUsesPerIterationLongContext(t *testing.T) {
	s, key := advisorTestService()
	p := s.billingService.fallbackPrices["claude-sonnet-4"]
	p.LongContextInputThreshold = 60
	p.LongContextInputMultiplier = 2
	p.LongContextOutputMultiplier = 2
	cost := s.calculateTokenCost(context.Background(), &ForwardResult{Usage: advisorTestUsage("advisor-test")}, key, "claude-sonnet-4", 1, time.Now())
	require.InDelta(t, .50045, cost.TotalCost, 1e-12, "two 50-token executor iterations must not become one long-context iteration")
	require.False(t, cost.LongContextBillingApplied)
}

func TestAdvisorBilling_PreflightRejectsUnknownAndAllowsFree(t *testing.T) {
	s, key := advisorTestService()
	body := []byte(`{"tools":[{"type":"advisor_20260301","model":"claude-invented-opus-unknown"}]}`)
	require.ErrorIs(t, s.ValidateAdvisorPricing(context.Background(), key, body), ErrAdvisorPricingUnavailable)
	zero := 0.0
	key.Group.ModelPricing = []ChannelModelPricing{{Models: []string{"claude-invented-opus-unknown"}, InputPrice: &zero, OutputPrice: &zero, CacheWritePrice: &zero, CacheReadPrice: &zero}}
	require.NoError(t, s.ValidateAdvisorPricing(context.Background(), key, body))
	cost := s.calculateTokenCost(context.Background(), &ForwardResult{Usage: advisorTestUsage("claude-invented-opus-unknown")}, key, "claude-sonnet-4", 1, time.Now())
	require.NotNil(t, cost.BillingDetails.Components[1].TotalCost)
	require.Zero(t, *cost.BillingDetails.Components[1].TotalCost)
	require.Equal(t, "complete", cost.BillingDetails.Status)
}

func TestAdvisorBilling_PreflightBeforeUpstream(t *testing.T) {
	s, _ := advisorTestService()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	// No account or HTTP client: reaching either dependency would panic.
	_, err := s.forwardAnthropicAPIKeyPassthroughWithInput(context.Background(), c, nil, anthropicPassthroughForwardInput{Body: []byte(`{"tools":[{"type":"advisor_20260301","model":"unknown"}]}`)})
	require.ErrorIs(t, err, ErrAdvisorPricingUnavailable)
	require.Equal(t, 503, w.Code)
}

func TestAdvisorUsage_CumulativeReplacementAndIdenticalCalls(t *testing.T) {
	for _, parser := range []struct {
		name  string
		parse func(string, *ClaudeUsage)
	}{{"passthrough", parseSSEUsagePassthrough}, {"normal", (&GatewayService{}).parseSSEUsage}} {
		t.Run(parser.name, func(t *testing.T) {
			var u ClaudeUsage
			frame := `{"type":"message_delta","usage":{"input_tokens":2,"output_tokens":3,"iterations":[{"type":"message","input_tokens":2,"output_tokens":3},{"type":"advisor_message","model":"advisor-test","input_tokens":10,"output_tokens":20},{"type":"advisor_message","model":"advisor-test","input_tokens":10,"output_tokens":20}]}}`
			parser.parse(frame, &u)
			copy := u.Clone()
			parser.parse(frame, &u)
			parser.parse(`{"type":"message_delta","usage":{"output_tokens":3}}`, &u)
			parser.parse(`{"type":"message_stop"}`, &u)
			require.Len(t, u.IterationUsage.Iterations, 3)
			require.True(t, claudeExecutorIterationsComplete(u))
			require.False(t, copy.IterationUsage.Terminal, "older failure snapshot is immutable")
			s, key := advisorTestService()
			cost := s.calculateTokenCost(context.Background(), &ForwardResult{Usage: u}, key, "claude-sonnet-4", 1, time.Now())
			require.InDelta(t, 1.000051, cost.TotalCost, 1e-12)
		})
	}
}

func TestAdvisorUsage_MalformedSnapshotsPreserveLastValid(t *testing.T) {
	for _, tail := range []string{`[]`, `[{"type":"advisor_message","model":"advisor-test","input_tokens":-1,"output_tokens":2}]`, `[{"type":"advisor_message","model":"advisor-test","input_tokens":1.5,"output_tokens":2}]`, `{}`} {
		u := advisorTestUsage("advisor-test")
		observeClaudeIterationEvent(`{"type":"message_delta","usage":{"iterations":`+tail+`}}`, &u)
		require.True(t, u.IterationUsage.Invalid)
		require.Len(t, u.IterationUsage.Iterations, 3)
		s, key := advisorTestService()
		cost := s.calculateTokenCost(context.Background(), &ForwardResult{Usage: u}, key, "claude-sonnet-4", 1, time.Now())
		require.Equal(t, "partial_usage", cost.BillingDetails.Status)
		require.InDelta(t, .50045, cost.TotalCost, 1e-12)
	}
}

func TestAdvisorUsage_AdvisorOnlyFailureIsBillable(t *testing.T) {
	u := ClaudeUsage{}
	observeClaudeIterationEvent(`{"type":"message_delta","usage":{"iterations":[{"type":"advisor_message","model":"advisor-test","input_tokens":5,"output_tokens":6}]}}`, &u)
	require.True(t, u.hasObservedTokens())
	decision := DecideFailureBilling(FailureBillingInput{Usage: u, UpstreamUsageOnly: true})
	require.True(t, decision.Billable)
	require.Equal(t, BillingProvenanceFailedUpstream, decision.Provenance)
	require.True(t, decision.Usage.HasAdvisorUsage())
}

func TestAdvisorBilling_CacheOverrideOnlyExecutor(t *testing.T) {
	u := *parseClaudeUsageFromResponseBody([]byte(`{"type":"message","stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":3,"cache_creation_input_tokens":4,"cache_creation":{"ephemeral_1h_input_tokens":4},"iterations":[{"type":"message","input_tokens":2,"output_tokens":3,"cache_creation_input_tokens":4,"cache_creation":{"ephemeral_1h_input_tokens":4}},{"type":"advisor_message","model":"advisor-test","input_tokens":5,"output_tokens":6,"cache_creation_input_tokens":7,"cache_creation":{"ephemeral_1h_input_tokens":7}}]}}`))
	applyCacheTTLOverride(&u, "5m")
	require.True(t, claudeExecutorIterationsComplete(u))
	require.Equal(t, 4, u.IterationUsage.Iterations[0].Usage.CacheCreation5mTokens)
	require.Equal(t, 7, u.IterationUsage.Iterations[1].Usage.CacheCreation1hTokens)
	s, key := advisorTestService()
	cost := s.calculateTokenCost(context.Background(), &ForwardResult{Usage: u}, key, "claude-sonnet-4", 1, time.Now())
	require.InDelta(t, .59, *cost.BillingDetails.Components[1].TotalCost, 1e-12)
}

func TestAdvisorBilling_RecordUsageOneCommandAndAccountCost(t *testing.T) {
	for _, providerRecorded := range []bool{true, false} {
		t.Run(fmt.Sprint(providerRecorded), func(t *testing.T) {
			usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
			billingRepo := &openAIRecordUsageBillingRepoStub{result: &UsageBillingApplyResult{Applied: true}}
			svc := newGatewayRecordUsageServiceWithBillingRepoForTest(usageRepo, billingRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{})
			prices, key := advisorTestService()
			svc.billingService, svc.resolver = prices.billingService, prices.resolver
			key.ID = 11
			key.Group.RateMultiplier = 2
			user := &User{ID: 22}
			key.User = user
			account := &Account{ID: 33, RateMultiplier: billingAmount(.5)}
			input := &recordUsageCoreInput{Result: &ForwardResult{RequestID: "advisor-record", Model: "claude-sonnet-4", Usage: advisorTestUsage("advisor-test")}, APIKey: key, User: user, Account: account,
				ProbeCoalesced: true, ProviderCostRecorded: providerRecorded}
			require.NoError(t, svc.recordUsageCore(context.Background(), input, nil))
			require.Equal(t, 1, billingRepo.calls)
			require.Equal(t, 1, usageRepo.calls)
			log := usageRepo.lastLog
			require.Equal(t, 100, log.InputTokens)
			require.Equal(t, 10, log.OutputTokens)
			require.InDelta(t, .50045, log.TotalCost, 1e-8)
			require.InDelta(t, 1.0009, log.ActualCost, 1e-8)
			require.NotNil(t, log.BillingDetails)
			var actual, total, accountTotal float64
			for _, c := range log.BillingDetails.Components {
				actual += *c.ActualCost
				total += *c.TotalCost
				accountTotal += *c.AccountStatsCost
			}
			require.InDelta(t, log.ActualCost, actual, 1e-8)
			require.InDelta(t, log.TotalCost, total, 1e-8)
			if providerRecorded {
				require.InDelta(t, .50045, accountTotal, 1e-8)
				require.InDelta(t, .250225, log.AccountCost(), 1e-8)
			} else {
				require.Zero(t, accountTotal)
				require.Zero(t, log.AccountCost())
			}
		})
	}
}

func TestAdvisorBilling_FailedSettlementClearsOnlyKnownActualCosts(t *testing.T) {
	for _, advisorModel := range []string{"advisor-test", "claude-unknown-advisor"} {
		t.Run(advisorModel, func(t *testing.T) {
			usageRepo := &openAIRecordUsageLogRepoStub{}
			billingErr := errors.New("advisor billing transaction failed")
			billingRepo := &openAIRecordUsageBillingRepoStub{err: billingErr}
			svc := newGatewayRecordUsageServiceWithBillingRepoForTest(usageRepo, billingRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{})
			prices, key := advisorTestService()
			svc.billingService, svc.resolver = prices.billingService, prices.resolver
			key.ID = 11
			key.Group.RateMultiplier = 2
			user := &User{ID: 22}
			key.User = user
			err := svc.RecordUsage(context.Background(), &RecordUsageInput{
				Result: &ForwardResult{RequestID: "advisor-failed", Model: "claude-sonnet-4", Usage: advisorTestUsage(advisorModel)},
				APIKey: key, User: user, Account: &Account{ID: 33},
			})
			require.ErrorIs(t, err, billingErr)
			require.Equal(t, 1, billingRepo.calls)
			require.Equal(t, 1, usageRepo.calls)
			log := usageRepo.lastLog
			require.Zero(t, log.ActualCost)
			require.Len(t, log.BillingDetails.Components, 3)
			var total float64
			for _, component := range log.BillingDetails.Components {
				if component.TotalCost == nil {
					require.Nil(t, component.ActualCost)
					continue
				}
				total += *component.TotalCost
				require.NotNil(t, component.ActualCost)
				require.Zero(t, *component.ActualCost)
			}
			require.InDelta(t, log.TotalCost, total, 1e-8)
			if advisorModel == "advisor-test" {
				require.InDelta(t, .50045, log.TotalCost, 1e-8)
			} else {
				require.InDelta(t, .00045, log.TotalCost, 1e-8)
				require.Equal(t, "partial_unpriced", log.BillingDetails.Status)
				require.Nil(t, log.BillingDetails.Components[1].ActualCost)
			}
		})
	}
}

func TestAdvisorBilling_AccountRulesUseAdvisorModel(t *testing.T) {
	r := newResolverWithChannel(t, []ChannelModelPricing{{Platform: "anthropic", Models: []string{"advisor-test"}, InputPrice: billingAmount(.04), OutputPrice: billingAmount(.08), CacheWritePrice: billingAmount(.05), CacheReadPrice: billingAmount(.005)}})
	r.billingService.fallbackPrices["advisor-test"] = &ModelPricing{InputPricePerToken: .01, OutputPricePerToken: .02}
	svc := &GatewayService{billingService: r.billingService, resolver: r, channelService: r.channelService}
	gid := groupIDPtr()
	key := &APIKey{GroupID: gid, Group: &Group{ID: *gid, LongContextPricingEnabled: true}}
	result := &ForwardResult{Usage: advisorTestUsage("advisor-test"), Model: "claude-sonnet-4"}
	cost := svc.calculateTokenCost(context.Background(), result, key, "claude-sonnet-4", 1, time.Now())
	require.InDelta(t, 2.00045, cost.TotalCost, 1e-8)
	log := &UsageLog{BillingDetails: cost.BillingDetails.Clone(), TotalCost: cost.TotalCost, ActualCost: cost.ActualCost}
	svc.applyAdvisorAccountStatsCost(context.Background(), log, key, &Account{ID: 99}, result, time.Now(), true)
	require.InDelta(t, .5, *log.BillingDetails.Components[1].AccountStatsCost, 1e-8, "default provider price uses the advisor catalog model")
}

func TestAdvisorBilling_ResponseModelTrialDoesNotReplaceRejectedDetails(t *testing.T) {
	for _, responseModel := range []string{"claude-3-5-haiku", "claude-opus-4.8"} {
		t.Run(responseModel, func(t *testing.T) {
			repo := &openAIRecordUsageLogRepoStub{inserted: true}
			svc := newGatewayRecordUsageServiceForTest(repo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{})
			svc.resolver = NewModelPricingResolver(nil, svc.billingService)
			usage := advisorTestUsage("claude-sonnet-4")
			user := &User{ID: 2}
			key := &APIKey{ID: 1, User: user, Group: &Group{ID: 5, RateMultiplier: 1, LongContextPricingEnabled: true}}
			result := &ForwardResult{RequestID: "response-trial", Model: "claude-sonnet-4", Usage: usage, UpstreamResponseModel: responseModel}
			baseline := svc.calculateTokenCost(context.Background(), result, key, "claude-sonnet-4", 1, time.Now())
			trial := svc.calculateTokenCost(context.Background(), result, key, responseModel, 1, time.Now())
			want := baseline
			wantModel := "claude-sonnet-4"
			if trial.TotalCost < baseline.TotalCost {
				want = trial
				wantModel = responseModel
			}
			err := svc.RecordUsage(context.Background(), &RecordUsageInput{Result: result, APIKey: key, User: user, Account: &Account{ID: 3}, ChannelUsageFields: ChannelUsageFields{ChannelID: 9, BillingModelSource: BillingModelSourceResponse, OriginalModel: "claude-sonnet-4", ChannelMappedModel: "claude-sonnet-4"}})
			require.NoError(t, err)
			require.InDelta(t, want.TotalCost, repo.lastLog.TotalCost, 1e-8)
			require.Equal(t, wantModel, repo.lastLog.BillingDetails.Components[0].Model)
			require.Equal(t, "claude-sonnet-4", repo.lastLog.BillingDetails.Components[1].Model)
		})
	}
}

func TestAdvisorUsage_UnknownIterationUsesExecutorAggregate(t *testing.T) {
	u := advisorTestUsage("advisor-test")
	u.IterationUsage.Iterations = append(u.IterationUsage.Iterations, ClaudeUsageIteration{Type: "compaction", Usage: UsageTokens{InputTokens: 900, OutputTokens: 800}})
	s, key := advisorTestService()
	cost := s.calculateTokenCost(context.Background(), &ForwardResult{Usage: u}, key, "claude-sonnet-4", 1, time.Now())
	require.Equal(t, "partial_usage", cost.BillingDetails.Status)
	require.InDelta(t, .50045, cost.TotalCost, 1e-12, "unknown iteration is not silently treated as executor")
	require.Len(t, cost.BillingDetails.Components, 2)
}

func TestAdvisorUsage_InvalidExecutorKeepsValidAdvisorAtOriginalIndex(t *testing.T) {
	for _, model := range []string{"advisor-test", "claude-unknown-advisor"} {
		u := *parseClaudeUsageFromResponseBody([]byte(fmt.Sprintf(`{"type":"message","stop_reason":"end_turn","usage":{"input_tokens":100,"output_tokens":10,"iterations":[{"type":"message","input_tokens":100},{"type":"advisor_message","model":%q,"input_tokens":10,"output_tokens":20}]}}`, model)))
		require.True(t, u.IterationUsage.Invalid)
		require.True(t, u.IterationUsage.Iterations[0].Invalid)
		require.False(t, u.IterationUsage.Iterations[1].Invalid)
		require.True(t, u.HasAdvisorUsage())
		s, key := advisorTestService()
		cost := s.calculateTokenCost(context.Background(), &ForwardResult{Usage: u}, key, "claude-sonnet-4", 1, time.Now())
		require.Len(t, cost.BillingDetails.Components, 2)
		require.Equal(t, 1, *cost.BillingDetails.Components[1].IterationIndex)
		if model == "advisor-test" {
			require.InDelta(t, .50045, cost.TotalCost, 1e-12)
			require.Equal(t, "partial_usage", cost.BillingDetails.Status)
		} else {
			require.InDelta(t, .00045, cost.TotalCost, 1e-12)
			require.Equal(t, "partial_unpriced", cost.BillingDetails.Status)
			require.Nil(t, cost.BillingDetails.Components[1].TotalCost)
		}
	}
}

func TestAdvisorUsage_IncompleteTailAndDecreasingCacheRetainKnownSnapshot(t *testing.T) {
	for _, tail := range []string{
		`{"type":"advisor_message","model":"advisor-test","input_tokens":10}`,
		`{"type":"advisor_message","model":"advisor-test","input_tokens":10,"output_tokens":20,"cache_creation_input_tokens":7,"cache_creation":{"ephemeral_5m_input_tokens":1,"ephemeral_1h_input_tokens":6}}`,
		`{"type":"advisor_message","model":"advisor-test","input_tokens":9223372036854775807,"output_tokens":20}`,
	} {
		var usage ClaudeUsage
		observeClaudeIterationEvent(`{"type":"message_delta","usage":{"iterations":[{"type":"advisor_message","model":"advisor-test","input_tokens":10,"output_tokens":20,"cache_creation_input_tokens":7,"cache_creation":{"ephemeral_5m_input_tokens":3,"ephemeral_1h_input_tokens":4}}]}}`, &usage)
		observeClaudeIterationEvent(`{"type":"message_delta","usage":{"iterations":[`+tail+`]}}`, &usage)
		require.True(t, usage.IterationUsage.Invalid)
		require.Equal(t, 20, usage.IterationUsage.Iterations[0].Usage.OutputTokens)
		require.Equal(t, 3, usage.IterationUsage.Iterations[0].Usage.CacheCreation5mTokens)
		require.Equal(t, 4, usage.IterationUsage.Iterations[0].Usage.CacheCreation1hTokens)
	}
}

type advisorAtomicBillingStub struct {
	UsageBillingRepository
	calls   int
	log     *UsageLog
	command *UsageBillingCommand
}

func (r *advisorAtomicBillingStub) ApplyWithUsageLog(_ context.Context, cmd *UsageBillingCommand, log *UsageLog) (*UsageBillingApplyResult, error) {
	r.calls++
	r.log, r.command = log, cmd
	return &UsageBillingApplyResult{Applied: true, UsageLogPersisted: true}, nil
}

func TestAdvisorBilling_SyntheticSnapshotSettlesAllModelsAtomically(t *testing.T) {
	for _, advisorModel := range []string{"advisor-test", "claude-unknown-advisor"} {
		t.Run(advisorModel, func(t *testing.T) {
			prices, key := advisorTestService()
			key.ID = 11
			key.Group.RateMultiplier = 1
			user := &User{ID: 22}
			key.User = user
			billing := &advisorAtomicBillingStub{}
			usageRepo := &probeFailingUsageLogRepoStub{}
			svc := newGatewayRecordUsageServiceForTest(usageRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{})
			svc.billingService, svc.resolver, svc.usageBillingRepo = prices.billingService, prices.resolver, billing
			snapshot := advisorTestUsage(advisorModel)
			err := svc.RecordSyntheticProbeUsage(context.Background(), &SyntheticProbeUsageInput{APIKey: key, User: user, Account: &Account{ID: 33}, Model: "claude-sonnet-4", RequestID: "probe:advisor-follower", InputTokens: 1, OutputTokens: 1,
				UsageSnapshot: &snapshot, ProbeCoalesced: true, ProbeLeaderRequestID: "probe:leader", ProviderCostRecorded: false})
			require.NoError(t, err)
			require.Equal(t, 1, billing.calls)
			require.Zero(t, usageRepo.createCalls)
			require.Equal(t, 100, billing.log.InputTokens)
			require.Equal(t, 10, billing.log.OutputTokens)
			require.Zero(t, billing.log.AccountCost())
			require.Equal(t, "probe:leader", *billing.log.ProbeLeaderRequestID)
			require.Len(t, billing.log.BillingDetails.Components, 3)
			if advisorModel == "advisor-test" {
				require.InDelta(t, .50045, billing.log.TotalCost, 1e-8)
			} else {
				require.Equal(t, "partial_unpriced", billing.log.BillingDetails.Status)
				require.Nil(t, billing.log.BillingDetails.Components[1].TotalCost)
			}
		})
	}
}

func TestAdvisorUsage_AllAnthropicStreamingPathsRetainIterations(t *testing.T) {
	payload := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"msg_advisor","type":"message","role":"assistant","content":[],"model":"claude-sonnet-4","usage":{"input_tokens":2,"output_tokens":0}}}`,
		``,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":"ok"}}`,
		``,
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":2,"output_tokens":3,"cache_creation_input_tokens":4,"cache_creation":{"ephemeral_5m_input_tokens":1,"ephemeral_1h_input_tokens":3},"iterations":[{"type":"message","input_tokens":2,"output_tokens":3,"cache_creation_input_tokens":4,"cache_creation":{"ephemeral_5m_input_tokens":1,"ephemeral_1h_input_tokens":3}},{"type":"advisor_message","model":"advisor-test","input_tokens":10,"output_tokens":20}]}}`,
		``,
		`event: message_stop`,
		`data: {"type":"message_stop"}`, ``, ``,
	}, "\n")
	for _, path := range []string{"normal", "passthrough", "responses_buffered", "responses_stream", "chat_buffered", "chat_stream"} {
		t.Run(path, func(t *testing.T) {
			svc := newMinimalGatewayService()
			c := partialUsageTestContext("/v1/messages")
			resp := &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(payload))}
			defer resp.Body.Close()
			var usage ClaudeUsage
			var err error
			switch path {
			case "normal":
				var result *streamingResult
				result, err = svc.handleStreamingResponse(context.Background(), resp, c, &Account{ID: 1}, time.Now(), "claude-sonnet-4", "claude-sonnet-4", false)
				if result != nil {
					usage = *result.usage
				}
			case "passthrough":
				var result *streamingResult
				result, err = svc.handleStreamingResponseAnthropicAPIKeyPassthrough(context.Background(), resp, c, &Account{ID: 1}, time.Now(), "claude-sonnet-4")
				if result != nil {
					usage = *result.usage
				}
			case "responses_buffered":
				var result *ForwardResult
				result, err = svc.handleResponsesBufferedStreamingResponse(resp, c, "claude-sonnet-4", "claude-sonnet-4", nil, time.Now(), apicompat.ResponsesClientToolMapping{})
				if result != nil {
					usage = result.Usage
				}
			case "responses_stream":
				var result *ForwardResult
				result, err = svc.handleResponsesStreamingResponse(resp, c, "claude-sonnet-4", "claude-sonnet-4", nil, time.Now(), apicompat.ResponsesClientToolMapping{})
				if result != nil {
					usage = result.Usage
				}
			case "chat_buffered":
				var result *ForwardResult
				result, err = svc.handleCCBufferedFromAnthropic(resp, c, "claude-sonnet-4", "claude-sonnet-4", nil, time.Now())
				if result != nil {
					usage = result.Usage
				}
			case "chat_stream":
				var result *ForwardResult
				result, err = svc.handleCCStreamingFromAnthropic(resp, c, "claude-sonnet-4", "claude-sonnet-4", nil, time.Now())
				if result != nil {
					usage = result.Usage
				}
			}
			require.NoError(t, err)
			require.True(t, usage.HasAdvisorUsage())
			require.Equal(t, 1, usage.CacheCreation5mTokens)
			require.Equal(t, 3, usage.CacheCreation1hTokens)
			require.True(t, claudeExecutorIterationsComplete(usage))
		})
	}
}

func TestAdvisorBilling_RoundingTinyIterationsNeverNegative(t *testing.T) {
	details := &BillingDetails{}
	for i := 0; i < 5; i++ {
		details.Components = append(details.Components, BillingComponent{TotalCost: billingAmount(.000000006), ActualCost: billingAmount(.000000006)})
	}
	details.Components = append(details.Components, BillingComponent{Role: "advisor", UnpricedReason: "advisor_pricing_unavailable"})
	quantizeBillingComponents(details, .00000003, .00000003, nil)
	var sum float64
	for _, c := range details.Components {
		if c.TotalCost != nil {
			require.GreaterOrEqual(t, *c.TotalCost, 0.0)
			sum += *c.TotalCost
		}
	}
	require.InDelta(t, .00000003, sum, 1e-15)
	require.Nil(t, details.Components[5].TotalCost)
}

func TestAdvisorUsage_OptionalTopLevelTTLDoesNotDiscardCompleteIterations(t *testing.T) {
	u := *parseClaudeUsageFromResponseBody([]byte(`{"type":"message","stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":3,"cache_creation_input_tokens":4,"iterations":[{"type":"message","input_tokens":2,"output_tokens":3,"cache_creation_input_tokens":4,"cache_creation":{"ephemeral_5m_input_tokens":1,"ephemeral_1h_input_tokens":3}},{"type":"advisor_message","model":"advisor-test","input_tokens":10,"output_tokens":20}]}}`))
	require.True(t, claudeExecutorIterationsComplete(u))
	s, key := advisorTestService()
	cost := s.calculateTokenCost(context.Background(), &ForwardResult{Usage: u}, key, "claude-sonnet-4", 1, time.Now())
	require.Equal(t, "complete", cost.BillingDetails.Status)
	require.Equal(t, 0, *cost.BillingDetails.Components[0].IterationIndex)
	require.Equal(t, 3, cost.BillingDetails.Components[0].CacheCreation1hTokens)
}
