package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type statusSummaryHandlerRepo struct {
	calls   int
	groupID *int64
}

func (r *statusSummaryHandlerRepo) ListAccountsForStatusSummary(_ context.Context, _ string, groupID *int64) ([]service.AccountStatusRow, error) {
	r.calls++
	r.groupID = groupID
	return []service.AccountStatusRow{
		{ID: 1, Name: "a", Platform: service.PlatformAnthropic, Status: service.StatusActive, Schedulable: true, GroupIDs: []int64{3}},
		{ID: 2, Name: "b", Platform: service.PlatformAnthropic, Status: service.StatusActive, Schedulable: false, GroupIDs: []int64{3}},
	}, nil
}

func (r *statusSummaryHandlerRepo) ListGroupsForStatusSummary(context.Context, string, *int64, []int64) ([]service.AccountStatusGroupRow, error) {
	return []service.AccountStatusGroupRow{{ID: 3, Name: "g", Platform: service.PlatformAnthropic, Status: service.StatusActive}}, nil
}

func newStatusSummaryRouter(repo *statusSummaryHandlerRepo) *gin.Engine {
	gin.SetMode(gin.TestMode)
	h := NewAccountHandler(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	h.SetAccountStatusSummaryService(service.NewAccountStatusSummaryService(repo, nil, nil))
	r := gin.New()
	r.GET("/api/v1/admin/accounts/status-summary", h.GetStatusSummary)
	return r
}

func TestAccountStatusSummaryHandler(t *testing.T) {
	accountStatusSummaryCache = newSnapshotCache(time.Minute)
	repo := &statusSummaryHandlerRepo{}
	router := newStatusSummaryRouter(repo)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts/status-summary?group_id=3&include_accounts=true&default_min_ratio=0.6", nil))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, int64(3), *repo.groupID)
	var resp struct {
		Data struct {
			Totals   service.AccountStateCounts          `json:"totals"`
			Groups   []service.AccountStatusGroupSummary `json:"groups"`
			Accounts []service.AccountStatusItem         `json:"accounts"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Equal(t, int64(2), resp.Data.Totals.Total)
	require.Equal(t, int64(1), resp.Data.Totals.Paused)
	require.Len(t, resp.Data.Groups, 1)
	require.True(t, resp.Data.Groups[0].Shortage.IsShort, "1 of 2 is below 0.6")
	require.Len(t, resp.Data.Accounts, 2)
	require.Equal(t, "miss", rec.Header().Get("X-Snapshot-Cache"))

	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts/status-summary?group_id=3&include_accounts=true&default_min_ratio=0.6", nil))
	require.Equal(t, "hit", rec.Header().Get("X-Snapshot-Cache"))
	require.Equal(t, 1, repo.calls)

	for _, bad := range []string{"?group_id=x", "?group_id=-1", "?include_accounts=maybe", "?default_min_ratio=2"} {
		rec = httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts/status-summary"+bad, nil))
		require.Equal(t, http.StatusBadRequest, rec.Code, bad)
	}
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts/status-summary?group_id=x", nil))
	require.Contains(t, rec.Body.String(), "INVALID_GROUP_FILTER")
}
