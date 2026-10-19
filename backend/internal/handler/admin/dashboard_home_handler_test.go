package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type fakeHomeBuilder struct {
	mu       sync.Mutex
	calls    int
	last     service.DashboardHomeQuery
	degraded bool
}

func (f *fakeHomeBuilder) Build(_ context.Context, q service.DashboardHomeQuery) *service.DashboardHome {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.last = q
	home := &service.DashboardHome{
		GeneratedAt: time.Date(2026, 10, 18, 1, 41, f.calls, 0, time.UTC),
		Timezone:    q.Location.String(),
		Stats:       &service.DashboardHomeSection[service.DashboardHomeStats]{Status: service.DashboardHomeStatusOK, Data: &service.DashboardHomeStats{TodayRequests: 18204}},
		Ops:         &service.DashboardHomeSection[service.DashboardHomeOps]{Status: service.DashboardHomeStatusOK, Data: &service.DashboardHomeOps{HealthScore: 94}},
	}
	if f.degraded {
		home.Ops = &service.DashboardHomeSection[service.DashboardHomeOps]{Status: service.DashboardHomeStatusTimeout, Error: service.DashboardHomeErrorSectionTimeout}
	}
	return home
}

func (f *fakeHomeBuilder) snapshot() (int, service.DashboardHomeQuery) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls, f.last
}

func newDashboardHomeRouter(builder dashboardHomeBuilder) *gin.Engine {
	gin.SetMode(gin.TestMode)
	dashboardHomeCache = newSnapshotCache(15 * time.Second)
	h := NewDashboardHandler(nil, nil)
	if builder != nil {
		h.home = builder
	}
	r := gin.New()
	r.GET("/api/v1/admin/dashboard/home", h.GetHome)
	return r
}

func homeGet(r http.Handler, query, etag string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/dashboard/home"+query, nil)
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func TestDashboardHomeHandlerParamValidation(t *testing.T) {
	builder := &fakeHomeBuilder{}
	r := newDashboardHomeRouter(builder)

	bad := homeGet(r, "?timezone=Mars/Base", "")
	require.Equal(t, http.StatusBadRequest, bad.Code)
	require.Contains(t, bad.Body.String(), `"reason":"INVALID_TIMEZONE"`)

	bad = homeGet(r, "?include=stats,nope", "")
	require.Equal(t, http.StatusBadRequest, bad.Code)
	require.Contains(t, bad.Body.String(), `"reason":"INVALID_INCLUDE"`)
	calls, _ := builder.snapshot()
	require.Zero(t, calls)

	ok := homeGet(r, "?timezone=Asia/Shanghai&include=stats,ops", "")
	require.Equal(t, http.StatusOK, ok.Code, ok.Body.String())
	_, last := builder.snapshot()
	require.Equal(t, "Asia/Shanghai", last.Location.String())
	require.Equal(t, []string{"ops", "stats"}, last.Include.Names())

	ok = homeGet(r, "", "")
	require.Equal(t, http.StatusOK, ok.Code)
	_, last = builder.snapshot()
	require.Equal(t, timezone.Location(), last.Location)
	require.Len(t, last.Include.Names(), 7)

	var body struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	require.NoError(t, json.Unmarshal(ok.Body.Bytes(), &body))
	require.Contains(t, body.Data, "generated_at")
	require.Contains(t, body.Data, "stats")
	require.NotContains(t, body.Data, "channels", "sections the builder left out are omitted")
}

func TestDashboardHomeHandlerUnavailableWithoutService(t *testing.T) {
	r := newDashboardHomeRouter(nil)
	rec := homeGet(r, "", "")
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.Contains(t, rec.Body.String(), `"reason":"SERVICE_UNAVAILABLE"`)
}

func TestDashboardHomeHandlerCacheAndETag(t *testing.T) {
	builder := &fakeHomeBuilder{}
	r := newDashboardHomeRouter(builder)

	first := homeGet(r, "", "")
	require.Equal(t, http.StatusOK, first.Code)
	require.Equal(t, "miss", first.Header().Get("X-Snapshot-Cache"))
	etag := first.Header().Get("ETag")
	require.NotEmpty(t, etag)
	require.Equal(t, "If-None-Match", first.Header().Get("Vary"))
	require.Equal(t, "private, no-cache", first.Header().Get("Cache-Control"))

	second := homeGet(r, "", "")
	require.Equal(t, "hit", second.Header().Get("X-Snapshot-Cache"))
	calls, _ := builder.snapshot()
	require.Equal(t, 1, calls)

	// A forced rebuild changes generated_at only, so the validator still matches.
	refreshed := homeGet(r, "?force_refresh=true", "")
	require.Equal(t, http.StatusOK, refreshed.Code)
	calls, last := builder.snapshot()
	require.Equal(t, 2, calls)
	require.True(t, last.Refresh)
	require.NotEqual(t, first.Body.String(), refreshed.Body.String())
	require.Equal(t, etag, refreshed.Header().Get("ETag"))

	notModified := homeGet(r, "", etag)
	require.Equal(t, http.StatusNotModified, notModified.Code)
	require.Empty(t, notModified.Body.String())

	// timezone and include are part of the cache key.
	other := homeGet(r, "?timezone=Asia/Shanghai", "")
	require.Equal(t, "miss", other.Header().Get("X-Snapshot-Cache"))
}

func TestDashboardHomeHandlerDegradedNotCached(t *testing.T) {
	builder := &fakeHomeBuilder{degraded: true}
	r := newDashboardHomeRouter(builder)

	for i := 1; i <= 2; i++ {
		rec := homeGet(r, "", "")
		require.Equal(t, http.StatusOK, rec.Code)
		require.Equal(t, "miss", rec.Header().Get("X-Snapshot-Cache"))
		require.NotEmpty(t, rec.Header().Get("ETag"))
		var body struct {
			Data struct {
				Ops struct {
					Status string `json:"status"`
					Error  string `json:"error"`
				} `json:"ops"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
		require.Equal(t, "timeout", body.Data.Ops.Status)
		require.Equal(t, "SECTION_TIMEOUT", body.Data.Ops.Error)
		calls, _ := builder.snapshot()
		require.Equal(t, i, calls)
	}
}
