//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestAdminAccessTokens_Lifecycle(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	owner := mustCreateUser(t, client, &service.User{Email: "sat-" + uuid.NewString() + "@example.com", Role: service.RoleAdmin})
	other := mustCreateUser(t, client, &service.User{Email: "sat-other-" + uuid.NewString() + "@example.com", Role: service.RoleAdmin})
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM admin_access_tokens WHERE user_id IN ($1, $2)", owner.ID, other.ID)
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM users WHERE id IN ($1, $2)", owner.ID, other.ID)
	})
	svc := service.NewAdminAccessTokenService(NewAdminAccessTokenRepository(integrationDB))

	created, err := svc.Create(ctx, service.CreateAdminAccessTokenInput{UserID: owner.ID, Name: "我的 iPhone", Scope: "write", ExpiresInDays: 90})
	require.NoError(t, err)
	require.Regexp(t, `^sat_[0-9a-f]{64}$`, created.Token)
	require.Equal(t, created.Token[:8], created.Record.Prefix)
	require.Equal(t, created.Token[len(created.Token)-4:], created.Record.Last4)
	require.WithinDuration(t, time.Now().Add(90*24*time.Hour), created.Record.ExpiresAt, time.Minute)

	var stored string
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT token_hash FROM admin_access_tokens WHERE id = $1", created.Record.ID).Scan(&stored))
	require.NotContains(t, stored, created.Token[4:], "only the hash is stored")

	token, err := svc.Authenticate(ctx, created.Token, "203.0.113.24")
	require.NoError(t, err)
	require.Equal(t, owner.ID, token.UserID)
	require.False(t, token.ReadOnly())

	list, err := svc.List(ctx, owner.ID)
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.NotNil(t, list[0].LastUsedAt, "first use is recorded")
	require.Equal(t, "203.0.113.24", list[0].LastUsedIP)

	readOnly, err := svc.Create(ctx, service.CreateAdminAccessTokenInput{UserID: owner.ID, Name: "iPad"})
	require.NoError(t, err)
	require.Equal(t, service.AdminAccessTokenScopeRead, readOnly.Record.Scope, "scope defaults to read")

	require.ErrorIs(t, svc.Revoke(ctx, other.ID, created.Record.ID), service.ErrAdminAccessTokenNotFound, "only the owner can revoke")
	require.NoError(t, svc.Revoke(ctx, owner.ID, created.Record.ID))
	_, err = svc.Authenticate(ctx, created.Token, "203.0.113.24")
	require.Equal(t, "INVALID_ADMIN_KEY", infraerrors.Reason(err), "revoked tokens stop working at once on this instance")
	require.ErrorIs(t, svc.Revoke(ctx, owner.ID, created.Record.ID), service.ErrAdminAccessTokenNotFound)

	list, err = svc.List(ctx, owner.ID)
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.Equal(t, readOnly.Record.ID, list[0].ID)

	_, err = integrationDB.ExecContext(ctx, "UPDATE admin_access_tokens SET expires_at = NOW() - INTERVAL '1 minute' WHERE id = $1", readOnly.Record.ID)
	require.NoError(t, err)
	fresh := service.NewAdminAccessTokenService(NewAdminAccessTokenRepository(integrationDB))
	_, err = fresh.Authenticate(ctx, readOnly.Token, "")
	require.Equal(t, "ACCESS_TOKEN_EXPIRED", infraerrors.Reason(err))

	_, err = fresh.Authenticate(ctx, "sat_"+uuid.NewString(), "")
	require.Equal(t, "INVALID_ADMIN_KEY", infraerrors.Reason(err))
}
