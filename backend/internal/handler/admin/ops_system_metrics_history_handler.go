package admin

import (
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// GetSystemMetricsHistory returns recent CPU / memory / runtime snapshots,
// averaged into at most max_points buckets.
// GET /api/v1/admin/ops/system-metrics/history?time_range=1h&max_points=60
// (or start_time + end_time, RFC3339)
func (h *OpsHandler) GetSystemMetricsHistory(c *gin.Context) {
	if h.opsService == nil {
		response.Error(c, http.StatusServiceUnavailable, "Ops service not available")
		return
	}
	if err := h.opsService.RequireMonitoringEnabled(c.Request.Context()); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	startTime, endTime, err := parseOpsTimeRange(c, "1h")
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	maxPoints, err := service.ParseOpsSystemMetricsMaxPoints(c.Query("max_points"))
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	history, err := h.opsService.GetSystemMetricsHistory(c.Request.Context(), startTime, endTime, maxPoints)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, history)
}
