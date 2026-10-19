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

// With filters, users-trend must pick its top users by the requested metric,
// the same users user-breakdown ranks first for that metric.
func TestUsageLogTrendBucket_FilteredUserTrendRanksByMetric(t *testing.T) {
	tx := testEntTx(t)
	client := tx.Client()
	repo := newUsageLogRepositoryWithSQL(client, tx)
	svc := service.NewDashboardService(repo, nil, nil, nil)
	ctx := usagequery.WithOptions(context.Background(), usagequery.Options{Timezone: "UTC"})

	group := mustCreateGroup(t, client, &service.Group{Name: "b12-" + uuid.NewString()})
	other := mustCreateGroup(t, client, &service.Group{Name: "b12-" + uuid.NewString()})
	spender := mustCreateUser(t, client, &service.User{Email: uuid.NewString() + "@metric.test", Username: "spender"})
	heavy := mustCreateUser(t, client, &service.User{Email: uuid.NewString() + "@metric.test", Username: "heavy"})
	outsider := mustCreateUser(t, client, &service.User{Email: uuid.NewString() + "@metric.test", Username: "outsider"})
	account := mustCreateAccount(t, client, &service.Account{Name: uuid.NewString()})
	start := time.Date(2022, 3, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(48 * time.Hour)

	create := func(user *service.User, groupID int64, at time.Time, tokens int, actual float64) {
		key := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID, Key: uuid.NewString(), Name: uuid.NewString()})
		_, err := repo.Create(ctx, &service.UsageLog{
			UserID: user.ID, APIKeyID: key.ID, AccountID: account.ID, GroupID: &groupID,
			RequestID: uuid.NewString(), Model: "b12", InputTokens: tokens,
			TotalCost: actual, ActualCost: actual, ProviderCostRecorded: true, CreatedAt: at,
		})
		require.NoError(t, err)
	}
	// spender: few tokens, high charge; heavy: many tokens, low charge.
	create(spender, group.ID, start.Add(time.Hour), 10, 30)
	create(spender, group.ID, start.Add(25*time.Hour), 20, 20)
	create(heavy, group.ID, start.Add(2*time.Hour), 900, 4)
	// outsider beats both on every metric but only outside the filtered group.
	create(outsider, other.ID, start.Add(3*time.Hour), 99999, 999)

	filters := UsageLogFilters{GroupID: group.ID}
	userIDs := func(points []usagestats.UserUsageTrendPoint) map[int64]bool {
		ids := map[int64]bool{}
		for _, point := range points {
			ids[point.UserID] = true
		}
		return ids
	}

	// Go through DashboardService so a duck-typing signature drift would fall
	// back to the tokens ranking and fail here.
	byCost, err := svc.GetUserUsageTrendWithFiltersByMetric(ctx, start, end, "day", 1, "actual_cost", filters)
	require.NoError(t, err)
	require.Equal(t, []usagestats.UserUsageTrendPoint{
		{Date: "2022-03-01", UserID: spender.ID, Email: spender.Email, Username: spender.Username, Requests: 1, Tokens: 10, Cost: 30, ActualCost: 30},
		{Date: "2022-03-02", UserID: spender.ID, Email: spender.Email, Username: spender.Username, Requests: 1, Tokens: 20, Cost: 20, ActualCost: 20},
	}, byCost)

	breakdown, err := repo.GetUserBreakdownStats(ctx, start, end, usagestats.UserBreakdownDimension{GroupID: group.ID, SortBy: "actual_cost"}, 1)
	require.NoError(t, err)
	require.Len(t, breakdown, 1)
	require.Equal(t, map[int64]bool{breakdown[0].UserID: true}, userIDs(byCost), "same users as user-breakdown sorted by actual_cost")

	legacy, err := repo.GetUserUsageTrendWithFilters(ctx, start, end, "day", 1, filters)
	require.NoError(t, err)
	require.Equal(t, map[int64]bool{heavy.ID: true}, userIDs(legacy))
	for _, metric := range []string{"tokens", "", "bogus"} {
		byTokens, err := svc.GetUserUsageTrendWithFiltersByMetric(ctx, start, end, "day", 1, metric, filters)
		require.NoError(t, err, metric)
		require.Equal(t, legacy, byTokens, "metric %q keeps the tokens ranking", metric)
	}

	both, err := svc.GetUserUsageTrendWithFiltersByMetric(ctx, start, end, "day", 10, "actual_cost", filters)
	require.NoError(t, err)
	require.Equal(t, map[int64]bool{spender.ID: true, heavy.ID: true}, userIDs(both))
}
