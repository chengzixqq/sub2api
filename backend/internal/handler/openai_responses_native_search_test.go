//go:build unit

package handler

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOpenAIResponsesNativeSearchBilling(t *testing.T) {
	price, free := 10.0, 0.0
	const one = `{"type":"web_search_call","id":"ws_1","status":"completed","action":{"type":"search"}}`
	const page = `{"type":"web_search_call","action":{"type":"open_page"}}`
	const anon = `{"type":"web_search_call"}`
	const positive = `{"input_tokens":5,"output_tokens":3}`
	const zero = `{"input_tokens":0,"output_tokens":0}`
	for _, tc := range []struct {
		name, output, usage string
		price               *float64
		want                float64
	}{
		{"one reported", one, zero + `,"tool_usage":{"web_search":{"num_requests":1}}`, &price, 0.01},
		{"search and two opens", one + "," + page + "," + page, zero, &price, 0.01},
		{"two discounted to one", one + "," + strings.Replace(one, "ws_1", "ws_2", 1), zero + `,"tool_usage":{"web_search":{"num_requests":1}}`, &price, 0.01},
		{"three searches", one + "," + strings.Replace(one, "ws_1", "ws_2", 1) + "," + strings.Replace(one, "ws_1", "ws_3", 1), zero, &price, 0.03},
		{"open only", page, zero, &price, 0},
		{"missing action", anon, zero, &price, 0.01},
		{"reported zero", one, zero + `,"tool_usage":{"web_search":{"num_requests":0}}`, &price, 0},
		{"report without native item", "", zero + `,"tool_usage":{"web_search":{"num_requests":9}}`, &price, 0},
		{"only search", one, zero, &price, 0.01},
		{"with tokens", one, positive, &price, 0.01005750},
		{"different ids", one + "," + strings.Replace(one, "ws_1", "ws_2", 1), positive, &price, 0.02005750},
		{"repeated id", one + "," + one, positive, &price, 0.01005750},
		{"anonymous", anon + "," + anon, zero, &price, 0.02},
		{"excluded", `{"type":"function_call","name":"web_search"},{"type":"web_search_call","status":"failed"},{"type":"web_search_call","status":"in_progress"}`, positive, &price, 0.00005750},
		{"unset", one, positive, nil, 0.00005750},
		{"free", one, positive, &free, 0.00005750},
	} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", tc.name, stream), func(t *testing.T) {
				final := `{"id":"resp_test","object":"response","model":"gpt-5.4","status":"completed","output":[` + tc.output + `],"usage":` + tc.usage + "}"
				resp := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(final))}
				if stream {
					sse := ""
					if strings.HasPrefix(tc.output, one) {
						sse = "data: " + `{"type":"response.output_item.done","output_index":0,"item":` + one + "}\n\n"
					}
					resp = preContentSSEResponse(io.NopCloser(strings.NewReader(sse + "data: " + `{"type":"response.completed","response":` + final + "}\n\n")))
				}
				ledger := &chatCancelBillingRepo{}
				rec, up := serveOpenAIUpstreamBilling(t, t.Context(), "/v1/responses", fmt.Sprintf(`{"model":"gpt-5.4","input":"hello","stream":%t}`, stream), resp, ledger, false, "force_responses", tc.price)
				require.Equal(t, 200, rec.Code, rec.Body.String())
				require.Equal(t, 1, up.calls())
				require.Len(t, ledger.commands, 1)
				require.InDelta(t, tc.want, ledger.commands[0].BalanceCost, 1e-12)
				t.Logf("commands=%d cost=%.8f", len(ledger.commands), ledger.commands[0].BalanceCost)
			})
		}
	}
}

func TestResponsesBillsWrappedSearchReport(t *testing.T) {
	price := 10.0
	const body = `{"data":{"response":{"output":[{"type":"web_search_call","id":"a","status":"completed"}],"usage":{"input_tokens":10,"output_tokens":4},"tool_usage":{"web_search":{"num_requests":2}}}}}`
	resp := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}
	ledger := &chatCancelBillingRepo{}
	rec, _ := serveOpenAIUpstreamBilling(t, t.Context(), "/v1/responses", `{"model":"gpt-5.4","input":"hello","stream":false}`, resp, ledger, false, "force_responses", &price)
	require.Equal(t, 200, rec.Code)
	require.Len(t, ledger.commands, 1)
	require.InDelta(t, 0.02008500, ledger.commands[0].BalanceCost, 1e-12)
}
