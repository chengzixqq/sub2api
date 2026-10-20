package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// todayStatsGroupHandlerRepo 同时实现不分组与分组两个批量读取，分别计数。
type todayStatsGroupHandlerRepo struct {
	service.UsageLogRepository
	groupErr    error
	mu          sync.Mutex
	batchCalls  int
	groupCalls  int
	gotGroupID  int64
	gotGroupIDs []int64
}

func (r *todayStatsGroupHandlerRepo) GetAccountWindowStatsBatch(_ context.Context, accountIDs []int64, _ time.Time) (map[int64]*usagestats.AccountStats, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.batchCalls++
	out := make(map[int64]*usagestats.AccountStats, len(accountIDs))
	for _, id := range accountIDs {
		out[id] = &usagestats.AccountStats{Requests: 10}
	}
	return out, nil
}

func (r *todayStatsGroupHandlerRepo) GetAccountWindowStatsBatchByGroup(_ context.Context, accountIDs []int64, groupID int64, _ time.Time) (map[int64]*usagestats.AccountStats, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.groupCalls++
	r.gotGroupID = groupID
	r.gotGroupIDs = append([]int64(nil), accountIDs...)
	if r.groupErr != nil {
		return nil, r.groupErr
	}
	out := make(map[int64]*usagestats.AccountStats, len(accountIDs))
	for _, id := range accountIDs {
		out[id] = &usagestats.AccountStats{Requests: groupID}
	}
	return out, nil
}

func (r *todayStatsGroupHandlerRepo) counts() (batch, group int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.batchCalls, r.groupCalls
}

// todayStatsGroupScopeAdmin 模拟 vendor：分组授权与账号收窄都可配置，并记录调用次数。
type todayStatsGroupScopeAdmin struct {
	*stubAdminService
	grantedGroups map[int64]bool
	groupCalls    int
	accountCalls  int
}

func (s *todayStatsGroupScopeAdmin) FilterGroupIDsByScope(_ context.Context, ids []int64) ([]int64, error) {
	s.groupCalls++
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if s.grantedGroups[id] {
			out = append(out, id)
		}
	}
	return out, nil
}

func (s *todayStatsGroupScopeAdmin) FilterAccountIDsByScope(_ context.Context, ids []int64) ([]int64, error) {
	s.accountCalls++
	return ids, nil
}

func newTodayStatsGroupRouter(t *testing.T, repo *todayStatsGroupHandlerRepo, adminService service.AdminService) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	previous := accountTodayStatsBatchCache
	accountTodayStatsBatchCache = newSnapshotCache(30 * time.Second)
	t.Cleanup(func() { accountTodayStatsBatchCache = previous })

	svc := service.NewAccountUsageService(nil, repo, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	handler := &AccountHandler{adminService: adminService, accountUsageService: svc}
	router := gin.New()
	router.POST("/api/v1/admin/accounts/today-stats/batch", handler.GetBatchTodayStats)
	return router
}

func decodeTodayStatsGroupData(t *testing.T, body []byte) map[string]json.RawMessage {
	t.Helper()
	var envelope struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	require.NoError(t, json.Unmarshal(body, &envelope), string(body))
	return envelope.Data
}

func TestBatchTodayStats_GroupIDFiltersAndEchoes(t *testing.T) {
	repo := &todayStatsGroupHandlerRepo{}
	router := newTodayStatsGroupRouter(t, repo, &stubAdminService{})

	rec := postTodayStatsBatch(router, `{"account_ids":[917003,917001],"group_id":7}`, "")

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	data := decodeTodayStatsGroupData(t, rec.Body.Bytes())
	require.JSONEq(t, `7`, string(data["group_id"]))
	require.JSONEq(t, `[]`, string(data["failed_account_ids"]))
	require.JSONEq(t, `false`, string(data["degraded"]))
	body := decodeTodayStatsBatch(t, rec)
	require.Equal(t, int64(7), body.Data.Stats["917001"].Requests)
	require.Equal(t, int64(7), body.Data.Stats["917003"].Requests)

	batch, group := repo.counts()
	require.Equal(t, 0, batch, "带 group_id 时不能走不分组的批量查询")
	require.Equal(t, 1, group)
	require.Equal(t, int64(7), repo.gotGroupID)
	require.Equal(t, []int64{917001, 917003}, repo.gotGroupIDs)
}

func TestBatchTodayStats_GroupCacheKeyIsolated(t *testing.T) {
	repo := &todayStatsGroupHandlerRepo{}
	router := newTodayStatsGroupRouter(t, repo, &stubAdminService{})

	requests := []struct {
		body      string
		wantCache string
		wantReq   int64
	}{
		{`{"account_ids":[917011]}`, "miss", 10},
		{`{"account_ids":[917011],"group_id":7}`, "miss", 7},
		{`{"account_ids":[917011],"group_id":9}`, "miss", 9},
		{`{"account_ids":[917011],"group_id":7}`, "hit", 7},
		{`{"account_ids":[917011]}`, "hit", 10},
	}
	for i, tc := range requests {
		rec := postTodayStatsBatch(router, tc.body, "")
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		require.Equal(t, tc.wantCache, rec.Header().Get("X-Snapshot-Cache"), "request %d %s", i, tc.body)
		body := decodeTodayStatsBatch(t, rec)
		require.Equal(t, tc.wantReq, body.Data.Stats["917011"].Requests, "request %d %s", i, tc.body)
	}
	batch, group := repo.counts()
	require.Equal(t, 1, batch)
	require.Equal(t, 2, group)
}

func TestBatchTodayStats_RejectsNonPositiveGroupID(t *testing.T) {
	for _, body := range []string{
		`{"account_ids":[917021],"group_id":0}`,
		`{"account_ids":[917021],"group_id":-1}`,
	} {
		repo := &todayStatsGroupHandlerRepo{}
		router := newTodayStatsGroupRouter(t, repo, &stubAdminService{})

		rec := postTodayStatsBatch(router, body, "")

		require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
		require.Equal(t, "INVALID_GROUP_FILTER", decodeTodayStatsBatch(t, rec).Reason)
		batch, group := repo.counts()
		require.Zero(t, batch)
		require.Zero(t, group)
	}
}

func TestBatchTodayStats_NullGroupIDTreatedAsAbsent(t *testing.T) {
	repo := &todayStatsGroupHandlerRepo{}
	router := newTodayStatsGroupRouter(t, repo, &stubAdminService{})

	rec := postTodayStatsBatch(router, `{"account_ids":[917031],"group_id":null}`, "")

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	data := decodeTodayStatsGroupData(t, rec.Body.Bytes())
	require.NotContains(t, data, "group_id")
	batch, group := repo.counts()
	require.Equal(t, 1, batch)
	require.Zero(t, group)
}

func TestBatchTodayStats_VendorUnauthorizedGroupReturns404(t *testing.T) {
	repo := &todayStatsGroupHandlerRepo{}
	scope := &todayStatsGroupScopeAdmin{stubAdminService: &stubAdminService{}, grantedGroups: map[int64]bool{8: true}}
	router := newTodayStatsGroupRouter(t, repo, scope)

	rec := postTodayStatsBatch(router, `{"account_ids":[917041],"group_id":7}`, "")

	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	require.Equal(t, "RESOURCE_NOT_FOUND", decodeTodayStatsBatch(t, rec).Reason)
	require.Equal(t, 1, scope.groupCalls)
	require.Zero(t, scope.accountCalls, "分组未授权时不应再收窄账号或取数")
	batch, group := repo.counts()
	require.Zero(t, batch)
	require.Zero(t, group)
}

func TestBatchTodayStats_VendorAuthorizedGroupStillScopesAccounts(t *testing.T) {
	repo := &todayStatsGroupHandlerRepo{}
	scope := &todayStatsGroupScopeAdmin{stubAdminService: &stubAdminService{}, grantedGroups: map[int64]bool{8: true}}
	router := newTodayStatsGroupRouter(t, repo, scope)

	rec := postTodayStatsBatch(router, `{"account_ids":[917051],"group_id":8}`, "")

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, 1, scope.groupCalls)
	require.Equal(t, 1, scope.accountCalls, "分组授权之后仍要按工作区收窄账号")
	_, group := repo.counts()
	require.Equal(t, 1, group)
}

func TestBatchTodayStats_GroupQueryFailureReturns503AndNotCached(t *testing.T) {
	repo := &todayStatsGroupHandlerRepo{groupErr: errors.New("group sql failed")}
	router := newTodayStatsGroupRouter(t, repo, &stubAdminService{})

	for i := 0; i < 2; i++ {
		rec := postTodayStatsBatch(router, `{"account_ids":[917061,917062],"group_id":7}`, "")
		require.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
		require.Equal(t, "ACCOUNT_STATS_UNAVAILABLE", decodeTodayStatsBatch(t, rec).Reason)
		require.Empty(t, rec.Header().Get("ETag"))
	}
	batch, group := repo.counts()
	require.Zero(t, batch, "分组查询失败时绝不退回不分组的数据")
	require.Equal(t, 2, group)
}

func TestBatchTodayStats_GroupEmptyIDsEchoesGroup(t *testing.T) {
	repo := &todayStatsGroupHandlerRepo{}
	router := newTodayStatsGroupRouter(t, repo, &stubAdminService{})

	rec := postTodayStatsBatch(router, `{"account_ids":[0],"group_id":7}`, "")

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	data := decodeTodayStatsGroupData(t, rec.Body.Bytes())
	require.JSONEq(t, `7`, string(data["group_id"]))
	require.JSONEq(t, `{}`, string(data["stats"]))
	require.JSONEq(t, `[]`, string(data["failed_account_ids"]))
}

func TestBatchTodayStats_WithoutGroupUnchanged(t *testing.T) {
	repo := &todayStatsGroupHandlerRepo{}
	router := newTodayStatsGroupRouter(t, repo, &stubAdminService{})

	rec := postTodayStatsBatch(router, `{"account_ids":[917071]}`, "")

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	data := decodeTodayStatsGroupData(t, rec.Body.Bytes())
	keys := make([]string, 0, len(data))
	for key := range data {
		keys = append(keys, key)
	}
	require.ElementsMatch(t, []string{"stats", "failed_account_ids", "degraded"}, keys)
	batch, group := repo.counts()
	require.Equal(t, 1, batch)
	require.Zero(t, group)
}
