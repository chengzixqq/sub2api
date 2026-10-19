package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// usageLogEffectiveRequestTypeExpr puts every usage row into exactly one
// request type. Rows written before request_type existed (request_type = 0)
// fall back to openai_ws_mode, then stream, the same way
// buildRequestTypeFilterConditionWithAlias matches them; keep the two in sync
// so each bucket equals usage/stats?request_type=<type>.
var usageLogEffectiveRequestTypeExpr = fmt.Sprintf(
	"CASE WHEN request_type IN (%d, %d, %d, %d, %d) THEN request_type WHEN openai_ws_mode THEN %d WHEN stream THEN %d ELSE %d END",
	int16(service.RequestTypeSync), int16(service.RequestTypeStream), int16(service.RequestTypeWSV2),
	int16(service.RequestTypeCyberBlocked), int16(service.RequestTypeLive),
	int16(service.RequestTypeWSV2), int16(service.RequestTypeStream), int16(service.RequestTypeSync),
)

// usageLogRequestTypeStatsQuery builds the grouped query around a WHERE clause
// from usageRankingWhere. The bucket alias must not be request_type: GROUP BY
// resolves an ambiguous name to the input column, which would split by the
// raw column and leave legacy rows in "unknown".
func usageLogRequestTypeStatsQuery(where string) string {
	return fmt.Sprintf(`
		SELECT
			%s AS effective_request_type,
			COUNT(*) AS requests,
			COALESCE(SUM(input_tokens + output_tokens + cache_creation_tokens + cache_read_tokens), 0) AS total_tokens,
			COALESCE(SUM(total_cost) FILTER (WHERE provider_cost_recorded), 0) AS cost,
			COALESCE(SUM(actual_cost), 0) AS actual_cost,
			COALESCE(SUM(CASE WHEN provider_cost_recorded THEN COALESCE(account_stats_cost, total_cost) * COALESCE(account_rate_multiplier, 1) ELSE 0 END), 0) AS account_cost
		FROM usage_logs
		%s
		GROUP BY effective_request_type
		ORDER BY requests DESC, effective_request_type ASC
	`, usageLogEffectiveRequestTypeExpr, where)
}

// GetRequestTypeStatsWithUsageFilters totals usage in [startTime, endTime) per
// effective request type, ordered by request count. Filters and the account
// whitelist match usage/stats (usageRankingWhere); types without usage are
// omitted and no usage yields an empty, non-nil slice.
func (r *usageLogRepository) GetRequestTypeStatsWithUsageFilters(ctx context.Context, startTime, endTime time.Time, filters UsageLogFilters) (results []usagestats.RequestTypeStat, err error) {
	where, args := usageRankingWhere(ctx, filters, []any{startTime, endTime})
	rows, err := r.sql.QueryContext(ctx, usageLogRequestTypeStatsQuery(where), args...)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = closeErr
			results = nil
		}
	}()

	results = make([]usagestats.RequestTypeStat, 0)
	for rows.Next() {
		var requestType int16
		var row usagestats.RequestTypeStat
		if err = rows.Scan(&requestType, &row.Requests, &row.TotalTokens, &row.Cost, &row.ActualCost, &row.AccountCost); err != nil {
			return nil, err
		}
		row.RequestType = service.RequestTypeFromInt16(requestType).String()
		results = append(results, row)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return results, nil
}
