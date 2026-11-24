//go:build unit

package service

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// Usage fixtures follow the public xAI Responses captures: x_search_calls and the
// x_posts_fetched / x_users_fetched item counts as in big-AGI grok-4.3_x-search, and the
// web_search_calls + x_search_calls + code_interpreter_calls mix as in grok-4.3_web+x-search+code.
// document_search_calls is listed in the xAI ModelOutput schema (server_side_tool_usage_details).
const (
	grokUsageXSearchDetails   = `{"x_search_calls":3,"x_posts_fetched":13,"x_users_fetched":3}`
	grokUsageWebXCodeDetails  = `{"web_search_calls":2,"x_search_calls":1,"code_interpreter_calls":1,"x_posts_fetched":6}`
	grokUsageDocumentDetails  = `{"document_search_calls":5}`
	grokUsageUnrelatedDetails = `{"code_interpreter_calls":1,"file_search_calls":1,"mcp_calls":1,"x_posts_fetched":7,"x_users_fetched":2}`
	grokFailedXSearchItemJSON = `{"type":"custom_tool_call","id":"ctc_4","call_id":"xs_999","name":"x_thread_fetch","input":"{}","status":"failed"}`
)

func grokUsageJSON(details string) string {
	usage := `{"input_tokens":5,"output_tokens":2,"total_tokens":7`
	if details != "" {
		usage += `,"server_side_tool_usage_details":` + details
	}
	return usage + "}"
}

type grokUsageCase struct {
	name    string
	items   []string
	details string
	want    int
}

func grokUsageCases() []grokUsageCase {
	return []grokUsageCase{
		{"x_search_calls only, posts and users fetched not read", []string{grokXSearchItemJSON}, grokUsageXSearchDetails, 3},
		{"web_search_calls, x_search_calls summed, code_interpreter_calls not read", []string{grokXSearchItemJSON}, grokUsageWebXCodeDetails, 3},
		{"document_search_calls", []string{grokXSearchItemJSON}, grokUsageDocumentDetails, 5},
		{"sum saturates", []string{grokXSearchItemJSON}, fmt.Sprintf(`{"x_search_calls":%d,"web_search_calls":2}`, math.MaxInt), math.MaxInt},
		{"oversized counter saturates", []string{grokXSearchItemJSON}, `{"document_search_calls":18446744073709551616}`, math.MaxInt},
		{"negative class does not cancel a successful class", []string{grokXSearchItemJSON}, `{"x_search_calls":-9,"web_search_calls":2}`, 2},
		{"all three fields", []string{grokXSearchItemJSON}, `{"web_search_calls":1,"x_search_calls":2,"document_search_calls":3}`, 6},
		{"only unrelated classes: usage wins with zero", []string{grokXSearchItemJSON, grokASearchItemJSON}, grokUsageUnrelatedDetails, 0},
		{"usage absent: items counted", []string{grokXSearchItemJSON, grokASearchItemJSON, grokClientToolJSON}, "", 2},
		{"usage lower than items: failed call not billed", []string{grokXSearchItemJSON, grokFailedXSearchItemJSON}, `{"x_search_calls":1}`, 1},
		{"usage zero, items present", []string{grokXSearchItemJSON}, `{"x_search_calls":0}`, 0},
	}
}

func TestGrokSearchCountFromUsageJSON(t *testing.T) {
	for _, tc := range grokUsageCases() {
		t.Run(tc.name, func(t *testing.T) {
			output := "[" + strings.Join(tc.items, ",") + "]"
			top := `{"output":` + output + `,"usage":` + grokUsageJSON(tc.details) + "}"
			nested := `{"response":{"output":` + output + `,"usage":` + grokUsageJSON(tc.details) + "}}"
			require.Equal(t, tc.want, countGrokNativeSearchCallsFromJSONBytes([]byte(top)))
			require.Equal(t, tc.want, countGrokNativeSearchCallsFromJSONBytes([]byte(nested)))
		})
	}
}

func TestGrokSearchCountFromUsageSSEBody(t *testing.T) {
	for _, tc := range grokUsageCases() {
		t.Run(tc.name, func(t *testing.T) {
			sse := withGrokUsage(grokCustomToolCallStream(tc.items, "[]"), tc.details)
			require.Equal(t, tc.want, countGrokNativeSearchCallsFromSSEBody(sse))
		})
	}
}

func TestGrokSearchCountWithTerminalUsage(t *testing.T) {
	usage := `"usage":` + grokUsageJSON(`{"x_search_calls":4}`)
	for _, eventType := range []string{"response.completed", "response.incomplete", "response.done"} {
		data := []byte(fmt.Sprintf(`{"type":%q,"response":{"output":[],%s}}`, eventType, usage))
		counter := grokSearchStreamCounter{count: 1}
		counter.observe(data, eventType)
		require.Equal(t, 4, counter.count, eventType)
	}
	for _, eventType := range []string{"response.created", "response.in_progress", "response.output_item.done"} {
		data := []byte(fmt.Sprintf(`{"type":%q,"response":{%s}}`, eventType, usage))
		counter := grokSearchStreamCounter{count: 1}
		counter.observe(data, eventType)
		require.Equal(t, 1, counter.count, eventType)
	}
	counter := grokSearchStreamCounter{count: 1}
	counter.observe([]byte(`{"type":"response.completed","response":{"usage":{"input_tokens":1}}}`), "")
	require.Equal(t, 1, counter.count)
}

func withGrokUsage(sse, details string) string {
	return strings.Replace(sse, `"usage":{"input_tokens":5,"output_tokens":2,"total_tokens":7}`, `"usage":`+grokUsageJSON(details), 1)
}

func TestGrokSearchCountFromUsageAcrossEndpoints(t *testing.T) {
	account := healthyGrokOAuthGatewayTestAccount(4243, "access-token")
	for _, tc := range grokUsageCases() {
		sse := withGrokUsage(grokCustomToolCallStream(tc.items, "[]"), tc.details)
		for _, path := range []string{"/v1/messages", "/v1/chat/completions"} {
			for _, streaming := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s %s stream=%v", tc.name, path, streaming), func(t *testing.T) {
					got := forwardGrokCustomToolCallStream(t, path, streaming, account, sse)
					require.Equal(t, tc.want, got.searchCount)
				})
			}
		}
		t.Run(fmt.Sprintf("%s /v1/responses stream", tc.name), func(t *testing.T) {
			require.Equal(t, tc.want, forwardGrokResponsesSearchCount(t, true, sse))
		})
		t.Run(fmt.Sprintf("%s /v1/responses json", tc.name), func(t *testing.T) {
			body := `{"id":"resp_1","object":"response","model":"grok-4.7","status":"completed","output":[` +
				strings.Join(tc.items, ",") + `],"usage":` + grokUsageJSON(tc.details) + "}"
			require.Equal(t, tc.want, forwardGrokResponsesSearchCount(t, false, body))
		})
	}
}

func forwardGrokResponsesSearchCount(t *testing.T, streaming bool, upstreamBody string) int {
	t.Helper()
	gin.SetMode(gin.TestMode)
	body := []byte(fmt.Sprintf(`{"model":"grok","input":"search","stream":%t}`, streaming))
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	account := healthyGrokOAuthGatewayTestAccount(9903, "access-token")
	repo := &mockAccountRepoForPlatform{accountsByID: map[int64]*Account{account.ID: account}}
	contentType := "application/json"
	if streaming {
		contentType = "text/event-stream"
	}
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{contentType}},
		Body:       io.NopCloser(strings.NewReader(upstreamBody)),
	}}
	svc := &OpenAIGatewayService{
		httpUpstream:      upstream,
		grokTokenProvider: NewGrokTokenProvider(repo, nil),
		accountRepo:       repo,
	}
	result, err := svc.forwardGrokResponses(context.Background(), c, account, body, "grok", streaming, time.Now())
	require.NoError(t, err)
	require.NotNil(t, result)
	return result.SearchCount
}

func TestGrokRawChatSearchUsage(t *testing.T) {
	account := grokAuditAPIKeyAccount(1)
	for _, streaming := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", streaming), func(t *testing.T) {
			usage := `{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7,"server_side_tool_usage_details":{"web_search_calls":2,"x_search_calls":1}}`
			body := `{"object":"chat.completion","model":"grok-4.7","choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":` + usage + `}`
			if streaming {
				body = `data: {"object":"chat.completion.chunk","choices":[{"delta":{"content":"ok"}}]}` + "\n\n" +
					`data: {"object":"chat.completion.chunk","choices":[],"usage":` + usage + `}` + "\n\ndata: [DONE]\n\n"
			}
			result := forwardGrokCustomToolCallStream(t, "/v1/chat/completions", streaming, account, body)
			require.Equal(t, 3, result.searchCount)
		})
	}
}

func TestGrokBufferedSearchReportKeepsProgressiveTokensAndRebuiltOutput(t *testing.T) {
	const initial = `"response":{"id":"resp_1","model":"grok-4.7","usage":{"input_tokens":8,"output_tokens":3,"input_tokens_details":{"image_tokens":2}}}`
	terminal := grokSSEFrame("response.completed", `"response":{"id":"resp_1","status":"completed","output":[],"usage":{"input_tokens":0,"output_tokens":0,"server_side_tool_usage_details":{"x_search_calls":0}}}`)
	stream := grokSSEFrame("response.created", initial) +
		grokSSEFrame("response.output_item.done", `"output_index":0,"item":`+grokXSearchItemJSON) +
		grokSSEFrame("response.output_text.delta", `"output_index":1,"delta":"found it"`) + terminal
	for _, path := range []string{"/v1/messages", "/v1/chat/completions"} {
		for _, finalBlankLine := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/final_blank_line=%t", path, finalBlankLine), func(t *testing.T) {
				data := stream
				if !finalBlankLine {
					data = strings.TrimRight(data, "\n")
				}
				resp := bufferedHostedItemsSSEResponse(data)
				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				c.Request = httptest.NewRequest(http.MethodPost, path, nil)
				svc := &OpenAIGatewayService{}
				handle := svc.handleAnthropicBufferedStreamingResponse
				if path == "/v1/chat/completions" {
					handle = svc.handleChatBufferedStreamingResponse
				}
				result, err := handle(resp, c, grokAuditAPIKeyAccount(1), "grok-4.7", "grok-4.7", "grok-4.7", time.Now())
				require.NoError(t, err)
				require.Contains(t, rec.Body.String(), "found it")
				require.Equal(t, 8, result.Usage.InputTokens)
				require.Equal(t, 3, result.Usage.OutputTokens)
				require.Equal(t, 2, result.Usage.ImageInputTokens)
				require.Zero(t, result.SearchCount)
				require.Zero(t, result.NativeSearchCount)
			})
		}
	}
}

func TestGrokResponsesFailedTerminalRetainsReportedSearchUsage(t *testing.T) {
	const terminal = `data: {"type":"response.failed","response":{"status":"failed","error":{"code":"server_error","message":"An error occurred while processing your request."},"usage":{"input_tokens":5,"server_side_tool_usage_details":{"x_search_calls":2}}}}` + "\n\n"
	for _, streaming := range []bool{true, false} {
		t.Run(fmt.Sprintf("stream=%t", streaming), func(t *testing.T) {
			body := []byte(fmt.Sprintf(`{"model":"grok","input":"search","stream":%t}`, streaming))
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
			account := healthyGrokOAuthGatewayTestAccount(1, "access-token")
			svc := grokAuditOAuthService(&httpUpstreamRecorder{resp: bufferedHostedItemsSSEResponse(terminal)}, account)
			result, err := svc.forwardGrokResponses(t.Context(), c, account, body, "grok", streaming, time.Now())
			var failover *UpstreamFailoverError
			require.ErrorAs(t, err, &failover)
			require.NotNil(t, result)
			require.Equal(t, 2, result.SearchCount)
			require.Zero(t, result.NativeSearchCount)
			require.Equal(t, 5, result.Usage.InputTokens)
		})
	}
}
