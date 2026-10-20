package repository

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
)

var _ service.OpsRequestAttemptsRepository = (*opsRepository)(nil)

const (
	// One request has a handful of ops rows (failure, stream failure,
	// recovered); a reused client X-Request-ID may match many more.
	opsRequestAttemptErrorRowLimit = 20
	// usage_logs is unique per (request_id, api_key_id).
	opsRequestAttemptUsageRowLimit = 10
)

// ListRequestAttemptErrorLogs matches ops_error_logs by request_id or
// client_request_id (both indexed). It keeps the newest rows when a reused
// client X-Request-ID matches more than the limit, returned oldest first.
func (r *opsRepository) ListRequestAttemptErrorLogs(ctx context.Context, lookup service.OpsRequestAttemptLookup) ([]*service.OpsRequestAttemptErrorRow, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("nil ops repository")
	}
	conditions := []string{"(request_id = ANY($1) OR client_request_id = ANY($2))"}
	args := []any{pq.Array(nonNilStrings(lookup.RequestIDs)), pq.Array(nonNilStrings(lookup.ClientRequestIDs))}
	conditions, args = appendOpsRequestAttemptTimeBounds("created_at", lookup, conditions, args)
	q := `
SELECT id, created_at, COALESCE(request_id, ''), COALESCE(client_request_id, ''),
       COALESCE(status_code, 0), COALESCE(error_phase, ''), COALESCE(upstream_errors::text, '')
FROM ops_error_logs
WHERE ` + strings.Join(conditions, " AND ") + `
ORDER BY created_at DESC, id DESC
LIMIT ` + itoa(opsRequestAttemptErrorRowLimit)

	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	out := make([]*service.OpsRequestAttemptErrorRow, 0, 4)
	for rows.Next() {
		var row service.OpsRequestAttemptErrorRow
		if err := rows.Scan(&row.ID, &row.CreatedAt, &row.RequestID, &row.ClientRequestID,
			&row.StatusCode, &row.ErrorPhase, &row.UpstreamErrorsJSON); err != nil {
			return nil, err
		}
		row.CreatedAt = row.CreatedAt.UTC()
		out = append(out, &row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	slices.Reverse(out)
	return out, nil
}

// ListRequestAttemptUsageLogs reads billed attempts by usage request_id with
// the account name and effective platform. The vendor account allow-list is
// applied as defense in depth even though the route is owner-only.
func (r *opsRepository) ListRequestAttemptUsageLogs(ctx context.Context, lookup service.OpsRequestAttemptLookup) ([]*service.OpsRequestAttemptUsageRow, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("nil ops repository")
	}
	conditions := []string{"ul.request_id = ANY($1)"}
	args := []any{pq.Array(nonNilStrings(lookup.RequestIDs))}
	conditions, args = appendOpsRequestAttemptTimeBounds("ul.created_at", lookup, conditions, args)
	conditions, args = appendUsageAccountScopeCondition(ctx, "ul.account_id", conditions, args)
	q := `
SELECT ul.id, ul.created_at, COALESCE(ul.request_id, ''), COALESCE(ul.account_id, 0), COALESCE(a.name, ''),
       COALESCE(` + usageLogEffectivePlatformExpr + `, ''),
       ul.duration_ms, ul.first_token_ms, COALESCE(ul.upstream_request_id, '')
FROM usage_logs ul
LEFT JOIN accounts a ON a.id = ul.account_id
LEFT JOIN groups g ON g.id = ul.group_id
WHERE ` + strings.Join(conditions, " AND ") + `
ORDER BY ul.created_at, ul.id
LIMIT ` + itoa(opsRequestAttemptUsageRowLimit)

	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	out := make([]*service.OpsRequestAttemptUsageRow, 0, 1)
	for rows.Next() {
		var row service.OpsRequestAttemptUsageRow
		var duration, firstToken sql.NullInt64
		if err := rows.Scan(&row.ID, &row.CreatedAt, &row.RequestID, &row.AccountID, &row.AccountName,
			&row.Platform, &duration, &firstToken, &row.UpstreamRequestID); err != nil {
			return nil, err
		}
		row.CreatedAt = row.CreatedAt.UTC()
		if duration.Valid {
			v := duration.Int64
			row.DurationMs = &v
		}
		if firstToken.Valid {
			v := firstToken.Int64
			row.FirstTokenMs = &v
		}
		out = append(out, &row)
	}
	return out, rows.Err()
}

// ListRequestAttemptAccountNames includes soft-deleted accounts so an old
// chain still names the account it used.
func (r *opsRepository) ListRequestAttemptAccountNames(ctx context.Context, ids []int64) (map[int64]string, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("nil ops repository")
	}
	names := make(map[int64]string, len(ids))
	if len(ids) == 0 {
		return names, nil
	}
	rows, err := r.db.QueryContext(ctx, `SELECT id, name FROM accounts WHERE id = ANY($1)`, pq.Array(ids))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id int64
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		names[id] = name
	}
	return names, rows.Err()
}

func appendOpsRequestAttemptTimeBounds(column string, lookup service.OpsRequestAttemptLookup, conditions []string, args []any) ([]string, []any) {
	if lookup.StartTime != nil {
		args = append(args, lookup.StartTime.UTC())
		conditions = append(conditions, column+" >= $"+itoa(len(args)))
	}
	if lookup.EndTime != nil {
		args = append(args, lookup.EndTime.UTC())
		conditions = append(conditions, column+" < $"+itoa(len(args)))
	}
	return conditions, args
}

// nonNilStrings keeps pq.Array from encoding a nil slice as SQL NULL.
func nonNilStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}
