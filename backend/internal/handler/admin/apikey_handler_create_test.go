package admin

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type fakeAPIKeyCreator struct {
	userID int64
	req    service.CreateAPIKeyRequest
	err    error
}

func (f *fakeAPIKeyCreator) AdminCreate(_ context.Context, userID int64, req service.CreateAPIKeyRequest) (*service.APIKey, error) {
	f.userID, f.req = userID, req
	if f.err != nil {
		return nil, f.err
	}
	return &service.APIKey{ID: 4099, UserID: userID, Key: "sk-full-plaintext", Name: req.Name, Status: "active"}, nil
}

type groupBindFailingAdmin struct {
	*stubAdminService
	deleted []int64
}

func (s *groupBindFailingAdmin) AdminUpdateAPIKeyGroupID(context.Context, int64, *int64) (*service.AdminUpdateAPIKeyGroupIDResult, error) {
	return nil, service.ErrGroupNotAllowed
}

func (s *groupBindFailingAdmin) AdminDeleteAPIKey(_ context.Context, keyID int64) error {
	s.deleted = append(s.deleted, keyID)
	return nil
}

func postCreateKey(t *testing.T, admin service.AdminService, creator adminAPIKeyCreator, body string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	h := &AdminAPIKeyHandler{adminService: admin, creator: creator}
	r := gin.New()
	r.POST("/api/v1/admin/users/:id/api-keys", h.CreateForUser)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/users/1/api-keys", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(rec, req)
	return rec
}

func TestAdminAPIKeyHandler_CreateForUser_BindsGroupWithAdminRules(t *testing.T) {
	stub := newStubAdminService()
	stub.apiKeys = []service.APIKey{{ID: 4099, UserID: 1, Key: "sk-full-plaintext", Name: "脚本"}}
	creator := &fakeAPIKeyCreator{}

	rec := postCreateKey(t, stub, creator, `{"name":"脚本","group_id":3,"quota":50,"expires_in_days":30,"ip_blacklist":["203.0.113.9"],"rate_limit_1d":20}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, int64(1), creator.userID)
	require.Equal(t, "脚本", creator.req.Name)
	require.Nil(t, creator.req.GroupID, "the key is created without a group, then bound through the admin group rules")
	require.Equal(t, 50.0, creator.req.Quota)
	require.Equal(t, 30, *creator.req.ExpiresInDays)
	require.Contains(t, rec.Body.String(), `"key":"sk-full-plaintext"`, "the full key is returned once, on creation")
	require.Contains(t, rec.Body.String(), `"group_id":3`)
}

func TestAdminAPIKeyHandler_CreateForUser_GroupFailureRollsBackKey(t *testing.T) {
	admin := &groupBindFailingAdmin{stubAdminService: newStubAdminService()}

	rec := postCreateKey(t, admin, &fakeAPIKeyCreator{}, `{"name":"脚本","group_id":3}`)
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Equal(t, []int64{4099}, admin.deleted, "a key whose group cannot be bound is deleted again")
}

func TestAdminAPIKeyHandler_CreateForUser_Validation(t *testing.T) {
	stub := newStubAdminService()
	require.Equal(t, http.StatusBadRequest, postCreateKey(t, stub, &fakeAPIKeyCreator{}, `{"quota":5}`).Code, "name is required")
	require.Equal(t, http.StatusBadRequest, postCreateKey(t, stub, &fakeAPIKeyCreator{}, `{"name":"x","group_id":-1}`).Code)
	require.Equal(t, http.StatusServiceUnavailable, postCreateKey(t, stub, nil, `{"name":"x"}`).Code)

	creator := &fakeAPIKeyCreator{err: service.ErrAPIKeyCountExceeded}
	require.Equal(t, http.StatusForbidden, postCreateKey(t, stub, creator, `{"name":"x"}`).Code)
}
