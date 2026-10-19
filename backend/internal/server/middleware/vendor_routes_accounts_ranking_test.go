package middleware

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// TestVendorRoutesDenyAccountsRanking keeps the station-wide account spending
// ranking away from vendors. Opening it requires registering the path in both
// vendorAllowedRoutes and vendorUsageScopedRoutes.
func TestVendorRoutesDenyAccountsRanking(t *testing.T) {
	scope := VendorScope{WorkspaceID: 7, Perms: allPermsOn()}
	if vendorRouteAllowed(newFullPathContext("GET", "/api/v1/admin/dashboard/accounts-ranking"), scope) {
		t.Fatal("vendor must not read the account spending ranking")
	}
}

// TestVendorRoutesDenyAccountStatusSummary: the summary names every group and
// reads ops alert thresholds, neither of which is workspace-scoped.
func TestVendorRoutesDenyAccountStatusSummary(t *testing.T) {
	scope := VendorScope{WorkspaceID: 7, Perms: allPermsOn()}
	if vendorRouteAllowed(newFullPathContext("GET", "/api/v1/admin/accounts/status-summary"), scope) {
		t.Fatal("vendor must not read the account status summary")
	}
	if !vendorRouteAllowed(newFullPathContext("GET", "/api/v1/admin/accounts/:id"), scope) {
		t.Fatal("vendor account reads must stay open")
	}
}

// TestVendorRoutesDenyDashboardRates: the ops where clauses carry no account
// scope, so the rates would be station-wide.
func TestVendorRoutesDenyDashboardRates(t *testing.T) {
	scope := VendorScope{WorkspaceID: 7, Perms: allPermsOn()}
	if vendorRouteAllowed(newFullPathContext("GET", "/api/v1/admin/dashboard/rates"), scope) {
		t.Fatal("vendor must not read station-wide dashboard rates")
	}
}

// TestVendorRoutesDenyGroupMemberBatch: membership batch writes are POST under
// /admin/groups, which the vendor whitelist only opens for GET / PUT / PATCH.
func TestVendorRoutesDenyGroupMemberBatch(t *testing.T) {
	scope := VendorScope{WorkspaceID: 7, Perms: allPermsOn()}
	if vendorRouteAllowed(newFullPathContext("POST", "/api/v1/admin/groups/:id/accounts/batch"), scope) {
		t.Fatal("vendor must not batch-edit group members")
	}
}

// TestVendorRoutesDenyGroupStats: key and subscription counts are end-user data.
func TestVendorRoutesDenyGroupStats(t *testing.T) {
	scope := VendorScope{WorkspaceID: 7, Perms: allPermsOn()}
	if vendorRouteAllowed(newFullPathContext("GET", "/api/v1/admin/groups/:id/stats"), scope) {
		t.Fatal("vendor must not read group stats")
	}
}

// TestVendorRoutesProxyFallbackRevertNeedsAccountManage: restoring rewrites
// account proxy_id, so ProxyManage alone is not enough.
func TestVendorRoutesProxyFallbackRevertNeedsAccountManage(t *testing.T) {
	path := "/api/v1/admin/proxies/:id/fallback-accounts/revert"
	proxyOnly := VendorScope{WorkspaceID: 7, Perms: service.WorkspacePermissions{ProxyManage: true}}
	if vendorRouteAllowed(newFullPathContext("POST", path), proxyOnly) {
		t.Fatal("ProxyManage alone must not restore account proxies")
	}
	both := VendorScope{WorkspaceID: 7, Perms: service.WorkspacePermissions{ProxyManage: true, AccountManage: true}}
	if !vendorRouteAllowed(newFullPathContext("POST", path), both) {
		t.Fatal("ProxyManage + AccountManage may restore")
	}
	if !vendorRouteAllowed(newFullPathContext("GET", "/api/v1/admin/proxies/:id/fallback-accounts"), proxyOnly) {
		t.Fatal("reading the fallback list follows proxy read")
	}
}

func TestVendorRoutesAllowAdminMe(t *testing.T) {
	scope := VendorScope{WorkspaceID: 7, Perms: service.WorkspacePermissions{}}
	if !vendorRouteAllowed(newFullPathContext("GET", "/api/v1/admin/me"), scope) {
		t.Fatal("every vendor may read /admin/me")
	}
}
