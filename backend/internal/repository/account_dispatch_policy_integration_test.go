//go:build integration

package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/Wei-Shaw/sub2api/migrations"
	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

type dispatchPolicyIntegrationFixture struct {
	client   *dbent.Client
	repo     *accountRepository
	group    *service.Group
	user     *service.User
	accounts []*dbent.Account
}

func newDispatchPolicyIntegrationFixture(t *testing.T, accountCount int) *dispatchPolicyIntegrationFixture {
	t.Helper()
	ctx := context.Background()
	client := testEntClient(t)
	f := &dispatchPolicyIntegrationFixture{
		client: client,
		repo:   newAccountRepositoryWithSQL(client, integrationDB, nil),
		group:  mustCreateGroup(t, client, &service.Group{Name: "dispatch-it-" + uuid.NewString(), Platform: service.PlatformAnthropic}),
		user:   mustCreateUser(t, client, &service.User{Email: "dispatch-it-" + uuid.NewString() + "@example.test"}),
	}
	t.Cleanup(func() {
		bg := context.Background()
		ids := make([]int64, 0, len(f.accounts))
		for _, account := range f.accounts {
			ids = append(ids, account.ID)
		}
		_, _ = integrationDB.ExecContext(bg, "DELETE FROM user_group_account_policies WHERE group_id = $1", f.group.ID)
		_, _ = integrationDB.ExecContext(bg, "DELETE FROM account_groups WHERE group_id = $1", f.group.ID)
		_, _ = integrationDB.ExecContext(bg, "DELETE FROM scheduler_outbox WHERE account_id = ANY($1::bigint[]) OR group_id = $2", pq.Array(ids), f.group.ID)
		_, _ = integrationDB.ExecContext(bg, "DELETE FROM accounts WHERE id = ANY($1::bigint[])", pq.Array(ids))
		_, _ = integrationDB.ExecContext(bg, "DELETE FROM users WHERE id = $1", f.user.ID)
		_, _ = integrationDB.ExecContext(bg, "DELETE FROM groups WHERE id = $1", f.group.ID)
	})
	builders := make([]*dbent.AccountCreate, accountCount)
	for i := range builders {
		builders[i] = client.Account.Create().
			SetName(fmt.Sprintf("dispatch-it-%d-%d", f.user.ID, i)).
			SetPlatform(service.PlatformAnthropic).
			SetType(service.AccountTypeAPIKey).
			SetCredentials(map[string]any{"api_key": "test-only"}).
			SetExtra(map[string]any{}).
			SetConcurrency(3).SetPriority(50).
			SetStatus(service.StatusActive).SetSchedulable(true)
	}
	var err error
	f.accounts, err = client.Account.CreateBulk(builders...).Save(ctx)
	require.NoError(t, err)
	return f
}

func (f *dispatchPolicyIntegrationFixture) policy(grantCount int) *service.AccountDispatchPolicy {
	policy := &service.AccountDispatchPolicy{UserID: f.user.ID, GroupID: f.group.ID, Mode: service.AccountDispatchModeReplace}
	for _, account := range f.accounts[:grantCount] {
		policy.Accounts = append(policy.Accounts, service.AccountDispatchGrant{AccountID: account.ID, Origin: service.AccountDispatchOriginExtra})
	}
	return policy
}

func TestAccountDispatchPolicyIntegration_Bulk500AndStrictValidation(t *testing.T) {
	t.Setenv("SUB2API_ACCOUNT_DISPATCH_WRITES_ENABLED", "true")
	f := newDispatchPolicyIntegrationFixture(t, 501)
	ctx := service.WithScope(context.Background(), service.AdminScope())
	admin := service.NewAdminService(
		nil, NewUserRepository(f.client, integrationDB), NewAdminGroupRepository(f.client, integrationDB), f.repo,
		nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, f.client,
		nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
	)
	svc, ok := admin.(service.AccountDispatchAdminService)
	require.True(t, ok)
	policy := f.policy(500)
	stored, err := svc.PutAccountDispatchPolicy(ctx, policy, 0)
	require.NoError(t, err)
	require.Len(t, stored.Accounts, 500)
	version := stored.Version

	_, err = svc.PutAccountDispatchPolicy(ctx, f.policy(501), version)
	require.Error(t, err)
	require.Equal(t, "TOO_MANY_ACCOUNT_DISPATCH_GRANTS", infraerrors.Reason(err))

	// A formerly valid configured account remains visible to the manager after a
	// platform change, while dispatch excludes it and strict saving rejects it.
	_, err = integrationDB.ExecContext(ctx, "UPDATE accounts SET platform = $1 WHERE id = $2", service.PlatformGemini, f.accounts[0].ID)
	require.NoError(t, err)
	listed, err := f.repo.ListAccountDispatchPolicies(ctx, f.group.ID)
	require.NoError(t, err)
	require.Len(t, listed, 1)
	require.Len(t, listed[0].Accounts, 500)
	require.Equal(t, f.accounts[0].ID, listed[0].Accounts[0].AccountID)
	require.NotNil(t, listed[0].Accounts[0].Eligible)
	require.False(t, *listed[0].Accounts[0].Eligible)
	require.Equal(t, "platform_mismatch", listed[0].Accounts[0].IneligibleReason)
	dispatch, err := f.repo.GetAccountDispatchPolicy(ctx, f.user.ID, f.group.ID)
	require.NoError(t, err)
	require.Len(t, dispatch.Accounts, 499)
	_, err = svc.PutAccountDispatchPolicy(ctx, policy, version)
	require.Error(t, err)
	listed, err = f.repo.ListAccountDispatchPolicies(ctx, f.group.ID)
	require.NoError(t, err)
	require.Len(t, listed[0].Accounts, 500, "invalid saves must not silently drop configured grants")
	require.Equal(t, version, listed[0].Version, "failed saves do not advance the committed policy")
	require.NoError(t, svc.DeleteAccountDispatchPolicy(ctx, f.user.ID, f.group.ID, version))
	restored, err := f.repo.GetAccountDispatchPolicy(ctx, f.user.ID, f.group.ID)
	require.NoError(t, err)
	require.Nil(t, restored, "restoring defaults deletes the policy even if some configured grants became invalid")
}

func TestAccountDispatchPolicyIntegration_Migration261CleansOnlyOrphanMembers(t *testing.T) {
	f := newDispatchPolicyIntegrationFixture(t, 3)
	ctx := context.Background()
	users := []*service.User{f.user}
	for i := 0; i < 2; i++ {
		user := mustCreateUser(t, f.client, &service.User{Email: "dispatch-migration-" + uuid.NewString() + "@example.test"})
		users = append(users, user)
		t.Cleanup(func() {
			_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM user_group_account_policies WHERE user_id = $1", user.ID)
			_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM users WHERE id = $1", user.ID)
		})
	}
	mustBindAccountToGroup(t, f.client, f.accounts[2].ID, f.group.ID, 1)
	before := make([]int64, len(users))
	for i, user := range users {
		require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO user_group_account_policies (user_id, group_id, mode) VALUES ($1, $2, 'replace') RETURNING version`, user.ID, f.group.ID).Scan(&before[i]))
	}
	_, err := integrationDB.ExecContext(ctx, `
		INSERT INTO user_group_account_grants (user_id, group_id, account_id, origin) VALUES
		($1, $4, $5, 'member'), ($1, $4, $6, 'extra'), ($1, $4, $7, 'member'),
		($2, $4, $5, 'member'), ($3, $4, $7, 'member')
	`, users[0].ID, users[1].ID, users[2].ID, f.group.ID, f.accounts[0].ID, f.accounts[1].ID, f.accounts[2].ID)
	require.NoError(t, err)
	migration, err := migrations.FS.ReadFile("261_revoke_stale_member_dispatch_grants.sql")
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, string(migration))
	require.NoError(t, err)

	after := make([]int64, len(users))
	for i, user := range users {
		var mode string
		require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT version, mode FROM user_group_account_policies WHERE user_id = $1 AND group_id = $2", user.ID, f.group.ID).Scan(&after[i], &mode))
		require.Equal(t, service.AccountDispatchModeReplace, mode)
	}
	require.Greater(t, after[0], before[0])
	require.Greater(t, after[1], before[1], "orphan-only replace policy survives empty with a newer version")
	require.Equal(t, before[2], after[2], "untouched valid members do not advance their policy version")
	var memberCount, extraCount, emptyCount int
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FILTER (WHERE origin = 'member'), COUNT(*) FILTER (WHERE origin = 'extra') FROM user_group_account_grants WHERE user_id = $1 AND group_id = $2", users[0].ID, f.group.ID).Scan(&memberCount, &extraCount))
	require.Equal(t, 1, memberCount)
	require.Equal(t, 1, extraCount)
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM user_group_account_grants WHERE user_id = $1 AND group_id = $2", users[1].ID, f.group.ID).Scan(&emptyCount))
	require.Zero(t, emptyCount)
	_, err = integrationDB.ExecContext(ctx, string(migration))
	require.NoError(t, err)
	for i, user := range users {
		var replayVersion int64
		require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT version FROM user_group_account_policies WHERE user_id = $1 AND group_id = $2", user.ID, f.group.ID).Scan(&replayVersion))
		require.Equal(t, after[i], replayVersion, "replaying migration 261 is idempotent")
	}
}

func TestAccountDispatchPolicyIntegration_ConcurrentDeletionDoesNotResurrectGrants(t *testing.T) {
	for _, target := range []string{"account", "user", "group"} {
		t.Run(target, func(t *testing.T) {
			f := newDispatchPolicyIntegrationFixture(t, 1)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			policy := f.policy(1)
			before, err := f.repo.PutAccountDispatchPolicy(ctx, policy, 0)
			require.NoError(t, err)
			start := make(chan struct{})
			deleted, replaced := make(chan error, 1), make(chan error, 1)
			go func() {
				<-start
				var deleteErr error
				switch target {
				case "account":
					deleteErr = f.repo.Delete(ctx, f.accounts[0].ID)
				case "user":
					deleteErr = newUserRepositoryWithSQL(f.client, integrationDB).Delete(ctx, f.user.ID)
				case "group":
					deleteErr = newGroupRepositoryWithSQL(f.client, integrationDB).Delete(ctx, f.group.ID)
				}
				deleted <- deleteErr
			}()
			go func() {
				<-start
				_, replaceErr := f.repo.PutAccountDispatchPolicy(ctx, policy, before.Version)
				replaced <- replaceErr
			}()
			close(start)
			select {
			case err := <-deleted:
				require.NoError(t, err)
			case <-ctx.Done():
				t.Fatal("deletion did not finish while policy replacement was running")
			}
			select {
			case err := <-replaced:
				if err != nil {
					switch target {
					case "account":
						require.ErrorContains(t, err, "dispatch accounts were deleted")
					case "user":
						require.ErrorIs(t, err, service.ErrUserNotFound)
					case "group":
						require.ErrorIs(t, err, service.ErrGroupNotFound)
					}
				}
			case <-ctx.Done():
				t.Fatal("policy replacement did not finish while deletion was running")
			}
			var grants, policies int
			require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM user_group_account_grants WHERE user_id = $1 AND group_id = $2", f.user.ID, f.group.ID).Scan(&grants))
			require.Zero(t, grants, "neither commit order may revive deleted grants")
			require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM user_group_account_policies WHERE user_id = $1 AND group_id = $2", f.user.ID, f.group.ID).Scan(&policies))
			if target == "account" {
				require.Equal(t, 1, policies, "account removal must preserve an empty replace policy")
			} else {
				require.Zero(t, policies, "user/group soft deletion removes the policy")
			}
		})
	}
}
