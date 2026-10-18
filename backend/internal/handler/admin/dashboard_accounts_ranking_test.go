package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type accountRankingRepoCapture struct {
	service.UsageLogRepository
	calls  int
	params usagestats.AccountSpendingRankingParams
}

func (s *accountRankingRepoCapture) GetAccountSpendingRanking(_ context.Context, params usagestats.AccountSpendingRankingParams) (*usagestats.AccountSpendingRankingResponse, error) {
	s.calls++
	s.params = params
	return &usagestats.AccountSpendingRankingResponse{
		Ranking:         []usagestats.AccountSpendingRankingItem{{AccountID: 12, AccountName: "Claude-主力", Platform: "anthropic", Status: "active", Requests: 3, Tokens: 300, ActualCost: 12.5}},
		TotalActualCost: 20,
	}, nil
}

func newAccountRankingRouter(repo *accountRankingRepoCapture) *gin.Engine {
	gin.SetMode(gin.TestMode)
	handler := NewDashboardHandler(service.NewDashboardService(repo, nil, nil, nil), nil)
	router := gin.New()
	router.GET("/admin/dashboard/accounts-ranking", handler.GetAccountSpendingRanking)
	return router
}

func TestDashboardAccountsRanking_DefaultsToTodayAndClampsLimit(t *testing.T) {
	dashboardAccountsRankingCache = newSnapshotCache(5 * time.Minute)
	repo := &accountRankingRepoCapture{}
	router := newAccountRankingRouter(repo)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/dashboard/accounts-ranking?limit=500&platform=anthropic&group_id=3", nil))

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, 50, repo.params.Limit)
	require.Equal(t, "anthropic", repo.params.Platform)
	require.Equal(t, int64(3), repo.params.GroupID)
	today := timezone.StartOfDayInUserLocation(timezone.NowInUserLocation(""), "")
	require.True(t, repo.params.Start.Equal(today), "start %v, want %v", repo.params.Start, today)
	require.True(t, repo.params.End.Equal(today.AddDate(0, 0, 1)))
	require.Contains(t, rec.Body.String(), `"account_name":"Claude-主力"`)
	require.Contains(t, rec.Body.String(), `"total_actual_cost":20`)
	require.Contains(t, rec.Body.String(), `"start_date":"`+today.Format("2006-01-02")+`"`)
	require.Contains(t, rec.Body.String(), `"end_date":"`+today.Format("2006-01-02")+`"`)
	require.Equal(t, "miss", rec.Header().Get("X-Snapshot-Cache"))

	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/dashboard/accounts-ranking?limit=500&platform=anthropic&group_id=3", nil))
	require.Equal(t, "hit", rec.Header().Get("X-Snapshot-Cache"))
	require.Equal(t, 1, repo.calls)
}

func TestDashboardAccountsRanking_ExplicitRangeAndDefaultLimit(t *testing.T) {
	dashboardAccountsRankingCache = newSnapshotCache(5 * time.Minute)
	repo := &accountRankingRepoCapture{}
	router := newAccountRankingRouter(repo)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/dashboard/accounts-ranking?start_date=2026-10-10&end_date=2026-10-12&timezone=UTC", nil))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, 10, repo.params.Limit)
	require.Equal(t, time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC), repo.params.Start.UTC())
	require.Equal(t, time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC), repo.params.End.UTC())

	for _, bad := range []string{"?group_id=x", "?limit=abc", "?limit=0"} {
		rec = httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/dashboard/accounts-ranking"+bad, nil))
		require.Equal(t, http.StatusBadRequest, rec.Code, bad)
	}
}
