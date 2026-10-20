//go:build unit

package service

import (
	"errors"
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

// TestMixedChannelErrorConflictErrorCarriesStringMetadata 锁定混合渠道 409 的稳定信封：
// message 与旧版逐字相同，metadata 全部是字符串（group_id 为十进制）。
func TestMixedChannelErrorConflictErrorCarriesStringMetadata(t *testing.T) {
	mixed := &MixedChannelError{GroupID: 27, GroupName: "claude-max", CurrentPlatform: "Antigravity", OtherPlatform: "Anthropic"}

	appErr := mixed.ConflictError()

	require.Equal(t, int32(http.StatusConflict), appErr.Code)
	require.Equal(t, MixedChannelWarningReason, appErr.Reason)
	require.Equal(t, "MIXED_CHANNEL_WARNING", appErr.Reason)
	require.Equal(t, mixed.Error(), appErr.Message)
	require.Equal(t, map[string]string{
		"group_id":         "27",
		"group_name":       "claude-max",
		"current_platform": "Antigravity",
		"other_platform":   "Anthropic",
	}, appErr.Metadata)

	// 每次调用返回独立副本：调用方改 metadata 不能污染下一次的结果。
	appErr.Metadata["group_name"] = "tampered"
	require.Equal(t, "claude-max", mixed.ConflictError().Metadata["group_name"])
}

func newMixedChannelCreateFixture() (*adminServiceImpl, *accountRepoStubForBulkUpdate) {
	accountRepo := &accountRepoStubForBulkUpdate{
		createID: 9,
		listByGroupData: map[int64][]Account{
			4: {{ID: 99, Platform: PlatformAnthropic}},
		},
	}
	groupRepo := &groupRepoStubForAdmin{
		getByIDByID: map[int64]*Group{
			3: {ID: 3, Name: "own-pool", Platform: PlatformAntigravity},
			4: {ID: 4, Name: "other-tenant-pool", Platform: PlatformAnthropic},
		},
	}
	svc := &adminServiceImpl{
		accountRepo:         accountRepo,
		groupRepo:           groupRepo,
		workspaceScopeCache: &stubGroupScopeCache{granted: []int64{3}},
	}
	return svc, accountRepo
}

func mixedChannelCreateInput() *CreateAccountInput {
	return &CreateAccountInput{
		Name:        "ag-oauth",
		Platform:    PlatformAntigravity,
		Type:        AccountTypeOAuth,
		Credentials: map[string]any{"refresh_token": "rt"},
		Concurrency: 1,
		GroupIDs:    []int64{4},
	}
}

// TestCreateAccountChecksGroupGrantBeforeMixedChannelRisk 锁定越权探测修复：
// vendor 绑定未授权分组时先被授权校验拦下（对外 404），
// 拿不到混合渠道 409 里别家分组的名称与平台构成。
func TestCreateAccountChecksGroupGrantBeforeMixedChannelRisk(t *testing.T) {
	svc, accountRepo := newMixedChannelCreateFixture()

	account, err := svc.CreateAccount(vendorGroupCtx(7), mixedChannelCreateInput())

	require.Nil(t, account)
	require.Error(t, err)
	require.True(t, errors.Is(err, domain.ErrWorkspaceScopeViolation), "want scope violation, got %v", err)
	var mixed *MixedChannelError
	require.False(t, errors.As(err, &mixed), "未授权分组不能返回混合渠道错误")
	require.NotContains(t, err.Error(), "other-tenant-pool")
	require.Nil(t, accountRepo.createAccount)
}

// TestCreateAccountAdminStillGetsMixedChannelError 锁定站长行为不变：
// 授权校验前移后，混合渠道风险照常返回 MixedChannelError，账号不落库。
func TestCreateAccountAdminStillGetsMixedChannelError(t *testing.T) {
	svc, accountRepo := newMixedChannelCreateFixture()

	account, err := svc.CreateAccount(adminGroupCtx(), mixedChannelCreateInput())

	require.Nil(t, account)
	var mixed *MixedChannelError
	require.True(t, errors.As(err, &mixed), "want MixedChannelError, got %v", err)
	require.Equal(t, int64(4), mixed.GroupID)
	require.Equal(t, "other-tenant-pool", mixed.GroupName)
	require.Equal(t, "Antigravity", mixed.CurrentPlatform)
	require.Equal(t, "Anthropic", mixed.OtherPlatform)
	require.Equal(t, MixedChannelWarningReason, infraerrors.Reason(mixed.ConflictError()))
	require.Nil(t, accountRepo.createAccount)
}

// TestCreateAccountVendorGrantedGroupStillGetsMixedChannelError 锁定 vendor 在
// 自己已授权的分组里遇到混合渠道时仍收到 409 所需的 MixedChannelError。
func TestCreateAccountVendorGrantedGroupStillGetsMixedChannelError(t *testing.T) {
	svc, accountRepo := newMixedChannelCreateFixture()
	accountRepo.listByGroupData[3] = []Account{{ID: 98, Platform: PlatformAnthropic}}
	input := mixedChannelCreateInput()
	input.GroupIDs = []int64{3}

	account, err := svc.CreateAccount(vendorGroupCtx(7), input)

	require.Nil(t, account)
	var mixed *MixedChannelError
	require.True(t, errors.As(err, &mixed), "want MixedChannelError, got %v", err)
	require.Equal(t, "own-pool", mixed.GroupName)
	require.Nil(t, accountRepo.createAccount)
}
