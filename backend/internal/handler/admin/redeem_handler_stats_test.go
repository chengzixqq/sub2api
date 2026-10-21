package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func serveRedeemStats(t *testing.T, stub *stubAdminService, query string) (int, map[string]any) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	h := NewRedeemHandler(stub, nil)
	r := gin.New()
	r.GET("/api/v1/admin/redeem-codes/stats", h.GetStats)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/redeem-codes/stats"+query, nil))
	var out map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out), rec.Body.String())
	return rec.Code, out
}

func TestRedeemHandler_Stats_ReturnsRealCountsAndPassesFilter(t *testing.T) {
	stub := newStubAdminService()
	stub.redeemStats = &service.RedeemCodeStats{
		Total: 10, Unused: 4, Used: 3, Expired: 2, Disabled: 1,
		ByType: map[string]int64{"balance": 10}, ValueDistributed: 12.5,
	}

	status, body := serveRedeemStats(t, stub, "?types=balance,concurrency&search=abc&batch_id=2f6c9a0e-8f1b-4c43-9d0e-6a1b2c3d4e5f&status=used")

	require.Equal(t, http.StatusOK, status, body)
	require.Equal(t, 1, stub.redeemStatsCalls)
	f := stub.lastRedeemStatsFilter
	require.Equal(t, []string{"balance", "concurrency"}, f.Types)
	require.Equal(t, "abc", f.Search)
	require.NotNil(t, f.BatchID)
	require.Equal(t, "2f6c9a0e-8f1b-4c43-9d0e-6a1b2c3d4e5f", f.BatchID.String())
	require.Empty(t, f.Status, "status is ignored: the stats are the status breakdown")

	data := body["data"].(map[string]any)
	require.EqualValues(t, 10, data["total_codes"])
	require.EqualValues(t, 4, data["active_codes"])
	require.EqualValues(t, 3, data["used_codes"])
	require.EqualValues(t, 2, data["expired_codes"])
	require.EqualValues(t, 12.5, data["total_value_distributed"])
	require.Equal(t, map[string]any{"unused": 4.0, "used": 3.0, "expired": 2.0, "disabled": 1.0}, data["by_status"])
	require.Equal(t, map[string]any{"balance": 10.0, "trial": 0.0}, data["by_type"], "trial stays for old clients")
}

func TestRedeemHandler_Stats_EmptyStatsKeepShape(t *testing.T) {
	stub := newStubAdminService()
	stub.redeemStats = &service.RedeemCodeStats{}

	status, body := serveRedeemStats(t, stub, "")

	require.Equal(t, http.StatusOK, status, body)
	data := body["data"].(map[string]any)
	require.EqualValues(t, 0, data["total_codes"])
	require.EqualValues(t, 0, data["total_value_distributed"])
	require.Equal(t, map[string]any{"unused": 0.0, "used": 0.0, "expired": 0.0, "disabled": 0.0}, data["by_status"])
	require.Equal(t, map[string]any{"trial": 0.0}, data["by_type"])
}

func TestRedeemHandler_Stats_RejectsBadFilter(t *testing.T) {
	cases := map[string]string{
		"?types=foo":                      "REDEEM_CODE_TYPE_INVALID",
		"?type=balance&types=concurrency": "REDEEM_CODE_TYPE_CONFLICT",
		"?batch_id=nope":                  "REDEEM_BATCH_ID_INVALID",
		"?created_by=-1":                  "INVALID_CREATED_BY",
		"?created_from=yesterday":         "INVALID_TIME_RANGE",
	}
	for query, reason := range cases {
		t.Run(query, func(t *testing.T) {
			stub := newStubAdminService()

			status, body := serveRedeemStats(t, stub, query)

			require.Equal(t, http.StatusBadRequest, status)
			require.Equal(t, reason, body["reason"])
			require.Zero(t, stub.redeemStatsCalls, "a bad filter never reaches the service")
		})
	}
}

func TestRedeemHandler_Stats_ServiceError(t *testing.T) {
	stub := newStubAdminService()
	stub.redeemStatsErr = service.ErrServiceUnavailable

	status, body := serveRedeemStats(t, stub, "")

	require.Equal(t, http.StatusServiceUnavailable, status)
	require.Equal(t, "SERVICE_UNAVAILABLE", body["reason"])
}
