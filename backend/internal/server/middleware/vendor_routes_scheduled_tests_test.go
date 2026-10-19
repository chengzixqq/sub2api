package middleware

import "testing"

// TestVendorRoutesScheduledTestPlans 锁住定时测试计划的路由层现状。
//
// 按账号列计划经 /admin/accounts 的 GET 前缀对 vendor 放行，归属由
// ScheduledTestHandler 经 GetAccount 校验；/scheduled-test-plans 下的
// 新建、修改、删除与结果列表不在白名单里。放开它们之前，handler 的
// 归属校验已就位，但仍需在 vendorAllowedRoutes 显式登记。
func TestVendorRoutesScheduledTestPlans(t *testing.T) {
	scope := VendorScope{WorkspaceID: 7, Perms: allPermsOn()}

	if !vendorRouteAllowed(newFullPathContext("GET", "/api/v1/admin/accounts/:id/scheduled-test-plans"), scope) {
		t.Fatal("vendor listing plans of its own accounts must stay reachable")
	}

	denied := []struct {
		method string
		path   string
	}{
		{"POST", "/api/v1/admin/scheduled-test-plans"},
		{"PUT", "/api/v1/admin/scheduled-test-plans/:id"},
		{"DELETE", "/api/v1/admin/scheduled-test-plans/:id"},
		{"GET", "/api/v1/admin/scheduled-test-plans/:id/results"},
	}
	for _, tc := range denied {
		if vendorRouteAllowed(newFullPathContext(tc.method, tc.path), scope) {
			t.Errorf("%s %s must stay closed to vendors", tc.method, tc.path)
		}
	}
}
