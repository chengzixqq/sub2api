//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagequery"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestUsageQuery_AllSectionsUseSameMinuteRangeAndFilters(t *testing.T) {
	tx := testEntTx(t)
	client := tx.Client()
	repo := newUsageLogRepositoryWithSQL(client, tx)
	user := mustCreateUser(t, client, &service.User{Email: uuid.NewString() + "@query.test"})
	group := mustCreateGroup(t, client, &service.Group{Name: "query-" + uuid.NewString()})
	key := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID, Key: uuid.NewString(), Name: "query"})
	account := mustCreateAccount(t, client, &service.Account{Name: uuid.NewString()})
	other := mustCreateAccount(t, client, &service.Account{Name: uuid.NewString()})
	start := time.Date(2022, 1, 1, 12, 15, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	ctx := usagequery.WithOptions(service.WithUsageAccountScope(context.Background(), []int64{account.ID}), usagequery.Options{Timezone: "Asia/Shanghai", ForceRefresh: true})
	create := func(at time.Time, model, mode string, accountID int64) {
		upstream := "upstream-model"
		log := &service.UsageLog{UserID: user.ID, APIKeyID: key.ID, AccountID: accountID, GroupID: &group.ID, RequestID: uuid.NewString(), Model: "mapped-model", RequestedModel: model, UpstreamModel: &upstream, UpstreamResponseModel: &upstream, BillingMode: &mode, InputTokens: 10, OutputTokens: 20, CacheCreationTokens: 5, CacheReadTokens: 7, TotalCost: 2, ActualCost: 1, ProviderCostRecorded: true, CreatedAt: at}
		_, err := repo.Create(context.Background(), log)
		require.NoError(t, err)
	}
	for _, at := range []time.Time{start.Add(-time.Second), start, start.Add(time.Hour), end.Add(-time.Second), end} {
		create(at, "requested-model", "token", account.ID)
	}
	create(start.Add(time.Minute), "other-model", "token", account.ID)
	create(start.Add(time.Minute), "requested-model", "image", account.ID)
	create(start.Add(time.Minute), "requested-model", "token", other.ID)
	filters := usagestats.UsageLogFilters{UserID: user.ID, APIKeyID: key.ID, GroupID: group.ID, Model: "requested-model", ModelFilterSource: usagestats.ModelSourceRequested, BillingMode: "token", StartTime: &start, EndTime: &end, ExactTotal: true}
	logs, page, err := repo.ListWithFilters(ctx, pagination.PaginationParams{Page: 1, PageSize: 20}, filters)
	require.NoError(t, err)
	require.Len(t, logs, 3)
	require.Equal(t, int64(3), page.Total)
	stats, err := repo.GetStatsWithFilters(ctx, filters)
	require.NoError(t, err)
	require.Equal(t, int64(3), stats.TotalRequests)
	require.Equal(t, 3.0, stats.TotalActualCost)
	models, err := repo.GetModelStatsWithUsageFiltersBySource(ctx, start, end, filters, usagestats.ModelSourceUpstream)
	require.NoError(t, err)
	require.Len(t, models, 1)
	require.Equal(t, "upstream-model", models[0].Model)
	require.Equal(t, stats.TotalRequests, models[0].Requests)
	require.Equal(t, stats.TotalActualCost, models[0].ActualCost)
	accountFilters := filters
	accountFilters.UserID, accountFilters.APIKeyID, accountFilters.AccountID = 0, 0, account.ID
	accountStats, err := repo.GetStatsWithFilters(ctx, accountFilters)
	require.NoError(t, err)
	accountModels, err := repo.GetModelStatsWithUsageFiltersBySource(ctx, start, end, accountFilters, usagestats.ModelSourceRequested)
	require.NoError(t, err)
	require.Len(t, accountModels, 1)
	require.Equal(t, accountStats.TotalActualCost, accountModels[0].ActualCost)
	groups, err := repo.GetGroupStatsWithUsageFilters(ctx, start, end, filters)
	require.NoError(t, err)
	require.Len(t, groups, 1)
	require.Equal(t, stats.TotalRequests, groups[0].Requests)
	require.Equal(t, stats.TotalActualCost, groups[0].ActualCost)
	trend, err := repo.GetUsageTrendWithUsageFilters(ctx, start, end, "hour", filters)
	require.NoError(t, err)
	var requests, tokens int64
	var cost float64
	for _, point := range trend {
		requests += point.Requests
		tokens += point.TotalTokens
		cost += point.ActualCost
	}
	require.Equal(t, stats.TotalRequests, requests)
	require.Equal(t, stats.TotalTokens, tokens)
	require.Equal(t, stats.TotalActualCost, cost)
	require.Equal(t, "2022-01-01 20:00", trend[0].Date)
	ranking, err := repo.GetUserSpendingRankingWithFilters(ctx, start, end, 12, filters)
	require.NoError(t, err)
	require.Equal(t, stats.TotalRequests, ranking.TotalRequests)
	require.Equal(t, stats.TotalActualCost, ranking.TotalActualCost)
	users, err := repo.GetUserUsageTrendWithFilters(ctx, start, end, "day", 12, filters)
	require.NoError(t, err)
	requests = 0
	for _, point := range users {
		requests += point.Requests
	}
	require.Equal(t, stats.TotalRequests, requests)
	keys, err := repo.GetAPIKeyUsageTrendWithFilters(ctx, start, end, "day", 12, filters)
	require.NoError(t, err)
	requests = 0
	for _, point := range keys {
		requests += point.Requests
	}
	require.Equal(t, stats.TotalRequests, requests)
	filters.ExactTotal = false
	filters.DeferredTotal = true
	logs, page, err = repo.ListWithFilters(ctx, pagination.PaginationParams{Page: 1, PageSize: 1}, filters)
	require.NoError(t, err)
	require.Len(t, logs, 1)
	require.Equal(t, int64(2), page.Total)
	filters.DeferredTotal = false
	filters.RequestID = logs[0].RequestID
	stats, err = repo.GetStatsWithFilters(ctx, filters)
	require.NoError(t, err)
	require.Equal(t, int64(1), stats.TotalRequests)
	models, err = repo.GetModelStatsWithUsageFiltersBySource(ctx, start, end, filters, usagestats.ModelSourceUpstream)
	require.NoError(t, err)
	require.Len(t, models, 1)
	require.Equal(t, int64(1), models[0].Requests)
}

func TestUsageQuery_AggregateGapsPartialEdgesAndStaleBuckets(t *testing.T) {
	tx := testEntTx(t)
	client := tx.Client()
	repo := newUsageLogRepositoryWithSQL(client, tx)
	user := mustCreateUser(t, client, &service.User{Email: uuid.NewString() + "@aggregate.test"})
	key := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID, Key: uuid.NewString(), Name: "query"})
	account := mustCreateAccount(t, client, &service.Account{Name: uuid.NewString()})
	base := time.Date(2021, 2, 3, 10, 0, 0, 0, time.UTC)
	for i := 0; i < 8; i++ {
		log := &service.UsageLog{UserID: user.ID, APIKeyID: key.ID, AccountID: account.ID, RequestID: uuid.NewString(), Model: "model", InputTokens: 1, OutputTokens: 2, TotalCost: 2, ActualCost: 1, ProviderCostRecorded: true, CreatedAt: base.Add(time.Duration(i) * 30 * time.Minute)}
		_, err := repo.Create(context.Background(), log)
		require.NoError(t, err)
	}
	_, err := tx.ExecContext(context.Background(), `UPDATE usage_dashboard_aggregation_watermark SET last_aggregated_at=$1,updated_at=CURRENT_TIMESTAMP WHERE id=1`, base.Add(4*time.Hour))
	require.NoError(t, err)
	// One valid complete bucket, one stale incorrect bucket and one absent bucket.
	_, err = tx.ExecContext(context.Background(), `INSERT INTO usage_dashboard_hourly(bucket_start,total_requests,input_tokens,output_tokens,total_cost,actual_cost,computed_at) VALUES ($1,2,2,4,4,2,CURRENT_TIMESTAMP),($2,999,999,999,999,999,CURRENT_TIMESTAMP-INTERVAL '1 hour') ON CONFLICT(bucket_start) DO UPDATE SET total_requests=EXCLUDED.total_requests,input_tokens=EXCLUDED.input_tokens,output_tokens=EXCLUDED.output_tokens,total_cost=EXCLUDED.total_cost,actual_cost=EXCLUDED.actual_cost,computed_at=EXCLUDED.computed_at`, base.Add(time.Hour), base.Add(2*time.Hour))
	require.NoError(t, err)
	start, end := base.Add(15*time.Minute), base.Add(3*time.Hour+15*time.Minute)
	filters := usagestats.UsageLogFilters{StartTime: &start, EndTime: &end}
	got, err := repo.GetUsageTrendWithUsageFilters(context.Background(), start, end, "hour", filters)
	require.NoError(t, err)
	rawCtx := usagequery.WithOptions(context.Background(), usagequery.Options{ForceRefresh: true})
	want, err := repo.GetUsageTrendWithUsageFilters(rawCtx, start, end, "hour", filters)
	require.NoError(t, err)
	require.Equal(t, want, got)
	var requests int64
	for _, point := range got {
		requests += point.Requests
	}
	require.Equal(t, int64(6), requests)
}
