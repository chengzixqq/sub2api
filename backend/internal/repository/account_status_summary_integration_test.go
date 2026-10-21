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
	expiredLimited := mustCreateAccount(s.T(), s.client, &service.Account{Name: "sum-expired-limited", RateLimitResetAt: &future})
	for _, a := range []*service.Account{available, limited, expired, errored, expiredLimited} {
		mustBindAccountToGroup(s.T(), s.client, a.ID, g1.ID, 1)
	}
	mustBindAccountToGroup(s.T(), s.client, available.ID, g2.ID, 1)
	_, err := s.client.Account.UpdateOneID(limited.ID).SetSchedulable(false).Save(s.ctx)
	s.Require().NoError(err)
	_, err = s.client.Account.UpdateOneID(expired.ID).SetExpiresAt(past).SetAutoPauseOnExpired(true).Save(s.ctx)
	s.Require().NoError(err)
	_, err = s.client.Account.UpdateOneID(expiredLimited.ID).SetExpiresAt(past).SetAutoPauseOnExpired(true).Save(s.ctx)
	s.Require().NoError(err)

	rows, err := s.repo.ListAccountsForStatusSummary(s.ctx, "", &g1.ID)
	s.Require().NoError(err)
	s.Require().Len(rows, 5)
	svc := service.NewAccountStatusSummaryService(s.repo, nil, nil)
	summary, err := svc.Summarize(s.ctx, service.AccountStatusSummaryQuery{GroupID: &g1.ID, IncludeAccounts: true})
	s.Require().NoError(err)
	s.Require().Len(summary.Groups, 1)
	group := summary.Groups[0]
	s.Require().Equal(int64(5), group.Total)
	s.Require().Equal(int64(1), group.Available)
	s.Require().Equal(int64(2), group.RateLimited)
	s.Require().Equal(int64(1), group.Stopped)
	s.Require().Equal(int64(1), group.Paused, "expired + auto pause counts as paused")
	s.Require().Equal(int64(1), group.Error)

	groupRepo := newGroupRepositoryWithSQL(s.client, s.repo.sql)
	listed, err := groupRepo.GetByID(s.ctx, g1.ID)
	s.Require().NoError(err)
	s.Require().Equal(listed.ActiveAccountCount, group.Available, "available must match the group list's active_account_count")
	s.Require().Equal(listed.AccountCount, group.Total)
	s.Require().Equal(listed.ErrorAccountCount, group.Error, "error must match the group list's error_account_count")
	s.Require().Equal(int64(0), listed.RateLimitedAccountCount, "group list counts only schedulable, unexpired accounts as rate_limited")
	// 分组列表按调度口径，idle 里有两类账号 D02 算进限时桶（rate_limited / overloaded / temp_unschedulable）：
	// 1）停了调度但仍在限时窗口（limited），D02 另记 stopped；
	// 2）已到期、开了到期自动暂停、调度开关仍开着又在限时窗口（expiredLimited），D02 先判窗口且不记 stopped，
	//    到期任务（每分钟一次）把它停调度后才变成第 1 类。
	// 所以 idle = paused + inactive + stopped + 第 2 类账号数；只有没有第 2 类时 idle = paused + inactive + stopped。
	s.Require().Equal(int64(3), listed.IdleAccountCount)
	s.Require().Equal(listed.IdleAccountCount, group.Paused+group.Inactive+group.Stopped+1,
		"expiredLimited is idle in the group list but rate_limited without stopped in D02")

	items := map[int64]*service.AccountStatusItem{}
	for i := range summary.Accounts {
		items[summary.Accounts[i].ID] = &summary.Accounts[i]
	}
	limitedItem := items[limited.ID]
	s.Require().NotNil(limitedItem)
	s.Require().Equal(service.AccountStateRateLimited, limitedItem.State)
	s.Require().True(limitedItem.Stopped)
	s.Require().Len(limitedItem.ModelRateLimits, 1)
	expiredLimitedItem := items[expiredLimited.ID]
	s.Require().NotNil(expiredLimitedItem)
	s.Require().Equal(service.AccountStateRateLimited, expiredLimitedItem.State, "timed windows are checked before expiry")
	s.Require().False(expiredLimitedItem.Stopped, "the schedulable switch is still on")

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
