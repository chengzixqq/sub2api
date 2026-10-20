-- Account test runs: the async test started by POST /admin/accounts/:id/test-runs (source=async) and the
-- outcome of every interactive SSE test (source=manual), so a client can leave and read the result later.
-- At most one running row per account (partial unique index); rows left running by a crash or restart are
-- marked interrupted once they are older than the stale window. Each account keeps its latest finished rows.
-- error_message is redacted before it is stored; response_text is truncated. The prompt is not stored.

CREATE TABLE IF NOT EXISTS account_test_runs (
    id BIGSERIAL PRIMARY KEY,
    run_id VARCHAR(40) NOT NULL,
    account_id BIGINT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    source VARCHAR(16) NOT NULL,
    status VARCHAR(16) NOT NULL,
    model_id VARCHAR(200) NOT NULL DEFAULT '',
    latency_ms BIGINT NULL,
    first_token_ms BIGINT NULL,
    upstream_status_code INT NULL,
    error_message TEXT NOT NULL DEFAULT '',
    response_text TEXT NOT NULL DEFAULT '',
    recovery JSONB NULL,
    started_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    finished_at TIMESTAMPTZ NULL,
    created_by BIGINT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_account_test_runs_run_id ON account_test_runs (run_id);
CREATE INDEX IF NOT EXISTS idx_account_test_runs_account_started ON account_test_runs (account_id, started_at DESC, id DESC);
CREATE UNIQUE INDEX IF NOT EXISTS idx_account_test_runs_one_running ON account_test_runs (account_id) WHERE status = 'running';
CREATE INDEX IF NOT EXISTS idx_account_test_runs_running_started ON account_test_runs (started_at) WHERE status = 'running';
