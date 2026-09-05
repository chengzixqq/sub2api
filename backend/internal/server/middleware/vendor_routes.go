package middleware

import (
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// vendorRoute 描述一条对 vendor 开放的管理端路由。
//
// 匹配按「路径前缀 + 方法」进行；permit 为 nil 表示只要身份是 vendor 即可访问
// （如 /auth/me、工作区自读），否则需对应权限档已开启。
type vendorRoute struct {
	prefix  string
	methods []string
	permit  func(service.WorkspacePermissions) bool
	// exclude prevents a broad prefix rule from inheriting access to an
	// endpoint that has a narrower permission contract.
	exclude []string
	// exact 为 true 时要求路径完全相等，而非前缀匹配。
	//
	// 看板下的端点粒度差异很大：/dashboard/trend 走已施加账号白名单的
	// usage_logs 查询，而 /dashboard/users-ranking 按全站用户维度聚合，
	// 没有也无法有账号谓词。用前缀放行整个 /dashboard 会连带放出后者，
	// 因此这类必须逐条精确登记。
	exact bool
}

// vendorDeniedRoutes 是先于白名单生效的排除清单。
//
// 需要它是因为白名单按前缀放行：一条 /admin/groups 的 GET 规则会连带放出
// 分组下所有子端点，其中若干是「以终端用户为键」的数据——客户名单、
// 各自的议价倍率与 RPM 例外。这些按设计由站长统管，不随分组是否授权而开放，
// 却又无法靠收紧前缀排除（它们与合法端点共享同一前缀）。
//
// 匹配到任一条即拒绝，不再查白名单。service 层对同一批端点另有拦截，
// 两处都在是有意为之：路由层挡住流量，service 层兜住日后新增的调用方。
var vendorDeniedRoutes = []vendorRoute{
	{prefix: "/api/v1/admin/groups/:id/api-keys", methods: allMethods},
	{prefix: "/api/v1/admin/groups/:id/rate-multipliers", methods: allMethods},
	{prefix: "/api/v1/admin/groups/:id/rpm-overrides", methods: allMethods},
	// V2 aggregates station-wide passive traffic and has no workspace predicate.
	{prefix: "/api/v1/admin/channel-monitor-v2", methods: allMethods},

	// 账号前缀下的全站级配置：这两条改的是探测器的全局开关与频率，
	// 不带 :id，没有任何归属可校验 —— 一个 vendor 改动会影响所有工作区。
	// 只拦写：GET 供账号编辑页读取当前值，对 vendor 无害。
	{prefix: "/api/v1/admin/accounts/upstream-billing-probe/settings", methods: writeMethods, exact: true},
	{prefix: "/api/v1/admin/accounts/ollama-cloud-usage/settings", methods: writeMethods, exact: true},

	// 导入/导出与跨账号同步：整库级数据搬运，且 CRS 同步会按上游返回
	// 批量建号，无法归属到发起方工作区。导出另有 step-up 2FA，但那道门
	// 只防凭证泄露，不防越权读取别家账号。
	{prefix: "/api/v1/admin/accounts/data", methods: allMethods, exact: true},
	{prefix: "/api/v1/admin/accounts/sync/crs", methods: allMethods},
	{prefix: "/api/v1/admin/accounts/import/codex-session", methods: allMethods, exact: true},

	// 代理导入/导出同理：导出会带出全站代理的主机、端口与凭证，
	// 导入则批量建号且无法归属发起方工作区。
	{prefix: "/api/v1/admin/proxies/data", methods: allMethods, exact: true},
}

// writeMethods 表示仅对写方法生效。
var writeMethods = []string{"POST", "PUT", "PATCH", "DELETE"}

// allMethods 表示该规则对任意 HTTP 方法生效。
var allMethods = []string{"GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"}

// vendorAllowedRoutes 是 vendor 可访问的管理端路由白名单。
//
// 这里是「默认拒绝」的唯一收口点：任何未列出的管理端端点，vendor 一律 403。
// 新增 vendor 能力时必须显式登记，避免加接口时无声扩权。
//
// 有意不放开的能力（站长专属）：用户管理、兑换码、订阅、公告、备份、
// 数据管理、系统设置、运维（ops）、审计日志、风控、合规。
var vendorAllowedRoutes = []vendorRoute{
	// 合规确认：必须恒开，且不受任何权限档约束。
	//
	// AdminComplianceGuard 对未确认者一律返回 423，只给 /compliance 自身开了
	// 免检通道。而本中间件挂在它之前——不放行这两条，vendor 会被 423 要求确认，
	// 却又被 403 挡在确认接口之外，成为无法自解的死锁。
	{prefix: "/api/v1/admin/compliance", methods: []string{"GET"}, exact: true},
	{prefix: "/api/v1/admin/compliance/accept", methods: []string{"POST"}, exact: true},

	// 自身信息与工作区自读：vendor 恒可访问。
	//
	// 只有 /me，没有自助调价端点：结算倍率是账号自身的 rate_multiplier，
	// 经 /api/v1/admin/accounts/:id 修改（已由下方账号规则按
	// AccountManage 放行），可调区间由工作区的 settlement_rate_min/max
	// 在 service 层夹住。工作区写路径一律不对 vendor 开放 —— 放出去
	// 等于供应商可自建工作区、给自己授权任意分组、自定结算区间。
	{prefix: "/api/v1/admin/workspaces/me", methods: []string{"GET"}},

	// 账号：在已授权分组内管理自己工作区的账号。
	{prefix: "/api/v1/admin/accounts", methods: []string{"GET"}, permit: permitAccountRead, exclude: vendorBatchAccountRoutes},
	{prefix: "/api/v1/admin/accounts/usage/batch", methods: []string{"POST"}, exact: true, permit: permitMonitorView},
	{prefix: "/api/v1/admin/accounts/today-stats/batch", methods: []string{"POST"}, exact: true, permit: permitMonitorView},
	{prefix: "/api/v1/admin/accounts", methods: []string{"POST", "PUT", "PATCH", "DELETE"}, permit: func(p service.WorkspacePermissions) bool {
		return p.AccountManage
	}, exclude: vendorBatchAccountRoutes},

	// Grok reauthorization helpers. Handlers require a scoped account_id and
	// derive the proxy from that existing account; all other Grok endpoints stay
	// station-owner only by default.
	{prefix: "/api/v1/admin/grok/oauth/capabilities", methods: []string{"GET"}, exact: true, permit: permitAccountManage},
	{prefix: "/api/v1/admin/grok/oauth/auth-url", methods: []string{"POST"}, exact: true, permit: permitAccountManage},
	{prefix: "/api/v1/admin/grok/oauth/exchange-code", methods: []string{"POST"}, exact: true, permit: permitAccountManage},
	{prefix: "/api/v1/admin/grok/oauth/refresh-token", methods: []string{"POST"}, exact: true, permit: permitAccountManage},
	{prefix: "/api/v1/admin/grok/oauth/sso-token", methods: []string{"POST"}, exact: true, permit: permitAccountManage},

	// 代理：管理自己工作区的代理。
	{prefix: "/api/v1/admin/proxies", methods: []string{"GET"}, permit: permitProxyRead},
	{prefix: "/api/v1/admin/proxies", methods: []string{"POST", "PUT", "PATCH", "DELETE"}, permit: func(p service.WorkspacePermissions) bool {
		return p.ProxyManage
	}},

	// 分组：只读已授权分组；改动按运营/计费两档分别判定（service 层再按字段细分）。
	{prefix: "/api/v1/admin/groups", methods: []string{"GET"}, permit: permitGroupRead},
	{prefix: "/api/v1/admin/groups", methods: []string{"PUT", "PATCH"}, permit: func(p service.WorkspacePermissions) bool {
		return p.GroupOps || p.GroupBilling
	}},

	// 用量与看板：仅监控档开启时可读，金额字段由 service 层按档裁剪。
	{prefix: "/api/v1/admin/usage", methods: []string{"GET"}, permit: permitMonitorView},
	// 看板：只放行按账号维度可收窄的三条，其余（users-ranking、users-trend、
	// api-keys-trend、user-breakdown、stats、snapshot-v2、realtime）按全站
	// 用户/密钥维度聚合，无法施加账号白名单，属站长专属。
	{prefix: "/api/v1/admin/dashboard/trend", methods: []string{"GET"}, exact: true, permit: permitMonitorView},
	{prefix: "/api/v1/admin/dashboard/models", methods: []string{"GET"}, exact: true, permit: permitMonitorView},
	{prefix: "/api/v1/admin/dashboard/groups", methods: []string{"GET"}, exact: true, permit: permitMonitorView},
}

var vendorBatchAccountRoutes = []string{
	"/api/v1/admin/accounts/usage/batch",
	"/api/v1/admin/accounts/today-stats/batch",
}

// vendorUsageScopedRoutes 是需要注入账号白名单的 vendor 路由前缀。
//
// 必须覆盖上表中所有会查询 usage_logs 的条目。漏登记一条不会导致 403，
// 而是让该端点在无白名单的情况下查询用量 —— 静默越权。因此新增用量类
// 路由时，两处都要改。
var vendorUsageScopedRoutes = []string{
	"/api/v1/admin/usage",
	"/api/v1/admin/dashboard/trend",
	"/api/v1/admin/dashboard/models",
	"/api/v1/admin/dashboard/groups",
}

// permitMonitorView 监控档开启方可读用量与看板。
func permitMonitorView(p service.WorkspacePermissions) bool {
	return p.MonitorView
}

func permitAccountManage(p service.WorkspacePermissions) bool {
	return p.AccountManage
}

// permitAccountRead 账号只读：账号管理或监控任一档开启即可查看。
func permitAccountRead(p service.WorkspacePermissions) bool {
	return p.AccountManage || p.MonitorView
}

// permitProxyRead 代理只读：代理管理档开启，或账号管理需要选代理时。
func permitProxyRead(p service.WorkspacePermissions) bool {
	return p.ProxyManage || p.AccountManage
}

// permitGroupRead 分组只读：任一与分组或账号相关的档开启即可查看已授权分组。
func permitGroupRead(p service.WorkspacePermissions) bool {
	return p.GroupOps || p.GroupBilling || p.AccountManage || p.MonitorView
}

// vendorRouteAllowed 判断当前请求是否命中 vendor 白名单且权限档满足。
//
// 使用 FullPath（含 :id 占位符）而非原始 URL，避免路径参数干扰前缀匹配；
// FullPath 为空（未匹配到任何路由）时一律拒绝。
func vendorRouteAllowed(c *gin.Context, scope VendorScope) bool {
	fullPath := c.FullPath()
	if fullPath == "" {
		return false
	}

	method := c.Request.Method

	// 排除清单先行：白名单按前缀放行，只有在这里才能挡住与合法端点
	// 同前缀的站长专属子路径。
	for _, route := range vendorDeniedRoutes {
		if route.exact && fullPath != route.prefix {
			continue
		}
		if !route.exact && !matchesPrefix(fullPath, route.prefix) {
			continue
		}
		if containsMethod(route.methods, method) {
			return false
		}
	}

	for _, route := range vendorAllowedRoutes {
		if containsPath(route.exclude, fullPath) {
			continue
		}
		if route.exact {
			if fullPath != route.prefix {
				continue
			}
		} else if !matchesPrefix(fullPath, route.prefix) {
			continue
		}
		if !containsMethod(route.methods, method) {
			continue
		}
		if route.permit == nil {
			return true
		}
		if route.permit(scope.Perms) {
			return true
		}
	}
	return false
}

func containsPath(paths []string, path string) bool {
	for _, candidate := range paths {
		if candidate == path {
			return true
		}
	}
	return false
}

// matchesPrefix 报告路径是否落在给定前缀下。
//
// 要求完全相等或以 "<prefix>/" 开头，避免 /accounts 误匹配 /accounts-export
// 这类同前缀但语义无关的端点。
func matchesPrefix(fullPath, prefix string) bool {
	if fullPath == prefix {
		return true
	}
	return strings.HasPrefix(fullPath, prefix+"/")
}

func containsMethod(methods []string, method string) bool {
	for _, candidate := range methods {
		if candidate == method {
			return true
		}
	}
	return false
}
