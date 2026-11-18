//go:build integration

package repository

import (
	"context"
	"database/sql"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagequery"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// legacyUserDashboardUsageQuery is userDashboardUsageQuery as it was before the
// history/today split, copied verbatim. It is the reference for equivalence.
const legacyUserDashboardUsageQuery = `
	SELECT
		` + usageLogEffectivePlatformExpr + ` as platform,
		COUNT(*) as total_requests,
		COALESCE(SUM(ul.input_tokens), 0) as total_input_tokens,
		COALESCE(SUM(ul.output_tokens), 0) as total_output_tokens,
		COALESCE(SUM(ul.cache_creation_tokens), 0) as total_cache_creation_tokens,
		COALESCE(SUM(ul.cache_read_tokens), 0) as total_cache_read_tokens,
		COALESCE(SUM(ul.total_cost) FILTER (WHERE ul.provider_cost_recorded), 0) as total_cost,
		COALESCE(SUM(ul.actual_cost), 0) as total_actual_cost,
		COALESCE(SUM(ul.duration_ms), 0) as total_duration_ms,
		COUNT(ul.duration_ms) as duration_count,
		COUNT(*) FILTER (WHERE ul.created_at >= $2) as today_requests,
		COALESCE(SUM(ul.input_tokens) FILTER (WHERE ul.created_at >= $2), 0) as today_input_tokens,
		COALESCE(SUM(ul.output_tokens) FILTER (WHERE ul.created_at >= $2), 0) as today_output_tokens,
		COALESCE(SUM(ul.cache_creation_tokens) FILTER (WHERE ul.created_at >= $2), 0) as today_cache_creation_tokens,
		COALESCE(SUM(ul.cache_read_tokens) FILTER (WHERE ul.created_at >= $2), 0) as today_cache_read_tokens,
		COALESCE(SUM(ul.total_cost) FILTER (WHERE ul.created_at >= $2 AND ul.provider_cost_recorded), 0) as today_cost,
		COALESCE(SUM(ul.actual_cost) FILTER (WHERE ul.created_at >= $2), 0) as today_actual_cost,
		COUNT(*) FILTER (WHERE ` + usageLogSuccessFilterUL + `) as successful_requests,
		COUNT(*) FILTER (WHERE ul.created_at >= $2 AND ` + usageLogSuccessFilterUL + `) as today_successful_requests
	FROM usage_logs ul
	LEFT JOIN groups g ON g.id = ul.group_id
	LEFT JOIN accounts a ON a.id = ul.account_id
	WHERE ul.user_id = $1
	GROUP BY ` + usageLogEffectivePlatformExpr + `
	ORDER BY total_actual_cost DESC
`

// legacyGetUserDashboardStats is GetUserDashboardStats as it was before the
// split, copied verbatim apart from the receiver and the query constant name.
func legacyGetUserDashboardStats(ctx context.Context, r *usageLogRepository, userID int64) (*UserDashboardStats, error) {
	stats := &UserDashboardStats{}
	if err := scanSingleRow(ctx, r.sql, `
		SELECT COUNT(*), COUNT(*) FILTER (WHERE status = $2)
		FROM api_keys WHERE user_id = $1 AND deleted_at IS NULL`,
		[]any{userID, service.StatusActive}, &stats.TotalAPIKeys, &stats.ActiveAPIKeys,
	); err != nil {
		return nil, err
	}

	rows, err := r.sql.QueryContext(ctx, legacyUserDashboardUsageQuery, userID, timezone.Today())
	if err != nil {
		return nil, err
	}
	var byPlatform []PlatformDashboardStats
	var totalDuration, durationCount int64
	var totalCost, totalActualCost, todayCost, todayActualCost decimal.Decimal
	for rows.Next() {
		var row UserDashboardStats
		var platform sql.NullString
		var successfulRequests, todaySuccessfulRequests, rowDuration, rowDurationCount int64
		var rowTotalCost, rowActualCost, rowTodayCost, rowTodayActualCost decimal.Decimal
		if err := rows.Scan(
			&platform,
			&row.TotalRequests, &row.TotalInputTokens, &row.TotalOutputTokens,
			&row.TotalCacheCreationTokens, &row.TotalCacheReadTokens,
			&rowTotalCost, &rowActualCost, &rowDuration, &rowDurationCount,
			&row.TodayRequests, &row.TodayInputTokens, &row.TodayOutputTokens,
			&row.TodayCacheCreationTokens, &row.TodayCacheReadTokens,
			&rowTodayCost, &rowTodayActualCost,
			&successfulRequests, &todaySuccessfulRequests,
		); err != nil {
			_ = rows.Close()
			return nil, err
		}
		row.TotalTokens = row.TotalInputTokens + row.TotalOutputTokens + row.TotalCacheCreationTokens + row.TotalCacheReadTokens
		row.TodayTokens = row.TodayInputTokens + row.TodayOutputTokens + row.TodayCacheCreationTokens + row.TodayCacheReadTokens
		stats.TotalRequests += row.TotalRequests
		stats.TotalInputTokens += row.TotalInputTokens
		stats.TotalOutputTokens += row.TotalOutputTokens
		stats.TotalCacheCreationTokens += row.TotalCacheCreationTokens
		stats.TotalCacheReadTokens += row.TotalCacheReadTokens
		stats.TodayRequests += row.TodayRequests
		stats.TodayInputTokens += row.TodayInputTokens
		stats.TodayOutputTokens += row.TodayOutputTokens
		stats.TodayCacheCreationTokens += row.TodayCacheCreationTokens
		stats.TodayCacheReadTokens += row.TodayCacheReadTokens
		totalDuration += rowDuration
		durationCount += rowDurationCount
		// Database costs are NUMERIC. Preserve exact decimal sums across cohorts
		// before the final float conversion used by the existing response contract.
		totalCost = totalCost.Add(rowTotalCost)
		totalActualCost = totalActualCost.Add(rowActualCost)
		todayCost = todayCost.Add(rowTodayCost)
		todayActualCost = todayActualCost.Add(rowTodayActualCost)
		if platform.Valid && platform.String != "" {
			byPlatform = append(byPlatform, PlatformDashboardStats{
				Platform: platform.String, TotalRequests: successfulRequests,
				TotalTokens: row.TotalTokens, TotalActualCost: rowActualCost.InexactFloat64(),
				TodayRequests: todaySuccessfulRequests, TodayTokens: row.TodayTokens,
				TodayActualCost: rowTodayActualCost.InexactFloat64(),
			})
		}
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	stats.ByPlatform = byPlatform
	stats.TotalTokens = stats.TotalInputTokens + stats.TotalOutputTokens + stats.TotalCacheCreationTokens + stats.TotalCacheReadTokens
	stats.TodayTokens = stats.TodayInputTokens + stats.TodayOutputTokens + stats.TodayCacheCreationTokens + stats.TodayCacheReadTokens
	stats.TotalCost, stats.TotalActualCost = totalCost.InexactFloat64(), totalActualCost.InexactFloat64()
	stats.TodayCost, stats.TodayActualCost = todayCost.InexactFloat64(), todayActualCost.InexactFloat64()
	if durationCount > 0 {
		stats.AverageDurationMs = float64(totalDuration) / float64(durationCount)
	}

	// Keep the recent five-minute index range independent of the all-time scan.
	rpm, tpm, err := r.getPerformanceStats(ctx, userID)
	if err != nil {
		return nil, err
	}
	stats.Rpm, stats.Tpm = rpm, tpm
	return stats, nil
}

// legacyGetAPIKeyDashboardStats is GetAPIKeyDashboardStats as it was before the
// split, copied verbatim apart from the receiver.
func legacyGetAPIKeyDashboardStats(ctx context.Context, r *usageLogRepository, apiKeyID int64) (*UserDashboardStats, error) {
	stats := &UserDashboardStats{}
	today := timezone.Today()

	// API Key 维度不需要统计 key 数量，设为 1
	stats.TotalAPIKeys = 1
	stats.ActiveAPIKeys = 1

	// 累计 Token 统计
	totalStatsQuery := `
		SELECT
			COUNT(*) as total_requests,
			COALESCE(SUM(input_tokens), 0) as total_input_tokens,
			COALESCE(SUM(output_tokens), 0) as total_output_tokens,
			COALESCE(SUM(cache_creation_tokens), 0) as total_cache_creation_tokens,
			COALESCE(SUM(cache_read_tokens), 0) as total_cache_read_tokens,
			COALESCE(SUM(total_cost) FILTER (WHERE provider_cost_recorded), 0) as total_cost,
			COALESCE(SUM(actual_cost), 0) as total_actual_cost,
			COALESCE(AVG(duration_ms), 0) as avg_duration_ms
		FROM usage_logs
		WHERE api_key_id = $1
	`
	if err := scanSingleRow(
		ctx,
		r.sql,
		totalStatsQuery,
		[]any{apiKeyID},
		&stats.TotalRequests,
		&stats.TotalInputTokens,
		&stats.TotalOutputTokens,
		&stats.TotalCacheCreationTokens,
		&stats.TotalCacheReadTokens,
		&stats.TotalCost,
		&stats.TotalActualCost,
		&stats.AverageDurationMs,
	); err != nil {
		return nil, err
	}
	stats.TotalTokens = stats.TotalInputTokens + stats.TotalOutputTokens + stats.TotalCacheCreationTokens + stats.TotalCacheReadTokens

	// 今日 Token 统计
	todayStatsQuery := `
		SELECT
			COUNT(*) as today_requests,
			COALESCE(SUM(input_tokens), 0) as today_input_tokens,
			COALESCE(SUM(output_tokens), 0) as today_output_tokens,
			COALESCE(SUM(cache_creation_tokens), 0) as today_cache_creation_tokens,
			COALESCE(SUM(cache_read_tokens), 0) as today_cache_read_tokens,
			COALESCE(SUM(total_cost) FILTER (WHERE provider_cost_recorded), 0) as today_cost,
			COALESCE(SUM(actual_cost), 0) as today_actual_cost
		FROM usage_logs
		WHERE api_key_id = $1 AND created_at >= $2
	`
	if err := scanSingleRow(
		ctx,
		r.sql,
		todayStatsQuery,
		[]any{apiKeyID, today},
		&stats.TodayRequests,
		&stats.TodayInputTokens,
		&stats.TodayOutputTokens,
		&stats.TodayCacheCreationTokens,
		&stats.TodayCacheReadTokens,
		&stats.TodayCost,
		&stats.TodayActualCost,
	); err != nil {
		return nil, err
	}
	stats.TodayTokens = stats.TodayInputTokens + stats.TodayOutputTokens + stats.TodayCacheCreationTokens + stats.TodayCacheReadTokens

	// 性能指标：RPM 和 TPM（最近5分钟，按 API Key 过滤）
	rpm, tpm, err := r.getPerformanceStatsByAPIKey(ctx, apiKeyID)
	if err != nil {
		return nil, err
	}
	stats.Rpm = rpm
	stats.Tpm = tpm

	return stats, nil
}

type dashboardSplitEnv struct {
	openaiGroup, compositeGroup             int64
	anthropic, gemini, antigravity, unknown int64
}

type dashboardSplitRow struct {
	userID, apiKeyID, accountID int64
	groupID                     *int64
	at                          time.Time
	provenance                  *string
	recorded                    bool
	total, actual               float64
	duration                    *int
	tokens                      int
}

func (s *UsageLogRepoSuite) newDashboardSplitEnv(prefix string) dashboardSplitEnv {
	t := s.T()
	openai := mustCreateGroup(t, s.client, &service.Group{Name: prefix + "-openai", Platform: "openai"})
	composite := mustCreateGroup(t, s.client, &service.Group{Name: prefix + "-composite", Platform: "composite"})
	anthropic := mustCreateAccount(t, s.client, &service.Account{Name: prefix + "-anthropic", Platform: "anthropic"})
	gemini := mustCreateAccount(t, s.client, &service.Account{Name: prefix + "-gemini", Platform: "gemini"})
	antigravity := mustCreateAccount(t, s.client, &service.Account{Name: prefix + "-antigravity", Platform: "antigravity"})
	unknown := mustCreateAccount(t, s.client, &service.Account{Name: prefix + "-unknown"})
	// Ent rejects empty new platform values; model a historical unknown platform.
	_, err := s.repo.sql.ExecContext(s.ctx, "UPDATE accounts SET platform = '' WHERE id = $1", unknown.ID)
	s.Require().NoError(err)
	return dashboardSplitEnv{
		openaiGroup: openai.ID, compositeGroup: composite.ID,
		anthropic: anthropic.ID, gemini: gemini.ID, antigravity: antigravity.ID, unknown: unknown.ID,
	}
}

func (s *UsageLogRepoSuite) newDashboardSplitUser(email string) (userID, firstKey, secondKey int64) {
	user := mustCreateUser(s.T(), s.client, &service.User{Email: email})
	first := mustCreateApiKey(s.T(), s.client, &service.APIKey{UserID: user.ID, Key: "sk-" + uuid.NewString(), Name: "first"})
	second := mustCreateApiKey(s.T(), s.client, &service.APIKey{UserID: user.ID, Key: "sk-" + uuid.NewString(), Name: "second"})
	return user.ID, first.ID, second.ID
}

func (s *UsageLogRepoSuite) insertDashboardSplitRows(rows []dashboardSplitRow) {
	for i, row := range rows {
		log := &service.UsageLog{
			UserID: row.userID, APIKeyID: row.apiKeyID, AccountID: row.accountID, GroupID: row.groupID,
			RequestID: uuid.NewString(), Model: "dashboard-split", CreatedAt: row.at,
			InputTokens: row.tokens, OutputTokens: row.tokens*2 + 1, CacheCreationTokens: row.tokens / 2, CacheReadTokens: row.tokens + 3,
			TotalCost: row.total, ActualCost: row.actual, DurationMs: row.duration,
			BillingProvenance: row.provenance, ProviderCostRecorded: row.recorded,
		}
		_, err := s.repo.Create(s.ctx, log)
		s.Require().NoError(err, "row %d", i)
		// Create records ordinary rows as provider-cost recorded; persist the
		// follower/import shape explicitly.
		if !row.recorded {
			_, err = s.repo.sql.ExecContext(s.ctx, "UPDATE usage_logs SET provider_cost_recorded = false WHERE id = $1", log.ID)
			s.Require().NoError(err)
		}
	}
}

func dashboardSplitIntPtr(v int) *int { return &v }

func dashboardSplitStrPtr(v string) *string { return &v }

// dashboardSplitHistoryRows spans every platform cohort before today's start,
// including the last microsecond before midnight.
func dashboardSplitHistoryRows(env dashboardSplitEnv, userID, firstKey, secondKey int64, today time.Time) []dashboardSplitRow {
	failed, estimated := dashboardSplitStrPtr("failed_upstream"), dashboardSplitStrPtr("estimated")
	return []dashboardSplitRow{
		{userID, firstKey, env.anthropic, &env.openaiGroup, today.Add(-72 * time.Hour), nil, true, 6.0000000001, 5.1234567891, dashboardSplitIntPtr(100), 11},
		{userID, firstKey, env.anthropic, &env.openaiGroup, today.Add(-time.Microsecond), failed, true, 0.5, 0.3333333333, dashboardSplitIntPtr(250), 7},
		{userID, secondKey, env.anthropic, &env.compositeGroup, today.Add(-26 * time.Hour), estimated, true, 1.5, 1.0000000001, dashboardSplitIntPtr(333), 13},
		{userID, firstKey, env.gemini, nil, today.Add(-26 * time.Hour), nil, false, 3, 2.2222222222, nil, 17},
		{userID, firstKey, env.unknown, nil, today.Add(-72 * time.Hour), nil, true, 10.1234567891, 9.9999999999, dashboardSplitIntPtr(400), 19},
	}
}

// dashboardSplitTodayRows starts exactly at today's start and covers billed
// failures, zero-cost requests, NULL durations and unrecorded provider cost.
func dashboardSplitTodayRows(env dashboardSplitEnv, userID, firstKey, secondKey int64, today time.Time) []dashboardSplitRow {
	failed := dashboardSplitStrPtr("failed_upstream")
	return []dashboardSplitRow{
		{userID, firstKey, env.anthropic, &env.openaiGroup, today, nil, false, 0.2222222222, 0.1111111111, nil, 23},
		{userID, secondKey, env.anthropic, &env.compositeGroup, today.Add(time.Hour), nil, true, 8.8888888888, 7.7777777777, dashboardSplitIntPtr(777), 29},
		{userID, firstKey, env.anthropic, &env.compositeGroup, today.Add(2 * time.Hour), nil, true, 0.0000000001, 0, dashboardSplitIntPtr(50), 31},
		{userID, secondKey, env.gemini, nil, today.Add(time.Hour), failed, true, 0.6, 0.4444444444, dashboardSplitIntPtr(999), 37},
		{userID, secondKey, env.antigravity, nil, today, nil, true, 0.0000000002, 0.0000000001, dashboardSplitIntPtr(1), 41},
		{userID, secondKey, env.unknown, nil, today.Add(2 * time.Hour), nil, false, 0.7, 0.5555555555, nil, 43},
	}
}

func (s *UsageLogRepoSuite) requireUserDashboardMatchesLegacy(repo *usageLogRepository, userID int64) *UserDashboardStats {
	want, err := legacyGetUserDashboardStats(s.ctx, s.repo, userID)
	s.Require().NoError(err)
	got, err := repo.GetUserDashboardStats(s.ctx, userID)
	s.Require().NoError(err)
	s.Require().Equal(want, got)
	return got
}

// requireAPIKeyDashboardMatchesLegacy compares every field exactly except the
// average duration: the legacy AVG(duration_ms) returns NUMERIC rounded to 16
// significant digits, while SUM/COUNT divides exactly before rounding to float64.
func (s *UsageLogRepoSuite) requireAPIKeyDashboardMatchesLegacy(repo *usageLogRepository, apiKeyID int64) *UserDashboardStats {
	want, err := legacyGetAPIKeyDashboardStats(s.ctx, s.repo, apiKeyID)
	s.Require().NoError(err)
	got, err := repo.GetAPIKeyDashboardStats(s.ctx, apiKeyID)
	s.Require().NoError(err)
	s.Require().InDelta(want.AverageDurationMs, got.AverageDurationMs, 1e-9)
	wantCopy, gotCopy := *want, *got
	wantCopy.AverageDurationMs, gotCopy.AverageDurationMs = 0, 0
	s.Require().Equal(wantCopy, gotCopy)
	return got
}

func (s *UsageLogRepoSuite) TestDashboardSplit_MatchesLegacyAllHistoryQueries() {
	env := s.newDashboardSplitEnv("dashboard-split-mixed")
	today := timezone.Today()
	userID, firstKey, secondKey := s.newDashboardSplitUser("dashboard-split-mixed@test.com")
	otherUser, otherKey, _ := s.newDashboardSplitUser("dashboard-split-other@test.com")
	s.insertDashboardSplitRows(dashboardSplitHistoryRows(env, userID, firstKey, secondKey, today))
	s.insertDashboardSplitRows(dashboardSplitTodayRows(env, userID, firstKey, secondKey, today))
	s.insertDashboardSplitRows([]dashboardSplitRow{
		{otherUser, otherKey, env.gemini, nil, today.Add(-time.Hour), nil, true, 50, 40, dashboardSplitIntPtr(5), 3},
		{otherUser, otherKey, env.anthropic, &env.openaiGroup, today.Add(time.Hour), nil, true, 60, 50, dashboardSplitIntPtr(6), 5},
	})

	cached := newUsageLogRepositoryWithSQL(s.client, s.tx)
	cached.dashboardHistoryCache = usagequery.NewDetachedCache(time.Minute, time.Minute)
	for _, repo := range []*usageLogRepository{s.repo, cached} {
		stats := s.requireUserDashboardMatchesLegacy(repo, userID)
		s.Require().Equal(int64(11), stats.TotalRequests)
		s.Require().Equal(int64(6), stats.TodayRequests)
		s.Require().Len(stats.ByPlatform, 4, "the unknown platform stays in totals only")
		s.Require().Equal([]string{"anthropic", "openai", "gemini", "antigravity"}, []string{
			stats.ByPlatform[0].Platform, stats.ByPlatform[1].Platform, stats.ByPlatform[2].Platform, stats.ByPlatform[3].Platform,
		}, "combined totals decide the order, not either segment alone")
		s.requireAPIKeyDashboardMatchesLegacy(repo, firstKey)
		s.requireAPIKeyDashboardMatchesLegacy(repo, secondKey)
		s.requireUserDashboardMatchesLegacy(repo, otherUser)
		s.requireAPIKeyDashboardMatchesLegacy(repo, otherKey)
	}
}

func (s *UsageLogRepoSuite) TestDashboardSplit_HistoryOnly() {
	env := s.newDashboardSplitEnv("dashboard-split-history")
	today := timezone.Today()
	userID, firstKey, secondKey := s.newDashboardSplitUser("dashboard-split-history@test.com")
	s.insertDashboardSplitRows(dashboardSplitHistoryRows(env, userID, firstKey, secondKey, today))

	stats := s.requireUserDashboardMatchesLegacy(s.repo, userID)
	s.Require().Equal(int64(5), stats.TotalRequests)
	s.Require().Zero(stats.TodayRequests)
	s.Require().Zero(stats.TodayActualCost)
	s.requireAPIKeyDashboardMatchesLegacy(s.repo, firstKey)
	s.requireAPIKeyDashboardMatchesLegacy(s.repo, secondKey)
}

func (s *UsageLogRepoSuite) TestDashboardSplit_TodayOnly() {
	env := s.newDashboardSplitEnv("dashboard-split-today")
	today := timezone.Today()
	userID, firstKey, secondKey := s.newDashboardSplitUser("dashboard-split-today@test.com")
	s.insertDashboardSplitRows(dashboardSplitTodayRows(env, userID, firstKey, secondKey, today))

	stats := s.requireUserDashboardMatchesLegacy(s.repo, userID)
	s.Require().Equal(int64(6), stats.TotalRequests)
	s.Require().Equal(stats.TotalRequests, stats.TodayRequests)
	s.Require().Equal(stats.TotalActualCost, stats.TodayActualCost)
	s.requireAPIKeyDashboardMatchesLegacy(s.repo, firstKey)
	s.requireAPIKeyDashboardMatchesLegacy(s.repo, secondKey)
}

func (s *UsageLogRepoSuite) TestDashboardSplit_NoData() {
	userID, firstKey, _ := s.newDashboardSplitUser("dashboard-split-empty@test.com")

	stats := s.requireUserDashboardMatchesLegacy(s.repo, userID)
	s.Require().Zero(stats.TotalRequests)
	s.Require().Nil(stats.ByPlatform)
	keyStats := s.requireAPIKeyDashboardMatchesLegacy(s.repo, firstKey)
	s.Require().Zero(keyStats.TotalRequests)
	s.Require().Zero(keyStats.AverageDurationMs)
}

// dashboardHistoryCountingExec counts history-segment queries and can hold them
// before they reach the database.
type dashboardHistoryCountingExec struct {
	sqlExecutor
	mu      sync.Mutex
	history int
	gate    chan struct{}
	entered chan struct{}
}

func (e *dashboardHistoryCountingExec) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	if strings.Contains(query, "created_at < $2") {
		e.mu.Lock()
		e.history++
		gate, entered := e.gate, e.entered
		e.mu.Unlock()
		if gate != nil {
			entered <- struct{}{}
			<-gate
		}
	}
	return e.sqlExecutor.QueryContext(ctx, query, args...)
}

func (e *dashboardHistoryCountingExec) historyQueries() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.history
}

func (s *UsageLogRepoSuite) newCachedDashboardRepo() (*usageLogRepository, *dashboardHistoryCountingExec) {
	exec := &dashboardHistoryCountingExec{sqlExecutor: s.tx}
	repo := newUsageLogRepositoryWithSQL(s.client, exec)
	repo.dashboardHistoryCache = usagequery.NewDetachedCache(time.Minute, time.Minute)
	return repo, exec
}

func (s *UsageLogRepoSuite) TestDashboardHistoryCache_ReusesHistoryAndReflectsNewTodayRows() {
	env := s.newDashboardSplitEnv("dashboard-cache-reuse")
	today := timezone.Today()
	userID, firstKey, secondKey := s.newDashboardSplitUser("dashboard-cache-reuse@test.com")
	s.insertDashboardSplitRows(dashboardSplitHistoryRows(env, userID, firstKey, secondKey, today))
	s.insertDashboardSplitRows(dashboardSplitTodayRows(env, userID, firstKey, secondKey, today))
	repo, exec := s.newCachedDashboardRepo()

	first := s.requireUserDashboardMatchesLegacy(repo, userID)
	firstKeyStats := s.requireAPIKeyDashboardMatchesLegacy(repo, firstKey)
	s.Require().Equal(2, exec.historyQueries())

	s.insertDashboardSplitRows([]dashboardSplitRow{
		{userID, firstKey, env.gemini, nil, today.Add(3 * time.Hour), nil, true, 1.25, 1.0000000003, dashboardSplitIntPtr(10), 47},
	})
	second := s.requireUserDashboardMatchesLegacy(repo, userID)
	secondKeyStats := s.requireAPIKeyDashboardMatchesLegacy(repo, firstKey)
	s.Require().Equal(2, exec.historyQueries(), "cached history must not be scanned again")
	s.Require().Equal(first.TotalRequests+1, second.TotalRequests)
	s.Require().Equal(first.TodayRequests+1, second.TodayRequests)
	s.Require().Equal(firstKeyStats.TodayRequests+1, secondKeyStats.TodayRequests)
}

func (s *UsageLogRepoSuite) TestDashboardHistoryCache_AbandonedLoadStillPopulatesCache() {
	env := s.newDashboardSplitEnv("dashboard-cache-abandoned")
	today := timezone.Today()
	userID, firstKey, secondKey := s.newDashboardSplitUser("dashboard-cache-abandoned@test.com")
	s.insertDashboardSplitRows(dashboardSplitHistoryRows(env, userID, firstKey, secondKey, today))
	repo, exec := s.newCachedDashboardRepo()
	exec.gate, exec.entered = make(chan struct{}), make(chan struct{}, 1)

	ctx, cancel := context.WithCancel(s.ctx)
	result := make(chan error, 1)
	go func() {
		_, err := repo.GetUserDashboardStats(ctx, userID)
		result <- err
	}()
	select {
	case <-exec.entered:
	case <-time.After(10 * time.Second):
		s.FailNow("history segment query never started")
	}
	cancel()
	s.Require().ErrorIs(<-result, context.Canceled)

	exec.mu.Lock()
	gate := exec.gate
	exec.gate = nil
	exec.mu.Unlock()
	close(gate)
	// Wait for the detached load before reusing the single transaction connection.
	key := dashboardHistoryCacheKey("user", userID, today)
	s.Require().Eventually(func() bool {
		_, hit := repo.dashboardHistoryCache.Get(key)
		return hit
	}, 10*time.Second, 5*time.Millisecond)

	s.requireUserDashboardMatchesLegacy(repo, userID)
	s.Require().Equal(1, exec.historyQueries(), "the abandoned history scan must be reused, not repeated")
}

func (s *UsageLogRepoSuite) TestDashboardHistoryCache_SeparatesUsersAndAPIKeys() {
	env := s.newDashboardSplitEnv("dashboard-cache-isolation")
	today := timezone.Today()
	firstUser, firstUserKey, firstUserSecondKey := s.newDashboardSplitUser("dashboard-cache-isolation-1@test.com")
	secondUser, secondUserKey, _ := s.newDashboardSplitUser("dashboard-cache-isolation-2@test.com")
	s.insertDashboardSplitRows(dashboardSplitHistoryRows(env, firstUser, firstUserKey, firstUserSecondKey, today))
	s.insertDashboardSplitRows([]dashboardSplitRow{
		{secondUser, secondUserKey, env.gemini, nil, today.Add(-time.Hour), nil, true, 50, 40, dashboardSplitIntPtr(5), 3},
	})
	repo, exec := s.newCachedDashboardRepo()

	for range 2 {
		first := s.requireUserDashboardMatchesLegacy(repo, firstUser)
		second := s.requireUserDashboardMatchesLegacy(repo, secondUser)
		s.Require().NotEqual(first.TotalRequests, second.TotalRequests)
		s.requireAPIKeyDashboardMatchesLegacy(repo, firstUserKey)
		s.requireAPIKeyDashboardMatchesLegacy(repo, firstUserSecondKey)
		s.requireAPIKeyDashboardMatchesLegacy(repo, secondUserKey)
	}
	s.Require().Equal(5, exec.historyQueries(), "each user and key loads its own history exactly once")
}
