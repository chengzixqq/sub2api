package service

import (
	"context"
	"fmt"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/shopspring/decimal"
)

// E10: every balance a user starts with, and every admin top-up, is an entry of the adjustment ledger with its
// compatibility redeem_codes row, so it shows in "充值与调整记录" and can be reconciled by request / action id.

// ErrUserBalanceNotEditable rejects a balance in PUT /admin/users/:id (Q13): it used to be accepted and silently
// ignored. Balances change only through POST /admin/users/:id/balance, which writes the ledger.
var ErrUserBalanceNotEditable = infraerrors.BadRequest(
	"USER_BALANCE_NOT_EDITABLE",
	"balance cannot be changed with PUT /admin/users/:id; use POST /admin/users/:id/balance",
)

// UserRechargeBreakdown splits a user's top-ups by source. Redeem is positive balance redeem codes (payment orders
// are credited this way too), Admin is positive admin_balance rows (admin top-ups, a raise by set, initial balances
// and site default grants), Affiliate is affiliate rebates transferred to the balance and Promo is promo-code bonuses.
// balance-history's total_recharged is Redeem + Admin.
type UserRechargeBreakdown struct {
	Redeem    float64
	Admin     float64
	Affiliate float64
	Promo     float64
}

// userRechargeBreakdownReader is optional on RedeemCodeRepository so existing test doubles keep compiling.
type userRechargeBreakdownReader interface {
	SumRechargeBreakdownByUser(ctx context.Context, userID int64) (UserRechargeBreakdown, error)
}

func (s *adminServiceImpl) GetUserRechargeBreakdown(ctx context.Context, userID int64) (*UserRechargeBreakdown, error) {
	reader, ok := s.redeemCodeRepo.(userRechargeBreakdownReader)
	if !ok {
		return nil, nil
	}
	breakdown, err := reader.SumRechargeBreakdownByUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	return &breakdown, nil
}

func isAdminAdjustmentRedeemType(codeType string) bool {
	return codeType == AdjustmentTypeAdminBalance || codeType == AdjustmentTypeAdminConcurrency
}

// attachAdjustmentLinks fills RedeemCode.Adjustment for the admin adjustment rows of one balance-history page.
// Redeemed codes and affiliate transfers (negative synthetic ids) are not ledger entries.
func (s *adminServiceImpl) attachAdjustmentLinks(ctx context.Context, userID int64, codes []RedeemCode) error {
	reader, ok := s.adjustmentRepo.(adminAdjustmentLinkReader)
	if !ok {
		return nil
	}
	ids := make([]int64, 0, len(codes))
	for i := range codes {
		if codes[i].ID > 0 && isAdminAdjustmentRedeemType(codes[i].Type) {
			ids = append(ids, codes[i].ID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	links, err := reader.LinksByRedeemCodeIDs(ctx, userID, ids)
	if err != nil {
		return fmt.Errorf("load admin adjustment links: %w", err)
	}
	for i := range codes {
		if codes[i].ID <= 0 || !isAdminAdjustmentRedeemType(codes[i].Type) {
			continue
		}
		if link, found := links[codes[i].ID]; found {
			codes[i].Adjustment = &link
		}
	}
	return nil
}

// resolveInitialBalance returns the balance a new admin-created user starts with and the ledger source: an explicit
// balance, else the site default_balance. Without a setting service the user starts at 0 and there is no source.
func (s *adminServiceImpl) resolveInitialBalance(ctx context.Context, input *CreateUserInput) (float64, string) {
	if input.Balance != nil {
		return *input.Balance, AdjustmentSourceAdminCreateUser
	}
	if s.settingService != nil {
		return s.settingService.GetDefaultBalance(ctx), AdjustmentSourceDefaultBalance
	}
	return 0, ""
}

// createUserWithInitialBalance creates the user and records its initial balance in one transaction: if the ledger
// cannot be written, the user is not created. The email uniqueness lock taken by Create is held until the commit.
func (s *adminServiceImpl) createUserWithInitialBalance(ctx context.Context, user *User, source string) error {
	tx, err := s.entClient.Tx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	opCtx := dbent.NewTxContext(ctx, tx)
	if err := s.userRepo.Create(opCtx, user); err != nil {
		return err
	}
	metadata := AdminAdjustmentMetadataFromContext(ctx)
	if err := recordInitialBalance(opCtx, tx.Client(), s.redeemCodeRepo, s.adjustmentRepo, user, source, metadata); err != nil {
		return err
	}
	return tx.Commit()
}

// recordInitialBalance writes the ledger entry of a user created in the transaction of client: kind balance,
// operation set, before 0, after the stored balance (read back, so it matches DECIMAL(20,8) exactly), plus the
// compatibility admin_balance redeem code, and accrues users.total_recharged. A stored balance of 0 or less
// (a negative initial balance, or one rounded away) writes nothing.
func recordInitialBalance(ctx context.Context, client *dbent.Client, redeemRepo RedeemCodeRepository, adjustmentRepo AdminUserAdjustmentRepository, user *User, source string, metadata AdminAdjustmentMetadata) error {
	stored, err := userBalanceExact(ctx, client, user.ID)
	if err != nil {
		return fmt.Errorf("read initial balance: %w", err)
	}
	if !stored.IsPositive() {
		return nil
	}
	if metadata.Notes == "" {
		metadata.Notes = adjustmentInitialBalanceNotes
	}
	after := stored.StringFixed(8)
	before := decimal.Zero.StringFixed(8)
	if err := writeAdjustmentRecords(ctx, client, redeemRepo, adjustmentRepo, []AdminUserAdjustmentWrite{{
		ActionID:       metadata.ActionID,
		Kind:           AdjustmentKindBalance,
		Operation:      AdjustmentOperationSet,
		RequestedValue: &after,
		Delta:          after,
		BeforeValue:    &before,
		AfterValue:     &after,
		UserID:         user.ID,
		UserEmail:      adjustmentStringPtr(user.Email),
		UserName:       adjustmentStringPtr(user.Username),
		Source:         source,
	}}, metadata); err != nil {
		return err
	}
	if err := accrueTotalRecharged(ctx, client, user.ID, after); err != nil {
		return err
	}
	accrued, _ := stored.Float64()
	user.TotalRecharged += accrued
	return nil
}

func userBalanceExact(ctx context.Context, client *dbent.Client, userID int64) (balance decimal.Decimal, err error) {
	rows, err := client.QueryContext(ctx, `SELECT balance::text FROM users WHERE id = $1 AND deleted_at IS NULL`, userID)
	if err != nil {
		return decimal.Zero, err
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
	}()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return decimal.Zero, err
		}
		return decimal.Zero, ErrUserNotFound
	}
	var raw string
	if err := rows.Scan(&raw); err != nil {
		return decimal.Zero, err
	}
	balance, err = decimal.NewFromString(raw)
	if err != nil {
		return decimal.Zero, fmt.Errorf("parse user balance: %w", err)
	}
	return balance, rows.Err()
}

// accrueTotalRecharged adds a top-up to users.total_recharged, the base of percentage balance notifications (Q12:
// admin top-ups and initial balances count like redeemed codes). Decreases never lower it.
func accrueTotalRecharged(ctx context.Context, client *dbent.Client, userID int64, amount string) error {
	result, err := client.ExecContext(ctx,
		`UPDATE users SET total_recharged = total_recharged + $1::numeric, updated_at = NOW() WHERE id = $2 AND deleted_at IS NULL`,
		amount, userID)
	if err != nil {
		return fmt.Errorf("accrue total recharged: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("accrue total recharged: %w", err)
	}
	if affected == 0 {
		return ErrUserNotFound
	}
	return nil
}

// writeAdjustmentRecords writes ledger rows and their compatibility redeem_codes rows (admin_balance /
// admin_concurrency, used by the target, same timestamp) inside the transaction of client, and links each ledger row
// to its redeem code. The operator snapshot is taken from metadata.OperatorID; rows without an operator (site grants)
// keep it empty.
func writeAdjustmentRecords(ctx context.Context, client *dbent.Client, redeemRepo RedeemCodeRepository, adjustmentRepo AdminUserAdjustmentRepository, rows []AdminUserAdjustmentWrite, metadata AdminAdjustmentMetadata) error {
	if len(rows) == 0 {
		return nil
	}
	var operatorName *string
	if metadata.OperatorID != nil {
		operatorEmail, name, err := adminAdjustmentUserSnapshot(ctx, client, *metadata.OperatorID)
		if err != nil {
			return fmt.Errorf("snapshot adjustment operator: %w", err)
		}
		metadata.OperatorEmail = operatorEmail
		operatorName = adjustmentStringPtr(name)
	}
	now := time.Now()
	redeemCodes := make([]RedeemCode, 0, len(rows))
	for i := range rows {
		code, err := GenerateRedeemCode()
		if err != nil {
			return err
		}
		recordType := AdjustmentTypeAdminBalance
		if rows[i].Kind == AdjustmentKindConcurrency {
			recordType = AdjustmentTypeAdminConcurrency
		}
		userID := rows[i].UserID
		delta, err := decimal.NewFromString(rows[i].Delta)
		if err != nil {
			return fmt.Errorf("parse adjustment delta: %w", err)
		}
		deltaFloat, _ := delta.Float64()
		redeemCodes = append(redeemCodes, RedeemCode{
			Code: code, Type: recordType, Value: deltaFloat, ValueExact: delta.StringFixed(8), Status: StatusUsed,
			UsedBy: &userID, UsedAt: &now, Notes: metadata.Notes,
		})
		rows[i].OperatorUserID = metadata.OperatorID
		rows[i].OperatorEmail = adjustmentStringPtr(metadata.OperatorEmail)
		rows[i].OperatorName = operatorName
		rows[i].Notes = adjustmentStringPtr(metadata.Notes)
		rows[i].ClientIP = adjustmentStringPtr(metadata.ClientIP)
		rows[i].AuthMethod = adjustmentStringPtr(metadata.AuthMethod)
		rows[i].RequestID = adjustmentStringPtr(metadata.RequestID)
		rows[i].CreatedAt = now
	}
	if err := redeemRepo.CreateBatch(ctx, redeemCodes); err != nil {
		return fmt.Errorf("create compatibility adjustment records: %w", err)
	}
	for i := range rows {
		if redeemCodes[i].ID > 0 {
			id := redeemCodes[i].ID
			rows[i].RedeemCodeID = &id
		}
	}
	if err := adjustmentRepo.CreateBatch(ctx, rows); err != nil {
		return fmt.Errorf("create admin user adjustments: %w", err)
	}
	return nil
}
