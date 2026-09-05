package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// stubCredRepo 是最小化 AccountRepository stub，仅实现 GetByID，供 credential_shadow_test 使用。
// 嵌入接口满足完整方法集；未实现的方法若被调用会 panic，从而快速暴露误调用。
type stubCredRepo struct {
	AccountRepository
	parent *Account
}

func (s *stubCredRepo) GetByID(_ context.Context, _ int64) (*Account, error) {
	return s.parent, nil
}

// GetByIDScoped 是管理端专用的带归属过滤读取。
//
// 委托给 GetByID：本 stub 服务的测试与工作区归属无关，
// 归属过滤的语义由专门的作用域测试覆盖。
func (s *stubCredRepo) GetByIDScoped(ctx context.Context, id int64) (*Account, error) {
	return s.GetByID(ctx, id)
}

func newStubCredRepo(parent *Account) AccountRepository {
	return &stubCredRepo{parent: parent}
}

func TestResolveCredentialAccount(t *testing.T) {
	ctx := context.Background()
	pid := int64(100)

	// 普通账号（非影子）→ 返回自身
	parent := &Account{ID: 100, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive}
	repo := newStubCredRepo(parent)
	got, err := resolveCredentialAccount(ctx, repo, parent)
	require.NoError(t, err)
	require.Equal(t, int64(100), got.ID)

	// 影子账号 + 合法 OpenAI OAuth 母账号 → 返回母账号
	shadow := &Account{ID: 200, ParentAccountID: &pid, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	got, err = resolveCredentialAccount(ctx, repo, shadow)
	require.NoError(t, err)
	require.Equal(t, int64(100), got.ID)

	// 影子账号 + 母账号非 OpenAI OAuth（API Key 类型）→ 返回 error
	badRepo := newStubCredRepo(&Account{ID: 100, Platform: PlatformOpenAI, Type: AccountTypeAPIKey})
	_, err = resolveCredentialAccount(ctx, badRepo, shadow)
	require.Error(t, err)
}
