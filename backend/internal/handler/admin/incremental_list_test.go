package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type adminUsageSinceRepo struct {
	service.UsageLogRepository
	rows    []service.UsageLog
	total   int64
	params  pagination.PaginationParams
	filters usagestats.UsageLogFilters
	calls   int
}

func (r *adminUsageSinceRepo) ListWithFilters(_ context.Context, params pagination.PaginationParams, filters usagestats.UsageLogFilters) ([]service.UsageLog, *pagination.PaginationResult, error) {
	r.calls++
	r.params, r.filters = params, filters
	return r.rows, &pagination.PaginationResult{Total: r.total, Page: params.Page, PageSize: params.PageSize}, nil
}

func newAdminUsageSinceRouter(repo *adminUsageSinceRepo) *gin.Engine {
	gin.SetMode(gin.TestMode)
	h := NewUsageHandler(service.NewUsageService(repo, nil, nil, nil), nil, nil, nil)
	r := gin.New()
	r.GET("/admin/usage", h.List)
	return r
}

func decodeIncrementalData(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope), rec.Body.String())
	return envelope.Data
}

func TestAdminUsageList_SinceID(t *testing.T) {
	repo := &adminUsageSinceRepo{rows: []service.UsageLog{{ID: 105}, {ID: 103}}, total: 2}
	r := newAdminUsageSinceRouter(repo)

	rec := serveOps(r, http.MethodGet, "/admin/usage?since_id=100&page_size=50&sort_by=created_at&sort_order=asc", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NotNil(t, repo.filters.SinceID)
	require.Equal(t, int64(100), *repo.filters.SinceID)
	require.Equal(t, 1, repo.params.Page)
	data := decodeIncrementalData(t, rec)
	require.EqualValues(t, 100, data["since_id"])
	require.EqualValues(t, 105, data["latest_id"])
	require.Equal(t, false, data["has_more"])
	require.Nil(t, data["total"])
	require.Nil(t, data["pages"])
	require.Equal(t, false, data["total_exact"])
	require.EqualValues(t, 1, data["page"])
	require.EqualValues(t, 50, data["page_size"])
	require.Len(t, data["items"], 2)

	// More new rows than one page: has_more tells the client to refresh fully.
	repo.total = 51
	data = decodeIncrementalData(t, serveOps(r, http.MethodGet, "/admin/usage?since_id=100&page_size=50", nil))
	require.Equal(t, true, data["has_more"])

	repo.rows, repo.total = []service.UsageLog{}, 0
	data = decodeIncrementalData(t, serveOps(r, http.MethodGet, "/admin/usage?since_id=100&page=1", nil))
	require.EqualValues(t, 100, data["latest_id"], "no new rows keeps the cursor")
	require.Empty(t, data["items"])
}

func TestAdminUsageList_SinceIDValidation(t *testing.T) {
	repo := &adminUsageSinceRepo{rows: []service.UsageLog{}}
	r := newAdminUsageSinceRouter(repo)

	for _, bad := range []string{"since_id=abc", "since_id=-1", "since_id=1.5"} {
		rec := serveOps(r, http.MethodGet, "/admin/usage?"+bad, nil)
		require.Equal(t, http.StatusBadRequest, rec.Code, bad)
		require.Contains(t, rec.Body.String(), "INVALID_SINCE_ID", bad)
		require.Contains(t, rec.Body.String(), "Invalid since_id", bad)
	}
	rec := serveOps(r, http.MethodGet, "/admin/usage?since_id=5&page=2", nil)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "SINCE_ID_WITH_PAGE")
	require.Contains(t, rec.Body.String(), "since_id cannot be combined with page")
	require.Zero(t, repo.calls)

	rec = serveOps(r, http.MethodGet, "/admin/usage?since_id=0", nil)
	require.Equal(t, http.StatusOK, rec.Code, "since_id=0 means everything newest-first")

	rec = serveOps(r, http.MethodGet, "/admin/usage?page=2", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Nil(t, repo.filters.SinceID)
	data := decodeIncrementalData(t, rec)
	require.NotContains(t, data, "latest_id")
	require.NotContains(t, data, "since_id")
	require.EqualValues(t, 2, data["page"])
}

type opsErrorSinceRepo struct {
	service.OpsRepository
	filter  *service.OpsErrorLogFilter
	errors  []*service.OpsErrorLog
	hasMore bool
}

func (r *opsErrorSinceRepo) ListErrorLogs(_ context.Context, f *service.OpsErrorLogFilter) (*service.OpsErrorLogList, error) {
	r.filter = f
	return &service.OpsErrorLogList{Errors: r.errors, Total: len(r.errors), Page: 1, PageSize: f.PageSize, HasMore: r.hasMore}, nil
}

func TestOpsRequestErrors_SinceID(t *testing.T) {
	repo := &opsErrorSinceRepo{errors: []*service.OpsErrorLog{{ID: 12}, {ID: 15}}, hasMore: true}
	gin.SetMode(gin.TestMode)
	h := NewOpsHandler(service.NewOpsService(repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil))
	r := gin.New()
	r.GET("/request-errors", h.ListRequestErrors)

	rec := serveOps(r, http.MethodGet, "/request-errors?since_id=9&page_size=2", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, int64(9), *repo.filter.SinceID)
	require.Nil(t, repo.filter.StartTime, "since_id without a time window drops the default 1h window")
	require.Nil(t, repo.filter.EndTime)
	data := decodeIncrementalData(t, rec)
	require.EqualValues(t, 9, data["since_id"])
	require.EqualValues(t, 15, data["latest_id"])
	require.Equal(t, true, data["has_more"])
	require.Nil(t, data["total"])
	query := data["query"].(map[string]any)
	require.Nil(t, query["start_time"])
	require.Nil(t, query["end_time"])

	rec = serveOps(r, http.MethodGet, "/request-errors?since_id=9&time_range=6h", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, repo.filter.StartTime)
	require.InDelta(t, (6 * time.Hour).Seconds(), repo.filter.EndTime.Sub(*repo.filter.StartTime).Seconds(), 1)

	rec = serveOps(r, http.MethodGet, "/request-errors?since_id=9&start_time=2026-01-01T00:00:00Z&end_time=2026-01-02T00:00:00Z", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), repo.filter.StartTime.UTC())

	rec = serveOps(r, http.MethodGet, "/request-errors?since_id=9&page=2", nil)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "SINCE_ID_WITH_PAGE")
	rec = serveOps(r, http.MethodGet, "/request-errors?since_id=x", nil)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "INVALID_SINCE_ID")

	rec = serveOps(r, http.MethodGet, "/request-errors", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Nil(t, repo.filter.SinceID)
	require.NotNil(t, repo.filter.StartTime, "the default 1h window is unchanged without since_id")
	require.NotContains(t, decodeIncrementalData(t, rec), "latest_id")
}
