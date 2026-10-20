package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// opsAlertEventDetailSelect reads events for the list and detail responses: the event columns, the covering silence
// (opsAlertEventSilenceJoin), the rule snapshot and the current rule, the group name and the manual resolver.
// The extra values are scalar subqueries rather than FROM-level joins, so the unqualified columns of
// buildOpsAlertEventsWhere stay unambiguous. current_rule's keys must match opsAlertRuleSnapshot.
// The group name ignores deleted_at so older events still show it; the resolver name matches the silence creator's.
const opsAlertEventDetailSelect = `
SELECT
  id,
  COALESCE(rule_id, 0),
  COALESCE(severity, ''),
  COALESCE(status, ''),
  COALESCE(title, ''),
  COALESCE(description, ''),
  metric_value,
  threshold_value,
  dimensions,
  fired_at,
  resolved_at,
  email_sent,
  created_at,
  silence_id,
  silence_until,
  rule_snapshot,
  (SELECT jsonb_build_object(
      'id', r.id, 'name', r.name, 'metric_type', r.metric_type, 'operator', r.operator, 'threshold', r.threshold,
      'window_minutes', r.window_minutes, 'sustained_minutes', r.sustained_minutes)
    FROM ops_alert_rules r WHERE r.id = ops_alert_events.rule_id) AS current_rule,
  (SELECT g.name FROM groups g WHERE g.id = NULLIF(ops_alert_events.dimensions->>'group_id', '')::BIGINT) AS group_name,
  resolved_by_user_id,
  (SELECT COALESCE(NULLIF(TRIM(u.username), ''), u.email) FROM users u WHERE u.id = ops_alert_events.resolved_by_user_id) AS resolved_by_user_name
FROM ops_alert_events
` + opsAlertEventSilenceJoin

// opsAlertRuleSnapshot is the stored form of an event's rule (ops_alert_events.rule_snapshot). Deleted is derived on
// read, so it is not stored.
type opsAlertRuleSnapshot struct {
	ID               int64   `json:"id"`
	Name             string  `json:"name"`
	MetricType       string  `json:"metric_type"`
	Operator         string  `json:"operator"`
	Threshold        float64 `json:"threshold"`
	WindowMinutes    int     `json:"window_minutes"`
	SustainedMinutes int     `json:"sustained_minutes"`
}

func (s opsAlertRuleSnapshot) toEventRule(deleted bool) *service.OpsAlertEventRule {
	return &service.OpsAlertEventRule{
		ID:               s.ID,
		Name:             s.Name,
		MetricType:       s.MetricType,
		Operator:         s.Operator,
		Threshold:        s.Threshold,
		WindowMinutes:    s.WindowMinutes,
		SustainedMinutes: s.SustainedMinutes,
		Deleted:          deleted,
	}
}

// opsAlertRuleSnapshotArg is the rule_snapshot value for an event's rule summary (NULL when there is none).
func opsAlertRuleSnapshotArg(rule *service.OpsAlertEventRule) (any, error) {
	if rule == nil {
		return sql.NullString{}, nil
	}
	b, err := json.Marshal(opsAlertRuleSnapshot{
		ID:               rule.ID,
		Name:             rule.Name,
		MetricType:       rule.MetricType,
		Operator:         rule.Operator,
		Threshold:        rule.Threshold,
		WindowMinutes:    rule.WindowMinutes,
		SustainedMinutes: rule.SustainedMinutes,
	})
	if err != nil {
		return nil, err
	}
	return sql.NullString{String: string(b), Valid: true}, nil
}

func decodeOpsAlertRuleSnapshot(raw []byte) (opsAlertRuleSnapshot, bool) {
	var out opsAlertRuleSnapshot
	if len(raw) == 0 || string(raw) == "null" {
		return out, false
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, false
	}
	return out, true
}

// resolveOpsAlertEventRule picks an event's rule: the snapshot taken when it fired (deleted when the rule is gone),
// else the current rule for events older than snapshots, else nil.
func resolveOpsAlertEventRule(snapshotRaw, currentRaw []byte) *service.OpsAlertEventRule {
	current, hasCurrent := decodeOpsAlertRuleSnapshot(currentRaw)
	if snapshot, ok := decodeOpsAlertRuleSnapshot(snapshotRaw); ok {
		return snapshot.toEventRule(!hasCurrent)
	}
	if hasCurrent {
		return current.toEventRule(false)
	}
	return nil
}

// scanOpsAlertEventDetail reads a row of opsAlertEventDetailSelect.
func scanOpsAlertEventDetail(row opsAlertEventRow) (*service.OpsAlertEvent, error) {
	var silenceID sql.NullInt64
	var silenceUntil sql.NullTime
	var snapshotRaw, currentRaw []byte
	var groupName sql.NullString
	var resolvedByID sql.NullInt64
	var resolvedByName sql.NullString
	ev, err := scanOpsAlertEventColumns(row, &silenceID, &silenceUntil, &snapshotRaw, &currentRaw, &groupName, &resolvedByID, &resolvedByName)
	if err != nil {
		return nil, err
	}
	if silenceID.Valid && silenceUntil.Valid {
		ev.Silence = &service.OpsAlertEventSilence{ID: silenceID.Int64, Until: silenceUntil.Time}
	}
	ev.Rule = resolveOpsAlertEventRule(snapshotRaw, currentRaw)
	if groupName.Valid {
		v := groupName.String
		ev.GroupName = &v
	}
	if resolvedByID.Valid {
		v := resolvedByID.Int64
		ev.ResolvedByUserID = &v
	}
	if resolvedByName.Valid {
		v := resolvedByName.String
		ev.ResolvedByUserName = &v
	}
	return ev, nil
}

// ResolveOrphanedAlertEvents resolves firing events whose rule no longer exists or is disabled. There is no foreign key
// on rule_id, so deleted rules leave their events behind.
func (r *opsRepository) ResolveOrphanedAlertEvents(ctx context.Context, resolvedAt time.Time) (int64, error) {
	if r == nil || r.db == nil {
		return 0, fmt.Errorf("nil ops repository")
	}
	q := `
UPDATE ops_alert_events e
SET status = $1, resolved_at = $2, resolved_by_user_id = NULL
WHERE e.status = $3
  AND e.rule_id > 0
  AND NOT EXISTS (SELECT 1 FROM ops_alert_rules r WHERE r.id = e.rule_id AND r.enabled)`
	res, err := r.db.ExecContext(ctx, q, service.OpsAlertStatusResolved, resolvedAt, service.OpsAlertStatusFiring)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// CountAlertEventsBySeverity counts events matching filter (the list's filters, without limit) by severity.
func (r *opsRepository) CountAlertEventsBySeverity(ctx context.Context, filter *service.OpsAlertEventFilter) (map[string]int64, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("nil ops repository")
	}
	where, args := buildOpsAlertEventsWhere(filter)
	q := `SELECT COALESCE(severity, ''), COUNT(*) FROM ops_alert_events ` + where + ` GROUP BY 1`
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	out := map[string]int64{}
	for rows.Next() {
		var severity string
		var n int64
		if err := rows.Scan(&severity, &n); err != nil {
			return nil, err
		}
		out[severity] += n
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
