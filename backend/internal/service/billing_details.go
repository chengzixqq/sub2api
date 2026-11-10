package service

// BillingDetails is an immutable request billing snapshot. Account costs and
// pricing sources are internal; HTTP DTOs must explicitly redact them for users.
type BillingDetails struct {
	Version    int                `json:"version"`
	Status     string             `json:"status"`
	Components []BillingComponent `json:"components"`
}

type BillingComponent struct {
	Role                  string   `json:"role"`
	IterationIndex        *int     `json:"iteration_index,omitempty"`
	Model                 string   `json:"model"`
	BillingMode           string   `json:"billing_mode"`
	InputTokens           int      `json:"input_tokens"`
	OutputTokens          int      `json:"output_tokens"`
	CacheCreationTokens   int      `json:"cache_creation_tokens"`
	CacheReadTokens       int      `json:"cache_read_tokens"`
	CacheCreation5mTokens int      `json:"cache_creation_5m_tokens"`
	CacheCreation1hTokens int      `json:"cache_creation_1h_tokens"`
	InputCost             *float64 `json:"input_cost"`
	OutputCost            *float64 `json:"output_cost"`
	CacheCreationCost     *float64 `json:"cache_creation_cost"`
	CacheReadCost         *float64 `json:"cache_read_cost"`
	TotalCost             *float64 `json:"total_cost"`
	ActualCost            *float64 `json:"actual_cost"`
	AccountStatsCost      *float64 `json:"account_stats_cost"`
	PricingSource         string   `json:"pricing_source"`
	UnpricedReason        string   `json:"unpriced_reason,omitempty"`
}

func billingAmount(v float64) *float64 { return &v }

// resetBillingDetailsActualCost mirrors a failed settlement's zero charged
// amount while retaining list prices and unknown amounts for audit.
func resetBillingDetailsActualCost(details *BillingDetails) {
	if details == nil {
		return
	}
	for i := range details.Components {
		if details.Components[i].ActualCost != nil {
			details.Components[i].ActualCost = billingAmount(0)
		}
	}
}

func (d *BillingDetails) Clone() *BillingDetails {
	if d == nil {
		return nil
	}
	out := *d
	out.Components = append([]BillingComponent(nil), d.Components...)
	for i := range out.Components {
		c := &out.Components[i]
		if c.IterationIndex != nil {
			n := *c.IterationIndex
			c.IterationIndex = &n
		}
		for _, p := range []**float64{&c.InputCost, &c.OutputCost, &c.CacheCreationCost, &c.CacheReadCost, &c.TotalCost, &c.ActualCost, &c.AccountStatsCost} {
			if *p != nil {
				*p = billingAmount(**p)
			}
		}
	}
	return &out
}
