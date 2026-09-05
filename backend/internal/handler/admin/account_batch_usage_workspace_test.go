package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type batchUsageScopeAdminStub struct {
	*stubAdminService
	requested []int64
}

func (s *batchUsageScopeAdminStub) FilterAccountIDsByScope(_ context.Context, ids []int64) ([]int64, error) {
	s.requested = append([]int64(nil), ids...)
	return []int64{}, nil
}

func TestGetBatchUsageScopesIDsBeforeForceProbe(t *testing.T) {
	gin.SetMode(gin.TestMode)
	adminService := &batchUsageScopeAdminStub{stubAdminService: &stubAdminService{}}
	handler := &AccountHandler{adminService: adminService}

	router := gin.New()
	router.POST("/api/v1/admin/accounts/usage/batch", handler.GetBatchUsage)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/admin/accounts/usage/batch",
		strings.NewReader(`{"account_ids":[9,9,3],"force":true}`),
	)
	request.Header.Set("Content-Type", "application/json")

	router.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, []int64{3, 9}, adminService.requested)
	require.Contains(t, recorder.Body.String(), `"usage":{}`)
	require.Contains(t, recorder.Body.String(), `"errors":{}`)
}
