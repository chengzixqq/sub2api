package service

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// MaxCapacitySummaryGroupIDs caps how many groups one
// GET /admin/groups/capacity-summary?group_id=…&group_ids=… request may name
// (after de-duplication). The App asks for one group at a time; the cap only
// bounds the cost of a hand-written query.
const MaxCapacitySummaryGroupIDs = 100

var (
	// ErrInvalidCapacityGroupID 表示 capacity-summary 的 group_id / group_ids 不是正整数。
	ErrInvalidCapacityGroupID = infraerrors.BadRequest(
		"INVALID_GROUP_ID",
		"group_id and group_ids must be positive integers",
	)
	// ErrTooManyCapacityGroupIDs 沿用批量读上限的 TOO_MANY_IDS（metadata.field / max）。
	ErrTooManyCapacityGroupIDs = tooManyBatchIDs("group_ids", "group IDs", MaxCapacitySummaryGroupIDs)
)

// GetGroupCapacityByIDs 只计算指定分组的容量，口径与 GetAllGroupCapacity 相同。
//
//   - ID 去重；有非正数或一个都没有时返回 ErrInvalidCapacityGroupID，超过上限返回 ErrTooManyCapacityGroupIDs。
//   - 任一分组不存在（含软删除）整次返回 ErrGroupNotFound，metadata.group_id 为请求顺序里第一个缺失的 ID。
//   - 停用分组直接略去（全量结果里本来就没有它），全部停用时返回非 nil 的空切片。
//   - 返回行按启用列表顺序（sort_order, id），是全量结果的子序列。
//
// 不做作用域授权：handler 先按 FilterGroupIDsByScope 拦掉未授权分组，
// 这里的 vendor 收窄与降级拒绝沿用 capacitiesForGroups。
func (s *GroupCapacityService) GetGroupCapacityByIDs(ctx context.Context, groupIDs []int64) ([]GroupCapacitySummary, error) {
	requested, err := normalizeCapacityGroupIDs(groupIDs)
	if err != nil {
		return nil, err
	}
	if err := s.requireExistingGroups(ctx, requested); err != nil {
		return nil, err
	}
	active, err := s.listActiveGroupIDs(ctx)
	if err != nil {
		return nil, err
	}
	selected := selectActiveGroupIDs(active, requested)
	if len(selected) == 0 {
		return []GroupCapacitySummary{}, nil
	}
	return s.capacitiesForGroups(ctx, selected)
}

func normalizeCapacityGroupIDs(groupIDs []int64) ([]int64, error) {
	if len(groupIDs) == 0 {
		return nil, ErrInvalidCapacityGroupID
	}
	seen := make(map[int64]struct{}, len(groupIDs))
	out := make([]int64, 0, len(groupIDs))
	for _, id := range groupIDs {
		if id <= 0 {
			return nil, ErrInvalidCapacityGroupID
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	if len(out) > MaxCapacitySummaryGroupIDs {
		return nil, ErrTooManyCapacityGroupIDs
	}
	return out, nil
}

// requireExistingGroups 校验分组都存在且未软删除。仓储实现了批量存在性读取时一条 SQL 查完，
// 否则逐个按轻量读取（不加载账号计数）回退。
func (s *GroupCapacityService) requireExistingGroups(ctx context.Context, groupIDs []int64) error {
	if reader, ok := s.groupRepo.(groupExistenceBatchReader); ok {
		exists, err := reader.ExistsByIDs(ctx, groupIDs)
		if err != nil {
			return fmt.Errorf("check groups exist: %w", err)
		}
		for _, id := range groupIDs {
			if !exists[id] {
				return capacityGroupNotFound(id)
			}
		}
		return nil
	}
	for _, id := range groupIDs {
		if _, err := s.groupRepo.GetByIDLite(ctx, id); err != nil {
			if errors.Is(err, ErrGroupNotFound) {
				return capacityGroupNotFound(id)
			}
			return fmt.Errorf("get group: %w", err)
		}
	}
	return nil
}

func capacityGroupNotFound(groupID int64) error {
	return ErrGroupNotFound.WithMetadata(map[string]string{"group_id": strconv.FormatInt(groupID, 10)})
}

// selectActiveGroupIDs 按启用列表的顺序取 requested 与 active 的交集。
func selectActiveGroupIDs(active, requested []int64) []int64 {
	wanted := make(map[int64]struct{}, len(requested))
	for _, id := range requested {
		wanted[id] = struct{}{}
	}
	out := make([]int64, 0, len(requested))
	for _, id := range active {
		if _, ok := wanted[id]; ok {
			out = append(out, id)
			delete(wanted, id)
		}
	}
	return out
}
