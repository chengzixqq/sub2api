package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type dashboardLiveRPMStub struct {
	total int
	err   error
}

func (*dashboardLiveRPMStub) GetRollingRPM(context.Context, int64) (int, error) {
	panic("unexpected individual RPM read")
}
func (*dashboardLiveRPMStub) GetRollingRPMBatch(context.Context, []int64) (map[int64]int, error) {
	panic("unexpected batch RPM read")
}
func (s *dashboardLiveRPMStub) GetTotalRollingRPM(context.Context) (int, error) {
	return s.total, s.err
}

func TestDashboardHandler_SnapshotRuntimeRPMUpdatesCachedETag(t *testing.T) {
	t.Cleanup(resetDashboardReadCachesForTest)
	resetDashboardReadCachesForTest()
	gin.SetMode(gin.TestMode)
	repo := &dashboardStatsContractRepo{stats: usagestats.DashboardStats{Rpm: 2000, Tpm: 100000, TotalAccountCost: 12.34}}
	svc := service.NewDashboardService(repo, nil, nil, nil)
	runtime := &dashboardLiveRPMStub{total: 1000}
	svc.SetRollingRPMCache(runtime)
	h := NewDashboardHandler(svc, nil)
	router := gin.New()
	router.GET("/snapshot", h.GetSnapshotV2)
	router.GET("/stats", h.GetStats)
	url := "/snapshot?start_date=2026-10-01&end_date=2026-10-02&include_trend=false&include_model_stats=false"
	get := func(path, etag string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.Header.Set("If-None-Match", etag)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	first := get(url, "")
	require.Equal(t, http.StatusOK, first.Code, first.Body.String())
	require.Equal(t, "miss", first.Header().Get("X-Snapshot-Cache"))
	require.Contains(t, first.Body.String(), `"rpm":1000`)
	runtime.total = 1500
	second := get(url, first.Header().Get("ETag"))
	require.Equal(t, http.StatusOK, second.Code, second.Body.String())
	require.Equal(t, "hit", second.Header().Get("X-Snapshot-Cache"))
	require.Contains(t, second.Body.String(), `"rpm":1500`)
	require.NotEqual(t, first.Header().Get("ETag"), second.Header().Get("ETag"))
	third := get(url, second.Header().Get("ETag"))
	require.Equal(t, http.StatusNotModified, third.Code)
	stats := get("/stats", "")
	require.Equal(t, http.StatusOK, stats.Code)
	require.Contains(t, stats.Body.String(), `"rpm":1500`)
	runtime.err = errors.New("redis unavailable")
	failure := get(url, second.Header().Get("ETag"))
	require.Equal(t, http.StatusOK, failure.Code)
	var payload struct {
		Data struct {
			Stats struct {
				RPM       int     `json:"rpm"`
				Available bool    `json:"rpm_available"`
				Cost      float64 `json:"total_account_cost"`
			} `json:"stats"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(failure.Body.Bytes(), &payload))
	require.Zero(t, payload.Data.Stats.RPM)
	require.False(t, payload.Data.Stats.Available)
	require.Equal(t, 12.34, payload.Data.Stats.Cost)
}
