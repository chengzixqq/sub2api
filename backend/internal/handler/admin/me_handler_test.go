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
	require.Equal(t, "admin@example.com", requireJSONType[map[string]any](t, data["user"])["email"])
	require.Nil(t, data["workspace"])
	security := requireJSONType[map[string]any](t, data["security"])
	require.Equal(t, true, security["step_up_available"])
	require.Equal(t, true, security["step_up_granted"])
	require.EqualValues(t, 812, security["step_up_expires_in"])
	require.Equal(t, true, requireJSONType[map[string]any](t, data["compliance"])["required"], "compliance is reported, not enforced")
	features := requireJSONType[map[string]any](t, data["features"])
	require.Equal(t, true, features["ops_monitoring_enabled"])
	require.Equal(t, false, features["ops_realtime_monitoring_enabled"])
	require.Contains(t, features, "ops_monitoring_disabled_reason")
	require.Nil(t, features["ops_monitoring_disabled_reason"])
	server := requireJSONType[map[string]any](t, data["server"])
	require.Equal(t, "0.2.16", server["version"])
	require.Contains(t, server["capabilities"], "accounts.test_result")
	require.Contains(t, server["capabilities"], "accounts.recover_schedulable")
	require.Contains(t, server["capabilities"], "accounts.runtime_batch")
	require.Contains(t, server["capabilities"], "accounts.list_query")
	require.Contains(t, server["capabilities"], "accounts.clear_error_preserve_inactive")
	require.Contains(t, server["capabilities"], "accounts.oauth_error_reasons")
	require.Contains(t, server["capabilities"], "accounts.test_runs")
	require.Contains(t, server["capabilities"], "accounts.token_status")
	require.Contains(t, server["capabilities"], "accounts.reauthorize")
	require.Contains(t, server["capabilities"], "accounts.apply_oauth_preserve_status")
	require.Contains(t, server["capabilities"], "accounts.today_stats_failures")
	require.Contains(t, server["capabilities"], "accounts.today_stats_group")
	require.Contains(t, server["capabilities"], "accounts.mixed_channel_details")
	require.Contains(t, server["capabilities"], "admin.idempotent_writes")
	require.Contains(t, server["capabilities"], "announcements.idempotent_create")
	require.Contains(t, server["capabilities"], "announcements.read_stats")
	require.Contains(t, server["capabilities"], "api_keys.current_concurrency")
	require.Contains(t, server["capabilities"], "api_keys.effective_status")
	require.Contains(t, server["capabilities"], "auth.captcha_provider")
	require.Contains(t, server["capabilities"], "scheduled_tests.upstream_status")
	require.Contains(t, server["capabilities"], "dashboard.home")
	require.Contains(t, server["capabilities"], "dashboard.rates")
	require.Contains(t, server["capabilities"], "dashboard.request_types")
	require.Contains(t, server["capabilities"], "dashboard.users_ranking_sort")
	require.Contains(t, server["capabilities"], "dashboard.users_trend_filtered_metric")
	require.Contains(t, server["capabilities"], "groups.account_buckets")
	require.Contains(t, server["capabilities"], "groups.capacity_filter")
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
	require.Contains(t, server["capabilities"], "meta.catalog")
	require.Contains(t, server["capabilities"], "users.balance_ledger")
	require.Contains(t, server["capabilities"], "users.platform_quota_item")
	require.Contains(t, server["capabilities"], "users.rpm_tracked")
	require.Contains(t, server["capabilities"], "redeem_codes.delete_guard")
	require.Contains(t, server["capabilities"], "redeem_codes.stats")
	require.Contains(t, server["capabilities"], "push.notifications")
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
	security := requireJSONType[map[string]any](t, data["security"])
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
	ws := requireJSONType[map[string]any](t, data["workspace"])
	require.EqualValues(t, 3, ws["id"])
	perms := requireJSONType[map[string]any](t, ws["permissions"])
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
	features := requireJSONType[map[string]any](t, decodeMe(t, rec)["features"])
	require.Equal(t, false, features["ops_monitoring_enabled"])
	require.Equal(t, "setting_off", features["ops_monitoring_disabled_reason"])
}

func TestAdminMe_WebAccountsEditDeepLinkFollowsEmbeddedFrontend(t *testing.T) {
	capabilities := func(embeddedWeb bool) []any {
		h := NewMeHandler(MeDeps{EmbeddedWeb: embeddedWeb})
		rec := httptest.NewRecorder()
		newMeRouter(h, "jwt", service.RoleAdmin).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/me", nil))
		return requireJSONType[[]any](t, requireJSONType[map[string]any](t, decodeMe(t, rec)["server"])["capabilities"])
	}

	require.Contains(t, capabilities(true), "web.accounts_edit_deep_link")
	// 前端单独部署时后端无法知道 Web 版本，不能声称支持。
	require.NotContains(t, capabilities(false), "web.accounts_edit_deep_link")
	// 追加能力不能改动共享的基础列表。
	require.NotContains(t, adminMeCapabilities, "web.accounts_edit_deep_link")
}
