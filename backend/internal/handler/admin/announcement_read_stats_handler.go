package admin

import (
	"strconv"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/handler/dto"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// includeAnnouncementReadStats reports whether the optional
// include_read_stats query asks for per-announcement read counts.
func includeAnnouncementReadStats(c *gin.Context) bool {
	return parseBoolQueryWithDefault(c.Query("include_read_stats"), false)
}

// attachAnnouncementReadStats fills ReadStats on out (parallel to items).
func (h *AnnouncementHandler) attachAnnouncementReadStats(c *gin.Context, items []service.Announcement, out []dto.Announcement) bool {
	if len(items) == 0 {
		return true
	}
	stats, err := h.announcementService.ReadStatsFor(c.Request.Context(), items)
	if err != nil {
		response.ErrorFrom(c, err)
		return false
	}
	for i := range items {
		out[i].ReadStats = dto.AnnouncementReadStatsFromService(stats[items[i].ID])
	}
	return true
}

// parseAnnouncementReadStatusFilter reads read_state and eligible; an
// eligible value strconv.ParseBool rejects is INVALID_READ_STATUS_FILTER
// (read_state is validated by the service).
func parseAnnouncementReadStatusFilter(c *gin.Context, search string) (service.AnnouncementReadStatusFilter, error) {
	filter := service.AnnouncementReadStatusFilter{
		Search:    search,
		ReadState: c.Query("read_state"),
	}
	if raw := strings.TrimSpace(c.Query("eligible")); raw != "" {
		eligible, err := strconv.ParseBool(raw)
		if err != nil {
			return filter, service.ErrAnnouncementInvalidReadStatusFilter
		}
		filter.Eligible = &eligible
	}
	return filter, nil
}

// ReadSummary returns read counts for one announcement.
// GET /api/v1/admin/announcements/:id/read-summary
func (h *AnnouncementHandler) ReadSummary(c *gin.Context) {
	announcementID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || announcementID <= 0 {
		response.BadRequest(c, "Invalid announcement ID")
		return
	}

	summary, err := h.announcementService.GetReadSummary(c.Request.Context(), announcementID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, dto.AnnouncementReadSummaryFromService(summary))
}
