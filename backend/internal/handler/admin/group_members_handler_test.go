package admin

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestGroupBatchUpdateMembers(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := newStubAdminService()
	r := gin.New()
	r.POST("/groups/:id/accounts/batch", NewGroupHandler(svc, nil, nil).BatchUpdateMembers)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, jsonRequest(http.MethodPost, "/groups/3/accounts/batch", `{"add":[{"account_id":11},{"account_id":12,"priority":2}],"remove":[13],"confirm_mixed_channel_risk":true}`))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, []service.GroupMemberAdd{{AccountID: 11}, {AccountID: 12, Priority: 2}}, svc.lastGroupMembersInput.Add)
	require.Equal(t, []int64{13}, svc.lastGroupMembersInput.Remove)
	require.True(t, svc.lastGroupMembersInput.SkipMixedChannelCheck)
	require.Contains(t, rec.Body.String(), `"group_id":3`)

	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, jsonRequest(http.MethodPost, "/groups/x/accounts/batch", `{}`))
	require.Equal(t, http.StatusBadRequest, rec.Code)

	svc.updateGroupMembersErr = service.ErrGroupNotFound
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, jsonRequest(http.MethodPost, "/groups/3/accounts/batch", `{"add":[{"account_id":11}]}`))
	require.Equal(t, http.StatusNotFound, rec.Code)
}
