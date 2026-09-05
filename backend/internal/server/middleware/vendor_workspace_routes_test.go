package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// workspaceRouteCase 描述一条工作区端点的期望可见性。
type workspaceRouteCase struct {
	method   string
	template string
	// vendorAllowed 为 true 表示已授权 vendor 应能访问。
	vendorAllowed bool
	why           string
}

// workspaceRouteCases 覆盖全部工作区端点。
//
// 站长专属项占多数：工作区的增删改、成员绑定与分组授权都是发放权限的动作，
// 供应商一旦能碰其中任何一个，就能给自己授权任意分组或绑定他人。
var workspaceRouteCases = []workspaceRouteCase{
	{"GET", "/api/v1/admin/workspaces/me", true, "自读本工作区"},

	// 结算倍率挂在账号上（走 /admin/accounts/:id），工作区侧没有调价端点。
	// 这条锁定它不会被重新引入：工作区任何写路径对 vendor 开放，都等于
	// 供应商可自定结算区间，把站长设的 settlement_rate_min/max 架空。
	{"PUT", "/api/v1/admin/workspaces/self/grants/:group_id/cost-rate", false, "工作区侧无调价端点，倍率改账号"},

	{"GET", "/api/v1/admin/workspaces", false, "列出全部工作区属站长视角"},
	{"POST", "/api/v1/admin/workspaces", false, "自建工作区等于自行扩权"},
	{"GET", "/api/v1/admin/workspaces/:id", false, "读他人工作区"},
	{"PUT", "/api/v1/admin/workspaces/:id", false, "改工作区可自行放开权限档"},
	{"DELETE", "/api/v1/admin/workspaces/:id", false, "删他人工作区"},
	{"GET", "/api/v1/admin/workspaces/:id/members", false, "成员名单属运营信息"},
	{"POST", "/api/v1/admin/workspaces/:id/members", false, "自行拉人入驻"},
	{"DELETE", "/api/v1/admin/workspaces/:id/members/:user_id", false, "踢出他人成员"},
	{"GET", "/api/v1/admin/workspaces/:id/grants", false, "窥探他人授权"},
	{"PUT", "/api/v1/admin/workspaces/:id/grants", false, "自行授权任意分组——最严重的越权"},
	{"DELETE", "/api/v1/admin/workspaces/:id/grants/:group_id", false, "收回他人授权"},
}

// fullVendorScope 是权限档全开的 vendor 作用域。
//
// 故意全开：这样测试断言的就是「路由白名单本身不放行」，
// 而不是靠权限档恰好为 false 侥幸通过。
func fullVendorScope() VendorScope {
	return VendorScope{
		WorkspaceID: 7,
		Perms: service.WorkspacePermissions{
			AccountManage: true,
			GroupOps:      true,
			GroupBilling:  true,
			ProxyManage:   true,
			MonitorView:   true,
		},
	}
}

// TestVendorWorkspaceRouteVisibility 锁定工作区端点对 vendor 的可见性。
//
// 用 gin 真实注册模板再取 FullPath()，因此这条测试同时验证白名单里
// 手写的 exact 路径与实际注册的模板逐字一致 —— 拼错一个段会让
// 自助调价静默变成 403，而这种错误只靠读代码很难发现。
func TestVendorWorkspaceRouteVisibility(t *testing.T) {
	gin.SetMode(gin.TestMode)

	for _, tc := range workspaceRouteCases {
		t.Run(tc.method+" "+tc.template, func(t *testing.T) {
			var allowed bool
			r := gin.New()
			r.Handle(tc.method, tc.template, func(c *gin.Context) {
				allowed = vendorRouteAllowed(c, fullVendorScope())
				c.Status(http.StatusOK)
			})

			// 用模板串本身发请求：:id 等占位符会被当作普通字面量匹配，
			// FullPath() 仍返回注册模板，正是白名单的比对对象。
			req := httptest.NewRequest(tc.method, tc.template, nil)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			if w.Code != http.StatusOK {
				t.Fatalf("路由未命中（%d），模板与请求路径不匹配", w.Code)
			}
			if allowed != tc.vendorAllowed {
				t.Errorf("vendorRouteAllowed = %v, 期望 %v —— %s", allowed, tc.vendorAllowed, tc.why)
			}
		})
	}
}

// TestVendorWorkspaceWritesAlwaysDenied 锁定工作区写路径对 vendor 恒关。
//
// 结算区间（settlement_rate_min/max）是站长夹住供应商定价的唯一手段，
// 而它就存在工作区行上。因此任何工作区写端点一旦对 vendor 开放，
// 供应商就能自行放宽区间，把区间校验架空 —— 权限档全开也不例外，
// 这也是本用例把五档全开的原因：放行与否不取决于权限档。
func TestVendorWorkspaceWritesAlwaysDenied(t *testing.T) {
	gin.SetMode(gin.TestMode)

	writes := []struct {
		method string
		tmpl   string
	}{
		{http.MethodPut, "/api/v1/admin/workspaces/self/grants/:group_id/cost-rate"},
		{http.MethodPut, "/api/v1/admin/workspaces/:id"},
		{http.MethodPost, "/api/v1/admin/workspaces"},
		{http.MethodPut, "/api/v1/admin/workspaces/:id/grants"},
	}

	for _, tc := range writes {
		t.Run(tc.method+" "+tc.tmpl, func(t *testing.T) {
			var allowed bool
			r := gin.New()
			r.Handle(tc.method, tc.tmpl, func(c *gin.Context) {
				allowed = vendorRouteAllowed(c, VendorScope{
					WorkspaceID: 7,
					// 五档全开：仍不得放行。
					Perms: service.WorkspacePermissions{
						AccountManage: true, GroupOps: true, GroupBilling: true,
						ProxyManage: true, MonitorView: true,
					},
				})
				c.Status(http.StatusOK)
			})

			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(tc.method, tc.tmpl, nil))

			if w.Code != http.StatusOK {
				t.Fatalf("路由未命中（%d）", w.Code)
			}
			if allowed {
				t.Error("工作区写路径不得对 vendor 放行 —— 否则可自行放宽结算区间")
			}
		})
	}
}
