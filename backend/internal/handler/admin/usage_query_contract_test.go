package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagequery"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestAdminUsageQueryContract(t *testing.T) {
	for _, path := range []string{"/admin/usage", "/admin/usage/stats"} {
		t.Run(path, func(t *testing.T) {
			repo := &adminUsageRepoCapture{}
			router := newAdminUsageRequestTypeTestRouter(repo)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path+"?start_time=2030-01-01T12:15:00Z&end_time=2030-01-01T12:16:00Z&timezone=Asia%2FShanghai&start_date=invalid&count_mode=deferred&nocache=true", nil))
			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
			filters := repo.listFilters
			if path == "/admin/usage/stats" {
				filters = repo.statsFilters
			}
			require.NotNil(t, filters.StartTime)
			require.NotNil(t, filters.EndTime)
			require.Equal(t, time.Minute, filters.EndTime.Sub(*filters.StartTime))
			var payload struct {
				Data map[string]json.RawMessage `json:"data"`
			}
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &payload))
			var meta usagequery.Metadata
			require.NoError(t, json.Unmarshal(payload.Data["query"], &meta))
			require.Equal(t, "Asia/Shanghai", meta.Timezone)
			require.Equal(t, 20, meta.StartTime.Hour())
			if path == "/admin/usage" {
				require.True(t, filters.DeferredTotal)
				require.JSONEq(t, "null", string(payload.Data["total"]))
				require.JSONEq(t, "false", string(payload.Data["total_exact"]))
			}
		})
	}
}

func TestAdminUsageRejectsInvalidRangeBeforeQuerying(t *testing.T) {
	for _, path := range []string{"/admin/usage", "/admin/usage/stats"} {
		for _, query := range []string{"start_time=2030-01-01T12:00:00Z", "end_time=2030-01-01T12:00:00Z", "start_time=invalid&end_time=invalid", "start_time=2030-01-02T00:00:00Z&end_time=2030-01-01T00:00:00Z"} {
			router := newAdminUsageRequestTypeTestRouter(&adminUsageRepoCapture{})
			w := httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path+"?"+query, nil))
			require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
		}
	}
}

func TestUsageCacheKeySeparatesIdentityScopeAndTimezone(t *testing.T) {
	base := service.WithScope(context.Background(), service.AdminScope())
	key := func(ctx context.Context, user, zone string) string {
		return scopedUsageCacheKey(usagequery.WithOptions(ctx, usagequery.Options{Identity: user, Timezone: zone}), "query")
	}
	require.NotEqual(t, key(base, "user:1", "UTC"), key(base, "user:2", "UTC"))
	require.NotEqual(t, key(base, "user:1", "UTC"), key(base, "user:1", "Asia/Shanghai"))
	require.NotEqual(t, key(base, "user:1", "UTC"), key(service.WithUsageAccountScope(base, []int64{}), "user:1", "UTC"))
	require.NotEqual(t, key(service.WithUsageAccountScope(base, []int64{1}), "user:1", "UTC"), key(service.WithUsageAccountScope(base, []int64{2}), "user:1", "UTC"))
	require.Equal(t, key(service.WithUsageAccountScope(base, []int64{1, 2}), "user:1", "UTC"), key(service.WithUsageAccountScope(base, []int64{2, 1}), "user:1", "UTC"))
}

func TestDashboardForceRefreshReplacesCacheAndSnapshotDoesNotNestTTL(t *testing.T) {
	resetDashboardReadCachesForTest()
	t.Cleanup(resetDashboardReadCachesForTest)
	repo := &dashboardUsageRepoCacheProbe{}
	h := NewDashboardHandler(service.NewDashboardService(repo, nil, nil, nil), nil)
	router := gin.New()
	router.GET("/trend", h.GetUsageTrend)
	router.GET("/snapshot", h.GetSnapshotV2)
	query := "?start_time=2030-01-01T00:00:00Z&end_time=2030-01-02T00:00:00Z&timezone=UTC&granularity=hour&include_stats=false&include_model_stats=false"
	get := func(path string) {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	}
	get("/trend" + query)
	require.EqualValues(t, 1, repo.trendCalls.Load())
	get("/snapshot" + query)
	require.EqualValues(t, 2, repo.trendCalls.Load(), "snapshot must load fresh components instead of re-aging the standalone cache")
	get("/snapshot" + query)
	require.EqualValues(t, 2, repo.trendCalls.Load())
	get("/snapshot" + query + "&force_refresh=true")
	require.EqualValues(t, 3, repo.trendCalls.Load())
	get("/snapshot" + query)
	require.EqualValues(t, 3, repo.trendCalls.Load(), "refresh must replace the snapshot entry")
	get("/trend" + query + "&force_refresh=true")
	require.EqualValues(t, 4, repo.trendCalls.Load())
	get("/trend" + query)
	require.EqualValues(t, 4, repo.trendCalls.Load())
}
