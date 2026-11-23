//go:build unit

package handler

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestResponsesMissingUsageBilling(t *testing.T) {
	const text = `[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hi"}]}]`
	for _, tc := range []struct {
		name, body string
		status     int
		cost       float64
	}{
		{"delivered", `{"id":"resp_test","object":"response","model":"gpt-5.4","status":"completed","output":` + text + "}", 200, 0.00001750},
		{"compatible", `{"status":"completed","output":` + text + "}", 200, 0.00001750},
		{"empty output", `{"object":"response","status":"completed","output":[]}`, 200, 0},
		{"null", `{"object":"response","status":"completed","output":[],"usage":null}`, 502, 0},
		{"wrong type", `{"object":"response","output":[],"usage":"invalid"}`, 502, 0},
		{"in progress", `{"type":"response.in_progress","object":"response","output":[]}`, 502, 0},
		{"incomplete details", `{"object":"response","incomplete_details":{"reason":"max_output_tokens"},"output":` + text + "}", 502, 0},
		{"failed", `{"object":"response","status":"failed","output":[]}`, 502, 0},
		{"error", `{"object":"response","output":[],"error":{"message":"failed"}}`, 502, 0},
		{"non response", `{"result":"ok"}`, 502, 0},
		{"truncated", `{"object":"response","output":[]`, 502, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ledger := &chatCancelBillingRepo{}
			resp := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(tc.body))}
			rec, up := serveOpenAIUpstreamBilling(t, t.Context(), "/v1/responses", `{"model":"gpt-5.4","input":"hello","stream":false}`, resp, ledger, false, "force_responses", nil)
			require.Equal(t, tc.status, rec.Code)
			require.Equal(t, 1, up.calls())
			if tc.status == 502 {
				require.Empty(t, ledger.commands)
				return
			}
			require.False(t, gjson.Get(rec.Body.String(), "usage").Exists())
			require.Len(t, ledger.commands, 1)
			require.InDelta(t, tc.cost, ledger.commands[0].BalanceCost, 1e-12)
		})
	}
}
