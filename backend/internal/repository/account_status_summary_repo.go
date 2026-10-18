package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
)

var _ service.AccountStatusSummaryRepository = (*accountRepository)(nil)

// ListAccountsForStatusSummary reads every live account with the columns the
// schedule-state summary needs in one query: group ids are aggregated and only
// the model_rate_limits key of extra is fetched.
func (r *accountRepository) ListAccountsForStatusSummary(ctx context.Context, platform string, groupID *int64) (rows []service.AccountStatusRow, err error) {
	args := []any{}
	conditions := []string{"a.deleted_at IS NULL"}
	if platform = strings.TrimSpace(platform); platform != "" {
		args = append(args, platform)
		conditions = append(conditions, "a.platform = $"+itoa(len(args)))
	}
	if groupID != nil && *groupID > 0 {
		args = append(args, *groupID)
		conditions = append(conditions, "EXISTS (SELECT 1 FROM account_groups f WHERE f.account_id = a.id AND f.group_id = $"+itoa(len(args))+")")
	}
	if workspaceID := service.ScopeFromContextOrDeny(ctx).WorkspaceFilter(); workspaceID != nil {
		args = append(args, *workspaceID)
		conditions = append(conditions, "a.workspace_id = $"+itoa(len(args)))
	}

	result, err := r.sql.QueryContext(ctx, `
		SELECT a.id, a.name, a.platform, a.status, COALESCE(a.error_message, ''), a.schedulable,
			a.rate_limit_reset_at, a.overload_until, a.temp_unschedulable_until,
			COALESCE(a.temp_unschedulable_reason, ''), a.expires_at, a.auto_pause_on_expired,
			a.extra -> 'model_rate_limits',
			COALESCE((SELECT array_agg(ag.group_id ORDER BY ag.priority, ag.group_id) FROM account_groups ag WHERE ag.account_id = a.id), '{}')
		FROM accounts a
		`+buildWhere(conditions)+`
		ORDER BY a.id`, args...)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := result.Close(); closeErr != nil && err == nil {
			err = closeErr
			rows = nil
		}
	}()

	rows = []service.AccountStatusRow{}
	for result.Next() {
		var (
			row              service.AccountStatusRow
			rateLimitResetAt sql.NullTime
			overloadUntil    sql.NullTime
			tempUntil        sql.NullTime
			expiresAt        sql.NullTime
			modelRateLimits  []byte
			groupIDs         pq.Int64Array
		)
		if err = result.Scan(&row.ID, &row.Name, &row.Platform, &row.Status, &row.ErrorMessage, &row.Schedulable,
			&rateLimitResetAt, &overloadUntil, &tempUntil, &row.TempUnschedulableReason, &expiresAt,
			&row.AutoPauseOnExpired, &modelRateLimits, &groupIDs); err != nil {
			return nil, err
		}
		row.RateLimitResetAt = batchImageNullTimePtr(rateLimitResetAt)
		row.OverloadUntil = batchImageNullTimePtr(overloadUntil)
		row.TempUnschedulableUntil = batchImageNullTimePtr(tempUntil)
		row.ExpiresAt = batchImageNullTimePtr(expiresAt)
		if len(modelRateLimits) > 0 {
			// A malformed runtime map only loses the per-model detail.
			_ = json.Unmarshal(modelRateLimits, &row.ModelRateLimits)
		}
		row.GroupIDs = []int64(groupIDs)
		rows = append(rows, row)
	}
	if err = result.Err(); err != nil {
		return nil, err
	}
	return rows, nil
}

// ListGroupsForStatusSummary returns the live active groups matching the
// filters plus the live groups in extraIDs, whatever their status.
func (r *accountRepository) ListGroupsForStatusSummary(ctx context.Context, platform string, groupID *int64, extraIDs []int64) (groups []service.AccountStatusGroupRow, err error) {
	args := []any{pq.Array(extraIDs)}
	match := []string{"g.status = 'active'"}
	if platform = strings.TrimSpace(platform); platform != "" {
		args = append(args, platform)
		match = append(match, "g.platform = $"+itoa(len(args)))
	}
	filter := ""
	if groupID != nil && *groupID > 0 {
		args = append(args, *groupID)
		filter = " AND g.id = $" + itoa(len(args))
	}

	result, err := r.sql.QueryContext(ctx, `
		SELECT g.id, g.name, g.platform, g.status
		FROM groups g
		WHERE g.deleted_at IS NULL`+filter+`
			AND ((`+strings.Join(match, " AND ")+`) OR g.id = ANY($1))
		ORDER BY g.id`, args...)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := result.Close(); closeErr != nil && err == nil {
			err = closeErr
			groups = nil
		}
	}()

	groups = []service.AccountStatusGroupRow{}
	for result.Next() {
		var g service.AccountStatusGroupRow
		if err = result.Scan(&g.ID, &g.Name, &g.Platform, &g.Status); err != nil {
			return nil, err
		}
		groups = append(groups, g)
	}
	if err = result.Err(); err != nil {
		return nil, err
	}
	return groups, nil
}
