package repository

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// ApplyWithUsageLog is the strict transaction used by synthetic probe
// followers. It claims the billing idempotency key, applies all user/provider
// effects, and inserts the usage row before committing. A failed usage insert
// therefore rolls back the debit and the dedup claim together.
func (r *usageBillingRepository) ApplyWithUsageLog(ctx context.Context, cmd *service.UsageBillingCommand, usageLog *service.UsageLog) (_ *service.UsageBillingApplyResult, err error) {
	if cmd == nil {
		return &service.UsageBillingApplyResult{}, nil
	}
	if usageLog == nil {
		return nil, errors.New("usage log is nil")
	}
	if r == nil || r.db == nil {
		return nil, errors.New("usage billing repository db is nil")
	}

	cmd.Normalize()
	if cmd.RequestID == "" {
		return nil, service.ErrUsageBillingRequestIDRequired
	}
	if strings.TrimSpace(usageLog.RequestID) != cmd.RequestID || usageLog.APIKeyID != cmd.APIKeyID {
		return nil, errors.New("usage log identity does not match billing command")
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()

	applied, err := r.claimUsageBillingKey(ctx, tx, cmd)
	if err != nil {
		return nil, err
	}
	if !applied {
		// A dedup key can outlive a failed/non-atomic usage-log write from an
		// older path. Verify the row before treating the retry as complete; for
		// an active (non-archived) key, repair the missing row in this transaction.
		persisted, err := ensureUsageLogForExistingBilling(ctx, tx, cmd, usageLog)
		if err != nil {
			return nil, err
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		tx = nil
		return &service.UsageBillingApplyResult{Applied: false, UsageLogPersisted: persisted}, nil
	}

	result := &service.UsageBillingApplyResult{Applied: true, UsageLogPersisted: true}
	if err := r.applyUsageBillingEffects(ctx, tx, cmd, result); err != nil {
		return nil, err
	}
	if err := execUsageLogInsertNoResult(ctx, tx, prepareUsageLogInsert(usageLog)); err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	tx = nil
	return result, nil
}

func ensureUsageLogForExistingBilling(ctx context.Context, tx *sql.Tx, cmd *service.UsageBillingCommand, usageLog *service.UsageLog) (bool, error) {
	var exists bool
	if err := tx.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM usage_logs WHERE request_id = $1 AND api_key_id = $2
		)
	`, cmd.RequestID, cmd.APIKeyID).Scan(&exists); err != nil {
		return false, err
	}
	if exists {
		return true, nil
	}

	// Archived dedup keys represent a historical request whose usage row may
	// have been intentionally purged. Never synthesize a new row for that case.
	var archived bool
	if err := tx.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM usage_billing_dedup_archive WHERE request_id = $1 AND api_key_id = $2
		)
	`, cmd.RequestID, cmd.APIKeyID).Scan(&archived); err != nil {
		return false, err
	}
	if archived {
		return false, errors.New("usage billing dedup exists but usage log is unavailable")
	}

	if err := execUsageLogInsertNoResult(ctx, tx, prepareUsageLogInsert(usageLog)); err != nil {
		return false, err
	}
	return true, nil
}
