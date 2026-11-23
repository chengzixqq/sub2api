package service

// Executor charges are keyed by iteration position in ledger details.
// Index -1 denotes an aggregate executor charge.
type executorCharge struct {
	tokens UsageTokens
	cost   *CostBreakdown
}

// priceCompactedExecutor prices the message total and each metered compaction on the same
// rate card. The callback carries the caller's group, discounts and pricing time.
func priceCompactedExecutor(usage ClaudeUsage, combined UsageTokens, perRequest bool,
	price func(UsageTokens, *int) (*CostBreakdown, error), parts map[int]executorCharge,
) (*CostBreakdown, error) {
	state := usage.IterationUsage
	total := &CostBreakdown{BillingMode: string(BillingModeToken)}
	record := func(index int, tokens UsageTokens, cost *CostBreakdown) {
		if parts != nil {
			parts[index] = executorCharge{tokens, cost}
		}
		addIterationCost(total, cost)
	}
	contextTokens := combined.InputTokens + combined.CacheReadTokens + combined.CacheCreationTokens
	pricePart := func(tokens UsageTokens) (*CostBreakdown, error) {
		own, err := price(tokens, nil)
		if err != nil {
			return nil, err
		}
		if tokens.InputTokens+tokens.CacheReadTokens+tokens.CacheCreationTokens == contextTokens {
			return own, nil
		}
		shared, err := price(tokens, &contextTokens)
		if err != nil {
			return nil, err
		}
		if shared.TotalCost > own.TotalCost {
			return shared, nil
		}
		return own, nil
	}
	message := combined
	if perRequest {
		cost, err := price(combined, nil)
		if err != nil {
			return nil, err
		}
		total.BillingMode = cost.BillingMode
		record(-1, combined, cost)
	} else {
		// Subtract only the buckets that addCompactionUsage added. This also
		// preserves top-level counters on interrupted or mismatched streams.
		singleMessageIndex := -1
		messageCount := 0
		for i, entry := range state.Iterations {
			if entry.Invalid {
				continue
			}
			if entry.Type == "message" {
				messageCount++
				singleMessageIndex = i
				continue
			}
			if entry.Type != "compaction" || entry.Usage == (UsageTokens{}) {
				continue
			}
			tokens := entry.Usage
			message.InputTokens -= tokens.InputTokens
			message.OutputTokens -= tokens.OutputTokens
			message.CacheReadTokens -= tokens.CacheReadTokens
			message.CacheCreationTokens -= tokens.CacheCreationTokens
			message.CacheCreation5mTokens -= tokens.CacheCreation5mTokens
			message.CacheCreation1hTokens -= tokens.CacheCreation1hTokens
			cost, err := pricePart(tokens)
			if err != nil {
				return nil, err
			}
			record(i, tokens, cost)
		}
		if message != (UsageTokens{}) {
			cost, err := pricePart(message)
			if err != nil {
				return nil, err
			}
			if messageCount != 1 || !claudeIterationsMatchAggregate(usage, true) {
				singleMessageIndex = -1
			}
			record(singleMessageIndex, message, cost)
		}
	}

	// Normally the message's own-tier quote already bounds the production
	// debit. Recovering missing TTL details can change its token buckets.
	if original := state.originalExecutor; original != nil && *original != message {
		baseline, err := price(*original, nil)
		if err != nil {
			return nil, err
		}
		if baseline.TotalCost > total.TotalCost {
			if parts != nil {
				clear(parts)
				parts[-1] = executorCharge{*original, baseline}
			}
			return baseline, nil
		}
	}
	return total, nil
}
