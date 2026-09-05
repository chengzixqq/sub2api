-- 191_usage_log_billing_provenance.sql
-- 标记用量行的「成功/失败」与「上游用量/本地估算」两个正交维度。
-- NULL = 历史行 + 成功且用量来自上游，行为完全不变。
-- 可空无默认值：PostgreSQL 11+ 上是元数据变更，不重写 usage_logs 大表。
ALTER TABLE usage_logs ADD COLUMN IF NOT EXISTS billing_provenance VARCHAR(20);
