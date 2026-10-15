package admin

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagequery"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type dashboardCacheContextProbe struct {
	service.UsageLogRepository
	calls   atomic.Int32
	entered chan struct{}
	release chan struct{}
}

func (r *dashboardCacheContextProbe) GetUserUsageTrend(ctx context.Context, _, _ time.Time, _ string, _ int, _ string) ([]usagestats.UserUsageTrendPoint, error) {
	r.calls.Add(1)
	id := int64(1)
	if ids, restricted := service.UsageAccountScopeFrom(ctx); restricted && len(ids) > 0 {
		id = ids[0]
	}
	return []usagestats.UserUsageTrendPoint{{Date: usagequery.OptionsFrom(ctx).Timezone, UserID: id}}, nil
}

func (r *dashboardCacheContextProbe) load() float64 {
	n := r.calls.Add(1)
	if r.entered != nil {
		r.entered <- struct{}{}
	}
	if r.release != nil {
		<-r.release
	}
	return float64(n)
}

func (r *dashboardCacheContextProbe) GetBatchUserUsageStats(context.Context, []int64, time.Time, time.Time) (map[int64]*usagestats.BatchUserUsageStats, error) {
	return map[int64]*usagestats.BatchUserUsageStats{1: {UserID: 1, TodayActualCost: r.load()}}, nil
}

func (r *dashboardCacheContextProbe) GetBatchAPIKeyUsageStats(context.Context, []int64, time.Time, time.Time) (map[int64]*usagestats.BatchAPIKeyUsageStats, error) {
	return map[int64]*usagestats.BatchAPIKeyUsageStats{1: {APIKeyID: 1, TodayActualCost: r.load()}}, nil
}

func TestDashboardUserTrendCache_SeparatesQueryContexts(t *testing.T) {
	for _, dimension := range []string{"timezone", "account_scope", "identity"} {
		t.Run(dimension, func(t *testing.T) {
			dashboardUsersTrendCache = newSnapshotCache(time.Minute)
			t.Cleanup(resetDashboardReadCachesForTest)
			repo := &dashboardCacheContextProbe{}
			h := NewDashboardHandler(service.NewDashboardService(repo, nil, nil, nil), nil)
			start := time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)
			ctxA := usagequery.WithOptions(context.Background(), usagequery.Options{Identity: "admin:1", Timezone: "UTC"})
			ctxB := usagequery.WithOptions(context.Background(), usagequery.Options{Identity: "admin:1", Timezone: "Asia/Shanghai"})
			if dimension == "account_scope" {
				ctxA = service.WithUsageAccountScope(ctxA, []int64{101})
				ctxB = service.WithUsageAccountScope(ctxA, []int64{202})
			} else if dimension == "identity" {
				ctxB = usagequery.WithOptions(context.Background(), usagequery.Options{Identity: "admin:2", Timezone: "UTC"})
			}
			a, hit, err := h.getUserUsageTrendCached(ctxA, start, start.Add(24*time.Hour), "hour", 12, "tokens")
			require.NoError(t, err)
			require.False(t, hit)
			b, hit, err := h.getUserUsageTrendCached(ctxB, start, start.Add(24*time.Hour), "hour", 12, "tokens")
			require.NoError(t, err)
			t.Logf("dimension=%s first=%+v second=%+v second_hit=%v backend_loads=%d", dimension, a, b, hit, repo.calls.Load())
			require.False(t, hit, "distinct query contexts must not share cached results")
			require.Equal(t, int32(2), repo.calls.Load())
			if dimension != "identity" {
				require.NotEqual(t, a, b)
			}
		})
	}
}

func resetDashboardBatchCachesForTest() {
	dashboardBatchUsersUsageCache = newSnapshotCache(time.Minute)
	dashboardBatchAPIKeysUsageCache = newSnapshotCache(time.Minute)
}

func dashboardBatchCacheRouter(repo *dashboardCacheContextProbe, kind string) (*gin.Engine, string) {
	gin.SetMode(gin.TestMode)
	h := NewDashboardHandler(service.NewDashboardService(repo, nil, nil, nil), nil)
	router := gin.New()
	if kind == "users" {
		router.POST("/batch", h.GetBatchUsersUsage)
		return router, `{"user_ids":[1,1]}`
	}
	router.POST("/batch", h.GetBatchAPIKeysUsage)
	return router, `{"api_key_ids":[1,1]}`
}

func dashboardBatchCacheRequest(router http.Handler, body, query string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/batch"+query, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func TestDashboardBatchUsageCache_HonorsExplicitRefresh(t *testing.T) {
	for _, kind := range []string{"users", "keys"} {
		for _, query := range []string{"?force_refresh=true", "?nocache=1"} {
			t.Run(kind+query, func(t *testing.T) {
				resetDashboardBatchCachesForTest()
				t.Cleanup(resetDashboardBatchCachesForTest)
				repo := &dashboardCacheContextProbe{}
				router, body := dashboardBatchCacheRouter(repo, kind)
				a := dashboardBatchCacheRequest(router, body, "")
				b := dashboardBatchCacheRequest(router, body, query)
				require.Equal(t, http.StatusOK, a.Code)
				require.Equal(t, http.StatusOK, b.Code)
				t.Logf("kind=%s refresh=%s first=%s second=%s cache=%s loads=%d", kind, query, a.Body, b.Body, b.Header().Get("X-Snapshot-Cache"), repo.calls.Load())
				require.Equal(t, "miss", b.Header().Get("X-Snapshot-Cache"))
				require.Equal(t, int32(2), repo.calls.Load())
				require.NotEqual(t, a.Body.String(), b.Body.String())
			})
		}
	}
}

func TestDashboardBatchUsageCache_CoalescesConcurrentColdRequests(t *testing.T) {
	for _, kind := range []string{"users", "keys"} {
		t.Run(kind, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				resetDashboardBatchCachesForTest()
				t.Cleanup(resetDashboardBatchCachesForTest)
				repo := &dashboardCacheContextProbe{entered: make(chan struct{}, 8), release: make(chan struct{})}
				router, body := dashboardBatchCacheRouter(repo, kind)
				var done sync.WaitGroup
				results := make(chan *httptest.ResponseRecorder, 8)
				start := make(chan struct{})
				for range 8 {
					done.Add(1)
					go func() { defer done.Done(); <-start; results <- dashboardBatchCacheRequest(router, body, "") }()
				}
				close(start)
				select {
				case <-repo.entered:
				case <-time.After(time.Second):
					t.Fatal("backend load not started")
				}
				// Wait until all eight HTTP requests are blocked on shared work.
				synctest.Wait()
				close(repo.release)
				done.Wait()
				close(results)
				for rec := range results {
					require.Equal(t, http.StatusOK, rec.Code)
				}
				t.Logf("kind=%s concurrent_requests=8 backend_loads=%d", kind, repo.calls.Load())
				require.Equal(t, int32(1), repo.calls.Load())
			})
		})
	}
}

func TestDashboardBatchUsageCache_TodayChangesAtServerMidnight(t *testing.T) {
	for _, kind := range []string{"users", "keys"} {
		t.Run(kind, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				// A long TTL keeps the old entry valid across midnight. The day in
				// the query key must select a new result independently of expiry.
				dashboardBatchUsersUsageCache = newSnapshotCache(48 * time.Hour)
				dashboardBatchAPIKeysUsageCache = newSnapshotCache(48 * time.Hour)
				t.Cleanup(resetDashboardBatchCachesForTest)
				repo := &dashboardCacheContextProbe{}
				router, body := dashboardBatchCacheRouter(repo, kind)
				a := dashboardBatchCacheRequest(router, body, "")
				require.Equal(t, http.StatusOK, a.Code)
				firstDay := timezone.Today()
				time.Sleep(firstDay.AddDate(0, 0, 1).Sub(timezone.Now()) + time.Second)
				require.NotEqual(t, firstDay, timezone.Today())
				b := dashboardBatchCacheRequest(router, body, "")
				require.Equal(t, http.StatusOK, b.Code)
				t.Logf("kind=%s first_day=%s second_day=%s cache=%s loads=%d", kind, firstDay.Format(time.DateOnly), timezone.Today().Format(time.DateOnly), b.Header().Get("X-Snapshot-Cache"), repo.calls.Load())
				require.Equal(t, "miss", b.Header().Get("X-Snapshot-Cache"))
				require.Equal(t, int32(2), repo.calls.Load())
				require.NotEqual(t, a.Body.String(), b.Body.String())
			})
		})
	}
}
