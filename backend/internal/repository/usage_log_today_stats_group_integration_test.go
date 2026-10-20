//go:build integration

package repository

import (
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
)

// TestGetAccountWindowStatsBatchByGroup 锁定 today-stats/batch 带 group_id 的口径：
// 只统计 usage_logs.group_id（Key 所属分组）等于该分组的行，与 groups/usage-summary 一致；
// 各分组之和加上未分组的用量等于不分组的结果。
func (s *UsageLogRepoSuite) TestGetAccountWindowStatsBatchByGroup() {
	suffix := uuid.NewString()
	user := mustCreateUser(s.T(), s.client, &service.User{Email: "today-group-" + suffix + "@test.com"})
	apiKey := mustCreateApiKey(s.T(), s.client, &service.APIKey{UserID: user.ID, Key: "sk-today-group-" + suffix, Name: "k"})
	accountX := mustCreateAccount(s.T(), s.client, &service.Account{Name: "today-group-x-" + suffix})
	accountY := mustCreateAccount(s.T(), s.client, &service.Account{Name: "today-group-y-" + suffix})
	groupA := mustCreateGroup(s.T(), s.client, &service.Group{Name: "today-group-a-" + suffix})
	groupB := mustCreateGroup(s.T(), s.client, &service.Group{Name: "today-group-b-" + suffix})

	now := time.Now().UTC()
	startTime := now.Add(-2 * time.Hour)
	insert := func(account *service.Account, groupID *int64, cost float64, createdAt time.Time) {
		_, err := s.repo.Create(s.ctx, &service.UsageLog{
			UserID: user.ID, APIKeyID: apiKey.ID, AccountID: account.ID, GroupID: groupID,
			RequestID: uuid.NewString(), Model: "claude-3", InputTokens: 10, OutputTokens: 5,
			TotalCost: cost, ActualCost: cost, CreatedAt: createdAt,
		})
		s.Require().NoError(err)
	}
	a, b := groupA.ID, groupB.ID
	insert(accountX, &a, 1, now.Add(-30*time.Minute))
	insert(accountX, &b, 2, now.Add(-20*time.Minute))
	insert(accountX, nil, 4, now.Add(-10*time.Minute))
	insert(accountY, &a, 8, now.Add(-5*time.Minute))
	insert(accountX, &a, 16, now.Add(-26*time.Hour)) // 窗口之前，不计入

	ids := []int64{accountX.ID, accountY.ID}

	byA, err := s.repo.GetAccountWindowStatsBatchByGroup(s.ctx, ids, groupA.ID, startTime)
	s.Require().NoError(err)
	s.Require().Len(byA, 2)
	s.Require().Equal(int64(1), byA[accountX.ID].Requests)
	s.Require().Equal(int64(15), byA[accountX.ID].Tokens)
	s.Require().InDelta(1.0, byA[accountX.ID].UserCost, 1e-9)
	s.Require().InDelta(1.0, byA[accountX.ID].StandardCost, 1e-9)
	s.Require().Equal(int64(1), byA[accountY.ID].Requests)
	s.Require().InDelta(8.0, byA[accountY.ID].UserCost, 1e-9)

	byB, err := s.repo.GetAccountWindowStatsBatchByGroup(s.ctx, ids, groupB.ID, startTime)
	s.Require().NoError(err)
	s.Require().Equal(int64(1), byB[accountX.ID].Requests)
	s.Require().InDelta(2.0, byB[accountX.ID].UserCost, 1e-9)
	s.Require().NotNil(byB[accountY.ID], "分组里没有用量的账号也要补零值")
	s.Require().Zero(byB[accountY.ID].Requests)
	s.Require().Zero(byB[accountY.ID].UserCost)

	all, err := s.repo.GetAccountWindowStatsBatch(s.ctx, ids, startTime)
	s.Require().NoError(err)
	s.Require().Equal(int64(3), all[accountX.ID].Requests)
	s.Require().InDelta(7.0, all[accountX.ID].UserCost, 1e-9)
	const ungroupedCost = 4.0
	s.Require().InDelta(all[accountX.ID].UserCost, byA[accountX.ID].UserCost+byB[accountX.ID].UserCost+ungroupedCost, 1e-9)
	s.Require().InDelta(all[accountX.ID].Cost, byA[accountX.ID].Cost+byB[accountX.ID].Cost+ungroupedCost, 1e-9)

	for _, invalid := range []int64{0, -1} {
		_, err := s.repo.GetAccountWindowStatsBatchByGroup(s.ctx, ids, invalid, startTime)
		s.Require().Error(err, "group_id=%d 必须报错，不能退化成不分组查询", invalid)
	}

	empty, err := s.repo.GetAccountWindowStatsBatchByGroup(s.ctx, nil, groupA.ID, startTime)
	s.Require().NoError(err)
	s.Require().Empty(empty)
}
