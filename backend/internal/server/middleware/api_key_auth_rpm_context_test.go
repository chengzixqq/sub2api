package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestAPIKeyAuthRPMScopeContext(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name   string
		mode   string
		google bool
	}{
		{name: "standard/normal", mode: config.RunModeStandard},
		{name: "simple/normal", mode: config.RunModeSimple},
		{name: "standard/google", mode: config.RunModeStandard, google: true},
		{name: "simple/google", mode: config.RunModeSimple, google: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			user := &service.User{ID: 7, Role: service.RoleUser, Status: service.StatusActive, Balance: 10, Concurrency: 3}
			key := &service.APIKey{ID: 100, UserID: user.ID, Key: "rpm-context-test-key", Status: service.StatusActive, User: user}
			repo := fakeAPIKeyRepo{getByKey: func(_ context.Context, credential string) (*service.APIKey, error) {
				if credential != key.Key {
					return nil, service.ErrAPIKeyNotFound
				}
				copy := *key
				return &copy, nil
			}}
			cfg := &config.Config{RunMode: tc.mode}
			svc := service.NewAPIKeyService(repo, nil, nil, nil, nil, nil, cfg)
			router := gin.New()
			if tc.google {
				router.Use(APIKeyAuthWithSubscriptionGoogle(svc, nil, cfg))
			} else {
				router.Use(gin.HandlerFunc(NewAPIKeyAuthMiddleware(svc, nil, cfg)))
			}
			var observedUserID, observedKeyID int64
			var userOK, keyOK, authenticated bool
			router.POST("/v1beta/test", func(c *gin.Context) {
				observedUserID, userOK = c.Request.Context().Value(ctxkey.UserID).(int64)
				observedKeyID, keyOK = c.Request.Context().Value(ctxkey.APIKeyID).(int64)
				_, authenticated = GetAPIKeyFromContext(c)
				c.Status(http.StatusOK)
			})
			request := httptest.NewRequest(http.MethodPost, "/v1beta/test?user_id=991&api_key_id=992", strings.NewReader(`{"user_id":991,"api_key_id":992}`))
			// Earlier context and caller-provided IDs must never replace the
			// server-verified API key owner or key identity.
			ctx := context.WithValue(request.Context(), ctxkey.UserID, int64(9001))
			ctx = context.WithValue(ctx, ctxkey.APIKeyID, int64(9002))
			request = request.WithContext(ctx)
			request.Header.Set("Authorization", "Bearer "+key.Key)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			require.Equal(t, http.StatusOK, response.Code, response.Body.String())
			require.True(t, authenticated)
			require.True(t, userOK)
			require.True(t, keyOK)
			require.Equal(t, [2]int64{user.ID, key.ID}, [2]int64{observedUserID, observedKeyID}, "RPM scope must belong to the authenticated owner and key")
		})
	}
}
