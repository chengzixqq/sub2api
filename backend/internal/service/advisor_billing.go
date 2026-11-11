package service

import (
	"context"
	"time"
)

func billingComponent(role, model string, index *int, tokens UsageTokens, cost *CostBreakdown, source string) BillingComponent {
	c := BillingComponent{Role: role, Model: model, IterationIndex: index, BillingMode: string(BillingModeToken),
		InputTokens: tokens.InputTokens, OutputTokens: tokens.OutputTokens, CacheCreationTokens: tokens.CacheCreationTokens,
		CacheReadTokens: tokens.CacheReadTokens, CacheCreation5mTokens: tokens.CacheCreation5mTokens, CacheCreation1hTokens: tokens.CacheCreation1hTokens,
		CacheCreation30mTokens: tokens.CacheCreation30mTokens, CacheCreation12hTokens: tokens.CacheCreation12hTokens,
		PricingSource: source}
	if cost != nil {
		if cost.BillingMode != "" {
			c.BillingMode = cost.BillingMode
		}
		c.InputCost, c.OutputCost = billingAmount(cost.InputCost), billingAmount(cost.OutputCost)
		c.CacheCreationCost, c.CacheReadCost = billingAmount(cost.CacheCreationCost), billingAmount(cost.CacheReadCost)
		c.TotalCost, c.ActualCost = billingAmount(cost.TotalCost), billingAmount(cost.ActualCost)
	}
	return c
}

func addIterationCost(total, cost *CostBreakdown) {
	if cost == nil {
		return
	}
	total.InputCost += cost.InputCost
	total.OutputCost += cost.OutputCost
	total.CacheCreationCost += cost.CacheCreationCost
	total.CacheReadCost += cost.CacheReadCost
	total.ImageInputCost += cost.ImageInputCost
	total.ImageOutputCost += cost.ImageOutputCost
	total.TotalCost += cost.TotalCost
	total.ActualCost += cost.ActualCost
	total.LongContextBillingApplied = total.LongContextBillingApplied || cost.LongContextBillingApplied
}

// calculateTokenCost preserves the scalar executor fields, while the bill has
// one component per independently priced iteration. No second billing command
// or request log is issued for an advisor call.
func (s *GatewayService) calculateTokenCost(ctx context.Context, result *ForwardResult, key *APIKey, model string, multiplier float64, at time.Time) *CostBreakdown {
	state := result.Usage.IterationUsage
	if state == nil || !state.AdvisorSeen {
		return s.calculateExecutorTokenCost(ctx, result, key, model, multiplier, at)
	}
	details := &BillingDetails{Version: 1, Status: "complete", Components: make([]BillingComponent, 0, len(state.Iterations))}
	complete := claudeExecutorIterationsComplete(result.Usage)
	if !complete {
		details.Status = "partial_usage"
	}
	resolver := s.advisorResolver()
	pricingInput := PricingInput{Model: model}
	if key != nil {
		pricingInput.GroupID, pricingInput.Group = key.GroupID, key.Group
	}
	executorPricing := resolver.Resolve(ctx, pricingInput)
	total := &CostBreakdown{BillingMode: string(executorPricing.Mode), BillingDetails: details}
	if total.BillingMode == "" {
		total.BillingMode = string(BillingModeToken)
	}
	// Per-request pricing applies once even when an executor iterates. An
	// incomplete executor snapshot uses its authoritative top-level aggregate.
	if !complete || executorPricing.Mode != BillingModeToken {
		cost := s.calculateExecutorTokenCost(ctx, result, key, model, multiplier, at)
		addIterationCost(total, cost)
		details.Components = append(details.Components, billingComponent("executor", model, nil, claudeUsageTokens(result.Usage), cost, executorPricing.Source))
	}
	for i, entry := range state.Iterations {
		if entry.Invalid {
			continue
		}
		idx := i
		switch entry.Type {
		case "message":
			if !complete || executorPricing.Mode != BillingModeToken {
				continue
			}
			part := *result
			part.Usage = claudeUsageFromTokens(entry.Usage)
			cost := s.calculateExecutorTokenCost(ctx, &part, key, model, multiplier, at)
			addIterationCost(total, cost)
			details.Components = append(details.Components, billingComponent("executor", model, &idx, entry.Usage, cost, executorPricing.Source))
		case "advisor_message":
			input := pricingInput
			input.Model = entry.Model
			resolved, err := resolver.ResolveTokenOnly(ctx, input)
			var cost *CostBreakdown
			if err == nil && advisorPricingCoversUsage(resolver, resolved, entry.Model, entry.Usage, false) {
				cost, err = s.billingService.CalculateCostUnified(CostInput{Ctx: ctx, Model: entry.Model, GroupID: input.GroupID, Group: input.Group,
					Tokens: entry.Usage, RateMultiplier: multiplier, PricingAt: at, Resolver: resolver, Resolved: resolved})
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
	return total
}

func componentUsage(c BillingComponent) UsageTokens {
	return UsageTokens{InputTokens: c.InputTokens, OutputTokens: c.OutputTokens, CacheCreationTokens: c.CacheCreationTokens,
		CacheReadTokens: c.CacheReadTokens, CacheCreation5mTokens: c.CacheCreation5mTokens, CacheCreation1hTokens: c.CacheCreation1hTokens,
		CacheCreation30mTokens: c.CacheCreation30mTokens, CacheCreation12hTokens: c.CacheCreation12hTokens}
}

// Existing request surcharges belong to the executor side of the bill.
func reconcileExecutorSurcharges(cost *CostBreakdown) {
	if cost == nil || cost.BillingDetails == nil {
		return
	}
	var total, actual float64
	var executor *BillingComponent
	for i := range cost.BillingDetails.Components {
		c := &cost.BillingDetails.Components[i]
		if c.TotalCost != nil {
			total += *c.TotalCost
		}
		if c.ActualCost != nil {
			actual += *c.ActualCost
		}
		if executor == nil && c.Role == "executor" && c.TotalCost != nil && c.ActualCost != nil {
			executor = c
		}
	}
	if executor != nil {
		*executor.TotalCost += cost.TotalCost - total
		*executor.ActualCost += cost.ActualCost - actual
	}
}

// applyAdvisorAccountStatsCost resolves each model separately, preserving the
// existing account multiplier snapshot at the request level.
func (s *GatewayService) applyAdvisorAccountStatsCost(ctx context.Context, log *UsageLog, key *APIKey, account *Account, result *ForwardResult, at time.Time, providerRecorded bool, executorLongContext ...bool) {
	if log == nil || log.BillingDetails == nil {
		return
	}
	var total float64
	executorStatsRequestRecorded := false
	longContext := true
	if len(executorLongContext) > 0 {
		longContext = executorLongContext[0]
	}
	groupID := int64(0)
	if key != nil && key.GroupID != nil {
		groupID = *key.GroupID
	}
	for i := range log.BillingDetails.Components {
		c := &log.BillingDetails.Components[i]
		if !providerRecorded {
			c.AccountStatsCost = billingAmount(0)
			continue
		}
		if c.TotalCost == nil {
			continue
		}
		tokens := componentUsage(*c)
		var amount *float64
		if c.Role == "executor" {
			model := upstreamSentModel(result.Model, result.UpstreamModel)
			requestCount := 1
			if executorStatsRequestRecorded {
				requestCount = 0
			}
			executorStatsRequestRecorded = true
			amount = resolveAccountStatsCost(ctx, s.channelService, s.billingService, account.ID, groupID, model, tokens, requestCount, *c.TotalCost,
				optionalStringValue(result.ServiceTier), at, longContext, optionalStringValue(result.ReasoningEffort))
		} else {
			amount = s.advisorAccountStatsCost(ctx, account.ID, groupID, c.Model, tokens, *c.TotalCost, at)
		}
		if amount == nil {
			amount = billingAmount(*c.TotalCost)
		}
		c.AccountStatsCost = billingAmount(*amount)
		total += *amount
	}
	log.AccountStatsCost = billingAmount(QuantizeUsageBillingAmount(total))
	quantizeBillingComponents(log.BillingDetails, log.TotalCost, log.ActualCost, log.AccountStatsCost)
}

func (s *GatewayService) advisorAccountStatsCost(ctx context.Context, accountID, groupID int64, model string, tokens UsageTokens, customerCost float64, at time.Time) *float64 {
	if s.channelService != nil {
		channel, err := s.channelService.GetChannelForGroup(ctx, groupID)
		if err == nil && channel != nil {
			platform := s.channelService.GetGroupPlatform(ctx, groupID)
			for _, rule := range channel.AccountStatsPricingRules {
				if !matchAccountStatsRule(&rule, accountID, groupID) {
					continue
				}
				card := findPricingForModel(rule.Pricing, platform, normalizeChannelPricingModelName(model))
				if card == nil || (card.BillingMode != "" && card.BillingMode != BillingModeToken) {
					continue
				}
				// Reuse the same token resolver, including explicit free prices.
				resolver := NewModelPricingResolver(nil, s.billingService)
				group := &Group{ModelPricing: []ChannelModelPricing{card.Clone()}, LongContextPricingEnabled: true}
				resolved, resolveErr := resolver.ResolveTokenOnly(ctx, PricingInput{Model: model, Group: group})
				if resolveErr == nil && advisorPricingCoversUsage(resolver, resolved, model, tokens, false) {
					// Account rules keep their configured context intervals.
					resolved.Intervals = filterValidIntervals(card.Intervals)
					cost, costErr := s.billingService.CalculateCostUnified(CostInput{Ctx: ctx, Model: model, Tokens: tokens, RateMultiplier: 1, PricingAt: at, Resolver: resolver, Resolved: resolved})
					if costErr == nil {
						return billingAmount(cost.TotalCost)
					}
				}
			}
			if channel.ApplyPricingToAccountStats {
				return billingAmount(customerCost)
			}
		}
	}
	resolver := NewModelPricingResolver(nil, s.billingService)
	resolved, err := resolver.ResolveTokenOnly(ctx, PricingInput{Model: model})
	if err != nil || !advisorPricingCoversUsage(resolver, resolved, model, tokens, false) {
		return nil
	}
	cost, err := s.billingService.CalculateCostUnified(CostInput{Ctx: ctx, Model: model, Tokens: tokens, RateMultiplier: 1, PricingAt: at, Resolver: resolver, Resolved: resolved})
	if err != nil {
		return nil
	}
	return billingAmount(cost.TotalCost)
}

// Quantize component displays against the ledger total. Distribute a negative
// rounding remainder over existing positive components, so tiny repeated
// iterations can never create a negative component. Unknown prices stay null.
func quantizeBillingComponents(details *BillingDetails, total, actual float64, account *float64) {
	if details == nil {
		return
	}
	for kind, target := range []float64{total, actual} {
		amounts := make([]*float64, 0, len(details.Components))
		for i := range details.Components {
			p := details.Components[i].TotalCost
			if kind == 1 {
				p = details.Components[i].ActualCost
			}
			if p != nil {
				amounts = append(amounts, p)
			}
		}
		quantizeComponentAmounts(amounts, target)
	}
	if account != nil {
		amounts := make([]*float64, 0, len(details.Components))
		for i := range details.Components {
			if p := details.Components[i].AccountStatsCost; p != nil {
				amounts = append(amounts, p)
			}
		}
		quantizeComponentAmounts(amounts, *account)
	}
}

func quantizeComponentAmounts(amounts []*float64, target float64) {
	var sum float64
	for _, p := range amounts {
		*p = QuantizeUsageBillingAmount(*p)
		sum += *p
	}
	remainder := QuantizeUsageBillingAmount(QuantizeUsageBillingAmount(target) - sum)
	if remainder >= 0 {
		if len(amounts) > 0 {
			last := amounts[len(amounts)-1]
			*last = QuantizeUsageBillingAmount(*last + remainder)
		}
		return
	}
	for i := len(amounts) - 1; i >= 0 && remainder < 0; i-- {
		deduct := min(*amounts[i], -remainder)
		*amounts[i] = QuantizeUsageBillingAmount(*amounts[i] - deduct)
		remainder = QuantizeUsageBillingAmount(remainder + deduct)
	}
}
