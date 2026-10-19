package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// documentedBatchUsageIDLimit pins the public contract (E12); it intentionally
// does not reference service.MaxBatchUsageIDs so a silent change fails here.
const documentedBatchUsageIDLimit = 1000

// batchUsageIDProbe records the ID lists that reach the usage_logs aggregation.
type batchUsageIDProbe struct {
	service.UsageLogRepository
	mu    sync.Mutex
	loads [][]int64
}

func (r *batchUsageIDProbe) record(ids []int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.loads = append(r.loads, append([]int64(nil), ids...))
}

func (r *batchUsageIDProbe) snapshot() [][]int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([][]int64(nil), r.loads...)
}

func (r *batchUsageIDProbe) GetBatchUserUsageStats(_ context.Context, ids []int64, _, _ time.Time) (map[int64]*usagestats.BatchUserUsageStats, error) {
	r.record(ids)
	return map[int64]*usagestats.BatchUserUsageStats{}, nil
}

func (r *batchUsageIDProbe) GetBatchAPIKeyUsageStats(_ context.Context, ids []int64, _, _ time.Time) (map[int64]*usagestats.BatchAPIKeyUsageStats, error) {
	r.record(ids)
	return map[int64]*usagestats.BatchAPIKeyUsageStats{}, nil
}

type batchUsageLimitCase struct {
	kind    string
	field   string
	message string
}

var batchUsageLimitCases = []batchUsageLimitCase{
	{kind: "users", field: "user_ids", message: "Too many user IDs (maximum 1000 allowed)"},
	{kind: "keys", field: "api_key_ids", message: "Too many API key IDs (maximum 1000 allowed)"},
}

func sequentialBatchUsageIDs(n int) []int64 {
	ids := make([]int64, n)
	for i := range ids {
		ids[i] = int64(i + 1)
	}
	return ids
}

func batchUsageBody(t *testing.T, field string, ids []int64) string {
	t.Helper()
	raw, err := json.Marshal(map[string][]int64{field: ids})
	require.NoError(t, err)
	return string(raw)
}

func newBatchUsageLimitRouter(t *testing.T, kind string) (*batchUsageIDProbe, http.Handler) {
	t.Helper()
	resetDashboardBatchCachesForTest()
	t.Cleanup(resetDashboardBatchCachesForTest)
	repo := &batchUsageIDProbe{}
	router, _ := dashboardBatchCacheRouter(repo, kind)
	return repo, router
}

func TestDashboardBatchUsage_RejectsMoreThanMaxIDs(t *testing.T) {
	for _, tc := range batchUsageLimitCases {
		t.Run(tc.kind, func(t *testing.T) {
			repo, router := newBatchUsageLimitRouter(t, tc.kind)
			body := batchUsageBody(t, tc.field, sequentialBatchUsageIDs(documentedBatchUsageIDLimit+1))

			rec := dashboardBatchCacheRequest(router, body, "")

			require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			var resp struct {
				Code     int               `json:"code"`
				Message  string            `json:"message"`
				Reason   string            `json:"reason"`
				Metadata map[string]string `json:"metadata"`
			}
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
			require.Equal(t, http.StatusBadRequest, resp.Code)
			require.Equal(t, tc.message, resp.Message)
			require.Equal(t, "TOO_MANY_IDS", resp.Reason)
			require.Equal(t, map[string]string{"field": tc.field, "max": "1000"}, resp.Metadata)
			require.Empty(t, repo.snapshot(), "oversized batches must be rejected before aggregating usage_logs")
			require.Empty(t, rec.Header().Get("X-Snapshot-Cache"))
		})
	}
}

func TestDashboardBatchUsage_AcceptsExactlyMaxIDs(t *testing.T) {
	for _, tc := range batchUsageLimitCases {
		t.Run(tc.kind, func(t *testing.T) {
			repo, router := newBatchUsageLimitRouter(t, tc.kind)
			body := batchUsageBody(t, tc.field, sequentialBatchUsageIDs(documentedBatchUsageIDLimit))

			rec := dashboardBatchCacheRequest(router, body, "")

			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			require.Equal(t, "miss", rec.Header().Get("X-Snapshot-Cache"))
			loads := repo.snapshot()
			require.Len(t, loads, 1)
			require.Len(t, loads[0], documentedBatchUsageIDLimit)
		})
	}
}

func TestDashboardBatchUsage_LimitCountsDistinctPositiveIDs(t *testing.T) {
	for _, tc := range batchUsageLimitCases {
		t.Run(tc.kind, func(t *testing.T) {
			repo, router := newBatchUsageLimitRouter(t, tc.kind)
			ids := sequentialBatchUsageIDs(documentedBatchUsageIDLimit)
			ids = append(ids, 1, 2, 3, documentedBatchUsageIDLimit, 0, -1, -5)
			require.Greater(t, len(ids), documentedBatchUsageIDLimit)

			rec := dashboardBatchCacheRequest(router, batchUsageBody(t, tc.field, ids), "")

			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			loads := repo.snapshot()
			require.Len(t, loads, 1)
			require.Equal(t, sequentialBatchUsageIDs(documentedBatchUsageIDLimit), loads[0])
		})
	}
}
