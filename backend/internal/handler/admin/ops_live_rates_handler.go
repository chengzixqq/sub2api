package admin

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// dashboardRatesCache absorbs several clients polling the home screen; the end
// time is bucketed to the same 5 seconds so they share an entry.
var dashboardRatesCache = newSnapshotCache(5 * time.Second)

const dashboardRatesBucket = 5 * time.Second

// GetLiveRates returns the recent-window RPM / TPM / error rate. Unlike the ops
// dashboard it does not require ops monitoring; error fields are null when
// monitoring is off.
// GET /api/v1/admin/dashboard/rates?window=1m|5m|15m&platform=&group_id=
func (h *OpsHandler) GetLiveRates(c *gin.Context) {
	if h.opsService == nil {
		response.Error(c, http.StatusServiceUnavailable, "Ops service not available")
		return
	}
	window, err := service.ParseOpsLiveRatesWindow(strings.TrimSpace(c.Query("window")))
	if err != nil {
		response.BadRequest(c, "Invalid window")
		return
	}
	query := service.OpsLiveRatesQuery{
		Window:   window,
		End:      time.Now().UTC().Truncate(dashboardRatesBucket),
		Platform: strings.TrimSpace(c.Query("platform")),
	}
	if v := strings.TrimSpace(c.Query("group_id")); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil || id <= 0 {
			response.BadRequest(c, "Invalid group_id")
			return
		}
		query.GroupID = &id
	}

	ctx := c.Request.Context()
	key := scopedUsageCacheKey(ctx, mustMarshalDashboardCacheKey(query))
	entry, hit, err := dashboardRatesCache.GetOrLoadContext(ctx, key, func(work context.Context) (any, error) {
		return h.opsService.GetLiveRates(work, query)
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	rates, err := snapshotPayloadAs[*service.OpsLiveRates](entry.Payload)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	c.Header("X-Snapshot-Cache", cacheStatusValue(hit))
	response.Success(c, rates)
}
