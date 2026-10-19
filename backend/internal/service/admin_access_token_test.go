package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type memAccessTokenRepo struct {
	tokens  []*AdminAccessToken
	touches int
	gets    int
}

func (r *memAccessTokenRepo) Create(_ context.Context, t *AdminAccessToken) error {
	t.ID = int64(len(r.tokens) + 1)
	t.CreatedAt = time.Now()
	copied := *t
	r.tokens = append(r.tokens, &copied)
	return nil
}

func (r *memAccessTokenRepo) GetByHash(_ context.Context, hash string) (*AdminAccessToken, error) {
	r.gets++
	for _, t := range r.tokens {
		if t.TokenHash == hash {
			copied := *t
			return &copied, nil
		}
	}
	return nil, ErrAdminAccessTokenNotFound
}

func (r *memAccessTokenRepo) ListByUser(context.Context, int64, time.Time) ([]*AdminAccessToken, error) {
	return r.tokens, nil
}

func (r *memAccessTokenRepo) CountActiveByUser(_ context.Context, userID int64, now time.Time) (int, error) {
	n := 0
	for _, t := range r.tokens {
		if t.UserID == userID && t.RevokedAt == nil && t.ExpiresAt.After(now) {
			n++
		}
	}
	return n, nil
}

func (r *memAccessTokenRepo) Revoke(_ context.Context, userID, id int64, now time.Time) (*AdminAccessToken, error) {
	for _, t := range r.tokens {
		if t.ID == id && t.UserID == userID && t.RevokedAt == nil {
			t.RevokedAt = &now
			copied := *t
			return &copied, nil
		}
	}
	return nil, ErrAdminAccessTokenNotFound
}

func (r *memAccessTokenRepo) TouchLastUsed(_ context.Context, id int64, at time.Time, ip string) error {
	r.touches++
	for _, t := range r.tokens {
		if t.ID == id {
			t.LastUsedAt = &at
			t.LastUsedIP = ip
		}
	}
	return nil
}

func TestAdminAccessTokenCreateValidates(t *testing.T) {
	svc := NewAdminAccessTokenService(&memAccessTokenRepo{})
	ctx := context.Background()
	_, err := svc.Create(ctx, CreateAdminAccessTokenInput{UserID: 1, Name: " \x01 "})
	require.ErrorIs(t, err, ErrAdminAccessTokenName)
	_, err = svc.Create(ctx, CreateAdminAccessTokenInput{UserID: 1, Name: "a", Scope: "admin"})
	require.ErrorIs(t, err, ErrAdminAccessTokenScope)
	_, err = svc.Create(ctx, CreateAdminAccessTokenInput{UserID: 1, Name: "a", ExpiresInDays: 366})
	require.ErrorIs(t, err, ErrAdminAccessTokenExpiry)
	_, err = svc.Create(ctx, CreateAdminAccessTokenInput{UserID: 1, Name: "a", ExpiresInDays: -1})
	require.ErrorIs(t, err, ErrAdminAccessTokenExpiry)

	for i := 0; i < maxAdminAccessTokensPerUser; i++ {
		_, err := svc.Create(ctx, CreateAdminAccessTokenInput{UserID: 1, Name: "a"})
		require.NoError(t, err)
	}
	_, err = svc.Create(ctx, CreateAdminAccessTokenInput{UserID: 1, Name: "a"})
	require.ErrorIs(t, err, ErrAdminAccessTokenLimit)
}

func TestAdminAccessTokenAuthenticateCachesAndThrottlesTouches(t *testing.T) {
	repo := &memAccessTokenRepo{}
	svc := NewAdminAccessTokenService(repo)
	now := time.Now()
	svc.now = func() time.Time { return now }
	ctx := context.Background()
	created, err := svc.Create(ctx, CreateAdminAccessTokenInput{UserID: 1, Name: "phone", Scope: "write"})
	require.NoError(t, err)

	for i := 0; i < 5; i++ {
		_, err := svc.Authenticate(ctx, created.Token, "198.51.100.1")
		require.NoError(t, err)
	}
	require.Equal(t, 1, repo.gets, "cached for 30 seconds")
	require.Equal(t, 1, repo.touches, "last use written at most every 5 minutes")

	now = now.Add(31 * time.Second)
	_, err = svc.Authenticate(ctx, created.Token, "198.51.100.1")
	require.NoError(t, err)
	require.Equal(t, 2, repo.gets)

	now = now.Add(5 * time.Minute)
	_, err = svc.Authenticate(ctx, created.Token, "198.51.100.1")
	require.NoError(t, err)
	require.Equal(t, 2, repo.touches)

	require.NoError(t, svc.Revoke(ctx, 1, created.Record.ID))
	_, err = svc.Authenticate(ctx, created.Token, "198.51.100.1")
	require.ErrorIs(t, err, ErrAdminAccessTokenInvalid)
}

func TestIsMachineCredential(t *testing.T) {
	require.True(t, IsMachineCredential(AuditAuthMethodAdminAPIKey))
	require.True(t, IsMachineCredential(AuditAuthMethodAdminAccessToken))
	require.False(t, IsMachineCredential(AuditAuthMethodJWT))
	require.False(t, IsMachineCredential(""))
}
