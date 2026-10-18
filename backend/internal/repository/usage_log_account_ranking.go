package repository

import (
	"context"
	"fmt"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
)

const (
	accountSpendingRankingDefaultLimit = 10
	accountSpendingRankingMaxLimit     = 50
)

// GetAccountSpendingRanking ranks accounts by actual charged cost within
// [Start, End). Costs follow accounts/today-stats: account_cost and
// standard_cost only count provider_cost_recorded rows. Soft-deleted accounts
// are excluded and the workspace usage scope is applied.
func (r *usageLogRepository) GetAccountSpendingRanking(ctx context.Context, params usagestats.AccountSpendingRankingParams) (result *usagestats.AccountSpendingRankingResponse, err error) {
	limit := params.Limit
	if limit <= 0 {
		limit = accountSpendingRankingDefaultLimit
	}
	if limit > accountSpendingRankingMaxLimit {
		limit = accountSpendingRankingMaxLimit
	}

	args := []any{params.Start, params.End, limit}
	conditions := []string{"ul.created_at >= $1", "ul.created_at < $2", "a.deleted_at IS NULL"}
	if params.GroupID > 0 {
		args = append(args, params.GroupID)
		conditions = append(conditions, fmt.Sprintf("ul.group_id = $%d", len(args)))
	}
	if params.Platform != "" {
		args = append(args, params.Platform)
		conditions = append(conditions, fmt.Sprintf("a.platform = $%d", len(args)))
	}
	conditions, args = appendUsageAccountScopeCondition(ctx, "ul.account_id", conditions, args)

	query := `
		WITH account_spend AS (
			SELECT
				ul.account_id,
				COUNT(*) AS requests,
				COALESCE(SUM(ul.input_tokens + ul.output_tokens + ul.cache_creation_tokens + ul.cache_read_tokens), 0) AS tokens,
				COALESCE(SUM(ul.actual_cost), 0) AS actual_cost,
				COALESCE(SUM(CASE WHEN ul.provider_cost_recorded THEN COALESCE(ul.account_stats_cost, ul.total_cost) * COALESCE(ul.account_rate_multiplier, 1) ELSE 0 END), 0) AS account_cost,
				COALESCE(SUM(ul.total_cost) FILTER (WHERE ul.provider_cost_recorded), 0) AS standard_cost
			FROM usage_logs ul
			JOIN accounts a ON a.id = ul.account_id
			` + buildWhere(conditions) + `
			GROUP BY ul.account_id
		),
		ranked AS (
			SELECT *, COALESCE(SUM(actual_cost) OVER (), 0) AS total_actual_cost
			FROM account_spend
			ORDER BY actual_cost DESC, tokens DESC, account_id ASC
			LIMIT $3
		)
		SELECT r.account_id, a.name, a.platform, a.status, r.requests, r.tokens,
			r.actual_cost, r.account_cost, r.standard_cost, r.total_actual_cost
		FROM ranked r
		JOIN accounts a ON a.id = r.account_id
		ORDER BY r.actual_cost DESC, r.tokens DESC, r.account_id ASC
	`

	rows, err := r.sql.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = closeErr
			result = nil
		}
	}()

	out := &usagestats.AccountSpendingRankingResponse{Ranking: make([]usagestats.AccountSpendingRankingItem, 0)}
	for rows.Next() {
		var item usagestats.AccountSpendingRankingItem
		if err = rows.Scan(&item.AccountID, &item.AccountName, &item.Platform, &item.Status, &item.Requests, &item.Tokens,
			&item.ActualCost, &item.AccountCost, &item.StandardCost, &out.TotalActualCost); err != nil {
			return nil, err
		}
		out.Ranking = append(out.Ranking, item)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
