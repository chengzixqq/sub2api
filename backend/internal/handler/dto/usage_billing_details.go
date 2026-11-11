package dto

import "github.com/Wei-Shaw/sub2api/internal/service"

// Explicit wire fields prevent internal account pricing from reaching user APIs.
type UsageBillingDetails struct {
	Version    int                     `json:"version"`
	Status     string                  `json:"status"`
	Components []UsageBillingComponent `json:"components"`
}
type UsageBillingComponent struct {
	Role                   string   `json:"role"`
	IterationIndex         *int     `json:"iteration_index,omitempty"`
	Model                  string   `json:"model"`
	BillingMode            string   `json:"billing_mode"`
	InputTokens            int      `json:"input_tokens"`
	OutputTokens           int      `json:"output_tokens"`
	CacheCreationTokens    int      `json:"cache_creation_tokens"`
	CacheReadTokens        int      `json:"cache_read_tokens"`
	CacheCreation5mTokens  int      `json:"cache_creation_5m_tokens"`
	CacheCreation1hTokens  int      `json:"cache_creation_1h_tokens"`
	CacheCreation30mTokens int      `json:"cache_creation_30m_tokens"`
	CacheCreation12hTokens int      `json:"cache_creation_12h_tokens"`
	InputCost              *float64 `json:"input_cost"`
	OutputCost             *float64 `json:"output_cost"`
	CacheCreationCost      *float64 `json:"cache_creation_cost"`
	CacheReadCost          *float64 `json:"cache_read_cost"`
	TotalCost              *float64 `json:"total_cost"`
	ActualCost             *float64 `json:"actual_cost"`
	AccountStatsCost       *float64 `json:"account_stats_cost,omitempty"`
	PricingSource          string   `json:"pricing_source,omitempty"`
	UnpricedReason         string   `json:"unpriced_reason,omitempty"`
}

func usageBillingDetailsFromService(log *service.UsageLog, admin bool) *UsageBillingDetails {
	if log == nil || log.BillingDetails == nil {
		return nil
	}
	details := log.BillingDetails
	result := &UsageBillingDetails{Version: details.Version, Status: details.Status, Components: make([]UsageBillingComponent, 0, len(details.Components))}
	for _, c := range details.Components {
		item := UsageBillingComponent{Role: c.Role, IterationIndex: c.IterationIndex, Model: c.Model, BillingMode: c.BillingMode,
			InputTokens: c.InputTokens, OutputTokens: c.OutputTokens, CacheCreationTokens: c.CacheCreationTokens, CacheReadTokens: c.CacheReadTokens,
			CacheCreation5mTokens: c.CacheCreation5mTokens, CacheCreation1hTokens: c.CacheCreation1hTokens,
			CacheCreation30mTokens: c.CacheCreation30mTokens, CacheCreation12hTokens: c.CacheCreation12hTokens,
			InputCost: c.InputCost, OutputCost: c.OutputCost, CacheCreationCost: c.CacheCreationCost, CacheReadCost: c.CacheReadCost,
			TotalCost: c.TotalCost, ActualCost: c.ActualCost}
		if admin {
			item.AccountStatsCost = c.AccountStatsCost
			item.PricingSource = c.PricingSource
			item.UnpricedReason = c.UnpricedReason
		} else if c.Role == "executor" {
			item.Model = log.RequestedModel
			if item.Model == "" {
				item.Model = log.Model
			}
		}
		result.Components = append(result.Components, item)
	}
	return result
}

// PublicUsageBillingDetails returns a detached whitelist projection for scoped
// credentials and vendor views, including executor model redaction.
func PublicUsageBillingDetails(details *UsageBillingDetails, model string) *UsageBillingDetails {
	if details == nil {
		return nil
	}
	result := &UsageBillingDetails{Version: details.Version, Status: details.Status, Components: make([]UsageBillingComponent, 0, len(details.Components))}
	for _, c := range details.Components {
		item := UsageBillingComponent{Role: c.Role, IterationIndex: c.IterationIndex, Model: c.Model, BillingMode: c.BillingMode,
			InputTokens: c.InputTokens, OutputTokens: c.OutputTokens, CacheCreationTokens: c.CacheCreationTokens, CacheReadTokens: c.CacheReadTokens,
			CacheCreation5mTokens: c.CacheCreation5mTokens, CacheCreation1hTokens: c.CacheCreation1hTokens,
			CacheCreation30mTokens: c.CacheCreation30mTokens, CacheCreation12hTokens: c.CacheCreation12hTokens,
			InputCost: c.InputCost, OutputCost: c.OutputCost, CacheCreationCost: c.CacheCreationCost, CacheReadCost: c.CacheReadCost, TotalCost: c.TotalCost, ActualCost: c.ActualCost}
		if c.Role == "executor" {
			item.Model = model
		}
		result.Components = append(result.Components, item)
	}
	return result
}
