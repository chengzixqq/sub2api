package handler

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagequery"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type usageLiveRPMStub struct {
	users   map[int64]int
	keys    map[int64]int
	err     error
	userIDs []int64
	keyIDs  []int64
}

func (s *usageLiveRPMStub) GetUserRollingRPM(_ context.Context, id int64) (int, error) {
	s.userIDs = append(s.userIDs, id)
	return s.users[id], s.err
}

func (s *usageLiveRPMStub) GetAPIKeyRollingRPM(_ context.Context, id int64) (int, error) {
	s.keyIDs = append(s.keyIDs, id)
	return s.keys[id], s.err
}

type usageLiveKeyRepo struct{ service.UsageLogRepository }

func (*usageLiveKeyRepo) GetAPIKeyDashboardStats(context.Context, int64) (*usagestats.UserDashboardStats, error) {
	return &usagestats.UserDashboardStats{Rpm: 2000, TodayRequests: 123, TotalActualCost: 12.34}, nil
}

func TestUsageHandler_DashboardRuntimeRPMBypassesSummaryCache(t *testing.T) {
	previous := userUsageCache
	userUsageCache = usagequery.NewCache(30 * time.Second)
	t.Cleanup(func() { userUsageCache = previous })
	gin.SetMode(gin.TestMode)
	repo := &dashboardSummaryRepo{value: 1}
	svc := service.NewUsageService(repo, nil, nil, nil)
	runtime := &usageLiveRPMStub{users: map[int64]int{42: 1000, 43: 9999}}
	svc.SetScopedRollingRPMCache(runtime)
	h := NewUsageHandler(svc, nil, nil, nil)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 42})
		c.Next()
	})
	router.GET("/stats", h.DashboardStats)
	first := summaryRequest(router, http.MethodGet, "/stats", "", 42)
	require.Equal(t, http.StatusOK, first.Code)
	require.Contains(t, first.Body.String(), `"rpm":1000`)
	runtime.users[42] = 1500
	repo.value = 10
	second := summaryRequest(router, http.MethodGet, "/stats", "", 42)
	require.Equal(t, http.StatusOK, second.Code)
	require.Contains(t, second.Body.String(), `"rpm":1500`)
	require.Contains(t, second.Body.String(), `"total_requests":43`, "historical summary should still use its normal cache")
	require.Equal(t, 1, repo.calls)
	for _, id := range runtime.userIDs {
		require.EqualValues(t, 42, id)
	}
	runtime.err = errors.New("redis unavailable")
	failed := summaryRequest(router, http.MethodGet, "/stats", "", 42)
	require.Equal(t, http.StatusOK, failed.Code)
	require.Contains(t, failed.Body.String(), `"rpm":0`)
	require.Contains(t, failed.Body.String(), `"rpm_available":false`)
}

func TestGatewayHandler_UsageDataUsesAPIKeyRuntimeRPM(t *testing.T) {
	svc := service.NewUsageService(&usageLiveKeyRepo{}, nil, nil, nil)
	runtime := &usageLiveRPMStub{keys: map[int64]int{51: 1000, 52: 9999}}
	svc.SetScopedRollingRPMCache(runtime)
	h := &GatewayHandler{usageService: svc}
	data := h.buildUsageData(context.Background(), 51)
	require.EqualValues(t, 1000, data["rpm"])
	available, ok := data["rpm_available"].(*bool)
	require.True(t, ok)
	require.True(t, *available)
	require.Equal(t, []int64{51}, runtime.keyIDs)
	require.Empty(t, runtime.userIDs)
	runtime.err = errors.New("redis unavailable")
	data = h.buildUsageData(context.Background(), 51)
	require.EqualValues(t, 0, data["rpm"])
	available, ok = data["rpm_available"].(*bool)
	require.True(t, ok)
	require.False(t, *available)
}
