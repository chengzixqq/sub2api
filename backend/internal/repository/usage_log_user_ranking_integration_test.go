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

// Users-ranking sorts by actual charge, request count or tokens, and reports
// per-user and range-wide standard / account costs plus the number of users
// with usage, all computed before LIMIT.
func TestUsageLogUserRanking_SortAndTotals(t *testing.T) {
	tx := testEntTx(t)
	client := tx.Client()
	repo := newUsageLogRepositoryWithSQL(client, tx)
	account := mustCreateAccount(t, client, &service.Account{Name: uuid.NewString()})
	newUser := func(name string) (*service.User, *service.APIKey) {
		user := mustCreateUser(t, client, &service.User{Email: uuid.NewString() + "@ranking.test", Username: name})
		key := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID, Key: uuid.NewString(), Name: name})
		return user, key
	}
	userA, keyA := newUser("spender")
	userB, keyB := newUser("chatty")
	userC, keyC := newUser("verbose")
	start := time.Date(2019, 3, 5, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	ctx := service.WithUsageAccountScope(context.Background(), []int64{account.ID})
	half := 0.5
	create := func(user *service.User, key *service.APIKey, tokens int, cost, actual float64, multiplier *float64, recorded bool) {
		log := &service.UsageLog{
			UserID: user.ID, APIKeyID: key.ID, AccountID: account.ID, RequestID: uuid.NewString(), Model: "ranking",
			InputTokens: tokens, TotalCost: cost, ActualCost: actual, AccountRateMultiplier: multiplier,
			ProviderCostRecorded: true, CreatedAt: start.Add(time.Hour),
		}
		_, err := repo.Create(context.Background(), log)
		require.NoError(t, err)
		if !recorded {
			_, err = tx.ExecContext(context.Background(), "UPDATE usage_logs SET provider_cost_recorded = false WHERE id = $1", log.ID)
			require.NoError(t, err)
		}
	}
	// A charges the most, B sends the most requests, C uses the most tokens.
	create(userA, keyA, 100, 50, 40, nil, true)
	for i := 0; i < 3; i++ {
		create(userB, keyB, 10, 2, 1, &half, true)
	}
	create(userC, keyC, 1000, 4, 2, nil, true)
	create(userC, keyC, 5, 9, 1, nil, false) // not billed by the provider: no cost
	// Outside the window: must not count toward any total.
	_, err := repo.Create(context.Background(), &service.UsageLog{
		UserID: userA.ID, APIKeyID: keyA.ID, AccountID: account.ID, RequestID: uuid.NewString(), Model: "ranking",
		InputTokens: 999, TotalCost: 99, ActualCost: 99, ProviderCostRecorded: true, CreatedAt: end,
	})
	require.NoError(t, err)

	for _, tc := range []struct {
		sortBy string
		first  int64
	}{
		{usagestats.UserRankingSortActualCost, userA.ID},
		{usagestats.UserRankingSortRequests, userB.ID},
		{usagestats.UserRankingSortTokens, userC.ID},
	} {
		got, err := repo.GetUserSpendingRankingSorted(ctx, start, end, 1, tc.sortBy, UsageLogFilters{})
		require.NoError(t, err, tc.sortBy)
		require.Len(t, got.Ranking, 1, tc.sortBy)
		require.Equal(t, tc.first, got.Ranking[0].UserID, tc.sortBy)
		require.Equal(t, int64(3), got.TotalUsers, "total_users ignores LIMIT (%s)", tc.sortBy)
		require.Equal(t, 46.0, got.TotalActualCost, tc.sortBy)
		require.Equal(t, 60.0, got.TotalCost, "unrecorded provider cost is excluded (%s)", tc.sortBy)
		require.Equal(t, 57.0, got.TotalAccountCost, "account cost applies the rate multiplier (%s)", tc.sortBy)
		require.Equal(t, int64(6), got.TotalRequests, tc.sortBy)
		require.Equal(t, int64(1135), got.TotalTokens, tc.sortBy)
	}

	var distinctUsers int64
	require.NoError(t, scanSingleRow(context.Background(), tx,
		"SELECT COUNT(DISTINCT user_id) FROM usage_logs WHERE created_at >= $1 AND created_at < $2 AND account_id = $3",
		[]any{start, end, account.ID}, &distinctUsers))

	full, err := repo.GetUserSpendingRankingSorted(ctx, start, end, 12, usagestats.UserRankingSortRequests, UsageLogFilters{})
	require.NoError(t, err)
	require.Equal(t, distinctUsers, full.TotalUsers)
	require.Equal(t, []usagestats.UserSpendingRankingItem{
		{UserID: userB.ID, Email: userB.Email, Username: userB.Username, ActualCost: 3, Cost: 6, AccountCost: 3, Requests: 3, Tokens: 30},
		{UserID: userC.ID, Email: userC.Email, Username: userC.Username, ActualCost: 3, Cost: 4, AccountCost: 4, Requests: 2, Tokens: 1005},
		{UserID: userA.ID, Email: userA.Email, Username: userA.Username, ActualCost: 40, Cost: 50, AccountCost: 50, Requests: 1, Tokens: 100},
	}, full.Ranking)

	byDefault, err := repo.GetUserSpendingRankingSorted(ctx, start, end, 12, "", UsageLogFilters{})
	require.NoError(t, err)
	legacy, err := repo.GetUserSpendingRankingWithFilters(ctx, start, end, 12, UsageLogFilters{})
	require.NoError(t, err)
	require.Equal(t, legacy, byDefault)
	require.Equal(t, []int64{userA.ID, userC.ID, userB.ID}, []int64{byDefault.Ranking[0].UserID, byDefault.Ranking[1].UserID, byDefault.Ranking[2].UserID})

	empty, err := repo.GetUserSpendingRankingSorted(ctx, end.Add(time.Hour), end.Add(2*time.Hour), 5, usagestats.UserRankingSortTokens, UsageLogFilters{})
	require.NoError(t, err)
	require.Empty(t, empty.Ranking)
	require.Zero(t, empty.TotalUsers)
	require.Zero(t, empty.TotalCost)
	require.Zero(t, empty.TotalAccountCost)
}
