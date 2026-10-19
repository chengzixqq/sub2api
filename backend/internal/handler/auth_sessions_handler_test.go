//go:build unit

package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

type sessionsTestEnv struct {
	handler *AuthHandler
	auth    *service.AuthService
	user    *service.User
}

func newSessionsTestEnv(t *testing.T) *sessionsTestEnv {
	t.Helper()
	gin.SetMode(gin.TestMode)
	server := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	user := &service.User{ID: 31, Email: "s@example.com", Role: service.RoleUser, Status: service.StatusActive}
	cfg := &config.Config{JWT: config.JWTConfig{Secret: "sessions-secret", ExpireHour: 1, RefreshTokenExpireDays: 30}}
	auth := service.NewAuthService(nil, &userHandlerRepoStub{user: user}, nil, repository.NewRefreshTokenCache(rdb), cfg, nil, nil, nil, nil, nil, nil, nil, nil)
	return &sessionsTestEnv{handler: &AuthHandler{authService: auth}, auth: auth, user: user}
}

func (e *sessionsTestEnv) login(t *testing.T, client *service.SessionClient) string {
	t.Helper()
	ctx := service.WithSessionBinding(context.Background(), &service.SessionBinding{IP: "203.0.113.24", UserAgent: "Sub2Mobile-iOS"})
	pair, err := e.auth.GenerateTokenPair(service.WithSessionClient(ctx, client), e.user, "")
	require.NoError(t, err)
	claims, err := e.auth.ValidateToken(pair.AccessToken)
	require.NoError(t, err)
	return claims.SessionID
}

func (e *sessionsTestEnv) call(t *testing.T, method, path, sid string, body []byte, params gin.Params, h gin.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(method, path, bytes.NewReader(body))
	if body != nil {
		c.Request.Header.Set("Content-Type", "application/json")
	}
	c.Params = params
	c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: e.user.ID})
	c.Set(middleware2.ContextKeySessionID, sid)
	h(c)
	return w
}

type sessionsListBody struct {
	Data struct {
		Items []struct {
			ID         string `json:"id"`
			DeviceName string `json:"device_name"`
			ClientType string `json:"client_type"`
			Current    bool   `json:"current"`
		} `json:"items"`
	} `json:"data"`
}

func TestAuthSessions_ListMarksCurrent(t *testing.T) {
	env := newSessionsTestEnv(t)
	phone := env.login(t, &service.SessionClient{Type: "ios", DeviceName: "我的 iPhone"})
	env.login(t, nil)

	w := env.call(t, http.MethodGet, "/api/v1/auth/sessions", phone, nil, nil, env.handler.ListSessions)
	require.Equal(t, http.StatusOK, w.Code)
	var body sessionsListBody
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	require.Len(t, body.Data.Items, 2)
	current := 0
	for _, item := range body.Data.Items {
		if item.Current {
			current++
			require.Equal(t, phone, item.ID)
			require.Equal(t, "我的 iPhone", item.DeviceName)
			require.Equal(t, "ios", item.ClientType)
		}
	}
	require.Equal(t, 1, current)
}

func TestAuthSessions_DeleteOtherAndMissing(t *testing.T) {
	env := newSessionsTestEnv(t)
	phone := env.login(t, nil)
	other := env.login(t, nil)

	w := env.call(t, http.MethodDelete, "/api/v1/auth/sessions/"+other, phone, nil, gin.Params{{Key: "id", Value: other}}, env.handler.RevokeSession)
	require.Equal(t, http.StatusOK, w.Code)
	var ok struct {
		Data struct {
			Current bool `json:"current"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &ok))
	require.False(t, ok.Data.Current)

	w = env.call(t, http.MethodDelete, "/api/v1/auth/sessions/"+other, phone, nil, gin.Params{{Key: "id", Value: other}}, env.handler.RevokeSession)
	require.Equal(t, http.StatusNotFound, w.Code)
	var missing struct {
		Reason string `json:"reason"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &missing))
	require.Equal(t, "SESSION_NOT_FOUND", missing.Reason)

	w = env.call(t, http.MethodDelete, "/api/v1/auth/sessions/"+phone, phone, nil, gin.Params{{Key: "id", Value: phone}}, env.handler.RevokeSession)
	require.Equal(t, http.StatusOK, w.Code)
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &ok))
	require.True(t, ok.Data.Current, "deleting the current session tells the client to sign out")
}

func TestAuthSessions_RevokeAllKeepCurrent(t *testing.T) {
	env := newSessionsTestEnv(t)
	phone := env.login(t, nil)
	env.login(t, nil)
	env.login(t, nil)

	w := env.call(t, http.MethodPost, "/api/v1/auth/revoke-all-sessions", phone, []byte(`{"keep_current":true}`), nil, env.handler.RevokeAllSessions)
	require.Equal(t, http.StatusOK, w.Code)
	sessions, err := env.auth.ListUserSessions(context.Background(), env.user.ID)
	require.NoError(t, err)
	require.Len(t, sessions, 1)
	require.Equal(t, phone, sessions[0].ID)

	w = env.call(t, http.MethodPost, "/api/v1/auth/revoke-all-sessions", phone, []byte(`{"keep_current":`), nil, env.handler.RevokeAllSessions)
	require.Equal(t, http.StatusBadRequest, w.Code, "a malformed body must not fall back to signing out everywhere")
	sessions, err = env.auth.ListUserSessions(context.Background(), env.user.ID)
	require.NoError(t, err)
	require.Len(t, sessions, 1)
}
