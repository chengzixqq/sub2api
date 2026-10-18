package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUsageLogAccountCost(t *testing.T) {
	half, stats := 0.5, 0.8
	cases := []struct {
		name string
		log  UsageLog
		want float64
	}{
		{"total cost times multiplier", UsageLog{TotalCost: 2, AccountRateMultiplier: &half, ProviderCostRecorded: true}, 1},
		{"stats cost overrides total cost", UsageLog{TotalCost: 2, AccountStatsCost: &stats, AccountRateMultiplier: &half, ProviderCostRecorded: true}, 0.4},
		{"missing multiplier counts as 1", UsageLog{TotalCost: 2, ProviderCostRecorded: true}, 2},
		{"coalesced follower costs nothing", UsageLog{TotalCost: 2, AccountRateMultiplier: &half, ProviderCostRecorded: false}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.InDelta(t, tc.want, tc.log.AccountCost(), 1e-12)
		})
	}
	var nilLog *UsageLog
	require.Zero(t, nilLog.AccountCost())
}
