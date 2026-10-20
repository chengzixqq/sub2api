package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/web"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// The Web-only capability must follow whether this binary embeds the frontend:
// plain builds (tests, embed_off) must not claim it, `-tags embed` builds do.
func TestProvideMeHandler_WebCapabilityFollowsEmbeddedFrontend(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := ProvideMeHandler(nil, nil, nil, nil, nil, nil, BuildInfo{Version: "test"})
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 1})
		c.Next()
	})
	r.GET("/admin/me", h.Get)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/me", nil))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var resp struct {
		Data struct {
			Server struct {
				Capabilities []string `json:"capabilities"`
			} `json:"server"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	if web.HasEmbeddedFrontend() {
		require.Contains(t, resp.Data.Server.Capabilities, "web.accounts_edit_deep_link")
	} else {
		require.NotContains(t, resp.Data.Server.Capabilities, "web.accounts_edit_deep_link")
	}
}
