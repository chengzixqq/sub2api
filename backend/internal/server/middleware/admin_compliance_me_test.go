package middleware

import "testing"

func TestAdminComplianceBypassesAdminMe(t *testing.T) {
	if !isAdminComplianceBypassPath("/api/v1/admin/me") {
		t.Fatal("/admin/me reports compliance and must not be blocked by it")
	}
	if isAdminComplianceBypassPath("/api/v1/admin/me/extra") || isAdminComplianceBypassPath("/api/v1/admin/users") {
		t.Fatal("only the exact /admin/me path is exempt")
	}
}
