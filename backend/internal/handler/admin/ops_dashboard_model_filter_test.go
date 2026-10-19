package admin

import (
	"context"
	"database/sql"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type opsDashboardCaptureRepo struct {
	service.OpsRepository
	mu      sync.Mutex
	filters []service.OpsDashboardFilter
	token   *service.OpsOpenAITokenStatsFilter
	calls   int
}

func (r *opsDashboardCaptureRepo) record(f *service.OpsDashboardFilter) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	r.filters = append(r.filters, *f)
}

func (r *opsDashboardCaptureRepo) reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = 0
	r.filters = nil
	r.token = nil
}

func (r *opsDashboardCaptureRepo) last() service.OpsDashboardFilter {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.filters[len(r.filters)-1]
}

func (r *opsDashboardCaptureRepo) GetDashboardOverview(_ context.Context, f *service.OpsDashboardFilter) (*service.OpsDashboardOverview, error) {
	r.record(f)
	return &service.OpsDashboardOverview{}, nil
}

func (r *opsDashboardCaptureRepo) GetThroughputTrend(_ context.Context, f *service.OpsDashboardFilter, _ int) (*service.OpsThroughputTrendResponse, error) {
	r.record(f)
	return &service.OpsThroughputTrendResponse{}, nil
}

func (r *opsDashboardCaptureRepo) GetLatencyHistogram(_ context.Context, f *service.OpsDashboardFilter) (*service.OpsLatencyHistogramResponse, error) {
	r.record(f)
	return &service.OpsLatencyHistogramResponse{}, nil
}

func (r *opsDashboardCaptureRepo) GetErrorTrend(_ context.Context, f *service.OpsDashboardFilter, _ int) (*service.OpsErrorTrendResponse, error) {
	r.record(f)
	return &service.OpsErrorTrendResponse{}, nil
}

func (r *opsDashboardCaptureRepo) GetErrorDistribution(_ context.Context, f *service.OpsDashboardFilter) (*service.OpsErrorDistributionResponse, error) {
	r.record(f)
	return &service.OpsErrorDistributionResponse{}, nil
}

func (r *opsDashboardCaptureRepo) GetOpenAITokenStats(_ context.Context, f *service.OpsOpenAITokenStatsFilter) (*service.OpsOpenAITokenStatsResponse, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	copied := *f
	r.token = &copied
	return &service.OpsOpenAITokenStatsResponse{}, nil
}

func (r *opsDashboardCaptureRepo) GetLatestSystemMetrics(context.Context, int) (*service.OpsSystemMetricsSnapshot, error) {
	return nil, sql.ErrNoRows
}

func (r *opsDashboardCaptureRepo) ListJobHeartbeats(context.Context) ([]*service.OpsJobHeartbeat, error) {
	return nil, nil
}

var opsDashboardModelFilterPaths = []string{
	"/overview",
	"/throughput-trend",
	"/latency-histogram",
	"/error-trend",
	"/error-distribution",
	"/snapshot-v2",
}

func newOpsDashboardModelFilterRouter(repo *opsDashboardCaptureRepo) *gin.Engine {
	gin.SetMode(gin.TestMode)
	h := NewOpsHandler(service.NewOpsService(repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil))
	r := gin.New()
	r.GET("/overview", h.GetDashboardOverview)
	r.GET("/throughput-trend", h.GetDashboardThroughputTrend)
	r.GET("/latency-histogram", h.GetDashboardLatencyHistogram)
	r.GET("/error-trend", h.GetDashboardErrorTrend)
	r.GET("/error-distribution", h.GetDashboardErrorDistribution)
	r.GET("/snapshot-v2", h.GetDashboardSnapshotV2)
	r.GET("/openai-token-stats", h.GetDashboardOpenAITokenStats)
	return r
}

func resetOpsDashboardSnapshotV2CacheForTest() {
	opsDashboardSnapshotV2Cache = newSnapshotCache(30 * time.Second)
	resetOpsDashboardBucketCachesForTest()
}

func TestOpsDashboardModelFilter_ParsedForAllEndpoints(t *testing.T) {
	t.Cleanup(resetOpsDashboardSnapshotV2CacheForTest)
	resetOpsDashboardSnapshotV2CacheForTest()
	repo := &opsDashboardCaptureRepo{}
	r := newOpsDashboardModelFilterRouter(repo)

	for _, path := range opsDashboardModelFilterPaths {
		repo.reset()
		rec := serveOps(r, http.MethodGet, path+"?model=claude-opus-4&model_source=upstream&mode=preagg&group_id=3", nil)
		require.Equal(t, http.StatusOK, rec.Code, path+": "+rec.Body.String())
		f := repo.last()
		require.Equal(t, "claude-opus-4", f.Model, path)
		require.Equal(t, "upstream", f.ModelSource, path)
		require.Equal(t, service.OpsQueryModeRaw, f.QueryMode, path+": model has no pre-aggregated dimension")
		require.Equal(t, int64(3), *f.GroupID, path)

		repo.reset()
		rec = serveOps(r, http.MethodGet, path+"?model=%20%20m%20%20", nil)
		require.Equal(t, http.StatusOK, rec.Code, path)
		f = repo.last()
		require.Equal(t, "m", f.Model, path)
		require.Equal(t, "requested", f.ModelSource, path)
	}

	repo.reset()
	rec := serveOps(r, http.MethodGet, "/openai-token-stats?model=claude-opus-4&model_source=upstream&top_n=5", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, "claude-opus-4", repo.token.Model)
	require.Equal(t, "upstream", repo.token.ModelSource)
	require.Equal(t, 5, repo.token.TopN)

	repo.reset()
	rec = serveOps(r, http.MethodGet, "/openai-token-stats?model=m", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, "m", repo.token.Model)
	require.Equal(t, "requested", repo.token.ModelSource)
}

func TestOpsDashboardModelFilter_RejectsInvalidSource(t *testing.T) {
	t.Cleanup(resetOpsDashboardSnapshotV2CacheForTest)
	resetOpsDashboardSnapshotV2CacheForTest()
	repo := &opsDashboardCaptureRepo{}
	r := newOpsDashboardModelFilterRouter(repo)

	paths := append(append([]string(nil), opsDashboardModelFilterPaths...), "/openai-token-stats")
	for _, path := range paths {
		for _, query := range []string{"model=m&model_source=mapping", "model=m&model_source=bogus", "model_source=mapping"} {
			repo.reset()
			rec := serveOps(r, http.MethodGet, path+"?"+query, nil)
			require.Equal(t, http.StatusBadRequest, rec.Code, path+"?"+query)
			require.Contains(t, rec.Body.String(), "Invalid model_source, use requested/upstream", path+"?"+query)
			require.Zero(t, repo.calls, path+"?"+query)
		}
	}
}

func TestOpsDashboardModelFilter_DefaultUnchanged(t *testing.T) {
	t.Cleanup(resetOpsDashboardSnapshotV2CacheForTest)
	resetOpsDashboardSnapshotV2CacheForTest()
	repo := &opsDashboardCaptureRepo{}
	r := newOpsDashboardModelFilterRouter(repo)

	for _, path := range opsDashboardModelFilterPaths {
		repo.reset()
		rec := serveOps(r, http.MethodGet, path+"?mode=preagg&platform=openai&model_source=upstream", nil)
		require.Equal(t, http.StatusOK, rec.Code, path+": "+rec.Body.String())
		f := repo.last()
		require.Empty(t, f.Model, path)
		require.Empty(t, f.ModelSource, path)
		require.Equal(t, service.OpsQueryModePreagg, f.QueryMode, path)
		require.Equal(t, "openai", f.Platform, path)

		rec = serveOps(r, http.MethodGet, path+"?group_id=abc", nil)
		require.Equal(t, http.StatusBadRequest, rec.Code, path)
		require.Contains(t, rec.Body.String(), "Invalid group_id", path)

		rec = serveOps(r, http.MethodGet, path+"?start_time=2026-01-01T00:00:00Z", nil)
		require.Equal(t, http.StatusBadRequest, rec.Code, path)
		require.Contains(t, rec.Body.String(), "start_time and end_time must be provided together", path)
	}

	repo.reset()
	rec := serveOps(r, http.MethodGet, "/openai-token-stats", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Empty(t, repo.token.Model)
	require.Empty(t, repo.token.ModelSource)

	rec = serveOps(r, http.MethodGet, "/openai-token-stats?group_id=abc", nil)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "invalid group_id")
}

func TestOpsDashboardSnapshotV2_CacheKeyIncludesModel(t *testing.T) {
	t.Cleanup(resetOpsDashboardSnapshotV2CacheForTest)
	resetOpsDashboardSnapshotV2CacheForTest()
	repo := &opsDashboardCaptureRepo{}
	r := newOpsDashboardModelFilterRouter(repo)
	window := "start_time=2026-01-01T00:00:00Z&end_time=2026-01-01T01:00:00Z"

	steps := []struct {
		query  string
		model  string
		source string
		cache  string
	}{
		{"model=a", "a", "requested", "miss"},
		{"model=b", "b", "requested", "miss"},
		{"model=b", "b", "requested", "hit"},
		{"model=b&model_source=upstream", "b", "upstream", "miss"},
	}
	for _, step := range steps {
		repo.reset()
		rec := serveOps(r, http.MethodGet, "/snapshot-v2?"+window+"&"+step.query, nil)
		require.Equal(t, http.StatusOK, rec.Code, step.query)
		require.Equal(t, step.cache, rec.Header().Get("X-Snapshot-Cache"), step.query)
		if step.cache == "miss" {
			require.Equal(t, 3, repo.calls, step.query)
			require.Equal(t, step.model, repo.last().Model, step.query)
			require.Equal(t, step.source, repo.last().ModelSource, step.query)
		} else {
			require.Zero(t, repo.calls, step.query)
		}
	}
}
