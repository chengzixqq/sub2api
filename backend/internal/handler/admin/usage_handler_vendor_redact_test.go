package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type adminUsageRepoWithRow struct {
	service.UsageLogRepository
}

func (adminUsageRepoWithRow) ListWithFilters(ctx context.Context, params pagination.PaginationParams, filters usagestats.UsageLogFilters) ([]service.UsageLog, *pagination.PaginationResult, error) {
	return []service.UsageLog{{
		ID: 1, UserID: 7, APIKeyID: 9,
		User:   &service.User{ID: 7, Email: "alice@example.com", Balance: 123.45},
		APIKey: &service.APIKey{ID: 9, UserID: 7, Key: "sk-secret-plaintext", Name: "脚本"},
	}}, &pagination.PaginationResult{Total: 1, Page: params.Page, PageSize: params.PageSize, Pages: 1}, nil
}

func serveUsageListWithScope(t *testing.T, scope service.Scope) string {
	t.Helper()
	gin.SetMode(gin.TestMode)
	handler := NewUsageHandler(service.NewUsageService(adminUsageRepoWithRow{}, nil, nil, nil), nil, nil, nil)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Request = c.Request.WithContext(service.WithScope(c.Request.Context(), scope))
	})
	router.GET("/admin/usage", handler.List)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/usage", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	return rec.Body.String()
}

func TestAdminUsageList_StationOwnerSeesFullRow(t *testing.T) {
	body := serveUsageListWithScope(t, service.AdminScope())
	require.Contains(t, body, "alice@example.com")
	require.Contains(t, body, "sk-secret-plaintext")
}

func TestAdminUsageList_VendorGetsRedactedRow(t *testing.T) {
	body := serveUsageListWithScope(t, service.VendorScope(3, service.WorkspacePermissions{MonitorView: true}))
	require.Contains(t, body, "a***@example.com")
	require.Contains(t, body, "脚本")
	require.NotContains(t, body, "alice@example.com")
	require.NotContains(t, body, "sk-secret-plaintext")
	require.NotContains(t, body, "123.45")
}
