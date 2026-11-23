//go:build unit

package handler

import (
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"net/http/httptest"
	"testing"
)

// The relay rows produce the same HTTP 400 error body and no upstream-error
// context. Classify those actual inputs using the Ops middleware's rules.
func TestBatchFiveRelayOpsClassification(t *testing.T) {
	for _, name := range []string{"compact_only", "dual_compact", "dual_clear", "compact_no_field", "context_empty", "neither"} {
		t.Run(name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest("POST", "/v1/messages", nil)
			phase, business, owner, source := classifyOpsErrorLog(c, "invalid_request_error", "context_management: Extra inputs are not permitted", "", 400)
			severity := classifyOpsSeverity("invalid_request_error", 400)
			t.Logf("RELAYOPS %s phase=%s business=%t owner=%s source=%s severity=%s", name, phase, business, owner, source, severity)
			require.Equal(t, "request", phase)
			require.False(t, business)
			require.Equal(t, "client", owner)
			require.Equal(t, "client_request", source)
			require.Equal(t, "P3", severity)
		})
	}
}
