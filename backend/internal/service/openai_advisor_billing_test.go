//go:build unit

package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const nativeAdvisorUsageJSON = `{"input_tokens":100,"output_tokens":20,"iterations":[{"type":"message","input_tokens":40,"output_tokens":8},{"type":"advisor_message","model":"advisor-model","input_tokens":80,"output_tokens":5},{"type":"message","input_tokens":60,"output_tokens":12}]}`

func nativeAdvisorTestUsage() ClaudeUsage {
	return *parseClaudeUsageFromResponseBody([]byte(`{"type":"message","stop_reason":"end_turn","usage":` + nativeAdvisorUsageJSON + `}`))
}

func TestOpenAIAdvisorRecordUsage_BillsOneRequestWithIndependentModels(t *testing.T) {
	for _, advisorModel := range []string{"advisor-model", "unknown-advisor"} {
		t.Run(advisorModel, func(t *testing.T) {
			usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
			userRepo := &openAIRecordUsageUserRepoStub{}
			svc := newOpenAIRecordUsageServiceForTest(usageRepo, userRepo, &openAIRecordUsageSubRepoStub{}, nil)
			svc.billingService = NewBillingService(svc.cfg, newStubPricingServiceFromJSON(t, `{
				"executor-model":{"litellm_provider":"anthropic","mode":"chat","input_cost_per_token":0.000001,"output_cost_per_token":0.000002},
				"advisor-model":{"litellm_provider":"anthropic","mode":"chat","input_cost_per_token":0.000003,"output_cost_per_token":0.000015}
			}`))
			key := openAIRecordUsageAPIKeyWithGroup(svc, 1, true)
			key.GroupID = i64p(1)
			key.Group.RateMultiplier = 2
			usage := nativeAdvisorTestUsage()
			usage.IterationUsage.Iterations[1].Model = advisorModel
			err := svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
				Result: &OpenAIForwardResult{RequestID: "advisor-test", Model: "executor-model", Usage: claudeUsageToOpenAIUsage(&usage)},
				APIKey: key, User: &User{ID: 2}, Account: &Account{ID: 3, Platform: PlatformAnthropic, Type: AccountTypeAPIKey},
			})
			require.NoError(t, err)
			log := usageRepo.lastLog
			require.NotNil(t, log)
			require.Equal(t, 100, log.InputTokens, "top-level scalar fields remain executor-only")
			require.Equal(t, 20, log.OutputTokens)
			require.Len(t, log.BillingDetails.Components, 3)
			want := 0.000455
			if advisorModel == "unknown-advisor" {
				want = 0.00014
				require.Equal(t, "partial_unpriced", log.BillingDetails.Status)
				require.Nil(t, log.BillingDetails.Components[1].TotalCost)
			} else {
				require.Equal(t, "complete", log.BillingDetails.Status)
			}
			require.InDelta(t, want, log.TotalCost, 1e-10)
			require.InDelta(t, want*2, log.ActualCost, 1e-10)
			require.Equal(t, 1, userRepo.deductCalls)
			require.InDelta(t, want*2, userRepo.lastAmount, 1e-10)
			var sum float64
			for _, c := range log.BillingDetails.Components {
				if c.TotalCost != nil {
					sum += *c.TotalCost
				}
			}
			require.InDelta(t, want, sum, 1e-10)
		})
	}
}

func TestOpenAIAdvisorRecordUsage_FreeFastPreservesAdvisorActualCost(t *testing.T) {
	for _, advisorModel := range []string{"advisor-model", "unknown-advisor"} {
		t.Run(advisorModel, func(t *testing.T) {
			usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
			userRepo := &openAIRecordUsageUserRepoStub{}
			svc := newOpenAIRecordUsageServiceForTest(usageRepo, userRepo, &openAIRecordUsageSubRepoStub{}, nil)
			svc.billingService = NewBillingService(svc.cfg, newStubPricingServiceFromJSON(t, `{
				"executor-model":{"litellm_provider":"anthropic","mode":"chat","input_cost_per_token":0.000001,"output_cost_per_token":0.000002},
				"advisor-model":{"litellm_provider":"anthropic","mode":"chat","input_cost_per_token":0.000003,"output_cost_per_token":0.000015}
			}`))
			key := openAIRecordUsageAPIKeyWithGroup(svc, 1, true)
			key.GroupID = i64p(1)
			key.Group.Platform = PlatformOpenAI
			key.Group.RateMultiplier = 2
			key.Group.FreeOpenAIFast = true
			fastMultiplier := 3.0
			key.Group.ModelPricing = []ChannelModelPricing{{Models: []string{"executor-model"}, BillingMode: BillingModeToken, FastMultiplier: &fastMultiplier}}
			usage := nativeAdvisorTestUsage()
			usage.IterationUsage.Iterations[1].Model = advisorModel
			serviceTier := "priority"
			err := svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
				Result: &OpenAIForwardResult{RequestID: "advisor-free-fast", Model: "executor-model", ServiceTier: &serviceTier, Usage: claudeUsageToOpenAIUsage(&usage)},
				APIKey: key, User: &User{ID: 2}, Account: &Account{ID: 3, Platform: PlatformOpenAI, Type: AccountTypeAPIKey},
			})
			require.NoError(t, err)
			log := usageRepo.lastLog
			require.NotNil(t, log)
			require.Len(t, log.BillingDetails.Components, 3)
			require.Equal(t, "priority", *log.ServiceTier)
			components := log.BillingDetails.Components
			require.InDelta(t, 0.000056*3, *components[0].TotalCost, 1e-10)
			require.InDelta(t, 0.000056*2, *components[0].ActualCost, 1e-10)
			require.InDelta(t, 0.000084*3, *components[2].TotalCost, 1e-10)
			require.InDelta(t, 0.000084*2, *components[2].ActualCost, 1e-10)
			advisorCost := 0.000315
			if advisorModel == "unknown-advisor" {
				advisorCost = 0
				require.Nil(t, components[1].TotalCost)
				require.Nil(t, components[1].ActualCost)
			} else {
				require.InDelta(t, advisorCost, *components[1].TotalCost, 1e-10)
				require.InDelta(t, advisorCost*2, *components[1].ActualCost, 1e-10)
			}
			require.InDelta(t, 0.00014*3+advisorCost, log.TotalCost, 1e-10)
			require.InDelta(t, (0.00014+advisorCost)*2, log.ActualCost, 1e-10)
			require.InDelta(t, log.ActualCost, userRepo.lastAmount, 1e-10)
			var sum float64
			for _, component := range components {
				if component.ActualCost != nil {
					sum += *component.ActualCost
				}
			}
			require.InDelta(t, log.ActualCost, sum, 1e-10)
		})
	}
}

func TestOpenAIAdvisorRecordUsage_BillingFailureClearsKnownActualCosts(t *testing.T) {
	for _, advisorModel := range []string{"advisor-model", "unknown-advisor"} {
		t.Run(advisorModel, func(t *testing.T) {
			usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
			billingErr := errors.New("advisor billing transaction failed")
			billingRepo := &openAIRecordUsageBillingRepoStub{err: billingErr}
			userRepo := &openAIRecordUsageUserRepoStub{}
			svc := newOpenAIRecordUsageServiceWithBillingRepoForTest(usageRepo, billingRepo, userRepo, &openAIRecordUsageSubRepoStub{}, nil)
			svc.billingService = NewBillingService(svc.cfg, newStubPricingServiceFromJSON(t, `{
				"executor-model":{"litellm_provider":"anthropic","mode":"chat","input_cost_per_token":0.000001,"output_cost_per_token":0.000002},
				"advisor-model":{"litellm_provider":"anthropic","mode":"chat","input_cost_per_token":0.000003,"output_cost_per_token":0.000015}
			}`))
			key := openAIRecordUsageAPIKeyWithGroup(svc, 1, true)
			key.GroupID = i64p(1)
			key.Group.RateMultiplier = 2
			usage := nativeAdvisorTestUsage()
			usage.IterationUsage.Iterations[1].Model = advisorModel
			err := svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
				Result: &OpenAIForwardResult{RequestID: "advisor-billing-failure", Model: "executor-model", Usage: claudeUsageToOpenAIUsage(&usage)},
				APIKey: key, User: &User{ID: 2}, Account: &Account{ID: 3, Platform: PlatformAnthropic, Type: AccountTypeAPIKey},
			})
			require.ErrorIs(t, err, billingErr)
			require.Equal(t, 1, billingRepo.calls)
			require.Zero(t, userRepo.deductCalls)
			require.Equal(t, 1, usageRepo.calls)
			log := usageRepo.lastLog
			require.NotNil(t, log)
			require.Positive(t, log.TotalCost)
			require.Zero(t, log.ActualCost)
			require.Len(t, log.BillingDetails.Components, 3)
			for _, component := range log.BillingDetails.Components {
				if component.Model == "unknown-advisor" {
					require.Nil(t, component.TotalCost)
					require.Nil(t, component.ActualCost)
					continue
				}
				require.NotNil(t, component.TotalCost)
				require.Positive(t, *component.TotalCost)
				require.NotNil(t, component.ActualCost)
				require.Zero(t, *component.ActualCost)
			}
		})
	}
}

func TestOpenAIAdvisorUsage_ClonePreservesTTLAndIsolation(t *testing.T) {
	usage := nativeAdvisorTestUsage()
	usage.CacheCreationInputTokens, usage.CacheCreation5mTokens, usage.CacheCreation1hTokens = 15, 5, 10
	usage.CacheReadInputTokens = 20
	converted := claudeUsageToOpenAIUsage(&usage)
	cloned := converted.Clone()
	roundTrip := converted.ToClaudeUsage()
	usage.IterationUsage.Iterations[1].Model = "mutated-source"
	converted.IterationUsage.Iterations[1].Model = "mutated-converted"
	require.Equal(t, "advisor-model", cloned.IterationUsage.Iterations[1].Model)
	require.Equal(t, "advisor-model", roundTrip.IterationUsage.Iterations[1].Model)
	require.True(t, cloned.HasAdvisorUsage())
	require.Equal(t, 135, cloned.InputTokens)
	require.Equal(t, 100, roundTrip.InputTokens)
	require.Equal(t, 5, roundTrip.CacheCreation5mTokens)
	require.Equal(t, 10, roundTrip.CacheCreation1hTokens)
}

func TestOpenAIAdvisorUsage_OpenAITTLRoundTripAndCompleteness(t *testing.T) {
	u := nativeAdvisorTestUsage()
	u.CacheCreationInputTokens, u.CacheCreation30mTokens, u.CacheCreation12hTokens = 20, 5, 15
	executor := &u.IterationUsage.Iterations[0].Usage
	executor.CacheCreationTokens, executor.CacheCreation30mTokens, executor.CacheCreation12hTokens = 20, 5, 15
	converted := claudeUsageToOpenAIUsage(&u)
	snapshot := converted.ToClaudeUsage()
	require.Equal(t, 120, converted.InputTokens)
	require.Equal(t, 5, converted.CacheCreation30mInputTokens)
	require.Equal(t, 15, converted.CacheCreation12hInputTokens)
	require.Equal(t, u, snapshot)
	require.True(t, claudeExecutorIterationsComplete(snapshot))
	fromTokens := claudeUsageFromTokens(claudeUsageTokens(snapshot))
	fromTokens.IterationUsage = snapshot.IterationUsage
	require.Equal(t, snapshot, fromTokens)
	u.IterationUsage.Iterations[0].Usage.CacheCreation12hTokens = 0
	require.Equal(t, 15, snapshot.IterationUsage.Iterations[0].Usage.CacheCreation12hTokens)
	partial := snapshot.Clone()
	partial.IterationUsage.Iterations[0].Usage.CacheCreation30mTokens = 0
	partial.IterationUsage.Iterations[0].Usage.CacheCreation12hTokens = 0
	require.False(t, claudeExecutorIterationsComplete(partial), "aggregate-only iterations cannot replace priced top-level TTL detail")
}

func TestOpenAIAdvisorRecordUsage_OpenAITTLAndProbeAttribution(t *testing.T) {
	for _, complete := range []bool{true, false} {
		for _, providerRecorded := range []bool{true, false} {
			t.Run(fmt.Sprintf("complete=%t/provider=%t", complete, providerRecorded), func(t *testing.T) {
				usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
				userRepo := &openAIRecordUsageUserRepoStub{}
				svc := newOpenAIRecordUsageServiceForTest(usageRepo, userRepo, &openAIRecordUsageSubRepoStub{}, nil)
				svc.billingService = newTestBillingServiceForResolver()
				svc.billingService.fallbackPrices["claude-sonnet-4"] = &ModelPricing{InputPricePerToken: 1e-6, OutputPricePerToken: 2e-6, CacheCreationPricePerToken: 3e-6, CacheCreation12hPricePerToken: billingAmount(6e-6)}
				svc.billingService.fallbackPrices["advisor-model"] = &ModelPricing{InputPricePerToken: 3e-6, OutputPricePerToken: 15e-6}
				key := openAIRecordUsageAPIKeyWithGroup(svc, 1, true)
				key.GroupID = i64p(1)
				key.Group.RateMultiplier = 2
				usage := nativeAdvisorTestUsage()
				usage.CacheCreationInputTokens, usage.CacheCreation30mTokens, usage.CacheCreation12hTokens = 20, 5, 15
				executor := &usage.IterationUsage.Iterations[0].Usage
				executor.CacheCreationTokens, executor.CacheCreation30mTokens, executor.CacheCreation12hTokens = 20, 5, 15
				usage.IterationUsage.Terminal = complete
				err := svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
					Result: &OpenAIForwardResult{RequestID: "advisor-ttl", Model: "claude-sonnet-4", Usage: claudeUsageToOpenAIUsage(&usage)},
					APIKey: key, User: &User{ID: 2}, Account: &Account{ID: 3, Platform: PlatformAnthropic, Type: AccountTypeAPIKey},
					ProbeCoalesced: !providerRecorded, ProbeLeaderRequestID: "ttl-leader", ProviderCostRecorded: providerRecorded,
				})
				require.NoError(t, err)
				log := usageRepo.lastLog
				require.Equal(t, 100, log.InputTokens)
				require.Equal(t, 20, log.CacheCreationTokens)
				require.Equal(t, 5, log.CacheCreation30mTokens)
				require.Equal(t, 15, log.CacheCreation12hTokens)
				require.InDelta(t, .00056, log.TotalCost, 1e-10)
				require.InDelta(t, .00112, log.ActualCost, 1e-10)
				require.Equal(t, 1, userRepo.deductCalls)
				require.InDelta(t, .00112, userRepo.lastAmount, 1e-10)
				component := log.BillingDetails.Components[0]
				require.Equal(t, 5, component.CacheCreation30mTokens)
				require.Equal(t, 15, component.CacheCreation12hTokens)
				if providerRecorded {
					require.InDelta(t, .00056, *log.AccountStatsCost, 1e-10)
				} else {
					require.Zero(t, *log.AccountStatsCost)
					for _, component := range log.BillingDetails.Components {
						require.Zero(t, *component.AccountStatsCost)
					}
				}
			})
		}
	}
}

func TestOpenAIAdvisorNativePreflight_RejectsBeforeUpstream(t *testing.T) {
	svc := &OpenAIGatewayService{}
	body := []byte(`{"model":"executor-model","tools":[{"type":"advisor_20260301","name":"advisor","model":"unknown-advisor"}]}`)
	for _, path := range []string{"messages", "chat", "responses"} {
		t.Run(path, func(t *testing.T) {
			c := adaptiveProtocolTestContext("/v1/"+path, body)
			var err error
			switch path {
			case "messages":
				_, err = svc.forwardAnthropicViaNativeAnthropicEndpoint(context.Background(), c, nil, body, "")
			case "chat":
				_, err = svc.forwardChatCompletionsViaNativeAnthropic(context.Background(), c, nil, body, "")
			case "responses":
				_, err = svc.forwardResponsesViaNativeAnthropic(context.Background(), c, nil, body, "")
			}
			require.ErrorIs(t, err, ErrAdvisorPricingUnavailable)
			require.Equal(t, http.StatusServiceUnavailable, c.Writer.Status())
		})
	}
}

func TestOpenAIAdvisorNativeForward_PreservesDeclaredModel(t *testing.T) {
	for _, path := range []string{"messages", "chat", "responses"} {
		t.Run(path, func(t *testing.T) {
			input := `"messages":[{"role":"user","content":"hi"}]`
			if path == "responses" {
				input = `"input":"hi"`
			}
			body := []byte(`{"model":"k3","max_tokens":100,"tools":[{"type":"advisor_20260301","name":"advisor","model":"advisor-model","max_uses":2}],` + input + `}`)
			upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(nativeAdvisorSSE()))}}
			if path == "messages" {
				upstream.resp = nativeAnthropicBufferedResponse()
			}
			svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}
			svc.billingService = NewBillingService(svc.cfg, newStubPricingServiceFromJSON(t, `{"advisor-model":{"litellm_provider":"anthropic","mode":"chat","input_cost_per_token":0.000003,"output_cost_per_token":0.000015}}`))
			c := adaptiveProtocolTestContext("/v1/"+path, body)
			c.Request.Header.Set("anthropic-beta", "advisor-tool-2026-03-01")
			var err error
			switch path {
			case "messages":
				_, err = svc.forwardAnthropicViaNativeAnthropicEndpoint(context.Background(), c, nativeAnthropicTestAccount(), body, "")
			case "chat":
				_, err = svc.forwardChatCompletionsViaNativeAnthropic(context.Background(), c, nativeAnthropicTestAccount(), body, "")
			case "responses":
				_, err = svc.forwardResponsesViaNativeAnthropic(context.Background(), c, nativeAnthropicTestAccount(), body, "")
			}
			require.NoError(t, err)
			require.NotNil(t, upstream.lastReq)
			require.Equal(t, "advisor-model", gjson.GetBytes(upstream.lastBody, "tools.0.model").String())
			require.EqualValues(t, 2, gjson.GetBytes(upstream.lastBody, "tools.0.max_uses").Int())
			require.False(t, gjson.GetBytes(upstream.lastBody, "tools.0.input_schema").Exists())
		})
	}
}

func nativeAdvisorSSE() string {
	return "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_advisor\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"executor-model\",\"content\":[],\"usage\":{\"input_tokens\":100,\"output_tokens\":0}}}\n\n" +
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":" + nativeAdvisorUsageJSON + "}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
}

func TestOpenAIAdvisorNativeResponses_PreserveIterationUsage(t *testing.T) {
	for _, path := range []string{"messages", "chat", "responses"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", path, stream), func(t *testing.T) {
				c := adaptiveProtocolTestContext("/v1/"+path, nil)
				svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig()}
				body := nativeAdvisorSSE()
				contentType := "text/event-stream"
				if path == "messages" && !stream {
					body = `{"id":"msg_advisor","type":"message","role":"assistant","model":"executor-model","content":[],"stop_reason":"end_turn","usage":` + nativeAdvisorUsageJSON + `}`
					contentType = "application/json"
				}
				resp := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{contentType}}, Body: io.NopCloser(strings.NewReader(body))}
				var result *OpenAIForwardResult
				var err error
				switch path {
				case "messages":
					if stream {
						result, err = svc.handleNativeAnthropicStreamingResponse(context.Background(), resp, c, nativeAnthropicTestAccount(), "executor-model", "executor-model", "executor-model", nil, time.Now())
					} else {
						result, err = svc.handleNativeAnthropicBufferedResponse(context.Background(), resp, c, nativeAnthropicTestAccount(), "executor-model", "executor-model", "executor-model", nil, time.Now())
					}
				case "chat":
					if stream {
						result, err = svc.handleCCStreamingFromNativeAnthropic(resp, c, "executor-model", "executor-model", "executor-model", nil, time.Now())
					} else {
						result, err = svc.handleCCBufferedFromNativeAnthropic(resp, c, "executor-model", "executor-model", "executor-model", nil, time.Now())
					}
				case "responses":
					if stream {
						result, err = svc.handleResponsesStreamingFromNativeAnthropic(resp, c, "executor-model", "executor-model", "executor-model", nil, time.Now(), apicompat.ResponsesClientToolMapping{})
					} else {
						result, err = svc.handleResponsesBufferedFromNativeAnthropic(resp, c, "executor-model", "executor-model", "executor-model", nil, time.Now(), apicompat.ResponsesClientToolMapping{})
					}
				}
				require.NoError(t, err)
				require.NotNil(t, result)
				require.True(t, result.Usage.HasAdvisorUsage())
				require.True(t, claudeExecutorIterationsComplete(result.Usage.ToClaudeUsage()))
				require.Len(t, result.Usage.IterationUsage.Iterations, 3)
			})
		}
	}
}
