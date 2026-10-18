package admin

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func putAPIKey(t *testing.T, stub *stubAdminService, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/api-keys/4087", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	setupAPIKeyHandler(stub).ServeHTTP(rec, req)
	return rec
}

func TestAdminAPIKeyHandler_Update_DisableReturnsPrevious(t *testing.T) {
	stub := newStubAdminService()
	stub.apiKeys = []service.APIKey{{ID: 4087, Name: "脚本", Status: "active"}}

	rec := putAPIKey(t, stub, `{"status":"inactive"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"previous":{"status":"active"}`)
	require.Contains(t, rec.Body.String(), `"status":"inactive"`)
}

func TestAdminAPIKeyHandler_Update_ParsesAllFields(t *testing.T) {
	stub := newStubAdminService()
	stub.apiKeys = []service.APIKey{{ID: 4087, Status: "active"}}

	rec := putAPIKey(t, stub, `{"name":"脚本-新","quota":100,"reset_quota":true,"expires_at":"2026-12-31T23:59:59+08:00","ip_whitelist":[],"ip_blacklist":["203.0.113.9"],"rate_limit_5h":0,"rate_limit_1d":30}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	in := stub.lastAPIKeyFieldsInput
	require.NotNil(t, in)
	require.Equal(t, "脚本-新", *in.Name)
	require.Equal(t, 100.0, *in.Quota)
	require.True(t, in.ResetQuota)
	require.True(t, time.Date(2026, 12, 31, 15, 59, 59, 0, time.UTC).Equal(*in.ExpiresAt))
	require.False(t, in.ClearExpiration)
	require.NotNil(t, in.IPWhitelist)
	require.Empty(t, *in.IPWhitelist, "an empty list clears the rule")
	require.Equal(t, []string{"203.0.113.9"}, *in.IPBlacklist)
	require.Equal(t, 0.0, *in.RateLimit5h)
	require.Equal(t, 30.0, *in.RateLimit1d)
	require.Nil(t, in.RateLimit7d)
}

func TestAdminAPIKeyHandler_Update_EmptyExpiresAtClears(t *testing.T) {
	stub := newStubAdminService()
	stub.apiKeys = []service.APIKey{{ID: 4087}}

	require.Equal(t, http.StatusOK, putAPIKey(t, stub, `{"expires_at":""}`).Code)
	require.True(t, stub.lastAPIKeyFieldsInput.ClearExpiration)
	require.Nil(t, stub.lastAPIKeyFieldsInput.ExpiresAt)

	require.Equal(t, http.StatusBadRequest, putAPIKey(t, stub, `{"expires_at":"next week"}`).Code)
}

// The Web only sends group_id: the field path is not touched and the response keeps its shape (no "previous").
func TestAdminAPIKeyHandler_Update_GroupOnlyKeepsLegacyResponse(t *testing.T) {
	stub := newStubAdminService()
	stub.apiKeys = []service.APIKey{{ID: 4087, Status: "active"}}

	rec := putAPIKey(t, stub, `{"group_id":3}`)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Nil(t, stub.lastAPIKeyFieldsInput)
	require.NotContains(t, rec.Body.String(), `"previous"`)
	require.Contains(t, rec.Body.String(), `"group_id":3`)
}
