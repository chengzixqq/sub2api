//go:build unit

package service

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/stretchr/testify/require"
)

type legacyStickyReadRepo struct {
	AccountRepository
	accounts  []Account
	lookupErr error
	failAfter int
	reads     map[int64]int
	listCalls int
	setErrors []int64
}

func (r *legacyStickyReadRepo) GetByID(_ context.Context, id int64) (*Account, error) {
	if r.reads == nil {
		r.reads = make(map[int64]int)
	}
	r.reads[id]++
	if id == 1 && r.reads[id] > r.failAfter {
		return nil, r.lookupErr
	}
	for i := range r.accounts {
		if r.accounts[i].ID == id {
			return &r.accounts[i], nil
		}
	}
	return nil, ErrAccountNotFound
}

func (r *legacyStickyReadRepo) ListSchedulableByGroupIDAndPlatform(context.Context, int64, string) ([]Account, error) {
	r.listCalls++
	return r.accounts, nil
}

func (r *legacyStickyReadRepo) ListSchedulableByGroupIDAndPlatforms(context.Context, int64, []string) ([]Account, error) {
	r.listCalls++
	return r.accounts, nil
}

func (r *legacyStickyReadRepo) ListSchedulableUngroupedByPlatforms(context.Context, []string) ([]Account, error) {
	r.listCalls++
	return r.accounts, nil
}

func (r *legacyStickyReadRepo) SetError(_ context.Context, id int64, _ string) error {
	r.setErrors = append(r.setErrors, id)
	return nil
}

func TestGatewayService_LegacyStickyReadFailure(t *testing.T) {
	for _, mixed := range []bool{false, true} {
		for _, routed := range []bool{false, true} {
			for _, missing := range []bool{false, true} {
				t.Run(fmt.Sprintf("mixed=%t/routed=%t/missing=%t", mixed, routed, missing), func(t *testing.T) {
					groupID := int64(10)
					lookupErr := errors.New("account database unavailable")
					if missing {
						lookupErr = ErrAccountNotFound
					}
					repo := &legacyStickyReadRepo{lookupErr: lookupErr, accounts: []Account{
						{ID: 1, Platform: PlatformAnthropic, Status: StatusActive, Schedulable: true, GroupIDs: []int64{groupID}, Priority: 0},
						{ID: 2, Platform: PlatformAnthropic, Status: StatusActive, Schedulable: true, GroupIDs: []int64{groupID}, Priority: 1},
					}}
					group := &Group{ID: groupID, Platform: PlatformAnthropic, Status: StatusActive, Hydrated: true}
					if routed {
						group.ModelRoutingEnabled = true
						group.ModelRouting = map[string][]int64{"claude-sonnet-4-5": {1, 2}}
					}
					cache := &mockGatewayCacheForPlatform{sessionBindings: map[string]int64{"session": 1}}
					svc := &GatewayService{accountRepo: repo, cache: cache, cfg: testConfig(), groupRepo: &mockGroupRepoForGateway{groups: map[int64]*Group{groupID: group}}}
					ctx := context.WithValue(context.Background(), ctxkey.Group, group)
					var selected *Account
					var err error
					if mixed {
						selected, err = svc.selectAccountWithMixedScheduling(ctx, &groupID, "session", "claude-sonnet-4-5", nil, PlatformAnthropic)
					} else {
						selected, err = svc.selectAccountForModelWithPlatform(ctx, &groupID, "session", "claude-sonnet-4-5", nil, PlatformAnthropic)
					}
					if missing {
						require.NoError(t, err)
						require.NotNil(t, selected)
						require.Equal(t, int64(2), selected.ID)
						require.Equal(t, 1, cache.deletedSessions["session"])
					} else {
						require.ErrorIs(t, err, lookupErr)
						require.Nil(t, selected)
						require.Zero(t, repo.listCalls, "database errors must not fall through to the shared pool")
						require.Empty(t, cache.deletedSessions)
					}
					require.Equal(t, 1, repo.reads[1], "a stale sticky candidate must not be retried in the same request")
				})
			}
		}
	}
}

type legacyStickyGroupReadRepo struct {
	GroupRepository
	err error
}

func (r *legacyStickyGroupReadRepo) GetByIDLite(context.Context, int64) (*Group, error) {
	return nil, r.err
}

func TestGatewayService_LegacyPrivacyGroupReadFailure(t *testing.T) {
	for _, mixed := range []bool{false, true} {
		t.Run(fmt.Sprintf("mixed=%t", mixed), func(t *testing.T) {
			groupID := int64(10)
			lookupErr := errors.New("group database unavailable")
			repo := &legacyStickyReadRepo{accounts: []Account{{ID: 2, Platform: PlatformAnthropic, Status: StatusActive, Schedulable: true}}}
			svc := &GatewayService{accountRepo: repo, cfg: testConfig(), groupRepo: &legacyStickyGroupReadRepo{err: lookupErr}}
			var selected *Account
			var err error
			if mixed {
				selected, err = svc.selectAccountWithMixedScheduling(context.Background(), &groupID, "", "", nil, PlatformAnthropic)
			} else {
				selected, err = svc.selectAccountForModelWithPlatform(context.Background(), &groupID, "", "", nil, PlatformAnthropic)
			}
			require.ErrorIs(t, err, lookupErr)
			require.Nil(t, selected)
			require.Zero(t, repo.listCalls)
		})
	}
}

func TestGatewayService_LegacyStickyRequiresPrivacy(t *testing.T) {
	for _, mixed := range []bool{false, true} {
		for _, routed := range []bool{false, true} {
			for _, requirePrivacy := range []bool{false, true} {
				t.Run(fmt.Sprintf("mixed=%t/routed=%t/required=%t", mixed, routed, requirePrivacy), func(t *testing.T) {
					groupID := int64(10)
					platform, model := PlatformOpenAI, "gpt-4"
					accounts := []Account{
						{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, GroupIDs: []int64{groupID}, Priority: 0},
						{ID: 2, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, GroupIDs: []int64{groupID}, Priority: 1, Extra: map[string]any{"privacy_mode": PrivacyModeTrainingOff}},
					}
					if mixed {
						platform, model = PlatformAnthropic, "claude-sonnet-4-5"
						accounts[0].Platform = PlatformAntigravity
						accounts[0].Extra = map[string]any{"mixed_scheduling": true}
						accounts[1].Platform = PlatformAnthropic
					}
					repo := &legacyStickyReadRepo{failAfter: 10, accounts: accounts}
					group := &Group{ID: groupID, Platform: platform, Status: StatusActive, Hydrated: true, RequirePrivacySet: requirePrivacy}
					if routed {
						group.ModelRoutingEnabled = true
						group.ModelRouting = map[string][]int64{model: {1, 2}}
					}
					cache := &mockGatewayCacheForPlatform{sessionBindings: map[string]int64{"session": 1}}
					svc := &GatewayService{accountRepo: repo, cache: cache, cfg: testConfig(), groupRepo: &mockGroupRepoForGateway{groups: map[int64]*Group{groupID: group}}}
					ctx := context.WithValue(context.Background(), ctxkey.Group, group)
					var selected *Account
					var err error
					if mixed {
						selected, err = svc.selectAccountWithMixedScheduling(ctx, &groupID, "session", model, nil, platform)
					} else {
						selected, err = svc.selectAccountForModelWithPlatform(ctx, &groupID, "session", model, nil, platform)
					}
					require.NoError(t, err)
					require.NotNil(t, selected)
					if requirePrivacy {
						require.Equal(t, int64(2), selected.ID, "sticky must obey the same group privacy admission as the candidate pool")
						require.Contains(t, repo.setErrors, int64(1))
					} else {
						require.Equal(t, int64(1), selected.ID, "groups without the privacy requirement must retain their sticky behavior")
						require.Empty(t, repo.setErrors)
					}
				})
			}
		}
	}
}

func TestGeminiMessagesCompatService_StickyReadFailure(t *testing.T) {
	for _, failAfter := range []int{0, 1} {
		for _, missing := range []bool{false, true} {
			t.Run(fmt.Sprintf("successful_reads=%d/missing=%t", failAfter, missing), func(t *testing.T) {
				lookupErr := errors.New("Gemini account database unavailable")
				if missing {
					lookupErr = ErrAccountNotFound
				}
				repo := &legacyStickyReadRepo{lookupErr: lookupErr, failAfter: failAfter, accounts: []Account{
					{ID: 1, Platform: PlatformGemini, Status: StatusActive, Schedulable: true, Priority: 0},
					{ID: 2, Platform: PlatformGemini, Status: StatusActive, Schedulable: true, Priority: 1},
				}}
				cache := &mockGatewayCacheForGemini{sessionBindings: map[string]int64{"gemini:session": 1}}
				svc := &GeminiMessagesCompatService{accountRepo: repo, cache: cache}
				selected, err := svc.SelectAccountForModelWithExclusions(context.Background(), nil, "session", "gemini-2.5-flash", nil)
				if missing {
					require.NoError(t, err)
					require.NotNil(t, selected)
					require.Equal(t, int64(2), selected.ID)
					require.Equal(t, 1, cache.deletedSessions["gemini:session"])
				} else {
					require.ErrorIs(t, err, lookupErr)
					require.Nil(t, selected)
					require.Zero(t, repo.listCalls, "database errors must not fall through to the shared pool")
					require.Empty(t, cache.deletedSessions)
				}
				require.Equal(t, failAfter+1, repo.reads[1])
			})
		}
	}
}
