package admin

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// opsAlertEventResolver is the acting admin recorded on a manual resolution (the first admin for the Admin Key, the
// token owner for sat_ tokens); nil for other statuses or when no user is known.
func opsAlertEventResolver(c *gin.Context, status string) *int64 {
	if status != service.OpsAlertStatusManualResolved {
		return nil
	}
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok || subject.UserID <= 0 {
		return nil
	}
	userID := subject.UserID
	return &userID
}

// GetAlertEventCounts returns the number of firing alert events by severity; silenced firing events are included.
// GET /api/v1/admin/ops/alert-events/counts?platform=&group_id=
func (h *OpsHandler) GetAlertEventCounts(c *gin.Context) {
	if h.opsService == nil {
		response.Error(c, http.StatusServiceUnavailable, "Ops service not available")
		return
	}
	if err := h.opsService.RequireMonitoringEnabled(c.Request.Context()); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	var groupID *int64
	if v := strings.TrimSpace(c.Query("group_id")); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil || id <= 0 {
			response.BadRequest(c, "Invalid group_id")
			return
		}
		groupID = &id
	}

	counts, err := h.opsService.GetFiringAlertEventCounts(c.Request.Context(), c.Query("platform"), groupID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, counts)
}
