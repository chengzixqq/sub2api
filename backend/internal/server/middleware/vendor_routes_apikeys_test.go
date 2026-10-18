package middleware

import "testing"

// TestVendorRoutesDenyAdminAPIKeyManagement locks end-user key management to the station owner: none of these routes
// sit under a vendor prefix today, and this keeps a future broad rule from opening them by accident.
func TestVendorRoutesDenyAdminAPIKeyManagement(t *testing.T) {
	scope := VendorScope{WorkspaceID: 7, Perms: allPermsOn()}

	for _, tc := range []struct{ method, path string }{
		{"GET", "/api/v1/admin/api-keys/:id"},
		{"PUT", "/api/v1/admin/api-keys/:id"},
		{"DELETE", "/api/v1/admin/api-keys/:id"},
		{"GET", "/api/v1/admin/users/:id/api-keys"},
		{"POST", "/api/v1/admin/users/:id/api-keys"},
	} {
		if vendorRouteAllowed(newFullPathContext(tc.method, tc.path), scope) {
			t.Errorf("%s %s manages end-user API keys and must stay closed to vendors", tc.method, tc.path)
		}
	}
}
