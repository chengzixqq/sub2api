-- Alert events carry the rule as it was when they fired and the admin who resolved them by hand.
-- rule_snapshot: id, name, metric_type, operator, threshold, window_minutes, sustained_minutes, written by the
-- evaluator; existing events stay NULL and fall back to the current rule when read.
-- resolved_by_user_id: set by PUT /admin/ops/alert-events/:id/status with status=manual_resolved; existing rows stay NULL.

ALTER TABLE ops_alert_events ADD COLUMN IF NOT EXISTS rule_snapshot JSONB;
ALTER TABLE ops_alert_events ADD COLUMN IF NOT EXISTS resolved_by_user_id BIGINT;
