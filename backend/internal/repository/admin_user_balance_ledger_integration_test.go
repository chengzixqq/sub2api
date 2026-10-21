//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

// E10: every credit that creates or tops up a user's balance from the admin side or the site defaults is recorded in
// the adjustment ledger together with a compatibility redeem_codes row; balance-history shows the before/after of
// admin adjustments, and admin top-ups accrue users.total_recharged (Q12).

// memorySettingRepo keeps the settings of one test in memory so it never writes the shared settings table.
type memorySettingRepo struct {
	values map[string]string
}

func (r *memorySettingRepo) Get(_ context.Context, key string) (*service.Setting, error) {
	value, ok := r.values[key]
	if !ok {
		return nil, service.ErrSettingNotFound
	}
	return &service.Setting{Key: key, Value: value}, nil
}

func (r *memorySettingRepo) GetValue(ctx context.Context, key string) (string, error) {
	setting, err := r.Get(ctx, key)
	if err != nil {
		return "", err
	}
	return setting.Value, nil
}

func (r *memorySettingRepo) Set(_ context.Context, key, value string) error {
	r.values[key] = value
	return nil
}

func (r *memorySettingRepo) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	out := make(map[string]string, len(keys))
	for _, key := range keys {
		if value, ok := r.values[key]; ok {
			out[key] = value
		}
	}
	return out, nil
}

func (r *memorySettingRepo) SetMultiple(_ context.Context, settings map[string]string) error {
	for key, value := range settings {
		r.values[key] = value
	}
	return nil
}

func (r *memorySettingRepo) GetAll(context.Context) (map[string]string, error) {
	out := make(map[string]string, len(r.values))
	for key, value := range r.values {
		out[key] = value
	}
	return out, nil
}

func (r *memorySettingRepo) Delete(_ context.Context, key string) error {
	delete(r.values, key)
	return nil
}

func ledgerSettingService(values map[string]string) *service.SettingService {
	return service.NewSettingService(&memorySettingRepo{values: values}, &config.Config{})
}

func newLedgerAdminService(client *dbent.Client, redeemRepo service.RedeemCodeRepository, adjustmentRepo service.AdminUserAdjustmentRepository, settings *service.SettingService) service.AdminService {
	return service.NewAdminService(
		nil, NewUserRepository(client, integrationDB), nil, nil, nil, nil, redeemRepo, adjustmentRepo, nil, nil, nil,
		nil, nil, nil, client, settings, nil, nil, nil, nil, nil, nil, nil, nil, nil,
	)
}

func newLedgerAuthService(client *dbent.Client, redeemRepo service.RedeemCodeRepository, adjustmentRepo service.AdminUserAdjustmentRepository, settings *service.SettingService) *service.AuthService {
	cfg := &config.Config{JWT: config.JWTConfig{Secret: "e10-ledger-secret", ExpireHour: 1}}
	svc := service.NewAuthService(client, NewUserRepository(client, integrationDB), redeemRepo, nil, cfg, settings, nil, nil, nil, nil, nil, nil, nil)
	if adjustmentRepo != nil {
		svc.SetAdminUserAdjustmentRepository(adjustmentRepo)
	}
	return svc
}

func userAdjustments(t *testing.T, repo service.AdminUserAdjustmentRepository, userID int64) []service.AdminUserAdjustment {
	t.Helper()
	rows, _, err := repo.List(context.Background(), service.AdminUserAdjustmentFilter{UserID: &userID}, 1, 100)
	require.NoError(t, err)
	return rows
}

func userTotalRecharged(t *testing.T, userID int64) float64 {
	t.Helper()
	var value float64
	require.NoError(t, integrationDB.QueryRowContext(context.Background(), "SELECT total_recharged FROM users WHERE id = $1", userID).Scan(&value))
	return value
}

func userCountByEmail(t *testing.T, email string) int {
	t.Helper()
	var count int
	require.NoError(t, integrationDB.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM users WHERE email = $1", email).Scan(&count))
	return count
}

func userIDByEmail(t *testing.T, email string) int64 {
	t.Helper()
	var id int64
	require.NoError(t, integrationDB.QueryRowContext(context.Background(), "SELECT id FROM users WHERE email = $1 AND deleted_at IS NULL", email).Scan(&id))
	return id
}

// requireInitialBalanceRow checks the single ledger row of a new user and its compatibility redeem code.
func requireInitialBalanceRow(t *testing.T, row service.AdminUserAdjustment, userID int64, source, amount string) {
	t.Helper()
	require.Equal(t, source, row.Source)
	require.Equal(t, service.AdjustmentKindBalance, row.Kind)
	require.Equal(t, service.AdjustmentOperationSet, row.Operation)
	require.Equal(t, "0.00000000", adjustmentValueOrEmpty(row.BeforeValue))
	require.Equal(t, amount, adjustmentValueOrEmpty(row.AfterValue))
	require.Equal(t, amount, adjustmentValueOrEmpty(row.RequestedValue))
	require.Equal(t, amount, row.Delta)
	require.Equal(t, "初始余额", adjustmentValueOrEmpty(row.Notes))
	require.NotNil(t, row.RedeemCodeID, "new ledger rows point at their compatibility redeem code")

	var codeType, value string
	var usedBy int64
	require.NoError(t, integrationDB.QueryRowContext(context.Background(),
		"SELECT type, value::text, used_by FROM redeem_codes WHERE id = $1", *row.RedeemCodeID).Scan(&codeType, &value, &usedBy))
	require.Equal(t, service.AdjustmentTypeAdminBalance, codeType)
	require.Equal(t, amount, value)
	require.Equal(t, userID, usedBy)
}

func TestAdminCreateUserInitialBalanceWritesLedgerAndRecharge(t *testing.T) {
	client := testEntClient(t)
	redeemRepo := NewRedeemCodeRepository(client)
	adjustmentRepo := NewAdminUserAdjustmentRepository(client)
	adminService := newLedgerAdminService(client, redeemRepo, adjustmentRepo, nil)
	operator := mustCreateUser(t, client, &service.User{Email: "e10-owner-" + uuid.NewString() + "@example.com", Role: service.RoleAdmin})
	balance := 50.0

	created, err := adminService.CreateUser(adjustmentIntegrationContext(operator, ""), &service.CreateUserInput{
		Email: "e10-created-" + uuid.NewString() + "@example.com", Password: "Strong-password-123!",
		Balance: &balance, Concurrency: 7,
	})
	require.NoError(t, err)
	require.InDelta(t, 50, created.Balance, 1e-9)
	require.InDelta(t, 50, created.TotalRecharged, 1e-9)
	require.InDelta(t, 50, currentUserBalance(t, created.ID), 1e-9)
	require.InDelta(t, 50, userTotalRecharged(t, created.ID), 1e-9)

	rows := userAdjustments(t, adjustmentRepo, created.ID)
	require.Len(t, rows, 1, "the initial concurrency is not a ledger entry; only the balance is")
	requireInitialBalanceRow(t, rows[0], created.ID, service.AdjustmentSourceAdminCreateUser, "50.00000000")
	require.NotNil(t, rows[0].OperatorUserID)
	require.Equal(t, operator.ID, *rows[0].OperatorUserID)
	require.Equal(t, operator.Email, adjustmentValueOrEmpty(rows[0].OperatorEmail))

	codes, total, totalRecharged, err := adminService.GetUserBalanceHistory(context.Background(), created.ID, 1, 20, "")
	require.NoError(t, err)
	require.Equal(t, int64(1), total)
	require.InDelta(t, 50, totalRecharged, 1e-9)
	require.Len(t, codes, 1)
	require.Equal(t, *rows[0].RedeemCodeID, codes[0].ID)
	require.NotNil(t, codes[0].Adjustment)
	require.Equal(t, rows[0].ID, codes[0].Adjustment.AdjustmentID)
	require.Equal(t, "0.00000000", adjustmentValueOrEmpty(codes[0].Adjustment.BeforeValue))
	require.Equal(t, "50.00000000", adjustmentValueOrEmpty(codes[0].Adjustment.AfterValue))
	require.Equal(t, operator.Email, adjustmentValueOrEmpty(codes[0].Adjustment.OperatorEmail))
	require.Equal(t, "初始余额", codes[0].Notes)
}

func TestAdminCreateUserDefaultBalanceUsesDefaultBalanceSource(t *testing.T) {
	client := testEntClient(t)
	adjustmentRepo := NewAdminUserAdjustmentRepository(client)
	settings := ledgerSettingService(map[string]string{service.SettingKeyDefaultBalance: "7"})
	adminService := newLedgerAdminService(client, NewRedeemCodeRepository(client), adjustmentRepo, settings)
	operator := mustCreateUser(t, client, &service.User{Email: "e10-default-owner-" + uuid.NewString() + "@example.com", Role: service.RoleAdmin})

	created, err := adminService.CreateUser(adjustmentIntegrationContext(operator, "开户"), &service.CreateUserInput{
		Email: "e10-default-" + uuid.NewString() + "@example.com", Password: "Strong-password-123!",
	})
	require.NoError(t, err)
	require.InDelta(t, 7, currentUserBalance(t, created.ID), 1e-9)
	require.InDelta(t, 7, userTotalRecharged(t, created.ID), 1e-9)

	rows := userAdjustments(t, adjustmentRepo, created.ID)
	require.Len(t, rows, 1)
	require.Equal(t, service.AdjustmentSourceDefaultBalance, rows[0].Source)
	require.Equal(t, "7.00000000", adjustmentValueOrEmpty(rows[0].AfterValue))
	require.Equal(t, "开户", adjustmentValueOrEmpty(rows[0].Notes), "a note sent by the admin wins over the default note")
}

func TestAdminCreateUserZeroBalanceWritesNoLedger(t *testing.T) {
	client := testEntClient(t)
	adjustmentRepo := NewAdminUserAdjustmentRepository(client)
	adminService := newLedgerAdminService(client, NewRedeemCodeRepository(client), adjustmentRepo, nil)
	zero := 0.0

	created, err := adminService.CreateUser(context.Background(), &service.CreateUserInput{
		Email: "e10-zero-" + uuid.NewString() + "@example.com", Password: "Strong-password-123!", Balance: &zero,
	})
	require.NoError(t, err)
	require.Empty(t, userAdjustments(t, adjustmentRepo, created.ID))
	require.Equal(t, int64(0), redeemAdjustmentCount(t, created.ID, service.AdjustmentTypeAdminBalance))
	require.InDelta(t, 0, userTotalRecharged(t, created.ID), 1e-9)
}

func TestAdminCreateUserRollsBackWhenLedgerWriteFails(t *testing.T) {
	client := testEntClient(t)
	redeemRepo := NewRedeemCodeRepository(client)
	adjustmentRepo := NewAdminUserAdjustmentRepository(client)
	for name, svc := range map[string]service.AdminService{
		"redeem codes": newLedgerAdminService(client, &failingRedeemCodeRepository{RedeemCodeRepository: redeemRepo}, adjustmentRepo, nil),
		"adjustments":  newLedgerAdminService(client, redeemRepo, &failingAdminUserAdjustmentRepository{AdminUserAdjustmentRepository: adjustmentRepo}, nil),
	} {
		t.Run(name, func(t *testing.T) {
			email := "e10-rollback-" + uuid.NewString() + "@example.com"
			balance := 12.0
			_, err := svc.CreateUser(context.Background(), &service.CreateUserInput{Email: email, Password: "Strong-password-123!", Balance: &balance})
			require.ErrorIs(t, err, errForcedAdjustmentWrite)
			require.Zero(t, userCountByEmail(t, email), "the user must not exist without its initial-balance entry")
		})
	}
}

func TestAdminBalanceIncreaseAccruesTotalRecharged(t *testing.T) {
	client := testEntClient(t)
	adminService := newLedgerAdminService(client, NewRedeemCodeRepository(client), NewAdminUserAdjustmentRepository(client), nil)
	operator := mustCreateUser(t, client, &service.User{Email: "e10-accrue-owner-" + uuid.NewString() + "@example.com", Role: service.RoleAdmin})
	target := mustCreateUser(t, client, &service.User{Email: "e10-accrue-" + uuid.NewString() + "@example.com", Balance: 10})
	require.InDelta(t, 0, userTotalRecharged(t, target.ID), 1e-9)

	steps := []struct {
		operation, amount string
		wantErr           bool
		wantTotal         float64
	}{
		{service.AdjustmentOperationAdd, "20", false, 20},      // 10 → 30
		{service.AdjustmentOperationSubtract, "5", false, 20},  // 30 → 25: a decrease does not lower it
		{service.AdjustmentOperationSet, "50", false, 45},      // 25 → 50: a raise by set counts as +25
		{service.AdjustmentOperationSet, "10", false, 45},      // 50 → 10
		{service.AdjustmentOperationSet, "10", false, 45},      // no-op
		{service.AdjustmentOperationSubtract, "100", true, 45}, // rejected
		{service.AdjustmentOperationAdd, "0.5", false, 45.5},   // fractional top-up
	}
	for i, step := range steps {
		_, err := adminService.UpdateUserBalance(adjustmentIntegrationContext(operator, "accrue"), target.ID, step.amount, step.operation, "accrue")
		if step.wantErr {
			require.Error(t, err, "step %d", i)
		} else {
			require.NoError(t, err, "step %d", i)
		}
		require.InDelta(t, step.wantTotal, userTotalRecharged(t, target.ID), 1e-9, "step %d", i)
	}
}

func TestBalanceHistoryLinksAdjustmentsIncludingLegacyAndPreLinkRows(t *testing.T) {
	client := testEntClient(t)
	adjustmentRepo := NewAdminUserAdjustmentRepository(client)
	adminService := newLedgerAdminService(client, NewRedeemCodeRepository(client), adjustmentRepo, nil)
	operator := mustCreateUser(t, client, &service.User{Email: "e10-link-owner-" + uuid.NewString() + "@example.com", Role: service.RoleAdmin})
	target := mustCreateUser(t, client, &service.User{Email: "e10-link-" + uuid.NewString() + "@example.com", Balance: 10})
	ctx := context.Background()

	_, err := adminService.UpdateUserBalance(adjustmentIntegrationContext(operator, "new"), target.ID, "20", service.AdjustmentOperationAdd, "new")
	require.NoError(t, err)

	// A row written between migration 223 and E10: same timestamp and amount, no redeem_code_id.
	preLinkAt := time.Now().Add(-2 * time.Hour).UTC().Truncate(time.Microsecond)
	preLinkCodeID := insertRawRedeemCode(t, target.ID, service.AdjustmentTypeAdminBalance, "5", preLinkAt)
	var preLinkAdjustmentID int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
INSERT INTO admin_user_adjustments (action_id, kind, operation, delta, before_value, after_value, user_id, operator_email, source, created_at)
VALUES ($1, 'balance', 'add', 5, 1, 6, $2, 'old-operator@example.com', 'admin_balance', $3) RETURNING id`,
		uuid.New(), target.ID, preLinkAt).Scan(&preLinkAdjustmentID))
	// The same user and amount at another moment must not be taken for it.
	_, err = integrationDB.ExecContext(ctx, `
INSERT INTO admin_user_adjustments (action_id, kind, operation, delta, before_value, after_value, user_id, source, created_at)
VALUES ($1, 'balance', 'add', 5, 100, 105, $2, 'admin_balance', $3)`, uuid.New(), target.ID, preLinkAt.Add(time.Second))
	require.NoError(t, err)

	// A row migrated by 223 from an old redeem code: linked by legacy_redeem_code_id, no before/after.
	legacyAt := time.Now().Add(-3 * time.Hour).UTC().Truncate(time.Microsecond)
	legacyCodeID := insertRawRedeemCode(t, target.ID, service.AdjustmentTypeAdminConcurrency, "2", legacyAt)
	var legacyAdjustmentID int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
INSERT INTO admin_user_adjustments (action_id, kind, operation, delta, user_id, source, legacy_redeem_code_id, created_at)
VALUES ($1, 'concurrency', 'legacy', 2, $2, 'legacy_redeem_code', $3, $4) RETURNING id`,
		uuid.New(), target.ID, legacyCodeID, legacyAt).Scan(&legacyAdjustmentID))

	// A redeemed balance code and an affiliate transfer are not admin adjustments.
	balanceCodeID := insertRawRedeemCode(t, target.ID, service.RedeemTypeBalance, "40", time.Now().Add(-4*time.Hour).UTC())
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO user_affiliate_ledger (user_id, action, amount) VALUES ($1, 'transfer', 5)`, target.ID)
	require.NoError(t, err)

	codes, total, _, err := adminService.GetUserBalanceHistory(ctx, target.ID, 1, 50, "")
	require.NoError(t, err)
	require.Equal(t, int64(5), total)
	byID := make(map[int64]service.RedeemCode, len(codes))
	for _, code := range codes {
		byID[code.ID] = code
	}
	newRows := userAdjustments(t, adjustmentRepo, target.ID)
	var newRow *service.AdminUserAdjustment
	for i := range newRows {
		if newRows[i].RedeemCodeID != nil {
			newRow = &newRows[i]
		}
	}
	require.NotNil(t, newRow)
	linked := byID[*newRow.RedeemCodeID]
	require.NotNil(t, linked.Adjustment)
	require.Equal(t, newRow.ID, linked.Adjustment.AdjustmentID)
	require.Equal(t, "10.00000000", adjustmentValueOrEmpty(linked.Adjustment.BeforeValue))
	require.Equal(t, "30.00000000", adjustmentValueOrEmpty(linked.Adjustment.AfterValue))
	require.Equal(t, operator.Email, adjustmentValueOrEmpty(linked.Adjustment.OperatorEmail))

	require.NotNil(t, byID[preLinkCodeID].Adjustment)
	require.Equal(t, preLinkAdjustmentID, byID[preLinkCodeID].Adjustment.AdjustmentID)
	require.Equal(t, "1.00000000", adjustmentValueOrEmpty(byID[preLinkCodeID].Adjustment.BeforeValue))
	require.Equal(t, "6.00000000", adjustmentValueOrEmpty(byID[preLinkCodeID].Adjustment.AfterValue))
	require.Equal(t, "old-operator@example.com", adjustmentValueOrEmpty(byID[preLinkCodeID].Adjustment.OperatorEmail))

	require.NotNil(t, byID[legacyCodeID].Adjustment)
	require.Equal(t, legacyAdjustmentID, byID[legacyCodeID].Adjustment.AdjustmentID)
	require.Nil(t, byID[legacyCodeID].Adjustment.BeforeValue)
	require.Nil(t, byID[legacyCodeID].Adjustment.AfterValue)

	require.Nil(t, byID[balanceCodeID].Adjustment)
	affiliateRows := 0
	for _, code := range codes {
		if code.Type == service.RedeemTypeAffiliateBalance {
			affiliateRows++
			require.Nil(t, code.Adjustment)
		}
	}
	require.Equal(t, 1, affiliateRows)

	filtered, _, _, err := adminService.GetUserBalanceHistory(ctx, target.ID, 1, 50, service.AdjustmentTypeAdminBalance)
	require.NoError(t, err)
	require.Len(t, filtered, 2)
	for _, code := range filtered {
		require.NotNil(t, code.Adjustment, "the type filter keeps the links (code %d)", code.ID)
	}
}

func TestUserRechargeBreakdownSumsEachSource(t *testing.T) {
	client := testEntClient(t)
	redeemRepo := NewRedeemCodeRepository(client)
	adminService := newLedgerAdminService(client, redeemRepo, NewAdminUserAdjustmentRepository(client), nil)
	operator := mustCreateUser(t, client, &service.User{Email: "e10-sum-owner-" + uuid.NewString() + "@example.com", Role: service.RoleAdmin})
	target := mustCreateUser(t, client, &service.User{Email: "e10-sum-" + uuid.NewString() + "@example.com", Balance: 1})
	ctx := context.Background()

	insertRawRedeemCode(t, target.ID, service.RedeemTypeBalance, "40", time.Now().UTC())
	_, err := adminService.UpdateUserBalance(adjustmentIntegrationContext(operator, "top up"), target.ID, "20", service.AdjustmentOperationAdd, "top up")
	require.NoError(t, err)
	_, err = adminService.UpdateUserBalance(adjustmentIntegrationContext(operator, "refund"), target.ID, "4", service.AdjustmentOperationSubtract, "refund")
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO user_affiliate_ledger (user_id, action, amount) VALUES ($1, 'transfer', 5), ($1, 'accrue', 9)`, target.ID)
	require.NoError(t, err)
	var promoID int64
	require.NoError(t, integrationDB.QueryRowContext(ctx,
		`INSERT INTO promo_codes (code, bonus_amount) VALUES ($1, 3) RETURNING id`, "E10"+uuid.NewString()[:8]).Scan(&promoID))
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), `DELETE FROM promo_codes WHERE id = $1`, promoID)
	})
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO promo_code_usages (promo_code_id, user_id, bonus_amount) VALUES ($1, $2, 3)`, promoID, target.ID)
	require.NoError(t, err)

	breakdown, err := adminService.GetUserRechargeBreakdown(ctx, target.ID)
	require.NoError(t, err)
	require.NotNil(t, breakdown)
	require.InDelta(t, 40, breakdown.Redeem, 1e-9)
	require.InDelta(t, 20, breakdown.Admin, 1e-9)
	require.InDelta(t, 5, breakdown.Affiliate, 1e-9)
	require.InDelta(t, 3, breakdown.Promo, 1e-9)

	require.Zero(t, breakdown.Grant)

	totalRecharged, err := redeemRepo.SumPositiveBalanceByUser(ctx, target.ID)
	require.NoError(t, err)
	require.InDelta(t, breakdown.Redeem+breakdown.Admin+breakdown.Grant, totalRecharged, 1e-9, "total_recharged is redeem + admin + grant")

	empty, err := adminService.GetUserRechargeBreakdown(ctx, operator.ID)
	require.NoError(t, err)
	require.Equal(t, service.UserRechargeBreakdown{}, *empty)
}

// Site grants (signup and first-bind default balance, no operator) are admin_balance rows like an admin top-up, but
// the breakdown reports them apart so a free grant is not shown as an admin recharge. An admin-created user's initial
// balance, even when taken from the site default, has an operator and stays an admin credit.
func TestUserRechargeBreakdownSeparatesSiteGrants(t *testing.T) {
	client := testEntClient(t)
	redeemRepo := NewRedeemCodeRepository(client)
	adjustmentRepo := NewAdminUserAdjustmentRepository(client)
	settings := ledgerSettingService(map[string]string{
		service.SettingKeyRegistrationEnabled:                      "true",
		service.SettingKeyDefaultBalance:                           "3",
		service.SettingKeyAuthSourceDefaultLinuxDoBalance:          "4",
		service.SettingKeyAuthSourceDefaultLinuxDoGrantOnFirstBind: "true",
	})
	auth := newLedgerAuthService(client, redeemRepo, adjustmentRepo, settings)
	adminService := newLedgerAdminService(client, redeemRepo, adjustmentRepo, settings)
	operator := mustCreateUser(t, client, &service.User{Email: "e10-grant-owner-" + uuid.NewString() + "@example.com", Role: service.RoleAdmin})
	ctx := context.Background()

	_, signedUp, err := auth.Register(ctx, "e10-grant-signup-"+uuid.NewString()+"@example.com", "Strong-password-123!")
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), `DELETE FROM user_provider_default_grants WHERE user_id = $1`, signedUp.ID)
	})
	require.NoError(t, auth.ApplyProviderDefaultSettingsOnFirstBind(ctx, signedUp.ID, "linuxdo"))
	_, err = adminService.UpdateUserBalance(adjustmentIntegrationContext(operator, "top up"), signedUp.ID, "20", service.AdjustmentOperationAdd, "top up")
	require.NoError(t, err)

	breakdown, err := adminService.GetUserRechargeBreakdown(ctx, signedUp.ID)
	require.NoError(t, err)
	require.NotNil(t, breakdown)
	require.InDelta(t, 7, breakdown.Grant, 1e-9, "signup and first-bind grants")
	require.InDelta(t, 20, breakdown.Admin, 1e-9, "only the admin top-up")
	require.Zero(t, breakdown.Redeem)
	totalRecharged, err := redeemRepo.SumPositiveBalanceByUser(ctx, signedUp.ID)
	require.NoError(t, err)
	require.InDelta(t, 27, totalRecharged, 1e-9)
	require.InDelta(t, breakdown.Redeem+breakdown.Admin+breakdown.Grant, totalRecharged, 1e-9)

	created, err := adminService.CreateUser(adjustmentIntegrationContext(operator, ""), &service.CreateUserInput{
		Email: "e10-grant-created-" + uuid.NewString() + "@example.com", Password: "Strong-password-123!",
	})
	require.NoError(t, err)
	createdBreakdown, err := adminService.GetUserRechargeBreakdown(ctx, created.ID)
	require.NoError(t, err)
	require.InDelta(t, 3, createdBreakdown.Admin, 1e-9, "an admin-created initial balance is an admin credit")
	require.Zero(t, createdBreakdown.Grant)
}

func TestRedeemCodeCreateBatchExactReturnsIDs(t *testing.T) {
	client := testEntClient(t)
	repo := NewRedeemCodeRepository(client)
	codes := []service.RedeemCode{
		{Code: uuid.NewString()[:32], Type: service.RedeemTypeBalance, ValueExact: "1.12345678", Status: service.StatusUnused},
		{Code: uuid.NewString()[:32], Type: service.RedeemTypeBalance, ValueExact: "2.50000000", Status: service.StatusUnused},
	}
	require.NoError(t, repo.CreateBatch(context.Background(), codes))
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), `DELETE FROM redeem_codes WHERE id = ANY($1)`, pq.Array([]int64{codes[0].ID, codes[1].ID}))
	})

	require.Positive(t, codes[0].ID)
	require.Positive(t, codes[1].ID)
	require.NotEqual(t, codes[0].ID, codes[1].ID)
	for _, code := range codes {
		var value, stored string
		require.NoError(t, integrationDB.QueryRowContext(context.Background(), `SELECT value::text, code FROM redeem_codes WHERE id = $1`, code.ID).Scan(&value, &stored))
		require.Equal(t, code.ValueExact, value)
		require.Equal(t, code.Code, stored)
	}
}

// Q12 "站点 default_balance 入账": a self-service signup and a third-party first login that create a user with the
// site default balance record it like an admin-created initial balance, with no operator.
func TestSignupDefaultBalanceWritesLedger(t *testing.T) {
	client := testEntClient(t)
	adjustmentRepo := NewAdminUserAdjustmentRepository(client)
	settings := ledgerSettingService(map[string]string{
		service.SettingKeyRegistrationEnabled: "true",
		service.SettingKeyDefaultBalance:      "3",
	})
	auth := newLedgerAuthService(client, NewRedeemCodeRepository(client), adjustmentRepo, settings)

	t.Run("email signup", func(t *testing.T) {
		email := "e10-signup-" + uuid.NewString() + "@example.com"
		_, user, err := auth.Register(context.Background(), email, "Strong-password-123!")
		require.NoError(t, err)
		requireSignupLedger(t, adjustmentRepo, user.ID)
	})
	t.Run("oauth first login", func(t *testing.T) {
		email := "e10-oauth-" + uuid.NewString() + "@example.com"
		_, user, err := auth.LoginOrRegisterOAuth(context.Background(), email, "oauth-user")
		require.NoError(t, err)
		requireSignupLedger(t, adjustmentRepo, user.ID)

		// Logging in again creates nothing.
		_, again, err := auth.LoginOrRegisterOAuth(context.Background(), email, "oauth-user")
		require.NoError(t, err)
		require.Equal(t, user.ID, again.ID)
		require.Len(t, userAdjustments(t, adjustmentRepo, user.ID), 1)
	})
}

func requireSignupLedger(t *testing.T, adjustmentRepo service.AdminUserAdjustmentRepository, userID int64) {
	t.Helper()
	require.InDelta(t, 3, currentUserBalance(t, userID), 1e-9)
	require.InDelta(t, 3, userTotalRecharged(t, userID), 1e-9)
	rows := userAdjustments(t, adjustmentRepo, userID)
	require.Len(t, rows, 1)
	requireInitialBalanceRow(t, rows[0], userID, service.AdjustmentSourceDefaultBalance, "3.00000000")
	require.Nil(t, rows[0].OperatorUserID, "a signup grant has no operator")
	require.Nil(t, rows[0].OperatorEmail)
}

func TestSignupWithZeroDefaultBalanceOrWithoutLedgerRepoWritesNoLedger(t *testing.T) {
	client := testEntClient(t)
	adjustmentRepo := NewAdminUserAdjustmentRepository(client)

	zero := newLedgerAuthService(client, NewRedeemCodeRepository(client), adjustmentRepo, ledgerSettingService(map[string]string{
		service.SettingKeyRegistrationEnabled: "true",
		service.SettingKeyDefaultBalance:      "0",
	}))
	_, user, err := zero.Register(context.Background(), "e10-signup-zero-"+uuid.NewString()+"@example.com", "Strong-password-123!")
	require.NoError(t, err)
	require.Empty(t, userAdjustments(t, adjustmentRepo, user.ID))

	// Without the ledger dependency (old wiring, unit tests) signup keeps the plain path.
	plain := newLedgerAuthService(client, NewRedeemCodeRepository(client), nil, ledgerSettingService(map[string]string{
		service.SettingKeyRegistrationEnabled: "true",
		service.SettingKeyDefaultBalance:      "3",
	}))
	_, user, err = plain.Register(context.Background(), "e10-signup-plain-"+uuid.NewString()+"@example.com", "Strong-password-123!")
	require.NoError(t, err)
	require.InDelta(t, 3, currentUserBalance(t, user.ID), 1e-9)
	require.Empty(t, userAdjustments(t, adjustmentRepo, user.ID))
}

func TestSignupRollsBackWhenLedgerWriteFails(t *testing.T) {
	client := testEntClient(t)
	adjustmentRepo := &failingAdminUserAdjustmentRepository{AdminUserAdjustmentRepository: NewAdminUserAdjustmentRepository(client)}
	auth := newLedgerAuthService(client, NewRedeemCodeRepository(client), adjustmentRepo, ledgerSettingService(map[string]string{
		service.SettingKeyRegistrationEnabled: "true",
		service.SettingKeyDefaultBalance:      "3",
	}))

	email := "e10-signup-fail-" + uuid.NewString() + "@example.com"
	_, _, err := auth.Register(context.Background(), email, "Strong-password-123!")
	require.ErrorIs(t, err, service.ErrServiceUnavailable)
	require.Zero(t, userCountByEmail(t, email))

	oauthEmail := "e10-oauth-fail-" + uuid.NewString() + "@example.com"
	_, _, err = auth.LoginOrRegisterOAuth(context.Background(), oauthEmail, "oauth-user")
	require.ErrorIs(t, err, service.ErrServiceUnavailable)
	require.Zero(t, userCountByEmail(t, oauthEmail))
}

// The provider grant on a first third-party bind tops up an existing user from the same site defaults.
func TestFirstBindDefaultBalanceWritesLedger(t *testing.T) {
	client := testEntClient(t)
	adjustmentRepo := NewAdminUserAdjustmentRepository(client)
	auth := newLedgerAuthService(client, NewRedeemCodeRepository(client), adjustmentRepo, ledgerSettingService(map[string]string{
		service.SettingKeyAuthSourceDefaultLinuxDoBalance:          "4",
		service.SettingKeyAuthSourceDefaultLinuxDoGrantOnFirstBind: "true",
	}))
	user := mustCreateUser(t, client, &service.User{Email: "e10-bind-" + uuid.NewString() + "@example.com", Balance: 1})
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), `DELETE FROM user_provider_default_grants WHERE user_id = $1`, user.ID)
	})

	require.NoError(t, auth.ApplyProviderDefaultSettingsOnFirstBind(context.Background(), user.ID, "linuxdo"))
	require.NoError(t, auth.ApplyProviderDefaultSettingsOnFirstBind(context.Background(), user.ID, "linuxdo"), "the grant is once per provider")

	require.InDelta(t, 5, currentUserBalance(t, user.ID), 1e-9)
	require.InDelta(t, 4, userTotalRecharged(t, user.ID), 1e-9)
	rows := userAdjustments(t, adjustmentRepo, user.ID)
	require.Len(t, rows, 1)
	require.Equal(t, service.AdjustmentSourceDefaultBalance, rows[0].Source)
	require.Equal(t, service.AdjustmentOperationAdd, rows[0].Operation)
	require.Equal(t, "1.00000000", adjustmentValueOrEmpty(rows[0].BeforeValue))
	require.Equal(t, "5.00000000", adjustmentValueOrEmpty(rows[0].AfterValue))
	require.Equal(t, "4.00000000", rows[0].Delta)
	require.NotNil(t, rows[0].RedeemCodeID)
	require.Nil(t, rows[0].OperatorUserID)
}

// Usage billing may overdraw a balance. A first-bind grant to an overdrawn user is still credited (as AddBalance did
// before E10) and recorded, even though the balance stays negative.
func TestFirstBindGrantToOverdrawnUserStillApplies(t *testing.T) {
	client := testEntClient(t)
	adjustmentRepo := NewAdminUserAdjustmentRepository(client)
	auth := newLedgerAuthService(client, NewRedeemCodeRepository(client), adjustmentRepo, ledgerSettingService(map[string]string{
		service.SettingKeyAuthSourceDefaultLinuxDoBalance:          "2",
		service.SettingKeyAuthSourceDefaultLinuxDoGrantOnFirstBind: "true",
	}))
	user := mustCreateUser(t, client, &service.User{Email: "e10-bind-overdrawn-" + uuid.NewString() + "@example.com", Balance: -5})
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), `DELETE FROM user_provider_default_grants WHERE user_id = $1`, user.ID)
	})

	require.NoError(t, auth.ApplyProviderDefaultSettingsOnFirstBind(context.Background(), user.ID, "linuxdo"))

	require.InDelta(t, -3, currentUserBalance(t, user.ID), 1e-9)
	require.InDelta(t, 2, userTotalRecharged(t, user.ID), 1e-9)
	rows := userAdjustments(t, adjustmentRepo, user.ID)
	require.Len(t, rows, 1)
	require.Equal(t, service.AdjustmentOperationAdd, rows[0].Operation)
	require.Equal(t, "-5.00000000", adjustmentValueOrEmpty(rows[0].BeforeValue))
	require.Equal(t, "-3.00000000", adjustmentValueOrEmpty(rows[0].AfterValue))
	require.Equal(t, "2.00000000", rows[0].Delta)
	require.Equal(t, "首次绑定赠送余额", adjustmentValueOrEmpty(rows[0].Notes))
	var grants int
	require.NoError(t, integrationDB.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM user_provider_default_grants WHERE user_id = $1 AND provider_type = 'linuxdo'`, user.ID).Scan(&grants))
	require.Equal(t, 1, grants, "the grant is recorded once the credit commits")
}

func insertRawRedeemCode(t *testing.T, userID int64, codeType, value string, usedAt time.Time) int64 {
	t.Helper()
	var id int64
	require.NoError(t, integrationDB.QueryRowContext(context.Background(), `
INSERT INTO redeem_codes (code, type, value, status, used_by, used_at, created_at)
VALUES ($1, $2, $3::numeric, 'used', $4, $5, $5) RETURNING id`,
		uuid.NewString()[:32], codeType, value, userID, usedAt).Scan(&id))
	return id
}

// The quick top-up strip (E05) lists users an admin topped up recently. A signup grant has no operator and must not
// push them out; an admin-created user with an initial balance is a real admin credit and does show.
func TestQuickTopupRecentSkipsSiteGrants(t *testing.T) {
	client := testEntClient(t)
	adjustmentRepo := NewAdminUserAdjustmentRepository(client)
	redeemRepo := NewRedeemCodeRepository(client)
	auth := newLedgerAuthService(client, redeemRepo, adjustmentRepo, ledgerSettingService(map[string]string{
		service.SettingKeyRegistrationEnabled: "true",
		service.SettingKeyDefaultBalance:      "3",
	}))
	adminService := newLedgerAdminService(client, redeemRepo, adjustmentRepo, nil)
	operator := mustCreateUser(t, client, &service.User{Email: "e10-quick-owner-" + uuid.NewString() + "@example.com", Role: service.RoleAdmin})

	_, signedUp, err := auth.Register(context.Background(), "e10-quick-signup-"+uuid.NewString()+"@example.com", "Strong-password-123!")
	require.NoError(t, err)
	balance := 9.0
	created, err := adminService.CreateUser(adjustmentIntegrationContext(operator, ""), &service.CreateUserInput{
		Email: "e10-quick-created-" + uuid.NewString() + "@example.com", Password: "Strong-password-123!", Balance: &balance,
	})
	require.NoError(t, err)

	quick, ok := adjustmentRepo.(service.QuickTopupRepository)
	require.True(t, ok)
	// The shared database holds other tests' adjustments: a large limit keeps ours in the result.
	result, err := quick.QuickTopup(context.Background(), service.QuickTopupQuery{RecentLimit: 100000, RecentDays: 1})
	require.NoError(t, err)
	recent := make(map[int64]bool, len(result.Recent))
	for _, item := range result.Recent {
		recent[item.UserID] = true
	}
	require.False(t, recent[signedUp.ID], "a signup grant is not an admin top-up")
	require.True(t, recent[created.ID], "an admin-created initial balance is")
}
