//go:build unit

package handler

import (
	"context"
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type rejectedModelKeyRepo struct{ service.APIKeyRepository }

func (*rejectedModelKeyRepo) GetByKeyForAuth(context.Context, string) (*service.APIKey, error) {
	return nil, service.ErrAPIKeyNotFound
}

func TestCodexManifestErrorWire(t *testing.T) {
	for _, path := range []string{"/models", "/v1/models"} {
		for headers := 1; headers < 8; headers++ {
			for _, stage := range []string{"auth", "ungrouped", "allowlist", "handler"} {
				t.Run(fmt.Sprintf("%s/headers=%d/%s", path, headers, stage), func(t *testing.T) {
					cfg := &config.Config{RunMode: config.RunModeStandard}
					r := gin.New()
					wantStatus := 403
					want := ""
					switch stage {
					case "auth":
						r.Use(gin.HandlerFunc(middleware.NewAPIKeyAuthMiddleware(service.NewAPIKeyService(&rejectedModelKeyRepo{}, nil, nil, nil, nil, nil, cfg), nil, cfg)))
						wantStatus = 401
						want = `{"code":"INVALID_API_KEY","message":"Invalid API key","reason":"INVALID_API_KEY"}`
						if headers == 4 {
							want = `{"code":"API_KEY_REQUIRED","message":"API key is required in Authorization header (Bearer scheme), x-api-key header, or x-goog-api-key header","reason":"API_KEY_REQUIRED"}`
						}
					case "ungrouped":
						r.Use(func(c *gin.Context) { c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{}) }, middleware.RequireGroupAssignment(service.NewSettingService(&ingressRejectSettingRepo{}, cfg), middleware.AnthropicErrorWriter))
						want = `{"error":{"message":"API Key is not assigned to any group and cannot be used. Please contact the administrator to assign it to a group.","type":"permission_error"},"type":"error"}`
					case "allowlist":
						r.Use(func(c *gin.Context) {
							c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{Group: &service.Group{ModelAllowlist: service.GroupModelAllowlist{Enabled: true, Models: []string{"allowed"}}}})
						}, middleware.GroupModelAllowlist())
						wantStatus = 404
						want = `{"error":{"code":"model_not_found","message":"Model \"blocked\" is not available for this group","type":"invalid_request_error"}}`
					case "handler":
						wantStatus = 401
						want = `{"error":{"message":"API key group is required","type":"invalid_request_error"},"type":"error"}`
					}
					r.GET(path, (&GatewayHandler{}).CodexModels)
					w := httptest.NewRecorder()
					req := httptest.NewRequest("GET", path+"?client_version=1&model=blocked", nil)
					if headers&1 != 0 {
						req.Header.Set("Authorization", "Bearer test")
					}
					if headers&2 != 0 {
						req.Header.Set("x-api-key", "test")
					}
					if headers&4 != 0 {
						req.Header.Set("anthropic-version", "2023-06-01")
					}
					r.ServeHTTP(w, req)
					t.Logf("WIRE %d %q %q", w.Code, w.Header().Get("Content-Type"), w.Body.String())
					require.Equal(t, wantStatus, w.Code)
					require.Equal(t, "application/json; charset=utf-8", w.Header().Get("Content-Type"))
					require.Equal(t, want, w.Body.String())
				})
			}
		}
	}
}
