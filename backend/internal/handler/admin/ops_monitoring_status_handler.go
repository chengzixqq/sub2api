package admin

import (
	"log/slog"
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// GetMonitoringStatus tells the config hard switch apart from the admin
// setting. It is deliberately not gated by RequireMonitoringEnabled: clients
// call it exactly when the other ops endpoints answer 404 OPS_DISABLED.
// GET /api/v1/admin/ops/monitoring-status
func (h *OpsHandler) GetMonitoringStatus(c *gin.Context) {
	if h.opsService == nil {
		response.Error(c, http.StatusServiceUnavailable, "Ops service not available")
		return
	}
	status, err := h.opsService.GetMonitoringStatus(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, status)
}

// UpdateMonitoringStatus writes only the switches present in the body and
// answers like GET. 409 OPS_CONFIG_DISABLED while ops.enabled=false.
// PUT /api/v1/admin/ops/monitoring-status
func (h *OpsHandler) UpdateMonitoringStatus(c *gin.Context) {
	if h.opsService == nil {
		response.Error(c, http.StatusServiceUnavailable, "Ops service not available")
		return
	}
	var req service.OpsMonitoringStatusUpdate
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request body")
		return
	}
	before, after, err := h.opsService.UpdateMonitoringStatus(c.Request.Context(), req)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if changed := diffOpsMonitoringStatus(before, after); len(changed) > 0 {
		subject, _ := middleware.GetAuthSubjectFromContext(c)
		role, _ := middleware.GetUserRoleFromContext(c)
		slog.Info("ops monitoring status updated",
			"audit", true,
			"user_id", subject.UserID,
			"role", role,
			"changed", changed,
		)
	}
	response.Success(c, after)
}

// diffOpsMonitoringStatus names the switches that actually changed, using the
// same field names as the settings audit log.
func diffOpsMonitoringStatus(before, after *service.OpsMonitoringStatus) []string {
	if before == nil || after == nil {
		return nil
	}
	changed := make([]string, 0, 2)
	if before.MonitoringEnabled != after.MonitoringEnabled {
		changed = append(changed, "ops_monitoring_enabled")
	}
	if before.RealtimeMonitoringEnabled != after.RealtimeMonitoringEnabled {
		changed = append(changed, "ops_realtime_monitoring_enabled")
	}
	return changed
}
