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

type todayStatsBatchRepoStub struct {
	UsageLogRepository
	batchErr    error
	batchStats  map[int64]*usagestats.AccountStats
	failIDs     map[int64]bool
	failAll     bool
	mu          sync.Mutex
	batchCalls  int
	singleCalls int
}

func (r *todayStatsBatchRepoStub) GetAccountWindowStatsBatch(_ context.Context, _ []int64, _ time.Time) (map[int64]*usagestats.AccountStats, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.batchCalls++
	if r.batchErr != nil {
		return nil, r.batchErr
	}
	return r.batchStats, nil
}

func (r *todayStatsBatchRepoStub) GetAccountWindowStats(_ context.Context, accountID int64, _ time.Time) (*usagestats.AccountStats, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.singleCalls++
	if r.failAll || r.failIDs[accountID] {
		return nil, errors.New("stats query failed")
	}
	return &usagestats.AccountStats{Requests: 5}, nil
}

func newTodayStatsBatchService(repo *todayStatsBatchRepoStub) *AccountUsageService {
	return NewAccountUsageService(nil, repo, nil, nil, nil, nil, nil, nil, nil, nil, nil)
}

func TestGetTodayStatsBatchDetailed_BatchSuccessNotDegraded(t *testing.T) {
	repo := &todayStatsBatchRepoStub{batchStats: map[int64]*usagestats.AccountStats{11: {Requests: 7}}}
	svc := newTodayStatsBatchService(repo)

	stats, failed, degraded, err := svc.GetTodayStatsBatchDetailed(context.Background(), []int64{11, 12})

	require.NoError(t, err)
	require.False(t, degraded)
	require.NotNil(t, failed)
	require.Empty(t, failed)
	require.Equal(t, int64(7), stats[11].Requests)
	require.NotNil(t, stats[12])
	require.Equal(t, int64(0), stats[12].Requests)
	require.Equal(t, 0, repo.singleCalls)
}

func TestGetTodayStatsBatchDetailed_PartialFallbackReportsFailedIDs(t *testing.T) {
	repo := &todayStatsBatchRepoStub{
		batchErr: errors.New("batch sql failed"),
		failIDs:  map[int64]bool{15: true, 9: true},
	}
	svc := newTodayStatsBatchService(repo)

	stats, failed, degraded, err := svc.GetTodayStatsBatchDetailed(context.Background(), []int64{15, 3, 0, -1, 15, 9, 3})

	require.NoError(t, err)
	require.True(t, degraded)
	require.Equal(t, []int64{15, 9}, failed)
	require.Len(t, stats, 3)
	require.NotNil(t, stats[15])
	require.Equal(t, WindowStats{}, *stats[15])
	require.Equal(t, WindowStats{}, *stats[9])
	require.Equal(t, int64(5), stats[3].Requests)
}

func TestGetTodayStatsBatchDetailed_FallbackAllSucceedStillDegraded(t *testing.T) {
	repo := &todayStatsBatchRepoStub{batchErr: errors.New("batch sql failed")}
	svc := newTodayStatsBatchService(repo)

	stats, failed, degraded, err := svc.GetTodayStatsBatchDetailed(context.Background(), []int64{1, 2})

	require.NoError(t, err)
	require.True(t, degraded)
	require.NotNil(t, failed)
	require.Empty(t, failed)
	require.Equal(t, int64(5), stats[1].Requests)
	require.Equal(t, int64(5), stats[2].Requests)
}

func TestGetTodayStatsBatchDetailed_AllFailedReturnsUnavailable(t *testing.T) {
	repo := &todayStatsBatchRepoStub{batchErr: errors.New("batch sql failed"), failAll: true}
	svc := newTodayStatsBatchService(repo)

	stats, failed, degraded, err := svc.GetTodayStatsBatchDetailed(context.Background(), []int64{4, 2})

	require.Error(t, err)
	require.ErrorIs(t, err, ErrAccountStatsUnavailable)
	require.Equal(t, "ACCOUNT_STATS_UNAVAILABLE", infraerrors.Reason(err))
	require.Equal(t, http.StatusServiceUnavailable, infraerrors.Code(err))
	require.True(t, degraded)
	require.Equal(t, []int64{4, 2}, failed)
	require.Equal(t, WindowStats{}, *stats[4])
	require.Equal(t, WindowStats{}, *stats[2])
}

func TestGetTodayStatsBatchDetailed_EmptyInput(t *testing.T) {
	repo := &todayStatsBatchRepoStub{failAll: true, batchErr: errors.New("batch sql failed")}
	svc := newTodayStatsBatchService(repo)

	for _, ids := range [][]int64{nil, {}, {0, -3}} {
		stats, failed, degraded, err := svc.GetTodayStatsBatchDetailed(context.Background(), ids)
		require.NoError(t, err)
		require.False(t, degraded)
		require.NotNil(t, failed)
		require.Empty(t, failed)
		require.Empty(t, stats)
	}
	require.Equal(t, 0, repo.batchCalls)
}

func TestGetTodayStatsBatch_LegacySignatureStillSucceedsOnTotalFailure(t *testing.T) {
	repo := &todayStatsBatchRepoStub{batchErr: errors.New("batch sql failed"), failAll: true}
	svc := newTodayStatsBatchService(repo)

	stats, err := svc.GetTodayStatsBatch(context.Background(), []int64{8, 6})

	require.NoError(t, err)
	require.Equal(t, WindowStats{}, *stats[8])
	require.Equal(t, WindowStats{}, *stats[6])
}
