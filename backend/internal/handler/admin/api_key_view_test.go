package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

const keyViewSecret = "sk-abcdefghijklmnopqrstuv3f9a"

func newKeyViewRouter(t *testing.T) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	svc := newStubAdminService()
	svc.apiKeys = []service.APIKey{{ID: 4087, UserID: 1, Key: keyViewSecret, Name: "默认", Status: service.StatusActive}}
	r := gin.New()
	users := NewUserHandler(svc, nil, nil, nil, nil, nil, nil)
	groups := NewGroupHandler(svc, nil, nil)
	keys := NewAdminAPIKeyHandler(svc, nil)
	r.GET("/users/:id/api-keys", users.GetUserAPIKeys)
	r.GET("/groups/:id/api-keys", groups.GetGroupAPIKeys)
	r.GET("/api-keys/:id", keys.Get)
	r.PUT("/api-keys/:id", keys.UpdateGroup)
	return r
}

func TestAPIKeyView_MaskedHidesPlaintext(t *testing.T) {
	r := newKeyViewRouter(t)
	for _, req := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/users/1/api-keys?key_view=masked", nil),
		httptest.NewRequest(http.MethodGet, "/groups/2/api-keys?key_view=masked", nil),
		httptest.NewRequest(http.MethodGet, "/api-keys/4087?key_view=masked", nil),
		jsonRequest(http.MethodPut, "/api-keys/4087?key_view=masked", `{}`),
	} {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code, req.URL.String()+": "+rec.Body.String())
		require.NotContains(t, rec.Body.String(), keyViewSecret, req.URL.String())
		require.Contains(t, rec.Body.String(), `"key_masked":"sk-…3f9a"`, req.URL.String())
	}
}

func TestAPIKeyView_DefaultIsFullAndInvalidRejected(t *testing.T) {
	r := newKeyViewRouter(t)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/users/1/api-keys", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), keyViewSecret, "Web keeps the full key by default")
	require.Contains(t, rec.Body.String(), `"key_masked":"sk-…3f9a"`)

	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/users/1/api-keys?key_view=partial", nil))
	require.Equal(t, http.StatusBadRequest, rec.Code)
}

type keyViewUsageRepo struct {
	service.UsageLogRepository
}

func (keyViewUsageRepo) ListWithFilters(_ context.Context, params pagination.PaginationParams, _ usagestats.UsageLogFilters) ([]service.UsageLog, *pagination.PaginationResult, error) {
	return []service.UsageLog{{ID: 1, APIKeyID: 4087, APIKey: &service.APIKey{ID: 4087, Key: keyViewSecret, Name: "默认"}}},
		&pagination.PaginationResult{Total: 1, Page: params.Page, PageSize: params.PageSize, Pages: 1}, nil
}

func TestAPIKeyView_UsageListMasked(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := NewUsageHandler(service.NewUsageService(keyViewUsageRepo{}, nil, nil, nil), nil, nil, nil)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Request = c.Request.WithContext(service.WithScope(c.Request.Context(), service.AdminScope()))
	})
	r.GET("/admin/usage", handler.List)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/usage?key_view=masked", nil))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NotContains(t, rec.Body.String(), keyViewSecret)
	var resp struct {
		Data struct {
			Items []struct {
				APIKey struct {
					Key       string `json:"key"`
					KeyMasked string `json:"key_masked"`
					Name      string `json:"name"`
				} `json:"api_key"`
			} `json:"items"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Len(t, resp.Data.Items, 1)
	require.Empty(t, resp.Data.Items[0].APIKey.Key)
	require.Equal(t, "sk-…3f9a", resp.Data.Items[0].APIKey.KeyMasked)
	require.Equal(t, "默认", resp.Data.Items[0].APIKey.Name)
}

func jsonRequest(method, path, body string) *http.Request {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	return req
}

func TestAPIKeyReveal(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := newStubAdminService()
	svc.apiKeys = []service.APIKey{
		{ID: 4087, UserID: 1, Key: keyViewSecret, Name: "默认"},
		{ID: 5000, UserID: 1, Key: "__deleted__5000__1700000000", Name: "旧"},
	}
	r := gin.New()
	r.POST("/api-keys/:id/reveal", NewAdminAPIKeyHandler(svc, nil).Reveal)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api-keys/4087/reveal", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	require.Contains(t, rec.Body.String(), keyViewSecret)

	for _, path := range []string{"/api-keys/5000/reveal", "/api-keys/999/reveal"} {
		rec = httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, nil))
		require.Equal(t, http.StatusNotFound, rec.Code, path)
	}
}
