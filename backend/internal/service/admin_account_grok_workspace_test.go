package service

import (
	"context"
	"testing"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

func TestValidateAccountCreateScopeRejectsVendorGrokCreation(t *testing.T) {
	ctx := WithScope(context.Background(), VendorScope(7, WorkspacePermissions{AccountManage: true}))

	err := validateAccountCreateScope(ctx, PlatformGrok)

	require.Error(t, err)
	require.True(t, infraerrors.IsForbidden(err))
	require.Equal(t, "WORKSPACE_GROK_ACCOUNT_CREATE_FORBIDDEN", infraerrors.Reason(err))
}

func TestValidateAccountCreateScopeKeepsOwnerAndOtherPlatforms(t *testing.T) {
	vendorCtx := WithScope(context.Background(), VendorScope(7, WorkspacePermissions{AccountManage: true}))
	ownerCtx := WithScope(context.Background(), AdminScope())

	require.NoError(t, validateAccountCreateScope(vendorCtx, PlatformOpenAI))
	require.NoError(t, validateAccountCreateScope(ownerCtx, PlatformGrok))
}
