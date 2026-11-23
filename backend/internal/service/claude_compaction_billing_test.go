//go:build unit

package service

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"golang.org/x/time/rate"
)

const (
	compactionMessageFields = `"input_tokens":285,"output_tokens":11,"cache_creation_input_tokens":4,"cache_creation":{"ephemeral_5m_input_tokens":1,"ephemeral_1h_input_tokens":3},"cache_read_input_tokens":5`
	compactionMessage       = `{"type":"message",` + compactionMessageFields + `}`
	compactionFirst         = `{"type":"compaction","input_tokens":68495,"output_tokens":227,"cache_creation_input_tokens":6,"cache_creation":{"ephemeral_5m_input_tokens":2,"ephemeral_1h_input_tokens":4},"cache_read_input_tokens":7}`
	compactionSecond        = `{"type":"compaction","input_tokens":20,"output_tokens":2,"cache_creation":{"ephemeral_5m_input_tokens":3,"ephemeral_1h_input_tokens":1},"cache_read_input_tokens":9}`
	compactionFields        = compactionMessageFields + `,"iterations":[` + compactionFirst + `,` + compactionSecond + `,` + compactionMessage + `]`
)

func recordCompactionUsage(t *testing.T, u ClaudeUsage, stream, forceCache bool, ttl string) *UsageLog {
	t.Helper()
	logs := &openAIRecordUsageLogRepoStub{inserted: true}
	ledger := &openAIRecordUsageBillingRepoStub{result: &UsageBillingApplyResult{Applied: true}}
	svc := newGatewayRecordUsageServiceWithBillingRepoForTest(logs, ledger, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{})
	prices, key := advisorTestService()
	svc.billingService, svc.resolver = prices.billingService, prices.resolver
	price := svc.billingService.fallbackPrices["claude-sonnet-4"]
	price.SupportsCacheBreakdown = true
	price.CacheCreation5mPrice, price.CacheCreation1hPrice = 3.75e-6, 6e-6
	key.ID, key.Group.RateMultiplier = 11, 2
	key.User = &User{ID: 22}
	account := &Account{ID: 33, Platform: PlatformAnthropic, Type: AccountTypeAPIKey, RateMultiplier: billingAmount(.5)}
	if ttl != "" {
		account.Type = AccountTypeOAuth
		account.Extra = map[string]any{"cache_ttl_override_enabled": true, "cache_ttl_override_target": ttl}
	}
	result := &ForwardResult{Model: "claude-sonnet-4", RequestID: "compact", Stream: stream, Usage: u}
	input := &RecordUsageInput{Result: result, APIKey: key, User: key.User, Account: account, ForceCacheBilling: forceCache}
	snapshot := result.Usage.Clone()
	quote := svc.QuoteUsageCost(context.Background(), input)
	require.NoError(t, svc.RecordUsage(context.Background(), input))
	if snapshot.HasCompactionUsage() || ttl == "" {
		require.Equal(t, snapshot, result.Usage)
	}
	require.Equal(t, 1, ledger.calls)
	require.Equal(t, 1, logs.calls)
	if snapshot.HasCompactionUsage() || ttl == "" {
		require.InDelta(t, logs.lastLog.ActualCost, quote, 1e-8)
	}
	require.Equal(t, logs.lastLog.ActualCost, ledger.lastCmd.BalanceCost)
	return logs.lastLog
}

func TestClaudeCompactionBillingCombinedContextPricing(t *testing.T) {
	body := `{"type":"message","stop_reason":"end_turn","usage":{"input_tokens":50,"output_tokens":5,"iterations":[{"type":"compaction","input_tokens":50,"output_tokens":5},{"type":"message","input_tokens":50,"output_tokens":5}]}}`
	u := parseClaudeUsageFromResponseBody([]byte(body))
	svc, key := advisorTestService()
	p := svc.billingService.fallbackPrices["claude-sonnet-4"]
	p.LongContextInputThreshold, p.LongContextInputMultiplier, p.LongContextOutputMultiplier = 60, 2, 2
	user := &User{ID: 22}
	key.Group.RateMultiplier = 1
	input := &RecordUsageInput{Result: &ForwardResult{Model: "claude-sonnet-4", Usage: *u}, APIKey: key, User: user}
	require.InDelta(t, .0009, svc.QuoteUsageCost(context.Background(), input), 1e-10)
	// The stream can stop after a valid compaction but before a complete message snapshot.
	u.IterationUsage.Terminal = false
	require.InDelta(t, .0009, svc.QuoteUsageCost(context.Background(), input), 1e-10)
	resolver := svc.resolver
	svc.resolver = nil
	require.InDelta(t, .0009, svc.QuoteUsageCost(context.Background(), input), 1e-10, "catalog fallback also uses the combined context")
	svc.resolver = resolver
	key.Group.ModelPricing = []ChannelModelPricing{{Models: []string{"claude-sonnet-4"}, BillingMode: BillingModePerRequest, PerRequestPrice: billingAmount(.25)}}
	require.InDelta(t, .25, svc.QuoteUsageCost(context.Background(), input), 1e-10)
}

func TestClaudeCompactionBillingCombinedLongContext(t *testing.T) {
	for _, tc := range []struct {
		name      string
		cacheRead int
		inclusive bool
		wantLong  bool
	}{
		{"below_threshold", 29999, false, false},
		{"at_threshold", 30000, false, false},
		{"above_threshold", 30001, false, true},
		{"inclusive_threshold", 30000, true, true},
	} {
		for _, force := range []bool{false, true} {
			for _, complete := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/force_%t/complete_%t", tc.name, force, complete), func(t *testing.T) {
					// Each executor is below 200k. Cache creation's TTL detail must
					// not count twice; the advisor's 10 input tokens must not count.
					body := fmt.Sprintf(`{"type":"message","stop_reason":"end_turn","usage":{"input_tokens":60000,"output_tokens":11,"cache_read_input_tokens":30000,"cache_creation_input_tokens":10000,"cache_creation":{"ephemeral_5m_input_tokens":4000,"ephemeral_1h_input_tokens":6000},"iterations":[{"type":"compaction","input_tokens":60000,"output_tokens":227,"cache_read_input_tokens":%d,"cache_creation_input_tokens":10000,"cache_creation":{"ephemeral_5m_input_tokens":4000,"ephemeral_1h_input_tokens":6000}},{"type":"advisor_message","model":"advisor-test","input_tokens":10,"output_tokens":20},{"type":"message","input_tokens":60000,"output_tokens":11,"cache_read_input_tokens":30000,"cache_creation_input_tokens":10000,"cache_creation":{"ephemeral_5m_input_tokens":4000,"ephemeral_1h_input_tokens":6000}}]}}`, tc.cacheRead)
					u := parseClaudeUsageFromResponseBody([]byte(body))
					u.IterationUsage.Terminal = complete
					svc, key := advisorTestService()
					key.Group.RateMultiplier = 1
					p := svc.billingService.fallbackPrices["claude-sonnet-4"]
					p.LongContextInputThreshold, p.LongContextInputMultiplier, p.LongContextOutputMultiplier = 200000, 2, 2
					p.LongContextThresholdInclusive = tc.inclusive
					p.SupportsCacheBreakdown = true
					p.CacheCreation5mPrice, p.CacheCreation1hPrice = 3.75e-6, 6e-6
					advisor := svc.billingService.fallbackPrices["advisor-test"]
					advisor.LongContextInputThreshold, advisor.LongContextInputMultiplier, advisor.LongContextOutputMultiplier = 5, 2, 2
					result := &ForwardResult{Model: "claude-sonnet-4", Usage: *u}
					input := &RecordUsageInput{Result: result, APIKey: key, User: &User{ID: 22}, Account: &Account{}, ForceCacheBilling: force}
					quote := svc.QuoteUsageCost(context.Background(), input)
					bill := result.withCompactionUsage(context.Background())
					svc.applyBillingCachePolicy(context.Background(), &bill.Usage, input.Account, force)
					cost := svc.calculateTokenCost(context.Background(), bill, key, result.Model, 1, time.Now())
					wantCompaction := .234405 + float64(tc.cacheRead)*.3e-6
					wantMessage := .240165
					if force {
						wantMessage = .078165
					}
					if tc.wantLong {
						wantCompaction *= 2
						wantMessage *= 2
					}
					require.InDelta(t, wantCompaction+wantMessage+1, cost.TotalCost, 1e-10)
					require.InDelta(t, cost.TotalCost, quote, 1e-10)
					require.Len(t, cost.BillingDetails.Components, 3)
					for _, part := range cost.BillingDetails.Components {
						switch {
						case part.Role == "advisor":
							require.InDelta(t, 1, *part.TotalCost, 1e-10, "advisor uses its own context tier")
						case part.IterationIndex != nil && *part.IterationIndex == 0:
							require.Equal(t, 60000, part.InputTokens)
							require.Equal(t, tc.cacheRead, part.CacheReadTokens)
							require.InDelta(t, wantCompaction, *part.TotalCost, 1e-10)
						default:
							require.InDelta(t, wantMessage, *part.TotalCost, 1e-10)
						}
					}
				})
			}
		}
	}
}

func TestClaudeCompactionBillingCombinedContextIntervals(t *testing.T) {
	for _, tc := range []struct {
		name                        string
		enabled                     bool
		highInput, highOutput, want float64
	}{{"disabled", false, 6e-6, 30e-6, .603573}, {"long_context", true, 6e-6, 30e-6, 1.207146}, {"cheaper_high_volume", true, 1e-6, 5e-6, .603573}} {
		t.Run(tc.name, func(t *testing.T) {
			bs, resolver := newTokenCostTestEnv(t, PlatformAnthropic, []ChannelModelPricing{{
				Platform: PlatformAnthropic, Models: []string{"claude-sonnet-4"}, BillingMode: BillingModeToken,
				Intervals: []PricingInterval{
					{MinTokens: 0, MaxTokens: new(int(200000)), InputPrice: billingAmount(3e-6), OutputPrice: billingAmount(15e-6)},
					{MinTokens: 200000, InputPrice: billingAmount(tc.highInput), OutputPrice: billingAmount(tc.highOutput)},
				},
			}}, nil)
			svc := &GatewayService{billingService: bs, resolver: resolver}
			gid := int64(100)
			key := &APIKey{GroupID: &gid, Group: &Group{ID: gid, LongContextPricingEnabled: tc.enabled}}
			resolved := resolver.Resolve(context.Background(), PricingInput{Model: "claude-sonnet-4", GroupID: &gid, Group: key.Group})
			require.Equal(t, PricingSourceChannel, resolved.Source)
			require.Len(t, resolved.Intervals, 2)
			u := parseClaudeUsageFromResponseBody([]byte(`{"type":"message","stop_reason":"end_turn","usage":{"input_tokens":100000,"output_tokens":11,"iterations":[{"type":"compaction","input_tokens":100001,"output_tokens":227},{"type":"message","input_tokens":100000,"output_tokens":11}]}}`))
			result := (&ForwardResult{Usage: *u}).withCompactionUsage(context.Background())
			cost := svc.calculateTokenCost(context.Background(), result, key, "claude-sonnet-4", 1, time.Now())
			require.InDelta(t, tc.want, cost.TotalCost, 1e-10)
			require.Len(t, cost.BillingDetails.Components, 2)

		})
	}
}

func TestClaudeCompactionBillingNoCompactionLongContextBaseline(t *testing.T) {
	for _, advisor := range []bool{false, true} {
		for _, force := range []bool{false, true} {
			t.Run(fmt.Sprintf("advisor_%t/force_%t", advisor, force), func(t *testing.T) {
				svc, key := advisorTestService()
				p := svc.billingService.fallbackPrices["claude-sonnet-4"]
				p.LongContextInputThreshold, p.LongContextInputMultiplier, p.LongContextOutputMultiplier = 60, 2, 2
				u := advisorTestUsage("advisor-test")
				if !advisor {
					u.IterationUsage.AdvisorSeen = false
					u.IterationUsage.Iterations = append(u.IterationUsage.Iterations[:1], u.IterationUsage.Iterations[2:]...)
				}
				svc.applyBillingCachePolicy(context.Background(), &u, &Account{}, force)
				cost := svc.calculateTokenCost(context.Background(), &ForwardResult{Usage: u}, key, "claude-sonnet-4", 1, time.Now())
				if advisor {
					want := .50045
					if force {
						want = .50018
					}
					require.InDelta(t, want, cost.TotalCost, 1e-10, "f01b99baf prices message iterations separately with advisor")
					require.False(t, cost.LongContextBillingApplied)
				} else {
					want := .0009
					if force {
						want = .00036
					}
					require.InDelta(t, want, cost.TotalCost, 1e-10, "f01b99baf prices the top-level aggregate without advisor")
					require.True(t, cost.LongContextBillingApplied)
					require.Nil(t, cost.BillingDetails)
				}
			})
		}
	}
}

func TestClaudeCompactionBillingIncompleteStream(t *testing.T) {
	var usage ClaudeUsage
	parseSSEUsagePassthrough(`{"type":"message_delta","usage":{"iterations":[`+compactionFirst+`,{"type":"compaction","input_tokens":-1,"output_tokens":2}]}}`, &usage)
	require.True(t, usage.hasObservedTokens())
	decision := DecideFailureBilling(FailureBillingInput{Usage: usage, UpstreamUsageOnly: true})
	require.True(t, decision.Billable)
	log := recordCompactionUsage(t, decision.Usage, true, false, "")
	require.Equal(t, 68495, log.InputTokens)
	require.Equal(t, 227, log.OutputTokens)
	require.InDelta(t, .2089236, log.TotalCost, 1e-8)
}

func TestClaudeCompactionBillingReportedAPIKeyUsage(t *testing.T) {
	body := `{"type":"message","stop_reason":"end_turn","usage":{"input_tokens":285,"output_tokens":11,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"iterations":[{"type":"compaction","input_tokens":68495,"output_tokens":227},{"type":"message","input_tokens":285,"output_tokens":11}]}}`
	usage := parseClaudeUsageFromResponseBody([]byte(body))
	for _, force := range []bool{false, true} {
		t.Run(fmt.Sprintf("force_cache_%t", force), func(t *testing.T) {
			log := recordCompactionUsage(t, *usage, false, force, "")
			require.Equal(t, 238, log.OutputTokens)
			if force {
				require.Equal(t, 68495, log.InputTokens)
				require.Equal(t, 285, log.CacheReadTokens)
				require.InDelta(t, .2091405, log.TotalCost, 1e-10)
				require.InDelta(t, .418281, log.ActualCost, 1e-10)
				t.Logf("official sample ForceCache: total=$%.7f, actual (2x)=$%.7f", log.TotalCost, log.ActualCost)
			} else {
				require.Equal(t, 68780, log.InputTokens)
				require.Zero(t, log.CacheReadTokens)
				require.InDelta(t, .20991, log.TotalCost, 1e-10)
			}
		})
	}
}

func TestClaudeCompactionOverflowBillsTopLevelUsage(t *testing.T) {
	var messages bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&messages, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	// The first compaction fits. The second overflows after partial accumulation.
	body := fmt.Sprintf(`{"type":"message","stop_reason":"end_turn","usage":{%s,"iterations":[%s,{"type":"compaction","input_tokens":%d,"output_tokens":0},{"type":"advisor_message","model":"advisor-test","input_tokens":10,"output_tokens":20}]}}`, compactionMessageFields, compactionFirst, math.MaxInt)
	u := parseClaudeUsageFromResponseBody([]byte(body))
	log := recordCompactionUsage(t, *u, false, false, "")
	require.Equal(t, 285, log.InputTokens)
	require.Equal(t, 11, log.OutputTokens)
	require.Equal(t, 1, log.CacheCreation5mTokens)
	require.Equal(t, 3, log.CacheCreation1hTokens)
	require.Equal(t, 5, log.CacheReadTokens)
	require.InDelta(t, .00104325, log.TotalCost, 1e-10)
	require.Nil(t, log.BillingDetails)
	require.Contains(t, messages.String(), "level=ERROR msg=billing.compaction_usage_fallback request_id=compact")
	require.Contains(t, messages.String(), "compaction usage exceeds representable token total")
}

func checkCompactionLog(t *testing.T, log *UsageLog) {
	t.Helper()
	require.Equal(t, 68800, log.InputTokens)
	require.Equal(t, 240, log.OutputTokens)
	require.Equal(t, 14, log.CacheCreationTokens)
	require.Equal(t, 6, log.CacheCreation5mTokens)
	require.Equal(t, 8, log.CacheCreation1hTokens)
	require.Equal(t, 21, log.CacheReadTokens)
	// 68800 inputs, 240 outputs, 6 short writes, 8 long writes and 21 reads.
	require.InDelta(t, .2100768, log.TotalCost, 1e-8)
	require.InDelta(t, .4201536, log.ActualCost, 1e-8)
	require.InDelta(t, .1050384, log.AccountCost(), 1e-8)
}

func checkForwardedCompactionLog(t *testing.T, usage ClaudeUsage, stream bool, path string) {
	t.Helper()
	if path == "oauth_ttl" {
		log := recordCompactionUsage(t, usage, stream, false, "5m")
		require.Equal(t, 68800, log.InputTokens)
		require.Equal(t, 240, log.OutputTokens)
		require.Equal(t, 21, log.CacheReadTokens)
		require.Equal(t, 14, log.CacheCreationTokens)
		require.Equal(t, 9, log.CacheCreation5mTokens)
		require.Equal(t, 5, log.CacheCreation1hTokens)
		require.InDelta(t, .21007005, log.TotalCost, 1e-8)
		return
	}
	checkCompactionLog(t, recordCompactionUsage(t, usage, stream, false, ""))
}

func TestClaudeCompactionBillingNonStreaming(t *testing.T) {
	body := `{"type":"message","stop_reason":"end_turn","usage":{` + compactionFields + `}}`
	for _, path := range []string{"apikey_passthrough", "normal", "oauth_ttl"} {
		t.Run(path, func(t *testing.T) {
			svc := newMinimalGatewayService()
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}
			account := &Account{ID: 33, Platform: PlatformAnthropic, Type: AccountTypeAPIKey}
			var usage *ClaudeUsage
			var err error
			if path == "oauth_ttl" {
				account.Type = AccountTypeOAuth
				account.Extra = map[string]any{"cache_ttl_override_enabled": true, "cache_ttl_override_target": "5m"}
				c.Request.Header.Set("Anthropic-Beta", "compact-2026-01-12")
			}
			if path != "apikey_passthrough" {
				usage, err = svc.handleNonStreamingResponse(context.Background(), resp, c, account, "claude-sonnet-4", "claude-sonnet-4")
			} else {
				usage, err = svc.handleNonStreamingResponseAnthropicAPIKeyPassthrough(context.Background(), resp, c, account)
			}
			require.NoError(t, err)
			if path == "oauth_ttl" {
				assertCompactionWireTTL(t, gjson.Get(rec.Body.String(), "usage"))
			} else {
				require.Equal(t, body, rec.Body.String())
			}
			checkForwardedCompactionLog(t, *usage, false, path)
		})
	}
}

func TestClaudeCompactionBillingStreaming(t *testing.T) {
	for _, placement := range []string{"start", "delta", "delta_without_ttl", "both", "growing"} {
		t.Run(placement, func(t *testing.T) {
			start, delta := compactionMessageFields, compactionMessageFields
			switch placement {
			case "start":
				start = compactionFields
			case "delta":
				delta = compactionFields
			case "delta_without_ttl":
				delta = `"output_tokens":11,"iterations":[` + compactionFirst + `,` + compactionSecond + `,` + compactionMessage + `]`
			case "both":
				start, delta = compactionFields, compactionFields
			case "growing":
				// BetaMessageDeltaUsage can update the aggregate without a top-level
				// cache_creation breakdown. The completed message iteration has both TTLs.
				start = `"input_tokens":5,"cache_creation_input_tokens":1,"cache_creation":{"ephemeral_5m_input_tokens":1},"iterations":[` + compactionFirst + `]`
				delta = `"input_tokens":285,"output_tokens":11,"cache_read_input_tokens":5,"cache_creation_input_tokens":4,"iterations":[` + compactionFirst + `,` + compactionSecond + `,` + compactionMessage + `]`
			}
			payload := `event: message_start
data: {"type":"message_start","message":{"id":"msg_compact","type":"message","role":"assistant","content":[],"model":"claude-sonnet-4","usage":{` + start + `}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"compaction","content":null}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"compaction_delta","content":"summary"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{` + delta + `}}

event: message_stop
data: {"type":"message_stop"}

`
			for _, path := range []string{"apikey_passthrough", "normal", "oauth_ttl"} {
				t.Run(path, func(t *testing.T) {
					rec := httptest.NewRecorder()
					c, _ := gin.CreateTestContext(rec)
					c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
					resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(payload))}
					svc := newMinimalGatewayService()
					account := &Account{ID: 33, Platform: PlatformAnthropic, Type: AccountTypeAPIKey}
					var result *streamingResult
					var err error
					if path == "oauth_ttl" {
						account.Type = AccountTypeOAuth
						account.Extra = map[string]any{"cache_ttl_override_enabled": true, "cache_ttl_override_target": "5m"}
						c.Request.Header.Set("Anthropic-Beta", "compact-2026-01-12")
					}
					if path != "apikey_passthrough" {
						result, err = svc.handleStreamingResponse(context.Background(), resp, c, account, time.Now(), "claude-sonnet-4", "claude-sonnet-4", false)
					} else {
						result, err = svc.handleStreamingResponseAnthropicAPIKeyPassthrough(context.Background(), resp, c, account, time.Now(), "claude-sonnet-4")
					}
					require.NoError(t, err)
					if path == "oauth_ttl" {
						for _, line := range strings.Split(rec.Body.String(), "\n") {
							if data, ok := extractAnthropicSSEDataLine(line); ok {
								event := gjson.Parse(data)
								node := event.Get("usage")
								if event.Get("type").Str == "message_start" {
									node = event.Get("message.usage")
								}
								if node.IsObject() {
									assertCompactionWireTTL(t, node)
								}
							}
						}
						if placement == "delta_without_ttl" {
							require.Equal(t, 3, result.usage.CacheCreation1hTokens)
						}
					} else {
						require.Equal(t, payload, rec.Body.String())
					}
					checkForwardedCompactionLog(t, *result.usage, true, path)
				})
			}
		})
	}
}

func TestClaudeCompactionBillingWithoutCompaction(t *testing.T) {
	for _, iterations := range []string{"", `,"iterations":[` + compactionMessage + `]`} {
		u := parseClaudeUsageFromResponseBody([]byte(`{"type":"message","stop_reason":"end_turn","content":[{"type":"compaction","content":"previous summary"}],"usage":{` + compactionMessageFields + iterations + `}}`))
		log := recordCompactionUsage(t, *u, false, false, "")
		require.Equal(t, 285, log.InputTokens)
		require.Equal(t, 11, log.OutputTokens)
		require.Equal(t, 4, log.CacheCreationTokens)
		require.Equal(t, 1, log.CacheCreation5mTokens)
		require.Equal(t, 3, log.CacheCreation1hTokens)
		require.Equal(t, 5, log.CacheReadTokens)
		require.InDelta(t, .00104325, log.TotalCost, 1e-8)
		require.InDelta(t, .0020865, log.ActualCost, 1e-8)
		require.Nil(t, log.BillingDetails)
	}
}

func TestClaudeCompactionBillingOptionalMessageTTL(t *testing.T) {
	body := `{"type":"message","stop_reason":"end_turn","usage":{"input_tokens":285,"output_tokens":11,"cache_creation_input_tokens":4,"cache_read_input_tokens":5,"iterations":[` +
		compactionFirst + `,` + compactionSecond + `,` + compactionMessage + `]}}`
	u := parseClaudeUsageFromResponseBody([]byte(body))
	require.True(t, claudeExecutorIterationsComplete(*u))
	checkCompactionLog(t, recordCompactionUsage(t, *u, false, false, ""))
}

func TestClaudeCompactionBillingWithAdvisorAndCacheOverrides(t *testing.T) {
	advisor := `{"type":"advisor_message","model":"advisor-test","input_tokens":10,"output_tokens":20,"cache_creation_input_tokens":7,"cache_creation":{"ephemeral_1h_input_tokens":7}}`
	body := `{"type":"message","stop_reason":"end_turn","usage":{` + compactionMessageFields + `,"iterations":[` + compactionFirst + `,` + advisor + `,` + compactionMessage + `]}}`
	u := parseClaudeUsageFromResponseBody([]byte(body))
	log := recordCompactionUsage(t, *u, false, true, "5m")
	require.NotNil(t, log.BillingDetails)
	require.Equal(t, "complete", log.BillingDetails.Status)
	require.Len(t, log.BillingDetails.Components, 3)
	require.Equal(t, 68495, log.InputTokens)
	require.Equal(t, 238, log.OutputTokens)
	require.Equal(t, 297, log.CacheReadTokens)
	require.Equal(t, 6, log.CacheCreation5mTokens)
	require.Equal(t, 4, log.CacheCreation1hTokens)
	executor := log.BillingDetails.Components[0]
	require.Equal(t, "executor", executor.Role)
	require.Equal(t, 68495, executor.InputTokens)
	require.Equal(t, 7, executor.CacheReadTokens)
	require.Equal(t, 2, executor.CacheCreation5mTokens)
	require.Equal(t, 4, executor.CacheCreation1hTokens)
	part := log.BillingDetails.Components[1]
	require.Equal(t, "advisor", part.Role)
	require.Equal(t, 10, part.InputTokens)
	require.Equal(t, 7, part.CacheCreation1hTokens)
	require.InDelta(t, .92, *part.TotalCost, 1e-8)
	require.InDelta(t, 1.1291906, log.TotalCost, 1e-8)
	require.InDelta(t, 2.2583812, log.ActualCost, 1e-8)
}

func TestCompactionBetaOAuthTTLWithoutCompaction(t *testing.T) {
	const raw = `{"cache_creation_input_tokens":4,"cache_creation":{"ephemeral_5m_input_tokens":1,"ephemeral_1h_input_tokens":3}}`
	const rewritten = `{"cache_creation_input_tokens":4,"cache_creation":{"ephemeral_5m_input_tokens":4,"ephemeral_1h_input_tokens":0}}`
	const marshaled = `{"cache_creation":{"ephemeral_1h_input_tokens":0,"ephemeral_5m_input_tokens":4},"cache_creation_input_tokens":4}`
	const stop = "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	for _, tc := range []struct{ name, body, want string }{
		{"nonstream", `{"type":"message", "usage":` + raw + `}`, `{"type":"message", "usage":` + rewritten + `}`},
		{"stream", "event: message_start\ndata: " + `{"type":"message_start","message":{"usage":` + raw + `}}` + "\n\nevent: message_delta\ndata: " + `{"type":"message_delta","usage":` + raw + `}` + "\n\n" + stop,
			"event: message_start\ndata: " + `{"message":{"usage":` + marshaled + `},"type":"message_start"}` + "\n\nevent: message_delta\ndata: " + `{"type":"message_delta","usage":` + marshaled + `}` + "\n\n" + stop},
	} {
		t.Run(tc.name, func(t *testing.T) {
			account, settings, _ := cacheTTLOverrideBoundaryAccount(t, "global")
			svc := newMinimalGatewayService()
			svc.settingService = settings
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			req.Header.Set("Anthropic-Beta", "compact-2026-01-12")
			resp := &http.Response{Request: req, Header: http.Header{}, StatusCode: 200, Body: io.NopCloser(strings.NewReader(tc.body))}
			var u *ClaudeUsage
			if tc.name == "stream" {
				result, err := svc.handleStreamingResponse(context.Background(), resp, c, account, time.Now(), "m", "m", false)
				require.NoError(t, err)
				u = result.usage
			} else {
				result, err := svc.handleNonStreamingResponse(context.Background(), resp, c, account, "m", "m")
				require.NoError(t, err)
				u = result
			}
			require.Equal(t, tc.want, rec.Body.String())
			require.Equal(t, 4, u.CacheCreation5mTokens)
			require.Zero(t, u.CacheCreation1hTokens)
		})
	}
}

func TestCompactionBetaReadsAllHeaderValues(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	resp := &http.Response{}
	for _, key := range []string{"Anthropic-Beta", "anthropic-beta", "ANTHROPIC-BETA"} {
		c.Request.Header = http.Header{key: []string{"other-beta", "third-beta, compact-2026-01-12"}}
		require.True(t, responseUsesCompactionBeta(c, resp))
		resp.Request = c.Request.Clone(context.Background())
		require.True(t, responseUsesCompactionBeta(c, resp))
		resp.Request.Header[key] = []string{"compact-2026-01-12-preview"}
		require.False(t, responseUsesCompactionBeta(c, resp))
		resp.Request = nil
	}
}

func TestZeroCompactionPreservesBaselineAdvisorBilling(t *testing.T) {
	u := advisorTestUsage("advisor-test")
	u.IterationUsage.Iterations = append(u.IterationUsage.Iterations, ClaudeUsageIteration{Type: "compaction"})
	svc, key := advisorTestService()
	p := svc.billingService.fallbackPrices["claude-sonnet-4"]
	p.LongContextInputThreshold, p.LongContextInputMultiplier, p.LongContextOutputMultiplier = 60, 2, 2
	cost := svc.calculateTokenCost(context.Background(), &ForwardResult{Usage: u}, key, "claude-sonnet-4", 1, time.Now())
	// f01b99baf uses one executor aggregate and preserves the advisor's wire index.
	require.Equal(t, "partial_usage", cost.BillingDetails.Status)
	require.Equal(t, 1, cost.BillingDetails.Version)
	require.Len(t, cost.BillingDetails.Components, 2)
	require.Nil(t, cost.BillingDetails.Components[0].IterationIndex)
	require.Equal(t, 1, *cost.BillingDetails.Components[1].IterationIndex)
	require.InDelta(t, .5009, cost.TotalCost, 1e-10)
	require.True(t, cost.LongContextBillingApplied)
}

func TestBedrockCompactionUsageSource(t *testing.T) {
	const native = `"usage":{"input_tokens":25,"output_tokens":5,"iterations":[{"type":"message","input_tokens":25,"output_tokens":5},{"type":"compaction","input_tokens":50,"output_tokens":7}]}`
	const metrics = `"amazon-bedrock-invocationMetrics":{"inputTokenCount":75,"outputTokenCount":12}`
	const start = `{"type":"message_start","message":{` + native + `}}`
	const stop = `{"type":"message_stop"}`
	for _, tc := range []struct {
		name, body     string
		events         []string
		wantIterations bool
	}{

		{"nonstream_metrics", `{"type":"message",` + metrics + `}`, nil, false},
		{"nonstream_native_and_metrics", `{"type":"message","stop_reason":"end_turn",` + native + `,` + metrics + `}`, nil, true},
		{"stream_delta_metrics", "", []string{start, `{"type":"message_delta",` + metrics + `}`, stop}, true},
		{"stream_partial_native_after_metrics", "", []string{start, `{"type":"message_delta",` + metrics + `}`, `{"type":"message_delta","usage":{"output_tokens":12}}`, stop}, true},
		{"stream_start_native_and_metrics", "", []string{`{"type":"message_start","message":{` + native + `},` + metrics + `}`, `{"type":"message_delta","usage":{"output_tokens":5}}`, stop}, true},
		{"stream_stop_metrics", "", []string{start, `{"type":"message_stop",` + metrics + `}`}, true},
		{"stream_stop_native_and_metrics", "", []string{start, `{"type":"message_stop",` + native + `,` + metrics + `}`}, true},
		{"stream_native_and_metrics", "", []string{start, `{"type":"message_delta",` + native + `,` + metrics + `}`, stop}, true},
		{"stream_native_supersedes_metrics", "", []string{start, `{"type":"message_delta",` + metrics + `}`, `{"type":"message_delta",` + native + `}`, stop}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := newMinimalGatewayService()
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}}
			account := newBedrockErrorTestAccount()
			var u *ClaudeUsage
			if tc.events == nil {
				resp.Body = io.NopCloser(strings.NewReader(tc.body))
				var err error
				u, err = svc.handleBedrockNonStreamingResponse(context.Background(), resp, c, account)
				require.NoError(t, err)
			} else {
				var frames bytes.Buffer
				for _, event := range tc.events {
					frames.Write(bedrockChunkFrame(event))
				}
				resp.Body = io.NopCloser(&frames)
				result, err := svc.handleBedrockStreamingResponse(context.Background(), resp, c, account, time.Now(), "claude-sonnet-4")
				require.NoError(t, err)
				u = result.usage
			}
			require.NotContains(t, rec.Body.String(), "amazon-bedrock-invocationMetrics")
			log := recordCompactionUsage(t, *u, tc.events != nil, false, "")
			require.Equal(t, 75, log.InputTokens)
			require.Equal(t, 12, log.OutputTokens)
			require.InDelta(t, 75*3e-6+12*15e-6, log.TotalCost, 1e-10)
			if tc.wantIterations {
				require.NotNil(t, log.BillingDetails)
				require.Len(t, log.BillingDetails.Components, 2)
			} else {
				require.Nil(t, log.BillingDetails)
			}
		})
	}
}

func TestZeroCompactionAddsNoBillingComponent(t *testing.T) {
	const zero = `{"type":"compaction","input_tokens":0,"output_tokens":0,"cache_read_input_tokens":0,"cache_creation_input_tokens":0,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":0}}`
	for _, paid := range []bool{false, true} {
		fields := compactionMessageFields + `,"iterations":[` + compactionMessage + `,` + zero
		if paid {
			fields += `,` + compactionFirst
		}
		u := parseClaudeUsageFromResponseBody([]byte(`{"type":"message","stop_reason":"end_turn","usage":{` + fields + `]}}`))
		log := recordCompactionUsage(t, *u, false, false, "")
		if paid {
			require.Equal(t, "complete", log.BillingDetails.Status)
			require.Len(t, log.BillingDetails.Components, 2)
			require.Equal(t, 2, *log.BillingDetails.Components[1].IterationIndex)
			require.Equal(t, 68780, log.InputTokens)
		} else {
			require.Nil(t, log.BillingDetails)
			require.InDelta(t, .00104325, log.TotalCost, 1e-10)
		}
	}
}

func TestCompactionForceCacheBillingSwitch(t *testing.T) {
	body := `{"type":"message","stop_reason":"end_turn","usage":{` + compactionFields + `}}`
	for _, force := range []bool{false, true} {
		raw := parseClaudeUsageFromResponseBody([]byte(body))
		result := (&ForwardResult{Usage: *raw}).withCompactionUsage(context.Background())
		svc, _ := advisorTestService()
		svc.applyBillingCachePolicy(context.Background(), &result.Usage, &Account{}, force)
		wantInput := 68800
		if force {
			wantInput = 68515
		}
		require.Equal(t, wantInput, result.Usage.InputTokens)
		require.Equal(t, 68800+21-wantInput, result.Usage.CacheReadInputTokens)
		require.Equal(t, raw.IterationUsage.Iterations[:2], result.Usage.IterationUsage.Iterations[:2], "compaction input and cache buckets retain upstream values")
		require.Equal(t, 14, result.Usage.CacheCreationInputTokens)
		require.Equal(t, 6, result.Usage.CacheCreation5mTokens)
		require.Equal(t, 8, result.Usage.CacheCreation1hTokens)
		require.True(t, claudeIterationsMatchAggregate(result.Usage, true))
		require.Equal(t, 285, raw.InputTokens)
	}
}

func TestBedrockInvocationMetricsWithAdvisorAndCompaction(t *testing.T) {
	start := `{"type":"message_start","message":{"usage":{"input_tokens":25,"iterations":[{"type":"message","input_tokens":25,"output_tokens":5},{"type":"compaction","input_tokens":50,"output_tokens":7},{"type":"advisor_message","model":"advisor-test","input_tokens":10,"output_tokens":20}]}}}`
	stop := `{"type":"message_stop","amazon-bedrock-invocationMetrics":{"inputTokenCount":75,"outputTokenCount":12}}`
	frames := append(bedrockChunkFrame(start), bedrockChunkFrame(stop)...)
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(frames))}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	result, err := newMinimalGatewayService().handleBedrockStreamingResponse(context.Background(), resp, c, newBedrockErrorTestAccount(), time.Now(), "claude-sonnet-4")
	require.NoError(t, err)
	log := recordCompactionUsage(t, *result.usage, true, false, "")
	require.Equal(t, 75, log.InputTokens)
	require.Equal(t, 12, log.OutputTokens)
	require.InDelta(t, .5+75*3e-6+12*15e-6, log.TotalCost, 1e-10)
	require.NotNil(t, log.BillingDetails)
	require.Len(t, log.BillingDetails.Components, 3)
	require.Equal(t, 50, log.BillingDetails.Components[1].InputTokens)
	require.Equal(t, "advisor", log.BillingDetails.Components[2].Role)
	require.Equal(t, 2, *log.BillingDetails.Components[2].IterationIndex)
}

func assertCompactionWireTTL(t *testing.T, node gjson.Result) {
	t.Helper()
	for _, u := range append(node.Get("iterations").Array(), node) {
		if cache := u.Get("cache_creation"); cache.IsObject() {
			require.Zero(t, cache.Get("ephemeral_1h_input_tokens").Int())
			if agg := u.Get("cache_creation_input_tokens"); agg.Exists() {
				require.Equal(t, agg.Int(), cache.Get("ephemeral_5m_input_tokens").Int())
			}
		}
	}
}

func TestBedrockCompactionMetricsBilling(t *testing.T) {
	const usage = `{"input_tokens":25,"output_tokens":5,"iterations":[{"type":"message","input_tokens":25,"output_tokens":5},{"type":"compaction","input_tokens":50,"output_tokens":7}]}`
	native := parseClaudeUsageFromResponseBody([]byte(`{"type":"message","stop_reason":"end_turn","usage":` + usage + `}`))
	for _, counts := range [][2]int{{75, 12}, {90, 18}, {90, 3}, {9, 18}, {0, 0}} {
		metrics := fmt.Sprintf(`"amazon-bedrock-invocationMetrics":{"inputTokenCount":%d,"outputTokenCount":%d}`, counts[0], counts[1])
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d_%d/stream=%t", counts[0], counts[1], stream), func(t *testing.T) {
				var u *ClaudeUsage
				if stream {
					u, _ = readBedrockBillingFixture(t, "", `{"type":"message_start","message":{"usage":`+usage+`}}`, `{"type":"message_stop",`+metrics+`}`)
				} else {
					u, _ = readBedrockBillingFixture(t, `{"type":"message","stop_reason":"end_turn","usage":`+usage+`,`+metrics+`}`)
				}
				for _, force := range []bool{false, true} {
					log := recordCompactionUsage(t, *u, stream, force, "")
					input, read := max(counts[0], 75), 0
					if force {
						read = input - 50
						input = 50
					}
					output := max(counts[1], 12)
					require.Equal(t, [3]int{input, output, read}, [3]int{log.InputTokens, log.OutputTokens, log.CacheReadTokens})
					require.InDelta(t, float64(input)*3e-6+float64(output)*15e-6+float64(read)*.3e-6, log.TotalCost, 1e-10)
					require.GreaterOrEqual(t, log.TotalCost, recordCompactionUsage(t, *native, stream, force, "").TotalCost)
				}
			})
		}
	}
}

func TestAntigravityUpstreamCompactionSettlement(t *testing.T) {
	const usage = `{"input_tokens":285,"output_tokens":11,"iterations":[{"type":"compaction","input_tokens":68495,"output_tokens":227},{"type":"message","input_tokens":285,"output_tokens":11}]}`
	for _, mode := range []string{"buffered", "stream", "stream_no_space"} {
		stream := mode != "buffered"
		t.Run(mode, func(t *testing.T) {
			body := `{"type":"message","stop_reason":"end_turn","usage":` + usage + `}`
			if stream {
				body = "data: " + `{"type":"message_start","message":{"usage":` + usage + `}}` + "\n\ndata: " + `{"type":"message_delta","usage":{"output_tokens":11}}` + "\n\ndata: " + `{"type":"message_stop"}` + "\n\n"
			}
			if mode == "stream_no_space" {
				body = strings.ReplaceAll(body, "data: ", "data:")
			}
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			c.Request.Header.Set("Anthropic-Beta", "compact-2026-01-12")
			resp := &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}
			upstream := &httpUpstreamStub{resp: resp}
			svc := &AntigravityGatewayService{httpUpstream: upstream, settingService: &SettingService{}}
			result, err := svc.ForwardUpstream(context.Background(), c, &Account{ID: 701, Type: AccountTypeUpstream, Credentials: map[string]any{"base_url": "https://upstream.example", "api_key": "test-key"}}, []byte(fmt.Sprintf(`{"model":"claude-sonnet-4","stream":%t,"messages":[],"context_management":{"edits":[{"type":"compact_20260112"}]}}`, stream)))
			require.NoError(t, err)
			require.NotNil(t, result.Usage.IterationUsage)
			require.True(t, result.Usage.IterationUsage.Terminal)
			log := recordCompactionUsage(t, result.Usage, stream, true, "")
			t.Logf("compaction counts=%d/%d/%d total=%.9f", log.InputTokens, log.OutputTokens, log.CacheReadTokens, log.TotalCost)
			require.Equal(t, [3]int{68495, 238, 285}, [3]int{log.InputTokens, log.OutputTokens, log.CacheReadTokens})
			require.InDelta(t, .2091405, log.TotalCost, 1e-10)
		})
	}
}

func TestOpenAINativeCompactionSettlement(t *testing.T) {
	for _, advisor := range []bool{false, true} {
		t.Run(fmt.Sprint(advisor), func(t *testing.T) {
			extra := ""
			if advisor {
				extra = `,{"type":"advisor_message","model":"advisor-test","input_tokens":10,"output_tokens":20}`
			}
			raw := parseClaudeUsageFromResponseBody([]byte(`{"type":"message","stop_reason":"end_turn","usage":{"input_tokens":25,"output_tokens":5,"iterations":[{"type":"message","input_tokens":25,"output_tokens":5},{"type":"compaction","input_tokens":50,"output_tokens":7}` + extra + `]}}`))
			logs := &openAIRecordUsageLogRepoStub{inserted: true}
			userRepo := &openAIRecordUsageUserRepoStub{}
			svc := newOpenAIRecordUsageServiceForTest(logs, userRepo, &openAIRecordUsageSubRepoStub{}, nil)
			prices, key := advisorTestService()
			svc.billingService, svc.resolver = prices.billingService, prices.resolver
			key.Group.RateMultiplier = 1
			result := &OpenAIForwardResult{RequestID: "compact", Model: "claude-sonnet-4", Usage: claudeUsageToOpenAIUsage(raw)}
			require.NoError(t, svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{Result: result, APIKey: key, Account: &Account{ID: 3, Type: AccountTypeAPIKey, Platform: PlatformOpenAI}, User: &User{ID: 2}}))
			require.Equal(t, 75, logs.lastLog.InputTokens)
			require.Equal(t, 12, logs.lastLog.OutputTokens)
			want := .000405
			if advisor {
				want += .5
			}
			require.InDelta(t, want, logs.lastLog.TotalCost, 1e-10)
			require.InDelta(t, want, userRepo.lastAmount, 1e-10)
			require.Equal(t, 25, result.Usage.InputTokens)
		})
	}
}

func TestCompactionCustomerChargeRespectsConfiguredMultiplier(t *testing.T) {
	logs := &openAIRecordUsageLogRepoStub{inserted: true}
	ledger := &openAIRecordUsageBillingRepoStub{result: &UsageBillingApplyResult{Applied: true}}
	svc := newGatewayRecordUsageServiceWithBillingRepoForTest(logs, ledger, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{})
	prices, key := advisorTestService()
	svc.billingService, svc.resolver = prices.billingService, prices.resolver
	key.Group.RateMultiplier = .5
	user := &User{ID: 22}
	account := &Account{ID: 33, RateMultiplier: billingAmount(1.5)}
	u := parseClaudeUsageFromResponseBody([]byte(`{"type":"message","stop_reason":"end_turn","usage":{"input_tokens":25,"output_tokens":5,"iterations":[{"type":"message","input_tokens":25,"output_tokens":5},{"type":"compaction","input_tokens":50,"output_tokens":7}]}}`))
	in := &RecordUsageInput{Result: &ForwardResult{Model: "claude-sonnet-4", RequestID: "compact", Usage: *u}, APIKey: key, User: user, Account: account}
	quote := svc.QuoteUsageCost(context.Background(), in)
	require.NoError(t, svc.RecordUsage(context.Background(), in))
	require.InDelta(t, .0006075, logs.lastLog.AccountCost(), 1e-10)
	require.InDelta(t, .0002025, ledger.lastCmd.BalanceCost, 1e-10)
	require.InDelta(t, ledger.lastCmd.BalanceCost, quote, 1e-10)
}

func TestBedrockCompactionIncompleteMessageSnapshot(t *testing.T) {
	for _, tc := range []struct {
		name, entries, delta string
		wantIn, wantOut      int
	}{
		{"only_compaction", `{"type":"compaction","input_tokens":50,"output_tokens":7}`, `{"type":"message_delta","amazon-bedrock-invocationMetrics":{"inputTokenCount":75,"outputTokenCount":12}}`, 75, 12},
		{"stale_message_iteration", `{"type":"message","input_tokens":15,"output_tokens":5},{"type":"compaction","input_tokens":50,"output_tokens":7}`, `{"type":"message_stop","amazon-bedrock-invocationMetrics":{"inputTokenCount":0,"outputTokenCount":0}}`, 75, 12},
	} {
		t.Run(tc.name, func(t *testing.T) {
			start := `{"type":"message_start","message":{"usage":{"input_tokens":25,"iterations":[` + tc.entries + `]}}}`
			u, _ := readBedrockBillingFixture(t, "", start, `{"type":"message_delta","usage":{"output_tokens":5}}`, tc.delta)
			log := recordCompactionUsage(t, *u, true, false, "")
			require.Equal(t, [2]int{tc.wantIn, tc.wantOut}, [2]int{log.InputTokens, log.OutputTokens})
		})
	}
}

func TestBedrockCompactionFinalCounters(t *testing.T) {
	const start = `{"type":"message_start","message":{"usage":{"input_tokens":25}}}`
	for _, tc := range []struct {
		name, stop    string
		input, output int
	}{
		{"valid_zero_compaction", `{"type":"message_stop","usage":{"iterations":[{"type":"compaction","input_tokens":0,"output_tokens":0}]},"amazon-bedrock-invocationMetrics":{"inputTokenCount":80,"outputTokenCount":13}}`, 25, 0},
		{"native_stop_after_partial_start", `{"type":"message_stop","usage":{"input_tokens":40,"output_tokens":9,"iterations":[{"type":"compaction","input_tokens":50,"output_tokens":7}]}}`, 90, 16},
		{"invalid_compaction", `{"type":"message_stop","usage":{"iterations":[{"type":"compaction","input_tokens":-1,"output_tokens":7}]},"amazon-bedrock-invocationMetrics":{"inputTokenCount":80,"outputTokenCount":13}}`, 25, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u, _ := readBedrockBillingFixture(t, "", start, tc.stop)
			log := recordCompactionUsage(t, *u, true, false, "")
			require.Equal(t, [2]int{tc.input, tc.output}, [2]int{log.InputTokens, log.OutputTokens})
		})
	}
}

func TestOpenAINativeExecutorIntervals(t *testing.T) {
	for _, tc := range []struct {
		name                        string
		enabled                     bool
		highInput, highOutput, want float64
	}{{"disabled", false, 6e-6, 30e-6, .603573}, {"long_context", true, 6e-6, 30e-6, 1.207146}, {"cheaper_high_volume", true, 1e-6, 5e-6, .603573}} {
		t.Run(tc.name, func(t *testing.T) {
			bs, resolver := newTokenCostTestEnv(t, PlatformAnthropic, []ChannelModelPricing{{
				Platform: PlatformAnthropic, Models: []string{"claude-sonnet-4"}, BillingMode: BillingModeToken,
				Intervals: []PricingInterval{
					{MinTokens: 0, MaxTokens: new(int(200000)), InputPrice: billingAmount(3e-6), OutputPrice: billingAmount(15e-6)},
					{MinTokens: 200000, InputPrice: billingAmount(tc.highInput), OutputPrice: billingAmount(tc.highOutput)},
				},
			}}, nil)
			svc := &GatewayService{billingService: bs, resolver: resolver}
			gid := int64(100)
			key := &APIKey{GroupID: &gid, Group: &Group{ID: gid, LongContextPricingEnabled: tc.enabled}}
			resolved := resolver.Resolve(context.Background(), PricingInput{Model: "claude-sonnet-4", GroupID: &gid, Group: key.Group})
			require.Equal(t, PricingSourceChannel, resolved.Source)
			require.Len(t, resolved.Intervals, 2)
			for _, compact := range []bool{false, true} {
				for _, advisor := range []bool{false, true} {
					entries := `{"type":"message","input_tokens":100000,"output_tokens":11}`
					want := .300165
					if compact {
						entries += `,{"type":"compaction","input_tokens":100001,"output_tokens":227}`
						want = tc.want
					}
					if advisor {
						entries += `,{"type":"advisor_message","model":"claude-sonnet-4","input_tokens":1,"output_tokens":1}`
						want += .000018
					}
					u := parseClaudeUsageFromResponseBody([]byte(`{"type":"message","stop_reason":"end_turn","usage":{"input_tokens":100000,"output_tokens":11,"iterations":[` + entries + `]}}`))
					logs := &openAIRecordUsageLogRepoStub{inserted: true}
					user := &openAIRecordUsageUserRepoStub{}
					open := newOpenAIRecordUsageServiceForTest(logs, user, &openAIRecordUsageSubRepoStub{}, nil)
					open.resolver, open.billingService = resolver, bs
					key.Group.RateMultiplier = 1
					result := &OpenAIForwardResult{RequestID: "interval", Model: "claude-sonnet-4", Usage: claudeUsageToOpenAIUsage(u)}
					require.NoError(t, open.RecordUsage(t.Context(), &OpenAIRecordUsageInput{APIKey: key, Account: &Account{ID: 3, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}, User: &User{ID: 2}, Result: result}))
					t.Logf("interval compact=%t advisor=%t debit=%.9f", compact, advisor, user.lastAmount)
					native := &RecordUsageInput{APIKey: key, User: &User{ID: 2}, Result: &ForwardResult{Usage: *u, Model: "claude-sonnet-4"}}
					require.InDelta(t, want, user.lastAmount, 1e-9)
					require.InDelta(t, svc.QuoteUsageCost(t.Context(), native), user.lastAmount, 1e-9)
				}
			}
		})
	}
}

func TestAntigravityUpstreamPlainSettlement(t *testing.T) {
	for _, model := range []string{"", "advisor-test", "unpriced-advisor"} {
		entries := `{"type":"message","input_tokens":285,"output_tokens":11}`
		if model != "" {
			entries += `,{"type":"advisor_message","model":"` + model + `","input_tokens":15,"output_tokens":9}`
		}
		usage := `{"input_tokens":285,"output_tokens":11,"iterations":[` + entries + `]}`
		for _, mode := range []string{"buffered", "stream"} {
			stream := mode != "buffered"
			t.Run(model+"/"+mode, func(t *testing.T) {
				body := `{"type":"message","stop_reason":"end_turn","usage":` + usage + `}`
				if stream {
					body = "data: " + `{"type":"message_start","message":{"usage":` + usage + `}}` + "\n\ndata: " + `{"type":"message_delta","usage":{"output_tokens":11}}` + "\n\ndata: " + `{"type":"message_stop"}` + "\n\n"
				}
				if mode == "stream_no_space" {
					body = strings.ReplaceAll(body, "data: ", "data:")
				}
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
				c.Request.Header.Set("Anthropic-Beta", "compact-2026-01-12")
				resp := &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}
				upstream := &httpUpstreamStub{resp: resp}
				svc := &AntigravityGatewayService{httpUpstream: upstream, settingService: &SettingService{}}
				result, err := svc.ForwardUpstream(context.Background(), c, &Account{ID: 701, Type: AccountTypeUpstream, Credentials: map[string]any{"base_url": "https://upstream.example", "api_key": "test-key"}}, []byte(fmt.Sprintf(`{"model":"claude-sonnet-4","stream":%t,"messages":[],"context_management":{"edits":[{"type":"compact_20260112"}]}}`, stream)))
				require.NoError(t, err)
				log := recordCompactionUsage(t, result.Usage, stream, false, "")
				t.Logf("plain input=%d output=%d total=%.9f", log.InputTokens, log.OutputTokens, log.TotalCost)
				require.Nil(t, result.Usage.IterationUsage)
				require.Equal(t, [2]int{285, 11}, [2]int{log.InputTokens, log.OutputTokens})
				require.InDelta(t, .00102, log.TotalCost, 1e-10)
			})
		}
	}
}

func TestCompactionTTLOverrideSettlement(t *testing.T) {
	for _, compact := range []bool{false, true} {
		entries := compactionMessage
		if compact {
			entries += "," + compactionFirst
		}
		u := parseClaudeUsageFromResponseBody([]byte(`{"type":"message","stop_reason":"end_turn","usage":{` + compactionMessageFields + `,"iterations":[` + entries + `]}}`))
		for _, target := range []string{"5m", "1h"} {
			log := recordCompactionUsage(t, *u, false, false, target)
			want := [2]int{4, 0}
			if target == "1h" {
				want = [2]int{0, 4}
			}
			if compact && target == "5m" {
				want = [2]int{6, 4}
			}
			if compact && target == "1h" {
				want = [2]int{0, 10}
			}
			t.Logf("ttl compact=%t target=%s buckets=%d/%d debit=%.9f", compact, target, log.CacheCreation5mTokens, log.CacheCreation1hTokens, log.ActualCost)
			require.Equal(t, want, [2]int{log.CacheCreation5mTokens, log.CacheCreation1hTokens})
		}
	}
}

func TestCompactionOverlapObservation(t *testing.T) {
	previousLimiter := compactionOverlapLogLimit
	compactionOverlapLogLimit = rate.NewLimiter(rate.Every(time.Minute), 1)
	t.Cleanup(func() { compactionOverlapLogLimit = previousLimiter })
	var messages bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&messages, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	u := parseClaudeUsageFromResponseBody([]byte(`{"type":"message","stop_reason":"end_turn","usage":{"input_tokens":150,"output_tokens":7,"iterations":[{"type":"compaction","input_tokens":50,"output_tokens":2},{"type":"message","input_tokens":100,"output_tokens":5}]}}`))
	for range 2 {
		r := (&ForwardResult{Usage: *u}).withCompactionUsage(t.Context())
		require.Equal(t, 200, r.Usage.InputTokens)
	}
	require.Equal(t, 1, strings.Count(messages.String(), "billing.compaction_usage_overlap"))
}
