package admin

import (
	"context"
	"strconv"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

var accountStatusSummaryCache = newSnapshotCache(10 * time.Second)

// SetAccountStatusSummaryService attaches the ops-independent status summary.
func (h *AccountHandler) SetAccountStatusSummaryService(summary *service.AccountStatusSummaryService) {
	if h != nil {
		h.statusSummary = summary
	}
}

// GetStatusSummary counts accounts by schedule state for the whole site, each
// platform and each group, and judges group shortage. It works whether or not
// ops monitoring is enabled.
// GET /api/v1/admin/accounts/status-summary
func (h *AccountHandler) GetStatusSummary(c *gin.Context) {
	query := service.AccountStatusSummaryQuery{Platform: strings.TrimSpace(c.Query("platform"))}
	if raw := strings.TrimSpace(c.Query("group_id")); raw != "" {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || id <= 0 {
			response.ErrorFrom(c, infraerrors.BadRequest("INVALID_GROUP_FILTER", "group_id must be a positive integer"))
			return
		}
		query.GroupID = &id
	}
	if raw := strings.TrimSpace(c.Query("include_accounts")); raw != "" {
		include, err := strconv.ParseBool(raw)
		if err != nil {
			response.BadRequest(c, "include_accounts must be a boolean")
			return
		}
		query.IncludeAccounts = include
	}
	if raw := strings.TrimSpace(c.Query("default_min_ratio")); raw != "" {
		ratio, err := service.ParseGroupShortageMinRatio(raw)
		if err != nil {
			response.BadRequest(c, err.Error())
			return
		}
		query.DefaultMinRatio = &ratio
	}
	if h.statusSummary == nil {
		response.InternalError(c, "account status summary is not configured")
		return
	}

	ctx := c.Request.Context()
	key := scopedUsageCacheKey(ctx, mustMarshalDashboardCacheKey(query))
	entry, hit, err := accountStatusSummaryCache.GetOrLoadContext(ctx, key, func(work context.Context) (any, error) {
		return h.statusSummary.Summarize(work, query)
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	summary, err := snapshotPayloadAs[*service.AccountStatusSummary](entry.Payload)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	c.Header("X-Snapshot-Cache", cacheStatusValue(hit))
	response.Success(c, summary)
}
