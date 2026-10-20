//go:build unit

package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type fakeAccessTokenAuth struct {
	tokens map[string]*service.AdminAccessToken
}

func (f *fakeAccessTokenAuth) Authenticate(_ context.Context, plain, _ string) (*service.AdminAccessToken, error) {
	if t, ok := f.tokens[plain]; ok {
		if !time.Now().Before(t.ExpiresAt) {
			return nil, service.ErrAdminAccessTokenExpired
		}
		return t, nil
	}
	return nil, service.ErrAdminAccessTokenInvalid
}

type accessTokenTestEnv struct {
	router *gin.Engine
	seen   *gin.Context
}

func newAccessTokenTestEnv(t *testing.T, users map[int64]*service.User, tokens map[string]*service.AdminAccessToken) *accessTokenTestEnv {
	t.Helper()
	gin.SetMode(gin.TestMode)
	userRepo := &stubJWTUserRepo{users: users}
	userSvc := service.NewUserService(userRepo, nil, nil, nil)
	env := &accessTokenTestEnv{router: gin.New()}
	auth := adminAuthWithAccessTokens(nil, userSvc, nil, nil, &fakeAccessTokenAuth{tokens: tokens})
	group := env.router.Group("/api/v1/admin", auth)
	record := func(c *gin.Context) { env.seen = c; c.Status(http.StatusOK) }
	group.GET("/dashboard/stats", record)
	group.PUT("/users/:id", record)
	group.DELETE("/access-tokens/:id", record)
	group.POST("/accounts/runtime/batch", record)
	group.POST("/accounts/today-stats/batch", record)
	group.POST("/accounts/batch-delete", record)
	group.POST("/accounts/usage/batch", record)
	group.PUT("/accounts/runtime/batch", record)
	return env
}

func (e *accessTokenTestEnv) do(method, path, key string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, nil)
	req.Header.Set("x-api-key", key)
	e.router.ServeHTTP(w, req)
	return w
}

func TestAdminAccessToken_ActsAsItsOwner(t *testing.T) {
	owner := &service.User{ID: 42, Email: "second-admin@example.com", Role: service.RoleAdmin, Status: service.StatusActive}
	env := newAccessTokenTestEnv(t, map[int64]*service.User{42: owner}, map[string]*service.AdminAccessToken{
		"sat_write": {ID: 7, UserID: 42, Scope: service.AdminAccessTokenScopeWrite, ExpiresAt: time.Now().Add(time.Hour)},
	})

	w := env.do(http.MethodPut, "/api/v1/admin/users/1", "sat_write")
	require.Equal(t, http.StatusOK, w.Code)
	subject, ok := GetAuthSubjectFromContext(env.seen)
	require.True(t, ok)
	require.Equal(t, int64(42), subject.UserID, "the token's owner, not the first admin")
	require.Equal(t, service.AuditAuthMethodAdminAccessToken, env.seen.GetString("auth_method"))
	require.Equal(t, "second-admin@example.com", env.seen.GetString(ContextKeyAuthEmail))
	require.Equal(t, int64(7), env.seen.GetInt64(ContextKeyAdminAccessTokenID))
}

func TestAdminAccessToken_ReadOnlyBlocksWrites(t *testing.T) {
	owner := &service.User{ID: 42, Role: service.RoleAdmin, Status: service.StatusActive}
	env := newAccessTokenTestEnv(t, map[int64]*service.User{42: owner}, map[string]*service.AdminAccessToken{
		"sat_read": {ID: 8, UserID: 42, Scope: service.AdminAccessTokenScopeRead, ExpiresAt: time.Now().Add(time.Hour)},
	})

	require.Equal(t, http.StatusOK, env.do(http.MethodGet, "/api/v1/admin/dashboard/stats", "sat_read").Code)
	w := env.do(http.MethodPut, "/api/v1/admin/users/1", "sat_read")
	require.Equal(t, http.StatusForbidden, w.Code)
	require.Equal(t, "ACCESS_TOKEN_READ_ONLY", decodeErrorBody(t, w)["reason"])

	require.Equal(t, http.StatusForbidden, env.do(http.MethodDelete, "/api/v1/admin/access-tokens/9", "sat_read").Code)
	require.Equal(t, http.StatusOK, env.do(http.MethodDelete, "/api/v1/admin/access-tokens/"+strconv.Itoa(8), "sat_read").Code,
		"a read-only token may still revoke itself")
}

// TestAdminAccessToken_ReadOnlyAllowsReadBatchPOSTs: two batch reads carry
// their ID lists in a POST body but change nothing, so a read-only token may
// call them. The allowance is per method and exact route.
func TestAdminAccessToken_ReadOnlyAllowsReadBatchPOSTs(t *testing.T) {
	owner := &service.User{ID: 42, Role: service.RoleAdmin, Status: service.StatusActive}
	env := newAccessTokenTestEnv(t, map[int64]*service.User{42: owner}, map[string]*service.AdminAccessToken{
		"sat_read": {ID: 8, UserID: 42, Scope: service.AdminAccessTokenScopeRead, ExpiresAt: time.Now().Add(time.Hour)},
	})

	for _, path := range []string{"/api/v1/admin/accounts/runtime/batch", "/api/v1/admin/accounts/today-stats/batch"} {
		require.Equal(t, http.StatusOK, env.do(http.MethodPost, path, "sat_read").Code, path)
	}
	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/api/v1/admin/accounts/batch-delete"},
		// usage/batch with force=true probes upstream and persists snapshots.
		{http.MethodPost, "/api/v1/admin/accounts/usage/batch"},
		{http.MethodPut, "/api/v1/admin/accounts/runtime/batch"},
		{http.MethodPut, "/api/v1/admin/users/1"},
	} {
		w := env.do(tc.method, tc.path, "sat_read")
		require.Equal(t, http.StatusForbidden, w.Code, "%s %s", tc.method, tc.path)
		require.Equal(t, "ACCESS_TOKEN_READ_ONLY", decodeErrorBody(t, w)["reason"], "%s %s", tc.method, tc.path)
	}
}

func TestAdminAccessToken_Rejections(t *testing.T) {
	users := map[int64]*service.User{
		42: {ID: 42, Role: service.RoleAdmin, Status: service.StatusActive},
		43: {ID: 43, Role: service.RoleAdmin, Status: service.StatusDisabled},
		44: {ID: 44, Role: service.RoleUser, Status: service.StatusActive},
	}
	env := newAccessTokenTestEnv(t, users, map[string]*service.AdminAccessToken{
		"sat_expired":  {ID: 1, UserID: 42, Scope: "write", ExpiresAt: time.Now().Add(-time.Minute)},
		"sat_disabled": {ID: 2, UserID: 43, Scope: "write", ExpiresAt: time.Now().Add(time.Hour)},
		"sat_demoted":  {ID: 3, UserID: 44, Scope: "write", ExpiresAt: time.Now().Add(time.Hour)},
		"sat_orphan":   {ID: 4, UserID: 99, Scope: "write", ExpiresAt: time.Now().Add(time.Hour)},
	})
	cases := []struct {
		key    string
		status int
		reason string
	}{
		{"sat_unknown", http.StatusUnauthorized, "INVALID_ADMIN_KEY"},
		{"sat_expired", http.StatusUnauthorized, "ACCESS_TOKEN_EXPIRED"},
		{"sat_disabled", http.StatusUnauthorized, "USER_INACTIVE"},
		{"sat_demoted", http.StatusForbidden, "FORBIDDEN"},
		{"sat_orphan", http.StatusUnauthorized, "INVALID_ADMIN_KEY"},
	}
	for _, tc := range cases {
		w := env.do(http.MethodGet, "/api/v1/admin/dashboard/stats", tc.key)
		require.Equal(t, tc.status, w.Code, tc.key)
		require.Equal(t, tc.reason, decodeErrorBody(t, w)["reason"], tc.key)
	}
}

func TestStepUpRejectsAdminAccessTokens(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/x", nil)
	c.Set("auth_method", service.AuditAuthMethodAdminAccessToken)
	require.False(t, enforceStepUp(c, nil, nil, nil))
	require.Equal(t, http.StatusForbidden, w.Code)
	require.Equal(t, "STEP_UP_ADMIN_API_KEY_FORBIDDEN", decodeErrorBody(t, w)["reason"])
}
