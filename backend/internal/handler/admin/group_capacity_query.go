package admin

import (
	"context"
	"strconv"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// parseCapacityGroupIDs 读取 capacity-summary 的可选 group_id / group_ids。
//
//   - group_id 可重复，每个值必须是单个正整数（不接受逗号）；
//   - group_ids 可重复，每个值按逗号拆分，去首尾空白、跳过空段；
//   - 两者取并集，去重并保持首次出现的顺序。
//
// 一个 ID 都没拿到（不带参数、或只有空值 / 逗号）时返回 nil, nil，表示走全量，
// 与 parseAccountIDs 对空值的处理一致。非法值返回 ErrInvalidCapacityGroupID，
// 去重后超过上限返回 ErrTooManyCapacityGroupIDs。
func parseCapacityGroupIDs(c *gin.Context) ([]int64, error) {
	seen := make(map[int64]struct{})
	var ids []int64
	add := func(raw string) error {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || id <= 0 {
			return service.ErrInvalidCapacityGroupID
		}
		if _, ok := seen[id]; ok {
			return nil
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
		if len(ids) > service.MaxCapacitySummaryGroupIDs {
			return service.ErrTooManyCapacityGroupIDs
		}
		return nil
	}

	for _, value := range c.QueryArray("group_id") {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if err := add(value); err != nil {
			return nil, err
		}
	}
	for _, value := range c.QueryArray("group_ids") {
		for _, part := range strings.Split(value, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			if err := add(part); err != nil {
				return nil, err
			}
		}
	}
	return ids, nil
}

// requireCapacityGroupAccess 先判授权再读库：vendor 请求了任何未授权的分组都返回
// 404 RESOURCE_NOT_FOUND（不带 metadata），不区分分组是否存在；站长原样放行。
func (h *GroupHandler) requireCapacityGroupAccess(ctx context.Context, ids []int64) error {
	allowed, err := h.adminService.FilterGroupIDsByScope(ctx, ids)
	if err != nil {
		return err
	}
	if len(allowed) < len(ids) {
		return domain.ErrWorkspaceScopeViolation
	}
	return nil
}

// capacityForRequestedGroups 是带 group_id / group_ids 时的取数路径：授权 → 按 ID 计算。
func (h *GroupHandler) capacityForRequestedGroups(ctx context.Context, ids []int64) ([]service.GroupCapacitySummary, error) {
	if err := h.requireCapacityGroupAccess(ctx, ids); err != nil {
		return nil, err
	}
	return h.groupCapacityService.GetGroupCapacityByIDs(ctx, ids)
}
