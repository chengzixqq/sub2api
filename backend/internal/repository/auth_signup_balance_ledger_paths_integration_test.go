//go:build integration

package repository

import (
	"context"
	"testing"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

// E10: the signup paths not covered in admin_user_balance_ledger_integration_test.go. Each one creates the user with
// the site default balance and must write the ledger row in the user's transaction; when the invitation claim fails
// in that transaction, the user and its ledger row are rolled back together.

// claimFailingRedeemCodeRepository loses the invitation claim, as when a concurrent signup took the code first.
type claimFailingRedeemCodeRepository struct {
	service.RedeemCodeRepository
}

func (r *claimFailingRedeemCodeRepository) Use(context.Context, int64, int64) error {
	return service.ErrRedeemCodeUsed
}

// newLedgerAuthServiceWithTokens is newLedgerAuthService with a refresh-token cache, for the signup paths that issue
// a token pair.
func newLedgerAuthServiceWithTokens(t *testing.T, client *dbent.Client, redeemRepo service.RedeemCodeRepository, adjustmentRepo service.AdminUserAdjustmentRepository, settings *service.SettingService) *service.AuthService {
	t.Helper()
	server := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	cfg := &config.Config{JWT: config.JWTConfig{Secret: "e10-ledger-secret", ExpireHour: 1, RefreshTokenExpireDays: 1}}
	svc := service.NewAuthService(client, NewUserRepository(client, integrationDB), redeemRepo, NewRefreshTokenCache(rdb), cfg, settings, nil, nil, nil, nil, nil, nil, nil)
	svc.SetAdminUserAdjustmentRepository(adjustmentRepo)
	return svc
}

func invitationLedgerSettings() *service.SettingService {
	return ledgerSettingService(map[string]string{
		service.SettingKeyRegistrationEnabled:   "true",
		service.SettingKeyInvitationCodeEnabled: "true",
		service.SettingKeyDefaultBalance:        "3",
	})
}

func mustCreateInvitationCode(t *testing.T) (string, int64) {
	t.Helper()
	code := "E10INV" + uuid.NewString()[:24]
	var id int64
	require.NoError(t, integrationDB.QueryRowContext(context.Background(), `
INSERT INTO redeem_codes (code, type, value, status, created_at) VALUES ($1, $2, 0, $3, NOW()) RETURNING id`,
		code, service.RedeemTypeInvitation, service.StatusUnused).Scan(&id))
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), `DELETE FROM redeem_codes WHERE id = $1`, id)
	})
	return code, id
}

func requireInvitationClaimedBy(t *testing.T, invitationID, userID int64) {
	t.Helper()
	var status string
	var usedBy *int64
	require.NoError(t, integrationDB.QueryRowContext(context.Background(),
		`SELECT status, used_by FROM redeem_codes WHERE id = $1`, invitationID).Scan(&status, &usedBy))
	require.Equal(t, service.StatusUsed, status)
	require.NotNil(t, usedBy)
	require.Equal(t, userID, *usedBy)
}

// requireNoSignupTrace checks that a rolled-back signup left neither the user nor a ledger row behind.
func requireNoSignupTrace(t *testing.T, email string, invitationID int64) {
	t.Helper()
	require.Zero(t, userCountByEmail(t, email), "the user is rolled back with the lost claim")
	var ledgerRows int
	require.NoError(t, integrationDB.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM admin_user_adjustments WHERE user_email = $1`, email).Scan(&ledgerRows))
	require.Zero(t, ledgerRows, "the grant entry is rolled back with the user")
	var status string
	require.NoError(t, integrationDB.QueryRowContext(context.Background(),
		`SELECT status FROM redeem_codes WHERE id = $1`, invitationID).Scan(&status))
	require.Equal(t, service.StatusUnused, status)
}

func TestSignupWithInvitationWritesLedgerInClaimTransaction(t *testing.T) {
	client := testEntClient(t)
	redeemRepo := NewRedeemCodeRepository(client)
	adjustmentRepo := NewAdminUserAdjustmentRepository(client)

	t.Run("claimed", func(t *testing.T) {
		code, invitationID := mustCreateInvitationCode(t)
		auth := newLedgerAuthService(client, redeemRepo, adjustmentRepo, invitationLedgerSettings())
		_, user, err := auth.RegisterWithVerification(context.Background(), "e10-invite-"+uuid.NewString()+"@example.com", "Strong-password-123!", "", "", code, "")
		require.NoError(t, err)
		requireSignupLedger(t, adjustmentRepo, user.ID)
		requireInvitationClaimedBy(t, invitationID, user.ID)
	})
	t.Run("claim lost", func(t *testing.T) {
		code, invitationID := mustCreateInvitationCode(t)
		auth := newLedgerAuthService(client, &claimFailingRedeemCodeRepository{RedeemCodeRepository: redeemRepo}, adjustmentRepo, invitationLedgerSettings())
		email := "e10-invite-lost-" + uuid.NewString() + "@example.com"
		_, _, err := auth.RegisterWithVerification(context.Background(), email, "Strong-password-123!", "", "", code, "")
		require.ErrorIs(t, err, service.ErrInvitationCodeInvalid)
		requireNoSignupTrace(t, email, invitationID)
	})
}

// LoginOrRegisterOAuthWithTokenPair opens its own transaction when an invitation code is required and passes it to
// createSignupUser, which must reuse it instead of committing the user and its grant on its own.
func TestOAuthTokenPairSignupWithInvitationWritesLedgerInClaimTransaction(t *testing.T) {
	client := testEntClient(t)
	redeemRepo := NewRedeemCodeRepository(client)
	adjustmentRepo := NewAdminUserAdjustmentRepository(client)

	t.Run("claimed", func(t *testing.T) {
		code, invitationID := mustCreateInvitationCode(t)
		auth := newLedgerAuthServiceWithTokens(t, client, redeemRepo, adjustmentRepo, invitationLedgerSettings())
		pair, user, err := auth.LoginOrRegisterOAuthWithTokenPair(context.Background(), "e10-oauth-invite-"+uuid.NewString()+"@example.com", "oauth-user", code, "", "")
		require.NoError(t, err)
		require.NotEmpty(t, pair.AccessToken)
		requireSignupLedger(t, adjustmentRepo, user.ID)
		requireInvitationClaimedBy(t, invitationID, user.ID)
	})
	t.Run("claim lost", func(t *testing.T) {
		code, invitationID := mustCreateInvitationCode(t)
		auth := newLedgerAuthServiceWithTokens(t, client, &claimFailingRedeemCodeRepository{RedeemCodeRepository: redeemRepo}, adjustmentRepo, invitationLedgerSettings())
		email := "e10-oauth-invite-lost-" + uuid.NewString() + "@example.com"
		_, _, err := auth.LoginOrRegisterOAuthWithTokenPair(context.Background(), email, "oauth-user", code, "", "")
		require.ErrorIs(t, err, service.ErrInvitationCodeInvalid)
		requireNoSignupTrace(t, email, invitationID)
	})
	t.Run("without invitation", func(t *testing.T) {
		auth := newLedgerAuthServiceWithTokens(t, client, redeemRepo, adjustmentRepo, ledgerSettingService(map[string]string{
			service.SettingKeyRegistrationEnabled: "true",
			service.SettingKeyDefaultBalance:      "3",
		}))
		_, user, err := auth.LoginOrRegisterOAuthWithTokenPair(context.Background(), "e10-oauth-pair-"+uuid.NewString()+"@example.com", "oauth-user", "", "", "")
		require.NoError(t, err)
		requireSignupLedger(t, adjustmentRepo, user.ID)
	})
}

// The pending-OAuth email registration and the verified email OAuth auto-registration create users through their own
// guards; both record the site grant.
func TestOAuthEmailSignupPathsWriteLedger(t *testing.T) {
	client := testEntClient(t)
	adjustmentRepo := NewAdminUserAdjustmentRepository(client)
	auth := newLedgerAuthServiceWithTokens(t, client, NewRedeemCodeRepository(client), adjustmentRepo, ledgerSettingService(map[string]string{
		service.SettingKeyRegistrationEnabled: "true",
		service.SettingKeyDefaultBalance:      "3",
	}))

	t.Run("pending oauth email registration", func(t *testing.T) {
		_, user, err := auth.RegisterVerifiedOAuthEmailAccount(context.Background(), "e10-pending-"+uuid.NewString()+"@example.com", "Strong-password-123!", "", "linuxdo")
		require.NoError(t, err)
		requireSignupLedger(t, adjustmentRepo, user.ID)
	})
	t.Run("verified email oauth auto registration", func(t *testing.T) {
		email := "e10-email-oauth-" + uuid.NewString() + "@example.com"
		input := service.EmailOAuthIdentityInput{
			ProviderType: "github", ProviderSubject: "e10-" + uuid.NewString(), Email: email, EmailVerified: true, Username: "octo",
		}
		_, user, err := auth.LoginOrRegisterVerifiedEmailOAuth(context.Background(), input)
		require.NoError(t, err)
		requireSignupLedger(t, adjustmentRepo, user.ID)

		// The next login finds the identity and grants nothing more.
		_, again, err := auth.LoginOrRegisterVerifiedEmailOAuth(context.Background(), input)
		require.NoError(t, err)
		require.Equal(t, user.ID, again.ID)
		require.Len(t, userAdjustments(t, adjustmentRepo, user.ID), 1)
	})
}
