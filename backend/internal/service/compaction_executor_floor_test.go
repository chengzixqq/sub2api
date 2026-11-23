//go:build unit

package service

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestCompactionFloorUsesQuotedTokens(t *testing.T) {
	original := UsageTokens{CacheCreationTokens: 90}
	combined := UsageTokens{CacheCreationTokens: 95, CacheCreation1hTokens: 90}
	usage := ClaudeUsage{IterationUsage: &ClaudeIterationUsage{originalExecutor: &original, Iterations: []ClaudeUsageIteration{{Type: "compaction", Usage: UsageTokens{CacheCreationTokens: 5}}}}}
	quote := func(tokens UsageTokens, _ *int) (*CostBreakdown, error) {
		amount := float64(tokens.CacheCreationTokens) - .5*float64(tokens.CacheCreation1hTokens)
		return &CostBreakdown{TotalCost: amount, ActualCost: amount}, nil
	}
	parts := map[int]executorCharge{}
	cost, err := priceCompactedExecutor(usage, combined, false, quote, parts)
	require.NoError(t, err)
	require.Equal(t, 90.0, cost.ActualCost)
	require.Len(t, parts, 1)
	require.Equal(t, original, parts[-1].tokens)
	require.Equal(t, cost, parts[-1].cost)
}
