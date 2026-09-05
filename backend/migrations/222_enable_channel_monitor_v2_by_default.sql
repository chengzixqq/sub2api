-- This upgrade intentionally starts passive Channel Monitor V2. Administrators
-- can explicitly switch back to v1 after the migration.
INSERT INTO settings (key, value)
VALUES ('channel_monitor_mode', 'v2')
ON CONFLICT (key) DO UPDATE
SET value = EXCLUDED.value,
    updated_at = NOW();
