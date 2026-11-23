//go:build unit

package service

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const intervalExecutorModel = "claude-sonnet-4"

func compactionIntervalCards() map[string][]PricingInterval {
	tier := func(min int, max *int, price float64) PricingInterval {
		return PricingInterval{MinTokens: min, MaxTokens: max, InputPrice: billingAmount(price), OutputPrice: billingAmount(5 * price)}
	}
	low := new(int(100000))
	high := new(int(200000))
	cacheLow := tier(0, high, 3e-6)
	cacheLow.CacheReadPrice = billingAmount(1.2e-6)
	cacheLow.CacheWritePrice = billingAmount(8e-6)
	cacheLow.CacheWrite1hPrice = billingAmount(.5e-6)
	cacheHigh := tier(200000, nil, 1e-6)
	cacheHigh.CacheReadPrice = billingAmount(.4e-6)
	cacheHigh.CacheWritePrice = billingAmount(2e-6)
	cacheHigh.CacheWrite1hPrice = billingAmount(.2e-6)
	return map[string][]PricingInterval{
		"decreasing":       {tier(0, high, 3e-6), tier(200000, nil, 1e-6)},
		"increasing":       {tier(0, high, 3e-6), tier(200000, nil, 6e-6)},
		"peak":             {tier(0, low, 3e-6), tier(100000, high, 5e-6), tier(200000, nil, 1e-6)},
		"cache_decreasing": {cacheLow, cacheHigh},
		"surcharge":        {tier(128000, high, 6e-6)},
	}
}

type intervalUsageCase struct {
	name, top  string
	iterations []string
	compact    bool
}

func compactionIntervalUsages() []intervalUsageCase {
	const top = `"input_tokens":150000,"output_tokens":20`
	const half = `{"type":"message","input_tokens":75000,"output_tokens":10}`
	const compact = `{"type":"compaction","input_tokens":60000,"output_tokens":100}`
	const cached = `"input_tokens":55000,"output_tokens":20,"cache_read_input_tokens":45000,"cache_creation_input_tokens":50000`
	const cacheMsg = `{"type":"message",` + cached + `,"cache_creation":{"ephemeral_1h_input_tokens":50000}}`
	return []intervalUsageCase{
		{"plain", top, nil, false},
		{"plain_split", top, []string{half, half}, false},
		{"single", top, []string{`{"type":"message",` + top + `}`, compact}, true},
		{"split", top, []string{half, compact, half}, true},
		{"cache", cached, []string{cacheMsg, `{"type":"compaction","input_tokens":25000,"output_tokens":30,"cache_read_input_tokens":20000,"cache_creation_input_tokens":20000}`}, true},
		{"partial", top, []string{half, compact}, true},
		{"recovered_ttl", cached, []string{cacheMsg, `{"type":"compaction","input_tokens":1,"output_tokens":1}`}, true},
	}
}

// Both entries use the same configured card and capture the actual billing command.
func recordIntervalDebits(t *testing.T, usage *ClaudeUsage, key *APIKey, bs *BillingService, resolver *ModelPricingResolver) (openAI, gateway float64) {
	t.Helper()
	user := &User{ID: 2}
	ledger := &openAIRecordUsageBillingRepoStub{result: &UsageBillingApplyResult{Applied: true}}
	logs := &openAIRecordUsageLogRepoStub{inserted: true}
	users := &openAIRecordUsageUserRepoStub{}
	subs := &openAIRecordUsageSubRepoStub{}
	open := newOpenAIRecordUsageServiceForTest(logs, users, subs, nil)
	open.billingService, open.resolver, open.usageBillingRepo = bs, resolver, ledger
	result := &OpenAIForwardResult{RequestID: "open", Model: intervalExecutorModel, Usage: claudeUsageToOpenAIUsage(usage)}
	require.NoError(t, open.RecordUsage(t.Context(), &OpenAIRecordUsageInput{APIKey: key, User: user, Account: &Account{ID: 3, Platform: PlatformKimi, Type: AccountTypeAPIKey}, Result: result}))
	require.NotNil(t, ledger.lastCmd)
	openAI = ledger.lastCmd.BalanceCost
	ledger.lastCmd = nil
	gw := newGatewayRecordUsageServiceWithBillingRepoForTest(logs, ledger, users, subs)
	gw.billingService, gw.resolver = bs, resolver
	input := &RecordUsageInput{APIKey: key, User: user, Account: &Account{ID: 4, Platform: PlatformAnthropic, Type: AccountTypeAPIKey}, Result: &ForwardResult{RequestID: "gateway", Model: intervalExecutorModel, Usage: usage.Clone()}}
	require.NoError(t, gw.RecordUsage(t.Context(), input))
	require.NotNil(t, ledger.lastCmd)
	return openAI, ledger.lastCmd.BalanceCost
}

func TestCompactionIntervalSettlement(t *testing.T) {
	for card, intervals := range compactionIntervalCards() {
		bs, resolver := newTokenCostTestEnv(t, PlatformAnthropic, []ChannelModelPricing{{Platform: PlatformAnthropic, Models: []string{intervalExecutorModel}, BillingMode: BillingModeToken, InputPrice: billingAmount(3e-6), OutputPrice: billingAmount(15e-6), Intervals: intervals}}, nil)
		for _, enabled := range []bool{false, true} {
			gid := int64(100)
			key := &APIKey{ID: 11, GroupID: &gid, Group: &Group{ID: gid, RateMultiplier: 1, LongContextPricingEnabled: enabled}}
			for _, u := range compactionIntervalUsages() {
				for _, advisor := range []bool{false, true} {
					for _, terminal := range []bool{false, true} {
						t.Run(fmt.Sprintf("%s/long=%t/%s/advisor=%t/terminal=%t", card, enabled, u.name, advisor, terminal), func(t *testing.T) {
							entries := append([]string(nil), u.iterations...)
							var advisorEntries []string
							if advisor {
								advisorEntries = []string{`{"type":"advisor_message","model":"claude-sonnet-4","input_tokens":1,"output_tokens":1}`}
								entries = append(entries, advisorEntries...)
							}
							decode := func(iters []string) *ClaudeUsage {
								stop := ""
								if terminal {
									stop = `"stop_reason":"end_turn",`
								}
								return parseClaudeUsageFromResponseBody([]byte(`{"type":"message",` + stop + `"usage":{` + u.top + `,"iterations":[` + strings.Join(iters, ",") + `]}}`))
							}
							o, g := recordIntervalDebits(t, decode(entries), key, bs, resolver)
							t.Logf("DEBIT %s %.10f %.10f", t.Name(), o, g)
							require.Equal(t, o, g)
							if u.compact {
								// The production parser prices the top-level executor when compaction
								// interrupts its all-message breakdown. Advisors remain independent.
								baseOpen, baseGateway := recordIntervalDebits(t, decode(advisorEntries), key, bs, resolver)
								require.GreaterOrEqual(t, o, baseOpen)
								require.GreaterOrEqual(t, g, baseGateway)
								if enabled && terminal && !advisor && u.name == "split" {
									switch card {
									case "peak":
										require.InDelta(t, .932, o, 1e-9)
									case "surcharge":
										require.InDelta(t, 1.0821, o, 1e-9)
									}
								}
							}
						})
					}
				}
			}
		}
	}
}

func TestCompactionChannelPerRequestSettlement(t *testing.T) {
	for _, card := range []struct {
		name      string
		mode      BillingMode
		intervals []PricingInterval
	}{
		{"flat", BillingModePerRequest, nil}, {"tiered", BillingModePerRequest, []PricingInterval{
			{MinTokens: 0, MaxTokens: new(int(100)), PerRequestPrice: billingAmount(.25)}, {MinTokens: 100, PerRequestPrice: billingAmount(.5)}}},
		{"image", BillingModeImage, nil}, {"video", BillingModeVideo, nil},
	} {
		bs, resolver := newTokenCostTestEnv(t, PlatformAnthropic, []ChannelModelPricing{{Platform: PlatformAnthropic, Models: []string{intervalExecutorModel}, BillingMode: card.mode, PerRequestPrice: billingAmount(.25), Intervals: card.intervals}}, nil)
		for _, compacted := range []bool{false, true} {
			var want float64
			for _, loadedID := range []bool{true, false} {
				t.Run(fmt.Sprintf("%s/compact=%t/id=%t", card.name, compacted, loadedID), func(t *testing.T) {
					group := &Group{ID: 100, RateMultiplier: 1.1, LongContextPricingEnabled: true}
					key := &APIKey{ID: 11, Group: group}
					if loadedID {
						key.GroupID = &group.ID
					}
					iterations := `{"type":"message","input_tokens":160,"output_tokens":10}`
					if compacted {
						iterations += `,{"type":"compaction","input_tokens":20,"output_tokens":2},{"type":"compaction","input_tokens":25,"output_tokens":3}`
					}
					usage := parseClaudeUsageFromResponseBody([]byte(`{"type":"message","stop_reason":"end_turn","usage":{"input_tokens":160,"output_tokens":10,"iterations":[` + iterations + `]}}`))
					o, g := recordIntervalDebits(t, usage, key, bs, resolver)
					t.Logf("CARDROW %s %.9f %.9f", t.Name(), o, g)
					require.Positive(t, g)
					require.Equal(t, o, g)
					if loadedID {
						want = g
					} else {
						require.Equal(t, want, g)
					}
				})
			}
		}
	}
}
