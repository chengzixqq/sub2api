package admin

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestImportCodexSessions_DispatchScopeNeverSilentlyChangesExisting(t *testing.T) {
	for _, scope := range []string{service.DispatchScopeShared, service.DispatchScopeAuthorizedOnly} {
		t.Run(scope, func(t *testing.T) {
			token := buildCodexAccessToken(t, "workspace-1", "user-1", time.Now().Add(time.Hour))
			svc := newCodexImportMemoryAdminService([]service.Account{{
				ID: 10, Name: "existing", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, DispatchScope: scope,
				Credentials: map[string]any{"chatgpt_account_id": "workspace-1", "chatgpt_user_id": "user-1", "access_token": token},
			}})
			handler := &AccountHandler{adminService: svc}
			result, err := handler.importCodexSessions(context.Background(), CodexSessionImportRequest{DispatchScope: service.DispatchScopeAuthorizedOnly}, []codexImportEntry{{Index: 1, Value: map[string]any{"access_token": token}}})
			require.NoError(t, err)
			require.Zero(t, result.Created)
			if scope == service.DispatchScopeShared {
				require.Equal(t, 1, result.Failed)
				require.Zero(t, result.Updated)
				require.Empty(t, svc.updatedAccounts)
				require.Contains(t, result.Errors[0].Message, "调度范围")
			} else {
				require.Zero(t, result.Failed)
				require.Equal(t, 1, result.Updated)
			}
		})
	}
}

func TestImportCodexSessions_PrivateCreationPassesScopeAtomically(t *testing.T) {
	svc := newCodexImportMemoryAdminService(nil)
	handler := &AccountHandler{adminService: svc}
	result, err := handler.importCodexSessions(context.Background(), CodexSessionImportRequest{DispatchScope: service.DispatchScopeAuthorizedOnly, SkipDefaultGroupBind: boolPtr(true)}, []codexImportEntry{{Index: 1, Value: buildCodexAccessOnlyImportValue(t, "workspace-1", "user-1")}})
	require.NoError(t, err)
	require.Equal(t, 1, result.Created)
	require.Len(t, svc.createdAccounts, 1)
	require.Equal(t, service.DispatchScopeAuthorizedOnly, svc.createdAccounts[0].DispatchScope)
	require.True(t, svc.createdAccounts[0].SkipDefaultGroupBind)
}
