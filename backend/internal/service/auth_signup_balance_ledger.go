package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// E10 / Q12 "站点 default_balance 入账": balances granted from the site defaults when a user signs up, logs in with a
// third-party account for the first time or binds one for the first time are ledger entries like an admin top-up,
// written in the same transaction as the grant (see admin_user_balance_ledger.go).

// SetAdminUserAdjustmentRepository enables the ledger entry for balances granted from the site defaults at signup,
// at a third-party first login and on a first third-party bind. Without it (and without an ent client and redeem
// repository) those grants are written to the user only, as before.
func (s *AuthService) SetAdminUserAdjustmentRepository(repo AdminUserAdjustmentRepository) {
	s.adjustmentRepo = repo
}

func (s *AuthService) canRecordGrantedBalance() bool {
	return s != nil && s.entClient != nil && s.redeemRepo != nil && s.adjustmentRepo != nil
}

// signupGrantMetadata describes a site grant: there is no operator; the request id ties it to the signup request.
func signupGrantMetadata(ctx context.Context) AdminAdjustmentMetadata {
	metadata := AdminAdjustmentMetadata{ActionID: uuid.New()}
	if requestID, _ := ctx.Value(ctxkey.RequestID).(string); strings.TrimSpace(requestID) != "" {
		metadata.RequestID = strings.TrimSpace(requestID)
	} else if requestID, _ := ctx.Value(ctxkey.ClientRequestID).(string); strings.TrimSpace(requestID) != "" {
		metadata.RequestID = strings.TrimSpace(requestID)
	}
	return metadata
}

// createSignupUser runs create and, when the new user starts with a positive balance (site default_balance or the
// provider grant), records it in the ledger in the same transaction (source default_balance, no operator). It reuses
// a transaction already carried by ctx (registration with an invitation code), otherwise opens one. A ledger failure
// fails the signup: the user is not created.
func (s *AuthService) createSignupUser(ctx context.Context, user *User, create func(context.Context, *User) error) error {
	if user == nil || user.Balance <= 0 || !s.canRecordGrantedBalance() {
		return create(ctx, user)
	}
	if dbent.TxFromContext(ctx) != nil {
		if err := create(ctx, user); err != nil {
			return err
		}
		return s.recordSignupBalance(ctx, user)
	}
	tx, err := s.entClient.Tx(ctx)
	if err != nil {
		return fmt.Errorf("begin signup transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	txCtx := dbent.NewTxContext(ctx, tx)
	if err := create(txCtx, user); err != nil {
		return err
	}
	if err := s.recordSignupBalance(txCtx, user); err != nil {
		return err
	}
	return tx.Commit()
}

// recordSignupBalance writes the initial-balance entry of a user just created in the transaction carried by ctx.
func (s *AuthService) recordSignupBalance(ctx context.Context, user *User) error {
	if user == nil || user.Balance <= 0 || !s.canRecordGrantedBalance() {
		return nil
	}
	tx := dbent.TxFromContext(ctx)
	if tx == nil {
		return errors.New("signup balance ledger requires a transaction")
	}
	return recordInitialBalance(ctx, tx.Client(), s.redeemRepo, s.adjustmentRepo, user, AdjustmentSourceDefaultBalance, signupGrantMetadata(ctx))
}

// grantFirstBindBalance adds the provider grant of a first third-party bind to an existing user inside the
// transaction of client and records it (operation add, source default_balance, no operator). Without the ledger
// dependencies, or for a non-positive amount, it adds the balance as before.
func (s *AuthService) grantFirstBindBalance(ctx context.Context, client *dbent.Client, userID int64, amount float64) error {
	delta := decimal.NewFromFloat(amount).Round(8)
	exactRepo, exact := s.userRepo.(ExactBalanceAdjustmentRepository)
	if !delta.IsPositive() || !exact || !s.canRecordGrantedBalance() || dbent.TxFromContext(ctx) == nil {
		return client.User.UpdateOneID(userID).AddBalance(amount).Exec(ctx)
	}
	change, err := exactRepo.AdjustBalanceExact(ctx, userID, delta.StringFixed(8))
	if err != nil {
		return err
	}
	before, after, applied, err := exactBalanceChange(change)
	if err != nil {
		return err
	}
	if !applied.IsPositive() {
		return nil
	}
	email, name, err := adminAdjustmentUserSnapshot(ctx, client, userID)
	if err != nil {
		return err
	}
	metadata := signupGrantMetadata(ctx)
	metadata.Notes = adjustmentFirstBindGrantNotes
	requested := delta.StringFixed(8)
	beforeValue := before.StringFixed(8)
	afterValue := after.StringFixed(8)
	deltaValue := applied.StringFixed(8)
	if err := writeAdjustmentRecords(ctx, client, s.redeemRepo, s.adjustmentRepo, []AdminUserAdjustmentWrite{{
		ActionID:       metadata.ActionID,
		Kind:           AdjustmentKindBalance,
		Operation:      AdjustmentOperationAdd,
		RequestedValue: &requested,
		Delta:          deltaValue,
		BeforeValue:    &beforeValue,
		AfterValue:     &afterValue,
		UserID:         userID,
		UserEmail:      adjustmentStringPtr(email),
		UserName:       adjustmentStringPtr(name),
		Source:         AdjustmentSourceDefaultBalance,
	}}, metadata); err != nil {
		return err
	}
	return accrueTotalRecharged(ctx, client, userID, deltaValue)
}
