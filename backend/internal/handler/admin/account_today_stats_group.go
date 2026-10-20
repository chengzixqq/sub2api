package admin

import (
	"context"
	"slices"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// resolveTodayStatsGroupFilter 校验 today-stats/batch 的可选 group_id。
//
// 未传或传 null 返回 0（不按分组）。传了必须是正数，否则 400 INVALID_GROUP_FILTER；
// vendor 只能查已授权分组，未授权一律 404 RESOURCE_NOT_FOUND，不泄露分组是否存在。
// 返回 false 时已写好错误响应。
func (h *AccountHandler) resolveTodayStatsGroupFilter(c *gin.Context, raw *int64) (int64, bool) {
	if raw == nil {
		return 0, true
	}
	groupID := *raw
	if groupID <= 0 {
		response.ErrorFrom(c, service.ErrInvalidGroupFilter)
		return 0, false
	}
	allowed, err := h.adminService.FilterGroupIDsByScope(c.Request.Context(), []int64{groupID})
	if err != nil {
		response.ErrorFrom(c, err)
		return 0, false
	}
	if !slices.Contains(allowed, groupID) {
		response.ErrorFrom(c, domain.ErrWorkspaceScopeViolation)
		return 0, false
	}
	return groupID, true
}

// fetchTodayStatsBatch 按是否带分组选择取数路径：带分组只统计该分组的用量，
// 失败直接报错不回退；不带分组与原来逐字一致。
func (h *AccountHandler) fetchTodayStatsBatch(ctx context.Context, accountIDs []int64, groupID int64) (map[int64]*service.WindowStats, []int64, bool, error) {
	if groupID > 0 {
		return h.accountUsageService.GetTodayStatsBatchByGroup(ctx, accountIDs, groupID)
	}
	return h.accountUsageService.GetTodayStatsBatchDetailed(ctx, accountIDs)
}

// withTodayStatsGroup 带分组时在响应 data 里回显 group_id，App 据此确认过滤已生效
// （老服务端会静默忽略 group_id）；不带分组时原样返回，响应与原来一致。
func withTodayStatsGroup(payload gin.H, groupID int64) gin.H {
	if groupID > 0 {
		payload["group_id"] = groupID
	}
	return payload
}
