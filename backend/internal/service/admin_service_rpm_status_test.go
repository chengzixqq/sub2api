//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/stretchr/testify/require"
)

type rpmStatusUserRepoStub struct {
	UserRepository
	user *User
}

func (s *rpmStatusUserRepoStub) GetByID(_ context.Context, _ int64) (*User, error) {
	return s.user, nil
}

type rpmStatusAPIKeyRepoStub struct {
	APIKeyRepository
	keys []APIKey
}

func (s *rpmStatusAPIKeyRepoStub) ListByUserID(_ context.Context, _ int64, _ pagination.PaginationParams, _ APIKeyListFilters) ([]APIKey, *pagination.PaginationResult, error) {
	return s.keys, &pagination.PaginationResult{Total: int64(len(s.keys))}, nil
}

type rpmStatusGroupRepoStub struct {
	GroupRepository
	groups map[int64]*Group
	errs   map[int64]error
}

func (s *rpmStatusGroupRepoStub) GetByIDLite(_ context.Context, id int64) (*Group, error) {
	if err := s.errs[id]; err != nil {
		return nil, err
	}
	return s.groups[id], nil
}

type rpmStatusRateRepoStub struct {
	UserGroupRateRepository
	overrides map[int64]*int
	errs      map[int64]error
}

func (s *rpmStatusRateRepoStub) GetRPMOverrideByUserAndGroup(_ context.Context, _, groupID int64) (*int, error) {
	if err := s.errs[groupID]; err != nil {
		return nil, err
	}
	return s.overrides[groupID], nil
}

type rpmStatusCacheStub struct {
	UserRPMCache
	userUsed  int
	groupUsed map[int64]int
}

func (s *rpmStatusCacheStub) IncrementUserGroupRPM(context.Context, int64, int64) (int, error) {
	return 0, nil
}

func (s *rpmStatusCacheStub) IncrementUserRPM(context.Context, int64) (int, error) {
	return 0, nil
}

func (s *rpmStatusCacheStub) GetUserGroupRPM(_ context.Context, _, groupID int64) (int, error) {
	return s.groupUsed[groupID], nil
}

func (s *rpmStatusCacheStub) GetUserRPM(context.Context, int64) (int, error) {
	return s.userUsed, nil
}

func TestAdminService_GetUserRPMStatus_AggregatesUserAndGroupLimits(t *testing.T) {
	groupOneID := int64(1)
	groupTwoID := int64(2)
	override := 7
	svc := &adminServiceImpl{
		userRepo: &rpmStatusUserRepoStub{user: &User{
			ID:       42,
			RPMLimit: 20,
		}},
		apiKeyRepo: &rpmStatusAPIKeyRepoStub{keys: []APIKey{
			{ID: 100, UserID: 42, GroupID: &groupTwoID},
			{ID: 101, UserID: 42, GroupID: &groupOneID},
			{ID: 102, UserID: 42, GroupID: &groupTwoID},
			{ID: 103, UserID: 42},
		}},
		groupRepo: &rpmStatusGroupRepoStub{groups: map[int64]*Group{
			groupOneID: {ID: groupOneID, Name: "group-one", RPMLimit: 10},
			groupTwoID: {ID: groupTwoID, Name: "group-two", RPMLimit: 60},
		}},
		userGroupRateRepo: &rpmStatusRateRepoStub{overrides: map[int64]*int{
			groupTwoID: &override,
		}},
		userRPMCache: &rpmStatusCacheStub{
			userUsed: 5,
			groupUsed: map[int64]int{
				groupOneID: 3,
				groupTwoID: 4,
			},
		},
	}

	status, err := svc.GetUserRPMStatus(context.Background(), 42)
	require.NoError(t, err)
	require.Equal(t, &UserRPMStatus{
		UserRPMUsed:    5,
		UserRPMLimit:   20,
		UserRPMTracked: true,
		PerGroup: []UserGroupRPMStatus{
			{GroupID: groupOneID, GroupName: "group-one", Used: 3, Limit: 10, Source: "group", Tracked: true},
			{GroupID: groupTwoID, GroupName: "group-two", Used: 4, Limit: 7, Source: "override", Tracked: true},
		},
	}, status)
}

func newRPMStatusServiceForTest(user *User, groups map[int64]*Group, overrides map[int64]*int, cfg *config.Config) *adminServiceImpl {
	keys := make([]APIKey, 0, len(groups))
	for id := range groups {
		groupID := id
		keys = append(keys, APIKey{ID: 1000 + groupID, UserID: user.ID, GroupID: &groupID})
	}
	return &adminServiceImpl{
		cfg:               cfg,
		userRepo:          &rpmStatusUserRepoStub{user: user},
		apiKeyRepo:        &rpmStatusAPIKeyRepoStub{keys: keys},
		groupRepo:         &rpmStatusGroupRepoStub{groups: groups},
		userGroupRateRepo: &rpmStatusRateRepoStub{overrides: overrides},
		userRPMCache:      &rpmStatusCacheStub{groupUsed: map[int64]int{}},
	}
}

func TestAdminService_GetUserRPMStatus_TrackedFalseWhenUnlimited(t *testing.T) {
	groups := map[int64]*Group{
		1: {ID: 1, Name: "a", RPMLimit: 0},
		2: {ID: 2, Name: "b", RPMLimit: 30},
		3: {ID: 3, Name: "c", RPMLimit: 0},
		4: {ID: 4, Name: "d", RPMLimit: 10},
	}
	overrides := map[int64]*int{
		2: intPtr(0),
		3: intPtr(5),
	}
	svc := newRPMStatusServiceForTest(&User{ID: 42, RPMLimit: 0}, groups, overrides, nil)

	status, err := svc.GetUserRPMStatus(context.Background(), 42)
	require.NoError(t, err)
	require.Equal(t, &UserRPMStatus{
		UserRPMUsed:    0,
		UserRPMLimit:   0,
		UserRPMTracked: false,
		PerGroup: []UserGroupRPMStatus{
			{GroupID: 1, GroupName: "a", Limit: 0, Source: "group", Tracked: false},
			{GroupID: 2, GroupName: "b", Limit: 0, Source: "override", Tracked: false},
			{GroupID: 3, GroupName: "c", Limit: 5, Source: "override", Tracked: true},
			{GroupID: 4, GroupName: "d", Limit: 10, Source: "group", Tracked: true},
		},
	}, status)
}

func TestAdminService_GetUserRPMStatus_SimpleModeNeverTracked(t *testing.T) {
	groups := map[int64]*Group{1: {ID: 1, Name: "a", RPMLimit: 10}}
	svc := newRPMStatusServiceForTest(&User{ID: 42, RPMLimit: 60}, groups, map[int64]*int{}, &config.Config{RunMode: config.RunModeSimple})

	status, err := svc.GetUserRPMStatus(context.Background(), 42)
	require.NoError(t, err)
	require.Equal(t, 60, status.UserRPMLimit)
	require.False(t, status.UserRPMTracked)
	require.Len(t, status.PerGroup, 1)
	require.Equal(t, 10, status.PerGroup[0].Limit)
	require.Equal(t, "group", status.PerGroup[0].Source)
	require.False(t, status.PerGroup[0].Tracked)
}

// TestAdminService_GetUserRPMStatus_TrackedMatchesCheckRPMCounting 保证 rpm-status 的
// tracked 与网关 checkRPM 实际是否累加计数器保持同一口径。
func TestAdminService_GetUserRPMStatus_TrackedMatchesCheckRPMCounting(t *testing.T) {
	overrideCases := []*int{nil, intPtr(0), intPtr(3)}
	for _, userLimit := range []int{0, 5} {
		for _, groupLimit := range []int{0, 10} {
			for _, override := range overrideCases {
				overrideLabel := "none"
				if override != nil {
					overrideLabel = fmt.Sprintf("%d", *override)
				}
				name := fmt.Sprintf("user=%d/group=%d/override=%s", userLimit, groupLimit, overrideLabel)
				t.Run(name, func(t *testing.T) {
					cache := &userRPMCacheStub{}
					billing := newBillingServiceForRPM(t, cache, &rpmOverrideRepoStub{override: override})
					user := &User{ID: 42, RPMLimit: userLimit}
					group := &Group{ID: 1, Name: "g", RPMLimit: groupLimit}
					require.NoError(t, billing.checkRPM(context.Background(), user, group))

					svc := newRPMStatusServiceForTest(&User{ID: 42, RPMLimit: userLimit},
						map[int64]*Group{1: group}, map[int64]*int{1: override}, &config.Config{})
					status, err := svc.GetUserRPMStatus(context.Background(), 42)
					require.NoError(t, err)
					require.Len(t, status.PerGroup, 1)
					require.Equal(t, cache.userGroupCalls > 0, status.PerGroup[0].Tracked, "group tracked")
					require.Equal(t, cache.userCalls > 0, status.UserRPMTracked, "user tracked")
				})
			}
		}
	}
}

// TestAdminService_GetUserRPMStatus_MetadataReadFailureDegrades 锁定读库失败时的降级口径
// （注释里写给 App 的约定）：接口照常返回、used 照常给出；分组读取失败且没有 override 时
// source 为空、limit=0、tracked=false；分组读取失败但 override 读到时仍以 override 为准；
// override 读取失败时回退到分组 rpm_limit（与 checkRPM 读库失败时的回退一致）。
func TestAdminService_GetUserRPMStatus_MetadataReadFailureDegrades(t *testing.T) {
	dbDown := errors.New("db down")
	groups := map[int64]*Group{
		1: {ID: 1, Name: "a", RPMLimit: 10},
		2: {ID: 2, Name: "b", RPMLimit: 10},
		3: {ID: 3, Name: "c", RPMLimit: 10},
	}
	svc := newRPMStatusServiceForTest(&User{ID: 42, RPMLimit: 0}, groups, nil, &config.Config{})
	svc.groupRepo = &rpmStatusGroupRepoStub{groups: groups, errs: map[int64]error{1: dbDown, 2: dbDown}}
	svc.userGroupRateRepo = &rpmStatusRateRepoStub{
		overrides: map[int64]*int{2: intPtr(5), 3: intPtr(0)},
		errs:      map[int64]error{3: dbDown},
	}
	svc.userRPMCache = &rpmStatusCacheStub{groupUsed: map[int64]int{1: 4, 2: 2, 3: 1}}

	status, err := svc.GetUserRPMStatus(context.Background(), 42)
	require.NoError(t, err)
	require.Equal(t, []UserGroupRPMStatus{
		{GroupID: 1, Used: 4, Limit: 0, Source: "", Tracked: false},
		{GroupID: 2, Used: 2, Limit: 5, Source: "override", Tracked: true},
		{GroupID: 3, GroupName: "c", Used: 1, Limit: 10, Source: "group", Tracked: true},
	}, status.PerGroup)
}

func TestUserRPMStatus_JSONIncludesTrackedFalse(t *testing.T) {
	raw, err := json.Marshal(UserRPMStatus{PerGroup: []UserGroupRPMStatus{{GroupID: 1}}})
	require.NoError(t, err)
	require.Contains(t, string(raw), `"user_rpm_tracked":false`)
	require.Contains(t, string(raw), `"tracked":false`)
}
