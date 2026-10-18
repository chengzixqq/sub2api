package middleware

import "testing"

// TestVendorRoutesDenyGroupSubscriptions locks subscription lists away from vendors.
//
// Subscriptions are station-owner customer data (see the vendorAllowedRoutes comment). The group subscription list sits
// under the /admin/groups GET prefix that vendors may read, so prefix matching would expose every group's subscribers —
// including groups never granted to the vendor. The per-user list is outside every vendor prefix and must stay denied.
func TestVendorRoutesDenyGroupSubscriptions(t *testing.T) {
	scope := VendorScope{WorkspaceID: 7, Perms: allPermsOn()}

	for _, path := range []string{
		"/api/v1/admin/groups/:id/subscriptions",
		"/api/v1/admin/users/:id/subscriptions",
	} {
		if vendorRouteAllowed(newFullPathContext("GET", path), scope) {
			t.Errorf("GET %s lists end-user subscriptions and must stay closed to vendors", path)
		}
	}

	// The group itself stays readable for vendors with a group tier.
	if !vendorRouteAllowed(newFullPathContext("GET", "/api/v1/admin/groups/:id"), scope) {
		t.Error("GET /api/v1/admin/groups/:id must stay reachable for vendors")
	}
}
