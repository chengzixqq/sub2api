package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type dashboardSortedRankingRepo struct {
	dashboardUsageRepoCapture
	sortedCalls int
	sortBy      string
	limit       int
}

func (s *dashboardSortedRankingRepo) GetUserSpendingRankingSorted(_ context.Context, _, _ time.Time, limit int, sortBy string, _ usagestats.UsageLogFilters) (*usagestats.UserSpendingRankingResponse, error) {
	s.sortedCalls++
	s.sortBy = sortBy
	s.limit = limit
	return &usagestats.UserSpendingRankingResponse{
		Ranking: []usagestats.UserSpendingRankingItem{
			{UserID: 7, Email: "a@x.com", Username: "alice", ActualCost: 210.5, Cost: 260.1, AccountCost: 88.2, Requests: 3100, Tokens: 15000000},
		},
		TotalUsers:       143,
		TotalActualCost:  1284.5,
		TotalCost:        1610.14,
		TotalAccountCost: 512.08,
		TotalRequests:    18204,
		TotalTokens:      96400000,
	}, nil
}

func newUsersRankingSortRouter(repo *dashboardSortedRankingRepo) *gin.Engine {
	gin.SetMode(gin.TestMode)
	handler := NewDashboardHandler(service.NewDashboardService(repo, nil, nil, nil), nil)
	router := gin.New()
	router.GET("/admin/dashboard/users-ranking", handler.GetUserSpendingRanking)
	return router
}

func getUsersRanking(t *testing.T, router *gin.Engine, query string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	path := "/admin/dashboard/users-ranking?start_date=2025-01-01&end_date=2025-01-02"
	if query != "" {
		path += "&" + query
	}
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func TestDashboardUsersRanking_SortByAndTotals(t *testing.T) {
	dashboardUsersRankingCache = newSnapshotCache(time.Minute)
	repo := &dashboardSortedRankingRepo{}
	router := newUsersRankingSortRouter(repo)

	rec := getUsersRanking(t, router, "limit=5")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, "miss", rec.Header().Get("X-Snapshot-Cache"))
	require.Equal(t, usagestats.UserRankingSortActualCost, repo.sortBy, "default sort stays actual_cost")
	require.Equal(t, 5, repo.limit)
	require.Equal(t, 1, repo.sortedCalls)

	var body struct {
		Data map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.EqualValues(t, 143, body.Data["total_users"])
	require.EqualValues(t, 1284.5, body.Data["total_actual_cost"])
	require.EqualValues(t, 1610.14, body.Data["total_cost"])
	require.EqualValues(t, 512.08, body.Data["total_account_cost"])
	require.EqualValues(t, 18204, body.Data["total_requests"])
	require.EqualValues(t, 96400000, body.Data["total_tokens"])
	row := body.Data["ranking"].([]any)[0].(map[string]any)
	require.EqualValues(t, 260.1, row["cost"])
	require.EqualValues(t, 88.2, row["account_cost"])
	require.EqualValues(t, 210.5, row["actual_cost"])

	// An explicit default shares the cached entry with the bare request.
	rec = getUsersRanking(t, router, "limit=5&sort_by=actual_cost")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "hit", rec.Header().Get("X-Snapshot-Cache"))
	require.Equal(t, 1, repo.sortedCalls)

	// Other sorts must not be served from the actual_cost entry.
	rec = getUsersRanking(t, router, "limit=5&sort_by=requests")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "miss", rec.Header().Get("X-Snapshot-Cache"))
	require.Equal(t, usagestats.UserRankingSortRequests, repo.sortBy)
	require.Equal(t, 2, repo.sortedCalls)

	rec = getUsersRanking(t, router, "limit=5&sort_by=%20requests%20")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "hit", rec.Header().Get("X-Snapshot-Cache"), "sort_by is trimmed before keying the cache")
	require.Equal(t, 2, repo.sortedCalls)

	rec = getUsersRanking(t, router, "limit=5&sort_by=tokens")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "miss", rec.Header().Get("X-Snapshot-Cache"))
	require.Equal(t, usagestats.UserRankingSortTokens, repo.sortBy)

	rec = getUsersRanking(t, router, "limit=5&sort_by=")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "hit", rec.Header().Get("X-Snapshot-Cache"), "blank sort_by is the default")
}

func TestDashboardUsersRanking_InvalidSortBy(t *testing.T) {
	dashboardUsersRankingCache = newSnapshotCache(time.Minute)
	repo := &dashboardSortedRankingRepo{}
	router := newUsersRankingSortRouter(repo)

	for _, sortBy := range []string{"bogus", "cost", "ACTUAL_COST"} {
		rec := getUsersRanking(t, router, "sort_by="+sortBy)
		require.Equal(t, http.StatusBadRequest, rec.Code, sortBy)
		var body struct {
			Message string `json:"message"`
			Reason  string `json:"reason"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
		require.Equal(t, "INVALID_SORT_BY", body.Reason, sortBy)
		require.Equal(t, "Invalid sort_by, use actual_cost/requests/tokens", body.Message, sortBy)
	}
	require.Zero(t, repo.sortedCalls)
}
