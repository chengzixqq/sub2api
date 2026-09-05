//go:build integration

package repository

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

var errForcedAdjustmentWrite = errors.New("forced adjustment write failure")
var errForcedGroupRateWrite = errors.New("forced group rate write failure")

type failingRedeemCodeRepository struct {
	service.RedeemCodeRepository
}

func (f *failingRedeemCodeRepository) CreateBatch(context.Context, []service.RedeemCode) error {
	return errForcedAdjustmentWrite
}

type failingAdminUserAdjustmentRepository struct {
	service.AdminUserAdjustmentRepository
}

func (f *failingAdminUserAdjustmentRepository) CreateBatch(context.Context, []service.AdminUserAdjustmentWrite) error {
	return errForcedAdjustmentWrite
}

type syncThenFailGroupRateRepository struct {
	service.UserGroupRateRepository
	sawTransaction bool
}

func (f *syncThenFailGroupRateRepository) SyncUserGroupRates(ctx context.Context, userID int64, rates map[int64]*float64) error {
	f.sawTransaction = dbent.TxFromContext(ctx) != nil
	if err := f.UserGroupRateRepository.SyncUserGroupRates(ctx, userID, rates); err != nil {
		return err
	}
	return errForcedGroupRateWrite
}

func TestAdminUserBalanceAdjustmentPersistsExactLedgerAndRejectsMutation(t *testing.T) {
	client := testEntClient(t)
	userRepo := NewUserRepository(client, integrationDB)
	redeemRepo := NewRedeemCodeRepository(client)
	adjustmentRepo := NewAdminUserAdjustmentRepository(client)
	adminService := newAdjustmentIntegrationAdminService(client, userRepo, redeemRepo, adjustmentRepo)
	operator := mustCreateUser(t, client, &service.User{
		Email: "adjustment-owner-" + uuid.NewString() + "@example.com", Username: "Owner Snapshot", Role: service.RoleAdmin,
	})
	target := mustCreateUser(t, client, &service.User{
		Email: "adjustment-target-" + uuid.NewString() + "@example.com", Username: "Target Snapshot", Balance: 10,
	})
	ctx := adjustmentIntegrationContext(operator, "balance sequence")

	updated, err := adminService.UpdateUserBalance(ctx, target.ID, "5", service.AdjustmentOperationAdd, "balance sequence")
	require.NoError(t, err)
	require.InDelta(t, 15, updated.Balance, 1e-9)
	updated, err = adminService.UpdateUserBalance(adjustmentIntegrationContext(operator, "subtract"), target.ID, "3", service.AdjustmentOperationSubtract, "subtract")
	require.NoError(t, err)
	require.InDelta(t, 12, updated.Balance, 1e-9)
	updated, err = adminService.UpdateUserBalance(adjustmentIntegrationContext(operator, "set"), target.ID, "20", service.AdjustmentOperationSet, "set")
	require.NoError(t, err)
	require.InDelta(t, 20, updated.Balance, 1e-9)

	rows, _, err := adjustmentRepo.List(context.Background(), service.AdminUserAdjustmentFilter{Keyword: target.Email}, 1, 20)
	require.NoError(t, err)
	require.Len(t, rows, 3)
	require.Equal(t, int64(3), adjustmentCountForUsers(t, target.ID))
	for _, row := range rows {
		require.Equal(t, service.AdjustmentKindBalance, row.Kind)
		require.NotNil(t, row.UserID)
		require.Equal(t, target.ID, *row.UserID)
		require.NotNil(t, row.OperatorUserID)
		require.Equal(t, operator.ID, *row.OperatorUserID)
		require.Equal(t, "Owner Snapshot", adjustmentValueOrEmpty(row.OperatorName))
		require.Equal(t, "Target Snapshot", adjustmentValueOrEmpty(row.UserName))
	}
	require.Equal(t, int64(3), redeemAdjustmentCount(t, target.ID, service.AdjustmentTypeAdminBalance))

	_, err = adminService.UpdateUserBalance(adjustmentIntegrationContext(operator, "no-op"), target.ID, "20", service.AdjustmentOperationSet, "no-op")
	require.NoError(t, err)
	require.Equal(t, int64(3), adjustmentCountForUsers(t, target.ID), "no-op set must not create a ledger row")
	_, err = adminService.UpdateUserBalance(adjustmentIntegrationContext(operator, "too precise"), target.ID, "0.0000000049", service.AdjustmentOperationAdd, "too precise")
	require.Error(t, err)
	require.InDelta(t, 20, currentUserBalance(t, target.ID), 1e-9)
	require.Equal(t, int64(3), adjustmentCountForUsers(t, target.ID), "over-scale amounts must not create a ledger row")
	require.Equal(t, int64(3), redeemAdjustmentCount(t, target.ID, service.AdjustmentTypeAdminBalance))

	_, err = adminService.UpdateUserBalance(adjustmentIntegrationContext(operator, "negative"), target.ID, "21", service.AdjustmentOperationSubtract, "negative")
	require.Error(t, err)
	require.Equal(t, int64(3), adjustmentCountForUsers(t, target.ID), "rejected subtraction must not create a ledger row")
	require.InDelta(t, 20, currentUserBalance(t, target.ID), 1e-9)

	_, err = integrationDB.ExecContext(context.Background(), "UPDATE admin_user_adjustments SET notes = 'tampered' WHERE user_id = $1", target.ID)
	require.ErrorContains(t, err, "append-only")
	_, err = integrationDB.ExecContext(context.Background(), "DELETE FROM admin_user_adjustments WHERE user_id = $1", target.ID)
	require.ErrorContains(t, err, "append-only")
}

func TestAdminUserAdjustmentAllowsFractionalLegacyConcurrencyOnly(t *testing.T) {
	actionID := uuid.New()
	_, err := integrationDB.ExecContext(context.Background(), `
INSERT INTO admin_user_adjustments (action_id, kind, operation, delta, source)
VALUES ($1, 'concurrency', 'legacy', 1.5, 'legacy_test')`, actionID)
	require.NoError(t, err)

	_, err = integrationDB.ExecContext(context.Background(), `
INSERT INTO admin_user_adjustments (action_id, kind, operation, delta, source)
VALUES ($1, 'concurrency', 'add', 1.5, 'admin_test')`, uuid.New())
	require.ErrorContains(t, err, "admin_user_adjustments_concurrency_integral_check")
}

func TestUserGroupRateRepositoryUsesEntTransactionContext(t *testing.T) {
	client := testEntClient(t)
	repo := NewUserGroupRateRepository(integrationDB)
	user := mustCreateUser(t, client, &service.User{
		Email: "group-rate-tx-user-" + uuid.NewString() + "@example.com",
	})
	group := mustCreateGroup(t, client, &service.Group{
		Name: "group-rate-tx-" + uuid.NewString(),
	})
	rate := 1.25

	tx, err := client.Tx(context.Background())
	require.NoError(t, err)
	txCtx := dbent.NewTxContext(context.Background(), tx)
	require.NoError(t, repo.SyncUserGroupRates(txCtx, user.ID, map[int64]*float64{group.ID: &rate}))
	require.NoError(t, tx.Rollback())

	stored, err := repo.GetByUserAndGroup(context.Background(), user.ID, group.ID)
	require.NoError(t, err)
	require.Nil(t, stored, "rolled-back group rate must not escape the Ent transaction")
}

func TestAdminUpdateUserConcurrencyAndGroupRatesAreAtomic(t *testing.T) {
	client := testEntClient(t)
	userRepo := NewUserRepository(client, integrationDB)
	redeemRepo := NewRedeemCodeRepository(client)
	adjustmentRepo := NewAdminUserAdjustmentRepository(client)
	realRateRepo := NewUserGroupRateRepository(integrationDB)
	operator := mustCreateUser(t, client, &service.User{
		Email: "group-rate-owner-" + uuid.NewString() + "@example.com", Role: service.RoleAdmin,
	})
	group := mustCreateGroup(t, client, &service.Group{
		Name: "group-rate-atomic-" + uuid.NewString(),
	})

	t.Run("commits user ledger compatibility record and rate together", func(t *testing.T) {
		target := mustCreateUser(t, client, &service.User{
			Email: "group-rate-success-" + uuid.NewString() + "@example.com", Concurrency: 2,
		})
		adminService := newAdjustmentIntegrationAdminServiceWithGroupRates(
			client, userRepo, redeemRepo, adjustmentRepo, realRateRepo,
		)
		concurrency := 5
		rate := 1.4

		updated, err := adminService.UpdateUser(
			adjustmentIntegrationContext(operator, "atomic group rate success"),
			target.ID,
			&service.UpdateUserInput{
				Concurrency: &concurrency,
				GroupRates:  map[int64]*float64{group.ID: &rate},
			},
		)
		require.NoError(t, err)
		require.Equal(t, concurrency, updated.Concurrency)
		require.Equal(t, concurrency, currentUserConcurrency(t, target.ID))
		storedRate, err := realRateRepo.GetByUserAndGroup(context.Background(), target.ID, group.ID)
		require.NoError(t, err)
		require.NotNil(t, storedRate)
		require.InDelta(t, rate, *storedRate, 1e-9)
		require.Equal(t, int64(1), adjustmentCountForUsers(t, target.ID))
		require.Equal(t, int64(1), redeemAdjustmentCount(t, target.ID, service.AdjustmentTypeAdminConcurrency))
	})

	t.Run("rolls back every write when rate sync fails", func(t *testing.T) {
		target := mustCreateUser(t, client, &service.User{
			Email: "group-rate-failure-" + uuid.NewString() + "@example.com", Concurrency: 2,
		})
		failingRateRepo := &syncThenFailGroupRateRepository{UserGroupRateRepository: realRateRepo}
		adminService := newAdjustmentIntegrationAdminServiceWithGroupRates(
			client, userRepo, redeemRepo, adjustmentRepo, failingRateRepo,
		)
		concurrency := 7
		rate := 1.6

		_, err := adminService.UpdateUser(
			adjustmentIntegrationContext(operator, "atomic group rate rollback"),
			target.ID,
			&service.UpdateUserInput{
				Concurrency: &concurrency,
				GroupRates:  map[int64]*float64{group.ID: &rate},
			},
		)
		require.ErrorIs(t, err, errForcedGroupRateWrite)
		require.True(t, failingRateRepo.sawTransaction)
		require.Equal(t, 2, currentUserConcurrency(t, target.ID))
		storedRate, getErr := realRateRepo.GetByUserAndGroup(context.Background(), target.ID, group.ID)
		require.NoError(t, getErr)
		require.Nil(t, storedRate)
		require.Equal(t, int64(0), adjustmentCountForUsers(t, target.ID))
		require.Equal(t, int64(0), redeemAdjustmentCount(t, target.ID, service.AdjustmentTypeAdminConcurrency))
	})
}

func TestAdminUserBalanceAdjustmentPreservesDecimal20Precision(t *testing.T) {
	client := testEntClient(t)
	userRepo := NewUserRepository(client, integrationDB)
	adminService := newAdjustmentIntegrationAdminService(
		client,
		userRepo,
		NewRedeemCodeRepository(client),
		NewAdminUserAdjustmentRepository(client),
	)
	operator := mustCreateUser(t, client, &service.User{
		Email: "precision-owner-" + uuid.NewString() + "@example.com", Role: service.RoleAdmin,
	})
	target := mustCreateUser(t, client, &service.User{
		Email: "precision-target-" + uuid.NewString() + "@example.com",
	})
	require.NoError(t, integrationDB.QueryRowContext(context.Background(), `
UPDATE users SET balance = 100000000.00000001 WHERE id = $1
RETURNING balance::text`, target.ID).Scan(new(string)))

	_, err := adminService.UpdateUserBalance(
		adjustmentIntegrationContext(operator, "decimal precision"),
		target.ID,
		"0.00000001",
		service.AdjustmentOperationAdd,
		"decimal precision",
	)
	require.NoError(t, err)

	var balance string
	require.NoError(t, integrationDB.QueryRowContext(context.Background(),
		"SELECT balance::text FROM users WHERE id = $1", target.ID).Scan(&balance))
	require.Equal(t, "100000000.00000002", balance)

	var requested, before, after, delta string
	require.NoError(t, integrationDB.QueryRowContext(context.Background(), `
SELECT requested_value::text, before_value::text, after_value::text, delta::text
FROM admin_user_adjustments
WHERE user_id = $1 AND notes = 'decimal precision'
ORDER BY id DESC LIMIT 1`, target.ID).Scan(&requested, &before, &after, &delta))
	require.Equal(t, "0.00000001", requested)
	require.Equal(t, "100000000.00000001", before)
	require.Equal(t, "100000000.00000002", after)
	require.Equal(t, "0.00000001", delta)

	var compatibilityValue string
	require.NoError(t, integrationDB.QueryRowContext(context.Background(), `
SELECT value::text FROM redeem_codes
WHERE used_by = $1 AND type = $2
ORDER BY id DESC LIMIT 1`, target.ID, service.AdjustmentTypeAdminBalance).Scan(&compatibilityValue))
	require.Equal(t, "0.00000001", compatibilityValue)
}

func TestAdminUserBalanceAdjustmentRollsBackOnCompatibilityOrLedgerFailure(t *testing.T) {
	client := testEntClient(t)
	userRepo := NewUserRepository(client, integrationDB)
	realRedeemRepo := NewRedeemCodeRepository(client)
	realAdjustmentRepo := NewAdminUserAdjustmentRepository(client)
	operator := mustCreateUser(t, client, &service.User{
		Email: "rollback-owner-" + uuid.NewString() + "@example.com", Role: service.RoleAdmin,
	})

	tests := []struct {
		name       string
		redeemRepo service.RedeemCodeRepository
		ledgerRepo service.AdminUserAdjustmentRepository
	}{
		{
			name: "compatibility record failure",
			redeemRepo: &failingRedeemCodeRepository{
				RedeemCodeRepository: realRedeemRepo,
			},
			ledgerRepo: realAdjustmentRepo,
		},
		{
			name:       "ledger failure",
			redeemRepo: realRedeemRepo,
			ledgerRepo: &failingAdminUserAdjustmentRepository{
				AdminUserAdjustmentRepository: realAdjustmentRepo,
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			target := mustCreateUser(t, client, &service.User{
				Email: "rollback-target-" + uuid.NewString() + "@example.com", Balance: 10,
			})
			adminService := newAdjustmentIntegrationAdminService(client, userRepo, test.redeemRepo, test.ledgerRepo)

			_, err := adminService.UpdateUserBalance(adjustmentIntegrationContext(operator, test.name), target.ID, "5", service.AdjustmentOperationAdd, test.name)
			require.ErrorIs(t, err, errForcedAdjustmentWrite)
			require.InDelta(t, 10, currentUserBalance(t, target.ID), 1e-9)
			require.Equal(t, int64(0), adjustmentCountForUsers(t, target.ID))
			require.Equal(t, int64(0), redeemAdjustmentCount(t, target.ID, service.AdjustmentTypeAdminBalance))
		})
	}
}

func TestAdminAdjustmentIdempotentActionReplaysWithoutDuplicateWrites(t *testing.T) {
	client := testEntClient(t)
	userRepo := NewUserRepository(client, integrationDB)
	adjustmentRepo := NewAdminUserAdjustmentRepository(client)
	adminService := newAdjustmentIntegrationAdminService(client, userRepo, NewRedeemCodeRepository(client), adjustmentRepo)
	operator := mustCreateUser(t, client, &service.User{
		Email: "idempotent-owner-" + uuid.NewString() + "@example.com", Role: service.RoleAdmin,
	})
	target := mustCreateUser(t, client, &service.User{
		Email: "idempotent-target-" + uuid.NewString() + "@example.com", Balance: 10, Concurrency: 2,
	})
	actionID := uuid.New()
	ctx := adjustmentIntegrationIdempotentContext(operator, actionID, "same action")

	results := make(chan *service.User, 2)
	errors := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			updated, err := adminService.UpdateUserBalance(ctx, target.ID, "5", service.AdjustmentOperationAdd, "same action")
			results <- updated
			errors <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errors)
	for err := range errors {
		require.NoError(t, err)
	}
	for updated := range results {
		require.NotNil(t, updated)
		require.InDelta(t, 15, updated.Balance, 1e-9)
	}
	require.InDelta(t, 15, currentUserBalance(t, target.ID), 1e-9)
	assertAdjustmentActionRows(t, actionID, service.AdjustmentKindBalance, 1)
	require.Equal(t, int64(1), redeemAdjustmentCount(t, target.ID, service.AdjustmentTypeAdminBalance))

	batchActionID := uuid.New()
	batchCtx := adjustmentIntegrationIdempotentContext(operator, batchActionID, "batch same action")
	for range 2 {
		affected, err := adminService.BatchUpdateConcurrency(batchCtx, []int64{target.ID}, 3, service.AdjustmentOperationAdd)
		require.NoError(t, err)
		require.Equal(t, 1, affected)
	}
	require.Equal(t, 5, currentUserConcurrency(t, target.ID))
	assertAdjustmentActionRows(t, batchActionID, service.AdjustmentKindConcurrency, 1)
	require.Equal(t, int64(1), redeemAdjustmentCount(t, target.ID, service.AdjustmentTypeAdminConcurrency))
}

func TestAdminConcurrencyBatchLedgerAndConcurrentBalanceAdjustment(t *testing.T) {
	client := testEntClient(t)
	userRepo := NewUserRepository(client, integrationDB)
	redeemRepo := NewRedeemCodeRepository(client)
	adjustmentRepo := NewAdminUserAdjustmentRepository(client)
	adminService := newAdjustmentIntegrationAdminService(client, userRepo, redeemRepo, adjustmentRepo)
	operator := mustCreateUser(t, client, &service.User{
		Email: "batch-owner-" + uuid.NewString() + "@example.com", Role: service.RoleAdmin,
	})
	first := mustCreateUser(t, client, &service.User{Email: "batch-first-" + uuid.NewString() + "@example.com", Concurrency: 2})
	second := mustCreateUser(t, client, &service.User{Email: "batch-second-" + uuid.NewString() + "@example.com", Concurrency: 3})

	setCtx := adjustmentIntegrationContext(operator, "batch set")
	setMetadata := service.AdminAdjustmentMetadataFromContext(setCtx)
	affected, err := adminService.BatchUpdateConcurrency(setCtx, []int64{first.ID, second.ID, first.ID}, 5, service.AdjustmentOperationSet)
	require.NoError(t, err)
	require.Equal(t, 2, affected)
	assertAdjustmentActionRows(t, setMetadata.ActionID, service.AdjustmentKindConcurrency, 2)

	rpm := 60
	affected, err = adminService.BatchUpdateLimits(adjustmentIntegrationContext(operator, "batch limits"), []int64{first.ID, second.ID}, intPointer(4), &rpm)
	require.NoError(t, err)
	require.Equal(t, 2, affected)
	require.Equal(t, 4, currentUserConcurrency(t, first.ID))
	require.Equal(t, 60, currentUserRPM(t, first.ID))

	before := adjustmentCountForUsers(t, first.ID, second.ID)
	affected, err = adminService.BatchUpdateLimits(adjustmentIntegrationContext(operator, "no change"), []int64{first.ID, second.ID}, intPointer(4), &rpm)
	require.NoError(t, err)
	require.Equal(t, 2, affected)
	require.Equal(t, before, adjustmentCountForUsers(t, first.ID, second.ID), "unchanged concurrency must not create ledger rows")

	rpmOnly := 90
	_, err = adminService.BatchUpdateLimits(adjustmentIntegrationContext(operator, "rpm only"), []int64{first.ID, second.ID}, nil, &rpmOnly)
	require.NoError(t, err)
	require.Equal(t, before, adjustmentCountForUsers(t, first.ID, second.ID), "RPM-only updates are not administrator value adjustments")
	require.Equal(t, adjustmentCountForUsers(t, first.ID, second.ID), redeemAdjustmentCountForUsers(t, service.AdjustmentTypeAdminConcurrency, first.ID, second.ID))

	balanceTarget := mustCreateUser(t, client, &service.User{Email: "concurrent-balance-" + uuid.NewString() + "@example.com", Balance: 100})
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wait sync.WaitGroup
	wait.Add(2)
	go func() {
		defer wait.Done()
		<-start
		_, err := adminService.UpdateUserBalance(adjustmentIntegrationContext(operator, "concurrent admin add"), balanceTarget.ID, "10", service.AdjustmentOperationAdd, "concurrent admin add")
		errs <- err
	}()
	go func() {
		defer wait.Done()
		<-start
		_, err := userRepo.AdjustBalance(context.Background(), balanceTarget.ID, -7)
		errs <- err
	}()
	close(start)
	wait.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	require.InDelta(t, 103, currentUserBalance(t, balanceTarget.ID), 1e-9)

	var beforeValue, afterValue, delta float64
	err = integrationDB.QueryRowContext(context.Background(), `
SELECT before_value::float8, after_value::float8, delta::float8
FROM admin_user_adjustments
WHERE user_id = $1 AND notes = 'concurrent admin add'`, balanceTarget.ID).Scan(&beforeValue, &afterValue, &delta)
	require.NoError(t, err)
	require.InDelta(t, 10, delta, 1e-9)
	require.InDelta(t, delta, afterValue-beforeValue, 1e-9)
}

func TestAdminSetBalanceLocksBeforeCapturingLedgerValues(t *testing.T) {
	client := testEntClient(t)
	userRepo := NewUserRepository(client, integrationDB)
	adminService := newAdjustmentIntegrationAdminService(
		client,
		userRepo,
		NewRedeemCodeRepository(client),
		NewAdminUserAdjustmentRepository(client),
	)
	operator := mustCreateUser(t, client, &service.User{
		Email: "set-race-owner-" + uuid.NewString() + "@example.com", Role: service.RoleAdmin,
	})
	target := mustCreateUser(t, client, &service.User{
		Email: "set-race-target-" + uuid.NewString() + "@example.com", Balance: 100,
	})

	billingTx, err := integrationDB.BeginTx(context.Background(), nil)
	require.NoError(t, err)
	defer func() { _ = billingTx.Rollback() }()
	_, err = billingTx.ExecContext(context.Background(),
		"UPDATE users SET balance = balance - 7, updated_at = NOW() WHERE id = $1", target.ID)
	require.NoError(t, err)

	result := make(chan error, 1)
	go func() {
		_, updateErr := adminService.UpdateUserBalance(
			adjustmentIntegrationContext(operator, "set after billing lock"),
			target.ID,
			"20",
			service.AdjustmentOperationSet,
			"set after billing lock",
		)
		result <- updateErr
	}()
	waitForBlockedAdminSet(t)
	require.NoError(t, billingTx.Commit())
	require.NoError(t, <-result)
	require.InDelta(t, 20, currentUserBalance(t, target.ID), 1e-9)

	var beforeValue, afterValue, delta float64
	err = integrationDB.QueryRowContext(context.Background(), `
SELECT before_value::float8, after_value::float8, delta::float8
FROM admin_user_adjustments
WHERE user_id = $1 AND notes = 'set after billing lock'`, target.ID).Scan(&beforeValue, &afterValue, &delta)
	require.NoError(t, err)
	require.InDelta(t, 93, beforeValue, 1e-9)
	require.InDelta(t, 20, afterValue, 1e-9)
	require.InDelta(t, -73, delta, 1e-9)
}

func TestCreateUserInitialValuesDoNotCreateAdjustmentLedger(t *testing.T) {
	client := testEntClient(t)
	userRepo := NewUserRepository(client, integrationDB)
	adminService := newAdjustmentIntegrationAdminService(
		client,
		userRepo,
		NewRedeemCodeRepository(client),
		NewAdminUserAdjustmentRepository(client),
	)
	balance := 25.0
	created, err := adminService.CreateUser(context.Background(), &service.CreateUserInput{
		Email: "initial-values-" + uuid.NewString() + "@example.com", Password: "Strong-password-123!",
		Balance: &balance, Concurrency: 7,
	})
	require.NoError(t, err)
	require.Equal(t, int64(0), adjustmentCountForUsers(t, created.ID))
}

func newAdjustmentIntegrationAdminService(
	client *dbent.Client,
	userRepo service.UserRepository,
	redeemRepo service.RedeemCodeRepository,
	adjustmentRepo service.AdminUserAdjustmentRepository,
) service.AdminService {
	return newAdjustmentIntegrationAdminServiceWithGroupRates(client, userRepo, redeemRepo, adjustmentRepo, nil)
}

func newAdjustmentIntegrationAdminServiceWithGroupRates(
	client *dbent.Client,
	userRepo service.UserRepository,
	redeemRepo service.RedeemCodeRepository,
	adjustmentRepo service.AdminUserAdjustmentRepository,
	groupRateRepo service.UserGroupRateRepository,
) service.AdminService {
	return service.NewAdminService(
		userRepo, nil, nil, nil, nil, redeemRepo, adjustmentRepo, groupRateRepo, nil, nil,
		nil, nil, nil, client, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
	)
}

func adjustmentIntegrationContext(operator *service.User, notes string) context.Context {
	operatorID := operator.ID
	return service.WithAdminAdjustmentMetadata(context.Background(), service.AdminAdjustmentMetadata{
		OperatorID: &operatorID, OperatorEmail: operator.Email, Notes: notes,
		ClientIP: "127.0.0.1", AuthMethod: "jwt", RequestID: uuid.NewString(),
	})
}

func adjustmentIntegrationIdempotentContext(operator *service.User, actionID uuid.UUID, notes string) context.Context {
	operatorID := operator.ID
	return service.WithAdminAdjustmentMetadata(context.Background(), service.AdminAdjustmentMetadata{
		ActionID: actionID, Idempotent: true, OperatorID: &operatorID, OperatorEmail: operator.Email, Notes: notes,
		ClientIP: "127.0.0.1", AuthMethod: "jwt", RequestID: uuid.NewString(),
	})
}

func adjustmentCountForUsers(t *testing.T, userIDs ...int64) int64 {
	t.Helper()
	var count int64
	require.NoError(t, integrationDB.QueryRowContext(context.Background(),
		"SELECT COUNT(*) FROM admin_user_adjustments WHERE user_id = ANY($1)", pq.Array(userIDs)).Scan(&count))
	return count
}

func redeemAdjustmentCount(t *testing.T, userID int64, kind string) int64 {
	t.Helper()
	return redeemAdjustmentCountForUsers(t, kind, userID)
}

func redeemAdjustmentCountForUsers(t *testing.T, kind string, userIDs ...int64) int64 {
	t.Helper()
	var count int64
	require.NoError(t, integrationDB.QueryRowContext(context.Background(),
		"SELECT COUNT(*) FROM redeem_codes WHERE used_by = ANY($1) AND type = $2", pq.Array(userIDs), kind).Scan(&count))
	return count
}

func currentUserBalance(t *testing.T, userID int64) float64 {
	t.Helper()
	var value float64
	require.NoError(t, integrationDB.QueryRowContext(context.Background(), "SELECT balance FROM users WHERE id = $1", userID).Scan(&value))
	return value
}

func currentUserConcurrency(t *testing.T, userID int64) int {
	t.Helper()
	var value int
	require.NoError(t, integrationDB.QueryRowContext(context.Background(), "SELECT concurrency FROM users WHERE id = $1", userID).Scan(&value))
	return value
}

func currentUserRPM(t *testing.T, userID int64) int {
	t.Helper()
	var value int
	require.NoError(t, integrationDB.QueryRowContext(context.Background(), "SELECT rpm_limit FROM users WHERE id = $1", userID).Scan(&value))
	return value
}

func assertAdjustmentActionRows(t *testing.T, actionID uuid.UUID, kind string, expected int) {
	t.Helper()
	var count int
	require.NoError(t, integrationDB.QueryRowContext(context.Background(),
		"SELECT COUNT(*) FROM admin_user_adjustments WHERE action_id = $1 AND kind = $2", actionID, kind).Scan(&count))
	require.Equal(t, expected, count)
}

func waitForBlockedAdminSet(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var waiting int
		err := integrationDB.QueryRowContext(context.Background(), `
SELECT COUNT(*)
FROM pg_stat_activity
WHERE query LIKE '%admin_set_balance%'
  AND wait_event_type = 'Lock'`).Scan(&waiting)
		require.NoError(t, err)
		if waiting > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("admin set balance query did not block on the billing row lock")
}

func adjustmentValueOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func intPointer(value int) *int { return &value }
