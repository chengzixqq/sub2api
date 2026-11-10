package dto

import (
	"encoding/json"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestUsageBillingDetails_PrivacyAndUnknownAmounts(t *testing.T) {
	price := 0.2
	log := &service.UsageLog{Model: "internal-executor", RequestedModel: "public-executor", BillingDetails: &service.BillingDetails{Version: 1, Status: "partial_unpriced", Components: []service.BillingComponent{
		{Role: "executor", Model: "internal-executor", TotalCost: &price, ActualCost: &price, AccountStatsCost: &price, PricingSource: "private-channel"},
		{Role: "advisor", Model: "claude-advisor", InputTokens: 10, UnpricedReason: "internal-pricing-debug"},
	}}}
	user := UsageLogFromService(log)
	raw, err := json.Marshal(user)
	require.NoError(t, err)
	for _, secret := range []string{"account_stats_cost", "pricing_source", "unpriced_reason", "internal-executor", "private-channel", "internal-pricing-debug"} {
		require.NotContains(t, string(raw), secret)
	}
	require.Contains(t, string(raw), `"total_cost":null`)
	require.Equal(t, "public-executor", user.BillingDetails.Components[0].Model)
	admin := UsageLogFromServiceAdmin(log)
	require.Equal(t, "private-channel", admin.BillingDetails.Components[0].PricingSource)
	require.Equal(t, "internal-executor", admin.BillingDetails.Components[0].Model)
	RedactAdminUsageLogForVendor(admin)
	require.Nil(t, admin.BillingDetails.Components[0].AccountStatsCost)
	require.Empty(t, admin.BillingDetails.Components[0].PricingSource)
	require.Equal(t, "public-executor", admin.BillingDetails.Components[0].Model)
	require.Equal(t, "private-channel", log.BillingDetails.Components[0].PricingSource)
	require.Nil(t, UsageLogFromService(&service.UsageLog{}).BillingDetails)
}
