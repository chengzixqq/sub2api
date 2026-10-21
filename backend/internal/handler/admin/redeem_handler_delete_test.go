package admin

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func newRedeemDeleteRouter(adminSvc *stubAdminService) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	handler := NewRedeemHandler(adminSvc, nil)
	router.DELETE("/api/v1/admin/redeem-codes/:id", handler.Delete)
	router.POST("/api/v1/admin/redeem-codes/batch-delete", handler.BatchDelete)
	return router
}

func serveRedeemDelete(t *testing.T, router *gin.Engine, method, path, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	var out map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out), rec.Body.String())
	return rec.Code, out
}

func TestRedeemHandler_Delete_UsedAndMissing(t *testing.T) {
	cases := []struct {
		name    string
		err     error
		status  int
		reason  string
		message string
	}{
		{"used code", service.ErrRedeemCodeDeleteUsed, http.StatusConflict, "REDEEM_CODE_USED", "used redeem codes cannot be deleted"},
		{"missing code", service.ErrRedeemCodeNotFound, http.StatusNotFound, "REDEEM_CODE_NOT_FOUND", "redeem code not found"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			adminSvc := newStubAdminService()
			adminSvc.redeemDeleteErr = tc.err

			status, body := serveRedeemDelete(t, newRedeemDeleteRouter(adminSvc), http.MethodDelete, "/api/v1/admin/redeem-codes/88", "")
			require.Equal(t, tc.status, status)
			require.Equal(t, tc.reason, body["reason"])
			require.Equal(t, tc.message, body["message"])
		})
	}

	status, body := serveRedeemDelete(t, newRedeemDeleteRouter(newStubAdminService()), http.MethodDelete, "/api/v1/admin/redeem-codes/89", "")
	require.Equal(t, http.StatusOK, status)
	data, ok := body["data"].(map[string]any)
	require.True(t, ok, body)
	require.Equal(t, "Redeem code deleted successfully", data["message"])
}

func TestRedeemHandler_BatchDelete_ReportsSkipped(t *testing.T) {
	adminSvc := newStubAdminService()
	adminSvc.redeemBatchDeleteResult = &service.RedeemCodeBatchDeleteResult{Deleted: 1, SkippedIDs: []int64{88}}

	status, body := serveRedeemDelete(t, newRedeemDeleteRouter(adminSvc), http.MethodPost, "/api/v1/admin/redeem-codes/batch-delete", `{"ids":[88,89]}`)
	require.Equal(t, http.StatusOK, status)
	data, ok := body["data"].(map[string]any)
	require.True(t, ok, body)
	require.EqualValues(t, 1, data["deleted"])
	require.EqualValues(t, 1, data["skipped_used"])
	require.Equal(t, []any{float64(88)}, data["skipped_ids"])
	require.Equal(t, "Redeem codes deleted successfully", data["message"])
}

func TestRedeemHandler_BatchDelete_NothingSkippedIsEmptyList(t *testing.T) {
	status, body := serveRedeemDelete(t, newRedeemDeleteRouter(newStubAdminService()), http.MethodPost, "/api/v1/admin/redeem-codes/batch-delete", `{"ids":[1,2]}`)
	require.Equal(t, http.StatusOK, status)
	data, ok := body["data"].(map[string]any)
	require.True(t, ok, body)
	require.EqualValues(t, 2, data["deleted"])
	require.EqualValues(t, 0, data["skipped_used"])
	require.Equal(t, []any{}, data["skipped_ids"], "[] rather than null")
}

func TestRedeemHandler_BatchDelete_Error(t *testing.T) {
	adminSvc := newStubAdminService()
	adminSvc.redeemBatchDeleteErr = errors.New("db down")

	status, body := serveRedeemDelete(t, newRedeemDeleteRouter(adminSvc), http.MethodPost, "/api/v1/admin/redeem-codes/batch-delete", `{"ids":[1]}`)
	require.Equal(t, http.StatusInternalServerError, status)
	require.NotContains(t, body, "data")
}
