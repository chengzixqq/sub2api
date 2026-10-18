//go:build unit

package service

import (
	"context"
	"errors"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/stretchr/testify/require"
)

func adminCtx() context.Context { return WithScope(context.Background(), AdminScope()) }

func TestAdminDeleteAPIKey_DeletesAndInvalidatesAuthCache(t *testing.T) {
	repo := &apiKeyRepoStub{apiKey: &APIKey{ID: 4087, UserID: 1, Key: "sk-live-4087"}}
	cache := &authCacheInvalidatorStub{}
	svc := &adminServiceImpl{apiKeyRepo: repo, authCacheInvalidator: cache}

	require.NoError(t, svc.AdminDeleteAPIKey(adminCtx(), 4087))
	require.Equal(t, []int64{4087}, repo.deletedIDs)
	require.Equal(t, []string{"sk-live-4087"}, cache.keys)
}

// Deleting twice succeeds: the repository treats an already soft-deleted key as done.
func TestAdminDeleteAPIKey_AlreadyDeletedIsIdempotent(t *testing.T) {
	repo := &apiKeyRepoStub{getByIDErr: ErrAPIKeyNotFound}
	cache := &authCacheInvalidatorStub{}
	svc := &adminServiceImpl{apiKeyRepo: repo, authCacheInvalidator: cache}

	require.NoError(t, svc.AdminDeleteAPIKey(adminCtx(), 4087))
	require.Equal(t, []int64{4087}, repo.deletedIDs, "the repository decides between already-deleted and never-existed")
	require.Empty(t, cache.keys)
}

func TestAdminDeleteAPIKey_UnknownKey(t *testing.T) {
	repo := &apiKeyRepoStub{getByIDErr: ErrAPIKeyNotFound, deleteErr: ErrAPIKeyNotFound}
	svc := &adminServiceImpl{apiKeyRepo: repo}

	require.ErrorIs(t, svc.AdminDeleteAPIKey(adminCtx(), 999), ErrAPIKeyNotFound)
}

func TestAdminDeleteAPIKey_LookupFailureStops(t *testing.T) {
	repo := &apiKeyRepoStub{getByIDErr: errors.New("db down")}
	svc := &adminServiceImpl{apiKeyRepo: repo}

	require.Error(t, svc.AdminDeleteAPIKey(adminCtx(), 4087))
	require.Empty(t, repo.deletedIDs)
}

// End-user keys are station-owner data: vendors are refused even if a future route exposes the method.
func TestAdminDeleteAPIKey_VendorScopeIsRefused(t *testing.T) {
	repo := &apiKeyRepoStub{apiKey: &APIKey{ID: 4087, Key: "sk"}}
	svc := &adminServiceImpl{apiKeyRepo: repo}

	vendor := WithScope(context.Background(), VendorScope(3, WorkspacePermissions{AccountManage: true}))
	require.ErrorIs(t, svc.AdminDeleteAPIKey(vendor, 4087), domain.ErrWorkspaceScopeViolation)
	require.Empty(t, repo.deletedIDs)
}
