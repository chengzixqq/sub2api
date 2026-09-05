-- 193_workspace_settlement_rate_range.sql
-- Move the supplier settlement-rate range from per-group grants to workspaces.
-- Existing grant values are widened per workspace so no configured range is lost
-- when a workspace has more than one granted group. Keep the legacy grant columns
-- during this release so the previous image remains a valid rollback target.

ALTER TABLE workspaces
    ADD COLUMN IF NOT EXISTS settlement_rate_min DECIMAL(10,4) NULL;

ALTER TABLE workspaces
    ADD COLUMN IF NOT EXISTS settlement_rate_max DECIMAL(10,4) NULL;

WITH workspace_rate_ranges AS (
    SELECT
        workspace_id,
        MIN(cost_rate_multiplier) FILTER (WHERE cost_rate_multiplier IS NOT NULL) AS settlement_rate_min,
        MAX(cost_rate_max) FILTER (WHERE cost_rate_max IS NOT NULL) AS settlement_rate_max
    FROM workspace_group_grants
    GROUP BY workspace_id
)
UPDATE workspaces AS w
SET settlement_rate_min = r.settlement_rate_min,
    settlement_rate_max = r.settlement_rate_max
FROM workspace_rate_ranges AS r
WHERE w.id = r.workspace_id
  AND (r.settlement_rate_min IS NOT NULL OR r.settlement_rate_max IS NOT NULL);
