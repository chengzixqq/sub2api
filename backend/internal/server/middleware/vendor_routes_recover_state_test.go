package middleware

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// TestVendorRecoverStateFollowsAccountManage 锁住 recover-state 与调度开关
// 对 vendor 的放行条件一致。
//
// recover-state 可带 {"schedulable":...} 在同一请求里设置调度开关，
// 两条路由必须同受 AccountManage 约束，否则合并请求会变成绕过权限的捷径。
func TestVendorRecoverStateFollowsAccountManage(t *testing.T) {
	allowed := VendorScope{WorkspaceID: 7, Perms: allPermsOn()}
	noManagePerms := allPermsOn()
	noManagePerms.AccountManage = false
	denied := VendorScope{WorkspaceID: 7, Perms: noManagePerms}

	for _, path := range []string{
		"/api/v1/admin/accounts/:id/recover-state",
		"/api/v1/admin/accounts/:id/schedulable",
	} {
		if !vendorRouteAllowed(newFullPathContext("POST", path), allowed) {
			t.Errorf("POST %s must be reachable with AccountManage", path)
		}
		if vendorRouteAllowed(newFullPathContext("POST", path), denied) {
			t.Errorf("POST %s must be denied without AccountManage", path)
		}
	}
}

// TestVendorRoutesReauthorizeNeedsAccountManage: reauthorize writes new
// credentials like apply-oauth-credentials, so it is open to vendors only with
// AccountManage (the handler additionally pins the account's own proxy).
func TestVendorRoutesReauthorizeNeedsAccountManage(t *testing.T) {
	path := "/api/v1/admin/accounts/:id/reauthorize"
	if !vendorRouteAllowed(newFullPathContext("POST", path), VendorScope{WorkspaceID: 7, Perms: allPermsOn()}) {
		t.Fatal("vendor with AccountManage may reauthorize its accounts")
	}
	monitorOnly := VendorScope{WorkspaceID: 7, Perms: service.WorkspacePermissions{MonitorView: true}}
	if vendorRouteAllowed(newFullPathContext("POST", path), monitorOnly) {
		t.Fatal("MonitorView alone must not reauthorize accounts")
	}
}
