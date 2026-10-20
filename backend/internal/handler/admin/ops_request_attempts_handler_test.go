package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type requestAttemptsCaptureRepo struct {
	service.OpsRepository
	errLookups []service.OpsRequestAttemptLookup
	errRows    []*service.OpsRequestAttemptErrorRow
	usageRows  []*service.OpsRequestAttemptUsageRow
}

func (r *requestAttemptsCaptureRepo) ListRequestAttemptErrorLogs(_ context.Context, lookup service.OpsRequestAttemptLookup) ([]*service.OpsRequestAttemptErrorRow, error) {
	r.errLookups = append(r.errLookups, lookup)
	return r.errRows, nil
}

func (r *requestAttemptsCaptureRepo) ListRequestAttemptUsageLogs(context.Context, service.OpsRequestAttemptLookup) ([]*service.OpsRequestAttemptUsageRow, error) {
	return r.usageRows, nil
}

func (r *requestAttemptsCaptureRepo) ListRequestAttemptAccountNames(_ context.Context, ids []int64) (map[int64]string, error) {
	names := make(map[int64]string, len(ids))
	for _, id := range ids {
		names[id] = "acct-" + strings.Repeat("x", int(id))
	}
	return names, nil
}

func TestOpsHandlerGetRequestAttempts(t *testing.T) {
	usageAt := time.Date(2026, 10, 18, 1, 41, 10, 0, time.UTC)
	repo := &requestAttemptsCaptureRepo{
		errRows: []*service.OpsRequestAttemptErrorRow{{
			ID: 8812, CreatedAt: usageAt.Add(time.Second), RequestID: "req_5f3a", ClientRequestID: "cid-1", StatusCode: 200,
			UpstreamErrorsJSON: `[{"at_unix_ms":1760751660400,"started_at_unix_ms":1760751660000,"duration_ms":400,"account_id":3,"upstream_status_code":429,"kind":"failover"}]`,
		}},
		usageRows: []*service.OpsRequestAttemptUsageRow{{ID: 9018, CreatedAt: usageAt, RequestID: "client:cid-1", AccountID: 1, AccountName: "Claude-01", DurationMs: ptrInt64(9800)}},
	}
	gin.SetMode(gin.TestMode)
	h := NewOpsHandler(service.NewOpsService(repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil))
	r := gin.New()
	r.GET("/ops/requests/:request_id/attempts", h.GetRequestAttempts)

	rec := serveOps(r, http.MethodGet, "/ops/requests/req_5f3a/attempts", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var resp struct {
		Data struct {
			RequestID        string `json:"request_id"`
			ClientRequestID  string `json:"client_request_id"`
			ClientStatusCode *int   `json:"client_status_code"`
			ErrorLogIDs      []int64
			Attempts         []map[string]any `json:"attempts"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Equal(t, "req_5f3a", resp.Data.RequestID)
	require.Equal(t, "cid-1", resp.Data.ClientRequestID)
	require.Equal(t, 200, *resp.Data.ClientStatusCode)
	require.Len(t, resp.Data.Attempts, 2)
	first, second := resp.Data.Attempts[0], resp.Data.Attempts[1]
	require.EqualValues(t, 1, first["index"])
	require.Equal(t, "failed", first["outcome"])
	require.EqualValues(t, 400, first["duration_ms"])
	require.Equal(t, "2025-10-18T01:41:00Z", first["started_at"])
	require.Equal(t, "acct-xxx", first["account_name"], "failover events are named from accounts")
	require.Nil(t, first["usage_id"])
	require.EqualValues(t, 8812, first["error_log_id"])
	require.EqualValues(t, 2, second["index"])
	require.Equal(t, "success", second["outcome"])
	require.EqualValues(t, 9018, second["usage_id"])
	require.EqualValues(t, 200, second["upstream_status_code"])
	require.Contains(t, rec.Body.String(), `"error_log_ids":[8812]`)
	require.Nil(t, repo.errLookups[0].StartTime, "no around means no time bound")

	rec = serveOps(r, http.MethodGet, "/ops/requests/client:cid-1/attempts?around=2026-10-18T01:41:10Z", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	last := repo.errLookups[len(repo.errLookups)-1]
	require.Equal(t, []string{"cid-1"}, last.ClientRequestIDs)
	require.True(t, last.StartTime.Equal(usageAt.Add(-time.Hour)))
	require.True(t, last.EndTime.Equal(usageAt.Add(time.Hour)))

	calls := len(repo.errLookups)
	for _, tc := range []struct {
		path   string
		status int
		reason string
	}{
		{"/ops/requests/req_5f3a/attempts?around=abc", http.StatusBadRequest, "INVALID_AROUND"},
		{"/ops/requests/req_5f3a/attempts?around=2026-10-18", http.StatusBadRequest, "INVALID_AROUND"},
		{"/ops/requests/" + strings.Repeat("a", 257) + "/attempts", http.StatusBadRequest, "INVALID_REQUEST_ID"},
		{"/ops/requests/%20/attempts", http.StatusBadRequest, "INVALID_REQUEST_ID"},
		{"/ops/requests/%00/attempts", http.StatusBadRequest, "INVALID_REQUEST_ID"},
		{"/ops/requests/req%FF/attempts", http.StatusBadRequest, "INVALID_REQUEST_ID"},
	} {
		rec = serveOps(r, http.MethodGet, tc.path, nil)
		require.Equal(t, tc.status, rec.Code, tc.path)
		var body struct {
			Reason string `json:"reason"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
		require.Equal(t, tc.reason, body.Reason, tc.path)
	}
	require.Equal(t, calls, len(repo.errLookups), "rejected requests never reach the repository")

	repo.errRows, repo.usageRows = nil, nil
	rec = serveOps(r, http.MethodGet, "/ops/requests/req-missing/attempts", nil)
	require.Equal(t, http.StatusNotFound, rec.Code)
	require.Contains(t, rec.Body.String(), `"reason":"OPS_REQUEST_NOT_FOUND"`)

	disabled := NewOpsHandler(nil)
	r2 := gin.New()
	r2.GET("/ops/requests/:request_id/attempts", disabled.GetRequestAttempts)
	rec = serveOps(r2, http.MethodGet, "/ops/requests/req-1/attempts", nil)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
}
