package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type dashboardStatsContractRepo struct {
	service.UsageLogRepository
	stats usagestats.DashboardStats
}

func (r *dashboardStatsContractRepo) GetDashboardStats(context.Context) (*usagestats.DashboardStats, error) {
	stats := r.stats
	return &stats, nil
}

func TestDashboardHandler_GetStatsPreservesAccountCosts(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &dashboardStatsContractRepo{stats: usagestats.DashboardStats{
		TotalCost: 120, TotalActualCost: 60, TotalAccountCost: 12.3456,
		TodayCost: 20, TodayActualCost: 10, TodayAccountCost: 2.3456,
	}}
	handler := NewDashboardHandler(service.NewDashboardService(repo, nil, nil, nil), nil)
	router := gin.New()
	router.GET("/stats", handler.GetStats)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/stats", nil))
	require.Equal(t, http.StatusOK, response.Code)
	// These six values use separate billing semantics. In particular, account
	// cost must be present on /stats just as it is on snapshot-v2.
	var bodyEnvelope struct {
		Data json.RawMessage `json:"data"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &bodyEnvelope))
	var data map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(bodyEnvelope.Data, &data))
	for field, want := range map[string]float64{
		"total_cost": 120, "total_actual_cost": 60, "total_account_cost": 12.3456,
		"today_cost": 20, "today_actual_cost": 10, "today_account_cost": 2.3456,
	} {
		raw, exists := data[field]
		require.True(t, exists, "missing dashboard field %s", field)
		var got float64
		require.NoError(t, json.Unmarshal(raw, &got))
		require.Equal(t, want, got, "dashboard field %s", field)
	}
}
