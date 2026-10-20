//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/stretchr/testify/require"
)

type accountRepoStubForClearAccountError struct {
	mockAccountRepoForGemini
	account                  *Account
	clearErrorCalls          int
	clearRateLimitCalls      int
	clearAntigravityCalls    int
	clearModelRateLimitCalls int
	clearTempUnschedCalls    int
}

func (r *accountRepoStubForClearAccountError) GetByID(ctx context.Context, id int64) (*Account, error) {
	return r.account, nil
}

// GetByIDScoped 是管理端专用的带归属过滤读取。
//
// 委托给 GetByID：本 stub 服务的测试与工作区归属无关，
// 归属过滤的语义由专门的作用域测试覆盖。
func (r *accountRepoStubForClearAccountError) GetByIDScoped(ctx context.Context, id int64) (*Account, error) {
	return r.GetByID(ctx, id)
}

func (r *accountRepoStubForClearAccountError) ClearError(ctx context.Context, id int64) error {
	r.clearErrorCalls++
	r.account.Status = StatusActive
	r.account.ErrorMessage = ""
	return nil
}

func (r *accountRepoStubForClearAccountError) ClearRateLimit(ctx context.Context, id int64) error {
	r.clearRateLimitCalls++
	r.account.RateLimitedAt = nil
	r.account.RateLimitResetAt = nil
	return nil
}

func (r *accountRepoStubForClearAccountError) ClearAntigravityQuotaScopes(ctx context.Context, id int64) error {
	r.clearAntigravityCalls++
	return nil
}

func (r *accountRepoStubForClearAccountError) ClearModelRateLimits(ctx context.Context, id int64) error {
	r.clearModelRateLimitCalls++
	return nil
}

func (r *accountRepoStubForClearAccountError) ClearTempUnschedulable(ctx context.Context, id int64) error {
	r.clearTempUnschedCalls++
	r.account.TempUnschedulableUntil = nil
	r.account.TempUnschedulableReason = ""
	return nil
}

func TestAdminService_ClearAccountError_AlsoClearsRecoverableRuntimeState(t *testing.T) {
	until := time.Now().Add(10 * time.Minute)
	resetAt := time.Now().Add(5 * time.Minute)
	repo := &accountRepoStubForClearAccountError{
		account: &Account{
			ID:                      31,
			Platform:                PlatformOpenAI,
			Type:                    AccountTypeOAuth,
			Status:                  StatusError,
			ErrorMessage:            "refresh failed",
			RateLimitResetAt:        &resetAt,
			TempUnschedulableUntil:  &until,
			TempUnschedulableReason: "missing refresh token",
		},
	}
	blocker := &runtimeBlockRecorder{}
	svc := &adminServiceImpl{accountRepo: repo, runtimeBlocker: blocker}

	updated, err := svc.ClearAccountError(context.Background(), 31)
	require.NoError(t, err)
	require.NotNil(t, updated)
	require.Equal(t, 1, repo.clearErrorCalls)
	require.Equal(t, 1, repo.clearRateLimitCalls)
	require.Equal(t, 1, repo.clearAntigravityCalls)
	require.Equal(t, 1, repo.clearModelRateLimitCalls)
	require.Equal(t, 1, repo.clearTempUnschedCalls)
	require.Nil(t, updated.RateLimitResetAt)
	require.Nil(t, updated.TempUnschedulableUntil)
	require.Empty(t, updated.TempUnschedulableReason)
	require.Equal(t, []int64{31}, blocker.clearedIDs)
}

// conditionalClearRepoStub adds the narrow conditional clear capability on top
// of accountRepoStubForClearAccountError. The base stub intentionally does not
// implement it so the fail-closed path can be exercised.
type conditionalClearRepoStub struct {
	*accountRepoStubForClearAccountError
	conditionalClearCalls int
}

func (r *conditionalClearRepoStub) ClearErrorPreservingInactive(ctx context.Context, id int64) (bool, error) {
	r.conditionalClearCalls++
	if r.account.Status != StatusActive && r.account.Status != StatusError {
		return false, nil
	}
	r.account.Status = StatusActive
	r.account.ErrorMessage = ""
	return true, nil
}

func newClearErrorStubAccount(id int64, status string) *Account {
	until := time.Now().Add(10 * time.Minute)
	resetAt := time.Now().Add(5 * time.Minute)
	return &Account{
		ID:                      id,
		Platform:                PlatformOpenAI,
		Type:                    AccountTypeOAuth,
		Status:                  status,
		ErrorMessage:            "previous failure",
		RateLimitResetAt:        &resetAt,
		TempUnschedulableUntil:  &until,
		TempUnschedulableReason: "upstream 529",
	}
}

func requireRuntimeStateCleared(t *testing.T, repo *accountRepoStubForClearAccountError, account *Account) {
	t.Helper()
	require.Equal(t, 1, repo.clearRateLimitCalls)
	require.Equal(t, 1, repo.clearAntigravityCalls)
	require.Equal(t, 1, repo.clearModelRateLimitCalls)
	require.Equal(t, 1, repo.clearTempUnschedCalls)
	require.Nil(t, account.RateLimitResetAt)
	require.Nil(t, account.TempUnschedulableUntil)
	require.Empty(t, account.TempUnschedulableReason)
}

func TestAdminService_ClearAccountErrorWithOptions_PreserveInactiveKeepsStatus(t *testing.T) {
	base := &accountRepoStubForClearAccountError{account: newClearErrorStubAccount(41, "inactive")}
	repo := &conditionalClearRepoStub{accountRepoStubForClearAccountError: base}
	blocker := &runtimeBlockRecorder{}
	svc := &adminServiceImpl{accountRepo: repo, runtimeBlocker: blocker}

	result, err := svc.ClearAccountErrorWithOptions(context.Background(), 41, ClearAccountErrorOptions{PreserveInactive: true})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, result.Account)
	require.True(t, result.SkippedInactive)
	require.Equal(t, "inactive", result.Account.Status)
	require.Equal(t, "previous failure", result.Account.ErrorMessage, "停用账号整个跳过 ClearError，错误信息保持原样")
	require.Equal(t, 0, base.clearErrorCalls, "preserve 时不得调用无条件 ClearError")
	require.Equal(t, 1, repo.conditionalClearCalls)
	requireRuntimeStateCleared(t, base, result.Account)
	require.Equal(t, []int64{41}, blocker.clearedIDs)
}

func TestAdminService_ClearAccountErrorWithOptions_PreserveInactiveTreatsDisabledAsInactive(t *testing.T) {
	base := &accountRepoStubForClearAccountError{account: newClearErrorStubAccount(42, StatusDisabled)}
	repo := &conditionalClearRepoStub{accountRepoStubForClearAccountError: base}
	svc := &adminServiceImpl{accountRepo: repo}

	result, err := svc.ClearAccountErrorWithOptions(context.Background(), 42, ClearAccountErrorOptions{PreserveInactive: true})
	require.NoError(t, err)
	require.True(t, result.SkippedInactive)
	require.Equal(t, StatusDisabled, result.Account.Status)
	require.Equal(t, 0, base.clearErrorCalls)
}

func TestAdminService_ClearAccountErrorWithOptions_PreserveInactiveRecoversErrorAccount(t *testing.T) {
	base := &accountRepoStubForClearAccountError{account: newClearErrorStubAccount(43, StatusError)}
	repo := &conditionalClearRepoStub{accountRepoStubForClearAccountError: base}
	blocker := &runtimeBlockRecorder{}
	svc := &adminServiceImpl{accountRepo: repo, runtimeBlocker: blocker}

	result, err := svc.ClearAccountErrorWithOptions(context.Background(), 43, ClearAccountErrorOptions{PreserveInactive: true})
	require.NoError(t, err)
	require.False(t, result.SkippedInactive)
	require.Equal(t, StatusActive, result.Account.Status)
	require.Empty(t, result.Account.ErrorMessage)
	require.Equal(t, 0, base.clearErrorCalls)
	require.Equal(t, 1, repo.conditionalClearCalls)
	requireRuntimeStateCleared(t, base, result.Account)
	require.Equal(t, []int64{43}, blocker.clearedIDs)
}

func TestAdminService_ClearAccountErrorWithOptions_DefaultReactivatesInactive(t *testing.T) {
	base := &accountRepoStubForClearAccountError{account: newClearErrorStubAccount(44, "inactive")}
	repo := &conditionalClearRepoStub{accountRepoStubForClearAccountError: base}
	svc := &adminServiceImpl{accountRepo: repo}

	result, err := svc.ClearAccountErrorWithOptions(context.Background(), 44, ClearAccountErrorOptions{})
	require.NoError(t, err)
	require.False(t, result.SkippedInactive)
	require.Equal(t, StatusActive, result.Account.Status, "默认行为不变：停用账号也会被重新启用")
	require.Equal(t, 1, base.clearErrorCalls)
	require.Equal(t, 0, repo.conditionalClearCalls)
	requireRuntimeStateCleared(t, base, result.Account)

	// 旧入口 ClearAccountError 同样保持原行为。
	legacyBase := &accountRepoStubForClearAccountError{account: newClearErrorStubAccount(45, "inactive")}
	legacyRepo := &conditionalClearRepoStub{accountRepoStubForClearAccountError: legacyBase}
	legacy := &adminServiceImpl{accountRepo: legacyRepo}
	account, err := legacy.ClearAccountError(context.Background(), 45)
	require.NoError(t, err)
	require.Equal(t, StatusActive, account.Status)
	require.Equal(t, 1, legacyBase.clearErrorCalls)
	require.Equal(t, 0, legacyRepo.conditionalClearCalls)
}

func TestAdminService_ClearAccountErrorWithOptions_PreserveInactiveFailsClosedWithoutConditionalRepo(t *testing.T) {
	repo := &accountRepoStubForClearAccountError{account: newClearErrorStubAccount(46, "inactive")}
	blocker := &runtimeBlockRecorder{}
	svc := &adminServiceImpl{accountRepo: repo, runtimeBlocker: blocker}

	result, err := svc.ClearAccountErrorWithOptions(context.Background(), 46, ClearAccountErrorOptions{PreserveInactive: true})
	require.Error(t, err)
	require.Nil(t, result)
	require.Equal(t, 0, repo.clearErrorCalls)
	require.Equal(t, 0, repo.clearRateLimitCalls)
	require.Equal(t, 0, repo.clearAntigravityCalls)
	require.Equal(t, 0, repo.clearModelRateLimitCalls)
	require.Equal(t, 0, repo.clearTempUnschedCalls)
	require.Empty(t, blocker.clearedIDs)
	require.Equal(t, "inactive", repo.account.Status)
}

// workspaceScopedClearErrorRepoStub 让 GetByIDScoped 按 context 作用域过滤，
// 与真实仓储一致：vendor 读不到其他工作区的账号时返回 not found。
// 基础 stub 的 GetByIDScoped 总是返回账号，只能覆盖「归属通过」分支。
type workspaceScopedClearErrorRepoStub struct {
	*conditionalClearRepoStub
}

func (r *workspaceScopedClearErrorRepoStub) GetByIDScoped(ctx context.Context, id int64) (*Account, error) {
	if !ScopeFromContextOrDeny(ctx).OwnsWorkspaceID(r.account.WorkspaceID) {
		return nil, ErrAccountNotFound
	}
	return r.account, nil
}

func newWorkspaceScopedClearErrorRepo(id, workspaceID int64, status string) *workspaceScopedClearErrorRepoStub {
	account := newClearErrorStubAccount(id, status)
	account.WorkspaceID = workspaceID
	base := &accountRepoStubForClearAccountError{account: account}
	return &workspaceScopedClearErrorRepoStub{
		conditionalClearRepoStub: &conditionalClearRepoStub{accountRepoStubForClearAccountError: base},
	}
}

func clearErrorVendorCtx(workspaceID int64) context.Context {
	return WithScope(context.Background(), VendorScope(workspaceID, WorkspacePermissions{AccountManage: true}))
}

func requireNoClearErrorWrites(t *testing.T, repo *workspaceScopedClearErrorRepoStub, blocker *runtimeBlockRecorder) {
	t.Helper()
	require.Equal(t, 0, repo.conditionalClearCalls)
	require.Equal(t, 0, repo.clearErrorCalls)
	require.Equal(t, 0, repo.clearRateLimitCalls)
	require.Equal(t, 0, repo.clearAntigravityCalls)
	require.Equal(t, 0, repo.clearModelRateLimitCalls)
	require.Equal(t, 0, repo.clearTempUnschedCalls)
	require.Empty(t, blocker.clearedIDs)
	require.Equal(t, StatusError, repo.account.Status)
	require.Equal(t, "previous failure", repo.account.ErrorMessage)
	require.NotNil(t, repo.account.RateLimitResetAt)
	require.NotNil(t, repo.account.TempUnschedulableUntil)
}

// vendor 对其他工作区的账号清除错误：无论是否带 preserve_inactive，
// 都必须在任何写之前以 ErrWorkspaceScopeViolation 拒绝。
func TestAdminService_ClearAccountErrorWithOptions_VendorForeignAccountRejectedWithoutWrites(t *testing.T) {
	for _, opts := range []ClearAccountErrorOptions{{PreserveInactive: true}, {}} {
		repo := newWorkspaceScopedClearErrorRepo(51, 8, StatusError)
		blocker := &runtimeBlockRecorder{}
		svc := &adminServiceImpl{accountRepo: repo, runtimeBlocker: blocker}

		result, err := svc.ClearAccountErrorWithOptions(clearErrorVendorCtx(7), 51, opts)
		require.ErrorIs(t, err, domain.ErrWorkspaceScopeViolation, "opts=%+v", opts)
		require.Nil(t, result)
		requireNoClearErrorWrites(t, repo, blocker)
	}

	// 旧入口 ClearAccountError（刷新凭据等调用方）同样先校验归属。
	repo := newWorkspaceScopedClearErrorRepo(52, 8, StatusError)
	blocker := &runtimeBlockRecorder{}
	svc := &adminServiceImpl{accountRepo: repo, runtimeBlocker: blocker}
	account, err := svc.ClearAccountError(clearErrorVendorCtx(7), 52)
	require.ErrorIs(t, err, domain.ErrWorkspaceScopeViolation)
	require.Nil(t, account)
	requireNoClearErrorWrites(t, repo, blocker)
}

// 对照：同一个按作用域过滤的 stub，vendor 清除自家工作区的账号可以成功，
// 证明上面的拒绝来自归属校验而不是 stub 本身读不到账号。
func TestAdminService_ClearAccountErrorWithOptions_VendorOwnAccountPreserveInactive(t *testing.T) {
	repo := newWorkspaceScopedClearErrorRepo(53, 7, StatusError)
	blocker := &runtimeBlockRecorder{}
	svc := &adminServiceImpl{accountRepo: repo, runtimeBlocker: blocker}

	result, err := svc.ClearAccountErrorWithOptions(clearErrorVendorCtx(7), 53, ClearAccountErrorOptions{PreserveInactive: true})
	require.NoError(t, err)
	require.False(t, result.SkippedInactive)
	require.Equal(t, StatusActive, result.Account.Status)
	require.Equal(t, 1, repo.conditionalClearCalls)
	require.Equal(t, 0, repo.clearErrorCalls)
	requireRuntimeStateCleared(t, repo.accountRepoStubForClearAccountError, result.Account)
	require.Equal(t, []int64{53}, blocker.clearedIDs)
}
