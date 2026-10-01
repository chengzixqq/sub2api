package handler

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestFailureSettlementPreservesReasoningPricing(t *testing.T) {
	for _, platform := range []string{service.PlatformAnthropic, service.PlatformOpenAI} {
		for _, tc := range []struct {
			name        string
			multipliers map[string]float64
			finalEffort string
			want        float64
		}{
			{"explicit max", map[string]float64{"max": 2.5}, "max", 0.5},
			{"default max", nil, "max", 0.2},
			{"retry changed effort", map[string]float64{"max": 2.5, "high": 1.5}, "high", 0.3},
			{"retry without effort", map[string]float64{"max": 2.5}, "", 0.2},
		} {
			t.Run(platform+"/"+tc.name, func(t *testing.T) {
				cfg := &config.Config{}
				cfg.Default.RateMultiplier = 1
				pricing := service.NewBillingService(cfg, nil)
				resolver := service.NewModelPricingResolver(nil, pricing)
				repo := &fakeFailureSinkUsageLogRepo{}
				c := newFailureSinkTestContext("/v1/messages")
				groupID := int64(77)
				inputPrice, outputPrice := 0.001, 0.002
				model := "claude-fable-5-1"
				key := &service.APIKey{ID: 10, User: &service.User{ID: 20}, GroupID: &groupID, Group: &service.Group{
					ID: groupID, Hydrated: true, Platform: platform, RateMultiplier: 1,
					ModelPricing: []service.ChannelModelPricing{{Models: []string{model}, BillingMode: service.BillingModeToken,
						InputPrice: &inputPrice, OutputPrice: &outputPrice, ReasoningEffortMultipliers: tc.multipliers}},
				}}
				var sink failureSettlementSink
				if platform == service.PlatformAnthropic {
					svc := service.NewGatewayService(nil, nil, repo, nil, &fakeFailureSinkUserRepo{}, &fakeFailureSinkSubRepo{}, nil, nil, cfg, nil, nil, pricing, nil, &service.BillingCacheService{}, nil, nil, &service.DeferredService{}, nil, nil, nil, nil, nil, nil, nil, resolver, nil, nil, nil, nil)
					sink = (&GatewayHandler{gatewayService: svc}).claudeFailureSink(c, key, nil, model, service.ChannelMappingResult{}, nil)
				} else {
					svc := service.NewOpenAIGatewayService(nil, repo, nil, &fakeFailureSinkUserRepo{}, &fakeFailureSinkSubRepo{}, nil, nil, cfg, nil, nil, pricing, nil, &service.BillingCacheService{}, nil, &service.DeferredService{}, nil, nil, resolver, nil, nil, nil, nil, nil)
					sink = (&OpenAIGatewayHandler{gatewayService: svc}).openAIFailureSink(c, openAIFailureSinkParams{APIKey: key, ReqModel: model})
				}
				guard := newBillingSettlementGuard(guardDeps{sink: sink})
				requested, initial := "max", "max"
				observe := func(effort *string) {
					if platform == service.PlatformAnthropic {
						guard.ObserveForwardResult(&service.ForwardResult{RequestID: "partial-request", UpstreamModel: model + "-routed", Usage: service.ClaudeUsage{InputTokens: 100, OutputTokens: 50}, ReasoningEffort: effort, RequestedReasoningEffort: &requested})
					} else {
						guard.ObserveOpenAIForwardResult(&service.OpenAIForwardResult{RequestID: "partial-request", UpstreamModel: model + "-routed", Usage: service.OpenAIUsage{InputTokens: 100, OutputTokens: 50}, ReasoningEffort: effort, RequestedReasoningEffort: &requested})
					}
					guard.ObserveForwardOutcome(errors.New("interrupted upstream"), true)
				}
				account := &service.Account{ID: 30, Platform: platform, Type: service.AccountTypeAPIKey}
				guard.ObserveAttempt(account)
				observe(&initial)
				if tc.finalEffort != initial {
					guard.ObserveAttempt(account)
					observe(&tc.finalEffort)
				}
				guard.Flush()
				guard.Flush()
				calls, usage := repo.snapshot()
				require.Equal(t, 1, calls)
				require.NotNil(t, usage)
				require.Equal(t, "partial-request", usage.RequestID)
				require.Equal(t, failureBillingStringPointer(model+"-routed"), usage.UpstreamModel)
				require.InDelta(t, tc.want, usage.TotalCost, 1e-8)
				require.Equal(t, failureBillingStringPointer(tc.finalEffort), usage.ReasoningEffort)
				require.Equal(t, &requested, usage.RequestedReasoningEffort)
			})
		}
	}
}

func TestFailureSinkWorkerDoesNotReadReusedGinContext(t *testing.T) {
	for _, platform := range []string{service.PlatformAnthropic, service.PlatformOpenAI} {
		t.Run(platform, func(t *testing.T) {
			pool := service.NewUsageRecordWorkerPoolWithOptions(service.UsageRecordWorkerPoolOptions{
				WorkerCount: 1, QueueSize: 4, TaskTimeout: time.Second,
			})
			t.Cleanup(pool.Stop)
			entered, release := make(chan struct{}), make(chan struct{})
			pool.Submit(func(context.Context) { close(entered); <-release })
			<-entered
			c := newFailureSinkTestContext("/v1/messages")
			pricingCtx, _ := service.WithGatewayTokenRequestPricing(c.Request.Context())
			c.Request = c.Request.WithContext(pricingCtx)
			repo := &fakeFailureSinkUsageLogRepo{}
			key := &service.APIKey{ID: 10, User: &service.User{ID: 20}}
			var sink failureSettlementSink
			if platform == service.PlatformAnthropic {
				sink = (&GatewayHandler{gatewayService: newFailureSinkTestGatewayService(repo), usageRecordWorkerPool: pool}).claudeFailureSink(c, key, nil, "claude-fable-5-1", service.ChannelMappingResult{}, nil)
			} else {
				sink = (&OpenAIGatewayHandler{gatewayService: newCyberUsageTestGatewayService(repo), usageRecordWorkerPool: pool}).openAIFailureSink(c, openAIFailureSinkParams{APIKey: key, ReqModel: "gpt-5.4"})
			}
			sink(service.FailureBillingDecision{Billable: true, Usage: service.ClaudeUsage{InputTokens: 10}, Provenance: service.BillingProvenanceFailedUpstream}, &service.Account{ID: 30, Platform: platform})
			// The handler has returned; Gin may now reuse or clear its request.
			c.Request = nil
			close(release)
			require.Eventually(t, func() bool { calls, _ := repo.snapshot(); return calls == 1 }, time.Second, time.Millisecond)
		})
	}
}

func TestForwardBillingEffortSnapshotUsesCurrentAttempt(t *testing.T) {
	c := newFailureSinkTestContext("/v1/messages")
	c.Request = c.Request.WithContext(service.WithRequestedReasoningEffort(c.Request.Context(), "max"))
	result := &service.ForwardResult{Model: "claude-fable-5-1"}
	stampForwardBillingReasoningEffort(result, c, &service.ParsedRequest{OutputEffort: "high"})
	require.Equal(t, "max", *result.RequestedReasoningEffort)
	require.Equal(t, "high", *result.ReasoningEffort)
	// A provider's observed final effort must take precedence over the body.
	actual := "medium"
	result.ReasoningEffort = &actual
	stampForwardBillingReasoningEffort(result, c, &service.ParsedRequest{OutputEffort: "high"})
	require.Equal(t, "medium", *result.ReasoningEffort)
}
