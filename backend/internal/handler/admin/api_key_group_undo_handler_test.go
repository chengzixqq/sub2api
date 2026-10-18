package admin

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestAPIKeyUpdate_RevokeGroupAccess(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := newStubAdminService()
	svc.apiKeys = []service.APIKey{{ID: 4087, UserID: 1, Key: "sk-x", Name: "k"}}
	r := gin.New()
	r.PUT("/api-keys/:id", NewAdminAPIKeyHandler(svc, nil).UpdateGroup)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, jsonRequest(http.MethodPut, "/api-keys/4087", `{"group_id":2,"revoke_group_access_id":10}`))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, int64(10), svc.lastRevokeGroupAccessID)
	require.Contains(t, rec.Body.String(), `"revoked_group_access":true`)
	require.Contains(t, rec.Body.String(), `"revoked_group_id":10`)

	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, jsonRequest(http.MethodPut, "/api-keys/4087", `{"group_id":2,"revoke_group_access_id":0}`))
	require.Equal(t, http.StatusBadRequest, rec.Code)

	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, jsonRequest(http.MethodPut, "/api-keys/4087", `{"group_id":2}`))
	require.Equal(t, http.StatusOK, rec.Code)
	require.NotContains(t, rec.Body.String(), "revoked_group_access", "unchanged response without the new field")
}

func TestReplaceGroup_ReturnsMigratedKeyIDs(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := newStubAdminService()
	r := gin.New()
	r.POST("/users/:id/replace-group", NewUserHandler(svc, nil, nil, nil, nil, nil, nil).ReplaceGroup)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, jsonRequest(http.MethodPost, "/users/1/replace-group", `{"old_group_id":12,"new_group_id":10,"key_ids":[4087,4099],"keep_old_group_access":true}`))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, []int64{4087, 4099}, svc.lastReplaceOptions.KeyIDs)
	require.True(t, svc.lastReplaceOptions.KeepOldGroupAccess)
	require.Contains(t, rec.Body.String(), `"migrated_keys":2`)
	require.Contains(t, rec.Body.String(), `"migrated_key_ids":[4087,4099]`)
	require.Contains(t, rec.Body.String(), `"new_group_newly_granted":true`)
}
