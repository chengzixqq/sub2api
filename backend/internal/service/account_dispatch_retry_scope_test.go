//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAccountDispatchRetryScope_StaleCandidateSurvivesRepeatedSelection(t *testing.T) {
	groupID := int64(93108)
	stale := newOpenAIImageSizeTestAccount(81281, 0)
	healthy := newOpenAIImageSizeTestAccount(81282, 1, "4K")
	stale.GroupIDs, healthy.GroupIDs = []int64{groupID}, []int64{groupID}
	current := stale
	current.Schedulable = false
	repo := &imageSizeDispatchFreshRepo{fresh: map[int64]*Account{stale.ID: &current, healthy.ID: &healthy}}
	cache := &snapshotHydrationCache{snapshot: []*Account{&stale, &healthy}, accounts: map[int64]*Account{stale.ID: &stale, healthy.ID: &healthy}}
	var acquired, released []int64
	svc := newOpenAIImageSizeSchedulerTestService(openAIImageSizeSchedulerModes[0], nil, nil, schedulerTestConcurrencyCache{acquiredIDs: &acquired, releasedIDs: &released})
	svc.accountRepo = repo
	svc.schedulerSnapshot = NewSchedulerSnapshotService(cache, nil, repo, nil, svc.cfg)
	ctx := WithAccountDispatchRetryScope(WithOpenAIImageSizeRouting(context.Background(), "auto"))
	selectAccount := func(ctx context.Context, failedIDs map[int64]struct{}) {
		t.Helper()
		selection, _, err := svc.SelectAccountWithSchedulerForImages(ctx, &groupID, "", "gpt-image-2", failedIDs, OpenAIImagesCapabilityNative)
		require.NoError(t, err)
		require.Equal(t, healthy.ID, selection.Account.ID)
		require.True(t, selection.Acquired)
		selection.ReleaseFunc()
	}
	selectAccount(ctx, map[int64]struct{}{99999: {}})
	// The handler may rebuild its transient failure list after backoff.
	selectAccount(ctx, map[int64]struct{}{})
	require.Equal(t, []int64{stale.ID, healthy.ID, healthy.ID}, acquired)
	require.Equal(t, acquired, released)
	require.Equal(t, 1, repo.reads[stale.ID])

	// A new request, WebSocket turn, or batch subrequest gets independent state.
	selectAccount(WithAccountDispatchRetryScope(ctx), map[int64]struct{}{})
	require.Equal(t, []int64{stale.ID, healthy.ID, healthy.ID, stale.ID, healthy.ID}, acquired)
	require.Equal(t, acquired, released)
	require.Equal(t, 2, repo.reads[stale.ID])
}

func TestAccountDispatchRetryScope_TransientInputExclusionCanBeCleared(t *testing.T) {
	groupID := int64(93109)
	account := newOpenAIImageSizeTestAccount(81291, 0)
	account.GroupIDs = []int64{groupID}
	var acquired []int64
	svc := newOpenAIImageSizeSchedulerTestService(openAIImageSizeSchedulerModes[0], []Account{account}, nil, schedulerTestConcurrencyCache{acquiredIDs: &acquired})
	ctx := WithAccountDispatchRetryScope(context.Background())
	selection, _, err := svc.SelectAccountWithSchedulerForImages(ctx, &groupID, "", "gpt-image-2", map[int64]struct{}{account.ID: {}}, OpenAIImagesCapabilityNative)
	require.ErrorIs(t, err, ErrNoAvailableAccounts)
	require.Nil(t, selection)
	require.Empty(t, acquired)
	selection, _, err = svc.SelectAccountWithSchedulerForImages(ctx, &groupID, "", "gpt-image-2", map[int64]struct{}{}, OpenAIImagesCapabilityNative)
	require.NoError(t, err)
	require.Equal(t, account.ID, selection.Account.ID)
	require.Equal(t, []int64{account.ID}, acquired)
	selection.ReleaseFunc()
}

func TestAccountDispatchRetryScope_TypedRejectionAndNestedScopeIsolation(t *testing.T) {
	parent := WithAccountDispatchRetryScope(context.Background())
	selectionCtx, retry := withAccountDispatchCandidateRetry(parent, nil)
	again, err := retry.retry(selectionCtx, rejectAccountDispatchCandidate(11, "account was removed"))
	require.NoError(t, err)
	require.True(t, again)
	_, next := withAccountDispatchCandidateRetry(parent, map[int64]struct{}{})
	require.Contains(t, next.excluded, int64(11))

	child := WithAccountDispatchRetryScope(selectionCtx)
	childSelectionCtx, childRetry := withAccountDispatchCandidateRetry(child, nil)
	require.Empty(t, childRetry.excluded)
	excludeStaleAccountDispatchCandidate(childSelectionCtx, 12)
	_, parentAfterChild := withAccountDispatchCandidateRetry(parent, nil)
	require.Contains(t, parentAfterChild.excluded, int64(11))
	require.NotContains(t, parentAfterChild.excluded, int64(12))
}
