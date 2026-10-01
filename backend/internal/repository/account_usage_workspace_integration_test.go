//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAccountUsageGroupsStayInWorkspace(t *testing.T) {
	for _, provider := range []string{"opencode", "ollama"} {
		t.Run(provider, func(t *testing.T) {
			ctx := context.Background()
			tx := testEntTx(t)
			repo := newAccountRepositoryWithSQL(tx.Client(), tx, nil)
			workspace, err := tx.Client().Workspace.Create().SetName("usage-isolation-" + provider).Save(ctx)
			require.NoError(t, err)
			baseURL := "https://opencode.ai/zen/go/v1"
			if provider == "ollama" {
				baseURL = "https://ollama.com"
			}
			create := func(name string, workspaceID int64) *service.Account {
				return mustCreateAccount(t, tx.Client(), &service.Account{
					Name: name, WorkspaceID: workspaceID, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
					Credentials: map[string]any{"base_url": baseURL, "api_key": "workspace-shared-" + provider},
				})
			}
			own, sibling, other := create("own", 1), create("sibling", 1), create("other", workspace.ID)
			var members []service.Account
			if provider == "opencode" {
				members, err = repo.ListOpenCodeGoUsageGroupAccounts(ctx, []*service.Account{own})
			} else {
				members, err = repo.ListOllamaCloudUsageGroupAccounts(ctx, []*service.Account{own})
			}
			require.NoError(t, err)
			require.Len(t, members, 2)
			for _, member := range members {
				require.Equal(t, int64(1), member.WorkspaceID)
			}
			if provider == "opencode" {
				err = repo.SetOpenCodeGoUsageAutoRefresh(ctx, own, true)
			} else {
				err = repo.SaveOllamaCloudUsageSession(ctx, own, "cipher:own-session", true)
			}
			require.NoError(t, err)
			loaded, err := repo.GetByID(ctx, other.ID)
			require.NoError(t, err)
			for _, key := range []string{service.OpenCodeGoUsageAutoRefreshExtraKey, service.OllamaCloudUsageAutoRefreshExtraKey, service.OllamaCloudUsageSessionExtraKey} {
				require.NotContains(t, loaded.Extra, key, "same key in another workspace must not be mutated")
			}
			loaded, err = repo.GetByID(ctx, sibling.ID)
			require.NoError(t, err)
			if provider == "opencode" {
				require.Equal(t, true, loaded.Extra[service.OpenCodeGoUsageAutoRefreshExtraKey])
			} else {
				require.Equal(t, "cipher:own-session", loaded.Extra[service.OllamaCloudUsageSessionExtraKey])
			}
			if provider == "opencode" {
				err = repo.SetOpenCodeGoUsageAutoRefresh(ctx, other, true)
			} else {
				err = repo.SaveOllamaCloudUsageSession(ctx, other, "cipher:other-session", true)
			}
			require.NoError(t, err)
			if provider == "opencode" {
				members, err = repo.ListDueOpenCodeGoUsageAccounts(ctx, time.Now(), time.Minute, time.Hour, 100)
			} else {
				members, err = repo.ListDueOllamaCloudUsageAccounts(ctx, time.Now(), time.Minute, time.Hour, 100)
			}
			require.NoError(t, err)
			found := map[int64]bool{}
			for _, member := range members {
				if member.ID == own.ID || member.ID == sibling.ID || member.ID == other.ID {
					found[member.WorkspaceID] = true
				}
			}
			require.Equal(t, map[int64]bool{1: true, workspace.ID: true}, found, "runner must schedule one refresh per workspace/key")
		})
	}
}
