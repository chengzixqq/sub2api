package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
)

const userSpendingRankingDefaultLimit = 12

// userSpendingRankingOrder maps an accepted sort_by to its ORDER BY clause.
// Only these constant strings ever reach the SQL text.
var userSpendingRankingOrder = map[string]string{
	usagestats.UserRankingSortActualCost: "actual_cost DESC, tokens DESC, user_id ASC",
	usagestats.UserRankingSortRequests:   "requests DESC, actual_cost DESC, user_id ASC",
	usagestats.UserRankingSortTokens:     "tokens DESC, actual_cost DESC, user_id ASC",
}

func userSpendingRankingOrderBy(sortBy string) string {
	if order, ok := userSpendingRankingOrder[sortBy]; ok {
		return order
	}
	return userSpendingRankingOrder[usagestats.UserRankingSortActualCost]
}

// GetUserSpendingRankingSorted ranks users with usage in [startTime, endTime)
// by actual charge, request count or tokens; unknown sortBy values use the
// actual_cost order. cost and account_cost only count provider_cost_recorded
// rows, matching usage/stats and user-breakdown. The totals and total_users are
// window aggregates over every ranked user, evaluated before LIMIT.
func (r *usageLogRepository) GetUserSpendingRankingSorted(ctx context.Context, startTime, endTime time.Time, limit int, sortBy string, filters UsageLogFilters) (result *UserSpendingRankingResponse, err error) {
	if limit <= 0 {
		limit = userSpendingRankingDefaultLimit
	}
	orderBy := userSpendingRankingOrderBy(sortBy)

	args := []any{startTime, endTime, limit}
	where, args := usageRankingWhere(ctx, filters, args)
	// The ranked CTE and the outer SELECT share orderBy so that LIMIT keeps
	// exactly the users that are finally listed, in the same order.
	query := fmt.Sprintf(`
		WITH user_spend AS (
			SELECT
				user_id,
				COALESCE(SUM(actual_cost), 0) as actual_cost,
				COALESCE(SUM(total_cost) FILTER (WHERE provider_cost_recorded), 0) as cost,
				COALESCE(SUM(CASE WHEN provider_cost_recorded THEN COALESCE(account_stats_cost, total_cost) * COALESCE(account_rate_multiplier, 1) ELSE 0 END), 0) as account_cost,
				COUNT(*) as requests,
				COALESCE(SUM(input_tokens + output_tokens + cache_creation_tokens + cache_read_tokens), 0) as tokens
			FROM usage_logs %s
			GROUP BY user_id
		),
		ranked AS (
			SELECT
				user_id,
				actual_cost,
				cost,
				account_cost,
				requests,
				tokens,
				COUNT(*) OVER () as total_users,
				COALESCE(SUM(actual_cost) OVER (), 0) as total_actual_cost,
				COALESCE(SUM(cost) OVER (), 0) as total_cost,
				COALESCE(SUM(account_cost) OVER (), 0) as total_account_cost,
				COALESCE(SUM(requests) OVER (), 0) as total_requests,
				COALESCE(SUM(tokens) OVER (), 0) as total_tokens
			FROM user_spend
			ORDER BY %s
			LIMIT $3
		)
		SELECT
			r.user_id,
			COALESCE(us.email, '') as email,
			COALESCE(us.username, '') as username,
			r.actual_cost,
			r.cost,
			r.account_cost,
			r.requests,
			r.tokens,
			r.total_users,
			r.total_actual_cost,
			r.total_cost,
			r.total_account_cost,
			r.total_requests,
			r.total_tokens
		FROM ranked r
		LEFT JOIN users us ON r.user_id = us.id
		ORDER BY %s
	`, where, orderBy, orderBy)

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

	out := &UserSpendingRankingResponse{Ranking: make([]UserSpendingRankingItem, 0)}
	for rows.Next() {
		var row UserSpendingRankingItem
		if err = rows.Scan(&row.UserID, &row.Email, &row.Username, &row.ActualCost, &row.Cost, &row.AccountCost, &row.Requests, &row.Tokens,
			&out.TotalUsers, &out.TotalActualCost, &out.TotalCost, &out.TotalAccountCost, &out.TotalRequests, &out.TotalTokens); err != nil {
			return nil, err
		}
		out.Ranking = append(out.Ranking, row)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
