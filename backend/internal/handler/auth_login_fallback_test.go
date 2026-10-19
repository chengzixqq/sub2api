//go:build unit

package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type failingStoreRefreshCache struct {
	userHandlerRefreshTokenCacheStub
}

func (failingStoreRefreshCache) StoreRefreshToken(context.Context, string, *service.RefreshTokenData, time.Duration) error {
	return errors.New("redis down")
}

func TestRespondWithTokenPair_FallbackMarksRefreshUnavailable(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{JWT: config.JWTConfig{Secret: "fallback-secret", ExpireHour: 1, RefreshTokenExpireDays: 30}}
	auth := service.NewAuthService(nil, nil, nil, &failingStoreRefreshCache{}, cfg, nil, nil, nil, nil, nil, nil, nil, nil)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest("POST", "/api/v1/auth/login", nil)

	respondWithTokenPair(c, auth, &service.User{ID: 1, Email: "a@example.com", Status: service.StatusActive})

	require.Equal(t, 200, rec.Code, rec.Body.String())
	var resp struct {
		Data map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.NotEmpty(t, resp.Data["access_token"])
	require.EqualValues(t, 3600, resp.Data["expires_in"])
	require.Equal(t, true, resp.Data["refresh_token_unavailable"])
	require.NotContains(t, resp.Data, "refresh_token")
}
