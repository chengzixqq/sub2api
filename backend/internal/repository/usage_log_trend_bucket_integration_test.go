//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagequery"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// Ranking is over the complete filtered range, while the returned points retain
// local-date buckets. Provider costs, customer charges, and request counts have
// separate meanings, including historical billed failures and probe followers.
func TestUsageLogTrendBucket_RankingPreservesRangeScopeAndBilling(t *testing.T) {
	tx := testEntTx(t)
	client := tx.Client()
	repo := newUsageLogRepositoryWithSQL(client, tx)
	userA := mustCreateUser(t, client, &service.User{Email: uuid.NewString() + "@bucket.test", Username: "spender"})
	userB := mustCreateUser(t, client, &service.User{Email: uuid.NewString() + "@bucket.test", Username: "tokens"})
	keyA := mustCreateApiKey(t, client, &service.APIKey{UserID: userA.ID, Key: uuid.NewString(), Name: "a"})
	keyA2 := mustCreateApiKey(t, client, &service.APIKey{UserID: userA.ID, Key: uuid.NewString(), Name: "a2"})
	keyB := mustCreateApiKey(t, client, &service.APIKey{UserID: userB.ID, Key: uuid.NewString(), Name: "b"})
	account := mustCreateAccount(t, client, &service.Account{Name: uuid.NewString()})
	otherAccount := mustCreateAccount(t, client, &service.Account{Name: uuid.NewString()})
	start := time.Date(2022, 1, 1, 15, 30, 0, 0, time.UTC)
	end := start.Add(48 * time.Hour)
	ctx := usagequery.WithOptions(service.WithUsageAccountScope(context.Background(), []int64{account.ID}), usagequery.Options{Timezone: "Asia/Shanghai"})
	mode, failure := "token", "failed_upstream"
	create := func(user *service.User, key *service.APIKey, at time.Time, tokens int, cost, actual float64, recorded bool, accountID int64, model string, provenance *string) {
		log := &service.UsageLog{
			UserID: user.ID, APIKeyID: key.ID, AccountID: accountID,
			RequestID: uuid.NewString(), Model: model, BillingMode: &mode,
			InputTokens: tokens, TotalCost: cost, ActualCost: actual,
			ProviderCostRecorded: true, BillingProvenance: provenance, CreatedAt: at,
		}
		_, err := repo.Create(context.Background(), log)
		require.NoError(t, err)
		if !recorded {
			_, err = tx.ExecContext(context.Background(), "UPDATE usage_logs SET provider_cost_recorded = false WHERE id = $1", log.ID)
			require.NoError(t, err)
		}
	}
	create(userA, keyA, start, 100, 2, 1, true, account.ID, "target", nil)
	create(userA, keyA, start.Add(2*time.Hour), 20, 9, 2, false, account.ID, "target", nil)
	create(userA, keyA, start.Add(3*time.Hour), 30, 3, 13, true, account.ID, "target", &failure)
	create(userA, keyA2, start.Add(27*time.Hour), 1, 1, 1, true, account.ID, "target", nil)
	create(userB, keyB, start, 400, 4, 4, true, account.ID, "target", nil)
	create(userB, keyB, start.Add(26*time.Hour), 50, 5, 5, true, account.ID, "target", nil)
	// None of these rows may influence either the ranking or its global totals.
	create(userA, keyA, start.Add(-time.Second), 999, 99, 99, true, account.ID, "target", nil)
	create(userA, keyA, end, 999, 99, 99, true, account.ID, "target", nil)
	create(userA, keyA, start.Add(time.Hour), 999, 99, 99, true, otherAccount.ID, "target", nil)
	create(userA, keyA, start.Add(time.Hour), 999, 99, 99, true, account.ID, "other-model", nil)
	filters := UsageLogFilters{Model: "target", BillingMode: mode}

	points, err := repo.getUserUsageTrendWithFilters(ctx, start, end, "day", 1, "actual_cost", filters)
	require.NoError(t, err)
	require.Equal(t, []usagestats.UserUsageTrendPoint{
		{Date: "2022-01-01", UserID: userA.ID, Email: userA.Email, Username: userA.Username, Requests: 1, Tokens: 100, Cost: 2, ActualCost: 1},
		{Date: "2022-01-02", UserID: userA.ID, Email: userA.Email, Username: userA.Username, Requests: 2, Tokens: 50, Cost: 3, ActualCost: 15},
		{Date: "2022-01-03", UserID: userA.ID, Email: userA.Email, Username: userA.Username, Requests: 1, Tokens: 1, Cost: 1, ActualCost: 1},
	}, points)
	points, err = repo.GetUserUsageTrendWithFilters(ctx, start, end, "day", 1, filters)
	require.NoError(t, err)
	require.Len(t, points, 2)
	require.Equal(t, userB.ID, points[0].UserID)
	require.Equal(t, "2022-01-01", points[0].Date)
	require.Equal(t, int64(400), points[0].Tokens)
	require.Equal(t, "2022-01-03", points[1].Date)
	require.Equal(t, int64(50), points[1].Tokens)

	keys, err := repo.GetAPIKeyUsageTrendWithFilters(ctx, start, end, "day", 1, filters)
	require.NoError(t, err)
	require.Equal(t, []usagestats.APIKeyUsageTrendPoint{
		{Date: "2022-01-01", APIKeyID: keyB.ID, KeyName: keyB.Name, Requests: 1, Tokens: 400},
		{Date: "2022-01-03", APIKeyID: keyB.ID, KeyName: keyB.Name, Requests: 1, Tokens: 50},
	}, keys)

	ranking, err := repo.GetUserSpendingRankingWithFilters(ctx, start, end, 1, filters)
	require.NoError(t, err)
	require.Equal(t, &usagestats.UserSpendingRankingResponse{
		Ranking:         []usagestats.UserSpendingRankingItem{{UserID: userA.ID, Email: userA.Email, Username: userA.Username, ActualCost: 17, Cost: 6, AccountCost: 6, Requests: 4, Tokens: 151}},
		TotalUsers:      2,
		TotalActualCost: 26, TotalCost: 15, TotalAccountCost: 15, TotalRequests: 6, TotalTokens: 601,
	}, ranking)

	filters.APIKeyID = keyA.ID
	keys, err = repo.GetAPIKeyUsageTrendWithFilters(ctx, start, end, "hour", 1, filters)
	require.NoError(t, err)
	require.Len(t, keys, 3)
	require.Equal(t, "2022-01-01 23:00", keys[0].Date)
	require.Equal(t, "2022-01-02 01:00", keys[1].Date)
	require.Equal(t, "2022-01-02 02:00", keys[2].Date)
	for _, point := range keys {
		require.Equal(t, keyA.ID, point.APIKeyID)
	}
	filters.APIKeyID = keyA2.ID
	ranking, err = repo.GetUserSpendingRankingWithFilters(ctx, start, end, 0, filters)
	require.NoError(t, err)
	require.Equal(t, int64(1), ranking.TotalRequests)
	require.Equal(t, 1.0, ranking.TotalActualCost)
	require.Equal(t, int64(1), ranking.TotalTokens)

	empty, err := repo.GetUserSpendingRankingWithFilters(ctx, end, end.Add(time.Hour), 1, UsageLogFilters{Model: "nonexistent"})
	require.NoError(t, err)
	require.Empty(t, empty.Ranking)
	require.Zero(t, empty.TotalActualCost)
	require.Zero(t, empty.TotalRequests)
	require.Zero(t, empty.TotalTokens)
}

func TestUsageLogTrendBucket_CalendarAndRepeatedHourSemantics(t *testing.T) {
	fallBack := []time.Time{
		time.Date(2024, 11, 3, 5, 30, 0, 0, time.UTC),
		time.Date(2024, 11, 3, 6, 30, 0, 0, time.UTC),
	}
	yearBoundary := []time.Time{
		time.Date(2020, 12, 31, 23, 30, 0, 0, time.UTC),
		time.Date(2021, 1, 1, 0, 30, 0, 0, time.UTC),
	}
	for _, tc := range []struct {
		name, granularity, zone, sessionZone string
		times                                []time.Time
		dates                                []string
		requests                             []int64
	}{
		{"explicit zone repeated hour", "hour", "America/New_York", "UTC", fallBack, []string{"2024-11-03 01:00"}, []int64{2}},
		{"session zone repeated hour", "hour", "", "America/New_York", fallBack, []string{"2024-11-03 01:00"}, []int64{2}},
		{"ISO week across calendar year", "week", "UTC", "UTC", yearBoundary, []string{"2020-53"}, []int64{2}},
		{"separate calendar months", "month", "UTC", "UTC", yearBoundary, []string{"2020-12", "2021-01"}, []int64{1, 1}},
		{"unknown granularity remains daily", "unsupported", "UTC", "UTC", yearBoundary, []string{"2020-12-31", "2021-01-01"}, []int64{1, 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tx := testEntTx(t)
			client := tx.Client()
			repo := newUsageLogRepositoryWithSQL(client, tx)
			_, err := tx.ExecContext(context.Background(), "SELECT set_config('TimeZone', $1, true)", tc.sessionZone)
			require.NoError(t, err)
			user := mustCreateUser(t, client, &service.User{Email: uuid.NewString() + "@calendar.test"})
			key := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID, Key: uuid.NewString(), Name: "calendar"})
			account := mustCreateAccount(t, client, &service.Account{Name: uuid.NewString()})
			for _, at := range tc.times {
				_, err := repo.Create(context.Background(), &service.UsageLog{
					UserID: user.ID, APIKeyID: key.ID, AccountID: account.ID, RequestID: uuid.NewString(),
					Model: "calendar", InputTokens: 3, OutputTokens: 2, CacheCreationTokens: 1, CacheReadTokens: 1,
					TotalCost: 2, ActualCost: 1, ProviderCostRecorded: true, CreatedAt: at,
				})
				require.NoError(t, err)
			}
			ctx := usagequery.WithOptions(context.Background(), usagequery.Options{Timezone: tc.zone})
			filters := UsageLogFilters{UserID: user.ID}
			start, end := tc.times[0].Add(-time.Hour), tc.times[1].Add(time.Hour)
			users, err := repo.GetUserUsageTrendWithFilters(ctx, start, end, tc.granularity, 1, filters)
			require.NoError(t, err)
			keys, err := repo.GetAPIKeyUsageTrendWithFilters(ctx, start, end, tc.granularity, 1, filters)
			require.NoError(t, err)
			require.Len(t, users, len(tc.dates))
			require.Len(t, keys, len(tc.dates))
			for i, date := range tc.dates {
				require.Equal(t, date, users[i].Date)
				require.Equal(t, date, keys[i].Date)
				require.Equal(t, tc.requests[i], users[i].Requests)
				require.Equal(t, tc.requests[i], keys[i].Requests)
				require.Equal(t, tc.requests[i]*7, users[i].Tokens)
				require.Equal(t, tc.requests[i]*7, keys[i].Tokens)
				require.Equal(t, float64(tc.requests[i])*2, users[i].Cost)
				require.Equal(t, float64(tc.requests[i]), users[i].ActualCost)
			}
		})
	}
}
