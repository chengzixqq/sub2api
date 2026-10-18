package middleware

import "testing"

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
