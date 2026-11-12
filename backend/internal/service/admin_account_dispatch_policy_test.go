package service

import (
	"context"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/DATA-DOG/go-sqlmock"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

type dispatchPolicyAdminRepo struct {
	AccountRepository
	accounts []*Account
	policy   *AccountDispatchPolicy
}

func (r *dispatchPolicyAdminRepo) GetByIDs(context.Context, []int64) ([]*Account, error) {
	return r.accounts, nil
}
func (r *dispatchPolicyAdminRepo) GetAccountDispatchPolicy(context.Context, int64, int64) (*AccountDispatchPolicy, error) {
	return r.policy, nil
}
func (r *dispatchPolicyAdminRepo) ListAccountDispatchPolicies(context.Context, int64) ([]AccountDispatchPolicy, error) {
	return nil, nil
}
func (r *dispatchPolicyAdminRepo) PutAccountDispatchPolicy(_ context.Context, policy *AccountDispatchPolicy, expected int64) (*AccountDispatchPolicy, error) {
	current := int64(0)
	if r.policy != nil {
		current = r.policy.Version
	}
	if expected != current {
		return nil, ErrAccountDispatchPolicyVersionConflict(current)
	}
	copy := *policy
	copy.Version = expected + 1
	r.policy = &copy
	return &copy, nil
}
func (r *dispatchPolicyAdminRepo) DeleteAccountDispatchPolicy(_ context.Context, _, _, expected int64) error {
	if r.policy == nil || expected != r.policy.Version {
		return ErrAccountDispatchPolicyVersionConflict(0)
	}
	r.policy = nil
	return nil
}

type dispatchPolicyGroupRepo struct {
	GroupRepository
	group *Group
}

func (r dispatchPolicyGroupRepo) GetByID(context.Context, int64) (*Group, error) { return r.group, nil }

type dispatchPolicyUserRepo struct {
	UserRepository
	user *User
}

func (r dispatchPolicyUserRepo) GetByID(context.Context, int64) (*User, error) { return r.user, nil }

func TestAdminAccountDispatchPolicy_EmptyReplaceAndRestoreDefault(t *testing.T) {
	t.Setenv(accountDispatchWritesEnv, "1")
	ctx := WithScope(context.Background(), AdminScope())
	repo := &dispatchPolicyAdminRepo{}
	s := &adminServiceImpl{accountRepo: repo, groupRepo: dispatchPolicyGroupRepo{group: &Group{ID: 9, Platform: PlatformOpenAI}}, userRepo: dispatchPolicyUserRepo{user: &User{ID: 7}}}
	policy, err := s.PutAccountDispatchPolicy(ctx, &AccountDispatchPolicy{UserID: 7, GroupID: 9, Mode: AccountDispatchModeReplace, Accounts: []AccountDispatchGrant{}}, 0)
	require.NoError(t, err)
	require.NotNil(t, policy)
	require.Equal(t, AccountDispatchModeReplace, policy.Mode)
	require.Empty(t, policy.Accounts)
	require.EqualValues(t, 1, policy.Version)
	_, err = s.PutAccountDispatchPolicy(ctx, policy, 0)
	require.ErrorContains(t, err, "version changed")
	require.NoError(t, s.DeleteAccountDispatchPolicy(ctx, 7, 9, 1))
	require.Nil(t, repo.policy)
}

func TestAdminAccountDispatchPolicy_ValidatesOriginAndPlatform(t *testing.T) {
	t.Setenv(accountDispatchWritesEnv, "1")
	ctx := WithScope(context.Background(), AdminScope())
	repo := &dispatchPolicyAdminRepo{accounts: []*Account{{ID: 1, Platform: PlatformOpenAI, WorkspaceID: 1, GroupIDs: []int64{9}}}}
	s := &adminServiceImpl{accountRepo: repo, groupRepo: dispatchPolicyGroupRepo{group: &Group{ID: 9, Platform: PlatformOpenAI}}, userRepo: dispatchPolicyUserRepo{user: &User{ID: 7}}}
	policy := &AccountDispatchPolicy{UserID: 7, GroupID: 9, Mode: AccountDispatchModeAppend, Accounts: []AccountDispatchGrant{{AccountID: 1, Origin: AccountDispatchOriginExtra}}}
	_, err := s.PutAccountDispatchPolicy(ctx, policy, 0)
	require.ErrorContains(t, err, "membership changed")
	repo.accounts[0].GroupIDs = nil
	repo.accounts[0].Platform = PlatformAnthropic
	_, err = s.PutAccountDispatchPolicy(ctx, policy, 0)
	require.ErrorContains(t, err, "single-platform")
	_, err = s.PutAccountDispatchPolicy(context.Background(), policy, 0)
	require.Error(t, err)
}

func TestAdminAccountDispatchPolicy_BatchesWorkspaceGrantChecks(t *testing.T) {
	t.Setenv(accountDispatchWritesEnv, "1")
	ctx := WithScope(context.Background(), AdminScope())
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	t.Cleanup(func() { _ = client.Close() })

	accounts := make([]*Account, 500)
	grants := make([]AccountDispatchGrant, len(accounts))
	for i := range accounts {
		id := int64(i + 1000)
		workspaceID := int64(2 + i%2)
		accounts[i] = &Account{ID: id, Platform: PlatformOpenAI, WorkspaceID: workspaceID}
		grants[i] = AccountDispatchGrant{AccountID: id, Origin: AccountDispatchOriginExtra}
	}
	repo := &dispatchPolicyAdminRepo{accounts: accounts}
	s := &adminServiceImpl{
		accountRepo: repo,
		groupRepo:   dispatchPolicyGroupRepo{group: &Group{ID: 9, Platform: PlatformOpenAI}},
		userRepo:    dispatchPolicyUserRepo{user: &User{ID: 7}},
		entClient:   client,
	}
	workspaceIDs, err := pq.Array([]int64{2, 3}).Value()
	require.NoError(t, err)
	mock.ExpectQuery(`(?s)SELECT wg.workspace_id FROM workspace_group_grants wg.*ANY\(\$1::bigint\[\]\).*ORDER BY wg.workspace_id`).
		WithArgs(workspaceIDs, int64(9)).
		WillReturnRows(sqlmock.NewRows([]string{"workspace_id"}).AddRow(2).AddRow(3))

	policy, err := s.PutAccountDispatchPolicy(ctx, &AccountDispatchPolicy{
		UserID: 7, GroupID: 9, Mode: AccountDispatchModeAppend, Accounts: grants,
	}, 0)
	require.NoError(t, err)
	require.Len(t, policy.Accounts, 500)
	require.NoError(t, mock.ExpectationsWereMet(), "workspace access must be checked in one query for unique workspace IDs")
}

func TestBuildAccountForCreate_AuthorizedOnlyRequiresGateAndPreservesState(t *testing.T) {
	ctx := WithScope(context.Background(), AdminScope())
	input := &CreateAccountInput{Name: "test", Platform: PlatformOpenAI, Type: AccountTypeAPIKey, DispatchScope: DispatchScopeAuthorizedOnly}
	t.Setenv(accountDispatchWritesEnv, "")
	_, err := buildAccountForCreate(ctx, input, nil)
	require.Error(t, err)
	t.Setenv(accountDispatchWritesEnv, "1")
	account, err := buildAccountForCreate(ctx, input, nil)
	require.NoError(t, err)
	require.Equal(t, DispatchScopeAuthorizedOnly, account.DispatchScope)
	require.True(t, account.Schedulable)
	require.Equal(t, StatusActive, account.Status)
}
