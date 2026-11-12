ALTER TABLE accounts
    ADD COLUMN IF NOT EXISTS dispatch_scope VARCHAR(24) NOT NULL DEFAULT 'shared';

DO $$ BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'accounts_dispatch_scope_valid'
    ) THEN
        ALTER TABLE accounts ADD CONSTRAINT accounts_dispatch_scope_valid
            CHECK (dispatch_scope IN ('shared', 'authorized_only'));
    END IF;
END $$;

-- Do not reuse a version after deleting/recreating a policy (stale If-Match ABA).
CREATE SEQUENCE IF NOT EXISTS user_group_account_policy_version_seq AS BIGINT;

CREATE TABLE IF NOT EXISTS user_group_account_policies (
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    group_id BIGINT NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
    mode VARCHAR(12) NOT NULL CHECK (mode IN ('append', 'replace')),
    version BIGINT NOT NULL DEFAULT nextval('user_group_account_policy_version_seq'),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (user_id, group_id)
);

CREATE INDEX IF NOT EXISTS user_group_account_policies_group_idx
    ON user_group_account_policies (group_id);

CREATE TABLE IF NOT EXISTS user_group_account_grants (
    user_id BIGINT NOT NULL,
    group_id BIGINT NOT NULL,
    account_id BIGINT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    origin VARCHAR(8) NOT NULL CHECK (origin IN ('member', 'extra')),
    PRIMARY KEY (user_id, group_id, account_id),
    FOREIGN KEY (user_id, group_id)
        REFERENCES user_group_account_policies (user_id, group_id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS user_group_account_grants_account_idx
    ON user_group_account_grants (account_id);
