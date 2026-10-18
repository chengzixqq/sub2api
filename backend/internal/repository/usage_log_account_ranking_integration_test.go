//go:build integration

package repository

import (
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
)

func (s *UsageLogRepoSuite) TestGetAccountSpendingRanking() {
	user := mustCreateUser(s.T(), s.client, &service.User{Email: "acct-rank@test.com"})
	apiKey := mustCreateApiKey(s.T(), s.client, &service.APIKey{UserID: user.ID, Key: "sk-acct-rank", Name: "k"})
	big := mustCreateAccount(s.T(), s.client, &service.Account{Name: "rank-big", Platform: service.PlatformAnthropic})
	small := mustCreateAccount(s.T(), s.client, &service.Account{Name: "rank-small", Platform: service.PlatformOpenAI})
	gone := mustCreateAccount(s.T(), s.client, &service.Account{Name: "rank-deleted"})
	start := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)

	rate := 0.5
	add := func(account *service.Account, actual, total float64, recorded bool, at time.Time) {
		_, err := s.repo.Create(s.ctx, &service.UsageLog{
			UserID: user.ID, APIKeyID: apiKey.ID, AccountID: account.ID, RequestID: uuid.NewString(),
			Model: "m", InputTokens: 100, OutputTokens: 50, TotalCost: total, ActualCost: actual,
			AccountRateMultiplier: &rate, ProviderCostRecorded: recorded, ProbeCoalesced: !recorded, CreatedAt: at,
		})
		s.Require().NoError(err)
	}
	add(big, 3, 2, true, start.Add(time.Hour))
	add(big, 4, 2, false, start.Add(2*time.Hour))
	add(small, 1, 1, true, start.Add(3*time.Hour))
	add(gone, 9, 9, true, start.Add(4*time.Hour))
	add(big, 100, 100, true, start.Add(-time.Hour)) // outside the range
	_, err := s.client.Account.UpdateOneID(gone.ID).SetDeletedAt(time.Now()).Save(s.ctx)
	s.Require().NoError(err)

	got, err := s.repo.GetAccountSpendingRanking(s.ctx, usagestats.AccountSpendingRankingParams{Start: start, End: end, Limit: 10})
	s.Require().NoError(err)
	s.Require().Len(got.Ranking, 2, "soft-deleted accounts are excluded")
	first := got.Ranking[0]
	s.Require().Equal(big.ID, first.AccountID)
	s.Require().Equal("rank-big", first.AccountName)
	s.Require().Equal(service.PlatformAnthropic, first.Platform)
	s.Require().Equal(service.StatusActive, first.Status)
	s.Require().Equal(int64(2), first.Requests)
	s.Require().Equal(int64(300), first.Tokens)
	s.Require().InDelta(7.0, first.ActualCost, 1e-9)
	s.Require().InDelta(2.0, first.StandardCost, 1e-9, "only provider_cost_recorded rows count")
	s.Require().InDelta(1.0, first.AccountCost, 1e-9)
	s.Require().Equal(small.ID, got.Ranking[1].AccountID)
	s.Require().InDelta(8.0, got.TotalActualCost, 1e-9)

	limited, err := s.repo.GetAccountSpendingRanking(s.ctx, usagestats.AccountSpendingRankingParams{Start: start, End: end, Limit: 1})
	s.Require().NoError(err)
	s.Require().Len(limited.Ranking, 1)
	s.Require().InDelta(8.0, limited.TotalActualCost, 1e-9, "the total covers every account, not just the page")

	scoped, err := s.repo.GetAccountSpendingRanking(service.WithUsageAccountScope(s.ctx, []int64{small.ID}), usagestats.AccountSpendingRankingParams{Start: start, End: end, Limit: 10})
	s.Require().NoError(err)
	s.Require().Len(scoped.Ranking, 1)
	s.Require().Equal(small.ID, scoped.Ranking[0].AccountID)
	s.Require().InDelta(1.0, scoped.TotalActualCost, 1e-9)

	byPlatform, err := s.repo.GetAccountSpendingRanking(s.ctx, usagestats.AccountSpendingRankingParams{Start: start, End: end, Limit: 10, Platform: service.PlatformOpenAI})
	s.Require().NoError(err)
	s.Require().Len(byPlatform.Ranking, 1)
	s.Require().Equal(small.ID, byPlatform.Ranking[0].AccountID)
	s.Require().InDelta(1.0, byPlatform.TotalActualCost, 1e-9)
}

func (s *UsageLogRepoSuite) TestUsageTrendAccountCostMatchesStats() {
	user := mustCreateUser(s.T(), s.client, &service.User{Email: "trend-acct-cost@test.com"})
	apiKey := mustCreateApiKey(s.T(), s.client, &service.APIKey{UserID: user.ID, Key: "sk-trend-acct-cost", Name: "k"})
	account := mustCreateAccount(s.T(), s.client, &service.Account{Name: "trend-acct-cost"})
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	half, stats := 0.5, 3.0
	for i, row := range []struct {
		total    float64
		stats    *float64
		recorded bool
	}{{2, nil, true}, {4, &stats, true}, {8, nil, false}} {
		_, err := s.repo.Create(s.ctx, &service.UsageLog{
			UserID: user.ID, APIKeyID: apiKey.ID, AccountID: account.ID, RequestID: uuid.NewString(), Model: "m",
			TotalCost: row.total, ActualCost: row.total, AccountStatsCost: row.stats, AccountRateMultiplier: &half,
			ProviderCostRecorded: row.recorded, ProbeCoalesced: !row.recorded, CreatedAt: start.Add(time.Duration(i) * 25 * time.Hour),
		})
		s.Require().NoError(err)
	}
	end := start.Add(4 * 24 * time.Hour)
	filters := usagestats.UsageLogFilters{AccountID: account.ID}
	trend, err := s.repo.GetUsageTrendWithUsageFilters(s.ctx, start, end, "day", filters)
	s.Require().NoError(err)
	var sum float64
	for _, point := range trend {
		s.Require().NotNil(point.AccountCost)
		sum += *point.AccountCost
	}
	// 2×0.5 + 3×0.5 + 0 (follower) = 2.5
	s.Require().InDelta(2.5, sum, 1e-9)

	filters.StartTime, filters.EndTime = &start, &end
	stats2, err := s.repo.GetStatsWithFilters(s.ctx, filters)
	s.Require().NoError(err)
	s.Require().NotNil(stats2.TotalAccountCost)
	s.Require().InDelta(*stats2.TotalAccountCost, sum, 1e-9, "trend account cost sums to usage/stats total_account_cost")

	userTrend, err := s.repo.GetUserUsageTrendByUserID(s.ctx, user.ID, start, end, "day")
	s.Require().NoError(err)
	s.Require().NotEmpty(userTrend)
}
