package middleware

import "testing"

// TestVendorRoutesDenyUsageDirectoryEndpoints locks the station-wide lookups under /admin/usage away from vendors.
//
// The /admin/usage GET prefix is open to vendors with the monitor tier so they can read their own usage rows, but
// search-users and search-api-keys query the whole site (every user's email, every key's name) with no workspace
// predicate, and cleanup tasks are station-owner maintenance. Prefix matching would otherwise expose all three.
func TestVendorRoutesDenyUsageDirectoryEndpoints(t *testing.T) {
	scope := VendorScope{WorkspaceID: 7, Perms: allPermsOn()}

	denied := []struct {
		method string
		path   string
	}{
		{"GET", "/api/v1/admin/usage/search-users"},
		{"GET", "/api/v1/admin/usage/search-api-keys"},
		{"GET", "/api/v1/admin/usage/cleanup-tasks"},
		{"POST", "/api/v1/admin/usage/cleanup-tasks"},
		{"POST", "/api/v1/admin/usage/cleanup-tasks/:id/cancel"},
	}
	for _, tc := range denied {
		if vendorRouteAllowed(newFullPathContext(tc.method, tc.path), scope) {
			t.Errorf("%s %s queries site-wide data and must stay closed to vendors", tc.method, tc.path)
		}
	}

	// The usage list and stats stay reachable: they are narrowed by the injected account whitelist.
	for _, path := range []string{"/api/v1/admin/usage", "/api/v1/admin/usage/stats"} {
		if !vendorRouteAllowed(newFullPathContext("GET", path), scope) {
			t.Errorf("GET %s must stay reachable for vendors with the monitor tier", path)
		}
	}
}
