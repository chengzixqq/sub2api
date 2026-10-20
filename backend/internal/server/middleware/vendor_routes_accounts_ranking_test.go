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

// TestVendorRoutesDenyAccountTestRuns: async tests take a capped per-instance
// worker and are owner-only for now; the interactive SSE test stays open.
func TestVendorRoutesDenyAccountTestRuns(t *testing.T) {
	scope := VendorScope{WorkspaceID: 7, Perms: allPermsOn()}
	denied := []struct{ method, path string }{
		{"POST", "/api/v1/admin/accounts/:id/test-runs"},
		{"GET", "/api/v1/admin/accounts/:id/test-runs/latest"},
		{"GET", "/api/v1/admin/account-test-runs/:run_id"},
	}
	for _, route := range denied {
		if vendorRouteAllowed(newFullPathContext(route.method, route.path), scope) {
			t.Errorf("vendor must not reach %s %s", route.method, route.path)
		}
	}
	if !vendorRouteAllowed(newFullPathContext("POST", "/api/v1/admin/accounts/:id/test"), scope) {
		t.Fatal("vendor SSE account tests must stay open")
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

// TestVendorRoutesDenyDashboardHome: the home aggregate reads station-wide
// stats, ops, alerts, group availability and channel health, none of which
// can be narrowed to a workspace.
func TestVendorRoutesDenyDashboardHome(t *testing.T) {
	scope := VendorScope{WorkspaceID: 7, Perms: allPermsOn()}
	if vendorRouteAllowed(newFullPathContext("GET", "/api/v1/admin/dashboard/home"), scope) {
		t.Fatal("vendor must not read the station-wide home aggregate")
	}
}

// TestVendorRoutesDenyDashboardRequestTypes: the per-type split returns cost,
// actual_cost and account_cost untrimmed, while vendor dashboards trim amounts
// by monitoring tier. Opening it requires registering the path in both
// vendorAllowedRoutes and vendorUsageScopedRoutes and trimming those amounts.
func TestVendorRoutesDenyDashboardRequestTypes(t *testing.T) {
	scope := VendorScope{WorkspaceID: 7, Perms: allPermsOn()}
	if vendorRouteAllowed(newFullPathContext("GET", "/api/v1/admin/dashboard/request-types"), scope) {
		t.Fatal("vendor must not read the request type split")
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

// TestVendorRoutesDenyAdminAccessTokens: sat_ tokens act as full admins, so
// vendors can neither mint nor list them.
func TestVendorRoutesDenyAdminAccessTokens(t *testing.T) {
	scope := VendorScope{WorkspaceID: 7, Perms: allPermsOn()}
	for _, route := range [][2]string{
		{"GET", "/api/v1/admin/access-tokens"},
		{"POST", "/api/v1/admin/access-tokens"},
		{"DELETE", "/api/v1/admin/access-tokens/:id"},
	} {
		if vendorRouteAllowed(newFullPathContext(route[0], route[1]), scope) {
			t.Fatalf("vendor must not reach %s %s", route[0], route[1])
		}
	}
}

// TestVendorRoutesDenyOpsAlertEventCounts: ops monitoring is site-owner only; the alert counts stay closed to vendors.
func TestVendorRoutesDenyOpsAlertEventCounts(t *testing.T) {
	scope := VendorScope{WorkspaceID: 7, Perms: allPermsOn()}
	if vendorRouteAllowed(newFullPathContext("GET", "/api/v1/admin/ops/alert-events/counts"), scope) {
		t.Fatal("vendor must not read ops alert event counts")
	}
}

// TestVendorRoutesDenyOpsSystemMetricsHistory: server CPU / memory history is site-wide ops data.
func TestVendorRoutesDenyOpsSystemMetricsHistory(t *testing.T) {
	scope := VendorScope{WorkspaceID: 7, Perms: allPermsOn()}
	if vendorRouteAllowed(newFullPathContext("GET", "/api/v1/admin/ops/system-metrics/history"), scope) {
		t.Fatal("vendor must not read ops system metrics history")
	}
}

// TestVendorRoutesDenyOpsMonitoringStatus: the ops switch is site-wide and owner-only.
func TestVendorRoutesDenyOpsMonitoringStatus(t *testing.T) {
	scope := VendorScope{WorkspaceID: 7, Perms: allPermsOn()}
	for _, method := range []string{"GET", "PUT"} {
		if vendorRouteAllowed(newFullPathContext(method, "/api/v1/admin/ops/monitoring-status"), scope) {
			t.Fatalf("vendor must not reach %s ops monitoring-status", method)
		}
	}
}

// TestVendorRoutesDenyOpsRequestAttempts: ops error logs cannot be narrowed to a workspace.
func TestVendorRoutesDenyOpsRequestAttempts(t *testing.T) {
	scope := VendorScope{WorkspaceID: 7, Perms: allPermsOn()}
	if vendorRouteAllowed(newFullPathContext("GET", "/api/v1/admin/ops/requests/:request_id/attempts"), scope) {
		t.Fatal("vendor must not read ops request attempts")
	}
}

// TestVendorRoutesAccountRuntimeBatchFollowsAccountRead: the live-counter batch
// is a read at the same tier as GET /accounts, so either AccountManage or
// MonitorView opens it. It must be listed as a batch route so the broad
// /accounts write prefix (AccountManage) does not decide it.
func TestVendorRoutesAccountRuntimeBatchFollowsAccountRead(t *testing.T) {
	const path = "/api/v1/admin/accounts/runtime/batch"
	for _, perms := range []service.WorkspacePermissions{
		{MonitorView: true},
		{AccountManage: true},
	} {
		scope := VendorScope{WorkspaceID: 7, Perms: perms}
		if !vendorRouteAllowed(newFullPathContext("POST", path), scope) {
			t.Errorf("POST %s should be open with %+v", path, perms)
		}
		if vendorRouteAllowed(newFullPathContext("GET", path), scope) {
			t.Errorf("GET %s must not inherit the batch read exception (%+v)", path, perms)
		}
	}
	for _, perms := range []service.WorkspacePermissions{
		{},
		{ProxyManage: true},
		{GroupOps: true, GroupBilling: true},
	} {
		if vendorRouteAllowed(newFullPathContext("POST", path), VendorScope{WorkspaceID: 7, Perms: perms}) {
			t.Errorf("POST %s must require AccountManage or MonitorView, got allowed with %+v", path, perms)
		}
	}

	excluded := false
	for _, route := range vendorBatchAccountRoutes {
		if route == path {
			excluded = true
		}
	}
	if !excluded {
		t.Fatalf("%s must be registered in vendorBatchAccountRoutes", path)
	}
}

// TestVendorRoutesAllowMetaCatalog: the catalog is compile-time constants that
// already ship in the public Web bundle, so every vendor may read it (the
// handler trims platform_auth by tier). Only the exact GET path opens, and it
// queries no usage rows, so it needs no account allowlist.
func TestVendorRoutesAllowMetaCatalog(t *testing.T) {
	const path = "/api/v1/admin/meta/catalog"
	for _, perms := range []service.WorkspacePermissions{{}, {MonitorView: true}, allPermsOn()} {
		scope := VendorScope{WorkspaceID: 7, Perms: perms}
		if !vendorRouteAllowed(newFullPathContext("GET", path), scope) {
			t.Errorf("GET %s should be open to every vendor (%+v)", path, perms)
		}
		if vendorRouteAllowed(newFullPathContext("POST", path), scope) {
			t.Errorf("POST %s must stay closed (%+v)", path, perms)
		}
	}
	all := VendorScope{WorkspaceID: 7, Perms: allPermsOn()}
	for _, sibling := range []string{"/api/v1/admin/meta", "/api/v1/admin/meta/catalog/extra", "/api/v1/admin/meta/other"} {
		if vendorRouteAllowed(newFullPathContext("GET", sibling), all) {
			t.Errorf("GET %s must not inherit the exact catalog rule", sibling)
		}
	}
	if containsString(vendorUsageScopedRoutes, path) {
		t.Errorf("%s reads no usage rows and must not trigger the account allowlist lookup", path)
	}
}

// TestMetaCatalogVendorOAuthMatchesReachableHelpers keeps the vendor
// platform_auth of GET /meta/catalog in step with the vendor route allowlist:
// a vendor with account management is offered an OAuth-flow type (oauth,
// setup-token) on a platform only when every helper that flow calls is
// reachable. Adding an OAuth platform to the catalog fails here until its
// helpers are listed below.
func TestMetaCatalogVendorOAuthMatchesReachableHelpers(t *testing.T) {
	oauthHelpers := map[string]map[string][]string{
		service.PlatformAnthropic: {
			service.AccountTypeOAuth: {
				"/api/v1/admin/accounts/generate-auth-url",
				"/api/v1/admin/accounts/exchange-code",
				"/api/v1/admin/accounts/cookie-auth",
			},
			service.AccountTypeSetupToken: {
				"/api/v1/admin/accounts/generate-setup-token-url",
				"/api/v1/admin/accounts/exchange-setup-token-code",
				"/api/v1/admin/accounts/setup-token-cookie-auth",
			},
		},
		service.PlatformOpenAI: {
			service.AccountTypeOAuth: {"/api/v1/admin/openai/generate-auth-url", "/api/v1/admin/openai/exchange-code"},
		},
		service.PlatformGemini: {
			service.AccountTypeOAuth: {"/api/v1/admin/gemini/oauth/auth-url", "/api/v1/admin/gemini/oauth/exchange-code"},
		},
		service.PlatformAntigravity: {
			service.AccountTypeOAuth: {"/api/v1/admin/antigravity/oauth/auth-url", "/api/v1/admin/antigravity/oauth/exchange-code"},
		},
		service.PlatformGrok: {
			service.AccountTypeOAuth: {"/api/v1/admin/grok/oauth/auth-url", "/api/v1/admin/grok/oauth/exchange-code"},
		},
	}
	full := service.BuildAdminMetaCatalog(service.AdminScope()).PlatformAuth
	vendorAuth := service.BuildAdminMetaCatalog(service.VendorScope(7, service.WorkspacePermissions{AccountManage: true})).PlatformAuth
	scope := VendorScope{WorkspaceID: 7, Perms: service.WorkspacePermissions{AccountManage: true}}

	for platform, types := range full {
		for _, accountType := range types {
			if accountType != service.AccountTypeOAuth && accountType != service.AccountTypeSetupToken {
				continue
			}
			helpers, ok := oauthHelpers[platform][accountType]
			if !ok {
				t.Fatalf("%s/%s is an OAuth flow without listed helper routes", platform, accountType)
			}
			if _, creatable := vendorAuth[platform]; !creatable {
				// Creation itself is forbidden for vendors (Grok).
				continue
			}
			reachable := true
			for _, path := range helpers {
				if !vendorRouteAllowed(newFullPathContext("POST", path), scope) {
					reachable = false
				}
			}
			offered := containsString(vendorAuth[platform], accountType)
			if offered != reachable {
				t.Errorf("vendor platform_auth[%s] offers %s=%v but its helpers reachable=%v", platform, accountType, offered, reachable)
			}
		}
	}
}
