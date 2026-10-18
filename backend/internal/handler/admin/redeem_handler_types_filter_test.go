package admin

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func listRedeemCodes(t *testing.T, stub *stubAdminService, query string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	h := NewRedeemHandler(stub, nil)
	r := gin.New()
	r.GET("/api/v1/admin/redeem-codes", h.List)
	r.GET("/api/v1/admin/redeem-codes/export", h.Export)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/redeem-codes"+query, nil))
	return rec
}

func TestRedeemHandler_List_TypesAndExcludeTypes(t *testing.T) {
	stub := newStubAdminService()

	rec := listRedeemCodes(t, stub, "?types=balance,%20concurrency,balance,subscription,invitation&status=used")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, []string{"balance", "concurrency", "subscription", "invitation"}, stub.lastListRedeemCodes.filter.Types, "trimmed and de-duplicated")
	require.Equal(t, "used", stub.lastListRedeemCodes.filter.Status)

	rec = listRedeemCodes(t, stub, "?exclude_types=admin_balance,admin_concurrency")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, []string{"admin_balance", "admin_concurrency"}, stub.lastListRedeemCodes.filter.ExcludeTypes)

	rec = listRedeemCodes(t, stub, "")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Empty(t, stub.lastListRedeemCodes.filter.Types)
	require.Empty(t, stub.lastListRedeemCodes.filter.ExcludeTypes)
}

func TestRedeemHandler_List_RejectsBadTypes(t *testing.T) {
	stub := newStubAdminService()

	rec := listRedeemCodes(t, stub, "?types=foo")
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "REDEEM_CODE_TYPE_INVALID")

	rec = listRedeemCodes(t, stub, "?type=balance&types=concurrency")
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "REDEEM_CODE_TYPE_CONFLICT")

	rec = listRedeemCodes(t, stub, "/export?exclude_types=nope")
	require.Equal(t, http.StatusBadRequest, rec.Code, "export uses the same filter")
}
