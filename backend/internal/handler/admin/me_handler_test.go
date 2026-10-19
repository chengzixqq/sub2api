package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type meUserReaderFake struct{ user *service.User }

func (f meUserReaderFake) GetByID(context.Context, int64) (*service.User, error) { return f.user, nil }

type meSettingsFake struct {
	stepUp, binding bool
	compliance      *service.AdminComplianceStatus
	public          *service.PublicSettings
}

func (f meSettingsFake) IsStepUpEnabled(context.Context) bool         { return f.stepUp }
func (f meSettingsFake) IsSessionBindingEnabled(context.Context) bool { return f.binding }
func (f meSettingsFake) GetAdminComplianceStatus(context.Context, int64) (*service.AdminComplianceStatus, error) {
	return f.compliance, nil
}
func (f meSettingsFake) GetPublicSettings(context.Context) (*service.PublicSettings, error) {
	return f.public, nil
}

type meOpsFake struct {
	monitoring, realtime bool
	reason               string
}

func (f meOpsFake) IsMonitoringEnabled(context.Context) bool         { return f.monitoring }
func (f meOpsFake) IsRealtimeMonitoringEnabled(context.Context) bool { return f.realtime }
func (f meOpsFake) MonitoringDisabledReason(context.Context) string  { return f.reason }

type meStepUpFake struct {
	granted bool
	ttl     int64
}

func (f meStepUpFake) StepUpGrantTTL(context.Context, int64, string) (bool, int64, error) {
	return f.granted, f.ttl, nil
}

type meWorkspaceFake struct{ ws *service.Workspace }

func (f meWorkspaceFake) ResolveByUserID(context.Context, int64) (*service.Workspace, error) {
	return f.ws, nil
}

func newMeRouter(h *MeHandler, authMethod, role string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 1})
		c.Set(string(middleware.ContextKeyUserRole), role)
		c.Set("auth_method", authMethod)
		c.Next()
	})
	r.GET("/admin/me", h.Get)
	return r
}

func decodeMe(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var resp struct {
		Data map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	return resp.Data
}

func TestAdminMe_JWTAdmin(t *testing.T) {
	h := NewMeHandler(MeDeps{
		Users:    meUserReaderFake{user: &service.User{ID: 1, Email: "admin@example.com", Username: "admin", Role: service.RoleAdmin, TotpEnabled: true}},
		Settings: meSettingsFake{stepUp: true, compliance: &service.AdminComplianceStatus{Required: true, Version: "v2026.06.10"}, public: &service.PublicSettings{ChannelMonitorEnabled: true, ChannelMonitorMode: "v1", PaymentEnabled: true}},
		Ops:      meOpsFake{monitoring: true, realtime: false},
		StepUp:   meStepUpFake{granted: true, ttl: 812},
		Version:  "0.2.16",
		RunMode:  "standard",
	})
	rec := httptest.NewRecorder()
	newMeRouter(h, "jwt", service.RoleAdmin).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/me", nil))
	data := decodeMe(t, rec)
	require.Equal(t, "jwt", data["auth_method"])
	require.Equal(t, "admin@example.com", data["user"].(map[string]any)["email"])
	require.Nil(t, data["workspace"])
	security := data["security"].(map[string]any)
	require.Equal(t, true, security["step_up_available"])
	require.Equal(t, true, security["step_up_granted"])
	require.EqualValues(t, 812, security["step_up_expires_in"])
	require.Equal(t, true, data["compliance"].(map[string]any)["required"], "compliance is reported, not enforced")
	features := data["features"].(map[string]any)
	require.Equal(t, true, features["ops_monitoring_enabled"])
	require.Equal(t, false, features["ops_realtime_monitoring_enabled"])
	require.Contains(t, features, "ops_monitoring_disabled_reason")
	require.Nil(t, features["ops_monitoring_disabled_reason"])
	server := data["server"].(map[string]any)
	require.Equal(t, "0.2.16", server["version"])
	require.Contains(t, server["capabilities"], "accounts.test_result")
	require.Contains(t, server["capabilities"], "dashboard.home")
	require.Contains(t, server["capabilities"], "dashboard.rates")
	require.Contains(t, server["capabilities"], "dashboard.request_types")
	require.Contains(t, server["capabilities"], "dashboard.users_ranking_sort")
	require.Contains(t, server["capabilities"], "dashboard.users_trend_filtered_metric")
	require.Contains(t, server["capabilities"], "ops.alert_event_counts")
	require.Contains(t, server["capabilities"], "ops.alert_event_details")
	require.Contains(t, server["capabilities"], "ops.alert_latency_metrics")
	require.Contains(t, server["capabilities"], "ops.dashboard_model_filter")
	require.Contains(t, server["capabilities"], "ops.ingress_reject_totals")
	require.Contains(t, server["capabilities"], "ops.job_heartbeat_status")
	require.Contains(t, server["capabilities"], "ops.monitoring_status")
	require.Contains(t, server["capabilities"], "ops.request_attempts")
	require.Contains(t, server["capabilities"], "ops.system_metrics_history")
	require.Contains(t, server["capabilities"], "ops.token_stats_custom_range")
	require.Contains(t, server["capabilities"], "lists.etag")
	require.Contains(t, server["capabilities"], "logs.since_id")
}

func TestAdminMe_AdminAPIKeyHasNoStepUp(t *testing.T) {
	h := NewMeHandler(MeDeps{
		Users:    meUserReaderFake{user: &service.User{ID: 1, Role: service.RoleAdmin, TotpEnabled: true}},
		Settings: meSettingsFake{stepUp: true, compliance: &service.AdminComplianceStatus{}, public: &service.PublicSettings{}},
		Ops:      meOpsFake{monitoring: true, realtime: true},
		StepUp:   meStepUpFake{granted: true, ttl: 600},
	})
	rec := httptest.NewRecorder()
	newMeRouter(h, service.AuditAuthMethodAdminAPIKey, service.RoleAdmin).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/me", nil))
	data := decodeMe(t, rec)
	require.Equal(t, "admin_api_key", data["auth_method"])
	security := data["security"].(map[string]any)
	require.Equal(t, false, security["step_up_available"])
	require.Equal(t, false, security["step_up_granted"])
}

func TestAdminMe_VendorWorkspace(t *testing.T) {
	h := NewMeHandler(MeDeps{
		Users:      meUserReaderFake{user: &service.User{ID: 1, Role: service.RoleVendor}},
		Settings:   meSettingsFake{compliance: &service.AdminComplianceStatus{}, public: &service.PublicSettings{}},
		Ops:        meOpsFake{},
		StepUp:     meStepUpFake{},
		Workspaces: meWorkspaceFake{ws: &service.Workspace{ID: 3, Name: "供应商甲", Permissions: service.WorkspacePermissions{AccountManage: true, ProxyManage: true}}},
	})
	rec := httptest.NewRecorder()
	newMeRouter(h, "jwt", service.RoleVendor).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/me", nil))
	data := decodeMe(t, rec)
	ws := data["workspace"].(map[string]any)
	require.EqualValues(t, 3, ws["id"])
	perms := ws["permissions"].(map[string]any)
	require.Equal(t, true, perms["account_manage"])
	require.Equal(t, false, perms["group_billing"])
}

func TestAdminMe_OpsMonitoringDisabledReason(t *testing.T) {
	h := NewMeHandler(MeDeps{
		Users: meUserReaderFake{user: &service.User{ID: 1, Role: service.RoleAdmin}},
		Ops:   meOpsFake{monitoring: false, reason: service.OpsMonitoringDisabledSettingOff},
	})
	rec := httptest.NewRecorder()
	newMeRouter(h, "jwt", service.RoleAdmin).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/me", nil))
	features := decodeMe(t, rec)["features"].(map[string]any)
	require.Equal(t, false, features["ops_monitoring_enabled"])
	require.Equal(t, "setting_off", features["ops_monitoring_disabled_reason"])
}
