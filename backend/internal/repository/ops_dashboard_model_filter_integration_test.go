//go:build integration

package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestOpsDashboardModelFilter_MatchesUsageStats(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	user := mustCreateUser(t, client, &service.User{})
	key := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID})
	account := mustCreateAccount(t, client, &service.Account{Name: "ops-model-filter", Platform: service.PlatformAnthropic})
	start := time.Date(2022, 6, 7, 8, 0, 0, 0, time.UTC)
	end := start.Add(30 * time.Minute)
	suffix := uuid.NewString()
	requested, upstream, other := "req-"+suffix, "up-"+suffix, "other-"+suffix
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(ctx, "DELETE FROM usage_logs WHERE user_id = $1", user.ID)
		_, _ = integrationDB.ExecContext(ctx, "DELETE FROM ops_error_logs WHERE user_id = $1", user.ID)
	})

	insertUsage := func(i int, model, requestedModel, upstreamModel string) {
		t.Helper()
		_, err := integrationDB.ExecContext(ctx, `INSERT INTO usage_logs
			(user_id, api_key_id, account_id, request_id, model, requested_model, upstream_model, input_tokens, output_tokens, duration_ms, created_at)
			VALUES ($1,$2,$3,$4,$5,NULLIF($6,''),NULLIF($7,''),100,50,1200,$8)`,
			user.ID, key.ID, account.ID, fmt.Sprintf("ops-model-%s-%d", suffix, i), model, requestedModel, upstreamModel, start.Add(time.Duration(i+1)*time.Minute))
		require.NoError(t, err)
	}
	insertUsage(0, requested, requested, upstream)
	insertUsage(1, requested, requested, upstream)
	insertUsage(2, other, other, "")

	insertError := func(model, requestedModel, upstreamModel string) {
		t.Helper()
		_, err := integrationDB.ExecContext(ctx, `INSERT INTO ops_error_logs
			(user_id, error_phase, error_type, severity, status_code, upstream_status_code, is_business_limited, error_owner, model, requested_model, upstream_model, created_at)
			VALUES ($1,'upstream','upstream_error','error',502,502,FALSE,'provider',$2,NULLIF($3,''),NULLIF($4,''),$5)`,
			user.ID, model, requestedModel, upstreamModel, start.Add(5*time.Minute))
		require.NoError(t, err)
	}
	insertError(requested, requested, upstream)
	insertError(other, other, "")

	repo := NewOpsRepository(integrationDB).(*opsRepository)
	usageRepo := newUsageLogRepositoryWithSQL(client, integrationDB)
	filter := func(model, source string, mode service.OpsQueryMode) *service.OpsDashboardFilter {
		return &service.OpsDashboardFilter{StartTime: start, EndTime: end, Model: model, ModelSource: source, QueryMode: mode}
	}

	overview, err := repo.GetDashboardOverview(ctx, filter(requested, "requested", service.OpsQueryModeRaw))
	require.NoError(t, err)
	require.EqualValues(t, 2, overview.SuccessCount)
	require.EqualValues(t, 1, overview.ErrorCountTotal)
	stats, err := usageRepo.GetStatsWithFilters(ctx, usagestats.UsageLogFilters{
		StartTime: &start, EndTime: &end, Model: requested, ModelFilterSource: usagestats.ModelSourceRequested,
	})
	require.NoError(t, err)
	require.Equal(t, stats.TotalRequests, overview.SuccessCount, "same counting rule as usage/stats")

	preagg, err := repo.GetDashboardOverview(ctx, filter(requested, "requested", service.OpsQueryModePreagg))
	require.NoError(t, err, "a model filter cannot be answered by pre-aggregated tables")
	require.EqualValues(t, 2, preagg.SuccessCount)

	upstreamOverview, err := repo.GetDashboardOverview(ctx, filter(upstream, "upstream", service.OpsQueryModeRaw))
	require.NoError(t, err)
	require.EqualValues(t, 2, upstreamOverview.SuccessCount)
	require.EqualValues(t, 1, upstreamOverview.ErrorCountTotal)
	requestedByUpstreamName, err := repo.GetDashboardOverview(ctx, filter(upstream, "requested", service.OpsQueryModeRaw))
	require.NoError(t, err)
	require.Zero(t, requestedByUpstreamName.SuccessCount)

	trend, err := repo.GetThroughputTrend(ctx, filter(requested, "requested", service.OpsQueryModeRaw), 60)
	require.NoError(t, err)
	var trendRequests int64
	for _, p := range trend.Points {
		trendRequests += p.RequestCount
	}
	require.EqualValues(t, 3, trendRequests, "2 successes + 1 error")
	require.Nil(t, trend.ByPlatform)
	require.Nil(t, trend.TopGroups)

	histogram, err := repo.GetLatencyHistogram(ctx, filter(requested, "requested", service.OpsQueryModeRaw))
	require.NoError(t, err)
	require.EqualValues(t, 2, histogram.TotalRequests)

	errTrend, err := repo.GetErrorTrend(ctx, filter(requested, "requested", service.OpsQueryModeRaw), 60)
	require.NoError(t, err)
	var errTotal int64
	for _, p := range errTrend.Points {
		errTotal += p.ErrorCountTotal
	}
	require.EqualValues(t, 1, errTotal)

	distribution, err := repo.GetErrorDistribution(ctx, filter(requested, "requested", service.OpsQueryModeRaw))
	require.NoError(t, err)
	require.EqualValues(t, 1, distribution.Total)

	tokenStats, err := repo.GetOpenAITokenStats(ctx, &service.OpsOpenAITokenStatsFilter{
		TimeRange: "30m", StartTime: start, EndTime: end, Model: requested, ModelSource: "requested", TopN: 10,
	})
	require.NoError(t, err)
	var tokenRequests int64
	for _, item := range tokenStats.Items {
		tokenRequests += item.RequestCount
	}
	require.EqualValues(t, 2, tokenRequests)
}
