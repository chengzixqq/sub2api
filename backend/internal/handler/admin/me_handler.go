package admin

import (
	"context"
	"log/slog"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// adminMeCapabilities lists server features a client may probe for; append one
// string whenever a mobile-facing capability ships.
var adminMeCapabilities = []string{
	"accounts.apply_oauth_preserve_status",
	"accounts.clear_error_preserve_inactive",
	"accounts.list_query",
	"accounts.mixed_channel_details",
	"accounts.oauth_error_reasons",
	"accounts.patch",
	"accounts.reauthorize",
	"accounts.recover_schedulable",
	"accounts.runtime_batch",
	"admin.access_tokens",
	"admin.idempotent_writes",
	"accounts.status_summary",
	"accounts.structured_error",
	"accounts.test_result",
	"accounts.test_runs",
	"accounts.today_stats_failures",
	"accounts.today_stats_group",
	"accounts.token_status",
	"announcements.idempotent_create",
	"announcements.read_stats",
	"api_keys.effective_status",
	"api_keys.masked_view",
	"dashboard.account_cost",
	"dashboard.accounts_ranking",
	"dashboard.home",
	"dashboard.rates",
	"dashboard.request_types",
	"dashboard.users_ranking_sort",
	"dashboard.users_trend_filtered_metric",
	"groups.member_batch",
	"lists.etag",
	"logs.since_id",
	"meta.catalog",
	"ops.alert_event_counts",
	"ops.alert_event_details",
	"ops.alert_latency_metrics",
	"ops.dashboard_model_filter",
	"ops.ingress_reject_totals",
	"ops.job_heartbeat_status",
	"ops.monitoring_status",
	"ops.request_attempts",
	"ops.system_metrics_history",
	"ops.token_stats_custom_range",
	"proxies.fallback_accounts",
	"scheduled_tests.upstream_status",
	"usage.model_source",
	"users.quick_topup",
}

// adminMeEmbeddedWebCapabilities are Web frontend features. They are listed
// only when the frontend is embedded in this binary; a separately deployed
// frontend may be older or newer than the server.
var adminMeEmbeddedWebCapabilities = []string{
	"web.accounts_edit_deep_link",
}

type meUserReader interface {
	GetByID(ctx context.Context, id int64) (*service.User, error)
}

type meSettingsReader interface {
	IsStepUpEnabled(ctx context.Context) bool
	IsSessionBindingEnabled(ctx context.Context) bool
	GetAdminComplianceStatus(ctx context.Context, adminUserID int64) (*service.AdminComplianceStatus, error)
	GetPublicSettings(ctx context.Context) (*service.PublicSettings, error)
}

type meOpsReader interface {
	IsMonitoringEnabled(ctx context.Context) bool
	IsRealtimeMonitoringEnabled(ctx context.Context) bool
	MonitoringDisabledReason(ctx context.Context) string
}

type meStepUpReader interface {
	StepUpGrantTTL(ctx context.Context, userID int64, sessionKey string) (bool, int64, error)
}

type meWorkspaceReader interface {
	ResolveByUserID(ctx context.Context, userID int64) (*service.Workspace, error)
}

// MeDeps are the read-only sources of GET /admin/me; nil ones are skipped.
type MeDeps struct {
	Users      meUserReader
	Settings   meSettingsReader
	Ops        meOpsReader
	StepUp     meStepUpReader
	Workspaces meWorkspaceReader
	Version    string
	RunMode    string
	// EmbeddedWeb reports whether this binary serves the embedded Web frontend
	// (built with -tags embed). Only then does the Web version match the server,
	// so Web-only capabilities are advertised just in that case.
	EmbeddedWeb bool
}

// MeHandler answers who is calling the admin API and what the server offers.
type MeHandler struct{ deps MeDeps }

func NewMeHandler(deps MeDeps) *MeHandler { return &MeHandler{deps: deps} }

type meUser struct {
	ID       int64  `json:"id"`
	Email    string `json:"email"`
	Username string `json:"username"`
	Role     string `json:"role"`
}

type meWorkspacePermissions struct {
	AccountManage bool `json:"account_manage"`
	GroupOps      bool `json:"group_ops"`
	GroupBilling  bool `json:"group_billing"`
	ProxyManage   bool `json:"proxy_manage"`
	MonitorView   bool `json:"monitor_view"`
}

type meWorkspace struct {
	ID          int64                  `json:"id"`
	Name        string                 `json:"name"`
	Permissions meWorkspacePermissions `json:"permissions"`
}

type meSecurity struct {
	TotpEnabled           bool  `json:"totp_enabled"`
	StepUpEnabled         bool  `json:"step_up_enabled"`
	StepUpAvailable       bool  `json:"step_up_available"`
	StepUpGranted         bool  `json:"step_up_granted"`
	StepUpExpiresIn       int64 `json:"step_up_expires_in"`
	SessionBindingEnabled bool  `json:"session_binding_enabled"`
}

type meCompliance struct {
	Required bool   `json:"required"`
	Version  string `json:"version"`
}

type meFeatures struct {
	OpsMonitoringEnabled         bool `json:"ops_monitoring_enabled"`
	OpsRealtimeMonitoringEnabled bool `json:"ops_realtime_monitoring_enabled"`
	// OpsMonitoringDisabledReason is config_off / setting_off while ops
	// monitoring is off on this instance, otherwise null.
	OpsMonitoringDisabledReason *string `json:"ops_monitoring_disabled_reason"`
	ChannelMonitorEnabled       bool    `json:"channel_monitor_enabled"`
	ChannelMonitorMode          string  `json:"channel_monitor_mode"`
	BackendModeEnabled          bool    `json:"backend_mode_enabled"`
	PaymentEnabled              bool    `json:"payment_enabled"`
	SubscriptionEnabled         bool    `json:"subscription_enabled"`
}

type meServer struct {
	Version         string   `json:"version"`
	RunMode         string   `json:"run_mode"`
	ServerTimezone  string   `json:"server_timezone"`
	ServerUTCOffset string   `json:"server_utc_offset"`
	Capabilities    []string `json:"capabilities"`
}

type meResponse struct {
	AuthMethod string       `json:"auth_method"`
	User       *meUser      `json:"user"`
	Workspace  *meWorkspace `json:"workspace"`
	Security   meSecurity   `json:"security"`
	Compliance meCompliance `json:"compliance"`
	Features   meFeatures   `json:"features"`
	Server     meServer     `json:"server"`
}

// Get returns identity, auth method, workspace permissions, step-up state,
// compliance, feature switches and server capabilities in one call. It works
// for admin JWT, admin API key and vendor JWT, and is exempt from the
// compliance guard (compliance is reported, not enforced).
// GET /api/v1/admin/me
func (h *MeHandler) Get(c *gin.Context) {
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	ctx := c.Request.Context()
	authMethod := c.GetString("auth_method")
	if authMethod == "" {
		authMethod = "jwt"
	}
	out := meResponse{AuthMethod: authMethod, Server: h.serverInfo()}

	var user *service.User
	if h.deps.Users != nil {
		u, err := h.deps.Users.GetByID(ctx, subject.UserID)
		if err != nil {
			response.ErrorFrom(c, err)
			return
		}
		user = u
		out.User = &meUser{ID: u.ID, Email: u.Email, Username: u.Username, Role: u.Role}
	}
	if role, _ := middleware.GetUserRoleFromContext(c); role == service.RoleVendor && h.deps.Workspaces != nil {
		if ws, err := h.deps.Workspaces.ResolveByUserID(ctx, subject.UserID); err == nil && ws != nil {
			out.Workspace = &meWorkspace{ID: ws.ID, Name: ws.Name, Permissions: meWorkspacePermissions{
				AccountManage: ws.Permissions.AccountManage, GroupOps: ws.Permissions.GroupOps,
				GroupBilling: ws.Permissions.GroupBilling, ProxyManage: ws.Permissions.ProxyManage,
				MonitorView: ws.Permissions.MonitorView,
			}}
		} else if err != nil {
			slog.Warn("admin_me_resolve_workspace_failed", "user_id", subject.UserID, "error", err)
		}
	}

	out.Security = h.security(c, subject.UserID, authMethod, user)
	if h.deps.Settings != nil {
		if status, err := h.deps.Settings.GetAdminComplianceStatus(ctx, subject.UserID); err == nil && status != nil {
			out.Compliance = meCompliance{Required: status.Required, Version: status.Version}
		}
		if public, err := h.deps.Settings.GetPublicSettings(ctx); err == nil && public != nil {
			out.Features.ChannelMonitorEnabled = public.ChannelMonitorEnabled
			out.Features.ChannelMonitorMode = public.ChannelMonitorMode
			out.Features.BackendModeEnabled = public.BackendModeEnabled
			out.Features.PaymentEnabled = public.PaymentEnabled
			out.Features.SubscriptionEnabled = public.SubscriptionEnabled
		}
	}
	if h.deps.Ops != nil {
		out.Features.OpsMonitoringEnabled = h.deps.Ops.IsMonitoringEnabled(ctx)
		out.Features.OpsRealtimeMonitoringEnabled = out.Features.OpsMonitoringEnabled && h.deps.Ops.IsRealtimeMonitoringEnabled(ctx)
		if reason := h.deps.Ops.MonitoringDisabledReason(ctx); reason != "" {
			out.Features.OpsMonitoringDisabledReason = &reason
		}
	}
	response.Success(c, out)
}

func (h *MeHandler) security(c *gin.Context, userID int64, authMethod string, user *service.User) meSecurity {
	ctx := c.Request.Context()
	sec := meSecurity{TotpEnabled: user != nil && user.TotpEnabled}
	if h.deps.Settings != nil {
		sec.StepUpEnabled = h.deps.Settings.IsStepUpEnabled(ctx)
		sec.SessionBindingEnabled = h.deps.Settings.IsSessionBindingEnabled(ctx)
	}
	// Admin API keys can never pass step-up (the middleware rejects them).
	sec.StepUpAvailable = !service.IsMachineCredential(authMethod) && sec.TotpEnabled
	if sec.StepUpAvailable && h.deps.StepUp != nil {
		granted, ttl, err := h.deps.StepUp.StepUpGrantTTL(ctx, userID, middleware.StepUpSessionKey(c, userID))
		if err == nil && granted {
			sec.StepUpGranted = true
			sec.StepUpExpiresIn = ttl
		}
	}
	return sec
}

func (h *MeHandler) serverInfo() meServer {
	now := time.Now().In(timezone.Location())
	capabilities := append([]string(nil), adminMeCapabilities...)
	if h.deps.EmbeddedWeb {
		capabilities = append(capabilities, adminMeEmbeddedWebCapabilities...)
	}
	return meServer{
		Version:         h.deps.Version,
		RunMode:         h.deps.RunMode,
		ServerTimezone:  timezone.Name(),
		ServerUTCOffset: now.Format("-07:00"),
		Capabilities:    capabilities,
	}
}
