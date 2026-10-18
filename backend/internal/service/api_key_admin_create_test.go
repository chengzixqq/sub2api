//go:build unit

package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/stretchr/testify/require"
)

type apiKeyCreateRepoStub struct {
	APIKeyRepository // unexpected calls panic
	created          []*APIKey
	count            int64
}

func (s *apiKeyCreateRepoStub) Create(_ context.Context, key *APIKey) error {
	key.ID = 4099
	clone := *key
	s.created = append(s.created, &clone)
	return nil
}

func (s *apiKeyCreateRepoStub) CountByUserID(context.Context, int64) (int64, error) {
	return s.count, nil
}

func newAdminCreateService(repo *apiKeyCreateRepoStub, maxActive int) *APIKeyService {
	cfg := &config.Config{}
	cfg.Default.APIKeyPrefix = "sk-"
	cfg.APIKeyCreate.MaxActivePerUser = maxActive
	cfg.APIKeyCreate.MaxPerUserPerHour = 1 // the admin path must not count against the user's hourly allowance
	return &APIKeyService{apiKeyRepo: repo, userRepo: &userRepoStub{user: &User{ID: 1, Status: StatusActive}}, cfg: cfg}
}

func TestAdminCreateAPIKey_GeneratesKeyWithFields(t *testing.T) {
	repo := &apiKeyCreateRepoStub{}
	svc := newAdminCreateService(repo, 0)
	days := 30

	key, err := svc.AdminCreate(adminCtx(), 1, CreateAPIKeyRequest{
		Name: "<脚本>", Quota: 50, ExpiresInDays: &days, IPBlacklist: []string{"203.0.113.9"}, RateLimit1d: 20,
	})
	require.NoError(t, err)
	require.Equal(t, int64(4099), key.ID)
	require.True(t, strings.HasPrefix(key.Key, "sk-"))
	require.Len(t, key.Key, len("sk-")+64)
	require.Equal(t, "&lt;脚本&gt;", key.Name)
	require.Equal(t, StatusActive, key.Status)
	require.Equal(t, 50.0, key.Quota)
	require.Equal(t, 20.0, key.RateLimit1d)
	require.WithinDuration(t, time.Now().AddDate(0, 0, 30), *key.ExpiresAt, time.Minute)
	require.Nil(t, key.GroupID, "the handler binds the group with the admin group rules afterwards")
	require.Len(t, repo.created, 1)
}

func TestAdminCreateAPIKey_RespectsActiveKeyLimitOnly(t *testing.T) {
	repo := &apiKeyCreateRepoStub{count: 5}
	svc := newAdminCreateService(repo, 5)

	_, err := svc.AdminCreate(adminCtx(), 1, CreateAPIKeyRequest{Name: "x"})
	require.ErrorIs(t, err, ErrAPIKeyCountExceeded)
	require.Empty(t, repo.created)

	repo.count = 4
	_, err = svc.AdminCreate(adminCtx(), 1, CreateAPIKeyRequest{Name: "x"})
	require.NoError(t, err)
	_, err = svc.AdminCreate(adminCtx(), 1, CreateAPIKeyRequest{Name: "y"})
	require.NoError(t, err, "no hourly creation counter on the admin path")
}

func TestAdminCreateAPIKey_Validation(t *testing.T) {
	repo := &apiKeyCreateRepoStub{}
	svc := newAdminCreateService(repo, 0)
	custom := "sk-my-own-key-0001"

	_, err := svc.AdminCreate(adminCtx(), 1, CreateAPIKeyRequest{Name: "  "})
	require.Error(t, err)
	_, err = svc.AdminCreate(adminCtx(), 1, CreateAPIKeyRequest{Name: "x", Quota: -1})
	require.Error(t, err)
	_, err = svc.AdminCreate(adminCtx(), 1, CreateAPIKeyRequest{Name: "x", IPWhitelist: []string{"nope"}})
	require.ErrorIs(t, err, ErrInvalidIPPattern)
	_, err = svc.AdminCreate(adminCtx(), 1, CreateAPIKeyRequest{Name: "x", CustomKey: &custom})
	require.Error(t, err, "admins cannot set a custom key")
	_, err = svc.AdminCreate(adminCtx(), 2, CreateAPIKeyRequest{Name: "x", GroupID: ptrInt64Svc(3)})
	require.Error(t, err, "groups are bound afterwards with the admin group rules")
	require.Empty(t, repo.created)
}

func TestAdminCreateAPIKey_UnknownUser(t *testing.T) {
	repo := &apiKeyCreateRepoStub{}
	svc := newAdminCreateService(repo, 0)
	svc.userRepo = &userRepoStub{}

	_, err := svc.AdminCreate(adminCtx(), 999, CreateAPIKeyRequest{Name: "x"})
	require.ErrorIs(t, err, ErrUserNotFound)
}

func TestAdminCreateAPIKey_VendorScopeIsRefused(t *testing.T) {
	repo := &apiKeyCreateRepoStub{}
	svc := newAdminCreateService(repo, 0)
	vendor := WithScope(context.Background(), VendorScope(3, WorkspacePermissions{AccountManage: true}))

	_, err := svc.AdminCreate(vendor, 1, CreateAPIKeyRequest{Name: "x"})
	require.ErrorIs(t, err, domain.ErrWorkspaceScopeViolation)
	require.Empty(t, repo.created)
}
