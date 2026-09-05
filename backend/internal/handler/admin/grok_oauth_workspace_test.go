package admin

import (
	"context"
	"errors"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

type grokReauthAdminStub struct {
	*stubAdminService
	account       *service.Account
	accountErr    error
	proxyErr      error
	proxyLookupID int64
}

func (s *grokReauthAdminStub) GetAccount(context.Context, int64) (*service.Account, error) {
	return s.account, s.accountErr
}

func (s *grokReauthAdminStub) GetProxy(_ context.Context, id int64) (*service.Proxy, error) {
	s.proxyLookupID = id
	if s.proxyErr != nil {
		return nil, s.proxyErr
	}
	return &service.Proxy{ID: id}, nil
}

func grokVendorContext(perms service.WorkspacePermissions) context.Context {
	return service.WithScope(context.Background(), service.VendorScope(7, perms))
}

func TestGrokVendorReauthRequiresAccountManageAndAccountID(t *testing.T) {
	handler := &GrokOAuthHandler{adminService: &grokReauthAdminStub{stubAdminService: &stubAdminService{}}}
	accountID := int64(42)

	_, err := handler.constrainGrokReauthProxy(grokVendorContext(service.WorkspacePermissions{}), &accountID, nil)
	require.ErrorIs(t, err, domain.ErrWorkspacePermissionDenied)

	_, err = handler.constrainGrokReauthProxy(grokVendorContext(service.WorkspacePermissions{AccountManage: true}), nil, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "GROK_OAUTH_ACCOUNT_REQUIRED")
}

func TestGrokVendorReauthUsesScopedAccountProxy(t *testing.T) {
	accountID, accountProxyID, requestedProxyID := int64(42), int64(11), int64(99)
	adminService := &grokReauthAdminStub{
		stubAdminService: &stubAdminService{},
		account: &service.Account{
			ID:          accountID,
			Platform:    service.PlatformGrok,
			ProxyID:     &accountProxyID,
			WorkspaceID: 7,
		},
	}
	handler := &GrokOAuthHandler{adminService: adminService}

	resolved, err := handler.constrainGrokReauthProxy(
		grokVendorContext(service.WorkspacePermissions{AccountManage: true}),
		&accountID,
		&requestedProxyID,
	)

	require.NoError(t, err)
	require.NotNil(t, resolved)
	require.Equal(t, accountProxyID, *resolved)
	require.Equal(t, accountProxyID, adminService.proxyLookupID)
}

func TestGrokVendorReauthPropagatesScopedAccountLookupFailure(t *testing.T) {
	accountID := int64(42)
	handler := &GrokOAuthHandler{adminService: &grokReauthAdminStub{
		stubAdminService: &stubAdminService{},
		accountErr:       service.ErrAccountNotFound,
	}}

	_, err := handler.constrainGrokReauthProxy(
		grokVendorContext(service.WorkspacePermissions{AccountManage: true}),
		&accountID,
		nil,
	)

	require.True(t, errors.Is(err, service.ErrAccountNotFound))
}

func TestGrokStationOwnerHelpersRemainBackwardCompatible(t *testing.T) {
	requestedProxyID := int64(99)
	handler := &GrokOAuthHandler{}

	resolved, err := handler.constrainGrokReauthProxy(
		service.WithScope(context.Background(), service.AdminScope()),
		nil,
		&requestedProxyID,
	)

	require.NoError(t, err)
	require.Same(t, &requestedProxyID, resolved)
}
