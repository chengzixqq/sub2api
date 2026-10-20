package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type todayStatsBatchHandlerRepo struct {
	service.UsageLogRepository
	batchErr   error
	failIDs    map[int64]bool
	failAll    bool
	mu         sync.Mutex
	batchCalls int
}

func (r *todayStatsBatchHandlerRepo) GetAccountWindowStatsBatch(_ context.Context, accountIDs []int64, _ time.Time) (map[int64]*usagestats.AccountStats, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.batchCalls++
	if r.batchErr != nil {
		return nil, r.batchErr
	}
	out := make(map[int64]*usagestats.AccountStats, len(accountIDs))
	for _, id := range accountIDs {
		out[id] = &usagestats.AccountStats{Requests: 3}
	}
	return out, nil
}

func (r *todayStatsBatchHandlerRepo) GetAccountWindowStats(_ context.Context, accountID int64, _ time.Time) (*usagestats.AccountStats, error) {
	if r.failAll || r.failIDs[accountID] {
		return nil, errors.New("stats query failed")
	}
	return &usagestats.AccountStats{Requests: 4}, nil
}

func (r *todayStatsBatchHandlerRepo) calls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.batchCalls
}

func newTodayStatsBatchRouter(repo *todayStatsBatchHandlerRepo) *gin.Engine {
	gin.SetMode(gin.TestMode)
	accountTodayStatsBatchCache = newSnapshotCache(30 * time.Second)
	svc := service.NewAccountUsageService(nil, repo, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	handler := &AccountHandler{adminService: &stubAdminService{}, accountUsageService: svc}
	router := gin.New()
	router.POST("/api/v1/admin/accounts/today-stats/batch", handler.GetBatchTodayStats)
	return router
}

func postTodayStatsBatch(router *gin.Engine, body string, ifNoneMatch string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/today-stats/batch", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if ifNoneMatch != "" {
		req.Header.Set("If-None-Match", ifNoneMatch)
	}
	router.ServeHTTP(rec, req)
	return rec
}

type todayStatsBatchBody struct {
	Code    int    `json:"code"`
	Reason  string `json:"reason"`
	Message string `json:"message"`
	Data    struct {
		Stats map[string]struct {
			Requests int64 `json:"requests"`
		} `json:"stats"`
		FailedAccountIDs []int64 `json:"failed_account_ids"`
		Degraded         *bool   `json:"degraded"`
	} `json:"data"`
}

func decodeTodayStatsBatch(t *testing.T, rec *httptest.ResponseRecorder) todayStatsBatchBody {
	t.Helper()
	var body todayStatsBatchBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body), rec.Body.String())
	return body
}

func TestGetBatchTodayStats_PartialFailureSkipsCache(t *testing.T) {
	repo := &todayStatsBatchHandlerRepo{
		batchErr: errors.New("batch sql failed"),
		failIDs:  map[int64]bool{916002: true},
	}
	router := newTodayStatsBatchRouter(repo)

	for i := 0; i < 2; i++ {
		rec := postTodayStatsBatch(router, `{"account_ids":[916002,916001]}`, "")
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		require.Contains(t, rec.Body.String(), `"failed_account_ids":[916002]`)
		require.Contains(t, rec.Body.String(), `"degraded":true`)
		body := decodeTodayStatsBatch(t, rec)
		require.Equal(t, int64(0), body.Data.Stats["916002"].Requests)
		require.Equal(t, int64(4), body.Data.Stats["916001"].Requests)
		require.Equal(t, "miss", rec.Header().Get("X-Snapshot-Cache"))
		require.Empty(t, rec.Header().Get("ETag"))
	}
	require.Equal(t, 2, repo.calls())
}

func TestGetBatchTodayStats_SuccessCachedWithEmptyFailures(t *testing.T) {
	repo := &todayStatsBatchHandlerRepo{}
	router := newTodayStatsBatchRouter(repo)

	rec := postTodayStatsBatch(router, `{"account_ids":[916011,916012]}`, "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"failed_account_ids":[]`)
	require.Contains(t, rec.Body.String(), `"degraded":false`)
	require.Equal(t, "miss", rec.Header().Get("X-Snapshot-Cache"))
	etag := rec.Header().Get("ETag")
	require.NotEmpty(t, etag)

	rec = postTodayStatsBatch(router, `{"account_ids":[916011,916012]}`, "")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "hit", rec.Header().Get("X-Snapshot-Cache"))
	require.Contains(t, rec.Body.String(), `"failed_account_ids":[]`)

	rec = postTodayStatsBatch(router, `{"account_ids":[916011,916012]}`, etag)
	require.Equal(t, http.StatusNotModified, rec.Code)
	require.Equal(t, 1, repo.calls())
}

func TestGetBatchTodayStats_FallbackWithoutFailuresIsCached(t *testing.T) {
	repo := &todayStatsBatchHandlerRepo{batchErr: errors.New("batch sql failed")}
	router := newTodayStatsBatchRouter(repo)

	rec := postTodayStatsBatch(router, `{"account_ids":[916021]}`, "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"failed_account_ids":[]`)
	require.Contains(t, rec.Body.String(), `"degraded":true`)
	require.NotEmpty(t, rec.Header().Get("ETag"))

	rec = postTodayStatsBatch(router, `{"account_ids":[916021]}`, "")
	require.Equal(t, "hit", rec.Header().Get("X-Snapshot-Cache"))
	require.Equal(t, 1, repo.calls())
}

func TestGetBatchTodayStats_AllFailedReturns503AndNotCached(t *testing.T) {
	repo := &todayStatsBatchHandlerRepo{batchErr: errors.New("batch sql failed"), failAll: true}
	router := newTodayStatsBatchRouter(repo)

	for i := 0; i < 2; i++ {
		rec := postTodayStatsBatch(router, `{"account_ids":[916031,916032]}`, "")
		require.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
		body := decodeTodayStatsBatch(t, rec)
		require.Equal(t, "ACCOUNT_STATS_UNAVAILABLE", body.Reason)
		require.Empty(t, rec.Header().Get("ETag"))
	}
	require.Equal(t, 2, repo.calls())
}

func TestGetBatchTodayStats_EmptyIDsIncludesNewFields(t *testing.T) {
	repo := &todayStatsBatchHandlerRepo{}
	router := newTodayStatsBatchRouter(repo)

	rec := postTodayStatsBatch(router, `{"account_ids":[0]}`, "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"stats":{}`)
	require.Contains(t, rec.Body.String(), `"failed_account_ids":[]`)
	require.Contains(t, rec.Body.String(), `"degraded":false`)
	require.Equal(t, 0, repo.calls())
}

type todayStatsScopeDenyAdmin struct {
	*stubAdminService
}

func (s *todayStatsScopeDenyAdmin) FilterAccountIDsByScope(context.Context, []int64) ([]int64, error) {
	return []int64{}, nil
}

func TestGetBatchTodayStats_ScopedOutIDsIncludesNewFields(t *testing.T) {
	gin.SetMode(gin.TestMode)
	accountTodayStatsBatchCache = newSnapshotCache(30 * time.Second)
	repo := &todayStatsBatchHandlerRepo{}
	svc := service.NewAccountUsageService(nil, repo, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	handler := &AccountHandler{adminService: &todayStatsScopeDenyAdmin{stubAdminService: &stubAdminService{}}, accountUsageService: svc}
	router := gin.New()
	router.POST("/api/v1/admin/accounts/today-stats/batch", handler.GetBatchTodayStats)

	rec := postTodayStatsBatch(router, `{"account_ids":[916041]}`, "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"stats":{}`)
	require.Contains(t, rec.Body.String(), `"failed_account_ids":[]`)
	require.Contains(t, rec.Body.String(), `"degraded":false`)
	require.Equal(t, 0, repo.calls())
}
