package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagequery"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type gatewayUsageSummaryRepo struct {
	service.UsageLogRepository
	mu         sync.Mutex
	trendCalls map[int64]int
	modelCalls map[int64]int
	trendDays  []int
	trendErr   error
	modelErr   error
}

func newGatewayUsageSummaryRepo() *gatewayUsageSummaryRepo {
	return &gatewayUsageSummaryRepo{trendCalls: map[int64]int{}, modelCalls: map[int64]int{}}
}

func (r *gatewayUsageSummaryRepo) GetAPIKeyDashboardStats(context.Context, int64) (*usagestats.UserDashboardStats, error) {
	return &usagestats.UserDashboardStats{TotalRequests: 1}, nil
}

func (r *gatewayUsageSummaryRepo) GetUsageTrendWithFilters(_ context.Context, startTime, endTime time.Time, _ string, _, apiKeyID, _, _ int64, _ string, _ *int16, _ *bool, _ *int8) ([]usagestats.TrendDataPoint, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.trendCalls[apiKeyID]++
	r.trendDays = append(r.trendDays, int(endTime.Sub(startTime).Hours()/24+0.5))
	if r.trendErr != nil {
		return nil, r.trendErr
	}
	return []usagestats.TrendDataPoint{{Date: "2026-11-17", Requests: apiKeyID}}, nil
}

func (r *gatewayUsageSummaryRepo) GetModelStatsWithFilters(_ context.Context, _, _ time.Time, _, apiKeyID, _, _ int64, _ *int16, _ *bool, _ *int8) ([]usagestats.ModelStat, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.modelCalls[apiKeyID]++
	if r.modelErr != nil {
		return nil, r.modelErr
	}
	return []usagestats.ModelStat{{Model: fmt.Sprintf("model-%d", apiKeyID), Requests: apiKeyID}}, nil
}

func (r *gatewayUsageSummaryRepo) calls(apiKeyID int64) (trend, model int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.trendCalls[apiKeyID], r.modelCalls[apiKeyID]
}

func gatewayUsageTestRouter(t *testing.T, repo *gatewayUsageSummaryRepo) *gin.Engine {
	t.Helper()
	previousDaily, previousModels := gatewayUsageDailyCache, gatewayUsageModelStatsCache
	gatewayUsageDailyCache = usagequery.NewDetachedCache(5*time.Minute, time.Minute)
	gatewayUsageModelStatsCache = usagequery.NewDetachedCache(5*time.Minute, time.Minute)
	t.Cleanup(func() { gatewayUsageDailyCache, gatewayUsageModelStatsCache = previousDaily, previousModels })
	gin.SetMode(gin.TestMode)
	h := &GatewayHandler{usageService: service.NewUsageService(repo, nil, nil, nil)}
	r := gin.New()
	r.Use(func(c *gin.Context) {
		keyID, _ := strconv.ParseInt(c.GetHeader("X-Test-Key"), 10, 64)
		userID, _ := strconv.ParseInt(c.GetHeader("X-Test-User"), 10, 64)
		c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{ID: keyID, UserID: userID, Quota: 100, Status: service.StatusAPIKeyActive})
		c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: userID})
		c.Next()
	})
	r.GET("/v1/usage", h.Usage)
	return r
}

type gatewayUsageTestResponse struct {
	Usage      map[string]any                     `json:"usage"`
	DailyUsage []usagestats.APIKeyDailyUsagePoint `json:"daily_usage"`
	ModelStats []usagestats.ModelStat             `json:"model_stats"`
	raw        map[string]json.RawMessage
}

func gatewayUsageRequest(t *testing.T, r http.Handler, url string, keyID, userID int64) gatewayUsageTestResponse {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("X-Test-Key", strconv.FormatInt(keyID, 10))
	req.Header.Set("X-Test-User", strconv.FormatInt(userID, 10))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var resp gatewayUsageTestResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp.raw))
	return resp
}

func TestGatewayUsage_CachesDailyTrendAndModelStatsPerKey(t *testing.T) {
	repo := newGatewayUsageSummaryRepo()
	r := gatewayUsageTestRouter(t, repo)

	first := gatewayUsageRequest(t, r, "/v1/usage", 7, 42)
	second := gatewayUsageRequest(t, r, "/v1/usage", 7, 42)
	trend, model := repo.calls(7)
	require.Equal(t, 1, trend, "the daily trend scan must be reused by the next poll")
	require.Equal(t, 1, model, "the model statistics scan must be reused by the next poll")
	require.Equal(t, first.DailyUsage, second.DailyUsage)
	require.Equal(t, first.ModelStats, second.ModelStats)
	require.Equal(t, []usagestats.APIKeyDailyUsagePoint{{Date: "2026-11-17", Requests: 7}}, second.DailyUsage)
	require.Equal(t, "model-7", second.ModelStats[0].Model)
	require.NotNil(t, second.Usage, "the summary still loads on every request")

	other := gatewayUsageRequest(t, r, "/v1/usage", 8, 42)
	trend, model = repo.calls(8)
	require.Equal(t, 1, trend, "another key must not reuse key 7's trend")
	require.Equal(t, 1, model, "another key must not reuse key 7's model statistics")
	require.Equal(t, int64(8), other.DailyUsage[0].Requests)
	require.Equal(t, "model-8", other.ModelStats[0].Model)
}

func TestGatewayUsage_CacheKeySeparatesUsersAndQueryParameters(t *testing.T) {
	repo := newGatewayUsageSummaryRepo()
	r := gatewayUsageTestRouter(t, repo)

	gatewayUsageRequest(t, r, "/v1/usage", 7, 42)
	gatewayUsageRequest(t, r, "/v1/usage", 7, 43)
	gatewayUsageRequest(t, r, "/v1/usage?days=7", 7, 42)
	gatewayUsageRequest(t, r, "/v1/usage?days=7", 7, 42)
	gatewayUsageRequest(t, r, "/v1/usage?timezone=Asia/Shanghai", 7, 42)
	gatewayUsageRequest(t, r, "/v1/usage?start_date=2026-11-01&end_date=2026-11-10", 7, 42)
	gatewayUsageRequest(t, r, "/v1/usage?start_date=2026-11-01&end_date=2026-11-10", 7, 42)
	trend, model := repo.calls(7)
	require.Equal(t, 5, trend)
	require.Equal(t, 5, model)
	require.Contains(t, repo.trendDays, 7, "days=7 must query its own range, not the default 30 days")
}

func TestGatewayUsage_DoesNotCacheFailuresAndOmitsFields(t *testing.T) {
	repo := newGatewayUsageSummaryRepo()
	repo.trendErr = errors.New("trend temporarily unavailable")
	repo.modelErr = errors.New("model statistics temporarily unavailable")
	r := gatewayUsageTestRouter(t, repo)

	failed := gatewayUsageRequest(t, r, "/v1/usage", 7, 42)
	require.NotContains(t, failed.raw, "daily_usage")
	require.NotContains(t, failed.raw, "model_stats")
	require.Contains(t, failed.raw, "usage")

	repo.mu.Lock()
	repo.trendErr, repo.modelErr = nil, nil
	repo.mu.Unlock()
	recovered := gatewayUsageRequest(t, r, "/v1/usage", 7, 42)
	require.Contains(t, recovered.raw, "daily_usage")
	require.Contains(t, recovered.raw, "model_stats")
	trend, model := repo.calls(7)
	require.Equal(t, 2, trend, "a failed trend must not be cached")
	require.Equal(t, 2, model, "failed model statistics must not be cached")
}
