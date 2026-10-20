package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"

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
	gotStarts   []time.Time
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

func (r *todayStatsGroupHandlerRepo) GetAccountWindowStatsBatchByGroup(_ context.Context, accountIDs []int64, groupID int64, startTime time.Time) (map[int64]*usagestats.AccountStats, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.groupCalls++
	r.gotGroupID = groupID
	r.gotGroupIDs = append([]int64(nil), accountIDs...)
	r.gotStarts = append(r.gotStarts, startTime)
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
// ownedAccounts 为 nil 时账号原样放行；否则只保留其中的账号，模拟按工作区收窄。
type todayStatsGroupScopeAdmin struct {
	*stubAdminService
	grantedGroups map[int64]bool
	ownedAccounts map[int64]bool
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
	if s.ownedAccounts == nil {
		return ids, nil
	}
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if s.ownedAccounts[id] {
			out = append(out, id)
		}
	}
	return out, nil
}

func newTodayStatsGroupRouter(t *testing.T, repo *todayStatsGroupHandlerRepo, adminService service.AdminService) *gin.Engine {
	t.Helper()
	return newTodayStatsGroupRouterWithTTL(t, repo, adminService, 30*time.Second)
}

func newTodayStatsGroupRouterWithTTL(t *testing.T, repo *todayStatsGroupHandlerRepo, adminService service.AdminService, ttl time.Duration) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	previous := accountTodayStatsBatchCache
	accountTodayStatsBatchCache = newSnapshotCache(ttl)
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
	// 917052 属于别家工作区：即使分组 8 已授权（共享分组），也不能查到它在该分组的用量。
	scope := &todayStatsGroupScopeAdmin{
		stubAdminService: &stubAdminService{},
		grantedGroups:    map[int64]bool{8: true},
		ownedAccounts:    map[int64]bool{917051: true},
	}
	router := newTodayStatsGroupRouter(t, repo, scope)
	const body = `{"account_ids":[917051,917052],"group_id":8}`

	// 站长先用同一组 ID 查询并写入缓存，vendor 随后不能命中这条更宽的结果。
	adminRec := postTodayStatsBatch(newTodayStatsGroupRouterSharingCache(repo, &stubAdminService{}), body, "")
	require.Equal(t, http.StatusOK, adminRec.Code, adminRec.Body.String())
	require.Len(t, decodeTodayStatsBatch(t, adminRec).Data.Stats, 2)

	rec := postTodayStatsBatch(router, body, "")

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, "miss", rec.Header().Get("X-Snapshot-Cache"), "vendor 不能命中站长写入的未收窄缓存")
	require.Equal(t, 1, scope.groupCalls)
	require.Equal(t, 1, scope.accountCalls, "分组授权之后仍要按工作区收窄账号")
	_, group := repo.counts()
	require.Equal(t, 2, group)
	require.Equal(t, []int64{917051}, repo.gotGroupIDs, "仓储只能收到收窄后的账号")
	got := decodeTodayStatsBatch(t, rec)
	require.Contains(t, got.Data.Stats, "917051")
	require.NotContains(t, got.Data.Stats, "917052", "响应不能含别家工作区账号")
	require.Len(t, got.Data.Stats, 1)
}

// newTodayStatsGroupRouterSharingCache 复用当前测试已替换好的 accountTodayStatsBatchCache，
// 用另一个作用域构造 handler，验证不同作用域之间不串用缓存。
func newTodayStatsGroupRouterSharingCache(repo *todayStatsGroupHandlerRepo, adminService service.AdminService) *gin.Engine {
	svc := service.NewAccountUsageService(nil, repo, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	handler := &AccountHandler{adminService: adminService, accountUsageService: svc}
	router := gin.New()
	router.POST("/api/v1/admin/accounts/today-stats/batch", handler.GetBatchTodayStats)
	return router
}

// 分组路径的缓存键必须带服务器时区的当天日期：跨零点后即使旧条目还没过期，
// 也要重新取数，不能把昨天整天在该分组的用量当成“今日”返回。
func TestBatchTodayStats_GroupCacheChangesAtServerMidnight(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		repo := &todayStatsGroupHandlerRepo{}
		// 有效期远长于到零点的时间，确保换天靠的是键里的日期而不是过期。
		router := newTodayStatsGroupRouterWithTTL(t, repo, &stubAdminService{}, 48*time.Hour)
		const body = `{"account_ids":[917081],"group_id":7}`

		first := postTodayStatsBatch(router, body, "")
		require.Equal(t, http.StatusOK, first.Code, first.Body.String())
		require.Equal(t, "miss", first.Header().Get("X-Snapshot-Cache"))
		same := postTodayStatsBatch(router, body, "")
		require.Equal(t, "hit", same.Header().Get("X-Snapshot-Cache"), "同一天内仍应命中缓存")

		firstDay := timezone.Today()
		time.Sleep(firstDay.AddDate(0, 0, 1).Sub(timezone.Now()) + time.Second)
		require.NotEqual(t, firstDay, timezone.Today())

		next := postTodayStatsBatch(router, body, "")
		require.Equal(t, http.StatusOK, next.Code, next.Body.String())
		require.Equal(t, "miss", next.Header().Get("X-Snapshot-Cache"), "跨零点后不能复用昨天的分组今日统计")
		_, group := repo.counts()
		require.Equal(t, 2, group)
		require.Len(t, repo.gotStarts, 2)
		require.True(t, repo.gotStarts[1].Equal(timezone.Today()), "第二次取数的起点应为新的一天")
	})
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
