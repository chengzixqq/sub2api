//go:build integration

package repository

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func (s *AccountRepoSuite) newPatchAdminService() service.AdminService {
	s.repo.schedulerCache = &schedulerCacheRecorder{}
	// The suite already supplies a transaction-backed repository for isolation.
	// Standalone tests below use a root client to verify PATCH commit/rollback.
	return service.NewAdminService(
		nil, nil, nil, s.repo, nil, nil, nil, nil, nil, nil, nil,
		nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
	)
}

type failAccountBillingSettingsUpdateRepo struct {
	*accountRepository
	err error
}

func (r *failAccountBillingSettingsUpdateRepo) UpdateWithAccountBillingSettings(
	ctx context.Context,
	account *service.Account,
	probeEnabled *bool,
	rateSyncEnabled *bool,
	rateMultiplier *float64,
) error {
	if err := r.accountRepository.UpdateWithAccountBillingSettings(ctx, account, probeEnabled, rateSyncEnabled, rateMultiplier); err != nil {
		return err
	}
	return r.err
}

func newPatchAdminServiceForIntegration(client *dbent.Client, accountRepo service.AdminAccountRepository) service.AdminService {
	return service.NewAdminService(
		nil, nil, NewAdminGroupRepository(client, integrationDB), accountRepo, nil, nil, nil, nil, nil, nil, nil,
		nil, nil, nil, client, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
	)
}

func cleanupPatchTestAccounts(t *testing.T, client *dbent.Client, accountIDs ...int64) {
	t.Helper()
	ctx := context.Background()
	for _, id := range accountIDs {
		_, _ = integrationDB.ExecContext(ctx, "DELETE FROM scheduler_outbox WHERE account_id = $1", id)
		_ = client.Account.DeleteOneID(id).Exec(ctx)
	}
}

func (s *AccountRepoSuite) TestPatchAccount_MergesKeysAndKeepsRuntimeState() {
	account := mustCreateAccount(s.T(), s.client, &service.Account{
		Name:     "patch-merge",
		Platform: service.PlatformAnthropic,
		Type:     service.AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_key":            "sk-original",
			"base_url":           "https://old.example.com",
			"custom_error_codes": []any{529},
		},
		Extra: map[string]any{"pool_mode": true, "base_rpm": 30},
	})
	svc := s.newPatchAdminService()
	got, err := s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	startVersion := got.ConfigVersion

	// The gateway records a model rate limit after the client loaded the account.
	s.Require().NoError(s.repo.SetModelRateLimit(s.ctx, account.ID, "claude-sonnet-4-5", time.Now().Add(time.Hour)))

	priority := 7
	patched, err := svc.PatchAccount(s.ctx, account.ID, &service.PatchAccountInput{
		Fields:                service.UpdateAccountInput{Priority: &priority},
		CredentialsPatch:      map[string]any{"base_url": "https://new.example.com", "custom_error_codes": nil},
		ExtraPatch:            map[string]any{"pool_mode": false},
		ExpectedConfigVersion: &startVersion,
	})
	s.Require().NoError(err)
	s.Require().Equal(startVersion+1, patched.ConfigVersion)

	stored, err := s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	s.Require().Equal(7, stored.Priority)
	s.Require().Equal("sk-original", stored.Credentials["api_key"])
	s.Require().Equal("https://new.example.com", stored.Credentials["base_url"])
	s.Require().NotContains(stored.Credentials, "custom_error_codes")
	s.Require().Equal(false, stored.Extra["pool_mode"])
	s.Require().EqualValues(30, stored.Extra["base_rpm"])
	s.Require().Contains(stored.Extra, "model_rate_limits", "runtime rate limit written meanwhile must survive the patch")
}

func (s *AccountRepoSuite) TestPatchAccount_StaleVersionIsRejected() {
	account := mustCreateAccount(s.T(), s.client, &service.Account{Name: "patch-stale", Type: service.AccountTypeAPIKey, Credentials: map[string]any{"api_key": "sk"}})
	svc := s.newPatchAdminService()
	got, err := s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	stale := got.ConfigVersion

	// Somebody else saves the account.
	got.Name = "patch-stale-renamed"
	s.Require().NoError(s.repo.Update(s.ctx, got))

	_, err = svc.PatchAccount(s.ctx, account.ID, &service.PatchAccountInput{
		Fields:                service.UpdateAccountInput{Name: "mine"},
		ExpectedConfigVersion: &stale,
	})
	s.Require().Error(err)
	s.Require().Equal(http.StatusPreconditionFailed, infraerrors.Code(err))
	s.Require().Equal("ACCOUNT_VERSION_CONFLICT", infraerrors.Reason(err))

	stored, err := s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	s.Require().Equal("patch-stale-renamed", stored.Name)

	// Without If-Match the same edit goes through.
	_, err = svc.PatchAccount(s.ctx, account.ID, &service.PatchAccountInput{Fields: service.UpdateAccountInput{Name: "mine"}})
	s.Require().NoError(err)
}

func (s *AccountRepoSuite) TestPatchAccount_SensitiveKeyCannotBeDeleted() {
	account := mustCreateAccount(s.T(), s.client, &service.Account{Name: "patch-sensitive", Type: service.AccountTypeAPIKey, Credentials: map[string]any{"api_key": "sk"}})
	svc := s.newPatchAdminService()
	_, err := svc.PatchAccount(s.ctx, account.ID, &service.PatchAccountInput{CredentialsPatch: map[string]any{"api_key": nil}})
	s.Require().Error(err)
	s.Require().Equal("CREDENTIAL_KEY_NOT_DELETABLE", infraerrors.Reason(err))
}

func TestPatchAccount_DispatchScopeRollbackOnBillingUpdateFailure(t *testing.T) {
	t.Setenv("SUB2API_ACCOUNT_DISPATCH_WRITES_ENABLED", "true")
	client := testEntClient(t)
	cache := &schedulerCacheRecorder{}
	repo := newAccountRepositoryWithSQL(client, integrationDB, cache)
	ctx := service.WithScope(context.Background(), service.AdminScope())
	account := mustCreateAccount(t, client, &service.Account{
		Name:        fmt.Sprintf("patch-dispatch-rollback-%d", time.Now().UnixNano()),
		Platform:    service.PlatformOpenAI,
		Type:        service.AccountTypeOAuth,
		Credentials: map[string]any{"access_token": "token"},
	})
	t.Cleanup(func() { cleanupPatchTestAccounts(t, client, account.ID) })
	before, err := repo.GetByID(ctx, account.ID)
	require.NoError(t, err)
	wantScope := service.DispatchScopeAuthorizedOnly
	injectedErr := errors.New("injected billing settings persistence failure")

	svc := newPatchAdminServiceForIntegration(client, &failAccountBillingSettingsUpdateRepo{
		accountRepository: repo,
		err:               injectedErr,
	})
	patched, err := svc.PatchAccount(ctx, account.ID, &service.PatchAccountInput{
		Fields: service.UpdateAccountInput{
			Name:          "must-not-commit",
			DispatchScope: &wantScope,
		},
		ExpectedConfigVersion: &before.ConfigVersion,
	})
	require.Nil(t, patched)
	require.ErrorIs(t, err, injectedErr)

	stored, err := repo.GetByID(ctx, account.ID)
	require.NoError(t, err)
	require.Equal(t, before.Name, stored.Name)
	require.Equal(t, service.DispatchScopeShared, stored.DispatchScope)
	require.Equal(t, before.ConfigVersion, stored.ConfigVersion)
	require.Empty(t, cache.setAccounts, "failed transactions must not publish cache snapshots")
}

func TestPatchAccount_WithoutDispatchScopeRollbackOnBillingUpdateFailure(t *testing.T) {
	client := testEntClient(t)
	cache := &schedulerCacheRecorder{}
	repo := newAccountRepositoryWithSQL(client, integrationDB, cache)
	ctx := service.WithScope(context.Background(), service.AdminScope())
	account := mustCreateAccount(t, client, &service.Account{
		Name:        fmt.Sprintf("patch-ordinary-rollback-%d", time.Now().UnixNano()),
		Platform:    service.PlatformOpenAI,
		Type:        service.AccountTypeOAuth,
		Credentials: map[string]any{"access_token": "token"},
	})
	t.Cleanup(func() { cleanupPatchTestAccounts(t, client, account.ID) })
	before, err := repo.GetByID(ctx, account.ID)
	require.NoError(t, err)
	injectedErr := errors.New("injected billing settings persistence failure")
	svc := newPatchAdminServiceForIntegration(client, &failAccountBillingSettingsUpdateRepo{
		accountRepository: repo,
		err:               injectedErr,
	})

	priority := 9
	patched, err := svc.PatchAccount(ctx, account.ID, &service.PatchAccountInput{
		Fields: service.UpdateAccountInput{Name: "must-not-commit", Priority: &priority},
	})
	require.Nil(t, patched)
	require.ErrorIs(t, err, injectedErr)

	stored, err := repo.GetByID(ctx, account.ID)
	require.NoError(t, err)
	require.Equal(t, before.Name, stored.Name)
	require.Equal(t, before.Priority, stored.Priority)
	require.Equal(t, before.ConfigVersion, stored.ConfigVersion)
	require.Empty(t, cache.setAccounts, "failed transactions must not publish cache snapshots")
}

func TestPatchAccount_DispatchScopeRefreshesMainAndShadowSnapshots(t *testing.T) {
	t.Setenv("SUB2API_ACCOUNT_DISPATCH_WRITES_ENABLED", "true")
	client := testEntClient(t)
	cache := &schedulerCacheRecorder{}
	repo := newAccountRepositoryWithSQL(client, integrationDB, cache)
	ctx := service.WithScope(context.Background(), service.AdminScope())
	parent := mustCreateAccount(t, client, &service.Account{
		Name:        fmt.Sprintf("patch-dispatch-parent-%d", time.Now().UnixNano()),
		Platform:    service.PlatformOpenAI,
		Type:        service.AccountTypeOAuth,
		Credentials: map[string]any{"access_token": "parent-token"},
	})
	shadow := mustCreateAccount(t, client, &service.Account{
		Name:            fmt.Sprintf("patch-dispatch-shadow-%d", time.Now().UnixNano()),
		Platform:        service.PlatformOpenAI,
		Type:            service.AccountTypeOAuth,
		ParentAccountID: &parent.ID,
		QuotaDimension:  service.QuotaDimensionSpark,
	})
	t.Cleanup(func() { cleanupPatchTestAccounts(t, client, shadow.ID, parent.ID) })
	before, err := repo.GetByID(ctx, parent.ID)
	require.NoError(t, err)
	wantScope := service.DispatchScopeAuthorizedOnly
	svc := newPatchAdminServiceForIntegration(client, repo)

	patched, err := svc.PatchAccount(ctx, parent.ID, &service.PatchAccountInput{
		Fields:                service.UpdateAccountInput{DispatchScope: &wantScope},
		ExpectedConfigVersion: &before.ConfigVersion,
	})
	require.NoError(t, err)
	require.Equal(t, wantScope, patched.DispatchScope)

	require.Len(t, cache.setAccounts, 2)
	refreshed := make(map[int64]*service.Account, len(cache.setAccounts))
	for _, cached := range cache.setAccounts {
		require.NotNil(t, cached)
		refreshed[cached.ID] = cached
	}
	require.Contains(t, refreshed, parent.ID)
	require.Contains(t, refreshed, shadow.ID)
	require.Equal(t, wantScope, refreshed[parent.ID].DispatchScope)
	require.Equal(t, service.DispatchScopeShared, refreshed[shadow.ID].DispatchScope)
}

func TestPatchAccount_ReturnsCommittedAccountFieldsAndGroups(t *testing.T) {
	client := testEntClient(t)
	repo := newAccountRepositoryWithSQL(client, integrationDB, nil)
	ctx := service.WithScope(context.Background(), service.AdminScope())
	account := mustCreateAccount(t, client, &service.Account{
		Name:        fmt.Sprintf("patch-response-before-%d", time.Now().UnixNano()),
		Platform:    service.PlatformOpenAI,
		Type:        service.AccountTypeOAuth,
		Credentials: map[string]any{"access_token": "token"},
	})
	group := mustCreateGroup(t, client, &service.Group{
		Name:     fmt.Sprintf("patch-response-group-%d", time.Now().UnixNano()),
		Platform: service.PlatformOpenAI,
	})
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM account_groups WHERE account_id = $1", account.ID)
		cleanupPatchTestAccounts(t, client, account.ID)
		_ = client.Group.DeleteOneID(group.ID).Exec(context.Background())
	})
	before, err := repo.GetByID(ctx, account.ID)
	require.NoError(t, err)
	require.Empty(t, before.GroupIDs)
	groupIDs := []int64{group.ID}
	wantName := fmt.Sprintf("patch-response-after-%d", time.Now().UnixNano())
	svc := newPatchAdminServiceForIntegration(client, repo)

	patched, err := svc.PatchAccount(ctx, account.ID, &service.PatchAccountInput{
		Fields: service.UpdateAccountInput{
			Name:     wantName,
			GroupIDs: &groupIDs,
		},
		ExpectedConfigVersion: &before.ConfigVersion,
	})
	require.NoError(t, err)
	require.Equal(t, wantName, patched.Name)
	require.Equal(t, before.ConfigVersion+1, patched.ConfigVersion)
	require.Equal(t, groupIDs, patched.GroupIDs)

	stored, err := repo.GetByID(ctx, account.ID)
	require.NoError(t, err)
	require.Equal(t, stored.Name, patched.Name)
	require.Equal(t, stored.ConfigVersion, patched.ConfigVersion)
	require.Equal(t, stored.GroupIDs, patched.GroupIDs)
}

func (s *AccountRepoSuite) TestPatchAccount_KeepsStatusSetMeanwhile() {
	account := mustCreateAccount(s.T(), s.client, &service.Account{Name: "patch-status", Type: service.AccountTypeAPIKey, Credentials: map[string]any{"api_key": "sk"}})
	svc := s.newPatchAdminService()

	// The account fails while the admin edits notes; the edit must not revive it.
	s.Require().NoError(s.repo.SetError(s.ctx, account.ID, "Authentication failed (401): bad key"))
	notes := "rotating key tomorrow"
	_, err := svc.PatchAccount(s.ctx, account.ID, &service.PatchAccountInput{Fields: service.UpdateAccountInput{Notes: &notes}})
	s.Require().NoError(err)

	stored, err := s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	s.Require().Equal(service.StatusError, stored.Status)
	s.Require().False(stored.Schedulable)
	s.Require().NotNil(stored.ErrorAt)
	s.Require().Equal("rotating key tomorrow", *stored.Notes)
}

func (s *AccountRepoSuite) TestConfigVersion_RuntimeWritesDoNotBumpIt() {
	account := mustCreateAccount(s.T(), s.client, &service.Account{Name: "cv-runtime"})
	before, err := s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)

	s.Require().NoError(s.repo.SetModelRateLimit(s.ctx, account.ID, "m", time.Now().Add(time.Minute)))
	s.Require().NoError(s.repo.BatchUpdateLastUsed(s.ctx, map[int64]time.Time{account.ID: time.Now()}))
	s.Require().NoError(s.repo.SetError(s.ctx, account.ID, "x"))

	after, err := s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	s.Require().Equal(before.ConfigVersion, after.ConfigVersion)
}
