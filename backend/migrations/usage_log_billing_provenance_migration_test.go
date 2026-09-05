package migrations

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestMigration191IsIdempotent 锁定 191 可重复执行。
//
// 失败计费特性依赖 usage_logs.billing_provenance 落库；该列一旦被手工加过、
// 或迁移在多副本部署中被并发跑到第二遍，缺少 IF NOT EXISTS 就会以
// 「column already exists」中断整条迁移链，把后续迁移一起挡住。
func TestMigration191IsIdempotent(t *testing.T) {
	content, err := FS.ReadFile("191_usage_log_billing_provenance.sql")
	require.NoError(t, err)

	sql := string(content)
	require.Contains(t, sql, "ADD COLUMN IF NOT EXISTS",
		"191 必须用 ADD COLUMN IF NOT EXISTS，否则重复执行会以 column already exists 中断迁移链")
}

// TestMigration191ColumnIsNullableVarchar20 锁定列的类型与可空性。
//
// 两者都直接决定计费语义，不是随意的 schema 细节：
//   - VARCHAR(20) 必须容得下最长的取值 failed_estimated（16 字符）；
//   - 必须可空且无默认值。NULL 表示「历史行 + 成功且用量来自上游」，
//     行为完全不变；一旦给了默认值，历史成功行会被追认成某种 provenance，
//     Task 2 的成功过滤器就会把它们从请求计数里剔掉，直接篡改历史统计。
//     可空无默认值在 PostgreSQL 11+ 上还只是元数据变更，不重写 usage_logs 大表。
func TestMigration191ColumnIsNullableVarchar20(t *testing.T) {
	content, err := FS.ReadFile("191_usage_log_billing_provenance.sql")
	require.NoError(t, err)

	sql := string(content)
	require.Contains(t, sql, "billing_provenance VARCHAR(20)",
		"列类型必须是 VARCHAR(20)，要容得下最长取值 failed_estimated")
	require.NotContains(t, sql, "NOT NULL",
		"该列必须可空：NULL 表示历史行与上游用量成功行，加 NOT NULL 会阻断迁移或篡改历史语义")
	require.NotContains(t, sql, "DEFAULT",
		"该列不得有默认值：给了默认值等于把历史成功行追认成某种 provenance，会被成功过滤器剔出请求计数")
	require.Contains(t, sql, "usage_logs",
		"必须作用在 usage_logs 表上")
}
