//go:build unit

package service

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func readBedrockBillingFixture(t *testing.T, body string, events ...string) (*ClaudeUsage, string) {
	t.Helper()
	svc := newMinimalGatewayService()
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	resp := &http.Response{StatusCode: 200, Header: http.Header{}}
	account := newBedrockErrorTestAccount()
	if events == nil {
		resp.Body = io.NopCloser(strings.NewReader(body))
		u, err := svc.handleBedrockNonStreamingResponse(context.Background(), resp, c, account)
		require.NoError(t, err)
		return u, rec.Body.String()
	}
	var frames bytes.Buffer
	for _, event := range events {
		frames.Write(bedrockChunkFrame(event))
	}
	resp.Body = io.NopCloser(&frames)
	res, err := svc.handleBedrockStreamingResponse(context.Background(), resp, c, account, time.Now(), "m")
	require.NoError(t, err)
	return res.usage, rec.Body.String()
}

func TestBedrockBillingProductionBaseline(t *testing.T) {
	const usage = `{"input_tokens":120,"output_tokens":42,"cache_read_input_tokens":30}`
	const start = `{"type":"message_start","message":{"usage":{"input_tokens":120,"cache_read_input_tokens":30}}}`
	const delta = `{"type":"message_delta","usage":{"output_tokens":42}}`
	for _, counts := range [][2]int{{120, 42}, {150, 50}, {150, 40}, {0, 0}} {
		metrics := fmt.Sprintf(`"amazon-bedrock-invocationMetrics":{"inputTokenCount":%d,"outputTokenCount":%d}`, counts[0], counts[1])
		stopWire := fmt.Sprintf(`{"type":"message_stop","usage":{"input_tokens":%d,"output_tokens":%d}}`, counts[0], counts[1])
		for _, tc := range []struct {
			name, body, wire    string
			events              []string
			input, output, read int
		}{
			{"stream", "", "event: message_start\ndata: " + start + "\n\nevent: message_delta\ndata: " + delta + "\n\nevent: message_stop\ndata: " + stopWire + "\n\n", []string{start, delta, `{"type":"message_stop",` + metrics + `}`}, 120, 42, 30},
			{"object", `{"usage":` + usage + `,` + metrics + `}`, `{"usage":` + usage + `}`, nil, 120, 42, 30},
			{"null", `{"usage":null,` + metrics + `}`, `{"usage":null}`, nil, 0, 0, 0},
			{"absent", `{` + metrics + `}`, fmt.Sprintf(`{"usage":{"input_tokens":%d,"output_tokens":%d}}`, counts[0], counts[1]), nil, counts[0], counts[1], 0},
		} {
			t.Run(fmt.Sprintf("%s_%d_%d", tc.name, counts[0], counts[1]), func(t *testing.T) {
				u, wire := readBedrockBillingFixture(t, tc.body, tc.events...)
				require.Equal(t, []int{tc.input, tc.output, tc.read}, []int{u.InputTokens, u.OutputTokens, u.CacheReadInputTokens})
				require.Equal(t, tc.wire, wire)
				logs := &openAIRecordUsageLogRepoStub{inserted: true}
				ledger := &openAIRecordUsageBillingRepoStub{result: &UsageBillingApplyResult{Applied: true}}
				svc := newGatewayRecordUsageServiceWithBillingRepoForTest(logs, ledger, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{})
				prices, key := advisorTestService()
				svc.billingService, svc.resolver = prices.billingService, prices.resolver
				key.Group.RateMultiplier = 1
				err := svc.RecordUsage(context.Background(), &RecordUsageInput{Result: &ForwardResult{Model: "claude-sonnet-4", RequestID: "bedrock-baseline", Usage: *u}, APIKey: key, User: &User{ID: 22}, Account: &Account{ID: 33}})
				require.NoError(t, err)
				wantCost := float64(tc.input)*3e-6 + float64(tc.output)*15e-6 + float64(tc.read)*.3e-6
				require.InDelta(t, wantCost, logs.lastLog.TotalCost, 1e-10)
				require.InDelta(t, wantCost, ledger.lastCmd.BalanceCost, 1e-10)
			})
		}
	}
}

func TestBedrockCCBillingProductionBaseline(t *testing.T) {
	groupID := int64(1)
	channels := &ChannelService{}
	channels.cache.Store(&channelCache{loadedAt: time.Now(), channelByGroupID: map[int64]*Channel{
		groupID: {Status: StatusActive, FeaturesConfig: map[string]any{featureKeyBedrockCCCompat: true}},
	}})
	svc := &GatewayService{channelService: channels}
	for _, beta := range []string{"", "context-management-2025-06-27", "claude-code-20250219,oauth-2025-04-20,context-management-2025-06-27"} {
		t.Run(beta, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			c.Request.Header.Set("Anthropic-Beta", beta)
			body := []byte(`{"model":"claude-opus-4-6","messages":[],"context_management":{"edits":[{"type":"compact_20260112"}]}}`)
			out := svc.ApplyBedrockCCCompat(c, body, "claude-opus-4-6", &Account{Platform: PlatformAnthropic, Type: AccountTypeBedrock}, &groupID)
			require.Equal(t, `{"model":"claude-opus-4-6","messages":[],"max_tokens":81920,"anthropic_version":"bedrock-2023-05-31"}`, string(out))
		})
	}
}
