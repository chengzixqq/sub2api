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

type createAccountCapturingAdmin struct {
	*stubAdminService
	inputs []*service.CreateAccountInput
}

func (s *createAccountCapturingAdmin) CreateAccount(_ context.Context, in *service.CreateAccountInput) (*service.Account, error) {
	s.inputs = append(s.inputs, in)
	schedulable := in.Schedulable == nil || *in.Schedulable
	return &service.Account{ID: int64(20 + len(s.inputs)), Name: in.Name, Platform: in.Platform, Type: in.Type, Status: service.StatusActive, Schedulable: schedulable}, nil
}

func postAccounts(t *testing.T, admin service.AdminService, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	h := NewAccountHandler(admin, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	r := gin.New()
	r.POST("/api/v1/admin/accounts", h.Create)
	r.POST("/api/v1/admin/accounts/batch", h.BatchCreate)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func TestAccountHandler_Create_PassesSchedulableAndSkipDefaultGroup(t *testing.T) {
	admin := &createAccountCapturingAdmin{stubAdminService: newStubAdminService()}

	rec := postAccounts(t, admin, "/api/v1/admin/accounts", `{"name":"Claude-新","platform":"anthropic","type":"apikey","credentials":{"api_key":"sk-ant-x"},"group_ids":[],"schedulable":false,"skip_default_group_bind":true}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	in := admin.inputs[0]
	require.NotNil(t, in.Schedulable)
	require.False(t, *in.Schedulable)
	require.True(t, in.SkipDefaultGroupBind)
	require.Contains(t, rec.Body.String(), `"schedulable":false`)

	rec = postAccounts(t, admin, "/api/v1/admin/accounts", `{"name":"Web","platform":"anthropic","type":"apikey","credentials":{"api_key":"sk"}}`)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Nil(t, admin.inputs[1].Schedulable, "the Web request is unchanged")
	require.False(t, admin.inputs[1].SkipDefaultGroupBind)
}

func TestAccountHandler_BatchCreate_PassesSchedulableAndSkipDefaultGroup(t *testing.T) {
	admin := &createAccountCapturingAdmin{stubAdminService: newStubAdminService()}

	rec := postAccounts(t, admin, "/api/v1/admin/accounts/batch", `{"accounts":[{"name":"a","platform":"anthropic","type":"apikey","credentials":{"api_key":"sk"},"schedulable":false,"skip_default_group_bind":true}]}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.False(t, *admin.inputs[0].Schedulable)
	require.True(t, admin.inputs[0].SkipDefaultGroupBind)
}
