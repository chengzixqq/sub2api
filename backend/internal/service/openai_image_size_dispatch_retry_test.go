//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/stretchr/testify/require"
)

type imageSizeDispatchFreshRepo struct {
	schedulerTestOpenAIAccountRepo
	fresh map[int64]*Account
	err   error
	errAt map[int64]int
	reads map[int64]int
}

func (r *imageSizeDispatchFreshRepo) GetByID(ctx context.Context, id int64) (*Account, error) {
	if r.reads == nil {
		r.reads = make(map[int64]int)
	}
	r.reads[id]++
	if r.err != nil && (r.errAt == nil || (r.errAt[id] > 0 && r.reads[id] >= r.errAt[id])) {
		return nil, r.err
	}
	if account, ok := r.fresh[id]; ok {
		return account, nil
	}
	return nil, ErrAccountNotFound
}

func (r *imageSizeDispatchFreshRepo) GetAccountDispatchPolicy(context.Context, int64, int64) (*AccountDispatchPolicy, error) {
	return nil, nil
}

func TestOpenAIImageSizeDispatchRetry_StaleCandidateIsAcquiredAtMostOnce(t *testing.T) {
	groupID := int64(93101)
	stale := newOpenAIImageSizeTestAccount(81201, 0)
	healthy := newOpenAIImageSizeTestAccount(81202, 1, "4K")
	stale.GroupIDs, healthy.GroupIDs = []int64{groupID}, []int64{groupID}
	current := stale
	current.Schedulable = false
	repo := &imageSizeDispatchFreshRepo{fresh: map[int64]*Account{stale.ID: &current, healthy.ID: &healthy}}
	cache := &snapshotHydrationCache{snapshot: []*Account{&stale, &healthy}, accounts: map[int64]*Account{stale.ID: &stale, healthy.ID: &healthy}}
	var acquired, released []int64
	svc := newOpenAIImageSizeSchedulerTestService(openAIImageSizeSchedulerModes[0], nil, nil, schedulerTestConcurrencyCache{acquiredIDs: &acquired, releasedIDs: &released})
	svc.accountRepo = repo
	svc.schedulerSnapshot = NewSchedulerSnapshotService(cache, nil, repo, nil, svc.cfg)
	scheduler := newDefaultOpenAIAccountScheduler(svc, nil)
	ctx, retry := withAccountDispatchCandidateRetry(WithOpenAIImageSizeRouting(context.Background(), "auto"), nil)
	passes := 0
	selection, _, err := selectOpenAIAccountByImageSizePasses(ctx, func(passCtx context.Context) (*AccountSelectionResult, OpenAIAccountScheduleDecision, error) {
		passes++
		return scheduler.Select(passCtx, OpenAIAccountScheduleRequest{
			GroupID: &groupID, Platform: PlatformOpenAI, RequestedModel: "gpt-image-2",
			RequiredImageCapability: OpenAIImagesCapabilityNative, RequiredTransport: OpenAIUpstreamTransportHTTPSSE,
			StickyAccountID: stale.ID, StickyWeighted: true, ExcludedIDs: retry.excluded,
		})
	})
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.Equal(t, healthy.ID, selection.Account.ID)
	require.Equal(t, 2, passes)
	require.Equal(t, []int64{stale.ID, healthy.ID}, acquired)
	require.Equal(t, []int64{stale.ID}, released)
	selection.ReleaseFunc()
}

func TestOpenAIImageSizeDispatchRetry_ReadFailureStopsSelection(t *testing.T) {
	groupID := int64(93102)
	first := newOpenAIImageSizeTestAccount(81211, 0)
	other := newOpenAIImageSizeTestAccount(81212, 1, "4K")
	first.GroupIDs, other.GroupIDs = []int64{groupID}, []int64{groupID}
	readErr := errors.New("database read failed")
	repo := &imageSizeDispatchFreshRepo{err: readErr}
	cache := &snapshotHydrationCache{snapshot: []*Account{&first, &other}, accounts: map[int64]*Account{first.ID: &first, other.ID: &other}}
	var acquired, released []int64
	svc := newOpenAIImageSizeSchedulerTestService(openAIImageSizeSchedulerModes[0], nil, nil, schedulerTestConcurrencyCache{acquiredIDs: &acquired, releasedIDs: &released})
	svc.accountRepo = repo
	svc.schedulerSnapshot = NewSchedulerSnapshotService(cache, nil, repo, nil, svc.cfg)
	selection, _, err := svc.SelectAccountWithSchedulerForImages(WithOpenAIImageSizeRouting(context.Background(), "auto"), &groupID, "", "gpt-image-2", nil, OpenAIImagesCapabilityNative)
	require.ErrorIs(t, err, readErr)
	require.Nil(t, selection)
	require.Equal(t, []int64{first.ID}, acquired)
	require.Equal(t, []int64{first.ID}, released)
}

func TestOpenAIImageSizeDispatchRetry_ReadFailureDoesNotUseEarlierWaitPlan(t *testing.T) {
	later := newOpenAIImageSizeTestAccount(81221, 0, "4K")
	readErr := errors.New("database read failed")
	passes := 0
	selection, _, err := selectOpenAIAccountByImageSizePasses(WithOpenAIImageSizeRouting(context.Background(), "auto"), func(ctx context.Context) (*AccountSelectionResult, OpenAIAccountScheduleDecision, error) {
		passes++
		_, _ = openAIImageSizeVetoReason(ctx, &later)
		if passes == 1 {
			return &AccountSelectionResult{Account: &Account{ID: 81222}, WaitPlan: &AccountWaitPlan{AccountID: 81222}}, OpenAIAccountScheduleDecision{}, nil
		}
		return nil, OpenAIAccountScheduleDecision{}, readErr
	})
	require.ErrorIs(t, err, readErr)
	require.Nil(t, selection)
	require.Equal(t, 2, passes)
}

func TestOpenAIImageSizeDispatchRetry_PassMismatchIsNotPermanentlyExcluded(t *testing.T) {
	groupID := int64(93103)
	snapshot := newOpenAIImageSizeTestAccount(81231, 0)
	current := newOpenAIImageSizeTestAccount(snapshot.ID, 0, "4K")
	snapshot.GroupIDs, current.GroupIDs = []int64{groupID}, []int64{groupID}
	repo := &imageSizeDispatchFreshRepo{fresh: map[int64]*Account{current.ID: &current}}
	svc := &OpenAIGatewayService{accountRepo: repo, schedulerSnapshot: &SchedulerSnapshotService{}}
	ctx, retry := withAccountDispatchCandidateRetry(context.Background(), nil)
	firstCtx := context.WithValue(ctx, openAIImageSizePassCtxKey{}, &openAIImageSizePass{rank: 0})
	account, err := svc.recheckSelectedOpenAIAccountFromDBResult(firstCtx, &snapshot, &groupID, PlatformOpenAI, "gpt-image-2", false, "")
	require.NoError(t, err)
	require.Nil(t, account)
	require.Empty(t, retry.excluded)
	secondCtx := context.WithValue(ctx, openAIImageSizePassCtxKey{}, &openAIImageSizePass{rank: 1})
	account, err = svc.recheckSelectedOpenAIAccountFromDBResult(secondCtx, &snapshot, &groupID, PlatformOpenAI, "gpt-image-2", false, "")
	require.NoError(t, err)
	require.Equal(t, current.ID, account.ID)
	require.Empty(t, retry.excluded)
}

func TestOpenAILegacyDispatchRetry_ReadFailureStopsSelection(t *testing.T) {
	for _, mode := range openAIImageSizeSchedulerModes[1:] {
		for _, afterAcquire := range []bool{false, true} {
			name := mode.name + "/before_acquire"
			if afterAcquire {
				name = mode.name + "/after_acquire"
			}
			t.Run(name, func(t *testing.T) {
				groupID := int64(93104)
				first := newOpenAIImageSizeTestAccount(81241, 0)
				other := newOpenAIImageSizeTestAccount(81242, 1)
				first.GroupIDs, other.GroupIDs = []int64{groupID}, []int64{groupID}
				readErr := errors.New("database read failed")
				failAt := 1
				if afterAcquire {
					failAt = 2
					if !mode.loadBatch {
						// Sequential selection hydrates once before acquiring its slot.
						failAt = 3
					}
				}
				repo := &imageSizeDispatchFreshRepo{
					fresh: map[int64]*Account{first.ID: &first, other.ID: &other},
					err:   readErr, errAt: map[int64]int{first.ID: failAt},
				}
				cache := &snapshotHydrationCache{snapshot: []*Account{&first, &other}, accounts: map[int64]*Account{first.ID: &first, other.ID: &other}}
				var acquired, released []int64
				svc := newOpenAIImageSizeSchedulerTestService(mode, nil, nil, schedulerTestConcurrencyCache{acquiredIDs: &acquired, releasedIDs: &released})
				svc.accountRepo = repo
				svc.schedulerSnapshot = NewSchedulerSnapshotService(cache, nil, repo, nil, svc.cfg)
				ctx := context.WithValue(context.Background(), ctxkey.UserID, int64(101))
				selection, _, err := svc.SelectAccountWithSchedulerForImages(WithOpenAIImageSizeRouting(ctx, "auto"), &groupID, "", "gpt-image-2", nil, OpenAIImagesCapabilityNative)
				require.ErrorIs(t, err, readErr)
				require.Nil(t, selection)
				require.Equal(t, failAt, repo.reads[first.ID])
				if afterAcquire {
					require.Equal(t, []int64{first.ID}, acquired)
					require.Equal(t, []int64{first.ID}, released)
				} else {
					require.Empty(t, acquired)
					require.Empty(t, released)
					require.Zero(t, repo.reads[other.ID], "a lookup failure must not try another candidate")
				}
			})
		}
	}
}

func TestOpenAIDispatchRetry_StickyReadFailureStopsSelection(t *testing.T) {
	for _, mode := range openAIImageSizeSchedulerModes {
		t.Run(mode.name, func(t *testing.T) {
			groupID := int64(93105)
			first := newOpenAIImageSizeTestAccount(81251, 0)
			other := newOpenAIImageSizeTestAccount(81252, 1)
			first.GroupIDs, other.GroupIDs = []int64{groupID}, []int64{groupID}
			readErr := errors.New("sticky database read failed")
			repo := &imageSizeDispatchFreshRepo{fresh: map[int64]*Account{other.ID: &other}, err: readErr, errAt: map[int64]int{first.ID: 1}}
			cache := &snapshotHydrationCache{snapshot: []*Account{&first, &other}, accounts: map[int64]*Account{first.ID: &first, other.ID: &other}}
			var acquired, released []int64
			stickyCache := &schedulerTestGatewayCache{sessionBindings: map[string]int64{"sticky": first.ID}}
			svc := newOpenAIImageSizeSchedulerTestService(mode, nil, stickyCache, schedulerTestConcurrencyCache{acquiredIDs: &acquired, releasedIDs: &released})
			svc.accountRepo = repo
			svc.schedulerSnapshot = NewSchedulerSnapshotService(cache, nil, repo, nil, svc.cfg)
			ctx := WithOpenAIImageSizeRouting(context.Background(), "auto")
			var selection *AccountSelectionResult
			var err error
			if mode.advanced {
				scheduler := newDefaultOpenAIAccountScheduler(svc, nil)
				selection, _, err = scheduler.Select(ctx, OpenAIAccountScheduleRequest{
					GroupID: &groupID, Platform: PlatformOpenAI, RequestedModel: "gpt-image-2",
					SessionHash: "sticky", StickyAccountID: first.ID, RequiredImageCapability: OpenAIImagesCapabilityNative,
				})
			} else {
				selection, _, err = svc.SelectAccountWithSchedulerForImages(ctx, &groupID, "sticky", "gpt-image-2", nil, OpenAIImagesCapabilityNative)
			}
			require.ErrorIs(t, err, readErr)
			require.Nil(t, selection)
			require.Empty(t, acquired)
			require.Empty(t, released)
			require.Equal(t, 1, repo.reads[first.ID])
			require.Zero(t, repo.reads[other.ID])
			require.Equal(t, first.ID, stickyCache.sessionBindings["sticky"], "a read failure must not revoke the sticky binding")
		})
	}
}

func TestOpenAIDispatchRetry_PreviousResponseReadFailureStopsSelection(t *testing.T) {
	groupID := int64(93106)
	account := newOpenAIImageSizeTestAccount(81261, 0)
	account.GroupIDs = []int64{groupID}
	readErr := errors.New("previous response database read failed")
	repo := &imageSizeDispatchFreshRepo{err: readErr}
	cache := &snapshotHydrationCache{snapshot: []*Account{&account}, accounts: map[int64]*Account{account.ID: &account}}
	var acquired []int64
	svc := newOpenAIImageSizeSchedulerTestService(openAIImageSizeSchedulerModes[0], nil, nil, schedulerTestConcurrencyCache{acquiredIDs: &acquired})
	svc.accountRepo = repo
	svc.schedulerSnapshot = NewSchedulerSnapshotService(cache, nil, repo, nil, svc.cfg)
	store := NewOpenAIWSStateStore(nil)
	svc.openaiWSStateStore = store
	require.NoError(t, store.BindResponseAccount(context.Background(), groupID, "resp_read_error", account.ID, time.Hour))
	selection, err := svc.SelectAccountByPreviousResponseID(context.Background(), &groupID, "resp_read_error", "gpt-image-2", nil, false)
	require.ErrorIs(t, err, readErr)
	require.Nil(t, selection)
	accountID, err := svc.ResolveAccountIDByPreviousResponseIDForScheduler(context.Background(), &groupID, "resp_read_error", "gpt-image-2", nil, "", false)
	require.ErrorIs(t, err, readErr)
	require.Zero(t, accountID)
	require.Empty(t, acquired)
	boundID, err := store.GetResponseAccount(context.Background(), groupID, "resp_read_error")
	require.NoError(t, err)
	require.Equal(t, account.ID, boundID)
}

func TestOpenAIDispatchRetry_SubscriptionWaitReadFailureStopsSelection(t *testing.T) {
	for _, failSubscription := range []bool{false, true} {
		name := "regular_pool"
		if failSubscription {
			name = "subscription_pool"
		}
		t.Run(name, func(t *testing.T) {
			groupID := int64(93107)
			subscription := newOpenAIImageSizeTestAccount(81271, 0)
			subscription.Type = AccountTypeOAuth
			subscription.Credentials = map[string]any{"plan_type": "plus"}
			regular := newOpenAIImageSizeTestAccount(81272, 1)
			subscription.GroupIDs, regular.GroupIDs = []int64{groupID}, []int64{groupID}
			readErr := errors.New("waiting account database read failed")
			failID := regular.ID
			currentRegular := regular
			if failSubscription {
				failID = subscription.ID
				currentRegular.Schedulable = false
			}
			repo := &imageSizeDispatchFreshRepo{
				fresh: map[int64]*Account{subscription.ID: &subscription, regular.ID: &currentRegular},
				err:   readErr, errAt: map[int64]int{failID: 1},
			}
			cache := &snapshotHydrationCache{snapshot: []*Account{&subscription, &regular}, accounts: map[int64]*Account{subscription.ID: &subscription, regular.ID: &regular}}
			var acquired []int64
			svc := newOpenAIImageSizeSchedulerTestService(openAIImageSizeSchedulerModes[0], nil, nil, schedulerTestConcurrencyCache{
				acquiredIDs: &acquired,
				loadMap: map[int64]*AccountLoadInfo{
					subscription.ID: {AccountID: subscription.ID, CurrentConcurrency: 1, LoadRate: 100},
					regular.ID:      {AccountID: regular.ID, CurrentConcurrency: 1, LoadRate: 100},
				},
			})
			svc.accountRepo = repo
			svc.schedulerSnapshot = NewSchedulerSnapshotService(cache, nil, repo, nil, svc.cfg)
			scheduler := newDefaultOpenAIAccountScheduler(svc, nil)
			selection, _, err := scheduler.Select(context.Background(), OpenAIAccountScheduleRequest{
				GroupID: &groupID, Platform: PlatformOpenAI, RequestedModel: "gpt-5.1", SubscriptionPriority: true,
			})
			require.ErrorIs(t, err, readErr)
			require.Nil(t, selection)
			require.Empty(t, acquired)
			require.Equal(t, 1, repo.reads[failID])
			if !failSubscription {
				require.Zero(t, repo.reads[subscription.ID], "a regular-pool read failure must not try the subscription wait fallback")
			}
		})
	}
}
