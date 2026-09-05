-- Restore the official active-probe site mode and make new monitors quota-backed.
-- Existing monitor rows are intentionally untouched; the application keeps their
-- legacy probe value when reading/updating them.
INSERT INTO settings (key, value, updated_at)
VALUES ('channel_monitor_mode', 'v1', NOW())
ON CONFLICT (key) DO UPDATE
SET value = EXCLUDED.value,
    updated_at = NOW();

ALTER TABLE channel_monitors
    ALTER COLUMN check_mode SET DEFAULT 'quota';

-- User-facing quota/balance data remains hidden by default. Admin views are not
-- gated by this setting.
INSERT INTO settings (key, value, updated_at)
VALUES ('channel_monitor_show_quota', 'false', NOW())
ON CONFLICT (key) DO UPDATE
SET value = EXCLUDED.value,
    updated_at = NOW();
