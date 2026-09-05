package repository

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 该过滤器决定哪些用量行计入 Dashboard 成功统计。已计费的失败行携带真实 cost,
// 若只判 actual_cost > 0 会被误算成成功,污染全部成功率/用量指标。
//
// 下面这条测试只检查常量本身的文本构造(是否判定 billing_provenance、是否 NULL 历史行仍算
// 成功、是否白名单而非黑名单),这是常量自身的性质。它不能证明常量被用在了正确的地方——
// 把同一个常量整个删掉不用、或者拼进 WHERE 子句而不是 FILTER (WHERE ...),这条测试照样
// 全部通过。下面的 TestUsageLogSuccessFilter_* 系列测试通过 go-sqlmock 捕获
// GetUserDashboardStats/GetBatchUserUsageStats 实际下发的 SQL,专门校验"常量被用在哪、
// 以什么形式施加"这一正交的性质。两组测试各自覆盖不同的失败模式,缺一不可。
func TestUsageLogSuccessFilterExcludesBilledFailures(t *testing.T) {
	assert.Contains(t, usageLogSuccessFilterUL, "billing_provenance",
		"success filter must discriminate billed failure rows")
	assert.Contains(t, usageLogSuccessFilterUL, "IS NULL",
		"historical rows have NULL provenance and must stay counted")
	assert.NotContains(t, usageLogSuccessFilterUL, "failed_",
		"filter must whitelist success values, not blacklist failure values")
}

// newCapturingSQLMock 返回一个使用自定义 QueryMatcher 的 sqlmock 连接：该 matcher 对任意
// expected/actual SQL 都判定为匹配,但会把每一次实际执行的 SQL 原文记录到返回的 slice 里。
//
// 目的是拿到 usageLogSuccessFilterUL 拼接进查询后、真正下发给驱动的 SQL 文本本身,而不是
// 像 newSQLMock（usage_cleanup_repo_test.go）那样只用正则判断"是否匹配"、拿不到原文。
//
// 刻意保持默认的 ordered 匹配模式（不调用 MatchExpectationsInOrder(false)）：go-sqlmock 在
// ordered 模式下，每次实际调用只会触发一次 QueryMatcher.Match 回调；切到 unordered 模式后,
// 同一次调用会多触发一次（一次在按序查找可用 expectation 的循环内，一次在循环结束后再确认
// 一遍),会导致 captured 里出现重复项。配合下面按方法内实际调用顺序一一注册的 ExpectQuery,
// 默认 ordered 模式下每次调用都能拿到干净、不重复的一条记录。
func newCapturingSQLMock(t *testing.T) (*sql.DB, sqlmock.Sqlmock, *[]string) {
	t.Helper()
	captured := make([]string, 0, 8)
	matcher := sqlmock.QueryMatcherFunc(func(_, actualSQL string) error {
		captured = append(captured, actualSQL)
		return nil
	})
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(matcher))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return db, mock, &captured
}

// findCapturedQuery 通过一个只应出现在目标查询里的结构性子串（fingerprint）从 captured 里
// 定位它,而不是依赖下标位置——下标会随着方法内前置查询数量的变化而失效,fingerprint 只要
// 选得足够独特（比如别名 "ul"，本包里只有这两条查询会把 usage_logs 起别名为 ul）就不受影响。
// 要求 fingerprint 命中且只命中一条,命中 0 条或多条都说明测试假设已经不成立,直接 fail。
func findCapturedQuery(t *testing.T, captured []string, fingerprint string) string {
	t.Helper()
	var matches []string
	for _, q := range captured {
		if strings.Contains(q, fingerprint) {
			matches = append(matches, q)
		}
	}
	require.Len(t, matches, 1,
		"expected exactly one captured query containing %q, captured=%v", fingerprint, captured)
	return matches[0]
}

// columnExpr 提取 `<expr> as <alias>` 这一 SELECT 列在 query 里对应的 SQL 片段：从上一列
// "as <precedingAlias>" 结束处，取到本列 "as <alias>" 结束处（首列传 precedingAlias=""）。
//
// 这不是通用 SQL 解析——它依赖于本文件测试的两条查询里，除了 platform 列本身,其余每一列的
// 表达式都不含嵌套的 "as xxx" 标记，所以每个别名对应的 "as <alias>" 文本在整条查询里唯一
// 出现一次。对这两条形状固定的查询,这个前提成立,足够精确地把断言锁定在某一个聚合列的
// 表达式上,而不是整条 SQL 字符串——避免 strings.Contains(sql, "billing_provenance") 这种
// 对整条 SQL 的检查在过滤器被拼进 WHERE 子句时也会误判通过。
func columnExpr(t *testing.T, query, alias, precedingAlias string) string {
	t.Helper()
	marker := "as " + alias
	end := strings.Index(query, marker)
	require.Greater(t, end, -1, "column alias %q not found in query:\n%s", alias, query)
	end += len(marker)

	start := 0
	if precedingAlias != "" {
		prevMarker := "as " + precedingAlias
		prevIdx := strings.Index(query, prevMarker)
		require.Greater(t, prevIdx, -1, "preceding alias %q not found in query:\n%s", precedingAlias, query)
		start = prevIdx + len(prevMarker)
	}
	require.Greater(t, end, start, "column %q expression range is empty or inverted", alias)
	return query[start:end]
}

// trailingWhereClause 提取 GROUP BY 之前、最后一个 "WHERE" 开始的子句——也就是查询末尾那个
// 裸的 WHERE,而不是聚合函数内部 FILTER (WHERE ...) 里的 WHERE（这些都在 GROUP BY 之前的
// SELECT 列表里，位置更靠前，LastIndex 会跳过它们)。定位方式不假设该子句的具体内容,因此就算
// 未来有人把 usageLogSuccessFilterUL 拼回这里、或者附加了别的条件,也能拿到完整片段来断言。
func trailingWhereClause(t *testing.T, query string) string {
	t.Helper()
	groupByIdx := strings.Index(query, "GROUP BY")
	require.Greater(t, groupByIdx, -1, "query must contain GROUP BY:\n%s", query)
	whereIdx := strings.LastIndex(query[:groupByIdx], "WHERE")
	require.Greater(t, whereIdx, -1, "query must contain a trailing WHERE clause before GROUP BY:\n%s", query)
	return query[whereIdx:groupByIdx]
}

// TestUsageLogSuccessFilter_DashboardPlatformQuery_AppliedOnlyToRequestCounts 用 go-sqlmock
// 捕获 GetUserDashboardStats 里 platformQuery 实际下发的 SQL,校验 usageLogSuccessFilterUL
// 只以 FILTER (WHERE ...) 的形式施加在 total_requests/today_requests 这两个"请求数"聚合列
// 上：
//   - 不出现在查询末尾裸的 WHERE 子句里——否则会把被计费的失败行整行过滤掉,连它产生的真实
//     cost/token 都不计入,与"必须计入花费类统计"的要求相悖；
//   - 不出现在 total_tokens/total_actual_cost/today_tokens/today_actual_cost 这些花费类聚合
//     上——否则被计费的失败行产生的真实上游费用会从花费报表里消失,与不做该过滤的总账对不上。
//
// 只断言常量文本在整条 SQL 字符串里"某处出现"是不够的：把过滤器拼进 WHERE 子句的回退版本
// 同样会让 strings.Contains(sql, "billing_provenance") 为真,而那正是这个测试要拦住的 bug。
// 所以这里把查询切成按列命名的片段再分别断言,而不是对整条字符串做 Contains。
func TestUsageLogSuccessFilter_DashboardPlatformQuery_AppliedOnlyToRequestCounts(t *testing.T) {
	db, mock, captured := newCapturingSQLMock(t)
	repo := &usageLogRepository{sql: db}

	// GetUserDashboardStats 依次发起 6 次 QueryContext：API Key 总数、API Key 活跃数、
	// totalStatsQuery、todayStatsQuery、getPerformanceStats 内部查询,最后才是 platformQuery。
	// 默认 ordered 模式按注册顺序一一对应,这里给每一步都返回能让扫描成功的最小合法数据,
	// 让方法一路跑到底、真正下发 platformQuery 供上面的 matcher 捕获。
	mock.ExpectQuery("").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(int64(1)))
	mock.ExpectQuery("").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(int64(1)))
	mock.ExpectQuery("").WillReturnRows(sqlmock.NewRows([]string{
		"total_requests", "total_input_tokens", "total_output_tokens",
		"total_cache_creation_tokens", "total_cache_read_tokens",
		"total_cost", "total_actual_cost", "avg_duration_ms",
	}).AddRow(int64(0), int64(0), int64(0), int64(0), int64(0), float64(0), float64(0), float64(0)))
	mock.ExpectQuery("").WillReturnRows(sqlmock.NewRows([]string{
		"today_requests", "today_input_tokens", "today_output_tokens",
		"today_cache_creation_tokens", "today_cache_read_tokens",
		"today_cost", "today_actual_cost",
	}).AddRow(int64(0), int64(0), int64(0), int64(0), int64(0), float64(0), float64(0)))
	mock.ExpectQuery("").WillReturnRows(sqlmock.NewRows([]string{"request_count", "token_count"}).
		AddRow(int64(0), int64(0)))
	mock.ExpectQuery("").WillReturnRows(sqlmock.NewRows([]string{
		"platform", "total_requests", "total_tokens", "total_actual_cost",
		"today_requests", "today_tokens", "today_actual_cost",
	}))

	_, err := repo.GetUserDashboardStats(context.Background(), 42)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())

	platformQuery := findCapturedQuery(t, *captured, "usage_logs ul")

	totalRequestsExpr := columnExpr(t, platformQuery, "total_requests", "platform")
	totalTokensExpr := columnExpr(t, platformQuery, "total_tokens", "total_requests")
	totalActualCostExpr := columnExpr(t, platformQuery, "total_actual_cost", "total_tokens")
	todayRequestsExpr := columnExpr(t, platformQuery, "today_requests", "total_actual_cost")
	todayTokensExpr := columnExpr(t, platformQuery, "today_tokens", "today_requests")
	todayActualCostExpr := columnExpr(t, platformQuery, "today_actual_cost", "today_tokens")

	assert.Contains(t, totalRequestsExpr, "FILTER (WHERE",
		"total_requests must gate on the success filter via FILTER (WHERE ...), not a plain COUNT(*)")
	assert.Contains(t, totalRequestsExpr, usageLogSuccessFilterUL,
		"total_requests must apply the exact success filter")

	assert.Contains(t, todayRequestsExpr, "FILTER (WHERE",
		"today_requests must gate on the success filter via FILTER (WHERE ...), not a plain COUNT(*)")
	assert.Contains(t, todayRequestsExpr, usageLogSuccessFilterUL,
		"today_requests must apply the exact success filter")

	assert.NotContains(t, totalTokensExpr, "billing_provenance",
		"total_tokens is a usage aggregate: billed failure rows must stay counted")
	assert.NotContains(t, totalActualCostExpr, "billing_provenance",
		"total_actual_cost is a spend aggregate: billed failure rows carry real upstream cost and must stay counted")
	assert.NotContains(t, todayTokensExpr, "billing_provenance",
		"today_tokens is a usage aggregate: billed failure rows must stay counted")
	assert.NotContains(t, todayActualCostExpr, "billing_provenance",
		"today_actual_cost is a spend aggregate: billed failure rows carry real upstream cost and must stay counted")

	assert.NotContains(t, trailingWhereClause(t, platformQuery), "billing_provenance",
		"success filter must not be spliced into the query's WHERE clause: that would drop billed failure rows "+
			"from total_tokens/total_actual_cost/today_tokens/today_actual_cost too, understating real spend")
}

// TestUsageLogSuccessFilter_BatchUserUsageStatsQuery_NeverApplied 校验 GetBatchUserUsageStats
// 实际下发的查询完全不引用 usageLogSuccessFilterUL：这条查询只聚合 actual_cost（花费类统计,
// 没有任何请求数列),被计费的失败行产生了真实的上游费用,必须无条件计入,该过滤器在这里根本
// 不适用——既没有可以施加 FILTER (WHERE ...) 的请求数列,也不能拼进 WHERE 子句。
func TestUsageLogSuccessFilter_BatchUserUsageStatsQuery_NeverApplied(t *testing.T) {
	db, mock, captured := newCapturingSQLMock(t)
	repo := &usageLogRepository{sql: db}

	// 非空 userIDs 时 GetBatchUserUsageStats 只发起这一次 QueryContext。
	mock.ExpectQuery("").WillReturnRows(sqlmock.NewRows([]string{
		"user_id", "platform", "total_cost", "today_cost",
	}))

	_, err := repo.GetBatchUserUsageStats(context.Background(), []int64{42}, time.Time{}, time.Time{})
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())

	batchQuery := findCapturedQuery(t, *captured, "usage_logs ul")
	assert.NotContains(t, batchQuery, "billing_provenance",
		"GetBatchUserUsageStats aggregates spend only; applying the request-count success filter here "+
			"would silently drop billed-failure cost from batch totals")
}
