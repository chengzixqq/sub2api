//go:build unit

package handler

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestClaudeErrorWireBridgeRules(t *testing.T) {
	const failed = `data: {"type":"response.failed","response":{"status":"failed","error":{"code":"invalid_request_error","message":"matching rule"}}}` + "\n\n"
	const started = preContentHandlerCreated + `data: {"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"hello"}` + "\n\n"
	for _, tc := range []struct {
		name, events, keyword, msg string
		stream, sse                bool
	}{
		{"exhausted", preContentRateLimitFailed, "rate_limit_exceeded", "Rate limit reached for gpt-5.4", true, false},
		{"buffered", failed, "invalid_request_error", "matching rule", false, false},
		{"before_output", failed, "invalid_request_error", "matching rule", true, false},
		{"held_output", preContentHandlerCreated + failed, "invalid_request_error", "matching rule", true, true},
		{"after_output", started + failed, "invalid_request_error", "matching rule", true, true},
	} {
		for _, fill := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/fill=%t", tc.name, fill), func(t *testing.T) {
				id := ""
				if fill {
					id = "req_011C123456789ABCDEFGHJKL"
				}
				rec, upstream := serveMessagesScenario(t, preContentFailoverScenario{
					accounts: preContentAccounts(1), passthrough: newFailedFrameCodeRule(tc.keyword), filledRequestID: id,
					respond: func(int) (*http.Response, error) {
						return preContentSSEResponse(io.NopCloser(strings.NewReader(tc.events))), nil
					},
					requestBody: fmt.Sprintf(`{"model":"gpt-5.4","max_tokens":32,"messages":[{"role":"user","content":"hello"}],"stream":%t}`, tc.stream),
				})
				require.Equal(t, 1, upstream.calls())
				body := rec.Body.String()
				want := `{"type":"error","error":{"type":"upstream_error","message":"` + tc.msg + `"}`
				if fill {
					want += `,"request_id":"` + id + `"`
				}
				want += "}"
				payload := body
				if tc.sse {
					i := strings.LastIndex(body, "data: ")
					require.NotEqual(t, -1, i, body)
					payload = strings.TrimSpace(body[i+6:])
				}
				t.Logf("RULE %s status=%d type=%s msg=%q", t.Name(), rec.Code, gjson.Get(payload, "error.type").String(), gjson.Get(payload, "error.message").String())
				if tc.sse {
					require.Equal(t, 200, rec.Code)
					require.True(t, strings.HasSuffix(body, "event: error\ndata: "+want+"\n\n"), body)
					require.Equal(t, 1, strings.Count(body, "event: error"))
				} else {
					require.Equal(t, 418, rec.Code)
					require.Equal(t, "application/json", rec.Header().Get("Content-Type"))
					require.Equal(t, want, body)
				}
			})
		}
	}
}
