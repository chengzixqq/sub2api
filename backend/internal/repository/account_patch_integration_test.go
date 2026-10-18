//go:build integration

package repository

import (
	"net/http"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

func (s *AccountRepoSuite) newPatchAdminService() service.AdminService {
	s.repo.schedulerCache = &schedulerCacheRecorder{}
	return service.NewAdminService(
		nil, nil, nil, s.repo, nil, nil, nil, nil, nil, nil, nil,
		nil, nil, nil, s.client, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
	)
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
