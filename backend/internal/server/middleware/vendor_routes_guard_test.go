package middleware

import (
	"net/http/httptest"
	"regexp"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// allPermsOn 返回所有权限档均开启的权限集。
//
// 用它做「即便权限全开也不得访问」的断言：把路由白名单与权限档解耦，
// station-only 端点必须因为不在白名单里而被拒，而不是因为权限没开。
func allPermsOn() service.WorkspacePermissions {
	return service.WorkspacePermissions{
		AccountManage: true,
		ProxyManage:   true,
		GroupOps:      true,
		GroupBilling:  true,
		MonitorView:   true,
	}
}

// TestVendorRoutesDenyStationOnlyEndpoints 锁定站长专属端点对 vendor 永久关闭。
//
// 这是「默认拒绝」的核心回归锚点：即使 vendor 权限档全开，
// 这些端点也必须落在白名单之外。列表对应 vendor_routes.go 中
// 「有意不放开的能力」注释，两处需保持一致。
func TestVendorRoutesDenyStationOnlyEndpoints(t *testing.T) {
	scope := VendorScope{WorkspaceID: 7, Perms: allPermsOn()}

	stationOnly := []struct {
		path   string
		method string
	}{
		{"/api/v1/admin/users", "GET"},
		{"/api/v1/admin/users/:id", "PUT"},
		{"/api/v1/admin/redeem", "GET"},
		{"/api/v1/admin/subscriptions", "GET"},
		{"/api/v1/admin/announcements", "GET"},
		{"/api/v1/admin/backup", "POST"},
		{"/api/v1/admin/data", "GET"},
		{"/api/v1/admin/settings", "GET"},
		{"/api/v1/admin/settings", "PUT"},
		{"/api/v1/admin/ops", "GET"},
		{"/api/v1/admin/audit-logs", "GET"},
		{"/api/v1/admin/risk", "GET"},
		{"/api/v1/admin/channel-monitor-v2/config", "GET"},
		{"/api/v1/admin/channel-monitor-v2/snapshot", "GET"},
		// 合规确认曾在此列，是错的：AdminComplianceGuard 对未确认者一律 423，
		// 而本白名单挡住确认接口本身，vendor 会被两个中间件夹死。
		// 现由 TestVendorComplianceSelfServiceReachable 断言其必须可达。
	}

	for _, tc := range stationOnly {
		c := newFullPathContext(tc.method, tc.path)
		if vendorRouteAllowed(c, scope) {
			t.Errorf("%s %s 必须对 vendor 关闭（权限全开也不得放行）", tc.method, tc.path)
		}
	}
}

// TestVendorRoutesDenyUnscopableDashboards 锁定无法收窄的看板端点保持关闭。
//
// 这些端点按全站用户/密钥维度聚合，无法施加账号白名单。
// 若被放行，vendor 将看到其他供应商与站长自持账号的数据。
func TestVendorRoutesDenyUnscopableDashboards(t *testing.T) {
	scope := VendorScope{WorkspaceID: 7, Perms: allPermsOn()}

	unscopable := []string{
		"/api/v1/admin/dashboard/users-ranking",
		"/api/v1/admin/dashboard/users-trend",
		"/api/v1/admin/dashboard/api-keys-trend",
		"/api/v1/admin/dashboard/user-breakdown",
		"/api/v1/admin/dashboard/stats",
		"/api/v1/admin/dashboard/snapshot-v2",
		"/api/v1/admin/dashboard/realtime",
	}

	for _, path := range unscopable {
		c := newFullPathContext("GET", path)
		if vendorRouteAllowed(c, scope) {
			t.Errorf("%s 无法按账号收窄，必须对 vendor 关闭", path)
		}
	}
}

// TestVendorRoutesRejectEmptyFullPath 锁定未匹配到路由时拒绝。
//
// FullPath 为空意味着 gin 没有匹配到任何注册路由。此时若放行，
// 等于对未知路径开门。
func TestVendorRoutesRejectEmptyFullPath(t *testing.T) {
	scope := VendorScope{WorkspaceID: 7, Perms: allPermsOn()}
	c := newFullPathContext("GET", "")
	if vendorRouteAllowed(c, scope) {
		t.Error("FullPath 为空时必须拒绝")
	}
}

// TestVendorRoutesPrefixDoesNotLeakSiblings 锁定前缀匹配不会误放同前缀端点。
//
// /accounts 不得放行 /accounts-export：后者是另一个端点，
// 若因字符串前缀相同而被放行，等于绕过白名单。
func TestVendorRoutesPrefixDoesNotLeakSiblings(t *testing.T) {
	scope := VendorScope{WorkspaceID: 7, Perms: allPermsOn()}

	for _, path := range []string{
		"/api/v1/admin/accounts-export",
		"/api/v1/admin/groups-export",
		"/api/v1/admin/proxies-import",
	} {
		c := newFullPathContext("GET", path)
		if vendorRouteAllowed(c, scope) {
			t.Errorf("%s 与白名单项同前缀但语义无关，必须拒绝", path)
		}
	}
}

// TestVendorRoutesMethodIsEnforced 锁定方法维度的收窄。
//
// 只读档不得写：MonitorView 单开时可以 GET 账号，但不能 DELETE。
func TestVendorRoutesMethodIsEnforced(t *testing.T) {
	monitorOnly := VendorScope{
		WorkspaceID: 7,
		Perms:       service.WorkspacePermissions{MonitorView: true},
	}

	if !vendorRouteAllowed(newFullPathContext("GET", "/api/v1/admin/accounts"), monitorOnly) {
		t.Error("MonitorView 应可读账号列表")
	}
	for _, method := range []string{"POST", "PUT", "PATCH", "DELETE"} {
		c := newFullPathContext(method, "/api/v1/admin/accounts")
		if vendorRouteAllowed(c, monitorOnly) {
			t.Errorf("MonitorView 单开时不得 %s 账号", method)
		}
	}
}

func TestVendorMonitorViewCanReadExactAccountBatches(t *testing.T) {
	monitorOnly := VendorScope{
		WorkspaceID: 7,
		Perms:       service.WorkspacePermissions{MonitorView: true},
	}

	for _, path := range []string{
		"/api/v1/admin/accounts/usage/batch",
		"/api/v1/admin/accounts/today-stats/batch",
	} {
		if !vendorRouteAllowed(newFullPathContext("POST", path), monitorOnly) {
			t.Errorf("POST %s should be available with MonitorView", path)
		}
		if vendorRouteAllowed(newFullPathContext("GET", path), monitorOnly) {
			t.Errorf("GET %s must not inherit the batch read exception", path)
		}
	}

	withoutMonitor := VendorScope{
		WorkspaceID: 7,
		Perms:       service.WorkspacePermissions{AccountManage: true},
	}
	for _, path := range []string{
		"/api/v1/admin/accounts/usage/batch",
		"/api/v1/admin/accounts/today-stats/batch",
	} {
		if vendorRouteAllowed(newFullPathContext("POST", path), withoutMonitor) {
			t.Errorf("POST %s must require MonitorView", path)
		}
	}
}

func TestVendorGrokOAuthOnlyAllowsScopedReauthHelpers(t *testing.T) {
	scope := VendorScope{
		WorkspaceID: 7,
		Perms:       service.WorkspacePermissions{AccountManage: true},
	}
	allowed := []struct {
		method string
		path   string
	}{
		{"GET", "/api/v1/admin/grok/oauth/capabilities"},
		{"POST", "/api/v1/admin/grok/oauth/auth-url"},
		{"POST", "/api/v1/admin/grok/oauth/exchange-code"},
		{"POST", "/api/v1/admin/grok/oauth/refresh-token"},
		{"POST", "/api/v1/admin/grok/oauth/sso-token"},
	}
	for _, route := range allowed {
		if !vendorRouteAllowed(newFullPathContext(route.method, route.path), scope) {
			t.Errorf("%s %s should be available for scoped reauthorization", route.method, route.path)
		}
	}

	stationOnly := []struct {
		method string
		path   string
	}{
		{"POST", "/api/v1/admin/grok/oauth/password"},
		{"POST", "/api/v1/admin/grok/oauth/create-from-oauth"},
		{"POST", "/api/v1/admin/grok/sso-to-oauth"},
		{"POST", "/api/v1/admin/grok/oauth/reconcile"},
		{"GET", "/api/v1/admin/grok/runtime-sanity"},
		{"POST", "/api/v1/admin/grok/accounts/:id/refresh"},
	}
	for _, route := range stationOnly {
		if vendorRouteAllowed(newFullPathContext(route.method, route.path), scope) {
			t.Errorf("%s %s must remain station-owner only", route.method, route.path)
		}
	}
}

// TestVendorRoutesWorkspaceSelfReadNeedsNoPerm 锁定工作区自读不依赖权限档。
//
// vendor 必须能读到自己的工作区信息，否则前端无法渲染身份与权限，
// 会陷入「登录了但什么都看不到」的死局。
func TestVendorRoutesWorkspaceSelfReadNeedsNoPerm(t *testing.T) {
	noPerms := VendorScope{WorkspaceID: 7}
	c := newFullPathContext("GET", "/api/v1/admin/workspaces/me")
	if !vendorRouteAllowed(c, noPerms) {
		t.Error("工作区自读不应依赖任何权限档")
	}
}

// newFullPathContext 构造一个 FullPath 已就绪的 gin 上下文。
//
// 通过真实注册路由并发起请求来获得 FullPath —— gin.CreateTestContext
// 造出的上下文 FullPath() 恒为空，用它测白名单会让所有断言假通过。
// path 传空字符串时返回未匹配任何路由的上下文，用于覆盖 FullPath 为空的分支。
func newFullPathContext(method, path string) *gin.Context {
	gin.SetMode(gin.TestMode)

	if path == "" {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(method, "/unmatched", nil)
		return c
	}

	var captured *gin.Context
	r := gin.New()
	r.Handle(method, path, func(c *gin.Context) { captured = c })

	// 把 :id 之类的占位符替换成具体值，才能命中注册的路由。
	requestPath := placeholderPattern.ReplaceAllString(path, "1")
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(method, requestPath, nil))

	return captured
}

var placeholderPattern = regexp.MustCompile(`:[^/]+`)

// TestVendorComplianceSelfServiceReachable 锁定 vendor 能自行完成合规确认。
//
// AdminComplianceGuard 对未确认者返回 423，且自带 bypass 让合规端点本身免检；
// 但 VendorScope 挂在它之前，若白名单不放行这两个端点，vendor 会被夹死：
// 业务请求 423 提示去确认，确认接口却 403。这是死锁而非越权，故必须放行。
func TestVendorComplianceSelfServiceReachable(t *testing.T) {
	// 权限档全关：合规确认是账号自身义务，不该依赖任何业务授权。
	scope := VendorScope{WorkspaceID: 7}

	for _, tc := range []struct{ method, path string }{
		{"GET", "/api/v1/admin/compliance"},
		{"POST", "/api/v1/admin/compliance/accept"},
	} {
		c := newFullPathContext(tc.method, tc.path)
		if !vendorRouteAllowed(c, scope) {
			t.Errorf("%s %s 必须对 vendor 放行，否则 423 与 403 互锁", tc.method, tc.path)
		}
	}
}

// TestVendorGroupsDenyCustomerFacetEndpoints 锁定分组下的客户维度子资源不外泄。
//
// /admin/groups 的 GET 由权限档整体放行，但其下若干子资源承载的是
// 终端用户信息（谁持有 Key、谁享受折扣、谁的 RPM 上限）——那是站长统管的
// 客户名单，不属于「分组运维」。它们必须由 deny 层先行拦下，
// 而不能指望 service 层过滤：过滤只能收窄行，删不掉字段语义。
func TestVendorGroupsDenyCustomerFacetEndpoints(t *testing.T) {
	scope := VendorScope{WorkspaceID: 7, Perms: allPermsOn()}

	customerFacet := []struct{ method, path string }{
		{"GET", "/api/v1/admin/groups/:id/api-keys"},
		{"GET", "/api/v1/admin/groups/:id/rate-multipliers"},
		{"PUT", "/api/v1/admin/groups/:id/rate-multipliers/:user_id"},
		{"DELETE", "/api/v1/admin/groups/:id/rate-multipliers/:user_id"},
		{"GET", "/api/v1/admin/groups/:id/rpm-overrides"},
		{"PUT", "/api/v1/admin/groups/:id/rpm-overrides/:user_id"},
	}

	for _, tc := range customerFacet {
		c := newFullPathContext(tc.method, tc.path)
		if vendorRouteAllowed(c, scope) {
			t.Errorf("%s %s 泄露客户名单，必须对 vendor 关闭（权限全开也不得放行）", tc.method, tc.path)
		}
	}
}
