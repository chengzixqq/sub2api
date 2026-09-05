-- Migration 220 snapshots and clears non-Grok video pricing. This fork keeps
-- those operator-defined prices, so restore the snapshot in a new immutable
-- migration and retain the backup table for rollback/audit.
UPDATE groups AS g
SET video_price_480p = b.video_price_480p,
    video_price_720p = b.video_price_720p,
    video_price_1080p = b.video_price_1080p,
    video_model_prices = b.video_model_prices,
    updated_at = NOW()
FROM groups_video_price_backup_220 AS b
WHERE g.id = b.group_id
  AND g.platform IS DISTINCT FROM 'grok'
  AND g.platform IS DISTINCT FROM 'composite';

COMMENT ON TABLE groups_video_price_backup_220 IS
    'Migration 220 pre-cleanup snapshot retained after migration 221 restored non-Grok video prices.';
