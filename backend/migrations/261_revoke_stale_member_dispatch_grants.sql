-- A member grant is valid only while the account remains in the configured
-- group. Preserve extra grants and empty replace policies, but invalidate stale
-- editors whenever this structural cleanup removes a grant.
WITH revoked AS (
    DELETE FROM user_group_account_grants g
    WHERE g.origin = 'member'
      AND NOT EXISTS (
          SELECT 1 FROM account_groups ag
          WHERE ag.account_id = g.account_id AND ag.group_id = g.group_id
      )
    RETURNING g.user_id, g.group_id
), affected AS (
    SELECT DISTINCT user_id, group_id FROM revoked
)
UPDATE user_group_account_policies p
SET version = nextval('user_group_account_policy_version_seq'), updated_at = NOW()
FROM affected a
WHERE p.user_id = a.user_id AND p.group_id = a.group_id;
