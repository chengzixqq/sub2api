package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/shopspring/decimal"
)

// getPerformanceStats 获取 RPM 和 TPM（近5分钟平均值，可选按用户过滤）
func (r *usageLogRepository) getPerformanceStats(ctx context.Context, userID int64) (rpm, tpm int64, err error) {
	fiveMinutesAgo := time.Now().Add(-5 * time.Minute)
	query := `
		SELECT
			COUNT(*) as request_count,
			COALESCE(SUM(input_tokens + output_tokens), 0) as token_count
		FROM usage_logs
		WHERE created_at >= $1`
	args := []any{fiveMinutesAgo}
	if userID > 0 {
		query += " AND user_id = $2"
		args = append(args, userID)
	}

	var requestCount int64
	var tokenCount int64
	if err := scanSingleRow(ctx, r.sql, query, args, &requestCount, &tokenCount); err != nil {
		return 0, 0, err
	}
	return requestCount / 5, tokenCount / 5, nil
}

// UserStats 用户使用统计
type UserStats struct {
	TotalRequests   int64   `json:"total_requests"`
	TotalTokens     int64   `json:"total_tokens"`
	TotalCost       float64 `json:"total_cost"`
	InputTokens     int64   `json:"input_tokens"`
	OutputTokens    int64   `json:"output_tokens"`
	CacheReadTokens int64   `json:"cache_read_tokens"`
}

func (r *usageLogRepository) GetUserStats(ctx context.Context, userID int64, startTime, endTime time.Time) (*UserStats, error) {
	query := `
		SELECT
			COUNT(*) as total_requests,
			COALESCE(SUM(input_tokens + output_tokens + cache_creation_tokens + cache_read_tokens), 0) as total_tokens,
			COALESCE(SUM(actual_cost), 0) as total_cost,
			COALESCE(SUM(input_tokens), 0) as input_tokens,
			COALESCE(SUM(output_tokens), 0) as output_tokens,
			COALESCE(SUM(cache_read_tokens), 0) as cache_read_tokens
		FROM usage_logs
		WHERE user_id = $1 AND created_at >= $2 AND created_at < $3
	`

	stats := &UserStats{}
	if err := scanSingleRow(
		ctx,
		r.sql,
		query,
		[]any{userID, startTime, endTime},
		&stats.TotalRequests,
		&stats.TotalTokens,
		&stats.TotalCost,
		&stats.InputTokens,
		&stats.OutputTokens,
		&stats.CacheReadTokens,
	); err != nil {
		return nil, err
	}
	return stats, nil
}

// DashboardStats 仪表盘统计
type DashboardStats = usagestats.DashboardStats

func (r *usageLogRepository) GetDashboardStats(ctx context.Context) (*DashboardStats, error) {
	stats := &DashboardStats{}
	now := timezone.Now()
	todayStart := timezone.Today()

	if err := r.fillDashboardEntityStats(ctx, stats, todayStart, now); err != nil {
		return nil, err
	}
	if err := r.fillDashboardUsageStatsAggregated(ctx, stats, todayStart, now); err != nil {
		return nil, err
	}

	rpm, tpm, err := r.getPerformanceStats(ctx, 0)
	if err != nil {
		return nil, err
	}
	stats.Rpm = rpm
	stats.Tpm = tpm

	return stats, nil
}

func (r *usageLogRepository) GetDashboardStatsWithRange(ctx context.Context, start, end time.Time) (*DashboardStats, error) {
	startUTC := start.UTC()
	endUTC := end.UTC()
	if !endUTC.After(startUTC) {
		return nil, errors.New("统计时间范围无效")
	}

	stats := &DashboardStats{}
	now := timezone.Now()
	todayStart := timezone.Today()

	if err := r.fillDashboardEntityStats(ctx, stats, todayStart, now); err != nil {
		return nil, err
	}
	if err := r.fillDashboardUsageStatsFromUsageLogs(ctx, stats, startUTC, endUTC, todayStart, now); err != nil {
		return nil, err
	}

	rpm, tpm, err := r.getPerformanceStats(ctx, 0)
	if err != nil {
		return nil, err
	}
	stats.Rpm = rpm
	stats.Tpm = tpm

	return stats, nil
}

func (r *usageLogRepository) fillDashboardEntityStats(ctx context.Context, stats *DashboardStats, todayUTC, now time.Time) error {
	userStatsQuery := `
		SELECT
			COUNT(*) as total_users,
			COUNT(CASE WHEN created_at >= $1 THEN 1 END) as today_new_users
		FROM users
		WHERE deleted_at IS NULL
	`
	if err := scanSingleRow(
		ctx,
		r.sql,
		userStatsQuery,
		[]any{todayUTC},
		&stats.TotalUsers,
		&stats.TodayNewUsers,
	); err != nil {
		return err
	}

	apiKeyStatsQuery := `
		SELECT
			COUNT(*) as total_api_keys,
			COUNT(CASE WHEN status = $1 THEN 1 END) as active_api_keys
		FROM api_keys
		WHERE deleted_at IS NULL
	`
	if err := scanSingleRow(
		ctx,
		r.sql,
		apiKeyStatsQuery,
		[]any{service.StatusActive},
		&stats.TotalAPIKeys,
		&stats.ActiveAPIKeys,
	); err != nil {
		return err
	}

	accountStatsQuery := `
		SELECT
			COUNT(*) as total_accounts,
			COUNT(CASE WHEN status = $1 AND schedulable = true THEN 1 END) as normal_accounts,
			COUNT(CASE WHEN status = $2 THEN 1 END) as error_accounts,
			COUNT(CASE WHEN rate_limited_at IS NOT NULL AND rate_limit_reset_at > $3 THEN 1 END) as ratelimit_accounts,
			COUNT(CASE WHEN overload_until IS NOT NULL AND overload_until > $4 THEN 1 END) as overload_accounts
		FROM accounts
		WHERE deleted_at IS NULL
	`
	if err := scanSingleRow(
		ctx,
		r.sql,
		accountStatsQuery,
		[]any{service.StatusActive, service.StatusError, now, now},
		&stats.TotalAccounts,
		&stats.NormalAccounts,
		&stats.ErrorAccounts,
		&stats.RateLimitAccounts,
		&stats.OverloadAccounts,
	); err != nil {
		return err
	}

	return nil
}

func (r *usageLogRepository) fillDashboardUsageStatsAggregated(ctx context.Context, stats *DashboardStats, todayUTC, now time.Time) error {
	totalStatsQuery := `
		SELECT
			COALESCE(SUM(total_requests), 0) as total_requests,
			COALESCE(SUM(input_tokens), 0) as total_input_tokens,
			COALESCE(SUM(output_tokens), 0) as total_output_tokens,
			COALESCE(SUM(cache_creation_tokens), 0) as total_cache_creation_tokens,
			COALESCE(SUM(cache_read_tokens), 0) as total_cache_read_tokens,
			COALESCE(SUM(total_cost), 0) as total_cost,
			COALESCE(SUM(actual_cost), 0) as total_actual_cost,
			COALESCE(SUM(account_cost), 0) as total_account_cost,
			COALESCE(SUM(total_duration_ms), 0) as total_duration_ms
		FROM usage_dashboard_daily
	`
	var totalDurationMs int64
	if err := scanSingleRow(
		ctx,
		r.sql,
		totalStatsQuery,
		nil,
		&stats.TotalRequests,
		&stats.TotalInputTokens,
		&stats.TotalOutputTokens,
		&stats.TotalCacheCreationTokens,
		&stats.TotalCacheReadTokens,
		&stats.TotalCost,
		&stats.TotalActualCost,
		&stats.TotalAccountCost,
		&totalDurationMs,
	); err != nil {
		return err
	}
	stats.TotalTokens = stats.TotalInputTokens + stats.TotalOutputTokens + stats.TotalCacheCreationTokens + stats.TotalCacheReadTokens
	if stats.TotalRequests > 0 {
		stats.AverageDurationMs = float64(totalDurationMs) / float64(stats.TotalRequests)
	}

	todayStatsQuery := `
		SELECT
			total_requests as today_requests,
			input_tokens as today_input_tokens,
			output_tokens as today_output_tokens,
			cache_creation_tokens as today_cache_creation_tokens,
			cache_read_tokens as today_cache_read_tokens,
			total_cost as today_cost,
			actual_cost as today_actual_cost,
			account_cost as today_account_cost,
			active_users as active_users
		FROM usage_dashboard_daily
		WHERE bucket_date = $1::date
	`
	if err := scanSingleRow(
		ctx,
		r.sql,
		todayStatsQuery,
		[]any{todayUTC},
		&stats.TodayRequests,
		&stats.TodayInputTokens,
		&stats.TodayOutputTokens,
		&stats.TodayCacheCreationTokens,
		&stats.TodayCacheReadTokens,
		&stats.TodayCost,
		&stats.TodayActualCost,
		&stats.TodayAccountCost,
		&stats.ActiveUsers,
	); err != nil {
		if err != sql.ErrNoRows {
			return err
		}
	}
	stats.TodayTokens = stats.TodayInputTokens + stats.TodayOutputTokens + stats.TodayCacheCreationTokens + stats.TodayCacheReadTokens

	hourlyActiveQuery := `
		SELECT active_users
		FROM usage_dashboard_hourly
		WHERE bucket_start = $1
	`
	hourStart := truncateToHour(now.In(timezone.Location()))
	if err := scanSingleRow(ctx, r.sql, hourlyActiveQuery, []any{hourStart}, &stats.HourlyActiveUsers); err != nil {
		if err != sql.ErrNoRows {
			return err
		}
	}

	return nil
}

func (r *usageLogRepository) fillDashboardUsageStatsFromUsageLogs(ctx context.Context, stats *DashboardStats, startUTC, endUTC, todayUTC, now time.Time) error {
	todayEnd := todayUTC.Add(24 * time.Hour)
	combinedStatsQuery := `
		WITH scoped AS (
			SELECT
				created_at,
				provider_cost_recorded,
				input_tokens,
				output_tokens,
				cache_creation_tokens,
				cache_read_tokens,
				CASE WHEN provider_cost_recorded THEN total_cost ELSE 0 END AS total_cost,
				actual_cost,
				CASE WHEN provider_cost_recorded THEN COALESCE(account_stats_cost, total_cost) * COALESCE(account_rate_multiplier, 1) ELSE 0 END AS account_cost,
				COALESCE(duration_ms, 0) AS duration_ms
			FROM usage_logs
			WHERE created_at >= LEAST($1::timestamptz, $3::timestamptz)
				AND created_at < GREATEST($2::timestamptz, $4::timestamptz)
		)
		SELECT
			COUNT(*) FILTER (WHERE created_at >= $1::timestamptz AND created_at < $2::timestamptz) AS total_requests,
			COALESCE(SUM(input_tokens) FILTER (WHERE created_at >= $1::timestamptz AND created_at < $2::timestamptz), 0) AS total_input_tokens,
			COALESCE(SUM(output_tokens) FILTER (WHERE created_at >= $1::timestamptz AND created_at < $2::timestamptz), 0) AS total_output_tokens,
			COALESCE(SUM(cache_creation_tokens) FILTER (WHERE created_at >= $1::timestamptz AND created_at < $2::timestamptz), 0) AS total_cache_creation_tokens,
			COALESCE(SUM(cache_read_tokens) FILTER (WHERE created_at >= $1::timestamptz AND created_at < $2::timestamptz), 0) AS total_cache_read_tokens,
			COALESCE(SUM(total_cost) FILTER (WHERE provider_cost_recorded AND created_at >= $1::timestamptz AND created_at < $2::timestamptz), 0) AS total_cost,
			COALESCE(SUM(actual_cost) FILTER (WHERE created_at >= $1::timestamptz AND created_at < $2::timestamptz), 0) AS total_actual_cost,
			COALESCE(SUM(account_cost) FILTER (WHERE created_at >= $1::timestamptz AND created_at < $2::timestamptz), 0) AS total_account_cost,
			COALESCE(SUM(duration_ms) FILTER (WHERE created_at >= $1::timestamptz AND created_at < $2::timestamptz), 0) AS total_duration_ms,
			COUNT(*) FILTER (WHERE created_at >= $3::timestamptz AND created_at < $4::timestamptz) AS today_requests,
			COALESCE(SUM(input_tokens) FILTER (WHERE created_at >= $3::timestamptz AND created_at < $4::timestamptz), 0) AS today_input_tokens,
			COALESCE(SUM(output_tokens) FILTER (WHERE created_at >= $3::timestamptz AND created_at < $4::timestamptz), 0) AS today_output_tokens,
			COALESCE(SUM(cache_creation_tokens) FILTER (WHERE created_at >= $3::timestamptz AND created_at < $4::timestamptz), 0) AS today_cache_creation_tokens,
			COALESCE(SUM(cache_read_tokens) FILTER (WHERE created_at >= $3::timestamptz AND created_at < $4::timestamptz), 0) AS today_cache_read_tokens,
			COALESCE(SUM(total_cost) FILTER (WHERE provider_cost_recorded AND created_at >= $3::timestamptz AND created_at < $4::timestamptz), 0) AS today_cost,
			COALESCE(SUM(actual_cost) FILTER (WHERE created_at >= $3::timestamptz AND created_at < $4::timestamptz), 0) AS today_actual_cost,
			COALESCE(SUM(account_cost) FILTER (WHERE created_at >= $3::timestamptz AND created_at < $4::timestamptz), 0) AS today_account_cost
		FROM scoped
	`
	var totalDurationMs int64
	if err := scanSingleRow(
		ctx,
		r.sql,
		combinedStatsQuery,
		[]any{startUTC, endUTC, todayUTC, todayEnd},
		&stats.TotalRequests,
		&stats.TotalInputTokens,
		&stats.TotalOutputTokens,
		&stats.TotalCacheCreationTokens,
		&stats.TotalCacheReadTokens,
		&stats.TotalCost,
		&stats.TotalActualCost,
		&stats.TotalAccountCost,
		&totalDurationMs,
		&stats.TodayRequests,
		&stats.TodayInputTokens,
		&stats.TodayOutputTokens,
		&stats.TodayCacheCreationTokens,
		&stats.TodayCacheReadTokens,
		&stats.TodayCost,
		&stats.TodayActualCost,
		&stats.TodayAccountCost,
	); err != nil {
		return err
	}
	stats.TotalTokens = stats.TotalInputTokens + stats.TotalOutputTokens + stats.TotalCacheCreationTokens + stats.TotalCacheReadTokens
	if stats.TotalRequests > 0 {
		stats.AverageDurationMs = float64(totalDurationMs) / float64(stats.TotalRequests)
	}

	stats.TodayTokens = stats.TodayInputTokens + stats.TodayOutputTokens + stats.TodayCacheCreationTokens + stats.TodayCacheReadTokens

	hourStart := now.UTC().Truncate(time.Hour)
	hourEnd := hourStart.Add(time.Hour)
	activeUsersQuery := `
		WITH scoped AS (
			SELECT user_id, created_at
			FROM usage_logs
			WHERE created_at >= LEAST($1::timestamptz, $3::timestamptz)
				AND created_at < GREATEST($2::timestamptz, $4::timestamptz)
		)
		SELECT
			COUNT(DISTINCT CASE WHEN created_at >= $1::timestamptz AND created_at < $2::timestamptz THEN user_id END) AS active_users,
			COUNT(DISTINCT CASE WHEN created_at >= $3::timestamptz AND created_at < $4::timestamptz THEN user_id END) AS hourly_active_users
		FROM scoped
	`
	if err := scanSingleRow(ctx, r.sql, activeUsersQuery, []any{todayUTC, todayEnd, hourStart, hourEnd}, &stats.ActiveUsers, &stats.HourlyActiveUsers); err != nil {
		return err
	}

	return nil
}

// UserDashboardStats 用户仪表盘统计
type UserDashboardStats = usagestats.UserDashboardStats

// PlatformDashboardStats 单平台用量明细
type PlatformDashboardStats = usagestats.PlatformDashboardStats

const (
	// History before today's start barely changes within a day. The day start is
	// part of the cache key, so entries retire at midnight.
	dashboardHistoryCacheTTL         = 15 * time.Minute
	dashboardHistoryCacheWorkTimeout = 2 * time.Minute
)

func dashboardHistoryCacheKey(kind string, id int64, todayStart time.Time) string {
	return fmt.Sprintf("%s:%d:%d", kind, id, todayStart.Unix())
}

// userDashboardSegmentSelect aggregates one side of today's start per platform.
// Grand totals are combined from every platform cohort, including unknown ones.
// Ordinary GROUP BY retains parallel partial aggregation eligibility. Only the
// platform request counts exclude billed failures; their costs and tokens remain.
const userDashboardSegmentSelect = `
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
		COUNT(*) FILTER (WHERE ` + usageLogSuccessFilterUL + `) as successful_requests
	FROM usage_logs ul
	LEFT JOIN groups g ON g.id = ul.group_id
	LEFT JOIN accounts a ON a.id = ul.account_id
	WHERE ul.user_id = $1 AND `

const userDashboardSegmentGroupBy = `
	GROUP BY ` + usageLogEffectivePlatformExpr

const (
	userDashboardHistoryQuery = userDashboardSegmentSelect + `ul.created_at < $2` + userDashboardSegmentGroupBy
	userDashboardTodayQuery   = userDashboardSegmentSelect + `ul.created_at >= $2` + userDashboardSegmentGroupBy
)

// dashboardUsageTotals is one cohort's sums. Values are combined into new copies.
type dashboardUsageTotals struct {
	requests            int64
	inputTokens         int64
	outputTokens        int64
	cacheCreationTokens int64
	cacheReadTokens     int64
	cost                decimal.Decimal
	actualCost          decimal.Decimal
	durationMs          int64
	durationCount       int64
}

func (t dashboardUsageTotals) tokens() int64 {
	return t.inputTokens + t.outputTokens + t.cacheCreationTokens + t.cacheReadTokens
}

func (t dashboardUsageTotals) plus(o dashboardUsageTotals) dashboardUsageTotals {
	return dashboardUsageTotals{
		requests:            t.requests + o.requests,
		inputTokens:         t.inputTokens + o.inputTokens,
		outputTokens:        t.outputTokens + o.outputTokens,
		cacheCreationTokens: t.cacheCreationTokens + o.cacheCreationTokens,
		cacheReadTokens:     t.cacheReadTokens + o.cacheReadTokens,
		// Database costs are NUMERIC. Preserve exact decimal sums across cohorts
		// before the final float conversion used by the existing response contract.
		cost:          t.cost.Add(o.cost),
		actualCost:    t.actualCost.Add(o.actualCost),
		durationMs:    t.durationMs + o.durationMs,
		durationCount: t.durationCount + o.durationCount,
	}
}

type dashboardPlatformTotals struct {
	platform           string
	successfulRequests int64
	tokens             int64
	actualCost         decimal.Decimal
}

func (p dashboardPlatformTotals) plus(o dashboardPlatformTotals) dashboardPlatformTotals {
	return dashboardPlatformTotals{
		platform:           p.platform,
		successfulRequests: p.successfulRequests + o.successfulRequests,
		tokens:             p.tokens + o.tokens,
		actualCost:         p.actualCost.Add(o.actualCost),
	}
}

// dashboardUsageSegment is one side of today's start. Cached history segments
// are shared between requests and must never be modified.
type dashboardUsageSegment struct {
	totals    dashboardUsageTotals
	platforms []dashboardPlatformTotals
}

// loadDashboardHistory reads usage before todayStart through the detached cache
// when enabled, so an abandoned scan still serves later requests that day.
func (r *usageLogRepository) loadDashboardHistory(ctx context.Context, kind string, id int64, todayStart time.Time, load func(context.Context) (*dashboardUsageSegment, error)) (*dashboardUsageSegment, error) {
	if r.dashboardHistoryCache == nil {
		return load(ctx)
	}
	entry, _, err := r.dashboardHistoryCache.GetOrLoad(ctx, dashboardHistoryCacheKey(kind, id, todayStart), false, func(work context.Context) (any, error) {
		return load(work)
	})
	if err != nil {
		return nil, err
	}
	segment, ok := entry.Payload.(*dashboardUsageSegment)
	if !ok || segment == nil {
		return nil, errors.New("unexpected dashboard history cache payload")
	}
	return segment, nil
}

func (r *usageLogRepository) queryUserDashboardSegment(ctx context.Context, query string, userID int64, todayStart time.Time) (*dashboardUsageSegment, error) {
	rows, err := r.sql.QueryContext(ctx, query, userID, todayStart)
	if err != nil {
		return nil, err
	}
	segment := &dashboardUsageSegment{}
	for rows.Next() {
		var row dashboardUsageTotals
		var platform sql.NullString
		var successfulRequests int64
		if err := rows.Scan(
			&platform,
			&row.requests, &row.inputTokens, &row.outputTokens,
			&row.cacheCreationTokens, &row.cacheReadTokens,
			&row.cost, &row.actualCost, &row.durationMs, &row.durationCount,
			&successfulRequests,
		); err != nil {
			_ = rows.Close()
			return nil, err
		}
		segment.totals = segment.totals.plus(row)
		if platform.Valid && platform.String != "" {
			segment.platforms = append(segment.platforms, dashboardPlatformTotals{
				platform: platform.String, successfulRequests: successfulRequests,
				tokens: row.tokens(), actualCost: row.actualCost,
			})
		}
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return segment, nil
}

// applyDashboardSegments writes history+today totals and today's figures into
// stats without modifying either segment.
func applyDashboardSegments(stats *UserDashboardStats, history, today *dashboardUsageSegment) {
	total := history.totals.plus(today.totals)
	stats.TotalRequests = total.requests
	stats.TotalInputTokens = total.inputTokens
	stats.TotalOutputTokens = total.outputTokens
	stats.TotalCacheCreationTokens = total.cacheCreationTokens
	stats.TotalCacheReadTokens = total.cacheReadTokens
	stats.TotalTokens = total.tokens()
	stats.TotalCost, stats.TotalActualCost = total.cost.InexactFloat64(), total.actualCost.InexactFloat64()
	if total.durationCount > 0 {
		stats.AverageDurationMs = float64(total.durationMs) / float64(total.durationCount)
	}
	stats.TodayRequests = today.totals.requests
	stats.TodayInputTokens = today.totals.inputTokens
	stats.TodayOutputTokens = today.totals.outputTokens
	stats.TodayCacheCreationTokens = today.totals.cacheCreationTokens
	stats.TodayCacheReadTokens = today.totals.cacheReadTokens
	stats.TodayTokens = today.totals.tokens()
	stats.TodayCost, stats.TodayActualCost = today.totals.cost.InexactFloat64(), today.totals.actualCost.InexactFloat64()
	stats.ByPlatform = mergeDashboardPlatforms(history.platforms, today.platforms)
}

// mergeDashboardPlatforms combines both segments per platform and orders them by
// the combined exact actual cost, highest first.
func mergeDashboardPlatforms(history, today []dashboardPlatformTotals) []PlatformDashboardStats {
	type platformTotals struct{ total, today dashboardPlatformTotals }
	var order []*platformTotals
	byName := make(map[string]*platformTotals, len(history)+len(today))
	entry := func(name string) *platformTotals {
		p, ok := byName[name]
		if !ok {
			p = &platformTotals{total: dashboardPlatformTotals{platform: name}, today: dashboardPlatformTotals{platform: name}}
			byName[name] = p
			order = append(order, p)
		}
		return p
	}
	for _, row := range history {
		p := entry(row.platform)
		p.total = p.total.plus(row)
	}
	for _, row := range today {
		p := entry(row.platform)
		p.total = p.total.plus(row)
		p.today = p.today.plus(row)
	}
	if len(order) == 0 {
		return nil
	}
	slices.SortStableFunc(order, func(a, b *platformTotals) int {
		if c := b.total.actualCost.Cmp(a.total.actualCost); c != 0 {
			return c
		}
		return strings.Compare(a.total.platform, b.total.platform)
	})
	merged := make([]PlatformDashboardStats, 0, len(order))
	for _, p := range order {
		merged = append(merged, PlatformDashboardStats{
			Platform: p.total.platform, TotalRequests: p.total.successfulRequests,
			TotalTokens: p.total.tokens, TotalActualCost: p.total.actualCost.InexactFloat64(),
			TodayRequests: p.today.successfulRequests, TodayTokens: p.today.tokens,
			TodayActualCost: p.today.actualCost.InexactFloat64(),
		})
	}
	return merged
}

// GetUserDashboardStats 获取用户专属的仪表盘统计
func (r *usageLogRepository) GetUserDashboardStats(ctx context.Context, userID int64) (*UserDashboardStats, error) {
	stats := &UserDashboardStats{}
	if err := scanSingleRow(ctx, r.sql, `
		SELECT COUNT(*), COUNT(*) FILTER (WHERE status = $2)
		FROM api_keys WHERE user_id = $1 AND deleted_at IS NULL`,
		[]any{userID, service.StatusActive}, &stats.TotalAPIKeys, &stats.ActiveAPIKeys,
	); err != nil {
		return nil, err
	}

	// History before today's start is cached when enabled; today's index range
	// on (user_id, created_at) is always read live.
	todayStart := timezone.Today()
	history, err := r.loadDashboardHistory(ctx, "user", userID, todayStart, func(ctx context.Context) (*dashboardUsageSegment, error) {
		return r.queryUserDashboardSegment(ctx, userDashboardHistoryQuery, userID, todayStart)
	})
	if err != nil {
		return nil, err
	}
	today, err := r.queryUserDashboardSegment(ctx, userDashboardTodayQuery, userID, todayStart)
	if err != nil {
		return nil, err
	}
	applyDashboardSegments(stats, history, today)

	// Keep the recent five-minute index range independent of the history scan.
	rpm, tpm, err := r.getPerformanceStats(ctx, userID)
	if err != nil {
		return nil, err
	}
	stats.Rpm, stats.Tpm = rpm, tpm
	return stats, nil
}

// getPerformanceStatsByAPIKey 获取指定 API Key 的 RPM 和 TPM（近5分钟平均值）
func (r *usageLogRepository) getPerformanceStatsByAPIKey(ctx context.Context, apiKeyID int64) (rpm, tpm int64, err error) {
	fiveMinutesAgo := time.Now().Add(-5 * time.Minute)
	query := `
		SELECT
			COUNT(*) as request_count,
			COALESCE(SUM(input_tokens + output_tokens + cache_creation_tokens + cache_read_tokens), 0) as token_count
		FROM usage_logs
		WHERE created_at >= $1 AND api_key_id = $2`
	args := []any{fiveMinutesAgo, apiKeyID}

	var requestCount int64
	var tokenCount int64
	if err := scanSingleRow(ctx, r.sql, query, args, &requestCount, &tokenCount); err != nil {
		return 0, 0, err
	}
	return requestCount / 5, tokenCount / 5, nil
}

// apiKeyDashboardSegmentSelect aggregates one side of today's start for an API
// Key. Duration sums and counts combine across segments into SUM/COUNT, the
// same value as AVG over every non-NULL duration.
const apiKeyDashboardSegmentSelect = `
	SELECT
		COUNT(*) as total_requests,
		COALESCE(SUM(input_tokens), 0) as total_input_tokens,
		COALESCE(SUM(output_tokens), 0) as total_output_tokens,
		COALESCE(SUM(cache_creation_tokens), 0) as total_cache_creation_tokens,
		COALESCE(SUM(cache_read_tokens), 0) as total_cache_read_tokens,
		COALESCE(SUM(total_cost) FILTER (WHERE provider_cost_recorded), 0) as total_cost,
		COALESCE(SUM(actual_cost), 0) as total_actual_cost,
		COALESCE(SUM(duration_ms), 0) as total_duration_ms,
		COUNT(duration_ms) as duration_count
	FROM usage_logs
	WHERE api_key_id = $1 AND `

const (
	apiKeyDashboardHistoryQuery = apiKeyDashboardSegmentSelect + `created_at < $2`
	apiKeyDashboardTodayQuery   = apiKeyDashboardSegmentSelect + `created_at >= $2`
)

func (r *usageLogRepository) queryAPIKeyDashboardSegment(ctx context.Context, query string, apiKeyID int64, todayStart time.Time) (*dashboardUsageSegment, error) {
	segment := &dashboardUsageSegment{}
	t := &segment.totals
	if err := scanSingleRow(
		ctx,
		r.sql,
		query,
		[]any{apiKeyID, todayStart},
		&t.requests,
		&t.inputTokens,
		&t.outputTokens,
		&t.cacheCreationTokens,
		&t.cacheReadTokens,
		&t.cost,
		&t.actualCost,
		&t.durationMs,
		&t.durationCount,
	); err != nil {
		return nil, err
	}
	return segment, nil
}

// GetAPIKeyDashboardStats 获取指定 API Key 的仪表盘统计（按 api_key_id 过滤）
func (r *usageLogRepository) GetAPIKeyDashboardStats(ctx context.Context, apiKeyID int64) (*UserDashboardStats, error) {
	stats := &UserDashboardStats{}
	todayStart := timezone.Today()

	// API Key 维度不需要统计 key 数量，设为 1
	stats.TotalAPIKeys = 1
	stats.ActiveAPIKeys = 1

	// 今日零点之前的累计统计可走缓存；今日统计走 (api_key_id, created_at) 索引现查
	history, err := r.loadDashboardHistory(ctx, "api_key", apiKeyID, todayStart, func(ctx context.Context) (*dashboardUsageSegment, error) {
		return r.queryAPIKeyDashboardSegment(ctx, apiKeyDashboardHistoryQuery, apiKeyID, todayStart)
	})
	if err != nil {
		return nil, err
	}
	today, err := r.queryAPIKeyDashboardSegment(ctx, apiKeyDashboardTodayQuery, apiKeyID, todayStart)
	if err != nil {
		return nil, err
	}
	applyDashboardSegments(stats, history, today)

	// 性能指标：RPM 和 TPM（最近5分钟，按 API Key 过滤）
	rpm, tpm, err := r.getPerformanceStatsByAPIKey(ctx, apiKeyID)
	if err != nil {
		return nil, err
	}
	stats.Rpm = rpm
	stats.Tpm = tpm

	return stats, nil
}
