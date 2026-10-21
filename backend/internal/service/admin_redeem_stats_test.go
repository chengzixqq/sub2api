//go:build unit

package service

import (
	"context"
	"errors"
	"net/http"
	"testing"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

type redeemStatsRepoStub struct {
	redeemRepoStub
	filters []RedeemCodeListFilter
	stats   *RedeemCodeStats
	err     error
}

func (s *redeemStatsRepoStub) StatsWithFilters(_ context.Context, filter RedeemCodeListFilter) (*RedeemCodeStats, error) {
	s.filters = append(s.filters, filter)
	return s.stats, s.err
}

func TestAdminServiceGetRedeemCodeStats_ClearsStatusAndFillsTypeKeys(t *testing.T) {
	repoStats := &RedeemCodeStats{Total: 3, Used: 3, ByType: map[string]int64{RedeemTypeBalance: 3}, ValueDistributed: 12.5}
	repo := &redeemStatsRepoStub{stats: repoStats}
	svc := &adminServiceImpl{redeemCodeRepo: repo}

	stats, err := svc.GetRedeemCodeStats(context.Background(), RedeemCodeListFilter{
		Status: StatusUnused, Types: []string{RedeemTypeBalance}, Search: "abc",
	})

	require.NoError(t, err)
	require.Len(t, repo.filters, 1)
	require.Empty(t, repo.filters[0].Status, "stats are the status breakdown, so the status filter is dropped")
	require.Equal(t, []string{RedeemTypeBalance}, repo.filters[0].Types)
	require.Equal(t, "abc", repo.filters[0].Search)
	require.Equal(t, map[string]int64{
		RedeemTypeBalance: 3, RedeemTypeConcurrency: 0, RedeemTypeSubscription: 0, RedeemTypeInvitation: 0,
		AdjustmentTypeAdminBalance: 0, AdjustmentTypeAdminConcurrency: 0,
	}, stats.ByType)
	require.Equal(t, int64(3), stats.Total)
	require.Equal(t, int64(3), stats.Used)
	require.InDelta(t, 12.5, stats.ValueDistributed, 1e-9)
	require.Equal(t, map[string]int64{RedeemTypeBalance: 3}, repoStats.ByType, "the repository result is not modified")
}

func TestAdminServiceGetRedeemCodeStats_KeepsUnknownTypes(t *testing.T) {
	repo := &redeemStatsRepoStub{stats: &RedeemCodeStats{Total: 1, Unused: 1, ByType: map[string]int64{"legacy": 1}}}
	svc := &adminServiceImpl{redeemCodeRepo: repo}

	stats, err := svc.GetRedeemCodeStats(context.Background(), RedeemCodeListFilter{})

	require.NoError(t, err)
	require.Equal(t, int64(1), stats.ByType["legacy"])
	require.Len(t, stats.ByType, 7)
}

func TestAdminServiceGetRedeemCodeStats_RepositoryError(t *testing.T) {
	boom := errors.New("db down")
	svc := &adminServiceImpl{redeemCodeRepo: &redeemStatsRepoStub{err: boom}}

	_, err := svc.GetRedeemCodeStats(context.Background(), RedeemCodeListFilter{})

	require.ErrorIs(t, err, boom)
}

func TestAdminServiceGetRedeemCodeStats_UnsupportedRepository(t *testing.T) {
	svc := &adminServiceImpl{redeemCodeRepo: &redeemRepoStub{}}

	_, err := svc.GetRedeemCodeStats(context.Background(), RedeemCodeListFilter{})

	require.ErrorIs(t, err, ErrServiceUnavailable)
	require.Equal(t, "SERVICE_UNAVAILABLE", infraerrors.Reason(err))
	require.Equal(t, http.StatusServiceUnavailable, infraerrors.Code(err))
}
