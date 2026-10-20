package service

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/stretchr/testify/require"
)

// todayStatsGroupRepoStub 只实现分组批量读取；不分组的批量与逐个查询一旦被调用就计数，
// 用来证明分组路径绝不退回未按分组过滤的数据。
type todayStatsGroupRepoStub struct {
	UsageLogRepository
	groupErr    error
	groupStats  map[int64]*usagestats.AccountStats
	mu          sync.Mutex
	groupCalls  int
	gotIDs      []int64
	gotGroupID  int64
	gotStart    time.Time
	batchCalls  int
	singleCalls int
}

func (r *todayStatsGroupRepoStub) GetAccountWindowStatsBatchByGroup(_ context.Context, accountIDs []int64, groupID int64, startTime time.Time) (map[int64]*usagestats.AccountStats, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.groupCalls++
	r.gotIDs = append([]int64(nil), accountIDs...)
	r.gotGroupID = groupID
	r.gotStart = startTime
	if r.groupErr != nil {
		return nil, r.groupErr
	}
	return r.groupStats, nil
}

func (r *todayStatsGroupRepoStub) GetAccountWindowStatsBatch(context.Context, []int64, time.Time) (map[int64]*usagestats.AccountStats, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.batchCalls++
	return map[int64]*usagestats.AccountStats{}, nil
}

func (r *todayStatsGroupRepoStub) GetAccountWindowStats(context.Context, int64, time.Time) (*usagestats.AccountStats, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.singleCalls++
	return &usagestats.AccountStats{Requests: 99}, nil
}

// todayStatsNoGroupReaderRepo 支持不分组的批量与逐个查询，但不支持分组读取。
type todayStatsNoGroupReaderRepo struct {
	UsageLogRepository
	calls int
}

func (r *todayStatsNoGroupReaderRepo) GetAccountWindowStatsBatch(context.Context, []int64, time.Time) (map[int64]*usagestats.AccountStats, error) {
	r.calls++
	return map[int64]*usagestats.AccountStats{1: {Requests: 42}}, nil
}

func (r *todayStatsNoGroupReaderRepo) GetAccountWindowStats(context.Context, int64, time.Time) (*usagestats.AccountStats, error) {
	r.calls++
	return &usagestats.AccountStats{Requests: 42}, nil
}

func TestGetTodayStatsBatchByGroup_PassesGroupDedupesAndZeroFills(t *testing.T) {
	repo := &todayStatsGroupRepoStub{groupStats: map[int64]*usagestats.AccountStats{
		3: {Requests: 2, Tokens: 30, Cost: 1.5, StandardCost: 1.25, UserCost: 2.5},
	}}
	svc := NewAccountUsageService(nil, repo, nil, nil, nil, nil, nil, nil, nil, nil, nil)

	before := GroupUsageTodayStart(time.Now())
	stats, failed, degraded, err := svc.GetTodayStatsBatchByGroup(context.Background(), []int64{3, 0, 1, 3, -2}, 7)
	after := GroupUsageTodayStart(time.Now())

	require.NoError(t, err)
	require.False(t, degraded)
	require.NotNil(t, failed)
	require.Empty(t, failed)
	require.Equal(t, 1, repo.groupCalls)
	require.Equal(t, []int64{3, 1}, repo.gotIDs)
	require.Equal(t, int64(7), repo.gotGroupID)
	// 账号今日与分组今日（站长 groups/usage-summary）必须是同一起点，否则成员行与分组今日对不上。
	require.True(t, repo.gotStart.Equal(before) || repo.gotStart.Equal(after), "start=%s before=%s after=%s", repo.gotStart, before, after)

	require.Len(t, stats, 2)
	require.Equal(t, WindowStats{Requests: 2, Tokens: 30, Cost: 1.5, StandardCost: 1.25, UserCost: 2.5}, *stats[3])
	require.Equal(t, WindowStats{}, *stats[1])
	require.Zero(t, repo.batchCalls, "分组路径不能调用不分组的批量查询")
	require.Zero(t, repo.singleCalls, "分组路径不能逐个回退")
}

func TestGetTodayStatsBatchByGroup_ErrorIsNotZeroFilled(t *testing.T) {
	repo := &todayStatsGroupRepoStub{groupErr: errors.New("group sql failed")}
	svc := NewAccountUsageService(nil, repo, nil, nil, nil, nil, nil, nil, nil, nil, nil)

	stats, failed, degraded, err := svc.GetTodayStatsBatchByGroup(context.Background(), []int64{5, 6}, 9)

	require.Error(t, err)
	require.ErrorIs(t, err, ErrAccountStatsUnavailable)
	require.Equal(t, http.StatusServiceUnavailable, infraerrors.Code(err))
	require.Nil(t, stats, "失败时不能返回补 0 的统计")
	require.Equal(t, []int64{5, 6}, failed)
	require.False(t, degraded)
	require.Zero(t, repo.batchCalls)
	require.Zero(t, repo.singleCalls)
}

func TestGetTodayStatsBatchByGroup_RepoWithoutGroupReaderFailsClosed(t *testing.T) {
	repo := &todayStatsNoGroupReaderRepo{}
	svc := NewAccountUsageService(nil, repo, nil, nil, nil, nil, nil, nil, nil, nil, nil)

	stats, failed, _, err := svc.GetTodayStatsBatchByGroup(context.Background(), []int64{1}, 7)

	require.ErrorIs(t, err, ErrAccountStatsUnavailable)
	require.Nil(t, stats)
	require.Equal(t, []int64{1}, failed)
	require.Zero(t, repo.calls, "仓储不支持分组读取时绝不退回不分组的数据")
}

func TestGetTodayStatsBatchByGroup_RejectsNonPositiveGroup(t *testing.T) {
	repo := &todayStatsGroupRepoStub{}
	svc := NewAccountUsageService(nil, repo, nil, nil, nil, nil, nil, nil, nil, nil, nil)

	for _, groupID := range []int64{0, -1} {
		_, _, _, err := svc.GetTodayStatsBatchByGroup(context.Background(), []int64{1}, groupID)
		require.ErrorIs(t, err, ErrInvalidGroupFilter)
		require.Equal(t, "INVALID_GROUP_FILTER", infraerrors.Reason(err))
		require.Equal(t, http.StatusBadRequest, infraerrors.Code(err))
	}
	require.Zero(t, repo.groupCalls)
}

func TestGetTodayStatsBatchByGroup_EmptyInputSkipsQuery(t *testing.T) {
	repo := &todayStatsGroupRepoStub{groupErr: errors.New("must not be called")}
	svc := NewAccountUsageService(nil, repo, nil, nil, nil, nil, nil, nil, nil, nil, nil)

	for _, ids := range [][]int64{nil, {}, {0, -3}} {
		stats, failed, degraded, err := svc.GetTodayStatsBatchByGroup(context.Background(), ids, 7)
		require.NoError(t, err)
		require.False(t, degraded)
		require.NotNil(t, failed)
		require.Empty(t, failed)
		require.NotNil(t, stats)
		require.Empty(t, stats)
	}
	require.Zero(t, repo.groupCalls)
}
