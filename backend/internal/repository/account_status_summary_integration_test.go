//go:build integration

package repository

import (
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

func (s *AccountRepoSuite) TestStatusSummary_MatchesGroupListCounts() {
	g1 := mustCreateGroup(s.T(), s.client, &service.Group{Name: "summary-g1", Platform: service.PlatformAnthropic})
	g2 := mustCreateGroup(s.T(), s.client, &service.Group{Name: "summary-g2", Platform: service.PlatformAnthropic})
	future := time.Now().Add(time.Hour)
	past := time.Now().Add(-time.Hour)

	available := mustCreateAccount(s.T(), s.client, &service.Account{Name: "sum-available"})
	limited := mustCreateAccount(s.T(), s.client, &service.Account{Name: "sum-limited", RateLimitResetAt: &future,
		Extra: map[string]any{"model_rate_limits": map[string]any{"claude-opus-4": map[string]any{"rate_limit_reset_at": future.UTC().Format(time.RFC3339)}}}})
	expired := mustCreateAccount(s.T(), s.client, &service.Account{Name: "sum-expired"})
	errored := mustCreateAccount(s.T(), s.client, &service.Account{Name: "sum-error", Status: service.StatusError, ErrorMessage: "x"})
	for _, a := range []*service.Account{available, limited, expired, errored} {
		mustBindAccountToGroup(s.T(), s.client, a.ID, g1.ID, 1)
	}
	mustBindAccountToGroup(s.T(), s.client, available.ID, g2.ID, 1)
	_, err := s.client.Account.UpdateOneID(limited.ID).SetSchedulable(false).Save(s.ctx)
	s.Require().NoError(err)
	_, err = s.client.Account.UpdateOneID(expired.ID).SetExpiresAt(past).SetAutoPauseOnExpired(true).Save(s.ctx)
	s.Require().NoError(err)

	rows, err := s.repo.ListAccountsForStatusSummary(s.ctx, "", &g1.ID)
	s.Require().NoError(err)
	s.Require().Len(rows, 4)
	svc := service.NewAccountStatusSummaryService(s.repo, nil, nil)
	summary, err := svc.Summarize(s.ctx, service.AccountStatusSummaryQuery{GroupID: &g1.ID, IncludeAccounts: true})
	s.Require().NoError(err)
	s.Require().Len(summary.Groups, 1)
	group := summary.Groups[0]
	s.Require().Equal(int64(4), group.Total)
	s.Require().Equal(int64(1), group.Available)
	s.Require().Equal(int64(1), group.RateLimited)
	s.Require().Equal(int64(1), group.Stopped)
	s.Require().Equal(int64(1), group.Paused, "expired + auto pause counts as paused")
	s.Require().Equal(int64(1), group.Error)

	groupRepo := newGroupRepositoryWithSQL(s.client, s.repo.sql)
	listed, err := groupRepo.GetByID(s.ctx, g1.ID)
	s.Require().NoError(err)
	s.Require().Equal(listed.ActiveAccountCount, group.Available, "available must match the group list's active_account_count")
	s.Require().Equal(listed.AccountCount, group.Total)
	s.Require().Equal(listed.ErrorAccountCount, group.Error, "error must match the group list's error_account_count")
	// 分组列表按调度口径：停了调度但仍在限流窗口的 limited 账号算 idle，
	// D02 把它算进 rate_limited 并另记 stopped，所以 idle = paused + inactive + stopped。
	s.Require().Equal(listed.IdleAccountCount, group.Paused+group.Inactive+group.Stopped)

	var limitedItem *service.AccountStatusItem
	for i := range summary.Accounts {
		if summary.Accounts[i].ID == limited.ID {
			limitedItem = &summary.Accounts[i]
		}
	}
	s.Require().NotNil(limitedItem)
	s.Require().Equal(service.AccountStateRateLimited, limitedItem.State)
	s.Require().Len(limitedItem.ModelRateLimits, 1)

	all, err := svc.Summarize(s.ctx, service.AccountStatusSummaryQuery{})
	s.Require().NoError(err)
	var g2Summary *service.AccountStatusGroupSummary
	for i := range all.Groups {
		if all.Groups[i].GroupID == g2.ID {
			g2Summary = &all.Groups[i]
		}
	}
	s.Require().NotNil(g2Summary)
	s.Require().Equal(int64(1), g2Summary.Total, "an account in two groups counts in both")
}
