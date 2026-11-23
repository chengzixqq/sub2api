//go:build unit

package service

import (
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestAdvisorPricingErrorWire(t *testing.T) {
	for _, path := range []string{"/v1/chat/completions", "/v1/responses"} {
		t.Run(path, func(t *testing.T) {
			upstream := &anthropicHTTPUpstreamRecorder{}
			svc := fallbackNativeGatewayForTest(t, upstream, nil)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest("POST", path, nil)
			body := []byte(`{"tools":[{"type":"advisor_20260301","model":"unknown-advisor"}]}`)
			var err error
			if path == "/v1/responses" {
				_, err = svc.ForwardAsResponses(t.Context(), c, nil, body, nil)
			} else {
				_, err = svc.ForwardAsChatCompletions(t.Context(), c, nil, body, nil)
			}
			require.ErrorIs(t, err, ErrAdvisorPricingUnavailable)
			require.Nil(t, upstream.lastReq)
			t.Logf("WIRE %s %d %q %q", t.Name(), rec.Code, rec.Header().Get("Content-Type"), rec.Body.String())
			require.Equal(t, 503, rec.Code)
			require.Equal(t, "application/json; charset=utf-8", rec.Header().Get("Content-Type"))
			require.Equal(t, `{"error":{"message":"Advisor model pricing is unavailable","type":"advisor_pricing_unavailable"},"type":"error"}`, rec.Body.String())
		})
	}
}

func TestNonClaudeErrorAfterPingWire(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
	c.Header("Content-Type", "text/event-stream")
	_, err := c.Writer.WriteString(": ping\n\n")
	require.NoError(t, err)
	c.Writer.Flush()
	c.Set("anthropic_filled_request_id", "req_fill")
	writeAnthropicError(c, 503, "advisor_pricing_unavailable", "missing <price>")
	t.Logf("WIRE %s %d %q %q", t.Name(), rec.Code, rec.Header().Get("Content-Type"), rec.Body.String())
	require.Equal(t, 200, rec.Code)
	require.Equal(t, "text/event-stream", rec.Header().Get("Content-Type"))
	require.Equal(t, ": ping\n\nevent: error\ndata: "+`{"error":{"message":"missing \u003cprice\u003e","type":"advisor_pricing_unavailable"},"type":"error"}`+"\n\n", rec.Body.String())
}
