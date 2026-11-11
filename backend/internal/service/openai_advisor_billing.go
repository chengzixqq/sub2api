package service

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func (u OpenAIUsage) Clone() OpenAIUsage {
	if u.IterationUsage != nil {
		u.IterationUsage = cloneClaudeIterations(u.IterationUsage)
	}
	return u
}

func (u OpenAIUsage) HasAdvisorUsage() bool {
	return openAIUsageToClaudeUsage(u).HasAdvisorUsage()
}

// ToClaudeUsage returns an independent snapshot using mutually exclusive input
// and cache buckets, suitable for probe coalescing and deferred settlement.
func (u OpenAIUsage) ToClaudeUsage() ClaudeUsage {
	return openAIUsageToClaudeUsage(u).Clone()
}

func openAIUsageToClaudeUsage(u OpenAIUsage) ClaudeUsage {
	return ClaudeUsage{
		InputTokens:  max(u.InputTokens-u.CacheCreationInputTokens-u.CacheReadInputTokens, 0),
		OutputTokens: u.OutputTokens, CacheCreationInputTokens: u.CacheCreationInputTokens,
		CacheReadInputTokens: u.CacheReadInputTokens, CacheCreation5mTokens: u.CacheCreation5mTokens,
		CacheCreation1hTokens: u.CacheCreation1hTokens, IterationUsage: u.IterationUsage,
		CacheCreation30mTokens: u.CacheCreation30mInputTokens, CacheCreation12hTokens: u.CacheCreation12hInputTokens,
	}
}

func (s *OpenAIGatewayService) nativeAdvisorGateway() *GatewayService {
	return &GatewayService{billingService: s.billingService, resolver: s.resolver, channelService: s.channelService}
}

func (s *OpenAIGatewayService) validateNativeAdvisorPricing(ctx context.Context, c *gin.Context, body []byte) error {
	var key *APIKey
	if c != nil {
		if value, ok := c.Get("api_key"); ok {
			key, _ = value.(*APIKey)
		}
	}
	return s.nativeAdvisorGateway().ValidateAdvisorPricing(ctx, key, body)
}

// Typed OpenAI converters do not model Anthropic advisor configuration. Restore
// only these native server tools from the original request after conversion.
func restoreNativeAdvisorTools(converted, original []byte) ([]byte, error) {
	var advisors []json.RawMessage
	gjson.GetBytes(original, "tools").ForEach(func(_, tool gjson.Result) bool {
		if strings.HasPrefix(tool.Get("type").String(), "advisor_") {
			advisors = append(advisors, json.RawMessage(tool.Raw))
		}
		return true
	})
	if len(advisors) == 0 {
		return converted, nil
	}
	var tools []json.RawMessage
	gjson.GetBytes(converted, "tools").ForEach(func(_, tool gjson.Result) bool {
		if !strings.HasPrefix(tool.Get("type").String(), "advisor_") {
			tools = append(tools, json.RawMessage(tool.Raw))
		}
		return true
	})
	return sjson.SetBytes(converted, "tools", append(tools, advisors...))
}

// Observe the raw event before the compatibility converters discard extensions.
// Scalar input/cache normalization remains in mergeAnthropicUsage for providers
// whose Anthropic-compatible input_tokens field includes cached tokens.
func observeNativeAnthropicUsage(payload string, usage *ClaudeUsage) {
	observeClaudeIterationEvent(payload, usage)
}

// Free Fast changes customer charges while retaining priority list prices.
// Match by identity so a tier discount cannot spill into an advisor component
// when the final amounts are rounded against the ledger total.
func applyOpenAIStandardActualCost(cost, standard *CostBreakdown) {
	cost.ActualCost = standard.ActualCost
	if cost.BillingDetails == nil || standard.BillingDetails == nil {
		return
	}
	type componentKey struct {
		role, model string
		index       int
	}
	key := func(component BillingComponent) componentKey {
		index := -1
		if component.IterationIndex != nil {
			index = *component.IterationIndex
		}
		return componentKey{role: component.Role, model: component.Model, index: index}
	}
	actuals := make(map[componentKey]*float64, len(standard.BillingDetails.Components))
	for _, component := range standard.BillingDetails.Components {
		actuals[key(component)] = component.ActualCost
	}
	for i := range cost.BillingDetails.Components {
		component := &cost.BillingDetails.Components[i]
		if amount, ok := actuals[key(*component)]; ok {
			component.ActualCost = nil
			if amount != nil {
				component.ActualCost = billingAmount(*amount)
			}
		}
	}
}

// Native Anthropic compatibility requests retain the established OpenAI
// executor pricing policy. Only an observed advisor activates component billing.
func (s *OpenAIGatewayService) calculateOpenAIRecordUsageCost(
	ctx context.Context, result *OpenAIForwardResult, key *APIKey, models []string,
	multiplier, imageMultiplier, videoMultiplier, webSearchMultiplier float64,
	tokens UsageTokens, serviceTier string, longContextGate *bool, modelLongContextExempt bool,
	at time.Time,
) (*CostBreakdown, error) {
	calculate := func(part *OpenAIForwardResult, units UsageTokens, candidates []string) (*CostBreakdown, error) {
		return s.calculateOpenAIExecutorRecordUsageCost(ctx, part, key, candidates, multiplier,
			imageMultiplier, videoMultiplier, webSearchMultiplier, units, serviceTier, longContextGate, modelLongContextExempt, at)
	}
	if result == nil || result.Usage.IterationUsage == nil || !result.Usage.IterationUsage.AdvisorSeen {
		return calculate(result, tokens, models)
	}
	// Resolve the actual executor candidate once. Every iteration follows that
	// same model and policy; advisor entries always use their own response model.
	model := firstUsageBillingModel(models)
	var executorCost *CostBreakdown
	var executorErr error
	for _, candidate := range models {
		executorCost, executorErr = calculate(result, tokens, []string{candidate})
		if executorErr == nil {
			model = candidate
			break
		}
	}
	if len(models) == 0 {
		executorCost, executorErr = calculate(result, tokens, models)
	}
	if executorErr != nil && !isUsagePricingUnavailableError(executorErr) {
		return nil, executorErr
	}
	resolver := s.nativeAdvisorGateway().advisorResolver()
	pricingInput := PricingInput{Model: model}
	if key != nil {
		pricingInput.GroupID, pricingInput.Group = key.GroupID, key.Group
	}
	executorPricing := resolver.Resolve(ctx, pricingInput)
	details := &BillingDetails{Version: 1, Status: "complete"}
	total := &CostBreakdown{BillingMode: string(BillingModeToken), BillingDetails: details}
	if executorCost != nil && executorCost.BillingMode != "" {
		total.BillingMode = executorCost.BillingMode
	}
	state := result.Usage.IterationUsage
	complete := claudeExecutorIterationsComplete(openAIUsageToClaudeUsage(result.Usage))
	if !complete {
		details.Status = "partial_usage"
	}
	splitExecutor := complete && total.BillingMode == string(BillingModeToken) && executorErr == nil &&
		result.ImageCount == 0 && result.VideoCount == 0 && result.WebSearchCalls == 0 && result.SearchCount == 0 && result.AudioUsage == nil
	if !splitExecutor {
		component := billingComponent("executor", model, nil, tokens, executorCost, executorPricing.Source)
		if executorErr != nil {
			component.UnpricedReason = "executor_pricing_unavailable"
			details.Status = "partial_unpriced"
		}
		addIterationCost(total, executorCost)
		details.Components = append(details.Components, component)
	}
	for index, entry := range state.Iterations {
		if entry.Invalid {
			continue
		}
		idx := index
		switch entry.Type {
		case "message":
			if !splitExecutor {
				continue
			}
			part := *result
			usage := claudeUsageFromTokens(entry.Usage)
			part.Usage = claudeUsageToOpenAIUsage(&usage)
			cost, err := calculate(&part, entry.Usage, []string{model})
			if err != nil {
				return nil, err
			}
			addIterationCost(total, cost)
			details.Components = append(details.Components, billingComponent("executor", model, &idx, entry.Usage, cost, executorPricing.Source))
		case "advisor_message":
			input := pricingInput
			input.Model = entry.Model
			resolved, err := resolver.ResolveTokenOnly(ctx, input)
			var cost *CostBreakdown
			if err == nil && advisorPricingCoversUsage(resolver, resolved, entry.Model, entry.Usage, false) {
				cost, err = s.billingService.CalculateCostUnified(CostInput{
					Ctx: ctx, Model: entry.Model, GroupID: input.GroupID, Group: input.Group, Tokens: entry.Usage,
					RateMultiplier: multiplier, PricingAt: at, Resolver: resolver, Resolved: resolved,
				})
			}
			source := "unknown"
			if resolved != nil {
				source = resolved.Source
			}
			component := billingComponent("advisor", entry.Model, &idx, entry.Usage, cost, source)
			if err != nil || cost == nil {
				component.UnpricedReason = "advisor_pricing_unavailable"
				details.Status = "partial_unpriced"
			} else {
				addIterationCost(total, cost)
			}
			details.Components = append(details.Components, component)
		}
	}
	return total, nil
}
