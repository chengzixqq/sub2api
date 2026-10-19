//go:build unit

package middleware

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type stubRevocationCache struct {
	service.RefreshTokenCache
	revoked map[string]bool
	err     error
}

func (s *stubRevocationCache) RevokeSession(_ context.Context, sid string, _ time.Duration) error {
	s.revoked[sid] = true
	return nil
}

func (s *stubRevocationCache) SetUserTokensValidAfter(context.Context, int64, time.Time, string, time.Duration) error {
	return nil
}

func (s *stubRevocationCache) GetSessionRevocation(_ context.Context, sid string, _ int64) (service.SessionRevocation, error) {
	if s.err != nil {
		return service.SessionRevocation{}, s.err
	}
	return service.SessionRevocation{Revoked: s.revoked[sid]}, nil
}

func newRevocationTestEnv(t *testing.T, cache *stubRevocationCache) (*service.AuthService, *service.UserService, *service.User) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{}
	cfg.JWT.Secret = "test-jwt-secret-32bytes-long!!!"
	cfg.JWT.AccessTokenExpireMinutes = 60
	user := &service.User{ID: 1, Email: "admin@example.com", Role: service.RoleAdmin, Status: service.StatusActive, TokenVersion: 1}
	userRepo := &stubJWTUserRepo{users: map[int64]*service.User{1: user}}
	authSvc := service.NewAuthService(nil, userRepo, nil, cache, cfg, nil, nil, nil, nil, nil, nil, nil, nil)
	return authSvc, service.NewUserService(userRepo, nil, nil, nil), user
}

func revocationErrorCode(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	code, _ := body["code"].(string)
	return code
}

func TestJWTAuth_RevokedSessionReturnsTokenRevoked(t *testing.T) {
	cache := &stubRevocationCache{revoked: map[string]bool{}}
	authSvc, userSvc, user := newRevocationTestEnv(t, cache)
	r := gin.New()
	r.Use(gin.HandlerFunc(NewJWTAuthMiddleware(authSvc, userSvc, nil, nil)))
	r.GET("/protected", func(c *gin.Context) { c.Status(http.StatusOK) })

	token, err := authSvc.GenerateToken(context.Background(), user)
	require.NoError(t, err)
	call := func() *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/protected", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		r.ServeHTTP(w, req)
		return w
	}
	require.Equal(t, http.StatusOK, call().Code)

	authSvc.RevokeAccessToken(context.Background(), token)
	w := call()
	require.Equal(t, http.StatusUnauthorized, w.Code)
	require.Equal(t, "TOKEN_REVOKED", revocationErrorCode(t, w))
}

func TestJWTAuth_RevocationCheckFailureFailsOpen(t *testing.T) {
	cache := &stubRevocationCache{revoked: map[string]bool{}, err: errors.New("redis down")}
	authSvc, userSvc, user := newRevocationTestEnv(t, cache)
	r := gin.New()
	r.Use(gin.HandlerFunc(NewJWTAuthMiddleware(authSvc, userSvc, nil, nil)))
	r.GET("/protected", func(c *gin.Context) { c.Status(http.StatusOK) })

	token, err := authSvc.GenerateToken(context.Background(), user)
	require.NoError(t, err)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
}

func TestAdminJWT_RevokedSessionReturnsTokenRevoked(t *testing.T) {
	cache := &stubRevocationCache{revoked: map[string]bool{}}
	authSvc, userSvc, user := newRevocationTestEnv(t, cache)
	token, err := authSvc.GenerateToken(context.Background(), user)
	require.NoError(t, err)
	authSvc.RevokeAccessToken(context.Background(), token)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/admin/me", nil)
	require.False(t, validateJWTForAdmin(c, token, authSvc, userSvc, nil, nil))
	require.Equal(t, http.StatusUnauthorized, w.Code)
	require.Equal(t, "TOKEN_REVOKED", revocationErrorCode(t, w))
}
