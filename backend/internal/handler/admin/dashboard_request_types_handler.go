package admin

import (
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/gin-gonic/gin"
)

// GetRequestTypeStats totals usage per request type (sync / stream / ws_v2 /
// cyber / live), ordered by request count; types without usage are omitted.
// GET /api/v1/admin/dashboard/request-types
// Query params: start_time / end_time or start_date / end_date + timezone
// (default: the last 7 days plus today), and the dashboard filters except
// request_type and stream, which are rejected because the response is already
// split by request type.
func (h *DashboardHandler) GetRequestTypeStats(c *gin.Context) {
	for _, name := range []string{"request_type", "stream"} {
		if strings.TrimSpace(c.Query(name)) != "" {
			response.BadRequest(c, invalidDashboardParam(name, "the result is already split by request type").Error())
			return
		}
	}
	startTime, endTime := parseTimeRange(c)
	if c.IsAborted() {
		return
	}
	filters, err := parseDashboardSnapshotV2Filters(c)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	stats, hit, err := h.getRequestTypeStatsCached(queryContext(c), startTime, endTime, filters.usageFilters())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	c.Header("X-Snapshot-Cache", cacheStatusValue(hit))
	response.Success(c, gin.H{
		"request_types": stats,
		"start_date":    startTime.Format("2006-01-02"),
		"end_date":      endTime.Add(-time.Nanosecond).Format("2006-01-02"),
		"query":         dashboardQueryMetadata(c),
	})
}
