package repository

import (
	"context"
	"database/sql"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
)

// The dispatcher's event scans read ops_alert_events, accounts and account_test_runs directly (not through their
// repositories, whose interfaces have many test doubles); keep the column names in sync with those tables.

// pushEventScanLimit bounds one scan; the newest rows come first so a burst does not hide current events.
const pushEventScanLimit = 500

// pushAlertEventSelect reads events with their rule name / operator / window (D06 snapshot first, then the current
// rule) and whether an active scoped silence covers them (opsAlertEventSilenceJoin, as in the event list).
const pushAlertEventSelect = `
SELECT
  ops_alert_events.id,
  COALESCE(ops_alert_events.rule_id, 0),
  COALESCE(ops_alert_events.severity, ''),
  COALESCE(ops_alert_events.status, ''),
  COALESCE(NULLIF(ops_alert_events.rule_snapshot->>'name', ''), r.name, ''),
  COALESCE(ops_alert_events.title, ''),
  COALESCE(NULLIF(ops_alert_events.rule_snapshot->>'operator', ''), r.operator, ''),
  COALESCE((ops_alert_events.rule_snapshot->>'window_minutes')::INT, r.window_minutes, 0),
  ops_alert_events.metric_value,
  ops_alert_events.threshold_value,
  COALESCE(ops_alert_events.dimensions->>'platform', ''),
  NULLIF(ops_alert_events.dimensions->>'group_id', '')::BIGINT,
  ops_alert_events.fired_at,
  ops_alert_events.resolved_at,
  active_silence.silence_id IS NOT NULL
FROM ops_alert_events
LEFT JOIN ops_alert_rules r ON r.id = ops_alert_events.rule_id
` + opsAlertEventSilenceJoin

func (r *pushRepository) queryAlertEvents(ctx context.Context, where string, args ...any) ([]*service.PushAlertEventRow, error) {
	rows, err := r.db.QueryContext(ctx, pushAlertEventSelect+"\n"+where, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []*service.PushAlertEventRow{}
	for rows.Next() {
		var row service.PushAlertEventRow
		var metric, threshold sql.NullFloat64
		var groupID sql.NullInt64
		var resolvedAt sql.NullTime
		if err := rows.Scan(&row.EventID, &row.RuleID, &row.Severity, &row.Status, &row.RuleName, &row.Title, &row.Operator,
			&row.WindowMinutes, &metric, &threshold, &row.Platform, &groupID, &row.FiredAt, &resolvedAt, &row.Silenced); err != nil {
			return nil, err
		}
		if metric.Valid {
			v := metric.Float64
			row.MetricValue = &v
		}
		if threshold.Valid {
			v := threshold.Float64
			row.ThresholdValue = &v
		}
		if groupID.Valid {
			v := groupID.Int64
			row.GroupID = &v
		}
		row.ResolvedAt = pushNullTimePtr(resolvedAt)
		out = append(out, &row)
	}
	return out, rows.Err()
}

func (r *pushRepository) ListAlertEventsFiredBetween(ctx context.Context, from, to time.Time) ([]*service.PushAlertEventRow, error) {
	return r.queryAlertEvents(ctx, `WHERE ops_alert_events.fired_at >= $1 AND ops_alert_events.fired_at < $2
ORDER BY ops_alert_events.fired_at DESC, ops_alert_events.id DESC LIMIT $3`, from, to, pushEventScanLimit)
}

func (r *pushRepository) ListAlertEventsResolvedBetween(ctx context.Context, from, to time.Time) ([]*service.PushAlertEventRow, error) {
	return r.queryAlertEvents(ctx, `WHERE ops_alert_events.resolved_at >= $1 AND ops_alert_events.resolved_at < $2
  AND ops_alert_events.status IN ($4, $5)
ORDER BY ops_alert_events.resolved_at DESC, ops_alert_events.id DESC LIMIT $3`,
		from, to, pushEventScanLimit, service.OpsAlertStatusResolved, service.OpsAlertStatusManualResolved)
}

func (r *pushRepository) ListAccountErrorsBetween(ctx context.Context, from, to time.Time) ([]*service.PushAccountErrorRow, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT a.id, a.platform, COALESCE(a.error_message, ''), a.error_at,
  COALESCE((SELECT array_agg(ag.group_id ORDER BY ag.group_id) FROM account_groups ag WHERE ag.account_id = a.id), '{}')
FROM accounts a
WHERE a.status = $3 AND a.deleted_at IS NULL AND a.error_at >= $1 AND a.error_at < $2
ORDER BY a.error_at DESC, a.id DESC
LIMIT $4`, from, to, service.StatusError, pushEventScanLimit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []*service.PushAccountErrorRow{}
	for rows.Next() {
		var row service.PushAccountErrorRow
		var groups pq.Int64Array
		if err := rows.Scan(&row.AccountID, &row.Platform, &row.ErrorMessage, &row.ErrorAt, &groups); err != nil {
			return nil, err
		}
		row.GroupIDs = []int64(groups)
		out = append(out, &row)
	}
	return out, rows.Err()
}

func (r *pushRepository) ListAsyncTestRunsFinishedBetween(ctx context.Context, from, to time.Time) ([]*service.PushTestRunRow, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT t.run_id, t.account_id, COALESCE(a.platform, ''), t.status, t.model_id, t.latency_ms, t.upstream_status_code,
  t.error_message, t.finished_at, t.created_by,
  COALESCE((SELECT array_agg(ag.group_id ORDER BY ag.group_id) FROM account_groups ag WHERE ag.account_id = t.account_id), '{}')
FROM account_test_runs t
JOIN accounts a ON a.id = t.account_id
WHERE t.source = $3 AND t.status <> $4 AND t.created_by IS NOT NULL
  AND t.finished_at >= $1 AND t.finished_at < $2
ORDER BY t.finished_at DESC, t.id DESC
LIMIT $5`, from, to, service.AccountTestRunSourceAsync, service.AccountTestRunStatusRunning, pushEventScanLimit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []*service.PushTestRunRow{}
	for rows.Next() {
		var row service.PushTestRunRow
		var latency sql.NullInt64
		var upstream sql.NullInt32
		var groups pq.Int64Array
		if err := rows.Scan(&row.RunID, &row.AccountID, &row.Platform, &row.Status, &row.ModelID, &latency, &upstream,
			&row.ErrorMessage, &row.FinishedAt, &row.CreatedBy, &groups); err != nil {
			return nil, err
		}
		if latency.Valid {
			v := latency.Int64
			row.LatencyMs = &v
		}
		if upstream.Valid {
			v := int(upstream.Int32)
			row.UpstreamStatusCode = &v
		}
		row.GroupIDs = []int64(groups)
		out = append(out, &row)
	}
	return out, rows.Err()
}
