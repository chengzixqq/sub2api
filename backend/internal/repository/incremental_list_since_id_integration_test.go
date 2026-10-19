//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func (s *UsageLogRepoSuite) TestListWithFilters_SinceID() {
	suffix := uuid.NewString()
	user := mustCreateUser(s.T(), s.client, &service.User{Email: "since-" + suffix + "@test.com"})
	apiKey := mustCreateApiKey(s.T(), s.client, &service.APIKey{UserID: user.ID, Key: "sk-since-" + suffix, Name: "k"})
	account := mustCreateAccount(s.T(), s.client, &service.Account{Name: "acc-since-" + suffix})
	other := mustCreateAccount(s.T(), s.client, &service.Account{Name: "acc-since-other-" + suffix})

	now := time.Now()
	first := s.createUsageLog(user, apiKey, account, 1, 1, 0.1, now)
	second := s.createUsageLog(user, apiKey, account, 1, 1, 0.1, now.Add(-time.Hour))
	third := s.createUsageLog(user, apiKey, account, 1, 1, 0.1, now.Add(-2*time.Hour))

	since := first.ID
	params := pagination.PaginationParams{Page: 2, PageSize: 10, SortBy: "created_at", SortOrder: "desc"}
	filters := usagestats.UsageLogFilters{AccountID: account.ID, SinceID: &since, ExactTotal: true}
	logs, page, err := s.repo.ListWithFilters(s.ctx, params, filters)
	s.Require().NoError(err)
	// id order wins over created_at: the later-inserted rows are newer.
	s.Require().Len(logs, 2)
	s.Require().Equal(third.ID, logs[0].ID)
	s.Require().Equal(second.ID, logs[1].ID)
	s.Require().Equal(int64(2), page.Total)

	params.PageSize = 1
	logs, page, err = s.repo.ListWithFilters(s.ctx, params, filters)
	s.Require().NoError(err)
	s.Require().Len(logs, 1)
	s.Require().Equal(third.ID, logs[0].ID)
	s.Require().Greater(page.Total, int64(1), "a full page plus one signals has_more")

	otherLog := s.createUsageLog(user, apiKey, other, 1, 1, 0.1, now)
	scoped := service.WithUsageAccountScope(s.ctx, []int64{other.ID})
	logs, _, err = s.repo.ListWithFilters(scoped, pagination.PaginationParams{Page: 1, PageSize: 10}, usagestats.UsageLogFilters{SinceID: &since})
	s.Require().NoError(err)
	s.Require().Len(logs, 1)
	s.Require().Equal(otherLog.ID, logs[0].ID)
	for _, log := range logs {
		s.Require().Equal(other.ID, log.AccountID, "the vendor account scope still narrows incremental reads")
	}
}

func TestOpsErrorLog_SinceIDIncremental(t *testing.T) {
	ctx := context.Background()
	repo, ok := NewOpsRepository(integrationDB).(*opsRepository)
	require.True(t, ok)
	prefix := "since-" + uuid.NewString()

	ids := make([]int64, 0, 3)
	for i := 0; i < 3; i++ {
		id, err := repo.InsertErrorLog(ctx, &service.OpsInsertErrorLogInput{
			RequestID: prefix + "-" + string(rune('a'+i)), ErrorPhase: "upstream", ErrorType: "upstream_error", Severity: "error",
			StatusCode: 502, CreatedAt: time.Now().Add(-time.Duration(i) * time.Hour),
		})
		require.NoError(t, err)
		ids = append(ids, id)
	}
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM ops_error_logs WHERE request_id LIKE $1", prefix+"%")
	})

	since := ids[0]
	list, err := repo.ListErrorLogs(ctx, &service.OpsErrorLogFilter{Query: prefix, View: "all", SinceID: &since, Page: 1, PageSize: 10})
	require.NoError(t, err)
	require.Len(t, list.Errors, 2)
	require.Equal(t, ids[2], list.Errors[0].ID)
	require.Equal(t, ids[1], list.Errors[1].ID)
	require.False(t, list.HasMore)
	require.Equal(t, 2, list.Total)

	list, err = repo.ListErrorLogs(ctx, &service.OpsErrorLogFilter{Query: prefix, View: "all", SinceID: &since, Page: 1, PageSize: 1})
	require.NoError(t, err)
	require.Len(t, list.Errors, 1)
	require.Equal(t, ids[2], list.Errors[0].ID)
	require.True(t, list.HasMore)
}
