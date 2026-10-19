//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// dashboard/request-types splits the usage/stats totals by effective request
// type. Every bucket must equal usage/stats?request_type=<type>, including
// rows written before request_type existed (request_type = 0), which are
// classified from the legacy stream / openai_ws_mode flags.
func TestUsageLogRequestTypeStats_MatchUsageStats(t *testing.T) {
	tx := testEntTx(t)
	client := tx.Client()
	repo := newUsageLogRepositoryWithSQL(client, tx)
	account := mustCreateAccount(t, client, &service.Account{Name: "rt-" + uuid.NewString()})
	other := mustCreateAccount(t, client, &service.Account{Name: "rt-other-" + uuid.NewString()})
	user := mustCreateUser(t, client, &service.User{Email: uuid.NewString() + "@request-types.test"})
	key := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID, Key: uuid.NewString(), Name: "request-types"})
	start := time.Date(2019, 4, 2, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	ctx := context.Background()

	upstream := "rt-upstream-" + uuid.NewString()
	half := 0.5
	statsCost := 0.3
	type row struct {
		requestType service.RequestType
		tokens      int
		cost        float64
		actual      float64
		multiplier  *float64
		statsCost   *float64
		upstream    *string
		createdAt   time.Time
	}
	create := func(r row) int64 {
		log := &service.UsageLog{
			UserID: user.ID, APIKeyID: key.ID, AccountID: account.ID, RequestID: uuid.NewString(),
			Model: "rt-model", RequestedModel: "rt-model", UpstreamModel: r.upstream,
			InputTokens: r.tokens, TotalCost: r.cost, ActualCost: r.actual,
			AccountRateMultiplier: r.multiplier, AccountStatsCost: r.statsCost,
			RequestType: r.requestType, ProviderCostRecorded: true, CreatedAt: r.createdAt,
		}
		_, err := repo.Create(ctx, log)
		require.NoError(t, err)
		return log.ID
	}
	at := start.Add(time.Hour)
	// Explicit types: sync 3, stream 5, ws_v2 3, live 2, cyber 1.
	create(row{requestType: service.RequestTypeSync, tokens: 10, cost: 1, actual: 0.8, createdAt: at})
	create(row{requestType: service.RequestTypeSync, tokens: 20, cost: 2, actual: 1.6, multiplier: &half, createdAt: at})
	unrecorded := create(row{requestType: service.RequestTypeSync, tokens: 30, cost: 7, actual: 0.5, createdAt: at})
	for i := 0; i < 5; i++ {
		create(row{requestType: service.RequestTypeStream, tokens: 100, cost: 0.4, actual: 0.3, upstream: &upstream, createdAt: at})
	}
	for i := 0; i < 3; i++ {
		create(row{requestType: service.RequestTypeWSV2, tokens: 7, cost: 0.2, actual: 0.1, createdAt: at})
	}
	create(row{requestType: service.RequestTypeLive, tokens: 3, cost: 1, actual: 0.9, multiplier: &half, createdAt: at})
	create(row{requestType: service.RequestTypeLive, tokens: 4, cost: 1, actual: 0.9, multiplier: &half, statsCost: &statsCost, createdAt: at})
	create(row{requestType: service.RequestTypeCyberBlocked, tokens: 1, cost: 0.05, actual: 0, upstream: &upstream, createdAt: at})
	// Legacy rows: (stream, openai_ws_mode) -> sync, stream, ws_v2, ws_v2.
	for _, legacy := range []struct{ stream, ws bool }{{false, false}, {true, false}, {true, true}, {false, true}} {
		id := create(row{requestType: service.RequestTypeSync, tokens: 5, cost: 0.1, actual: 0.1, createdAt: at})
		_, err := tx.ExecContext(ctx, "UPDATE usage_logs SET request_type = 0, stream = $2, openai_ws_mode = $3 WHERE id = $1", id, legacy.stream, legacy.ws)
		require.NoError(t, err)
	}
	// Not billed by the provider: counts toward requests and actual_cost only.
	_, err := tx.ExecContext(ctx, "UPDATE usage_logs SET provider_cost_recorded = false WHERE id = $1", unrecorded)
	require.NoError(t, err)
	// Outside the window.
	create(row{requestType: service.RequestTypeLive, tokens: 999, cost: 99, actual: 99, createdAt: end})

	filters := UsageLogFilters{AccountID: account.ID}
	got, err := repo.GetRequestTypeStatsWithUsageFilters(ctx, start, end, filters)
	require.NoError(t, err)

	types := make([]string, 0, len(got))
	for _, item := range got {
		types = append(types, item.RequestType)
	}
	require.Equal(t, []string{"stream", "ws_v2", "sync", "live", "cyber"}, types)
	for i := 1; i < len(got); i++ {
		require.Greater(t, got[i-1].Requests, got[i].Requests, "requests must be strictly descending here")
	}
	bucket := func(name string) usagestats.RequestTypeStat {
		for _, item := range got {
			if item.RequestType == name {
				return item
			}
		}
		t.Fatalf("missing request type %s", name)
		return usagestats.RequestTypeStat{}
	}
	syncBucket := bucket("sync")
	require.Equal(t, int64(4), syncBucket.Requests)
	require.Equal(t, int64(65), syncBucket.TotalTokens)
	require.InDelta(t, 3.1, syncBucket.Cost, 1e-9, "the unrecorded row's total_cost is excluded")
	require.InDelta(t, 3.0, syncBucket.ActualCost, 1e-9, "actual_cost counts every row")
	require.InDelta(t, 2.1, syncBucket.AccountCost, 1e-9, "account_cost applies the rate multiplier")
	live := bucket("live")
	require.InDelta(t, 0.65, live.AccountCost, 1e-9, "account_stats_cost replaces total_cost before the multiplier")

	stats, err := repo.GetStatsWithFilters(ctx, requestTypeStatsRange(filters, start, end))
	require.NoError(t, err)
	var requests, tokens int64
	var cost, actual, accountCost float64
	for _, item := range got {
		requests += item.Requests
		tokens += item.TotalTokens
		cost += item.Cost
		actual += item.ActualCost
		accountCost += item.AccountCost
	}
	require.Equal(t, stats.TotalRequests, requests)
	require.Equal(t, stats.TotalTokens, tokens)
	require.InDelta(t, stats.TotalCost, cost, 1e-9)
	require.InDelta(t, stats.TotalActualCost, actual, 1e-9)
	require.NotNil(t, stats.TotalAccountCost)
	require.InDelta(t, *stats.TotalAccountCost, accountCost, 1e-9)

	for _, item := range got {
		parsed, err := service.ParseUsageRequestType(item.RequestType)
		require.NoError(t, err)
		requestType := int16(parsed)
		single := requestTypeStatsRange(filters, start, end)
		single.RequestType = &requestType
		want, err := repo.GetStatsWithFilters(ctx, single)
		require.NoError(t, err)
		require.Equal(t, want.TotalRequests, item.Requests, item.RequestType)
		require.Equal(t, want.TotalTokens, item.TotalTokens, item.RequestType)
		require.InDelta(t, want.TotalCost, item.Cost, 1e-9, item.RequestType)
		require.InDelta(t, want.TotalActualCost, item.ActualCost, 1e-9, item.RequestType)
		require.NotNil(t, want.TotalAccountCost, item.RequestType)
		require.InDelta(t, *want.TotalAccountCost, item.AccountCost, 1e-9, item.RequestType)
	}

	scoped, err := repo.GetRequestTypeStatsWithUsageFilters(service.WithUsageAccountScope(ctx, []int64{other.ID}), start, end, filters)
	require.NoError(t, err)
	require.NotNil(t, scoped)
	require.Empty(t, scoped, "the account whitelist still applies")

	byUpstream := UsageLogFilters{AccountID: account.ID, Model: upstream, ModelFilterSource: usagestats.ModelSourceUpstream}
	upstreamOnly, err := repo.GetRequestTypeStatsWithUsageFilters(ctx, start, end, byUpstream)
	require.NoError(t, err)
	require.Len(t, upstreamOnly, 2)
	require.Equal(t, "stream", upstreamOnly[0].RequestType)
	require.Equal(t, int64(5), upstreamOnly[0].Requests)
	require.Equal(t, "cyber", upstreamOnly[1].RequestType)
	require.Equal(t, int64(1), upstreamOnly[1].Requests)
	requested, err := repo.GetRequestTypeStatsWithUsageFilters(ctx, start, end, UsageLogFilters{AccountID: account.ID, Model: upstream, ModelFilterSource: usagestats.ModelSourceRequested})
	require.NoError(t, err)
	require.Empty(t, requested, "model_source=requested matches the requested model, not the upstream one")
}

func requestTypeStatsRange(filters UsageLogFilters, start, end time.Time) UsageLogFilters {
	filters.StartTime, filters.EndTime = &start, &end
	return filters
}
