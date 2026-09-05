package routes

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUserAdjustmentRoutesAreOwnerOnly(t *testing.T) {
	content, err := os.ReadFile("admin.go")
	require.NoError(t, err)
	source := strings.Join(strings.Fields(string(content)), " ")

	require.Contains(t, source, `adjustments := admin.Group("/user-adjustments")`)
	require.Contains(t, source, "adjustments.Use(middleware.AdminOnly())")
	require.Contains(t, source, `adjustments.GET("", h.Admin.UserAdjustment.List)`)
	require.Contains(t, source, `adjustments.GET("/export", h.Admin.UserAdjustment.Export)`)
}

func TestPaymentAdminRoutesAreOwnerOnly(t *testing.T) {
	content, err := os.ReadFile("payment.go")
	require.NoError(t, err)
	source := strings.Join(strings.Fields(string(content)), " ")

	group := strings.Index(source, `adminGroup := v1.Group("/admin/payment")`)
	ownerOnly := strings.Index(source, "adminGroup.Use(middleware.AdminOnly())")
	require.GreaterOrEqual(t, group, 0)
	require.Greater(t, ownerOnly, group)
}
