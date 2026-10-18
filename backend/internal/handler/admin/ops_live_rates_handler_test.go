package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type liveRatesCaptureRepo struct {
	service.OpsRepository
	calls  int
	filter *service.OpsDashboardFilter
}

func (r *liveRatesCaptureRepo) GetLiveRateCounts(_ context.Context, f *service.OpsDashboardFilter, _ bool) (*service.OpsLiveRateCounts, error) {
	r.calls++
	r.filter = f
	return &service.OpsLiveRateCounts{SuccessCount: 30, TokenConsumed: 3000}, nil
}

func TestDashboardLiveRates(t *testing.T) {
	dashboardRatesCache = newSnapshotCache(time.Minute)
	repo := &liveRatesCaptureRepo{}
	gin.SetMode(gin.TestMode)
	h := NewOpsHandler(service.NewOpsService(repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil))
	r := gin.New()
	r.GET("/rates", h.GetLiveRates)

	rec := serveOps(r, http.MethodGet, "/rates?window=1m&platform=anthropic&group_id=4", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, "anthropic", repo.filter.Platform)
	require.Equal(t, int64(4), *repo.filter.GroupID)
	require.Equal(t, time.Minute, repo.filter.EndTime.Sub(repo.filter.StartTime))
	var resp struct {
		Data service.OpsLiveRates `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Equal(t, 60, resp.Data.WindowSeconds)
	require.InDelta(t, 3000.0, resp.Data.TPM, 1e-9)

	rec = serveOps(r, http.MethodGet, "/rates?window=1m&platform=anthropic&group_id=4", nil)
	require.Equal(t, "hit", rec.Header().Get("X-Snapshot-Cache"))
	require.Equal(t, 1, repo.calls)

	rec = serveOps(r, http.MethodGet, "/rates", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, 5*time.Minute, repo.filter.EndTime.Sub(repo.filter.StartTime), "default window is 5m")

	for _, bad := range []string{"window=2m", "group_id=x", "group_id=0"} {
		rec = serveOps(r, http.MethodGet, "/rates?"+bad, nil)
		require.Equal(t, http.StatusBadRequest, rec.Code, bad)
	}
}
