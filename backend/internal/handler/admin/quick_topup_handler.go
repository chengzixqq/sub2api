package admin

import (
	"strconv"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// QuickTopup feeds the user list's quick top-up avatar strip: recently
// adjusted users and users below their low-balance threshold.
// GET /api/v1/admin/users/quick-topup?recent_limit=8&low_limit=8&recent_days=30
func (h *UserAdjustmentHandler) QuickTopup(c *gin.Context) {
	query := service.QuickTopupQuery{RecentLimit: 8, LowLimit: 8, RecentDays: 30}
	for name, target := range map[string]*int{"recent_limit": &query.RecentLimit, "low_limit": &query.LowLimit, "recent_days": &query.RecentDays} {
		raw := strings.TrimSpace(c.Query(name))
		if raw == "" {
			continue
		}
		v, err := strconv.Atoi(raw)
		if err != nil {
			response.BadRequest(c, "Invalid "+name)
			return
		}
		*target = v
	}
	result, err := h.service.QuickTopup(c.Request.Context(), query)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}
