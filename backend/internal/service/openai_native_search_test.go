//go:build unit

package service

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestResponsesCountsOnlyCompletedNativeSearch(t *testing.T) {
	const body = `{"object":"response","status":"completed","output":[{"type":"web_search_call","id":"one"},{"type":"web_search_call","status":"failed"},{"type":"function_call","name":"web_search"}],"usage":{"input_tokens":5,"output_tokens":3}}`
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
	price := 10.0
	c.Set("api_key", &APIKey{Group: &Group{SearchPricePer1k: &price}})
	resp := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
	svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig()}
	result, err := svc.handleNonStreamingResponse(t.Context(), resp, c, rawChatCompletionsTestAccount(), "m", "m")
	require.NoError(t, err)
	require.Equal(t, 1, result.searchCount)
}

func TestNativeSearchSurchargePreservesModelAndMediaPrice(t *testing.T) {
	price := 10.0
	tier := "priority"
	for _, tc := range []struct {
		name     string
		img      int
		response string
	}{
		{"pricier response model", 0, openAIPriceyFixtureModel},
		{"cheaper response model", 0, openAICheapFixtureModel},
		{"image and free fast", 1, ""},
		{"unpriced model", 0, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
			userRepo := &openAIRecordUsageUserRepoStub{}
			svc := newOpenAIRecordUsageServiceForTest(usageRepo, userRepo, &openAIRecordUsageSubRepoStub{}, nil)
			model := openAICheapFixtureModel
			if tc.response == openAICheapFixtureModel {
				model = openAIPriceyFixtureModel
			}
			if tc.name == "unpriced model" {
				model = "batch20-no-such-model"
			}
			gid := int64(7)
			key := &APIKey{ID: 10, GroupID: &gid, Group: &Group{ID: gid, Platform: PlatformOpenAI, RateMultiplier: 1, SearchPricePer1k: &price, FreeOpenAIFast: true}}
			result := &OpenAIForwardResult{Model: model, UpstreamModel: model, UpstreamResponseModel: tc.response, Usage: OpenAIUsage{InputTokens: 20, OutputTokens: 10}, ImageCount: tc.img, ServiceTier: &tier}
			input := &OpenAIRecordUsageInput{Result: result, APIKey: key, User: &User{ID: 20}, Account: &Account{ID: 30, Platform: PlatformOpenAI}, ChannelUsageFields: ChannelUsageFields{OriginalModel: model, BillingModelSource: BillingModelSourceResponse}}
			result.RequestID = "base"
			require.NoError(t, svc.RecordUsage(t.Context(), input))
			base := usageRepo.lastLog.ActualCost
			result.NativeSearchCount = 1
			result.RequestID = "search"
			require.NoError(t, svc.RecordUsage(t.Context(), input))
			require.InDelta(t, base+0.01, usageRepo.lastLog.ActualCost, 1e-10)
			t.Logf("base=%.8f search=%.8f", base, usageRepo.lastLog.ActualCost)
		})
	}
}

func TestWSHTTPBridgeChargesSearchAtFrozenTurnPrice(t *testing.T) {
	const sse = "data: " + `{"type":"response.output_item.done","item":{"type":"web_search_call","call_id":"one"}}` + "\n\ndata: " + `{"type":"response.completed","response":{"id":"resp_s","status":"completed","output":[{"type":"web_search_call","id":"one"}],"usage":{"input_tokens":5,"output_tokens":3}}}` + "\n\n"
	price := 10.0
	payload := []byte(`{"type":"response.create","model":"gpt-5.4","stream":true,"input":"hi"}`)
	for _, enabled := range []bool{false, true} {
		up := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(sse))}}
		svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: up}
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest("GET", "/v1/responses", nil)
		if !enabled {
			c.Set("api_key", &APIKey{Group: &Group{SearchPricePer1k: &price}})
		}
		hooks := &OpenAIWSIngressHooks{NativeSearchPrice: func(turn int) *float64 {
			require.Equal(t, 2, turn)
			if enabled {
				return &price
			}
			return nil
		}}
		result, err := svc.proxyOpenAIWSHTTPBridgeTurn(t.Context(), c, rawChatCompletionsTestAccount(), "test", payload, len(payload), "gpt-5.4", "", "", "", "", 2, func([]byte) error { return nil }, hooks)
		require.NoError(t, err)
		want := 0
		if enabled {
			want = 1
		}
		require.Equal(t, want, result.NativeSearchCount)
		require.Equal(t, 5, result.Usage.InputTokens)
	}
}

func TestNativeSearchReconcilesDoneWithUnidentifiedTerminal(t *testing.T) {
	const done = "data: " + `{"type":"response.output_item.done","item":{"type":"web_search_call","id":"one"}}` + "\n\n"
	const anonymous = `{"type":"web_search_call"}`
	for _, tc := range []struct {
		items string
		want  int
	}{
		{anonymous, 1},
		{anonymous + "," + anonymous, 2},
		{`{"type":"web_search_call","id":"two"}`, 2},
		{`{"type":"web_search_call","id":"one"},` + anonymous, 2},
	} {
		final := "data: " + `{"type":"response.completed","response":{"output":[` + tc.items + `]}}` + "\n\n"
		require.Equal(t, tc.want, countOpenAINativeSearchSSE(done+final), tc.items)
	}
}

func TestNativeSearchTerminalAuthority(t *testing.T) {
	const search = `{"type":"web_search_call","id":"s","action":{"type":"search"}}`
	const prefix = "data: " + `{"type":"response.output_item.done","item":` + search + "}\n\n"
	for _, tc := range []struct {
		report string
		want   int
	}{
		{"0", 0}, {"3", 3}, {"1.5", 1}, {"-1", 1}, {`"4"`, 1}, {"null", 1},
	} {
		final := "data: " + `{"type":"response.completed","response":{"output":[],"tool_usage":{"web_search":{"num_requests":` + tc.report + "}}}}\n\n"
		require.Equal(t, tc.want, countOpenAINativeSearchSSE(prefix+final), tc.report)
		require.Zero(t, countOpenAINativeSearchSSE(final))
	}
	for _, tc := range []struct {
		action string
		want   int
	}{{"search", 1}, {"open_page", 0}, {"find_in_page", 0}, {"find", 0}, {"future_action", 1}} {
		item := strings.Replace(search, `"type":"search"`, `"type":"`+tc.action+`"`, 1)
		require.Equal(t, tc.want, countOpenAINativeSearchSSE("data: "+`{"type":"response.output_item.done","item":`+item+"}\n\n"))
	}
	added := "data: " + `{"type":"response.output_item.added","item":` + search + "}\n\n"
	reported := "data: " + `{"type":"response.done","response":{"tool_usage":{"web_search":{"num_requests":2}}}}` + "\n\n"
	require.Equal(t, 2, countOpenAINativeSearchSSE(added+reported))
	require.Zero(t, countOpenAINativeSearchSSE(added))
}

// An error envelope can carry completed items, but cannot discount them.
func TestNativeSearchAuthorityRequiresSuccessfulTerminal(t *testing.T) {
	const body = `{"type":"response.failed","response":{"output":[{"type":"web_search_call","action":{"type":"search"},"status":"completed"}],"tool_usage":{"web_search":{"num_requests":0}}}}`
	require.Equal(t, 1, countOpenAINativeSearchJSON([]byte(body)))
	_, calls := responseImageAndSearchCounts([]byte(body))
	require.Equal(t, 1, calls)
}

func TestPassthroughSearchIgnoresProtocolWordsInText(t *testing.T) {
	state := &openAIPassthroughTurnSearch{}
	body := []byte(`{"type":"response.completed","response":{"output":[{"type":"message","content":[{"type":"output_text","text":"web_search_call documentation"}]}],"usage":{"input_tokens":2,"output_tokens":1}}}`)
	var first *openAIPassthroughTurnSearch
	require.Nil(t, first.observe("response.completed", "resp", body))
	state.observe("response.completed", "resp", body)
	require.Empty(t, state.terminalPayload)
	require.Zero(t, state.take("response.completed", "resp"))
}
