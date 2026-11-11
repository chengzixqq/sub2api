package service

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOpenAICacheWriteTTLSnapshots(t *testing.T) {
	svc := &OpenAIGatewayService{}
	for name, parse := range map[string]func([]byte, *OpenAIUsage){
		"sse":       svc.parseSSEUsageBytes,
		"websocket": parseOpenAIWSResponseUsageFromCompletedEvent,
	} {
		t.Run(name, func(t *testing.T) { testOpenAICacheWriteTTLSnapshots(t, parse) })
	}
}

func testOpenAICacheWriteTTLSnapshots(t *testing.T, parse func([]byte, *OpenAIUsage)) {
	const initialUsage = `{"input_tokens":100,"input_tokens_details":{"cache_write_tokens":100,"cache_write_tokens_details":{"ttl_30m_tokens":20,"ttl_12h_tokens":80}},"output_tokens":1}`
	for _, tc := range []struct {
		name                        string
		next                        string
		wantTotal, want30m, want12h int
	}{
		{"missing details preserve known split", `{"input_tokens":100,"input_tokens_details":{"cache_write_tokens":100},"output_tokens":2}`, 100, 20, 80},
		{"changed total falls back to aggregate", `{"input_tokens":120,"input_tokens_details":{"cache_write_tokens":120},"output_tokens":2}`, 120, 0, 0},
		{"reported split replaces old split", `{"input_tokens":100,"input_tokens_details":{"cache_write_tokens":100,"cache_write_tokens_details":{"ttl_30m_tokens":70,"ttl_12h_tokens":30}},"output_tokens":2}`, 100, 70, 30},
		{"changed total with reported split", `{"input_tokens":120,"input_tokens_details":{"cache_write_tokens":120,"cache_write_tokens_details":{"ttl_30m_tokens":20,"ttl_12h_tokens":100}},"output_tokens":2}`, 120, 20, 100},
		{"partial details fall back to aggregate", `{"input_tokens":100,"input_tokens_details":{"cache_write_tokens":100,"cache_write_tokens_details":{"ttl_12h_tokens":80}},"output_tokens":2}`, 100, 0, 0},
		{"inconsistent details fall back to aggregate", `{"input_tokens":100,"input_tokens_details":{"cache_write_tokens":100,"cache_write_tokens_details":{"ttl_30m_tokens":20,"ttl_12h_tokens":70}},"output_tokens":2}`, 100, 0, 0},
		{"empty details fall back to aggregate", `{"input_tokens":100,"input_tokens_details":{"cache_write_tokens":100,"cache_write_tokens_details":{}},"output_tokens":2}`, 100, 0, 0},
		{"null details fall back to aggregate", `{"input_tokens":100,"input_tokens_details":{"cache_write_tokens":100,"cache_write_tokens_details":null},"output_tokens":2}`, 100, 0, 0},
		{"all-zero snapshot preserves known usage", `{}`, 100, 20, 80},
	} {
		for _, eventType := range []string{"response.in_progress", "response.completed", "response.failed"} {
			t.Run(tc.name+"/"+eventType, func(t *testing.T) {
				usage := &OpenAIUsage{}
				parse([]byte(fmt.Sprintf(`{"type":"response.in_progress","response":{"usage":%s}}`, initialUsage)), usage)
				next := []byte(fmt.Sprintf(`{"type":%q,"response":{"usage":%s}}`, eventType, tc.next))
				// Cumulative snapshots can be repeated; they must not add tokens.
				parse(next, usage)
				parse(next, usage)
				require.Equal(t, tc.wantTotal, usage.CacheCreationInputTokens)
				require.Equal(t, tc.want30m, usage.CacheCreation30mInputTokens)
				require.Equal(t, tc.want12h, usage.CacheCreation12hInputTokens)
				longPrice := 2e-6
				cost := (&BillingService{}).computeTokenBreakdown(&ModelPricing{
					CacheCreationPricePerToken: 1e-6, CacheCreation12hPricePerToken: &longPrice,
				}, UsageTokens{
					CacheCreationTokens:    usage.CacheCreationInputTokens,
					CacheCreation30mTokens: usage.CacheCreation30mInputTokens,
					CacheCreation12hTokens: usage.CacheCreation12hInputTokens,
				}, 1, "", false)
				wantCost := float64(tc.wantTotal-tc.want12h)*1e-6 + float64(tc.want12h)*longPrice
				require.InDelta(t, wantCost, cost.CacheCreationCost, 1e-12)
			})
		}
	}
}
