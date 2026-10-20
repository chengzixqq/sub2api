package admin

import (
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// GetRequestAttempts merges one request's upstream retry chain: failed tries
// from ops_error_logs.upstream_errors plus the billed try from usage_logs.
// GET /api/v1/admin/ops/requests/:request_id/attempts?around=<RFC3339>
// request_id may be the ops request_id, the client_request_id, or a usage
// request_id ("client:<id>" / "probe:<id>"); around narrows both lookups to
// ±1h. An id containing "/" cannot be a path segment (gin matches the decoded
// path), so callers use the server client_request_id for those requests.
func (h *OpsHandler) GetRequestAttempts(c *gin.Context) {
	if h.opsService == nil {
		response.Error(c, http.StatusServiceUnavailable, "Ops service not available")
		return
	}
	if err := h.opsService.RequireMonitoringEnabled(c.Request.Context()); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	around, err := service.ParseOpsRequestAttemptsAround(c.Query("around"))
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	chain, err := h.opsService.GetRequestAttempts(c.Request.Context(), c.Param("request_id"), around)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, chain)
}
