package repository

import (
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAdjustmentWhereBuildsAllFiltersWithHalfOpenTimeRange(t *testing.T) {
	start := time.Date(2026, 8, 4, 8, 30, 0, 0, time.UTC)
	end := time.Date(2026, 8, 9, 16, 0, 0, 0, time.UTC)
	where, args := adjustmentWhere(service.AdminUserAdjustmentFilter{
		Kind: service.AdjustmentKindBalance, Operation: service.AdjustmentOperationSet,
		Direction: "increase", Keyword: `alice_%`, Operator: `owner\ops`,
		StartTime: &start, EndTime: &end,
	})

	require.Contains(t, where, "kind = $1")
	require.Contains(t, where, "operation = $2")
	require.Contains(t, where, "delta > 0")
	require.Contains(t, where, "user_id::text ILIKE $3")
	require.Contains(t, where, "operator_name")
	require.Contains(t, where, "created_at >= $5")
	require.Contains(t, where, "created_at < $6")
	require.NotContains(t, where, "created_at <=")
	require.Equal(t, []any{
		service.AdjustmentKindBalance,
		service.AdjustmentOperationSet,
		`%alice\_\%%`,
		`%owner\\ops%`,
		start,
		end,
	}, args)
}

func TestAdjustmentWhereDirectionDecrease(t *testing.T) {
	where, args := adjustmentWhere(service.AdminUserAdjustmentFilter{Direction: "decrease"})
	require.Contains(t, where, "delta < 0")
	require.Empty(t, args)
}
