package service

import (
	"context"
	"errors"
)

// ClearAccountErrorOptions tunes ClearAccountErrorWithOptions. The zero value
// keeps the historical behavior.
type ClearAccountErrorOptions struct {
	// PreserveInactive leaves accounts whose status is neither active nor
	// error (inactive, legacy disabled) untouched: their status, error message
	// and error time stay as they are, while rate limits, overload,
	// temporary unschedulable state and model rate limits are still cleared.
	// Without it every account is reactivated, as before.
	PreserveInactive bool
}

// ClearAccountErrorResult reports the outcome of ClearAccountErrorWithOptions.
type ClearAccountErrorResult struct {
	Account *Account
	// SkippedInactive is true when PreserveInactive kept the account's status
	// because it was not active or error. It reflects the conditional update's
	// affected rows, not a separate read, so it is free of read-then-write races.
	SkippedInactive bool
}

// AccountErrorClearerPreservingInactive is an optional repository capability
// used by ClearAccountErrorWithOptions with PreserveInactive.
//
// ClearErrorPreservingInactive clears the error and sets the account active
// only when its status is active or error, in a single conditional update. It
// reports false, without touching the account or the scheduler snapshot, when
// the account is in any other status.
type AccountErrorClearerPreservingInactive interface {
	ClearErrorPreservingInactive(ctx context.Context, id int64) (bool, error)
}

var errAccountConditionalErrorClearNotConfigured = errors.New("account conditional error clear is not configured")

// ClearAccountErrorWithOptions clears an account's error and recoverable
// runtime state. See ClearAccountErrorOptions for PreserveInactive.
//
// Ownership is checked before any write, and with PreserveInactive the
// repository capability is resolved before any write too, so a misconfigured
// repository never leaves the account half cleared.
func (s *adminServiceImpl) ClearAccountErrorWithOptions(ctx context.Context, id int64, opts ClearAccountErrorOptions) (*ClearAccountErrorResult, error) {
	if err := s.requireAccountAccess(ctx, id); err != nil {
		return nil, err
	}
	skipped := false
	if opts.PreserveInactive {
		clearer, ok := s.accountRepo.(AccountErrorClearerPreservingInactive)
		if !ok {
			return nil, errAccountConditionalErrorClearNotConfigured
		}
		cleared, err := clearer.ClearErrorPreservingInactive(ctx, id)
		if err != nil {
			return nil, err
		}
		skipped = !cleared
	} else if err := s.accountRepo.ClearError(ctx, id); err != nil {
		return nil, err
	}
	if err := s.clearAccountRecoverableRuntimeState(ctx, id); err != nil {
		return nil, err
	}
	account, err := s.accountRepo.GetByIDScoped(ctx, id)
	if err != nil {
		return nil, err
	}
	return &ClearAccountErrorResult{Account: account, SkippedInactive: skipped}, nil
}

// clearAccountRecoverableRuntimeState clears rate limits, Antigravity quota
// scopes, model rate limits, temporary unschedulable state and the in-memory
// scheduling block — everything ClearAccountError resets besides the status.
func (s *adminServiceImpl) clearAccountRecoverableRuntimeState(ctx context.Context, id int64) error {
	if err := s.accountRepo.ClearRateLimit(ctx, id); err != nil {
		return err
	}
	if err := s.accountRepo.ClearAntigravityQuotaScopes(ctx, id); err != nil {
		return err
	}
	if err := s.accountRepo.ClearModelRateLimits(ctx, id); err != nil {
		return err
	}
	if err := s.accountRepo.ClearTempUnschedulable(ctx, id); err != nil {
		return err
	}
	if s.runtimeBlocker != nil {
		s.runtimeBlocker.ClearAccountSchedulingBlock(id)
	}
	return nil
}
