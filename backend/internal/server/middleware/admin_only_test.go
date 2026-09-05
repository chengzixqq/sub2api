package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestAdminOnlyAllowsOwnerSessionsAndAdminAPIKeys(t *testing.T) {
	for _, authMethod := range []string{service.AuditAuthMethodJWT, service.AuditAuthMethodAdminAPIKey} {
		t.Run(authMethod, func(t *testing.T) {
			recorder := adminOnlyRequest(service.RoleAdmin, authMethod)
			require.Equal(t, http.StatusOK, recorder.Code)
		})
	}
}

func TestAdminOnlyRejectsVendorAndUser(t *testing.T) {
	for _, role := range []string{service.RoleVendor, service.RoleUser} {
		t.Run(role, func(t *testing.T) {
			recorder := adminOnlyRequest(role, service.AuditAuthMethodJWT)
			require.Equal(t, http.StatusForbidden, recorder.Code)
		})
	}
}

func adminOnlyRequest(role, authMethod string) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/owner-only",
		func(c *gin.Context) {
			c.Set(string(ContextKeyUserRole), role)
			c.Set("auth_method", authMethod)
			c.Next()
		},
		AdminOnly(),
		func(c *gin.Context) { c.Status(http.StatusOK) },
	)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/owner-only", nil))
	return recorder
}
