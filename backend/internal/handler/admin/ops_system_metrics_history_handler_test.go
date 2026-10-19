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

type systemMetricsHistoryCaptureRepo struct {
	service.OpsRepository
	calls  int
	start  time.Time
	end    time.Time
	bucket int
}

func (r *systemMetricsHistoryCaptureRepo) ListSystemMetricsHistory(_ context.Context, start, end time.Time, bucketSeconds int) ([]*service.OpsSystemMetricsHistoryPoint, error) {
	r.calls++
	r.start, r.end, r.bucket = start, end, bucketSeconds
	cpu := 12.5
	return []*service.OpsSystemMetricsHistoryPoint{{CreatedAt: end, SampleCount: 1, CPUUsagePercent: &cpu}}, nil
}

func TestGetSystemMetricsHistoryHandler(t *testing.T) {
	fixed := time.Date(2026, 10, 18, 1, 41, 0, 0, time.UTC)
	restore := opsNow
	opsNow = func() time.Time { return fixed }
	t.Cleanup(func() { opsNow = restore })

	repo := &systemMetricsHistoryCaptureRepo{}
	gin.SetMode(gin.TestMode)
	h := NewOpsHandler(service.NewOpsService(repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil))
	r := gin.New()
	r.GET("/history", h.GetSystemMetricsHistory)

	rec := serveOps(r, http.MethodGet, "/history?time_range=1h", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.True(t, repo.end.Equal(fixed))
	require.Equal(t, time.Hour, repo.end.Sub(repo.start))
	require.Equal(t, 60, repo.bucket)
	var resp struct {
		Data struct {
			StartTime     time.Time         `json:"start_time"`
			EndTime       time.Time         `json:"end_time"`
			WindowMinutes int               `json:"window_minutes"`
			BucketSeconds int               `json:"bucket_seconds"`
			Points        []json.RawMessage `json:"points"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Equal(t, 1, resp.Data.WindowMinutes)
	require.Equal(t, 60, resp.Data.BucketSeconds)
	require.True(t, resp.Data.EndTime.Equal(fixed))
	require.True(t, resp.Data.StartTime.Equal(fixed.Add(-time.Hour)))
	require.Len(t, resp.Data.Points, 1)
	require.Contains(t, string(resp.Data.Points[0]), `"cpu_usage_percent":12.5`)
	require.Contains(t, string(resp.Data.Points[0]), `"sample_count":1`)

	rec = serveOps(r, http.MethodGet, "/history", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, time.Hour, repo.end.Sub(repo.start), "default window is 1h")
	require.Equal(t, 60, repo.bucket)

	rec = serveOps(r, http.MethodGet, "/history?time_range=6h&max_points=36", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, 600, repo.bucket)

	rec = serveOps(r, http.MethodGet, "/history?time_range=24h", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, 1440, repo.bucket)

	rec = serveOps(r, http.MethodGet, "/history?start_time=2026-10-17T22:00:00Z&end_time=2026-10-18T00:00:00Z", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.True(t, repo.start.Equal(time.Date(2026, 10, 17, 22, 0, 0, 0, time.UTC)))
	require.True(t, repo.end.Equal(time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC)))
	require.Equal(t, 120, repo.bucket)

	calls := repo.calls
	for _, tc := range []struct {
		query  string
		reason string
	}{
		{"max_points=abc", "INVALID_MAX_POINTS"},
		{"max_points=0", "INVALID_MAX_POINTS"},
		{"max_points=1441", "INVALID_MAX_POINTS"},
		{"start_time=2026-10-17T22:00:00Z", ""},
		{"start_time=2026-10-18T00:00:00Z&end_time=2026-10-17T22:00:00Z", ""},
		{"start_time=2026-09-01T00:00:00Z&end_time=2026-10-18T00:00:00Z", "OPS_TIME_RANGE_TOO_LARGE"},
	} {
		rec = serveOps(r, http.MethodGet, "/history?"+tc.query, nil)
		require.Equal(t, http.StatusBadRequest, rec.Code, tc.query)
		var body struct {
			Reason string `json:"reason"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
		if tc.reason != "" {
			require.Equal(t, tc.reason, body.Reason, tc.query)
		}
	}
	require.Equal(t, calls, repo.calls, "rejected requests never reach the repository")
}
