-- 192_workspace_vendor_delegation.sql
-- 工作区（供应商代运营）机制：三张新表 + 三处加列。
--
-- 设计要点：
--   1. accounts/proxies 加 workspace_id NOT NULL DEFAULT 1，存量数据自动归入
--      「站长直管」工作区（id=1），零数据搬运。
--   2. groups 不加 workspace_id —— 一个分组可同时授权给多家供应商，
--      单值列无法表达；归属关系由 workspace_group_grants 承载。
--   3. audit_logs 加可空 workspace_id，用于站长的供应商操作记录总览。
--      NULL = 站长本人或系统操作，语义上「不属于任何供应商工作区」。

-- 工作区：一个工作区代表一家供应商（或站长直管）。
CREATE TABLE IF NOT EXISTS workspaces (
    id BIGSERIAL PRIMARY KEY,
    name VARCHAR(100) NOT NULL,
    description VARCHAR(500) NOT NULL DEFAULT '',
    status VARCHAR(20) NOT NULL DEFAULT 'active',
    -- 五个权限档，默认全关：新建工作区在站长显式开权限前什么都做不了。
    perm_account_manage BOOLEAN NOT NULL DEFAULT FALSE,
    perm_group_ops BOOLEAN NOT NULL DEFAULT FALSE,
    perm_group_billing BOOLEAN NOT NULL DEFAULT FALSE,
    perm_proxy_manage BOOLEAN NOT NULL DEFAULT FALSE,
    perm_monitor_view BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at TIMESTAMPTZ NULL
);

CREATE INDEX IF NOT EXISTS workspaces_status_idx ON workspaces (status);
CREATE INDEX IF NOT EXISTS workspaces_deleted_at_idx ON workspaces (deleted_at);

-- 预置 id=1「站长直管」，作为存量 accounts/proxies 的归属。
-- 固定 id 以便 DEFAULT 1 生效；ON CONFLICT 保证幂等。
INSERT INTO workspaces (id, name, description, status)
VALUES (1, '站长直管', '系统预置工作区，承载所有未划归供应商的资源', 'active')
ON CONFLICT (id) DO NOTHING;

-- 重置序列，避免后续 INSERT 撞上已占用的 id=1。
SELECT setval('workspaces_id_seq', GREATEST((SELECT MAX(id) FROM workspaces), 1));

-- 工作区成员：供应商账号 → 工作区。一个用户至多属于一个工作区。
CREATE TABLE IF NOT EXISTS workspace_members (
    id BIGSERIAL PRIMARY KEY,
    -- UNIQUE 而非主键：保留"一个用户至多属于一个工作区"的约束，
    -- 同时让 Ent 沿用默认 id 主键惯例（避免自定义单列主键的写法摩擦）。
    user_id BIGINT NOT NULL UNIQUE REFERENCES users(id) ON DELETE CASCADE,
    workspace_id BIGINT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS workspace_members_workspace_id_idx
    ON workspace_members (workspace_id);

-- 分组授权：站长把某个分组开放给某家供应商，并锁定其账号的基准优先级与成本倍率。
CREATE TABLE IF NOT EXISTS workspace_group_grants (
    id BIGSERIAL PRIMARY KEY,
    workspace_id BIGINT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    group_id BIGINT NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
    -- 该供应商在此分组内新增账号时强制套用的 account_groups.priority。
    -- 供应商不可自选，避免多家互相抬价抢流量。
    base_priority INTEGER NOT NULL DEFAULT 50,
    -- 成本倍率（结算口径）：该供应商在此分组的账号按此倍率计成本。
    -- NULL = 回退到 accounts.rate_multiplier（兼容未设置的情况）。
    -- decimal(10,4) 与 accounts.rate_multiplier 保持同一精度，避免结算口径漂移。
    cost_rate_multiplier DECIMAL(10,4) NULL,
    -- 成本倍率上限：供应商只能在 (0, cost_rate_max] 内下调，防止单方面抬价。
    cost_rate_max DECIMAL(10,4) NULL,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    -- 代理主键 + 唯一约束，而非复合主键：语义等价（同一分组对同一工作区
    -- 只能有一条授权），但让 Ent 沿用默认 id 主键惯例。
    UNIQUE (workspace_id, group_id)
);

CREATE INDEX IF NOT EXISTS workspace_group_grants_group_id_idx
    ON workspace_group_grants (group_id);

-- 资源归属列。NOT NULL DEFAULT 1：存量行自动归入站长直管，回滚即删列。
ALTER TABLE accounts ADD COLUMN IF NOT EXISTS workspace_id BIGINT NOT NULL DEFAULT 1;
ALTER TABLE proxies ADD COLUMN IF NOT EXISTS workspace_id BIGINT NOT NULL DEFAULT 1;

-- 审计归属：可空，NULL 表示站长/系统操作。
ALTER TABLE audit_logs ADD COLUMN IF NOT EXISTS workspace_id BIGINT NULL;

-- 外键用 RESTRICT：工作区下还挂着账号/代理时不允许删除，避免资源变孤儿。
-- audit_logs 用 SET NULL：删工作区不该连带丢审计历史。
-- DO 块保证幂等（ADD CONSTRAINT 无 IF NOT EXISTS 语法）。
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'accounts_workspace_id_fkey'
    ) THEN
        ALTER TABLE accounts ADD CONSTRAINT accounts_workspace_id_fkey
            FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE RESTRICT;
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'proxies_workspace_id_fkey'
    ) THEN
        ALTER TABLE proxies ADD CONSTRAINT proxies_workspace_id_fkey
            FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE RESTRICT;
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'audit_logs_workspace_id_fkey'
    ) THEN
        ALTER TABLE audit_logs ADD CONSTRAINT audit_logs_workspace_id_fkey
            FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE SET NULL;
    END IF;
END $$;

CREATE INDEX IF NOT EXISTS accounts_workspace_id_idx ON accounts (workspace_id);
CREATE INDEX IF NOT EXISTS proxies_workspace_id_idx ON proxies (workspace_id);

CREATE INDEX IF NOT EXISTS audit_logs_workspace_id_created_at_idx
    ON audit_logs (workspace_id, created_at DESC);
