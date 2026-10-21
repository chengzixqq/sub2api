package middleware

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// Push registration bodies carry the device token, the ntfy / webhook secret and webhook URLs that may embed a
// token in their path, which key-level redaction cannot catch; the APNs config body keeps its audit trail with the
// p8 key masked by the "privatekey" key rule.
func TestPushRoutesAuditBodies(t *testing.T) {
	require.Contains(t, auditBodyOmittedRoutes, "POST /api/v1/admin/push/devices")
	require.NotContains(t, auditBodyOmittedRoutes, "PUT /api/v1/admin/push/config")

	redacted := service.RedactAuditBody([]byte(`{"apns_team_id":"TEAM123456","apns_private_key":"-----BEGIN PRIVATE KEY-----abc"}`), "application/json")
	require.Contains(t, redacted, "TEAM123456")
	require.NotContains(t, redacted, "BEGIN PRIVATE KEY")
}
