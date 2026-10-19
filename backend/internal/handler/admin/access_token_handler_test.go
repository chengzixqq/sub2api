package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestAccessTokenCreateRequiresSignedInSession(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := NewAccessTokenHandler(nil)
	for _, method := range []string{service.AuditAuthMethodAdminAPIKey, service.AuditAuthMethodAdminAccessToken} {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/access-tokens", bytes.NewBufferString(`{"name":"x"}`))
		c.Set("auth_method", method)
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 1})
		c.Set(string(middleware.ContextKeyUserRole), service.RoleAdmin)
		h.Create(c)

		require.Equal(t, http.StatusForbidden, w.Code, method)
		var body map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
		require.Equal(t, "STEP_UP_ADMIN_API_KEY_FORBIDDEN", body["reason"], method)
	}
}
