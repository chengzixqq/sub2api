package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type quickTopupHandlerRepo struct {
	adjustmentHandlerRepoStub
	query service.QuickTopupQuery
}

func (r *quickTopupHandlerRepo) QuickTopup(_ context.Context, q service.QuickTopupQuery) (*service.QuickTopupResult, error) {
	r.query = q
	return &service.QuickTopupResult{
		Recent:     []service.QuickTopupRecentUser{{UserID: 1, Balance: 12.4, LastDelta: "20.00000000", LastOperation: "add"}},
		LowBalance: []service.QuickTopupLowBalanceUser{},
	}, nil
}

func TestQuickTopupHandler(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &quickTopupHandlerRepo{}
	r := gin.New()
	r.GET("/users/quick-topup", NewUserAdjustmentHandler(service.NewAdminUserAdjustmentService(repo)).QuickTopup)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/users/quick-topup", nil))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, service.QuickTopupQuery{RecentLimit: 8, LowLimit: 8, RecentDays: 30}, repo.query)
	require.Contains(t, rec.Body.String(), `"last_delta":"20.00000000"`)
	require.Contains(t, rec.Body.String(), `"low_balance_enabled":false`)

	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/users/quick-topup?recent_limit=3&low_limit=0&recent_days=7", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, service.QuickTopupQuery{RecentLimit: 3, LowLimit: 0, RecentDays: 7}, repo.query)

	for _, bad := range []string{"recent_limit=x", "recent_limit=50", "recent_days=0"} {
		rec = httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/users/quick-topup?"+bad, nil))
		require.Equal(t, http.StatusBadRequest, rec.Code, bad)
	}
}
