package service

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

// newGroupCapacityByIDsFixture 构造启用分组 [10,20,30]、另有存在但停用的 40 的容量服务。
func newGroupCapacityByIDsFixture() (*GroupCapacityService, *groupCapacityAccountRepoStub, *groupCapacityGroupRepoStub) {
	accountRepo := &groupCapacityAccountRepoStub{
		rows: []GroupAccountCapacityRow{
			{GroupID: 10, AccountID: 1, Concurrency: 2, Extra: map[string]any{"max_sessions": 3, "base_rpm": 11}},
			{GroupID: 20, AccountID: 1, Concurrency: 2, Extra: map[string]any{"max_sessions": 3, "base_rpm": 11}},
			{GroupID: 20, AccountID: 2, Concurrency: 4, Extra: map[string]any{"max_sessions": 1, "base_rpm": 13}},
			{GroupID: 30, AccountID: 3, Concurrency: 5},
		},
	}
	groupRepo := &groupCapacityGroupRepoStub{
		groupIDs: []int64{10, 20, 30},
		existing: map[int64]bool{10: true, 20: true, 30: true, 40: true},
	}
	concurrencyCache := &groupCapacityConcurrencyCacheStub{counts: map[int64]int{1: 1, 2: 2, 3: 4}}
	sessionCache := &groupCapacitySessionCacheStub{counts: map[int64]int{1: 2, 2: 1}}
	rpmCache := &groupCapacityRPMCacheStub{counts: map[int64]int{1: 5, 2: 7}}
	svc := NewGroupCapacityService(accountRepo, groupRepo, NewConcurrencyService(concurrencyCache), sessionCache, rpmCache)
	return svc, accountRepo, groupRepo
}

func TestGetGroupCapacityByIDsReturnsOnlyRequestedActiveGroup(t *testing.T) {
	svc, accountRepo, _ := newGroupCapacityByIDsFixture()
	all, err := svc.GetAllGroupCapacity(context.Background())
	require.NoError(t, err)
	var want GroupCapacitySummary
	for _, row := range all {
		if row.GroupID == 20 {
			want = row
		}
	}
	require.Equal(t, int64(20), want.GroupID)

	svc, accountRepo, _ = newGroupCapacityByIDsFixture()
	got, err := svc.GetGroupCapacityByIDs(context.Background(), []int64{20})
	require.NoError(t, err)
	require.Equal(t, []int64{20}, accountRepo.requested)
	require.Equal(t, []GroupCapacitySummary{want}, got)
}

func TestGetGroupCapacityByIDsKeepsActiveOrderAndDedupes(t *testing.T) {
	svc, accountRepo, _ := newGroupCapacityByIDsFixture()

	got, err := svc.GetGroupCapacityByIDs(context.Background(), []int64{30, 10, 10})
	require.NoError(t, err)
	require.Equal(t, []int64{10, 30}, accountRepo.requested)
	require.Len(t, got, 2)
	require.Equal(t, int64(10), got[0].GroupID)
	require.Equal(t, int64(30), got[1].GroupID)
}

func TestGetGroupCapacityByIDsInactiveGroupReturnsEmpty(t *testing.T) {
	svc, accountRepo, _ := newGroupCapacityByIDsFixture()

	got, err := svc.GetGroupCapacityByIDs(context.Background(), []int64{40})
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Empty(t, got)
	require.Nil(t, accountRepo.requested)
	require.Zero(t, accountRepo.unscopedCalls+accountRepo.scopedCalls)
}

func TestGetGroupCapacityByIDsActiveGroupWithoutAccountsReturnsZeroRow(t *testing.T) {
	accountRepo := &groupCapacityAccountRepoStub{}
	groupRepo := &groupCapacityGroupRepoStub{groupIDs: []int64{10}, existing: map[int64]bool{10: true}}
	svc := NewGroupCapacityService(accountRepo, groupRepo, nil, nil, nil)

	got, err := svc.GetGroupCapacityByIDs(context.Background(), []int64{10})
	require.NoError(t, err)
	require.Equal(t, []GroupCapacitySummary{{GroupID: 10}}, got)
}

func TestGetGroupCapacityByIDsMissingGroupReturnsNotFound(t *testing.T) {
	svc, accountRepo, groupRepo := newGroupCapacityByIDsFixture()

	got, err := svc.GetGroupCapacityByIDs(context.Background(), []int64{10, 999, 998})
	require.Nil(t, got)
	require.True(t, errors.Is(err, ErrGroupNotFound))
	require.Equal(t, http.StatusNotFound, infraerrors.Code(err))
	require.Equal(t, "GROUP_NOT_FOUND", infraerrors.Reason(err))
	require.Equal(t, "999", infraerrors.FromError(err).Metadata["group_id"])
	require.Equal(t, 1, groupRepo.existsCalls)
	require.Zero(t, groupRepo.listCalls)
	require.Zero(t, accountRepo.unscopedCalls+accountRepo.scopedCalls)
}

func TestGetGroupCapacityByIDsRejectsNonPositiveID(t *testing.T) {
	for _, ids := range [][]int64{{0}, {-1}, {10, 0}, nil, {}} {
		svc, accountRepo, groupRepo := newGroupCapacityByIDsFixture()

		_, err := svc.GetGroupCapacityByIDs(context.Background(), ids)
		require.Error(t, err, "%v", ids)
		require.Equal(t, http.StatusBadRequest, infraerrors.Code(err), "%v", ids)
		require.Equal(t, "INVALID_GROUP_ID", infraerrors.Reason(err), "%v", ids)
		require.Zero(t, groupRepo.existsCalls)
		require.Zero(t, accountRepo.unscopedCalls+accountRepo.scopedCalls)
	}
}

func TestGetGroupCapacityByIDsVendorUsesScopedLister(t *testing.T) {
	svc, accountRepo, _ := newGroupCapacityByIDsFixture()
	ctx := WithScope(context.Background(), VendorScope(7, WorkspacePermissions{GroupOps: true}))

	_, err := svc.GetGroupCapacityByIDs(ctx, []int64{20})
	require.NoError(t, err)
	require.Equal(t, 1, accountRepo.scopedCalls)
	require.Equal(t, int64(7), accountRepo.scopedWorkspaceID)
	require.Zero(t, accountRepo.unscopedCalls)
}

func TestGetGroupCapacityByIDsFallbackRejectsVendor(t *testing.T) {
	groupRepo := &groupCapacityGroupRepoStub{groupIDs: []int64{10}, existing: map[int64]bool{10: true}}
	svc := NewGroupCapacityService(&groupCapacityPlainAccountRepoStub{}, groupRepo, nil, nil, nil)
	ctx := WithScope(context.Background(), VendorScope(7, WorkspacePermissions{GroupOps: true}))

	_, err := svc.GetGroupCapacityByIDs(ctx, []int64{10})
	require.ErrorIs(t, err, domain.ErrWorkspaceScopeViolation)
}

// groupCapacityLiteGroupRepoStub 不实现 ExistsByIDs，存在性回退到逐个 GetByIDLite。
type groupCapacityLiteGroupRepoStub struct {
	GroupRepository
	groupIDs  []int64
	existing  map[int64]bool
	liteCalls []int64
}

func (s *groupCapacityLiteGroupRepoStub) ListActiveIDs(context.Context) ([]int64, error) {
	return append([]int64(nil), s.groupIDs...), nil
}

func (s *groupCapacityLiteGroupRepoStub) GetByIDLite(_ context.Context, id int64) (*Group, error) {
	s.liteCalls = append(s.liteCalls, id)
	if !s.existing[id] {
		return nil, ErrGroupNotFound
	}
	return &Group{ID: id}, nil
}

func TestGetGroupCapacityByIDsFallsBackToGetByIDLite(t *testing.T) {
	accountRepo := &groupCapacityAccountRepoStub{rows: []GroupAccountCapacityRow{{GroupID: 10, AccountID: 1, Concurrency: 3}}}
	groupRepo := &groupCapacityLiteGroupRepoStub{groupIDs: []int64{10, 20}, existing: map[int64]bool{10: true}}
	svc := NewGroupCapacityService(accountRepo, groupRepo, nil, nil, nil)

	got, err := svc.GetGroupCapacityByIDs(context.Background(), []int64{10})
	require.NoError(t, err)
	require.Equal(t, []GroupCapacitySummary{{GroupID: 10, ConcurrencyMax: 3}}, got)
	require.Equal(t, []int64{10}, groupRepo.liteCalls)

	_, err = svc.GetGroupCapacityByIDs(context.Background(), []int64{10, 20})
	require.ErrorIs(t, err, ErrGroupNotFound)
	require.Equal(t, "20", infraerrors.FromError(err).Metadata["group_id"])
}

func TestGetGroupCapacityByIDsRejectsTooManyIDs(t *testing.T) {
	svc, accountRepo, groupRepo := newGroupCapacityByIDsFixture()
	ids := make([]int64, 0, MaxCapacitySummaryGroupIDs+2)
	for i := 1; i <= MaxCapacitySummaryGroupIDs+1; i++ {
		ids = append(ids, int64(i))
	}
	ids = append(ids, 1) // 重复项不计数

	_, err := svc.GetGroupCapacityByIDs(context.Background(), ids[:MaxCapacitySummaryGroupIDs])
	require.False(t, errors.Is(err, ErrTooManyCapacityGroupIDs))

	_, err = svc.GetGroupCapacityByIDs(context.Background(), ids)
	require.ErrorIs(t, err, ErrTooManyCapacityGroupIDs)
	require.Equal(t, "TOO_MANY_IDS", infraerrors.Reason(err))
	require.Equal(t, "group_ids", infraerrors.FromError(err).Metadata["field"])
	require.Equal(t, "100", infraerrors.FromError(err).Metadata["max"])
	require.Equal(t, 1, groupRepo.existsCalls) // 只有第一次（未超限）查了存在性
	require.Zero(t, accountRepo.unscopedCalls+accountRepo.scopedCalls)
}
