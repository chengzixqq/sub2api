package service

import (
	"context"
	"net/http"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const (
	DispatchScopeShared         = "shared"
	DispatchScopeAuthorizedOnly = "authorized_only"
	AccountDispatchModeAppend   = "append"
	AccountDispatchModeReplace  = "replace"
	AccountDispatchOriginMember = "member"
	AccountDispatchOriginExtra  = "extra"
)

type AccountDispatchGrant struct {
	AccountID        int64  `json:"account_id"`
	Origin           string `json:"origin"`
	AccountName      string `json:"account_name,omitempty"`
	DispatchScope    string `json:"dispatch_scope,omitempty"`
	Eligible         *bool  `json:"eligible,omitempty"`
	IneligibleReason string `json:"ineligible_reason,omitempty"`
}

type AccountDispatchPolicy struct {
	UserID   int64                  `json:"user_id"`
	GroupID  int64                  `json:"group_id"`
	Version  int64                  `json:"version"`
	Mode     string                 `json:"mode"`
	Accounts []AccountDispatchGrant `json:"accounts"`
	Username string                 `json:"username,omitempty"`
	Email    string                 `json:"email,omitempty"`
}

// AccountDispatchPolicyReader is optional on AccountRepository so existing test doubles remain valid.
type AccountDispatchPolicyReader interface {
	GetAccountDispatchPolicy(ctx context.Context, userID, groupID int64) (*AccountDispatchPolicy, error)
}

type AccountDispatchPolicyWriter interface {
	AccountDispatchPolicyReader
	ListAccountDispatchPolicies(ctx context.Context, groupID int64) ([]AccountDispatchPolicy, error)
	PutAccountDispatchPolicy(ctx context.Context, policy *AccountDispatchPolicy, expectedVersion int64) (*AccountDispatchPolicy, error)
	DeleteAccountDispatchPolicy(ctx context.Context, userID, groupID, expectedVersion int64) error
}

type AccountDispatchAdminService interface {
	ListAccountDispatchPolicies(ctx context.Context, groupID int64) ([]AccountDispatchPolicy, error)
	PutAccountDispatchPolicy(ctx context.Context, policy *AccountDispatchPolicy, expectedVersion int64) (*AccountDispatchPolicy, error)
	DeleteAccountDispatchPolicy(ctx context.Context, userID, groupID, expectedVersion int64) error
}

type accountDispatchScopeContextKey struct{}

func withAccountDispatchScopeUpdate(ctx context.Context, scope string) context.Context {
	return context.WithValue(ctx, accountDispatchScopeContextKey{}, scope)
}

func AccountDispatchScopeUpdateFromContext(ctx context.Context) (string, bool) {
	scope, ok := ctx.Value(accountDispatchScopeContextKey{}).(string)
	return scope, ok
}

func ErrAccountDispatchPolicyVersionConflict(version int64) error {
	return infraerrors.Newf(http.StatusPreconditionFailed, "ACCOUNT_DISPATCH_POLICY_VERSION_CONFLICT", "account dispatch policy version changed (current=%d)", version)
}
