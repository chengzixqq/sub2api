package admin

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func conditionalGet(r http.Handler, path, etag string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func requireConditionalHeaders(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	etag := rec.Header().Get("ETag")
	require.NotEmpty(t, etag)
	require.Equal(t, "private, no-cache", rec.Header().Get("Cache-Control"))
	require.Equal(t, "If-None-Match", rec.Header().Get("Vary"))
	return etag
}

func TestDashboardStats_ETagIgnoresUptime(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &dashboardStatsContractRepo{stats: usagestats.DashboardStats{TotalCost: 1, Rpm: 3}}
	h := NewDashboardHandler(service.NewDashboardService(repo, nil, nil, nil), nil)
	r := gin.New()
	r.GET("/stats", h.GetStats)

	first := conditionalGet(r, "/stats", "")
	require.Equal(t, http.StatusOK, first.Code)
	etag := requireConditionalHeaders(t, first)
	require.Contains(t, first.Body.String(), `"uptime"`, "uptime is still returned")

	h.startTime = h.startTime.Add(-time.Hour)
	unchanged := conditionalGet(r, "/stats", etag)
	require.Equal(t, http.StatusNotModified, unchanged.Code)
	require.Empty(t, unchanged.Body.String())
	require.Equal(t, etag, unchanged.Header().Get("ETag"))

	repo.stats.TotalCost = 2
	changed := conditionalGet(r, "/stats", etag)
	require.Equal(t, http.StatusOK, changed.Code)
	require.NotEqual(t, etag, changed.Header().Get("ETag"))
}

func TestAdminUserList_ETag304(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := newStubAdminService()
	r := gin.New()
	r.GET("/users", NewUserHandler(svc, nil, nil, nil, nil, nil, nil).List)

	first := conditionalGet(r, "/users?page=1&page_size=20", "")
	require.Equal(t, http.StatusOK, first.Code)
	etag := requireConditionalHeaders(t, first)
	require.JSONEq(t, `{"items":1,"total":1,"page":1,"page_size":20,"pages":1}`, paginatedShape(t, first))

	require.Equal(t, http.StatusNotModified, conditionalGet(r, "/users?page=1&page_size=20", etag).Code)
	require.Equal(t, http.StatusNotModified, conditionalGet(r, "/users?page=1&page_size=20", `W/`+etag).Code)

	svc.users[0].Balance = 99
	changed := conditionalGet(r, "/users?page=1&page_size=20", etag)
	require.Equal(t, http.StatusOK, changed.Code)
	require.NotEqual(t, etag, changed.Header().Get("ETag"))
}

func TestAdminGroupList_ETag304(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for name, cfg := range map[string]*config.Config{"standard": nil, "simple": {RunMode: config.RunModeSimple}} {
		t.Run(name, func(t *testing.T) {
			svc := newStubAdminService()
			r := gin.New()
			r.GET("/groups", NewGroupHandlerWithConfig(svc, nil, nil, nil, cfg).List)

			first := conditionalGet(r, "/groups", "")
			require.Equal(t, http.StatusOK, first.Code, first.Body.String())
			etag := requireConditionalHeaders(t, first)
			require.Equal(t, http.StatusNotModified, conditionalGet(r, "/groups", etag).Code)

			svc.groups[0].Name = "renamed"
			changed := conditionalGet(r, "/groups", etag)
			require.Equal(t, http.StatusOK, changed.Code)
			require.NotEqual(t, etag, changed.Header().Get("ETag"))
		})
	}
}

func TestDashboardSnapshotV2_ETagIgnoresGeneratedAt(t *testing.T) {
	t.Cleanup(resetDashboardReadCachesForTest)
	resetDashboardReadCachesForTest()
	gin.SetMode(gin.TestMode)
	repo := &dashboardStatsContractRepo{stats: usagestats.DashboardStats{TotalCost: 5}}
	h := NewDashboardHandler(service.NewDashboardService(repo, nil, nil, nil), nil)
	r := gin.New()
	r.GET("/snapshot", h.GetSnapshotV2)
	url := "/snapshot?start_date=2026-10-01&end_date=2026-10-02&include_trend=false&include_model_stats=false"

	first := conditionalGet(r, url, "")
	require.Equal(t, http.StatusOK, first.Code, first.Body.String())
	etag := first.Header().Get("ETag")
	require.NotEmpty(t, etag)

	// Regenerate the snapshot: query.generated_at moves, the data does not.
	// The pause outlasts coarse (Windows) clock ticks.
	time.Sleep(20 * time.Millisecond)
	resetDashboardReadCachesForTest()
	again := conditionalGet(r, url, "")
	require.Equal(t, "miss", again.Header().Get("X-Snapshot-Cache"))
	require.NotEqual(t, generatedAtOf(t, first), generatedAtOf(t, again))
	require.Equal(t, etag, again.Header().Get("ETag"))

	resetDashboardReadCachesForTest()
	require.Equal(t, http.StatusNotModified, conditionalGet(r, url, etag).Code)

	resetDashboardReadCachesForTest()
	repo.stats.TotalCost = 6
	require.Equal(t, http.StatusOK, conditionalGet(r, url, etag).Code)
}

func TestOpsDashboardOverview_BucketedCache(t *testing.T) {
	t.Cleanup(resetOpsDashboardBucketCachesForTest)
	fixed := time.Date(2026, 3, 1, 10, 0, 7, 0, time.UTC)
	restore := opsNow
	opsNow = func() time.Time { return fixed }
	t.Cleanup(func() { opsNow = restore })

	for _, path := range []string{"/overview", "/throughput-trend"} {
		resetOpsDashboardBucketCachesForTest()
		repo := &opsDashboardCaptureRepo{}
		r := newOpsDashboardModelFilterRouter(repo)

		first := serveOps(r, http.MethodGet, path+"?time_range=1h", nil)
		require.Equal(t, http.StatusOK, first.Code, path+": "+first.Body.String())
		require.Equal(t, "miss", first.Header().Get("X-Snapshot-Cache"), path)
		f := repo.last()
		require.Equal(t, time.Date(2026, 3, 1, 10, 0, 5, 0, time.UTC), f.EndTime.UTC(), path+": end is floored to 5s")
		require.Equal(t, time.Hour, f.EndTime.Sub(f.StartTime), path)

		opsNow = func() time.Time { return fixed.Add(2 * time.Second) }
		second := serveOps(r, http.MethodGet, path+"?time_range=1h", nil)
		require.Equal(t, "hit", second.Header().Get("X-Snapshot-Cache"), path)
		require.Equal(t, 1, repo.calls, path)

		other := serveOps(r, http.MethodGet, path+"?time_range=1h&model=m", nil)
		require.Equal(t, "miss", other.Header().Get("X-Snapshot-Cache"), path+": filters are part of the key")
		require.Equal(t, 2, repo.calls, path)

		opsNow = func() time.Time { return fixed.Add(4 * time.Second) }
		next := serveOps(r, http.MethodGet, path+"?time_range=1h", nil)
		require.Equal(t, "miss", next.Header().Get("X-Snapshot-Cache"), path+": a new 5s bucket reloads")

		explicit := serveOps(r, http.MethodGet, path+"?start_time=2026-01-01T00:00:01.5Z&end_time=2026-01-01T00:59:59.5Z", nil)
		require.Equal(t, http.StatusOK, explicit.Code, path)
		require.Equal(t, time.Date(2026, 1, 1, 0, 59, 59, 500_000_000, time.UTC), repo.last().EndTime.UTC(), path+": explicit range is not floored")
		opsNow = func() time.Time { return fixed }
	}
}

func TestOpsDashboardBucketedCache_MonitoringCheckedBeforeCache(t *testing.T) {
	t.Cleanup(resetOpsDashboardBucketCachesForTest)
	resetOpsDashboardBucketCachesForTest()
	repo := &opsDashboardCaptureRepo{}
	r := newOpsDashboardModelFilterRouter(repo)
	require.Equal(t, http.StatusOK, serveOps(r, http.MethodGet, "/overview", nil).Code)

	disabled := gin.New()
	h := NewOpsHandler(service.NewOpsService(repo, nil, &config.Config{Ops: config.OpsConfig{Enabled: false}}, nil, nil, nil, nil, nil, nil, nil, nil))
	disabled.GET("/overview", h.GetDashboardOverview)
	require.Equal(t, http.StatusNotFound, serveOps(disabled, http.MethodGet, "/overview", nil).Code)
}

func generatedAtOf(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	data := decodeIncrementalData(t, rec)
	return data["query"].(map[string]any)["generated_at"].(string)
}

func paginatedShape(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	data := decodeIncrementalData(t, rec)
	items, _ := data["items"].([]any)
	data["items"] = len(items)
	return mustMarshalDashboardCacheKey(data)
}
